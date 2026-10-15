// health_check.go - 连通性检测相关功能
// 仅保留手动/批量检测，不再执行后台定时轮询。

package endpoint

import (
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"sinuo/internal/events"
	timezonepolicy "sinuo/internal/timezone"
	"sinuo/internal/utils"
)

// SetOnHealthCheckComplete 设置检测完成回调。
// 为兼容既有调用点，保留原函数名。
func (m *Manager) SetOnHealthCheckComplete(fn func()) {
	m.onHealthCheckComplete = fn
}

func (m *Manager) notifyWebInterface(endpoint *Endpoint) {
	if m.eventBus == nil || endpoint == nil {
		return
	}
	endpoint.mutex.RLock()
	status := endpoint.Status
	name := endpoint.Config.Name
	endpoint.mutex.RUnlock()
	eventType := events.EventEndpointHealthy
	priority := events.PriorityHigh
	if !status.Healthy {
		eventType = events.EventEndpointUnhealthy
		priority = events.PriorityCritical
	}
	m.eventBus.Publish(events.Event{
		Type: eventType, Source: "endpoint_manager", Priority: priority,
		Data: map[string]interface{}{
			"endpoint": name, "healthy": status.Healthy,
			"response_time":     utils.FormatResponseTime(status.ResponseTime),
			"last_check":        timezonepolicy.FormatStorage(status.LastCheck),
			"consecutive_fails": status.ConsecutiveFails,
			"change_type":       "health_changed",
		},
	})
}

// notifyHealthCheckComplete 仅通知前端刷新，健康检查不改写用户路由模式。
func (m *Manager) notifyHealthCheckComplete() {
	if m.onHealthCheckComplete != nil {
		go m.onHealthCheckComplete()
	}
}

// checkEndpointHealth checks endpoint connectivity.
// 2xx/4xx 代表链路可达，网络错误与 5xx 代表连通失败。
func (m *Manager) checkEndpointHealth(endpoint *Endpoint) {
	start := time.Now()
	healthURL := endpoint.Config.URL
	req, err := http.NewRequestWithContext(m.ctx, "GET", healthURL, nil)
	if err != nil {
		m.updateEndpointStatus(endpoint, false, 0)
		return
	}

	// Add authorization header with dynamically resolved token
	token := m.GetTokenForEndpoint(endpoint)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := m.client.Do(req)
	responseTime := time.Since(start)

	if err != nil {
		// Network or connection error
		slog.Warn(fmt.Sprintf("❌ [连通性测试] 端点网络错误: %s - 错误: %s, 响应时间: %dms",
			endpoint.Config.Name, err.Error(), responseTime.Milliseconds()))
		m.updateEndpointStatus(endpoint, false, responseTime)
		return
	}

	resp.Body.Close()

	healthy := (resp.StatusCode >= 200 && resp.StatusCode < 300) ||
		(resp.StatusCode >= 400 && resp.StatusCode < 500)

	// Log connectivity test results
	if healthy {
		slog.Debug(fmt.Sprintf("✅ [连通性测试] 端点可达: %s - 状态码: %d, 响应时间: %dms",
			endpoint.Config.Name,
			resp.StatusCode,
			responseTime.Milliseconds()))
	} else {
		slog.Warn(fmt.Sprintf("⚠️ [连通性测试] 端点不可达: %s - 状态码: %d, 响应时间: %dms",
			endpoint.Config.Name,
			resp.StatusCode,
			responseTime.Milliseconds()))
	}

	m.updateEndpointStatus(endpoint, healthy, responseTime)
}

// updateEndpointStatus updates the latest connectivity status of an endpoint.
func (m *Manager) updateEndpointStatus(endpoint *Endpoint, healthy bool, responseTime time.Duration) {
	endpoint.mutex.Lock()

	endpoint.Status.LastCheck = time.Now()
	endpoint.Status.ResponseTime = responseTime
	endpoint.Status.NeverChecked = false // 标记为已检测

	// 记录状态变化前的可达状态
	wasUnhealthy := !endpoint.Status.Healthy

	if healthy {
		endpoint.Status.Healthy = true
		endpoint.Status.ConsecutiveFails = 0

		if wasUnhealthy {
			slog.Info(fmt.Sprintf("✅ [连通性测试] 端点恢复可达: %s - 响应时间: %dms",
				endpoint.Config.Name, responseTime.Milliseconds()))
		}
	} else {
		endpoint.Status.ConsecutiveFails++
		wasHealthy := endpoint.Status.Healthy

		endpoint.Status.Healthy = false

		if wasHealthy {
			slog.Warn(fmt.Sprintf("❌ [连通性测试] 端点最近检测不可达: %s - 连续失败: %d次, 响应时间: %dms",
				endpoint.Config.Name, endpoint.Status.ConsecutiveFails, responseTime.Milliseconds()))
		} else {
			slog.Debug(fmt.Sprintf("❌ [连通性测试] 端点仍然不可达: %s - 连续失败: %d次, 响应时间: %dms",
				endpoint.Config.Name, endpoint.Status.ConsecutiveFails, responseTime.Milliseconds()))
		}
	}

	endpoint.mutex.Unlock()

	// 通知Web界面端点状态变化
	go m.notifyWebInterface(endpoint)

	// 最近检测结果从不可达转为可达时通知前端刷新，不改写用户路由模式。
	if healthy && wasUnhealthy {
		go m.notifyHealthCheckComplete()
	}
}

// IsHealthy returns the health status of an endpoint
func (e *Endpoint) IsHealthy() bool {
	e.mutex.RLock()
	defer e.mutex.RUnlock()
	return e.Status.Healthy
}

// GetResponseTime returns the last response time of an endpoint
func (e *Endpoint) GetResponseTime() time.Duration {
	e.mutex.RLock()
	defer e.mutex.RUnlock()
	return e.Status.ResponseTime
}

// GetStatus returns a copy of the endpoint status
func (e *Endpoint) GetStatus() EndpointStatus {
	e.mutex.RLock()
	defer e.mutex.RUnlock()
	return e.Status
}

// ManualHealthCheck performs a manual connectivity check on a specific endpoint by name.
// 为兼容既有调用点，保留原函数名。
func (m *Manager) ManualHealthCheck(endpointName string) error {
	var targetEndpoint *Endpoint

	// v5.0+: 使用读锁查找端点
	m.endpointsMu.RLock()
	for _, endpoint := range m.endpoints {
		if endpoint.Config.Name == endpointName {
			targetEndpoint = endpoint
			break
		}
	}
	m.endpointsMu.RUnlock()

	if targetEndpoint == nil {
		return fmt.Errorf("端点 '%s' 未找到", endpointName)
	}

	slog.Info(fmt.Sprintf("🔍 [连通性测试] 开始检查端点: %s", endpointName))
	m.checkEndpointHealth(targetEndpoint)

	// Get status and log completion with response time
	status := targetEndpoint.Status
	healthStatus := "可达"
	if !status.Healthy {
		if status.NeverChecked {
			healthStatus = "未检测"
		} else {
			healthStatus = "不可达"
		}
	}

	slog.Info(fmt.Sprintf("🔍 [连通性测试] 检查完成: %s - 状态: %s, 响应时间: %s",
		endpointName, healthStatus, utils.FormatResponseTime(status.ResponseTime)))

	return nil
}

// BatchHealthCheckAll 批量检测所有端点的最近连通性状态。
// 并发执行所有端点的检测，提高效率。
// 使用信号量限制并发数量，避免资源耗尽
func (m *Manager) BatchHealthCheckAll() (int, int, error) {
	slog.Info("🔍 [批量连通性测试] 开始检测所有端点")

	// v5.0+: 使用快照机制获取端点列表
	m.endpointsMu.RLock()
	endpoints := make([]*Endpoint, len(m.endpoints))
	copy(endpoints, m.endpoints)
	m.endpointsMu.RUnlock()

	if len(endpoints) == 0 {
		return 0, 0, fmt.Errorf("没有配置任何端点")
	}

	slog.Info(fmt.Sprintf("   共有 %d 个端点需要检测", len(endpoints)))

	// 使用信号量限制并发数量（最多 20 个并发）
	const maxConcurrentChecks = 20
	semaphore := make(chan struct{}, maxConcurrentChecks)

	// 使用 WaitGroup 并发检测所有端点
	var wg sync.WaitGroup
	var healthyCount, unhealthyCount int
	var countMu sync.Mutex

	for _, endpoint := range endpoints {
		wg.Add(1)
		semaphore <- struct{}{} // 获取信号量

		go func(ep *Endpoint) {
			defer wg.Done()
			defer func() { <-semaphore }() // 释放信号量

			// 执行连通性检测（复用现有方法）
			m.checkEndpointHealth(ep)

			// 获取检测结果（需要加锁读取）
			ep.mutex.RLock()
			healthy := ep.Status.Healthy
			responseTime := ep.Status.ResponseTime
			ep.mutex.RUnlock()

			// 统计检测结果
			countMu.Lock()
			if healthy {
				healthyCount++
			} else {
				unhealthyCount++
			}
			countMu.Unlock()

			// 记录检测结果
			healthStatus := "❌ 不可达"
			if healthy {
				healthStatus = "✅ 可达"
			}
			slog.Debug(fmt.Sprintf("   检测端点: %s - 状态: %s, 响应时间: %s",
				ep.Config.Name,
				healthStatus,
				utils.FormatResponseTime(responseTime),
			))
		}(endpoint)
	}

	// 等待所有检测完成
	wg.Wait()

	slog.Info(fmt.Sprintf("✅ [批量连通性测试] 完成，共检测 %d 个端点 (可达: %d, 不可达: %d)",
		len(endpoints), healthyCount, unhealthyCount))

	return healthyCount, unhealthyCount, nil
}
