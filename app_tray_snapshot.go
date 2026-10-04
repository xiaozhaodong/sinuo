package main

import (
	"fmt"
	"math"
	"net"
	"strconv"
	"time"

	"cc-forwarder/internal/tray"
)

func (t *appTray) snapshot() tray.Snapshot {
	a := t.app
	s := tray.Snapshot{Active: t.activeCount(), Waiting: t.quit.Waiting(), CloseToTray: true}
	migration := a.GetMigrationStatus()
	if migration.State != "ready" {
		s.Recovery = migration.State != "initializing"
		if !s.Recovery {
			s.StartupState = "正在启动"
		}
		return s
	}
	status := a.GetSystemStatus()
	s.Running = status.ProxyRunning
	s.Address = net.JoinHostPort(status.ProxyHost, strconv.Itoa(status.ProxyPort))
	if !s.Running {
		return s
	}
	var err error
	s.CloseToTray, err = a.readTraySetting("close_to_tray", true)
	s.SettingsError = trayError(err)
	t.readTrayStats(&s)
	routing, err := a.GetModelRoutingSettings()
	if err != nil {
		s.ClaudeError, s.CodexError = "模型路由状态读取失败: "+err.Error(), "模型路由状态读取失败: "+err.Error()
	} else {
		s.ClaudeModels, s.CodexModels = routing.ClaudeEnabled, routing.CodexEnabled
		t.readClaudeRoute(&s)
	}
	entry, err := a.GetClaudeCodeEntryStatus()
	s.EntryError = trayError(err)
	s.EntryEnabled, s.EntryRunning, s.EntryMode, s.EntryPort, s.EntryListenError = entry.Enabled, entry.Running, entry.Mode, entry.ListenPort, entry.Error
	if entry.Enabled && entry.Mode == "subscription" {
		s.EntryQuota = trayClaudeQuota(a.GetClaudeSubscriptionRateLimit(), time.Now())
	}
	t.readCodexEntry(&s)
	if s.CodexError == "" {
		t.readCodexRoute(&s)
	}
	return s
}

func trayError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (t *appTray) readTrayStats(s *tray.Snapshot) {
	if time.Since(t.statsAt) >= 30*time.Second {
		t.stats, t.statsErr = t.app.GetUsageSummary("", "")
		t.statsAt = time.Now()
	}
	s.TodayRequests, s.TodayCost, s.StatsError = t.stats.TodayRequests, t.stats.TodayCost, trayError(t.statsErr)
}

func (t *appTray) readClaudeRoute(s *tray.Snapshot) {
	if s.ClaudeModels {
		bindings, err := t.app.ListModelRouteBindings()
		s.ClaudeError = trayError(err)
		for _, b := range bindings {
			name := b.Model
			if b.Kind == "web_tools" {
				name = "联网工具"
			}
			binding := tray.Binding{ID: b.AssignmentID, Name: name, Kind: b.Kind, Enabled: b.Enabled,
				Mode: b.RouteMode, Target: b.RouteTarget, LastTarget: b.LastEffective}
			for _, ep := range b.Endpoints {
				binding.Endpoints = append(binding.Endpoints, tray.Endpoint{Name: ep.Name, Priority: ep.Priority, Available: ep.AvailabilityEnabled, Status: trayEndpointStatus(ep.Status)})
			}
			s.ClaudeBindings = append(s.ClaudeBindings, binding)
		}
		return
	}
	routing, err := t.app.GetClaudeRoutingState()
	if err != nil {
		s.ClaudeError = err.Error()
		return
	}
	s.ClaudeMode, s.ClaudeTarget, s.ClaudeLastTarget = routing.Mode, routing.EndpointName, routing.LastEffectiveEndpoint
	endpoints, err := t.app.GetEndpointRecords()
	s.ClaudeError = trayError(err)
	for _, ep := range endpoints {
		state := ""
		switch {
		case ep.InCooldown:
			state = "冷却中"
		case ep.NeverChecked:
			state = "未检测"
		case !ep.Healthy:
			state = "不健康"
		}
		s.Endpoints = append(s.Endpoints, tray.Endpoint{Name: ep.Name, Priority: ep.Priority, Available: ep.AvailabilityEnabled, Status: state})
	}
}

func trayEndpointStatus(status string) string {
	labels := map[string]string{"ok": "", "missing": "端点缺失", "disabled": "已停用", "availability_disabled": "硬关闭",
		"auto_schedule_disabled": "未参与自动调度", "cooldown": "冷却中", "unhealthy": "不可达"}
	if label, ok := labels[status]; ok {
		return label
	}
	return status
}

func (t *appTray) readCodexRoute(s *tray.Snapshot) {
	accounts, err := t.app.GetUpstreamAccounts()
	if err != nil {
		s.CodexError = err.Error()
		return
	}
	for _, a := range accounts {
		until, _ := time.Parse(time.RFC3339Nano, a.CooldownUntil)
		s.Accounts = append(s.Accounts, tray.Account{ID: a.ID, Name: a.AccountName, Group: a.GroupKey, State: a.State,
			Provider: a.ProviderType, Plan: a.PlanType, Priority: a.Priority, Enabled: a.Enabled, Pinned: a.IsActiveSelection,
			HasCredential: a.HasCredential, Cooling: until.After(time.Now()), FiveHourUsed: a.Quota5HUsedPercent, WeeklyUsed: a.QuotaWeeklyUsedPercent})
	}
	if s.CodexModels {
		bindings, err := t.app.ListCodexModelRouteBindings()
		s.CodexError = trayError(err)
		for _, b := range bindings {
			s.CodexBindings = append(s.CodexBindings, tray.Binding{ID: b.AssignmentID, Name: b.Model, Enabled: b.Enabled,
				Mode: b.RouteMode, TargetID: b.RouteTargetID, AccountIDs: b.AccountIDs, MemberEnabled: b.MemberEnabled})
		}
		return
	}
	recent, err := t.app.GetLatestAccountScheduleSnapshot()
	if err != nil {
		s.CodexError = err.Error()
		return
	}
	if recent.HasSnapshot {
		s.RecentAccountID = recent.SelectedAccountID
	}
}

// trayClaudeQuota 与请求页一致：逐窗口取较新来源，缺读数时取另一来源；过期读数不当作当前额度。
func trayClaudeQuota(q *ClaudeSubscriptionRateLimitInfo, now time.Time) string {
	if q == nil || (q.Headers == nil && q.Usage == nil) {
		return "尚无额度数据"
	}
	var header, usage [2]ClaudeSubscriptionQuotaWindowInfo
	var headerAt, usageAt time.Time
	if q.Headers != nil {
		header = [2]ClaudeSubscriptionQuotaWindowInfo{q.Headers.FiveHour, q.Headers.SevenDay}
		headerAt, _ = time.Parse(time.RFC3339Nano, q.Headers.CapturedAt)
	}
	if q.Usage != nil {
		usage = [2]ClaudeSubscriptionQuotaWindowInfo{q.Usage.FiveHour, q.Usage.SevenDay}
		usageAt, _ = time.Parse(time.RFC3339Nano, q.Usage.FetchedAt)
	}
	primary, secondary := header, usage
	if usageAt.After(headerAt) {
		primary, secondary = usage, header
	}
	labels := [2]string{}
	for i, value := range primary {
		if value.UsedPercent == nil {
			value = secondary[i]
		}
		labels[i] = "未知"
		reset, _ := time.Parse(time.RFC3339Nano, value.ResetAt)
		if !reset.IsZero() && !reset.After(now) {
			labels[i] = "已重置，待更新"
			continue
		}
		if value.UsedPercent != nil {
			labels[i] = fmt.Sprintf("剩 %.0f%%", math.Max(0, math.Min(100, 100-*value.UsedPercent)))
		}
	}
	return "5h " + labels[0] + " · d7 " + labels[1]
}

func (a *App) readTraySetting(key string, fallback bool) (bool, error) {
	record, err := a.GetSetting("desktop", key)
	if err != nil {
		return fallback, err
	}
	if record.Value == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(record.Value)
	if err != nil {
		return fallback, fmt.Errorf("桌面设置 %s 无效: %w", key, err)
	}
	return value, nil
}
