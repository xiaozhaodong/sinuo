// Package tray 构建与桌面框架无关的菜单和退出状态，便于单独验证。
package tray

import (
	"fmt"
	"math"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

// Action 只携带操作意图；App 执行时重新读取映射，不缓存写入载荷。
type Action struct {
	Kind, Mode, Target string
	ID                 int64
}

type Item struct {
	Label, Shortcut                         string
	Disabled, Checked, Checkable, Separator bool
	Action                                  Action
	Children                                []Item
}

type Endpoint struct {
	Name, Status string
	Priority     int
	Available    bool
}

type Binding struct {
	ID                                   int64
	Name, Kind, Mode, Target, LastTarget string
	Enabled                              bool
	Endpoints                            []Endpoint
	AccountIDs                           []int64
	MemberEnabled                        map[int64]bool
	TargetID                             int64
}

type Account struct {
	ID                                      int64
	Name, Group, State, Provider, Plan      string
	Priority                                int
	Enabled, Pinned, HasCredential, Cooling bool
	FiveHourUsed, WeeklyUsed                *float64
}

type Snapshot struct {
	Running, Recovery, Waiting, CloseToTray                 bool
	Address, StartupState, SettingsError                    string
	Active                                                  int
	TodayRequests                                           int64
	TodayCost                                               float64
	StatsError                                              string
	ClaudeMode, ClaudeTarget, ClaudeLastTarget, ClaudeError string
	ClaudeModels                                            bool
	Endpoints                                               []Endpoint
	ClaudeBindings                                          []Binding
	EntryEnabled                                            bool
	EntryMode, EntryQuota, EntryError                       string
	CodexModels                                             bool
	CodexBindings                                           []Binding
	Accounts                                                []Account
	RecentAccountID                                         int64
	CodexAccess, CodexError                                 string
	CodexReadOnly                                           bool
}

func info(label string) Item { return Item{Label: label, Disabled: true} }
func separator() Item        { return Item{Separator: true} }
func navigate(label, target string) Item {
	return Item{Label: label, Action: Action{Kind: "navigate", Target: target}}
}

func choice(label string, checked, disabled bool, action Action) Item {
	return Item{Label: label, Checked: checked, Checkable: true, Disabled: disabled, Action: action}
}

func routeLabel(mode, target, last string) string {
	label := "自动"
	if mode == "manual_fixed" {
		label = "固定"
	}
	if mode == "manual_preferred" {
		label = "优选"
	}
	if mode == "auto" || mode == "" {
		target = last
	}
	if target != "" {
		label += " → " + target
	}
	return label
}

func remaining(used *float64) string {
	if used == nil {
		return "未知"
	}
	return fmt.Sprintf("剩 %.0f%%", math.Max(0, math.Min(100, 100-*used)))
}

// 与 localeCompare 一样使用语言排序，避免纯字节排序把大写名称排到小写前面。
func nameOrder() *collate.Collator { return collate.New(language.Und) }
