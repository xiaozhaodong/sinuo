package main

import (
	"context"
	"fmt"

	"sinuo/internal/store"
)

// gpt56ModelPricingDefaults 返回 OpenAI GPT-5.6 标准层定价（USD / 1M tokens）。
// 两个历史字段保留原有基础价展示，Responses 写入按版本行的 30m / 12h 单价结算。
func gpt56ModelPricingDefaults() []*store.ModelPricingRecord {
	return []*store.ModelPricingRecord{
		{
			RateSource:           store.RateSourceOfficial,
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
			RateSource:           store.RateSourceOfficial,
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
			RateSource:           store.RateSourceOfficial,
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
			RateSource:           store.RateSourceOfficial,
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

func gpt56RateSheet(p *store.ModelPricingRecord) store.RateSheet {
	write30m, write12h := 1.25*p.InputPrice, 2.5*p.InputPrice
	return store.RateSheet{Rows: []store.RateRow{{
		Input: &p.InputPrice, Output: &p.OutputPrice, CacheRead: &p.CacheReadPrice,
		Cache5m: &p.CacheCreationPrice5m, Cache1h: &p.CacheCreationPrice1h, Cache30m: &write30m, Cache12h: &write12h,
	}}}
}

func (a *App) ensureGPT56ModelPricing(ctx context.Context) (int, error) {
	if a == nil || a.modelPricingStore == nil {
		return 0, fmt.Errorf("模型定价服务未启用")
	}
	inserted := 0
	for _, p := range gpt56ModelPricingDefaults() {
		created, err := a.modelPricingStore.SeedResponsesRateSheet(ctx, p, gpt56RateSheet(p))
		if err != nil {
			return inserted, fmt.Errorf("初始化模型价表 %s: %w", p.ModelName, err)
		}
		if created {
			inserted++
		}
	}
	return inserted, nil
}
