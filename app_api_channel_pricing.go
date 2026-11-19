package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"sinuo/internal/store"
	"sinuo/internal/tracking"
)

// PublishModelRateSheet 发布完整价表，已有请求继续引用旧版本。
func (a *App) PublishModelRateSheet(model, source string, sheet store.RateSheet) error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.modelPricingService == nil || a.modelPricingStore == nil {
		return fmt.Errorf("模型定价服务未启用")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.modelPricingStore.PublishRateSheet(ctx, model, source, sheet); err != nil {
		return err
	}
	if err := a.syncPricingToTracker(ctx); err != nil {
		return err
	}
	return a.modelPricingService.LoadCache(ctx)
}

func (a *App) SaveChannelModelRule(channel store.BillingChannel, model string, rule store.ChannelModelRule) error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.modelPricingStore == nil {
		return fmt.Errorf("模型定价服务未启用")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.modelPricingStore.SaveChannelModelRule(ctx, channel, model, rule); err != nil {
		return err
	}
	return a.syncPricingToTracker(ctx)
}

func (a *App) GetChannelModelRule(channel store.BillingChannel, model string) (store.ChannelModelRule, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.modelPricingStore == nil {
		return store.ChannelModelRule{}, fmt.Errorf("模型定价服务未启用")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return a.modelPricingStore.CurrentChannelRule(ctx, channel, model)
}

// GetChannelModelRules 返回编辑页展示的当前渠道模型规则。
func (a *App) GetChannelModelRules(channel store.BillingChannel) (map[string]store.ChannelModelRule, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.modelPricingStore == nil {
		return nil, fmt.Errorf("模型定价服务未启用")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return a.modelPricingStore.CurrentChannelRules(ctx, channel)
}

func (a *App) GetModelRateSheet(model string) (*store.RateSheet, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.modelPricingStore == nil {
		return nil, fmt.Errorf("模型定价服务未启用")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return a.modelPricingStore.CurrentRateSheet(ctx, model)
}

func (a *App) GetRequestOfficialCost(requestID string) (tracking.OfficialReferenceCost, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.usageTracker == nil {
		return tracking.OfficialReferenceCost{}, fmt.Errorf("请求计费服务未启用")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return a.usageTracker.RecalculateOfficialCost(ctx, requestID)
}

type ModelRateSheetEditor struct {
	VersionID  int64            `json:"version_id"`
	Source     string           `json:"source"`
	Structured bool             `json:"structured"`
	Sheet      store.RateSheet  `json:"sheet"`
	Builtin    *store.RateSheet `json:"builtin,omitempty"`
}

// GetModelRateSheetEditor 一次读取当前版本、来源及各档单价。旧基础价保持 NULL 与零的区别。
func (a *App) GetModelRateSheetEditor(model string) (ModelRateSheetEditor, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.modelPricingStore == nil {
		return ModelRateSheetEditor{}, fmt.Errorf("模型定价服务未启用")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, err := a.modelPricingStore.CurrentBaseRate(ctx, model)
	if err != nil {
		return ModelRateSheetEditor{}, err
	}
	result := ModelRateSheetEditor{VersionID: r.ID, Source: r.Source, Structured: r.Sheet != nil}
	if r.Sheet != nil {
		result.Sheet = *r.Sheet
	} else {
		optional := func(v sql.NullFloat64) *float64 {
			if !v.Valid {
				return nil
			}
			return &v.Float64
		}
		result.Sheet.Rows = []store.RateRow{{Input: &r.Input, Output: &r.Output, CacheRead: optional(r.CacheRead), Cache5m: optional(r.Cache5m), Cache1h: optional(r.Cache1h)}}
	}
	if builtin, ok := officialModelRateSheets()[model]; ok {
		result.Builtin = &builtin
	}
	return result, nil
}
