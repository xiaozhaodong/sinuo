package tray

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

func codexMenu(s Snapshot) Item {
	if s.CodexError != "" {
		return Item{Label: "Codex  读取失败", Icon: iconCodex, Children: []Item{info(s.CodexError).withIcon(iconAlert), manage("在主窗口管理账号池…", "account-pool")}}
	}
	if s.CodexModels {
		return Item{Label: "Codex  模型路由", Icon: iconCodex, Children: scopeNote("Codex", s.codexEntrySubscription(), codexBindings(s))}
	}
	label, pinned := "自动", int64(0)
	for _, account := range s.Accounts {
		if account.Pinned {
			pinned, label = account.ID, "固定 → "+account.Name
			break
		}
		if account.ID == s.RecentAccountID {
			label = "自动 → " + account.Name
		}
	}
	items := []Item{choice("自动调度（按编排）", pinned == 0, pinned == 0, Action{Kind: "codex", Mode: "auto"}).withIcon(iconAuto), separator(),
		operationMenu("manual_fixed", accountChoices(s.Accounts, pinned, s.RecentAccountID))}
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

func accountChoices(accounts []Account, pinned, recent int64) []Item {
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
		authDisabled := strings.ToLower(strings.TrimSpace(a.State)) == "disabled_auth"
		if authDisabled {
			label += " · 鉴权失效"
		} else if a.Cooling {
			label += " · 冷却中"
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
		items = append(items, Item{Label: b.Name + "  " + routeLabel(b.Mode, target, ""), Icon: iconModel, Children: modelAccountChoices(b, accounts)})
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

func modelAccountChoices(b Binding, accounts map[int64]Account) []Item {
	items := []Item{choice("自动调度", b.Mode == "auto", b.Mode == "auto", Action{Kind: "codex_binding", ID: b.ID, Mode: "auto"}).withIcon(iconAuto), separator()}
	for _, mode := range []string{"manual_preferred", "manual_fixed"} {
		targets := []Item{}
		for _, id := range b.AccountIDs {
			account, exists := accounts[id]
			paused := false
			if enabled, ok := b.MemberEnabled[id]; ok {
				paused = !enabled
			}
			label := fmt.Sprintf("%s #%d · %s", modelAccountName(accounts, id), id, modelAccountState(account, exists, paused))
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

func modelAccountState(a Account, exists, paused bool) string {
	if paused {
		return "未参与自动调度"
	}
	if !exists {
		return "账号不存在"
	}
	if !a.Enabled || a.State == "disabled_auth" {
		return "已停用"
	}
	if !a.HasCredential {
		return "无凭据"
	}
	if a.Cooling {
		return "冷却中"
	}
	return "可参与"
}
