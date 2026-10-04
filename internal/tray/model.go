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
	Label, Shortcut, Icon                   string
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
	// Claude Code 入口：与主端口分开监听；EntryError 是配置读取失败，EntryListenError 是监听失败
	EntryEnabled, EntryRunning                          bool
	EntryMode, EntryQuota, EntryError, EntryListenError string
	EntryPort                                           int
	CodexModels                                         bool
	CodexBindings                                       []Binding
	Accounts                                            []Account
	RecentAccountID                                     int64
	CodexError                                          string
	// Codex 独立入口：State 见 CodexEntry* 常量；Label 是不可操作时的状态文案；
	// Revision 随写入动作带回，执行时与最新配置比对，防止按过期菜单写入
	CodexEntryState, CodexEntryLabel, CodexEntryMode string
	CodexEntryPort                                   int
	CodexEntryRevision                               int64
}

const (
	CodexEntryHidden      = ""            // 未初始化：不显示
	CodexEntryReady       = "ready"       // 运行中且配置与运行态一致：可切换模式、可停用
	CodexEntryStopped     = "stopped"     // 用户已停用且已停稳：可启用
	CodexEntryUnavailable = "unavailable" // 启动中、失败、读取失败等：只读，跳主窗口
)

// codexPoolBypassed：Codex 独立入口在用且不是账号池模式时，账号池只服务主端口，
// 托盘图标不因它降级（与第 2 期口径一致）。
func (s Snapshot) codexPoolBypassed() bool {
	switch s.CodexEntryState {
	case CodexEntryHidden, CodexEntryStopped:
		return false
	case CodexEntryReady:
		return s.CodexEntryMode != "pool"
	}
	return true
}

func info(label string) Item { return Item{Label: label, Disabled: true} }
func separator() Item        { return Item{Separator: true} }
func navigate(label, target string) Item {
	return Item{Label: label, Action: Action{Kind: "navigate", Target: target}}
}

// manage 是跳到主窗口管理页的入口，统一用设置图标。
func manage(label, target string) Item { return navigate(label, target).withIcon(iconManage) }

func choice(label string, checked, disabled bool, action Action) Item {
	return Item{Label: label, Checked: checked, Checkable: true, Disabled: disabled, Action: action}
}

// operationMenu 是「优选到 / 固定到」的目标子菜单；目标项本身不带图标，状态由勾选表达。
func operationMenu(mode string, targets []Item) Item {
	if mode == "manual_fixed" {
		return Item{Label: "固定到", Icon: iconPin, Children: targets}
	}
	return Item{Label: "优选到", Icon: iconPrefer, Children: targets}
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
