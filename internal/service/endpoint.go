// Package service 提供业务逻辑层实现
// 端点服务 - v5.0.0 新增 (2025-12-05)
package service

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"sinuo/config"
	"sinuo/internal/endpoint"
	"sinuo/internal/modelrewrite"
	"sinuo/internal/store"
	timezonepolicy "sinuo/internal/timezone"
	"sinuo/internal/utils"
)

// endpointAdmissionDrainTimeout 停用/删除时等待 in-flight admission 排空的上限（§7.6 规则 8）
const endpointAdmissionDrainTimeout = 3 * time.Second

// EndpointService 端点管理业务服务
// 连接 EndpointStore（数据持久化）和 EndpointManager（运行时管理）
type EndpointService struct {
	store   store.EndpointStore
	manager *endpoint.Manager
}

// NewEndpointService 创建端点服务实例
func NewEndpointService(
	store store.EndpointStore,
	manager *endpoint.Manager,
) *EndpointService {
	return &EndpointService{
		store:   store,
		manager: manager,
	}
}

// CreateEndpoint 创建新端点
// 1. 保存到数据库
// 2. 添加到运行时管理器
func (s *EndpointService) CreateEndpoint(ctx context.Context, record *store.EndpointRecord) (*store.EndpointRecord, error) {
	// 验证必填字段
	if err := s.validateRecord(record); err != nil {
		return nil, err
	}

	// 检查名称唯一性（运行时）
	if existing := s.manager.GetEndpointByNameAny(record.Name); existing != nil {
		return nil, fmt.Errorf("端点 '%s' 已存在", record.Name)
	}

	// 保存到数据库
	created, err := s.store.Create(ctx, record)
	if err != nil {
		return nil, fmt.Errorf("保存端点到数据库失败: %w", err)
	}

	// 转换为配置并添加到管理器
	cfg := s.recordToConfig(created)
	if err := s.manager.AddEndpoint(cfg); err != nil {
		// 回滚数据库操作
		_ = s.store.Delete(ctx, record.Name)
		return nil, fmt.Errorf("添加端点到管理器失败: %w", err)
	}

	slog.Info(fmt.Sprintf("✅ [EndpointService] 创建端点成功: %s", record.Name))
	return created, nil
}

// GetEndpoint 获取端点详情
func (s *EndpointService) GetEndpoint(ctx context.Context, name string) (*store.EndpointRecord, error) {
	record, err := s.store.Get(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("获取端点失败: %w", err)
	}
	return record, nil
}

// ListEndpoints 列出所有端点
func (s *EndpointService) ListEndpoints(ctx context.Context) ([]*store.EndpointRecord, error) {
	records, err := s.store.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("列出端点失败: %w", err)
	}
	return records, nil
}

// UpdateEndpoint 更新端点配置
// 1. 更新数据库
// 2. 更新运行时管理器
func (s *EndpointService) UpdateEndpoint(ctx context.Context, record *store.EndpointRecord) error {
	// 验证端点存在
	existing, err := s.store.Get(ctx, record.Name)
	if err != nil {
		return fmt.Errorf("查询端点失败: %w", err)
	}
	if existing == nil {
		return fmt.Errorf("端点 '%s' 不存在", record.Name)
	}

	// 验证必填字段
	if err := s.validateRecord(record); err != nil {
		return err
	}

	// 更新数据库
	if err := s.store.Update(ctx, record); err != nil {
		return fmt.Errorf("更新数据库失败: %w", err)
	}

	// 更新运行时管理器
	if err := s.syncEndpointRuntime(record); err != nil {
		slog.Warn(fmt.Sprintf("⚠️ [EndpointService] 更新运行时管理器失败: %v", err))
		// 不回滚数据库，下次重启会同步
	}

	slog.Info(fmt.Sprintf("✅ [EndpointService] 更新端点成功: %s", record.Name))
	return nil
}

// SyncEndpointRuntime 从数据库读取端点，并通过唯一映射入口同步到运行时管理器。
func (s *EndpointService) SyncEndpointRuntime(ctx context.Context, name string) error {
	record, err := s.store.Get(ctx, name)
	if err != nil {
		return fmt.Errorf("读取端点配置失败: %w", err)
	}
	if record == nil {
		return fmt.Errorf("端点 '%s' 不存在", name)
	}
	return s.syncEndpointRuntime(record)
}

func (s *EndpointService) syncEndpointRuntime(record *store.EndpointRecord) error {
	if record == nil {
		return fmt.Errorf("端点配置不能为空")
	}
	if err := s.validateRecord(record); err != nil {
		return err
	}
	cfg := s.recordToConfig(record)
	if s.manager.GetEndpointByNameAny(record.Name) == nil {
		return s.manager.AddEndpoint(cfg)
	}
	return s.manager.UpdateEndpointConfig(record.Name, cfg)
}

// DeleteEndpoint 删除端点
// v7：删除经 writer coordinator 串行——DB 删除成功后才移除内存；
// DB 删除失败时内存不动、返回错误（替代旧的"先删内存再删 DB、失败不恢复"顺序）
// v8 §7.6：删除前设置 pending gate 阻断新 attempt 并等待 in-flight admission 排空
func (s *EndpointService) DeleteEndpoint(ctx context.Context, name string) error {
	// 验证端点存在
	existing, err := s.store.Get(ctx, name)
	if err != nil {
		return fmt.Errorf("查询端点失败: %w", err)
	}
	if existing == nil {
		return fmt.Errorf("端点 '%s' 不存在", name)
	}

	s.manager.SetPendingAvailabilityGate(name, true)
	if !s.manager.WaitAdmissionsDrained(name, endpointAdmissionDrainTimeout) {
		slog.Warn("⚠️ 端点删除等待 in-flight attempt 排空超时，gate 继续生效", "endpoint", name)
	}

	if err := s.store.Delete(ctx, name); err != nil {
		s.manager.SetPendingAvailabilityGate(name, false)
		return fmt.Errorf("删除端点数据库记录失败: %w", err)
	}
	if err := s.manager.RemoveEndpoint(name); err != nil {
		s.manager.SetPendingAvailabilityGate(name, false)
		return fmt.Errorf("删除端点运行态失败: %w", err)
	}
	s.manager.SetPendingAvailabilityGate(name, false)

	slog.Info(fmt.Sprintf("✅ [EndpointService] 删除端点成功: %s", name))
	return nil
}

// SetEndpointAvailability 更新硬启用状态（v8 §7.6：persist-then-publish）。
// 停用先设置 pending gate 阻断新 attempt；事务写库成功后原子发布运行时；
// 写库失败清除 gate、运行态不变；发布失败执行补偿事务回滚 DB，不返回假成功。
func (s *EndpointService) SetEndpointAvailability(ctx context.Context, name string, enabled bool) error {
	record, err := s.store.Get(ctx, name)
	if err != nil {
		return fmt.Errorf("获取端点失败: %w", err)
	}
	if record == nil {
		return fmt.Errorf("端点 '%s' 不存在", name)
	}
	previous := record.IsAvailabilityEnabled()

	if !enabled && s.manager != nil {
		s.manager.SetPendingAvailabilityGate(name, true)
	}

	if err := s.store.SetAvailabilityEnabled(ctx, name, enabled); err != nil {
		if !enabled && s.manager != nil {
			s.manager.SetPendingAvailabilityGate(name, false)
		}
		return fmt.Errorf("持久化硬启用状态失败: %w", err)
	}

	if s.manager != nil {
		// v8 §7.6 规则 8：停用发布前等待已取得的 admission 退出；
		// 超时不阻塞停用（gate 持续生效，泄漏的 lease 不能永久阻塞）
		if !enabled && !s.manager.WaitAdmissionsDrained(name, endpointAdmissionDrainTimeout) {
			slog.Warn("⚠️ 端点停用等待 in-flight attempt 排空超时，gate 继续生效", "endpoint", name)
		}
		if err := s.manager.PublishEndpointAvailability(name, enabled); err != nil {
			// 补偿事务：回滚 DB，绝不在运行态未生效时返回成功
			if rollbackErr := s.store.SetAvailabilityEnabled(ctx, name, previous); rollbackErr != nil {
				slog.Error("硬启用发布失败且回滚失败，状态可能不一致", "endpoint", name, "error", rollbackErr)
			}
			s.manager.SetPendingAvailabilityGate(name, false)
			return fmt.Errorf("发布端点硬启用运行态失败: %w", err)
		}
		s.manager.SetPendingAvailabilityGate(name, false)
	}

	slog.Info("✅ [EndpointService] 端点硬启用状态已更新", "endpoint", name, "enabled", enabled)
	return nil
}

// SetEndpointAutoSchedule 更新“参与自动调度”状态（复用同一协调顺序，§7.6 规则 5）
func (s *EndpointService) SetEndpointAutoSchedule(ctx context.Context, name string, enabled bool) error {
	record, err := s.store.Get(ctx, name)
	if err != nil {
		return fmt.Errorf("获取端点失败: %w", err)
	}
	if record == nil {
		return fmt.Errorf("端点 '%s' 不存在", name)
	}
	previous := record.FailoverEnabled

	if err := s.store.SetFailoverEnabled(ctx, name, enabled); err != nil {
		return fmt.Errorf("持久化自动调度状态失败: %w", err)
	}

	if s.manager != nil {
		if err := s.manager.PublishEndpointAutoSchedule(name, enabled); err != nil {
			if rollbackErr := s.store.SetFailoverEnabled(ctx, name, previous); rollbackErr != nil {
				slog.Error("自动调度发布失败且回滚失败，状态可能不一致", "endpoint", name, "error", rollbackErr)
			}
			return fmt.Errorf("发布端点自动调度运行态失败: %w", err)
		}
	}

	slog.Info("✅ [EndpointService] 端点自动调度状态已更新", "endpoint", name, "enabled", enabled)
	return nil
}

// SyncFromDatabase 从数据库同步端点到管理器
func (s *EndpointService) SyncFromDatabase(ctx context.Context) error {
	records, err := s.store.List(ctx)
	if err != nil {
		return fmt.Errorf("获取端点列表失败: %w", err)
	}

	slog.Info(fmt.Sprintf("🔄 [EndpointService] 从数据库同步 %d 个端点", len(records)))

	// 转换为配置数组
	endpoints := make([]config.EndpointConfig, len(records))
	for i, record := range records {
		if err := s.validateRecord(record); err != nil {
			return fmt.Errorf("端点 %q 配置无效: %w", record.Name, err)
		}
		endpoints[i] = s.recordToConfig(record)
	}

	// 使用专门的同步方法（不走 UpdateConfig）
	s.manager.SyncEndpoints(endpoints)
	return nil
}

// GetEndpointWithHealth 获取端点详情（包含健康状态）
func (s *EndpointService) GetEndpointWithHealth(ctx context.Context, name string) (map[string]interface{}, error) {
	record, err := s.store.Get(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("获取端点失败: %w", err)
	}
	if record == nil {
		return nil, fmt.Errorf("端点 '%s' 不存在", name)
	}

	// 获取健康状态
	status := s.manager.GetEndpointStatus(name)

	result := map[string]interface{}{
		"id":                                record.ID,
		"name":                              record.Name,
		"url":                               record.URL,
		"token_masked":                      maskToken(record.Token),
		"api_key_masked":                    maskToken(record.ApiKey),
		"priority":                          record.Priority,
		"failover_enabled":                  record.FailoverEnabled,
		"timeout_seconds":                   record.TimeoutSeconds,
		"model_rewrite_rules":               record.ModelRewriteRules,
		"soft_failure_window_minutes":       record.SoftFailureWindowMinutes,
		"soft_failure_threshold":            record.SoftFailureThreshold,
		"cost_multiplier":                   record.CostMultiplier,
		"cache_creation_cost_multiplier_1h": record.CacheCreationCostMultiplier1h,
		"availability_enabled":              record.IsAvailabilityEnabled(),
		"created_at":                        record.CreatedAt,
		"updated_at":                        record.UpdatedAt,
		"health": map[string]interface{}{
			"healthy":          status.Healthy,
			"never_checked":    status.NeverChecked,
			"last_check":       "",
			"response_time_ms": status.ResponseTime.Milliseconds(),
		},
	}

	if !status.LastCheck.IsZero() {
		health := result["health"].(map[string]interface{})
		health["last_check"] = timezonepolicy.FormatStorage(status.LastCheck)
	}

	return result, nil
}

// validateRecord 验证端点记录
func (s *EndpointService) validateRecord(record *store.EndpointRecord) error {
	if record == nil {
		return fmt.Errorf("端点配置不能为空")
	}
	record.Name = strings.TrimSpace(record.Name)
	record.URL = strings.TrimSpace(record.URL)
	if record.Name == "" {
		return fmt.Errorf("端点名称不能为空")
	}
	if record.URL == "" {
		return fmt.Errorf("端点 URL 不能为空")
	}
	parsedURL, err := url.Parse(record.URL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Hostname() == "" {
		return fmt.Errorf("端点 URL 必须是有效的 HTTP 或 HTTPS 地址")
	}
	if record.Priority < 0 {
		return fmt.Errorf("端点优先级不能小于 0")
	}
	if record.CostMultiplier <= 0 || record.InputCostMultiplier <= 0 || record.OutputCostMultiplier <= 0 ||
		record.CacheCreationCostMultiplier <= 0 || record.CacheCreationCostMultiplier1h <= 0 || record.CacheReadCostMultiplier <= 0 {
		return fmt.Errorf("端点成本倍率必须大于 0")
	}
	for key := range record.Headers {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("端点 Header 名称不能为空")
		}
	}
	if err := config.ValidateSoftFailureOverrides(record.SoftFailureWindowMinutes, record.SoftFailureThreshold); err != nil {
		return err
	}
	record.ModelRewriteRules = strings.TrimSpace(record.ModelRewriteRules)
	if err := modelrewrite.ValidateExact(record.ModelRewriteRules, "/v1/messages", "/v1/messages/count_tokens"); err != nil {
		return fmt.Errorf("模型兼容改写规则无效: %w", err)
	}
	return nil
}

// recordToConfig 将数据库记录转换为配置对象
func (s *EndpointService) recordToConfig(record *store.EndpointRecord) config.EndpointConfig {
	cfg := config.EndpointConfig{
		Name:                     record.Name,
		URL:                      record.URL,
		Priority:                 record.Priority,
		Token:                    record.Token,
		ApiKey:                   record.ApiKey,
		Headers:                  record.Headers,
		Timeout:                  time.Duration(record.TimeoutSeconds) * time.Second,
		SupportsCountTokens:      record.SupportsCountTokens,
		ModelRewriteRules:        record.ModelRewriteRules,
		SoftFailureWindowMinutes: config.CloneSoftFailureValue(record.SoftFailureWindowMinutes),
		SoftFailureThreshold:     config.CloneSoftFailureValue(record.SoftFailureThreshold),
	}

	// 设置 FailoverEnabled（v8 语义：参与自动调度）
	fe := record.FailoverEnabled
	cfg.FailoverEnabled = &fe

	// v8: 硬启用
	avail := record.IsAvailabilityEnabled()
	cfg.AvailabilityEnabled = &avail

	// 设置 Cooldown
	if record.CooldownSeconds != nil {
		cd := time.Duration(*record.CooldownSeconds) * time.Second
		cfg.Cooldown = &cd
	}

	return cfg
}

// maskToken 脱敏 Token
func maskToken(token string) string {
	return utils.MaskToken(token)
}
