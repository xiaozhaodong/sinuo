package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"sinuo/internal/endpoint"
	"sinuo/internal/privacy"
)

// 隐私策略短路时写入 lifecycle 的失败原因
const (
	PrivacyFailureReasonBlocked    = "privacy_blocked"
	PrivacyFailureReasonScanFailed = "privacy_scan_failed"
)

// PrivacyFilter 出站请求隐私过滤接口（由 service.PrivacyService 实现）。
// Apply 返回的 error 只会是 *privacy.PolicyError，调用方必须用
// AsPrivacyPolicyError 识别并短路返回，不允许进入 retry/failover/冷却。
type PrivacyFilter interface {
	Apply(ctx context.Context, req privacy.Request, body []byte) (privacy.ApplyResult, error)
	SnapshotVersion() int64
}

// PrivacyScopeFingerprinter 可选接口：由过滤器根据当前规则快照返回 scope-aware cache key。
// 不实现时回退到保守的全维度 fingerprint。
type PrivacyScopeFingerprinter interface {
	PrivacyScopeFingerprint(req privacy.Request, snapshotVersion int64) string
}

// privacyStateContextKey 请求级隐私状态 context key
type privacyStateContextKey struct{}

// PrivacyRequestState 请求级 attempt cache。
// 同一 requestID 下同一 scopeFingerprint 重试时复用过滤结果，
// 避免重复扫描和重复命中统计（命中统计只在真实 Apply 时累计）。
type PrivacyRequestState struct {
	RequestID string
	mu        sync.Mutex
	cache     map[string]*privacyCacheEntry
	// resultRecorder 仅在真实 Apply 后触发（cache hit / 关闭 / 跳过不触发）。
	resultRecorder func(privacy.ApplyResult, *privacy.PolicyError)
}

type privacyCacheEntry struct {
	body []byte
	err  error
}

// NewPrivacyRequestState 创建请求级隐私状态
func NewPrivacyRequestState(requestID string) *PrivacyRequestState {
	return &PrivacyRequestState{
		RequestID: requestID,
		cache:     make(map[string]*privacyCacheEntry),
	}
}

// WithPrivacyRequestState 将隐私状态写入 context
func WithPrivacyRequestState(ctx context.Context, state *PrivacyRequestState) context.Context {
	if state == nil {
		return ctx
	}
	return context.WithValue(ctx, privacyStateContextKey{}, state)
}

// PrivacyRequestStateFromContext 从 context 读取隐私状态；缺失时返回 nil（不缓存但仍过滤）
func PrivacyRequestStateFromContext(ctx context.Context) *PrivacyRequestState {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(privacyStateContextKey{}).(*PrivacyRequestState)
	return state
}

func (s *PrivacyRequestState) lookup(fingerprint string) (*privacyCacheEntry, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.cache[fingerprint]
	return entry, ok
}

func (s *PrivacyRequestState) store(fingerprint string, body []byte, err error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache[fingerprint] = &privacyCacheEntry{body: body, err: err}
}

// SetResultRecorder 挂载真实扫描结果的持久化回调（加锁设置）。
func (s *PrivacyRequestState) SetResultRecorder(recorder func(privacy.ApplyResult, *privacy.PolicyError)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resultRecorder = recorder
}

// notifyResult 在锁内复制函数指针后解锁调用，禁止持 state 锁执行 tracker 写入。
func (s *PrivacyRequestState) notifyResult(result privacy.ApplyResult, policyErr *privacy.PolicyError) {
	if s == nil {
		return
	}
	s.mu.Lock()
	recorder := s.resultRecorder
	s.mu.Unlock()
	if recorder != nil {
		recorder(result, policyErr)
	}
}

// privacyScopeFingerprint 计算 attempt cache 键：
// 默认保守包含所有 scope 维度；PrivacyScopeFingerprinter 可在无相关 scope 规则时省略端点/账号维度。
// body hash 用于避免不同端点预处理后 body 不同却误复用缓存。
func privacyScopeFingerprint(filter PrivacyFilter, req privacy.Request, snapshotVersion int64, body []byte) string {
	base := strings.Join([]string{
		req.Path,
		req.UpstreamType,
		req.EndpointName,
		strconv.FormatInt(req.AccountID, 10),
		req.ProviderType,
		strconv.FormatInt(snapshotVersion, 10),
	}, "|")
	if fingerprinter, ok := filter.(PrivacyScopeFingerprinter); ok {
		base = fingerprinter.PrivacyScopeFingerprint(req, snapshotVersion)
	}
	sum := sha256.Sum256(body)
	return base + "|body=" + hex.EncodeToString(sum[:])
}

// ApplyPrivacyFilter 统一过滤入口：带 attempt cache 与命中摘要日志。
// 返回过滤后的 body；返回 *privacy.PolicyError 时调用方必须短路。
func ApplyPrivacyFilter(filter PrivacyFilter, r *http.Request, req privacy.Request, body []byte) ([]byte, error) {
	if filter == nil {
		return body, nil
	}

	var state *PrivacyRequestState
	if r != nil {
		state = PrivacyRequestStateFromContext(r.Context())
	}
	if state != nil && req.RequestID == "" {
		req.RequestID = state.RequestID
	}

	fingerprint := privacyScopeFingerprint(filter, req, filter.SnapshotVersion(), body)
	if entry, ok := state.lookup(fingerprint); ok {
		if entry.err != nil {
			return nil, entry.err
		}
		return entry.body, nil
	}

	ctx := context.Background()
	if r != nil {
		ctx = r.Context()
	}
	result, err := filter.Apply(ctx, req, body)
	logPrivacyApplyResult(req, result, err)
	// 真实 Apply 后通知持久化（cache hit 不经过这里，天然不重复计）。
	if err != nil {
		policyErr := AsPrivacyPolicyError(err)
		state.notifyResult(result, policyErr)
		state.store(fingerprint, nil, err)
		return nil, err
	}
	state.notifyResult(result, nil)
	state.store(fingerprint, result.Body, nil)
	return result.Body, nil
}

// ApplyPrivacyFilterForEndpoint endpoint 链路（常规 + 流式）共用 helper。
// 必须在 prepareBodyForEndpoint 之后调用。
func ApplyPrivacyFilterForEndpoint(filter PrivacyFilter, r *http.Request, body []byte, ep *endpoint.Endpoint) ([]byte, error) {
	if filter == nil || ep == nil || r == nil {
		return body, nil
	}
	req := privacy.Request{
		Path:         r.URL.Path,
		Method:       r.Method,
		UpstreamType: privacy.UpstreamTypeEndpoint,
		EndpointName: ep.Config.Name,
		ContentType:  r.Header.Get("Content-Type"),
	}
	return ApplyPrivacyFilter(filter, r, req, body)
}

// AsPrivacyPolicyError 识别隐私策略短路错误
func AsPrivacyPolicyError(err error) *privacy.PolicyError {
	if err == nil {
		return nil
	}
	var policyErr *privacy.PolicyError
	if errors.As(err, &policyErr) {
		return policyErr
	}
	return nil
}

// PrivacyFailureReason 将策略错误映射为 lifecycle 失败原因
func PrivacyFailureReason(policyErr *privacy.PolicyError) string {
	if policyErr != nil && policyErr.Code == privacy.CodeScanFailed {
		return PrivacyFailureReasonScanFailed
	}
	return PrivacyFailureReasonBlocked
}

// WritePrivacyPolicyErrorResponse 向客户端写出 JSON 策略错误（不含命中原文）
func WritePrivacyPolicyErrorResponse(w http.ResponseWriter, policyErr *privacy.PolicyError) {
	if policyErr == nil {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(policyErr.StatusCode)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"type":    policyErr.Code,
			"message": policyErr.Message,
		},
	})
}

// logPrivacyApplyResult 记录命中摘要：规则名、命中数、耗时、跳过原因。
func logPrivacyApplyResult(req privacy.Request, result privacy.ApplyResult, err error) {
	if err != nil {
		slog.Warn(fmt.Sprintf("🛡️ [隐私保护] [%s] 请求被策略拒绝: %v (path=%s upstream=%s)",
			req.RequestID, err, req.Path, req.UpstreamType))
		return
	}
	if result.Action == privacy.ModeDisabled {
		return
	}
	if result.HitCount > 0 || result.Truncated || result.SkippedReason != "" {
		names := make([]string, 0, len(result.RuleHits))
		for _, hit := range result.RuleHits {
			names = append(names, fmt.Sprintf("%s(%d)", hit.RuleName, hit.Count))
		}
		debugMatches := formatPrivacyDebugMatches(result.RuleHits)
		if debugMatches != "" {
			debugMatches = fmt.Sprintf(" matches=[%s]", debugMatches)
		}
		slog.Info(fmt.Sprintf("🛡️ [隐私保护] [%s] action=%s hits=%d rules=[%s] changed=%t truncated=%t skipped=%q duration=%s path=%s upstream=%s/%s%s",
			req.RequestID, result.Action, result.HitCount, strings.Join(names, ","),
			result.Changed, result.Truncated, result.SkippedReason, result.ScanDuration,
			req.Path, req.UpstreamType, req.EndpointName, debugMatches))
	}
}

func formatPrivacyDebugMatches(hits []privacy.RuleHit) string {
	var parts []string
	for _, hit := range hits {
		for _, match := range hit.Matches {
			parts = append(parts, fmt.Sprintf("%s:%s", hit.RuleName, strconv.Quote(match)))
		}
	}
	return strings.Join(parts, ",")
}

// privacyScanRuleJSON 隐私扫描摘要中的单条规则（只有规则名/计数，绝无命中原文）。
type privacyScanRuleJSON struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// BuildPrivacyScanJSON 按白名单构造隐私扫描摘要 JSON。
// 语义为「最后一次真实扫描的结果」；action=disabled / 模式关闭时返回空串，调用方跳过写入。
// PolicyError 短路时同样写入（action=blocked + blocked_code，取实际 PolicyError.Code）。
func BuildPrivacyScanJSON(result privacy.ApplyResult, policyErr *privacy.PolicyError, scanCount int) string {
	if scanCount <= 0 {
		return ""
	}
	if policyErr == nil && (result.Action == "" || result.Action == privacy.ModeDisabled) {
		return ""
	}

	summary := struct {
		Action      string                `json:"action"`
		HitCount    int                   `json:"hit_count"`
		Rules       []privacyScanRuleJSON `json:"rules"`
		Truncated   bool                  `json:"truncated"`
		ScanMs      int64                 `json:"scan_ms"`
		ScanCount   int                   `json:"scan_count"`
		BlockedCode string                `json:"blocked_code,omitempty"`
	}{
		Action:    result.Action,
		HitCount:  result.HitCount,
		Rules:     make([]privacyScanRuleJSON, 0, len(result.RuleHits)),
		Truncated: result.Truncated,
		ScanMs:    result.ScanDuration.Milliseconds(),
		ScanCount: scanCount,
	}
	if policyErr != nil {
		summary.Action = "blocked"
		summary.BlockedCode = policyErr.Code
		if policyErr.Result.HitCount > 0 {
			summary.HitCount = policyErr.Result.HitCount
		}
	}
	for _, hit := range result.RuleHits {
		summary.Rules = append(summary.Rules, privacyScanRuleJSON{Name: hit.RuleName, Count: hit.Count})
	}

	payload, err := json.Marshal(summary)
	if err != nil {
		return ""
	}
	return string(payload)
}
