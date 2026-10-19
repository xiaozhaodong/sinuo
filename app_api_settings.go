// app_api_settings.go - v5.1+ 系统设置管理 API (Wails Bindings)
// 提供 SQLite 设置存储的增删改查功能
// 创建时间: 2025-12-08

package main

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"sinuo/internal/service"
	"sinuo/internal/store"
	"sinuo/internal/utils"
)

// ============================================================
// v5.1+ 系统设置管理 API (SQLite)
// ============================================================

// SettingInfo 设置信息（给前端用的结构体）
type SettingInfo struct {
	ID               int64  `json:"id"`
	Category         string `json:"category"`
	Key              string `json:"key"`
	Value            string `json:"value"`
	ValueType        string `json:"value_type"`
	Label            string `json:"label"`
	Description      string `json:"description"`
	DisplayOrder     int    `json:"display_order"`
	RequiresRestart  bool   `json:"requires_restart"`
	SecretConfigured bool   `json:"secret_configured"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}

// CategoryInfo 分类信息
type CategoryInfo struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Icon        string `json:"icon"`
	Order       int    `json:"order"`
}

// SettingsStorageStatus 设置存储状态
type SettingsStorageStatus struct {
	Enabled       bool `json:"enabled"`
	TotalCount    int  `json:"total_count"`
	CategoryCount int  `json:"category_count"`
	IsInitialized bool `json:"is_initialized"`
}

// UpdateSettingInput 更新单个设置的输入
type UpdateSettingInput struct {
	Category string `json:"category"`
	Key      string `json:"key"`
	Value    string `json:"value"`
}

// BatchUpdateSettingsInput 批量更新设置的输入
type BatchUpdateSettingsInput struct {
	Settings []UpdateSettingInput `json:"settings"`
}

// PortInfo 端口信息
type PortInfo struct {
	PreferredPort      int      `json:"preferred_port"`
	ActualPort         int      `json:"actual_port"`
	IsDefault          bool     `json:"is_default"`
	WasOccupied        bool     `json:"was_occupied"`
	LANAccess          bool     `json:"lan_access"`
	LANAccessActive    bool     `json:"lan_access_active"`
	LANTokenConfigured bool     `json:"lan_token_configured"`
	LANAddresses       []string `json:"lan_addresses"`
}

// GetSettingsStorageStatus 获取设置存储状态
func (a *App) GetSettingsStorageStatus() SettingsStorageStatus {
	a.mu.RLock()
	defer a.mu.RUnlock()

	status := SettingsStorageStatus{
		Enabled: false,
	}

	if a.settingsService == nil {
		return status
	}

	status.Enabled = true

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 获取统计信息
	initialized, _ := a.settingsService.IsInitialized(ctx)
	status.IsInitialized = initialized

	records, err := a.settingsService.GetAll(ctx)
	if err == nil {
		status.TotalCount = len(records)
	}

	categories := a.settingsService.GetCategories()
	status.CategoryCount = len(categories)

	return status
}

// GetSettingCategories 获取所有设置分类
func (a *App) GetSettingCategories() []CategoryInfo {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.settingsService == nil {
		return []CategoryInfo{}
	}

	serviceCategories := a.settingsService.GetCategories()
	result := make([]CategoryInfo, 0, len(serviceCategories))

	for _, cat := range serviceCategories {
		result = append(result, CategoryInfo{
			Name:        cat.Name,
			Label:       cat.Label,
			Description: cat.Description,
			Icon:        cat.Icon,
			Order:       cat.Order,
		})
	}

	return result
}

// GetAllSettings 获取所有设置
func (a *App) GetAllSettings() ([]SettingInfo, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.settingsService == nil {
		return nil, fmt.Errorf("设置服务未启用 (需要设置 usage_tracking.enabled: true)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	records, err := a.settingsService.GetAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取设置列表失败: %w", err)
	}

	result := make([]SettingInfo, 0, len(records))
	for _, r := range records {
		// 受保护分类（Codex 订阅代理）由专用设置页独占：通用列表不返回，
		// 既不让它混进通用表单，也不让单行 JSON 配置走普通读取路径。
		// 桌面内部标记与当前平台不生效的桌面设置同样不返回。
		if service.IsProtectedSettingsCategory(r.Category) || !service.SettingVisible(r.Category, r.Key, runtime.GOOS) {
			continue
		}
		if !a.settingsService.IsRegisteredSetting(r.Category, r.Key) {
			continue
		}
		result = append(result, a.settingRecordToInfo(r))
	}

	return result, nil
}

// GetSettingsByCategory 获取指定分类的设置
func (a *App) GetSettingsByCategory(category string) ([]SettingInfo, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.settingsService == nil {
		return nil, fmt.Errorf("设置服务未启用")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	records, err := a.settingsService.GetByCategory(ctx, category)
	if err != nil {
		return nil, fmt.Errorf("获取分类设置失败: %w", err)
	}

	result := make([]SettingInfo, 0, len(records))
	for _, r := range records {
		if !service.SettingVisible(r.Category, r.Key, runtime.GOOS) {
			continue
		}
		result = append(result, a.settingRecordToInfo(r))
	}

	return result, nil
}

// GetSetting 获取单个设置
func (a *App) GetSetting(category, key string) (SettingInfo, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.settingsService == nil {
		return SettingInfo{}, fmt.Errorf("设置服务未启用")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	record, err := a.settingsService.Get(ctx, category, key)
	if err != nil {
		return SettingInfo{}, fmt.Errorf("获取设置失败: %w", err)
	}
	if record == nil {
		return SettingInfo{}, fmt.Errorf("设置 '%s.%s' 不存在", category, key)
	}

	return a.settingRecordToInfo(record), nil
}

// UpdateSetting 更新单个设置
func (a *App) UpdateSetting(input UpdateSettingInput) error {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.settingsService == nil {
		return fmt.Errorf("设置服务未启用")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.validateLANAccessSettingUpdates(ctx, []UpdateSettingInput{input}); err != nil {
		return err
	}
	if err := validateRetentionSettingUpdates([]UpdateSettingInput{input}); err != nil {
		return err
	}
	if input.Category == service.CategoryImageGeneration {
		if err := a.validateImageGenerationSettingUpdates(ctx, []UpdateSettingInput{input}); err != nil {
			return err
		}
	}
	if err := normalizeClaudeModelSetting(&input); err != nil {
		return err
	}
	if input.Category == service.CategoryImageGeneration && input.Key == "api_key" && input.Value == "" {
		return nil
	}
	if input.Category == service.CategoryImageGeneration {
		input.Value = strings.TrimSpace(input.Value)
	}

	if err := a.settingsService.Set(ctx, input.Category, input.Key, input.Value); err != nil {
		return fmt.Errorf("更新设置失败: %w", err)
	}
	if input.Category == service.CategoryServer && input.Key == "lan_access" {
		// RequiresRestart settings intentionally skip the generic on-change
		// callback, but the gate must reject remote requests immediately.
		a.syncLANAccessGate()
	}

	if a.logger != nil {
		a.logger.Info("✅ 设置已更新", "category", input.Category, "key", input.Key)
	}

	return nil
}

// BatchUpdateSettings 批量更新设置
func (a *App) BatchUpdateSettings(input BatchUpdateSettingsInput) error {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.settingsService == nil {
		return fmt.Errorf("设置服务未启用")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := a.validateLANAccessSettingUpdates(ctx, input.Settings); err != nil {
		return err
	}
	if err := validateRetentionSettingUpdates(input.Settings); err != nil {
		return err
	}
	if err := a.validateImageGenerationSettingUpdates(ctx, input.Settings); err != nil {
		return err
	}
	for index := range input.Settings {
		if err := normalizeClaudeModelSetting(&input.Settings[index]); err != nil {
			return err
		}
	}

	// 转换为 store.SettingRecord
	records := make([]*store.SettingRecord, 0, len(input.Settings))
	for _, s := range input.Settings {
		if s.Category == service.CategoryImageGeneration && s.Key == "api_key" && s.Value == "" {
			continue
		}
		value := s.Value
		if s.Category == service.CategoryImageGeneration {
			value = strings.TrimSpace(value)
		}
		records = append(records, &store.SettingRecord{
			Category: s.Category,
			Key:      s.Key,
			Value:    value,
		})
	}

	if len(records) == 0 {
		return nil
	}
	if err := a.settingsService.UpdateAndApply(ctx, records); err != nil {
		return fmt.Errorf("批量更新设置失败: %w", err)
	}

	if a.logger != nil {
		a.logger.Info("✅ 设置已批量更新并应用", "count", len(records))
	}

	return nil
}

// ResetCategorySettings 重置分类设置为默认值
func (a *App) ResetCategorySettings(category string) error {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.settingsService == nil {
		return fmt.Errorf("设置服务未启用")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if category == service.CategoryAuth {
		lanAccess := a.settingsService.GetBool(ctx, service.CategoryServer, "lan_access", false)
		if lanAccess {
			return fmt.Errorf("开启局域网访问时不能重置「访问鉴权」，请先关闭局域网访问")
		}
	}

	if err := a.settingsService.ResetCategory(ctx, category); err != nil {
		return fmt.Errorf("重置设置失败: %w", err)
	}

	if a.logger != nil {
		a.logger.Info("✅ 设置分类已重置", "category", category)
	}

	return nil
}

// GetPortInfo 获取端口信息
func (a *App) GetPortInfo() PortInfo {
	a.mu.RLock()
	defer a.mu.RUnlock()

	// 默认值
	info := PortInfo{
		PreferredPort: 8087,
		ActualPort:    8087,
		IsDefault:     true,
		WasOccupied:   false,
		LANAddresses:  []string{},
	}
	if a.config != nil {
		info.PreferredPort = a.config.Server.Port
		info.ActualPort = a.config.Server.Port
	}

	if a.portManager != nil {
		portInfo := a.portManager.GetPortInfo()
		info = PortInfo{
			PreferredPort:   portInfo.PreferredPort,
			ActualPort:      portInfo.ActualPort,
			IsDefault:       portInfo.IsDefault,
			WasOccupied:     portInfo.WasOccupied,
			LANAccessActive: portInfo.LANAccessActive,
			LANAddresses:    []string{},
		}
	}
	info.LANAccess, info.LANTokenConfigured = a.lanAccessState()
	if info.LANAccessActive {
		info.LANAddresses = privateLANAddresses(info.ActualPort)
	}

	return info
}

func privateLANAddresses(port int) []string {
	addresses := make(map[string]struct{})
	interfaces, err := net.Interfaces()
	if err != nil {
		return []string{}
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		ifaceAddresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, ifaceAddress := range ifaceAddresses {
			prefix, err := netip.ParsePrefix(ifaceAddress.String())
			if err != nil {
				continue
			}
			addr := prefix.Addr().Unmap()
			if !addr.Is4() || !addr.IsPrivate() {
				continue
			}
			addresses["http://"+net.JoinHostPort(addr.String(), strconv.Itoa(port))] = struct{}{}
		}
	}
	result := make([]string, 0, len(addresses))
	for address := range addresses {
		result = append(result, address)
	}
	sort.Strings(result)
	return result
}

// UpdatePreferredPort 更新首选端口（需要重启生效）
func (a *App) UpdatePreferredPort(port int) error {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.settingsService == nil {
		return fmt.Errorf("设置服务未启用")
	}

	// 验证端口范围
	if port < 1 || port > 65535 {
		return fmt.Errorf("端口号必须在 1-65535 之间")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := a.settingsService.Set(ctx, service.CategoryServer, "preferred_port", fmt.Sprintf("%d", port)); err != nil {
		return fmt.Errorf("更新首选端口失败: %w", err)
	}

	// 更新 PortManager
	if a.portManager != nil {
		a.portManager.SetPreferredPort(port)
	}

	if a.logger != nil {
		a.logger.Info("✅ 首选端口已更新（需要重启生效）", "port", port)
	}

	return nil
}

// CheckPortAvailable 检查端口是否可用
func (a *App) CheckPortAvailable(port int) bool {
	return utils.IsPortAvailable(port)
}

// settingRecordToInfo 将数据库记录转换为前端 Info 结构
func (a *App) settingRecordToInfo(r *store.SettingRecord) SettingInfo {
	info := SettingInfo{
		ID:              r.ID,
		Category:        r.Category,
		Key:             r.Key,
		Value:           r.Value,
		ValueType:       r.ValueType,
		Label:           r.Label,
		Description:     r.Description,
		DisplayOrder:    r.DisplayOrder,
		RequiresRestart: r.RequiresRestart,
	}
	if r.ValueType == service.ValueTypePassword {
		info.SecretConfigured = r.Value != ""
		info.Value = ""
	}

	if !r.CreatedAt.IsZero() {
		info.CreatedAt = formatAPITime(r.CreatedAt)
	}
	if !r.UpdatedAt.IsZero() {
		info.UpdatedAt = formatAPITime(r.UpdatedAt)
	}

	return info
}
