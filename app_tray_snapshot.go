package main

import (
	"context"
	"fmt"
	"math"
	"net"
	"runtime"
	"strconv"
	"strings"
	"time"

	timezonepolicy "sinuo/internal/timezone"
	"sinuo/internal/tray"
)

// trayStatsCacheTTL：今日统计要查库，菜单频繁重建时复用 30 秒内的结果。
const trayStatsCacheTTL = 30 * time.Second

func (t *appTray) snapshot() tray.Snapshot {
	a := t.app
	s := tray.Snapshot{Now: time.Now(), Active: t.activeCount(), Waiting: t.quit.Waiting(), CloseToTray: true}
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
	s.MainURL = trayLocalURL(status.ProxyHost, status.ProxyPort)
	t.readDesktopSettings(&s)
	t.readTrayStats(&s)
	if policy, err := a.activeTimezonePolicy(); err == nil {
		s.Location = policy.Location()
	}
	t.failoverMu.Lock()
	s.Failovers = append([]tray.FailoverEntry(nil), t.failovers...)
	t.failoverMu.Unlock()
	t.readModelRouting(&s)
	entry, err := a.GetClaudeCodeEntryStatus()
	s.EntryError = trayError(err)
	s.EntryEnabled, s.EntryRunning, s.EntryMode, s.EntryPort, s.EntryListenError = entry.Enabled, entry.Running, entry.Mode, entry.ListenPort, entry.Error
	if err == nil && entry.Enabled && entry.Running {
		s.EntryURL = entry.BaseURL
	}
	if entry.Enabled && entry.Mode == "subscription" {
		quota := a.GetClaudeSubscriptionRateLimit()
		s.EntryQuota, s.EntryQuotaShort = trayClaudeQuota(quota, s.Now), trayClaudeQuotaShort(quota, s.Now)
	}
	t.readCodexEntry(&s)
	if s.CodexError == "" {
		t.readCodexRoute(&s)
	}
	return s
}

func (t *appTray) readModelRouting(s *tray.Snapshot) {
	routing, err := t.app.GetModelRoutingSettings()
	if err != nil {
		s.ClaudeError, s.CodexError = "模型路由状态读取失败: "+err.Error(), "模型路由状态读取失败: "+err.Error()
		return
	}
	s.ClaudeModels, s.CodexModels = routing.ClaudeEnabled, routing.CodexEnabled
	s.CodexRoutingAvailable = routing.CodexStatus != "unavailable"
	t.readClaudeRoute(s)
}

func trayError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// readDesktopSettings 读桌面设置；读取失败时按默认值处理，并在菜单里置灰相关开关。
func (t *appTray) readDesktopSettings(s *tray.Snapshot) {
	a := t.app
	var errs [3]error
	s.CloseToTray, errs[0] = a.readTraySetting("close_to_tray", true)
	notify, err := a.readTraySetting("system_notifications", true)
	errs[1] = err
	t.notify.setEnabled(notify)
	if runtime.GOOS == "darwin" {
		var mode string
		mode, errs[2] = a.readTraySettingValue("menubar_label")
		s.MenuBarLabel = tray.NormalizeMenuBarMode(mode)
	}
	for _, err := range errs {
		if err != nil {
			s.SettingsError = err.Error()
			break
		}
	}
}

func (t *appTray) readTrayStats(s *tray.Snapshot) {
	if t.statsStale.Swap(false) || time.Since(t.statsAt) >= trayStatsCacheTTL {
		t.stats, t.statsErr = t.app.trayTodayStats()
		t.statsAt = time.Now()
	}
	s.TodayRequests, s.TodayFailed, s.TodayCost, s.StatsError = t.stats.requests, t.stats.failed, t.stats.cost, trayError(t.statsErr)
}

// trayTodayStats 是托盘统计行与菜单栏文字用的今日数字。
type trayTodayStats struct {
	requests, failed int64
	cost             float64
}

// trayTodayStats 一条按业务日的索引查询取齐今日请求数、花费与失败数：请求数和花费与
// GetUsageSummary 的今日统计同一口径（同表、同时区日界），但不像它那样顺带扫全部历史；
// 失败只计 status = 'failed'，与请求页「已失败」筛选一致（UsageSummary.FailedRequests 是启动以来的内存计数）。
func (a *App) trayTodayStats() (trayTodayStats, error) {
	var stats trayTodayStats
	db := a.coreDB()
	if db == nil {
		return stats, nil
	}
	policy, err := a.activeTimezonePolicy()
	if err != nil {
		return stats, err
	}
	start, end, err := policy.DayRange(policy.BusinessDate(time.Now()))
	if err != nil {
		return stats, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(total_cost_usd), 0), COUNT(CASE WHEN status = 'failed' THEN 1 END)
		FROM request_logs WHERE start_time >= ? AND start_time < ?`,
		timezonepolicy.FormatStorage(start), timezonepolicy.FormatStorage(end)).Scan(&stats.requests, &stats.cost, &stats.failed)
	return stats, err
}

// trayLocalURL 是给本机客户端用的地址：监听在全部网卡时换成回环地址。
func trayLocalURL(host string, port int) string {
	switch strings.TrimSpace(host) {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(port))
}

// trayTime 解析 API 返回的时间文本；为空或无法解析时返回零值（菜单只显示「冷却中」）。
func trayTime(raw string) time.Time {
	value, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}
	}
	return value
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
				binding.Endpoints = append(binding.Endpoints, tray.Endpoint{Name: ep.Name, Priority: ep.Priority, Available: ep.AvailabilityEnabled,
					Status: trayEndpointStatus(ep.Status), CooldownUntil: trayTime(ep.CooldownUntil)})
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
		s.Endpoints = append(s.Endpoints, tray.Endpoint{Name: ep.Name, Priority: ep.Priority, Available: ep.AvailabilityEnabled,
			Status: state, CooldownUntil: trayTime(ep.CooldownUntil)})
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
		until := trayTime(a.CooldownUntil)
		s.Accounts = append(s.Accounts, tray.Account{ID: a.ID, Name: a.AccountName, Group: a.GroupKey, State: a.State,
			Provider: a.ProviderType, Plan: a.PlanType, Priority: a.Priority, Enabled: a.Enabled, Pinned: a.IsActiveSelection,
			HasCredential: a.HasCredential, Cooling: until.After(s.Now), CooldownUntil: until,
			FiveHourUsed: a.Quota5HUsedPercent, WeeklyUsed: a.QuotaWeeklyUsedPercent})
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

// trayClaudeQuotaWindows 与请求页一致：逐窗口取较新来源，缺读数时取另一来源；
// 重置时间已过的读数不当作当前额度。返回 5h / d7 的剩余百分比（未知或已重置时为 nil）与是否已重置。
func trayClaudeQuotaWindows(q *ClaudeSubscriptionRateLimitInfo, now time.Time) (left [2]*float64, reset [2]bool) {
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
	for i, value := range primary {
		if value.UsedPercent == nil {
			value = secondary[i]
		}
		if at, _ := time.Parse(time.RFC3339Nano, value.ResetAt); !at.IsZero() && !at.After(now) {
			reset[i] = true
			continue
		}
		if value.UsedPercent != nil {
			remaining := math.Max(0, math.Min(100, 100-*value.UsedPercent))
			left[i] = &remaining
		}
	}
	return left, reset
}

func trayClaudeQuota(q *ClaudeSubscriptionRateLimitInfo, now time.Time) string {
	if q == nil || (q.Headers == nil && q.Usage == nil) {
		return "尚无额度数据"
	}
	left, reset := trayClaudeQuotaWindows(q, now)
	labels := [2]string{}
	for i := range labels {
		switch {
		case reset[i]:
			labels[i] = "已重置，待更新"
		case left[i] != nil:
			labels[i] = fmt.Sprintf("剩 %.0f%%", *left[i])
		default:
			labels[i] = "未知"
		}
	}
	return "5h " + labels[0] + " · d7 " + labels[1]
}

// trayClaudeQuotaShort 是菜单栏文字：剩余更少的窗口（相同时取 5h），与请求页角标同一口径；没有有效读数时为空。
func trayClaudeQuotaShort(q *ClaudeSubscriptionRateLimitInfo, now time.Time) string {
	if q == nil || (q.Headers == nil && q.Usage == nil) {
		return ""
	}
	left, _ := trayClaudeQuotaWindows(q, now)
	names, tightest := [2]string{"5h", "d7"}, -1
	for i := range left {
		if left[i] != nil && (tightest < 0 || *left[i] < *left[tightest]) {
			tightest = i
		}
	}
	if tightest < 0 {
		return ""
	}
	return fmt.Sprintf("%s %.0f%%", names[tightest], *left[tightest])
}

func (a *App) readTraySettingValue(key string) (string, error) {
	record, err := a.GetSetting("desktop", key)
	if err != nil {
		return "", err
	}
	return record.Value, nil
}

func (a *App) readTraySetting(key string, fallback bool) (bool, error) {
	raw, err := a.readTraySettingValue(key)
	if err != nil {
		return fallback, err
	}
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback, fmt.Errorf("桌面设置 %s 无效: %w", key, err)
	}
	return value, nil
}
