package service

import (
	"context"
	"errors"
	"testing"

	"sinuo/internal/store"
)

func TestSettingsInitDefaults_RemovesRetiredRowsAndPreservesActiveValues(t *testing.T) {
	_, svc, st := newCodexSubscriptionProxySettingsTestEnv(t)
	ctx := context.Background()
	retired := [][2]string{
		{"strategy", "type"}, {"strategy", "fast_test_timeout"},
		{"retry", "max_attempts"}, {"update", "enabled"},
		{CategoryRequest, "suspend_timeout"}, {CategoryRequest, "max_suspended"},
		{CategoryHealth, "check_interval"}, {CategoryRequest, "failover_cooldown"},
		{CategoryHealth, "health_path"}, {CategoryTokenCounting, "estimation_ratio"},
		{CategoryRetention, "cleanup_interval"}, {CategoryClaudeRouting, "state_model_version"},
		{CategoryRequest, "failure_time_window"}, {CategoryRequest, "failure_threshold"}, {CategoryRequest, "failure_action"},
		{"failure_tracker", "time_window"}, {"failure_tracker", "threshold"}, {"failure_tracker", "action"},
		{"failover", "default_cooldown"},
	}
	active := map[[2]string]string{
		{CategoryClaudeRouting, "mode"}: "manual_fixed", {CategoryClaudeRouting, "revision"}: "42",
		{"system", "last_seen_timezone"}: "UTC", {"account_pool", "pinned_account_id"}: "17",
		{CategoryRequest, "eof_retry_hint"}: "true", {CategoryRetention, "retention_days"}: "31",
	}
	for _, key := range retired {
		if err := st.Set(ctx, key[0], key[1], "old"); err != nil {
			t.Fatal(err)
		}
	}
	for key, value := range active {
		if err := st.Set(ctx, key[0], key[1], value); err != nil {
			t.Fatal(err)
		}
	}
	callbacks := 0
	svc.SetOnChangeCallback(func() { callbacks++ })
	for range 2 {
		if err := svc.InitDefaults(ctx); err != nil {
			t.Fatal(err)
		}
		assertSettingsCleanupRows(t, st, retired, active)
	}
	if callbacks != 0 {
		t.Fatalf("初始化清理不应触发热更新，得到 %d 次", callbacks)
	}
}

func assertSettingsCleanupRows(t *testing.T, st *store.SQLiteSettingsStore, retired [][2]string, active map[[2]string]string) {
	t.Helper()
	ctx := context.Background()
	for _, key := range retired {
		row, err := st.Get(ctx, key[0], key[1])
		if err != nil || row != nil {
			t.Fatalf("退役设置 %v 未删除: %+v, %v", key, row, err)
		}
	}
	for key, want := range active {
		row, err := st.Get(ctx, key[0], key[1])
		if err != nil || row == nil || row.Value != want {
			t.Fatalf("在用设置 %v 被改写: %+v, %v", key, row, err)
		}
	}
}

func TestSettingsClaudeRouting_GenericWritesAreProtected(t *testing.T) {
	_, svc, st := newCodexSubscriptionProxySettingsTestEnv(t)
	ctx := context.Background()
	if err := svc.InitDefaults(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Set(ctx, CategoryClaudeRouting, "mode", "manual_fixed"); err != nil {
		t.Fatal(err)
	}
	record := &store.SettingRecord{Category: CategoryClaudeRouting, Key: "mode", Value: "auto"}
	attempts := map[string]func() error{
		"Set":            func() error { return svc.Set(ctx, record.Category, record.Key, record.Value) },
		"Delete":         func() error { return svc.Delete(ctx, record.Category, record.Key) },
		"BatchSet":       func() error { return svc.BatchSet(ctx, []*store.SettingRecord{record}) },
		"UpdateAndApply": func() error { return svc.UpdateAndApply(ctx, []*store.SettingRecord{record}) },
		"ResetCategory":  func() error { return svc.ResetCategory(ctx, CategoryClaudeRouting) },
	}
	for name, attempt := range attempts {
		if err := attempt(); !errors.Is(err, ErrProtectedSettingsCategory) {
			t.Fatalf("%s 未拒绝受保护分类: %v", name, err)
		}
	}
	row, err := st.Get(ctx, CategoryClaudeRouting, "mode")
	if err != nil || row == nil || row.Value != "manual_fixed" {
		t.Fatalf("受保护设置被改写: %+v, %v", row, err)
	}
	for _, category := range svc.GetCategories() {
		if category.Name == CategoryClaudeRouting || category.Name == "strategy" || category.Name == "retry" {
			t.Fatalf("设置列表仍包含隐藏分类 %s", category.Name)
		}
	}
}

func TestSettingsRegistration_OnlyDefaultKeysAreRegistered(t *testing.T) {
	_, svc, _ := newCodexSubscriptionProxySettingsTestEnv(t)
	for _, row := range svc.GetAllDefaults() {
		if !svc.IsRegisteredSetting(row.Category, row.Key) {
			t.Fatalf("默认设置 %s.%s 未登记", row.Category, row.Key)
		}
	}
	for _, key := range [][2]string{
		{"strategy", "type"}, {"retry", "max_attempts"}, {"update", "enabled"},
		{CategoryHealth, "check_interval"}, {CategoryRequest, "failover_cooldown"},
		{CategoryHealth, "health_path"}, {CategoryTokenCounting, "estimation_ratio"},
		{CategoryRequest, "suspend_timeout"}, {CategoryRequest, "max_suspended"},
		{CategoryRequest, "failure_time_window"}, {CategoryRequest, "failure_threshold"}, {CategoryRequest, "failure_action"},
		{"failure_tracker", "threshold"}, {"failover", "default_cooldown"},
		{CategoryRetention, "cleanup_interval"}, {CategoryClaudeRouting, "state_model_version"},
		{"codex_models", "catalog"}, {"claude_models", "catalog"},
		{"system", "last_seen_timezone"}, {"account_pool", "pinned_account_id"},
	} {
		if svc.IsRegisteredSetting(key[0], key[1]) {
			t.Fatalf("退役或内部设置 %v 不应登记", key)
		}
	}
}
