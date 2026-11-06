package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"sinuo/internal/middleware"
	"sinuo/internal/proxy/subscriptionws"
	svc "sinuo/internal/service"
	"sinuo/internal/tracking"
)

func requestIDTestCore(t *testing.T) *tracking.CoreDatabase {
	t.Helper()
	core, err := tracking.OpenCoreDatabase(tracking.DatabaseConfig{Type: "sqlite", DatabasePath: filepath.Join(t.TempDir(), "usage.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.Close() })
	if err := core.InitSchema(); err != nil {
		t.Fatal(err)
	}
	return core
}

func TestRequestIDEntrypoints_ReservedBeforeUse(t *testing.T) {
	core := requestIDTestCore(t)
	checked := 0
	check := func(id string) {
		t.Helper()
		var count int
		if err := core.DB().QueryRow(`SELECT COUNT(*) FROM request_id_reservations WHERE request_id = ?`, id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if len(id) != 12 || count != 1 {
			t.Fatalf("入口使用了未占号 ID: %q count=%d", id, count)
		}
		checked++
	}
	monitoring := middleware.NewMonitoringMiddleware(nil)
	logging := middleware.NewLoggingMiddleware(slog.New(slog.NewTextHandler(io.Discard, nil)))
	logging.SetMonitoringMiddleware(monitoring)
	logging.SetRequestIDGenerator(core.NewRequestID)
	logging.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := r.Context().Value("conn_id").(string)
		check(id)
		if monitoring.GetMetrics().ActiveConnections[id] == nil {
			t.Fatal("监控和上下文没有使用同一 ID")
		}
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/messages", nil))

	env := newSubscriptionTestEnv(t, nil, svc.CodexSubscriptionProxyModeSubscription)
	env.handler.requestIDs = core.NewRequestID
	// serveRoute 使用真实分配器，将同一个分配器传到 WS 上下文。
	env.handler.ws = requestIDTestWS{inspect: func(r *http.Request, id string) {
		check(id)
		if r.Context().Value("conn_id") != id {
			t.Fatal("订阅上下文 ID 不一致")
		}
		state := newSubscriptionSubmissionState(r.Context(), id, nil)
		submission, ok, err := state.beginCreate([]byte(`{"type":"response.create","input":[]}`))
		if err != nil || !ok || submission == nil {
			t.Fatalf("WS 提交创建失败: %v", err)
		}
		check(submission.RequestID)
		if submission.Context.Value("conn_id") != submission.RequestID {
			t.Fatal("WS 提交上下文 ID 不一致")
		}
		if _, _, err := state.handleUpstreamForForward([]byte(`{"type":"response.created","response":{"id":"resp-first"}}`)); err != nil {
			t.Fatal(err)
		}
		if _, _, err := state.handleUpstreamForForward([]byte(`{"type":"response.created","response":{"id":"resp-successor"}}`)); err != nil {
			t.Fatal(err)
		}
		check(state.byResponseID["resp-successor"].RequestID)
		state.close("connection_closed")
	}}
	request := httptest.NewRequest("GET", "/v1/responses", nil)
	env.handler.serveRoute(httptest.NewRecorder(), request, &subscriptionRoute{WebSocket: true, InternalPath: "/v1/responses"}, env.handler.snapshot())
	if checked != 4 {
		t.Fatalf("入口验证数量错误: %d", checked)
	}
	var count int
	if err := core.DB().QueryRow(`SELECT COUNT(*) FROM request_id_reservations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != checked {
		t.Fatalf("逻辑请求重复占号: %d vs %d", count, checked)
	}
}

type requestIDTestWS struct{ inspect func(*http.Request, string) }

func (s requestIDTestWS) serve(w http.ResponseWriter, r *http.Request, _ *subscriptionRoute, _ *SubscriptionProxySnapshot, id string) {
	s.inspect(r, id)
	w.WriteHeader(http.StatusNoContent)
}

func TestRequestIDEntrypoints_FailureStopsForwarding(t *testing.T) {
	core := requestIDTestCore(t)
	// 真实数据库写失败，确保入口不会退回随机 ID。
	if _, err := core.DB().Exec(`CREATE TRIGGER reject_id BEFORE INSERT ON request_id_reservations BEGIN SELECT RAISE(ABORT, 'reservation storage failed'); END;`); err != nil {
		t.Fatal(err)
	}
	monitoring := middleware.NewMonitoringMiddleware(nil)
	logging := middleware.NewLoggingMiddleware(slog.New(slog.NewTextHandler(io.Discard, nil)))
	logging.SetMonitoringMiddleware(monitoring)
	logging.SetRequestIDGenerator(core.NewRequestID)
	forwarded := false
	response := httptest.NewRecorder()
	logging.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { forwarded = true })).ServeHTTP(response, httptest.NewRequest("POST", "/v1/messages", nil))
	if forwarded || response.Code != 503 || monitoring.GetMetrics().TotalRequests != 0 || monitoring.GetMetrics().RequestActivity().Count() != 0 {
		t.Fatalf("普通入口占号失败仍转发或残留请求: status=%d forwarded=%v", response.Code, forwarded)
	}
	env := newSubscriptionTestEnv(t, nil, svc.CodexSubscriptionProxyModeSubscription)
	env.handler.requestIDs = core.NewRequestID
	resp := env.request("GET", "models", nil)
	body := readBody(t, resp)
	if resp.StatusCode != 503 || !bytes.Contains([]byte(body), []byte("request_id_allocation_failed")) || env.upstreamCount() != 0 || env.handler.activity.Count() != 0 {
		t.Fatalf("订阅入口占号失败仍转发或残留请求: status=%d body=%s", resp.StatusCode, body)
	}
}

func TestRequestIDWebSocket_FailureDoesNotCreateSubmission(t *testing.T) {
	allocationErr := errors.New("reservation unavailable")
	state := newSubscriptionSubmissionState(context.Background(), "req-00000001", &recordingSubmissionObserver{})
	state.requestIDs = func(context.Context) (string, error) { return "", allocationErr }
	interceptor := &subscriptionWSInterceptor{submissions: state}
	for _, payload := range []string{
		`{"type":"response.create","stream_id":"lane-a","input":[]}`,
		`{"type":"response.steer","response_id":"resp-1","input":[]}`,
	} {
		result, err := interceptor.Intercept(context.Background(), subscriptionws.DirectionDownstreamToUpstream, []byte(payload))
		if err != nil || result.Forward != nil || !bytes.Contains(result.Reply, []byte("request_id_allocation_failed")) {
			t.Fatalf("占号失败应返回 WS 错误并阻止转发: %+v, %v", result, err)
		}
		if bytes.Contains([]byte(payload), []byte("stream_id")) && !bytes.Contains(result.Reply, []byte(`"stream_id":"lane-a"`)) {
			t.Fatal("占号失败未保留 stream_id")
		}
	}
	if state.sequence != 0 || len(state.active) != 0 || len(state.pending) != 0 {
		t.Fatal("占号失败仍创建了逻辑请求")
	}
	if _, _, err := state.handleUpstreamForForward([]byte(`{"type":"response.created","response":{"id":"resp-successor"}}`)); !errors.Is(err, allocationErr) {
		t.Fatalf("服务端后续响应占号失败未上传错误: %v", err)
	}
	if len(state.byResponseID) != 0 {
		t.Fatal("占号失败仍绑定了服务端响应")
	}
}
