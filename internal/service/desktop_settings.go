package service

import "sinuo/internal/store"

const CategoryDesktop = "desktop"

func desktopDefaults() []*store.SettingRecord {
	return []*store.SettingRecord{
		{Category: CategoryDesktop, Key: "close_to_tray", Value: "true", ValueType: ValueTypeBool,
			Label: "关闭窗口后保留在菜单栏", Description: "关闭主窗口后代理继续运行，可从系统托盘重新打开", DisplayOrder: 1},
		{Category: CategoryDesktop, Key: "request_animation", Value: "true", ValueType: ValueTypeBool,
			Label: "请求进行中显示菜单栏动画", Description: "有请求在跑时菜单栏图标播放流光；系统开启「减少动态效果」时固定显示一帧（仅 macOS）", DisplayOrder: 2},
		{Category: CategoryDesktop, Key: "system_notifications", Value: "true", ValueType: ValueTypeBool,
			Label: "窗口隐藏时用系统通知提示异常", Description: "主窗口隐藏或最小化时，故障转移与「请求一定会失败」会发系统通知，同类提示 10 分钟内只发一次（仅 macOS）", DisplayOrder: 3},
		{Category: CategoryDesktop, Key: "menubar_label", Value: "off", ValueType: ValueTypeString,
			Label: "菜单栏文字", Description: "在菜单栏图标旁显示今日花费、Claude 订阅剩余或 Codex 当前账号剩余；没有数据时只显示图标（仅 macOS）", DisplayOrder: 4},
		{Category: CategoryDesktop, Key: "tray_hint_shown", Value: "false", ValueType: ValueTypeBool,
			Label: "已提示托盘驻留", Description: "内部标记，不在设置页展示", DisplayOrder: 9},
	}
}

// macOnlyDesktopSettings 只在 macOS 生效：其他平台的托盘动画、系统通知与菜单栏文字都未实现。
var macOnlyDesktopSettings = map[string]bool{"request_animation": true, "system_notifications": true, "menubar_label": true}

// SettingVisible 决定设置是否出现在设置页：桌面内部标记不展示，仅 macOS 生效的桌面设置在其他平台隐藏。
// 默认值仍在所有平台注册，托盘按默认值读取不会出错。
func SettingVisible(category, key, goos string) bool {
	if category != CategoryDesktop {
		return true
	}
	if key == "tray_hint_shown" {
		return false
	}
	return goos == "darwin" || !macOnlyDesktopSettings[key]
}
