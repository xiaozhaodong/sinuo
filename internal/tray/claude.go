package tray

import (
	"sort"
	"time"
)

func claudeMenu(s Snapshot) Item {
	if s.ClaudeError != "" {
		return Item{Label: "Claude  读取失败", Icon: iconClaude, Children: []Item{info(s.ClaudeError).withIcon(iconAlert), manage("在主窗口管理端点…", "endpoints")}}
	}
	if s.ClaudeModels {
		return Item{Label: "Claude  模型路由", Icon: iconClaude, Children: scopeNote("Claude Code", s.claudeEntrySubscription(), claudeBindings(s.ClaudeBindings, s.Now))}
	}
	endpoints := make([]Endpoint, 0, len(s.Endpoints))
	for _, ep := range s.Endpoints {
		if ep.Available {
			endpoints = append(endpoints, ep)
		}
	}
	order := nameOrder()
	sort.SliceStable(endpoints, func(i, j int) bool {
		if endpoints[i].Priority != endpoints[j].Priority {
			return endpoints[i].Priority < endpoints[j].Priority
		}
		return order.CompareString(endpoints[i].Name, endpoints[j].Name) < 0
	})
	items := endpointChoices(Binding{Mode: s.ClaudeMode, Target: s.ClaudeTarget, LastTarget: s.ClaudeLastTarget, Endpoints: endpoints}, "claude", s.Now)
	if len(endpoints) == 0 {
		items = []Item{info("无可用 Claude 端点").withIcon(iconAlert)}
	}
	items = append(items, separator(), manage("在主窗口管理端点…", "endpoints"))
	return Item{Label: "Claude  " + routeLabel(s.ClaudeMode, s.ClaudeTarget, s.ClaudeLastTarget), Icon: iconClaude,
		Children: scopeNote("Claude Code", s.claudeEntrySubscription(), items)}
}

func claudeBindings(bindings []Binding, now time.Time) []Item {
	sorted := append([]Binding(nil), bindings...)
	order := nameOrder()
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if (a.Kind == "web_tools") != (b.Kind == "web_tools") {
			return a.Kind == "web_tools"
		}
		if a.Enabled != b.Enabled {
			return a.Enabled
		}
		return order.CompareString(a.Name, b.Name) < 0
	})
	items := []Item{}
	for _, b := range sorted {
		label, icon := b.Name+"  "+routeLabel(b.Mode, b.Target, b.LastTarget), iconModel
		if b.Kind == "web_tools" {
			icon = iconWebTools
		}
		if !b.Enabled {
			items = append(items, info(b.Name+" · 已停用").withIcon(icon))
			continue
		}
		items = append(items, Item{Label: label, Icon: icon, Children: endpointChoices(b, "claude_binding", now)})
	}
	if len(items) == 0 {
		items = append(items, info("尚无模型映射，Claude 请求将返回未分配错误").withIcon(iconAlert))
	}
	return append(items, separator(), manage("管理模型映射…", "claude-model-routing"))
}

func endpointChoices(b Binding, kind string, now time.Time) []Item {
	auto := b.Mode == "auto" || b.Mode == ""
	items := []Item{choice("自动调度", auto, auto, Action{Kind: kind, ID: b.ID, Mode: "auto"}).withIcon(iconAuto), separator()}
	for _, mode := range []string{"manual_preferred", "manual_fixed"} {
		targets := []Item{}
		// 模型成员保留 API 顺序；健康、冷却、硬停用只标注，不增加页面没有的禁用条件。
		for _, ep := range b.Endpoints {
			label := ep.Name
			if ep.Name == b.LastTarget {
				label += " · 最近命中"
			}
			if ep.Status != "" {
				label += " · " + withCooldown(ep.Status, ep.CooldownUntil, now)
			}
			targets = append(targets, choice(label, b.Mode == mode && b.Target == ep.Name, false, Action{Kind: kind, ID: b.ID, Mode: mode, Target: ep.Name}))
		}
		if len(targets) == 0 {
			targets = append(targets, info("未选择任何端点"))
		}
		items = append(items, operationMenu(mode, targets))
	}
	return items
}
