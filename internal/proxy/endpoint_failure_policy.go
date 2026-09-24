package proxy

import (
	"net/http"
	"strings"
	"time"

	"cc-forwarder/config"
	"cc-forwarder/internal/endpoint"
	"cc-forwarder/internal/proxy/handlers"
)

// 端点侧统一故障决策表（收敛方案 §9.1）。
// 纯决策，不执行任何副作用；标记与写回由转发管线按 Mark/Category 执行。

// EndpointForwardAction 决策动作
type EndpointForwardAction int

const (
	// EndpointForwardProcess 2xx/3xx：进入响应处理阶段
	EndpointForwardProcess EndpointForwardAction = iota
	// EndpointForwardNextCandidate 重放安全硬失败：换下一候选
	EndpointForwardNextCandidate
	// EndpointForwardPassthroughError 歧义失败/5xx：以错误语义回客户端
	//（「真实码→500+Retry-After」转换仅发生在此动作的写回点）
	EndpointForwardPassthroughError
	// EndpointForwardPassthroughRaw 其余 4xx：原样透传上游响应
	EndpointForwardPassthroughRaw
	// EndpointForwardRateLimited 普通 429：由管线执行同端点短重试 /
	// 软失败结算 / 阈值触发换候选（§9.2）
	EndpointForwardRateLimited
)

// EndpointFailureMark 端点状态标记类别
type EndpointFailureMark int

const (
	// EndpointMarkNone 不记录
	EndpointMarkNone EndpointFailureMark = iota
	// EndpointMarkSoftFailure 分类软失败记 1 次；达阈值进入类别 cooldown（§9.3）
	EndpointMarkSoftFailure
	// EndpointMarkAuthCooldown 鉴权类长冷却（默认 30m）
	EndpointMarkAuthCooldown
	// EndpointMarkNegativeCache 写路由负缓存（FailureClass），不计软失败
	EndpointMarkNegativeCache
)

// EndpointFailureDecision 一次转发结果的决策
type EndpointFailureDecision struct {
	Action       EndpointForwardAction
	Mark         EndpointFailureMark
	Category     endpoint.SoftFailureCategory // Mark 为 SoftFailure 时有效
	FailureClass handlers.FailureClass        // Mark 为 NegativeCache 时有效
	RetryAfter   time.Duration                // 429 时解析的 Retry-After（0 = 未提供）
	Reason       string
}

// decideEndpointForwardOutcome 按 §9.1 决策表对一次端点转发结果分类。
//   - forwardErr 非 nil 时为 P0 连接阶段：以 trace.WroteHeaders 分界重放安全/歧义
//   - resp 非 nil 时为 P1 响应头阶段：按状态码区分「确定未执行」与「歧义」
//   - respBodySample 供 4xx 的模型不支持 / schema 不兼容文本判定（调用方窥读并复原 body）
func decideEndpointForwardOutcome(forwardErr error, resp *http.Response, trace *handlers.UpstreamTraceState, respBodySample string) EndpointFailureDecision {
	if forwardErr != nil {
		if !trace.WroteHeaders() {
			return EndpointFailureDecision{
				Action:   EndpointForwardNextCandidate,
				Mark:     EndpointMarkSoftFailure,
				Category: endpoint.SoftFailureCategoryConnection,
				Reason:   FailoverReasonConnectionFailedBeforeHeaders,
			}
		}
		return EndpointFailureDecision{
			Action:   EndpointForwardPassthroughError,
			Mark:     EndpointMarkSoftFailure,
			Category: endpoint.SoftFailureCategoryTransport,
			Reason:   "ambiguous_failure_after_wrote_headers",
		}
	}

	if resp == nil {
		// 无错误也无响应：按歧义失败保守处理
		return EndpointFailureDecision{
			Action:   EndpointForwardPassthroughError,
			Mark:     EndpointMarkSoftFailure,
			Category: endpoint.SoftFailureCategoryTransport,
			Reason:   FailoverReasonEmptyResponse,
		}
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return EndpointFailureDecision{
			Action: EndpointForwardNextCandidate,
			Mark:   EndpointMarkAuthCooldown,
			Reason: FailoverReasonAuthRejected,
		}
	case resp.StatusCode == http.StatusForbidden:
		if handlers.IsModelUnsupportedError(respBodySample) {
			return EndpointFailureDecision{
				Action:       EndpointForwardNextCandidate,
				Mark:         EndpointMarkNegativeCache,
				FailureClass: handlers.FailureClassModelUnsupported,
				Reason:       FailoverReasonModelUnsupported,
			}
		}
		return EndpointFailureDecision{
			Action: EndpointForwardNextCandidate,
			Mark:   EndpointMarkAuthCooldown,
			Reason: FailoverReasonAuthRejected,
		}
	case resp.StatusCode == http.StatusTooManyRequests:
		return EndpointFailureDecision{
			Action:     EndpointForwardRateLimited,
			Mark:       EndpointMarkSoftFailure,
			Category:   endpoint.SoftFailureCategoryRateLimit,
			RetryAfter: parseAccountRetryAfter(resp),
			Reason:     FailoverReasonRateLimited,
		}
	case resp.StatusCode == http.StatusRequestEntityTooLarge:
		return EndpointFailureDecision{
			Action:       EndpointForwardNextCandidate,
			Mark:         EndpointMarkNegativeCache,
			FailureClass: handlers.FailureClassPayloadTooLarge,
			Reason:       FailoverReasonPayloadTooLarge,
		}
	case resp.StatusCode >= http.StatusInternalServerError:
		return EndpointFailureDecision{
			Action:   EndpointForwardPassthroughError,
			Mark:     EndpointMarkSoftFailure,
			Category: endpoint.SoftFailureCategoryServerError,
			Reason:   FailoverReasonServerError,
		}
	case resp.StatusCode >= http.StatusBadRequest:
		lowerBody := normalizeEndpointFailureBody(respBodySample)
		if handlers.IsModelUnsupportedError(lowerBody) {
			return EndpointFailureDecision{
				Action:       EndpointForwardNextCandidate,
				Mark:         EndpointMarkNegativeCache,
				FailureClass: handlers.FailureClassModelUnsupported,
				Reason:       FailoverReasonModelUnsupported,
			}
		}
		if isSchemaIncompatibleError(lowerBody) {
			return EndpointFailureDecision{
				Action:       EndpointForwardNextCandidate,
				Mark:         EndpointMarkNegativeCache,
				FailureClass: handlers.FailureClassSchemaIncompatible,
				Reason:       FailoverReasonSchemaIncompatible,
			}
		}
		return EndpointFailureDecision{
			Action: EndpointForwardPassthroughRaw,
			Mark:   EndpointMarkNone,
			Reason: "client_error_passthrough",
		}
	default:
		return EndpointFailureDecision{
			Action: EndpointForwardProcess,
			Mark:   EndpointMarkNone,
			Reason: "process_response",
		}
	}
}

// endpointSoftFailureCooldown 各类别阈值触发后的 cooldown 时长（§12 配置）。
// rate_limit 优先尊重最后一次有效 Retry-After（§9.2 规则 10）。
func endpointSoftFailureCooldown(cfg *config.Config, category endpoint.SoftFailureCategory, retryAfter time.Duration) time.Duration {
	switch category {
	case endpoint.SoftFailureCategoryRateLimit:
		if retryAfter > 0 {
			return retryAfter
		}
		if cfg.Failover.RateLimitRetry.DefaultCooldown > 0 {
			return cfg.Failover.RateLimitRetry.DefaultCooldown
		}
		return 180 * time.Second
	case endpoint.SoftFailureCategoryServerError:
		if cfg.Failover.ServerErrorCooldown > 0 {
			return cfg.Failover.ServerErrorCooldown
		}
		return 120 * time.Second
	default: // connection / transport
		if cfg.Failover.ConnectionCooldown > 0 {
			return cfg.Failover.ConnectionCooldown
		}
		return 90 * time.Second
	}
}

func normalizeEndpointFailureBody(body string) string {
	return strings.ToLower(body)
}

// isSchemaIncompatibleError 判定错误文本是否为「schema 不兼容」（原 retry_manager 分类逻辑并入）
func isSchemaIncompatibleError(errText string) bool {
	if errText == "" {
		return false
	}
	return strings.Contains(errText, "extra inputs are not permitted") ||
		strings.Contains(errText, "context_management") ||
		strings.Contains(errText, "cache_control.scope") ||
		(strings.Contains(errText, "anthropic-beta") && strings.Contains(errText, "not supported")) ||
		strings.Contains(errText, "field not supported") ||
		strings.Contains(errText, "字段不支持") ||
		strings.Contains(errText, "参数不允许")
}
