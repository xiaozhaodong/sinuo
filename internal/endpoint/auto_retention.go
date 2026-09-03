// Package endpoint - v8 Auto retained 运行态（收敛方案 §8.4）
// 自动模式在同一 priority 层内的成功粘性；纯运行态，不落库。
package endpoint

import (
	"sync"
	"time"
)

// endpointAutoRetention 同层级成功粘性记录
type endpointAutoRetention struct {
	EndpointName string
	Priority     int
	SelectedAt   time.Time
	LastSuccess  time.Time
}

type autoRetentionState struct {
	mu sync.Mutex
	// 按作用域和 priority 层保存；低层 retained 不阻挡高层恢复。
	byScope map[string]map[int]*endpointAutoRetention
}

func newAutoRetentionState() *autoRetentionState {
	return &autoRetentionState{byScope: make(map[string]map[int]*endpointAutoRetention)}
}

// UpdateAutoRetention 为自动候选或 manual preferred 的自动 fallback 建立/刷新 retained。
// 用户显式 preferred/fixed 目标的成功不写 retained；路由意图已变化时放弃更新。
func (m *Manager) UpdateAutoRetention(endpointName string, priority int, selectionSource string, routeRevisionAtSelection int64) bool {
	scope := RouteScope{Kind: RouteScopeGlobal, Revision: routeRevisionAtSelection}
	return m.UpdateAutoRetentionForScope(scope, endpointName, priority, selectionSource)
}

// UpdateAutoRetentionForScope 在指定作用域内建立/刷新 retained。
func (m *Manager) UpdateAutoRetentionForScope(scope RouteScope, endpointName string, priority int, selectionSource string) bool {
	if !m.ScopeStillValid(scope) {
		return false
	}
	switch selectionSource {
	case "auto_priority", "auto_retained", "fallback":
	default:
		return false
	}
	ep := m.GetEndpointByNameAny(endpointName)
	if ep == nil || !m.EndpointHardEnabled(ep) || !m.IsEndpointRoutable(ep) {
		return false
	}

	m.autoRetention.mu.Lock()
	defer m.autoRetention.mu.Unlock()
	now := time.Now()
	key := scope.Key()
	tiers := m.autoRetention.byScope[key]
	if tiers == nil {
		tiers = make(map[int]*endpointAutoRetention)
		m.autoRetention.byScope[key] = tiers
	}
	entry := tiers[priority]
	if entry == nil || entry.EndpointName != endpointName {
		entry = &endpointAutoRetention{EndpointName: endpointName, Priority: priority, SelectedAt: now}
	}
	entry.LastSuccess = now
	tiers[priority] = entry
	return true
}

// RetainedInTier 返回指定层级的 retained 端点名（无则空）
func (m *Manager) RetainedInTier(priority int) string {
	return m.RetainedInTierForScope((RouteScope{Kind: RouteScopeGlobal}).Key(), priority)
}

// RetainedInTierForScope 返回指定作用域和层级的 retained 端点名。
func (m *Manager) RetainedInTierForScope(scopeKey string, priority int) string {
	m.autoRetention.mu.Lock()
	defer m.autoRetention.mu.Unlock()
	if entry := m.autoRetention.byScope[scopeKey][priority]; entry != nil {
		return entry.EndpointName
	}
	return ""
}

// ClearAutoRetentionFor 清除指定端点的 retained（删除/硬停用/暂停/冷却/负缓存时，§8.4 规则 3）
func (m *Manager) ClearAutoRetentionFor(endpointName string) {
	m.autoRetention.mu.Lock()
	defer m.autoRetention.mu.Unlock()
	for scopeKey, tiers := range m.autoRetention.byScope {
		for tier, entry := range tiers {
			if entry != nil && entry.EndpointName == endpointName {
				delete(tiers, tier)
			}
		}
		if len(tiers) == 0 {
			delete(m.autoRetention.byScope, scopeKey)
		}
	}
}

// PruneAutoRetentionScopes 丢弃已不存在的模型组作用域的 retained。
// 组被删除或成员/策略变更后重新编译时调用；全局作用域（空 key）永不剪枝。
func (m *Manager) PruneAutoRetentionScopes(validScopeKeys map[string]struct{}) {
	if m == nil {
		return
	}
	m.autoRetention.mu.Lock()
	defer m.autoRetention.mu.Unlock()
	for scopeKey := range m.autoRetention.byScope {
		if scopeKey == "" {
			continue
		}
		if _, ok := validScopeKeys[scopeKey]; !ok {
			delete(m.autoRetention.byScope, scopeKey)
		}
	}
}
