package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sinuo/internal/accountauth"
	"sinuo/internal/store"
	"sinuo/internal/tracking"
)

func streamFixtureServer(t *testing.T, name string) *httptest.Server {
	t.Helper()
	body := phaseFixture(t, name)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Anthropic-Request-Id", "synthetic-final-upstream")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func onlyPhaseRequest(t *testing.T, tracker *tracking.UsageTracker) *tracking.RequestLifecycleData {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		rows, _, err := tracker.QueryRequestDetailsWithHotPool(context.Background(), &tracking.QueryOptions{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 1 && (rows[0].Status == "completed" || rows[0].Status == "failed" || rows[0].Status == "cancelled") {
			return timingLifecycleData(t, tracker, rows[0].RequestID)
		}
		if time.Now().After(deadline) {
			t.Fatalf("终态未出现: %+v", rows)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func requireObservedPipeline(t *testing.T, data *tracking.RequestLifecycleData, hidden bool) {
	t.Helper()
	if data.FirstContentMs == nil || data.FirstTextMs == nil || data.StreamFlowMs == nil || data.ReasoningHidden == nil || *data.ReasoningHidden != hidden || data.StopReason == nil {
		t.Fatalf("阶段缺失: %+v", data.StreamPhases)
	}
	if data.UpstreamRequestID == nil || *data.UpstreamRequestID != "synthetic-final-upstream" {
		t.Fatalf("上游ID=%v", data.UpstreamRequestID)
	}
	if data.Detail.FirstContentMs == nil || data.Detail.UpstreamWriteMs == nil {
		t.Fatal("列表DTO丢失首字锚点")
	}
}

func TestStreamPhases_EndpointAndClaudeSubscriptionEndToEnd(t *testing.T) {
	upstream := streamFixtureServer(t, "claude_thinking")
	t.Run("endpoint", func(t *testing.T) {
		h, _ := newEndpointPipelineTestHandler(t, endpointPipelineConfig("synthetic-endpoint", upstream.URL, 1))
		tracker := attachRetryTracker(t, h)
		if rec := performEndpointPipelineRequest(t, h, true); rec.Code != 200 {
			t.Fatalf("%d: %s", rec.Code, rec.Body.String())
		}
		data := onlyPhaseRequest(t, tracker)
		requireObservedPipeline(t, data, false)
		if data.FirstReasoningMs == nil || *data.StopReason != "end_turn" || data.Detail.OutputTokens != 400 {
			t.Fatalf("%+v", data)
		}
	})
	t.Run("claude_subscription", func(t *testing.T) {
		h, _, tracker := newClaudeSubscriptionTestHandler(t, upstream.URL)
		if rec := performClaudeSubscriptionRequest(t, h, claudeSubscriptionRequest{streaming: true}); rec.Code != 200 {
			t.Fatalf("%d: %s", rec.Code, rec.Body.String())
		}
		requireObservedPipeline(t, onlyPhaseRequest(t, tracker), false)
	})
}

func phaseAccountHandler(t *testing.T, url string, count int) (*Handler, *tracking.UsageTracker) {
	t.Helper()
	var accounts []*store.UpstreamAccountRecord
	for i := 1; i <= count; i++ {
		accounts = append(accounts, &store.UpstreamAccountRecord{ID: int64(i), AccountName: "synthetic-account", ProviderType: accountauth.ProviderAPIKey, CredentialRaw: "synthetic-key", BaseURL: url, Enabled: true})
	}
	h := newAccountPipelineTestHandler(t, url, &mockAccountPoolService{accounts: accounts})
	t.Cleanup(h.endpointManager.Stop)
	return h, attachRetryTracker(t, h)
}

func performPhaseAccountRequest(h *Handler, stream bool) *httptest.ResponseRecorder {
	body := `{"model":"gpt-5.5","input":[]}`
	if stream {
		body = `{"model":"gpt-5.5","input":[],"stream":true}`
	}
	r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	r = r.WithContext(context.WithValue(r.Context(), "conn_id", "synthetic-phase-account"))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestStreamPhases_AccountPoolEndToEnd(t *testing.T) {
	upstream := streamFixtureServer(t, "codex_hidden_reasoning")
	h, tracker := phaseAccountHandler(t, upstream.URL, 1)
	if rec := performPhaseAccountRequest(h, true); rec.Code != 200 {
		t.Fatalf("%d: %s", rec.Code, rec.Body.String())
	}
	data := onlyPhaseRequest(t, tracker)
	requireObservedPipeline(t, data, true)
	if *data.StopReason != "completed" || data.Detail.OutputTokens != 500 || !strings.Contains(data.Detail.ResponseReasoning, "reasoning_tokens:200") {
		t.Fatalf("%+v", data)
	}
}

func TestStreamPhases_AccountRetryUsesFinalResponseIDAndRegularStop(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempt := hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if attempt == 1 {
			w.Header().Set("Request-Id", "synthetic-failed-attempt")
			w.WriteHeader(401)
			_, _ = io.WriteString(w, `{"error":{"type":"authentication_error","message":"synthetic"}}`)
			return
		}
		w.Header().Set("Request-Id", "synthetic-final-upstream")
		_, _ = io.WriteString(w, `{"object":"response","status":"completed","model":"gpt-5.5","output":[],"usage":{"input_tokens":1,"output_tokens":2}}`)
	}))
	defer upstream.Close()
	h, tracker := phaseAccountHandler(t, upstream.URL, 2)
	if rec := performPhaseAccountRequest(h, false); rec.Code != 200 {
		t.Fatalf("%d: %s", rec.Code, rec.Body.String())
	}
	data := onlyPhaseRequest(t, tracker)
	if data.UpstreamRequestID == nil || *data.UpstreamRequestID != "synthetic-final-upstream" || *data.StopReason != "completed" || data.FirstContentMs != nil || data.ReasoningHidden != nil || hits.Load() != 2 {
		t.Fatalf("%+v, hits=%d", data.StreamPhases, hits.Load())
	}
}

func TestStreamPhases_ClaudeErrorResponseKeepsID(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Request-Id", "synthetic-error-response")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(403)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"permission_error","message":"synthetic"}}`)
	}))
	defer upstream.Close()
	h, _, tracker := newClaudeSubscriptionTestHandler(t, upstream.URL)
	if rec := performClaudeSubscriptionRequest(t, h, claudeSubscriptionRequest{}); rec.Code != 403 {
		t.Fatal(rec.Code)
	}
	data := onlyPhaseRequest(t, tracker)
	if data.UpstreamRequestID == nil || *data.UpstreamRequestID != "synthetic-error-response" || data.FirstContentMs != nil {
		t.Fatalf("%+v", data.StreamPhases)
	}
}
