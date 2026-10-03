package tray

import (
	"bytes"
	"image/png"
	"reflect"
	"strings"
	"testing"
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

func runningSnapshot() Snapshot {
	return Snapshot{Running: true, Address: "127.0.0.1:8088", CloseToTray: true, ClaudeMode: "auto",
		Endpoints: []Endpoint{{Name: "relay", Available: true}}, Accounts: []Account{{ID: 1, Name: "plus-01", Enabled: true}}}
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
		claude := menuItem(t, Build(s), "Claude")
		if menuItem(t, claude.Children, "自动调度").Checked != (mode == "auto") {
			t.Fatal("自动勾选错误")
		}
		for _, operation := range []struct{ label, mode string }{{"优选到", "manual_preferred"}, {"固定到", "manual_fixed"}} {
			choices := menuItem(t, claude.Children, operation.label).Children
			if len(choices) != 3 || choices[0].Action.Target != "alpha" || choices[1].Action.Target != "beta" {
				t.Fatalf("过滤或排序错误: %+v", choices)
			}
			for _, choice := range choices {
				if choice.Disabled || choice.Checked != (mode == operation.mode && choice.Action.Target == "beta") {
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
	items := menuItem(t, Build(s), "Claude").Children
	if !strings.HasPrefix(items[0].Label, "联网工具") || !strings.HasPrefix(items[1].Label, "z-model") || !items[2].Disabled {
		t.Fatalf("映射排序/停用错误: %+v", items)
	}
	choices := menuItem(t, items[1].Children, "优选到").Children
	if len(choices) != 1 || choices[0].Action.Target != "only-z" || choices[0].Disabled {
		t.Fatalf("不应套用全局端点过滤规则: %+v", choices)
	}
}

func TestMenu_ClaudeEntryModes(t *testing.T) {
	for _, tc := range []struct {
		enabled bool
		mode    string
		quota   bool
	}{{false, "subscription", false}, {true, "endpoint", false}, {true, "subscription", true}} {
		s := runningSnapshot()
		s.EntryEnabled, s.EntryMode, s.EntryQuota = tc.enabled, tc.mode, "5h 剩 62% · d7 剩 81%"
		items := Build(s)
		if hasItem(items, "接入方式") != tc.enabled || hasItem(items, "订阅额度") != tc.quota {
			t.Fatalf("入口模式展示错误: %+v", tc)
		}
		if tc.enabled {
			choices := menuItem(t, items, "接入方式").Children
			for _, choice := range choices {
				if choice.Checked != (choice.Action.Mode == tc.mode) {
					t.Fatal("接入方式勾选错误")
				}
			}
		}
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
	codex := menuItem(t, Build(s), "Codex")
	if codex.Label != "Codex  固定 → cooling" || hasItem(codex.Children, "优选到") {
		t.Fatalf("全局只允许自动和固定: %+v", codex)
	}
	choices := menuItem(t, codex.Children, "固定到").Children
	var groups []string
	for _, choice := range choices {
		if choice.Action.Kind == "" {
			if !choice.Disabled {
				t.Fatal("分组标题必须置灰")
			}
			groups = append(groups, choice.Label)
			continue
		}
		wantDisabled := choice.Action.ID == 3 || choice.Action.ID == 4
		if choice.Disabled != wantDisabled {
			t.Fatalf("固定规则错误: %+v", choice)
		}
	}
	if !reflect.DeepEqual(groups, []string{"── 主组 ──", "── 备组 ──", "── 冷备 ──"}) {
		t.Fatalf("组顺序错误: %v", groups)
	}
	if choices[1].Action.ID != 10 {
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
	bindings := menuItem(t, Build(s), "Codex").Children
	if !menuItem(t, bindings, "off").Disabled {
		t.Fatal("停用映射必须置灰")
	}
	model := menuItem(t, bindings, "model")
	preferred, fixed := menuItem(t, model.Children, "优选到").Children, menuItem(t, model.Children, "固定到").Children
	for i := range preferred {
		if preferred[i].Disabled != (i == 0) || fixed[i].Disabled {
			t.Fatalf("不能把全局账号池禁用规则套到映射: preferred=%+v fixed=%+v", preferred[i], fixed[i])
		}
	}
	if !fixed[0].Checked || preferred[0].Checked {
		t.Fatal("勾选只能在当前模式列表")
	}
}

func TestMenu_CodexSubscriptionAndErrors(t *testing.T) {
	s := runningSnapshot()
	s.CodexReadOnly, s.CodexAccess = true, "订阅直通"
	item := menuItem(t, Build(s), "Codex")
	if len(item.Children) != 0 || item.Action.Kind != "navigate" || item.Action.Target != "codex-access" {
		t.Fatalf("订阅直通不能写入: %+v", item)
	}
	s.CodexReadOnly, s.CodexError, s.ClaudeError, s.SettingsError = false, "账号读取失败", "映射读取失败", "设置读取失败"
	items := Build(s)
	if menuItem(t, items, "Claude").Children[0].Action.Kind != "" || !menuItem(t, items, "关闭窗口").Disabled {
		t.Fatal("读取失败时不得伪造可操作状态")
	}
	s.Waiting, s.Active = true, 3
	if Build(s)[0].Label != "退出中，等待 3 个请求" {
		t.Fatal("等待退出状态没有显示实时请求数")
	}
}

func TestMenu_EmptyMappingsAndInputsUnchanged(t *testing.T) {
	s := runningSnapshot()
	s.ClaudeModels, s.CodexModels = true, true
	items := Build(s)
	if !hasItem(menuItem(t, items, "Claude").Children, "尚无模型映射") || !hasItem(menuItem(t, items, "Codex").Children, "尚无模型绑定") {
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
