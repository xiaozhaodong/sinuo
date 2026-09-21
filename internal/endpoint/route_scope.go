package endpoint

import (
	"strings"
	"time"
)

// EndpointMemberView 是模型路由组成员在端点运行时的只读视图。
// 配置写入仍由 endpoint/service 层负责，模型路由只消费这份快照。
type EndpointMemberView struct {
	Name                string
	Exists              bool
	Priority            int
	AvailabilityEnabled bool
	AutoScheduleEnabled bool
	Healthy             bool
	CooldownUntil       time.Time
	CooldownReason      string
}

// EndpointMemberViewer 由端点管理器提供给模型路由服务，避免服务层复制运行态事实。
type EndpointMemberViewer func(name string) EndpointMemberView

const (
	RouteScopeGlobal        = "global"
	RouteScopeUpstreamGroup = "upstream_group"
	RouteScopeUnassigned    = "unassigned"
)

// RouteScope 是一次请求解析后的稳定路由作用域。
// Revision 对 global 表示 Claude override revision，对 upstream_group 表示组内路由 revision。
type RouteScope struct {
	Kind             string
	GroupID          int64
	GroupName        string
	Members          []string
	Mode             string
	Target           string
	Revision         int64
	SnapshotVersion  int64
	FallbackEnabled  bool
	UnassignedReason string
	Model            string
	// MatchedKey 是命中的分配键：模型名或联网工具保留键；global / unassigned 为空。
	MatchedKey string
}

// Key 是 retained 等运行态的作用域键。
// 组作用域带上 RouteRevision：成员或组内策略一变 revision 就 bump，key 随之改变，
// 旧 retained 立刻停止影响候选排序，不需要额外的失效逻辑。
// 陈旧 revision 的条目由 PruneAutoRetentionScopes 在快照重建后回收。
func (s RouteScope) Key() string {
	if s.Kind == RouteScopeUpstreamGroup && s.GroupID != 0 {
		return "group:" + formatInt64(s.GroupID) + ":r" + formatInt64(s.Revision)
	}
	return ""
}

func (s RouteScope) HasModelAssignment() bool {
	return s.Kind == RouteScopeUpstreamGroup
}

func (s RouteScope) Clone() RouteScope {
	s.Members = append([]string(nil), s.Members...)
	return s
}

// RouteScopePolicy 是模型路由服务与端点调度器之间的最小运行时契约。
// NoteDecision / SettleDecision 接收完整作用域：实现方必须先做 fencing（快照版本 + 组 revision），
// 在途请求期间被改派或关闭路由的旧作用域不得再写入组运行态。
type RouteScopePolicy interface {
	// ResolveScope 按请求模型与是否携带联网工具解析作用域。
	ResolveScope(model string, webTools bool) (RouteScope, bool)
	NoteDecision(scope RouteScope, effectiveEndpoint string)
	SettleDecision(scope RouteScope, effectiveEndpoint, outcome string)
	ScopeStillValid(scope RouteScope) bool
}

const (
	claudeMessagesPath    = "/v1/messages"
	claudeCountTokensPath = "/v1/messages/count_tokens"
)

// modelRoutingAppliesTo 限定模型路由只作用于 Claude messages 契约路径。
// 其他进入端点管线的路径（如 OpenAI 兼容路径）即便 body 带 model 也保持全局路由，
// 否则会被模型分组误过滤，甚至对非 Claude 请求返回 model_routing_unassigned。
func modelRoutingAppliesTo(path string) bool {
	return path == claudeMessagesPath || path == claudeCountTokensPath
}

func formatInt64(value int64) string {
	// 组 ID 来自 SQLite，避免 fmt 在热路径引入格式化选项。
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	buf := [20]byte{}
	i := len(buf)
	for value > 0 {
		i--
		buf[i] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func (m *Manager) SetRouteScopePolicy(policy RouteScopePolicy) {
	if m == nil {
		return
	}
	m.routeScopeMu.Lock()
	m.routeScopePolicy = policy
	m.routeScopeMu.Unlock()
}

func (m *Manager) routeScopePolicySnapshot() RouteScopePolicy {
	if m == nil {
		return nil
	}
	m.routeScopeMu.RLock()
	policy := m.routeScopePolicy
	m.routeScopeMu.RUnlock()
	return policy
}

// ResolveRouteScope 将请求模型解析为模型组作用域；未启用模型路由或路径不在
// Claude messages 契约内时保持 global。
func (m *Manager) ResolveRouteScope(profile RouteRequestProfile) RouteScope {
	override := m.routeOverride.Snapshot()
	global := RouteScope{
		Kind:            RouteScopeGlobal,
		Mode:            override.Mode,
		Target:          override.EndpointName,
		Revision:        override.Revision,
		FallbackEnabled: override.FallbackEnabled,
		Model:           strings.TrimSpace(profile.Model),
	}
	policy := m.routeScopePolicySnapshot()
	if policy == nil || !m.IsClaudeRoutingReady() || !modelRoutingAppliesTo(profile.Path) {
		return global
	}
	// 联网工具覆盖只作用于 /v1/messages：count_tokens 是纯计数请求，带 web_search /
	// web_fetch 定义时仍按模型映射归组，避免联网映射固定到不支持计数的端点后把
	// 同一条请求的 count_tokens 打成 422。
	if scope, ok := policy.ResolveScope(profile.Model, profile.WebTools && !profile.IsCountTokens); ok {
		return scope.Clone()
	}
	return global
}

// RouteStateForScope 将作用域映射为生命周期记录使用的 legacy route state。
// 纯映射：不写 LastDecisionAt / LastEffectiveEndpoint，那两个字段由真正做出决策的
// NoteRouteDecisionForScope 负责；否则一次只读映射会把「上次决策时间」改写成「本次读取时间」。
// 未分配作用域必须保留 manual_fixed 语义，防止无候选被误记为全局 auto。
func (m *Manager) RouteStateForScope(scope RouteScope) RouteOverrideState {
	state := m.routeOverride.Snapshot()
	if scope.Kind == RouteScopeGlobal {
		return state
	}
	state.Mode = scope.Mode
	state.EndpointName = scope.Target
	state.FallbackEnabled = scope.FallbackEnabled
	state.Revision = scope.Revision
	state.FallbackReason = scope.UnassignedReason
	state.LastEffectiveEndpoint = ""
	if scope.Kind == RouteScopeUnassigned {
		state.Mode = RouteModeManualFixed
		state.EndpointName = ""
		state.FallbackEnabled = false
	}
	return state
}

func (m *Manager) NoteRouteDecisionForScope(scope RouteScope, effectiveEndpoint, fallbackReason string) RouteOverrideState {
	if scope.Kind == RouteScopeGlobal {
		return m.NoteRouteDecision(effectiveEndpoint, fallbackReason)
	}
	state := m.RouteStateForScope(scope)
	state.LastEffectiveEndpoint = effectiveEndpoint
	state.FallbackReason = fallbackReason
	state.LastDecisionAt = time.Now()
	if policy := m.routeScopePolicySnapshot(); policy != nil && scope.Kind == RouteScopeUpstreamGroup {
		policy.NoteDecision(scope, effectiveEndpoint)
	}
	return state
}

func (m *Manager) SettleRouteDecision(scope RouteScope, effectiveEndpoint, outcome string) {
	if policy := m.routeScopePolicySnapshot(); policy != nil && scope.Kind == RouteScopeUpstreamGroup {
		policy.SettleDecision(scope, effectiveEndpoint, outcome)
	}
}

func (m *Manager) ScopeStillValid(scope RouteScope) bool {
	if scope.Kind == RouteScopeGlobal {
		return m.routeOverride.Snapshot().Revision == scope.Revision
	}
	if policy := m.routeScopePolicySnapshot(); policy != nil {
		return policy.ScopeStillValid(scope)
	}
	return false
}

// EndpointMemberView 返回端点当前的配置与健康运行态，供模型路由配置页和服务校验复用。
func (m *Manager) EndpointMemberView(name string) EndpointMemberView {
	view := EndpointMemberView{Name: strings.TrimSpace(name)}
	if m == nil || view.Name == "" {
		return view
	}
	ep := m.GetEndpointByNameAny(view.Name)
	if ep == nil {
		return view
	}
	ep.mutex.RLock()
	view.Exists = true
	view.Priority = ep.Config.Priority
	view.AvailabilityEnabled = ep.Config.IsAvailabilityEnabled()
	view.AutoScheduleEnabled = ep.Config.IsAutoScheduleEnabled()
	view.Healthy = ep.Status.Healthy
	if until, reason, active := ep.Status.EffectiveCooldown(time.Now()); active {
		view.CooldownUntil = until
		view.CooldownReason = reason
	}
	ep.mutex.RUnlock()
	return view
}

// GetEndpointMemberView 是更明确的别名，供外部装配代码使用。
func (m *Manager) GetEndpointMemberView(name string) EndpointMemberView {
	return m.EndpointMemberView(name)
}
