package tray

import (
	"reflect"
	"testing"
	"time"
)

var problemNow = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func coolingNames(p Problem) []string {
	var names []string
	for _, target := range p.Cooling {
		names = append(names, target.Name)
	}
	return names
}

func TestDiagnose_ProblemsAndFixes(t *testing.T) {
	until := problemNow.Add(3 * time.Minute)
	up := Endpoint{Name: "a", Available: true}
	cooling := Endpoint{Name: "b", Available: true, Status: "冷却中", CooldownUntil: until}
	hardOffCooling := Endpoint{Name: "d", Available: false, Status: "冷却中"}
	unhealthy := Endpoint{Name: "c", Available: true, Status: "不健康"}
	good := Account{ID: 1, Name: "good", Enabled: true, HasCredential: true}
	authFailed := Account{ID: 2, Name: "auth", Enabled: true, HasCredential: true, State: "disabled_auth", Cooling: true}
	coolingAccount := Account{ID: 3, Name: "cool", Enabled: true, HasCredential: true, Cooling: true, CooldownUntil: until}
	disabledCooling := Account{ID: 4, Name: "off", Enabled: false, HasCredential: true, Cooling: true}

	cases := []struct {
		name    string
		edit    func(*Snapshot)
		reason  string
		page    string
		auto    *Action
		cooling []string
	}{
		{"全局固定到冷却端点：可解除冷却或改自动", func(s *Snapshot) {
			s.ClaudeMode, s.ClaudeTarget, s.Endpoints = "manual_fixed", "b", []Endpoint{up, cooling}
		}, "Claude 固定端点 b 不可用", "endpoints", &Action{Kind: "claude", Mode: "auto"}, []string{"b"}},
		{"固定到硬关闭的端点：解除冷却无济于事", func(s *Snapshot) {
			s.ClaudeMode, s.ClaudeTarget, s.Endpoints = "manual_fixed", "d", []Endpoint{up, hardOffCooling}
		}, "Claude 固定端点 d 不可用", "endpoints", &Action{Kind: "claude", Mode: "auto"}, nil},
		{"固定目标已被删除", func(s *Snapshot) {
			s.ClaudeMode, s.ClaudeTarget, s.Endpoints = "manual_fixed", "gone", []Endpoint{up}
		}, "Claude 固定端点 gone 不可用", "endpoints", &Action{Kind: "claude", Mode: "auto"}, nil},
		{"全局没有可用端点：只列只因冷却不可用的端点", func(s *Snapshot) {
			s.Endpoints = []Endpoint{cooling, unhealthy, hardOffCooling}
		}, "Claude 没有可用端点", "endpoints", nil, []string{"b"}},
		{"映射固定端点冷却：只列目标、改自动只改这条映射", func(s *Snapshot) {
			s.ClaudeModels = true
			s.ClaudeBindings = []Binding{{ID: 7, Name: "opus", Enabled: true, Mode: "manual_fixed", Target: "b",
				Endpoints: []Endpoint{{Name: "a", Status: "冷却中"}, {Name: "b", Status: "冷却中", CooldownUntil: until}}}}
		}, "模型映射 opus：固定端点 b 不可用", "claude-model-routing", &Action{Kind: "claude_binding", ID: 7, Mode: "auto"}, []string{"b"}},
		{"映射自动但成员都不可用", func(s *Snapshot) {
			s.ClaudeModels = true
			s.ClaudeBindings = []Binding{{ID: 8, Name: "sonnet", Enabled: true, Mode: "auto",
				Endpoints: []Endpoint{{Name: "a", Status: "冷却中"}, {Name: "b", Status: "未参与自动调度"}, {Name: "c", Status: "硬关闭"}}}}
		}, "模型映射 sonnet：没有可用端点", "claude-model-routing", nil, []string{"a"}},
		{"账号池没有可调度账号：只列只因冷却不可用的账号", func(s *Snapshot) {
			s.Accounts = []Account{authFailed, coolingAccount, disabledCooling}
		}, "Codex 没有可调度账号", "account-pool", nil, []string{"cool"}},
		{"Codex 映射固定账号冷却", func(s *Snapshot) {
			s.CodexModels, s.Accounts = true, []Account{good, coolingAccount}
			s.CodexBindings = []Binding{{ID: 5, Name: "gpt", Enabled: true, Mode: "manual_fixed", TargetID: 3,
				AccountIDs: []int64{1, 3}, MemberEnabled: map[int64]bool{1: true, 3: true}}}
		}, "Codex 模型映射 gpt：固定账号 cool 不可用", "codex-model-routing", &Action{Kind: "codex_binding", ID: 5, Mode: "auto"}, []string{"cool"}},
		{"Codex 映射固定账号已不存在", func(s *Snapshot) {
			s.CodexModels, s.Accounts = true, []Account{good}
			s.CodexBindings = []Binding{{ID: 5, Name: "gpt", Enabled: true, Mode: "manual_fixed", TargetID: 99, AccountIDs: []int64{1, 99}}}
		}, "Codex 模型映射 gpt：固定账号 账号 99 不可用", "codex-model-routing", &Action{Kind: "codex_binding", ID: 5, Mode: "auto"}, nil},
		{"Codex 映射只剩暂停成员可用：暂停成员不列入解除冷却", func(s *Snapshot) {
			s.CodexModels, s.Accounts = true, []Account{good, coolingAccount, {ID: 6, Name: "paused-cool", Enabled: true, HasCredential: true, Cooling: true}}
			s.CodexBindings = []Binding{{ID: 5, Name: "gpt", Enabled: true, Mode: "auto",
				AccountIDs: []int64{1, 3, 6}, MemberEnabled: map[int64]bool{1: false, 3: true, 6: false}}}
		}, "Codex 模型映射 gpt：没有可调度账号", "codex-model-routing", nil, []string{"cool"}},
	}
	for _, tc := range cases {
		s := running()
		tc.edit(&s)
		problems := Diagnose(s)
		if len(problems) != 1 {
			t.Fatalf("%s: 应只有一个问题: %+v", tc.name, problems)
		}
		p := problems[0]
		if p.Reason != tc.reason || p.Page != tc.page || !reflect.DeepEqual(p.Auto, tc.auto) || !reflect.DeepEqual(coolingNames(p), tc.cooling) {
			t.Fatalf("%s: 问题与修复不符: %+v", tc.name, p)
		}
		if got := EvaluateIcon(s); got.State != IconDegraded || got.Reason != tc.reason {
			t.Fatalf("%s: 图标原因应与菜单一致: %+v", tc.name, got)
		}
	}
}

func TestDiagnose_BothSidesClaudeFirstAndStoppedNone(t *testing.T) {
	s := running()
	s.Endpoints = []Endpoint{{Name: "a", Available: true, Status: "冷却中"}}
	s.Accounts = []Account{{ID: 1, Name: "x", Enabled: true, HasCredential: true, Cooling: true}}
	problems := Diagnose(s)
	if len(problems) != 2 || problems[0].Reason != "Claude 没有可用端点" || problems[1].Reason != "Codex 没有可调度账号" {
		t.Fatalf("两侧各一个问题、Claude 在前: %+v", problems)
	}
	if target := problems[1].Cooling[0].Action; target != (Action{Kind: "account_cooldown", ID: 1}) {
		t.Fatalf("账号解除冷却动作错误: %+v", target)
	}
	if target := problems[0].Cooling[0].Action; target != (Action{Kind: "endpoint_cooldown", Target: "a"}) {
		t.Fatalf("端点解除冷却动作错误: %+v", target)
	}
	s.Running = false
	if Diagnose(s) != nil {
		t.Fatal("代理未运行时不判定问题")
	}
}

func TestMenu_ProblemRows(t *testing.T) {
	until := problemNow.Add(3 * time.Minute)
	s := runningSnapshot()
	s.Now = problemNow
	s.ClaudeMode, s.ClaudeTarget = "manual_fixed", "relay"
	s.Endpoints = []Endpoint{{Name: "relay", Available: true, Status: "冷却中", CooldownUntil: until}}
	items := Build(s)
	if items[2].Label != "Claude 固定端点 relay 不可用" || items[3].Separator != true {
		t.Fatalf("降级行应紧跟统计行、位于第一个分隔线之前: %+v", items[:4])
	}
	fixes := items[2].Children
	if fixes[0].Label != "解除 relay 的冷却（剩 3 分钟）" || fixes[0].Action != (Action{Kind: "endpoint_cooldown", Target: "relay"}) ||
		fixes[1].Action != (Action{Kind: "claude", Mode: "auto"}) || !fixes[2].Separator ||
		fixes[3].Action != (Action{Kind: "navigate", Target: "endpoints"}) {
		t.Fatalf("单个冷却目标应直接解除，随后是改自动与管理入口: %+v", fixes)
	}

	s = runningSnapshot()
	s.Now = problemNow
	s.Endpoints = []Endpoint{{Name: "a", Available: true, Status: "冷却中", CooldownUntil: until}, {Name: "b", Available: true, Status: "冷却中"}}
	clear := menuItem(t, menuItem(t, Build(s), "Claude 没有可用端点").Children, "解除冷却")
	if len(clear.Children) != 2 || clear.Children[0].Label != "a（剩 3 分钟）" || clear.Children[1].Label != "b" || clear.Children[0].Icon != "" {
		t.Fatalf("多个冷却目标应先选操作再选目标，目标项不带图标: %+v", clear)
	}

	s = runningSnapshot()
	s.Endpoints = []Endpoint{{Name: "a", Available: true, Status: "不健康"}}
	row := menuItem(t, Build(s), "Claude 没有可用端点")
	if row.Label != "Claude 没有可用端点（在主窗口查看）" || len(row.Children) != 0 || row.Action != (Action{Kind: "navigate", Target: "endpoints"}) {
		t.Fatalf("没有可执行的修复时直接跳管理页: %+v", row)
	}
}
