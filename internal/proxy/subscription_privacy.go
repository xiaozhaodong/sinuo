package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

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
	return handlers.ApplyPrivacyFilter(subscriptionPrivacyFilter{inner: filter}, r, req, body)
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
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := validateSubscriptionJSONValue(decoder); err != nil {
		return err
	}
	if token, err := decoder.Token(); err != io.EOF {
		if err != nil {
			return err
		}
		return fmt.Errorf("unexpected trailing JSON token %v", token)
	}
	return nil
}

func validateSubscriptionJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("object key is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate object key")
			}
			seen[key] = struct{}{}
			if err := validateSubscriptionJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return fmt.Errorf("unterminated object")
		}
	case '[':
		for decoder.More() {
			if err := validateSubscriptionJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return fmt.Errorf("unterminated array")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter")
	}
	return nil
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
// 原本有效的嵌套 JSON 文本必须继续有效，防止自定义替换中的引号破坏工具参数。
func validateSubscriptionPrivacyReplacement(original, rewritten []byte) error {
	if err := validateSubscriptionJSON(rewritten); err != nil {
		return err
	}
	var before, after any
	if err := json.Unmarshal(original, &before); err != nil {
		return err
	}
	if err := json.Unmarshal(rewritten, &after); err != nil {
		return err
	}
	return validateSubscriptionNestedJSON(before, after)
}

func validateSubscriptionNestedJSON(before, after any) error {
	switch value := before.(type) {
	case map[string]any:
		updated, ok := after.(map[string]any)
		if !ok {
			return fmt.Errorf("privacy replacement changed object structure")
		}
		for key, child := range value {
			if err := validateSubscriptionNestedJSON(child, updated[key]); err != nil {
				return err
			}
		}
	case []any:
		updated, ok := after.([]any)
		if !ok || len(updated) != len(value) {
			return fmt.Errorf("privacy replacement changed array structure")
		}
		for index, child := range value {
			if err := validateSubscriptionNestedJSON(child, updated[index]); err != nil {
				return err
			}
		}
	case string:
		updated, ok := after.(string)
		if !ok {
			return fmt.Errorf("privacy replacement changed text structure")
		}
		trimmed := strings.TrimSpace(value)
		if value == updated || len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') || !json.Valid([]byte(value)) {
			return nil
		}
		if err := validateSubscriptionJSONDocument([]byte(updated)); err != nil {
			return fmt.Errorf("privacy replacement invalidated nested JSON")
		}
	}
	return nil
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
