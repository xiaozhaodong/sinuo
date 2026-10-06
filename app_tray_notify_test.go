package main

import (
	"errors"
	"testing"
	"time"

	"cc-forwarder/internal/monitor"
	"cc-forwarder/internal/proxy"
	"cc-forwarder/internal/tray"

	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

func TestTray_FailoverNoticeMatchesToastWording(t *testing.T) {
	claude := proxy.FailoverEvent{Lane: proxy.FailoverLaneCC, From: "a", To: "b", ReasonCode: proxy.FailoverReasonRateLimited,
		ReasonDetail: "Retry-After 30s"}
	want := tray.Notice{ID: "failover:cc|a|b|rate_limited", Title: "Claude 请求临时改用备用端点",
		Body: "改用「b」（原端点「a」）：触发限流", Target: "requests"}
	if got := failoverNotice(claude); got != want {
		t.Fatalf("CC 端点只说本次请求临时改用，原因只取标签: %+v", got)
	}
	if got := failoverLogText(claude); got != "Claude 临时改用 b（原 a · 触发限流）" {
		t.Fatalf("最近切换文案: %q", got)
	}
	codex := proxy.FailoverEvent{Lane: proxy.FailoverLaneCodex, From: "x", To: "y", ReasonCode: "custom", ReasonLabel: "额度已用尽"}
	if got := failoverNotice(codex); got.Title != "Codex 账号已切换" || got.Body != "「x」→「y」：额度已用尽" {
		t.Fatalf("Codex 账号切换: %+v", got)
	}
	if got := failoverLogText(codex); got != "Codex x → y（额度已用尽）" {
		t.Fatalf("最近切换文案: %q", got)
	}
}

func TestTray_RecordFailoverMergesAndSignalsRefresh(t *testing.T) {
	tc := &appTray{app: NewApp(), refresh: make(chan struct{}, 1)}
	event := proxy.FailoverEvent{Lane: proxy.FailoverLaneCC, From: "a", To: "b", ReasonCode: proxy.FailoverReasonServerError}
	tc.recordFailover(event)
	tc.recordFailover(event)
	if len(tc.failovers) != 1 || tc.failovers[0].Count != 2 {
		t.Fatalf("同类切换合并计数: %+v", tc.failovers)
	}
	select {
	case <-tc.refresh:
	default:
		t.Fatal("记录后应合并重建菜单")
	}
}

func TestTray_ClaudeQuotaShortPicksTightestValidWindow(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	used := func(v float64) *float64 { return &v }
	quota := func(fiveHour, sevenDay ClaudeSubscriptionQuotaWindowInfo) *ClaudeSubscriptionRateLimitInfo {
		return &ClaudeSubscriptionRateLimitInfo{Headers: &ClaudeSubscriptionHeaderQuotaInfo{CapturedAt: now.Format(time.RFC3339),
			FiveHour: fiveHour, SevenDay: sevenDay}}
	}
	for _, tc := range []struct {
		name string
		q    *ClaudeSubscriptionRateLimitInfo
		want string
	}{
		{"没有数据", nil, ""},
		{"d7 更紧", quota(ClaudeSubscriptionQuotaWindowInfo{UsedPercent: used(10)}, ClaudeSubscriptionQuotaWindowInfo{UsedPercent: used(60)}), "d7 40%"},
		{"相同时取 5h", quota(ClaudeSubscriptionQuotaWindowInfo{UsedPercent: used(50)}, ClaudeSubscriptionQuotaWindowInfo{UsedPercent: used(50)}), "5h 50%"},
		{"已重置的窗口不参与", quota(ClaudeSubscriptionQuotaWindowInfo{UsedPercent: used(90), ResetAt: now.Add(-time.Second).Format(time.RFC3339)},
			ClaudeSubscriptionQuotaWindowInfo{UsedPercent: used(30)}), "d7 70%"},
		{"都没有读数", quota(ClaudeSubscriptionQuotaWindowInfo{}, ClaudeSubscriptionQuotaWindowInfo{}), ""},
	} {
		if got := trayClaudeQuotaShort(tc.q, now); got != tc.want {
			t.Errorf("%s: %q，期望 %q", tc.name, got, tc.want)
		}
	}
}

// 关闭动画时也要在一段请求结束后补算：等用量落库，再让统计失效并重建菜单；两次补算至少间隔 healthRefreshInterval。
func TestTray_RequestEndSettlesEvenWithoutAnimation(t *testing.T) {
	metrics := monitor.NewMetrics()
	tc := &appTray{app: NewApp(), refresh: make(chan struct{}, 1), icon: newTrayIcon(&fakeIconSetter{})}
	tc.icon.motion = func() tray.Motion { return tray.MotionOff }
	tc.metrics.Store(metrics)
	start := time.Date(2026, 10, 6, 1, 0, 0, 0, time.UTC)

	id := metrics.RecordRequest("test", "", "", "POST", "/v1/messages")
	tc.stepIcon(start)
	metrics.RecordResponse(id, 200, time.Millisecond, 0, "test")
	ended := start.Add(time.Second)
	if wait := tc.stepIcon(ended); wait != traySettleDelay {
		t.Fatalf("请求结束后应等用量落库再补算: wait=%v", wait)
	}
	select {
	case <-tc.refresh:
		t.Fatal("还没到补算时刻")
	default:
	}
	if wait := tc.stepIcon(ended.Add(traySettleDelay)); wait != 0 || !tc.statsStale.Load() {
		t.Fatalf("到点应让统计失效: wait=%v stale=%v", wait, tc.statsStale.Load())
	}
	select {
	case <-tc.refresh:
	default:
		t.Fatal("到点应重建菜单")
	}

	settled := ended.Add(traySettleDelay)
	id = metrics.RecordRequest("test", "", "", "POST", "/v1/messages")
	tc.stepIcon(settled.Add(time.Second))
	metrics.RecordResponse(id, 200, time.Millisecond, 0, "test")
	if wait := tc.stepIcon(settled.Add(2 * time.Second)); wait != healthRefreshInterval-2*time.Second {
		t.Fatalf("距上次补算不足 %v 时推迟到满间隔: wait=%v", healthRefreshInterval, wait)
	}
}

func TestTray_MenuBarLabelActionRejectsUnknownMode(t *testing.T) {
	a := NewApp()
	if err := a.executeTrayAction(tray.Action{Kind: "menubar_label", Mode: "bogus"}, nil); err == nil || err.Error() != "未知的菜单栏文字模式: bogus" {
		t.Fatalf("未知模式不得写入: %v", err)
	}
	if err := a.executeTrayAction(tray.Action{Kind: "menubar_label", Mode: tray.MenuBarCost}, nil); err == nil || err.Error() != "设置服务未启用" {
		t.Fatalf("合法模式转调 UpdateSetting: %v", err)
	}
}

func TestTray_ProblemsComparableSkipsIncompleteSnapshots(t *testing.T) {
	running := tray.Snapshot{Running: true}
	if !problemsComparable(running) {
		t.Fatal("代理运行且两侧都读到时比较")
	}
	for name, edit := range map[string]func(*tray.Snapshot){
		"代理未运行":       func(s *tray.Snapshot) { s.Running = false },
		"迁移恢复":        func(s *tray.Snapshot) { s.Recovery = true },
		"正在启动":        func(s *tray.Snapshot) { s.StartupState = "正在启动" },
		"Claude 读取失败": func(s *tray.Snapshot) { s.ClaudeError = "x" },
		"Codex 读取失败":  func(s *tray.Snapshot) { s.CodexError = "x" },
	} {
		s := running
		edit(&s)
		if problemsComparable(s) {
			t.Errorf("%s: 清单不完整，不应拿来比较（否则会误报已恢复）", name)
		}
	}
}

func TestTray_NoticeClickTargetOnlyForDefaultAction(t *testing.T) {
	click := func(action string, userInfo map[string]interface{}) notifications.NotificationResult {
		return notifications.NotificationResult{Response: notifications.NotificationResponse{ActionIdentifier: action, UserInfo: userInfo}}
	}
	if target, ok := noticeClickTarget(click(notifications.DefaultActionIdentifier, map[string]interface{}{"target": "requests"})); !ok || target != "requests" {
		t.Fatalf("点击通知本身应打开对应页面: %q %v", target, ok)
	}
	if target, ok := noticeClickTarget(click(notifications.DefaultActionIdentifier, nil)); !ok || target != "" {
		t.Fatalf("没有 target 时只打开窗口: %q %v", target, ok)
	}
	// Wails 回调前已把 Apple 原始值归一成 DEFAULT_ACTION，原始值与关闭动作都不应处理
	for _, action := range []string{"com.apple.UNNotificationDefaultActionIdentifier", "com.apple.UNNotificationDismissActionIdentifier", ""} {
		if _, ok := noticeClickTarget(click(action, map[string]interface{}{"target": "requests"})); ok {
			t.Fatalf("动作 %q 不应打开窗口", action)
		}
	}
	failed := click(notifications.DefaultActionIdentifier, map[string]interface{}{"target": "requests"})
	failed.Error = errors.New("notification response error")
	if _, ok := noticeClickTarget(failed); ok {
		t.Fatal("回调出错时不应打开窗口")
	}
}
