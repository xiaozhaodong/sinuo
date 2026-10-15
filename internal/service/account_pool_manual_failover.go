package service

import (
	"context"
	"fmt"
	"sort"

	"sinuo/internal/store"
)

type groupedManualFailoverTier struct {
	priority int
	accounts []*store.UpstreamAccountRecord
}

func targetGroupKeyFromTierIndex(targetTierIndex int) string {
	switch targetTierIndex {
	case 1:
		return accountGroupBackup
	case 2:
		return accountGroupCold
	default:
		return accountGroupPrimary
	}
}

func absGroupRankDelta(left, right string) int {
	delta := accountGroupRank(left) - accountGroupRank(right)
	if delta < 0 {
		return -delta
	}
	return delta
}

func firstAccountIDInGroup(accounts []*store.UpstreamAccountRecord) int64 {
	if len(accounts) == 0 {
		return 0
	}

	sorted := append([]*store.UpstreamAccountRecord(nil), accounts...)
	sortAccountsWithinGroup(sorted)
	for _, account := range sorted {
		if account != nil && account.ID > 0 {
			return account.ID
		}
	}
	return 0
}

func hasExplicitAccountGroup(accounts []*store.UpstreamAccountRecord) bool {
	for _, account := range accounts {
		if account == nil {
			continue
		}
		if normalizeAccountGroupKey(account.GroupKey) != "" {
			return true
		}
	}
	return false
}

// MoveAccountToTier 将账号移动到指定手动主备层。
func (s *AccountPoolService) MoveAccountToTier(ctx context.Context, accountID int64, targetTierIndex int) (bool, error) {
	if err := s.ensureRuntimeCache(ctx); err != nil {
		return false, err
	}
	if accountID <= 0 {
		return false, fmt.Errorf("无效的账号 ID: %d", accountID)
	}

	var (
		previousActiveID  int64
		hadPreviousActive bool
	)
	if s.runtimeCache != nil {
		previousActiveID, hadPreviousActive = s.runtimeCache.activeSelectionAccountID()
	}

	accounts, err := s.ListAccounts(ctx, true)
	if err != nil {
		return false, fmt.Errorf("加载账号列表失败: %w", err)
	}
	if hasExplicitAccountGroup(accounts) {
		return s.moveAccountToExplicitGroup(ctx, accounts, accountID, targetGroupKeyFromTierIndex(targetTierIndex), previousActiveID, hadPreviousActive)
	}
	tiers := buildManualFailoverTierGroups(accounts)
	if targetTierIndex > 0 && len(tiers) <= targetTierIndex {
		return false, nil
	}

	updates, err := buildManualFailoverPriorityUpdates(accounts, accountID, targetTierIndex)
	if err != nil {
		return false, err
	}
	if len(updates) == 0 {
		return false, nil
	}

	if err := s.store.UpdateAccountPriorities(ctx, updates); err != nil {
		return false, err
	}
	s.runtimeCache.applyPriorityUpdates(updates)
	selectionChanged := false
	if hadPreviousActive {
		selectionChanged = s.runtimeCache.selectAccount(previousActiveID)
	}
	if selectionChanged && s.softFailureTracker != nil {
		s.softFailureTracker.Clear(previousActiveID)
	}
	if s.scheduleSnapshots != nil {
		s.scheduleSnapshots.clear()
	}
	return len(updates) > 0 || selectionChanged, nil
}

func (s *AccountPoolService) moveAccountToExplicitGroup(ctx context.Context, accounts []*store.UpstreamAccountRecord, accountID int64, targetGroupKey string, previousActiveID int64, hadPreviousActive bool) (bool, error) {
	targetGroupKey = normalizeAccountGroupKey(targetGroupKey)
	if targetGroupKey == "" {
		targetGroupKey = accountGroupPrimary
	}

	var targetAccount *store.UpstreamAccountRecord
	for _, account := range accounts {
		if account == nil || account.ID != accountID {
			continue
		}
		targetAccount = account
		break
	}
	if targetAccount == nil {
		return false, fmt.Errorf("账号不存在: %d", accountID)
	}

	currentGroupKey := inferAccountGroupKey(targetAccount)
	if currentGroupKey == targetGroupKey {
		return false, nil
	}

	groupBuckets := map[string][]*store.UpstreamAccountRecord{
		accountGroupPrimary: {},
		accountGroupBackup:  {},
		accountGroupCold:    {},
	}
	for _, account := range accounts {
		if account == nil {
			continue
		}
		groupKey := inferAccountGroupKey(account)
		if account.ID == accountID {
			groupKey = targetGroupKey
		}
		groupBuckets[groupKey] = append(groupBuckets[groupKey], account)
	}

	updates := make(map[int64]store.AccountSchedulingUpdate)
	for _, groupKey := range []string{accountGroupPrimary, accountGroupBackup, accountGroupCold} {
		bucket := groupBuckets[groupKey]
		if len(bucket) == 0 {
			continue
		}
		sortAccountsWithinGroup(bucket)
		if groupKey == targetGroupKey {
			sort.SliceStable(bucket, func(i, j int) bool {
				left := bucket[i]
				right := bucket[j]
				if left == nil {
					return false
				}
				if right == nil {
					return true
				}
				if left.ID == accountID {
					return true
				}
				if right.ID == accountID {
					return false
				}
				if left.Priority != right.Priority {
					return left.Priority < right.Priority
				}
				return left.ID < right.ID
			})
		}
		for index, account := range bucket {
			if account == nil || account.ID <= 0 {
				continue
			}
			nextPriority := (index + 1) * 10
			updates[account.ID] = store.AccountSchedulingUpdate{
				GroupKey: groupKey,
				Priority: nextPriority,
			}
		}
	}

	if len(updates) > 0 {
		if err := s.store.UpdateAccountScheduling(ctx, updates); err != nil {
			return false, err
		}
		s.runtimeCache.applySchedulingUpdates(updates)
	}

	selectionChanged := false
	if hadPreviousActive {
		selectionChanged = s.runtimeCache.selectAccount(previousActiveID)
	}
	if selectionChanged && s.softFailureTracker != nil {
		s.softFailureTracker.Clear(previousActiveID)
	}
	if s.scheduleSnapshots != nil {
		s.scheduleSnapshots.clear()
	}
	return selectionChanged || len(updates) > 0, nil
}

func (s *AccountPoolService) SwapAccountGroups(ctx context.Context, sourceGroupKey, targetGroupKey string) (bool, error) {
	if err := s.ensureRuntimeCache(ctx); err != nil {
		return false, err
	}

	sourceGroupKey = normalizeAccountGroupKey(sourceGroupKey)
	targetGroupKey = normalizeAccountGroupKey(targetGroupKey)
	if sourceGroupKey == "" || targetGroupKey == "" {
		return false, fmt.Errorf("无效的组别: %s -> %s", sourceGroupKey, targetGroupKey)
	}
	if sourceGroupKey == targetGroupKey {
		return false, nil
	}
	if absGroupRankDelta(sourceGroupKey, targetGroupKey) != 1 {
		return false, fmt.Errorf("仅支持相邻组整组交换: %s <-> %s", sourceGroupKey, targetGroupKey)
	}

	accounts, err := s.ListAccounts(ctx, true)
	if err != nil {
		return false, fmt.Errorf("加载账号列表失败: %w", err)
	}

	groupBuckets := map[string][]*store.UpstreamAccountRecord{
		accountGroupPrimary: {},
		accountGroupBackup:  {},
		accountGroupCold:    {},
	}
	for _, account := range accounts {
		if account == nil {
			continue
		}
		groupBuckets[inferAccountGroupKey(account)] = append(groupBuckets[inferAccountGroupKey(account)], account)
	}

	sourceBucket := append([]*store.UpstreamAccountRecord(nil), groupBuckets[sourceGroupKey]...)
	targetBucket := append([]*store.UpstreamAccountRecord(nil), groupBuckets[targetGroupKey]...)
	if len(sourceBucket) == 0 && len(targetBucket) == 0 {
		return false, nil
	}

	var (
		previousActiveID  int64
		hadPreviousActive bool
		preferredSnapshot map[string]int64
	)
	if s.runtimeCache != nil {
		previousActiveID, hadPreviousActive = s.runtimeCache.activeSelectionAccountID()
		preferredSnapshot = s.runtimeCache.snapshotPreferredAccounts()
	}

	groupBuckets[sourceGroupKey] = targetBucket
	groupBuckets[targetGroupKey] = sourceBucket

	updates := make(map[int64]store.AccountSchedulingUpdate)
	for _, groupKey := range []string{accountGroupPrimary, accountGroupBackup, accountGroupCold} {
		bucket := append([]*store.UpstreamAccountRecord(nil), groupBuckets[groupKey]...)
		if len(bucket) == 0 {
			continue
		}
		sortAccountsWithinGroup(bucket)
		for index, account := range bucket {
			if account == nil || account.ID <= 0 {
				continue
			}
			updates[account.ID] = store.AccountSchedulingUpdate{
				GroupKey: groupKey,
				Priority: (index + 1) * 10,
			}
		}
	}

	if len(updates) == 0 {
		return false, nil
	}

	if err := s.store.UpdateAccountScheduling(ctx, updates); err != nil {
		return false, err
	}
	s.runtimeCache.applySchedulingUpdates(updates)

	remappedPreferred := make(map[string]int64, len(preferredSnapshot))
	for groupKey, accountID := range preferredSnapshot {
		normalizedGroupKey := normalizeAccountGroupKey(groupKey)
		switch normalizedGroupKey {
		case sourceGroupKey:
			remappedPreferred[targetGroupKey] = accountID
		case targetGroupKey:
			remappedPreferred[sourceGroupKey] = accountID
		default:
			remappedPreferred[normalizedGroupKey] = accountID
		}
	}
	s.runtimeCache.restorePreferredAccounts(remappedPreferred)

	selectedAccountID := int64(0)
	if hadPreviousActive {
		selectedAccountID = previousActiveID
	}
	if selectedAccountID == 0 && s.runtimeCache != nil {
		selectedAccountID = s.runtimeCache.preferredAccountID(accountGroupPrimary)
	}
	if selectedAccountID == 0 {
		selectedAccountID = firstAccountIDInGroup(groupBuckets[accountGroupPrimary])
	}

	selectionChanged := false
	if selectedAccountID > 0 {
		selectionChanged = s.runtimeCache.selectAccount(selectedAccountID)
		if s.softFailureTracker != nil {
			s.softFailureTracker.Clear(selectedAccountID)
		}
	}
	if s.scheduleSnapshots != nil {
		s.scheduleSnapshots.clear()
	}

	return len(updates) > 0 || selectionChanged, nil
}

func buildManualFailoverPriorityUpdates(accounts []*store.UpstreamAccountRecord, targetAccountID int64, targetTierIndex int) (map[int64]int, error) {
	tiers := buildManualFailoverTierGroups(accounts)
	if len(tiers) == 0 {
		return nil, fmt.Errorf("当前没有可调整顺序的账号")
	}

	var targetTier *groupedManualFailoverTier
	remainingTiers := make([]groupedManualFailoverTier, 0, len(tiers))
	for _, tier := range tiers {
		containsTarget := false
		for _, account := range tier.accounts {
			if account == nil {
				continue
			}
			if account.ID == targetAccountID {
				containsTarget = true
				break
			}
		}

		if containsTarget {
			copiedAccounts := make([]*store.UpstreamAccountRecord, 0, len(tier.accounts))
			copiedAccounts = append(copiedAccounts, tier.accounts...)
			targetTier = &groupedManualFailoverTier{
				priority: tier.priority,
				accounts: copiedAccounts,
			}
			continue
		}
		remainingTiers = append(remainingTiers, tier)
	}
	if targetTier == nil {
		return nil, fmt.Errorf("账号不存在: %d", targetAccountID)
	}

	if targetTierIndex < 0 {
		targetTierIndex = 0
	}
	if targetTierIndex > len(remainingTiers) {
		targetTierIndex = len(remainingTiers)
	}

	remainingTiers = append(remainingTiers[:targetTierIndex], append([]groupedManualFailoverTier{*targetTier}, remainingTiers[targetTierIndex:]...)...)

	updates := make(map[int64]int)
	for index, tier := range remainingTiers {
		nextPriority := (index + 1) * 10
		for _, account := range tier.accounts {
			if account == nil || account.ID <= 0 {
				continue
			}
			if account.Priority == nextPriority {
				continue
			}
			updates[account.ID] = nextPriority
		}
	}

	return updates, nil
}

func buildManualFailoverTierGroups(accounts []*store.UpstreamAccountRecord) []groupedManualFailoverTier {
	sorted := append([]*store.UpstreamAccountRecord(nil), accounts...)
	sort.SliceStable(sorted, func(i, j int) bool {
		left := sorted[i]
		right := sorted[j]
		if left == nil {
			return false
		}
		if right == nil {
			return true
		}
		if left.Priority != right.Priority {
			return left.Priority < right.Priority
		}
		return left.ID < right.ID
	})

	groups := make([]groupedManualFailoverTier, 0)
	for _, account := range sorted {
		if account == nil {
			continue
		}
		if len(groups) == 0 || groups[len(groups)-1].priority != account.Priority {
			groups = append(groups, groupedManualFailoverTier{
				priority: account.Priority,
				accounts: []*store.UpstreamAccountRecord{account},
			})
			continue
		}
		groups[len(groups)-1].accounts = append(groups[len(groups)-1].accounts, account)
	}
	return groups
}
