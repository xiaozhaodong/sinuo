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

func TestBuild_AppUpdateStates(t *testing.T) {
	for state, label := range map[string]string{"available": "有新版本 6.1.0…", "downloading": "正在下载 6.1.0…", "installing": "正在下载 6.1.0…", "ready": "重启以更新到 6.1.0…", "waiting_idle": "重启以更新到 6.1.0…", "restarting": "重启以更新到 6.1.0…"} {
		t.Run(state, func(t *testing.T) {
			snapshot := runningSnapshot()
			snapshot.UpdateVersion = "6.1.0"
			snapshot.UpdateState = state
			item := menuItem(t, Build(snapshot), label)
			if item.Action.Target != "settings-update" {
				t.Fatalf("action=%+v", item.Action)
			}
		})
	}
}
