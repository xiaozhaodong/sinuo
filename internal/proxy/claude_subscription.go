// Package proxy - Claude Code 订阅直通
// 创建时间: 2026-09-24
//
// Claude Code 入口处于订阅直通模式时，/v1/messages 与 /v1/messages/count_tokens 不进入端点调度，
// 直接带客户端自己的 claude.ai 登录凭据发往 Anthropic 官方。上游只有一个，按透明网关处理：
// 4xx / 5xx 原样透传、不冷却、不重试，由 Claude Code 自己提示或重试；流式成功带回
// anthropic-ratelimit-unified-* 等上游头。追踪记为 upstream_type=subscription，与 Codex 订阅直通对齐。
package proxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"cc-forwarder/internal/proxy/handlers"
)

const (
	// claudeSubscriptionUpstreamType 追踪记录的上游类型，与 Codex 订阅直通共用
	claudeSubscriptionUpstreamType = "subscription"
	// ClaudeSubscriptionUpstreamName 追踪记录的上游名
	ClaudeSubscriptionUpstreamName = "claude-subscription"
)

// handleClaudeSubscription 订阅直通：单次转发，响应透明透传
func (h *Handler) handleClaudeSubscription(ctx context.Context, w http.ResponseWriter, r *http.Request, bodyBytes []byte, lifecycleManager *RequestLifecycleManager, isSSE bool) {
	connID := lifecycleManager.GetRequestID()
	lifecycleManager.SetUpstream(claudeSubscriptionUpstreamType, ClaudeSubscriptionUpstreamName, ClaudeSubscriptionUpstreamName, 0)

	// 不代填任何本地凭据：没有登录凭据时直接说明原因，而不是让官方回一个难懂的 401
	if strings.TrimSpace(r.Header.Get("Authorization")) == "" {
		const message = "订阅直通需要 Claude Code 使用 claude.ai 登录，请求未携带登录凭据；Switchboard 不会代填本地凭据"
		lifecycleManager.FailRequest("subscription_unauthorized", message, http.StatusUnauthorized)
		writeClaudeCodeEntryError(w, http.StatusUnauthorized, "authentication_error", message)
		return
	}
	// 额度查询用最近一次请求的凭据；请求全部结束一段时间后补查额度
	h.claudeQuota.beginRequest(r.Header.Get("Authorization"), r.Header.Get("User-Agent"))
	defer h.claudeQuota.endRequest()

	lifecycleManager.UpdateStatus("forwarding", 0, 0)
	resp, upstreamCancel, traceState, forwardErr := h.forwarder.ForwardClaudeSubscription(
		ctx, r, bodyBytes, h.claudeSubscriptionBaseURL, isSSE, isSSE && shouldEnableEndpointTailDrain(r), lifecycleManager.RecordUpstreamWrite)
	recordEndpointUpstreamModel(lifecycleManager, traceState)
	releaseUpstream := func() {
		if upstreamCancel != nil {
			upstreamCancel()
			upstreamCancel = nil
		}
	}
	defer releaseUpstream()

	if policyErr := handlers.AsPrivacyPolicyError(forwardErr); policyErr != nil {
		slog.Warn(fmt.Sprintf("🛡️ [隐私保护] [%s] 订阅直通请求被策略拒绝: %s", connID, policyErr.Code))
		lifecycleManager.FailRequest(handlers.PrivacyFailureReason(policyErr), policyErr.Message, policyErr.StatusCode)
		handlers.WritePrivacyPolicyErrorResponse(w, policyErr)
		return
	}
	lifecycleManager.IncrementAttempt()
	if forwardErr != nil {
		h.failClaudeSubscriptionTransport(ctx, w, r, lifecycleManager, forwardErr)
		return
	}
	// 成功与 4xx / 5xx 都采集：429 带回的 status=rejected 正是额度用尽的信号
	h.claudeQuota.recordHeaders(resp.Header)

	if resp.StatusCode >= http.StatusBadRequest {
		slog.Warn(fmt.Sprintf("📥 [订阅直通] [%s] 官方返回 %d，原样透传", connID, resp.StatusCode))
		if err := h.writeRawResponse(w, resp); err != nil {
			lifecycleManager.FailRequest("subscription_response_write_error", err.Error(), resp.StatusCode)
			return
		}
		lifecycleManager.FailRequest(rawRelayFailureReason(resp.StatusCode), fmt.Sprintf("upstream returned %d", resp.StatusCode), resp.StatusCode)
		return
	}

	if !isSSE {
		h.processEndpointRegularResponse(w, r, resp, lifecycleManager, ClaudeSubscriptionUpstreamName)
		return
	}
	streamWriter, flusher, _ := handlers.NewStreamingResponseWriter(w)
	setEndpointStreamingHeaders(streamWriter)
	copyUpstreamStreamHeaders(streamWriter.Header(), resp.Header)
	h.processEndpointStreamingResponse(ctx, streamWriter, r, resp, flusher, lifecycleManager, ClaudeSubscriptionUpstreamName, nil, upstreamCancel)
}

// failClaudeSubscriptionTransport 连接层失败：客户端已断开按取消处理，其余以 Anthropic 错误格式返回
func (h *Handler) failClaudeSubscriptionTransport(ctx context.Context, w http.ResponseWriter, r *http.Request, lifecycleManager *RequestLifecycleManager, forwardErr error) {
	if ctx.Err() != nil {
		lifecycleManager.CancelRequest("client disconnected", nil)
		*r = *r.WithContext(context.WithValue(r.Context(), "final_status_code", 499))
		return
	}
	status, reason := http.StatusBadGateway, "subscription_upstream_unreachable"
	message := "订阅直通连接 Anthropic 失败，请检查网络后重试"
	if errors.Is(forwardErr, context.DeadlineExceeded) {
		status, reason = http.StatusGatewayTimeout, "subscription_upstream_timeout"
		message = "订阅直通等待 Anthropic 响应超时"
	}
	slog.Warn(fmt.Sprintf("❌ [订阅直通] [%s] %s: %v", lifecycleManager.GetRequestID(), reason, forwardErr))
	lifecycleManager.FailRequest(reason, forwardErr.Error(), status)
	writeClaudeCodeEntryError(w, status, "api_error", message)
}

// rawRelayFailureReason 原样透传官方错误时记录的失败原因
func rawRelayFailureReason(statusCode int) string {
	switch {
	case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden:
		return FailoverReasonAuthRejected
	case statusCode == http.StatusTooManyRequests:
		return FailoverReasonRateLimited
	case statusCode >= http.StatusInternalServerError:
		return FailoverReasonServerError
	default:
		return "upstream_client_error"
	}
}

// upstreamStreamSkipHeaders 流式成功响应不透传的上游头：由代理自己设置，或流处理后语义已变（如已解压）
var upstreamStreamSkipHeaders = map[string]struct{}{
	"Content-Type": {}, "Content-Length": {}, "Content-Encoding": {}, "Transfer-Encoding": {},
	"Connection": {}, "Keep-Alive": {}, "Cache-Control": {}, "Set-Cookie": {},
	"Proxy-Authenticate": {}, "Trailer": {}, "Upgrade": {},
}

// copyUpstreamStreamHeaders 把上游响应头带回客户端（anthropic-ratelimit-unified-*、retry-after、
// x-should-retry、request-id 等），覆盖同名值而不是追加
func copyUpstreamStreamHeaders(dst, src http.Header) {
	for key, values := range src {
		canonical := http.CanonicalHeaderKey(key)
		if _, skip := upstreamStreamSkipHeaders[canonical]; skip {
			continue
		}
		dst.Del(canonical)
		for _, value := range values {
			dst.Add(canonical, value)
		}
	}
}
