package tray

import "fmt"

// 菜单栏文字模式，与设置 desktop.menubar_label 的取值一致。
const (
	MenuBarOff    = "off"
	MenuBarCost   = "today_cost"
	MenuBarClaude = "claude_quota"
	MenuBarCodex  = "codex_quota"
)

var menuBarModes = []struct{ mode, label string }{
	{MenuBarOff, "不显示"}, {MenuBarCost, "今日花费"}, {MenuBarClaude, "Claude 订阅剩余"}, {MenuBarCodex, "Codex 账号剩余"},
}

// NormalizeMenuBarMode：未知值按不显示处理。
func NormalizeMenuBarMode(mode string) string {
	for _, m := range menuBarModes {
		if m.mode == mode {
			return mode
		}
	}
	return MenuBarOff
}

// MenuBarText 是菜单栏图标旁的文字；没有数据时为空，只显示图标。
func MenuBarText(s Snapshot) string {
	if !s.Running || s.Recovery || s.StartupState != "" {
		return ""
	}
	switch s.MenuBarLabel {
	case MenuBarCost:
		if s.StatsError == "" {
			return fmt.Sprintf("$%.2f", s.TodayCost)
		}
	case MenuBarClaude:
		if s.EntryError == "" && s.EntryEnabled && s.EntryMode == "subscription" {
			return s.EntryQuotaShort
		}
	case MenuBarCodex:
		if account, ok := s.currentCodexAccount(); ok && !s.CodexModels && s.CodexError == "" {
			if name, left, ok := tightestWindow(account); ok {
				return fmt.Sprintf("%s %.0f%%", name, left)
			}
		}
	}
	return ""
}

// menuBarMenu：MenuBarLabel 为空表示平台不支持（非 macOS），不显示该项。选项是目标项，不带图标。
func menuBarMenu(s Snapshot) Item {
	choices := make([]Item, 0, len(menuBarModes))
	for _, m := range menuBarModes {
		current := s.MenuBarLabel == m.mode
		choices = append(choices, choice(m.label, current, current || s.SettingsError != "", Action{Kind: "menubar_label", Mode: m.mode}))
	}
	return Item{Label: "菜单栏文字", Icon: iconMenuBarText, Children: choices}
}
