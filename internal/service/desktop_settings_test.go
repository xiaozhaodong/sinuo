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
	for key, want := range map[string]string{"close_to_tray": "true", "request_animation": "true", "system_notifications": "true",
		"menubar_label": "off", "tray_hint_shown": "false"} {
		row, err := svc.Get(ctx, CategoryDesktop, key)
		wantType := ValueTypeBool
		if key == "menubar_label" {
			wantType = ValueTypeString
		}
		if err != nil || row == nil || row.Value != want || row.ValueType != wantType || row.RequiresRestart {
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
	if err != nil || len(rows) != 5 {
		t.Fatalf("桌面设置应为关窗驻留、请求动画、系统通知、菜单栏文字与提示标记五项: %v %v", rows, err)
	}
}

func TestDesktopSettings_VisibleOnlyWhereSupported(t *testing.T) {
	for _, goos := range []string{"darwin", "windows", "linux"} {
		if !SettingVisible(CategoryDesktop, "close_to_tray", goos) || SettingVisible(CategoryDesktop, "tray_hint_shown", goos) {
			t.Fatalf("%s: 关窗驻留应展示，内部提示标记不展示", goos)
		}
		for _, key := range []string{"request_animation", "system_notifications", "menubar_label"} {
			if got := SettingVisible(CategoryDesktop, key, goos); got != (goos == "darwin") {
				t.Fatalf("%s: %s 只在 macOS 展示，得到 %v", goos, key, got)
			}
		}
	}
	if !SettingVisible(CategoryRequest, "eof_retry_hint", "linux") {
		t.Fatal("其他分类不受桌面过滤影响")
	}
}
