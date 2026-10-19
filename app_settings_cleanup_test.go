package main

import (
	"context"
	"log/slog"
	"runtime"
	"strings"
	"testing"
	"time"

	"sinuo/config"
	"sinuo/internal/service"
)

func TestSettingsList_OnlyReturnsRegisteredVisibleSettings(t *testing.T) {
	app := newImageGenerationSettingsTestApp(t)
	ctx := context.Background()
	for _, key := range [][2]string{
		{service.CategoryRequest, "suspend_timeout"}, {service.CategoryRequest, "unknown_setting"},
		{service.CategoryRequest, "failure_action"}, {service.CategoryRequest, "failure_time_window"},
		{service.CategoryRequest, "failure_threshold"},
		{service.CategoryClaudeRouting, "mode"}, {"codex_models", "catalog"},
		{"claude_models", "catalog"}, {"system", "last_seen_timezone"}, {"account_pool", "pinned_account_id"},
	} {
		if err := app.settingsStore.Set(ctx, key[0], key[1], "internal"); err != nil {
			t.Fatal(err)
		}
	}
	list, err := app.GetAllSettings()
	if err != nil {
		t.Fatal(err)
	}
	listed := map[[2]string]bool{}
	for _, row := range list {
		if !app.settingsService.IsRegisteredSetting(row.Category, row.Key) || service.IsProtectedSettingsCategory(row.Category) {
			t.Fatalf("设置页返回未登记或受保护行: %s.%s", row.Category, row.Key)
		}
		listed[[2]string{row.Category, row.Key}] = true
	}
	if !listed[[2]string{service.CategoryRequest, "eof_retry_hint"}] {
		t.Fatal("设置页漏掉在用设置")
	}
	rows, err := app.GetSettingsByCategory(service.CategoryRequest)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Key == "suspend_timeout" {
			return
		}
	}
	t.Fatal("按分类读取的行为不应改变")
}

func TestSettingsCleanup_PageHasNineCategoriesAndActiveControls(t *testing.T) {
	app := newImageGenerationSettingsTestApp(t)
	if categories := app.GetSettingCategories(); len(categories) != 9 {
		t.Fatalf("设置页应有 9 组，得到 %d", len(categories))
	}
	settings, err := app.GetAllSettings()
	if err != nil {
		t.Fatal(err)
	}
	wantCount := 23
	if runtime.GOOS != "darwin" {
		wantCount -= 3
	}
	if len(settings) != wantCount {
		t.Fatalf("当前平台设置页应有 %d 项，得到 %d", wantCount, len(settings))
	}
	var requestKeys []string
	for _, setting := range settings {
		if setting.Category == service.CategoryRequest {
			requestKeys = append(requestKeys, setting.Key)
		}
	}
	if len(requestKeys) != 2 || requestKeys[0] != "global_timeout" || requestKeys[1] != "eof_retry_hint" {
		t.Fatalf("请求控制应仅保留超时与流式提示: %v", requestKeys)
	}
}

func TestRetentionSettings_InvalidSaveRejectsWholeBatch(t *testing.T) {
	for _, value := range []string{"-1", "1.5", "abc", "", "999999999999999999999999"} {
		t.Run(value, func(t *testing.T) {
			app := newImageGenerationSettingsTestApp(t)
			callbacks := 0
			app.settingsService.SetOnChangeCallback(func() { callbacks++ })
			bad := UpdateSettingInput{Category: service.CategoryRetention, Key: "retention_days", Value: value}
			for name, save := range map[string]func() error{
				"single": func() error { return app.UpdateSetting(bad) },
				"batch": func() error {
					return app.BatchUpdateSettings(BatchUpdateSettingsInput{Settings: []UpdateSettingInput{
						{Category: service.CategoryRequest, Key: "eof_retry_hint", Value: "true"}, bad,
					}})
				},
			} {
				if err := save(); err == nil || !strings.Contains(err.Error(), "数据保留天数必须是大于或等于 0 的整数") {
					t.Fatalf("%s 应返回中文校验错误，得到 %v", name, err)
				}
			}
			ctx := context.Background()
			if app.settingsService.GetInt(ctx, service.CategoryRetention, "retention_days", -1) != 0 ||
				app.getSettingString(ctx, service.CategoryRequest, "eof_retry_hint", "") != "false" || callbacks != 0 {
				t.Fatal("无效保留天数不应落库或触发回调，整批其他设置也不应改写")
			}
		})
	}
}

func TestRetentionSettings_AppliesAtStartupAndOnSave(t *testing.T) {
	app := newImageGenerationSettingsTestApp(t)
	app.config = &config.Config{UsageTracking: config.UsageTrackingConfig{RetentionDays: 90, CleanupInterval: 12 * time.Hour}}
	app.logger = slog.Default()
	app.applySettingsToConfig()
	if app.config.UsageTracking.RetentionDays != 0 {
		t.Fatal("启动加载设置后应默认永久保留")
	}
	app.settingsService.SetOnChangeCallback(app.applySettingsToConfig)
	for _, value := range []string{"7", "0"} {
		input := UpdateSettingInput{Category: service.CategoryRetention, Key: "retention_days", Value: value}
		if err := app.UpdateSetting(input); err != nil {
			t.Fatal(err)
		}
		want := app.settingsService.GetInt(context.Background(), service.CategoryRetention, "retention_days", -1)
		if app.config.UsageTracking.RetentionDays != want {
			t.Fatal("保存后运行时配置未更新")
		}
		if err := app.BatchUpdateSettings(BatchUpdateSettingsInput{Settings: []UpdateSettingInput{input}}); err != nil {
			t.Fatal(err)
		}
	}
	if app.config.UsageTracking.CleanupInterval != 12*time.Hour {
		t.Fatal("清理间隔应继续使用 config.yaml 值")
	}
	if err := app.settingsStore.Set(context.Background(), service.CategoryRetention, "retention_days", "-3"); err != nil {
		t.Fatal(err)
	}
	app.applySettingsToConfig()
	if app.config.UsageTracking.RetentionDays != 0 {
		t.Fatal("旧库中的负数应按永久保留应用")
	}
}
