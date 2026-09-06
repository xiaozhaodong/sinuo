package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"cc-forwarder/internal/endpoint"
	"cc-forwarder/internal/service"
	"cc-forwarder/internal/store"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const EventModelRoutingUpdate = "model-routing:update"

type ModelRoutingSettingsInfo struct {
	ClaudeEnabled      bool   `json:"claude_enabled"`
	CodexEnabled       bool   `json:"codex_enabled"`
	Status             string `json:"status"`  // 兼容旧前端，等同 ClaudeStatus
	Version            int64  `json:"version"` // 兼容旧前端，等同 ClaudeVersion
	UpdatedAt          string `json:"updated_at"`
	BindingCount       int    `json:"binding_count"` // 兼容旧前端，等同 ClaudeBindingCount
	ClaudeStatus       string `json:"claude_status"`
	CodexStatus        string `json:"codex_status"`
	ClaudeVersion      int64  `json:"claude_version"`
	CodexVersion       int64  `json:"codex_version"`
	ClaudeBindingCount int    `json:"claude_binding_count"`
	CodexBindingCount  int    `json:"codex_binding_count"`
}

type CodexModelRouteBindingInfo struct {
	AssignmentID   int64          `json:"assignment_id"`
	Model          string         `json:"model"`
	AccountIDs     []int64        `json:"account_ids"`
	MemberEnabled  map[int64]bool `json:"member_enabled"`
	RouteMode      string         `json:"route_mode"`
	RouteTargetID  int64          `json:"route_target_account_id,omitempty"`
	Enabled        bool           `json:"enabled"`
	Note           string         `json:"note"`
	LastAccountID  int64          `json:"last_account_id,omitempty"`
	LastOutcome    string         `json:"last_outcome,omitempty"`
	LastDecisionAt string         `json:"last_decision_at,omitempty"`
	UpdatedAt      string         `json:"updated_at"`
}

type CodexModelRouteBindingInput struct {
	Model         string         `json:"model"`
	AccountIDs    []int64        `json:"account_ids"`
	MemberEnabled map[int64]bool `json:"member_enabled"`
	RouteMode     string         `json:"route_mode"`
	RouteTargetID int64          `json:"route_target_account_id"`
	Enabled       bool           `json:"enabled"`
	Note          string         `json:"note"`
}

type UpdateModelRoutingSettingsInput struct {
	ClaudeEnabled *bool `json:"claude_enabled,omitempty"`
	CodexEnabled  *bool `json:"codex_enabled,omitempty"`
}

type ModelRoutingMemberInfo struct {
	Name                string `json:"name"`
	Exists              bool   `json:"exists"`
	Priority            int    `json:"priority"`
	AvailabilityEnabled bool   `json:"availability_enabled"`
	AutoScheduleEnabled bool   `json:"auto_schedule_enabled"`
	Healthy             bool   `json:"healthy"`
	CooldownUntil       string `json:"cooldown_until,omitempty"`
	CooldownReason      string `json:"cooldown_reason,omitempty"`
	Detail              string `json:"detail,omitempty"`
	Status              string `json:"status"`
}

// ModelRouteBindingInfo 是一条「模型 → 端点集合」映射的前端视图。
// 底层的上游组是该模型的专属实现细节，不作为独立概念暴露给 UI。
type ModelRouteBindingInfo struct {
	AssignmentID   int64                    `json:"assignment_id"`
	Model          string                   `json:"model"`
	Endpoints      []ModelRoutingMemberInfo `json:"endpoints"`
	RouteMode      string                   `json:"route_mode"`
	RouteTarget    string                   `json:"route_target"`
	Enabled        bool                     `json:"enabled"`
	Note           string                   `json:"note"`
	SharedModels   []string                 `json:"shared_models"`
	LastEffective  string                   `json:"last_effective_endpoint,omitempty"`
	LastDecisionAt string                   `json:"last_decision_at,omitempty"`
	LastOutcome    string                   `json:"last_outcome,omitempty"`
	UpdatedAt      string                   `json:"updated_at"`
}

type ModelRouteBindingInput struct {
	Model       string   `json:"model"`
	Endpoints   []string `json:"endpoints"`
	RouteMode   string   `json:"route_mode"`
	RouteTarget string   `json:"route_target"`
	Enabled     bool     `json:"enabled"`
	Note        string   `json:"note"`
}

type ModelRoutingTestInput struct {
	Model string `json:"model"`
}

type ModelRoutingTestResult struct {
	Model            string                   `json:"model"`
	Assigned         bool                     `json:"assigned"`
	ScopeKind        string                   `json:"scope_kind"`
	GroupID          int64                    `json:"group_id,omitempty"`
	GroupName        string                   `json:"group_name,omitempty"`
	RouteMode        string                   `json:"route_mode,omitempty"`
	RouteTarget      string                   `json:"route_target,omitempty"`
	UnassignedReason string                   `json:"unassigned_reason,omitempty"`
	Members          []ModelRoutingMemberInfo `json:"members"`
}

type CodexModelRoutingTestInput struct {
	Model string `json:"model"`
}

type CodexModelRoutingTestAccountInfo struct {
	AccountID    int64  `json:"account_id"`
	AccountName  string `json:"account_name"`
	ProviderType string `json:"provider_type"`
	Status       string `json:"status"`
	Detail       string `json:"detail"`
	Decision     string `json:"decision"`
	Reason       string `json:"reason"`
}

type CodexModelRoutingTestResult struct {
	Model                string                             `json:"model"`
	Assigned             bool                               `json:"assigned"`
	ScopeKind            string                             `json:"scope_kind"`
	GroupID              int64                              `json:"group_id,omitempty"`
	GroupName            string                             `json:"group_name,omitempty"`
	RouteMode            string                             `json:"route_mode,omitempty"`
	RouteTargetAccountID int64                              `json:"route_target_account_id,omitempty"`
	UnassignedReason     string                             `json:"unassigned_reason,omitempty"`
	Accounts             []CodexModelRoutingTestAccountInfo `json:"accounts"`
}

func (a *App) modelRoutingServiceSnapshot() (*service.ModelRoutingService, error) {
	a.mu.RLock()
	svc := a.modelRoutingService
	a.mu.RUnlock()
	if svc == nil {
		return nil, fmt.Errorf("模型路由服务未初始化")
	}
	return svc, nil
}

func (a *App) GetModelRoutingSettings() (ModelRoutingSettingsInfo, error) {
	svc, err := a.modelRoutingServiceSnapshot()
	if err != nil {
		return ModelRoutingSettingsInfo{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	record, err := svc.GetSettings(ctx)
	if err != nil {
		return ModelRoutingSettingsInfo{}, err
	}
	bindings, err := svc.ListBindings(ctx)
	if err != nil {
		return ModelRoutingSettingsInfo{}, err
	}
	claudeStatus := svc.Status()
	claudeVersion := svc.SnapshotVersion()
	claudeBindingCount := len(bindings)
	info := ModelRoutingSettingsInfo{
		Status:             claudeStatus,
		Version:            claudeVersion,
		BindingCount:       claudeBindingCount,
		ClaudeStatus:       claudeStatus,
		ClaudeVersion:      claudeVersion,
		ClaudeBindingCount: claudeBindingCount,
		CodexStatus:        "unavailable",
	}
	a.mu.RLock()
	codexSvc := a.codexModelRoutingService
	a.mu.RUnlock()
	if codexSvc != nil {
		codexBindings, codexErr := codexSvc.ListBindings(ctx)
		if codexErr != nil {
			return ModelRoutingSettingsInfo{}, codexErr
		}
		info.CodexStatus = codexSvc.Status()
		info.CodexVersion = codexSvc.SnapshotVersion()
		info.CodexBindingCount = len(codexBindings)
	}
	if record != nil {
		info.ClaudeEnabled = record.ClaudeEnabled
		info.CodexEnabled = record.CodexEnabled
		info.UpdatedAt = formatTime(record.UpdatedAt)
	}
	return info, nil
}

func (a *App) UpdateModelRoutingSettings(input UpdateModelRoutingSettingsInput) (ModelRoutingSettingsInfo, error) {
	svc, err := a.modelRoutingServiceSnapshot()
	if err != nil {
		return ModelRoutingSettingsInfo{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	current, err := svc.GetSettings(ctx)
	if err != nil {
		return ModelRoutingSettingsInfo{}, err
	}
	if current == nil {
		current = &store.ModelRoutingSettingsRecord{ID: 1}
	}
	if input.ClaudeEnabled != nil {
		current.ClaudeEnabled = *input.ClaudeEnabled
	}
	if input.CodexEnabled != nil {
		current.CodexEnabled = *input.CodexEnabled
	}
	if _, err := svc.UpdateSettings(ctx, current); err != nil {
		return ModelRoutingSettingsInfo{}, err
	}
	if a.codexModelRoutingService != nil {
		if err := a.codexModelRoutingService.Initialize(ctx); err != nil {
			return ModelRoutingSettingsInfo{}, err
		}
	}
	a.emitModelRoutingUpdate()
	return a.GetModelRoutingSettings()
}

func (a *App) ListModelRouteBindings() ([]ModelRouteBindingInfo, error) {
	svc, err := a.modelRoutingServiceSnapshot()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bindings, err := svc.ListBindings(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]ModelRouteBindingInfo, 0, len(bindings))
	for _, binding := range bindings {
		endpoints := make([]ModelRoutingMemberInfo, 0, len(binding.Endpoints))
		for _, name := range binding.Endpoints {
			endpoints = append(endpoints, modelRoutingMemberInfo(svc.EndpointMember(name)))
		}
		result = append(result, ModelRouteBindingInfo{
			AssignmentID: binding.AssignmentID, Model: binding.Model, Endpoints: endpoints,
			RouteMode: binding.RouteMode, RouteTarget: binding.RouteTarget,
			Enabled: binding.Enabled, Note: binding.Note, SharedModels: binding.SharedModels,
			LastEffective: binding.Runtime.LastEffectiveEndpoint, LastDecisionAt: formatTime(binding.Runtime.LastDecisionAt),
			LastOutcome: binding.Runtime.LastOutcome, UpdatedAt: formatTime(binding.UpdatedAt),
		})
	}
	return result, nil
}

func (a *App) CreateModelRouteBinding(input ModelRouteBindingInput) (ModelRouteBindingInfo, error) {
	svc, err := a.modelRoutingServiceSnapshot()
	if err != nil {
		return ModelRouteBindingInfo{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	assignmentID, err := svc.CreateBinding(ctx, modelRouteBindingInput(input))
	if err != nil {
		return ModelRouteBindingInfo{}, err
	}
	a.emitModelRoutingUpdate()
	bindings, err := a.ListModelRouteBindings()
	if err != nil {
		return ModelRouteBindingInfo{}, err
	}
	for _, binding := range bindings {
		if binding.AssignmentID == assignmentID {
			return binding, nil
		}
	}
	return ModelRouteBindingInfo{}, fmt.Errorf("创建模型映射后无法读取记录 %d", assignmentID)
}

func (a *App) UpdateModelRouteBinding(assignmentID int64, input ModelRouteBindingInput) error {
	svc, err := a.modelRoutingServiceSnapshot()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := svc.UpdateBinding(ctx, assignmentID, modelRouteBindingInput(input)); err != nil {
		return err
	}
	a.emitModelRoutingUpdate()
	return nil
}

func (a *App) DeleteModelRouteBinding(assignmentID int64) error {
	svc, err := a.modelRoutingServiceSnapshot()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := svc.DeleteBinding(ctx, assignmentID); err != nil {
		return err
	}
	a.emitModelRoutingUpdate()
	return nil
}

func (a *App) codexModelRoutingSnapshot() (*service.CodexModelRoutingService, error) {
	a.mu.RLock()
	svc := a.codexModelRoutingService
	a.mu.RUnlock()
	if svc == nil {
		return nil, fmt.Errorf("Codex 模型路由服务未初始化")
	}
	return svc, nil
}

func (a *App) ListCodexModelRouteBindings() ([]CodexModelRouteBindingInfo, error) {
	svc, err := a.codexModelRoutingSnapshot()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bindings, err := svc.ListBindings(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]CodexModelRouteBindingInfo, 0, len(bindings))
	for _, b := range bindings {
		result = append(result, CodexModelRouteBindingInfo{AssignmentID: b.AssignmentID, Model: b.Model, AccountIDs: b.AccountIDs, MemberEnabled: b.MemberEnabled, RouteMode: b.RouteMode, RouteTargetID: b.RouteTargetID, Enabled: b.Enabled, Note: b.Note, LastAccountID: b.LastAccountID, LastOutcome: b.LastOutcome, LastDecisionAt: formatTime(b.LastDecisionAt), UpdatedAt: formatTime(b.UpdatedAt)})
	}
	return result, nil
}

func (a *App) CreateCodexModelRouteBinding(input CodexModelRouteBindingInput) (CodexModelRouteBindingInfo, error) {
	svc, err := a.codexModelRoutingSnapshot()
	if err != nil {
		return CodexModelRouteBindingInfo{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	id, err := svc.CreateBinding(ctx, service.CodexModelRouteBindingInput{Model: input.Model, AccountIDs: input.AccountIDs, MemberEnabled: input.MemberEnabled, RouteMode: input.RouteMode, RouteTargetID: input.RouteTargetID, Enabled: input.Enabled, Note: input.Note})
	if err != nil {
		return CodexModelRouteBindingInfo{}, err
	}
	a.emitModelRoutingUpdate()
	bindings, err := a.ListCodexModelRouteBindings()
	if err != nil {
		return CodexModelRouteBindingInfo{}, err
	}
	for _, binding := range bindings {
		if binding.AssignmentID == id {
			return binding, nil
		}
	}
	return CodexModelRouteBindingInfo{}, fmt.Errorf("创建 Codex 模型绑定后无法读取记录 %d", id)
}

func (a *App) DeleteCodexModelRouteBinding(assignmentID int64) error {
	svc, err := a.codexModelRoutingSnapshot()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := svc.DeleteBinding(ctx, assignmentID); err != nil {
		return err
	}
	a.emitModelRoutingUpdate()
	return nil
}

func (a *App) UpdateCodexModelRouteBinding(assignmentID int64, input CodexModelRouteBindingInput) error {
	svc, err := a.codexModelRoutingSnapshot()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := svc.UpdateBinding(ctx, assignmentID, service.CodexModelRouteBindingInput{Model: input.Model, AccountIDs: input.AccountIDs, MemberEnabled: input.MemberEnabled, RouteMode: input.RouteMode, RouteTargetID: input.RouteTargetID, Enabled: input.Enabled, Note: input.Note}); err != nil {
		return err
	}
	a.emitModelRoutingUpdate()
	return nil
}

func modelRouteBindingInput(input ModelRouteBindingInput) service.ModelRouteBindingInput {
	return service.ModelRouteBindingInput{
		Model:       input.Model,
		Endpoints:   append([]string(nil), input.Endpoints...),
		RouteMode:   input.RouteMode,
		RouteTarget: input.RouteTarget,
		Enabled:     input.Enabled,
		Note:        input.Note,
	}
}

func modelRoutingMemberInfo(view endpoint.EndpointMemberView) ModelRoutingMemberInfo {
	status := "missing"
	detail := "端点不存在"
	if view.Exists {
		status = "ok"
		detail = "可参与组内调度"
		if !view.AvailabilityEnabled {
			status = "availability_disabled"
			detail = "端点已硬关闭"
		} else if !view.AutoScheduleEnabled {
			status = "auto_schedule_disabled"
			detail = "端点未参与自动调度"
		} else if !view.CooldownUntil.IsZero() && time.Now().Before(view.CooldownUntil) {
			status = "cooldown"
			detail = view.CooldownReason
		} else if !view.Healthy {
			status = "unhealthy"
			detail = "最近一次检测不可达"
		}
	}
	return ModelRoutingMemberInfo{
		Name: view.Name, Exists: view.Exists, Priority: view.Priority,
		AvailabilityEnabled: view.AvailabilityEnabled, AutoScheduleEnabled: view.AutoScheduleEnabled,
		Healthy: view.Healthy, CooldownUntil: formatTime(view.CooldownUntil), CooldownReason: view.CooldownReason, Detail: detail, Status: status,
	}
}

func (a *App) TestModelRouting(input ModelRoutingTestInput) (ModelRoutingTestResult, error) {
	svc, err := a.modelRoutingServiceSnapshot()
	if err != nil {
		return ModelRoutingTestResult{}, err
	}
	model := strings.TrimSpace(input.Model)
	scope, ok := svc.ResolveScope(model)
	if !ok {
		// 模型路由未启用：请求会按全局端点策略走，试算面板如实反映这一点。
		scope = endpoint.RouteScope{Kind: endpoint.RouteScopeGlobal}
	}
	result := ModelRoutingTestResult{Model: model, Assigned: scope.Kind == endpoint.RouteScopeUpstreamGroup, ScopeKind: scope.Kind, GroupID: scope.GroupID, GroupName: scope.GroupName, RouteMode: scope.Mode, RouteTarget: scope.Target, UnassignedReason: scope.UnassignedReason, Members: []ModelRoutingMemberInfo{}}
	for _, member := range scope.Members {
		result.Members = append(result.Members, modelRoutingMemberInfo(svc.EndpointMember(member)))
	}
	return result, nil
}

func (a *App) TestCodexModelRouting(input CodexModelRoutingTestInput) (CodexModelRoutingTestResult, error) {
	svc, err := a.codexModelRoutingSnapshot()
	if err != nil {
		return CodexModelRoutingTestResult{}, err
	}
	model := strings.TrimSpace(input.Model)
	scope, applies := svc.ResolveScope(model)
	result := CodexModelRoutingTestResult{
		Model: model, Assigned: applies && scope.Kind == "upstream_group", ScopeKind: scope.Kind,
		GroupID: scope.GroupID, GroupName: scope.GroupName, RouteMode: scope.Mode,
		RouteTargetAccountID: scope.TargetAccountID, UnassignedReason: scope.UnassignedReason,
		Accounts: []CodexModelRoutingTestAccountInfo{},
	}
	if !applies || scope.Kind != "upstream_group" {
		if !applies {
			result.ScopeKind = "global"
		}
		return result, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	prepared, err := svc.Prepare(ctx, model, "/v1/responses")
	if err != nil {
		return CodexModelRoutingTestResult{}, err
	}
	for _, candidate := range prepared.Candidates {
		status := "skipped"
		if candidate.Decision == "selected" || candidate.Decision == "eligible" {
			status = "eligible"
		}
		result.Accounts = append(result.Accounts, CodexModelRoutingTestAccountInfo{
			AccountID: candidate.AccountID, AccountName: candidate.AccountName, ProviderType: candidate.ProviderType,
			Status: status, Detail: candidate.ReasonDetail, Decision: candidate.Decision, Reason: candidate.Reason,
		})
	}
	return result, nil
}

func (a *App) emitModelRoutingUpdate() {
	if a == nil || a.ctx == nil {
		return
	}
	settings, err := a.GetModelRoutingSettings()
	if err != nil {
		return
	}
	runtime.EventsEmit(a.ctx, EventModelRoutingUpdate, settings)
}
