package proxy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cc-forwarder/internal/accountauth"
	"cc-forwarder/internal/endpoint"
	"cc-forwarder/internal/middleware"
	"cc-forwarder/internal/store"
	"cc-forwarder/internal/tracking"
)

type sessionFailingReader struct{}

func (sessionFailingReader) Read([]byte) (int, error) {
	return 0, errors.New("synthetic body read failure")
}

func TestHandlerSessionStartsBeforeBodyRead(t *testing.T) {
	logs := &syncBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/messages") {
			_, _ = io.WriteString(w, `{"type":"message","model":"synthetic-model","content":[],"usage":{"input_tokens":1,"output_tokens":2,"speed":"standard"}}`)
		} else {
			_, _ = io.WriteString(w, `{"object":"response","model":"synthetic-model","status":"completed","output":[],"service_tier":"default","usage":{"input_tokens":1,"output_tokens":2}}`)
		}
	}))
	defer upstream.Close()
	for _, tc := range []struct {
		name, path, header, entryMode string
		readFailure                   bool
	}{
		{"codex", "/v1/responses", "session-id", "", false},
		{"claude", "/v1/messages", "X-Claude-Code-Session-Id", "", false},
		{"claude-entry", "/v1/messages", "X-Claude-Code-Session-Id", endpoint.ClaudeCodeEntryModeEndpoint, false},
		{"claude-subscription", "/v1/messages", "X-Claude-Code-Session-Id", endpoint.ClaudeCodeEntryModeSubscription, false},
		{"codex-body-failure", "/v1/responses", "session-id", "", true},
		{"claude-body-failure", "/v1/messages", "X-Claude-Code-Session-Id", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tracker := newSubscriptionUnitTracker(t)
			handler := newSessionTestHandler(t, upstream.URL, tracker)
			req := sessionTestRequest(tc.path, tc.name, tc.header, tc.readFailure)
			if tc.entryMode != "" {
				req = req.WithContext(endpoint.WithClaudeCodeEntry(req.Context(), endpoint.ClaudeCodeEntryRequest{Mode: tc.entryMode}))
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if !tc.readFailure && rec.Code != http.StatusOK {
				t.Fatalf("代理请求失败: status=%d body=%s", rec.Code, rec.Body.String())
			}
			row := waitSubscriptionDetail(t, tracker, tc.name)
			if row.SessionID != "SYNTHETIC-PRIVATE-SESSION" || (tc.readFailure && row.RequestReasoning != "") {
				t.Fatalf("开始记录未保留会话或读体失败产生了档位: %+v", row)
			}
		})
	}
	if strings.Contains(logs.String(), "SYNTHETIC-PRIVATE-SESSION") {
		t.Fatal("Go 后端日志泄漏会话 ID")
	}
}

func newSessionTestHandler(t *testing.T, upstream string, tracker *tracking.UsageTracker) *Handler {
	t.Helper()
	pool := &mockAccountPoolService{accounts: []*store.UpstreamAccountRecord{{
		ID: 71, AccountName: "synthetic", ProviderType: accountauth.ProviderAPIKey,
		CredentialRaw: "synthetic-key", BaseURL: upstream, Enabled: true,
	}}}
	handler := newAccountPipelineTestHandler(t, upstream, pool)
	handler.SetMonitoringMiddleware(middleware.NewMonitoringMiddleware(handler.endpointManager))
	handler.SetUsageTracker(tracker)
	handler.claudeSubscriptionBaseURL = upstream
	return handler
}

func sessionTestRequest(path, requestID, header string, failRead bool) *http.Request {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"synthetic-model","input":"hello","messages":[],"service_tier":"priority","speed":"fast"}`))
	req = req.WithContext(context.WithValue(req.Context(), "conn_id", requestID)) //nolint:staticcheck // 与入口已有 key 一致
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer synthetic-client")
	req.Header.Set(header, "SYNTHETIC-PRIVATE-SESSION")
	if failRead {
		req.Body = io.NopCloser(sessionFailingReader{})
	}
	return req
}

func TestSessionCountTokensInterceptionDoesNotTrack(t *testing.T) {
	tracker := newSubscriptionUnitTracker(t)
	handler := newSessionTestHandler(t, "http://unused.invalid", tracker)
	handler.config.TokenCounting.Enabled = true
	req := sessionTestRequest("/v1/messages/count_tokens", "session-count-tokens", "X-Claude-Code-Session-Id", true)
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if err := tracker.ForceFlush(); err != nil {
		t.Fatal(err)
	}
	count, err := tracker.CountRequestDetails(context.Background(), &tracking.QueryOptions{})
	if err != nil || count != 0 {
		t.Fatalf("本地 count_tokens 不应产生追踪记录: count=%d err=%v", count, err)
	}
}
