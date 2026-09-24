// app_api_endpoint.go - 端点管理 API (Wails Bindings)
// 包含端点状态、优先级设置与连通性测试等功能

package main

import (
	"fmt"

	"cc-forwarder/internal/endpoint"
)

// ============================================================
// 端点管理 API
// ============================================================

// EndpointInfo 端点信息
type EndpointInfo struct {
	Name            string  `json:"name"`
	URL             string  `json:"url"`
	Priority        int     `json:"priority"`
	Healthy         bool    `json:"healthy"`
	LastCheck       string  `json:"last_check"`
	ResponseTimeMs  float64 `json:"response_time_ms"`
	ConsecutiveFail int     `json:"consecutive_fail"`
}

// EndpointScheduleDecisionInfo 最近一次端点调度中的单端点决策。
type EndpointScheduleDecisionInfo struct {
	Name           string `json:"name"`
	Decision       string `json:"decision"`
	Reason         string `json:"reason"`
	AvailableAt    string `json:"available_at"`
	RuntimeOutcome string `json:"runtime_outcome,omitempty"`
	RuntimeError   string `json:"runtime_error,omitempty"`
}

// LatestEndpointScheduleSnapshotInfo 最近一次端点调度快照。
type LatestEndpointScheduleSnapshotInfo struct {
	HasSnapshot          bool                            `json:"has_snapshot"`
	RequestID            string                          `json:"request_id,omitempty"`
	CapturedAt           string                          `json:"captured_at"`
	UpdatedAt            string                          `json:"updated_at"`
	RequestPath          string                          `json:"request_path"`
	SelectedEndpoint     string                          `json:"selected_endpoint"`
	RouteMode            string                          `json:"route_mode"`
	RouteEndpointName    string                          `json:"route_endpoint_name"`
	RouteFallbackEnabled bool                            `json:"route_fallback_enabled"`
	RouteScopeKind       string                          `json:"route_scope_kind,omitempty"`
	RouteScope           string                          `json:"route_scope,omitempty"`
	RouteScopeGroupID    int64                           `json:"route_scope_group_id,omitempty"`
	RouteScopeGroupName  string                          `json:"route_scope_group_name,omitempty"`
	UpstreamGroup        *endpoint.UpstreamGroupSnapshot `json:"upstream_group,omitempty"`
	RequestModel         string                          `json:"request_model,omitempty"`
	ModelRoutingVersion  int64                           `json:"model_routing_version,omitempty"`
	FailoverEnabled      bool                            `json:"failover_enabled"`
	FinalOutcome         string                          `json:"final_outcome"`
	FinalError           string                          `json:"final_error"`
	Summary              string                          `json:"summary"`
	Decisions            []EndpointScheduleDecisionInfo  `json:"decisions"`
}

// GetEndpoints 获取所有端点状态
func (a *App) GetEndpoints() []EndpointInfo {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.endpointManager == nil {
		return []EndpointInfo{}
	}

	endpoints := a.endpointManager.GetAllEndpoints()
	result := make([]EndpointInfo, 0, len(endpoints))

	for _, ep := range endpoints {
		info := EndpointInfo{
			Name:            ep.Config.Name,
			URL:             ep.Config.URL,
			Priority:        ep.Config.Priority,
			Healthy:         ep.Status.Healthy,
			ConsecutiveFail: ep.Status.ConsecutiveFails,
			ResponseTimeMs:  float64(ep.Status.ResponseTime.Milliseconds()),
		}
		if !ep.Status.LastCheck.IsZero() {
			info.LastCheck = formatAPITime(ep.Status.LastCheck)
		}

		result = append(result, info)
	}

	return result
}

// GetLatestEndpointScheduleSnapshot 获取最近一次端点调度快照（Phase 5 观测 API）。
func (a *App) GetLatestEndpointScheduleSnapshot() (LatestEndpointScheduleSnapshotInfo, error) {
	a.mu.RLock()
	manager := a.endpointManager
	a.mu.RUnlock()

	if manager == nil {
		return LatestEndpointScheduleSnapshotInfo{}, fmt.Errorf("端点管理器未初始化")
	}

	snapshot := manager.GetLatestEndpointScheduleSnapshot()
	if snapshot == nil {
		return LatestEndpointScheduleSnapshotInfo{
			HasSnapshot: false,
			Decisions:   []EndpointScheduleDecisionInfo{},
		}, nil
	}

	out := LatestEndpointScheduleSnapshotInfo{
		HasSnapshot:          true,
		RequestID:            snapshot.RequestID,
		CapturedAt:           formatTime(snapshot.CapturedAt),
		UpdatedAt:            formatTime(snapshot.UpdatedAt),
		RequestPath:          snapshot.RequestPath,
		SelectedEndpoint:     snapshot.SelectedEndpoint,
		RouteMode:            snapshot.RouteMode,
		RouteEndpointName:    snapshot.RouteEndpointName,
		RouteFallbackEnabled: snapshot.RouteFallbackEnabled,
		RouteScopeKind:       snapshot.RouteScopeKind,
		RouteScope:           snapshot.RouteScope,
		RouteScopeGroupID:    snapshot.RouteScopeGroupID,
		RouteScopeGroupName:  snapshot.RouteScopeGroupName,
		UpstreamGroup:        snapshot.UpstreamGroup,
		RequestModel:         snapshot.RequestModel,
		ModelRoutingVersion:  snapshot.ModelRoutingVersion,
		FailoverEnabled:      snapshot.FailoverEnabled,
		FinalOutcome:         snapshot.FinalOutcome,
		FinalError:           snapshot.FinalError,
		Summary:              snapshot.Summary,
		Decisions:            make([]EndpointScheduleDecisionInfo, 0, len(snapshot.Decisions)),
	}
	for _, decision := range snapshot.Decisions {
		out.Decisions = append(out.Decisions, EndpointScheduleDecisionInfo{
			Name:           decision.Name,
			Decision:       decision.Decision,
			Reason:         decision.Reason,
			AvailableAt:    formatTime(decision.AvailableAt),
			RuntimeOutcome: decision.RuntimeOutcome,
			RuntimeError:   decision.RuntimeError,
		})
	}
	return out, nil
}

// SetEndpointPriority 设置端点优先级
func (a *App) SetEndpointPriority(name string, priority int) error {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.endpointManager == nil {
		return fmt.Errorf("端点管理器未初始化")
	}

	return a.endpointManager.UpdateEndpointPriority(name, priority)
}

// TriggerHealthCheck 手动触发连通性测试
// 为兼容既有前端绑定，保留原函数名。
func (a *App) TriggerHealthCheck(name string) error {
	a.mu.RLock()
	manager := a.endpointManager
	a.mu.RUnlock()

	if manager == nil {
		return fmt.Errorf("端点管理器未初始化")
	}

	err := manager.ManualHealthCheck(name)
	if err != nil {
		return err
	}

	// 检测完成后，推送端点状态更新到前端
	go a.emitEndpointUpdate()

	return nil
}

// BatchHealthCheckResult 批量连通性测试结果
type BatchHealthCheckResult struct {
	Success        bool   `json:"success"`
	Message        string `json:"message"`
	Total          int    `json:"total"`
	HealthyCount   int    `json:"healthy_count"`
	UnhealthyCount int    `json:"unhealthy_count"`
}

// BatchHealthCheckAll 批量检查所有端点的连通性状态
func (a *App) BatchHealthCheckAll() BatchHealthCheckResult {
	a.mu.RLock()
	manager := a.endpointManager
	a.mu.RUnlock()

	if manager == nil {
		return BatchHealthCheckResult{
			Success: false,
			Message: "端点管理器未初始化",
		}
	}

	// 调用 endpointManager 的批量连通性测试
	healthyCount, unhealthyCount, err := manager.BatchHealthCheckAll()
	if err != nil {
		return BatchHealthCheckResult{
			Success: false,
			Message: err.Error(),
		}
	}

	// 推送端点状态更新到前端
	go a.emitEndpointUpdate()

	return BatchHealthCheckResult{
		Success:        true,
		Message:        "批量连通性测试完成",
		Total:          healthyCount + unhealthyCount,
		HealthyCount:   healthyCount,
		UnhealthyCount: unhealthyCount,
	}
}
