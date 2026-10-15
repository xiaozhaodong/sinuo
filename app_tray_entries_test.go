package main

import (
	"errors"
	"strings"
	"testing"

	"sinuo/internal/tray"
)

type codexEntryFake struct {
	config        CodexSubscriptionProxyConfigInfo
	status        CodexSubscriptionProxyStatusInfo
	afterWrite    func(*codexEntryFake) // 模拟运行态随写入变化
	saved         *CodexSubscriptionProxyConfigInput
	savedRevision int64
	enabledCalls  []bool
}

func (f *codexEntryFake) GetCodexSubscriptionProxyConfig() (CodexSubscriptionProxyConfigInfo, error) {
	return f.config, nil
}

func (f *codexEntryFake) GetCodexSubscriptionProxyStatus() (CodexSubscriptionProxyStatusInfo, error) {
	return f.status, nil
}

func (f *codexEntryFake) SaveCodexSubscriptionProxyConfig(input CodexSubscriptionProxyConfigInput, revision int64) (CodexSubscriptionProxySaveResultInfo, error) {
	f.saved, f.savedRevision = &input, revision
	f.config.Mode, f.config.Revision = input.Mode, revision+1
	if f.afterWrite != nil {
		f.afterWrite(f)
	}
	return CodexSubscriptionProxySaveResultInfo{Config: f.config}, nil
}

func (f *codexEntryFake) SetCodexSubscriptionProxyEnabled(enabled bool, revision int64) (CodexSubscriptionProxySaveResultInfo, error) {
	f.enabledCalls, f.savedRevision = append(f.enabledCalls, enabled), revision
	f.config.Enabled, f.config.Revision = enabled, revision+1
	if f.afterWrite != nil {
		f.afterWrite(f)
	}
	return CodexSubscriptionProxySaveResultInfo{Config: f.config}, nil
}

// runtimeFollows 模拟运行态正常跟上新配置
func runtimeFollows(f *codexEntryFake) {
	f.status.Enabled, f.status.Mode, f.status.Revision = f.config.Enabled, f.config.Mode, f.config.Revision
	f.status.State = "stopped"
	if f.config.Enabled {
		f.status.State = "running"
	}
}

func readyCodexEntry() *codexEntryFake {
	f := &codexEntryFake{config: CodexSubscriptionProxyConfigInfo{Initialized: true, Enabled: true, Mode: "subscription",
		ListenHost: "127.0.0.1", ListenPort: 18089, WSCompression: "disabled", Revision: 7, AccessConfigured: true}}
	runtimeFollows(f)
	f.status.ActiveWebSockets = 4
	return f
}

type confirmRecorder struct {
	answer bool
	asked  []trayConfirm
}

func (r *confirmRecorder) confirm(c trayConfirm) bool {
	r.asked = append(r.asked, c)
	return r.answer
}

func TestTrayCodexEntryState_MatchesRequestPageRules(t *testing.T) {
	for _, tc := range []struct {
		name  string
		edit  func(*codexEntryFake)
		state string
	}{
		{"运行中且一致", func(*codexEntryFake) {}, tray.CodexEntryReady},
		{"未初始化不显示", func(f *codexEntryFake) { f.config.Initialized = false }, tray.CodexEntryHidden},
		{"缺少访问密钥", func(f *codexEntryFake) { f.config.AccessConfigured = false }, tray.CodexEntryUnavailable},
		{"用户停用且已停稳", func(f *codexEntryFake) { f.config.Enabled, f.status.State = false, "stopped" }, tray.CodexEntryStopped},
		{"停用中", func(f *codexEntryFake) { f.config.Enabled = false }, tray.CodexEntryUnavailable},
		{"启用但启动失败", func(f *codexEntryFake) { f.status.State = "failed" }, tray.CodexEntryUnavailable},
		{"运行态落后于配置", func(f *codexEntryFake) { f.status.Revision = 6 }, tray.CodexEntryUnavailable},
	} {
		f := readyCodexEntry()
		tc.edit(f)
		if state, _ := codexEntryState(f.config, f.status); state != tc.state {
			t.Errorf("%s: 状态 = %q，期望 %q", tc.name, state, tc.state)
		}
	}
}

func TestTrayCodexEntryMode_ConfirmsThenSavesOnlyMode(t *testing.T) {
	f, ask := readyCodexEntry(), &confirmRecorder{answer: true}
	f.afterWrite = runtimeFollows
	if err := switchTrayCodexEntryMode(f, tray.Action{Kind: "codex_entry", Mode: "pool", ID: 7}, ask.confirm); err != nil {
		t.Fatal(err)
	}
	want := CodexSubscriptionProxyConfigInput{Enabled: true, Mode: "pool", ListenHost: "127.0.0.1", ListenPort: 18089, WSCompression: "disabled"}
	if f.saved == nil || *f.saved != want || f.savedRevision != 7 {
		t.Fatalf("只能改模式并带上读到的 revision: %+v rev=%d", f.saved, f.savedRevision)
	}
	if len(ask.asked) != 1 || ask.asked[0].Destructive || !strings.Contains(ask.asked[0].Message, ":18089") ||
		!strings.Contains(ask.asked[0].Message, "WS 连接数：4") || ask.asked[0].Confirm != "确认切换到账号池" {
		t.Fatalf("确认框内容错误: %+v", ask.asked)
	}
}

func TestTrayCodexEntryMode_CancelStaleAndUnavailableDoNotWrite(t *testing.T) {
	f, ask := readyCodexEntry(), &confirmRecorder{}
	if err := switchTrayCodexEntryMode(f, tray.Action{Mode: "pool", ID: 7}, ask.confirm); err != nil || f.saved != nil {
		t.Fatalf("取消后不得写入: err=%v saved=%+v", err, f.saved)
	}
	ask = &confirmRecorder{answer: true}
	if err := switchTrayCodexEntryMode(f, tray.Action{Mode: "pool", ID: 6}, ask.confirm); !errors.Is(err, errTrayCodexEntryStale) || len(ask.asked) != 0 {
		t.Fatalf("revision 过期应直接拒绝、不弹确认: %v", err)
	}
	f.status.State = "failed"
	if err := switchTrayCodexEntryMode(f, tray.Action{Mode: "pool", ID: 7}, ask.confirm); err == nil || !strings.Contains(err.Error(), "启动失败") ||
		len(ask.asked) != 0 || f.saved != nil {
		t.Fatalf("不可切换的状态应说明原因: %v", err)
	}
}

func TestTrayCodexEntryMode_WarnsWhenRuntimeNotConfirmed(t *testing.T) {
	f := readyCodexEntry()
	f.afterWrite = func(f *codexEntryFake) { f.status.State, f.status.LastError = "failed", "端口被占用" }
	err := switchTrayCodexEntryMode(f, tray.Action{Mode: "pool", ID: 7}, (&confirmRecorder{answer: true}).confirm)
	var warning trayWarning
	if !errors.As(err, &warning) || !strings.Contains(warning.message, "端口被占用") {
		t.Fatalf("已保存但运行态未确认时应提醒: %v", err)
	}
}

func TestTrayCodexEntryEnabled_DisableConfirmsEnableDoesNot(t *testing.T) {
	f, ask := readyCodexEntry(), &confirmRecorder{}
	f.afterWrite = runtimeFollows
	disable := tray.Action{Kind: "codex_entry_disable", ID: 7}
	if err := setTrayCodexEntryEnabled(f, disable, false, ask.confirm); err != nil || len(f.enabledCalls) != 0 ||
		len(ask.asked) != 1 || !ask.asked[0].Destructive {
		t.Fatalf("停用需确认且默认取消: err=%v calls=%v asked=%+v", err, f.enabledCalls, ask.asked)
	}
	ask.answer = true
	if err := setTrayCodexEntryEnabled(f, disable, false, ask.confirm); err != nil || len(f.enabledCalls) != 1 || f.savedRevision != 7 {
		t.Fatalf("确认后停用: err=%v calls=%v", err, f.enabledCalls)
	}

	ask = &confirmRecorder{}
	f.afterWrite = func(f *codexEntryFake) {
		runtimeFollows(f)
		f.status.State, f.status.LastError = "failed", "端口被占用"
	}
	err := setTrayCodexEntryEnabled(f, tray.Action{Kind: "codex_entry_enable", ID: 8}, true, ask.confirm)
	var warning trayWarning
	if len(ask.asked) != 0 || len(f.enabledCalls) != 2 || !f.enabledCalls[1] || !errors.As(err, &warning) {
		t.Fatalf("启用无需确认，启动失败要提醒: err=%v calls=%v", err, f.enabledCalls)
	}
}

type claudeEntryFake struct {
	info   ClaudeCodeEntryInfo
	writes []bool
}

func (f *claudeEntryFake) GetClaudeCodeEntryStatus() (ClaudeCodeEntryInfo, error) { return f.info, nil }

func (f *claudeEntryFake) setClaudeCodeEntryEnabled(enabled bool) (ClaudeCodeEntryInfo, error) {
	f.writes = append(f.writes, enabled)
	f.info.Enabled, f.info.Running = enabled, false
	if enabled {
		f.info.Error = "端口被占用"
	}
	return f.info, nil
}

func TestTrayClaudeEntryEnabled(t *testing.T) {
	f, ask := &claudeEntryFake{info: ClaudeCodeEntryInfo{Enabled: true, Running: true}}, &confirmRecorder{}
	if err := setTrayClaudeEntryEnabled(f, false, ask.confirm); err != nil || len(f.writes) != 0 || !ask.asked[0].Destructive {
		t.Fatalf("停用需确认，取消不写入: err=%v writes=%v", err, f.writes)
	}
	if err := setTrayClaudeEntryEnabled(f, true, ask.confirm); err != nil || len(f.writes) != 0 {
		t.Fatalf("已是目标状态时不写入: err=%v writes=%v", err, f.writes)
	}
	ask.answer = true
	if err := setTrayClaudeEntryEnabled(f, false, ask.confirm); err != nil || len(f.writes) != 1 || f.writes[0] {
		t.Fatalf("确认后停用: err=%v writes=%v", err, f.writes)
	}
	err := setTrayClaudeEntryEnabled(f, true, ask.confirm)
	var warning trayWarning
	if len(ask.asked) != 2 || !errors.As(err, &warning) || !strings.Contains(warning.message, "端口被占用") {
		t.Fatalf("启用无需确认，监听失败要提醒: err=%v asked=%d", err, len(ask.asked))
	}
}
