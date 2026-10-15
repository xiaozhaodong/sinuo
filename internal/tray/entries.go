package tray

import "fmt"

// Claude Code 入口与 Codex 独立入口都与主端口分开监听：行名带端口，与主端口的路由策略区分开。

func entryPort(port int) string {
	if port <= 0 {
		return ""
	}
	return fmt.Sprintf(" · :%d", port)
}

func claudeEntryModeLabel(mode string) string {
	if mode == "subscription" {
		return "订阅直通"
	}
	return "端点"
}

// CodexEntryModeLabel 也用于托盘确认框文案。
func CodexEntryModeLabel(mode string) string {
	switch mode {
	case "subscription":
		return "订阅直通"
	case "pool":
		return "账号池"
	}
	return "未知模式"
}

func modeIcon(mode, other string) string {
	if mode == "subscription" {
		return iconSubscribe
	}
	return other
}

// claudeEntryItems：入口行与订阅额度行。停用时仍显示，便于从托盘重新启用；切换模式无需确认（与请求页一致）。
func claudeEntryItems(s Snapshot) []Item {
	if s.EntryError != "" {
		return []Item{info("Claude Code 入口  读取失败").withIcon(iconEntry)}
	}
	state, children := claudeEntryModeLabel(s.EntryMode), []Item{}
	switch {
	case !s.EntryEnabled:
		state = "已停用"
	case !s.EntryRunning:
		state = "监听失败"
		reason := s.EntryListenError
		if reason == "" {
			reason = "入口未在监听"
		}
		children = append(children, info(reason).withIcon(iconAlert), separator())
	}
	for _, mode := range []string{"subscription", "endpoint"} {
		current := s.EntryMode == mode
		children = append(children, choice(claudeEntryModeLabel(mode), current, current,
			Action{Kind: "entry", Mode: mode}).withIcon(modeIcon(mode, iconEndpoint)))
	}
	toggle := Item{Label: "启用入口", Icon: iconEnable, Action: Action{Kind: "entry_enable"}}
	if s.EntryEnabled {
		toggle = Item{Label: "停用入口…", Icon: iconDisable, Action: Action{Kind: "entry_disable"}}
	}
	items := []Item{{Label: "Claude Code 入口  " + state + entryPort(s.EntryPort), Icon: iconEntry,
		Children: append(children, separator(), toggle)}}
	if s.EntryEnabled && s.EntryMode == "subscription" {
		quota := s.EntryQuota
		if quota == "" {
			quota = "尚无额度数据"
		}
		items = append(items, info("订阅额度  "+quota).withIcon(iconQuota))
	}
	return items
}

// codexEntryItems：与请求页一致，只有运行中且配置与运行态一致时才能切换模式（切换前确认）；
// 停用后只显示已保存的模式并提供启用；其他状态只读，跳到主窗口查看。
func codexEntryItems(s Snapshot) []Item {
	switch s.CodexEntryState {
	case CodexEntryHidden:
		return nil
	case CodexEntryUnavailable:
		return []Item{navigate("Codex 入口  "+s.CodexEntryLabel+"（在主窗口查看）", "codex-access").withIcon(iconEntry)}
	}
	ready := s.CodexEntryState == CodexEntryReady
	children := []Item{}
	for _, mode := range []string{"subscription", "pool"} {
		current := s.CodexEntryMode == mode
		children = append(children, choice(CodexEntryModeLabel(mode), current, current || !ready,
			Action{Kind: "codex_entry", Mode: mode, ID: s.CodexEntryRevision}).withIcon(modeIcon(mode, iconPool)))
	}
	state := CodexEntryModeLabel(s.CodexEntryMode)
	toggle := Item{Label: "停用入口…", Icon: iconDisable, Action: Action{Kind: "codex_entry_disable", ID: s.CodexEntryRevision}}
	if !ready {
		state = "已停用"
		toggle = Item{Label: "启用入口", Icon: iconEnable, Action: Action{Kind: "codex_entry_enable", ID: s.CodexEntryRevision}}
	}
	return []Item{{Label: "Codex 入口  " + state + entryPort(s.CodexEntryPort), Icon: iconEntry,
		Children: append(children, separator(), toggle)}}
}

// scopeNote：入口为订阅直通时，路由子菜单里的策略只影响主端口，在顶部说明。
func scopeNote(entry string, subscription bool, items []Item) []Item {
	if !subscription {
		return items
	}
	return append([]Item{info(entry + " 入口为订阅直通，以下策略只影响主端口"), separator()}, items...)
}

func (s Snapshot) claudeEntrySubscription() bool {
	return s.EntryError == "" && s.EntryEnabled && s.EntryRunning && s.EntryMode == "subscription"
}

func (s Snapshot) codexEntrySubscription() bool {
	return s.CodexEntryState == CodexEntryReady && s.CodexEntryMode == "subscription"
}
