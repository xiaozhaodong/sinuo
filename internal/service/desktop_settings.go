package service

import "cc-forwarder/internal/store"

const CategoryDesktop = "desktop"

func desktopDefaults() []*store.SettingRecord {
	return []*store.SettingRecord{
		{Category: CategoryDesktop, Key: "close_to_tray", Value: "true", ValueType: ValueTypeBool,
			Label: "关闭窗口后保留在菜单栏", Description: "关闭主窗口后代理继续运行，可从系统托盘重新打开", DisplayOrder: 1},
		{Category: CategoryDesktop, Key: "request_animation", Value: "true", ValueType: ValueTypeBool,
			Label: "请求进行中显示菜单栏动画", Description: "有请求在跑时菜单栏图标播放流光；系统开启「减少动态效果」时固定显示一帧（仅 macOS）", DisplayOrder: 2},
		{Category: CategoryDesktop, Key: "tray_hint_shown", Value: "false", ValueType: ValueTypeBool,
			Label: "已提示托盘驻留", Description: "内部标记，不在设置页展示", DisplayOrder: 3},
	}
}
