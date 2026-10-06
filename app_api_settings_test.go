package main

import (
	"runtime"
	"testing"

	"cc-forwarder/internal/service"
)

func TestSettingsList_HidesDesktopSettingsNotSupportedHere(t *testing.T) {
	app := newImageGenerationSettingsTestApp(t)
	all, err := app.GetAllSettings()
	if err != nil {
		t.Fatal(err)
	}
	byCategory, err := app.GetSettingsByCategory(service.CategoryDesktop)
	if err != nil {
		t.Fatal(err)
	}
	for name, list := range map[string][]SettingInfo{"GetAllSettings": all, "GetSettingsByCategory": byCategory} {
		listed := map[string]bool{}
		for _, item := range list {
			if item.Category == service.CategoryDesktop {
				listed[item.Key] = true
			}
		}
		if !listed["close_to_tray"] || listed["tray_hint_shown"] {
			t.Fatalf("%s: 关窗驻留应展示，内部提示标记不展示: %v", name, listed)
		}
		for _, key := range []string{"request_animation", "system_notifications", "menubar_label"} {
			if listed[key] != (runtime.GOOS == "darwin") {
				t.Fatalf("%s: %s 只在 macOS 展示（当前 %s）: %v", name, key, runtime.GOOS, listed)
			}
		}
	}
}
