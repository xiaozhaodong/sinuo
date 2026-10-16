package main

import (
	"fmt"
	"strconv"

	"sinuo/internal/tray"
)

// executeTrayAction 只调用现有写入方法，不直接操作服务或数据库；confirm 用于入口操作与无启用映射时开启模型路由的确认。
// 解除冷却与端点页、账号页一致，一键执行、不弹确认。
func (a *App) executeTrayAction(action tray.Action, confirm trayConfirmFunc) error {
	switch action.Kind {
	case "claude":
		if action.Mode == "auto" {
			_, err := a.ClearClaudeRoutingOverride()
			return err
		}
		_, err := a.SetClaudeRoutingOverride(SetClaudeRoutingOverrideInput{
			Mode: action.Mode, EndpointName: action.Target, FallbackEnabled: action.Mode == "manual_preferred",
		})
		return err
	case "claude_binding":
		return updateTrayClaudeBinding(a, action)
	case "codex_binding":
		return updateTrayCodexBinding(a, action)
	case "model_routing":
		return setTrayModelRouting(a, action, confirm)
	case "entry":
		_, err := a.SetClaudeCodeEntryMode(action.Mode)
		return err
	case "entry_enable", "entry_disable":
		return setTrayClaudeEntryEnabled(a, action.Kind == "entry_enable", confirm)
	case "codex_entry":
		return switchTrayCodexEntryMode(a, action, confirm)
	case "codex_entry_enable", "codex_entry_disable":
		return setTrayCodexEntryEnabled(a, action, action.Kind == "codex_entry_enable", confirm)
	case "codex":
		return a.executeTrayCodexAction(action)
	case "endpoint_cooldown":
		return a.ClearEndpointCooldown(action.Target)
	case "account_cooldown":
		return a.ClearUpstreamAccountCooldown(action.ID)
	case "menubar_label":
		if tray.NormalizeMenuBarMode(action.Mode) != action.Mode {
			return fmt.Errorf("未知的菜单栏文字模式: %s", action.Mode)
		}
		return a.UpdateSetting(UpdateSettingInput{Category: "desktop", Key: "menubar_label", Value: action.Mode})
	case "close_to_tray":
		current, err := a.readTraySetting("close_to_tray", true)
		if err != nil {
			return err
		}
		return a.UpdateSetting(UpdateSettingInput{Category: "desktop", Key: "close_to_tray", Value: strconv.FormatBool(!current)})
	}
	return fmt.Errorf("未知托盘操作: %s", action.Kind)
}

func (a *App) executeTrayCodexAction(action tray.Action) error {
	if action.Mode == "auto" {
		result, err := a.EnableAutomaticAccountSelection()
		return trayActionResult(result.Success, result.Message, err)
	}
	result, err := a.PinUpstreamAccountSelection(action.ID)
	return trayActionResult(result.Success, result.Message, err)
}

func trayActionResult(success bool, message string, err error) error {
	if err != nil {
		return err
	}
	if !success {
		return fmt.Errorf("操作未生效: %s", message)
	}
	return nil
}

type trayBindingAPI interface {
	ListModelRouteBindings() ([]ModelRouteBindingInfo, error)
	UpdateModelRouteBinding(int64, ModelRouteBindingInput) error
	ListCodexModelRouteBindings() ([]CodexModelRouteBindingInfo, error)
	UpdateCodexModelRouteBinding(int64, CodexModelRouteBindingInput) error
}

func updateTrayClaudeBinding(api trayBindingAPI, action tray.Action) error {
	bindings, err := api.ListModelRouteBindings()
	if err != nil {
		return err
	}
	for _, b := range bindings {
		if b.AssignmentID != action.ID {
			continue
		}
		if !b.Enabled {
			return fmt.Errorf("模型映射已停用，请到主窗口启用")
		}
		names := make([]string, 0, len(b.Endpoints))
		for _, ep := range b.Endpoints {
			names = append(names, ep.Name)
		}
		target := action.Target
		if action.Mode == "auto" {
			target = ""
		}
		return api.UpdateModelRouteBinding(b.AssignmentID, ModelRouteBindingInput{
			Kind: b.Kind, Model: b.Model, Endpoints: names, Enabled: b.Enabled, Note: b.Note,
			RouteMode: action.Mode, RouteTarget: target,
		})
	}
	return fmt.Errorf("模型映射已被删除，请重新打开托盘菜单")
}

func updateTrayCodexBinding(api trayBindingAPI, action tray.Action) error {
	bindings, err := api.ListCodexModelRouteBindings()
	if err != nil {
		return err
	}
	for _, b := range bindings {
		if b.AssignmentID != action.ID {
			continue
		}
		if !b.Enabled {
			return fmt.Errorf("模型绑定已停用，请到主窗口启用")
		}
		target := int64(0)
		if action.Mode != "auto" {
			target, err = strconv.ParseInt(action.Target, 10, 64)
			if err != nil || target <= 0 {
				return fmt.Errorf("无效的托盘账号目标")
			}
		}
		return api.UpdateCodexModelRouteBinding(b.AssignmentID, CodexModelRouteBindingInput{
			Kind: b.Kind, Model: b.Model, AccountIDs: b.AccountIDs, MemberEnabled: b.MemberEnabled, Enabled: b.Enabled, Note: b.Note,
			RouteMode: action.Mode, RouteTargetID: target,
		})
	}
	return fmt.Errorf("模型绑定已被删除，请重新打开托盘菜单")
}
