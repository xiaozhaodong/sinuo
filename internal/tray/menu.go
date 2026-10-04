package tray

import "fmt"

// Build 不读数据库、不调用 App，也不执行任何写入。
func Build(s Snapshot) []Item {
	status := "代理未运行"
	if s.Recovery {
		status = "迁移恢复中"
	}
	if s.StartupState != "" {
		status = s.StartupState
	}
	if s.Running && !s.Recovery {
		status = "代理运行中 · " + s.Address
	}
	if s.Waiting {
		status = fmt.Sprintf("退出中，等待 %d 个请求", s.Active)
	}
	items := []Item{info(status).withIcon(iconStatus)}
	open := navigate("打开 AI Switchboard", "").withIcon(iconApp)
	open.Shortcut = "CmdOrCtrl+O"
	quit := Item{Label: "退出 AI Switchboard", Shortcut: "CmdOrCtrl+Q", Icon: iconQuit, Action: Action{Kind: "quit"}}
	if s.Waiting {
		quit.Label = "退出 AI Switchboard…"
	}
	if !s.Running || s.Recovery {
		return append(items, separator(), open, quit)
	}
	stats := fmt.Sprintf("进行中 %d · 今日 %d 次 · $%.2f", s.Active, s.TodayRequests, s.TodayCost)
	if s.StatsError != "" {
		stats = fmt.Sprintf("进行中 %d · 今日统计读取失败", s.Active)
	}
	items = append(items, info(stats).withIcon(iconStats), separator(), claudeMenu(s))
	items = append(items, claudeEntryItems(s)...)
	items = append(items, separator(), codexMenu(s))
	items = append(items, codexEntryItems(s)...)
	items = append(items, separator(), open,
		navigate("打开请求追踪", "requests").withIcon(iconRequests), navigate("打开日志", "logs").withIcon(iconLogs), separator())
	items = append(items, choice("关闭窗口后保留在菜单栏", s.CloseToTray, s.SettingsError != "", Action{Kind: "close_to_tray"}).withIcon(iconMenuBar))
	return append(items, quit)
}
