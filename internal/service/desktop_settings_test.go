package service

import (
	"context"
	"testing"
)

func TestDesktopSettings_DefaultsPersistenceAndMetadata(t *testing.T) {
	_, svc, _ := newCodexSubscriptionProxySettingsTestEnv(t)
	ctx := context.Background()
	if err := svc.InitDefaults(ctx); err != nil {
		t.Fatal(err)
	}
	category := svc.GetCategoryInfo(CategoryDesktop)
	if category == nil || category.Label != "桌面" {
		t.Fatal("桌面分类未注册")
	}
	for key, want := range map[string]string{"close_to_tray": "true", "tray_hint_shown": "false"} {
		row, err := svc.Get(ctx, CategoryDesktop, key)
		if err != nil || row == nil || row.Value != want || row.ValueType != ValueTypeBool || row.RequiresRestart {
			t.Fatalf("默认设置 %s 错误: %+v %v", key, row, err)
		}
	}
	if err := svc.Set(ctx, CategoryDesktop, "close_to_tray", "false"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Set(ctx, CategoryDesktop, "tray_hint_shown", "true"); err != nil {
		t.Fatal(err)
	}
	if err := svc.InitDefaults(ctx); err != nil {
		t.Fatal(err)
	}
	if svc.GetBool(ctx, CategoryDesktop, "close_to_tray", true) || !svc.GetBool(ctx, CategoryDesktop, "tray_hint_shown", false) {
		t.Fatal("重启初始化不能覆盖已保存设置")
	}
	rows, err := svc.GetByCategory(ctx, CategoryDesktop)
	if err != nil || len(rows) != 2 {
		t.Fatalf("第一期只能注册两个桌面设置: %v %v", rows, err)
	}
}
