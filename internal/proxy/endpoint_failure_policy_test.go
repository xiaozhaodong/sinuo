package proxy

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"sinuo/config"
	"sinuo/internal/endpoint"
	"sinuo/internal/proxy/handlers"
)

func responseWithStatus(status int, headers map[string]string) *http.Response {
	resp := &http.Response{StatusCode: status, Header: http.Header{}}
	for key, value := range headers {
		resp.Header.Set(key, value)
	}
	return resp
}

func tracedState(wroteHeaders bool) *handlers.UpstreamTraceState {
	state := &handlers.UpstreamTraceState{}
	if wroteHeaders {
		state.MarkWroteHeadersForTest()
	}
	return state
}

func TestDecideEndpointForwardOutcome_Table(t *testing.T) {
	cases := []struct {
		name             string
		forwardErr       error
		resp             *http.Response
		trace            *handlers.UpstreamTraceState
		bodySample       string
		wantAction       EndpointForwardAction
		wantMark         EndpointFailureMark
		wantCategory     endpoint.SoftFailureCategory
		wantFailureClass handlers.FailureClass
		wantRetryAfter   time.Duration
	}{
		{
			name:         "P0 连接失败（WroteHeaders 前）重放安全换候选",
			forwardErr:   errors.New("dial tcp: connection refused"),
			trace:        tracedState(false),
			wantAction:   EndpointForwardNextCandidate,
			wantMark:     EndpointMarkSoftFailure,
			wantCategory: endpoint.SoftFailureCategoryConnection,
		},
		{
			name:         "P0 本地准备失败（trace 为 nil）视为未发出",
			forwardErr:   errors.New("failed to apply endpoint auth"),
			trace:        nil,
			wantAction:   EndpointForwardNextCandidate,
			wantMark:     EndpointMarkSoftFailure,
			wantCategory: endpoint.SoftFailureCategoryConnection,
		},
		{
			name:         "P0 WroteHeaders 后失败为歧义，穿透",
			forwardErr:   errors.New("unexpected EOF"),
			trace:        tracedState(true),
			wantAction:   EndpointForwardPassthroughError,
			wantMark:     EndpointMarkSoftFailure,
			wantCategory: endpoint.SoftFailureCategoryTransport,
		},
		{
			name:       "401 鉴权失败换候选并长冷却",
			resp:       responseWithStatus(http.StatusUnauthorized, nil),
			trace:      tracedState(true),
			wantAction: EndpointForwardNextCandidate,
			wantMark:   EndpointMarkAuthCooldown,
		},
		{
			name:       "403 权限失败换候选并长冷却",
			resp:       responseWithStatus(http.StatusForbidden, nil),
			trace:      tracedState(true),
			wantAction: EndpointForwardNextCandidate,
			wantMark:   EndpointMarkAuthCooldown,
		},
		{
			// [Phase1 §9.2] 普通 429 交由管线执行同端点短重试与软失败结算
			name:         "429 无 Retry-After 进入限流软失败流程",
			resp:         responseWithStatus(http.StatusTooManyRequests, nil),
			trace:        tracedState(true),
			wantAction:   EndpointForwardRateLimited,
			wantMark:     EndpointMarkSoftFailure,
			wantCategory: endpoint.SoftFailureCategoryRateLimit,
		},
		{
			name:             "403 明确模型不支持写模型负缓存而非鉴权冷却",
			resp:             responseWithStatus(http.StatusForbidden, nil),
			trace:            tracedState(true),
			bodySample:       `{"error":{"message":"You do not have access to model provider-sonnet"}}`,
			wantAction:       EndpointForwardNextCandidate,
			wantMark:         EndpointMarkNegativeCache,
			wantFailureClass: handlers.FailureClassModelUnsupported,
		},
		{
			name:       "403 普通鉴权错误仍进入鉴权冷却",
			resp:       responseWithStatus(http.StatusForbidden, nil),
			trace:      tracedState(true),
			bodySample: `{"error":{"message":"invalid api key"}}`,
			wantAction: EndpointForwardNextCandidate,
			wantMark:   EndpointMarkAuthCooldown,
		},
		{
			// [Phase1 §9.2] Retry-After 用于本地重试等待（<=2s）与阈值后 cooldown
			name:           "429 带 Retry-After 解析响应头值",
			resp:           responseWithStatus(http.StatusTooManyRequests, map[string]string{"Retry-After": "5"}),
			trace:          tracedState(true),
			wantAction:     EndpointForwardRateLimited,
			wantMark:       EndpointMarkSoftFailure,
			wantCategory:   endpoint.SoftFailureCategoryRateLimit,
			wantRetryAfter: 5 * time.Second,
		},
		{
			name:             "413 换候选并写负缓存",
			resp:             responseWithStatus(http.StatusRequestEntityTooLarge, nil),
			trace:            tracedState(true),
			wantAction:       EndpointForwardNextCandidate,
			wantMark:         EndpointMarkNegativeCache,
			wantFailureClass: handlers.FailureClassPayloadTooLarge,
		},
		{
			name:             "400 模型不支持换候选并写负缓存",
			resp:             responseWithStatus(http.StatusBadRequest, nil),
			trace:            tracedState(true),
			bodySample:       `{"error":{"message":"Model not found: gpt-x"}}`,
			wantAction:       EndpointForwardNextCandidate,
			wantMark:         EndpointMarkNegativeCache,
			wantFailureClass: handlers.FailureClassModelUnsupported,
		},
		{
			name:             "400 schema 不兼容换候选并写负缓存",
			resp:             responseWithStatus(http.StatusBadRequest, nil),
			trace:            tracedState(true),
			bodySample:       `{"error":{"message":"extra inputs are not permitted"}}`,
			wantAction:       EndpointForwardNextCandidate,
			wantMark:         EndpointMarkNegativeCache,
			wantFailureClass: handlers.FailureClassSchemaIncompatible,
		},
		{
			name:       "404 普通客户端错误原样透传不记录",
			resp:       responseWithStatus(http.StatusNotFound, nil),
			trace:      tracedState(true),
			wantAction: EndpointForwardPassthroughRaw,
			wantMark:   EndpointMarkNone,
		},
		{
			name:         "500 服务器错误穿透并计 server_error 软失败",
			resp:         responseWithStatus(http.StatusInternalServerError, nil),
			trace:        tracedState(true),
			wantAction:   EndpointForwardPassthroughError,
			wantMark:     EndpointMarkSoftFailure,
			wantCategory: endpoint.SoftFailureCategoryServerError,
		},
		{
			name:         "503 服务器错误穿透并计 server_error 软失败",
			resp:         responseWithStatus(http.StatusServiceUnavailable, nil),
			trace:        tracedState(true),
			wantAction:   EndpointForwardPassthroughError,
			wantMark:     EndpointMarkSoftFailure,
			wantCategory: endpoint.SoftFailureCategoryServerError,
		},
		{
			name:       "200 进入响应处理",
			resp:       responseWithStatus(http.StatusOK, nil),
			trace:      tracedState(true),
			wantAction: EndpointForwardProcess,
			wantMark:   EndpointMarkNone,
		},
		{
			name:         "无错误无响应按歧义保守处理",
			resp:         nil,
			trace:        tracedState(true),
			wantAction:   EndpointForwardPassthroughError,
			wantMark:     EndpointMarkSoftFailure,
			wantCategory: endpoint.SoftFailureCategoryTransport,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision := decideEndpointForwardOutcome(tc.forwardErr, tc.resp, tc.trace, tc.bodySample)
			if decision.Action != tc.wantAction {
				t.Fatalf("action: got %v want %v (reason=%s)", decision.Action, tc.wantAction, decision.Reason)
			}
			if decision.Mark != tc.wantMark {
				t.Fatalf("mark: got %v want %v (reason=%s)", decision.Mark, tc.wantMark, decision.Reason)
			}
			if decision.Category != tc.wantCategory {
				t.Fatalf("category: got %q want %q", decision.Category, tc.wantCategory)
			}
			if decision.FailureClass != tc.wantFailureClass {
				t.Fatalf("failureClass: got %q want %q", decision.FailureClass, tc.wantFailureClass)
			}
			if decision.RetryAfter != tc.wantRetryAfter {
				t.Fatalf("retryAfter: got %v want %v", decision.RetryAfter, tc.wantRetryAfter)
			}
		})
	}
}

func TestEndpointSoftFailureCooldown(t *testing.T) {
	cfg := &config.Config{}
	cfg.Failover.RateLimitRetry.DefaultCooldown = 180 * time.Second
	cfg.Failover.ServerErrorCooldown = 120 * time.Second
	cfg.Failover.ConnectionCooldown = 90 * time.Second

	if got := endpointSoftFailureCooldown(cfg, endpoint.SoftFailureCategoryRateLimit, 5*time.Second); got != 5*time.Second {
		t.Fatalf("rate_limit 应优先 Retry-After，got %v", got)
	}
	if got := endpointSoftFailureCooldown(cfg, endpoint.SoftFailureCategoryRateLimit, 0); got != 180*time.Second {
		t.Fatalf("rate_limit 无头应用默认 180s，got %v", got)
	}
	if got := endpointSoftFailureCooldown(cfg, endpoint.SoftFailureCategoryServerError, 0); got != 120*time.Second {
		t.Fatalf("server_error 应为 120s，got %v", got)
	}
	if got := endpointSoftFailureCooldown(cfg, endpoint.SoftFailureCategoryConnection, 0); got != 90*time.Second {
		t.Fatalf("connection 应为 90s，got %v", got)
	}
	if got := endpointSoftFailureCooldown(cfg, endpoint.SoftFailureCategoryTransport, 0); got != 90*time.Second {
		t.Fatalf("transport 应复用 connection 冷却 90s，got %v", got)
	}
}
