package tray

import "fmt"

// SwitchReceipt 只描述用户主动切换的策略；名称由 App 层在写入成功后解析。
type SwitchReceipt struct {
	Kind, Mode, Subject, Target string
	ID                          int64
}

// IsSwitchAction 与回执文案共用动作范围，取消或跳过的开关动作由 App 层过滤。
func IsSwitchAction(kind string) bool {
	_, ok := switchReceiptBase(kind)
	return ok
}

// ReceiptNotice 使用对象级固定 ID，让同一对象的下一次切换替换上一条通知。
func ReceiptNotice(r SwitchReceipt) (Notice, bool) {
	if r.Kind == "model_routing" {
		return modelRoutingReceipt(r)
	}
	notice, ok := switchReceiptBase(r.Kind)
	if !ok {
		return Notice{}, false
	}
	switch r.Mode {
	case "auto":
		notice.Body = "自动调度"
		if r.Kind == "codex" {
			notice.Body += "（按编排）"
		}
	case "manual_preferred":
		notice.Body = fmt.Sprintf("优选「%s」", r.Target)
	case "manual_fixed":
		notice.Body = fmt.Sprintf("固定「%s」", r.Target)
	default:
		return Notice{}, false
	}
	if r.Kind == "claude_binding" || r.Kind == "codex_binding" {
		notice.ID += fmt.Sprintf(":%d", r.ID)
		notice.Body = r.Subject + "：" + notice.Body
	}
	return notice, true
}

func modelRoutingReceipt(r SwitchReceipt) (Notice, bool) {
	name, page, body := "Claude", "endpoints", "恢复按全局端点策略转发"
	switch r.Target {
	case "claude":
	case "codex":
		name, page, body = "Codex", "account-pool", "恢复按账号池编排调度"
	default:
		return Notice{}, false
	}
	state := "关闭"
	switch r.Mode {
	case "on":
		state, page, body = "开启", r.Target+"-model-routing", "按模型分配上游，未分配的模型返回 422"
		if r.Target == "codex" {
			body = "按模型分配上游，未分配的模型返回 400"
		}
	case "off":
	default:
		return Notice{}, false
	}
	return Notice{ID: "switch:model_routing:" + r.Target, Title: name + " 模型路由已" + state, Body: body, Target: page}, true
}

func switchReceiptBase(kind string) (Notice, bool) {
	notice := Notice{ID: "switch:" + kind}
	switch kind {
	case "claude":
		notice.Title, notice.Target = "Claude 端点策略已切换", "endpoints"
	case "claude_binding":
		notice.Title, notice.Target = "Claude 模型路由已切换", "claude-model-routing"
	case "codex":
		notice.Title, notice.Target = "Codex 账号策略已切换", "account-pool"
	case "codex_binding":
		notice.Title, notice.Target = "Codex 模型路由已切换", "codex-model-routing"
	case "model_routing":
	default:
		return Notice{}, false
	}
	return notice, true
}
