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
	items = append(items, statsItem(s))
	for _, problem := range Diagnose(s) {
		items = append(items, problemItem(problem, s.Now))
	}
	if len(s.Failovers) > 0 {
		items = append(items, recentFailovers(s))
	}
	items = append(items, separator(), claudeMenu(s))
	items = append(items, claudeEntryItems(s)...)
	items = append(items, separator(), codexMenu(s))
	items = append(items, codexEntryItems(s)...)
	items = append(items, separator(), open,
		navigate("打开请求追踪", "requests").withIcon(iconRequests), navigate("打开日志", "logs").withIcon(iconLogs))
	if s.MainURL != "" {
		items = append(items, copyMenu(s))
	}
	items = append(items, separator())
	items = append(items, choice("关闭窗口后保留在菜单栏", s.CloseToTray, s.SettingsError != "", Action{Kind: "close_to_tray"}).withIcon(iconMenuBar))
	if s.MenuBarLabel != "" {
		items = append(items, menuBarMenu(s))
	}
	return append(items, quit)
}

// statsItem：点击打开请求追踪；今日有失败时直接筛到今日失败。失败数为 0 时不占篇幅。
func statsItem(s Snapshot) Item {
	if s.StatsError != "" {
		return navigate(fmt.Sprintf("进行中 %d · 今日统计读取失败", s.Active), "requests").withIcon(iconStats)
	}
	label, target := fmt.Sprintf("进行中 %d · 今日 %d 次", s.Active, s.TodayRequests), "requests"
	if s.TodayFailed > 0 {
		label, target = label+fmt.Sprintf(" · 失败 %d", s.TodayFailed), "requests-failed"
	}
	return navigate(label+fmt.Sprintf(" · $%.2f", s.TodayCost), target).withIcon(iconStats)
}

// copyMenu 只列不含密钥的地址：Codex 独立入口的地址带密钥，且后端不再返回明文，不进托盘。
func copyMenu(s Snapshot) Item {
	targets := []Item{{Label: "主端口  " + s.MainURL, Action: Action{Kind: "copy", Target: s.MainURL}}}
	if s.EntryURL != "" {
		targets = append(targets, Item{Label: "Claude Code 入口  " + s.EntryURL, Action: Action{Kind: "copy", Target: s.EntryURL}})
	}
	return Item{Label: "复制接入地址", Icon: iconCopy, Children: targets}
}
