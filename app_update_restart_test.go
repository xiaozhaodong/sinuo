package main

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sinuo/internal/store"
)

func readyAppUpdate(t *testing.T) (*App, *appUpdateManager, *atomic.Int64, *atomic.Int32) {
	t.Helper()
	app, m := prepareAppUpdateDownload(t)
	m.update(func(s *AppUpdateStatus) { s.State = "ready"; s.ReadyVersion = "99.0.0" })
	active := new(atomic.Int64)
	restarts := new(atomic.Int32)
	m.activeRequests = active.Load
	m.restart = func(context.Context) error {
		if !app.updateRestarting.Load() {
			t.Error("restart 前没有设置更新关停标志")
		}
		for key, want := range map[string]string{appUpdatePendingVersionKey: "99.0.0", appUpdatePendingFromKey: Version} {
			if got, err := m.readSetting(key); err != nil || got != want {
				t.Errorf("pending %s=%q err=%v", key, got, err)
			}
		}
		restarts.Add(1)
		return nil
	}
	m.waitInterval = 2 * time.Millisecond
	return app, m, active, restarts
}

func TestAppUpdate_RestartConfirmationAndModes(t *testing.T) {
	for _, tc := range []struct {
		mode     string
		active   int64
		confirm  bool
		restarts int32
	}{{"", 0, false, 1}, {"", 3, true, 0}, {"now", 3, false, 1}} {
		t.Run(tc.mode+string(rune('0'+tc.active)), func(t *testing.T) {
			app, m, active, restarts := readyAppUpdate(t)
			active.Store(tc.active)
			if app.GetAppUpdateStatus().ActiveRequests != tc.active {
				t.Fatal("ready 状态未提供实时请求数")
			}
			result, err := app.RestartToUpdate(tc.mode)
			if err != nil || result.NeedsConfirm != tc.confirm || result.ActiveRequests != tc.active || restarts.Load() != tc.restarts {
				t.Fatalf("result=%+v err=%v restarts=%d", result, err, restarts.Load())
			}
			if tc.confirm && m.status().State != "ready" {
				t.Fatal("确认前改变了就绪状态")
			}
		})
	}
	app, m, _, _ := readyAppUpdate(t)
	if _, err := app.RestartToUpdate("invalid"); err == nil {
		t.Fatal("接受无效模式")
	}
	m.update(func(s *AppUpdateStatus) { s.State = "available" })
	if _, err := app.RestartToUpdate("now"); err == nil {
		t.Fatal("尚未 ready 就允许重启")
	}
}

func TestAppUpdate_WaitForIdleAndCancel(t *testing.T) {
	app, m, active, restarts := readyAppUpdate(t)
	active.Store(2)
	if _, err := app.RestartToUpdate("wait"); err != nil {
		t.Fatal(err)
	}
	if m.status().State != "waiting_idle" || restarts.Load() != 0 {
		t.Fatal("没有等待请求")
	}
	active.Store(1)
	waitAppUpdateState(t, m, func(s AppUpdateStatus) bool { return s.ActiveRequests == 1 })
	if err := app.CancelAppUpdateRestart(); err != nil {
		t.Fatal(err)
	}
	active.Store(0)
	time.Sleep(10 * time.Millisecond)
	if m.status().State != "ready" || restarts.Load() != 0 {
		t.Fatal("取消等待后仍重启")
	}
	active.Store(2)
	if _, err := app.RestartToUpdate("wait"); err != nil {
		t.Fatal(err)
	}
	active.Store(0)
	waitAppUpdateState(t, m, func(s AppUpdateStatus) bool { return s.State == "restarting" })
	deadline := time.Now().Add(time.Second)
	for restarts.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if restarts.Load() != 1 {
		t.Fatal("请求归零后没有重启")
	}
}

func TestAppUpdate_RestartFailureClearsPending(t *testing.T) {
	app, m, _, _ := readyAppUpdate(t)
	m.restart = func(context.Context) error { return errors.New("helper failed") }
	if _, err := app.RestartToUpdate("now"); err == nil {
		t.Fatal("未暴露重启失败")
	}
	if app.updateRestarting.Load() {
		t.Fatal("失败后仍使用更新关停时限")
	}
	if s := m.status(); s.State != "error" || s.ErrorStage != "install" || !s.UpdateAvailable {
		t.Fatalf("state=%+v", s)
	}
	for _, key := range []string{appUpdatePendingVersionKey, appUpdatePendingFromKey} {
		if value, err := m.readSetting(key); err != nil || value != "" {
			t.Fatalf("pending 未清除: %s=%q %v", key, value, err)
		}
	}
}

func TestAppUpdate_PendingReceipts(t *testing.T) {
	for _, tc := range []struct{ version, level, title string }{{Version, "success", "已更新到 " + Version}, {"99.0.0", "warning", "更新到 99.0.0 未完成，当前仍是 " + Version}} {
		t.Run(tc.level, func(t *testing.T) {
			_, m := updateTestManager(t)
			if err := m.writePending(tc.version, "old"); err != nil {
				t.Fatal(err)
			}
			var notices int
			m.notify = func(level, title, body string) {
				notices++
				if level != tc.level || !strings.Contains(title, tc.title) {
					t.Errorf("notice=%s %s", level, title)
				}
			}
			m.consumePendingUpdate()
			m.consumePendingUpdate()
			if notices != 1 {
				t.Fatalf("通知次数=%d", notices)
			}
			for _, key := range []string{appUpdatePendingVersionKey, appUpdatePendingFromKey} {
				if value, err := m.readSetting(key); err != nil || value != "" {
					t.Fatalf("回执未清除: %s %q %v", key, value, err)
				}
			}
		})
	}
}

type appUpdateFailPendingStore struct{ store.SettingsStore }

func (s appUpdateFailPendingStore) BatchSet(context.Context, []*store.SettingRecord) error {
	return errors.New("pending write failed")
}

func TestAppUpdate_PendingWriteFailureDoesNotRestart(t *testing.T) {
	app, manager, active, restarts := readyAppUpdate(t)
	app.settingsStore = appUpdateFailPendingStore{app.settingsStore}
	active.Store(1)
	if _, err := app.RestartToUpdate("wait"); err != nil {
		t.Fatal(err)
	}
	active.Store(0)
	waitAppUpdateState(t, manager, func(s AppUpdateStatus) bool { return s.State == "error" })
	if status := manager.status(); status.ErrorStage != "install" || status.Error == "" || restarts.Load() != 0 || app.updateRestarting.Load() {
		t.Fatalf("回执保存失败没有阻止重启或未暴露错误: %+v", status)
	}
}
