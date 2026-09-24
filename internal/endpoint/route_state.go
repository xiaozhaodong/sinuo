package endpoint

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	FailureClassNone                   = ""
	FailureClassClientCancel           = "client_cancel"
	FailureClassModelUnsupported       = "model_unsupported"
	FailureClassSchemaIncompatible     = "schema_incompatible"
	FailureClassPayloadTooLarge        = "payload_too_large"
	FailureClassCountTokensUnsupported = "count_tokens_unsupported"
)

type RouteRequestProfile struct {
	Path          string
	Model         string
	BodySize      int
	SchemaFields  []string
	IsCountTokens bool
	// ServerToolTypes 是 tools[] 中所有带 type 的 server tool 类型（如 web_search_20260209），
	// 供日志诊断请求到底带了什么工具；WebTools 由其中 web_search_ / web_fetch_ 前缀推出，
	// 模型路由据此优先命中「联网工具」映射。
	ServerToolTypes []string
	WebTools        bool
	// ClaudeCodeEntry 请求来自 Claude Code 专用 loopback 入口；只有这类请求能调度到订阅直通端点
	ClaudeCodeEntry bool
}

type RouteBlock struct {
	StatusCode int
	Code       string
	Message    string
	Endpoint   string
	Reason     string
	RetryAfter int
}

type RouteState struct {
	negativeHits *NegativeHitCache
}

func NewRouteState() *RouteState {
	return &RouteState{
		negativeHits: NewNegativeHitCache(30*time.Minute, 1024),
	}
}

type NegativeHitCache struct {
	ttl      time.Duration
	capacity int

	mu                    sync.Mutex
	unsupportedModel      map[string]time.Time
	unsupportedSchema     map[string]time.Time
	rejectedBodySize      map[string]time.Time
	unsupportedCountToken map[string]time.Time
	order                 map[string][]string
}

func NewNegativeHitCache(ttl time.Duration, capacity int) *NegativeHitCache {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	if capacity <= 0 {
		capacity = 1024
	}
	return &NegativeHitCache{
		ttl:                   ttl,
		capacity:              capacity,
		unsupportedModel:      make(map[string]time.Time),
		unsupportedSchema:     make(map[string]time.Time),
		rejectedBodySize:      make(map[string]time.Time),
		unsupportedCountToken: make(map[string]time.Time),
		order:                 make(map[string][]string),
	}
}

func BuildRouteRequestProfile(path string, bodyBytes []byte) RouteRequestProfile {
	profile := RouteRequestProfile{
		Path:          path,
		BodySize:      len(bodyBytes),
		IsCountTokens: strings.Contains(path, "/count_tokens"),
	}

	var payload map[string]interface{}
	if len(bodyBytes) > 0 && json.Unmarshal(bodyBytes, &payload) == nil {
		if model, ok := payload["model"].(string); ok {
			profile.Model = strings.TrimSpace(model)
		}
		profile.SchemaFields = extractSchemaFields(payload)
		profile.ServerToolTypes = extractServerToolTypes(payload)
		profile.WebTools = hasWebServerTools(profile.ServerToolTypes)
	}

	return profile
}

// webServerToolTypePrefixes 覆盖 web_search_20250305 / web_search_20260209 /
// web_fetch_20250910 / web_fetch_20260209 等所有日期后缀变体；只认 type，不认可改的 name。
var webServerToolTypePrefixes = []string{"web_search_", "web_fetch_"}

// extractServerToolTypes 收集 tools[] 里带 type 的工具类型（去重、小写）。
// 普通函数工具没有 type 或 type=custom，不计入。
func extractServerToolTypes(payload map[string]interface{}) []string {
	tools, ok := payload["tools"].([]interface{})
	if !ok {
		return nil
	}
	seen := make(map[string]struct{})
	types := make([]string, 0)
	for _, raw := range tools {
		tool, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		toolType, ok := tool["type"].(string)
		if !ok {
			continue
		}
		toolType = strings.ToLower(strings.TrimSpace(toolType))
		if toolType == "" || toolType == "custom" {
			continue
		}
		if _, duplicated := seen[toolType]; duplicated {
			continue
		}
		seen[toolType] = struct{}{}
		types = append(types, toolType)
	}
	return types
}

func hasWebServerTools(serverToolTypes []string) bool {
	for _, toolType := range serverToolTypes {
		for _, prefix := range webServerToolTypePrefixes {
			if strings.HasPrefix(toolType, prefix) {
				return true
			}
		}
	}
	return false
}

func extractSchemaFields(payload map[string]interface{}) []string {
	fields := make(map[string]struct{})
	if _, ok := payload["context_management"]; ok {
		fields["context_management"] = struct{}{}
	}
	if hasCacheControlScope(payload) {
		fields["cache_control.scope"] = struct{}{}
	}
	if _, ok := payload["anthropic_beta"]; ok {
		fields["anthropic_beta"] = struct{}{}
	}
	if _, ok := payload["anthropic-beta"]; ok {
		fields["anthropic-beta"] = struct{}{}
	}

	result := make([]string, 0, len(fields))
	for field := range fields {
		result = append(result, field)
	}
	sort.Strings(result)
	return result
}

func hasCacheControlScope(value interface{}) bool {
	switch typed := value.(type) {
	case map[string]interface{}:
		if cacheControl, ok := typed["cache_control"].(map[string]interface{}); ok {
			if _, exists := cacheControl["scope"]; exists {
				return true
			}
		}
		for _, nested := range typed {
			if hasCacheControlScope(nested) {
				return true
			}
		}
	case []interface{}:
		for _, nested := range typed {
			if hasCacheControlScope(nested) {
				return true
			}
		}
	}
	return false
}

func (rs *RouteState) RecordNegativeHit(endpointName, failureClass string, profile RouteRequestProfile, detail string) {
	if rs == nil || rs.negativeHits == nil || endpointName == "" {
		return
	}
	rs.negativeHits.Record(endpointName, failureClass, profile, detail)
}

func (rs *RouteState) HasNegativeHit(endpointName string, profile RouteRequestProfile) (bool, string) {
	if rs == nil || rs.negativeHits == nil || endpointName == "" {
		return false, ""
	}
	return rs.negativeHits.Has(endpointName, profile)
}

// NegativeHitWithExpiry 同 HasNegativeHit，并返回命中条目的过期时间（availableAt 来源）
func (rs *RouteState) NegativeHitWithExpiry(endpointName string, profile RouteRequestProfile) (bool, string, time.Time) {
	if rs == nil || rs.negativeHits == nil || endpointName == "" {
		return false, "", time.Time{}
	}
	return rs.negativeHits.HasWithExpiry(endpointName, profile)
}

func (rs *RouteState) ClearNegativeHits(endpointName string) {
	if rs == nil || rs.negativeHits == nil {
		return
	}
	rs.negativeHits.Clear(endpointName)
}

func (m *Manager) RecordNegativeRouteHit(endpointName, failureClass string, profile RouteRequestProfile, detail string) {
	m.routeState.RecordNegativeHit(endpointName, failureClass, profile, detail)
}

func (m *Manager) ClearNegativeHitCache(endpointName string) {
	m.routeState.ClearNegativeHits(endpointName)
}

// GetManualFixedRouteBlock 保留旧调用名，实际按请求模型解析完整作用域。
func (m *Manager) GetManualFixedRouteBlock(profile RouteRequestProfile) *RouteBlock {
	return m.GetRouteBlock(profile)
}

// GetRouteBlock 返回固定作用域不可路由时的明确错误。
// Claude 路由尚未完成启动加载时保持 nil，由上层统一返回 routing_not_ready。
func (m *Manager) GetRouteBlock(profile RouteRequestProfile) *RouteBlock {
	if m == nil || !m.IsClaudeRoutingReady() {
		return nil
	}
	scope := m.ResolveRouteScope(profile)
	if scope.Kind == RouteScopeUnassigned {
		code := "model_routing_unassigned"
		message := fmt.Sprintf("请求模型 %q 未分配上游组，请在模型路由面板分配上游组。", scope.Model)
		switch scope.UnassignedReason {
		case "model_routing_missing_model":
			code = "model_routing_missing_model"
			message = "The request model is required when Claude model routing is enabled."
		case "model_routing_load_failed":
			// fail-closed：配置坏了不代表可以放行到全局端点，
			// 但要让用户看到是配置问题而不是漏了分配。
			code = "model_routing_load_failed"
			message = "模型路由配置加载失败，已停止转发以避免绕过模型隔离。请在模型路由面板检查配置。"
		}
		// Token 计数关闭时 count_tokens 走普通透传管线，没有本地估算出口；
		// 未分配模型同样 fail-closed 返回 422，提示用户开启 Token 计数即可降级为估算。
		if profile.IsCountTokens {
			message += " 开启「Token 计数」后，count_tokens 会对未分配模型降级为本地估算。"
		}
		return &RouteBlock{StatusCode: http.StatusUnprocessableEntity, Code: code, Message: message, Reason: scope.UnassignedReason}
	}
	if scope.Mode != RouteModeManualFixed || scope.Target == "" {
		return nil
	}
	if scope.Kind == RouteScopeUpstreamGroup && !slices.Contains(scope.Members, scope.Target) {
		return &RouteBlock{
			StatusCode: http.StatusUnprocessableEntity,
			Code:       "endpoint_not_in_model_group",
			Message:    "Manual fixed endpoint is not a member of the model upstream group.",
			Endpoint:   scope.Target,
			Reason:     "manual_fixed_target_not_member",
		}
	}

	ep := m.GetEndpointByNameAny(scope.Target)
	if ep == nil {
		return &RouteBlock{
			StatusCode: http.StatusNotFound,
			Code:       "endpoint_not_found",
			Message:    "Manual fixed endpoint is not available.",
			Endpoint:   scope.Target,
			Reason:     "manual_fixed_endpoint_missing",
		}
	}
	ep.mutex.RLock()
	hardEnabled := ep.Config.IsAvailabilityEnabled()
	ep.mutex.RUnlock()
	if !hardEnabled {
		return &RouteBlock{
			StatusCode: http.StatusNotFound,
			Code:       "endpoint_not_found",
			Message:    "Manual fixed endpoint is disabled.",
			Endpoint:   scope.Target,
			Reason:     "manual_fixed_endpoint_disabled",
		}
	}
	if !endpointServesEntry(ep, profile) {
		return &RouteBlock{
			StatusCode: http.StatusUnprocessableEntity,
			Code:       routeSkipClientOAuthOutsideEntry,
			Message:    "手动固定的是 Claude Code 订阅直通端点，只接受 Claude Code 专用入口的请求。",
			Endpoint:   scope.Target,
			Reason:     "manual_fixed_" + routeSkipClientOAuthOutsideEntry,
		}
	}
	if hit, reason := m.routeState.HasNegativeHit(scope.Target, profile); hit {
		return &RouteBlock{
			StatusCode: http.StatusUnprocessableEntity,
			Code:       "endpoint_capability_mismatch",
			Message:    "Manual fixed endpoint is known to be incompatible with this request.",
			Endpoint:   scope.Target,
			Reason:     reason,
		}
	}
	// 复用统一的 path-aware 可路由判定：messages cooldown 不阻断 count_tokens，
	// count_tokens scoped cooldown 也必须得到明确的 manual-fixed RouteBlock。
	if routable, reason, availableAt := m.classifyEndpointRoutable(ep, profile); !routable {
		return &RouteBlock{
			StatusCode: http.StatusServiceUnavailable,
			Code:       "route_blocked_manual_fixed",
			Message:    "Manual fixed endpoint is temporarily unavailable and fallback is disabled.",
			Endpoint:   scope.Target,
			Reason:     reason,
			RetryAfter: retryAfterFromDuration(time.Until(availableAt)),
		}
	}
	return nil
}

func retryAfterFromDuration(duration time.Duration) int {
	if duration <= 0 {
		return 10
	}
	seconds := int(duration.Seconds())
	if seconds < 1 {
		return 1
	}
	return seconds
}

func (c *NegativeHitCache) Record(endpointName, failureClass string, profile RouteRequestProfile, detail string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	expiresAt := time.Now().Add(c.ttl)
	switch failureClass {
	case FailureClassModelUnsupported:
		if profile.Model != "" {
			c.put(c.unsupportedModel, "model", endpointName+"|"+strings.ToLower(profile.Model), expiresAt)
		}
	case FailureClassSchemaIncompatible:
		field := inferSchemaField(detail, profile.SchemaFields)
		if field != "" {
			c.put(c.unsupportedSchema, "schema", endpointName+"|"+field, expiresAt)
		}
	case FailureClassPayloadTooLarge:
		if bucket := bodySizeBucket(profile.BodySize); bucket > 0 {
			c.put(c.rejectedBodySize, "body", endpointName+"|"+bucketKey(bucket), expiresAt)
		}
	case FailureClassCountTokensUnsupported:
		c.put(c.unsupportedCountToken, "count_tokens", endpointName, expiresAt)
	}
}

func (c *NegativeHitCache) Has(endpointName string, profile RouteRequestProfile) (bool, string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	if profile.IsCountTokens {
		if c.hit(c.unsupportedCountToken, endpointName, now) {
			return true, FailureClassCountTokensUnsupported
		}
	}
	if profile.Model != "" {
		if c.hit(c.unsupportedModel, endpointName+"|"+strings.ToLower(profile.Model), now) {
			return true, FailureClassModelUnsupported
		}
	}
	for _, field := range profile.SchemaFields {
		if c.hit(c.unsupportedSchema, endpointName+"|"+field, now) {
			return true, FailureClassSchemaIncompatible
		}
	}
	if profile.BodySize > 0 {
		requestBucket := bodySizeBucket(profile.BodySize)
		for _, bucket := range bodyBuckets() {
			if bucket > requestBucket {
				continue
			}
			if c.hit(c.rejectedBodySize, endpointName+"|"+bucketKey(bucket), now) {
				return true, FailureClassPayloadTooLarge
			}
		}
	}
	return false, ""
}

func (c *NegativeHitCache) Clear(endpointName string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if endpointName == "" {
		clear(c.unsupportedModel)
		clear(c.unsupportedSchema)
		clear(c.rejectedBodySize)
		clear(c.unsupportedCountToken)
		clear(c.order)
		return
	}

	clearByPrefix(c.unsupportedModel, endpointName+"|")
	clearByPrefix(c.unsupportedSchema, endpointName+"|")
	clearByPrefix(c.rejectedBodySize, endpointName+"|")
	delete(c.unsupportedCountToken, endpointName)
}

func (c *NegativeHitCache) put(target map[string]time.Time, bucketName, key string, expiresAt time.Time) {
	target[key] = expiresAt
	c.order[bucketName] = append(removeOrderKey(c.order[bucketName], key), key)
	c.trim(target, bucketName)
}

func (c *NegativeHitCache) trim(target map[string]time.Time, bucketName string) {
	order := c.order[bucketName]
	for len(target) > c.capacity && len(order) > 0 {
		key := order[0]
		order = order[1:]
		if _, ok := target[key]; !ok {
			continue
		}
		delete(target, key)
	}
	c.order[bucketName] = order
}

func removeOrderKey(order []string, key string) []string {
	if len(order) == 0 {
		return order
	}
	write := 0
	for _, item := range order {
		if item == key {
			continue
		}
		order[write] = item
		write++
	}
	return order[:write]
}

func (c *NegativeHitCache) hit(target map[string]time.Time, key string, now time.Time) bool {
	ok, _ := c.hitWithExpiry(target, key, now)
	return ok
}

func (c *NegativeHitCache) hitWithExpiry(target map[string]time.Time, key string, now time.Time) (bool, time.Time) {
	expiresAt, ok := target[key]
	if !ok {
		return false, time.Time{}
	}
	if now.After(expiresAt) {
		delete(target, key)
		return false, time.Time{}
	}
	return true, expiresAt
}

// HasWithExpiry 同 Has，并返回命中条目的过期时间
func (c *NegativeHitCache) HasWithExpiry(endpointName string, profile RouteRequestProfile) (bool, string, time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	if profile.IsCountTokens {
		if ok, expiresAt := c.hitWithExpiry(c.unsupportedCountToken, endpointName, now); ok {
			return true, FailureClassCountTokensUnsupported, expiresAt
		}
	}
	if profile.Model != "" {
		if ok, expiresAt := c.hitWithExpiry(c.unsupportedModel, endpointName+"|"+strings.ToLower(profile.Model), now); ok {
			return true, FailureClassModelUnsupported, expiresAt
		}
	}
	for _, field := range profile.SchemaFields {
		if ok, expiresAt := c.hitWithExpiry(c.unsupportedSchema, endpointName+"|"+field, now); ok {
			return true, FailureClassSchemaIncompatible, expiresAt
		}
	}
	if profile.BodySize > 0 {
		requestBucket := bodySizeBucket(profile.BodySize)
		for _, bucket := range bodyBuckets() {
			if bucket > requestBucket {
				continue
			}
			if ok, expiresAt := c.hitWithExpiry(c.rejectedBodySize, endpointName+"|"+bucketKey(bucket), now); ok {
				return true, FailureClassPayloadTooLarge, expiresAt
			}
		}
	}
	return false, "", time.Time{}
}

func clearByPrefix(target map[string]time.Time, prefix string) {
	for key := range target {
		if strings.HasPrefix(key, prefix) {
			delete(target, key)
		}
	}
}

func inferSchemaField(detail string, fallback []string) string {
	lower := strings.ToLower(detail)
	candidates := []string{
		"context_management",
		"cache_control.scope",
		"anthropic_beta",
		"anthropic-beta",
	}
	for _, candidate := range candidates {
		if strings.Contains(lower, strings.ToLower(candidate)) {
			return candidate
		}
	}
	if len(fallback) > 0 {
		return fallback[0]
	}
	return ""
}

func bodyBuckets() []int {
	return []int{
		256 * 1024,
		512 * 1024,
		1024 * 1024,
		2 * 1024 * 1024,
		4 * 1024 * 1024,
		8 * 1024 * 1024,
		16 * 1024 * 1024,
	}
}

func bodySizeBucket(size int) int {
	if size <= 0 {
		return 0
	}
	for _, bucket := range bodyBuckets() {
		if size <= bucket {
			return bucket
		}
	}
	nextPower := math.Pow(2, math.Ceil(math.Log2(float64(size))))
	return int(nextPower)
}

func bucketKey(bucket int) string {
	return strconv.Itoa(bucket)
}
