package main

import (
	"context"
	"fmt"

	"sinuo/internal/store"
)

// gpt56ModelPricingDefaults 返回 OpenAI GPT-5.6 标准层定价（USD / 1M tokens）。
// OpenAI 只提供统一的缓存写入价格，因此两个历史缓存时长字段使用同一价格。
func gpt56ModelPricingDefaults() []*store.ModelPricingRecord {
	return []*store.ModelPricingRecord{
		{
			ModelName:            "gpt-5.6",
			DisplayName:          "GPT-5.6",
			Description:          "GPT-5.6 别名（路由到 GPT-5.6 Sol）官方标准定价",
			InputPrice:           5.0,
			OutputPrice:          30.0,
			CacheCreationPrice5m: 6.25,
			CacheCreationPrice1h: 6.25,
			CacheReadPrice:       0.50,
		},
		{
			ModelName:            "gpt-5.6-sol",
			DisplayName:          "GPT-5.6 Sol",
			Description:          "GPT-5.6 Sol 官方标准定价",
			InputPrice:           5.0,
			OutputPrice:          30.0,
			CacheCreationPrice5m: 6.25,
			CacheCreationPrice1h: 6.25,
			CacheReadPrice:       0.50,
		},
		{
			ModelName:            "gpt-5.6-terra",
			DisplayName:          "GPT-5.6 Terra",
			Description:          "GPT-5.6 Terra 官方标准定价",
			InputPrice:           2.5,
			OutputPrice:          15.0,
			CacheCreationPrice5m: 3.125,
			CacheCreationPrice1h: 3.125,
			CacheReadPrice:       0.25,
		},
		{
			ModelName:            "gpt-5.6-luna",
			DisplayName:          "GPT-5.6 Luna",
			Description:          "GPT-5.6 Luna 官方标准定价",
			InputPrice:           1.0,
			OutputPrice:          6.0,
			CacheCreationPrice5m: 1.25,
			CacheCreationPrice1h: 1.25,
			CacheReadPrice:       0.10,
		},
	}
}

// ensureGPT56ModelPricing 只插入缺失的内置定价，保留用户已有的自定义价格。
func (a *App) ensureGPT56ModelPricing(ctx context.Context) (int, error) {
	if a == nil || a.modelPricingService == nil {
		return 0, fmt.Errorf("模型定价服务未启用")
	}

	inserted := 0
	for _, pricing := range gpt56ModelPricingDefaults() {
		existing, err := a.modelPricingService.GetPricing(ctx, pricing.ModelName)
		if err != nil {
			return inserted, fmt.Errorf("检查模型定价 %s 失败: %w", pricing.ModelName, err)
		}
		if existing != nil {
			continue
		}
		if _, err := a.modelPricingService.CreatePricing(ctx, pricing); err != nil {
			return inserted, fmt.Errorf("创建模型定价 %s 失败: %w", pricing.ModelName, err)
		}
		inserted++
	}
	return inserted, nil
}
