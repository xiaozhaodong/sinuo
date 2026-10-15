package main

import (
	"errors"
	"fmt"

	"cc-forwarder/internal/tray"
)

// 托盘对两个独立入口（Claude Code 入口、Codex 独立入口）的读取与写入。
// 写入前重新读取最新状态；Codex 带上菜单展示时的 revision，配置已变化就拒绝，不按过期菜单写入。

// trayConfirm 是写入前的原生确认框；Destructive 时默认按钮为「取消」。
type trayConfirm struct {
	Title, Message, Confirm string
	Destructive             bool
}

// trayConfirmFunc 弹出确认并返回是否继续；测试中注入假实现。
type trayConfirmFunc func(trayConfirm) bool

// trayWarning 表示写入已生效、但结果需要提醒（例如已保存但运行态未确认），用警告框而不是错误框展示。
type trayWarning struct{ message string }

func (w trayWarning) Error() string { return w.message }

var errTrayCodexEntryStale = errors.New("Codex 独立入口状态已变化，请重新打开托盘菜单")

type trayClaudeEntryAPI interface {
	GetClaudeCodeEntryStatus() (ClaudeCodeEntryInfo, error)
	setClaudeCodeEntryEnabled(bool) (ClaudeCodeEntryInfo, error)
}

type trayCodexEntryAPI interface {
	GetCodexSubscriptionProxyConfig() (CodexSubscriptionProxyConfigInfo, error)
	GetCodexSubscriptionProxyStatus() (CodexSubscriptionProxyStatusInfo, error)
	SaveCodexSubscriptionProxyConfig(CodexSubscriptionProxyConfigInput, int64) (CodexSubscriptionProxySaveResultInfo, error)
	SetCodexSubscriptionProxyEnabled(bool, int64) (CodexSubscriptionProxySaveResultInfo, error)
}

func setTrayClaudeEntryEnabled(api trayClaudeEntryAPI, enabled bool, confirm trayConfirmFunc) error {
	current, err := api.GetClaudeCodeEntryStatus()
	if err != nil || current.Enabled == enabled {
		return err
	}
	if !enabled && !confirm(trayConfirm{Title: "停用 Claude Code 入口", Confirm: "停用", Destructive: true,
		Message: "停用后入口不再监听，仍指向它的 Claude Code 会连接失败；Sinuo 不会修改 Claude Code 的配置。确定停用吗？"}) {
		return nil
	}
	info, err := api.setClaudeCodeEntryEnabled(enabled)
	if err != nil || !enabled || info.Running {
		return err
	}
	reason := info.Error
	if reason == "" {
		reason = "入口未在监听"
	}
	return trayWarning{"入口已启用，但监听失败：" + reason}
}

var codexEntryStateLabels = map[string]string{"stopped": "未运行", "starting": "启动中", "draining": "停用中", "failed": "启动失败"}

// codexEntryState 与请求页 modeSwitchBlockReason 同一口径：运行中且配置与运行态一致才可切换模式；
// 配置停用且运行态已停稳是正常终态，可以重新启用；其余状态只读。
func codexEntryState(config CodexSubscriptionProxyConfigInfo, status CodexSubscriptionProxyStatusInfo) (state, label string) {
	switch {
	case !config.Initialized:
		return tray.CodexEntryHidden, ""
	case !config.AccessConfigured || config.Revision < 1:
		return tray.CodexEntryUnavailable, "尚未完成设置"
	case !config.Enabled && status.State == "stopped":
		return tray.CodexEntryStopped, ""
	case !config.Enabled && status.State == "running":
		return tray.CodexEntryUnavailable, "停用中"
	case status.State != "running":
		if label = codexEntryStateLabels[status.State]; label == "" {
			label = "状态未知"
		}
		return tray.CodexEntryUnavailable, label
	case !status.Enabled || status.Mode != config.Mode || status.Revision != config.Revision ||
		(config.Mode != "pool" && config.Mode != "subscription"):
		return tray.CodexEntryUnavailable, "状态待确认"
	}
	return tray.CodexEntryReady, ""
}

func readTrayCodexEntry(api trayCodexEntryAPI) (CodexSubscriptionProxyConfigInfo, CodexSubscriptionProxyStatusInfo, error) {
	config, err := api.GetCodexSubscriptionProxyConfig()
	if err != nil || !config.Initialized {
		return config, CodexSubscriptionProxyStatusInfo{}, err
	}
	status, err := api.GetCodexSubscriptionProxyStatus()
	return config, status, err
}

func (t *appTray) readCodexEntry(s *tray.Snapshot) {
	config, status, err := readTrayCodexEntry(t.app)
	if err != nil {
		s.CodexEntryState, s.CodexEntryLabel = tray.CodexEntryUnavailable, "读取失败"
		return
	}
	s.CodexEntryState, s.CodexEntryLabel = codexEntryState(config, status)
	s.CodexEntryMode, s.CodexEntryPort, s.CodexEntryRevision = config.Mode, config.ListenPort, config.Revision
}

// readCodexEntryAt 读取最新状态，并要求 revision 与菜单一致、状态为 want。
func readCodexEntryAt(api trayCodexEntryAPI, revision int64, want string) (CodexSubscriptionProxyConfigInfo, CodexSubscriptionProxyStatusInfo, error) {
	config, status, err := readTrayCodexEntry(api)
	if err != nil {
		return config, status, err
	}
	state, label := codexEntryState(config, status)
	switch {
	case config.Revision != revision:
		return config, status, errTrayCodexEntryStale
	case state == tray.CodexEntryUnavailable:
		return config, status, fmt.Errorf("Codex 独立入口%s，请到主窗口查看", label)
	case state != want:
		return config, status, errTrayCodexEntryStale
	}
	return config, status, nil
}

func codexModeConfirm(config CodexSubscriptionProxyConfigInfo, status CodexSubscriptionProxyStatusInfo, mode string) trayConfirm {
	effect := "Responses 使用客户端自带的本次凭据直连 ChatGPT Codex 后端。正文按当前隐私设置处理，账号池策略继续供主端口使用。"
	if mode == "pool" {
		effect = "Responses 发往账号池选中的上游，客户端身份头始终剥离；正文按当前隐私设置处理。账号不可用时按现有池策略处理，不会自动切回订阅。"
	}
	target := tray.CodexEntryModeLabel(mode)
	return trayConfirm{
		Title: fmt.Sprintf("Codex 独立入口：%s → %s", tray.CodexEntryModeLabel(config.Mode), target),
		Message: fmt.Sprintf("全局生效于所有使用独立入口（:%d）的 Codex 客户端，仅切换精确匹配的 Responses 请求；主端口的账号池路由不受影响。\n\n%s\n\n"+
			"HTTP 从下一请求生效，已建立的 WS 在重连后生效，其他接口继续订阅透传，在途请求保持原行为。当前底层 WS 连接数：%d（含上下游两侧）。",
			config.ListenPort, effect, status.ActiveWebSockets),
		Confirm: "确认切换到" + target,
	}
}

func switchTrayCodexEntryMode(api trayCodexEntryAPI, action tray.Action, confirm trayConfirmFunc) error {
	config, status, err := readCodexEntryAt(api, action.ID, tray.CodexEntryReady)
	if err != nil || config.Mode == action.Mode {
		return err
	}
	if !confirm(codexModeConfirm(config, status, action.Mode)) {
		return nil
	}
	// 只改模式，启用意图、监听地址与 WS 压缩沿用读到的配置；后端按 revision 做 CAS
	result, err := api.SaveCodexSubscriptionProxyConfig(CodexSubscriptionProxyConfigInput{Enabled: config.Enabled, Mode: action.Mode,
		ListenHost: config.ListenHost, ListenPort: config.ListenPort, WSCompression: config.WSCompression}, config.Revision)
	if err != nil {
		return err
	}
	after, afterStatus, err := readTrayCodexEntry(api)
	if err == nil {
		state, _ := codexEntryState(after, afterStatus)
		if state == tray.CodexEntryReady && after.Mode == action.Mode && after.Revision == result.Config.Revision {
			return nil
		}
	}
	detail := "配置或运行态已变化"
	if err != nil {
		detail = err.Error()
	} else if afterStatus.LastError != "" {
		detail = afterStatus.LastError
	}
	return trayWarning{fmt.Sprintf("模式已保存，但尚未确认运行态：%s。请到主窗口刷新查看，已有 WS 在重连后生效。", detail)}
}

func setTrayCodexEntryEnabled(api trayCodexEntryAPI, action tray.Action, enabled bool, confirm trayConfirmFunc) error {
	want := tray.CodexEntryReady
	if enabled {
		want = tray.CodexEntryStopped
	}
	config, _, err := readCodexEntryAt(api, action.ID, want)
	if err != nil {
		return err
	}
	if !enabled && !confirm(trayConfirm{Title: "停用 Codex 独立入口", Confirm: "停用", Destructive: true,
		Message: fmt.Sprintf("停用后独立入口（:%d）不再监听，仍指向它的 Codex 客户端将无法连接；Sinuo 不会修改客户端配置。"+
			"如需改用主端口，请先修改客户端接入配置。确定停用吗？", config.ListenPort)}) {
		return nil
	}
	if _, err := api.SetCodexSubscriptionProxyEnabled(enabled, config.Revision); err != nil || !enabled {
		return err
	}
	_, status, err := readTrayCodexEntry(api)
	if err == nil && status.State == "failed" {
		return trayWarning{"独立入口已启用，但启动失败：" + status.LastError}
	}
	return nil
}
