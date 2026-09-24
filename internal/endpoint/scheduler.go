package endpoint

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"cc-forwarder/config"
)

// 端点调度器（方案 §4.2）：纯读候选计算，无路由状态写副作用
//（不修改冷却 / 激活 / 失败追踪 / 负缓存）。
// fastest 策略的实时测速属 IO 行为，由转发管线在调用本方法前显式执行
//（测速只更新测速缓存，非路由状态）；本方法按测速缓存排序。

// EndpointScheduleDecision 单个端点的调度决策记录
type EndpointScheduleDecision struct {
	Name           string    `json:"name"`
	Decision       string    `json:"decision"` // candidate / skipped
	Reason         string    `json:"reason"`
	AvailableAt    time.Time `json:"available_at,omitempty"` // skipped 时的预计可用时间（零值=未知）
	RuntimeOutcome string    `json:"runtime_outcome,omitempty"`
	RuntimeError   string    `json:"runtime_error,omitempty"`
}

const (
	endpointDecisionCandidate = "candidate"
	endpointDecisionSkipped   = "skipped"
)

// 端点调度快照运行态与终态。字符串作为 Wails/UI 稳定契约。
const (
	EndpointScheduleRuntimeAttempting = "attempting"
	EndpointScheduleRuntimeTryNext    = "try_next"

	EndpointScheduleOutcomePending                  = "pending"
	EndpointScheduleOutcomeSuccess                  = "success"
	EndpointScheduleOutcomeQualityIncomplete        = "quality_incomplete"
	EndpointScheduleOutcomeFailedAfterCommit        = "failed_after_commit"
	EndpointScheduleOutcomeCancelled                = "cancelled"
	EndpointScheduleOutcomePrivacyBlocked           = "privacy_blocked"
	EndpointScheduleOutcomePassthroughError         = "passthrough_error"
	EndpointScheduleOutcomePassthroughRaw           = "passthrough_raw"
	EndpointScheduleOutcomeNoCandidates             = "no_candidates"
	EndpointScheduleOutcomeManualFixedBlocked       = "manual_fixed_blocked"
	EndpointScheduleOutcomeModelRoutingUnassigned   = "model_routing_unassigned"
	EndpointScheduleOutcomeAllCandidatesFailed      = "all_candidates_failed"
	EndpointScheduleOutcomeRejectedByFailureTracker = "rejected_by_failure_tracker"
	EndpointScheduleOutcomeRateLimited              = "rate_limited"
	endpointScheduleSnapshotPendingTTL              = 5 * time.Minute
)

// EndpointScheduleSnapshot 一次调度的完整决策快照（§4.5 观测）
type EndpointScheduleSnapshot struct {
	RequestID              string                     `json:"request_id"`
	CapturedAt             time.Time                  `json:"captured_at"`
	UpdatedAt              time.Time                  `json:"updated_at"`
	RequestPath            string                     `json:"request_path"`
	SelectedEndpoint       string                     `json:"selected_endpoint"`
	RouteMode              string                     `json:"route_mode"`
	RouteEndpointName      string                     `json:"route_endpoint_name"`
	RouteFallbackEnabled   bool                       `json:"route_fallback_enabled"`
	RouteScopeKind         string                     `json:"route_scope_kind,omitempty"`
	RouteScope             string                     `json:"route_scope,omitempty"`
	RouteScopeGroupID      int64                      `json:"route_scope_group_id,omitempty"`
	RouteScopeGroupName    string                     `json:"route_scope_group_name,omitempty"`
	UpstreamGroup          *UpstreamGroupSnapshot     `json:"upstream_group,omitempty"`
	RequestModel           string                     `json:"request_model,omitempty"`
	ModelRoutingVersion    int64                      `json:"model_routing_version,omitempty"`
	FailoverEnabled        bool                       `json:"failover_enabled"`
	CandidateAttemptBudget int                        `json:"candidate_attempt_budget,omitempty"`
	FinalOutcome           string                     `json:"final_outcome"`
	FinalError             string                     `json:"final_error"`
	Summary                string                     `json:"summary"`
	Decisions              []EndpointScheduleDecision `json:"decisions"`
}

type UpstreamGroupSnapshot struct {
	ID      int64    `json:"id"`
	Name    string   `json:"name"`
	Members []string `json:"members"`
}

type endpointScheduleSnapshotEntry struct {
	sequence uint64
	snapshot *EndpointScheduleSnapshot
}

// endpointScheduleSnapshotStore 保存最近一次请求和并发中的请求草稿。
// sequence 保证较早请求稍后完成时不会覆盖更晚开始的 latest。
type endpointScheduleSnapshotStore struct {
	mu             sync.RWMutex
	nextSequence   uint64
	latestSequence uint64
	latest         *EndpointScheduleSnapshot
	pending        map[string]endpointScheduleSnapshotEntry
}

func newEndpointScheduleSnapshotStore() *endpointScheduleSnapshotStore {
	return &endpointScheduleSnapshotStore{
		pending: make(map[string]endpointScheduleSnapshotEntry),
	}
}

func (s *endpointScheduleSnapshotStore) saveDraft(requestID, requestPath string, snapshot *EndpointScheduleSnapshot) {
	if s == nil || snapshot == nil {
		return
	}

	clone := cloneEndpointScheduleSnapshot(snapshot)
	now := time.Now()
	if clone.CapturedAt.IsZero() {
		clone.CapturedAt = now
	}
	clone.RequestID = requestID
	clone.RequestPath = requestPath
	clone.UpdatedAt = now
	if clone.FinalOutcome == "" {
		clone.FinalOutcome = EndpointScheduleOutcomePending
	}
	clone.Summary = endpointScheduleSummary(clone)

	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := now.Add(-endpointScheduleSnapshotPendingTTL)
	for id, entry := range s.pending {
		if entry.snapshot == nil || entry.snapshot.CapturedAt.Before(cutoff) {
			delete(s.pending, id)
		}
	}

	s.nextSequence++
	entry := endpointScheduleSnapshotEntry{
		sequence: s.nextSequence,
		snapshot: cloneEndpointScheduleSnapshot(clone),
	}
	if requestID != "" {
		s.pending[requestID] = entry
	}
	s.latestSequence = entry.sequence
	s.latest = clone
}

func (s *endpointScheduleSnapshotStore) updateAttempt(requestID, endpointName, outcome, runtimeError string) {
	if s == nil || endpointName == "" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.pending[requestID]
	if requestID == "" {
		ok = s.latest != nil && s.latest.RequestID == ""
		entry = endpointScheduleSnapshotEntry{sequence: s.latestSequence, snapshot: cloneEndpointScheduleSnapshot(s.latest)}
	}
	if !ok || entry.snapshot == nil {
		return
	}

	snapshot := entry.snapshot
	snapshot.SelectedEndpoint = endpointName
	snapshot.UpdatedAt = time.Now()
	for idx := range snapshot.Decisions {
		decision := &snapshot.Decisions[idx]
		if decision.Name != endpointName || decision.Decision != endpointDecisionCandidate {
			continue
		}
		decision.RuntimeOutcome = outcome
		decision.RuntimeError = runtimeError
		break
	}
	snapshot.Summary = endpointScheduleSummary(snapshot)
	entry.snapshot = cloneEndpointScheduleSnapshot(snapshot)
	if requestID != "" {
		s.pending[requestID] = entry
	}
	if entry.sequence == s.latestSequence {
		s.latest = cloneEndpointScheduleSnapshot(snapshot)
	}
}

// complete 写入终态并返回收口后的快照副本（nil 表示没有匹配到草稿，调用方不应持久化）。
func (s *endpointScheduleSnapshotStore) complete(requestID, endpointName, outcome, finalError string) *EndpointScheduleSnapshot {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.pending[requestID]
	if requestID == "" {
		ok = s.latest != nil && s.latest.RequestID == ""
		entry = endpointScheduleSnapshotEntry{sequence: s.latestSequence, snapshot: cloneEndpointScheduleSnapshot(s.latest)}
	}
	if !ok || entry.snapshot == nil {
		return nil
	}
	if requestID != "" {
		delete(s.pending, requestID)
	}

	snapshot := entry.snapshot
	if endpointName != "" {
		snapshot.SelectedEndpoint = endpointName
	}
	if outcome != "" {
		snapshot.FinalOutcome = outcome
	}
	snapshot.FinalError = finalError
	snapshot.UpdatedAt = time.Now()
	if endpointName != "" {
		for idx := range snapshot.Decisions {
			decision := &snapshot.Decisions[idx]
			if decision.Name != endpointName || decision.Decision != endpointDecisionCandidate {
				continue
			}
			decision.RuntimeOutcome = outcome
			decision.RuntimeError = finalError
			break
		}
	}
	snapshot.Summary = endpointScheduleSummary(snapshot)

	if entry.sequence == s.latestSequence {
		s.latest = cloneEndpointScheduleSnapshot(snapshot)
	}
	return cloneEndpointScheduleSnapshot(snapshot)
}

func (s *endpointScheduleSnapshotStore) getLatest() *EndpointScheduleSnapshot {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneEndpointScheduleSnapshot(s.latest)
}

func cloneEndpointScheduleSnapshot(snapshot *EndpointScheduleSnapshot) *EndpointScheduleSnapshot {
	if snapshot == nil {
		return nil
	}
	clone := *snapshot
	if len(snapshot.Decisions) > 0 {
		clone.Decisions = make([]EndpointScheduleDecision, len(snapshot.Decisions))
		copy(clone.Decisions, snapshot.Decisions)
	}
	return &clone
}

func endpointScheduleSummary(snapshot *EndpointScheduleSnapshot) string {
	if snapshot == nil {
		return ""
	}
	switch snapshot.FinalOutcome {
	case EndpointScheduleOutcomePending:
		if snapshot.SelectedEndpoint != "" {
			return fmt.Sprintf("正在尝试端点 %s", snapshot.SelectedEndpoint)
		}
		return "调度处理中"
	case EndpointScheduleOutcomeSuccess:
		return fmt.Sprintf("成功命中端点 %s", snapshot.SelectedEndpoint)
	case EndpointScheduleOutcomeQualityIncomplete:
		return fmt.Sprintf("端点 %s 返回了不完整响应", snapshot.SelectedEndpoint)
	case EndpointScheduleOutcomePassthroughRaw:
		return fmt.Sprintf("原样返回端点 %s 的客户端错误", snapshot.SelectedEndpoint)
	case EndpointScheduleOutcomeNoCandidates, EndpointScheduleOutcomeManualFixedBlocked, EndpointScheduleOutcomeModelRoutingUnassigned:
		return "本次请求没有可路由端点"
	case EndpointScheduleOutcomeAllCandidatesFailed:
		return "全部候选端点均失败"
	case EndpointScheduleOutcomeCancelled:
		return "请求已取消"
	default:
		if snapshot.SelectedEndpoint != "" {
			return fmt.Sprintf("端点 %s 调度结束：%s", snapshot.SelectedEndpoint, snapshot.FinalOutcome)
		}
		return snapshot.FinalOutcome
	}
}

// BeginEndpointScheduleSnapshot 保存本次调度草稿。传入 nil 时用于记录调度前置拒绝。
func (m *Manager) BeginEndpointScheduleSnapshot(requestID, requestPath string, snapshot *EndpointScheduleSnapshot) {
	if m == nil || m.scheduleSnapshots == nil {
		return
	}
	if snapshot == nil {
		cfg := m.getConfigSnapshot()
		override := m.routeOverride.Snapshot()
		snapshot = &EndpointScheduleSnapshot{
			CapturedAt:           time.Now(),
			RouteMode:            override.Mode,
			RouteEndpointName:    override.EndpointName,
			RouteFallbackEnabled: override.FallbackEnabled,
			RouteScopeKind:       RouteScopeGlobal,
			RouteScope:           RouteScopeGlobal,
			FailoverEnabled:      cfg.Failover.Enabled,
		}
	}
	m.scheduleSnapshots.saveDraft(requestID, requestPath, snapshot)
}

// BeginEndpointScheduleSnapshotForScope 记录尚未进入候选计算的前置分支。
func (m *Manager) BeginEndpointScheduleSnapshotForScope(requestID, requestPath string, scope RouteScope) {
	if m == nil {
		return
	}
	cfg := m.getConfigSnapshot()
	m.BeginEndpointScheduleSnapshot(requestID, requestPath, &EndpointScheduleSnapshot{
		CapturedAt:           time.Now(),
		RouteMode:            scope.Mode,
		RouteEndpointName:    scope.Target,
		RouteFallbackEnabled: scope.FallbackEnabled,
		RouteScopeKind:       scope.Kind,
		RouteScope:           scope.Kind,
		RouteScopeGroupID:    scope.GroupID,
		RouteScopeGroupName:  scope.GroupName,
		UpstreamGroup:        upstreamGroupSnapshot(scope),
		RequestModel:         scope.Model,
		ModelRoutingVersion:  scope.SnapshotVersion,
		FailoverEnabled:      cfg.Failover.Enabled,
		FinalOutcome:         EndpointScheduleOutcomePending,
	})
}

// RecordEndpointScheduleAttempt 更新候选端点的运行结果。
func (m *Manager) RecordEndpointScheduleAttempt(requestID, endpointName, outcome, runtimeError string) {
	if m == nil || m.scheduleSnapshots == nil {
		return
	}
	m.scheduleSnapshots.updateAttempt(requestID, endpointName, outcome, runtimeError)
}

// CompleteEndpointScheduleSnapshot 写入最终 Outcome，并返回终态快照副本。
// 调用方可将该副本持久化到请求生命周期；nil 表示没有匹配到当前请求草稿。
func (m *Manager) CompleteEndpointScheduleSnapshot(requestID, endpointName, outcome, finalError string) *EndpointScheduleSnapshot {
	if m == nil || m.scheduleSnapshots == nil {
		return nil
	}
	return m.scheduleSnapshots.complete(requestID, endpointName, outcome, finalError)
}

// GetLatestEndpointScheduleSnapshot 返回深拷贝，避免 UI 读取与管线更新产生竞态。
func (m *Manager) GetLatestEndpointScheduleSnapshot() *EndpointScheduleSnapshot {
	if m == nil || m.scheduleSnapshots == nil {
		return nil
	}
	return m.scheduleSnapshots.getLatest()
}

// EarliestAvailableAt 返回被跳过端点中最早的可用时间（零值=无已知恢复时间）。
// 供空候选 / 全部失败时计算 503 Retry-After。
func (s *EndpointScheduleSnapshot) EarliestAvailableAt() time.Time {
	if s == nil {
		return time.Time{}
	}
	var earliest time.Time
	for _, decision := range s.Decisions {
		if decision.Decision != endpointDecisionSkipped || decision.AvailableAt.IsZero() {
			continue
		}
		if earliest.IsZero() || decision.AvailableAt.Before(earliest) {
			earliest = decision.AvailableAt
		}
	}
	return earliest
}

// EndpointScheduleResult 调度结果：不可变候选计划 + revision 快照（v8 §8.1/§14.1）
type EndpointScheduleResult struct {
	Candidates            []*Endpoint // 与 Plans 对齐；attempt 前必须经 AcquireEndpointAttempt 重校验
	Plans                 []EndpointAttemptPlan
	RouteOverrideRevision int64
	Scope                 RouteScope
	Snapshot              *EndpointScheduleSnapshot
}

// endpointScheduleCandidateSnapshot 是一次调度计算使用的端点值快照。
// 资格判断、分层、排序和 Plan 生成必须只消费这里的数据，live 仅用于兼容返回 Candidates。
type endpointScheduleCandidateSnapshot struct {
	live         *Endpoint
	config       config.EndpointConfig
	status       EndpointStatus
	revision     int64
	failureEpoch int64
	token        string
	apiKey       string
}

func (m *Manager) snapshotEndpointCandidates() []endpointScheduleCandidateSnapshot {
	// 同一 generation barrier 下同时捕获 Config/revision 与当前活动凭据，
	// UpdateEndpointConfig 无法在两者之间发布半套状态。
	m.endpointConfigMu.RLock()
	defer m.endpointConfigMu.RUnlock()

	m.endpointsMu.RLock()
	snapshots := make([]endpointScheduleCandidateSnapshot, 0, len(m.endpoints))
	for _, ep := range m.endpoints {
		if ep == nil {
			continue
		}
		ep.mutex.RLock()
		snapshots = append(snapshots, endpointScheduleCandidateSnapshot{
			live:         ep,
			config:       cloneEndpointConfig(ep.Config),
			status:       ep.Status,
			revision:     ep.configRevision,
			failureEpoch: ep.failureEpoch,
		})
		ep.mutex.RUnlock()
	}
	m.endpointsMu.RUnlock()

	for idx := range snapshots {
		token, apiKey := m.resolveAttemptCredentials(snapshots[idx].config)
		snapshots[idx].token = token
		snapshots[idx].apiKey = apiKey
	}
	return snapshots
}

func findEndpointCandidateSnapshot(snapshot []endpointScheduleCandidateSnapshot, name string) *endpointScheduleCandidateSnapshot {
	for idx := range snapshot {
		if snapshot[idx].config.Name == name {
			return &snapshot[idx]
		}
	}
	return nil
}

// PrepareRouteCandidates 计算本次请求的候选端点序（v8 §8.2/§8.3）。
// Auto：硬启用 + 参与自动调度 + 健康过滤后按 priority 层级选最优可用层，
// 层内 retained 优先、其余按策略排序；低优先级层仅供重放安全硬失败 fallback。
// manual_preferred：目标置首（不受 auto flag 影响）；manual_fixed 只考虑目标端点。
// Failover.Enabled=false 时只保留第一个逻辑候选。
func (m *Manager) PrepareRouteCandidates(ctx context.Context, profile RouteRequestProfile) EndpointScheduleResult {
	cfg := m.getConfigSnapshot()
	scope := m.ResolveRouteScope(profile)

	snapshot := m.snapshotEndpointCandidates()

	result := EndpointScheduleResult{
		RouteOverrideRevision: scope.Revision,
		Scope:                 scope.Clone(),
		Snapshot: &EndpointScheduleSnapshot{
			CapturedAt:             time.Now(),
			RouteMode:              scope.Mode,
			RouteEndpointName:      scope.Target,
			RouteFallbackEnabled:   scope.FallbackEnabled,
			RouteScopeKind:         scope.Kind,
			RouteScope:             scope.Kind,
			RouteScopeGroupID:      scope.GroupID,
			RouteScopeGroupName:    scope.GroupName,
			UpstreamGroup:          upstreamGroupSnapshot(scope),
			RequestModel:           scope.Model,
			ModelRoutingVersion:    scope.SnapshotVersion,
			FailoverEnabled:        cfg.Failover.Enabled,
			CandidateAttemptBudget: cfg.Failover.MaxCandidateAttempts,
			FinalOutcome:           EndpointScheduleOutcomePending,
		},
	}

	record := func(name, decision, reason string, availableAt time.Time) {
		result.Snapshot.Decisions = append(result.Snapshot.Decisions, EndpointScheduleDecision{
			Name:        name,
			Decision:    decision,
			Reason:      reason,
			AvailableAt: availableAt,
		})
	}

	// §6.4：启动读取失败时 Claude 路由 not ready，不生成任何候选
	if !m.IsClaudeRoutingReady() {
		record("", endpointDecisionSkipped, "routing_not_ready", time.Time{})
		return result
	}
	if scope.Kind == RouteScopeUnassigned {
		record("", endpointDecisionSkipped, scope.UnassignedReason, time.Time{})
		return result
	}
	memberSet := make(map[string]struct{}, len(scope.Members))
	if scope.Kind == RouteScopeUpstreamGroup {
		for _, member := range scope.Members {
			memberSet[member] = struct{}{}
			if findEndpointCandidateSnapshot(snapshot, member) == nil {
				record(member, endpointDecisionSkipped, "member_missing", time.Time{})
			}
		}
	}

	appendPlan := func(candidate endpointScheduleCandidateSnapshot, source string) {
		plan := EndpointAttemptPlan{
			EndpointName:        candidate.config.Name,
			Priority:            candidate.config.Priority,
			URL:                 candidate.config.URL,
			Timeout:             candidate.config.Timeout,
			SupportsCountTokens: candidate.config.SupportsCountTokens,
			ConfigRevision:      candidate.revision,
			FailureEpoch:        candidate.failureEpoch,
			SelectionSource:     source,
			resolvedToken:       candidate.token,
			resolvedAPIKey:      candidate.apiKey,
			authMode:            config.NormalizeEndpointAuthMode(candidate.config.AuthMode),
		}
		result.Candidates = append(result.Candidates, candidate.live)
		result.Plans = append(result.Plans, plan)
		record(plan.EndpointName, endpointDecisionCandidate, source, time.Time{})
	}

	// manual_fixed：只考虑目标端点，不可路由时不静默转移
	if scope.Mode == RouteModeManualFixed && scope.Target != "" {
		if scope.Kind == RouteScopeUpstreamGroup {
			if _, ok := memberSet[scope.Target]; !ok {
				record(scope.Target, endpointDecisionSkipped, "manual_fixed_target_not_member", time.Time{})
				return result
			}
		}
		target := findEndpointCandidateSnapshot(snapshot, scope.Target)
		if target == nil {
			record(scope.Target, endpointDecisionSkipped, "manual_fixed_target_missing", time.Time{})
			return result
		}
		if !target.config.IsAvailabilityEnabled() {
			record(target.config.Name, endpointDecisionSkipped, "manual_fixed_availability_disabled", time.Time{})
			return result
		}
		if ok, reason, availableAt := m.classifyEndpointSnapshotRoutable(*target, profile); !ok {
			record(target.config.Name, endpointDecisionSkipped, "manual_fixed_"+reason, availableAt)
			return result
		}
		appendPlan(*target, "manual_fixed")
		return result
	}

	// manual_preferred 目标（不受 auto flag 影响，§8.2）
	// 组作用域只多一道成员校验，其余判定与全局作用域完全一致。
	var preferredTarget *endpointScheduleCandidateSnapshot
	if scope.Mode == RouteModeManualPreferred && scope.Target != "" {
		_, isMember := memberSet[scope.Target]
		if scope.Kind == RouteScopeUpstreamGroup && !isMember {
			record(scope.Target, endpointDecisionSkipped, "manual_preferred_target_not_member", time.Time{})
		} else if target := findEndpointCandidateSnapshot(snapshot, scope.Target); target == nil {
			record(scope.Target, endpointDecisionSkipped, "manual_preferred_target_missing", time.Time{})
		} else if !target.config.IsAvailabilityEnabled() {
			record(target.config.Name, endpointDecisionSkipped, "manual_preferred_availability_disabled", time.Time{})
		} else if ok, reason, availableAt := m.classifyEndpointSnapshotRoutable(*target, profile); !ok {
			record(target.config.Name, endpointDecisionSkipped, "manual_preferred_"+reason, availableAt)
		} else {
			preferredTarget = target
		}
	}

	// Auto 候选：硬启用 + auto schedule + 健康过滤（preferred 目标去重）
	eligible := make([]endpointScheduleCandidateSnapshot, 0, len(snapshot))
	for _, candidate := range snapshot {
		if scope.Kind == RouteScopeUpstreamGroup {
			if _, ok := memberSet[candidate.config.Name]; !ok {
				record(candidate.config.Name, endpointDecisionSkipped, "outside_model_group", time.Time{})
				continue
			}
		}
		if preferredTarget != nil && candidate.config.Name == preferredTarget.config.Name {
			continue
		}
		if !candidate.config.IsAvailabilityEnabled() {
			record(candidate.config.Name, endpointDecisionSkipped, "availability_disabled", time.Time{})
			continue
		}
		if !candidate.config.IsAutoScheduleEnabled() {
			record(candidate.config.Name, endpointDecisionSkipped, "auto_schedule_disabled", time.Time{})
			continue
		}
		if ok, reason, availableAt := m.classifyEndpointSnapshotRoutable(candidate, profile); !ok {
			record(candidate.config.Name, endpointDecisionSkipped, reason, availableAt)
			continue
		}
		eligible = append(eligible, candidate)
	}

	// §8.3：priority 层级；最优层内 retained 优先，其余按策略排序
	sort.SliceStable(eligible, func(i, j int) bool {
		return eligible[i].config.Priority < eligible[j].config.Priority
	})
	if preferredTarget != nil {
		appendPlan(*preferredTarget, "manual_preferred")
	}
	if len(eligible) > 0 {
		bestPriority := eligible[0].config.Priority
		bestTier := make([]endpointScheduleCandidateSnapshot, 0, len(eligible))
		lowerTiers := make([]endpointScheduleCandidateSnapshot, 0, len(eligible))
		for _, candidate := range eligible {
			if candidate.config.Priority == bestPriority {
				bestTier = append(bestTier, candidate)
			} else {
				lowerTiers = append(lowerTiers, candidate)
			}
		}

		bestTier = m.sortEndpointCandidateSnapshots(ctx, bestTier)
		retainedName := m.RetainedInTierForScope(scope.Key(), bestPriority)
		if retainedName != "" {
			for i, candidate := range bestTier {
				if candidate.config.Name == retainedName && i > 0 {
					// retained 只调整同层顺序：保存目标后将前缀整体右移，避免隐晦的嵌套 append。
					copy(bestTier[1:i+1], bestTier[:i])
					bestTier[0] = candidate
					break
				}
			}
		}

		for _, candidate := range bestTier {
			source := "auto_priority"
			if candidate.config.Name == retainedName {
				source = "auto_retained"
			} else if preferredTarget != nil {
				source = "fallback"
			}
			appendPlan(candidate, source)
		}
		for _, candidate := range lowerTiers {
			appendPlan(candidate, "fallback")
		}
	}

	// 认证边界：订阅直通与端点凭据两类候选不互相转移，以首个候选为准
	restrictToFirstCandidateAuthMode(&result)

	// §8.2：Failover.Enabled=false 时只保留第一个逻辑候选（不冻结后续请求）
	if !cfg.Failover.Enabled && len(result.Candidates) > 1 {
		for _, plan := range result.Plans[1:] {
			markCandidateDecisionSkipped(result.Snapshot, plan.EndpointName, "failover_disabled_single_candidate")
		}
		result.Candidates = result.Candidates[:1]
		result.Plans = result.Plans[:1]
	}

	result.Snapshot.Decisions = orderEndpointScheduleDecisions(result.Snapshot.Decisions, result.Plans)
	return result
}

func upstreamGroupSnapshot(scope RouteScope) *UpstreamGroupSnapshot {
	if scope.Kind != RouteScopeUpstreamGroup {
		return nil
	}
	return &UpstreamGroupSnapshot{ID: scope.GroupID, Name: scope.GroupName, Members: append([]string(nil), scope.Members...)}
}

// orderEndpointScheduleDecisions 让候选决策顺序与实际尝试顺序一致，
// 跳过项保持原解释顺序并排在候选项之后。
func orderEndpointScheduleDecisions(decisions []EndpointScheduleDecision, plans []EndpointAttemptPlan) []EndpointScheduleDecision {
	if len(decisions) == 0 || len(plans) == 0 {
		return decisions
	}
	byName := make(map[string]EndpointScheduleDecision, len(decisions))
	for _, decision := range decisions {
		if decision.Decision == endpointDecisionCandidate {
			byName[decision.Name] = decision
		}
	}

	ordered := make([]EndpointScheduleDecision, 0, len(decisions))
	for _, plan := range plans {
		if decision, ok := byName[plan.EndpointName]; ok {
			ordered = append(ordered, decision)
			delete(byName, plan.EndpointName)
		}
	}
	for _, decision := range decisions {
		if decision.Decision != endpointDecisionCandidate {
			ordered = append(ordered, decision)
			continue
		}
		if _, ok := byName[decision.Name]; ok {
			ordered = append(ordered, decision)
			delete(byName, decision.Name)
		}
	}
	return ordered
}

func (m *Manager) classifyEndpointSnapshotRoutable(candidate endpointScheduleCandidateSnapshot, profile RouteRequestProfile) (bool, string, time.Time) {
	// 订阅直通端点只服务 Claude Code 专用入口；一处判定覆盖手动固定、手动优先与自动三条路径
	if candidate.config.IsClientOAuth() && !profile.ClaudeCodeEntry {
		return false, routeSkipClientOAuthOutsideEntry, time.Time{}
	}
	now := time.Now()
	blockingUntil := time.Time{}
	if !candidate.status.GlobalCooldownUntil.IsZero() && now.Before(candidate.status.GlobalCooldownUntil) {
		blockingUntil = candidate.status.GlobalCooldownUntil
	}
	if !profile.IsCountTokens && !candidate.status.CooldownUntil.IsZero() && now.Before(candidate.status.CooldownUntil) &&
		candidate.status.CooldownUntil.After(blockingUntil) {
		blockingUntil = candidate.status.CooldownUntil
	}
	if !blockingUntil.IsZero() {
		return false, "cooldown", blockingUntil
	}
	if profile.IsCountTokens {
		if active, until, _ := m.ScopedCooldownActive(candidate.config.Name, SoftFailureScopeCountTokens); active {
			return false, "count_tokens_scoped_cooldown", until
		}
	}
	if hit, failureClass, expiresAt := m.routeState.NegativeHitWithExpiry(candidate.config.Name, profile); hit {
		return false, "negative_cache_" + failureClass, expiresAt
	}
	return true, "", time.Time{}
}

// classifyEndpointRoutable 判定端点是否可路由；不可路由时给出原因与预计可用时间。
// availableAt 四来源（§3.1 补充规则）：暂停到期、冷却到期、tripped 窗口到期（保守估计）、
// 负缓存条目 expiresAt。
func (m *Manager) classifyEndpointRoutable(ep *Endpoint, profile RouteRequestProfile) (bool, string, time.Time) {
	if ep == nil {
		return false, "endpoint_missing", time.Time{}
	}

	ep.mutex.RLock()
	name := ep.Config.Name
	messagesCooldownUntil := ep.Status.CooldownUntil
	globalCooldownUntil := ep.Status.GlobalCooldownUntil
	ep.mutex.RUnlock()

	now := time.Now()
	// global 槽阻断双 path；messages 槽不阻断 count_tokens（§14.4）
	blockingUntil := time.Time{}
	if !globalCooldownUntil.IsZero() && now.Before(globalCooldownUntil) {
		blockingUntil = globalCooldownUntil
	}
	if !profile.IsCountTokens && !messagesCooldownUntil.IsZero() && now.Before(messagesCooldownUntil) &&
		messagesCooldownUntil.After(blockingUntil) {
		blockingUntil = messagesCooldownUntil
	}
	if !blockingUntil.IsZero() {
		return false, "cooldown", blockingUntil
	}

	// §10：count_tokens 请求叠加进程内 scoped cooldown 过滤（不影响 /v1/messages）
	if profile.IsCountTokens {
		if active, until, _ := m.ScopedCooldownActive(name, SoftFailureScopeCountTokens); active {
			return false, "count_tokens_scoped_cooldown", until
		}
	}

	// v8：软失败阈值触发即写入 cooldown（上方已检查），不再单独判定 tracker tripped（§9.3）
	if hit, failureClass, expiresAt := m.routeState.NegativeHitWithExpiry(name, profile); hit {
		return false, "negative_cache_" + failureClass, expiresAt
	}

	return true, "", time.Time{}
}

// sortEndpointCandidateSnapshots 只使用值快照排序；实时测速也使用脱离 Manager 的配置副本。
func (m *Manager) sortEndpointCandidateSnapshots(ctx context.Context, rest []endpointScheduleCandidateSnapshot) []endpointScheduleCandidateSnapshot {
	cfg := m.getConfigSnapshot()
	if cfg.Strategy.Type == "fastest" && cfg.Strategy.FastTestEnabled && len(rest) > 1 && m.fastTester != nil {
		testEndpoints := make([]*Endpoint, 0, len(rest))
		for _, candidate := range rest {
			testConfig := cloneEndpointConfig(candidate.config)
			// 强制命中快照凭据，禁止 FastTester 再回查运行态。
			testConfig.Token = candidate.token
			testConfig.ApiKey = candidate.apiKey
			testEndpoints = append(testEndpoints, &Endpoint{Config: testConfig, Status: candidate.status})
		}
		results, _ := m.fastTester.TestEndpointsParallel(ctx, testEndpoints)
		if ordered := orderEndpointSnapshotsByFastTestResults(rest, results); len(ordered) == len(rest) {
			return ordered
		}
	}
	if cfg.Strategy.Type == "fastest" {
		sort.SliceStable(rest, func(i, j int) bool {
			if rest[i].status.ResponseTime == rest[j].status.ResponseTime {
				return rest[i].config.Priority < rest[j].config.Priority
			}
			return rest[i].status.ResponseTime < rest[j].status.ResponseTime
		})
	}
	return rest
}

// orderEndpointSnapshotsByFastTestResults 按实时测速结果排序：成功者按响应时间升序在前，
// 失败/未覆盖者保持原相对顺序在后。
func orderEndpointSnapshotsByFastTestResults(rest []endpointScheduleCandidateSnapshot, results []*FastTestResult) []endpointScheduleCandidateSnapshot {
	if len(results) == 0 {
		return nil
	}
	type timing struct {
		candidate    endpointScheduleCandidateSnapshot
		responseTime time.Duration
	}
	successTimes := make(map[string]time.Duration, len(results))
	for _, result := range results {
		if result != nil && result.Success && result.Endpoint != nil {
			successTimes[result.Endpoint.Config.Name] = result.ResponseTime
		}
	}
	fast := make([]timing, 0, len(rest))
	slow := make([]endpointScheduleCandidateSnapshot, 0, len(rest))
	for _, candidate := range rest {
		if responseTime, ok := successTimes[candidate.config.Name]; ok {
			fast = append(fast, timing{candidate: candidate, responseTime: responseTime})
		} else {
			slow = append(slow, candidate)
		}
	}
	sort.SliceStable(fast, func(i, j int) bool {
		if fast[i].responseTime == fast[j].responseTime {
			return fast[i].candidate.config.Priority < fast[j].candidate.config.Priority
		}
		return fast[i].responseTime < fast[j].responseTime
	})
	ordered := make([]endpointScheduleCandidateSnapshot, 0, len(rest))
	for _, item := range fast {
		ordered = append(ordered, item.candidate)
	}
	return append(ordered, slow...)
}
