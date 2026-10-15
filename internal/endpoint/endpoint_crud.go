// endpoint_crud.go - 动态端点管理功能
// 包含端点的增删改查操作（v5.0+ 新增）

package endpoint

import (
	"fmt"
	"log/slog"
	"time"

	"sinuo/config"
	"sinuo/internal/events"
	timezonepolicy "sinuo/internal/timezone"
)

// SyncEndpoints 从数据库同步端点（v5.0 Desktop 专用）
// 用于启动时从数据库加载端点，替换现有端点列表
func (m *Manager) SyncEndpoints(configs []config.EndpointConfig) {
	m.endpointConfigMu.Lock()
	defer m.endpointConfigMu.Unlock()

	// 创建新端点列表
	endpoints := make([]*Endpoint, len(configs))
	for i, cfg := range configs {
		endpoints[i] = &Endpoint{
			Config: cloneEndpointConfig(cfg),
			Status: EndpointStatus{
				Healthy:      false,
				NeverChecked: true,
			},
			configRevision: NextEndpointConfigRevision(),
		}
	}

	// 使用写锁替换端点列表
	m.endpointsMu.Lock()
	m.endpoints = endpoints
	m.endpointsMu.Unlock()

	slog.Info(fmt.Sprintf("🔄 [端点同步] 已同步 %d 个端点到管理器", len(configs)))
}

// AddEndpoint 动态添加端点（v5.0+ 新增）
// 线程安全地将新端点添加到管理器中
func (m *Manager) AddEndpoint(cfg config.EndpointConfig) error {
	if err := config.ValidateSoftFailureOverrides(cfg.SoftFailureWindowMinutes, cfg.SoftFailureThreshold); err != nil {
		return err
	}
	m.endpointConfigMu.Lock()

	// 验证端点名称唯一性
	m.endpointsMu.RLock()
	for _, ep := range m.endpoints {
		if ep.Config.Name == cfg.Name {
			m.endpointsMu.RUnlock()
			m.endpointConfigMu.Unlock()
			return fmt.Errorf("端点 '%s' 已存在", cfg.Name)
		}
	}
	m.endpointsMu.RUnlock()

	// 创建新端点
	endpoint := &Endpoint{
		Config: cloneEndpointConfig(cfg),
		Status: EndpointStatus{
			Healthy:      false,
			NeverChecked: true,
		},
		configRevision: NextEndpointConfigRevision(),
	}

	// 使用写锁添加端点
	m.endpointsMu.Lock()
	m.endpoints = append(m.endpoints, endpoint)
	m.endpointsMu.Unlock()
	m.endpointConfigMu.Unlock()

	// 发布事件通知
	if m.eventBus != nil {
		m.eventBus.Publish(events.Event{
			Type:     "endpoint_added",
			Source:   "endpoint_manager",
			Priority: events.PriorityHigh,
			Data: map[string]interface{}{
				"name":      cfg.Name,
				"url":       cfg.URL,
				"priority":  cfg.Priority,
				"timestamp": timezonepolicy.FormatStorage(time.Now()),
			},
		})
	}

	slog.Info(fmt.Sprintf("➕ [端点管理] 新增端点: %s (%s)", cfg.Name, cfg.URL))
	return nil
}

// RemoveEndpoint 动态移除端点（v5.0+ 新增）
// 线程安全地从管理器中移除端点
func (m *Manager) RemoveEndpoint(name string) error {
	m.endpointConfigMu.Lock()
	m.endpointsMu.Lock()

	// 查找并移除端点
	index := -1
	for i, ep := range m.endpoints {
		if ep.Config.Name == name {
			index = i
			break
		}
	}

	if index == -1 {
		m.endpointsMu.Unlock()
		m.endpointConfigMu.Unlock()
		return fmt.Errorf("端点 '%s' 未找到", name)
	}

	// 移除端点（保持切片顺序）
	removedEndpoint := m.endpoints[index]
	m.endpoints = append(m.endpoints[:index], m.endpoints[index+1:]...)
	removedEndpoint.mutex.RLock()
	removedURL := removedEndpoint.Config.URL
	removedEndpoint.mutex.RUnlock()
	m.endpointsMu.Unlock()

	// 清理软失败记录（避免内存泄漏）
	if m.softFailures != nil {
		m.softFailures.ClearEndpoint(name)
	}
	if m.scopedCooldowns != nil {
		m.scopedCooldowns.ClearEndpoint(name)
	}
	if m.routeState != nil {
		m.routeState.ClearNegativeHits(name)
	}
	if m.autoRetention != nil {
		m.ClearAutoRetentionFor(name)
	}
	m.endpointConfigMu.Unlock()

	// 发布事件通知
	if m.eventBus != nil {
		m.eventBus.Publish(events.Event{
			Type:     "endpoint_removed",
			Source:   "endpoint_manager",
			Priority: events.PriorityHigh,
			Data: map[string]interface{}{
				"name":      name,
				"url":       removedURL,
				"timestamp": timezonepolicy.FormatStorage(time.Now()),
			},
		})
	}

	slog.Info(fmt.Sprintf("➖ [端点管理] 移除端点: %s", name))
	return nil
}

// UpdateEndpointConfig 更新端点配置（v5.0+ 新增）
// 更新现有端点的配置（不包括名称）
func (m *Manager) UpdateEndpointConfig(name string, cfg config.EndpointConfig) error {
	if err := config.ValidateSoftFailureOverrides(cfg.SoftFailureWindowMinutes, cfg.SoftFailureThreshold); err != nil {
		return err
	}
	m.endpointConfigMu.Lock()
	m.endpointsMu.RLock()
	var targetEndpoint *Endpoint
	for _, ep := range m.endpoints {
		if ep.Config.Name == name {
			targetEndpoint = ep
			break
		}
	}
	m.endpointsMu.RUnlock()

	if targetEndpoint == nil {
		m.endpointConfigMu.Unlock()
		return fmt.Errorf("端点 '%s' 未找到", name)
	}

	// 保留原名称
	cfg.Name = name

	// 更新配置（v8：递增 config revision，使已快照的 AttemptPlan 在 admission 重校验时失效）
	targetEndpoint.mutex.Lock()
	if !config.SameSoftFailureOverrides(targetEndpoint.Config.SoftFailureWindowMinutes, targetEndpoint.Config.SoftFailureThreshold, cfg.SoftFailureWindowMinutes, cfg.SoftFailureThreshold) {
		m.softFailures.ClearEndpoint(name)
		targetEndpoint.failureEpoch++
	}
	targetEndpoint.Config = cloneEndpointConfig(cfg)
	targetEndpoint.configRevision = NextEndpointConfigRevision()
	targetEndpoint.mutex.Unlock()

	m.endpointConfigMu.Unlock()

	// 发布事件通知
	if m.eventBus != nil {
		m.eventBus.Publish(events.Event{
			Type:     "endpoint_updated",
			Source:   "endpoint_manager",
			Priority: events.PriorityHigh,
			Data: map[string]interface{}{
				"name":      name,
				"url":       cfg.URL,
				"priority":  cfg.Priority,
				"timestamp": timezonepolicy.FormatStorage(time.Now()),
			},
		})
	}

	slog.Info(fmt.Sprintf("✏️ [端点管理] 更新端点配置: %s", name))
	return nil
}

// UpdateEndpointPriority updates the priority of an endpoint by name
// 注意: v5.0 SQLite 模式下，优先级应通过 UpdateEndpointConfig 更新
// 此函数保留用于 YAML 配置模式的向后兼容
func (m *Manager) UpdateEndpointPriority(name string, newPriority int) error {
	if newPriority < 1 {
		return fmt.Errorf("优先级必须大于等于1")
	}

	m.endpointConfigMu.Lock()
	m.endpointsMu.RLock()
	// Find the endpoint
	var targetEndpoint *Endpoint
	for _, ep := range m.endpoints {
		if ep.Config.Name == name {
			targetEndpoint = ep
			break
		}
	}
	m.endpointsMu.RUnlock()

	if targetEndpoint == nil {
		m.endpointConfigMu.Unlock()
		return fmt.Errorf("端点 '%s' 未找到", name)
	}

	// Update the priority with lock
	targetEndpoint.mutex.Lock()
	targetEndpoint.Config.Priority = newPriority
	targetEndpoint.configRevision = NextEndpointConfigRevision()
	targetEndpoint.mutex.Unlock()
	m.endpointConfigMu.Unlock()

	slog.Info(fmt.Sprintf("🔄 端点优先级已更新: %s -> %d", name, newPriority))

	return nil
}
