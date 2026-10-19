package tray

import "testing"

func TestBuild_AppUpdateNavigation(t *testing.T) {
	snapshot := runningSnapshot()
	if hasItem(Build(snapshot), "有新版本") {
		t.Fatal("没有更新版本时显示了菜单")
	}
	snapshot.UpdateVersion = "6.1.0"
	items := Build(snapshot)
	item := menuItem(t, items, "有新版本 6.1.0…")
	if item.Action.Kind != "navigate" || item.Action.Target != "settings-update" {
		t.Fatalf("action=%+v", item.Action)
	}
	if items[len(items)-2].Label != item.Label || items[len(items)-1].Action.Kind != "quit" {
		t.Fatal("更新项未在退出前")
	}
}
