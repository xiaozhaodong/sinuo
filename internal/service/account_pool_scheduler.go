package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"cc-forwarder/internal/accountauth"
	"cc-forwarder/internal/store"
)

const (
	accountScheduleDecisionSelected = "selected"
	accountScheduleDecisionEligible = "eligible"
	accountScheduleDecisionSkipped  = "skipped"
)

const (
	// CodexStandaloneSearchPath 是只允许 ChatGPT OAuth 账号参与调度的独立搜索端点。
	CodexStandaloneSearchPath                    = "/v1/alpha/search"
	accountScheduleSkipRouteRequiresChatGPTOAuth = "route_requires_chatgpt_oauth"
)

const (
	AccountScheduleOutcomeSuccess                        = "success"
	AccountScheduleOutcomeAuthFailed                     = "auth_failed"
	AccountScheduleOutcomeTransientFailure               = "transient_failure"
	AccountScheduleOutcomePassthroughNoAvailableProvider = "passthrough_no_available_providers"
	AccountScheduleOutcomePassthroughOther4xx            = "passthrough_other_4xx"
	AccountScheduleOutcomeNoSchedulableAccounts          = "no_schedulable_accounts"
	AccountScheduleOutcomePrivacyBlocked                 = "privacy_blocked"
	latestAccountScheduleSnapshotPendingTTL              = 5 * time.Minute

	accountScheduleOutcomeSuccess                        = AccountScheduleOutcomeSuccess
	accountScheduleOutcomeAuthFailed                     = AccountScheduleOutcomeAuthFailed
	accountScheduleOutcomeTransientFailure               = AccountScheduleOutcomeTransientFailure
	accountScheduleOutcomePassthroughNoAvailableProvider = AccountScheduleOutcomePassthroughNoAvailableProvider
	accountScheduleOutcomePassthroughOther4xx            = AccountScheduleOutcomePassthroughOther4xx
	accountScheduleOutcomeNoSchedulableAccounts          = AccountScheduleOutcomeNoSchedulableAccounts
)

type AccountScheduleCandidateDecision struct {
	AccountID               int64      `json:"account_id"`
	AccountName             string     `json:"account_name"`
	ProviderType            string     `json:"provider_type"`
	Priority                int        `json:"priority"`
	TierIndex               int        `json:"tier_index"`
	TierLabel               string     `json:"tier_label"`
	QuotaStatus             string     `json:"quota_status"`
	EffectiveQuotaRemaining *float64   `json:"effective_quota_remaining,omitempty"`
	FailCount               int        `json:"fail_count"`
	LastSuccessAt           *time.Time `json:"last_success_at,omitempty"`
	Decision                string     `json:"decision"`
	Reason                  string     `json:"reason"`
	ReasonDetail            string     `json:"reason_detail"`
	RuntimeOutcome          string     `json:"runtime_outcome,omitempty"`
	RuntimeError            string     `json:"runtime_error,omitempty"`
}

type LatestAccountScheduleSnapshot struct {
	RequestID               string                             `json:"request_id"`
	CapturedAt              time.Time                          `json:"captured_at"`
	UpdatedAt               time.Time                          `json:"updated_at"`
	RequestPath             string                             `json:"request_path"`
	SelectedPriority        int                                `json:"selected_priority"`
	SelectedTierIndex       int                                `json:"selected_tier_index"`
	SelectedTierLabel       string                             `json:"selected_tier_label"`
	DegradedToLowerPriority bool                               `json:"degraded_to_lower_priority"`
	SelectedAccountID       int64                              `json:"selected_account_id"`
	SelectedAccountName     string                             `json:"selected_account_name"`
	FinalOutcome            string                             `json:"final_outcome"`
	FinalError              string                             `json:"final_error"`
	Summary                 string                             `json:"summary"`
	RequestModel            string                             `json:"request_model,omitempty"`
	ModelRoutingFamily      string                             `json:"model_routing_family,omitempty"`
	ModelRoutingVersion     int64                              `json:"model_routing_version,omitempty"`
	RouteScopeKind          string                             `json:"route_scope_kind,omitempty"`
	RouteScopeGroupID       int64                              `json:"route_scope_group_id,omitempty"`
	RouteScopeGroupName     string                             `json:"route_scope_group_name,omitempty"`
	RouteMode               string                             `json:"route_mode,omitempty"`
	RouteTargetAccountID    int64                              `json:"route_target_account_id,omitempty"`
	Candidates              []AccountScheduleCandidateDecision `json:"candidates"`
}

type rankedSchedulableAccount struct {
	account                 *store.UpstreamAccountRecord
	bucket                  int
	finalScore              float64
	effectiveQuotaRemaining *float64
	remaining5H             *float64
	remainingWeekly         *float64
	hoursToReset5H          *float64
	hoursToResetWeekly      *float64
	weeklyGuardrailApplied  bool
	skipReason              string
	skipReasonDetail        string
}

type rankedPriorityTier struct {
	groupKey string
	priority int
	index    int
	eligible []*rankedSchedulableAccount
	skipped  []*rankedSchedulableAccount
}

type accountSelectionSource int

const (
	accountSelectionSourceRanked accountSelectionSource = iota
	accountSelectionSourcePreferred
	accountSelectionSourceRetainedActive
	accountSelectionSourcePinned
)

type latestAccountScheduleSnapshotStore struct {
	mu      sync.RWMutex
	latest  *LatestAccountScheduleSnapshot
	pending map[string]*LatestAccountScheduleSnapshot
}

func newLatestAccountScheduleSnapshotStore() *latestAccountScheduleSnapshotStore {
	return &latestAccountScheduleSnapshotStore{
		pending: make(map[string]*LatestAccountScheduleSnapshot),
	}
}

func (s *latestAccountScheduleSnapshotStore) saveDraft(snapshot *LatestAccountScheduleSnapshot) {
	if s == nil || snapshot == nil {
		return
	}

	clone := cloneLatestAccountScheduleSnapshot(snapshot)
	now := time.Now()
	if clone.CapturedAt.IsZero() {
		clone.CapturedAt = now
	}
	clone.UpdatedAt = now

	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := now.Add(-latestAccountScheduleSnapshotPendingTTL)
	for id, pending := range s.pending {
		if pending == nil || pending.UpdatedAt.Before(cutoff) {
			delete(s.pending, id)
		}
	}
	if clone.RequestID != "" {
		s.pending[clone.RequestID] = cloneLatestAccountScheduleSnapshot(clone)
	}
	s.latest = clone
}

func (s *latestAccountScheduleSnapshotStore) complete(requestID string, accountID int64, accountName, outcome, finalError string) (*LatestAccountScheduleSnapshot, bool) {
	if s == nil {
		return nil, false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var snapshot *LatestAccountScheduleSnapshot
	if requestID != "" {
		if pending, ok := s.pending[requestID]; ok {
			snapshot = cloneLatestAccountScheduleSnapshot(pending)
		}
	}
	if snapshot == nil {
		// pending 可能因 TTL 被清理；仅允许回退到同一 requestID 的 latest。
		// requestID 不匹配时必须原样保留全局 latest，绝不能拿他人快照继续改写。
		if requestID == "" || s.latest == nil || s.latest.RequestID != requestID {
			return nil, false
		}
		snapshot = cloneLatestAccountScheduleSnapshot(s.latest)
	}

	snapshot.UpdatedAt = time.Now()
	if outcome != "" {
		snapshot.FinalOutcome = outcome
		// outcome 与错误必须成对覆盖；成功终态用空串清掉此前 attempt 错误，避免页面残留失败文案。
		snapshot.FinalError = finalError
	}
	if accountID > 0 {
		snapshot.SelectedAccountID = accountID
	}
	if strings.TrimSpace(accountName) != "" {
		snapshot.SelectedAccountName = accountName
	}

	for idx := range snapshot.Candidates {
		candidate := &snapshot.Candidates[idx]
		if candidate.AccountID != accountID {
			continue
		}
		candidate.RuntimeOutcome = outcome
		candidate.RuntimeError = finalError
		break
	}

	terminal := isTerminalAccountScheduleSnapshotUpdate(outcome, accountID)
	if terminal {
		delete(s.pending, requestID)
	} else {
		s.pending[requestID] = cloneLatestAccountScheduleSnapshot(snapshot)
	}
	s.latest = snapshot
	return cloneLatestAccountScheduleSnapshot(snapshot), true
}

// isTerminalAccountScheduleSnapshotUpdate 统一定义请求级快照的终态边界。
// auth/transient 且带具体账号表示一次候选 attempt；全池耗尽使用 accountID=0 收口。
func isTerminalAccountScheduleSnapshotUpdate(outcome string, accountID int64) bool {
	switch outcome {
	case accountScheduleOutcomeSuccess,
		accountScheduleOutcomePassthroughNoAvailableProvider,
		accountScheduleOutcomePassthroughOther4xx,
		accountScheduleOutcomeNoSchedulableAccounts,
		AccountScheduleOutcomePrivacyBlocked:
		return true
	case accountScheduleOutcomeTransientFailure:
		return accountID <= 0
	case CodexModelRoutingMissingModel,
		CodexModelRoutingUnassigned,
		CodexModelRoutingLoadFailed,
		CodexModelRoutingNoAvailable,
		CodexModelRoutingFixedUnavailable:
		return true
	default:
		return false
	}
}

func (s *latestAccountScheduleSnapshotStore) getLatest() *LatestAccountScheduleSnapshot {
	if s == nil {
		return nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneLatestAccountScheduleSnapshot(s.latest)
}

func (s *latestAccountScheduleSnapshotStore) clear() {
	if s == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.latest = nil
	s.pending = make(map[string]*LatestAccountScheduleSnapshot)
}

func cloneLatestAccountScheduleSnapshot(snapshot *LatestAccountScheduleSnapshot) *LatestAccountScheduleSnapshot {
	if snapshot == nil {
		return nil
	}

	clone := *snapshot
	if len(snapshot.Candidates) > 0 {
		clone.Candidates = make([]AccountScheduleCandidateDecision, len(snapshot.Candidates))
		copy(clone.Candidates, snapshot.Candidates)
	}
	return &clone
}

// AccountScheduleResult 单次调度的公开结果契约。
// Pinned 表示本次调度命中全局固定账号；转发管线只使用该调度时刻的
// 不可变快照值判定 pinned 行为，不得事后回查 runtime cache（避免调度后 pin/unpin 竞态）。
// Snapshot 为本次调度草稿的深拷贝，Preview 路径不带（requestID 为空）。
type AccountScheduleResult struct {
	Accounts []*store.UpstreamAccountRecord
	Pinned   bool
	Snapshot *LatestAccountScheduleSnapshot
}

// SaveLatestAccountScheduleSnapshot 保存由模型路由适配器生成的请求调度草稿。
func (s *AccountPoolService) SaveLatestAccountScheduleSnapshot(ctx context.Context, snapshot *LatestAccountScheduleSnapshot) error {
	_ = ctx
	if s == nil || s.scheduleSnapshots == nil || snapshot == nil {
		return nil
	}
	s.scheduleSnapshots.saveDraft(snapshot)
	return nil
}

func (s *AccountPoolService) PrepareSchedulableAccounts(ctx context.Context, requestID, requestPath string) (AccountScheduleResult, error) {
	return s.prepareSchedulableAccounts(ctx, requestID, requestPath)
}

func (s *AccountPoolService) PreviewSchedulableAccounts(ctx context.Context, requestPath string) (AccountScheduleResult, error) {
	if err := s.ensureRuntimeCache(ctx); err != nil {
		return AccountScheduleResult{}, err
	}
	now := time.Now()
	accounts := s.runtimeCache.listSchedulable(now)
	ordered, _, pinned := s.rankSchedulableAccounts(accounts, now, "", requestPath)
	return AccountScheduleResult{Accounts: ordered, Pinned: pinned}, nil
}

func (s *AccountPoolService) GetLatestAccountScheduleSnapshot(ctx context.Context) (*LatestAccountScheduleSnapshot, error) {
	_ = ctx
	if s.scheduleSnapshots == nil {
		return nil, nil
	}
	return s.scheduleSnapshots.getLatest(), nil
}

// SaveLatestScheduleSnapshot 保存路由分支的调度草稿，使后续终态回填仍作用于同一请求。
// 常规账号池路径在 prepareSchedulableAccounts 内保存；按模型路由绕过该排序器时由代理显式调用。
func (s *AccountPoolService) SaveLatestScheduleSnapshot(snapshot *LatestAccountScheduleSnapshot) {
	if s == nil || s.scheduleSnapshots == nil || snapshot == nil {
		return
	}
	s.scheduleSnapshots.saveDraft(snapshot)
}

func (s *AccountPoolService) CompleteLatestScheduleSnapshot(ctx context.Context, requestID string, accountID int64, accountName, outcome, finalError string) (*LatestAccountScheduleSnapshot, error) {
	_ = ctx
	if s.scheduleSnapshots == nil {
		return nil, nil
	}
	snapshot, _ := s.scheduleSnapshots.complete(requestID, accountID, accountName, outcome, finalError)
	return snapshot, nil
}

func (s *AccountPoolService) prepareSchedulableAccounts(ctx context.Context, requestID, requestPath string) (AccountScheduleResult, error) {
	if err := s.ensureRuntimeCache(ctx); err != nil {
		return AccountScheduleResult{}, err
	}
	now := time.Now()
	accounts := s.runtimeCache.listSchedulable(now)
	ordered, snapshot, pinned := s.rankSchedulableAccounts(accounts, now, requestID, requestPath)
	if s.scheduleSnapshots != nil && snapshot != nil {
		s.scheduleSnapshots.saveDraft(snapshot)
	}
	return AccountScheduleResult{Accounts: ordered, Pinned: pinned, Snapshot: snapshot}, nil
}

func (s *AccountPoolService) rankSchedulableAccounts(accounts []*store.UpstreamAccountRecord, now time.Time, requestID, requestPath string) ([]*store.UpstreamAccountRecord, *LatestAccountScheduleSnapshot, bool) {
	preparedTiers := preparePriorityTiers(accounts, now, requestPath)
	snapshot := &LatestAccountScheduleSnapshot{
		RequestID:   requestID,
		CapturedAt:  now,
		UpdatedAt:   now,
		RequestPath: requestPath,
	}

	selectedTier := (*rankedPriorityTier)(nil)
	selectedIndex := 0
	selectedSource := accountSelectionSourceRanked
	if s != nil && s.runtimeCache != nil {
		requireChatGPTOAuth := requestPath == CodexStandaloneSearchPath
		selectedTier, selectedIndex, selectedSource = s.runtimeCache.resolveActiveSelection(preparedTiers, requireChatGPTOAuth)
	} else {
		selectedTier = selectFirstEligibleTier(preparedTiers)
	}

	if selectedTier == nil {
		snapshot.FinalOutcome = accountScheduleOutcomeNoSchedulableAccounts
		if requestPath == CodexStandaloneSearchPath {
			snapshot.Summary = "当前没有可调度的 ChatGPT OAuth 账号：/v1/alpha/search 不会路由到 api_key 提供者"
		} else {
			snapshot.Summary = "当前没有可调度账号：所有候选均因状态或额度暂不可调度"
		}
		for _, tier := range preparedTiers {
			appendSkippedCandidates(snapshot, tier, tier.skipped)
		}
		return nil, snapshot, false
	}

	snapshot.SelectedPriority = selectedTier.priority
	snapshot.SelectedTierIndex = selectedTier.index
	snapshot.SelectedTierLabel = accountPriorityTierLabel(selectedTier.index)
	snapshot.DegradedToLowerPriority = selectedTier.index > 1
	if len(selectedTier.eligible) > 0 && selectedIndex >= 0 && selectedIndex < len(selectedTier.eligible) {
		snapshot.SelectedAccountID = selectedTier.eligible[selectedIndex].account.ID
		snapshot.SelectedAccountName = displayAccountName(selectedTier.eligible[selectedIndex].account)
	}
	snapshot.Summary = buildScheduleSummary(selectedTier, snapshot.SelectedAccountName)

	ordered := make([]*store.UpstreamAccountRecord, 0, len(selectedTier.eligible))
	for _, tier := range preparedTiers {
		if tier.index < selectedTier.index {
			for _, candidate := range tier.eligible {
				snapshot.Candidates = append(snapshot.Candidates, buildCandidateDecision(candidate, tier, accountScheduleDecisionSkipped, "higher_priority_tier_recovered_but_retained_degraded_tier", "当前仍保持已降级优先级组，未自动切回更高优先级组"))
			}
			appendSkippedCandidates(snapshot, tier, tier.skipped)
			continue
		}
		if tier.index == selectedTier.index {
			for idx, candidate := range selectedTierEligibleOrder(tier.eligible, selectedIndex) {
				decision := accountScheduleDecisionEligible
				reason := "same_tier_lower_rank"
				detail := buildRankingReasonDetail(candidate, idx+1, false, false, false)
				if idx == 0 {
					retainedActive := selectedSource == accountSelectionSourceRetainedActive || selectedSource == accountSelectionSourcePinned
					preferredGroup := selectedSource == accountSelectionSourcePreferred
					decision = accountScheduleDecisionSelected
					reason = "highest_ranked_in_selected_tier"
					if selectedIndex > 0 && retainedActive {
						reason = "retained_active_account_in_selected_tier"
					} else if selectedIndex > 0 && preferredGroup {
						reason = "preferred_account_in_selected_group"
					}
					detail = buildRankingReasonDetail(candidate, idx+1, true, retainedActive && selectedIndex > 0, preferredGroup && selectedIndex > 0)
				}
				snapshot.Candidates = append(snapshot.Candidates, buildCandidateDecision(candidate, tier, decision, reason, detail))
				ordered = append(ordered, candidate.account)
			}
			appendSkippedCandidates(snapshot, tier, tier.skipped)
			continue
		}

		for _, candidate := range tier.eligible {
			snapshot.Candidates = append(snapshot.Candidates, buildCandidateDecision(candidate, tier, accountScheduleDecisionSkipped, "higher_priority_tier_selected", "更高优先级组已有可用账号，本轮未启用该层"))
		}
		appendSkippedCandidates(snapshot, tier, tier.skipped)
	}

	return ordered, snapshot, selectedSource == accountSelectionSourcePinned
}

func selectedTierEligibleOrder(eligible []*rankedSchedulableAccount, selectedIndex int) []*rankedSchedulableAccount {
	if len(eligible) == 0 || selectedIndex <= 0 || selectedIndex >= len(eligible) {
		return eligible
	}

	ordered := make([]*rankedSchedulableAccount, 0, len(eligible))
	ordered = append(ordered, eligible[selectedIndex])
	ordered = append(ordered, eligible[:selectedIndex]...)
	ordered = append(ordered, eligible[selectedIndex+1:]...)
	return ordered
}

func preparePriorityTiers(accounts []*store.UpstreamAccountRecord, now time.Time, requestPath string) []*rankedPriorityTier {
	grouped := groupAccountsByPriority(accounts)
	prepared := make([]*rankedPriorityTier, 0, len(grouped))
	for _, tier := range grouped {
		eligible, skipped := rankPriorityTier(tier.accounts, now, requestPath)
		prepared = append(prepared, &rankedPriorityTier{
			groupKey: tier.groupKey,
			priority: tier.priority,
			index:    tier.index,
			eligible: eligible,
			skipped:  skipped,
		})
	}
	return prepared
}

type groupedPriorityTier struct {
	groupKey string
	priority int
	index    int
	accounts []*store.UpstreamAccountRecord
}

const (
	utilizationBucketKnownOAuth   = 1
	utilizationBucketUnknownOAuth = 2
	utilizationBucketAPIKey       = 3
)

const (
	weeklyGuardrailLowThreshold      = 20.0
	weeklyGuardrailCriticalThreshold = 10.0
	weeklyGuardrailLowFactor         = 0.6
	weeklyGuardrailCriticalFactor    = 0.3
	recentSuccessSuppressionWindow   = time.Minute
	recentSuccessSuppressionFactor   = 0.85
	minResetHours5H                  = 0.25
	minResetHoursWeekly              = 6.0
	sameTierAutoSwitchScoreFactor    = 1.25
)

func groupAccountsByPriority(accounts []*store.UpstreamAccountRecord) []groupedPriorityTier {
	buckets := map[string][]*store.UpstreamAccountRecord{
		accountGroupPrimary: {},
		accountGroupBackup:  {},
		accountGroupCold:    {},
	}

	for _, account := range accounts {
		if account == nil {
			continue
		}
		groupKey := inferAccountGroupKey(account)
		buckets[groupKey] = append(buckets[groupKey], account)
	}

	result := make([]groupedPriorityTier, 0, 3)
	for _, groupKey := range []string{accountGroupPrimary, accountGroupBackup, accountGroupCold} {
		groupAccounts := buckets[groupKey]
		if len(groupAccounts) == 0 {
			continue
		}
		sortAccountsWithinGroup(groupAccounts)
		result = append(result, groupedPriorityTier{
			groupKey: groupKey,
			priority: groupAccounts[0].Priority,
			index:    accountGroupRank(groupKey),
			accounts: groupAccounts,
		})
	}

	return result
}

func rankPriorityTier(accounts []*store.UpstreamAccountRecord, now time.Time, requestPath string) ([]*rankedSchedulableAccount, []*rankedSchedulableAccount) {
	eligible := make([]*rankedSchedulableAccount, 0)
	skipped := make([]*rankedSchedulableAccount, 0)
	for _, account := range accounts {
		candidate := classifySchedulableAccount(account, now)
		if requestPath == CodexStandaloneSearchPath && candidate.account != nil && !accountauth.IsChatGPTOAuthProvider(candidate.account.ProviderType) {
			candidate.skipReason = accountScheduleSkipRouteRequiresChatGPTOAuth
			candidate.skipReasonDetail = "Codex /v1/alpha/search 仅支持 ChatGPT OAuth 账号"
		}
		if candidate.skipReason != "" {
			skipped = append(skipped, candidate)
			continue
		}
		eligible = append(eligible, candidate)
	}

	sort.SliceStable(eligible, func(i, j int) bool {
		left := eligible[i]
		right := eligible[j]
		if left.bucket != right.bucket {
			return left.bucket < right.bucket
		}
		if left.bucket == utilizationBucketKnownOAuth && left.finalScore != right.finalScore {
			return left.finalScore > right.finalScore
		}
		if left.account.FailCount != right.account.FailCount {
			return left.account.FailCount < right.account.FailCount
		}
		if cmp := compareOptionalTimeDesc(left.account.LastSuccessAt, right.account.LastSuccessAt); cmp != 0 {
			return cmp > 0
		}
		return left.account.ID < right.account.ID
	})

	return eligible, skipped
}

func classifySchedulableAccount(account *store.UpstreamAccountRecord, now time.Time) *rankedSchedulableAccount {
	candidate := &rankedSchedulableAccount{account: account}
	if account == nil {
		candidate.skipReason = "invalid_account"
		candidate.skipReasonDetail = "账号记录为空，无法参与调度"
		return candidate
	}

	providerType := accountauth.NormalizeProviderType(account.ProviderType)
	quotaStatus := normalizeQuotaStatus(account.QuotaStatus)
	candidate.effectiveQuotaRemaining = effectiveQuotaRemaining(account)
	candidate.remaining5H, candidate.hoursToReset5H = quotaWindowUtilization(account.Quota5HUsedPercent, account.Quota5HResetAt, now)
	candidate.remainingWeekly, candidate.hoursToResetWeekly = quotaWindowUtilization(account.QuotaWeeklyUsedPercent, account.QuotaWeeklyResetAt, now)

	if providerType == accountauth.ProviderAPIKey {
		candidate.bucket = utilizationBucketAPIKey
		return candidate
	}

	if quotaStatus == "exhausted" && hasExhaustedQuotaReset(account, now) {
		candidate.skipReason = "quota_exhausted_until_reset"
		candidate.skipReasonDetail = "额度已耗尽且重置时间未到，本轮暂不参与调度"
		return candidate
	}

	if quotaStatus != "ok" {
		candidate.bucket = utilizationBucketUnknownOAuth
		return candidate
	}

	score, knownQuota := utilizationScore(account, candidate)
	if !knownQuota {
		candidate.bucket = utilizationBucketUnknownOAuth
		return candidate
	}

	candidate.bucket = utilizationBucketKnownOAuth
	candidate.finalScore, candidate.weeklyGuardrailApplied = applyWeeklyGuardrail(score, candidate.remainingWeekly)
	candidate.finalScore = applyRecentSuccessSuppression(candidate.finalScore, account.LastSuccessAt, now)
	return candidate
}

func effectiveQuotaRemaining(account *store.UpstreamAccountRecord) *float64 {
	if account == nil {
		return nil
	}
	if accountauth.NormalizeProviderType(account.ProviderType) == accountauth.ProviderAPIKey {
		return nil
	}

	planType := strings.TrimSpace(strings.ToLower(account.PlanType))
	if planType == "free" {
		return usedPercentToRemaining(account.QuotaWeeklyUsedPercent)
	}

	remaining5H := usedPercentToRemaining(account.Quota5HUsedPercent)
	remainingWeekly := usedPercentToRemaining(account.QuotaWeeklyUsedPercent)
	if remaining5H != nil && remainingWeekly != nil {
		if *remaining5H <= *remainingWeekly {
			return remaining5H
		}
		return remainingWeekly
	}
	if remaining5H != nil {
		return remaining5H
	}
	return remainingWeekly
}

func usedPercentToRemaining(used *float64) *float64 {
	if used == nil {
		return nil
	}
	remaining := 100 - *used
	if remaining < 0 {
		remaining = 0
	}
	if remaining > 100 {
		remaining = 100
	}
	return &remaining
}

func quotaWindowUtilization(usedPercent *float64, resetAt *time.Time, now time.Time) (*float64, *float64) {
	remaining := usedPercentToRemaining(usedPercent)
	if remaining == nil || resetAt == nil {
		return remaining, nil
	}
	hours := resetAt.Sub(now).Hours()
	if hours <= 0 {
		return remaining, nil
	}
	return remaining, &hours
}

func utilizationScore(account *store.UpstreamAccountRecord, candidate *rankedSchedulableAccount) (float64, bool) {
	if account == nil || candidate == nil {
		return 0, false
	}

	planType := strings.TrimSpace(strings.ToLower(account.PlanType))
	if planType == "free" {
		if candidate.remainingWeekly == nil || candidate.hoursToResetWeekly == nil {
			return 0, false
		}
		return pressureScore(*candidate.remainingWeekly, *candidate.hoursToResetWeekly, minResetHoursWeekly), true
	}

	score := 0.0
	knownQuota := false
	if candidate.remaining5H != nil && candidate.hoursToReset5H != nil {
		score += pressureScore(*candidate.remaining5H, *candidate.hoursToReset5H, minResetHours5H)
		knownQuota = true
	}
	if candidate.remainingWeekly != nil && candidate.hoursToResetWeekly != nil {
		score += 0.2 * pressureScore(*candidate.remainingWeekly, *candidate.hoursToResetWeekly, minResetHoursWeekly)
		knownQuota = true
	}

	return score, knownQuota
}

func pressureScore(remainingPercent, hoursToReset, minHours float64) float64 {
	if hoursToReset < minHours {
		hoursToReset = minHours
	}
	if hoursToReset <= 0 {
		return 0
	}
	return remainingPercent / hoursToReset
}

func applyWeeklyGuardrail(score float64, remainingWeekly *float64) (float64, bool) {
	if remainingWeekly == nil {
		return score, false
	}

	switch {
	case *remainingWeekly <= weeklyGuardrailCriticalThreshold:
		return score * weeklyGuardrailCriticalFactor, true
	case *remainingWeekly <= weeklyGuardrailLowThreshold:
		return score * weeklyGuardrailLowFactor, true
	default:
		return score, false
	}
}

func applyRecentSuccessSuppression(score float64, lastSuccessAt *time.Time, now time.Time) float64 {
	if lastSuccessAt == nil {
		return score
	}
	if now.Sub(*lastSuccessAt) > recentSuccessSuppressionWindow {
		return score
	}
	return score * recentSuccessSuppressionFactor
}

func hasExhaustedQuotaReset(account *store.UpstreamAccountRecord, now time.Time) bool {
	if account == nil {
		return false
	}

	planType := strings.TrimSpace(strings.ToLower(account.PlanType))
	if planType == "free" {
		return quotaWindowExhaustedUntilReset(account.QuotaWeeklyUsedPercent, account.QuotaWeeklyResetAt, now)
	}

	return quotaWindowExhaustedUntilReset(account.Quota5HUsedPercent, account.Quota5HResetAt, now) ||
		quotaWindowExhaustedUntilReset(account.QuotaWeeklyUsedPercent, account.QuotaWeeklyResetAt, now)
}

func quotaWindowExhaustedUntilReset(usedPercent *float64, resetAt *time.Time, now time.Time) bool {
	if usedPercent == nil || resetAt == nil || !resetAt.After(now) {
		return false
	}
	return *usedPercent >= 99.999
}

func normalizeQuotaStatus(raw string) string {
	return strings.TrimSpace(strings.ToLower(raw))
}

func compareOptionalTimeDesc(left, right *time.Time) int {
	switch {
	case left != nil && right == nil:
		return 1
	case left == nil && right != nil:
		return -1
	case left == nil && right == nil:
		return 0
	case left.After(*right):
		return 1
	case left.Before(*right):
		return -1
	default:
		return 0
	}
}

func accountPriorityTierLabel(index int) string {
	switch index {
	case 1:
		return "主组"
	case 2:
		return "备组"
	case 3:
		return "冷备"
	default:
		if index <= 0 {
			return "未分层"
		}
		return fmt.Sprintf("第 %d 层", index)
	}
}

func displayAccountName(account *store.UpstreamAccountRecord) string {
	if account == nil {
		return ""
	}
	if name := strings.TrimSpace(account.AccountName); name != "" {
		return name
	}
	return fmt.Sprintf("account-%d", account.ID)
}

func buildCandidateDecision(candidate *rankedSchedulableAccount, tier *rankedPriorityTier, decision, reason, reasonDetail string) AccountScheduleCandidateDecision {
	item := AccountScheduleCandidateDecision{
		Decision:     decision,
		Reason:       reason,
		ReasonDetail: reasonDetail,
	}
	if candidate == nil || candidate.account == nil || tier == nil {
		return item
	}
	item.AccountID = candidate.account.ID
	item.AccountName = displayAccountName(candidate.account)
	item.ProviderType = accountauth.NormalizeProviderType(candidate.account.ProviderType)
	item.Priority = candidate.account.Priority
	item.TierIndex = tier.index
	item.TierLabel = accountPriorityTierLabel(tier.index)
	item.QuotaStatus = normalizeQuotaStatus(candidate.account.QuotaStatus)
	item.EffectiveQuotaRemaining = candidate.effectiveQuotaRemaining
	item.FailCount = candidate.account.FailCount
	item.LastSuccessAt = candidate.account.LastSuccessAt
	return item
}

func buildRankingReasonDetail(candidate *rankedSchedulableAccount, rank int, selected, retainedActive, preferredGroup bool) string {
	if candidate == nil || candidate.account == nil {
		if selected {
			return "当前优先级组内排序第一"
		}
		return fmt.Sprintf("同层排序位次 %d", rank)
	}

	rankDetail := fmt.Sprintf("同层排序位次 %d", rank)
	if selected {
		rankDetail = "当前优先级组内排序第一"
	}

	switch candidate.bucket {
	case utilizationBucketKnownOAuth:
		return buildKnownQuotaReasonDetail(candidate, rankDetail, retainedActive, preferredGroup)
	case utilizationBucketUnknownOAuth:
		if retainedActive {
			return fmt.Sprintf("保持当前活跃账号，未因同组分数变化切换；quota 信息未知，保留候选但劣后于已知 OAuth；%s", rankDetail)
		}
		if preferredGroup {
			return fmt.Sprintf("当前组已指定首选账号，本轮优先命中；quota 信息未知，保留候选但劣后于已知 OAuth；%s", rankDetail)
		}
		return fmt.Sprintf("quota 信息未知，保留候选但劣后于已知 OAuth；%s", rankDetail)
	case utilizationBucketAPIKey:
		if retainedActive {
			return fmt.Sprintf("保持当前活跃账号，未因同组分数变化切换；api_key 账号作为不重置兜底，排在 OAuth 后；%s", rankDetail)
		}
		if preferredGroup {
			return fmt.Sprintf("当前组已指定首选账号，本轮优先命中；api_key 账号作为不重置兜底，排在 OAuth 后；%s", rankDetail)
		}
		return fmt.Sprintf("api_key 账号作为不重置兜底，排在 OAuth 后；%s", rankDetail)
	default:
		return rankDetail
	}
}

func buildKnownQuotaReasonDetail(candidate *rankedSchedulableAccount, rankDetail string, retainedActive, preferredGroup bool) string {
	parts := make([]string, 0, 4)
	if candidate.remaining5H != nil && candidate.hoursToReset5H != nil {
		parts = append(parts, fmt.Sprintf("5h 窗口 %.1f 小时后重置，剩余 %.0f%%", *candidate.hoursToReset5H, *candidate.remaining5H))
	} else if candidate.remainingWeekly != nil && candidate.hoursToResetWeekly != nil {
		parts = append(parts, fmt.Sprintf("weekly 窗口 %.1f 小时后重置，剩余 %.0f%%", *candidate.hoursToResetWeekly, *candidate.remainingWeekly))
	}
	if candidate.weeklyGuardrailApplied && candidate.remainingWeekly != nil {
		parts = append(parts, fmt.Sprintf("周额度仅剩 %.0f%%，已触发护栏降权", *candidate.remainingWeekly))
	}
	if retainedActive {
		parts = append(parts, "保持当前活跃账号，未因同组分数变化切换")
	} else if preferredGroup {
		parts = append(parts, "当前组已指定首选账号，本轮优先命中")
	}
	parts = append(parts, rankDetail)
	return strings.Join(parts, "；")
}

func appendSkippedCandidates(snapshot *LatestAccountScheduleSnapshot, tier *rankedPriorityTier, candidates []*rankedSchedulableAccount) {
	if snapshot == nil || tier == nil {
		return
	}
	for _, candidate := range candidates {
		reason := candidate.skipReason
		detail := candidate.skipReasonDetail
		if reason == "" {
			reason = "not_selected"
			detail = "当前不参与调度"
		}
		snapshot.Candidates = append(snapshot.Candidates, buildCandidateDecision(candidate, tier, accountScheduleDecisionSkipped, reason, detail))
	}
}

func buildScheduleSummary(selectedTier *rankedPriorityTier, accountName string) string {
	if selectedTier == nil {
		return "当前没有可调度账号"
	}
	if selectedTier.index <= 1 {
		return fmt.Sprintf("%s可用，优先选择 %s", accountPriorityTierLabel(selectedTier.index), accountName)
	}
	return fmt.Sprintf("更高优先级组当前不可用，已降级到%s并优先选择 %s", accountPriorityTierLabel(selectedTier.index), accountName)
}
