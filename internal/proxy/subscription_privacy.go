package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"cc-forwarder/internal/codexidentity"
	"cc-forwarder/internal/privacy"
	"cc-forwarder/internal/proxy/handlers"
)

const subscriptionInvalidJSONCode = "codex_subscription_invalid_json"

// subscriptionPrivacyResultObserver 是 P2b 的显式接缝：只接收安全摘要，绝不接收正文或 Matches。
type subscriptionPrivacyResultObserver func(requestID string, result privacy.ApplyResult, policyErr *privacy.PolicyError)

func newSubscriptionPrivacyRequestState(requestID string, observer subscriptionPrivacyResultObserver) *handlers.PrivacyRequestState {
	state := handlers.NewPrivacyRequestState(requestID)
	if observer != nil {
		state.SetResultRecorder(func(result privacy.ApplyResult, policyErr *privacy.PolicyError) {
			observer(requestID, subscriptionPrivacySummary(result), sanitizeSubscriptionPolicyError(policyErr))
		})
	}
	return state
}

func withSubscriptionPrivacyState(r *http.Request, state *handlers.PrivacyRequestState) *http.Request {
	if r == nil || state == nil {
		return r
	}
	return r.WithContext(handlers.WithPrivacyRequestState(r.Context(), state))
}

// applySubscriptionPrivacy 复用共享三档策略、scope 和 attempt cache。
// 调用方必须在正文规范化后、出站压缩前调用；PolicyError 时返回 nil 正文。
func applySubscriptionPrivacy(filter handlers.PrivacyFilter, r *http.Request, path string, body []byte) ([]byte, error) {
	if !isSubscriptionPrivacyPath(path) {
		return body, nil
	}
	if err := validateSubscriptionJSON(body); err != nil {
		return nil, &privacy.PolicyError{
			StatusCode: http.StatusBadRequest,
			Code:       subscriptionInvalidJSONCode,
			Message:    "request body contains invalid or ambiguous JSON",
		}
	}
	if filter == nil {
		return body, nil
	}
	req := privacy.Request{
		Path:         path,
		Method:       http.MethodPost,
		UpstreamType: privacy.UpstreamTypeSubscription,
		ContentType:  "application/json",
	}
	if r != nil {
		req.Method = r.Method
		req.ContentType = r.Header.Get("Content-Type")
	}
	if _, wrapped := filter.(subscriptionPrivacyFilter); !wrapped {
		filter = subscriptionPrivacyFilter{inner: filter}
	}
	return handlers.ApplyPrivacyFilter(filter, r, req, body)
}

// validateSubscriptionJSON 仅校验结构，不重编码正文，因此大整数与未命中字节保持不变。
// 每个对象独立拒绝重复键，避免 type/generate/input 等歧义字段被不同解析器作不同解释。
func validateSubscriptionJSON(body []byte) error {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return fmt.Errorf("request body must be a JSON object")
	}
	return validateSubscriptionJSONDocument(body)
}

func validateSubscriptionJSONDocument(body []byte) error {
	return codexidentity.ValidateJSON(body)
}

func isSubscriptionPrivacyPath(path string) bool {
	switch path {
	case "/v1/responses", "/v1/responses/compact", "/v1/alpha/search":
		return true
	default:
		return false
	}
}

// subscriptionPrivacyFilter 在共享 helper 记录日志和缓存前移除 Matches 与内部错误细节。
// PrivacyScopeFingerprint 仍原样转发，缓存键语义不变。
type subscriptionPrivacyFilter struct {
	inner handlers.PrivacyFilter
}

func (f subscriptionPrivacyFilter) Apply(ctx context.Context, req privacy.Request, body []byte) (privacy.ApplyResult, error) {
	_, poolRequest := subscriptionPoolMetadataFromContext(ctx)
	if req.UpstreamType != privacy.UpstreamTypeSubscription && !poolRequest {
		return f.inner.Apply(ctx, req, body)
	}
	result, err := f.inner.Apply(ctx, req, body)
	result = sanitizeSubscriptionPrivacyResult(result)
	if err == nil && !bytes.Equal(body, result.Body) {
		if validationErr := validateSubscriptionPrivacyReplacement(body, result.Body); validationErr != nil {
			result.Body = nil
			return result, &privacy.PolicyError{
				StatusCode: http.StatusUnprocessableEntity,
				Code:       privacy.CodeScanFailed,
				Message:    "privacy output contains invalid or ambiguous JSON",
				Result:     subscriptionPrivacySummary(result),
			}
		}
	}
	if err == nil {
		return result, nil
	}
	var policyErr *privacy.PolicyError
	if errors.As(err, &policyErr) {
		return result, sanitizeSubscriptionPolicyError(policyErr)
	}
	// PrivacyFilter 契约只允许 PolicyError；实现违约时也不能把原始 error 写入日志或响应。
	return result, &privacy.PolicyError{
		StatusCode: http.StatusUnprocessableEntity,
		Code:       privacy.CodeScanFailed,
		Message:    "privacy scan failed and the request was rejected",
		Result:     result,
	}
}

// validateSubscriptionPrivacyReplacement 只在正文变化时复验，不重编码发送正文。
// 工具回放中原本有效的嵌套 JSON 必须继续有效；普通消息/搜索文本不强制 JSON 语义。
func validateSubscriptionPrivacyReplacement(original, rewritten []byte) error {
	if err := validateSubscriptionJSON(rewritten); err != nil {
		return err
	}
	return validateSubscriptionReplayItems(original, rewritten)
}

func (f subscriptionPrivacyFilter) SnapshotVersion() int64 {
	if f.inner == nil {
		return 0
	}
	return f.inner.SnapshotVersion()
}

func (f subscriptionPrivacyFilter) PrivacyScopeFingerprint(req privacy.Request, snapshotVersion int64) string {
	if scoped, ok := f.inner.(handlers.PrivacyScopeFingerprinter); ok {
		return scoped.PrivacyScopeFingerprint(req, snapshotVersion)
	}
	// 保守键包含全部 scope 维度，与共享 helper 的默认语义一致。
	return strings.Join([]string{
		req.Path,
		req.UpstreamType,
		req.EndpointName,
		strconv.FormatInt(req.AccountID, 10),
		req.ProviderType,
		strconv.FormatInt(snapshotVersion, 10),
	}, "|")
}

func sanitizeSubscriptionPrivacyResult(result privacy.ApplyResult) privacy.ApplyResult {
	if len(result.RuleHits) > 0 {
		hits := make([]privacy.RuleHit, len(result.RuleHits))
		copy(hits, result.RuleHits)
		for i := range hits {
			hits[i].Matches = nil
		}
		result.RuleHits = hits
	}
	if strings.HasPrefix(result.SkippedReason, "scan_error:") {
		result.SkippedReason = "privacy_scan_failed_fail_open"
	}
	return result
}

func subscriptionPrivacySummary(result privacy.ApplyResult) privacy.ApplyResult {
	result = sanitizeSubscriptionPrivacyResult(result)
	result.Body = nil
	return result
}

func sanitizeSubscriptionPolicyError(policyErr *privacy.PolicyError) *privacy.PolicyError {
	if policyErr == nil {
		return nil
	}
	result := subscriptionPrivacySummary(policyErr.Result)
	message := "privacy policy rejected the request"
	switch policyErr.Code {
	case privacy.CodeScanBodyTooLarge:
		message = "scannable text exceeds the configured limit and the request was rejected"
	case privacy.CodeScanFailed:
		message = "privacy scan failed and the request was rejected"
	}
	return &privacy.PolicyError{
		StatusCode: policyErr.StatusCode,
		Code:       policyErr.Code,
		Message:    message,
		Result:     result,
	}
}
