package main

import (
	"context"
	"fmt"
	"strconv"

	"sinuo/internal/service"
)

func (a *App) applyRetentionSettings(ctx context.Context) {
	retentionDays := a.settingsService.GetInt(ctx, service.CategoryRetention, "retention_days", 0)
	if retentionDays < 0 {
		retentionDays = 0
	}
	a.config.UsageTracking.RetentionDays = retentionDays
	if a.usageTracker != nil {
		a.usageTracker.SetRetentionDays(retentionDays)
	}
}

func validateRetentionSettingUpdates(updates []UpdateSettingInput) error {
	for _, update := range updates {
		if update.Category != service.CategoryRetention || update.Key != "retention_days" {
			continue
		}
		days, err := strconv.Atoi(update.Value)
		if err != nil || days < 0 {
			return fmt.Errorf("数据保留天数必须是大于或等于 0 的整数")
		}
	}
	return nil
}
