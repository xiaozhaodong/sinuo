package tray

import "testing"

func TestMenuBarText(t *testing.T) {
	used := func(v float64) *float64 { return &v }
	base := runningSnapshot()
	base.TodayCost = 3.456
	base.EntryEnabled, base.EntryMode, base.EntryQuotaShort = true, "subscription", "5h 62%"
	base.Accounts[0].FiveHourUsed, base.Accounts[0].Pinned = used(88), true
	for _, tc := range []struct {
		name string
		edit func(*Snapshot)
		want string
	}{
		{"默认不显示", func(s *Snapshot) {}, ""},
		{"今日花费", func(s *Snapshot) { s.MenuBarLabel = MenuBarCost }, "$3.46"},
		{"统计读取失败时不显示花费", func(s *Snapshot) { s.MenuBarLabel, s.StatsError = MenuBarCost, "x" }, ""},
		{"Claude 订阅剩余", func(s *Snapshot) { s.MenuBarLabel = MenuBarClaude }, "5h 62%"},
		{"入口切到端点模式", func(s *Snapshot) { s.MenuBarLabel, s.EntryMode = MenuBarClaude, "endpoint" }, ""},
		{"入口停用", func(s *Snapshot) { s.MenuBarLabel, s.EntryEnabled = MenuBarClaude, false }, ""},
		{"Codex 当前账号剩余", func(s *Snapshot) { s.MenuBarLabel = MenuBarCodex }, "5h 12%"},
		{"Codex 模型路由下没有单一账号", func(s *Snapshot) { s.MenuBarLabel, s.CodexModels = MenuBarCodex, true }, ""},
		{"Codex 没有当前账号", func(s *Snapshot) { s.MenuBarLabel, s.Accounts[0].Pinned = MenuBarCodex, false }, ""},
		{"代理未运行", func(s *Snapshot) { s.MenuBarLabel, s.Running = MenuBarCost, false }, ""},
		{"正在启动", func(s *Snapshot) { s.MenuBarLabel, s.StartupState = MenuBarCost, "正在启动" }, ""},
	} {
		s := base
		s.Accounts = append([]Account(nil), base.Accounts...)
		tc.edit(&s)
		if got := MenuBarText(s); got != tc.want {
			t.Errorf("%s: %q，期望 %q", tc.name, got, tc.want)
		}
	}
	if NormalizeMenuBarMode("bogus") != MenuBarOff || NormalizeMenuBarMode(MenuBarCodex) != MenuBarCodex {
		t.Fatal("未知模式按不显示处理")
	}
}

func TestMenu_MenuBarLabelChoices(t *testing.T) {
	s := runningSnapshot()
	if hasItem(Build(s), "菜单栏文字") {
		t.Fatal("平台不支持（模式为空）时不显示")
	}
	s.MenuBarLabel = MenuBarCost
	choices := menuItem(t, Build(s), "菜单栏文字").Children
	if len(choices) != 4 || !choices[1].Checked || !choices[1].Disabled || choices[0].Disabled ||
		choices[2].Action != (Action{Kind: "menubar_label", Mode: MenuBarClaude}) || choices[2].Icon != "" {
		t.Fatalf("当前模式勾选置灰，其余可选，选项不带图标: %+v", choices)
	}
	s.SettingsError = "读取失败"
	for _, c := range menuItem(t, Build(s), "菜单栏文字").Children {
		if !c.Disabled {
			t.Fatal("设置读取失败时不能写入")
		}
	}
}
