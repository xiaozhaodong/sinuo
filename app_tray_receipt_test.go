package main

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"cc-forwarder/internal/tray"

	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

type trayReceiptAPIFake struct {
	trayBindingFake
	accounts     []UpstreamAccountInfo
	accountErr   error
	accountReads int
}

func (f *trayReceiptAPIFake) GetUpstreamAccounts() ([]UpstreamAccountInfo, error) {
	f.accountReads++
	return f.accounts, f.accountErr
}

func newTrayReceiptAPIFake() *trayReceiptAPIFake {
	return &trayReceiptAPIFake{
		trayBindingFake: trayBindingFake{
			claude: []ModelRouteBindingInfo{{AssignmentID: 7, Model: "claude-opus-4-1"}, {AssignmentID: 8, Kind: "web_tools"}},
			codex:  []CodexModelRouteBindingInfo{{AssignmentID: 9, Model: "gpt-5.6-luna"}},
		},
		accounts: []UpstreamAccountInfo{{ID: 2, AccountName: "duckcoding"}},
	}
}

func TestTray_ReceiptResolvesNames(t *testing.T) {
	for _, tc := range []struct {
		action          tray.Action
		subject, target string
	}{
		{tray.Action{Kind: "claude", Mode: "manual_fixed", Target: "venlacy"}, "", "venlacy"},
		{tray.Action{Kind: "claude", Mode: "auto", Target: "ignored"}, "", ""},
		{tray.Action{Kind: "claude_binding", Mode: "manual_preferred", ID: 7, Target: "venlacy"}, "claude-opus-4-1", "venlacy"},
		{tray.Action{Kind: "claude_binding", Mode: "auto", ID: 8}, "联网工具", ""},
		{tray.Action{Kind: "codex", Mode: "manual_fixed", ID: 2}, "", "duckcoding"},
		{tray.Action{Kind: "codex", Mode: "auto", ID: 2}, "", ""},
		{tray.Action{Kind: "codex_binding", Mode: "manual_preferred", ID: 9, Target: "2"}, "gpt-5.6-luna", "duckcoding"},
		{tray.Action{Kind: "codex_binding", Mode: "auto", ID: 9, Target: "2"}, "gpt-5.6-luna", ""},
	} {
		fake := newTrayReceiptAPIFake()
		got := resolveSwitchReceipt(fake, tc.action)
		want := tray.SwitchReceipt{Kind: tc.action.Kind, Mode: tc.action.Mode, ID: tc.action.ID, Subject: tc.subject, Target: tc.target}
		if got != want {
			t.Errorf("%+v: %+v，期望 %+v", tc.action, got, want)
		}
		if tc.action.Mode == "auto" && fake.accountReads != 0 {
			t.Error("自动模式不需要读取目标账号名称")
		}
	}
}

func TestTray_ReceiptNameFallbacks(t *testing.T) {
	for _, readErr := range []error{nil, errors.New("读取失败")} {
		fake := &trayReceiptAPIFake{trayBindingFake: trayBindingFake{readErr: readErr}, accountErr: readErr}
		for _, tc := range []struct {
			action          tray.Action
			subject, target string
		}{
			{tray.Action{Kind: "claude_binding", Mode: "manual_fixed", ID: 7, Target: "venlacy"}, "模型映射 #7", "venlacy"},
			{tray.Action{Kind: "codex", Mode: "manual_fixed", ID: 2}, "", "账号 #2"},
			{tray.Action{Kind: "codex_binding", Mode: "manual_fixed", ID: 9, Target: "2"}, "模型绑定 #9", "账号 #2"},
		} {
			got := resolveSwitchReceipt(fake, tc.action)
			if got.Subject != tc.subject || got.Target != tc.target {
				t.Errorf("读取错误 %v，动作 %+v，兜底 = %+v", readErr, tc.action, got)
			}
		}
	}
	fake := newTrayReceiptAPIFake()
	got := resolveSwitchReceipt(fake, tray.Action{Kind: "codex_binding", Mode: "manual_fixed", ID: 9, Target: "invalid"})
	if got.Target != "账号 #invalid" || fake.accountReads != 0 {
		t.Fatalf("无效目标不能按账号 0 查询: %+v", got)
	}
}

func TestTray_ReceiptChannelAllCombinations(t *testing.T) {
	for _, tc := range []struct {
		focused, visible, system bool
		want                     receiptChannel
	}{
		{false, false, false, receiptNone}, {false, false, true, receiptSystem},
		{false, true, false, receiptToast}, {false, true, true, receiptSystem},
		{true, false, false, receiptToast}, {true, false, true, receiptToast},
		{true, true, false, receiptToast}, {true, true, true, receiptToast},
	} {
		if got := chooseReceiptChannel(tc.focused, tc.visible, tc.system); got != tc.want {
			t.Errorf("%+v: 得到通道 %v", tc, got)
		}
	}
}

type trayReceiptDeliveryFake struct {
	closed                   atomic.Bool
	focused, visible, system bool
	onState                  func()
	channels                 []receiptChannel
	notices                  []tray.Notice
}

func (f *trayReceiptDeliveryFake) receiptClosed() bool { return f.closed.Load() }
func (f *trayReceiptDeliveryFake) receiptState() (bool, bool, bool) {
	if f.onState != nil {
		f.onState()
	}
	return f.focused, f.visible, f.system
}
func (f *trayReceiptDeliveryFake) deliverReceipt(channel receiptChannel, notice tray.Notice) {
	f.channels = append(f.channels, channel)
	f.notices = append(f.notices, notice)
}

func TestTray_ReceiptDeliveryDoesNotThrottle(t *testing.T) {
	for _, tc := range []struct {
		focused, visible, system bool
		channel                  receiptChannel
	}{
		{true, true, true, receiptToast}, {false, true, true, receiptSystem},
		{false, false, true, receiptSystem}, {false, true, false, receiptToast},
	} {
		fake := newTrayReceiptAPIFake()
		delivery := &trayReceiptDeliveryFake{focused: tc.focused, visible: tc.visible, system: tc.system}
		action := tray.Action{Kind: "codex", Mode: "manual_fixed", ID: 2}
		sendTraySwitchReceipt(fake, delivery, action)
		action.Mode = "auto"
		sendTraySwitchReceipt(fake, delivery, action)
		if len(delivery.notices) != 2 || len(delivery.channels) != 2 {
			t.Fatalf("主动切换每次都应回执: %+v", delivery.notices)
		}
		if delivery.channels[0] != tc.channel || delivery.channels[1] != tc.channel {
			t.Fatalf("只向选定通道投递: %v", delivery.channels)
		}
		if delivery.notices[0].ID != "switch:codex" || delivery.notices[1].ID != "switch:codex" ||
			delivery.notices[0].Body != "固定「duckcoding」" || delivery.notices[1].Body != "自动调度（按编排）" {
			t.Fatalf("固定 ID 应更新为最新切换内容: %+v", delivery.notices)
		}
	}
}

func TestTray_ReceiptSkipsHiddenDisabledAndClosed(t *testing.T) {
	fake := newTrayReceiptAPIFake()
	action := tray.Action{Kind: "codex", Mode: "manual_fixed", ID: 2}
	hidden := &trayReceiptDeliveryFake{}
	sendTraySwitchReceipt(fake, hidden, action)
	closed := &trayReceiptDeliveryFake{system: true}
	closed.closed.Store(true)
	sendTraySwitchReceipt(nil, closed, action) // 已关闭时连名称都不能读取。
	closing := &trayReceiptDeliveryFake{system: true}
	closing.onState = func() { closing.closed.Store(true) }
	sendTraySwitchReceipt(fake, closing, action)
	unsupported := &trayReceiptDeliveryFake{system: true}
	sendTraySwitchReceipt(nil, unsupported, tray.Action{Kind: "entry", Mode: "auto"})
	for _, delivery := range []*trayReceiptDeliveryFake{hidden, closed, closing, unsupported} {
		if len(delivery.notices) != 0 {
			t.Fatalf("不应投递: %+v", delivery.notices)
		}
	}
}

func TestTray_ReceiptSystemAvailability(t *testing.T) {
	tc := &appTray{}
	if _, _, system := tc.receiptState(); system {
		t.Fatal("没有通知服务时不可投递系统通知")
	}
	tc.notify = newTrayNotify(tc)
	if _, _, system := tc.receiptState(); system {
		t.Fatal("通知服务未启动时不可投递系统通知")
	}
	tc.notify.service.Store(&notifications.NotificationService{})
	if _, _, system := tc.receiptState(); !system {
		t.Fatal("服务可用且开关开启时应可投递")
	}
	tc.notify.setEnabled(false)
	if _, _, system := tc.receiptState(); system {
		t.Fatal("设置关闭时不可投递系统通知")
	}
}

type trayReceiptActionFunc func(tray.Action, trayConfirmFunc) error

func (f trayReceiptActionFunc) executeTrayAction(action tray.Action, confirm trayConfirmFunc) error {
	return f(action, confirm)
}

func TestTray_ReceiptOnlyAfterSuccessfulSwitch(t *testing.T) {
	for _, kind := range []string{"claude", "claude_binding", "codex", "codex_binding", "entry", "codex_entry", "entry_disable",
		"codex_entry_disable", "endpoint_cooldown", "account_cooldown", "menubar_label", "close_to_tray"} {
		for _, writeErr := range []error{nil, errors.New("写入失败")} {
			action := tray.Action{Kind: kind, Mode: "auto", ID: 7}
			sent := make(chan tray.Action, 1)
			api := trayReceiptActionFunc(func(got tray.Action, confirm trayConfirmFunc) error {
				if got != action || confirm == nil {
					t.Error("执行动作和确认函数必须原样传递")
				}
				return writeErr
			})
			err := executeTrayActionWithReceipt(api, action, func(trayConfirm) bool { return false }, func(a tray.Action) { sent <- a })
			if !errors.Is(err, writeErr) {
				t.Fatalf("必须保留写入结果: %v", err)
			}
			if writeErr == nil && tray.IsSwitchAction(kind) {
				if got := awaitTrayReceipt(t, sent); got != action {
					t.Fatalf("回执必须对应成功动作: %+v", got)
				}
			} else {
				select {
				case got := <-sent:
					t.Fatalf("失败或非切换操作不得回执: %+v", got)
				case <-time.After(10 * time.Millisecond):
				}
			}
		}
	}
}

func TestTray_ReceiptDoesNotBlockActionMutex(t *testing.T) {
	tc := &appTray{}
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer close(release)
	go func() {
		tc.actionMu.Lock()
		defer tc.actionMu.Unlock()
		api := trayReceiptActionFunc(func(tray.Action, trayConfirmFunc) error { return nil })
		_ = executeTrayActionWithReceipt(api, tray.Action{Kind: "claude", Mode: "auto"}, nil, func(tray.Action) {
			close(started)
			<-release // 模拟名称查询或发送最长 5 秒的等待。
		})
		close(finished)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("未启动回执 goroutine")
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("回执阻塞了托盘写入")
	}
	tc.actionMu.Lock()
	tc.actionMu.Unlock()
}

func awaitTrayReceipt(t *testing.T, sent <-chan tray.Action) tray.Action {
	t.Helper()
	select {
	case action := <-sent:
		return action
	case <-time.After(time.Second):
		t.Fatal("成功操作没有回执")
		return tray.Action{}
	}
}
