package tray

import (
	"bytes"
	"image/png"
	"testing"
)

func running() Snapshot { return Snapshot{Running: true} }

func TestEvaluateIcon(t *testing.T) {
	up := Endpoint{Name: "a", Available: true}
	cooling := Endpoint{Name: "b", Available: true, Status: "冷却中"}
	unchecked := Endpoint{Name: "c", Available: true, Status: "未检测"}
	good := Account{ID: 1, Enabled: true, HasCredential: true}
	authFailed := Account{ID: 2, Enabled: true, HasCredential: true, State: "disabled_auth"}
	coolingAccount := Account{ID: 3, Enabled: true, HasCredential: true, Cooling: true}

	cases := []struct {
		name string
		edit func(*Snapshot)
		want IconState
	}{
		{"代理未运行", func(s *Snapshot) { s.Running = false }, IconStopped},
		{"迁移恢复", func(s *Snapshot) { s.Recovery = true }, IconStopped},
		{"正在启动", func(s *Snapshot) { s.StartupState = "正在启动" }, IconStopped},
		{"没有配置端点和账号不报警", func(*Snapshot) {}, IconNormal},
		{"自动模式有可用端点", func(s *Snapshot) { s.Endpoints = []Endpoint{cooling, up} }, IconNormal},
		{"未检测按可用处理", func(s *Snapshot) { s.Endpoints = []Endpoint{unchecked} }, IconNormal},
		{"全部端点冷却", func(s *Snapshot) { s.Endpoints = []Endpoint{cooling} }, IconDegraded},
		{"硬关闭端点不算可用", func(s *Snapshot) { s.Endpoints = []Endpoint{{Name: "x", Status: ""}} }, IconDegraded},
		{"固定到冷却端点", func(s *Snapshot) {
			s.ClaudeMode, s.ClaudeTarget, s.Endpoints = "manual_fixed", "b", []Endpoint{up, cooling}
		}, IconDegraded},
		{"优选到冷却端点可回退", func(s *Snapshot) {
			s.ClaudeMode, s.ClaudeTarget, s.Endpoints = "manual_preferred", "b", []Endpoint{up, cooling}
		}, IconNormal},
		{"Claude 读取失败不判降级", func(s *Snapshot) { s.ClaudeError, s.Endpoints = "x", []Endpoint{cooling} }, IconNormal},
		{"映射固定端点不可达", func(s *Snapshot) {
			s.ClaudeModels = true
			s.ClaudeBindings = []Binding{{Name: "opus", Enabled: true, Mode: "manual_fixed", Target: "a",
				Endpoints: []Endpoint{{Name: "a", Status: "不可达"}, {Name: "b"}}}}
		}, IconDegraded},
		{"映射固定到未参与自动调度的端点仍可用", func(s *Snapshot) {
			s.ClaudeModels = true
			s.ClaudeBindings = []Binding{{Name: "opus", Enabled: true, Mode: "manual_fixed", Target: "a",
				Endpoints: []Endpoint{{Name: "a", Status: "未参与自动调度"}}}}
		}, IconNormal},
		{"映射自动但成员都冷却", func(s *Snapshot) {
			s.ClaudeModels = true
			s.ClaudeBindings = []Binding{{Name: "opus", Enabled: true, Mode: "auto",
				Endpoints: []Endpoint{{Name: "a", Status: "冷却中"}, {Name: "b", Status: "未参与自动调度"}}}}
		}, IconDegraded},
		{"停用的映射不参与", func(s *Snapshot) {
			s.ClaudeModels = true
			s.ClaudeBindings = []Binding{{Name: "opus", Mode: "auto", Endpoints: []Endpoint{{Name: "a", Status: "冷却中"}}}}
		}, IconNormal},
		{"账号池有可调度账号", func(s *Snapshot) { s.Accounts = []Account{authFailed, good} }, IconNormal},
		{"账号池全部不可调度", func(s *Snapshot) { s.Accounts = []Account{authFailed, coolingAccount} }, IconDegraded},
		{"Codex 入口订阅直通时不判定账号", func(s *Snapshot) {
			s.CodexEntryState, s.CodexEntryMode, s.Accounts = CodexEntryReady, "subscription", []Account{authFailed}
		}, IconNormal},
		{"Codex 入口状态不明时不判定账号", func(s *Snapshot) {
			s.CodexEntryState, s.Accounts = CodexEntryUnavailable, []Account{authFailed}
		}, IconNormal},
		{"Codex 入口账号池模式仍判定账号", func(s *Snapshot) {
			s.CodexEntryState, s.CodexEntryMode, s.Accounts = CodexEntryReady, "pool", []Account{authFailed}
		}, IconDegraded},
		{"Codex 入口已停用时判定账号", func(s *Snapshot) {
			s.CodexEntryState, s.CodexEntryMode, s.Accounts = CodexEntryStopped, "subscription", []Account{authFailed}
		}, IconDegraded},
		{"Codex 映射固定账号冷却", func(s *Snapshot) {
			s.CodexModels, s.Accounts = true, []Account{good, coolingAccount}
			s.CodexBindings = []Binding{{Name: "gpt", Enabled: true, Mode: "manual_fixed", TargetID: 3,
				AccountIDs: []int64{1, 3}, MemberEnabled: map[int64]bool{1: true, 3: true}}}
		}, IconDegraded},
		{"Codex 映射只剩暂停成员可用", func(s *Snapshot) {
			s.CodexModels, s.Accounts = true, []Account{good, coolingAccount}
			s.CodexBindings = []Binding{{Name: "gpt", Enabled: true, Mode: "auto",
				AccountIDs: []int64{1, 3}, MemberEnabled: map[int64]bool{1: false, 3: true}}}
		}, IconDegraded},
	}
	for _, tc := range cases {
		s := running()
		tc.edit(&s)
		got := EvaluateIcon(s)
		if got.State != tc.want {
			t.Errorf("%s: 状态 = %d，期望 %d（%s）", tc.name, got.State, tc.want, got.Reason)
		}
		if (got.State == IconDegraded) != (got.Reason != "") {
			t.Errorf("%s: 只有降级才给原因: %+v", tc.name, got)
		}
	}
}

func TestIconFrame_PlatformsAndFrames(t *testing.T) {
	if data, template := IconFrame(IconDegraded, 3, false); template || !bytes.Equal(data, NormalColor) {
		t.Fatal("非 macOS 始终使用彩色应用图标")
	}
	seen := map[string]bool{}
	for _, state := range []IconState{IconNormal, IconDegraded} {
		for frame := -1; frame < FlowFrameCount; frame++ {
			data, template := IconFrame(state, frame, true)
			img, err := png.Decode(bytes.NewReader(data))
			if err != nil || !template || img.Bounds().Dx() != 44 || img.Bounds().Dy() != 44 {
				t.Fatalf("状态 %d 帧 %d 无效: %v", state, frame, err)
			}
			seen[string(data)] = true
		}
	}
	if len(seen) != 2*(FlowFrameCount+1) {
		t.Fatalf("静态图标与每一帧都应不同，实际只有 %d 张", len(seen))
	}
	if data, _ := IconFrame(IconStopped, -1, true); !bytes.Equal(data, StoppedTemplate) {
		t.Fatal("停止态应显示空心 S")
	}
}
