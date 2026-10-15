package tray

import (
	"bytes"
	"fmt"
	"image/png"
	"reflect"
	"strings"
	"testing"
	"time"
)

func menuItem(t *testing.T, items []Item, prefix string) Item {
	t.Helper()
	for _, item := range items {
		if strings.HasPrefix(item.Label, prefix) {
			return item
		}
	}
	t.Fatalf("没有菜单项 %q: %+v", prefix, items)
	return Item{}
}

func hasItem(items []Item, prefix string) bool {
	for _, item := range items {
		if strings.HasPrefix(item.Label, prefix) {
			return true
		}
	}
	return false
}

func menuChoices(items []Item, kind, mode string) []Item {
	var choices []Item
	for _, item := range items {
		if item.Action.Kind == kind && item.Action.Mode == mode {
			choices = append(choices, item)
		}
	}
	return choices
}

func runningSnapshot() Snapshot {
	return Snapshot{Running: true, Address: "127.0.0.1:8088", CloseToTray: true, ClaudeMode: "auto", CodexRoutingAvailable: true,
		Endpoints: []Endpoint{{Name: "relay", Available: true}}, Accounts: []Account{{ID: 1, Name: "plus-01", Enabled: true, HasCredential: true}}}
}

func TestMenu_StoppedAndRecovery(t *testing.T) {
	for _, recovery := range []bool{false, true} {
		s := runningSnapshot()
		s.Running, s.Recovery = false, recovery
		items := Build(s)
		if len(items) != 4 || !items[0].Disabled || items[2].Action.Kind != "navigate" || items[3].Action.Kind != "quit" {
			t.Fatalf("停止时只允许状态、打开、退出: %+v", items)
		}
	}
}

func TestMenu_ClaudeGlobalSelectionAndOrder(t *testing.T) {
	for _, mode := range []string{"auto", "manual_preferred", "manual_fixed"} {
		s := runningSnapshot()
		s.ClaudeMode, s.ClaudeTarget, s.ClaudeLastTarget = mode, "beta", "alpha"
		s.Endpoints = []Endpoint{{Name: "hidden", Available: false}, {Name: "later", Priority: 20, Available: true},
			{Name: "beta", Priority: 1, Available: true, Status: "冷却中"}, {Name: "alpha", Priority: 1, Available: true, Status: "不健康"}}
		claude := menuItem(t, Build(s), "Claude  ")
		if menuItem(t, claude.Children, "自动调度").Checked != (mode == "auto") {
			t.Fatal("自动勾选错误")
		}
		for _, operation := range []string{"manual_preferred", "manual_fixed"} {
			choices := menuChoices(claude.Children, "claude", operation)
			if len(choices) != 3 || choices[0].Action.Target != "alpha" || choices[1].Action.Target != "beta" {
				t.Fatalf("过滤或排序错误: %+v", choices)
			}
			for _, choice := range choices {
				if choice.Disabled || choice.Checked != (mode == operation && choice.Action.Target == "beta") {
					t.Fatalf("置灰或勾选错误: %+v", choice)
				}
			}
		}
	}
}

func TestMenu_ClaudeModelsPreserveMembersAndSortWebFirst(t *testing.T) {
	s := runningSnapshot()
	s.ClaudeModels = true
	s.ClaudeBindings = []Binding{
		{ID: 1, Name: "z-model", Enabled: true, Mode: "auto", Endpoints: []Endpoint{{Name: "only-z", Status: "硬关闭"}}},
		{ID: 2, Name: "a-disabled", Mode: "manual_fixed"},
		{ID: 3, Name: "联网工具", Kind: "web_tools", Enabled: true, Mode: "manual_fixed", Target: "web", Endpoints: []Endpoint{{Name: "web"}}},
	}
	items := claudeItems(s)
	if len(items) != 3 || !strings.HasPrefix(items[1].Label, "联网工具") || !strings.HasPrefix(items[2].Label, "z-model") ||
		hasItem(items, "a-disabled") || !menuItem(t, items[0].Children, "另有 1 条映射已停用").Disabled {
		t.Fatalf("映射排序/停用错误: %+v", items)
	}
	choices := menuChoices(items[2].Children, "claude_binding", "manual_preferred")
	if len(choices) != 1 || choices[0].Action.Target != "only-z" || choices[0].Disabled {
		t.Fatalf("不应套用全局端点过滤规则: %+v", choices)
	}
}

func TestMenu_ClaudeEntryStates(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		enabled, running     bool
		mode, state, toggle  string
		quota, listenFailure bool
	}{
		{"停用仍显示并可启用", false, false, "subscription", "已停用", "entry_enable", false, false},
		{"端点模式", true, true, "endpoint", "端点", "entry_disable", false, false},
		{"订阅直通带额度", true, true, "subscription", "订阅直通", "entry_disable", true, false},
		{"监听失败显示原因", true, false, "subscription", "监听失败", "entry_disable", true, true},
	} {
		s := runningSnapshot()
		s.EntryEnabled, s.EntryRunning, s.EntryMode, s.EntryPort = tc.enabled, tc.running, tc.mode, 18088
		s.EntryQuota, s.EntryListenError = "5h 剩 62% · d7 剩 81%", "端口被占用"
		items := Build(s)
		entry := menuItem(t, items, "Claude Code 入口")
		if entry.Label != "Claude Code 入口  "+tc.state+" · :18088" || hasItem(items, "订阅额度") != tc.quota {
			t.Fatalf("%s: 入口行展示错误: %q", tc.name, entry.Label)
		}
		if hasItem(entry.Children, "端口被占用") != tc.listenFailure {
			t.Fatalf("%s: 监听失败原因展示错误: %+v", tc.name, entry.Children)
		}
		for _, choice := range entry.Children {
			if choice.Action.Kind == "entry" && choice.Checked != (choice.Action.Mode == tc.mode) {
				t.Fatalf("%s: 接入模式勾选错误", tc.name)
			}
		}
		if last := entry.Children[len(entry.Children)-1]; last.Action.Kind != tc.toggle {
			t.Fatalf("%s: 启停项错误: %+v", tc.name, last)
		}
		note := hasItem(menuItem(t, items, "Claude  ").Children, "Claude Code 入口为订阅直通")
		if note != (tc.enabled && tc.running && tc.mode == "subscription") {
			t.Fatalf("%s: 主端口策略说明错误", tc.name)
		}
	}
	s := runningSnapshot()
	s.EntryError = "设置读取失败"
	if entry := menuItem(t, Build(s), "Claude Code 入口"); !entry.Disabled || len(entry.Children) != 0 {
		t.Fatalf("入口读取失败时不得提供写入: %+v", entry)
	}
}

func TestMenu_CodexEntryStates(t *testing.T) {
	s := runningSnapshot()
	s.CodexEntryState, s.CodexEntryMode, s.CodexEntryPort, s.CodexEntryRevision = CodexEntryReady, "subscription", 28088, 7
	items := Build(s)
	// 订阅直通时账号池策略仍要保留（主端口在用），并说明只影响主端口
	menuItem(t, menuItem(t, items, "Codex  ").Children, "Codex 入口为订阅直通")
	entry := menuItem(t, items, "Codex 入口")
	subscription, pool := menuItem(t, entry.Children, "订阅直通"), menuItem(t, entry.Children, "账号池")
	if entry.Label != "Codex 入口  订阅直通 · :28088" || !subscription.Checked || !subscription.Disabled || pool.Disabled ||
		pool.Action != (Action{Kind: "codex_entry", Mode: "pool", ID: 7}) {
		t.Fatalf("运行中应可切到另一模式并带上 revision: %+v", entry)
	}
	if last := entry.Children[len(entry.Children)-1]; last.Action != (Action{Kind: "codex_entry_disable", ID: 7}) {
		t.Fatalf("运行中应可停用: %+v", last)
	}

	s.CodexEntryState = CodexEntryStopped
	entry = menuItem(t, Build(s), "Codex 入口")
	if entry.Label != "Codex 入口  已停用 · :28088" || !menuItem(t, entry.Children, "账号池").Disabled ||
		entry.Children[len(entry.Children)-1].Action.Kind != "codex_entry_enable" {
		t.Fatalf("停用后只能启用，不能切换模式: %+v", entry)
	}

	s.CodexEntryState, s.CodexEntryLabel = CodexEntryUnavailable, "启动失败"
	entry = menuItem(t, Build(s), "Codex 入口")
	if len(entry.Children) != 0 || entry.Action != (Action{Kind: "navigate", Target: "codex-access"}) {
		t.Fatalf("状态不明时只能跳主窗口: %+v", entry)
	}

	s.CodexEntryState = CodexEntryHidden
	if hasItem(Build(s), "Codex 入口") {
		t.Fatal("未初始化的独立入口不应显示")
	}
}

func TestMenu_ReadErrorsAndWaiting(t *testing.T) {
	s := runningSnapshot()
	s.CodexError, s.ClaudeError, s.SettingsError = "账号读取失败", "映射读取失败", "设置读取失败"
	items := Build(s)
	if menuItem(t, items, "Claude  ").Children[0].Action.Kind != "" || !menuItem(t, items, "关闭窗口").Disabled {
		t.Fatal("读取失败时不得伪造可操作状态")
	}
	s.Waiting, s.Active = true, 3
	if Build(s)[0].Label != "退出中，等待 3 个请求" {
		t.Fatal("等待退出状态没有显示实时请求数")
	}
}

func TestMenu_CodexGroupsAndPinRules(t *testing.T) {
	s := runningSnapshot()
	used := 30.0
	s.Accounts = []Account{
		{ID: 2, Name: "cold", Priority: 30, Enabled: true},
		{ID: 3, Name: "auth", Priority: 12, Enabled: true, State: " DISABLED_AUTH "},
		{ID: 10, Name: "missing-credential", Priority: 1, Enabled: true},
		{ID: 4, Name: "disabled", Priority: 1, Enabled: false},
		{ID: 5, Name: "cooling", Group: "备组", Priority: 1, Enabled: true, Cooling: true, Pinned: true, FiveHourUsed: &used},
	}
	s.RecentAccountID = 10
	codex := menuItem(t, Build(s), "Codex  ")
	if codex.Label != "Codex  固定 → cooling · 5h 剩 70%" || hasItem(codex.Children, "优选到") {
		t.Fatalf("全局只允许自动和固定: %+v", codex)
	}
	choices := menuChoices(codex.Children, "codex", "manual_fixed")
	var groups []string
	for _, choice := range codex.Children {
		if strings.HasPrefix(choice.Label, "── 固定到 · ") {
			if !choice.Disabled {
				t.Fatal("分组标题必须置灰")
			}
			groups = append(groups, choice.Label)
			continue
		}
		if choice.Action.Kind != "codex" || choice.Action.Mode != "manual_fixed" {
			continue
		}
		wantDisabled := choice.Action.ID == 3 || choice.Action.ID == 4
		if choice.Disabled != wantDisabled {
			t.Fatalf("固定规则错误: %+v", choice)
		}
	}
	if !reflect.DeepEqual(groups, []string{"── 固定到 · 主组 ──", "── 固定到 · 备组 ──", "── 固定到 · 冷备 ──"}) {
		t.Fatalf("组顺序错误: %v", groups)
	}
	if choices[0].Action.ID != 10 {
		t.Fatal("同优先级按 ID 的字符串排序")
	}
	if !strings.Contains(menuItem(t, choices, "cooling").Label, "剩 70%") {
		t.Fatal("额度必须显示剩余")
	}
}

func TestMenu_CodexModelPausedAndInvalidAccounts(t *testing.T) {
	s := runningSnapshot()
	s.CodexModels = true
	s.Accounts = []Account{{ID: 1, Name: "paused", Enabled: true}, {ID: 2, Name: "auth", Enabled: false, State: "disabled_auth"}}
	s.CodexBindings = []Binding{{ID: 8, Name: "model", Enabled: true, Mode: "manual_fixed", TargetID: 1,
		AccountIDs: []int64{1, 2, 99}, MemberEnabled: map[int64]bool{1: false}}, {ID: 9, Name: "off", Enabled: false}}
	bindings := Build(s)
	if hasItem(bindings, "off") || !hasItem(menuItem(t, bindings, "Codex  ").Children, "另有 1 条映射已停用") {
		t.Fatal("停用映射只在开关子菜单提示数量")
	}
	model := menuItem(t, bindings, "model")
	preferred, fixed := menuChoices(model.Children, "codex_binding", "manual_preferred"), menuChoices(model.Children, "codex_binding", "manual_fixed")
	for i := range preferred {
		if preferred[i].Disabled != (i == 0) || fixed[i].Disabled {
			t.Fatalf("不能把全局账号池禁用规则套到映射: preferred=%+v fixed=%+v", preferred[i], fixed[i])
		}
	}
	if !fixed[0].Checked || preferred[0].Checked {
		t.Fatal("勾选只能在当前模式列表")
	}
}

func TestMenu_EmptyMappingsAndInputsUnchanged(t *testing.T) {
	s := runningSnapshot()
	s.ClaudeModels, s.CodexModels = true, true
	items := Build(s)
	if !hasItem(items, "尚无启用的模型映射") || !hasItem(items, "尚无启用的模型绑定") {
		t.Fatal("空映射应显示未分配提示")
	}
	before := append([]Account(nil), s.Accounts...)
	Build(s)
	if !reflect.DeepEqual(before, s.Accounts) {
		t.Fatal("菜单构建修改了输入")
	}
}

func TestIcons_StaticTemplateAndColour(t *testing.T) {
	// 模板图标按菜单栏高度 22pt @2x 出图，字形只占 12pt，不撑满画布
	for _, tc := range []struct {
		data     []byte
		size     int
		template bool
	}{{NormalTemplate, 44, true}, {NormalColor, 48, false}} {
		img, err := png.Decode(bytes.NewReader(tc.data))
		if err != nil || img.Bounds().Dx() != tc.size || img.Bounds().Dy() != tc.size {
			t.Fatalf("图标尺寸或 PNG 无效: %v", err)
		}
		visible, transparent := 0, 0
		for y := 0; y < tc.size; y++ {
			for x := 0; x < tc.size; x++ {
				r, g, b, a := img.At(x, y).RGBA()
				if a > 0 {
					visible++
				} else {
					transparent++
				}
				if tc.template && (r != 0 || g != 0 || b != 0) {
					t.Fatal("模板图标只能通过 alpha 表达图形")
				}
			}
		}
		if visible < tc.size*tc.size/10 || transparent == 0 {
			t.Fatalf("图标为空或失去透明背景: visible=%d transparent=%d", visible, transparent)
		}
	}
}

func TestCooldownLeft(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		until time.Time
		now   time.Time
		want  string
	}{
		{time.Time{}, now, ""},
		{now.Add(5 * time.Minute), time.Time{}, ""},
		{now.Add(-time.Second), now, ""},
		{now.Add(30 * time.Second), now, "剩不到 1 分钟"},
		{now.Add(61 * time.Second), now, "剩 2 分钟"},
		{now.Add(59 * time.Minute), now, "剩 59 分钟"},
		{now.Add(time.Hour), now, "剩 1 小时"},
		{now.Add(65 * time.Minute), now, "剩 1 小时 5 分钟"},
	} {
		if got := cooldownLeft(tc.until, tc.now); got != tc.want {
			t.Errorf("cooldownLeft(%v) = %q，期望 %q", tc.until.Sub(tc.now), got, tc.want)
		}
	}
}

func TestMenu_CooldownShowsTimeLeft(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	until := now.Add(3 * time.Minute)
	s := runningSnapshot()
	s.Now = now
	s.Endpoints = []Endpoint{{Name: "relay", Available: true}, {Name: "cool", Available: true, Status: "冷却中", CooldownUntil: until},
		{Name: "unknown", Available: true, Status: "冷却中"}}
	s.Accounts = append(s.Accounts, Account{ID: 2, Name: "cooling", Enabled: true, HasCredential: true, Cooling: true, CooldownUntil: until})
	items := Build(s)
	endpoints := menuChoices(menuItem(t, items, "Claude  ").Children, "claude", "manual_fixed")
	if menuItem(t, endpoints, "cool").Label != "cool · 冷却中（剩 3 分钟）" || menuItem(t, endpoints, "unknown").Label != "unknown · 冷却中" {
		t.Fatalf("端点冷却应带剩余时间，截止时间未知时只显示冷却中: %+v", endpoints)
	}
	accounts := menuChoices(menuItem(t, items, "Codex  ").Children, "codex", "manual_fixed")
	if !strings.HasSuffix(menuItem(t, accounts, "cooling").Label, " · 冷却中（剩 3 分钟）") {
		t.Fatalf("账号冷却应带剩余时间: %+v", accounts)
	}

	s.CodexModels = true
	s.CodexBindings = []Binding{{ID: 8, Name: "gpt", Enabled: true, Mode: "auto", AccountIDs: []int64{1, 2}, MemberEnabled: map[int64]bool{1: true, 2: true}}}
	members := menuChoices(menuItem(t, Build(s), "gpt").Children, "codex_binding", "manual_fixed")
	if members[1].Label != "cooling #2 · 冷却中（剩 3 分钟）" {
		t.Fatalf("映射成员冷却应带剩余时间: %+v", members)
	}
}

func TestMenu_CodexRowShowsTightestQuota(t *testing.T) {
	used := func(v float64) *float64 { return &v }
	for _, tc := range []struct {
		name    string
		account Account
		want    string
	}{
		{"5h 更紧", Account{FiveHourUsed: used(88), WeeklyUsed: used(60)}, "5h 剩 12%"},
		{"d7 更紧", Account{FiveHourUsed: used(50), WeeklyUsed: used(80)}, "d7 剩 20%"},
		{"相同时取 5h", Account{FiveHourUsed: used(50), WeeklyUsed: used(50)}, "5h 剩 50%"},
		{"只有 d7 读数", Account{WeeklyUsed: used(10)}, "d7 剩 90%"},
		{"free 只看 d7", Account{Plan: "FREE", FiveHourUsed: used(99), WeeklyUsed: used(10)}, "d7 剩 90%"},
		{"api_key 无限额", Account{Provider: "api_key", FiveHourUsed: used(99)}, ""},
		{"读数未知", Account{}, ""},
		{"超额按 0 显示", Account{FiveHourUsed: used(120)}, "5h 剩 0%"},
	} {
		got := ""
		if name, left, ok := tightestWindow(tc.account); ok {
			got = fmt.Sprintf("%s 剩 %.0f%%", name, left)
		}
		if got != tc.want {
			t.Errorf("%s: %q，期望 %q", tc.name, got, tc.want)
		}
	}

	s := runningSnapshot()
	s.Accounts[0].FiveHourUsed = used(88)
	s.RecentAccountID = 1
	if label := menuItem(t, Build(s), "Codex  ").Label; label != "Codex  自动 → plus-01 · 5h 剩 12%" {
		t.Fatalf("最近命中的账号也带额度: %q", label)
	}
	s.RecentAccountID = 0
	if label := menuItem(t, Build(s), "Codex  ").Label; label != "Codex  自动" {
		t.Fatalf("没有当前账号时不带额度: %q", label)
	}
	s.CodexModels = true
	if label := menuItem(t, Build(s), "Codex  ").Label; label != "Codex  模型路由" {
		t.Fatalf("模型路由下没有单一当前账号: %q", label)
	}
}

func TestMenu_StatsLineNavigatesAndCopyAddresses(t *testing.T) {
	s := runningSnapshot()
	s.Active, s.TodayRequests, s.TodayFailed, s.TodayCost = 2, 128, 3, 3.42
	stats := Build(s)[1]
	if stats.Label != "进行中 2 · 今日 128 次 · 失败 3 · $3.42" || stats.Disabled || stats.Action != (Action{Kind: "navigate", Target: "requests-failed"}) {
		t.Fatalf("有失败时显示失败数并跳到今日失败: %+v", stats)
	}
	s.TodayFailed = 0
	if stats = Build(s)[1]; stats.Label != "进行中 2 · 今日 128 次 · $3.42" || stats.Action.Target != "requests" {
		t.Fatalf("没有失败时不显示失败数、跳请求追踪: %+v", stats)
	}
	s.StatsError = "读取失败"
	if stats = Build(s)[1]; stats.Label != "进行中 2 · 今日统计读取失败" || stats.Action.Target != "requests" {
		t.Fatalf("统计读取失败: %+v", stats)
	}

	if hasItem(Build(s), "复制接入地址") {
		t.Fatal("没有地址时不显示复制")
	}
	s.MainURL = "http://127.0.0.1:8088"
	addresses := menuItem(t, Build(s), "复制接入地址")
	if len(addresses.Children) != 1 || addresses.Children[0].Action != (Action{Kind: "copy", Target: "http://127.0.0.1:8088"}) {
		t.Fatalf("主端口地址: %+v", addresses)
	}
	s.EntryURL = "http://127.0.0.1:18088"
	addresses = menuItem(t, Build(s), "复制接入地址")
	if len(addresses.Children) != 2 || addresses.Children[1].Label != "Claude Code 入口  http://127.0.0.1:18088" ||
		addresses.Children[1].Action != (Action{Kind: "copy", Target: "http://127.0.0.1:18088"}) {
		t.Fatalf("Claude Code 入口地址: %+v", addresses)
	}
}
