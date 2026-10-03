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
	items := []Item{info(status)}
	open := navigate("打开 AI Switchboard", "")
	open.Shortcut = "CmdOrCtrl+O"
	quit := Item{Label: "退出 AI Switchboard", Shortcut: "CmdOrCtrl+Q", Action: Action{Kind: "quit"}}
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
	items = append(items, info(stats), separator(), claudeMenu(s))
	if s.EntryError != "" {
		items = append(items, info("接入方式 · 读取失败"))
	}
	if s.EntryEnabled && s.EntryError == "" {
		items = append(items, entryMenu(s))
		if s.EntryMode == "subscription" {
			quota := s.EntryQuota
			if quota == "" {
				quota = "尚无额度数据"
			}
			items = append(items, info("订阅额度  "+quota))
		}
	}
	items = append(items, separator(), codexMenu(s), separator(), open,
		navigate("打开请求追踪", "requests"), navigate("打开日志", "logs"), separator())
	items = append(items, choice("关闭窗口后保留在菜单栏", s.CloseToTray, s.SettingsError != "", Action{Kind: "close_to_tray"}))
	return append(items, quit)
}

func entryMenu(s Snapshot) Item {
	label := "端点"
	if s.EntryMode == "subscription" {
		label = "订阅直通"
	}
	return Item{Label: "接入方式  " + label, Children: []Item{
		choice("订阅直通", s.EntryMode == "subscription", s.EntryMode == "subscription", Action{Kind: "entry", Mode: "subscription"}),
		choice("端点", s.EntryMode == "endpoint", s.EntryMode == "endpoint", Action{Kind: "entry", Mode: "endpoint"}),
	}}
}
