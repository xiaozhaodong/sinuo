// app_api_storage.go - v5.0+ 端点存储管理 API (Wails Bindings)
// 提供 SQLite 端点存储的增删改查功能

package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"cc-forwarder/config"
	"cc-forwarder/internal/endpoint"
	"cc-forwarder/internal/store"
	"cc-forwarder/internal/utils"
)

// ============================================================
// v5.0+ 端点存储管理 API (SQLite)
// ============================================================
// 这些 API 仅在 endpoints_storage.type 为 "sqlite" 时可用
// 提供端点的增删改查功能

// EndpointRecordInfo 端点记录信息（给前端用的结构体）
type EndpointRecordInfo struct {
	ID                            int64             `json:"id"`
	Name                          string            `json:"name"`
	URL                           string            `json:"url"`
	TokenMasked                   string            `json:"token_masked"` // 脱敏后的 Token（列表展示用）
	ApiKeyMasked                  string            `json:"api_key_masked"`
	AuthMode                      string            `json:"auth_mode"` // configured / client_oauth
	Headers                       map[string]string `json:"headers"`
	Priority                      int               `json:"priority"`
	FailoverEnabled               bool              `json:"failover_enabled"`
	CooldownSeconds               *int              `json:"cooldown_seconds"`
	TimeoutSeconds                int               `json:"timeout_seconds"`
	SupportsCountTokens           bool              `json:"supports_count_tokens"`
	ModelRewriteRules             string            `json:"model_rewrite_rules"`
	SoftFailureWindowMinutes      *int              `json:"soft_failure_window_minutes"`
	SoftFailureThreshold          *int              `json:"soft_failure_threshold"`
	CostMultiplier                float64           `json:"cost_multiplier"`
	InputCostMultiplier           float64           `json:"input_cost_multiplier"`
	OutputCostMultiplier          float64           `json:"output_cost_multiplier"`
	CacheCreationCostMultiplier   float64           `json:"cache_creation_cost_multiplier"`
	CacheCreationCostMultiplier1h float64           `json:"cache_creation_cost_multiplier_1h"`
	CacheReadCostMultiplier       float64           `json:"cache_read_cost_multiplier"`
	AvailabilityEnabled           bool              `json:"availability_enabled"` // v8 硬启用状态
	CreatedAt                     string            `json:"created_at"`
	UpdatedAt                     string            `json:"updated_at"`
	// 运行时健康状态
	Healthy        bool    `json:"healthy"`
	NeverChecked   bool    `json:"never_checked"`
	LastCheck      string  `json:"last_check"` // 最近连通性测试时间
	ResponseTimeMs float64 `json:"response_time_ms"`
	// 冷却状态（请求级故障转移）
	InCooldown     bool   `json:"in_cooldown"`     // 是否处于冷却中
	CooldownUntil  string `json:"cooldown_until"`  // 冷却截止时间
	CooldownReason string `json:"cooldown_reason"` // 冷却原因
	// 手动解除冷却的持久化未完成标记（落库失败后置位，前端保留重试入口）
	CooldownPersistPending bool `json:"cooldown_persist_pending"`
}

// CreateEndpointInput 创建端点的输入参数
type CreateEndpointInput struct {
	Name                          string            `json:"name"`
	URL                           string            `json:"url"`
	Token                         string            `json:"token"`
	ApiKey                        string            `json:"api_key"`
	ClearToken                    bool              `json:"clear_token"`
	ClearApiKey                   bool              `json:"clear_api_key"`
	AuthMode                      string            `json:"auth_mode"` // 空值按 configured 处理
	Headers                       map[string]string `json:"headers"`
	Priority                      int               `json:"priority"`
	FailoverEnabled               bool              `json:"failover_enabled"`
	AvailabilityEnabled           *bool             `json:"availability_enabled,omitempty"`
	CooldownSeconds               *int              `json:"cooldown_seconds"`
	TimeoutSeconds                int               `json:"timeout_seconds"`
	SupportsCountTokens           bool              `json:"supports_count_tokens"`
	ModelRewriteRules             string            `json:"model_rewrite_rules"`
	SoftFailureWindowMinutes      *int              `json:"soft_failure_window_minutes"`
	SoftFailureThreshold          *int              `json:"soft_failure_threshold"`
	CostMultiplier                float64           `json:"cost_multiplier"`
	InputCostMultiplier           float64           `json:"input_cost_multiplier"`
	OutputCostMultiplier          float64           `json:"output_cost_multiplier"`
	CacheCreationCostMultiplier   float64           `json:"cache_creation_cost_multiplier"`
	CacheCreationCostMultiplier1h float64           `json:"cache_creation_cost_multiplier_1h"`
	CacheReadCostMultiplier       float64           `json:"cache_read_cost_multiplier"`
}

// EndpointStorageStatus 端点存储状态
type EndpointStorageStatus struct {
	Enabled        bool   `json:"enabled"`         // 是否启用 SQLite 存储
	StorageType    string `json:"storage_type"`    // "yaml" 或 "sqlite"
	TotalCount     int    `json:"total_count"`     // 端点总数
	AvailableCount int    `json:"available_count"` // 硬启用端点数
}

// GetEndpointStorageStatus 获取端点存储状态
func (a *App) GetEndpointStorageStatus() EndpointStorageStatus {
	a.mu.RLock()
	defer a.mu.RUnlock()

	status := EndpointStorageStatus{
		StorageType: "sqlite",
	}

	if a.config != nil {
		status.StorageType = a.config.EndpointsStorage.Type
	}

	// 如果使用 SQLite 存储且服务已初始化
	if a.endpointService != nil {
		status.Enabled = true

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		records, err := a.endpointService.ListEndpoints(ctx)
		if err == nil {
			status.TotalCount = len(records)
			for _, r := range records {
				if r.IsAvailabilityEnabled() {
					status.AvailableCount++
				}
			}
		}
	}

	return status
}

// GetEndpointRecords 获取所有端点记录（SQLite 存储）
func (a *App) GetEndpointRecords() ([]EndpointRecordInfo, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.endpointService == nil {
		return nil, fmt.Errorf("端点存储服务未启用 (需要设置 endpoints_storage.type: sqlite)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	records, err := a.endpointService.ListEndpoints(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取端点列表失败: %w", err)
	}

	result := make([]EndpointRecordInfo, 0, len(records))
	for _, r := range records {
		info := a.recordToInfo(r)

		// 获取运行时健康状态
		if a.endpointManager != nil {
			status := a.endpointManager.GetEndpointStatus(r.Name)
			info.Healthy = status.Healthy
			info.NeverChecked = status.NeverChecked
			info.ResponseTimeMs = float64(status.ResponseTime.Milliseconds())
			// 格式化最后检查时间
			if !status.LastCheck.IsZero() {
				info.LastCheck = formatAPITime(status.LastCheck)
			}
			// 冷却状态（两个 scope 槽取生效且截止最晚者）
			if until, reason, active := status.EffectiveCooldown(time.Now()); active {
				info.InCooldown = true
				info.CooldownUntil = formatAPITime(until)
				info.CooldownReason = reason
			}
		}
		// 手动解除冷却的持久化 pending 态（endpoint ID 不匹配即 false）
		info.CooldownPersistPending = a.isCooldownPersistPending(r.Name, r.ID)

		result = append(result, info)
	}

	return result, nil
}

// GetEndpointRecord 获取单个端点详情
func (a *App) GetEndpointRecord(name string) (EndpointRecordInfo, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.endpointService == nil {
		return EndpointRecordInfo{}, fmt.Errorf("端点存储服务未启用")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	record, err := a.endpointService.GetEndpoint(ctx, name)
	if err != nil {
		return EndpointRecordInfo{}, err
	}
	if record == nil {
		return EndpointRecordInfo{}, fmt.Errorf("端点 '%s' 不存在", name)
	}
	info := a.recordToInfo(record)
	if a.endpointManager != nil {
		status := a.endpointManager.GetEndpointStatus(name)
		info.Healthy = status.Healthy
		info.NeverChecked = status.NeverChecked
		info.ResponseTimeMs = float64(status.ResponseTime.Milliseconds())
		if !status.LastCheck.IsZero() {
			info.LastCheck = formatAPITime(status.LastCheck)
		}
	}

	return info, nil
}

// CreateEndpointRecord 创建新端点
func (a *App) CreateEndpointRecord(input CreateEndpointInput) error {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.endpointService == nil {
		return fmt.Errorf("端点存储服务未启用")
	}

	// 设置默认值
	if input.Priority == 0 {
		input.Priority = 1
	}
	if input.TimeoutSeconds == 0 {
		input.TimeoutSeconds = 300
	}
	if input.CostMultiplier == 0 {
		input.CostMultiplier = 1.0
	}
	defaultEndpointMultipliers(&input)
	availabilityEnabled := true
	if input.AvailabilityEnabled != nil {
		availabilityEnabled = *input.AvailabilityEnabled
	}

	record := &store.EndpointRecord{
		Name:                          input.Name,
		URL:                           input.URL,
		Token:                         input.Token,
		ApiKey:                        input.ApiKey,
		AuthMode:                      input.AuthMode,
		Headers:                       input.Headers,
		Priority:                      input.Priority,
		FailoverEnabled:               input.FailoverEnabled,
		CooldownSeconds:               input.CooldownSeconds,
		TimeoutSeconds:                input.TimeoutSeconds,
		SupportsCountTokens:           input.SupportsCountTokens,
		ModelRewriteRules:             strings.TrimSpace(input.ModelRewriteRules),
		SoftFailureWindowMinutes:      input.SoftFailureWindowMinutes,
		SoftFailureThreshold:          input.SoftFailureThreshold,
		CostMultiplier:                input.CostMultiplier,
		InputCostMultiplier:           input.InputCostMultiplier,
		OutputCostMultiplier:          input.OutputCostMultiplier,
		CacheCreationCostMultiplier:   input.CacheCreationCostMultiplier,
		CacheCreationCostMultiplier1h: input.CacheCreationCostMultiplier1h,
		CacheReadCostMultiplier:       input.CacheReadCostMultiplier,
		AvailabilityEnabled:           &availabilityEnabled,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := a.endpointService.CreateEndpoint(ctx, record)
	if err != nil {
		return fmt.Errorf("创建端点失败: %w", err)
	}

	if a.logger != nil {
		a.logger.Info("✅ 端点已创建", "name", input.Name)
	}

	// v5.0: endpointService.CreateEndpoint 已经将端点添加到内存并触发健康检测
	// 不需要再次调用 AddEndpoint，否则会导致 "端点已存在" 错误

	// v5.0: 创建成功后，异步同步端点倍率到 UsageTracker
	go a.syncEndpointMultipliersToTracker(context.Background())

	return nil
}

// UpdateEndpointRecord 更新端点
func (a *App) UpdateEndpointRecord(name string, input CreateEndpointInput) error {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.endpointService == nil {
		return fmt.Errorf("端点存储服务未启用")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// v5.0: 从数据库获取当前记录，用于保留敏感字段
	existingRecord, err := a.endpointService.GetEndpoint(ctx, name)
	if err != nil {
		return fmt.Errorf("获取端点失败: %w", err)
	}
	if existingRecord == nil {
		return fmt.Errorf("端点 '%s' 不存在", name)
	}

	token, err := resolveSecretEdit(existingRecord.Token, input.Token, input.ClearToken, "Token")
	if err != nil {
		return err
	}
	apiKey, err := resolveSecretEdit(existingRecord.ApiKey, input.ApiKey, input.ClearApiKey, "API Key")
	if err != nil {
		return err
	}
	token, apiKey = dropInheritedSecretsForClientOAuth(input, token, apiKey)
	defaultEndpointMultipliers(&input)

	record := &store.EndpointRecord{
		Name:                          name, // 使用 URL 参数中的 name
		URL:                           input.URL,
		Token:                         token,  // 空值时保留原有值
		ApiKey:                        apiKey, // 空值时保留原有值
		AuthMode:                      input.AuthMode,
		Headers:                       input.Headers,
		Priority:                      input.Priority,
		FailoverEnabled:               input.FailoverEnabled,
		CooldownSeconds:               input.CooldownSeconds,
		TimeoutSeconds:                input.TimeoutSeconds,
		SupportsCountTokens:           input.SupportsCountTokens,
		ModelRewriteRules:             strings.TrimSpace(input.ModelRewriteRules),
		SoftFailureWindowMinutes:      input.SoftFailureWindowMinutes,
		SoftFailureThreshold:          input.SoftFailureThreshold,
		CostMultiplier:                input.CostMultiplier,
		InputCostMultiplier:           input.InputCostMultiplier,
		OutputCostMultiplier:          input.OutputCostMultiplier,
		CacheCreationCostMultiplier:   input.CacheCreationCostMultiplier,
		CacheCreationCostMultiplier1h: input.CacheCreationCostMultiplier1h,
		CacheReadCostMultiplier:       input.CacheReadCostMultiplier,
		AvailabilityEnabled:           existingRecord.AvailabilityEnabled, // v8: 硬启用状态只能通过 SetEndpointAvailability 修改，编辑保留原值
	}

	availabilityChanged := input.AvailabilityEnabled != nil && *input.AvailabilityEnabled != existingRecord.IsAvailabilityEnabled()
	if availabilityChanged && !*input.AvailabilityEnabled {
		// 停用必须先关闭 admission，再发布其余配置，避免编辑期间出现短暂可调度窗口。
		if err := a.endpointService.SetEndpointAvailability(ctx, name, false); err != nil {
			return fmt.Errorf("更新端点硬启用状态失败: %w", err)
		}
		record.AvailabilityEnabled = input.AvailabilityEnabled
	}
	if err := a.endpointService.UpdateEndpoint(ctx, record); err != nil {
		return fmt.Errorf("更新端点失败: %w", err)
	}
	if availabilityChanged && *input.AvailabilityEnabled {
		// 启用最后发布，确保新配置完整可见后才重新开放 admission。
		if err := a.endpointService.SetEndpointAvailability(ctx, name, true); err != nil {
			return fmt.Errorf("更新端点硬启用状态失败: %w", err)
		}
	}

	if a.logger != nil {
		a.logger.Info("✅ 端点已更新", "name", name)
	}

	// v5.0: 更新成功后，异步同步端点倍率到 UsageTracker
	// 确保成本计算使用最新的倍率配置
	go a.syncEndpointMultipliersToTracker(context.Background())

	return nil
}

// DeleteEndpointRecord 删除端点
func (a *App) DeleteEndpointRecord(name string) error {
	// 只在快照期间持 a.mu 读锁，避免与内部再取 a.mu 的调用重入自锁
	a.mu.RLock()
	endpointService := a.endpointService
	manager := a.endpointManager
	a.mu.RUnlock()

	if endpointService == nil {
		return fmt.Errorf("端点存储服务未启用")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// v8 §11.2 规则 6：与 SetClaudeRoutingOverride 共用 routingMu 串行化，
	// 消除「检查存在 → 删除 → 写入 fixed」竞态。序列：
	// 1) override 指向该端点时先持久化清回 Auto（失败即中止删除，无任何丢失）；
	// 2) 删除端点；删除失败则补偿性恢复原 override（恢复失败时状态停留在 Auto，
	//    安全且无悬空目标）。manual fixed 的删除确认由前端在调用本 API 前完成。
	a.routingMu.Lock()
	defer a.routingMu.Unlock()
	if a.modelRoutingService != nil {
		models, refErr := a.modelRoutingService.ModelsUsingEndpoint(ctx, name)
		if refErr != nil {
			return fmt.Errorf("检查模型路由端点引用失败: %w", refErr)
		}
		if len(models) > 0 {
			return fmt.Errorf("端点 '%s' 仍被模型 %s 使用，请先在模型路由中取消选择", name, strings.Join(models, "、"))
		}
	}

	var clearedOverride *endpoint.RouteOverrideState
	if manager != nil {
		override := manager.GetClaudeRoutingOverride()
		if override.EndpointName == name && override.Mode != "auto" {
			if _, err := a.clearClaudeRoutingOverrideLocked(manager); err != nil {
				return fmt.Errorf("清除手动路由目标失败，已中止删除: %w", err)
			}
			clearedOverride = &override
			if a.logger != nil {
				a.logger.Info("🔀 已清除手动路由目标端点，恢复自动路由", "name", name, "mode", override.Mode)
			}
		}
	}

	if err := endpointService.DeleteEndpoint(ctx, name); err != nil {
		if clearedOverride != nil && manager != nil {
			if restoreErr := a.restoreClaudeRoutingOverrideLocked(manager, *clearedOverride); restoreErr != nil {
				if a.logger != nil {
					a.logger.Warn("⚠️ 删除失败后恢复手动路由失败，当前保持自动路由",
						"name", name, "error", restoreErr)
				}
			}
		}
		return fmt.Errorf("删除端点失败: %w", err)
	}

	if a.logger != nil {
		a.logger.Info("✅ 端点已删除", "name", name)
	}

	// 删除成功：按名清理孤儿 pending 标记（同名重建端点不继承旧标记）
	a.removeCooldownPersistPending(name)

	// v5.0: 删除成功后，异步同步端点倍率到 UsageTracker
	go a.syncEndpointMultipliersToTracker(context.Background())

	return nil
}

// recordToInfo 将数据库记录转换为前端 Info 结构
func (a *App) recordToInfo(r *store.EndpointRecord) EndpointRecordInfo {
	info := EndpointRecordInfo{
		ID:                            r.ID,
		Name:                          r.Name,
		URL:                           r.URL,
		TokenMasked:                   maskToken(r.Token),
		ApiKeyMasked:                  maskToken(r.ApiKey),
		AuthMode:                      config.NormalizeEndpointAuthMode(r.AuthMode),
		Headers:                       r.Headers,
		Priority:                      r.Priority,
		FailoverEnabled:               r.FailoverEnabled,
		CooldownSeconds:               r.CooldownSeconds,
		TimeoutSeconds:                r.TimeoutSeconds,
		SupportsCountTokens:           r.SupportsCountTokens,
		ModelRewriteRules:             r.ModelRewriteRules,
		SoftFailureWindowMinutes:      r.SoftFailureWindowMinutes,
		SoftFailureThreshold:          r.SoftFailureThreshold,
		CostMultiplier:                r.CostMultiplier,
		InputCostMultiplier:           r.InputCostMultiplier,
		OutputCostMultiplier:          r.OutputCostMultiplier,
		CacheCreationCostMultiplier:   r.CacheCreationCostMultiplier,
		CacheCreationCostMultiplier1h: r.CacheCreationCostMultiplier1h,
		CacheReadCostMultiplier:       r.CacheReadCostMultiplier,
		AvailabilityEnabled:           r.IsAvailabilityEnabled(),
	}

	if !r.CreatedAt.IsZero() {
		info.CreatedAt = formatAPITime(r.CreatedAt)
	}
	if !r.UpdatedAt.IsZero() {
		info.UpdatedAt = formatAPITime(r.UpdatedAt)
	}
	info.CooldownPersistPending = a.isCooldownPersistPending(r.Name, r.ID)

	return info
}

func resolveSecretEdit(current, replacement string, clear bool, field string) (string, error) {
	replacement = strings.TrimSpace(replacement)
	if clear && replacement != "" {
		return "", fmt.Errorf("%s 不能同时设置新值和清除标记", field)
	}
	if clear {
		return "", nil
	}
	if replacement == "" {
		return current, nil
	}
	return replacement, nil
}

// dropInheritedSecretsForClientOAuth 切换为订阅直通时，编辑表单不再提交凭据，
// 「空值保留原值」会把旧 Token / API Key 带进来导致校验失败；此时丢弃沿用的旧值。
// 用户显式填了新凭据则原样保留，交由校验拒绝。
func dropInheritedSecretsForClientOAuth(input CreateEndpointInput, token, apiKey string) (string, string) {
	if config.NormalizeEndpointAuthMode(input.AuthMode) != config.EndpointAuthModeClientOAuth {
		return token, apiKey
	}
	if strings.TrimSpace(input.Token) == "" {
		token = ""
	}
	if strings.TrimSpace(input.ApiKey) == "" {
		apiKey = ""
	}
	return token, apiKey
}

func defaultEndpointMultipliers(input *CreateEndpointInput) {
	if input == nil {
		return
	}
	values := []*float64{
		&input.CostMultiplier,
		&input.InputCostMultiplier,
		&input.OutputCostMultiplier,
		&input.CacheCreationCostMultiplier,
		&input.CacheCreationCostMultiplier1h,
		&input.CacheReadCostMultiplier,
	}
	for _, value := range values {
		if *value == 0 {
			*value = 1
		}
	}
}

// maskToken Token 脱敏显示
func maskToken(token string) string {
	return utils.MaskToken(token)
}
