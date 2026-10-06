package tray

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

func codexMenu(s Snapshot) Item {
	if s.CodexError != "" {
		return Item{Label: "Codex  读取失败", Icon: iconCodex, Children: []Item{info(s.CodexError).withIcon(iconAlert), manage("在主窗口管理账号池…", "account-pool")}}
	}
	if s.CodexModels {
		return Item{Label: "Codex  模型路由", Icon: iconCodex, Children: scopeNote("Codex", s.codexEntrySubscription(), codexBindings(s))}
	}
	label, pinned := "自动", int64(0)
	if account, ok := s.currentCodexAccount(); ok {
		label = "自动 → " + account.Name
		if account.Pinned {
			label, pinned = "固定 → "+account.Name, account.ID
		}
		if name, left, ok := tightestWindow(account); ok {
			label += fmt.Sprintf(" · %s 剩 %.0f%%", name, left)
		}
	}
	items := []Item{choice("自动调度（按编排）", pinned == 0, pinned == 0, Action{Kind: "codex", Mode: "auto"}).withIcon(iconAuto), separator(),
		operationMenu("manual_fixed", accountChoices(s.Accounts, pinned, s.RecentAccountID, s.Now))}
	if len(s.Accounts) == 0 {
		items = []Item{info("无可用 Codex 账号").withIcon(iconAlert)}
	}
	items = append(items, separator(), manage("在主窗口管理账号池…", "account-pool"))
	return Item{Label: "Codex  " + label, Icon: iconCodex, Children: scopeNote("Codex", s.codexEntrySubscription(), items)}
}

func accountGroup(a Account) int {
	switch strings.ToLower(strings.TrimSpace(a.Group)) {
	case "primary", "主组":
		return 0
	case "backup", "备组":
		return 1
	case "cold", "冷备":
		return 2
	}
	if a.Priority <= 10 {
		return 0
	}
	if a.Priority <= 20 {
		return 1
	}
	return 2
}

func accountChoices(accounts []Account, pinned, recent int64, now time.Time) []Item {
	sorted := append([]Account(nil), accounts...)
	order := nameOrder()
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if accountGroup(a) != accountGroup(b) {
			return accountGroup(a) < accountGroup(b)
		}
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		return order.CompareString(strconv.FormatInt(a.ID, 10), strconv.FormatInt(b.ID, 10)) < 0
	})
	items, previous := []Item{}, -1
	for _, a := range sorted {
		group := accountGroup(a)
		if group != previous {
			items = append(items, info([]string{"── 主组 ──", "── 备组 ──", "── 冷备 ──"}[group]))
			previous = group
		}
		label := a.Name + "  " + accountQuota(a)
		if a.ID == recent && a.ID != pinned {
			label += " · 最近命中"
		}
		if !a.Enabled {
			label += " · 已停用"
		}
		authDisabled := strings.ToLower(strings.TrimSpace(a.State)) == accountAuthDisabled
		if authDisabled {
			label += " · 鉴权失效"
		} else if a.Cooling {
			label += " · " + withCooldown(cooldownStatus, a.CooldownUntil, now)
		}
		items = append(items, choice(label, a.ID == pinned, !a.Enabled || authDisabled, Action{Kind: "codex", Mode: "manual_fixed", ID: a.ID}))
	}
	return items
}

func accountQuota(a Account) string {
	if a.Provider == "api_key" {
		return "5h / d7 无限额"
	}
	if strings.EqualFold(a.Plan, "free") {
		return "d7 " + remaining(a.WeeklyUsed)
	}
	return "5h " + remaining(a.FiveHourUsed) + " · d7 " + remaining(a.WeeklyUsed)
}

// currentCodexAccount：全局账号池模式下的当前账号，固定账号优先，否则取最近一次调度命中的账号。
func (s Snapshot) currentCodexAccount() (Account, bool) {
	var recent *Account
	for i, account := range s.Accounts {
		if account.Pinned {
			return account, true
		}
		if account.ID == s.RecentAccountID {
			recent = &s.Accounts[i]
		}
	}
	if recent == nil {
		return Account{}, false
	}
	return *recent, true
}

// tightestWindow 取剩余更少的额度窗口（相同时取 5h），与请求页角标同一口径；
// free 只有 d7，api_key 无限额与读数未知时返回 false。
func tightestWindow(a Account) (name string, left float64, ok bool) {
	if a.Provider == "api_key" {
		return "", 0, false
	}
	windows := []struct {
		name string
		used *float64
	}{{"5h", a.FiveHourUsed}, {"d7", a.WeeklyUsed}}
	if strings.EqualFold(a.Plan, "free") {
		windows = windows[1:]
	}
	for _, window := range windows {
		if window.used == nil {
			continue
		}
		if value := remainingPercent(*window.used); !ok || value < left {
			name, left, ok = window.name, value, true
		}
	}
	return name, left, ok
}

func codexBindings(s Snapshot) []Item {
	bindings := append([]Binding(nil), s.CodexBindings...)
	order := nameOrder()
	sort.SliceStable(bindings, func(i, j int) bool {
		if bindings[i].Enabled != bindings[j].Enabled {
			return bindings[i].Enabled
		}
		return order.CompareString(bindings[i].Name, bindings[j].Name) < 0
	})
	accounts := make(map[int64]Account, len(s.Accounts))
	for _, a := range s.Accounts {
		accounts[a.ID] = a
	}
	items := []Item{}
	for _, b := range bindings {
		if !b.Enabled {
			items = append(items, info(b.Name+" · 已停用").withIcon(iconModel))
			continue
		}
		target := ""
		if b.TargetID != 0 {
			target = modelAccountName(accounts, b.TargetID)
		}
		items = append(items, Item{Label: b.Name + "  " + routeLabel(b.Mode, target, ""), Icon: iconModel, Children: modelAccountChoices(b, accounts, s.Now)})
	}
	if len(items) == 0 {
		items = append(items, info("尚无模型绑定，匹配不到模型的请求将返回未分配错误").withIcon(iconAlert))
	}
	return append(items, separator(), manage("管理模型映射…", "codex-model-routing"))
}

func modelAccountName(accounts map[int64]Account, id int64) string {
	if account, ok := accounts[id]; ok && account.Name != "" {
		return account.Name
	}
	return fmt.Sprintf("账号 %d", id)
}

func modelAccountChoices(b Binding, accounts map[int64]Account, now time.Time) []Item {
	items := []Item{choice("自动调度", b.Mode == "auto", b.Mode == "auto", Action{Kind: "codex_binding", ID: b.ID, Mode: "auto"}).withIcon(iconAuto), separator()}
	for _, mode := range []string{"manual_preferred", "manual_fixed"} {
		targets := []Item{}
		for _, id := range b.AccountIDs {
			account, exists := accounts[id]
			paused := false
			if enabled, ok := b.MemberEnabled[id]; ok {
				paused = !enabled
			}
			label := fmt.Sprintf("%s #%d · %s", modelAccountName(accounts, id), id, modelAccountState(account, exists, paused, now))
			// 与 CodexModelRoutingSwitcher 一致：仅暂停成员不能优选，其他状态由写入 API 校验。
			targets = append(targets, choice(label, b.Mode == mode && b.TargetID == id, mode == "manual_preferred" && paused,
				Action{Kind: "codex_binding", ID: b.ID, Mode: mode, Target: strconv.FormatInt(id, 10)}))
		}
		if len(targets) == 0 {
			targets = append(targets, info("未选择任何账号"))
		}
		items = append(items, operationMenu(mode, targets))
	}
	return items
}

func modelAccountState(a Account, exists, paused bool, now time.Time) string {
	if paused {
		return "未参与自动调度"
	}
	if !exists {
		return "账号不存在"
	}
	if !a.Enabled || a.State == accountAuthDisabled {
		return "已停用"
	}
	if !a.HasCredential {
		return "无凭据"
	}
	if a.Cooling {
		return withCooldown(cooldownStatus, a.CooldownUntil, now)
	}
	return "可参与"
}
