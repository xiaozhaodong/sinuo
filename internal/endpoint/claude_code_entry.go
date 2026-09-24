package endpoint

import "context"

// claudeCodeEntryKey 标记请求来自 Claude Code 专用 loopback 入口。
// 放在 endpoint 包而非 proxy 包：proxy 与 proxy/handlers 都要读取，且 handlers 不能反向依赖 proxy。
type claudeCodeEntryKey struct{}

// WithClaudeCodeEntry 给请求上下文打上 Claude Code 入口标记。
// 只有带此标记的请求才可以调度到订阅直通（client_oauth）端点。
func WithClaudeCodeEntry(ctx context.Context) context.Context {
	return context.WithValue(ctx, claudeCodeEntryKey{}, true)
}

// IsClaudeCodeEntry 判断请求是否来自 Claude Code 专用入口
func IsClaudeCodeEntry(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	marked, _ := ctx.Value(claudeCodeEntryKey{}).(bool)
	return marked
}

// 调度决策原因：订阅直通端点相关
const (
	// routeSkipClientOAuthOutsideEntry 订阅直通端点只接受 Claude Code 入口的请求
	routeSkipClientOAuthOutsideEntry = "client_oauth_requires_claude_code_entry"
	// routeSkipCrossAuthBoundary 认证方式与首个候选不同：禁止订阅与 API 之间自动转移
	routeSkipCrossAuthBoundary = "cross_auth_boundary"
)

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

// endpointServesEntry 端点能否服务该请求的入口：订阅直通端点只服务 Claude Code 入口
func endpointServesEntry(ep *Endpoint, profile RouteRequestProfile) bool {
	if ep == nil {
		return false
	}
	ep.mutex.RLock()
	clientOAuth := ep.Config.IsClientOAuth()
	ep.mutex.RUnlock()
	return !clientOAuth || profile.ClaudeCodeEntry
}
