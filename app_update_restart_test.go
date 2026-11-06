package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sinuo/internal/middleware"
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

func TestAppUpdate_SharedRequestDrain(t *testing.T) {
	app, manager, _, restarts := readyAppUpdate(t)
	app.monitoringMiddleware = middleware.NewMonitoringMiddleware(nil)
	metrics := app.monitoringMiddleware.GetMetrics()
	activity := metrics.RequestActivity()
	manager.activeRequests = app.activeRequestCount
	metrics.RecordRequest(testMonitorRequestID(), "subscription", "", "", "GET", "/v1/responses")
	lease, _ := activity.TryStart()
	result, err := app.RestartToUpdate("")
	if err != nil || !result.NeedsConfirm || result.ActiveRequests != 1 {
		t.Fatalf("计数错误: %+v %v", result, err)
	}
	probe, accepted := activity.TryStart()
	if !accepted {
		t.Fatal("确认前关闭了接收")
	}
	probe.End()
	if _, err := app.RestartToUpdate("wait"); err != nil {
		t.Fatal(err)
	}
	if _, accepted := activity.TryStart(); accepted {
		t.Fatal("等待重启没有阻止新请求")
	}
	if err := app.CancelAppUpdateRestart(); err != nil {
		t.Fatal(err)
	}
	probe, accepted = activity.TryStart()
	if !accepted {
		t.Fatal("取消等待后没有恢复接收")
	}
	probe.End()
	if _, err := app.RestartToUpdate("wait"); err != nil {
		t.Fatal(err)
	}
	lease.End()
	waitAppUpdateState(t, manager, func(s AppUpdateStatus) bool { return s.State == "restarting" })
	deadline := time.Now().Add(time.Second)
	for restarts.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if restarts.Load() != 1 || metrics.ActiveConnectionCount() != 1 {
		t.Fatal("空闲 WS 阻止重启或未保留到最终关停")
	}
	if _, accepted := activity.TryStart(); accepted {
		t.Fatal("重启前放开了接收")
	}
}

func TestAppUpdate_DrainFailuresRestoreAdmission(t *testing.T) {
	for _, pendingFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "restart", true: "pending-write"}[pendingFailure], func(t *testing.T) {
			app, manager, _, _ := readyAppUpdate(t)
			app.monitoringMiddleware = middleware.NewMonitoringMiddleware(nil)
			metrics := app.monitoringMiddleware.GetMetrics()
			activity := metrics.RequestActivity()
			manager.activeRequests = app.activeRequestCount
			app.tray = &appTray{app: app}
			app.tray.metrics.Store(metrics)
			lease, _ := activity.TryStart()
			if pendingFailure {
				app.settingsStore = appUpdateFailPendingStore{app.settingsStore}
			} else {
				manager.restart = func(context.Context) error {
					if !app.shouldQuit() {
						t.Error("立即更新再次触发托盘确认")
					}
					return errors.New("helper failed")
				}
			}
			if _, err := app.RestartToUpdate("now"); err == nil {
				t.Fatal("重启失败未暴露")
			}
			probe, accepted := activity.TryStart()
			if !accepted {
				t.Fatal("失败后没有恢复接收，或托盘留下了排空状态")
			}
			probe.End()
			lease.End()
		})
	}
}

func TestAppUpdate_CancelKeepsTrayDrain(t *testing.T) {
	app, manager, _, _ := readyAppUpdate(t)
	app.monitoringMiddleware = middleware.NewMonitoringMiddleware(nil)
	activity := app.requestActivity()
	manager.activeRequests = app.activeRequestCount
	lease, _ := activity.TryStart()
	activity.BeginDrain(trayDrainOwner)
	if _, err := app.RestartToUpdate("wait"); err != nil {
		t.Fatal(err)
	}
	if err := app.CancelAppUpdateRestart(); err != nil {
		t.Fatal(err)
	}
	if _, accepted := activity.TryStart(); accepted {
		t.Fatal("取消更新等待解除了托盘等待")
	}
	lease.End()
	activity.CancelDrain(trayDrainOwner)
}

func waitRestartWatchdog(t *testing.T, manager *appUpdateManager) {
	t.Helper()
	manager.checkMu.Lock()
	done := manager.restartWatchdogDone
	manager.checkMu.Unlock()
	if done == nil {
		t.Fatal("未启动重启兜底任务")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("重启兜底任务未完成")
	}
}

func TestAppUpdate_RestartWatchdogRecoversOnlyWhileRunning(t *testing.T) {
	for _, scenario := range []string{"running", "closing", "context-cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			app, manager, _, restarts := readyAppUpdate(t)
			app.monitoringMiddleware = middleware.NewMonitoringMiddleware(nil)
			activity := app.requestActivity()
			manager.activeRequests = app.activeRequestCount
			expired := make(chan time.Time)
			manager.restartWatchdogAfter = func(timeout time.Duration) <-chan time.Time {
				if timeout <= 30*time.Second {
					t.Error("兜底早于 helper 的等待上限")
				}
				return expired
			}
			var logs bytes.Buffer
			manager.logger = slog.New(slog.NewJSONHandler(&logs, nil))
			if _, err := app.RestartToUpdate(""); err != nil {
				t.Fatal(err)
			}
			if restarts.Load() != 1 || manager.status().State != "restarting" {
				t.Fatal("重启没有正常派发")
			}
			if _, accepted := activity.TryStart(); accepted {
				t.Fatal("超时前提前恢复了接收")
			}
			switch scenario {
			case "closing":
				app.closing.Store(true)
			case "context-cancelled":
				manager.cancel()
			}
			close(expired)
			waitRestartWatchdog(t, manager)
			if scenario == "running" {
				lease, accepted := activity.TryStart()
				if !accepted || app.updateRestarting.Load() {
					t.Fatal("重启超时后未恢复接收或遗留强制退出标志")
				}
				lease.End()
				if state := manager.status(); state.State != "error" || state.ErrorStage != "install" || !strings.Contains(state.Error, "已恢复接收请求") {
					t.Fatalf("兜底状态=%+v", state)
				}
				var record map[string]any
				if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &record); err != nil || record["level"] != "WARN" || !strings.Contains(fmt.Sprint(record["msg"]), "已恢复接收请求") {
					t.Fatalf("缺少恢复接收告警: %s err=%v", logs.Bytes(), err)
				}
			} else {
				if _, accepted := activity.TryStart(); accepted {
					t.Fatal("正常关停期间被恢复接收")
				}
				if !app.updateRestarting.Load() || manager.status().State != "restarting" || logs.Len() != 0 {
					t.Fatal("兜底改变了正常退出状态或产生误告警")
				}
			}
			for key, want := range map[string]string{appUpdatePendingVersionKey: "99.0.0", appUpdatePendingFromKey: Version} {
				if value, err := manager.readSetting(key); err != nil || value != want {
					t.Fatalf("兜底改动了更新回执: %s=%q err=%v", key, value, err)
				}
			}
		})
	}
}

func TestAppUpdate_RestartWatchdogSkipsShutdownAndStops(t *testing.T) {
	for _, alreadyClosing := range []bool{true, false} {
		t.Run(map[bool]string{true: "already-closing", false: "manager-stopped"}[alreadyClosing], func(t *testing.T) {
			app, manager, _, _ := readyAppUpdate(t)
			clockCreated := false
			manager.restartWatchdogAfter = func(time.Duration) <-chan time.Time {
				clockCreated = true
				return make(chan time.Time)
			}
			if alreadyClosing {
				restart := manager.restart
				manager.restart = func(ctx context.Context) error { err := restart(ctx); app.closing.Store(true); return err }
			}
			if _, err := app.RestartToUpdate(""); err != nil {
				t.Fatal(err)
			}
			if alreadyClosing {
				if clockCreated || manager.restartWatchdogDone != nil {
					t.Fatal("已关停时仍启动了重启兜底")
				}
			} else {
				manager.Stop()
				waitRestartWatchdog(t, manager)
			}
		})
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
