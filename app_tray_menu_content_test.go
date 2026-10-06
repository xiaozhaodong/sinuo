package main

import (
	"testing"
	"time"

	"cc-forwarder/config"
	timezonepolicy "cc-forwarder/internal/timezone"
	"cc-forwarder/internal/tracking"
	"cc-forwarder/internal/tray"
)

func TestTray_LocalURLUsesLoopbackForWildcardHosts(t *testing.T) {
	for host, want := range map[string]string{
		"":          "http://127.0.0.1:8088",
		"0.0.0.0":   "http://127.0.0.1:8088",
		"::":        "http://127.0.0.1:8088",
		"[::]":      "http://127.0.0.1:8088",
		"127.0.0.1": "http://127.0.0.1:8088",
		"localhost": "http://localhost:8088",
		"::1":       "http://[::1]:8088",
		"[::1]":     "http://[::1]:8088",
	} {
		if got := trayLocalURL(host, 8088); got != want {
			t.Errorf("trayLocalURL(%q) = %q，期望 %q", host, got, want)
		}
	}
}

func TestTray_TimeParsesAPITimes(t *testing.T) {
	at := time.Date(2026, 10, 6, 4, 5, 6, 789000000, time.UTC)
	if got := trayTime(formatAPITime(at)); !got.Equal(at) {
		t.Fatalf("应能解析 API 时间文本: %v", got)
	}
	if !trayTime("").IsZero() || !trayTime("not-a-time").IsZero() {
		t.Fatal("空值与无法解析的文本应返回零值")
	}
}

// 解除冷却只转调现有写入方法：服务未就绪时返回它们自己的错误，而不是「未知托盘操作」。
func TestTray_CooldownActionsCallExistingWriters(t *testing.T) {
	a := NewApp()
	for action, want := range map[tray.Action]string{
		{Kind: "endpoint_cooldown", Target: "relay"}: "端点管理器或运行态存储未初始化",
		{Kind: "account_cooldown", ID: 3}:            "账号池服务未启用",
	} {
		if err := a.executeTrayAction(action, nil); err == nil || err.Error() != want {
			t.Errorf("%s: err = %v，期望 %q", action.Kind, err, want)
		}
	}
}

func TestTray_TodayFailedCountsOnlyTodayFailures(t *testing.T) {
	core, err := tracking.OpenCoreDatabase(tracking.DatabaseConfig{Type: "sqlite", DatabasePath: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.Close() })
	if err := core.InitSchema(); err != nil {
		t.Fatal(err)
	}
	a := NewApp()
	a.coreDatabase, a.config = core, &config.Config{Timezone: "UTC"}
	policy, err := timezonepolicy.New("UTC")
	if err != nil {
		t.Fatal(err)
	}
	start, _, err := policy.DayRange(policy.BusinessDate(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	rows := []struct {
		id, status string
		at         time.Time
	}{
		{"req-today-1", "failed", start.Add(time.Second)},
		{"req-today-2", "failed", start.Add(time.Minute)},
		{"req-today-ok", "completed", start.Add(time.Second)},
		{"req-today-cancel", "cancelled", start.Add(time.Second)},
		{"req-yesterday", "failed", start.Add(-time.Second)},
	}
	for _, row := range rows {
		if _, err := core.DB().Exec(`INSERT INTO request_logs (request_id, start_time, status) VALUES (?, ?, ?)`,
			row.id, timezonepolicy.FormatStorage(row.at), row.status); err != nil {
			t.Fatal(err)
		}
	}
	if failed, err := a.trayTodayFailed(); err != nil || failed != 2 {
		t.Fatalf("只计今日 failed: failed=%d err=%v", failed, err)
	}
	if failed, err := NewApp().trayTodayFailed(); err != nil || failed != 0 {
		t.Fatalf("数据库未就绪时按 0 处理: failed=%d err=%v", failed, err)
	}
}
