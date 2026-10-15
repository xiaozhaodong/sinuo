package proxy

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"sinuo/internal/endpoint"
	"sinuo/internal/middleware"
	"sinuo/internal/store"
	"sinuo/internal/tracking"
)

// waitRequestDetail 轮询直到请求行满足条件（热池或数据库）。
func waitRequestDetail(t *testing.T, tracker *tracking.UsageTracker, requestID string, ready func(tracking.RequestDetail) bool) tracking.RequestDetail {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := tracker.GetRequestLifecycleData(context.Background(), requestID)
		if err != nil {
			t.Fatalf("GetRequestLifecycleData failed: %v", err)
		}
		if data != nil && ready(data.Detail) {
			return data.Detail
		}
		if time.Now().After(deadline) {
			if data != nil {
				t.Fatalf("timed out waiting for %s, last detail=%+v", requestID, data.Detail)
			}
			t.Fatalf("timed out waiting for %s (no row)", requestID)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// attachTracking 挂载 tracker 与监控中间件（CompleteRequest 会调用监控中间件，nil 指针会 panic）。
func attachTracking(t *testing.T, handler *Handler) *tracking.UsageTracker {
	t.Helper()
	tracker := newLifecyclePanelTestTracker(t)
	handler.SetUsageTracker(tracker)
	handler.SetMonitoringMiddleware(middleware.NewMonitoringMiddleware(handler.endpointManager))
	return tracker
}

func performTrackedClaudeRequest(t *testing.T, handler *Handler, requestID, body string, streaming bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(body))
	req = req.WithContext(context.WithValue(req.Context(), "conn_id", requestID))
	req.Header.Set("Content-Type", "application/json")
	if streaming {
		req.Header.Set("Accept", "text/event-stream")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

// 422 未分配（无 attempt）快速终态后，start 行仍带 request_model / request_reasoning，response_model 为空。
func TestModelConsistency_UnassignedFastFailKeepsRequestProfile(t *testing.T) {
	handler, manager := newEndpointPipelineTestHandler(t, endpointPipelineConfig("primary", endpointPipelineClosedURL(t), 1))
	tracker := attachTracking(t, handler)
	manager.SetRouteScopePolicy(pipelineRouteScopePolicy{scope: endpoint.RouteScope{
		Kind: endpoint.RouteScopeUnassigned, Model: "claude-test", UnassignedReason: "model_routing_unassigned",
	}})

	const requestID = "req-mc-unassigned"
	recorder := performTrackedClaudeRequest(t, handler, requestID, `{"model":"claude-test","messages":[],"thinking":{"type":"adaptive"},"output_config":{"effort":"xhigh"}}`, false)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	detail := waitRequestDetail(t, tracker, requestID, func(d tracking.RequestDetail) bool { return d.Status == "failed" })
	if detail.RequestModel != "claude-test" || detail.RequestReasoning != "thinking:adaptive effort:xhigh" {
		t.Fatalf("request profile missing on fast fail: %+v", detail)
	}
	if detail.ResponseModel != "" || detail.UpstreamModel != "" || detail.ResponseReasoning != "" {
		t.Fatalf("no-attempt path must leave response/upstream columns empty: %+v", detail)
	}
	if detail.ModelName != "claude-test" {
		t.Fatalf("model_name billing fallback changed: %q", detail.ModelName)
	}
}

// 改写端点 → upstream_model 为目标；failover A(改写) → B(不改写) 最终为空；无 usage 时 response_model 仍写入。
func TestModelConsistency_EndpointRewriteAndFailoverUpstreamModel(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"model not found"}}`))
	}))
	t.Cleanup(primary.Close)
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// 无 usage 的 Claude JSON：响应模型与思考块仍要落库
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-haiku-4-5","content":[{"type":"thinking","thinking":"..."},{"type":"text","text":"ok"}]}`))
	}))
	t.Cleanup(backup.Close)

	primaryConfig := endpointPipelineConfig("primary", primary.URL, 1)
	primaryConfig.ModelRewriteRules = `[{"paths":["/v1/messages"],"match":"exact","from":"claude-test","to":"primary-model"}]`
	backupConfig := endpointPipelineConfig("backup", backup.URL, 2)
	handler, _ := newEndpointPipelineTestHandler(t, primaryConfig, backupConfig)
	tracker := attachTracking(t, handler)

	const requestID = "req-mc-failover"
	recorder := performTrackedClaudeRequest(t, handler, requestID, `{"model":"claude-test","messages":[],"thinking":{"type":"disabled"}}`, false)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected backup success, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	detail := waitRequestDetail(t, tracker, requestID, func(d tracking.RequestDetail) bool { return d.Status == "completed" })
	if detail.UpstreamModel != "" {
		t.Fatalf("failover to non-rewriting endpoint must clear upstream_model, got %q", detail.UpstreamModel)
	}
	if detail.RequestModel != "claude-test" || detail.RequestReasoning != "thinking:disabled" {
		t.Fatalf("request profile = %q / %q", detail.RequestModel, detail.RequestReasoning)
	}
	if detail.ResponseModel != "claude-haiku-4-5" || detail.ResponseReasoning != "thinking:present" {
		t.Fatalf("response diagnostics without usage = %q / %q", detail.ResponseModel, detail.ResponseReasoning)
	}
}

// 单端点改写成功：upstream_model 为改写目标。
func TestModelConsistency_EndpointRewriteRecordsUpstreamModel(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"provider-sonnet","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":3,"output_tokens":2}}`))
	}))
	t.Cleanup(upstream.Close)
	cfg := endpointPipelineConfig("rewrite", upstream.URL, 1)
	cfg.ModelRewriteRules = `[{"paths":["/v1/messages"],"match":"exact","from":"claude-test","to":"provider-sonnet"}]`
	handler, _ := newEndpointPipelineTestHandler(t, cfg)
	tracker := attachTracking(t, handler)

	const requestID = "req-mc-rewrite"
	if recorder := performTrackedClaudeRequest(t, handler, requestID, `{"model":"claude-test","messages":[]}`, false); recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	detail := waitRequestDetail(t, tracker, requestID, func(d tracking.RequestDetail) bool { return d.Status == "completed" })
	if detail.UpstreamModel != "provider-sonnet" || detail.ResponseModel != "provider-sonnet" || detail.RequestModel != "claude-test" {
		t.Fatalf("rewrite columns = upstream %q response %q request %q", detail.UpstreamModel, detail.ResponseModel, detail.RequestModel)
	}
	if detail.ResponseReasoning != "thinking:absent" {
		t.Fatalf("complete JSON without thinking block must be absent, got %q", detail.ResponseReasoning)
	}
}

// 账号池：api_key 改写 → upstream_model 为目标并携带 Codex 思考串；OAuth 账号为空。
func TestModelConsistency_AccountPipelineUpstreamModel(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","model":"gpt-5.5","reasoning":{"effort":"medium"},"output":[],"usage":{"input_tokens":1,"output_tokens":1,"output_tokens_details":{"reasoning_tokens":7}}}`))
	}))
	t.Cleanup(upstream.Close)

	service := &mockAccountPoolService{accounts: []*store.UpstreamAccountRecord{
		{ID: 9, AccountName: "anyrouter", ProviderType: "api_key", CredentialRaw: "sk-anyrouter", BaseURL: upstream.URL, ModelRewriteRules: testGPT54ModelRewriteRules, Enabled: true},
	}}
	handler := newAccountPipelineTestHandler(t, upstream.URL, service)
	tracker := attachTracking(t, handler)

	const requestID = "req-mc-account-rewrite"
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"model":"gpt-5.4","input":"hello","reasoning":{"effort":"xhigh"}}`))
	req = req.WithContext(context.WithValue(req.Context(), "conn_id", requestID))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	detail := waitRequestDetail(t, tracker, requestID, func(d tracking.RequestDetail) bool { return d.Status == "completed" })
	if detail.RequestModel != "gpt-5.4" || detail.UpstreamModel != "gpt-5.5" || detail.ResponseModel != "gpt-5.5" {
		t.Fatalf("account rewrite columns = request %q upstream %q response %q", detail.RequestModel, detail.UpstreamModel, detail.ResponseModel)
	}
	if detail.RequestReasoning != "effort:xhigh" || detail.ResponseReasoning != "effort:medium reasoning_tokens:7" {
		t.Fatalf("account reasoning = %q / %q", detail.RequestReasoning, detail.ResponseReasoning)
	}

	// OAuth 账号不改写 → upstream_model 为空
	oauthService := &mockAccountPoolService{accounts: []*store.UpstreamAccountRecord{
		{ID: 17, AccountName: "oauth-main", ProviderType: "chatgpt_refresh_token", CredentialRaw: `{"refresh_token":"rt-1","chatgpt_account_id":"acc-1"}`, BaseURL: upstream.URL, ModelRewriteRules: testGPT54ModelRewriteRules, Enabled: true},
	}}
	oauthHandler := newAccountPipelineTestHandler(t, upstream.URL, oauthService)
	oauthTracker := attachTracking(t, oauthHandler)
	oauthHandler.accountHTTPInitOnce.Do(func() {})
	oauthHandler.accountHTTPClient = upstream.Client()
	oauthHandler.accountSSEHTTPClient = upstream.Client()

	const oauthRequestID = "req-mc-account-oauth"
	req = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"model":"gpt-5.4","input":"hello"}`))
	req = req.WithContext(context.WithValue(req.Context(), "conn_id", oauthRequestID))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	oauthHandler.ServeHTTP(rec, req)
	detail = waitRequestDetail(t, oauthTracker, oauthRequestID, func(d tracking.RequestDetail) bool {
		return d.Status != "pending" && d.Status != "forwarding" && d.Status != "processing"
	})
	if detail.UpstreamModel != "" || detail.RequestModel != "gpt-5.4" {
		t.Fatalf("oauth account must not report upstream model: %+v", detail)
	}
}

// lifecycle manager：SetUpstreamModel 空串覆盖、SetResponseModel/Reasoning 非空覆盖、StartRequest 交付请求两项、终态携带诊断。
func TestRequestLifecycleManager_ModelConsistencyDiagnostics(t *testing.T) {
	tracker := newLifecyclePanelTestTracker(t)
	rlm := NewRequestLifecycleManager(tracker, nil, "req-mc-lifecycle", nil)
	rlm.SetRequestProfile("claude-test", "thinking:adaptive")
	rlm.StartRequest("127.0.0.1", "agent", "POST", "/v1/messages", false)

	rlm.SetUpstreamModel("provider-sonnet")
	rlm.SetUpstreamModel("")
	rlm.SetResponseModel("claude-test-20250929")
	rlm.SetResponseModel("")
	rlm.SetResponseModel("unknown")
	rlm.SetResponseReasoning("thinking:present")
	rlm.SetResponseReasoning("")
	diag := rlm.responseDiagnostics()
	if diag.UpstreamModel != "" || diag.ResponseModel != "claude-test-20250929" || diag.ResponseReasoning != "thinking:present" {
		t.Fatalf("diagnostics = %+v", diag)
	}

	rlm.FailRequest("server_error", "boom", 502)
	detail := waitRequestDetail(t, tracker, "req-mc-lifecycle", func(d tracking.RequestDetail) bool { return d.Status == "failed" })
	if detail.RequestModel != "claude-test" || detail.RequestReasoning != "thinking:adaptive" {
		t.Fatalf("start dimensions missing: %+v", detail)
	}
	if detail.ResponseModel != "claude-test-20250929" || detail.ResponseReasoning != "thinking:present" || detail.UpstreamModel != "" {
		t.Fatalf("final failure diagnostics missing: %+v", detail)
	}
}

// 订阅提交解析：envelope 与 nested response 都读 reasoning.effort，nested 优先。
func TestParseSubscriptionCreate_Reasoning(t *testing.T) {
	create, ok := parseSubscriptionCreate([]byte(`{"type":"response.create","model":"gpt-5.5","reasoning":{"effort":"low"},"response":{"model":"gpt-5.5-pro","reasoning":{"effort":"xhigh"}}}`))
	if !ok || create.Model != "gpt-5.5-pro" || create.Reasoning != "effort:xhigh" {
		t.Fatalf("nested reasoning must win: %+v", create)
	}
	create, ok = parseSubscriptionCreate([]byte(`{"type":"response.create","model":"gpt-5.5","reasoning":{"effort":"Medium"}}`))
	if !ok || create.Reasoning != "effort:medium" {
		t.Fatalf("envelope reasoning: %+v", create)
	}
	create, ok = parseSubscriptionCreate([]byte(`{"type":"response.create","model":"gpt-5.5"}`))
	if !ok || create.Reasoning != "" {
		t.Fatalf("missing reasoning must be empty: %+v", create)
	}
}

// failingBodyWriter 模拟客户端在响应头之后断开：WriteHeader 成功，Write 失败。
type failingBodyWriter struct {
	*httptest.ResponseRecorder
}

func (w *failingBodyWriter) Write([]byte) (int, error) {
	return 0, errors.New("client disconnected")
}

// 上游已完整返回、写回客户端失败时，失败结算仍保留 response_model / response_reasoning。
func TestModelConsistency_ClientWriteFailureKeepsResponseDiagnostics(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-haiku-4-5","content":[{"type":"thinking","thinking":"..."},{"type":"text","text":"ok"}],"usage":{"input_tokens":3,"output_tokens":2}}`))
	}))
	t.Cleanup(upstream.Close)
	handler, _ := newEndpointPipelineTestHandler(t, endpointPipelineConfig("primary", upstream.URL, 1))
	tracker := attachTracking(t, handler)

	const requestID = "req-mc-write-fail"
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(`{"model":"claude-test","messages":[]}`))
	req = req.WithContext(context.WithValue(req.Context(), "conn_id", requestID))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(&failingBodyWriter{ResponseRecorder: httptest.NewRecorder()}, req)

	detail := waitRequestDetail(t, tracker, requestID, func(d tracking.RequestDetail) bool { return d.Status == "failed" })
	if detail.ResponseModel != "claude-haiku-4-5" || detail.ResponseReasoning != "thinking:present" {
		t.Fatalf("write failure must keep received response diagnostics, got model=%q reasoning=%q", detail.ResponseModel, detail.ResponseReasoning)
	}
}
