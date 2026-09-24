package endpoint

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// Claude Code 入口的生效模式：只约束经入口进来的请求，主端口请求不受影响
const (
	// ClaudeCodeEntryModeAuto 按模型路由在订阅直通与端点凭据端点之间选择
	ClaudeCodeEntryModeAuto = "auto"
	// ClaudeCodeEntryModeSubscription 只用订阅直通端点
	ClaudeCodeEntryModeSubscription = "subscription"
	// ClaudeCodeEntryModeAPI 只用端点凭据端点（第三方），相当于 Codex 的池模式
	ClaudeCodeEntryModeAPI = "api"
)

// NormalizeClaudeCodeEntryMode 去空白、转小写，空值归一为 auto；非法值原样返回，由调用方校验
func NormalizeClaudeCodeEntryMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return ClaudeCodeEntryModeAuto
	}
	return mode
}

// IsValidClaudeCodeEntryMode 是否为已知模式（需先归一化）
func IsValidClaudeCodeEntryMode(mode string) bool {
	switch mode {
	case ClaudeCodeEntryModeAuto, ClaudeCodeEntryModeSubscription, ClaudeCodeEntryModeAPI:
		return true
	}
	return false
}

// ClaudeCodeEntryRequest 入口为单个请求算出的路由约束
type ClaudeCodeEntryRequest struct {
	// Mode 本次请求的生效模式
	Mode string
	// RequestClassOverride 生效模式来自「按请求类别路由」的覆盖，而非全局模式
	RequestClassOverride bool
}

// claudeCodeEntryKey 标记请求来自 Claude Code 专用 loopback 入口。
// 放在 endpoint 包而非 proxy 包：proxy 与 proxy/handlers 都要读取，且 handlers 不能反向依赖 proxy。
type claudeCodeEntryKey struct{}

// WithClaudeCodeEntry 给请求上下文打上 Claude Code 入口标记与生效模式。
// 只有带此标记的请求才可以调度到订阅直通（client_oauth）端点。
func WithClaudeCodeEntry(ctx context.Context, entry ClaudeCodeEntryRequest) context.Context {
	entry.Mode = NormalizeClaudeCodeEntryMode(entry.Mode)
	return context.WithValue(ctx, claudeCodeEntryKey{}, entry)
}

// ClaudeCodeEntryFromContext 读取入口标记；ok=false 表示不是入口请求
func ClaudeCodeEntryFromContext(ctx context.Context) (ClaudeCodeEntryRequest, bool) {
	if ctx == nil {
		return ClaudeCodeEntryRequest{}, false
	}
	entry, ok := ctx.Value(claudeCodeEntryKey{}).(ClaudeCodeEntryRequest)
	return entry, ok
}

// IsClaudeCodeEntry 判断请求是否来自 Claude Code 专用入口
func IsClaudeCodeEntry(ctx context.Context) bool {
	_, ok := ClaudeCodeEntryFromContext(ctx)
	return ok
}

// ApplyClaudeCodeEntry 把请求上下文里的入口标记与生效模式写进路由画像
func (p *RouteRequestProfile) ApplyClaudeCodeEntry(ctx context.Context) {
	entry, ok := ClaudeCodeEntryFromContext(ctx)
	p.ClaudeCodeEntry = ok
	p.ClaudeCodeEntryMode = ""
	p.ClaudeCodeEntryClassOverride = false
	if ok {
		p.ClaudeCodeEntryMode = NormalizeClaudeCodeEntryMode(entry.Mode)
		p.ClaudeCodeEntryClassOverride = entry.RequestClassOverride
	}
}

// 调度决策原因：订阅直通端点与入口模式相关
const (
	// routeSkipClientOAuthOutsideEntry 订阅直通端点只接受 Claude Code 入口的请求
	routeSkipClientOAuthOutsideEntry = "client_oauth_requires_claude_code_entry"
	// routeSkipCrossAuthBoundary 认证方式与首个候选不同：禁止订阅与 API 之间自动转移
	routeSkipCrossAuthBoundary = "cross_auth_boundary"
	// routeSkipEntryModeSubscriptionOnly 入口模式为仅订阅，跳过端点凭据端点
	routeSkipEntryModeSubscriptionOnly = "claude_code_mode_subscription_only"
	// routeSkipEntryModeAPIOnly 入口模式为仅 API，跳过订阅直通端点
	routeSkipEntryModeAPIOnly = "claude_code_mode_api_only"
	// routeSkipEntryRequestClassAPI 请求类别按设置走 API，跳过订阅直通端点
	routeSkipEntryRequestClassAPI = "claude_code_request_class_api"

	// RouteBlockCodeClaudeCodeModeNoEndpoint 入口模式下作用域内没有任何符合的端点
	RouteBlockCodeClaudeCodeModeNoEndpoint = "claude_code_mode_no_compatible_endpoint"
)

// entryRouteDecision 判断端点能否服务该请求：订阅直通端点只服务入口请求；
// 入口请求再受生效模式约束。返回不可服务时的跳过原因。
func entryRouteDecision(clientOAuth bool, profile RouteRequestProfile) (bool, string) {
	if !profile.ClaudeCodeEntry {
		if clientOAuth {
			return false, routeSkipClientOAuthOutsideEntry
		}
		return true, ""
	}
	switch profile.ClaudeCodeEntryMode {
	case ClaudeCodeEntryModeSubscription:
		if !clientOAuth {
			return false, routeSkipEntryModeSubscriptionOnly
		}
	case ClaudeCodeEntryModeAPI:
		if clientOAuth {
			if profile.ClaudeCodeEntryClassOverride {
				return false, routeSkipEntryRequestClassAPI
			}
			return false, routeSkipEntryModeAPIOnly
		}
	}
	return true, ""
}

// endpointServesEntry 在运行态端点上执行 entryRouteDecision
func endpointServesEntry(ep *Endpoint, profile RouteRequestProfile) (bool, string) {
	if ep == nil {
		return false, "endpoint_missing"
	}
	ep.mutex.RLock()
	clientOAuth := ep.Config.IsClientOAuth()
	ep.mutex.RUnlock()
	return entryRouteDecision(clientOAuth, profile)
}

// claudeCodeEntryModeBlock 入口请求在非自动模式下，作用域内（不看冷却）没有任何符合模式的端点时，
// 返回明确的配置错误，而不是笼统的 503；有符合的端点只是暂不可用时返回 nil，走常规 503。
func (m *Manager) claudeCodeEntryModeBlock(scope RouteScope, profile RouteRequestProfile) *RouteBlock {
	if !profile.ClaudeCodeEntry || profile.ClaudeCodeEntryMode == ClaudeCodeEntryModeAuto {
		return nil
	}
	if m.scopeHasEntryCompatibleEndpoint(scope, profile) {
		return nil
	}
	target := "当前可调度的端点"
	if scope.Model != "" {
		target = fmt.Sprintf("模型 %q 的端点集合", scope.Model)
	}
	var message string
	switch {
	case profile.ClaudeCodeEntryMode == ClaudeCodeEntryModeSubscription:
		message = fmt.Sprintf("Claude Code 接入模式为「仅订阅」，但%s中没有订阅直通端点。", target)
	case profile.ClaudeCodeEntryClassOverride:
		message = fmt.Sprintf("该请求类别按设置走 API，但%s中没有第三方端点。", target)
	default:
		message = fmt.Sprintf("Claude Code 接入模式为「仅 API」，但%s中没有第三方端点。", target)
	}
	return &RouteBlock{
		StatusCode: http.StatusUnprocessableEntity,
		Code:       RouteBlockCodeClaudeCodeModeNoEndpoint,
		Message:    message,
		Reason:     RouteBlockCodeClaudeCodeModeNoEndpoint,
	}
}

// scopeHasEntryCompatibleEndpoint 作用域内是否存在硬启用、参与自动调度且符合入口模式的端点
func (m *Manager) scopeHasEntryCompatibleEndpoint(scope RouteScope, profile RouteRequestProfile) bool {
	members := make(map[string]struct{}, len(scope.Members))
	for _, member := range scope.Members {
		members[member] = struct{}{}
	}
	m.endpointsMu.RLock()
	defer m.endpointsMu.RUnlock()
	for _, ep := range m.endpoints {
		if ep == nil {
			continue
		}
		ep.mutex.RLock()
		name := ep.Config.Name
		eligible := ep.Config.IsAvailabilityEnabled() && ep.Config.IsAutoScheduleEnabled()
		clientOAuth := ep.Config.IsClientOAuth()
		ep.mutex.RUnlock()
		if !eligible {
			continue
		}
		if scope.Kind == RouteScopeUpstreamGroup {
			if _, ok := members[name]; !ok {
				continue
			}
		}
		if ok, _ := entryRouteDecision(clientOAuth, profile); ok {
			return true
		}
	}
	return false
}

// restrictToFirstCandidateAuthMode 只保留与首个候选认证方式相同的候选，
// 避免订阅额度失败后静默改用第三方 API（或反向）。被剔除的候选在快照中记为 skipped。
func restrictToFirstCandidateAuthMode(result *EndpointScheduleResult) {
	if result == nil || len(result.Plans) < 2 {
		return
	}
	anchor := result.Plans[0].authMode
	keptCandidates := result.Candidates[:1]
	keptPlans := result.Plans[:1]
	for i := 1; i < len(result.Plans); i++ {
		plan := result.Plans[i]
		if plan.authMode == anchor {
			keptCandidates = append(keptCandidates, result.Candidates[i])
			keptPlans = append(keptPlans, plan)
			continue
		}
		markCandidateDecisionSkipped(result.Snapshot, plan.EndpointName, routeSkipCrossAuthBoundary)
	}
	result.Candidates = keptCandidates
	result.Plans = keptPlans
}

// markCandidateDecisionSkipped 把快照中已记为候选的端点改为跳过
func markCandidateDecisionSkipped(snapshot *EndpointScheduleSnapshot, name, reason string) {
	if snapshot == nil {
		return
	}
	for idx := range snapshot.Decisions {
		d := &snapshot.Decisions[idx]
		if d.Name == name && d.Decision == endpointDecisionCandidate {
			d.Decision = endpointDecisionSkipped
			d.Reason = reason
		}
	}
}

// Claude Code 请求类别（x-claude-code-request-class，需客户端设置 CLAUDE_CODE_GATEWAY_HINT_HEADERS=1）。
// 可配置为强制走 API 的类别不含 main：主对话始终按全局模式。
const (
	ClaudeCodeRequestClassHeader = "X-Claude-Code-Request-Class"

	ClaudeCodeRequestClassSubagent   = "subagent"
	ClaudeCodeRequestClassAuxiliary  = "auxiliary"
	ClaudeCodeRequestClassCompaction = "compaction"
	ClaudeCodeRequestClassWorkflow   = "workflow"
)

// ClaudeCodeAPIRequestClasses 可配置为强制走 API 的请求类别，按展示顺序排列
var ClaudeCodeAPIRequestClasses = []string{
	ClaudeCodeRequestClassSubagent,
	ClaudeCodeRequestClassAuxiliary,
	ClaudeCodeRequestClassCompaction,
	ClaudeCodeRequestClassWorkflow,
}

// NormalizeClaudeCodeAPIRequestClasses 去空白、转小写、去重并按固定顺序排列；含未知类别时返回错误
func NormalizeClaudeCodeAPIRequestClasses(classes []string) ([]string, error) {
	selected := make(map[string]struct{}, len(classes))
	for _, class := range classes {
		class = strings.ToLower(strings.TrimSpace(class))
		if class == "" {
			continue
		}
		selected[class] = struct{}{}
	}
	normalized := make([]string, 0, len(selected))
	for _, class := range ClaudeCodeAPIRequestClasses {
		if _, ok := selected[class]; ok {
			normalized = append(normalized, class)
			delete(selected, class)
		}
	}
	for unknown := range selected {
		return nil, fmt.Errorf("不支持的请求类别: %q", unknown)
	}
	return normalized, nil
}
