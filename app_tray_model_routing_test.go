package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"cc-forwarder/internal/service"
	"cc-forwarder/internal/store"
	"cc-forwarder/internal/tray"
)

type trayModelRoutingFake struct {
	trayBindingFake
	settings                               ModelRoutingSettingsInfo
	settingsErr, settingsWriteErr          error
	settingsReads, claudeReads, codexReads int
	updates                                []UpdateModelRoutingSettingsInput
}

func (f *trayModelRoutingFake) GetModelRoutingSettings() (ModelRoutingSettingsInfo, error) {
	f.settingsReads++
	return f.settings, f.settingsErr
}

func (f *trayModelRoutingFake) UpdateModelRoutingSettings(input UpdateModelRoutingSettingsInput) (ModelRoutingSettingsInfo, error) {
	f.updates = append(f.updates, input)
	if f.settingsWriteErr != nil {
		return ModelRoutingSettingsInfo{}, f.settingsWriteErr
	}
	if input.ClaudeEnabled != nil {
		f.settings.ClaudeEnabled = *input.ClaudeEnabled
	}
	if input.CodexEnabled != nil {
		f.settings.CodexEnabled = *input.CodexEnabled
	}
	return f.settings, nil
}

func (f *trayModelRoutingFake) ListModelRouteBindings() ([]ModelRouteBindingInfo, error) {
	f.claudeReads++
	return f.trayBindingFake.ListModelRouteBindings()
}

func (f *trayModelRoutingFake) ListCodexModelRouteBindings() ([]CodexModelRouteBindingInfo, error) {
	f.codexReads++
	return f.trayBindingFake.ListCodexModelRouteBindings()
}

func TestTray_ModelRoutingRejectsInvalidAction(t *testing.T) {
	for _, action := range []tray.Action{
		{Target: "unknown", Mode: "on"}, {Target: "", Mode: "off"},
		{Target: "claude", Mode: "auto"}, {Target: "codex", Mode: ""},
	} {
		fake, ask := &trayModelRoutingFake{}, &confirmRecorder{answer: true}
		if err := setTrayModelRouting(fake, action, ask.confirm); err == nil {
			t.Fatalf("非法目标或状态必须报错: %+v", action)
		}
		if fake.settingsReads != 0 || len(fake.updates) != 0 || len(ask.asked) != 0 {
			t.Fatal("非法操作不得读取或写入设置、弹出确认")
		}
	}
}

func TestTray_ModelRoutingSkipsStaleTarget(t *testing.T) {
	for _, target := range []string{"claude", "codex"} {
		for _, enabled := range []bool{false, true} {
			fake, ask := &trayModelRoutingFake{}, &confirmRecorder{answer: true}
			fake.settings.ClaudeEnabled, fake.settings.CodexEnabled = enabled, !enabled
			if target == "codex" {
				fake.settings.ClaudeEnabled, fake.settings.CodexEnabled = !enabled, enabled
			}
			mode := "off"
			if enabled {
				mode = "on"
			}
			err := setTrayModelRouting(fake, tray.Action{Target: target, Mode: mode}, ask.confirm)
			if !errors.Is(err, errTrayActionSkipped) || fake.settingsReads != 1 || len(fake.updates) != 0 ||
				fake.claudeReads+fake.codexReads != 0 || len(ask.asked) != 0 {
				t.Fatalf("已是目标状态时只读设置，不应重复取反、确认或写入: err=%v fake=%+v", err, fake)
			}
		}
	}
}

func TestTray_ModelRoutingEnabledMappingsWriteOnlyTarget(t *testing.T) {
	for _, target := range []string{"claude", "codex"} {
		fake, ask := &trayModelRoutingFake{}, &confirmRecorder{}
		fake.claude = []ModelRouteBindingInfo{{Enabled: false}, {Enabled: target == "claude"}}
		fake.codex = []CodexModelRouteBindingInfo{{Enabled: false}, {Enabled: target == "codex"}}
		fake.settings.ClaudeEnabled, fake.settings.CodexEnabled = target != "claude", target != "codex"
		before := fake.settings
		if err := setTrayModelRouting(fake, tray.Action{Target: target, Mode: "on"}, ask.confirm); err != nil {
			t.Fatal(err)
		}
		assertTrayRoutingUpdate(t, fake, target, true, before)
		if len(ask.asked) != 0 || fake.claudeReads != boolCount(target == "claude") || fake.codexReads != boolCount(target == "codex") {
			t.Fatalf("有启用映射时只读取对应列表，直接写入: %+v asked=%+v", fake, ask.asked)
		}
	}
}

func boolCount(value bool) int {
	if value {
		return 1
	}
	return 0
}

func assertTrayRoutingUpdate(t *testing.T, fake *trayModelRoutingFake, target string, enabled bool, before ModelRoutingSettingsInfo) {
	t.Helper()
	if len(fake.updates) != 1 {
		t.Fatalf("应只写入一次: %+v", fake.updates)
	}
	input, value, other := fake.updates[0], fake.updates[0].ClaudeEnabled, fake.updates[0].CodexEnabled
	if target == "codex" {
		value, other = input.CodexEnabled, input.ClaudeEnabled
		before.CodexEnabled = enabled
	} else {
		before.ClaudeEnabled = enabled
	}
	if value == nil || *value != enabled || other != nil || fake.settings != before {
		t.Fatalf("只提交目标字段，另一套开关不得被覆盖: input=%+v settings=%+v want=%+v", input, fake.settings, before)
	}
}

func TestTray_ModelRoutingEmptyEnableConfirms(t *testing.T) {
	for _, tc := range []struct {
		target, title, message string
		disabled               int
	}{
		{"claude", "开启模型路由", "还没有任何模型映射，开启后所有 Claude 请求都会返回未分配错误。仍要开启吗？", 0},
		{"claude", "开启模型路由", "当前 2 条映射全部处于停用状态，开启后所有 Claude 请求都会返回未分配错误。仍要开启吗？", 2},
		{"codex", "开启 Codex 模型路由", "当前没有启用中的 Codex 模型路由，开启后未绑定模型会返回 422。仍要开启吗？", 0},
		{"codex", "开启 Codex 模型路由", "当前没有启用中的 Codex 模型路由，开启后未绑定模型会返回 422。仍要开启吗？", 2},
	} {
		for _, answer := range []bool{false, true} {
			fake, ask := &trayModelRoutingFake{}, &confirmRecorder{answer: answer}
			fake.claude, fake.codex = make([]ModelRouteBindingInfo, tc.disabled), make([]CodexModelRouteBindingInfo, tc.disabled)
			fake.settings.ClaudeEnabled, fake.settings.CodexEnabled = tc.target != "claude", tc.target != "codex"
			before := fake.settings
			err := setTrayModelRouting(fake, tray.Action{Target: tc.target, Mode: "on"}, ask.confirm)
			if len(ask.asked) != 1 || ask.asked[0] != (trayConfirm{Title: tc.title, Message: tc.message, Confirm: "仍要开启", Destructive: true}) {
				t.Fatalf("确认文案须与页面一致、默认取消: %+v", ask.asked)
			}
			if !answer {
				if !errors.Is(err, errTrayActionSkipped) || len(fake.updates) != 0 || fake.settings != before {
					t.Fatalf("取消应跳过且不写入: err=%v fake=%+v", err, fake)
				}
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			assertTrayRoutingUpdate(t, fake, tc.target, true, before)
		}
	}
}

func TestTray_ModelRoutingDisableDoesNotConfirmOrReadMappings(t *testing.T) {
	for _, target := range []string{"claude", "codex"} {
		fake, ask := &trayModelRoutingFake{}, &confirmRecorder{}
		fake.settings.ClaudeEnabled, fake.settings.CodexEnabled = true, true
		fake.readErr = errors.New("关闭时不应读取映射")
		before := fake.settings
		if err := setTrayModelRouting(fake, tray.Action{Target: target, Mode: "off"}, ask.confirm); err != nil {
			t.Fatal(err)
		}
		assertTrayRoutingUpdate(t, fake, target, false, before)
		if len(ask.asked) != 0 || fake.claudeReads+fake.codexReads != 0 {
			t.Fatal("关闭不需要确认或读取映射")
		}
	}
}

func TestTray_ModelRoutingPropagatesReadAndWriteErrors(t *testing.T) {
	wantErr := errors.New("读取或写入失败")
	for _, target := range []string{"claude", "codex"} {
		for _, stage := range []string{"settings", "bindings", "write"} {
			fake, ask := &trayModelRoutingFake{}, &confirmRecorder{answer: true}
			switch stage {
			case "settings":
				fake.settingsErr = wantErr
			case "bindings":
				fake.readErr = wantErr
			case "write":
				fake.settingsWriteErr = wantErr
			}
			err := setTrayModelRouting(fake, tray.Action{Target: target, Mode: "on"}, ask.confirm)
			if !errors.Is(err, wantErr) || len(fake.updates) != boolCount(stage == "write") ||
				len(ask.asked) != boolCount(stage == "write") {
				t.Fatalf("%s/%s: 应保留错误，读取失败不得继续确认或写入: err=%v fake=%+v", target, stage, err, fake)
			}
		}
	}
}

func TestTray_ModelRoutingDispatchCallsExistingAPI(t *testing.T) {
	err := NewApp().executeTrayAction(tray.Action{Kind: "model_routing", Target: "claude", Mode: "on"}, nil)
	if err == nil || err.Error() != "模型路由服务未初始化" {
		t.Fatalf("开关必须接入既有 API，保留未就绪错误: %v", err)
	}
}

func TestTray_ModelRoutingSkippedReceiptReturnsNil(t *testing.T) {
	for _, skip := range []error{errTrayActionSkipped, fmt.Errorf("包装: %w", errTrayActionSkipped)} {
		api := trayReceiptActionFunc(func(tray.Action, trayConfirmFunc) error { return skip })
		sent := make(chan tray.Action, 1)
		err := executeTrayActionWithReceipt(api, tray.Action{Kind: "model_routing", Target: "claude", Mode: "on"}, nil,
			func(action tray.Action) { sent <- action })
		if err != nil {
			t.Fatalf("跳过对 runAction 应表现为成功，以便继续读回 checkbox: %v", err)
		}
		select {
		case got := <-sent:
			t.Fatalf("跳过不得回执: %+v", got)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestTray_ModelRoutingReceiptAfterSuccessfulSwitch(t *testing.T) {
	for _, target := range []string{"claude", "codex"} {
		for _, mode := range []string{"on", "off"} {
			fake, ask := &trayModelRoutingFake{}, &confirmRecorder{answer: true}
			fake.settings.ClaudeEnabled, fake.settings.CodexEnabled = mode == "off", mode == "off"
			action := tray.Action{Kind: "model_routing", Target: target, Mode: mode}
			api := trayReceiptActionFunc(func(got tray.Action, confirm trayConfirmFunc) error {
				return setTrayModelRouting(fake, got, confirm)
			})
			sent := make(chan tray.Action, 1)
			if err := executeTrayActionWithReceipt(api, action, ask.confirm, func(got tray.Action) { sent <- got }); err != nil {
				t.Fatal(err)
			}
			if got := awaitTrayReceipt(t, sent); got != action || len(fake.updates) != 1 {
				t.Fatalf("成功开关必须回执原始动作: %+v", got)
			}
			delivery := &trayReceiptDeliveryFake{focused: true}
			sendTraySwitchReceipt(nil, delivery, action) // 开关回执不需要读取名称。
			want, _ := tray.ReceiptNotice(tray.SwitchReceipt{Kind: action.Kind, Target: target, Mode: mode})
			if len(delivery.notices) != 1 || delivery.notices[0] != want {
				t.Fatalf("应投递对应开关回执: %+v", delivery.notices)
			}
		}
	}
}

func TestTray_ModelRoutingCancelAndStaleDoNotSendReceipt(t *testing.T) {
	for _, target := range []string{"claude", "codex"} {
		for _, current := range []bool{false, true} {
			fake, ask := &trayModelRoutingFake{}, &confirmRecorder{}
			fake.settings.ClaudeEnabled, fake.settings.CodexEnabled = current, current
			api := trayReceiptActionFunc(func(action tray.Action, confirm trayConfirmFunc) error {
				return setTrayModelRouting(fake, action, confirm)
			})
			sent := make(chan tray.Action, 1)
			err := executeTrayActionWithReceipt(api, tray.Action{Kind: "model_routing", Target: target, Mode: "on"}, ask.confirm,
				func(action tray.Action) { sent <- action })
			if err != nil || len(fake.updates) != 0 || len(ask.asked) != boolCount(!current) {
				t.Fatalf("取消或过期菜单只跳过，不弹错误: err=%v fake=%+v", err, fake)
			}
			select {
			case got := <-sent:
				t.Fatalf("取消或过期菜单不得回执: %+v", got)
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
}

func TestTray_ModelRoutingFailureDoesNotSendReceipt(t *testing.T) {
	for _, target := range []string{"claude", "codex"} {
		fake := &trayModelRoutingFake{settingsWriteErr: errors.New("保存失败")}
		api := trayReceiptActionFunc(func(action tray.Action, confirm trayConfirmFunc) error {
			return setTrayModelRouting(fake, action, confirm)
		})
		sent := make(chan tray.Action, 1)
		err := executeTrayActionWithReceipt(api, tray.Action{Kind: "model_routing", Target: target, Mode: "on"},
			(&confirmRecorder{answer: true}).confirm, func(action tray.Action) { sent <- action })
		if !errors.Is(err, fake.settingsWriteErr) {
			t.Fatalf("失败仍要交给 runAction 弹错误: %v", err)
		}
		select {
		case got := <-sent:
			t.Fatalf("写入失败不得回执: %+v", got)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestTray_ModelRoutingSnapshotTracksCodexAvailability(t *testing.T) {
	for _, available := range []bool{false, true} {
		app := newTrayModelRoutingApp(t, available)
		s := tray.Snapshot{}
		(&appTray{app: app}).readModelRouting(&s)
		if s.ClaudeError != "" || s.CodexError != "" || !s.ClaudeModels ||
			s.CodexModels != available || s.CodexRoutingAvailable != available {
			t.Fatalf("快照须承接实际开关与 Codex 服务可用状态: %+v", s)
		}
	}
	s := tray.Snapshot{}
	(&appTray{app: NewApp()}).readModelRouting(&s)
	if s.ClaudeError == "" || s.CodexError == "" || s.CodexRoutingAvailable {
		t.Fatalf("读取失败不得伪造可用状态: %+v", s)
	}
}

func newTrayModelRoutingApp(t *testing.T, codexAvailable bool) *App {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	app := NewApp()
	app.modelRoutingStore = store.NewSQLiteModelRoutingStore(db)
	app.modelRoutingService = service.NewModelRoutingService(app.modelRoutingStore)
	on := true
	if _, err := app.modelRoutingService.UpdateSettings(context.Background(), store.ModelRoutingSettingsPatch{ClaudeEnabled: &on, CodexEnabled: &on}); err != nil {
		t.Fatal(err)
	}
	if codexAvailable {
		app.codexModelRoutingService = service.NewCodexModelRoutingService(app.modelRoutingStore, nil)
		if err := app.codexModelRoutingService.Initialize(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	return app
}
