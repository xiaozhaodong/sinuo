package main

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"sinuo/internal/endpoint"
	"sinuo/internal/service"
	"sinuo/internal/store"
)

const EventClaudeRoutingUpdate = "claude-routing:update"

type ClaudeRoutingState struct {
	Mode                  string   `json:"mode"`
	EndpointName          string   `json:"endpoint_name"`
	SetBy                 string   `json:"set_by"`
	SetAt                 string   `json:"set_at"`
	FallbackEnabled       bool     `json:"fallback_enabled"`
	Revision              int64    `json:"revision"`
	FallbackReason        string   `json:"fallback_reason"`
	LastEffectiveEndpoint string   `json:"last_effective_endpoint"`
	LastDecisionAt        string   `json:"last_decision_at"`
	AvailableEndpoints    []string `json:"available_endpoints"`
}

type SetClaudeRoutingOverrideInput struct {
	Mode            string `json:"mode"`
	EndpointName    string `json:"endpoint_name"`
	FallbackEnabled bool   `json:"fallback_enabled"`
}

func (a *App) GetClaudeRoutingState() (ClaudeRoutingState, error) {
	a.mu.RLock()
	manager := a.endpointManager
	a.mu.RUnlock()

	if manager == nil {
		return ClaudeRoutingState{Mode: endpoint.RouteModeAuto, FallbackEnabled: true}, nil
	}
	return a.buildClaudeRoutingState(manager.GetClaudeRoutingOverride()), nil
}

func (a *App) SetClaudeRoutingOverride(input SetClaudeRoutingOverrideInput) (ClaudeRoutingState, error) {
	mode := endpoint.NormalizeRouteMode(input.Mode)
	if mode == endpoint.RouteModeAuto {
		return a.ClearClaudeRoutingOverride()
	}
	if input.EndpointName == "" {
		return ClaudeRoutingState{}, fmt.Errorf("手动路由需要指定端点")
	}

	a.mu.RLock()
	manager := a.endpointManager
	a.mu.RUnlock()
	if manager == nil {
		return ClaudeRoutingState{}, fmt.Errorf("端点管理器未初始化")
	}
	if !manager.IsClaudeRoutingReady() {
		return ClaudeRoutingState{}, fmt.Errorf("Claude 路由未就绪（routing_not_ready），请稍后重试")
	}
	// v8 §6.4：串行协调器，事务持久化成功后再原子发布运行态；
	// 用户意图只写 route override，不再先激活 endpoint（§11.2 规则 4）。
	// 端点存在性检查必须在 routingMu 内：删除流程同样持 routingMu，
	// 消除「检查存在 → 端点被删 → 写入 fixed」的竞态窗口
	a.routingMu.Lock()
	defer a.routingMu.Unlock()
	if a.modelRoutingService != nil {
		if snapshot := a.modelRoutingService.CurrentSnapshot(); snapshot != nil && snapshot.Enabled() {
			return ClaudeRoutingState{}, fmt.Errorf("按模型路由已启用，请在模型路由配置中调整组内策略")
		}
	}

	if manager.GetEndpointByNameAny(input.EndpointName) == nil {
		return ClaudeRoutingState{}, fmt.Errorf("端点 '%s' 不存在", input.EndpointName)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	fallbackEnabled := input.FallbackEnabled
	if mode == endpoint.RouteModeManualPreferred {
		fallbackEnabled = true
	}
	current := manager.GetClaudeRoutingOverride()
	next := endpoint.RouteOverrideState{
		Mode:            mode,
		EndpointName:    input.EndpointName,
		SetBy:           endpoint.RouteCallerUser,
		SetAt:           time.Now(),
		FallbackEnabled: fallbackEnabled,
		Revision:        current.Revision + 1,
	}

	if err := a.persistClaudeRoutingState(ctx, next); err != nil {
		// 写库失败：不修改运行态、不发事件（§6.4 规则 5）
		return ClaudeRoutingState{}, fmt.Errorf("持久化路由状态失败，未生效: %w", err)
	}
	state := manager.ApplyPersistedClaudeRoutingState(next)

	result := a.buildClaudeRoutingState(state)
	a.emitClaudeRoutingUpdate(result)
	a.emitEndpointUpdate()
	return result, nil
}

func (a *App) ClearClaudeRoutingOverride() (ClaudeRoutingState, error) {
	a.mu.RLock()
	manager := a.endpointManager
	a.mu.RUnlock()
	if manager == nil {
		return ClaudeRoutingState{Mode: endpoint.RouteModeAuto, FallbackEnabled: true}, nil
	}

	a.routingMu.Lock()
	defer a.routingMu.Unlock()

	return a.clearClaudeRoutingOverrideLocked(manager)
}

// clearClaudeRoutingOverrideLocked 清回 Auto 并持久化（调用方必须已持有 routingMu）
func (a *App) clearClaudeRoutingOverrideLocked(manager *endpoint.Manager) (ClaudeRoutingState, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	current := manager.GetClaudeRoutingOverride()
	next := endpoint.RouteOverrideState{
		Mode:            endpoint.RouteModeAuto,
		SetBy:           endpoint.RouteCallerUser,
		SetAt:           time.Now(),
		FallbackEnabled: true,
		Revision:        current.Revision + 1,
	}
	if err := a.persistClaudeRoutingState(ctx, next); err != nil {
		return ClaudeRoutingState{}, fmt.Errorf("持久化路由状态失败，未生效: %w", err)
	}
	state := manager.ApplyPersistedClaudeRoutingState(next)

	result := a.buildClaudeRoutingState(state)
	a.emitClaudeRoutingUpdate(result)
	return result, nil
}

// restoreClaudeRoutingOverrideLocked 补偿性恢复此前的 manual override
// （删除端点失败时回滚；调用方必须已持有 routingMu）
func (a *App) restoreClaudeRoutingOverrideLocked(manager *endpoint.Manager, prev endpoint.RouteOverrideState) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	current := manager.GetClaudeRoutingOverride()
	next := endpoint.RouteOverrideState{
		Mode:            prev.Mode,
		EndpointName:    prev.EndpointName,
		SetBy:           prev.SetBy,
		SetAt:           time.Now(),
		FallbackEnabled: prev.FallbackEnabled,
		Revision:        current.Revision + 1,
	}
	if err := a.persistClaudeRoutingState(ctx, next); err != nil {
		return err
	}
	state := manager.ApplyPersistedClaudeRoutingState(next)
	a.emitClaudeRoutingUpdate(a.buildClaudeRoutingState(state))
	return nil
}

func (a *App) ClearNegativeHitCache(endpointName string) error {
	a.mu.RLock()
	manager := a.endpointManager
	a.mu.RUnlock()
	if manager == nil {
		return fmt.Errorf("端点管理器未初始化")
	}

	manager.ClearNegativeHitCache(endpointName)
	return nil
}

func (a *App) loadClaudeRoutingOverride(ctx context.Context) {
	manager := a.endpointManager
	if a.settingsService == nil || manager == nil {
		return
	}

	// §6.4 规则 7：一次读取完整 category；任何读取失败保持 not_ready，不按默认 Auto 覆盖
	records, err := a.settingsService.GetByCategory(ctx, service.CategoryClaudeRouting)
	if err != nil {
		slog.Error("❌ [Claude路由] 启动读取路由状态失败，Claude 路由保持 not_ready", "error", err)
		manager.SetClaudeRoutingReady(false)
		return
	}
	values := make(map[string]string, len(records))
	for _, record := range records {
		if record != nil {
			values[record.Key] = record.Value
		}
	}

	mode := endpoint.NormalizeRouteMode(values["mode"])
	revision, _ := strconv.ParseInt(values["revision"], 10, 64)

	if mode == endpoint.RouteModeAuto {
		manager.ApplyPersistedClaudeRoutingState(endpoint.RouteOverrideState{
			Mode: endpoint.RouteModeAuto, SetBy: endpoint.RouteCallerStartupRecovery,
			FallbackEnabled: true, Revision: revision,
		})
		manager.SetClaudeRoutingReady(true)
		return
	}

	endpointName := values["endpoint_name"]
	if endpointName == "" || manager.GetEndpointByNameAny(endpointName) == nil {
		// 完整快照读取成功且目标确实缺失/不存在，才允许写回 Auto
		slog.Warn("⚠️ [Claude路由] 持久化手动端点不存在，已恢复自动路由", "endpoint", endpointName)
		next := endpoint.RouteOverrideState{
			Mode: endpoint.RouteModeAuto, SetBy: endpoint.RouteCallerStartupRecovery,
			SetAt: time.Now(), FallbackEnabled: true, Revision: revision + 1,
		}
		if err := a.persistClaudeRoutingState(ctx, next); err != nil {
			slog.Warn("⚠️ [Claude路由] 恢复自动路由持久化失败", "error", err)
		}
		manager.ApplyPersistedClaudeRoutingState(next)
		manager.SetClaudeRoutingReady(true)
		return
	}

	fallbackEnabled := mode == endpoint.RouteModeManualPreferred
	if raw, ok := values["fallback_enabled"]; ok && raw != "" {
		fallbackEnabled = raw == "true" || raw == "1" || raw == "yes"
	}
	setAt := time.Now()
	if parsed, err := time.Parse(time.RFC3339, values["set_at"]); err == nil {
		setAt = parsed
	}
	setBy := values["set_by"]
	if setBy == "" {
		setBy = endpoint.RouteCallerStartupRecovery
	}

	state := manager.ApplyPersistedClaudeRoutingState(endpoint.RouteOverrideState{
		Mode:            mode,
		EndpointName:    endpointName,
		SetBy:           setBy,
		SetAt:           setAt,
		FallbackEnabled: fallbackEnabled,
		Revision:        revision,
	})
	manager.SetClaudeRoutingReady(true)
	slog.Info("✅ [Claude路由] 已恢复持久化路由状态", "mode", state.Mode, "endpoint", state.EndpointName)
}

func (a *App) persistClaudeRoutingState(ctx context.Context, state endpoint.RouteOverrideState) error {
	if a.settingsService == nil || a.settingsStore == nil {
		return nil
	}

	values := map[string]string{
		"mode":             endpoint.NormalizeRouteMode(state.Mode),
		"endpoint_name":    state.EndpointName,
		"set_by":           state.SetBy,
		"set_at":           formatRouteTime(state.SetAt),
		"fallback_enabled": fmt.Sprintf("%t", state.FallbackEnabled),
		"revision":         strconv.FormatInt(state.Revision, 10),
	}

	records := make([]*store.SettingRecord, 0, len(values))
	for _, defaultRecord := range a.settingsService.GetAllDefaults() {
		if defaultRecord.Category != service.CategoryClaudeRouting {
			continue
		}
		record := *defaultRecord
		if value, ok := values[record.Key]; ok {
			record.Value = value
		}
		records = append(records, &record)
	}
	return a.settingsStore.BatchSet(ctx, records)
}

func (a *App) buildClaudeRoutingState(state endpoint.RouteOverrideState) ClaudeRoutingState {
	if state.Mode == "" {
		state.Mode = endpoint.RouteModeAuto
	}
	return ClaudeRoutingState{
		Mode:                  state.Mode,
		EndpointName:          state.EndpointName,
		SetBy:                 state.SetBy,
		SetAt:                 formatRouteTime(state.SetAt),
		FallbackEnabled:       state.FallbackEnabled,
		Revision:              state.Revision,
		FallbackReason:        state.FallbackReason,
		LastEffectiveEndpoint: state.LastEffectiveEndpoint,
		LastDecisionAt:        formatRouteTime(state.LastDecisionAt),
		AvailableEndpoints:    a.availableClaudeEndpoints(),
	}
}

func (a *App) availableClaudeEndpoints() []string {
	a.mu.RLock()
	manager := a.endpointManager
	a.mu.RUnlock()
	if manager == nil {
		return []string{}
	}
	endpoints := manager.GetAllEndpoints()
	names := make([]string, 0, len(endpoints))
	for _, ep := range endpoints {
		if ep == nil {
			continue
		}
		names = append(names, ep.Config.Name)
	}
	return names
}

func (a *App) emitClaudeRoutingUpdate(state ClaudeRoutingState) {
	if a.ctx == nil {
		return
	}
	a.emit(EventClaudeRoutingUpdate, state)
}

func formatRouteTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return formatAPITime(t)
}
