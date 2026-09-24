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

	"cc-forwarder/config"
	"cc-forwarder/internal/endpoint"
)

func clientOAuthPipelineConfig(name, url string, priority int) config.EndpointConfig {
	cfg := endpointPipelineConfig(name, url, priority)
	cfg.AuthMode = config.EndpointAuthModeClientOAuth
	return cfg
}

// performClaudeCodeEntryRequest 模拟经 Claude Code 专用入口进入、携带 OAuth token 的请求
func performClaudeCodeEntryRequest(t *testing.T, handler *Handler, streaming bool) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"model":"claude-test","messages":[]}`
	if streaming {
		body = `{"model":"claude-test","messages":[],"stream":true}`
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(body))
	ctx := context.WithValue(req.Context(), "conn_id", "req-endpoint-pipeline")
	req = req.WithContext(endpoint.WithClaudeCodeEntry(ctx))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-ant-oat01-client")
	if streaming {
		req.Header.Set("Accept", "text/event-stream")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func TestEndpointPipeline_ClientOAuthRelaysAuthErrorWithoutCooldown(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("streaming=%v", streaming), func(t *testing.T) {
			var subscriptionHits, vendorHits atomic.Int32
			subscription := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				subscriptionHits.Add(1)
				if got := r.Header.Get("Authorization"); got != "Bearer sk-ant-oat01-client" {
					t.Errorf("上游应收到客户端 OAuth token，got %q", got)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Should-Retry", "false")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"OAuth token has expired"}}`))
			}))
			defer subscription.Close()
			vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				vendorHits.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			defer vendor.Close()

			handler, manager := newEndpointPipelineTestHandler(t,
				clientOAuthPipelineConfig("subscription", subscription.URL, 1),
				clientOAuthPipelineConfig("subscription-backup", subscription.URL, 2),
				endpointPipelineConfig("vendor", vendor.URL, 3),
			)

			recorder := performClaudeCodeEntryRequest(t, handler, streaming)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("401 应原样透传，got status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), "OAuth token has expired") || recorder.Header().Get("X-Should-Retry") != "false" {
				t.Fatalf("错误体与响应头应原样带回: headers=%v body=%s", recorder.Header(), recorder.Body.String())
			}
			if values := recorder.Header().Values("Content-Type"); len(values) != 1 || values[0] != "application/json" {
				t.Fatalf("Content-Type 只能有一个且为上游值，got %v", values)
			}
			if subscriptionHits.Load() != 1 || vendorHits.Load() != 0 {
				t.Fatalf("不换候选、不跨认证方式: subscription=%d vendor=%d", subscriptionHits.Load(), vendorHits.Load())
			}
			if manager.IsEndpointInCooldown("subscription") {
				t.Fatal("订阅直通端点的 401 不得写鉴权冷却")
			}
			if counts := manager.GetSoftFailureCounts("subscription", endpoint.SoftFailureScopeMessages); len(counts) != 0 {
				t.Fatalf("订阅直通端点的 401 不计软失败，got %v", counts)
			}
		})
	}
}

func TestEndpointPipeline_ClientOAuthRelays429WithRateLimitHeaders(t *testing.T) {
	var hits atomic.Int32
	subscription := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "1")
		w.Header().Set("Anthropic-Ratelimit-Unified-Status", "rejected")
		w.Header().Set("Anthropic-Ratelimit-Unified-Reset", "1790250000")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"usage limit reached"}}`))
	}))
	defer subscription.Close()

	handler, manager := newEndpointPipelineTestHandler(t, clientOAuthPipelineConfig("subscription", subscription.URL, 1))
	recorder := performClaudeCodeEntryRequest(t, handler, true)

	if recorder.Code != http.StatusTooManyRequests || !strings.Contains(recorder.Body.String(), "usage limit reached") {
		t.Fatalf("429 应原样透传，got status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Anthropic-Ratelimit-Unified-Status") != "rejected" || recorder.Header().Get("Anthropic-Ratelimit-Unified-Reset") != "1790250000" {
		t.Fatalf("额度头应原样带回，got %v", recorder.Header())
	}
	if hits.Load() != 1 {
		t.Fatalf("订阅直通 429 不做同端点短重试，upstream hits=%d", hits.Load())
	}
	if counts := manager.GetSoftFailureCounts("subscription", endpoint.SoftFailureScopeMessages); len(counts) != 0 {
		t.Fatalf("订阅直通 429 不计软失败，got %v", counts)
	}
}

func TestEndpointPipeline_ClientOAuthServerErrorRelaysAndCountsSoftFailure(t *testing.T) {
	subscription := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`))
	}))
	defer subscription.Close()

	handler, manager := newEndpointPipelineTestHandler(t, clientOAuthPipelineConfig("subscription", subscription.URL, 1))
	recorder := performClaudeCodeEntryRequest(t, handler, false)

	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "Overloaded") {
		t.Fatalf("5xx 应原样透传（不改写成 500），got status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if counts := manager.GetSoftFailureCounts("subscription", endpoint.SoftFailureScopeMessages); counts[endpoint.SoftFailureCategoryServerError] != 1 {
		t.Fatalf("5xx 应记一次 server_error 软失败，got %v", counts)
	}
}

func TestEndpointPipeline_ClientOAuthStreamingSuccessCarriesUpstreamHeaders(t *testing.T) {
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

	handler, _ := newEndpointPipelineTestHandler(t, clientOAuthPipelineConfig("subscription", subscription.URL, 1))
	recorder := performClaudeCodeEntryRequest(t, handler, true)

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
}

func TestEndpointPipeline_ConfiguredStreamingClientErrorHasSingleContentType(t *testing.T) {
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`))
	}))
	defer vendor.Close()

	handler, _ := newEndpointPipelineTestHandler(t, endpointPipelineConfig("vendor", vendor.URL, 1))
	recorder := performEndpointPipelineRequest(t, handler, true)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("普通 4xx 应原样透传，got %d", recorder.Code)
	}
	if values := recorder.Header().Values("Content-Type"); len(values) != 1 || values[0] != "application/json" {
		t.Fatalf("流式请求透传 4xx 时 Content-Type 只能是上游值，got %v", values)
	}
}
