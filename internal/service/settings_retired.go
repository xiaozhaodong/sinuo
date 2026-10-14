package service

import (
	"context"
	"fmt"
	"log/slog"
)

var retiredSettingsCategories = []string{"strategy", "retry", "update", "failure_tracker", "failover"}

var retiredSettingKeys = [][2]string{
	{CategoryHealth, "check_interval"},
	{CategoryHealth, "health_path"},
	{CategoryTokenCounting, "estimation_ratio"},
	{CategoryRequest, "failover_cooldown"},
	{CategoryRequest, "suspend_timeout"},
	{CategoryRequest, "max_suspended"},
	{CategoryRequest, "failure_time_window"},
	{CategoryRequest, "failure_threshold"},
	{CategoryRequest, "failure_action"},
	{CategoryRetention, "cleanup_interval"},
	{CategoryClaudeRouting, "state_model_version"},
}

// IsRegisteredSetting 只认可默认设置中登记的分类与键。
func (s *SettingsService) IsRegisteredSetting(category, key string) bool {
	_, registered := s.registeredSettings[[2]string{category, key}]
	return registered
}

// removeRetiredSettings 直接清理存储，不触发热更新或通用分类保护。
func (s *SettingsService) removeRetiredSettings(ctx context.Context) error {
	records, err := s.store.GetAll(ctx)
	if err != nil {
		return fmt.Errorf("读取退役设置失败: %w", err)
	}
	categoryCounts := make(map[string]int)
	keys := make(map[[2]string]bool)
	for _, record := range records {
		categoryCounts[record.Category]++
		keys[[2]string{record.Category, record.Key}] = true
	}
	removed := 0
	var names []string
	defer func() {
		if removed > 0 {
			slog.Info("✅ [设置迁移] 已清除退役设置", "removed", removed, "settings", names)
		}
	}()
	for _, category := range retiredSettingsCategories {
		if categoryCounts[category] == 0 {
			continue
		}
		if err := s.store.DeleteByCategory(ctx, category); err != nil {
			return fmt.Errorf("删除退役分类 %s 失败: %w", category, err)
		}
		removed += categoryCounts[category]
		names = append(names, category)
	}
	for _, setting := range retiredSettingKeys {
		category, key := setting[0], setting[1]
		if !keys[setting] {
			continue
		}
		if err := s.store.Delete(ctx, category, key); err != nil {
			return fmt.Errorf("删除退役设置 %s.%s 失败: %w", category, key, err)
		}
		removed++
		names = append(names, category+"."+key)
	}
	return nil
}
