// manager.go - 端点管理器核心结构和基础功能
// 其他功能已拆分到独立文件：
// - health_check.go: 健康检查相关
// - endpoint_selection.go: 端点选择/路由
// - endpoint_crud.go: 动态端点管理
// - failover.go: 故障转移

package endpoint

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cc-forwarder/config"
	"cc-forwarder/internal/events"
	"cc-forwarder/internal/transport"
)

var failureTrackerCleanupInterval = 30 * time.Second

// EndpointStatus represents the health status of an endpoint
type EndpointStatus struct {
	Healthy          bool
	LastCheck        time.Time
	ResponseTime     time.Duration
	ConsecutiveFails int
	NeverChecked     bool // 表示从未被检测过
	// 冷却运行态按 scope 分槽（§14.4），互不覆盖：
	// messages 槽仅阻断 /v1/messages；global 槽（auth/quota）阻断双 path
	CooldownUntil        time.Time // messages scope 冷却截止时间
	CooldownReason       string    // messages scope 冷却原因
	GlobalCooldownUntil  time.Time // global scope 冷却截止时间
	GlobalCooldownReason string    // global scope 冷却原因
}

// EffectiveCooldown 返回当前生效冷却中截止最晚的一条（展示与 messages path 判定口径）
func (s EndpointStatus) EffectiveCooldown(now time.Time) (until time.Time, reason string, active bool) {
	if !s.CooldownUntil.IsZero() && now.Before(s.CooldownUntil) {
		until, reason, active = s.CooldownUntil, s.CooldownReason, true
	}
	if !s.GlobalCooldownUntil.IsZero() && now.Before(s.GlobalCooldownUntil) && s.GlobalCooldownUntil.After(until) {
		until, reason, active = s.GlobalCooldownUntil, s.GlobalCooldownReason, true
	}
	return until, reason, active
}

// Endpoint represents an endpoint with its configuration and status
type Endpoint struct {
	Config config.EndpointConfig
	Status EndpointStatus
	mutex  sync.RWMutex
	// v8：配置修订号（publish 时递增，AttemptPlan CAS 依据）与在途 admission 计数
	configRevision int64
	admissions     atomic.Int64
	// 故障运行态 epoch:手动解除冷却(ResetEndpointFailureState)时递增,
	// 旧 epoch 的晚到故障结算被写时 fencing 拒绝,不污染已重置的运行态
	failureEpoch int64
}

// RLock 锁定端点状态读锁（供外部安全读取状态）
func (e *Endpoint) RLock() {
	e.mutex.RLock()
}

// RUnlock 解锁端点状态读锁
func (e *Endpoint) RUnlock() {
	e.mutex.RUnlock()
}

// IsInCooldown 检查端点是否处于冷却状态（任一 scope 生效即视为冷却，/v1/messages 口径）
func (e *Endpoint) IsInCooldown() bool {
	e.mutex.RLock()
	defer e.mutex.RUnlock()
	_, _, active := e.Status.EffectiveCooldown(time.Now())
	return active
}

// Manager manages endpoints and their health status
type Manager struct {
	endpoints           []*Endpoint
	endpointsMu         sync.RWMutex // v5.0+: 保护 endpoints 切片的并发访问
	endpointConfigMu    sync.RWMutex // v8：配置/revision 发布与 attempt 结算的 generation barrier
	configMu            sync.RWMutex
	config              *config.Config
	client              *http.Client
	ctx                 context.Context
	cancel              context.CancelFunc
	wg                  sync.WaitGroup
	fastTester          *FastTester
	softFailures        *SoftFailureTracker // 分类软失败追踪器（§9.3）
	pendingGates        *pendingGateSet     // §7.6 停用中的安全阻断 gate
	autoRetention       *autoRetentionState // §8.4 Auto retained 运行态
	scopedCooldowns     *ScopedCooldowns    // §10 count_tokens 进程内 scoped cooldown（D17）
	routingNotReady     atomic.Bool         // §6.4：启动读取失败时 Claude 路由 not ready
	cooldownPersistMu   sync.RWMutex
	cooldownPersistHook func(name string, until time.Time, reason string, revision int64) // §14.4 冷却持久化钩子
	routeDecisionMu     sync.RWMutex
	routeDecisionNotify func(RouteOverrideState) // last_effective_endpoint 变更通知（UI 事件桥接）
	routeScopeMu        sync.RWMutex
	routeScopePolicy    RouteScopePolicy
	routeOverride       *RouteOverride
	routeState          *RouteState
	scheduleSnapshots   *endpointScheduleSnapshotStore
	// EventBus for decoupled event publishing
	eventBus events.EventBus
	// 健康检查完成回调（用于推送 Wails 事件）
	onHealthCheckComplete func()
	// 故障转移回调（用于同步数据库）
	// 参数: failedEndpoint 失败的端点名, newEndpoint 新激活的端点名
	onFailoverTriggered func(failedEndpoint, newEndpoint string)
}

// SetEventBus 设置端点健康状态事件总线。
func (m *Manager) SetEventBus(eventBus events.EventBus) {
	m.eventBus = eventBus
}

// NewManager creates a new endpoint manager
func NewManager(cfg *config.Config) *Manager {
	ctx, cancel := context.WithCancel(context.Background())

	// Create transport with proxy support
	httpTransport, err := transport.CreateTransport(cfg)
	if err != nil {
		slog.Error(fmt.Sprintf("❌ Failed to create HTTP transport with proxy: %s", err.Error()))
		// Fall back to default transport
		httpTransport = &http.Transport{}
	}

	manager := &Manager{
		config: cfg,
		client: &http.Client{
			Timeout:   cfg.Health.Timeout,
			Transport: httpTransport,
		},
		ctx:               ctx,
		cancel:            cancel,
		fastTester:        NewFastTester(cfg),
		routeOverride:     NewRouteOverride(),
		pendingGates:      newPendingGateSet(),
		autoRetention:     newAutoRetentionState(),
		scopedCooldowns:   NewScopedCooldowns(),
		routeState:        NewRouteState(),
		scheduleSnapshots: newEndpointScheduleSnapshotStore(),
		softFailures: NewSoftFailureTracker(
			cfg.FailureTracker.Enabled,
			cfg.FailureTracker.TimeWindow,
			cfg.FailureTracker.Threshold,
		),
	}

	// Initialize endpoints
	for _, endpointCfg := range cfg.Endpoints {
		endpoint := &Endpoint{
			Config: endpointCfg,
			Status: EndpointStatus{
				Healthy:      false,
				NeverChecked: true, // 标记为未检测，等待手动/批量连通性测试或真实请求结果
			},
			configRevision: NextEndpointConfigRevision(),
		}
		manager.endpoints = append(manager.endpoints, endpoint)
	}

	// Set manager reference in fast tester for dynamic token resolution
	manager.fastTester.SetManager(manager)

	return manager
}

// Start starts endpoint background routines.
// 2026-03: 后台健康轮询已停用，避免持续探测 /v1/models。
func (m *Manager) Start() {
	if m.softFailures != nil {
		m.wg.Add(1)
		go m.failureTrackerCleanupLoop()
	}

	slog.Debug("🛑 [端点管理] 后台连通性轮询已停用，仅保留失败追踪清理与请求级状态更新")
}

// Stop stops endpoint background routines.
func (m *Manager) Stop() {
	m.cancel()
	m.wg.Wait()
}

// UpdateConfig updates the manager configuration (hot-reload)
// v5.0 Desktop: 只更新配置参数，不重建端点（端点完全由数据库管理）
func (m *Manager) UpdateConfig(cfg *config.Config) {
	m.configMu.Lock()
	m.config = cfg
	m.configMu.Unlock()

	slog.Debug("🔄 [热更新] 更新配置参数完成，端点保持不变")

	// Update fast tester with new config
	if m.fastTester != nil {
		m.fastTester.UpdateConfig(cfg)
	}

	// 🔧 [热更新] 同步更新软失败追踪器配置
	if m.softFailures != nil {
		m.softFailures.UpdateConfig(cfg.FailureTracker.Enabled, cfg.FailureTracker.TimeWindow, cfg.FailureTracker.Threshold)
	}

	// Recreate transport with new proxy configuration
	if transport, err := transport.CreateTransport(cfg); err == nil {
		m.client = &http.Client{
			Transport: transport,
			Timeout:   cfg.Health.Timeout,
		}
	}
}

// GetTokenForEndpoint 返回端点自身的单 Token。
func (m *Manager) GetTokenForEndpoint(ep *Endpoint) string {
	if ep == nil {
		return ""
	}
	ep.mutex.RLock()
	defer ep.mutex.RUnlock()
	return ep.Config.Token
}

// GetApiKeyForEndpoint 返回端点自身的单 API Key。
func (m *Manager) GetApiKeyForEndpoint(ep *Endpoint) string {
	if ep == nil {
		return ""
	}
	ep.mutex.RLock()
	defer ep.mutex.RUnlock()
	return ep.Config.ApiKey
}

// GetConfig returns the manager's configuration
func (m *Manager) GetConfig() *config.Config {
	m.configMu.RLock()
	defer m.configMu.RUnlock()
	return m.config
}

// getConfigSnapshot returns a consistent config pointer for internal use.
func (m *Manager) getConfigSnapshot() *config.Config {
	m.configMu.RLock()
	cfg := m.config
	m.configMu.RUnlock()
	return cfg
}

// RecordSoftFailure 记录一次分类软失败，返回窗口内计数与是否达到阈值（§9.3）
func (m *Manager) RecordSoftFailure(endpointName string, scope SoftFailureScope, category SoftFailureCategory) (int, bool) {
	return m.softFailures.Record(endpointName, scope, category)
}

// RecordSoftFailureFenced 在 ep.mutex.RLock 下校验故障 epoch 并完成软失败记录
// (写时 fencing,与 ResetEndpointFailureState 的写锁临界区真互斥)。
// RLock 必须覆盖实际写入:若校验后释放锁再落写,"读 epoch → Reset 清理 → 落写"
// 的交错会让旧 epoch 写入残留(v4 要求零残留)。锁持有期间 Reset 的写锁被阻塞,
// 写入完成后 Reset 的清理会一并清掉;Reset 先完成则读到新 epoch 拒写。
// applied=false 表示 epoch 不匹配、写入被拒绝(count/tripped 为零值,
// 调用方不得据此驱动冷却写入或换候选行为)。
// tracker 自身叶子锁串行多个并发写者;锁序 ep.mutex(RLock) → SoftFailureTracker.mu 无环。
func (m *Manager) RecordSoftFailureFenced(endpointName string, scope SoftFailureScope, category SoftFailureCategory, epoch int64) (count int, tripped bool, applied bool) {
	ep := m.GetEndpointByNameAny(endpointName)
	if ep == nil {
		return 0, false, false
	}
	ep.mutex.RLock()
	defer ep.mutex.RUnlock()
	if ep.failureEpoch != epoch {
		return 0, false, false
	}
	count, tripped = m.softFailures.Record(endpointName, scope, category)
	return count, tripped, true
}

// RecordSuccess FullSuccess 清空端点 messages scope 的全部软失败类别。
// count_tokens scope 由其 handler 自行清理（Phase 3 接入）。
func (m *Manager) RecordSuccess(endpointName string) {
	m.softFailures.ClearScope(endpointName, SoftFailureScopeMessages)
}

// RecordSuccessSince FullSuccess 仅清除 since（请求开始时刻）之前的 messages 软失败：
// 慢请求的成功不会抹掉请求进行期间新记录的失败证据（IfNoNewerFailure）。
func (m *Manager) RecordSuccessSince(endpointName string, since time.Time) {
	m.softFailures.ClearScopeBefore(endpointName, SoftFailureScopeMessages, since)
}

// ClearSoftFailureScope 清空指定 scope 的软失败类别与 scoped cooldown
func (m *Manager) ClearSoftFailureScope(endpointName string, scope SoftFailureScope) {
	m.softFailures.ClearScope(endpointName, scope)
	m.scopedCooldowns.Clear(endpointName, scope)
}

// SetScopedCooldown 写入进程内 scoped cooldown（count_tokens 专用，D17）
func (m *Manager) SetScopedCooldown(endpointName string, scope SoftFailureScope, duration time.Duration, reason string) {
	if duration <= 0 {
		return
	}
	m.scopedCooldowns.Set(endpointName, scope, time.Now().Add(duration), reason)
}

// SetScopedCooldownFenced 在 ep.mutex.RLock 下校验故障 epoch 并完成 scoped cooldown
// 写入(写时 fencing,与 ResetEndpointFailureState 的写锁临界区真互斥)。
// RLock 必须覆盖实际写入,理由同 RecordSoftFailureFenced:校验后释放锁再落写,
// 会残留"读 epoch → Reset 清理 → 落写"的交错窗口。
// epoch 不匹配直接拒绝写入并返回 false。
func (m *Manager) SetScopedCooldownFenced(endpointName string, scope SoftFailureScope, duration time.Duration, reason string, epoch int64) bool {
	if duration <= 0 {
		return false
	}
	ep := m.GetEndpointByNameAny(endpointName)
	if ep == nil {
		return false
	}
	ep.mutex.RLock()
	defer ep.mutex.RUnlock()
	if ep.failureEpoch != epoch {
		return false
	}
	m.scopedCooldowns.Set(endpointName, scope, time.Now().Add(duration), reason)
	return true
}

// ScopedCooldownActive 查询 scoped cooldown
func (m *Manager) ScopedCooldownActive(endpointName string, scope SoftFailureScope) (bool, time.Time, string) {
	return m.scopedCooldowns.Active(endpointName, scope)
}

// GetFailureStats 获取失败统计信息（按端点聚合，兼容旧展示口径）
func (m *Manager) GetFailureStats() map[string]int {
	return m.softFailures.Stats()
}

// GetSoftFailureCounts 返回端点在指定 scope 的分类软失败计数（快照解释用）
func (m *Manager) GetSoftFailureCounts(endpointName string, scope SoftFailureScope) map[SoftFailureCategory]int {
	return m.softFailures.CountsFor(endpointName, scope)
}

// SoftFailureThreshold 当前软失败阈值
func (m *Manager) SoftFailureThreshold() int {
	return m.softFailures.Threshold()
}

func (m *Manager) failureTrackerCleanupLoop() {
	defer m.wg.Done()
	ticker := time.NewTicker(failureTrackerCleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.softFailures.CleanupExpiredEvents()
		}
	}
}

// IsEndpointRoutable reports whether an endpoint can currently participate in routing.
// 调度仅依赖真实请求失败追踪和冷却状态，不依赖后台健康轮询。
func (m *Manager) IsEndpointRoutable(ep *Endpoint) bool {
	if ep == nil {
		return false
	}

	// v8：软失败阈值触发即写入 cooldown，可路由性只看 cooldown（§9.3）
	return !ep.IsInCooldown()
}

// UpdateFailureTrackerConfig 热更新失败追踪器配置
func (m *Manager) UpdateFailureTrackerConfig(enabled bool, timeWindow time.Duration, threshold int) {
	m.softFailures.UpdateConfig(enabled, timeWindow, threshold)
}

// ShouldRejectRequest 检查是否应该拒绝请求（D15 冻结语义，§12）：
// action=reject 时，检查"第一逻辑候选"是否处于软失败阈值型 cooldown；
// tripped 即拒绝，不尝试备用端点。第一逻辑候选优先取 manual fixed/preferred
// 目标，否则按 hard 资格 + 自动调度资格取 priority 最小值端点。
func (m *Manager) ShouldRejectRequest(profiles ...RouteRequestProfile) (bool, string) {
	cfg := m.getConfigSnapshot()
	if !cfg.FailureTracker.Enabled || cfg.FailureTracker.Action != "reject" {
		return false, ""
	}

	var profile RouteRequestProfile
	if len(profiles) > 0 {
		profile = profiles[0]
	}
	name := m.firstLogicalCandidateName(profile)
	if name == "" {
		return false, ""
	}
	// 直接读 messages 槽：effective cooldown 取"截止最晚"者，更晚到期的 global
	// auth/quota 冷却会掩盖软失败 reason，导致 reject 误放行（§12）
	ep := m.GetEndpointByNameAny(name)
	if ep == nil {
		return false, ""
	}
	ep.mutex.RLock()
	until := ep.Status.CooldownUntil
	reason := ep.Status.CooldownReason
	ep.mutex.RUnlock()
	if !until.IsZero() && time.Now().Before(until) && strings.HasPrefix(reason, SoftFailureCooldownReasonPrefix) {
		return true, name
	}
	return false, ""
}

// firstLogicalCandidateName 计算第一逻辑候选（忽略健康/冷却状态）：
// manual fixed/preferred 目标优先（硬启用时）；否则按 hard 资格 + 自动调度资格
// 取 priority 最小值端点（v8：legacy active 已退役，不再参与判定）。

func (m *Manager) firstLogicalCandidateName(profiles ...RouteRequestProfile) string {
	var profile RouteRequestProfile
	if len(profiles) > 0 {
		profile = profiles[0]
	}
	scope := m.ResolveRouteScope(profile)
	if scope.Kind == RouteScopeUnassigned {
		return ""
	}
	if scope.Mode != RouteModeAuto && scope.Target != "" {
		if ep := m.GetEndpointByNameAny(scope.Target); ep != nil && m.EndpointHardEnabled(ep) {
			return scope.Target
		}
	}
	memberSet := make(map[string]struct{}, len(scope.Members))
	for _, member := range scope.Members {
		memberSet[member] = struct{}{}
	}

	m.endpointsMu.RLock()
	defer m.endpointsMu.RUnlock()
	bestName := ""
	bestPriority := 0
	for _, ep := range m.endpoints {
		if ep == nil {
			continue
		}
		ep.mutex.RLock()
		name := ep.Config.Name
		priority := ep.Config.Priority
		hardEnabled := ep.Config.IsAvailabilityEnabled()
		autoScheduleEnabled := ep.Config.IsAutoScheduleEnabled()
		ep.mutex.RUnlock()
		if !hardEnabled || !autoScheduleEnabled {
			continue
		}
		if scope.Kind == RouteScopeUpstreamGroup {
			if _, ok := memberSet[name]; !ok {
				continue
			}
		}
		if bestName == "" || priority < bestPriority {
			bestName = name
			bestPriority = priority
		}
	}
	return bestName
}
