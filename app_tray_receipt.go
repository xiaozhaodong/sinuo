package main

import (
	"errors"
	"fmt"
	"strconv"

	"cc-forwarder/internal/tray"
)

type trayActionAPI interface {
	executeTrayAction(tray.Action, trayConfirmFunc) error
}

type trayReceiptAPI interface {
	ListModelRouteBindings() ([]ModelRouteBindingInfo, error)
	ListCodexModelRouteBindings() ([]CodexModelRouteBindingInfo, error)
	GetUpstreamAccounts() ([]UpstreamAccountInfo, error)
}

type receiptChannel int

const (
	receiptNone receiptChannel = iota
	receiptToast
	receiptSystem
)

type trayReceiptDelivery interface {
	receiptClosed() bool
	receiptState() (focused, visible, system bool)
	deliverReceipt(receiptChannel, tray.Notice)
}

// executeTrayActionWithReceipt 保留原写入结果；读名称与投递独立运行，不占 actionMu。
func executeTrayActionWithReceipt(api trayActionAPI, action tray.Action, confirm trayConfirmFunc, send func(tray.Action)) error {
	if err := api.executeTrayAction(action, confirm); err != nil {
		if errors.Is(err, errTrayActionSkipped) {
			return nil
		}
		return err
	}
	if tray.IsSwitchAction(action.Kind) {
		go send(action)
	}
	return nil
}

func chooseReceiptChannel(focused, visible, system bool) receiptChannel {
	if focused {
		return receiptToast
	}
	if system {
		return receiptSystem
	}
	if visible {
		return receiptToast
	}
	return receiptNone
}

func (t *appTray) sendSwitchReceipt(action tray.Action) {
	sendTraySwitchReceipt(t.app, t, action)
}

func sendTraySwitchReceipt(api trayReceiptAPI, delivery trayReceiptDelivery, action tray.Action) {
	if delivery.receiptClosed() || !tray.IsSwitchAction(action.Kind) {
		return
	}
	notice, ok := tray.ReceiptNotice(resolveSwitchReceipt(api, action))
	if !ok || delivery.receiptClosed() {
		return
	}
	focused, visible, system := delivery.receiptState()
	channel := chooseReceiptChannel(focused, visible, system)
	if channel != receiptNone && !delivery.receiptClosed() {
		delivery.deliverReceipt(channel, notice)
	}
}

func (t *appTray) receiptClosed() bool { return t.closed.Load() }

// receiptState 会查询主线程上的窗口状态，只能从回执 goroutine 调用。
func (t *appTray) receiptState() (focused, visible, system bool) {
	visible = t.window != nil && t.window.IsVisible() && !t.window.IsMinimised()
	focused = visible && t.window.IsFocused()
	system = t.notify != nil && t.notify.service.Load() != nil && t.notify.enabled.Load()
	return
}

func (t *appTray) deliverReceipt(channel receiptChannel, notice tray.Notice) {
	if t.closed.Load() {
		return
	}
	switch channel {
	case receiptToast:
		t.app.emitNotification("success", notice.Title, notice.Body)
	case receiptSystem:
		if t.notify != nil && t.notify.enabled.Load() {
			t.notify.send(notice) // 已在后台，复用最长 5 秒的发送，不进入故障转移节流器。
		}
	}
}

func resolveSwitchReceipt(api trayReceiptAPI, action tray.Action) tray.SwitchReceipt {
	r := tray.SwitchReceipt{Kind: action.Kind, Mode: action.Mode, ID: action.ID}
	if action.Mode != "auto" {
		r.Target = action.Target
	}
	switch action.Kind {
	case "claude_binding":
		r.Subject = trayReceiptClaudeSubject(api, action.ID)
	case "codex":
		if action.Mode != "auto" {
			r.Target = trayReceiptAccountName(api, action.ID)
		}
	case "codex_binding":
		r.Subject = trayReceiptCodexSubject(api, action.ID)
		if action.Mode != "auto" {
			id, err := strconv.ParseInt(action.Target, 10, 64)
			if err == nil && id > 0 {
				r.Target = trayReceiptAccountName(api, id)
			} else {
				r.Target = "账号 #" + action.Target
			}
		}
	}
	return r
}

func trayReceiptClaudeSubject(api trayReceiptAPI, id int64) string {
	if bindings, err := api.ListModelRouteBindings(); err == nil {
		for _, b := range bindings {
			if b.AssignmentID == id {
				if b.Kind == "web_tools" {
					return "联网工具"
				}
				if b.Model != "" {
					return b.Model
				}
			}
		}
	}
	return fmt.Sprintf("模型映射 #%d", id)
}

func trayReceiptCodexSubject(api trayReceiptAPI, id int64) string {
	if bindings, err := api.ListCodexModelRouteBindings(); err == nil {
		for _, b := range bindings {
			if b.AssignmentID == id && b.Model != "" {
				return b.Model
			}
		}
	}
	return fmt.Sprintf("模型绑定 #%d", id)
}

func trayReceiptAccountName(api trayReceiptAPI, id int64) string {
	if accounts, err := api.GetUpstreamAccounts(); err == nil {
		for _, account := range accounts {
			if account.ID == id && account.AccountName != "" {
				return account.AccountName
			}
		}
	}
	return fmt.Sprintf("账号 #%d", id)
}
