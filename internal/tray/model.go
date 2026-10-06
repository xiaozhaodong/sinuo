// Package tray 构建与桌面框架无关的菜单和退出状态，便于单独验证。
package tray

import (
	"fmt"
	"math"
	"time"

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
	Name, Status  string
	Priority      int
	Available     bool
	CooldownUntil time.Time // 只用于显示剩余时间；是否冷却以 Status 为准
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
	CooldownUntil                           time.Time
}

type Snapshot struct {
	// Now 是读取快照的时刻，冷却剩余时间按它计算，菜单构建不直接读系统时钟
	Now                                     time.Time
	Running, Recovery, Waiting, CloseToTray bool
	Address, StartupState, SettingsError    string
	Active                                  int
	TodayRequests, TodayFailed              int64
	TodayCost                               float64
	StatsError                              string
	// Location 是显示时间用的时区（配置的 timezone）；为空时用系统时区
	Location *time.Location
	// MenuBarLabel 是菜单栏文字模式；为空表示平台不支持（非 macOS）
	MenuBarLabel string
	// Failovers 是本次运行以来最近的故障转移，新的在前
	Failovers []FailoverEntry
	// 复制用的接入地址：主端口总有；Claude Code 入口只在启用且监听中时才有
	MainURL, EntryURL                                       string
	ClaudeMode, ClaudeTarget, ClaudeLastTarget, ClaudeError string
	ClaudeModels                                            bool
	Endpoints                                               []Endpoint
	ClaudeBindings                                          []Binding
	// Claude Code 入口：与主端口分开监听；EntryError 是配置读取失败，EntryListenError 是监听失败
	EntryEnabled, EntryRunning                          bool
	EntryMode, EntryQuota, EntryError, EntryListenError string
	EntryQuotaShort                                     string // 菜单栏文字用：剩余更少的窗口，如「5h 62%」
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
	return fmt.Sprintf("剩 %.0f%%", remainingPercent(*used))
}

func remainingPercent(used float64) float64 { return math.Max(0, math.Min(100, 100-used)) }

// cooldownLeft 返回冷却剩余时间（分钟向上取整）；截止时间未知或已过时为空。
func cooldownLeft(until, now time.Time) string {
	if until.IsZero() || now.IsZero() || !until.After(now) {
		return ""
	}
	left := until.Sub(now)
	if left < time.Minute {
		return "剩不到 1 分钟"
	}
	minutes := int(math.Ceil(left.Minutes()))
	if minutes < 60 {
		return fmt.Sprintf("剩 %d 分钟", minutes)
	}
	if minutes%60 == 0 {
		return fmt.Sprintf("剩 %d 小时", minutes/60)
	}
	return fmt.Sprintf("剩 %d 小时 %d 分钟", minutes/60, minutes%60)
}

// withLeft 在文案后附上冷却剩余时间，例如「解除 relay 的冷却（剩 3 分钟）」；剩余时间未知时原样返回。
func withLeft(label string, until, now time.Time) string {
	if left := cooldownLeft(until, now); left != "" {
		return label + "（" + left + "）"
	}
	return label
}

// withCooldown 只给「冷却中」状态附上剩余时间，其他状态原样返回。
func withCooldown(status string, until, now time.Time) string {
	if status != cooldownStatus {
		return status
	}
	return withLeft(status, until, now)
}

// 与 localeCompare 一样使用语言排序，避免纯字节排序把大写名称排到小写前面。
func nameOrder() *collate.Collator { return collate.New(language.Und) }
