package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"cc-forwarder/config"
	"cc-forwarder/internal/endpoint"
	"cc-forwarder/internal/transport"
)

// CountTokensHandler 处理 /v1/messages/count_tokens 请求（v8 收敛方案 §10/§10.1）。
// Auto/Preferred：统一调度候选 + 尝试预算 + count_tokens scoped 软失败，耗尽后本地估算；
// Manual Fixed：只用目标端点，不 fallback、不估算，失败返回明确错误。
// 与 /v1/messages 的不对称是有意设计：纯计数请求无计费副作用、天然重放安全、
// 有估算兜底，因此普通 429/5xx 允许请求内换候选。
type CountTokensHandler struct {
	config          *config.Config
	endpointManager *endpoint.Manager
	forwarder       *Forwarder
}

// 估算原因白名单（§10.1：不得包含端点名、凭据或原始错误 body）
const (
	estimationReasonNoEligibleEndpoint    = "no_eligible_endpoint"
	estimationReasonUnsupported           = "unsupported"
	estimationReasonUpstreamFailed        = "upstream_failed"
	estimationReasonAttemptBudgetExceeded = "attempt_budget_exhausted"
)

// NewCountTokensHandler 创建 CountTokensHandler
func NewCountTokensHandler(cfg *config.Config, em *endpoint.Manager, f *Forwarder) *CountTokensHandler {
	return &CountTokensHandler{
		config:          cfg,
		endpointManager: em,
		forwarder:       f,
	}
}

// CountTokensRequest 定义 count_tokens 请求结构
type CountTokensRequest struct {
	Model    string                   `json:"model"`
	Messages []map[string]interface{} `json:"messages"`
	System   interface{}              `json:"system,omitempty"`
	Tools    []interface{}            `json:"tools,omitempty"`
}

// CountTokensResponse 定义响应结构
type CountTokensResponse struct {
	InputTokens int `json:"input_tokens"`
}

// countTokensAttemptOutcome 单端点尝试结果
type countTokensAttemptOutcome int

const (
	countTokensAttemptSuccess countTokensAttemptOutcome = iota
	countTokensAttemptRetryNext
	countTokensAttemptPrivacyBlocked
)

// Handle 处理 count_tokens 请求（§10.1 终态矩阵）
func (h *CountTokensHandler) Handle(ctx context.Context, w http.ResponseWriter, r *http.Request, bodyBytes []byte, connID string) {
	slog.Info(fmt.Sprintf("🔢 [Token计数] [%s] 收到count_tokens请求", connID))
	routeProfile := endpoint.BuildRouteRequestProfile(r.URL.Path, bodyBytes)
	routeProfile.ApplyClaudeCodeEntry(r.Context())
	scope := h.endpointManager.ResolveRouteScope(routeProfile)

	// Manual Fixed：只用目标，不 fallback、不估算（§10.1）
	// 未分配作用域刻意排除在外：count_tokens 不参与模型路由的 422 契约，
	// 未分配模型一律降级到本地估算，不要"修"成返回 422。
	if scope.Kind != endpoint.RouteScopeUnassigned && scope.Mode == endpoint.RouteModeManualFixed {
		h.handleManualFixed(ctx, w, r, bodyBytes, routeProfile, connID)
		return
	}

	result := h.endpointManager.PrepareRouteCandidates(ctx, routeProfile)
	type countTokensCandidate struct {
		plan endpoint.EndpointAttemptPlan
	}
	supported := make([]countTokensCandidate, 0, len(result.Candidates))
	sawCandidate := len(result.Candidates) > 0
	for _, plan := range result.Plans {
		if plan.SupportsCountTokens {
			supported = append(supported, countTokensCandidate{plan: plan})
		}
	}

	if len(supported) == 0 {
		reason := estimationReasonNoEligibleEndpoint
		if sawCandidate {
			reason = estimationReasonUnsupported
		}
		slog.Info(fmt.Sprintf("🔍 [Token计数] [%s] 无支持端点，使用本地估算 (%s)", connID, reason))
		h.respondWithEstimation(w, bodyBytes, connID, reason)
		return
	}

	// §10 规则 7：独立请求尝试预算
	budget := h.config.Failover.MaxCandidateAttempts
	if budget <= 0 {
		budget = 3
	}
	attempted := 0
	budgetExhausted := false
	for _, candidate := range supported {
		if attempted >= budget {
			budgetExhausted = true
			break
		}

		// §14.2：attempt 前原子重校验（删除 / pending gate / hard disable / config revision）
		admission, acquireErr := h.endpointManager.AcquireEndpointAttempt(candidate.plan)
		if acquireErr != nil {
			slog.Warn(fmt.Sprintf("⏭️ [Token计数] [%s] 候选 %s 跳过: %s", connID, candidate.plan.EndpointName, acquireErr.Error()))
			continue
		}
		attempted++
		target := admission.Target

		responseBytes, outcome, err := func() ([]byte, countTokensAttemptOutcome, error) {
			defer admission.Release()
			return h.attemptEndpoint(ctx, r, bodyBytes, target, routeProfile, connID)
		}()
		switch outcome {
		case countTokensAttemptPrivacyBlocked:
			policyErr := AsPrivacyPolicyError(err)
			slog.Warn(fmt.Sprintf("🛡️ [隐私保护] [%s] count_tokens 被策略拒绝: %s", connID, policyErr.Code))
			WritePrivacyPolicyErrorResponse(w, policyErr)
			return
		case countTokensAttemptSuccess:
			// FullSuccess：清同 path scope 软失败（§9.3 规则 2）；不更新 /v1/messages retained（§10 规则 6）
			h.endpointManager.ApplyEndpointAttemptSettlement(target.Name(), target.Revision(), func() {
				h.endpointManager.ClearSoftFailureScope(target.Name(), endpoint.SoftFailureScopeCountTokens)
			})
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(responseBytes)
			slog.Info(fmt.Sprintf("✅ [Token计数-转发] [%s] 端点 %s 转发成功", connID, target.Name()))
			return
		default:
			continue
		}
	}

	reason := estimationReasonUpstreamFailed
	if budgetExhausted {
		reason = estimationReasonAttemptBudgetExceeded
	}
	slog.Warn(fmt.Sprintf("⚠️ [Token计数] [%s] 转发失败，降级到本地估算 (%s)", connID, reason))
	h.respondWithEstimation(w, bodyBytes, connID, reason)
}

// handleManualFixed 固定模式：目标不可用返回明确错误，不估算（§10.1）
func (h *CountTokensHandler) handleManualFixed(ctx context.Context, w http.ResponseWriter, r *http.Request, bodyBytes []byte, profile endpoint.RouteRequestProfile, connID string) {
	result := h.endpointManager.PrepareRouteCandidates(ctx, profile)
	if len(result.Candidates) == 0 {
		if block := h.endpointManager.GetRouteBlock(profile); block != nil {
			writeCountTokensError(w, block.StatusCode, block.Code, block.Message)
			return
		}
		writeCountTokensError(w, http.StatusServiceUnavailable, "route_blocked_manual_fixed",
			"Manual fixed endpoint is not routable for count_tokens.")
		return
	}

	if !result.Plans[0].SupportsCountTokens {
		writeCountTokensError(w, http.StatusUnprocessableEntity, "endpoint_capability_mismatch",
			"Manual fixed endpoint does not support count_tokens.")
		return
	}

	// §14.2：attempt 前原子重校验
	admission, acquireErr := h.endpointManager.AcquireEndpointAttempt(result.Plans[0])
	if acquireErr != nil {
		writeCountTokensError(w, http.StatusServiceUnavailable, "route_blocked_manual_fixed",
			fmt.Sprintf("Manual fixed endpoint is not admittable: %s", acquireErr.Error()))
		return
	}
	target := admission.Target

	responseBytes, outcome, err := func() ([]byte, countTokensAttemptOutcome, error) {
		defer admission.Release()
		return h.attemptEndpoint(ctx, r, bodyBytes, target, profile, connID)
	}()
	switch outcome {
	case countTokensAttemptPrivacyBlocked:
		WritePrivacyPolicyErrorResponse(w, AsPrivacyPolicyError(err))
	case countTokensAttemptSuccess:
		h.endpointManager.ApplyEndpointAttemptSettlement(target.Name(), target.Revision(), func() {
			h.endpointManager.ClearSoftFailureScope(target.Name(), endpoint.SoftFailureScopeCountTokens)
		})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(responseBytes)
	default:
		writeCountTokensError(w, http.StatusBadGateway, "count_tokens_upstream_error",
			"Manual fixed endpoint failed to serve count_tokens.")
	}
}

// attemptEndpoint 单端点一次尝试；失败时按 §10 记录 count_tokens scoped 软失败或负缓存
func (h *CountTokensHandler) attemptEndpoint(ctx context.Context, r *http.Request, bodyBytes []byte, target *endpoint.EndpointAttemptTarget, profile endpoint.RouteRequestProfile, connID string) ([]byte, countTokensAttemptOutcome, error) {
	ep := &endpoint.Endpoint{Config: target.Config()}
	endpointBody, _ := prepareBodyForEndpoint(r.URL.Path, bodyBytes, ep)
	// 🛡️ 出站隐私过滤；策略拒绝直返，不降级估算、不计失败
	preparedBody, err := ApplyPrivacyFilterForEndpoint(h.forwarder.privacyFilter, r, endpointBody, ep)
	if err != nil {
		if AsPrivacyPolicyError(err) != nil {
			return nil, countTokensAttemptPrivacyBlocked, err
		}
		slog.Warn(fmt.Sprintf("⚠️ [Token计数] [%s] 请求准备失败: %v", connID, err))
		return nil, countTokensAttemptRetryNext, err
	}

	targetURL := ep.Config.URL + "/v1/messages/count_tokens"
	req, err := http.NewRequestWithContext(ctx, "POST", targetURL, bytes.NewReader(preparedBody))
	if err != nil {
		return nil, countTokensAttemptRetryNext, err
	}
	h.forwarder.CopyAttemptHeaders(r, req, target)

	httpTransport, err := transport.CreateTransport(h.config)
	if err != nil {
		return nil, countTokensAttemptRetryNext, err
	}
	client := &http.Client{Timeout: ep.Config.Timeout, Transport: httpTransport}

	resp, err := client.Do(req)
	if err != nil {
		slog.Debug(fmt.Sprintf("❌ [转发失败] [%s] 端点: %s, 错误: %v", connID, ep.Config.Name, err))
		h.recordScopedSoftFailure(ep.Config.Name, target.Revision(), target.FailureEpoch(), endpoint.SoftFailureCategoryConnection, 0)
		return nil, countTokensAttemptRetryNext, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		responseBytes, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			h.recordScopedSoftFailure(ep.Config.Name, target.Revision(), target.FailureEpoch(), endpoint.SoftFailureCategoryTransport, 0)
			return nil, countTokensAttemptRetryNext, readErr
		}
		return responseBytes, countTokensAttemptSuccess, nil
	}

	errorBody, _ := io.ReadAll(resp.Body)
	errorText := string(errorBody)
	switch {
	case ep.Config.IsClientOAuth() && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden):
		// 订阅直通：凭据来自客户端请求（token 过期等），端点本身没有问题；
		// 写全局鉴权冷却会让 /v1/messages 也被锁住，而本路径降级为估算后客户端毫无感知
		slog.Info(fmt.Sprintf("🔑 [Token计数] [%s] 订阅直通端点 %s 返回 %d，降级估算且不冷却端点", connID, ep.Config.Name, resp.StatusCode))
	case resp.StatusCode == http.StatusUnauthorized:
		// §10 规则 4：凭据两个 path 共用，认证失败写端点全局 auth cooldown
		h.recordAuthCooldown(ep.Config.Name, target.Revision(), target.FailureEpoch(), connID)
	case isCountTokensUnsupported(resp.StatusCode, errorText):
		// 须在 403 鉴权和 >=500 之前判定：明确的路径能力错误不应连坐 messages。
		h.recordCountTokensNegativeHit(ep.Config.Name, target.Revision(), endpoint.FailureClassCountTokensUnsupported, profile, errorText, connID)
	case resp.StatusCode == http.StatusForbidden && IsModelUnsupportedError(errorText):
		// 403 也可能表示当前模型不可用；按模型负缓存，不写全局 auth cooldown。
		h.recordCountTokensNegativeHit(ep.Config.Name, target.Revision(), endpoint.FailureClassModelUnsupported, profile, errorText, connID)
	case resp.StatusCode == http.StatusForbidden:
		h.recordAuthCooldown(ep.Config.Name, target.Revision(), target.FailureEpoch(), connID)
	case resp.StatusCode == http.StatusTooManyRequests:
		h.recordScopedSoftFailure(ep.Config.Name, target.Revision(), target.FailureEpoch(), endpoint.SoftFailureCategoryRateLimit, parseCountTokensRetryAfter(resp))
	case resp.StatusCode >= http.StatusBadRequest && resp.StatusCode < http.StatusInternalServerError && IsModelUnsupportedError(errorText):
		h.recordCountTokensNegativeHit(ep.Config.Name, target.Revision(), endpoint.FailureClassModelUnsupported, profile, errorText, connID)
	case resp.StatusCode >= http.StatusInternalServerError:
		h.recordScopedSoftFailure(ep.Config.Name, target.Revision(), target.FailureEpoch(), endpoint.SoftFailureCategoryServerError, 0)
	}
	return nil, countTokensAttemptRetryNext, fmt.Errorf("upstream returned %d", resp.StatusCode)
}

func (h *CountTokensHandler) recordAuthCooldown(endpointName string, revision int64, epoch int64, connID string) {
	cooldown := h.config.Failover.AuthCooldown
	if cooldown <= 0 {
		cooldown = 30 * time.Minute
	}
	h.endpointManager.ApplyEndpointFailureSettlement(endpointName, revision, epoch, func() {
		if h.endpointManager.SetEndpointCooldownFenced(endpointName, cooldown, "auth_rejected", epoch) {
			slog.Warn(fmt.Sprintf("🔑 [Token计数] [%s] 端点 %s 认证失败，写入全局鉴权冷却", connID, endpointName))
		}
	})
}

func (h *CountTokensHandler) recordCountTokensNegativeHit(endpointName string, revision int64, failureClass string, profile endpoint.RouteRequestProfile, detail, connID string) {
	h.endpointManager.ApplyEndpointAttemptSettlement(endpointName, revision, func() {
		h.endpointManager.RecordNegativeRouteHit(endpointName, failureClass, profile, detail)
		slog.Info(fmt.Sprintf("🧭 [Token计数] [%s] 端点 %s 记录 %s 负向缓存", connID, endpointName, failureClass))
	})
}

// recordScopedSoftFailure count_tokens scope 软失败；达阈值只写进程内 scoped cooldown（D17），
// 不得冷却 /v1/messages（§10 规则 5）
func (h *CountTokensHandler) recordScopedSoftFailure(endpointName string, revision int64, epoch int64, category endpoint.SoftFailureCategory, retryAfter time.Duration) {
	h.endpointManager.ApplyEndpointFailureSettlement(endpointName, revision, epoch, func() {
		count, tripped, _ := h.endpointManager.RecordSoftFailureFenced(endpointName, endpoint.SoftFailureScopeCountTokens, category, epoch)
		slog.Debug("count_tokens 软失败", "endpoint", endpointName, "category", category, "count", count)
		if !tripped {
			return
		}
		cooldown := retryAfter
		if cooldown <= 0 {
			switch category {
			case endpoint.SoftFailureCategoryRateLimit:
				cooldown = 180 * time.Second
				if h.config.Failover.RateLimitRetry.DefaultCooldown > 0 {
					cooldown = h.config.Failover.RateLimitRetry.DefaultCooldown
				}
			case endpoint.SoftFailureCategoryServerError:
				cooldown = 120 * time.Second
				if h.config.Failover.ServerErrorCooldown > 0 {
					cooldown = h.config.Failover.ServerErrorCooldown
				}
			default:
				cooldown = 90 * time.Second
				if h.config.Failover.ConnectionCooldown > 0 {
					cooldown = h.config.Failover.ConnectionCooldown
				}
			}
		}
		h.endpointManager.SetScopedCooldownFenced(endpointName, endpoint.SoftFailureScopeCountTokens, cooldown,
			endpoint.SoftFailureCooldownReason(category), epoch)
	})
}

func parseCountTokensRetryAfter(resp *http.Response) time.Duration {
	if resp == nil {
		return 0
	}
	value := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if value == "" {
		return 0
	}
	var seconds int
	if _, err := fmt.Sscanf(value, "%d", &seconds); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return 0
}

func writeCountTokensError(w http.ResponseWriter, statusCode int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"type": code, "message": message},
	})
}

func isCountTokensUnsupported(statusCode int, body string) bool {
	if statusCode == http.StatusNotFound || statusCode == http.StatusMethodNotAllowed || statusCode == http.StatusNotImplemented {
		return true
	}
	lower := strings.ToLower(body)
	mentionsCountTokens := strings.Contains(lower, "count_tokens") ||
		strings.Contains(lower, "count tokens") ||
		strings.Contains(lower, "count token") ||
		strings.Contains(lower, "token counting")
	if !mentionsCountTokens {
		return false
	}
	return strings.Contains(lower, "not implemented") ||
		strings.Contains(lower, "unsupported") ||
		strings.Contains(lower, "not supported")
}

// respondWithEstimation 返回本地估算结果（§10.1：估算不是上游 FullSuccess，
// 不清软失败、不更新 retained；reason 只允许白名单值）
func (h *CountTokensHandler) respondWithEstimation(w http.ResponseWriter, bodyBytes []byte, connID, reason string) {
	tokens, err := h.estimateTokens(bodyBytes)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to estimate tokens: %v", err), http.StatusBadRequest)
		return
	}

	response := CountTokensResponse{InputTokens: tokens}
	responseBytes, _ := json.Marshal(response)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Token-Estimation", "true") // 标记这是估算值
	w.Header().Set("X-Token-Estimation-Reason", reason)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(responseBytes)

	slog.Info(fmt.Sprintf("📊 [Token估算] [%s] 估算结果: %d tokens (reason=%s)", connID, tokens, reason))
}

// estimateTokens 本地估算token数量
func (h *CountTokensHandler) estimateTokens(bodyBytes []byte) (int, error) {
	var req CountTokensRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		return 0, fmt.Errorf("invalid request body: %w", err)
	}

	totalChars := 0

	// 统计消息内容
	for _, msg := range req.Messages {
		if content, ok := msg["content"].(string); ok {
			totalChars += utf8.RuneCountInString(content)
		}
	}

	// 统计系统提示
	if req.System != nil {
		switch sys := req.System.(type) {
		case string:
			totalChars += utf8.RuneCountInString(sys)
		case []interface{}:
			for _, item := range sys {
				if str, ok := item.(string); ok {
					totalChars += utf8.RuneCountInString(str)
				}
			}
		}
	}

	// 工具定义开销 (每个工具约100 tokens)
	if len(req.Tools) > 0 {
		totalChars += len(req.Tools) * 400
	}

	// 应用估算比例
	ratio := h.config.TokenCounting.EstimationRatio
	if ratio <= 0 {
		ratio = 4.0
	}
	estimatedTokens := int(float64(totalChars) / ratio)

	// 基础开销
	estimatedTokens += 50

	return estimatedTokens, nil
}
