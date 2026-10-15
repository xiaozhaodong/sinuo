// Package endpoint - 分类软失败追踪器（收敛方案 §9.3）
// 替换旧 FailureTracker：按 endpoint + pathScope + category 独立计数，
// 不同类别不互相凑数；达到阈值即视为 tripped，同时清除触发类别窗口
// （长期阻断由调用方写入 cooldown 表达，tracker 本身不承担阻断状态）。
package endpoint

import (
	"sync"
	"time"

	"sinuo/config"
)

// SoftFailureScope 软失败 path scope（§9.3：messages | count_tokens）
type SoftFailureScope string

const (
	SoftFailureScopeMessages    SoftFailureScope = "messages"
	SoftFailureScopeCountTokens SoftFailureScope = "count_tokens"
)

// SoftFailureCategory 软失败类别（§9.3）
type SoftFailureCategory string

const (
	SoftFailureCategoryConnection  SoftFailureCategory = "connection"
	SoftFailureCategoryTransport   SoftFailureCategory = "transport"
	SoftFailureCategoryRateLimit   SoftFailureCategory = "rate_limit"
	SoftFailureCategoryServerError SoftFailureCategory = "server_error"
)

// SoftFailureCooldownReasonPrefix 阈值触发 cooldown 的统一 reason 前缀，
// reject 判定与快照解释依赖该前缀识别"软失败阈值型冷却"。
const SoftFailureCooldownReasonPrefix = "soft_failure_"

// SoftFailureCooldownReason 组装阈值 cooldown 的 reason 字符串
func SoftFailureCooldownReason(category SoftFailureCategory) string {
	return SoftFailureCooldownReasonPrefix + string(category)
}

// FailureTrackerConfig 软失败窗口配置（复用 failure_tracker.* 配置键，§12）
type FailureTrackerConfig struct {
	Enabled    bool          // 是否启用
	TimeWindow time.Duration // 时间窗口
	Threshold  int           // 阈值
}

type softFailureKey struct {
	endpoint string
	scope    SoftFailureScope
	category SoftFailureCategory
}

// softFailureWindow 保存事件及其生效窗口；策略变更时由发布方清除。
type softFailureWindow struct {
	events []time.Time
	policy config.SoftFailurePolicy
	custom bool
}

// SoftFailureTracker 分类软失败追踪器（纯内存态，D13）
type SoftFailureTracker struct {
	mu     sync.Mutex
	config FailureTrackerConfig
	events map[softFailureKey]softFailureWindow
	now    func() time.Time
}

// NewSoftFailureTracker 创建分类软失败追踪器
func NewSoftFailureTracker(enabled bool, timeWindow time.Duration, threshold int) *SoftFailureTracker {
	return &SoftFailureTracker{
		config: FailureTrackerConfig{Enabled: enabled, TimeWindow: timeWindow, Threshold: threshold},
		events: make(map[softFailureKey]softFailureWindow),
		now:    time.Now,
	}
}

// Record 记录一次软失败，返回窗口内计数与是否达到阈值。
// 达到阈值时清除该类别窗口（§9.3 规则 4：避免 cooldown 到期立即再次触发）。
func (t *SoftFailureTracker) Record(endpointName string, scope SoftFailureScope, category SoftFailureCategory, windowMinutes, threshold *int) (count int, tripped bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.config.Enabled {
		return 0, false
	}

	key := softFailureKey{endpoint: endpointName, scope: scope, category: category}
	policy := config.ResolveSoftFailurePolicy(windowMinutes, threshold, t.config.TimeWindow, t.config.Threshold)
	entry := t.events[key]
	custom := windowMinutes != nil
	if entry.policy != policy || entry.custom != custom {
		entry.events = nil
	}
	now := t.now()
	windowStart := now.Add(-policy.Window)

	valid := make([]time.Time, 0, len(entry.events)+1)
	for _, item := range entry.events {
		if item.After(windowStart) {
			valid = append(valid, item)
		}
	}
	valid = append(valid, now)

	if len(valid) >= policy.Threshold {
		delete(t.events, key)
		return len(valid), true
	}
	t.events[key] = softFailureWindow{events: valid, policy: policy, custom: custom}
	return len(valid), false
}

// ClearScope 清除端点在指定 scope 的全部类别（FullSuccess，§9.3 规则 2）
func (t *SoftFailureTracker) ClearScope(endpointName string, scope SoftFailureScope) {
	t.ClearScopeBefore(endpointName, scope, time.Time{})
}

// ClearScopeBefore 清除端点在指定 scope 中 cutoff 之前的软失败事件；
// cutoff 之后的事件是比本次成功更新的失败证据，必须保留（IfNoNewerFailure：
// 较早发起的慢请求成功不得清除请求进行期间新记录的失败）。cutoff 零值=全清。
func (t *SoftFailureTracker) ClearScopeBefore(endpointName string, scope SoftFailureScope, cutoff time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for key, entry := range t.events {
		events := entry.events
		if key.endpoint != endpointName || key.scope != scope {
			continue
		}
		if cutoff.IsZero() {
			delete(t.events, key)
			continue
		}
		remaining := make([]time.Time, 0, len(events))
		for _, item := range events {
			if item.After(cutoff) {
				remaining = append(remaining, item)
			}
		}
		if len(remaining) == 0 {
			delete(t.events, key)
		} else {
			entry.events = remaining
			t.events[key] = entry
		}
	}
}

// ClearEndpoint 清除端点全部软失败记录（端点删除/手动清理）
func (t *SoftFailureTracker) ClearEndpoint(endpointName string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for key := range t.events {
		if key.endpoint == endpointName {
			delete(t.events, key)
		}
	}
}

// CountsFor 返回端点在指定 scope 的各类别有效计数（快照/UI 解释用）
func (t *SoftFailureTracker) CountsFor(endpointName string, scope SoftFailureScope) map[SoftFailureCategory]int {
	t.mu.Lock()
	defer t.mu.Unlock()

	result := make(map[SoftFailureCategory]int)
	if !t.config.Enabled {
		return result
	}
	now := t.now()
	for key, entry := range t.events {
		events := entry.events
		if key.endpoint != endpointName || key.scope != scope {
			continue
		}
		windowStart := now.Add(-entry.policy.Window)
		count := 0
		for _, item := range events {
			if item.After(windowStart) {
				count++
			}
		}
		if count > 0 {
			result[key.category] = count
		}
	}
	return result
}

// Stats 按端点聚合全部 scope/类别的有效计数（兼容旧 GetFailureStats 展示口径）
func (t *SoftFailureTracker) Stats() map[string]int {
	t.mu.Lock()
	defer t.mu.Unlock()

	stats := make(map[string]int)
	if !t.config.Enabled {
		return stats
	}
	now := t.now()
	for key, entry := range t.events {
		events := entry.events
		windowStart := now.Add(-entry.policy.Window)
		count := 0
		for _, item := range events {
			if item.After(windowStart) {
				count++
			}
		}
		if count > 0 {
			stats[key.endpoint] += count
		}
	}
	return stats
}

// Threshold 返回当前阈值（快照解释用）
func (t *SoftFailureTracker) Threshold(windowMinutes, threshold *int) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return config.ResolveSoftFailurePolicy(windowMinutes, threshold, t.config.TimeWindow, t.config.Threshold).Threshold
}

// UpdateConfig 热更新配置，返回默认策略及启用状态是否变化，供发布方隔离旧结算。
// 默认策略变化只清理继承计数；禁用时清空全部历史。
func (t *SoftFailureTracker) UpdateConfig(enabled bool, timeWindow time.Duration, threshold int) (policyChanged, enabledChanged bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	oldPolicy := config.ResolveSoftFailurePolicy(nil, nil, t.config.TimeWindow, t.config.Threshold)
	newPolicy := config.ResolveSoftFailurePolicy(nil, nil, timeWindow, threshold)
	policyChanged = oldPolicy != newPolicy
	enabledChanged = t.config.Enabled != enabled
	t.config = FailureTrackerConfig{Enabled: enabled, TimeWindow: timeWindow, Threshold: threshold}
	for key, entry := range t.events {
		if !enabled || policyChanged && !entry.custom {
			delete(t.events, key)
		}
	}
	return policyChanged, enabledChanged
}

// CleanupExpiredEvents 惰性清理过窗事件（由健康检查循环定期调用）
func (t *SoftFailureTracker) CleanupExpiredEvents() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.config.Enabled {
		return
	}
	now := t.now()
	for key, entry := range t.events {
		events := entry.events
		windowStart := now.Add(-entry.policy.Window)
		valid := events[:0]
		for _, item := range events {
			if item.After(windowStart) {
				valid = append(valid, item)
			}
		}
		if len(valid) == 0 {
			delete(t.events, key)
		} else {
			entry.events = valid
			t.events[key] = entry
		}
	}
}

// ---- count_tokens 进程内 scoped cooldown（D17：第一版不落库，不冷却 /v1/messages）----

type scopedCooldownEntry struct {
	Until  time.Time
	Reason string
}

// ScopedCooldowns 按 endpoint+scope 的内存冷却表
type ScopedCooldowns struct {
	mu      sync.Mutex
	entries map[softFailureKey]scopedCooldownEntry
}

// NewScopedCooldowns 创建 scoped cooldown 表
func NewScopedCooldowns() *ScopedCooldowns {
	return &ScopedCooldowns{entries: make(map[softFailureKey]scopedCooldownEntry)}
}

// Set 写入 scoped cooldown
func (c *ScopedCooldowns) Set(endpointName string, scope SoftFailureScope, until time.Time, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[softFailureKey{endpoint: endpointName, scope: scope}] = scopedCooldownEntry{Until: until, Reason: reason}
}

// Active 查询仍生效的 scoped cooldown
func (c *ScopedCooldowns) Active(endpointName string, scope SoftFailureScope) (bool, time.Time, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := softFailureKey{endpoint: endpointName, scope: scope}
	entry, ok := c.entries[key]
	if !ok {
		return false, time.Time{}, ""
	}
	if !entry.Until.After(time.Now()) {
		delete(c.entries, key)
		return false, time.Time{}, ""
	}
	return true, entry.Until, entry.Reason
}

// Clear 清除端点在指定 scope 的冷却
func (c *ScopedCooldowns) Clear(endpointName string, scope SoftFailureScope) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, softFailureKey{endpoint: endpointName, scope: scope})
}

// ClearEndpoint 清除端点在所有 scope 下的冷却（删除/同名重建隔离）。
func (c *ScopedCooldowns) ClearEndpoint(endpointName string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key := range c.entries {
		if key.endpoint == endpointName {
			delete(c.entries, key)
		}
	}
}
