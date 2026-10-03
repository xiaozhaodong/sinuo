package main

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"cc-forwarder/internal/monitor"
	"cc-forwarder/internal/tray"
)

type trayBindingFake struct {
	claude            []ModelRouteBindingInfo
	codex             []CodexModelRouteBindingInfo
	readErr, writeErr error
	claudeInput       ModelRouteBindingInput
	codexInput        CodexModelRouteBindingInput
	writes            int
}

func (f *trayBindingFake) ListModelRouteBindings() ([]ModelRouteBindingInfo, error) {
	return f.claude, f.readErr
}
func (f *trayBindingFake) ListCodexModelRouteBindings() ([]CodexModelRouteBindingInfo, error) {
	return f.codex, f.readErr
}
func (f *trayBindingFake) UpdateModelRouteBinding(_ int64, input ModelRouteBindingInput) error {
	f.claudeInput = input
	f.writes++
	return f.writeErr
}
func (f *trayBindingFake) UpdateCodexModelRouteBinding(_ int64, input CodexModelRouteBindingInput) error {
	f.codexInput = input
	f.writes++
	return f.writeErr
}

func TestTray_ClaudePatchReadsCurrentMembersAndPreservesFields(t *testing.T) {
	fake := &trayBindingFake{claude: []ModelRouteBindingInfo{{AssignmentID: 7, Kind: "web_tools", Model: "", Enabled: true,
		Endpoints: []ModelRoutingMemberInfo{{Name: "new-member"}, {Name: "second"}}, Note: "主窗口刚刚编辑", RouteMode: "auto"}}}
	action := tray.Action{Kind: "claude_binding", ID: 7, Mode: "manual_preferred", Target: "new-member"}
	if err := updateTrayClaudeBinding(fake, action); err != nil {
		t.Fatal(err)
	}
	want := ModelRouteBindingInput{Kind: "web_tools", Model: "", Endpoints: []string{"new-member", "second"},
		Enabled: true, Note: "主窗口刚刚编辑", RouteMode: "manual_preferred", RouteTarget: "new-member"}
	if fake.writes != 1 || !reflect.DeepEqual(fake.claudeInput, want) {
		t.Fatalf("必须一次写入且保留读回字段: %+v", fake)
	}
	action.Mode = "auto"
	if err := updateTrayClaudeBinding(fake, action); err != nil {
		t.Fatal(err)
	}
	if fake.claudeInput.RouteTarget != "" {
		t.Fatal("恢复自动必须清除目标")
	}
}

func TestTray_CodexPatchPreservesPausedMembers(t *testing.T) {
	fake := &trayBindingFake{codex: []CodexModelRouteBindingInfo{{AssignmentID: 5, Model: "gpt-test", Enabled: true,
		AccountIDs: []int64{2, 9}, MemberEnabled: map[int64]bool{2: false, 9: true}, Note: "保留备注"}}}
	if err := updateTrayCodexBinding(fake, tray.Action{ID: 5, Mode: "manual_fixed", Target: "2"}); err != nil {
		t.Fatal(err)
	}
	want := CodexModelRouteBindingInput{Model: "gpt-test", Enabled: true, AccountIDs: []int64{2, 9},
		MemberEnabled: map[int64]bool{2: false, 9: true}, Note: "保留备注", RouteMode: "manual_fixed", RouteTargetID: 2}
	if fake.writes != 1 || !reflect.DeepEqual(fake.codexInput, want) {
		t.Fatalf("成员开关不能丢失: %+v", fake)
	}
	if err := updateTrayCodexBinding(fake, tray.Action{ID: 5, Mode: "auto", Target: "9"}); err != nil {
		t.Fatal(err)
	}
	if fake.codexInput.RouteTargetID != 0 {
		t.Fatal("自动必须清空固定账号")
	}
}

func TestTray_PatchRejectsDeletedDisabledAndReadFailure(t *testing.T) {
	for _, fake := range []*trayBindingFake{
		{}, {claude: []ModelRouteBindingInfo{{AssignmentID: 1}}, codex: []CodexModelRouteBindingInfo{{AssignmentID: 1}}},
		{readErr: errors.New("读取失败")},
	} {
		if err := updateTrayClaudeBinding(fake, tray.Action{ID: 1, Mode: "auto"}); err == nil {
			t.Fatal("无效映射不得写入")
		}
		if err := updateTrayCodexBinding(fake, tray.Action{ID: 1, Mode: "auto"}); err == nil {
			t.Fatal("无效绑定不得写入")
		}
		if fake.writes != 0 {
			t.Fatal("读取失败/删除/停用后产生了写入")
		}
	}
	err := errors.New("routing_not_ready")
	fake := &trayBindingFake{writeErr: err, claude: []ModelRouteBindingInfo{{AssignmentID: 1, Enabled: true}}}
	if got := updateTrayClaudeBinding(fake, tray.Action{ID: 1, Mode: "auto"}); !errors.Is(got, err) {
		t.Fatalf("写入失败必须上报: %v", got)
	}
}

func TestTray_QuotaSelectsNewerWindowAndDropsExpiredReadings(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	old, newer := 10.0, 40.0
	q := &ClaudeSubscriptionRateLimitInfo{
		Headers: &ClaudeSubscriptionHeaderQuotaInfo{CapturedAt: now.Add(-time.Minute).Format(time.RFC3339),
			FiveHour: ClaudeSubscriptionQuotaWindowInfo{UsedPercent: &old}, SevenDay: ClaudeSubscriptionQuotaWindowInfo{UsedPercent: &old}},
		Usage: &ClaudeSubscriptionUsageInfo{FetchedAt: now.Format(time.RFC3339), FiveHour: ClaudeSubscriptionQuotaWindowInfo{UsedPercent: &newer}},
	}
	if got := trayClaudeQuota(q, now); got != "5h 剩 60% · d7 剩 90%" {
		t.Fatal(got)
	}
	q.Usage.FiveHour.ResetAt = now.Add(-time.Second).Format(time.RFC3339)
	if got := trayClaudeQuota(q, now); got != "5h 已重置，待更新 · d7 剩 90%" {
		t.Fatal(got)
	}
}

func TestTray_StartupSnapshotDoesNotWaitForAppLock(t *testing.T) {
	a := NewApp()
	tc := &appTray{app: a}
	a.mu.Lock()
	defer a.mu.Unlock()
	result := make(chan tray.Snapshot, 1)
	go func() { result <- tc.snapshot() }()
	select {
	case s := <-result:
		if s.Running || s.StartupState != "正在启动" {
			t.Fatalf("启动快照错误: %+v", s)
		}
	case <-time.After(time.Second):
		t.Fatal("托盘在启动锁上阻塞")
	}
}

func TestTray_AutomaticQuitRechecksNewRequest(t *testing.T) {
	a := NewApp()
	a.tray = &appTray{app: a}
	m := monitor.NewMetrics()
	a.tray.metrics.Store(m)
	a.tray.quit.Choose(tray.QuitWait, 1)
	a.tray.autoQuit.Store(true)
	id := m.RecordRequest("test", "", "", "POST", "/v1/responses")
	if a.shouldQuit() {
		t.Fatal("归零通知后新请求到达，应继续等待")
	}
	m.RecordResponse(id, 200, time.Millisecond, 0, "test")
	if !a.shouldQuit() {
		t.Fatal("请求结束后应允许退出")
	}
}

func TestTray_MenuRefreshKeepsTopologyForStatusAndChecks(t *testing.T) {
	before := []tray.Item{{Label: "进行中 1", Disabled: true}, {Label: "固定", Checkable: true, Action: tray.Action{Kind: "codex", ID: 1}}}
	after := append([]tray.Item(nil), before...)
	after[0].Label, after[1].Checked = "进行中 2", true
	if !sameTrayMenuShape(before, after) {
		t.Fatal("标签和勾选变化不应重建子菜单")
	}
	after[1].Action.ID = 2
	if sameTrayMenuShape(before, after) {
		t.Fatal("目标变化必须更新回调")
	}
}

func TestTray_RebuildTriggersExcludeStatusAndRequestChanges(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{
		{name: EventClaudeRoutingUpdate, want: true},
		{name: EventModelRoutingUpdate, want: true},
		{name: EventClaudeCodeEntryUpdate, want: true},
		{name: EventClaudeSubscriptionRateLimit, want: true},
		{name: EventEndpointUpdate, want: true},
		{name: EventCodexSubscriptionProxyStatus, want: true},
		{name: EventConfigReloaded, want: true},
		{name: EventSystemStatus, want: false},
		{name: EventRequestUpdate, want: false},
	} {
		if got := trayRebuildEvent(tc.name); got != tc.want {
			t.Errorf("trayRebuildEvent(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestTray_RequestChangesRefreshOnlyDuringQuitWait(t *testing.T) {
	metrics := monitor.NewMetrics()
	connID := metrics.RecordRequest("test", "", "", "POST", "/v1/responses")
	a := NewApp()
	trayInstance := &appTray{app: a, refresh: make(chan struct{}, 1)}
	trayInstance.metrics.Store(metrics)
	trayInstance.handleActiveConnectionChange()
	select {
	case <-trayInstance.refresh:
		t.Fatal("普通请求变化不应重建菜单")
	default:
	}

	trayInstance.quit.Request(1, false)
	trayInstance.quit.Choose(tray.QuitWait, 1)
	trayInstance.handleActiveConnectionChange()
	select {
	case <-trayInstance.refresh:
	default:
		t.Fatal("退出等待期间请求变化应刷新状态行")
	}
	metrics.RecordResponse(connID, 200, time.Millisecond, 0, "test")
}

func TestTray_FullscreenCloseOnlyUsesDeferredHideOnMac(t *testing.T) {
	if shouldLeaveTrayFullscreen("darwin", true) != true {
		t.Fatal("macOS 全屏关闭必须等待退出全屏事件")
	}
	if shouldLeaveTrayFullscreen("darwin", false) || shouldLeaveTrayFullscreen("windows", true) {
		t.Fatal("非 macOS 或非全屏不应走退出全屏分支")
	}
}
