package tray

import (
	"fmt"
	"slices"
	"time"
)

type IconState int

const (
	IconNormal IconState = iota
	IconDegraded
	IconStopped
)

// IconStatus 是托盘图标状态；Reason 只在降级时给出，写进悬停提示。
type IconStatus struct {
	State  IconState
	Reason string
}

// Problem 是一侧（Claude / Codex）请求一定会失败的原因，以及能在托盘就地执行的修复。
// 图标、悬停提示与菜单里的降级行都出自 Diagnose，口径一致。
type Problem struct {
	Reason  string
	Page    string          // 在主窗口查看的页面（托盘导航目标）
	Auto    *Action         // 固定目标不可用时提供「改为自动调度」
	Cooling []CoolingTarget // 只因冷却而不可用的端点或账号，可就地解除
}

// CoolingTarget 是可就地解除冷却的端点或账号。
type CoolingTarget struct {
	Name   string
	Until  time.Time
	Action Action
}

const (
	cooldownStatus      = "冷却中"
	accountAuthDisabled = "disabled_auth"
)

// 映射成员中确定无法接收请求的状态（与 app 侧 trayEndpointStatus 的文案一致）；
// 「未参与自动调度」仍可被优选 / 固定命中，未知状态按可用处理，避免误报。
var bindingEndpointDown = map[string]bool{"端点缺失": true, "已停用": true, "硬关闭": true, cooldownStatus: true, "不可达": true}

// EvaluateIcon 只在请求一定会失败时判为降级，原因取 Diagnose 的第一个问题。
func EvaluateIcon(s Snapshot) IconStatus {
	if !s.Running || s.Recovery || s.StartupState != "" {
		return IconStatus{State: IconStopped}
	}
	if problems := Diagnose(s); len(problems) > 0 {
		return IconStatus{State: IconDegraded, Reason: problems[0].Reason}
	}
	return IconStatus{State: IconNormal}
}

// Diagnose 每侧最多给出一个问题（Claude 在前）；读取失败的一侧不参与判定，避免误报。
func Diagnose(s Snapshot) []Problem {
	if !s.Running || s.Recovery || s.StartupState != "" {
		return nil
	}
	var problems []Problem
	if s.ClaudeError == "" {
		problem := claudeGlobalDown(s)
		if s.ClaudeModels {
			problem = claudeBindingsDown(s.ClaudeBindings)
		}
		if problem != nil {
			problems = append(problems, *problem)
		}
	}
	if !s.codexPoolBypassed() && s.CodexError == "" {
		problem := codexPoolDown(s.Accounts)
		if s.CodexModels {
			problem = codexBindingsDown(s.CodexBindings, s.Accounts)
		}
		if problem != nil {
			problems = append(problems, *problem)
		}
	}
	return problems
}

func globalEndpointUsable(e Endpoint) bool {
	return e.Available && e.Status != cooldownStatus && e.Status != "不健康"
}

// endpointCooling 列出只因冷却而不可用的端点；硬关闭的端点解除冷却也无济于事，不列出。
func endpointCooling(endpoints []Endpoint, global bool) []CoolingTarget {
	var targets []CoolingTarget
	for _, e := range endpoints {
		if e.Status != cooldownStatus || (global && !e.Available) {
			continue
		}
		targets = append(targets, CoolingTarget{Name: e.Name, Until: e.CooldownUntil,
			Action: Action{Kind: "endpoint_cooldown", Target: e.Name}})
	}
	return targets
}

// claudeGlobalDown：固定模式只发往目标端点；其余模式要求至少一个端点可用。没有端点时不报警。
func claudeGlobalDown(s Snapshot) *Problem {
	if s.ClaudeMode == "manual_fixed" {
		index := slices.IndexFunc(s.Endpoints, func(e Endpoint) bool { return e.Name == s.ClaudeTarget })
		if index >= 0 && globalEndpointUsable(s.Endpoints[index]) {
			return nil
		}
		problem := &Problem{Reason: fmt.Sprintf("Claude 固定端点 %s 不可用", s.ClaudeTarget), Page: "endpoints",
			Auto: &Action{Kind: "claude", Mode: "auto"}}
		if index >= 0 {
			problem.Cooling = endpointCooling(s.Endpoints[index:index+1], true)
		}
		return problem
	}
	if len(s.Endpoints) == 0 || slices.ContainsFunc(s.Endpoints, globalEndpointUsable) {
		return nil
	}
	return &Problem{Reason: "Claude 没有可用端点", Page: "endpoints", Cooling: endpointCooling(s.Endpoints, true)}
}

func claudeBindingsDown(bindings []Binding) *Problem {
	for _, b := range bindings {
		if !b.Enabled {
			continue
		}
		targetUp, anyAuto := false, false
		for _, e := range b.Endpoints {
			if e.Name == b.Target && !bindingEndpointDown[e.Status] {
				targetUp = true
			}
			if e.Status == "" {
				anyAuto = true
			}
		}
		switch {
		case b.Mode == "manual_fixed" && !targetUp:
			target := slices.DeleteFunc(slices.Clone(b.Endpoints), func(e Endpoint) bool { return e.Name != b.Target })
			return &Problem{Reason: fmt.Sprintf("模型映射 %s：固定端点 %s 不可用", b.Name, b.Target), Page: "claude-model-routing",
				Auto: &Action{Kind: "claude_binding", ID: b.ID, Mode: "auto"}, Cooling: endpointCooling(target, false)}
		case b.Mode != "manual_fixed" && !targetUp && !anyAuto:
			return &Problem{Reason: fmt.Sprintf("模型映射 %s：没有可用端点", b.Name), Page: "claude-model-routing",
				Cooling: endpointCooling(b.Endpoints, false)}
		}
	}
	return nil
}

// accountUsable 与调度器的硬约束一致：停用、缺凭据、鉴权失效、冷却中都不可调度。
func accountUsable(a Account) bool {
	return a.Enabled && a.HasCredential && a.State != accountAuthDisabled && !a.Cooling
}

// accountCooling 列出只因冷却而不可调度的账号；停用、缺凭据、鉴权失效的账号解除冷却也无济于事。
func accountCooling(accounts []Account) []CoolingTarget {
	var targets []CoolingTarget
	for _, a := range accounts {
		if !a.Cooling || !a.Enabled || !a.HasCredential || a.State == accountAuthDisabled {
			continue
		}
		targets = append(targets, CoolingTarget{Name: a.Name, Until: a.CooldownUntil,
			Action: Action{Kind: "account_cooldown", ID: a.ID}})
	}
	return targets
}

// codexPoolDown：固定账号不可用时调度会自动让出，只有整池没有可调度账号才判降级。
func codexPoolDown(accounts []Account) *Problem {
	if len(accounts) == 0 || slices.ContainsFunc(accounts, accountUsable) {
		return nil
	}
	return &Problem{Reason: "Codex 没有可调度账号", Page: "account-pool", Cooling: accountCooling(accounts)}
}

func codexBindingsDown(bindings []Binding, accounts []Account) *Problem {
	byID := make(map[int64]Account, len(accounts))
	for _, a := range accounts {
		byID[a.ID] = a
	}
	for _, b := range bindings {
		if !b.Enabled {
			continue
		}
		if b.Mode == "manual_fixed" {
			target, exists := byID[b.TargetID]
			if exists && accountUsable(target) {
				continue
			}
			problem := &Problem{Reason: fmt.Sprintf("Codex 模型映射 %s：固定账号 %s 不可用", b.Name, modelAccountName(byID, b.TargetID)),
				Page: "codex-model-routing", Auto: &Action{Kind: "codex_binding", ID: b.ID, Mode: "auto"}}
			if exists {
				problem.Cooling = accountCooling([]Account{target})
			}
			return problem
		}
		var members []Account
		for _, id := range b.AccountIDs {
			if account, exists := byID[id]; exists && b.MemberEnabled[id] {
				members = append(members, account)
			}
		}
		if !slices.ContainsFunc(members, accountUsable) {
			return &Problem{Reason: fmt.Sprintf("Codex 模型映射 %s：没有可调度账号", b.Name), Page: "codex-model-routing",
				Cooling: accountCooling(members)}
		}
	}
	return nil
}
