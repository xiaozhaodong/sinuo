// endpoint_selection.go - 端点选择/路由功能
// 包含健康端点获取与故障转移端点选择

package endpoint

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
)

func (m *Manager) GetHealthyEndpoints() []*Endpoint {
	return m.PrepareRouteCandidates(context.Background(), RouteRequestProfile{}).Candidates
}

func (m *Manager) GetHealthyEndpointsForRoute(profile RouteRequestProfile) []*Endpoint {
	return m.PrepareRouteCandidates(context.Background(), profile).Candidates
}

func findEndpointInSnapshot(snapshot []*Endpoint, name string) *Endpoint {
	for _, endpoint := range snapshot {
		if endpoint.Config.Name == name {
			return endpoint
		}
	}
	return nil
}

func (m *Manager) shouldSkipByRouteProfile(endpoint *Endpoint, profile *RouteRequestProfile) bool {
	if endpoint == nil || profile == nil {
		return false
	}
	if hit, reason := m.routeState.HasNegativeHit(endpoint.Config.Name, *profile); hit {
		slog.Info(fmt.Sprintf("⏭️ [路由负向缓存] 跳过端点: %s, reason=%s, model=%s, path=%s",
			endpoint.Config.Name, reason, profile.Model, profile.Path))
		return true
	}
	return false
}

func (m *Manager) FilterRouteCandidates(endpoints []*Endpoint, profile RouteRequestProfile) []*Endpoint {
	if len(endpoints) == 0 {
		return endpoints
	}

	override := m.routeOverride.Snapshot()
	filtered := make([]*Endpoint, 0, len(endpoints))
	for _, endpoint := range endpoints {
		if endpoint == nil {
			continue
		}
		if override.Mode == RouteModeManualFixed && override.EndpointName != "" && endpoint.Config.Name != override.EndpointName {
			continue
		}
		if m.shouldSkipByRouteProfile(endpoint, &profile) {
			continue
		}
		filtered = append(filtered, endpoint)
	}
	return m.applyRouteOverrideOrder(filtered)
}

func (m *Manager) applyRouteOverrideOrder(endpoints []*Endpoint) []*Endpoint {
	override := m.routeOverride.Snapshot()
	if override.Mode != RouteModeManualPreferred || override.EndpointName == "" || len(endpoints) < 2 {
		return endpoints
	}

	sort.SliceStable(endpoints, func(i, j int) bool {
		if endpoints[i].Config.Name == override.EndpointName {
			return true
		}
		if endpoints[j].Config.Name == override.EndpointName {
			return false
		}
		return false
	})
	return endpoints
}

// GetEndpointByName 返回可路由的同名端点。
func (m *Manager) GetEndpointByName(name string) *Endpoint {
	endpoint := m.GetEndpointByNameAny(name)
	if endpoint == nil || !m.IsEndpointRoutable(endpoint) {
		return nil
	}
	return endpoint
}

// GetEndpointByNameAny returns an endpoint by name from all endpoints (ignoring group status)
func (m *Manager) GetEndpointByNameAny(name string) *Endpoint {
	m.endpointsMu.RLock()
	defer m.endpointsMu.RUnlock()

	for _, endpoint := range m.endpoints {
		endpoint.mutex.RLock()
		matches := endpoint.Config.Name == name
		endpoint.mutex.RUnlock()
		if matches {
			return endpoint
		}
	}
	return nil
}

// GetAllEndpoints returns all endpoints (deprecated: use GetEndpoints instead)
func (m *Manager) GetAllEndpoints() []*Endpoint {
	m.endpointsMu.RLock()
	defer m.endpointsMu.RUnlock()

	result := make([]*Endpoint, len(m.endpoints))
	copy(result, m.endpoints)
	return result
}

// GetEndpoints returns all endpoints for Web interface
func (m *Manager) GetEndpoints() []*Endpoint {
	m.endpointsMu.RLock()
	defer m.endpointsMu.RUnlock()

	result := make([]*Endpoint, len(m.endpoints))
	copy(result, m.endpoints)
	return result
}

// GetEndpointStatus returns the status of an endpoint by name
func (m *Manager) GetEndpointStatus(name string) EndpointStatus {
	m.endpointsMu.RLock()
	defer m.endpointsMu.RUnlock()

	for _, ep := range m.endpoints {
		if ep.Config.Name == name {
			ep.mutex.RLock()
			status := ep.Status
			ep.mutex.RUnlock()
			return status
		}
	}
	return EndpointStatus{}
}

// GetEndpointCount 返回当前端点数量（v5.0+ 新增）
func (m *Manager) GetEndpointCount() int {
	m.endpointsMu.RLock()
	defer m.endpointsMu.RUnlock()
	return len(m.endpoints)
}
