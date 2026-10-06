package tray

import "time"

// 降级行跳转的管理页文案，与各子菜单末尾的管理入口一致。
var problemPageLabels = map[string]string{
	"endpoints":            "在主窗口管理端点…",
	"account-pool":         "在主窗口管理账号池…",
	"claude-model-routing": "管理模型映射…",
	"codex-model-routing":  "管理模型映射…",
}

// problemItem：有就地修复时展开子菜单（先修复、后跳主窗口）；没有可执行的修复时点击直接跳到管理页。
// 只有一个冷却目标时直接给出「解除 X 的冷却」，多个时先选操作再选目标，与路由子菜单的组织方式一致。
func problemItem(p Problem, now time.Time) Item {
	var fixes []Item
	switch len(p.Cooling) {
	case 0:
	case 1:
		target := p.Cooling[0]
		fixes = append(fixes, Item{Label: withLeft("解除 "+target.Name+" 的冷却", target.Until, now), Icon: iconClearCooldown, Action: target.Action})
	default:
		targets := make([]Item, 0, len(p.Cooling))
		for _, target := range p.Cooling {
			targets = append(targets, Item{Label: withLeft(target.Name, target.Until, now), Action: target.Action})
		}
		fixes = append(fixes, Item{Label: "解除冷却", Icon: iconClearCooldown, Children: targets})
	}
	if p.Auto != nil {
		fixes = append(fixes, Item{Label: "改为自动调度", Icon: iconAuto, Action: *p.Auto})
	}
	if len(fixes) == 0 {
		return navigate(p.Reason+"（在主窗口查看）", p.Page).withIcon(iconAlert)
	}
	return Item{Label: p.Reason, Icon: iconAlert, Children: append(fixes, separator(), manage(problemPageLabels[p.Page], p.Page))}
}
