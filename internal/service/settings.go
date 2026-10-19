// Package service 提供业务逻辑层实现
// 设置服务 - v5.1.0 新增 (2025-12-08)
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"sinuo/internal/codexidentity"
	"sinuo/internal/store"
)

// SettingCategory 设置分类常量
const (
	CategoryHealth          = "health"
	CategoryRequest         = "request"
	CategoryStreaming       = "streaming"
	CategoryAuth            = "auth"
	CategoryTokenCounting   = "token_counting"
	CategoryRetention       = "retention"
	CategoryHotPool         = "hot_pool"
	CategoryServer          = "server"
	CategoryClaudeRouting   = "claude_routing"
	CategoryImageGeneration = "image_generation"
	// CategoryCodexSubscriptionProxy 由 CodexSubscriptionProxySettingsService 独占：
	// 单行 JSON 配置 + revision CAS，不注册默认值/元数据，通用写入、重置、删除一律拒绝。
	CategoryCodexSubscriptionProxy = "codex_subscription_proxy"
)

// ErrProtectedSettingsCategory 表示该分类只能通过专用设置服务修改。
var ErrProtectedSettingsCategory = errors.New("该设置分类受保护，请使用专用设置页修改")

// protectedSettingsCategories 列出禁止经通用 Set/BatchSet/UpdateAndApply/Delete/ResetCategory 修改的分类。
var protectedSettingsCategories = map[string]struct{}{
	CategoryClaudeRouting:          {},
	CategoryCodexSubscriptionProxy: {},
	CategoryClaudeCodeEntry:        {},
	categoryCodexIdentityInternal:  {},
}

// IsProtectedSettingsCategory 判断分类是否由专用服务独占：
// 通用写入/重置/删除拒绝，通用列表读取也不返回（避免专用配置混进普通表单）。
func IsProtectedSettingsCategory(category string) bool {
	_, protected := protectedSettingsCategories[category]
	return protected
}

// assertSettingsCategoryWritable 在任何通用写入之前检查分类是否受保护。
func assertSettingsCategoryWritable(category string) error {
	if IsProtectedSettingsCategory(category) {
		return fmt.Errorf("%w: %s", ErrProtectedSettingsCategory, category)
	}
	return nil
}

// assertSettingsRecordsWritable 批量写入前整体校验，任一记录受保护则整批拒绝、不落库。
func assertSettingsRecordsWritable(records []*store.SettingRecord) error {
	for _, record := range records {
		if record == nil {
			continue
		}
		if err := assertSettingsCategoryWritable(record.Category); err != nil {
			return err
		}
	}
	return nil
}

// SettingValueType 设置值类型常量
const (
	ValueTypeString   = "string"
	ValueTypeInt      = "int"
	ValueTypeFloat    = "float"
	ValueTypeBool     = "bool"
	ValueTypeDuration = "duration"
	ValueTypeJSON     = "json"
	ValueTypePassword = "password"
)

// CategoryInfo 分类信息
type CategoryInfo struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Icon        string `json:"icon"`
	Order       int    `json:"order"`
}

// SettingsService 设置管理业务服务
type SettingsService struct {
	identityMu         sync.Mutex // 身份设置校验、写入和请求快照读取串行化
	identityCacheMu    sync.RWMutex
	identityCached     bool
	identityPolicy     codexidentity.Policy
	store              store.SettingsStore
	onChangeFunc       func() // 配置变更回调
	categoryLabels     map[string]CategoryInfo
	registeredSettings map[[2]string]struct{}
}

// NewSettingsService 创建设置服务实例
func NewSettingsService(store store.SettingsStore) *SettingsService {
	svc := &SettingsService{
		store: store,
		categoryLabels: map[string]CategoryInfo{
			CategoryDesktop: {Name: CategoryDesktop, Label: "桌面", Description: "窗口关闭与系统托盘", Icon: "🖥️", Order: 11},
			CategoryAuth: {
				Name:        CategoryAuth,
				Label:       "访问鉴权",
				Description: "配置代理服务的访问令牌",
				Icon:        "🔐",
				Order:       1,
			},
			CategoryHealth: {
				Name:        CategoryHealth,
				Label:       "连通性测试",
				Description: "Claude 端点连通性测试，结果只做展示，不影响路由",
				Icon:        "❤️",
				Order:       4,
			},
			CategoryRequest: {
				Name:        CategoryRequest,
				Label:       "请求控制",
				Description: "非流式请求超时与流式中断处理",
				Icon:        "⏱️",
				Order:       5,
			},
			// CategoryStreaming 和 CategoryHotPool 不在前端设置页面展示
			// 这些是底层技术配置，保留在 config.yaml 中
			CategoryTokenCounting: {
				Name:        CategoryTokenCounting,
				Label:       "Token 计数",
				Description: "Claude count_tokens 请求的处理方式",
				Icon:        "🔢",
				Order:       6,
			},
			CategoryRetention: {
				Name:        CategoryRetention,
				Label:       "数据保留",
				Description: "配置历史数据保留策略",
				Icon:        "📦",
				Order:       7,
			},
			CategoryCodexIdentity: {Name: CategoryCodexIdentity, Label: "Codex 身份", Description: "默认自动适配账号，无需随接入或路由模式切换", Icon: "🔗", Order: 10},
			CategoryImageGeneration: {
				Name:        CategoryImageGeneration,
				Label:       "图像生成",
				Description: "配置内置 Image Gen 使用的单一 OpenAI 兼容上游",
				Icon:        "🖼️",
				Order:       9,
			},
			CategoryServer: {
				Name:        CategoryServer,
				Label:       "服务端口",
				Description: "配置 API 服务端口",
				Icon:        "📡",
				Order:       0, // 最前（特殊处理，单独显示）
			},
		},
	}
	svc.registeredSettings = make(map[[2]string]struct{})
	for _, record := range svc.GetAllDefaults() {
		svc.registeredSettings[[2]string{record.Category, record.Key}] = struct{}{}
	}
	return svc
}

// SetOnChangeCallback 设置配置变更回调
func (s *SettingsService) SetOnChangeCallback(fn func()) {
	s.onChangeFunc = fn
}

// GetCategories 获取所有分类信息
func (s *SettingsService) GetCategories() []CategoryInfo {
	categories := make([]CategoryInfo, 0, len(s.categoryLabels))
	for _, info := range s.categoryLabels {
		categories = append(categories, info)
	}
	// 按 Order 排序
	for i := 0; i < len(categories)-1; i++ {
		for j := i + 1; j < len(categories); j++ {
			if categories[i].Order > categories[j].Order {
				categories[i], categories[j] = categories[j], categories[i]
			}
		}
	}
	return categories
}

// GetCategoryInfo 获取分类信息
func (s *SettingsService) GetCategoryInfo(category string) *CategoryInfo {
	if info, ok := s.categoryLabels[category]; ok {
		return &info
	}
	return nil
}

// Get 获取单个设置值
func (s *SettingsService) Get(ctx context.Context, category, key string) (*store.SettingRecord, error) {
	return s.store.Get(ctx, category, key)
}

// GetValue 获取设置值（仅返回值字符串）
func (s *SettingsService) GetValue(ctx context.Context, category, key string) (string, error) {
	record, err := s.store.Get(ctx, category, key)
	if err != nil {
		return "", err
	}
	if record == nil {
		return "", nil
	}
	return record.Value, nil
}

// GetInt 获取整数值
func (s *SettingsService) GetInt(ctx context.Context, category, key string, defaultVal int) int {
	val, err := s.GetValue(ctx, category, key)
	if err != nil || val == "" {
		return defaultVal
	}
	if i, err := strconv.Atoi(val); err == nil {
		return i
	}
	return defaultVal
}

// GetFloat 获取浮点数值
func (s *SettingsService) GetFloat(ctx context.Context, category, key string, defaultVal float64) float64 {
	val, err := s.GetValue(ctx, category, key)
	if err != nil || val == "" {
		return defaultVal
	}
	if f, err := strconv.ParseFloat(val, 64); err == nil {
		return f
	}
	return defaultVal
}

// GetBool 获取布尔值
func (s *SettingsService) GetBool(ctx context.Context, category, key string, defaultVal bool) bool {
	val, err := s.GetValue(ctx, category, key)
	if err != nil || val == "" {
		return defaultVal
	}
	return ParseBool(val, defaultVal)
}

// ParseBool parses the boolean spelling used by persisted settings and API
// updates. Invalid non-empty values retain the historical false result.
func ParseBool(value string, defaultVal bool) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1", "yes":
		return true
	case "":
		return defaultVal
	default:
		return false
	}
}

// GetDuration 获取时间间隔值
func (s *SettingsService) GetDuration(ctx context.Context, category, key string, defaultVal time.Duration) time.Duration {
	val, err := s.GetValue(ctx, category, key)
	if err != nil || val == "" {
		return defaultVal
	}
	if d, err := time.ParseDuration(val); err == nil {
		return d
	}
	return defaultVal
}

// GetByCategory 获取分类下的所有设置
func (s *SettingsService) GetByCategory(ctx context.Context, category string) ([]*store.SettingRecord, error) {
	return s.store.GetByCategory(ctx, category)
}

// GetAll 获取所有设置
func (s *SettingsService) GetAll(ctx context.Context) ([]*store.SettingRecord, error) {
	return s.store.GetAll(ctx)
}

// Set 设置单个值
func (s *SettingsService) Set(ctx context.Context, category, key, value string) error {
	if err := assertSettingsCategoryWritable(category); err != nil {
		return err
	}
	err := s.withCodexIdentityUpdate(ctx, []*store.SettingRecord{{Category: category, Key: key, Value: value}}, func() error {
		return s.store.Set(ctx, category, key, value)
	})
	if err != nil {
		return err
	}
	s.triggerOnChange(category, key)
	return nil
}

// Delete 删除单个设置
func (s *SettingsService) Delete(ctx context.Context, category, key string) error {
	if category == CategoryCodexIdentity {
		return fmt.Errorf("Codex 身份设置不能删除，请使用分类重置")
	}
	if err := assertSettingsCategoryWritable(category); err != nil {
		return err
	}
	if err := s.store.Delete(ctx, category, key); err != nil {
		return err
	}
	s.triggerOnChange(category, key)
	return nil
}

// BatchSet 批量设置（不触发回调，需手动触发）
func (s *SettingsService) BatchSet(ctx context.Context, records []*store.SettingRecord) error {
	if err := assertSettingsRecordsWritable(records); err != nil {
		return err
	}
	return s.withCodexIdentityUpdate(ctx, records, func() error { return s.store.BatchSet(ctx, records) })
}

// UpdateAndApply 批量更新并应用（触发热更新）
// 只更新 value，保留 label、description 等元数据
func (s *SettingsService) UpdateAndApply(ctx context.Context, records []*store.SettingRecord) error {
	if err := assertSettingsRecordsWritable(records); err != nil {
		return err
	}
	if err := s.withCodexIdentityUpdate(ctx, records, func() error { return s.store.BatchUpdateValues(ctx, records) }); err != nil {
		return fmt.Errorf("保存设置失败: %w", err)
	}

	// 触发配置热更新
	if s.onChangeFunc != nil {
		s.onChangeFunc()
		slog.Info("✅ [SettingsService] 设置已保存并应用热更新")
	}

	return nil
}

// ResetCategory 重置分类设置为默认值
func (s *SettingsService) ResetCategory(ctx context.Context, category string) error {
	if category == CategoryCodexIdentity {
		return s.UpdateAndApply(ctx, codexIdentityDefaults())
	}
	// 受保护分类禁止“先删分类再写默认值”，否则会清掉 config、密钥 hash 与 revision
	if err := assertSettingsCategoryWritable(category); err != nil {
		return err
	}
	// 删除当前分类的所有设置
	if err := s.store.DeleteByCategory(ctx, category); err != nil {
		return fmt.Errorf("删除分类设置失败: %w", err)
	}

	// 重新初始化默认值
	defaults := s.getDefaultsForCategory(category)
	if len(defaults) > 0 {
		if err := s.store.BatchSet(ctx, defaults); err != nil {
			return fmt.Errorf("重置默认值失败: %w", err)
		}
	}

	// 触发热更新
	if s.onChangeFunc != nil {
		s.onChangeFunc()
	}

	slog.Info(fmt.Sprintf("✅ [SettingsService] 分类 %s 已重置为默认值", category))
	return nil
}

// triggerOnChange 触发变更回调（检查是否需要重启）
func (s *SettingsService) triggerOnChange(category, key string) {
	record, _ := s.store.Get(context.Background(), category, key)
	if record != nil && record.RequiresRestart {
		slog.Info(fmt.Sprintf("⚠️ [SettingsService] 设置 %s.%s 已保存，需要重启生效", category, key))
		return // 需要重启的配置不触发热更新
	}

	if s.onChangeFunc != nil {
		s.onChangeFunc()
	}
}

// InitDefaults 初始化默认设置
func (s *SettingsService) InitDefaults(ctx context.Context) error {
	defaults := s.GetAllDefaults()
	// 迁移失败不能写入默认 mode，否则下次启动会把旧「关闭」当作已迁移删除；
	// 该分类保持待迁移（策略读取 fail-closed），其余设置照常初始化，下次启动重试。
	if err := s.migrateCodexIdentityMode(ctx); err != nil {
		slog.Warn("⚠️ [设置迁移] Codex 身份旧开关迁移失败，本次跳过该分类默认值", "error", err)
		defaults = excludeSettingsCategory(defaults, CategoryCodexIdentity)
	}

	// 始终同步元数据（label、description、value_type等）
	// 这样即使数据库中已有数据，也能更新到最新的元数据
	// 但会保留用户设置的 value 值
	if err := s.store.SyncMetadata(ctx, defaults); err != nil {
		return err
	}

	if err := s.removeRetiredSettings(ctx); err != nil {
		slog.Warn("⚠️ [设置迁移] 清除退役设置失败（不影响使用）", "error", err)
	}

	return nil
}

// IsInitialized 检查是否已初始化
func (s *SettingsService) IsInitialized(ctx context.Context) (bool, error) {
	return s.store.IsInitialized(ctx)
}

// GetAllDefaults 获取所有默认设置
func (s *SettingsService) GetAllDefaults() []*store.SettingRecord {
	var defaults []*store.SettingRecord

	defaults = append(defaults, codexIdentityDefaults()...)
	defaults = append(defaults, desktopDefaults()...)

	// Server 设置
	defaults = append(defaults, s.getDefaultsForCategory(CategoryServer)...)

	// Health 设置
	defaults = append(defaults, s.getDefaultsForCategory(CategoryHealth)...)

	// Request 设置
	defaults = append(defaults, s.getDefaultsForCategory(CategoryRequest)...)

	// Streaming 和 HotPool 不写入数据库，保留在 config.yaml 中

	// Auth 设置
	defaults = append(defaults, s.getDefaultsForCategory(CategoryAuth)...)

	// TokenCounting 设置
	defaults = append(defaults, s.getDefaultsForCategory(CategoryTokenCounting)...)

	// Retention 设置
	defaults = append(defaults, s.getDefaultsForCategory(CategoryRetention)...)

	// ClaudeRouting 设置
	defaults = append(defaults, s.getDefaultsForCategory(CategoryClaudeRouting)...)

	// Image Generation 设置
	defaults = append(defaults, s.getDefaultsForCategory(CategoryImageGeneration)...)

	return defaults
}

// getDefaultsForCategory 获取分类的默认设置
func (s *SettingsService) getDefaultsForCategory(category string) []*store.SettingRecord {
	switch category {
	case CategoryDesktop:
		return desktopDefaults()
	case CategoryCodexIdentity:
		return codexIdentityDefaults()
	case CategoryServer:
		return []*store.SettingRecord{
			{Category: CategoryServer, Key: "preferred_port", Value: "8087", ValueType: ValueTypeInt, Label: "首选端口", Description: "API 服务首选端口，被占用时自动递增", DisplayOrder: 1, RequiresRestart: true},
			{Category: CategoryServer, Key: "lan_access", Value: "false", ValueType: ValueTypeBool, Label: "允许局域网访问", Description: "开启后同一网络中的其他设备可访问主端口，须携带「访问鉴权」中的 Token，本机请求不受影响；关闭后立即拒绝其他设备，重启后只监听本机。Claude Code / Codex 独立入口始终只监听本机", DisplayOrder: 2, RequiresRestart: true},
		}

	case CategoryHealth:
		return []*store.SettingRecord{
			{Category: CategoryHealth, Key: "timeout", Value: "5s", ValueType: ValueTypeDuration, Label: "检测超时", Description: "Claude 端点手动、批量与启动连通性测试的超时时间", DisplayOrder: 2},
		}

	case CategoryRequest:
		return []*store.SettingRecord{
			{Category: CategoryRequest, Key: "global_timeout", Value: "300s", ValueType: ValueTypeDuration, Label: "全局超时", Description: "非流式请求的总超时，作用于 Codex 账号池与 Claude 订阅直通；Claude 端点使用各自的超时设置", DisplayOrder: 1, RequiresRestart: true},
			{Category: CategoryRequest, Key: "eof_retry_hint", Value: "false", ValueType: ValueTypeBool, Label: "流式中断重试提示", Description: "流式传输中断时发送特定格式响应，触发客户端自动重试", DisplayOrder: 6},
		}

	case CategoryStreaming:
		return []*store.SettingRecord{
			{Category: CategoryStreaming, Key: "heartbeat_interval", Value: "30s", ValueType: ValueTypeDuration, Label: "心跳间隔", Description: "流式连接心跳间隔", DisplayOrder: 1},
			{Category: CategoryStreaming, Key: "read_timeout", Value: "10s", ValueType: ValueTypeDuration, Label: "读取超时", Description: "流式数据读取超时", DisplayOrder: 2},
			{Category: CategoryStreaming, Key: "max_idle_time", Value: "120s", ValueType: ValueTypeDuration, Label: "最大空闲时间", Description: "流式连接最大空闲时间", DisplayOrder: 3},
			{Category: CategoryStreaming, Key: "response_header_timeout", Value: "60s", ValueType: ValueTypeDuration, Label: "响应头超时", Description: "等待服务端首次响应头的超时时间", DisplayOrder: 4},
		}

	case CategoryAuth:
		return []*store.SettingRecord{
			{Category: CategoryAuth, Key: "enabled", Value: "false", ValueType: ValueTypeBool, Label: "启用鉴权", Description: "是否启用 API 访问鉴权", DisplayOrder: 1},
			{Category: CategoryAuth, Key: "token", Value: "", ValueType: ValueTypeString, Label: "鉴权 Token", Description: "Bearer Token 值", DisplayOrder: 2},
		}

	case CategoryTokenCounting:
		return []*store.SettingRecord{
			{Category: CategoryTokenCounting, Key: "enabled", Value: "true", ValueType: ValueTypeBool, Label: "启用 Token 计数", Description: "拦截 Claude 的 count_tokens：转发给勾选「支持 count_tokens」的端点，无可用端点时本地估算；关闭后按普通请求转发。不影响订阅直通", DisplayOrder: 1},
		}

	case CategoryRetention:
		return []*store.SettingRecord{
			{Category: CategoryRetention, Key: "retention_days", Value: "0", ValueType: ValueTypeInt, Label: "数据保留天数", Description: "请求记录保留天数，0 表示永久保留；超期记录每 24 小时清理一次", DisplayOrder: 1},
		}

	case CategoryClaudeRouting:
		return []*store.SettingRecord{
			{Category: CategoryClaudeRouting, Key: "mode", Value: "auto", ValueType: ValueTypeString, Label: "路由模式", Description: "auto / manual_preferred / manual_fixed", DisplayOrder: 1},
			{Category: CategoryClaudeRouting, Key: "endpoint_name", Value: "", ValueType: ValueTypeString, Label: "手动端点", Description: "手动路由目标端点名称", DisplayOrder: 2},
			{Category: CategoryClaudeRouting, Key: "set_by", Value: "", ValueType: ValueTypeString, Label: "设置来源", Description: "最近一次路由模式修改来源", DisplayOrder: 3},
			{Category: CategoryClaudeRouting, Key: "set_at", Value: "", ValueType: ValueTypeString, Label: "设置时间", Description: "最近一次路由模式修改时间", DisplayOrder: 4},
			{Category: CategoryClaudeRouting, Key: "fallback_enabled", Value: "true", ValueType: ValueTypeBool, Label: "允许回退", Description: "手动优选模式下允许失败后回退其他端点", DisplayOrder: 5},
			{Category: CategoryClaudeRouting, Key: "revision", Value: "0", ValueType: ValueTypeInt, Label: "状态修订号", Description: "路由状态持久化修订号（协调器并发保护）", DisplayOrder: 6},
		}

	case CategoryImageGeneration:
		return []*store.SettingRecord{
			{Category: CategoryImageGeneration, Key: "enabled", Value: "false", ValueType: ValueTypeBool, Label: "启用图像生成", Description: "接管 POST /v1/images/generations 与 /v1/images/edits 并转发到下方配置的上游", DisplayOrder: 1},
			{Category: CategoryImageGeneration, Key: "direct_connect", Value: "false", ValueType: ValueTypeBool, Label: "图像 API 直连", Description: "开启后仅图像生成与编辑连接使用下方专用源端口；需在 Surge 规则中将同一 SRC-PORT 范围设为 DIRECT", DisplayOrder: 2},
			{Category: CategoryImageGeneration, Key: "direct_source_port_range", Value: "31080-31179", ValueType: ValueTypeString, Label: "直连源端口范围", Description: "Surge 规则示例：SRC-PORT,31080-31179,DIRECT；需放在会代理图像上游域名的规则之前", DisplayOrder: 3},
			{Category: CategoryImageGeneration, Key: "endpoint_url", Value: "", ValueType: ValueTypeString, Label: "生图完整请求 URL", Description: "例如 https://api.example.com/v1/images/generations；编辑请求自动使用同源 /images/edits", DisplayOrder: 4},
			{Category: CategoryImageGeneration, Key: "api_key", Value: "", ValueType: ValueTypePassword, Label: "API Key", Description: "仅保存在本机数据库且不会回显；重置本分类可清除", DisplayOrder: 5},
			{Category: CategoryImageGeneration, Key: "model", Value: "gpt-image-2", ValueType: ValueTypeString, Label: "默认模型", Description: "请求未提供 model 时使用；通常为 gpt-image-2", DisplayOrder: 6},
			{Category: CategoryImageGeneration, Key: "fixed_price_usd", Value: "0", ValueType: ValueTypeFloat, Label: "每次图像请求价格（USD）", Description: "每个成功的生成或编辑请求固定记录这一笔成本；0 表示暂不计费", DisplayOrder: 7},
			{Category: CategoryImageGeneration, Key: "timeout", Value: "300s", ValueType: ValueTypeDuration, Label: "请求超时", Description: "单次图像生成或编辑请求的最长等待时间", DisplayOrder: 8},
		}

	case CategoryHotPool:
		return []*store.SettingRecord{
			{Category: CategoryHotPool, Key: "enabled", Value: "true", ValueType: ValueTypeBool, Label: "启用热池", Description: "是否启用内存热池模式", DisplayOrder: 1},
			{Category: CategoryHotPool, Key: "max_age", Value: "30m", ValueType: ValueTypeDuration, Label: "最大存活时间", Description: "请求在热池中的最大存活时间", DisplayOrder: 2},
			{Category: CategoryHotPool, Key: "max_size", Value: "10000", ValueType: ValueTypeInt, Label: "最大容量", Description: "热池最大请求数量", DisplayOrder: 3},
			{Category: CategoryHotPool, Key: "cleanup_interval", Value: "1m", ValueType: ValueTypeDuration, Label: "清理间隔", Description: "僵尸请求清理间隔", DisplayOrder: 4},
			{Category: CategoryHotPool, Key: "archive_on_cleanup", Value: "true", ValueType: ValueTypeBool, Label: "清理时归档", Description: "清理时是否将请求归档到数据库", DisplayOrder: 5},
		}

	default:
		return nil
	}
}
