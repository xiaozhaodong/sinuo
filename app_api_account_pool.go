// app_api_account_pool.go - v6.0+ 账号池管理 API (Wails Bindings)
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"cc-forwarder/internal/store"
)

// UpstreamAccountInfo 上游账号信息
type UpstreamAccountInfo struct {
	ID                            int64    `json:"id"`
	ProviderType                  string   `json:"provider_type"`
	AccountName                   string   `json:"account_name"`
	CredentialRaw                 string   `json:"credential_raw"`
	CredentialRawMasked           string   `json:"credential_raw_masked"`
	HasCredential                 bool     `json:"has_credential"`
	IsActiveSelection             bool     `json:"is_active_selection"`
	IsGroupPreferred              bool     `json:"is_group_preferred"`
	BaseURL                       string   `json:"base_url"`
	ModelRewriteRules             string   `json:"model_rewrite_rules"`
	SoftFailureWindowMinutes      *int     `json:"soft_failure_window_minutes"`
	SoftFailureThreshold          *int     `json:"soft_failure_threshold"`
	EnableRequestCompression      bool     `json:"enable_request_compression"`
	StripCodexInternalMetadata    bool     `json:"strip_codex_internal_metadata"`
	CostMultiplier                float64  `json:"cost_multiplier"`
	InputCostMultiplier           float64  `json:"input_cost_multiplier"`
	OutputCostMultiplier          float64  `json:"output_cost_multiplier"`
	CacheCreationCostMultiplier   float64  `json:"cache_creation_cost_multiplier"`
	CacheCreationCostMultiplier1h float64  `json:"cache_creation_cost_multiplier_1h"`
	CacheReadCostMultiplier       float64  `json:"cache_read_cost_multiplier"`
	GroupKey                      string   `json:"group_key"`
	Priority                      int      `json:"priority"`
	Enabled                       bool     `json:"enabled"`
	State                         string   `json:"state"`
	CooldownUntil                 string   `json:"cooldown_until"`
	FailCount                     int      `json:"fail_count"`
	LastSuccessAt                 string   `json:"last_success_at"`
	LastError                     string   `json:"last_error"`
	PlanType                      string   `json:"plan_type"`
	ChatGPTAccountID              string   `json:"chatgpt_account_id"`
	ChatGPTUserID                 string   `json:"chatgpt_user_id"`
	OrganizationID                string   `json:"organization_id"`
	Quota5HUsedPercent            *float64 `json:"quota_5h_used_percent,omitempty"`
	Quota5HResetAt                string   `json:"quota_5h_reset_at"`
	QuotaWeeklyUsedPercent        *float64 `json:"quota_weekly_used_percent,omitempty"`
	QuotaWeeklyResetAt            string   `json:"quota_weekly_reset_at"`
	QuotaStatus                   string   `json:"quota_status"`
	QuotaRefreshedAt              string   `json:"quota_refreshed_at"`
	Fingerprint                   string   `json:"fingerprint"`
	CreatedAt                     string   `json:"created_at"`
	UpdatedAt                     string   `json:"updated_at"`
}

// CreateUpstreamAccountInput 创建/更新账号输入
type CreateUpstreamAccountInput struct {
	ProviderType                  string  `json:"provider_type"`
	AccountName                   string  `json:"account_name"`
	CredentialRaw                 string  `json:"credential_raw"`
	BaseURL                       string  `json:"base_url"`
	ModelRewriteRules             string  `json:"model_rewrite_rules"`
	SoftFailureWindowMinutes      *int    `json:"soft_failure_window_minutes"`
	SoftFailureThreshold          *int    `json:"soft_failure_threshold"`
	EnableRequestCompression      bool    `json:"enable_request_compression"`
	StripCodexInternalMetadata    *bool   `json:"strip_codex_internal_metadata,omitempty"`
	CostMultiplier                float64 `json:"cost_multiplier"`
	InputCostMultiplier           float64 `json:"input_cost_multiplier"`
	OutputCostMultiplier          float64 `json:"output_cost_multiplier"`
	CacheCreationCostMultiplier   float64 `json:"cache_creation_cost_multiplier"`
	CacheCreationCostMultiplier1h float64 `json:"cache_creation_cost_multiplier_1h"`
	CacheReadCostMultiplier       float64 `json:"cache_read_cost_multiplier"`
	GroupKey                      string  `json:"group_key"`
	Priority                      int     `json:"priority"`
	Enabled                       bool    `json:"enabled"`
}

// TestUpstreamAccountResult 测试连通性结果
type TestUpstreamAccountResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// UpstreamAccountCredentialInfo 单账号显式查看凭据响应。
type UpstreamAccountCredentialInfo struct {
	ID                  int64  `json:"id"`
	CredentialRaw       string `json:"credential_raw"`
	CredentialRawMasked string `json:"credential_raw_masked"`
	HasCredential       bool   `json:"has_credential"`
}

// RefreshUpstreamAccountProfileResult 刷新账号画像结果
type RefreshUpstreamAccountProfileResult struct {
	Success     bool   `json:"success"`
	Message     string `json:"message"`
	QuotaStatus string `json:"quota_status,omitempty"`
}

// MoveUpstreamAccountToTierResult 手动主备切换结果。
type MoveUpstreamAccountToTierResult struct {
	Success bool   `json:"success"`
	Changed bool   `json:"changed"`
	Message string `json:"message"`
}

// SwapUpstreamAccountGroupsResult 调度编排整组交换结果。
type SwapUpstreamAccountGroupsResult struct {
	Success bool   `json:"success"`
	Changed bool   `json:"changed"`
	Message string `json:"message"`
}

// SetGroupActiveAccountResult 调度编排中的组内首选账号设置结果。
type SetGroupActiveAccountResult struct {
	Success bool   `json:"success"`
	Changed bool   `json:"changed"`
	Message string `json:"message"`
}

// PinUpstreamAccountSelectionResult 固定具体账号选择结果。
type PinUpstreamAccountSelectionResult struct {
	Success bool   `json:"success"`
	Changed bool   `json:"changed"`
	Message string `json:"message"`
}

// EnableAutomaticAccountSelectionResult 启用编排自动选择结果。
type EnableAutomaticAccountSelectionResult struct {
	Success bool   `json:"success"`
	Changed bool   `json:"changed"`
	Message string `json:"message"`
}

// AccountScheduleCandidateDecisionInfo 最近一次调度候选账号决策信息
type AccountScheduleCandidateDecisionInfo struct {
	AccountID               int64    `json:"account_id"`
	AccountName             string   `json:"account_name"`
	ProviderType            string   `json:"provider_type"`
	Priority                int      `json:"priority"`
	TierIndex               int      `json:"tier_index"`
	TierLabel               string   `json:"tier_label"`
	QuotaStatus             string   `json:"quota_status"`
	EffectiveQuotaRemaining *float64 `json:"effective_quota_remaining,omitempty"`
	FailCount               int      `json:"fail_count"`
	LastSuccessAt           string   `json:"last_success_at"`
	Decision                string   `json:"decision"`
	Reason                  string   `json:"reason"`
	ReasonDetail            string   `json:"reason_detail"`
	RuntimeOutcome          string   `json:"runtime_outcome,omitempty"`
	RuntimeError            string   `json:"runtime_error,omitempty"`
}

// LatestAccountScheduleSnapshotInfo 最近一次调度快照信息
type LatestAccountScheduleSnapshotInfo struct {
	HasSnapshot             bool                                   `json:"has_snapshot"`
	RequestID               string                                 `json:"request_id,omitempty"`
	CapturedAt              string                                 `json:"captured_at"`
	UpdatedAt               string                                 `json:"updated_at"`
	RequestPath             string                                 `json:"request_path"`
	SelectedPriority        int                                    `json:"selected_priority"`
	SelectedTierIndex       int                                    `json:"selected_tier_index"`
	SelectedTierLabel       string                                 `json:"selected_tier_label"`
	DegradedToLowerPriority bool                                   `json:"degraded_to_lower_priority"`
	SelectedAccountID       int64                                  `json:"selected_account_id"`
	SelectedAccountName     string                                 `json:"selected_account_name"`
	FinalOutcome            string                                 `json:"final_outcome"`
	FinalError              string                                 `json:"final_error"`
	Summary                 string                                 `json:"summary"`
	RequestModel            string                                 `json:"request_model,omitempty"`
	ModelRoutingFamily      string                                 `json:"model_routing_family,omitempty"`
	ModelRoutingVersion     int64                                  `json:"model_routing_version,omitempty"`
	RouteScopeKind          string                                 `json:"route_scope_kind,omitempty"`
	RouteScopeGroupID       int64                                  `json:"route_scope_group_id,omitempty"`
	RouteScopeGroupName     string                                 `json:"route_scope_group_name,omitempty"`
	RouteMode               string                                 `json:"route_mode,omitempty"`
	RouteTargetAccountID    int64                                  `json:"route_target_account_id,omitempty"`
	Candidates              []AccountScheduleCandidateDecisionInfo `json:"candidates"`
}

// GetUpstreamAccounts 获取账号列表
func (a *App) GetUpstreamAccounts() ([]UpstreamAccountInfo, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.accountPoolService == nil {
		return nil, fmt.Errorf("账号池服务未启用")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	records, err := a.accountPoolService.ListAccounts(ctx, true)
	if err != nil {
		return nil, fmt.Errorf("获取账号列表失败: %w", err)
	}
	activeSelectionAccountID, hasActiveSelection, err := a.accountPoolService.GetActiveSelectionAccountID(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取当前选中账号失败: %w", err)
	}
	groupPreferredAccountIDs, err := a.accountPoolService.GetGroupPreferredAccountIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取组内首选账号失败: %w", err)
	}

	out := make([]UpstreamAccountInfo, 0, len(records))
	for _, rec := range records {
		groupKey := strings.TrimSpace(strings.ToLower(rec.GroupKey))
		isGroupPreferred := false
		if preferredID, ok := groupPreferredAccountIDs[groupKey]; ok && preferredID > 0 && preferredID == rec.ID {
			isGroupPreferred = true
		}
		out = append(out, UpstreamAccountInfo{
			ID:                            rec.ID,
			ProviderType:                  rec.ProviderType,
			AccountName:                   rec.AccountName,
			CredentialRaw:                 "",
			CredentialRawMasked:           maskCredentialRaw(rec.ProviderType, rec.CredentialRaw),
			HasCredential:                 strings.TrimSpace(rec.CredentialRaw) != "",
			IsActiveSelection:             hasActiveSelection && rec.ID == activeSelectionAccountID,
			IsGroupPreferred:              isGroupPreferred,
			BaseURL:                       rec.BaseURL,
			ModelRewriteRules:             rec.ModelRewriteRules,
			SoftFailureWindowMinutes:      rec.SoftFailureWindowMinutes,
			SoftFailureThreshold:          rec.SoftFailureThreshold,
			EnableRequestCompression:      rec.EnableRequestCompression,
			StripCodexInternalMetadata:    rec.StripCodexInternalMetadata,
			CostMultiplier:                rec.CostMultiplier,
			InputCostMultiplier:           rec.InputCostMultiplier,
			OutputCostMultiplier:          rec.OutputCostMultiplier,
			CacheCreationCostMultiplier:   rec.CacheCreationCostMultiplier,
			CacheCreationCostMultiplier1h: rec.CacheCreationCostMultiplier1h,
			CacheReadCostMultiplier:       rec.CacheReadCostMultiplier,
			GroupKey:                      rec.GroupKey,
			Priority:                      rec.Priority,
			Enabled:                       rec.Enabled,
			State:                         rec.State,
			CooldownUntil:                 formatOptionalAccountRecordTime(rec.CooldownUntil),
			FailCount:                     rec.FailCount,
			LastSuccessAt:                 formatOptionalAccountRecordTime(rec.LastSuccessAt),
			LastError:                     rec.LastError,
			PlanType:                      rec.PlanType,
			ChatGPTAccountID:              rec.ChatGPTAccountID,
			ChatGPTUserID:                 rec.ChatGPTUserID,
			OrganizationID:                rec.OrganizationID,
			Quota5HUsedPercent:            rec.Quota5HUsedPercent,
			Quota5HResetAt:                formatOptionalAccountRecordTime(rec.Quota5HResetAt),
			QuotaWeeklyUsedPercent:        rec.QuotaWeeklyUsedPercent,
			QuotaWeeklyResetAt:            formatOptionalAccountRecordTime(rec.QuotaWeeklyResetAt),
			QuotaStatus:                   rec.QuotaStatus,
			QuotaRefreshedAt:              formatOptionalAccountRecordTime(rec.QuotaRefreshedAt),
			Fingerprint:                   rec.Fingerprint,
			CreatedAt:                     formatAccountRecordTime(rec.CreatedAt),
			UpdatedAt:                     formatAccountRecordTime(rec.UpdatedAt),
		})
	}
	return out, nil
}

// GetUpstreamAccountCredential 显式读取单个账号的完整凭据。
func (a *App) GetUpstreamAccountCredential(id int64) (UpstreamAccountCredentialInfo, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.accountPoolService == nil {
		return UpstreamAccountCredentialInfo{}, fmt.Errorf("账号池服务未启用")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rec, err := a.accountPoolService.GetAccount(ctx, id)
	if err != nil {
		return UpstreamAccountCredentialInfo{}, fmt.Errorf("读取账号凭据失败: %w", err)
	}
	if rec == nil {
		return UpstreamAccountCredentialInfo{}, fmt.Errorf("账号不存在: %d", id)
	}

	return UpstreamAccountCredentialInfo{
		ID:                  rec.ID,
		CredentialRaw:       rec.CredentialRaw,
		CredentialRawMasked: maskCredentialRaw(rec.ProviderType, rec.CredentialRaw),
		HasCredential:       strings.TrimSpace(rec.CredentialRaw) != "",
	}, nil
}

// GetLatestAccountScheduleSnapshot 获取最近一次账号池调度快照
func (a *App) GetLatestAccountScheduleSnapshot() (LatestAccountScheduleSnapshotInfo, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.accountPoolService == nil {
		return LatestAccountScheduleSnapshotInfo{}, fmt.Errorf("账号池服务未启用")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	snapshot, err := a.accountPoolService.GetLatestAccountScheduleSnapshot(ctx)
	if err != nil {
		return LatestAccountScheduleSnapshotInfo{}, fmt.Errorf("获取最近一次调度快照失败: %w", err)
	}
	if snapshot == nil {
		return LatestAccountScheduleSnapshotInfo{HasSnapshot: false}, nil
	}

	out := LatestAccountScheduleSnapshotInfo{
		HasSnapshot:             true,
		RequestID:               snapshot.RequestID,
		CapturedAt:              formatTime(snapshot.CapturedAt),
		UpdatedAt:               formatTime(snapshot.UpdatedAt),
		RequestPath:             snapshot.RequestPath,
		SelectedPriority:        snapshot.SelectedPriority,
		SelectedTierIndex:       snapshot.SelectedTierIndex,
		SelectedTierLabel:       snapshot.SelectedTierLabel,
		DegradedToLowerPriority: snapshot.DegradedToLowerPriority,
		SelectedAccountID:       snapshot.SelectedAccountID,
		SelectedAccountName:     snapshot.SelectedAccountName,
		FinalOutcome:            snapshot.FinalOutcome,
		FinalError:              snapshot.FinalError,
		Summary:                 snapshot.Summary,
		RequestModel:            snapshot.RequestModel,
		ModelRoutingFamily:      snapshot.ModelRoutingFamily,
		ModelRoutingVersion:     snapshot.ModelRoutingVersion,
		RouteScopeKind:          snapshot.RouteScopeKind,
		RouteScopeGroupID:       snapshot.RouteScopeGroupID,
		RouteScopeGroupName:     snapshot.RouteScopeGroupName,
		RouteMode:               snapshot.RouteMode,
		RouteTargetAccountID:    snapshot.RouteTargetAccountID,
		Candidates:              make([]AccountScheduleCandidateDecisionInfo, 0, len(snapshot.Candidates)),
	}
	for _, candidate := range snapshot.Candidates {
		out.Candidates = append(out.Candidates, AccountScheduleCandidateDecisionInfo{
			AccountID:               candidate.AccountID,
			AccountName:             candidate.AccountName,
			ProviderType:            candidate.ProviderType,
			Priority:                candidate.Priority,
			TierIndex:               candidate.TierIndex,
			TierLabel:               candidate.TierLabel,
			QuotaStatus:             candidate.QuotaStatus,
			EffectiveQuotaRemaining: candidate.EffectiveQuotaRemaining,
			FailCount:               candidate.FailCount,
			LastSuccessAt:           formatOptionalAccountRecordTime(candidate.LastSuccessAt),
			Decision:                candidate.Decision,
			Reason:                  candidate.Reason,
			ReasonDetail:            candidate.ReasonDetail,
			RuntimeOutcome:          candidate.RuntimeOutcome,
			RuntimeError:            candidate.RuntimeError,
		})
	}
	return out, nil
}

// MoveUpstreamAccountToTier 将账号切到主组/备组。
func (a *App) MoveUpstreamAccountToTier(id int64, targetTier string) (MoveUpstreamAccountToTierResult, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.accountPoolService == nil {
		return MoveUpstreamAccountToTierResult{}, fmt.Errorf("账号池服务未启用")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	targetTierIndex := 0
	switch strings.TrimSpace(strings.ToLower(targetTier)) {
	case "backup":
		targetTierIndex = 1
	case "cold":
		targetTierIndex = 2
	case "", "primary":
		targetTierIndex = 0
	default:
		return MoveUpstreamAccountToTierResult{}, fmt.Errorf("不支持的目标层级: %s", targetTier)
	}

	changed, err := a.accountPoolService.MoveAccountToTier(ctx, id, targetTierIndex)
	if err != nil {
		return MoveUpstreamAccountToTierResult{}, fmt.Errorf("切换账号层级失败: %w", err)
	}

	message := "账号层级未发生变化"
	if changed {
		if targetTierIndex == 0 {
			message = "账号已设为主组目标账号，系统将按当前可调度状态优先使用；若暂时不可调度，恢复后会自动回切"
		} else if targetTierIndex == 2 {
			message = "账号已设为冷备目标账号，系统会在主组和备组都不可用时再使用该层"
		} else {
			message = "账号已设为备组目标账号，系统将按当前可调度状态优先使用；若暂时不可调度，恢复后会自动回切"
		}
	}
	return MoveUpstreamAccountToTierResult{
		Success: true,
		Changed: changed,
		Message: message,
	}, nil
}

func accountGroupLabelForDisplay(groupKey string) string {
	switch strings.TrimSpace(strings.ToLower(groupKey)) {
	case "primary":
		return "主组"
	case "backup":
		return "备组"
	case "cold":
		return "冷备"
	default:
		return "未知组"
	}
}

// SwapUpstreamAccountGroups 调度编排中的整组交换。
func (a *App) SwapUpstreamAccountGroups(sourceGroup, targetGroup string) (SwapUpstreamAccountGroupsResult, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.accountPoolService == nil {
		return SwapUpstreamAccountGroupsResult{}, fmt.Errorf("账号池服务未启用")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	changed, err := a.accountPoolService.SwapAccountGroups(ctx, sourceGroup, targetGroup)
	if err != nil {
		return SwapUpstreamAccountGroupsResult{}, fmt.Errorf("整组交换失败: %w", err)
	}

	message := "分组未发生变化"
	if changed {
		message = fmt.Sprintf("已完成%s与%s的整组交换", accountGroupLabelForDisplay(sourceGroup), accountGroupLabelForDisplay(targetGroup))
	}

	return SwapUpstreamAccountGroupsResult{
		Success: true,
		Changed: changed,
		Message: message,
	}, nil
}

// SetGroupActiveAccount 设置某个组的首选活跃账号，仅在该组被命中时优先生效。
func (a *App) SetGroupActiveAccount(groupKey string, id int64) (SetGroupActiveAccountResult, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.accountPoolService == nil {
		return SetGroupActiveAccountResult{}, fmt.Errorf("账号池服务未启用")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	changed, err := a.accountPoolService.SetGroupActiveAccount(ctx, groupKey, id)
	if err != nil {
		return SetGroupActiveAccountResult{}, fmt.Errorf("设置组内首选账号失败: %w", err)
	}

	message := "当前组首选账号未发生变化"
	if changed {
		message = fmt.Sprintf("已将%s的首选账号设置为指定账号，仅在该组被命中时优先生效", accountGroupLabelForDisplay(groupKey))
	}

	return SetGroupActiveAccountResult{
		Success: true,
		Changed: changed,
		Message: message,
	}, nil
}

// PinUpstreamAccountSelection 固定具体账号，直到其严格不可用。
func (a *App) PinUpstreamAccountSelection(id int64) (PinUpstreamAccountSelectionResult, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.accountPoolService == nil {
		return PinUpstreamAccountSelectionResult{}, fmt.Errorf("账号池服务未启用")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	changed, err := a.accountPoolService.PinAccountSelection(ctx, id)
	if err != nil {
		return PinUpstreamAccountSelectionResult{}, fmt.Errorf("固定账号失败: %w", err)
	}

	message := "账号固定目标未发生变化"
	if changed {
		message = "账号已固定为当前请求目标；仅在该账号严格不可用时才会自动切走"
	}

	return PinUpstreamAccountSelectionResult{
		Success: true,
		Changed: changed,
		Message: message,
	}, nil
}

// EnableAutomaticAccountSelection 清除全局固定账号，恢复按编排自动调度。
func (a *App) EnableAutomaticAccountSelection() (EnableAutomaticAccountSelectionResult, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.accountPoolService == nil {
		return EnableAutomaticAccountSelectionResult{}, fmt.Errorf("账号池服务未启用")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	changed, err := a.accountPoolService.EnableAutomaticAccountSelection(ctx)
	if err != nil {
		return EnableAutomaticAccountSelectionResult{}, fmt.Errorf("启用编排失败: %w", err)
	}

	message := "当前已处于按编排自动调度"
	if changed {
		message = "已启用编排自动调度，后续请求将按当前编排规则选择账号"
	}

	return EnableAutomaticAccountSelectionResult{
		Success: true,
		Changed: changed,
		Message: message,
	}, nil
}

// RefreshUpstreamAccountProfile 刷新账号画像与 quota 信息
func (a *App) RefreshUpstreamAccountProfile(id int64) (RefreshUpstreamAccountProfileResult, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.accountPoolService == nil {
		return RefreshUpstreamAccountProfileResult{}, fmt.Errorf("账号池服务未启用")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	result, err := a.accountPoolService.RefreshAccountProfile(ctx, id)
	if err != nil {
		return RefreshUpstreamAccountProfileResult{}, err
	}
	return RefreshUpstreamAccountProfileResult{
		Success:     result.Success,
		Message:     result.Message,
		QuotaStatus: result.QuotaStatus,
	}, nil
}

// CreateUpstreamAccount 创建账号
func (a *App) CreateUpstreamAccount(input CreateUpstreamAccountInput) error {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.accountPoolService == nil {
		return fmt.Errorf("账号池服务未启用")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := a.accountPoolService.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:                  strings.TrimSpace(input.ProviderType),
		AccountName:                   strings.TrimSpace(input.AccountName),
		CredentialRaw:                 strings.TrimSpace(input.CredentialRaw),
		BaseURL:                       strings.TrimSpace(input.BaseURL),
		ModelRewriteRules:             strings.TrimSpace(input.ModelRewriteRules),
		SoftFailureWindowMinutes:      input.SoftFailureWindowMinutes,
		SoftFailureThreshold:          input.SoftFailureThreshold,
		EnableRequestCompression:      input.EnableRequestCompression && store.SupportsAccountRequestCompression(input.ProviderType),
		StripCodexInternalMetadata:    input.StripCodexInternalMetadata != nil && *input.StripCodexInternalMetadata,
		CostMultiplier:                input.CostMultiplier,
		InputCostMultiplier:           input.InputCostMultiplier,
		OutputCostMultiplier:          input.OutputCostMultiplier,
		CacheCreationCostMultiplier:   input.CacheCreationCostMultiplier,
		CacheCreationCostMultiplier1h: input.CacheCreationCostMultiplier1h,
		CacheReadCostMultiplier:       input.CacheReadCostMultiplier,
		GroupKey:                      strings.TrimSpace(input.GroupKey),
		Priority:                      input.Priority,
		Enabled:                       input.Enabled,
		State:                         "active",
	})
	if err != nil {
		return fmt.Errorf("创建账号失败: %w", err)
	}
	a.syncAccountMultipliersToTrackerAsync()
	return nil
}

// UpdateUpstreamAccount 更新账号
func (a *App) UpdateUpstreamAccount(id int64, input CreateUpstreamAccountInput) error {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.accountPoolService == nil {
		return fmt.Errorf("账号池服务未启用")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	existing, err := a.accountPoolService.GetAccount(ctx, id)
	if err != nil {
		return fmt.Errorf("读取账号失败: %w", err)
	}
	if existing == nil {
		return fmt.Errorf("账号不存在: %d", id)
	}

	existing.ProviderType = strings.TrimSpace(input.ProviderType)
	existing.AccountName = strings.TrimSpace(input.AccountName)
	existing.CredentialRaw = strings.TrimSpace(input.CredentialRaw)
	existing.BaseURL = strings.TrimSpace(input.BaseURL)
	existing.ModelRewriteRules = strings.TrimSpace(input.ModelRewriteRules)
	existing.SoftFailureWindowMinutes = input.SoftFailureWindowMinutes
	existing.SoftFailureThreshold = input.SoftFailureThreshold
	existing.EnableRequestCompression = input.EnableRequestCompression && store.SupportsAccountRequestCompression(input.ProviderType)
	if input.StripCodexInternalMetadata != nil {
		existing.StripCodexInternalMetadata = *input.StripCodexInternalMetadata
	}
	existing.CostMultiplier = input.CostMultiplier
	existing.InputCostMultiplier = input.InputCostMultiplier
	existing.OutputCostMultiplier = input.OutputCostMultiplier
	existing.CacheCreationCostMultiplier = input.CacheCreationCostMultiplier
	existing.CacheCreationCostMultiplier1h = input.CacheCreationCostMultiplier1h
	existing.CacheReadCostMultiplier = input.CacheReadCostMultiplier
	existing.GroupKey = strings.TrimSpace(input.GroupKey)
	existing.Priority = input.Priority
	existing.Enabled = input.Enabled
	if existing.Enabled && existing.State == "disabled_auth" {
		existing.State = "active"
	}
	if err := a.accountPoolService.UpdateAccount(ctx, existing); err != nil {
		return fmt.Errorf("更新账号失败: %w", err)
	}
	if a.codexModelRoutingService != nil {
		a.codexModelRoutingService.ClearNegativeHits(id)
	}
	a.syncAccountMultipliersToTrackerAsync()
	return nil
}

// DeleteUpstreamAccount 删除账号
func (a *App) DeleteUpstreamAccount(id int64) error {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.accountPoolService == nil {
		return fmt.Errorf("账号池服务未启用")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a.routingMu.Lock()
	defer a.routingMu.Unlock()
	var modelsUsing func(context.Context, int64) ([]string, error)
	if a.codexModelRoutingService != nil {
		modelsUsing = a.codexModelRoutingService.ModelsUsingAccount
	}
	if err := a.accountPoolService.DeleteAccountWithModelGuard(ctx, id, modelsUsing); err != nil {
		return err
	}
	if a.codexModelRoutingService != nil {
		a.codexModelRoutingService.ClearNegativeHits(id)
	}
	a.syncAccountMultipliersToTrackerAsync()
	return nil
}

// ToggleUpstreamAccount 启停账号
func (a *App) ToggleUpstreamAccount(id int64, enabled bool) error {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.accountPoolService == nil {
		return fmt.Errorf("账号池服务未启用")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return a.accountPoolService.ToggleAccount(ctx, id, enabled)
}

// ClearUpstreamAccountCooldown 手动解除账号临时故障冷却。
func (a *App) ClearUpstreamAccountCooldown(id int64) error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.accountPoolService == nil {
		return fmt.Errorf("账号池服务未启用")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return a.accountPoolService.ClearAccountCooldown(ctx, id)
}

// TestUpstreamAccount 测试账号连通性
func (a *App) TestUpstreamAccount(id int64) (TestUpstreamAccountResult, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.accountPoolService == nil {
		return TestUpstreamAccountResult{}, fmt.Errorf("账号池服务未启用")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	err := a.accountPoolService.TestUpstreamAccount(ctx, id)
	if err != nil {
		return TestUpstreamAccountResult{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return TestUpstreamAccountResult{
		Success: true,
		Message: "连通性测试通过",
	}, nil
}

func formatOptionalTime(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return formatTime(*t)
}

func formatOptionalAccountRecordTime(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return formatAccountRecordTime(*t)
}

func formatAccountRecordTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return formatAPITime(t)
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return formatAPITime(t)
}

func maskCredentialRaw(providerType, raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}

	if strings.HasPrefix(trimmed, "{") {
		var payload map[string]any
		if err := json.Unmarshal([]byte(trimmed), &payload); err == nil {
			for _, key := range []string{"refresh_token", "access_token", "id_token", "token"} {
				if value, ok := payload[key].(string); ok {
					payload[key] = maskSecret(value)
				}
			}
			masked, err := json.Marshal(payload)
			if err == nil {
				return string(masked)
			}
		}
	}

	if strings.EqualFold(strings.TrimSpace(providerType), "api_key") {
		return maskSecret(trimmed)
	}
	return maskSecret(trimmed)
}

func maskSecret(secret string) string {
	trimmed := strings.TrimSpace(secret)
	if trimmed == "" {
		return ""
	}

	length := len(trimmed)
	switch {
	case length <= 6:
		return strings.Repeat("*", length)
	case length <= 12:
		return trimmed[:2] + strings.Repeat("*", length-4) + trimmed[length-2:]
	case length <= 20:
		return trimmed[:4] + strings.Repeat("*", length-8) + trimmed[length-4:]
	default:
		return trimmed[:6] + strings.Repeat("*", length-10) + trimmed[length-4:]
	}
}
