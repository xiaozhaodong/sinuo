package tray

import (
	"fmt"
	"slices"
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

// 映射成员中确定无法接收请求的状态（与 app 侧 trayEndpointStatus 的文案一致）；
// 「未参与自动调度」仍可被优选 / 固定命中，未知状态按可用处理，避免误报。
var bindingEndpointDown = map[string]bool{"端点缺失": true, "已停用": true, "硬关闭": true, "冷却中": true, "不可达": true}

// EvaluateIcon 只在请求一定会失败时判为降级；读取失败的一侧不参与判定，避免误报。
func EvaluateIcon(s Snapshot) IconStatus {
	if !s.Running || s.Recovery || s.StartupState != "" {
		return IconStatus{State: IconStopped}
	}
	if s.ClaudeError == "" {
		reason := claudeGlobalDown(s)
		if s.ClaudeModels {
			reason = claudeBindingsDown(s.ClaudeBindings)
		}
		if reason != "" {
			return IconStatus{State: IconDegraded, Reason: reason}
		}
	}
	if !s.CodexReadOnly && s.CodexError == "" {
		reason := codexPoolDown(s.Accounts)
		if s.CodexModels {
			reason = codexBindingsDown(s.CodexBindings, s.Accounts)
		}
		if reason != "" {
			return IconStatus{State: IconDegraded, Reason: reason}
		}
	}
	return IconStatus{State: IconNormal}
}

func globalEndpointUsable(e Endpoint) bool {
	return e.Available && e.Status != "冷却中" && e.Status != "不健康"
}

// claudeGlobalDown：固定模式只发往目标端点；其余模式要求至少一个端点可用。没有端点时不报警。
func claudeGlobalDown(s Snapshot) string {
	if s.ClaudeMode == "manual_fixed" {
		for _, e := range s.Endpoints {
			if e.Name == s.ClaudeTarget && globalEndpointUsable(e) {
				return ""
			}
		}
		return fmt.Sprintf("Claude 固定端点 %s 不可用", s.ClaudeTarget)
	}
	if len(s.Endpoints) == 0 || slices.ContainsFunc(s.Endpoints, globalEndpointUsable) {
		return ""
	}
	return "Claude 没有可用端点"
}

func claudeBindingsDown(bindings []Binding) string {
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
			return fmt.Sprintf("模型映射 %s：固定端点 %s 不可用", b.Name, b.Target)
		case b.Mode != "manual_fixed" && !targetUp && !anyAuto:
			return fmt.Sprintf("模型映射 %s：没有可用端点", b.Name)
		}
	}
	return ""
}

// accountUsable 与调度器的硬约束一致：停用、缺凭据、鉴权失效、冷却中都不可调度。
func accountUsable(a Account) bool {
	return a.Enabled && a.HasCredential && a.State != "disabled_auth" && !a.Cooling
}

// codexPoolDown：固定账号不可用时调度会自动让出，只有整池没有可调度账号才判降级。
func codexPoolDown(accounts []Account) string {
	if len(accounts) == 0 || slices.ContainsFunc(accounts, accountUsable) {
		return ""
	}
	return "Codex 没有可调度账号"
}

func codexBindingsDown(bindings []Binding, accounts []Account) string {
	usable := make(map[int64]bool, len(accounts))
	for _, a := range accounts {
		usable[a.ID] = accountUsable(a)
	}
	for _, b := range bindings {
		if !b.Enabled {
			continue
		}
		if b.Mode == "manual_fixed" {
			if !usable[b.TargetID] {
				return fmt.Sprintf("Codex 模型映射 %s：固定账号不可用", b.Name)
			}
			continue
		}
		anyMember := false
		for _, id := range b.AccountIDs {
			if b.MemberEnabled[id] && usable[id] {
				anyMember = true
				break
			}
		}
		if !anyMember {
			return fmt.Sprintf("Codex 模型映射 %s：没有可调度账号", b.Name)
		}
	}
	return ""
}
