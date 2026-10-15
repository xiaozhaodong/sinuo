package service

import (
	"sort"
	"sync"
	"time"

	"sinuo/config"
	"sinuo/internal/accountauth"
	"sinuo/internal/store"
)

type accountRuntimeCache struct {
	mu                  sync.RWMutex
	byID                map[int64]*store.UpstreamAccountRecord
	stateVersions       map[int64]uint64
	ordered             []*store.UpstreamAccountRecord
	preferredByGroup    map[string]int64
	autoRetainedByGroup map[string]int64
	activeTier          int
	activeAccount       int64
	selectionPinned     bool
}

type accountRuntimeStateSnapshot struct {
	enabled         bool
	state           string
	cooldownUntil   *time.Time
	failCount       int
	lastSuccessAt   *time.Time
	lastError       string
	updatedAt       time.Time
	stateVersion    uint64
	activeTier      int
	activeAccount   int64
	selectionPinned bool
}

func newAccountRuntimeCache() *accountRuntimeCache {
	return &accountRuntimeCache{
		byID:                make(map[int64]*store.UpstreamAccountRecord),
		stateVersions:       make(map[int64]uint64),
		preferredByGroup:    make(map[string]int64),
		autoRetainedByGroup: make(map[string]int64),
	}
}

func (c *accountRuntimeCache) replaceAll(records []*store.UpstreamAccountRecord) {
	if c == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	existingVersions := c.stateVersions
	c.byID = make(map[int64]*store.UpstreamAccountRecord, len(records))
	c.stateVersions = make(map[int64]uint64, len(records))
	for _, record := range records {
		if record == nil || record.ID <= 0 {
			continue
		}
		c.byID[record.ID] = cloneUpstreamAccountRecord(record)
		if existing, ok := existingVersions[record.ID]; ok && existing > 0 {
			c.stateVersions[record.ID] = existing
		} else {
			c.stateVersions[record.ID] = 1
		}
	}
	if c.activeAccount > 0 {
		if _, ok := c.byID[c.activeAccount]; !ok {
			c.activeAccount = 0
		}
	}
	c.syncPreferredAccountsLocked()
	c.syncAutoRetainedAccountsLocked()
	c.syncPinnedSelectionLocked()
	if c.activeTier > 0 && !c.hasPriorityLocked(c.activeTier) {
		c.clearSelectionLocked()
	} else if !c.selectionPinned {
		c.clearSelectionLocked()
	}
	c.rebuildOrderedLocked()
}

func (c *accountRuntimeCache) upsert(record *store.UpstreamAccountRecord) {
	if c == nil || record == nil || record.ID <= 0 {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.byID[record.ID] = cloneUpstreamAccountRecord(record)
	if _, ok := c.stateVersions[record.ID]; !ok {
		c.stateVersions[record.ID] = 1
	}
	c.syncPreferredAccountsLocked()
	c.syncAutoRetainedAccountsLocked()
	c.syncPinnedSelectionLocked()
	c.rebuildOrderedLocked()
}

func (c *accountRuntimeCache) mergeRecordPreservingRuntimeState(record *store.UpstreamAccountRecord) bool {
	if c == nil || record == nil || record.ID <= 0 {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	current := c.byID[record.ID]
	if current == nil {
		return false
	}

	runtimeEnabled := current.Enabled
	runtimeState := current.State
	runtimeCooldownUntil := cloneTimePtr(current.CooldownUntil)
	runtimeFailCount := current.FailCount
	runtimeLastSuccessAt := cloneTimePtr(current.LastSuccessAt)
	runtimeLastError := current.LastError
	runtimeUpdatedAt := current.UpdatedAt

	merged := cloneUpstreamAccountRecord(record)
	merged.Enabled = runtimeEnabled
	merged.State = runtimeState
	merged.CooldownUntil = runtimeCooldownUntil
	merged.FailCount = runtimeFailCount
	merged.LastSuccessAt = runtimeLastSuccessAt
	merged.LastError = runtimeLastError
	merged.UpdatedAt = runtimeUpdatedAt

	c.byID[record.ID] = merged
	c.syncPreferredAccountsLocked()
	c.syncAutoRetainedAccountsLocked()
	c.syncPinnedSelectionLocked()
	c.rebuildOrderedLocked()
	return true
}

func (c *accountRuntimeCache) remove(id int64) {
	if c == nil || id <= 0 {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.byID, id)
	delete(c.stateVersions, id)
	if c.activeAccount == id {
		c.activeAccount = 0
	}
	c.syncPreferredAccountsLocked()
	c.syncAutoRetainedAccountsLocked()
	if c.activeTier > 0 && !c.hasPriorityLocked(c.activeTier) {
		c.clearSelectionLocked()
	} else if !c.selectionPinned {
		c.clearSelectionLocked()
	}
	c.rebuildOrderedLocked()
}

func (c *accountRuntimeCache) applyPriorityUpdates(updates map[int64]int) {
	if c == nil || len(updates) == 0 {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	for id, priority := range updates {
		record := c.byID[id]
		if record == nil {
			continue
		}
		record.Priority = priority
	}
	c.syncPreferredAccountsLocked()
	c.syncAutoRetainedAccountsLocked()
	c.clearSelectionLocked()
	c.rebuildOrderedLocked()
}

func (c *accountRuntimeCache) applySchedulingUpdates(updates map[int64]store.AccountSchedulingUpdate) {
	if c == nil || len(updates) == 0 {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	for id, update := range updates {
		record := c.byID[id]
		if record == nil {
			continue
		}
		record.GroupKey = normalizeAccountGroupKey(update.GroupKey)
		record.Priority = update.Priority
	}
	c.syncPreferredAccountsLocked()
	c.syncAutoRetainedAccountsLocked()
	c.clearSelectionLocked()
	c.rebuildOrderedLocked()
}

func (c *accountRuntimeCache) setPreferredAccountInGroup(groupKey string, id int64) bool {
	if c == nil || id <= 0 {
		return false
	}

	groupKey = normalizeAccountGroupKey(groupKey)
	if groupKey == "" {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	record := c.byID[id]
	if record == nil || inferAccountGroupKey(record) != groupKey {
		return false
	}
	changed := c.preferredByGroup[groupKey] != id
	c.preferredByGroup[groupKey] = id
	return changed
}

func (c *accountRuntimeCache) swapPreferredAccounts(sourceGroupKey, targetGroupKey string) {
	if c == nil {
		return
	}

	sourceGroupKey = normalizeAccountGroupKey(sourceGroupKey)
	targetGroupKey = normalizeAccountGroupKey(targetGroupKey)
	if sourceGroupKey == "" || targetGroupKey == "" || sourceGroupKey == targetGroupKey {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	left := c.preferredByGroup[sourceGroupKey]
	right := c.preferredByGroup[targetGroupKey]
	if right > 0 {
		c.preferredByGroup[sourceGroupKey] = right
	} else {
		delete(c.preferredByGroup, sourceGroupKey)
	}
	if left > 0 {
		c.preferredByGroup[targetGroupKey] = left
	} else {
		delete(c.preferredByGroup, targetGroupKey)
	}
	c.syncPreferredAccountsLocked()
}

func (c *accountRuntimeCache) snapshotPreferredAccounts() map[string]int64 {
	if c == nil {
		return nil
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	if len(c.preferredByGroup) == 0 {
		return nil
	}

	snapshot := make(map[string]int64, len(c.preferredByGroup))
	for groupKey, accountID := range c.preferredByGroup {
		snapshot[groupKey] = accountID
	}
	return snapshot
}

func (c *accountRuntimeCache) groupPreferredAccountIDs() map[string]int64 {
	return c.snapshotPreferredAccounts()
}

func (c *accountRuntimeCache) restorePreferredAccounts(preferred map[string]int64) {
	if c == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.preferredByGroup = make(map[string]int64, len(preferred))
	for groupKey, accountID := range preferred {
		groupKey = normalizeAccountGroupKey(groupKey)
		if groupKey == "" || accountID <= 0 {
			continue
		}
		record := c.byID[accountID]
		if record == nil || inferAccountGroupKey(record) != groupKey {
			continue
		}
		c.preferredByGroup[groupKey] = accountID
	}
}

func (c *accountRuntimeCache) preferredAccountID(groupKey string) int64 {
	if c == nil {
		return 0
	}

	groupKey = normalizeAccountGroupKey(groupKey)
	if groupKey == "" {
		return 0
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	accountID := c.preferredByGroup[groupKey]
	if accountID <= 0 {
		return 0
	}
	record := c.byID[accountID]
	if record == nil || inferAccountGroupKey(record) != groupKey {
		return 0
	}
	return accountID
}

func (c *accountRuntimeCache) preferredSelectionIndex(groupKey string, eligible []*rankedSchedulableAccount) (int, bool) {
	if c == nil || len(eligible) == 0 {
		return 0, false
	}

	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.preferredSelectionIndexLocked(groupKey, eligible)
}

func (c *accountRuntimeCache) retainedSelectionIndex(groupKey string, eligible []*rankedSchedulableAccount) (int, bool) {
	if c == nil || len(eligible) == 0 {
		return 0, false
	}

	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.retainedSelectionIndexLocked(groupKey, eligible)
}

func (c *accountRuntimeCache) hasPinnedSelection() bool {
	if c == nil {
		return false
	}

	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.selectionPinned && c.activeAccount > 0
}

func (c *accountRuntimeCache) selectAccount(id int64) bool {
	if c == nil || id <= 0 {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	record := c.byID[id]
	if record == nil {
		return false
	}

	selectionTier := accountGroupRank(inferAccountGroupKey(record))
	changed := c.activeTier != selectionTier || c.activeAccount != id
	c.activeTier = selectionTier
	c.activeAccount = id
	c.selectionPinned = true
	return changed
}

func (c *accountRuntimeCache) clearPinnedSelection() bool {
	if c == nil {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.selectionPinned && c.activeTier == 0 && c.activeAccount == 0 {
		return false
	}
	c.clearSelectionLocked()
	return true
}

func (c *accountRuntimeCache) resolveActiveSelection(prepared []*rankedPriorityTier, requireChatGPTOAuth bool) (*rankedPriorityTier, int, accountSelectionSource) {
	if c == nil {
		return selectFirstEligibleTier(prepared), 0, accountSelectionSourceRanked
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	ignorePinnedSelection := false
	if requireChatGPTOAuth && c.selectionPinned && c.activeAccount > 0 {
		pinnedAccount := c.byID[c.activeAccount]
		ignorePinnedSelection = pinnedAccount != nil && !accountauth.IsChatGPTOAuthProvider(pinnedAccount.ProviderType)
	}
	selectedTier, selectedIndex, selectedSource := c.resolveActiveSelectionLocked(prepared, ignorePinnedSelection)
	if selectedTier == nil {
		if !c.selectionPinned {
			c.clearSelectionLocked()
		}
		return nil, -1, accountSelectionSourceRanked
	}

	if selectedIndex < 0 || selectedIndex >= len(selectedTier.eligible) {
		selectedIndex = 0
	}
	return selectedTier, selectedIndex, selectedSource
}

func (c *accountRuntimeCache) resolveActiveSelectionLocked(prepared []*rankedPriorityTier, ignorePinnedSelection bool) (*rankedPriorityTier, int, accountSelectionSource) {
	if len(prepared) == 0 {
		return nil, -1, accountSelectionSourceRanked
	}

	if !c.selectionPinned || ignorePinnedSelection {
		selectedTier := selectFirstEligibleTier(prepared)
		if selectedTier == nil {
			return nil, -1, accountSelectionSourceRanked
		}
		if preferredIndex, ok := c.preferredSelectionIndexLocked(selectedTier.groupKey, selectedTier.eligible); ok {
			return selectedTier, preferredIndex, accountSelectionSourcePreferred
		}
		if activeIndex, ok := c.retainedSelectionIndexLocked(selectedTier.groupKey, selectedTier.eligible); ok {
			if activeIndex > 0 && shouldRetainAutoActiveSelection(selectedTier.eligible[0], selectedTier.eligible[activeIndex]) {
				return selectedTier, activeIndex, accountSelectionSourceRetainedActive
			}
		}
		return selectedTier, 0, accountSelectionSourceRanked
	}

	if c.activeTier > 0 {
		for _, tier := range prepared {
			if tier == nil || tier.index != c.activeTier {
				continue
			}
			if len(tier.eligible) > 0 {
				if c.activeAccount > 0 {
					for idx, candidate := range tier.eligible {
						if candidate != nil && candidate.account != nil && candidate.account.ID == c.activeAccount {
							return tier, idx, accountSelectionSourcePinned
						}
					}
				}
				return tier, 0, accountSelectionSourceRanked
			}
			break
		}

		for _, tier := range prepared {
			if tier != nil && len(tier.eligible) > 0 {
				return tier, 0, accountSelectionSourceRanked
			}
		}
		return nil, -1, accountSelectionSourceRanked
	}

	selectedTier := selectFirstEligibleTier(prepared)
	if selectedTier == nil {
		return nil, -1, accountSelectionSourceRanked
	}
	return selectedTier, 0, accountSelectionSourceRanked
}

func selectFirstEligibleTier(prepared []*rankedPriorityTier) *rankedPriorityTier {
	for _, tier := range prepared {
		if tier != nil && len(tier.eligible) > 0 {
			return tier
		}
	}
	return nil
}

func (c *accountRuntimeCache) hasPriorityLocked(priority int) bool {
	for _, record := range c.byID {
		if record != nil && accountGroupRank(inferAccountGroupKey(record)) == priority {
			return true
		}
	}
	return false
}

func (c *accountRuntimeCache) matchesStateVersion(id int64, version uint64) bool {
	if c == nil || id <= 0 || version == 0 {
		return false
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	current, ok := c.stateVersions[id]
	return ok && current == version
}

// snapshotState 捕获指定版本的完整运行态，避免事件合并丢掉中间成功或重置的效果。
func (c *accountRuntimeCache) snapshotState(id int64, version uint64) (store.AccountRuntimeState, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	record := c.byID[id]
	if record == nil || version == 0 || c.stateVersions[id] != version {
		return store.AccountRuntimeState{}, false
	}
	return store.AccountRuntimeState{
		Enabled: record.Enabled, State: record.State,
		CooldownUntil: cloneTimePtr(record.CooldownUntil), FailCount: record.FailCount,
		LastSuccessAt: cloneTimePtr(record.LastSuccessAt), LastError: record.LastError, UpdatedAt: record.UpdatedAt,
	}, true
}

func (c *accountRuntimeCache) get(id int64) (*store.UpstreamAccountRecord, bool) {
	if c == nil || id <= 0 {
		return nil, false
	}

	now := time.Now()

	c.mu.RLock()
	defer c.mu.RUnlock()

	record, ok := c.byID[id]
	if !ok || record == nil {
		return nil, false
	}
	return normalizeAccountRuntimeView(cloneUpstreamAccountRecord(record), now), true
}

func (c *accountRuntimeCache) activeSelectionAccountID() (int64, bool) {
	if c == nil {
		return 0, false
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	if !c.selectionPinned || c.activeAccount <= 0 {
		return 0, false
	}
	record, ok := c.byID[c.activeAccount]
	if !ok || record == nil {
		return 0, false
	}
	return c.activeAccount, true
}

func (c *accountRuntimeCache) list(includeDisabled bool) []*store.UpstreamAccountRecord {
	if c == nil {
		return nil
	}

	now := time.Now()

	c.mu.RLock()
	defer c.mu.RUnlock()

	records := make([]*store.UpstreamAccountRecord, 0, len(c.ordered))
	for _, record := range c.ordered {
		if record == nil {
			continue
		}
		if !includeDisabled && !record.Enabled {
			continue
		}
		records = append(records, normalizeAccountRuntimeView(cloneUpstreamAccountRecord(record), now))
	}
	return records
}

func (c *accountRuntimeCache) listSchedulable(now time.Time) []*store.UpstreamAccountRecord {
	if c == nil {
		return nil
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	records := make([]*store.UpstreamAccountRecord, 0, len(c.ordered))
	for _, record := range c.ordered {
		if record == nil || !record.Enabled || record.State == "disabled_auth" {
			continue
		}
		if record.CooldownUntil != nil && record.CooldownUntil.After(now) {
			continue
		}
		records = append(records, normalizeAccountRuntimeView(cloneUpstreamAccountRecord(record), now))
	}
	return records
}

func (c *accountRuntimeCache) markSuccessIfNoNewerFailure(id int64, successAt, attemptStartedAt time.Time) (bool, uint64) {
	if c == nil || id <= 0 {
		return false, 0
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	record := c.byID[id]
	if record == nil {
		return false, 0
	}
	if record.State == "disabled_auth" {
		return false, 0
	}
	if record.UpdatedAt.After(attemptStartedAt) && (record.State == "cooldown" || record.State == "disabled_auth") {
		return false, 0
	}

	record.FailCount = 0
	record.State = "active"
	record.CooldownUntil = nil
	record.LastSuccessAt = cloneTimePtr(&successAt)
	record.LastError = ""
	record.UpdatedAt = successAt
	if c.selectionPinned {
		if c.activeAccount == id {
			c.activeTier = accountGroupRank(inferAccountGroupKey(record))
			c.activeAccount = id
		}
	} else if groupKey := inferAccountGroupKey(record); groupKey != "" {
		c.autoRetainedByGroup[groupKey] = id
	}
	return true, c.bumpStateVersionLocked(id)
}

func (c *accountRuntimeCache) markSuccess(id int64, successAt time.Time) (bool, uint64) {
	if c == nil || id <= 0 {
		return false, 0
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	record := c.byID[id]
	if record == nil {
		return false, 0
	}

	record.FailCount = 0
	record.State = "active"
	record.CooldownUntil = nil
	record.LastSuccessAt = cloneTimePtr(&successAt)
	record.LastError = ""
	record.UpdatedAt = successAt
	if c.selectionPinned {
		if c.activeAccount == id {
			c.activeTier = accountGroupRank(inferAccountGroupKey(record))
			c.activeAccount = id
		}
	}
	return true, c.bumpStateVersionLocked(id)
}

func (c *accountRuntimeCache) markAuthFailed(id int64, reason string, failedAt time.Time) (bool, uint64) {
	if c == nil || id <= 0 {
		return false, 0
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	record := c.byID[id]
	if record == nil {
		return false, 0
	}

	record.Enabled = false
	record.State = "disabled_auth"
	record.CooldownUntil = nil
	record.LastError = reason
	record.UpdatedAt = failedAt
	c.clearAutoRetainedAccountLocked(id)
	if c.activeAccount == id {
		// 保留当前层级 pin，但释放具体账号，让调度器在同层其他可用账号中继续选择。
		c.activeAccount = 0
		if c.activeTier <= 0 {
			c.selectionPinned = false
		}
	}
	return true, c.bumpStateVersionLocked(id)
}

func (c *accountRuntimeCache) markTransientFailure(id int64, reason string, cooldownUntil, failedAt time.Time) (bool, uint64) {
	if c == nil || id <= 0 {
		return false, 0
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	record := c.byID[id]
	if record == nil {
		return false, 0
	}
	// 鉴权禁用由显式恢复操作解除，普通失败不能降级它或使待落库鉴权事件失效。
	// ok=true、version=0 表示账号存在，但该结算没有发布新状态。
	if record.State == "disabled_auth" {
		return true, 0
	}

	record.FailCount++
	record.State = "cooldown"
	record.CooldownUntil = cloneTimePtr(&cooldownUntil)
	record.LastError = reason
	record.UpdatedAt = failedAt
	c.clearAutoRetainedAccountLocked(id)
	if c.activeAccount == id {
		// 手动 pin 的目标进入 cooldown 时，当前请求可以降级，但目标账号本身不能丢；
		// 等 cooldown 结束后，调度器应自动回到该账号。
		if !c.selectionPinned {
			c.activeAccount = 0
		}
		if c.activeTier <= 0 {
			c.selectionPinned = false
		}
	}
	return true, c.bumpStateVersionLocked(id)
}

// cooldownVersion 只为临时冷却返回版本，其他状态不得被清除操作推进版本或覆盖。
func (c *accountRuntimeCache) cooldownVersion(id int64) (uint64, bool) {
	if c == nil || id <= 0 {
		return 0, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	record := c.byID[id]
	if record == nil {
		return 0, false
	}
	if record.State != "cooldown" {
		return 0, true
	}
	return c.stateVersions[id], true
}

// clearCooldownIfVersion 在持久化成功后发布，保留落库期间新到达的失败和鉴权禁用。
func (c *accountRuntimeCache) clearCooldownIfVersion(id int64, version uint64, clearedAt time.Time) bool {
	if c == nil || id <= 0 || version == 0 {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	record := c.byID[id]
	if record == nil || record.State != "cooldown" || c.stateVersions[id] != version {
		return false
	}
	record.State = "active"
	record.FailCount = 0
	record.CooldownUntil = nil
	record.LastError = ""
	record.UpdatedAt = clearedAt
	c.bumpStateVersionLocked(id)
	return true
}

func (c *accountRuntimeCache) applyToggle(id int64, enabled bool, changedAt time.Time) (bool, uint64, accountRuntimeStateSnapshot) {
	if c == nil || id <= 0 {
		return false, 0, accountRuntimeStateSnapshot{}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	record := c.byID[id]
	if record == nil {
		return false, 0, accountRuntimeStateSnapshot{}
	}

	snapshot := accountRuntimeStateSnapshot{
		enabled:         record.Enabled,
		state:           record.State,
		cooldownUntil:   cloneTimePtr(record.CooldownUntil),
		failCount:       record.FailCount,
		lastSuccessAt:   cloneTimePtr(record.LastSuccessAt),
		lastError:       record.LastError,
		updatedAt:       record.UpdatedAt,
		stateVersion:    c.stateVersions[id],
		activeTier:      c.activeTier,
		activeAccount:   c.activeAccount,
		selectionPinned: c.selectionPinned,
	}

	record.Enabled = enabled
	if enabled {
		record.State = "active"
		record.FailCount = 0
		record.CooldownUntil = nil
		record.LastError = ""
	} else {
		c.clearAutoRetainedAccountLocked(id)
		if c.activeAccount == id {
			c.activeAccount = 0
		}
	}
	record.UpdatedAt = changedAt
	c.syncAutoRetainedAccountsLocked()
	if c.activeTier > 0 && !c.hasPriorityLocked(c.activeTier) {
		c.clearSelectionLocked()
	} else if !c.selectionPinned {
		c.clearSelectionLocked()
	}
	return true, c.bumpStateVersionLocked(id), snapshot
}

func (c *accountRuntimeCache) restoreRuntimeStateIfVersion(id int64, expectedVersion uint64, snapshot accountRuntimeStateSnapshot) bool {
	if c == nil || id <= 0 || expectedVersion == 0 {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	current := c.byID[id]
	if current == nil {
		return false
	}
	if c.stateVersions[id] != expectedVersion {
		return false
	}

	current.Enabled = snapshot.enabled
	current.State = snapshot.state
	current.CooldownUntil = cloneTimePtr(snapshot.cooldownUntil)
	current.FailCount = snapshot.failCount
	current.LastSuccessAt = cloneTimePtr(snapshot.lastSuccessAt)
	current.LastError = snapshot.lastError
	current.UpdatedAt = snapshot.updatedAt
	c.activeTier = snapshot.activeTier
	c.activeAccount = snapshot.activeAccount
	c.selectionPinned = snapshot.selectionPinned
	if snapshot.stateVersion > 0 {
		c.stateVersions[id] = snapshot.stateVersion
	}
	return true
}

func (c *accountRuntimeCache) bumpStateVersionLocked(id int64) uint64 {
	current := c.stateVersions[id]
	if current == 0 {
		current = 1
	} else {
		current++
	}
	c.stateVersions[id] = current
	return current
}

func (c *accountRuntimeCache) clearSelectionLocked() {
	c.activeTier = 0
	c.activeAccount = 0
	c.selectionPinned = false
}

func (c *accountRuntimeCache) preferredSelectionIndexLocked(groupKey string, eligible []*rankedSchedulableAccount) (int, bool) {
	groupKey = normalizeAccountGroupKey(groupKey)
	if groupKey == "" || len(eligible) == 0 {
		return 0, false
	}

	preferredID := c.preferredByGroup[groupKey]
	if preferredID <= 0 {
		return 0, false
	}
	for idx, candidate := range eligible {
		if candidate != nil && candidate.account != nil && candidate.account.ID == preferredID {
			return idx, true
		}
	}
	return 0, false
}

func (c *accountRuntimeCache) retainedSelectionIndexLocked(groupKey string, eligible []*rankedSchedulableAccount) (int, bool) {
	groupKey = normalizeAccountGroupKey(groupKey)
	if groupKey == "" || len(eligible) == 0 {
		return 0, false
	}

	retainedID := c.autoRetainedByGroup[groupKey]
	if retainedID <= 0 {
		return 0, false
	}
	for idx, candidate := range eligible {
		if candidate != nil && candidate.account != nil && candidate.account.ID == retainedID {
			return idx, true
		}
	}
	return 0, false
}

func eligibleAccountIndex(eligible []*rankedSchedulableAccount, accountID int64) int {
	if accountID <= 0 {
		return -1
	}
	for idx, candidate := range eligible {
		if candidate != nil && candidate.account != nil && candidate.account.ID == accountID {
			return idx
		}
	}
	return -1
}

func shouldRetainAutoActiveSelection(best, active *rankedSchedulableAccount) bool {
	if active == nil || active.account == nil {
		return false
	}
	if best == nil || best.account == nil || best.account.ID == active.account.ID {
		return true
	}
	if best.bucket != active.bucket {
		return false
	}
	if active.bucket != utilizationBucketKnownOAuth {
		return true
	}
	if active.finalScore <= 0 {
		return best.finalScore <= 0
	}
	return best.finalScore < active.finalScore*sameTierAutoSwitchScoreFactor
}

func (c *accountRuntimeCache) syncPreferredAccountsLocked() {
	if c == nil || len(c.preferredByGroup) == 0 {
		return
	}

	for groupKey, accountID := range c.preferredByGroup {
		record := c.byID[accountID]
		if record == nil || inferAccountGroupKey(record) != groupKey {
			delete(c.preferredByGroup, groupKey)
		}
	}
}

func (c *accountRuntimeCache) syncAutoRetainedAccountsLocked() {
	if c == nil || len(c.autoRetainedByGroup) == 0 {
		return
	}

	for groupKey, accountID := range c.autoRetainedByGroup {
		record := c.byID[accountID]
		if record == nil || inferAccountGroupKey(record) != groupKey || !record.Enabled {
			delete(c.autoRetainedByGroup, groupKey)
		}
	}
}

func (c *accountRuntimeCache) clearAutoRetainedAccountLocked(id int64) {
	if c == nil || id <= 0 || len(c.autoRetainedByGroup) == 0 {
		return
	}

	for groupKey, accountID := range c.autoRetainedByGroup {
		if accountID == id {
			delete(c.autoRetainedByGroup, groupKey)
		}
	}
}

func (c *accountRuntimeCache) syncPinnedSelectionLocked() {
	if c == nil || !c.selectionPinned || c.activeAccount <= 0 {
		return
	}
	record := c.byID[c.activeAccount]
	if record == nil {
		c.activeAccount = 0
		return
	}
	c.activeTier = accountGroupRank(inferAccountGroupKey(record))
}

func (c *accountRuntimeCache) rebuildOrderedLocked() {
	c.ordered = c.ordered[:0]
	if len(c.byID) == 0 {
		return
	}

	c.ordered = make([]*store.UpstreamAccountRecord, 0, len(c.byID))
	for _, record := range c.byID {
		c.ordered = append(c.ordered, record)
	}
	sort.SliceStable(c.ordered, func(i, j int) bool {
		left := c.ordered[i]
		right := c.ordered[j]
		if left == nil {
			return false
		}
		if right == nil {
			return true
		}
		leftGroupRank := accountGroupRank(inferAccountGroupKey(left))
		rightGroupRank := accountGroupRank(inferAccountGroupKey(right))
		if leftGroupRank != rightGroupRank {
			return leftGroupRank < rightGroupRank
		}
		if left.Priority != right.Priority {
			return left.Priority < right.Priority
		}
		return left.ID < right.ID
	})
}

func cloneUpstreamAccountRecord(record *store.UpstreamAccountRecord) *store.UpstreamAccountRecord {
	if record == nil {
		return nil
	}

	cloned := *record
	cloned.SoftFailureWindowMinutes = config.CloneSoftFailureValue(record.SoftFailureWindowMinutes)
	cloned.SoftFailureThreshold = config.CloneSoftFailureValue(record.SoftFailureThreshold)
	cloned.CooldownUntil = cloneTimePtr(record.CooldownUntil)
	cloned.LastSuccessAt = cloneTimePtr(record.LastSuccessAt)
	cloned.Quota5HUsedPercent = cloneFloat64Ptr(record.Quota5HUsedPercent)
	cloned.Quota5HResetAt = cloneTimePtr(record.Quota5HResetAt)
	cloned.QuotaWeeklyUsedPercent = cloneFloat64Ptr(record.QuotaWeeklyUsedPercent)
	cloned.QuotaWeeklyResetAt = cloneTimePtr(record.QuotaWeeklyResetAt)
	cloned.QuotaRefreshedAt = cloneTimePtr(record.QuotaRefreshedAt)
	return &cloned
}

func cloneTimePtr(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneFloat64Ptr(value *float64) *float64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func normalizeAccountRuntimeView(record *store.UpstreamAccountRecord, now time.Time) *store.UpstreamAccountRecord {
	if record == nil {
		return nil
	}
	if record.State == "cooldown" && record.CooldownUntil != nil && !record.CooldownUntil.After(now) {
		record.State = "active"
		record.CooldownUntil = nil
	}
	return record
}
