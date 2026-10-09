package tray

import "fmt"

// SwitchReceipt 只描述用户主动切换的策略；名称由 App 层在写入成功后解析。
type SwitchReceipt struct {
	Kind, Mode, Subject, Target string
	ID                          int64
}

// IsSwitchAction 与回执文案共用动作范围，带确认框的操作不在其中。
func IsSwitchAction(kind string) bool {
	_, ok := switchReceiptBase(kind)
	return ok
}

// ReceiptNotice 使用对象级固定 ID，让同一对象的下一次切换替换上一条通知。
func ReceiptNotice(r SwitchReceipt) (Notice, bool) {
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
	default:
		return Notice{}, false
	}
	return notice, true
}
