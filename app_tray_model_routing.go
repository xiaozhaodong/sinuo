package main

import (
	"errors"
	"fmt"

	"cc-forwarder/internal/tray"
)

var errTrayActionSkipped = errors.New("托盘操作已跳过")

type trayModelRoutingAPI interface {
	GetModelRoutingSettings() (ModelRoutingSettingsInfo, error)
	UpdateModelRoutingSettings(UpdateModelRoutingSettingsInput) (ModelRoutingSettingsInfo, error)
	ListModelRouteBindings() ([]ModelRouteBindingInfo, error)
	ListCodexModelRouteBindings() ([]CodexModelRouteBindingInfo, error)
}

// setTrayModelRouting 携带目标状态，过期菜单不取反；只提交对应字段，另一套策略保持原值。
func setTrayModelRouting(api trayModelRoutingAPI, action tray.Action, confirm trayConfirmFunc) error {
	if action.Target != "claude" && action.Target != "codex" {
		return fmt.Errorf("未知模型路由目标: %s", action.Target)
	}
	if action.Mode != "on" && action.Mode != "off" {
		return fmt.Errorf("未知模型路由开关状态: %s", action.Mode)
	}
	settings, err := api.GetModelRoutingSettings()
	if err != nil {
		return err
	}
	current, enabled := settings.ClaudeEnabled, action.Mode == "on"
	if action.Target == "codex" {
		current = settings.CodexEnabled
	}
	if current == enabled {
		return errTrayActionSkipped
	}
	if enabled {
		question, err := trayModelRoutingEnableConfirm(api, action.Target)
		if err != nil {
			return err
		}
		if question.Confirm != "" && !confirm(question) {
			return errTrayActionSkipped
		}
	}
	input := UpdateModelRoutingSettingsInput{ClaudeEnabled: &enabled}
	if action.Target == "codex" {
		input = UpdateModelRoutingSettingsInput{CodexEnabled: &enabled}
	}
	_, err = api.UpdateModelRoutingSettings(input)
	return err
}

func trayModelRoutingEnableConfirm(api trayModelRoutingAPI, target string) (trayConfirm, error) {
	question := trayConfirm{Title: "开启模型路由", Confirm: "仍要开启", Destructive: true,
		Message: "还没有任何模型映射，开启后所有 Claude 请求都会返回未分配错误。仍要开启吗？"}
	if target == "codex" {
		bindings, err := api.ListCodexModelRouteBindings()
		if err != nil {
			return trayConfirm{}, err
		}
		for _, b := range bindings {
			if b.Enabled {
				return trayConfirm{}, nil
			}
		}
		question.Title = "开启 Codex 模型路由"
		question.Message = "当前没有启用中的 Codex 模型路由，开启后未绑定模型会返回 422。仍要开启吗？"
		return question, nil
	}
	bindings, err := api.ListModelRouteBindings()
	if err != nil {
		return trayConfirm{}, err
	}
	for _, b := range bindings {
		if b.Enabled {
			return trayConfirm{}, nil
		}
	}
	if len(bindings) > 0 {
		question.Message = fmt.Sprintf("当前 %d 条映射全部处于停用状态，开启后所有 Claude 请求都会返回未分配错误。仍要开启吗？", len(bindings))
	}
	return question, nil
}
