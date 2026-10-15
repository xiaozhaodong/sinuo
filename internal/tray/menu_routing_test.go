package tray

import (
	"strings"
	"testing"
)

func TestMenu_RoutingToggleCarriesTargetState(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, available := range []bool{false, true} {
			s := runningSnapshot()
			s.ClaudeModels, s.CodexModels, s.CodexRoutingAvailable = enabled, enabled, available
			mode := "on"
			if enabled {
				mode = "off"
			}
			for _, target := range []string{"claude", "codex"} {
				label := "Claude  "
				if target == "codex" {
					label = "Codex  "
				}
				children := menuItem(t, Build(s), label).Children
				toggle := children[0]
				if toggle.Label != "按模型路由" || !toggle.Checkable || toggle.Checked != enabled ||
					toggle.Disabled != (target == "codex" && !available) ||
					toggle.Action != (Action{Kind: "model_routing", Target: target, Mode: mode}) {
					t.Fatalf("开关必须携带目标状态，且只在 Codex 服务不可用时置灰: %+v", toggle)
				}
			}
		}
	}
}

func TestMenu_ModelRowsAreTopLevelAndOrdered(t *testing.T) {
	s := runningSnapshot()
	s.ClaudeModels, s.CodexModels = true, true
	s.ClaudeBindings = []Binding{{Name: "z-model", Enabled: true}, {Name: "off"}, {Name: "a-model", Enabled: true},
		{Name: "联网工具", Kind: "web_tools", Enabled: true}}
	s.CodexBindings = []Binding{{Name: "z-codex", Enabled: true}, {Name: "off"}, {Name: "a-codex", Enabled: true}}
	items := Build(s)
	assertProviderRows(t, items, "Claude  模型路由", []string{"联网工具", "a-model", "z-model", "Claude Code 入口"})
	assertProviderRows(t, items, "Codex  模型路由", []string{"a-codex", "z-codex"})
	if hasItem(items, "off") {
		t.Fatal("停用映射不得出现在顶层")
	}
	if menuItem(t, items, "联网工具").Icon != iconWebTools || menuItem(t, items, "a-model").Icon != iconModel {
		t.Fatal("模型行必须保留顶层图标")
	}
	for _, label := range []string{"Claude  ", "Codex  "} {
		children := menuItem(t, items, label).Children
		if !menuItem(t, children, "另有 1 条映射已停用").Disabled || len(children) != 4 {
			t.Fatalf("开关子菜单只显示开关、停用数和管理入口: %+v", children)
		}
	}
}

func assertProviderRows(t *testing.T, items []Item, header string, labels []string) {
	t.Helper()
	for i, item := range items {
		if item.Label != header {
			continue
		}
		for j, label := range labels {
			if i+j+1 >= len(items) || !strings.HasPrefix(items[i+j+1].Label, label) {
				t.Fatalf("%s 后第 %d 项应为 %q: %+v", header, j+1, label, items)
			}
		}
		return
	}
	t.Fatalf("没有顶层菜单 %q", header)
}

func TestMenu_ModelSectionsAndSubscriptionNotes(t *testing.T) {
	s := runningSnapshot()
	s.ClaudeModels, s.CodexModels = true, true
	s.ClaudeBindings = []Binding{{ID: 1, Name: "claude-model", Enabled: true, Mode: "auto"}}
	s.CodexBindings = []Binding{{ID: 2, Name: "codex-model", Enabled: true, Mode: "auto"}}
	s.EntryEnabled, s.EntryRunning, s.EntryMode = true, true, "subscription"
	s.CodexEntryState, s.CodexEntryMode = CodexEntryReady, "subscription"
	items := Build(s)
	for _, tc := range []struct{ header, model, entry, page string }{
		{"Claude  ", "claude-model", "Claude Code", "claude-model-routing"},
		{"Codex  ", "codex-model", "Codex", "codex-model-routing"},
	} {
		for _, row := range []string{tc.header, tc.model} {
			children := menuItem(t, items, row).Children
			if children[0].Label != tc.entry+" 入口为订阅直通，以下策略只影响主端口" ||
				!children[0].Disabled || children[0].Icon != "" || !children[1].Separator {
				t.Fatalf("开关与模型子菜单顶部应说明订阅直通的作用域: %+v", children)
			}
			if last := children[len(children)-1]; last.Action != (Action{Kind: "navigate", Target: tc.page}) {
				t.Fatalf("管理入口应跳到对应映射页: %+v", last)
			}
		}
		children := menuItem(t, items, tc.model).Children[2:]
		if len(children) != 7 || !children[0].Checked || !children[0].Disabled || !children[5].Separator {
			t.Fatalf("自动与两个空段及管理入口的结构错误: %+v", children)
		}
		for _, i := range []int{1, 3} {
			if !children[i].Disabled || children[i].Action != (Action{}) || len(children[i].Children) != 0 ||
				!strings.HasPrefix(children[i+1].Label, "未选择任何") || !children[i+1].Disabled {
				t.Fatalf("空段应保留灰色标题与空目标说明: %+v", children)
			}
		}
		if children[1].Label != "── 优选 · 不可用时回落 ──" || children[3].Label != "── 固定 · 不可用时直接失败 ──" {
			t.Fatalf("必须保留两段操作说明: %+v", children)
		}
	}
}

func TestMenu_NoEnabledMappingsWarnAtTop(t *testing.T) {
	for _, bindings := range [][]Binding{nil, {{Name: "off-a"}, {Name: "off-b"}}} {
		s := runningSnapshot()
		s.ClaudeModels, s.CodexModels = true, true
		s.ClaudeBindings, s.CodexBindings = bindings, bindings
		items := Build(s)
		for _, tc := range []struct{ header, warning string }{
			{"Claude  模型路由", "尚无启用的模型映射，Claude 请求将返回未分配错误"},
			{"Codex  模型路由", "尚无启用的模型绑定，未匹配的模型将返回未分配错误"},
		} {
			assertProviderRows(t, items, tc.header, []string{tc.warning})
			warning := menuItem(t, items, tc.warning)
			if !warning.Disabled || warning.Icon != iconAlert || warning.Action != (Action{}) {
				t.Fatalf("无启用映射应在顶层明确警示: %+v", warning)
			}
			children := menuItem(t, items, tc.header).Children
			if hasItem(children, "另有 2 条映射已停用") != (len(bindings) > 0) || hasItem(children, "另有 0") {
				t.Fatalf("停用数只在大于 0 时出现: %+v", children)
			}
		}
	}
}

func TestMenu_GlobalEmptyKeepsRoutingToggle(t *testing.T) {
	s := runningSnapshot()
	s.Endpoints, s.Accounts = nil, nil
	for _, tc := range []struct{ header, empty, page string }{
		{"Claude  ", "无可用 Claude 端点", "endpoints"},
		{"Codex  ", "无可用 Codex 账号", "account-pool"},
	} {
		children := menuItem(t, Build(s), tc.header).Children
		if len(children) != 5 || children[0].Action.Kind != "model_routing" || !children[1].Separator ||
			children[2].Label != tc.empty || !children[2].Disabled || !children[3].Separator ||
			children[4].Action != (Action{Kind: "navigate", Target: tc.page}) {
			t.Fatalf("空全局列表仍要保留开关、提示与管理入口: %+v", children)
		}
	}
}

func TestMenu_SwitchActionsStayWithinOneSubmenu(t *testing.T) {
	for name, s := range iconSnapshots() {
		assertSwitchDepth(t, name, Build(s), 0)
	}
	s := runningSnapshot()
	s.ClaudeModels, s.CodexModels = true, true
	s.ClaudeBindings = []Binding{{ID: 1, Name: "claude-model", Enabled: true, Mode: "manual_fixed", Target: "cool",
		Endpoints: []Endpoint{{Name: "cool", Status: "冷却中"}}}}
	s.CodexBindings = []Binding{{ID: 2, Name: "codex-model", Enabled: true, Mode: "manual_fixed", TargetID: 1, AccountIDs: []int64{1}}}
	s.Accounts[0].Cooling = true
	assertSwitchDepth(t, "模型降级修复", Build(s), 0)
}

func assertSwitchDepth(t *testing.T, name string, items []Item, depth int) {
	t.Helper()
	for _, item := range items {
		switch item.Action.Kind {
		case "claude", "claude_binding", "codex", "codex_binding", "model_routing":
			if depth > 1 {
				t.Fatalf("%s: 切换操作 %q 的深度为 %d，应 ≤ 1", name, item.Label, depth)
			}
		}
		if item.Label == "优选到" || item.Label == "固定到" {
			t.Fatalf("%s: 不得重新引入操作子菜单: %+v", name, item)
		}
		assertSwitchDepth(t, name, item.Children, depth+1)
	}
}
