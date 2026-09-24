// app_api_model_pricing.go - v5.0+ 模型定价管理 API (Wails Bindings)
// 提供 SQLite 模型定价存储的增删改查功能
// 创建时间: 2025-12-06

package main

import (
	"context"
	"fmt"
	"time"

	"cc-forwarder/internal/store"
)

// ============================================================
// v5.0+ 模型定价管理 API (SQLite)
// ============================================================

// ModelPricingInfo 模型定价信息（给前端用的结构体）
type ModelPricingInfo struct {
	ID                   int64   `json:"id"`
	ModelName            string  `json:"model_name"`
	DisplayName          string  `json:"display_name"`
	Description          string  `json:"description"`
	InputPrice           float64 `json:"input_price"`             // USD per 1M tokens
	OutputPrice          float64 `json:"output_price"`            // USD per 1M tokens
	CacheCreationPrice5m float64 `json:"cache_creation_price_5m"` // 5分钟缓存创建 (USD per 1M tokens)
	CacheCreationPrice1h float64 `json:"cache_creation_price_1h"` // 1小时缓存创建 (USD per 1M tokens)
	CacheReadPrice       float64 `json:"cache_read_price"`        // USD per 1M tokens
	IsDefault            bool    `json:"is_default"`
	CreatedAt            string  `json:"created_at"`
	UpdatedAt            string  `json:"updated_at"`
}

// CreateModelPricingInput 创建模型定价的输入参数
type CreateModelPricingInput struct {
	ModelName            string  `json:"model_name"`
	DisplayName          string  `json:"display_name"`
	Description          string  `json:"description"`
	InputPrice           float64 `json:"input_price"`
	OutputPrice          float64 `json:"output_price"`
	CacheCreationPrice5m float64 `json:"cache_creation_price_5m"`
	CacheCreationPrice1h float64 `json:"cache_creation_price_1h"`
	CacheReadPrice       float64 `json:"cache_read_price"`
	IsDefault            bool    `json:"is_default"`
}

// ModelPricingStorageStatus 模型定价存储状态
type ModelPricingStorageStatus struct {
	Enabled    bool `json:"enabled"`     // 是否启用
	TotalCount int  `json:"total_count"` // 定价总数
	HasDefault bool `json:"has_default"` // 是否有默认定价
}

// GetModelPricingStorageStatus 获取模型定价存储状态
func (a *App) GetModelPricingStorageStatus() ModelPricingStorageStatus {
	a.mu.RLock()
	defer a.mu.RUnlock()

	status := ModelPricingStorageStatus{
		Enabled: false,
	}

	if a.modelPricingService == nil {
		return status
	}

	status.Enabled = true

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	count, err := a.modelPricingService.GetPricingCount(ctx)
	if err == nil {
		status.TotalCount = count
	}

	defaultPricing := a.modelPricingService.GetDefaultPricing(ctx)
	status.HasDefault = defaultPricing != nil

	return status
}

// GetModelPricings 获取所有模型定价
func (a *App) GetModelPricings() ([]ModelPricingInfo, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.modelPricingService == nil {
		return nil, fmt.Errorf("模型定价服务未启用 (需要设置 usage_tracking.enabled: true)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	records, err := a.modelPricingService.ListPricings(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取模型定价列表失败: %w", err)
	}

	result := make([]ModelPricingInfo, 0, len(records))
	for _, r := range records {
		result = append(result, a.pricingRecordToInfo(r))
	}

	return result, nil
}

// GetModelPricing 获取单个模型定价
func (a *App) GetModelPricing(modelName string) (ModelPricingInfo, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.modelPricingService == nil {
		return ModelPricingInfo{}, fmt.Errorf("模型定价服务未启用")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	record, err := a.modelPricingService.GetPricing(ctx, modelName)
	if err != nil {
		return ModelPricingInfo{}, fmt.Errorf("获取模型定价失败: %w", err)
	}
	if record == nil {
		return ModelPricingInfo{}, fmt.Errorf("模型定价 '%s' 不存在", modelName)
	}

	return a.pricingRecordToInfo(record), nil
}

// CreateModelPricing 创建新模型定价
func (a *App) CreateModelPricing(input CreateModelPricingInput) error {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.modelPricingService == nil {
		return fmt.Errorf("模型定价服务未启用")
	}

	// 设置默认值
	if input.InputPrice == 0 {
		input.InputPrice = 3.0
	}
	if input.OutputPrice == 0 {
		input.OutputPrice = 15.0
	}
	if input.CacheCreationPrice5m == 0 {
		input.CacheCreationPrice5m = input.InputPrice * 1.25
	}
	if input.CacheCreationPrice1h == 0 {
		input.CacheCreationPrice1h = input.InputPrice * 2.0
	}
	if input.CacheReadPrice == 0 {
		input.CacheReadPrice = input.InputPrice * 0.1
	}

	record := &store.ModelPricingRecord{
		ModelName:            input.ModelName,
		DisplayName:          input.DisplayName,
		Description:          input.Description,
		InputPrice:           input.InputPrice,
		OutputPrice:          input.OutputPrice,
		CacheCreationPrice5m: input.CacheCreationPrice5m,
		CacheCreationPrice1h: input.CacheCreationPrice1h,
		CacheReadPrice:       input.CacheReadPrice,
		IsDefault:            input.IsDefault,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := a.modelPricingService.CreatePricing(ctx, record)
	if err != nil {
		return fmt.Errorf("创建模型定价失败: %w", err)
	}

	// 成本计算读取的是 UsageTracker 的定价快照，写库后需立即同步，否则要重启才生效
	a.syncPricingToTracker(ctx)

	if a.logger != nil {
		a.logger.Info("✅ 模型定价已创建", "model", input.ModelName)
	}

	return nil
}

// UpdateModelPricing 更新模型定价
func (a *App) UpdateModelPricing(modelName string, input CreateModelPricingInput) error {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.modelPricingService == nil {
		return fmt.Errorf("模型定价服务未启用")
	}

	record := &store.ModelPricingRecord{
		ModelName:            modelName, // 使用参数中的 modelName
		DisplayName:          input.DisplayName,
		Description:          input.Description,
		InputPrice:           input.InputPrice,
		OutputPrice:          input.OutputPrice,
		CacheCreationPrice5m: input.CacheCreationPrice5m,
		CacheCreationPrice1h: input.CacheCreationPrice1h,
		CacheReadPrice:       input.CacheReadPrice,
		IsDefault:            input.IsDefault,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := a.modelPricingService.UpdatePricing(ctx, record); err != nil {
		return fmt.Errorf("更新模型定价失败: %w", err)
	}

	a.syncPricingToTracker(ctx)

	if a.logger != nil {
		a.logger.Info("✅ 模型定价已更新", "model", modelName)
	}

	return nil
}

// DeleteModelPricing 删除模型定价
func (a *App) DeleteModelPricing(modelName string) error {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.modelPricingService == nil {
		return fmt.Errorf("模型定价服务未启用")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := a.modelPricingService.DeletePricing(ctx, modelName); err != nil {
		return fmt.Errorf("删除模型定价失败: %w", err)
	}

	a.syncPricingToTracker(ctx)

	if a.logger != nil {
		a.logger.Info("✅ 模型定价已删除", "model", modelName)
	}

	return nil
}

// SetDefaultModelPricing 设置默认模型定价
func (a *App) SetDefaultModelPricing(modelName string) error {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.modelPricingService == nil {
		return fmt.Errorf("模型定价服务未启用")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := a.modelPricingService.SetDefaultPricing(ctx, modelName); err != nil {
		return fmt.Errorf("设置默认定价失败: %w", err)
	}

	if a.logger != nil {
		a.logger.Info("✅ 已设置默认模型定价", "model", modelName)
	}

	return nil
}

// pricingRecordToInfo 将数据库记录转换为前端 Info 结构
func (a *App) pricingRecordToInfo(r *store.ModelPricingRecord) ModelPricingInfo {
	info := ModelPricingInfo{
		ID:                   r.ID,
		ModelName:            r.ModelName,
		DisplayName:          r.DisplayName,
		Description:          r.Description,
		InputPrice:           r.InputPrice,
		OutputPrice:          r.OutputPrice,
		CacheCreationPrice5m: r.CacheCreationPrice5m,
		CacheCreationPrice1h: r.CacheCreationPrice1h,
		CacheReadPrice:       r.CacheReadPrice,
		IsDefault:            r.IsDefault,
	}

	if !r.CreatedAt.IsZero() {
		info.CreatedAt = formatAPITime(r.CreatedAt)
	}
	if !r.UpdatedAt.IsZero() {
		info.UpdatedAt = formatAPITime(r.UpdatedAt)
	}

	return info
}
