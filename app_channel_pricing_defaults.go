package main

import (
	"context"
	"fmt"
	"sinuo/internal/store"
)

// 首批为厂商公开 API 默认全球区域，单价单位 USD / 百万 token，按本地发布时间生效。
// https://developers.openai.com/api/docs/pricing 、https://platform.claude.com/docs/en/about-claude/pricing
func officialModelRateSheets() map[string]store.RateSheet {
	number := func(v float64) *float64 { return &v }
	openAI := func(tier int, fast bool, input, output, read float64) store.RateRow {
		return store.RateRow{Tier: tier, Fast: fast, Input: number(input), Output: number(output), CacheRead: number(read)}
	}
	claude := func(tier int, fast bool, input, output, write5m, write1h, read float64) store.RateRow {
		return store.RateRow{Tier: tier, Fast: fast, Input: number(input), Output: number(output), Cache5m: number(write5m), Cache1h: number(write1h), CacheRead: number(read)}
	}
	sheets := map[string]store.RateSheet{
		"gpt-5.4": {ContextLimits: []int64{272000}, Rows: []store.RateRow{
			openAI(0, false, 2.5, 15, .25), openAI(1, false, 5, 22.5, .5), openAI(0, true, 5, 30, .5),
		}},
		"claude-haiku-5-5": {ContextLimits: []int64{100000}, Rows: []store.RateRow{
			claude(0, false, .1, .5, .125, .2, .01), claude(1, false, .5, 2.5, .625, 1, .05),
		}},
		"claude-opus-5-5": {Rows: []store.RateRow{
			claude(0, false, 4, 20, 5, 8, .2), claude(0, true, 8, 40, 10, 16, .4),
		}},
	}
	for _, p := range gpt56ModelPricingDefaults() {
		sheets[p.ModelName] = gpt56RateSheet(p)
	}
	return sheets
}

// GetBuiltinModelRateSheet 供已有自定义价格的模型显式采用官方价表。
func (a *App) GetBuiltinModelRateSheet(model string) (store.RateSheet, error) {
	sheet, ok := officialModelRateSheets()[model]
	if !ok {
		return store.RateSheet{}, fmt.Errorf("模型 %q 没有内置分档价表", model)
	}
	return sheet, nil
}
func (a *App) seedModelRateSheets(ctx context.Context) error {
	for model, sheet := range officialModelRateSheets() {
		if sheet.HasResponsesWritePrices() {
			continue // 由 ensureGPT56ModelPricing 原子补齐展示信息和历史版本。
		}
		if err := a.modelPricingStore.SeedRateSheet(ctx, model, sheet); err != nil {
			return fmt.Errorf("初始化 %s 价表: %w", model, err)
		}
	}
	return nil
}
