package proxy

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sinuo/internal/endpoint"
	"sinuo/internal/middleware"
	"sinuo/internal/tracking"
)

const claudeSubscriptionTestToken = "Bearer sk-ant-oat01-client"

// newClaudeSubscriptionTestHandler 配一个第三方端点用来证明订阅直通不碰端点调度，并挂真实追踪器
func newClaudeSubscriptionTestHandler(t *testing.T, subscriptionURL string) (*Handler, *atomic.Int32, *tracking.UsageTracker) {
	t.Helper()
	var vendorHits atomic.Int32
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vendorHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(vendor.Close)

	handler, manager := newEndpointPipelineTestHandler(t, endpointPipelineConfig("vendor", vendor.URL, 1))
	tracker := newImageGenerationTestTracker(t)
	handler.SetMonitoringMiddleware(middleware.NewMonitoringMiddleware(manager))
	handler.SetUsageTracker(tracker)
	handler.claudeSubscriptionBaseURL = subscriptionURL
	// 生产中订阅请求只会在入口已启用且处于订阅直通模式时到来，此时额度开关已打开
	handler.SetClaudeSubscriptionQuotaActive(true)
	return handler, &vendorHits, tracker
}

type claudeSubscriptionRequest struct {
	path      string
	streaming bool
	noAuth    bool
	requestID string
	userAgent string
}

// performClaudeSubscriptionRequest 模拟经处于订阅直通模式的 Claude Code 入口进入的请求
func performClaudeSubscriptionRequest(t *testing.T, handler *Handler, spec claudeSubscriptionRequest) *httptest.ResponseRecorder {
	t.Helper()
	if spec.path == "" {
		spec.path = "/v1/messages"
	}
	if spec.requestID == "" {
		spec.requestID = "req-claude-subscription"
	}
	body := `{"model":"claude-test","messages":[]}`
	if spec.streaming {
		body = `{"model":"claude-test","messages":[],"stream":true}`
	}
	req := httptest.NewRequest(http.MethodPost, spec.path, bytes.NewBufferString(body))
	ctx := context.WithValue(req.Context(), "conn_id", spec.requestID)
	req = req.WithContext(endpoint.WithClaudeCodeEntry(ctx, endpoint.ClaudeCodeEntryRequest{Mode: endpoint.ClaudeCodeEntryModeSubscription}))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", "sk-client-api-key")
	if !spec.noAuth {
		req.Header.Set("Authorization", claudeSubscriptionTestToken)
	}
	if spec.streaming {
		req.Header.Set("Accept", "text/event-stream")
	}
	if spec.userAgent != "" {
		req.Header.Set("User-Agent", spec.userAgent)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

// findClaudeSubscriptionDetail 等待追踪记录进入终态后返回
func findClaudeSubscriptionDetail(t *testing.T, tracker *tracking.UsageTracker, requestID string) tracking.RequestDetail {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		detail := findTrackedImageRequest(t, tracker, requestID)
		if detail.Status == "completed" || detail.Status == "failed" || time.Now().After(deadline) {
			return detail
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestClaudeSubscription_ForwardsClientTokenAndTracksAsSubscription(t *testing.T) {
	received := make(chan http.Header, 1)
	subscription := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":3,"output_tokens":2}}`))
	}))
	defer subscription.Close()

	handler, vendorHits, tracker := newClaudeSubscriptionTestHandler(t, subscription.URL)
	recorder := performClaudeSubscriptionRequest(t, handler, claudeSubscriptionRequest{})

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "msg_1") {
		t.Fatalf("订阅直通应成功，got status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	headers := <-received
	if headers.Get("Authorization") != claudeSubscriptionTestToken || headers.Get("X-Api-Key") != "" {
		t.Fatalf("官方只应收到客户端 OAuth token，got authorization=%q api_key=%q", headers.Get("Authorization"), headers.Get("X-Api-Key"))
	}
	if vendorHits.Load() != 0 {
		t.Fatal("订阅直通不得进入端点调度")
	}

	detail := findClaudeSubscriptionDetail(t, tracker, "req-claude-subscription")
	if detail.Status != "completed" || detail.RequestFamily != tracking.RequestFamilyClaude {
		t.Fatalf("追踪终态或请求族不符: %+v", detail)
	}
	if detail.UpstreamType != "subscription" || detail.UpstreamName != ClaudeSubscriptionUpstreamName || detail.EndpointName != "" {
		t.Fatalf("追踪应记为订阅直通且不归属任何端点: type=%q name=%q endpoint=%q", detail.UpstreamType, detail.UpstreamName, detail.EndpointName)
	}
	if detail.InputTokens != 3 || detail.OutputTokens != 2 {
		t.Fatalf("应解析官方返回的用量，got input=%d output=%d", detail.InputTokens, detail.OutputTokens)
	}
}

func TestClaudeSubscription_RelaysErrorsAsIs(t *testing.T) {
	cases := []struct {
		status  int
		body    string
		headers map[string]string
	}{
		{http.StatusUnauthorized, `{"type":"error","error":{"type":"authentication_error","message":"OAuth token has expired"}}`, map[string]string{"X-Should-Retry": "false"}},
		{http.StatusTooManyRequests, `{"type":"error","error":{"type":"rate_limit_error","message":"usage limit reached"}}`, map[string]string{
			"Retry-After": "1", "Anthropic-Ratelimit-Unified-Status": "rejected", "Anthropic-Ratelimit-Unified-Reset": "1790250000",
		}},
		{529, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, map[string]string{"X-Should-Retry": "true"}},
	}
	for _, tc := range cases {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("status=%d/streaming=%v", tc.status, streaming), func(t *testing.T) {
				var hits atomic.Int32
				subscription := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hits.Add(1)
					w.Header().Set("Content-Type", "application/json")
					for key, value := range tc.headers {
						w.Header().Set(key, value)
					}
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.body))
				}))
				defer subscription.Close()

				handler, vendorHits, tracker := newClaudeSubscriptionTestHandler(t, subscription.URL)
				recorder := performClaudeSubscriptionRequest(t, handler, claudeSubscriptionRequest{streaming: streaming})

				if recorder.Code != tc.status || recorder.Body.String() != tc.body {
					t.Fatalf("应原样透传状态码与错误体，got status=%d body=%s", recorder.Code, recorder.Body.String())
				}
				for key, value := range tc.headers {
					if got := recorder.Header().Get(key); got != value {
						t.Fatalf("响应头 %s 应原样带回，got %q", key, got)
					}
				}
				if values := recorder.Header().Values("Content-Type"); len(values) != 1 || values[0] != "application/json" {
					t.Fatalf("Content-Type 只能是上游值，got %v", values)
				}
				if hits.Load() != 1 || vendorHits.Load() != 0 {
					t.Fatalf("不重试、不换上游: subscription=%d vendor=%d", hits.Load(), vendorHits.Load())
				}
				detail := findClaudeSubscriptionDetail(t, tracker, "req-claude-subscription")
				if detail.Status != "failed" || detail.HTTPStatusCode == nil || *detail.HTTPStatusCode != tc.status {
					t.Fatalf("追踪应按上游状态记失败: %+v", detail)
				}
			})
		}
	}
}

func TestClaudeSubscription_StreamingSuccessCarriesUpstreamHeaders(t *testing.T) {
	subscription := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.42")
		w.Header().Set("Request-Id", "req_upstream_1")
		w.Header().Set("Set-Cookie", "tracking=1")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-test\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n"))
		_, _ = w.Write([]byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n"))
		_, _ = w.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer subscription.Close()

	handler, _, tracker := newClaudeSubscriptionTestHandler(t, subscription.URL)
	recorder := performClaudeSubscriptionRequest(t, handler, claudeSubscriptionRequest{streaming: true})

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "message_stop") {
		t.Fatalf("流式成功应完整转发，got status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Anthropic-Ratelimit-Unified-5h-Utilization") != "0.42" || recorder.Header().Get("Request-Id") != "req_upstream_1" {
		t.Fatalf("流式成功响应应带回上游额度头，got %v", recorder.Header())
	}
	if values := recorder.Header().Values("Content-Type"); len(values) != 1 || values[0] != "text/event-stream" {
		t.Fatalf("Content-Type 只能有一个，got %v", values)
	}
	if recorder.Header().Get("Set-Cookie") != "" {
		t.Fatal("上游 Set-Cookie 不应带回客户端")
	}
	if detail := findClaudeSubscriptionDetail(t, tracker, "req-claude-subscription"); detail.Status != "completed" || detail.OutputTokens != 1 {
		t.Fatalf("流式用量应记入追踪: %+v", detail)
	}
}

func TestClaudeSubscription_CountTokensGoesToOfficialEvenWithTokenCounting(t *testing.T) {
	var gotPath string
	subscription := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"input_tokens":12}`))
	}))
	defer subscription.Close()

	handler, vendorHits, _ := newClaudeSubscriptionTestHandler(t, subscription.URL)
	handler.config.TokenCounting.Enabled = true
	recorder := performClaudeSubscriptionRequest(t, handler, claudeSubscriptionRequest{path: "/v1/messages/count_tokens"})

	if recorder.Code != http.StatusOK || recorder.Body.String() != `{"input_tokens":12}` {
		t.Fatalf("count_tokens 应返回官方结果而不是本地估算，got status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if gotPath != "/v1/messages/count_tokens" || vendorHits.Load() != 0 {
		t.Fatalf("count_tokens 应直发官方: path=%q vendor=%d", gotPath, vendorHits.Load())
	}
}

func TestClaudeSubscription_MissingAuthorizationRejectedLocally(t *testing.T) {
	var hits atomic.Int32
	subscription := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer subscription.Close()

	handler, _, _ := newClaudeSubscriptionTestHandler(t, subscription.URL)
	recorder := performClaudeSubscriptionRequest(t, handler, claudeSubscriptionRequest{noAuth: true})

	if recorder.Code != http.StatusUnauthorized || !strings.Contains(recorder.Body.String(), "claude.ai 登录") {
		t.Fatalf("缺少登录凭据应在本地说明原因，got status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if hits.Load() != 0 {
		t.Fatal("缺少登录凭据时不应请求官方")
	}
}

func TestClaudeSubscription_UnreachableUpstreamReturnsAnthropicError(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()

	handler, vendorHits, _ := newClaudeSubscriptionTestHandler(t, closedURL)
	recorder := performClaudeSubscriptionRequest(t, handler, claudeSubscriptionRequest{})

	if recorder.Code != http.StatusBadGateway || !strings.Contains(recorder.Body.String(), `"type":"error"`) {
		t.Fatalf("连接失败应以 Anthropic 错误格式返回 502，got status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if vendorHits.Load() != 0 {
		t.Fatal("订阅直通连接失败不得改走第三方端点")
	}
}

func TestClaudeCodeEntry_EndpointModeUsesEndpointsAndDropsOAuthBeta(t *testing.T) {
	received := make(chan http.Header, 1)
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer vendor.Close()

	vendorCfg := endpointPipelineConfig("vendor", vendor.URL, 1)
	vendorCfg.ApiKey = "vendor-key"
	handler, _ := newEndpointPipelineTestHandler(t, vendorCfg)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(`{"model":"claude-test","messages":[]}`))
	ctx := context.WithValue(req.Context(), "conn_id", "req-entry-vendor")
	req = req.WithContext(endpoint.WithClaudeCodeEntry(ctx, endpoint.ClaudeCodeEntryRequest{Mode: endpoint.ClaudeCodeEntryModeEndpoint}))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", claudeSubscriptionTestToken)
	req.Header.Set("Anthropic-Beta", "oauth-2025-04-20,interleaved-thinking-2025-05-14")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("端点模式应走第三方端点，got status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	headers := <-received
	if got := headers.Get("Anthropic-Beta"); got != "interleaved-thinking-2025-05-14" {
		t.Fatalf("第三方不应收到 OAuth capability，got %q", got)
	}
	if headers.Get("Authorization") != "" || headers.Get("X-Api-Key") != "vendor-key" {
		t.Fatalf("第三方只能收到端点 Key，got authorization=%q api_key=%q", headers.Get("Authorization"), headers.Get("X-Api-Key"))
	}
}
