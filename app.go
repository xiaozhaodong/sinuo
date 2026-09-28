// app.go - Wails 应用核心结构
// 封装所有业务组件，提供生命周期管理

package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cc-forwarder/config"
	"cc-forwarder/internal/endpoint"
	"cc-forwarder/internal/events"
	"cc-forwarder/internal/logging"
	"cc-forwarder/internal/middleware"
	"cc-forwarder/internal/migration"
	"cc-forwarder/internal/proxy"
	"cc-forwarder/internal/service"
	"cc-forwarder/internal/store"
	timezonepolicy "cc-forwarder/internal/timezone"
	"cc-forwarder/internal/tracking"
	"cc-forwarder/internal/transport"
	"cc-forwarder/internal/utils"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App 是 Wails 应用的核心结构
// 它封装了所有业务组件，并暴露方法给前端调用
type App struct {
	// Wails 上下文
	ctx context.Context

	// 核心组件
	config               *config.Config
	configWatcher        *config.ConfigWatcher
	logger               *slog.Logger
	endpointManager      *endpoint.Manager
	eventBus             events.EventBus // 接口类型，不是指针
	usageTracker         *tracking.UsageTracker
	timezonePolicy       *timezonepolicy.Policy
	coreDatabase         *tracking.CoreDatabase
	migrationMu          sync.RWMutex
	migrationCoordinator *migration.Coordinator
	migrationStatus      migration.Status
	proxyHandler         *proxy.Handler
	loggingMiddleware    *middleware.LoggingMiddleware
	monitoringMiddleware *middleware.MonitoringMiddleware
	authMiddleware       *middleware.AuthMiddleware

	// v5.0+ 端点存储 (SQLite)
	endpointStore   store.EndpointStore      // 端点数据持久化
	endpointService *service.EndpointService // 端点业务服务
	routingMu       sync.Mutex               // v8 §6.4: Claude 路由串行协调器

	// v9: 按模型选择 Claude 上游组
	modelRoutingStore        store.ModelRoutingStore
	modelRoutingService      *service.ModelRoutingService
	codexModelRoutingService *service.CodexModelRoutingService

	// v8:端点运行态存储(冷却持久化 + 手动解除冷却 tombstone)
	endpointRuntimeStateStore store.EndpointRuntimeStateStore

	// 手动解除冷却的持久化 pending 标记(按 endpoint ID + revision 管理,§4.4)
	cooldownPendingMu      sync.Mutex
	cooldownPersistPending map[string]cooldownPersistPendingEntry

	// v5.0+ 模型定价存储 (SQLite)
	modelPricingStore   store.ModelPricingStore      // 模型定价数据持久化
	modelPricingService *service.ModelPricingService // 模型定价业务服务

	// v5.1+ 系统设置存储 (SQLite)
	settingsStore   store.SettingsStore      // 设置数据持久化
	settingsService *service.SettingsService // 设置业务服务
	portManager     *utils.PortManager       // 端口管理器

	// v6.0+ 账号池存储 (SQLite)
	accountPoolStore   store.AccountPoolStore      // 账号池数据持久化
	accountPoolService *service.AccountPoolService // 账号池业务服务

	// v6.1+ 隐私保护存储 (SQLite)
	privacyStore   store.PrivacyStore      // 隐私规则数据持久化
	privacyService *service.PrivacyService // 隐私保护业务服务

	// v9: Codex 订阅代理独立入口（独立回环 listener；只读依赖旧入口的账号池与隐私服务）
	subscriptionProxyMu              sync.RWMutex
	subscriptionProxySettingsService *service.CodexSubscriptionProxySettingsService
	subscriptionProxyHandler         *proxy.CodexSubscriptionProxyHandler
	subscriptionProxyRuntime         *proxy.CodexSubscriptionProxyRuntime

	// Claude Code 专用入口（独立回环 listener；订阅直通模式直连 Anthropic 官方，端点模式与主端口相同）
	claudeCodeEntryMu       sync.Mutex // 串行化「保存配置 + 应用运行态」
	claudeCodeEntrySettings *service.ClaudeCodeEntrySettingsService
	claudeCodeEntryRuntime  *proxy.ClaudeCodeEntryRuntime
	claudeCodeEntryProxy    *proxy.Handler // 装配时记下，应用配置时切换订阅额度开关，免得在 claudeCodeEntryMu 下再拿 a.mu

	// HTTP 代理服务器 (保留，监听配置的端口)
	proxyServer   *http.Server
	proxyRequests *proxyRequestLifecycle
	shutdownDone  chan struct{}

	// 应用状态
	startTime          time.Time
	configPath         string
	configPathExplicit bool

	// 并发控制
	mu        sync.RWMutex
	isRunning bool

	// OpenAI OAuth 临时会话（用于授权链接 -> 回调URL换RT）
	oauthSessionMu sync.Mutex
	oauthSessions  map[string]openAIOAuthSession

	// 日志处理器（用于查询和广播）
	logHandler *logging.BroadcastHandler
	logEmitter *logging.EventEmitter

	// 启动连通性检查测试 seam（默认走真实实现）
	startupEndpointCheckRunner func()
	startupAccountCheckRunner  func()
}

// NewApp 创建新的应用实例
func NewApp() *App {
	return &App{
		startTime:              time.Now(),
		logger:                 slog.Default(),
		oauthSessions:          make(map[string]openAIOAuthSession),
		cooldownPersistPending: make(map[string]cooldownPersistPendingEntry),
	}
}

// startup 在 Wails 应用启动时调用
// 这里初始化所有组件并启动代理服务器
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.mu.Lock()
	defer a.mu.Unlock()

	// 1. 准备核心数据库，并在任何运行时写入组件启动前完成一次性迁移。
	if err := a.prepareCoreDatabaseAndMigration(ctx); err != nil {
		migrationStatus := a.failStartup(err)
		a.logger.Error("❌ 启动迁移失败，应用进入只读恢复模式", "error", err,
			"backup_dir", migrationStatus.BackupDir, "phase", migrationStatus.Phase)
		return
	}

	// 2. 初始化日志
	a.setupLogger()
	a.startOperationalComponentsLocked(ctx)
}

// startOperationalComponentsLocked 启动正常运行态组件；调用方必须持有 a.mu。
// 迁移失败时不会调用本方法，因此代理、后台检查、历史收集和配置热重载均不会启动。
func (a *App) startOperationalComponentsLocked(ctx context.Context) {

	// 3. 显示启动信息
	a.logger.Info("🚀 AI Switchboard 桌面版启动中...",
		"version", Version,
		"config_file", a.configPath)

	// 4. 初始化事件总线
	a.setupEventBus()

	// 5. 初始化使用追踪（SQLite 存储需要依赖数据库）
	a.setupUsageTracker()

	// 5.5 初始化设置服务 (v5.1+ SQLite)
	a.setupSettingsStore()
	a.noteTimezoneChange(ctx)

	// 6. 创建端点管理器（但不启动健康检查）
	a.endpointManager = endpoint.NewManager(a.config)
	a.endpointManager.SetEventBus(a.eventBus)
	// v5.0+ Wails 桌面应用：设置健康检查完成回调，推送事件到前端
	a.endpointManager.SetOnHealthCheckComplete(func() {
		a.emitEndpointUpdate()
	})

	// v7：故障转移回调仅用于前端事件；DB 写入统一走 endpoint runtime writer
	a.endpointManager.SetOnFailoverTriggered(func(failedEndpoint, newEndpoint string) {
		a.emitEndpointUpdate()
	})

	// v8 Phase 4：last_effective_endpoint 变化时推送路由事件，前端不再依赖 legacy active
	a.endpointManager.SetRouteDecisionNotifier(func(state endpoint.RouteOverrideState) {
		a.emitClaudeRoutingUpdate(a.buildClaudeRoutingState(state))
	})

	// 7. 初始化端点存储 (v5.0+ SQLite, 需要在创建 Manager 之后)
	// 从数据库同步端点到 Manager
	if a.config.EndpointsStorage.Type == "sqlite" {
		a.setupEndpointStore()
	}

	// 7.5 初始化模型定价存储 (v5.0+ SQLite)
	a.setupModelPricingStore()

	// 7.7 初始化账号池存储 (v6.0+ SQLite)
	a.setupAccountPoolStore()

	// 7.75 初始化隐私保护服务 (v6.1+ SQLite)
	a.setupPrivacyService()

	// 7.8 初始化按模型选择 Claude 上游组（设置读取失败回到全局，绑定加载失败按开关状态 fail-closed）
	a.setupModelRoutingService(ctx)

	// 7.6 同步端点倍率到 UsageTracker（用于成本计算）
	a.syncEndpointMultipliersToTracker(ctx)
	a.syncAccountMultipliersToTracker(ctx)

	// 7.9 恢复 Claude 手动路由状态
	a.loadClaudeRoutingOverride(ctx)

	// 8. 启动端点管理器（此时端点已从数据库加载完成）
	a.endpointManager.Start()

	// 显示代理配置
	if a.config.Proxy.Enabled {
		proxyInfo := transport.GetProxyInfo(a.config)
		a.logger.Info("🔗 " + proxyInfo)
	}

	// 9. 初始化代理处理器
	a.setupProxyHandler()

	// 10. 启动 HTTP 代理服务器
	a.startProxyServer()

	// 10.5 启动 Codex 订阅代理独立入口（未启用时不监听；失败不影响旧入口）
	a.setupCodexSubscriptionProxy(ctx)

	// 10.6 启动 Claude Code 专用入口（未启用时不监听；失败不影响主端口）
	a.setupClaudeCodeEntry(ctx)

	// 11. 设置配置热重载
	a.setupConfigReload()

	// 12. 设置事件桥接
	a.setupEventBridges()

	// 13. 启动历史数据收集器
	a.startHistoryCollector()

	a.isRunning = true
	a.logger.Info("✅ AI Switchboard 启动完成",
		"proxy_port", a.config.Server.Port)
	a.scheduleStartupConnectivityChecks()
	a.completeStartup()
}

// setupModelRoutingService 初始化模型路由服务并把其接入端点调度器。
// 开关读取失败时使用全局路由，用户可在恢复后重新开启；
// 已明确开启但绑定数据加载失败时按既有契约返回 422。
func (a *App) setupModelRoutingService(ctx context.Context) {
	db := a.coreDB()
	if db == nil || a.endpointManager == nil {
		a.logger.Error("❌ 无法获取数据库连接或端点管理器 (模型路由)")
		return
	}

	a.modelRoutingStore = store.NewSQLiteModelRoutingStore(db)
	a.modelRoutingService = service.NewModelRoutingService(a.modelRoutingStore)
	a.modelRoutingService.SetWriteMutex(&a.routingMu)
	a.modelRoutingService.SetEndpointMemberViewer(a.endpointManager.EndpointMemberView)
	a.modelRoutingService.SetScopePruner(a.endpointManager.PruneAutoRetentionScopes)
	a.endpointManager.SetRouteScopePolicy(a.modelRoutingService)
	if a.accountPoolService != nil {
		a.codexModelRoutingService = service.NewCodexModelRoutingService(a.modelRoutingStore, a.accountPoolService.ListAccounts)
		a.codexModelRoutingService.SetWriteMutex(&a.routingMu)
	}

	claudeInitCtx, claudeCancel := context.WithTimeout(ctx, 30*time.Second)
	claudeErr := a.modelRoutingService.Initialize(claudeInitCtx)
	claudeCancel()
	if claudeErr != nil {
		a.logger.Error("❌ Claude 模型路由初始化失败", "error", claudeErr,
			"model_routing_enabled", a.modelRoutingService.CurrentSnapshot().Enabled())
	}
	if a.codexModelRoutingService != nil {
		codexInitCtx, codexCancel := context.WithTimeout(ctx, 30*time.Second)
		codexErr := a.codexModelRoutingService.Initialize(codexInitCtx)
		codexCancel()
		if codexErr != nil {
			a.logger.Error("❌ Codex 模型路由初始化失败", "error", codexErr,
				"model_routing_enabled", a.codexModelRoutingService.Enabled())
		}
	}
	a.logger.Info("✅ 模型路由服务已加载 (SQLite)",
		"claude_status", a.modelRoutingService.Status(),
		"codex_status", func() string {
			if a.codexModelRoutingService == nil {
				return "unavailable"
			}
			return a.codexModelRoutingService.Status()
		}())
}

// shutdown 在 Wails 应用关闭时调用
func (a *App) shutdown(ctx context.Context) {
	a.mu.Lock()
	if a.shutdownDone != nil {
		done := a.shutdownDone
		a.mu.Unlock()
		<-done
		return
	}
	done := make(chan struct{})
	a.shutdownDone = done
	server, requests := a.proxyServer, a.proxyRequests
	subscriptionRuntime := a.subscriptionProxyRuntime
	claudeCodeEntryRuntime := a.claudeCodeEntryRuntime
	a.mu.Unlock()
	defer close(done)

	a.logger.Info("🛑 正在关闭 AI Switchboard...")

	// 0. 先停 Codex 订阅代理新入口：它持有独立 listener 与 WS 连接，
	//    停用只关闭自己的资源，不影响仍被旧入口使用的账号池与追踪服务。
	if subscriptionRuntime != nil {
		stopCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		if err := subscriptionRuntime.Stop(stopCtx); err != nil {
			a.logger.Error("Codex 订阅代理停用失败", "error", err)
		}
		cancel()
	}

	// 0.5 停 Claude Code 专用入口：它复用主端口的代理处理器，须先于主端口停止
	if claudeCodeEntryRuntime != nil {
		stopCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if err := claudeCodeEntryRuntime.Stop(stopCtx); err != nil {
			a.logger.Error("Claude Code 入口停用失败", "error", err)
		}
		cancel()
	}

	// 1. 停止接收新请求
	if server != nil {
		shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		var err error
		if requests != nil {
			err = requests.shutdown(shutdownCtx, server)
		} else {
			err = server.Shutdown(shutdownCtx)
		}
		if err != nil {
			a.logger.Error("代理服务器关闭失败", "error", err)
		}
	}
	// 请求回调可能读取 App，等待其退出时不能持有 App 写锁。
	a.mu.Lock()
	defer a.mu.Unlock()

	// 2. 关闭账号池服务后台任务
	if a.accountPoolService != nil {
		if err := a.accountPoolService.Close(); err != nil {
			a.logger.Error("账号池服务关闭失败", "error", err)
		}
	}

	// 3. 关闭端点管理器
	if a.endpointManager != nil {
		a.endpointManager.Stop()
	}

	// 4. 关闭使用追踪 (flush 数据库)
	if a.usageTracker != nil {
		if err := a.usageTracker.Close(); err != nil {
			a.logger.Error("使用追踪器关闭失败", "error", err)
		}
	}

	// 4.5 关闭核心数据库；它独立于 UsageTracker 生命周期并最后释放。
	if a.coreDatabase != nil {
		if err := a.coreDatabase.Close(); err != nil {
			a.logger.Error("核心数据库关闭失败", "error", err)
		}
	}

	// 5. 关闭事件总线
	if a.eventBus != nil {
		if err := a.eventBus.Stop(); err != nil {
			a.logger.Error("事件总线关闭失败", "error", err)
		}
	}

	// 5. 关闭配置监听
	if a.configWatcher != nil {
		a.configWatcher.Close()
	}

	// 6. 停止日志事件发射器
	if a.logEmitter != nil {
		a.logEmitter.Stop()
	}

	a.isRunning = false
	a.logger.Info("✅ AI Switchboard 已关闭")
}

// domReady 在前端 DOM 准备就绪时调用
func (a *App) domReady(ctx context.Context) {
	// 发送初始状态给前端
	a.emitSystemStatus()
}

// beforeClose 在窗口关闭前调用，返回 true 阻止关闭
func (a *App) beforeClose(ctx context.Context) bool {
	// 可以在这里询问用户是否确认关闭
	// 或者最小化到托盘而不是关闭
	return false
}

// setupLogger 设置日志
func (a *App) setupLogger() {
	logger, broadcastHandler := setupLogger(a.config.Logging)
	a.logger = logger
	slog.SetDefault(logger)

	// 存储日志处理器和发射器引用
	a.logHandler = broadcastHandler
	a.logEmitter = broadcastHandler.Emitter

	// 初始化调试配置
	utils.SetDebugConfig(a.config)

	a.logger.Info("✅ 日志系统初始化完成",
		"level", a.config.Logging.Level,
		"file_enabled", a.config.Logging.FileEnabled)
}

// setupEventBus 设置事件总线
func (a *App) setupEventBus() {
	a.eventBus = events.NewEventBus(a.logger)
	// 请求追踪页使用 Wails 事件驱动刷新；其他前端状态仍沿用各自现有推送路径。
	a.eventBus.SetSSEBroadcaster(&wailsRequestBroadcaster{app: a})
	if err := a.eventBus.Start(); err != nil {
		a.logger.Error("事件总线启动失败", "error", err)
	}
}

// setupEndpointStore 设置端点存储 (v5.0+ SQLite)
func (a *App) setupEndpointStore() {
	db := a.coreDB()
	if db == nil {
		a.logger.Error("❌ 无法获取数据库连接")
		return
	}

	// 创建 EndpointStore
	a.endpointStore = store.NewSQLiteEndpointStore(db)

	// 创建 EndpointService
	a.endpointService = service.NewEndpointService(a.endpointStore, a.endpointManager)

	// 从数据库同步端点到内存
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := a.endpointService.SyncFromDatabase(ctx); err != nil {
		a.logger.Error("❌ 从数据库同步 Claude 端点失败", "error", err)
	} else {
		a.logger.Info("✅ 端点存储已启用 (SQLite)")
	}

	// v8 §14.4：cooldown 持久化（global auth/quota 与 messages）与启动恢复
	a.setupEndpointRuntimeStates(ctx, db)
}

// setupEndpointRuntimeStates 桥接 endpoint_runtime_states：注入持久化钩子并恢复未过期 cooldown。
// scope 判定：auth/quota 型 reason → global（阻断两个 path）；其余软失败 → messages。
// count_tokens 普通 cooldown 保持进程内态（D17），不经过本桥接。
func (a *App) setupEndpointRuntimeStates(ctx context.Context, db *sql.DB) {
	runtimeStateStore := store.NewSQLiteEndpointRuntimeStateStore(db)
	a.endpointRuntimeStateStore = runtimeStateStore

	// 用库内最大 revision 播种发号器：兜底时钟回拨/数据库迁移场景，
	// 保证本进程新发号严格大于全部历史记录，Upsert 不被静默丢弃
	if maxRevision, err := runtimeStateStore.MaxRevision(ctx); err != nil {
		a.logger.Warn("⚠️ 读取端点运行态最大 revision 失败，跳过播种", "error", err)
	} else if maxRevision > 0 {
		endpoint.SeedCooldownRevision(maxRevision)
	}

	endpointIDByName := func(name string) (int64, bool) {
		record, err := a.endpointStore.Get(context.Background(), name)
		if err != nil || record == nil {
			return 0, false
		}
		return record.ID, true
	}

	a.endpointManager.SetCooldownPersistHook(func(name string, until time.Time, reason string, revision int64) {
		id, ok := endpointIDByName(name)
		if !ok {
			return
		}
		scope := endpoint.CooldownScopeForReason(reason)
		persistCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		untilCopy := until
		if err := runtimeStateStore.Upsert(persistCtx, &store.EndpointRuntimeStateRecord{
			EndpointID:     id,
			Scope:          scope,
			State:          "cooldown",
			CooldownUntil:  &untilCopy,
			CooldownReason: reason,
			Revision:       revision, // Set 侧锁内生成：落库顺序与内存写入顺序一致，旧任务被 Upsert 丢弃
		}); err != nil {
			a.logger.Warn("⚠️ 端点 cooldown 持久化失败", "endpoint", name, "error", err)
		}
	})

	// 启动恢复：仅内存 restore，不回写
	records, err := runtimeStateStore.ListActiveCooldowns(ctx, time.Now())
	if err != nil {
		a.logger.Warn("⚠️ 读取端点运行态失败，跳过 cooldown 恢复", "error", err)
		return
	}
	if len(records) == 0 {
		return
	}
	endpointRecords, err := a.endpointStore.List(ctx)
	if err != nil {
		return
	}
	nameByID := make(map[int64]string, len(endpointRecords))
	for _, record := range endpointRecords {
		nameByID[record.ID] = record.Name
	}
	restored := 0
	for _, record := range records {
		name, ok := nameByID[record.EndpointID]
		if !ok || record.CooldownUntil == nil {
			continue
		}
		a.endpointManager.RestoreEndpointCooldown(name, record.Scope, *record.CooldownUntil, record.CooldownReason)
		restored++
	}
	if restored > 0 {
		a.logger.Info("✅ 已恢复端点持久化 cooldown", "count", restored)
	}
}

// setupPrivacyService 设置隐私保护服务 (v6.1+ SQLite)
// 启动时加载规则并编译快照；编译降级不阻塞启动，通过日志与前端状态可见。
func (a *App) setupPrivacyService() {
	db := a.coreDB()
	if db == nil {
		a.logger.Error("❌ 无法获取数据库连接 (隐私保护)")
		return
	}

	a.privacyStore = store.NewSQLitePrivacyStore(db)
	a.privacyService = service.NewPrivacyService(a.privacyStore)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := a.privacyService.Initialize(ctx); err != nil {
		a.logger.Error("❌ 隐私保护服务初始化失败", "error", err)
		a.privacyService = nil
		a.privacyStore = nil
		return
	}
	if a.privacyService.Status() == service.PrivacyStatusDegraded {
		a.logger.Warn("⚠️ 隐私保护部分规则未激活（编译失败），请在前端检查规则状态")
	}
	a.logger.Info("✅ 隐私保护服务已启用 (SQLite)")
}

// setupModelPricingStore 设置模型定价存储 (v5.0+ SQLite)
func (a *App) setupModelPricingStore() {
	db := a.coreDB()
	if db == nil {
		a.logger.Error("❌ 无法获取数据库连接 (模型定价)")
		return
	}

	// 创建 ModelPricingStore
	a.modelPricingStore = store.NewSQLiteModelPricingStore(db)

	// 创建 ModelPricingService
	a.modelPricingService = service.NewModelPricingService(a.modelPricingStore)

	// 检查是否需要初始化默认数据
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	count, err := a.modelPricingService.GetPricingCount(ctx)
	if err != nil {
		a.logger.Warn("⚠️ 检查模型定价数据失败", "error", err)
		return
	}

	// 如果表为空，初始化默认数据
	if count == 0 {
		a.initDefaultModelPricing(ctx)
	}

	inserted, err := a.ensureGPT56ModelPricing(ctx)
	if err != nil {
		a.logger.Warn("⚠️ 补齐 GPT-5.6 模型定价失败", "error", err)
	} else if inserted > 0 {
		a.logger.Info("✅ 已补齐 GPT-5.6 模型定价", "count", inserted)
	}

	// 加载缓存
	if err := a.modelPricingService.LoadCache(ctx); err != nil {
		a.logger.Warn("⚠️ 加载模型定价缓存失败", "error", err)
	}

	// 同步定价到 UsageTracker（用于成本计算）
	a.syncPricingToTracker(ctx)
	if latestCount, countErr := a.modelPricingService.GetPricingCount(ctx); countErr == nil {
		count = latestCount
	}

	a.logger.Info("✅ 模型定价存储已启用 (SQLite)", "count", count)
}

// initDefaultModelPricing 初始化默认模型定价数据
func (a *App) initDefaultModelPricing(ctx context.Context) {
	// Claude 官方定价 (2025年最新)
	// 5分钟缓存: input * 1.25, 1小时缓存: input * 2.0, 读取: input * 0.1
	defaultPricings := []*store.ModelPricingRecord{
		// 默认定价
		{
			ModelName:            "_default",
			DisplayName:          "默认定价",
			Description:          "未知模型使用的默认定价",
			InputPrice:           3.0,
			OutputPrice:          15.0,
			CacheCreationPrice5m: 3.75, // 3.0 * 1.25
			CacheCreationPrice1h: 6.0,  // 3.0 * 2.0
			CacheReadPrice:       0.30, // 3.0 * 0.1
			IsDefault:            true,
		},
		// Claude Sonnet 4
		{
			ModelName:            "claude-sonnet-4-20250514",
			DisplayName:          "Claude Sonnet 4",
			Description:          "Claude Sonnet 4 (2025-05-14)",
			InputPrice:           3.0,
			OutputPrice:          15.0,
			CacheCreationPrice5m: 3.75,
			CacheCreationPrice1h: 6.0,
			CacheReadPrice:       0.30,
		},
		// Claude 3.5 Sonnet
		{
			ModelName:            "claude-3-5-sonnet-20241022",
			DisplayName:          "Claude 3.5 Sonnet",
			Description:          "Claude 3.5 Sonnet (2024-10-22)",
			InputPrice:           3.0,
			OutputPrice:          15.0,
			CacheCreationPrice5m: 3.75,
			CacheCreationPrice1h: 6.0,
			CacheReadPrice:       0.30,
		},
		// Claude 3.5 Haiku
		{
			ModelName:            "claude-3-5-haiku-20241022",
			DisplayName:          "Claude 3.5 Haiku",
			Description:          "Claude 3.5 Haiku (2024-10-22)",
			InputPrice:           0.80,
			OutputPrice:          4.0,
			CacheCreationPrice5m: 1.0,  // 0.80 * 1.25
			CacheCreationPrice1h: 1.6,  // 0.80 * 2.0
			CacheReadPrice:       0.08, // 0.80 * 0.1
		},
		// Claude Opus 4
		{
			ModelName:            "claude-opus-4-20250514",
			DisplayName:          "Claude Opus 4",
			Description:          "Claude Opus 4 (2025-05-14)",
			InputPrice:           15.0,
			OutputPrice:          75.0,
			CacheCreationPrice5m: 18.75, // 15.0 * 1.25
			CacheCreationPrice1h: 30.0,  // 15.0 * 2.0
			CacheReadPrice:       1.50,  // 15.0 * 0.1
		},
		// ========== Claude 4.5 系列 (2025年最新) ==========
		// Claude Sonnet 4.5
		{
			ModelName:            "claude-sonnet-4-5-20250929",
			DisplayName:          "Claude Sonnet 4.5",
			Description:          "Claude Sonnet 4.5 (2025-09-29)",
			InputPrice:           3.0,
			OutputPrice:          15.0,
			CacheCreationPrice5m: 3.75, // 3.0 * 1.25
			CacheCreationPrice1h: 6.0,  // 3.0 * 2.0
			CacheReadPrice:       0.30, // 3.0 * 0.1
		},
		// Claude Haiku 4.5
		{
			ModelName:            "claude-haiku-4-5-20251001",
			DisplayName:          "Claude Haiku 4.5",
			Description:          "Claude Haiku 4.5 (2025-10-01)",
			InputPrice:           1.0,
			OutputPrice:          5.0,
			CacheCreationPrice5m: 1.25, // 1.0 * 1.25
			CacheCreationPrice1h: 2.0,  // 1.0 * 2.0
			CacheReadPrice:       0.10, // 1.0 * 0.1
		},
		// Claude Opus 4.5
		{
			ModelName:            "claude-opus-4-5-20251101",
			DisplayName:          "Claude Opus 4.5",
			Description:          "Claude Opus 4.5 (2025-11-01)",
			InputPrice:           5.0,
			OutputPrice:          25.0,
			CacheCreationPrice5m: 6.25, // 5.0 * 1.25
			CacheCreationPrice1h: 10.0, // 5.0 * 2.0
			CacheReadPrice:       0.50, // 5.0 * 0.1
		},
		// ========== 旧版本兼容 ==========
		{
			ModelName:            "claude-3-opus-20240229",
			DisplayName:          "Claude 3 Opus",
			Description:          "Claude 3 Opus (2024-02-29)",
			InputPrice:           15.0,
			OutputPrice:          75.0,
			CacheCreationPrice5m: 18.75,
			CacheCreationPrice1h: 30.0,
			CacheReadPrice:       1.50,
		},
		{
			ModelName:            "claude-3-sonnet-20240229",
			DisplayName:          "Claude 3 Sonnet",
			Description:          "Claude 3 Sonnet (2024-02-29)",
			InputPrice:           3.0,
			OutputPrice:          15.0,
			CacheCreationPrice5m: 3.75,
			CacheCreationPrice1h: 6.0,
			CacheReadPrice:       0.30,
		},
		{
			ModelName:            "claude-3-haiku-20240307",
			DisplayName:          "Claude 3 Haiku",
			Description:          "Claude 3 Haiku (2024-03-07)",
			InputPrice:           0.25,
			OutputPrice:          1.25,
			CacheCreationPrice5m: 0.31,  // 0.25 * 1.25
			CacheCreationPrice1h: 0.50,  // 0.25 * 2.0
			CacheReadPrice:       0.025, // 0.25 * 0.1
		},
	}

	if err := a.modelPricingStore.BatchUpsert(ctx, defaultPricings); err != nil {
		a.logger.Error("❌ 初始化默认模型定价失败", "error", err)
		return
	}

	a.logger.Info("✅ 已初始化默认模型定价", "count", len(defaultPricings))
}

// syncPricingToTracker 同步模型定价到 UsageTracker
func (a *App) syncPricingToTracker(ctx context.Context) {
	if a.usageTracker == nil || a.modelPricingService == nil {
		return
	}

	records, err := a.modelPricingService.ListPricings(ctx)
	if err != nil {
		a.logger.Warn("⚠️ 获取模型定价列表失败", "error", err)
		return
	}

	// 转换为 tracking.ModelPricing 格式
	pricing := make(map[string]tracking.ModelPricing)
	for _, r := range records {
		pricing[r.ModelName] = a.modelPricingService.ToTrackingPricing(r)
	}

	// 更新 UsageTracker 的定价缓存
	a.usageTracker.UpdatePricing(pricing)
	a.logger.Debug("已同步模型定价到 UsageTracker", "count", len(pricing))
}

// syncEndpointMultipliersToTracker 同步端点倍率到 UsageTracker
// 成本计算公式：模型基础定价 * 端点倍率
func (a *App) syncEndpointMultipliersToTracker(ctx context.Context) {
	if a.usageTracker == nil || a.endpointStore == nil {
		return
	}

	endpoints, err := a.endpointStore.List(ctx)
	if err != nil {
		a.logger.Warn("⚠️ 获取端点列表失败", "error", err)
		return
	}

	// 转换为 tracking.EndpointMultiplier 格式
	multipliers := make(map[string]tracking.EndpointMultiplier)
	for _, ep := range endpoints {
		multipliers[ep.Name] = tracking.EndpointMultiplier{
			CostMultiplier:                ep.CostMultiplier,
			InputCostMultiplier:           ep.InputCostMultiplier,
			OutputCostMultiplier:          ep.OutputCostMultiplier,
			CacheCreationCostMultiplier:   ep.CacheCreationCostMultiplier,
			CacheCreationCostMultiplier1h: ep.CacheCreationCostMultiplier1h,
			CacheReadCostMultiplier:       ep.CacheReadCostMultiplier,
		}
	}

	// 更新 UsageTracker 的端点倍率缓存
	a.usageTracker.UpdateEndpointMultipliers(multipliers)
	a.logger.Debug("已同步端点倍率到 UsageTracker", "count", len(multipliers))
}

// syncAccountMultipliersToTracker 同步账号倍率到 UsageTracker
// 成本计算公式：模型基础定价 * 账号倍率
func (a *App) syncAccountMultipliersToTracker(ctx context.Context) {
	if a.usageTracker == nil || a.accountPoolStore == nil {
		return
	}

	accounts, err := a.accountPoolStore.ListAccounts(ctx, true)
	if err != nil {
		a.logger.Warn("⚠️ 获取账号池列表失败", "error", err)
		return
	}

	multipliers := make(map[int64]tracking.EndpointMultiplier)
	for _, acc := range accounts {
		if acc == nil || acc.ID <= 0 {
			continue
		}
		if strings.TrimSpace(strings.ToLower(acc.ProviderType)) != "api_key" {
			continue
		}
		multipliers[acc.ID] = tracking.EndpointMultiplier{
			CostMultiplier:                acc.CostMultiplier,
			InputCostMultiplier:           acc.InputCostMultiplier,
			OutputCostMultiplier:          acc.OutputCostMultiplier,
			CacheCreationCostMultiplier:   acc.CacheCreationCostMultiplier,
			CacheCreationCostMultiplier1h: acc.CacheCreationCostMultiplier1h,
			CacheReadCostMultiplier:       acc.CacheReadCostMultiplier,
		}
	}

	a.usageTracker.UpdateAccountMultipliers(multipliers)
	a.logger.Debug("已同步账号倍率到 UsageTracker", "count", len(multipliers))
}

func (a *App) syncAccountMultipliersToTrackerAsync() {
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				if a != nil && a.logger != nil {
					a.logger.Error("❌ 同步账号倍率到 UsageTracker 发生 panic", "panic", recovered)
				}
			}
		}()
		a.syncAccountMultipliersToTracker(context.Background())
	}()
}

// setupUsageTracker 设置使用追踪
func (a *App) setupUsageTracker() {
	if !a.config.UsageTracking.Enabled {
		a.logger.Info("📊 使用追踪已禁用")
		return
	}

	// 确保数据库路径不为空（防止 sqlite_adapter 使用默认相对路径）
	if a.config.UsageTracking.DatabasePath == "" {
		a.config.UsageTracking.DatabasePath = filepath.Join(utils.GetDataDir(), "usage.db")
		a.logger.Warn("⚠️ DatabasePath 为空，已设置为用户目录",
			"path", a.config.UsageTracking.DatabasePath)
	}

	a.logger.Info("📊 初始化使用追踪器", "db_path", a.config.UsageTracking.DatabasePath)

	// v5.0+ 重构：定价配置完全从 SQLite 加载，不再依赖 config.yaml
	// 初始化时使用空定价，后续由 syncPricingToTracker() 从数据库加载
	trackingConfig := &tracking.Config{
		Enabled:         a.config.UsageTracking.Enabled,
		DatabasePath:    a.config.UsageTracking.DatabasePath,
		Database:        a.config.UsageTracking.Database,
		BufferSize:      a.config.UsageTracking.BufferSize,
		BatchSize:       a.config.UsageTracking.BatchSize,
		FlushInterval:   a.config.UsageTracking.FlushInterval,
		MaxRetry:        a.config.UsageTracking.MaxRetry,
		RetentionDays:   a.config.UsageTracking.RetentionDays,
		CleanupInterval: a.config.UsageTracking.CleanupInterval,
		ModelPricing:    nil,                     // v5.0+: 定价从 SQLite model_pricing 表加载
		DefaultPricing:  tracking.ModelPricing{}, // v5.0+: 默认定价从 SQLite 加载
	}

	var err error
	a.usageTracker, err = tracking.NewUsageTrackerWithPolicy(trackingConfig, a.timezonePolicy)
	if err != nil {
		a.logger.Error("使用追踪器初始化失败", "error", err)
		return
	}

	a.logger.Info("📊 使用追踪已启用", "database", a.config.UsageTracking.DatabasePath)
}

// setupProxyHandler 设置代理处理器
func (a *App) setupProxyHandler() {
	// 创建代理处理器
	a.proxyHandler = proxy.NewHandler(a.endpointManager, a.config)
	a.proxyHandler.SetEventBus(a.eventBus)
	if a.settingsService != nil {
		a.proxyHandler.SetCodexIdentityProvider(a.settingsService)
	}

	// 创建中间件
	a.loggingMiddleware = middleware.NewLoggingMiddleware(a.logger)
	a.monitoringMiddleware = middleware.NewMonitoringMiddleware(a.endpointManager)
	a.authMiddleware = middleware.NewAuthMiddleware(a.config.Auth)

	// 连接组件
	a.monitoringMiddleware.SetEventBus(a.eventBus)
	a.loggingMiddleware.SetUsageTracker(a.usageTracker)
	a.loggingMiddleware.SetMonitoringMiddleware(a.monitoringMiddleware)
	a.proxyHandler.SetMonitoringMiddleware(a.monitoringMiddleware)

	if a.usageTracker != nil {
		a.proxyHandler.SetUsageTracker(a.usageTracker)
	}

	if a.accountPoolService != nil {
		a.proxyHandler.SetAccountPoolService(a.accountPoolService)
	}
	if a.codexModelRoutingService != nil {
		a.proxyHandler.SetCodexModelRouter(a.codexModelRoutingService)
	}
	// 请求级故障转移只推送到应用窗口内的全局 Toast，不触发系统通知中心。
	a.proxyHandler.SetOnFailoverTriggered(func(event proxy.FailoverEvent) {
		a.emitFailoverNotification(event)
	})
	a.proxyHandler.SetOnClaudeSubscriptionQuota(a.emitClaudeSubscriptionRateLimit)
	// 🛡️ 注入出站隐私过滤（必须在 SetUsageTracker 之后，重建的 handler 会重新注入）
	if a.privacyService != nil {
		a.proxyHandler.SetPrivacyFilter(a.privacyService)
	}
	a.proxyHandler.SetCodexModelListProvider(&codexModelListProvider{app: a})
	a.proxyHandler.SetClaudeModelListProvider(&claudeModelListProvider{app: a})
	a.proxyHandler.SetImageGenerationConfigProvider(&imageGenerationConfigProvider{app: a})
}

// startProxyServer 启动 HTTP 代理服务器
func (a *App) startProxyServer() {
	mux := http.NewServeMux()

	// 注册监控端点
	a.monitoringMiddleware.RegisterHealthEndpoint(mux)

	// 注册使用追踪健康检查端点
	if a.usageTracker != nil {
		mux.HandleFunc("/health/usage-tracker", func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			defer cancel()

			if err := a.usageTracker.HealthCheck(ctx); err != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				w.Write([]byte(fmt.Sprintf("Usage Tracker unhealthy: %v", err)))
				return
			}

			w.WriteHeader(http.StatusOK)
			w.Write([]byte("Usage Tracker healthy"))
		})
	}

	// 注册代理处理器
	mux.Handle("/", a.loggingMiddleware.Wrap(a.authMiddleware.Wrap(a.proxyHandler)))

	// v5.1+ 端口探测：自动寻找可用端口
	var actualPort int
	var listener net.Listener
	var err error

	if a.portManager != nil {
		// 使用 PortManager 进行端口探测
		listener, actualPort, err = utils.FindAndBind(a.portManager.GetPreferredPort(), 10)
		if err != nil {
			a.logger.Error("❌ 无法找到可用端口", "error", err)
			a.emitError("代理服务器启动失败", "无法找到可用端口: "+err.Error())
			return
		}
		a.portManager.SetActualPort(actualPort)
	} else {
		// 回退到传统方式（首选端口）
		actualPort = a.config.Server.Port
		addr := fmt.Sprintf("%s:%d", a.config.Server.Host, actualPort)
		listener, err = net.Listen("tcp", addr)
		if err != nil {
			a.logger.Error("❌ 端口绑定失败", "port", actualPort, "error", err)
			a.emitError("代理服务器启动失败", fmt.Sprintf("端口 %d 被占用: %v", actualPort, err))
			return
		}
	}

	// 更新配置中的实际端口
	a.config.Server.Port = actualPort

	a.proxyRequests = newProxyRequestLifecycle(mux)
	a.proxyServer = &http.Server{
		Handler:      a.proxyRequests,
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 0, // 流式请求禁用写入超时
		IdleTimeout:  120 * time.Second,
	}

	// 在 goroutine 中启动服务器
	go func() {
		a.logger.Info("🌐 HTTP 代理服务器启动中...",
			"address", listener.Addr().String())

		if err := a.proxyServer.Serve(listener); err != nil && err != http.ErrServerClosed {
			a.logger.Error("代理服务器启动失败", "error", err)
			// 通知前端
			a.emitError("代理服务器启动失败", err.Error())
		}
	}()

	// 等待服务器启动
	time.Sleep(100 * time.Millisecond)

	baseURL := fmt.Sprintf("http://%s:%d", a.config.Server.Host, actualPort)
	a.logger.Info("✅ 代理服务器启动成功",
		"url", baseURL)

	// 端口冲突提示
	if a.portManager != nil {
		portInfo := a.portManager.GetPortInfo()
		if portInfo.WasOccupied {
			a.logger.Warn(fmt.Sprintf("⚠️ 首选端口 %d 被占用，已自动切换到端口 %d",
				portInfo.PreferredPort, portInfo.ActualPort))
		}
	}

	// 安全警告
	if a.config.Server.Host != "127.0.0.1" && a.config.Server.Host != "localhost" && a.config.Server.Host != "::1" {
		if !a.config.Auth.Enabled {
			a.logger.Warn("⚠️  安全警告：服务器绑定到非本地地址但未启用鉴权！")
		}
	}
}

// setupConfigReload 设置配置热重载
func (a *App) setupConfigReload() {
	configWatcher, err := config.NewConfigWatcher(a.configPath, a.logger)
	if err != nil {
		a.logger.Error("❌ 配置热重载初始化失败", "error", err)
		return
	}
	a.configWatcher = configWatcher
	a.configWatcher.AddReloadCallback(func(newCfg *config.Config) error {
		a.mu.Lock()
		defer a.mu.Unlock()

		// 桌面版运行时强制使用用户目录下的日志与数据库路径
		a.applyDesktopRuntimePathOverrides(newCfg)
		if a.timezonePolicy == nil {
			return fmt.Errorf("活动时区策略未初始化")
		}
		if err := a.timezonePolicy.Update(newCfg.Timezone); err != nil {
			return fmt.Errorf("更新活动时区失败: %w", err)
		}
		if a.usageTracker != nil {
			a.usageTracker.OnTimezoneChanged()
		}

		// 更新配置引用
		a.config = newCfg

		// 停止旧的日志 Emitter，避免多个 Emitter 同时广播导致日志重复
		if a.logEmitter != nil {
			a.logEmitter.Stop()
		}

		// 更新日志
		newLogger, newBroadcastHandler := setupLogger(newCfg.Logging)
		slog.SetDefault(newLogger)
		a.logger = newLogger
		a.logHandler = newBroadcastHandler
		a.logEmitter = newBroadcastHandler.Emitter

		// 更新各组件
		a.configWatcher.UpdateLogger(newLogger)
		a.endpointManager.UpdateConfig(newCfg)
		a.proxyHandler.UpdateConfig(newCfg)
		a.authMiddleware.UpdateConfig(newCfg.Auth)

		// v5.0+ 注意：模型定价不再从 config.yaml 热重载
		// 定价配置通过前端「定价」页面管理，存储在 SQLite model_pricing 表中

		a.logger.Info("🔄 配置已重新加载")

		// 通知前端配置已更新
		a.emitConfigReloaded()
		return nil
	})

	a.logger.Info("🔄 配置热重载已启用")
}

// setupEventBridges 设置事件桥接
// 将内部 EventBus 事件转发到 Wails 前端
func (a *App) setupEventBridges() {
	// 注意：当前 EventBus 实现不支持订阅回调
	// 我们使用定时轮询来更新前端状态
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-a.ctx.Done():
				return
			case <-ticker.C:
				if a.isRunning {
					a.emitSystemStatus()
				}
			}
		}
	}()

	a.logger.Info("📡 事件桥接已启用")
}

// startHistoryCollector 启动历史数据收集器
// 定期收集 Metrics 历史数据点，用于图表显示
func (a *App) startHistoryCollector() {
	if a.monitoringMiddleware == nil {
		a.logger.Warn("⚠️  监控中间件未初始化，跳过历史数据收集器启动")
		return
	}

	// 立即收集一次初始数据点
	// 注意：必须直接在原始 *Metrics 上调用 AddHistoryDataPoints()
	// 不能调用 GetMetrics() 获取副本，因为那样修改的是副本而不是原始数据
	metrics := a.monitoringMiddleware.GetMetrics()
	if metrics != nil {
		metrics.AddHistoryDataPoints()
		a.logger.Info("📊 初始历史数据点已收集")
	}

	go func() {
		ticker := time.NewTicker(30 * time.Second) // 每30秒收集一次
		defer ticker.Stop()

		a.logger.Info("📊 历史数据收集器已启动 (30秒间隔)")

		for {
			select {
			case <-a.ctx.Done():
				a.logger.Info("📊 历史数据收集器已停止")
				return
			case <-ticker.C:
				// 收集历史数据点
				// 直接在原始 *Metrics 上调用，而不是获取副本
				metrics := a.monitoringMiddleware.GetMetrics()
				if metrics != nil {
					metrics.AddHistoryDataPoints()
				}
			}
		}
	}()
}

// emitError 发送错误通知到前端
func (a *App) emitError(title, message string) {
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "error", map[string]string{
			"title":   title,
			"message": message,
		})
	}
}

// emitConfigReloaded 通知前端配置已重载
func (a *App) emitConfigReloaded() {
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "config:reloaded", nil)
	}
}

// setupSettingsStore 设置系统设置存储 (v5.1+ SQLite)
func (a *App) setupSettingsStore() {
	db := a.coreDB()
	if db == nil {
		a.logger.Error("❌ 无法获取数据库连接 (设置存储)")
		return
	}

	// 创建 SettingsStore
	a.settingsStore = store.NewSQLiteSettingsStore(db)

	// 创建 SettingsService
	a.settingsService = service.NewSettingsService(a.settingsStore)
	if a.accountPoolService != nil {
		a.accountPoolService.SetSettingsService(a.settingsService)
	}

	// 设置配置变更回调 - 热更新
	a.settingsService.SetOnChangeCallback(func() {
		a.applySettingsToConfig()
	})

	// 初始化默认设置（如果表为空）
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := a.settingsService.InitDefaults(ctx); err != nil {
		a.logger.Error("❌ 初始化默认设置失败", "error", err)
		return
	}

	if err := a.migrateLegacyConnectivitySettings(ctx); err != nil {
		a.logger.Warn("⚠️ 连通性设置迁移失败", "error", err)
	}

	// 从数据库加载设置并应用到配置
	a.applySettingsToConfig()

	// 初始化端口管理器
	preferredPort := a.settingsService.GetInt(ctx, service.CategoryServer, "preferred_port", a.config.Server.Port)
	a.portManager = utils.NewPortManager(preferredPort)

	a.logger.Info("✅ 系统设置存储已启用 (SQLite)")
}

func (a *App) migrateLegacyConnectivitySettings(ctx context.Context) error {
	if a.settingsService == nil || a.settingsStore == nil {
		return nil
	}

	legacyPath := "/v1/models"
	updates := make([]*store.SettingRecord, 0, 2)

	checks := []struct {
		category string
		key      string
	}{
		{category: service.CategoryHealth, key: "health_path"},
		{category: service.CategoryStrategy, key: "fast_test_path"},
	}

	for _, item := range checks {
		record, err := a.settingsService.Get(ctx, item.category, item.key)
		if err != nil {
			return err
		}
		if record != nil && record.Value == legacyPath {
			updates = append(updates, &store.SettingRecord{
				Category: item.category,
				Key:      item.key,
				Value:    "",
			})
		}
	}

	if len(updates) == 0 {
		return nil
	}

	if err := a.settingsStore.BatchUpdateValues(ctx, updates); err != nil {
		return err
	}

	a.logger.Info("🔄 已迁移旧版连通性路径默认值", "updated", len(updates), "from", legacyPath, "to", "<endpoint-url>")
	return nil
}

// setupAccountPoolStore 设置账号池存储 (v6.0+ SQLite)
func (a *App) setupAccountPoolStore() {
	db := a.coreDB()
	if db == nil {
		a.logger.Error("❌ 无法获取数据库连接 (账号池存储)")
		if a.config != nil && a.config.AccountPool.Enabled {
			a.logger.Warn("⚠️ 账号池路由已启用，但数据库未就绪；Codex /v1/responses 与 /v1/responses/compact 将返回账号池未就绪错误")
		}
		return
	}

	a.accountPoolStore = store.NewSQLiteAccountPoolStore(db)
	a.accountPoolService = service.NewAccountPoolService(a.accountPoolStore, a.config)
	if a.settingsService != nil {
		a.accountPoolService.SetSettingsService(a.settingsService)
	}
	a.logger.Info("✅ 账号池存储已启用 (SQLite)")
}

func (a *App) coreDB() *sql.DB {
	if a == nil || a.coreDatabase == nil {
		return nil
	}
	return a.coreDatabase.DB()
}

// applySettingsToConfig 从数据库加载设置并应用到运行时配置
func (a *App) applySettingsToConfig() {
	if a.settingsService == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 策略配置
	a.config.Strategy.Type = a.getSettingString(ctx, service.CategoryStrategy, "type", a.config.Strategy.Type)
	a.config.Strategy.FastTestEnabled = a.settingsService.GetBool(ctx, service.CategoryStrategy, "fast_test_enabled", a.config.Strategy.FastTestEnabled)
	a.config.Strategy.FastTestCacheTTL = a.settingsService.GetDuration(ctx, service.CategoryStrategy, "fast_test_cache_ttl", a.config.Strategy.FastTestCacheTTL)
	a.config.Strategy.FastTestTimeout = a.settingsService.GetDuration(ctx, service.CategoryStrategy, "fast_test_timeout", a.config.Strategy.FastTestTimeout)
	a.config.Strategy.FastTestPath = a.getSettingString(ctx, service.CategoryStrategy, "fast_test_path", a.config.Strategy.FastTestPath)

	// 重试配置
	a.config.Retry.MaxAttempts = a.settingsService.GetInt(ctx, service.CategoryRetry, "max_attempts", a.config.Retry.MaxAttempts)
	a.config.Retry.BaseDelay = a.settingsService.GetDuration(ctx, service.CategoryRetry, "base_delay", a.config.Retry.BaseDelay)
	a.config.Retry.MaxDelay = a.settingsService.GetDuration(ctx, service.CategoryRetry, "max_delay", a.config.Retry.MaxDelay)
	a.config.Retry.Multiplier = a.settingsService.GetFloat(ctx, service.CategoryRetry, "multiplier", a.config.Retry.Multiplier)

	// 健康检查配置
	a.config.Health.CheckInterval = a.settingsService.GetDuration(ctx, service.CategoryHealth, "check_interval", a.config.Health.CheckInterval)
	a.config.Health.Timeout = a.settingsService.GetDuration(ctx, service.CategoryHealth, "timeout", a.config.Health.Timeout)
	a.config.Health.HealthPath = a.getSettingString(ctx, service.CategoryHealth, "health_path", a.config.Health.HealthPath)

	// 请求控制配置
	a.config.GlobalTimeout = a.settingsService.GetDuration(ctx, service.CategoryRequest, "global_timeout", a.config.GlobalTimeout)
	// v7：eof_retry_hint 设置键不变，赋值目标迁移到 Streaming（挂起体系已删除）
	a.config.Streaming.EOFRetryHint = a.settingsService.GetBool(ctx, service.CategoryRequest, "eof_retry_hint", a.config.Streaming.EOFRetryHint)

	// 失败处理配置（默认开启）
	a.config.FailureTracker.Enabled = true
	a.config.FailureTracker.TimeWindow = a.settingsService.GetDuration(ctx, service.CategoryRequest, "failure_time_window", a.config.FailureTracker.TimeWindow)
	a.config.FailureTracker.Threshold = a.settingsService.GetInt(ctx, service.CategoryRequest, "failure_threshold", a.config.FailureTracker.Threshold)
	a.config.FailureTracker.Action = a.getSettingString(ctx, service.CategoryRequest, "failure_action", a.config.FailureTracker.Action)
	a.config.Failover.DefaultCooldown = a.settingsService.GetDuration(ctx, service.CategoryRequest, "failover_cooldown", a.config.Failover.DefaultCooldown)

	// v7 D2 迁移：挂起体系已删除，旧值 suspend 一次性改写为 failover 并落库
	if a.config.FailureTracker.Action == "suspend" {
		slog.Warn("⚠️ [设置迁移] failure_action='suspend' 已废弃（挂起体系删除），自动改写为 'failover'")
		a.config.FailureTracker.Action = "failover"
		if err := a.settingsService.Set(ctx, service.CategoryRequest, "failure_action", "failover"); err != nil {
			slog.Warn("⚠️ [设置迁移] failure_action 落库失败（不影响本次运行）", "error", err)
		}
	}

	// v8：failure_action 与 Failover.Enabled 独立——reject 语义由 ShouldRejectRequest
	// 在请求入口判定（§12 D15），不再关闭「请求内换候选」总开关；
	// count_tokens 与重放安全硬失败 fallback 不受 failure_action 影响
	a.config.Failover.Enabled = true
	// 🔧 同步更新旧字段

	// 流式传输配置
	a.config.Streaming.HeartbeatInterval = a.settingsService.GetDuration(ctx, service.CategoryStreaming, "heartbeat_interval", a.config.Streaming.HeartbeatInterval)
	a.config.Streaming.ReadTimeout = a.settingsService.GetDuration(ctx, service.CategoryStreaming, "read_timeout", a.config.Streaming.ReadTimeout)
	a.config.Streaming.MaxIdleTime = a.settingsService.GetDuration(ctx, service.CategoryStreaming, "max_idle_time", a.config.Streaming.MaxIdleTime)
	a.config.Streaming.ResponseHeaderTimeout = a.settingsService.GetDuration(ctx, service.CategoryStreaming, "response_header_timeout", a.config.Streaming.ResponseHeaderTimeout)

	// 访问控制配置
	a.config.Auth.Enabled = a.settingsService.GetBool(ctx, service.CategoryAuth, "enabled", a.config.Auth.Enabled)
	a.config.Auth.Token = a.getSettingString(ctx, service.CategoryAuth, "token", a.config.Auth.Token)

	// Token 计数配置
	a.config.TokenCounting.Enabled = a.settingsService.GetBool(ctx, service.CategoryTokenCounting, "enabled", a.config.TokenCounting.Enabled)
	a.config.TokenCounting.EstimationRatio = a.settingsService.GetFloat(ctx, service.CategoryTokenCounting, "estimation_ratio", a.config.TokenCounting.EstimationRatio)

	// 数据保留配置
	a.config.UsageTracking.RetentionDays = a.settingsService.GetInt(ctx, service.CategoryRetention, "retention_days", a.config.UsageTracking.RetentionDays)
	a.config.UsageTracking.CleanupInterval = a.settingsService.GetDuration(ctx, service.CategoryRetention, "cleanup_interval", a.config.UsageTracking.CleanupInterval)

	a.logger.Debug("已从数据库加载设置")

	// 更新各组件配置
	if a.endpointManager != nil {
		a.endpointManager.UpdateConfig(a.config)
	}
	if a.proxyHandler != nil {
		a.proxyHandler.UpdateConfig(a.config)
	}
	if a.authMiddleware != nil {
		a.authMiddleware.UpdateConfig(a.config.Auth)
	}
}

// getSettingString 获取字符串设置值（带默认值）
func (a *App) getSettingString(ctx context.Context, category, key, defaultVal string) string {
	val, err := a.settingsService.GetValue(ctx, category, key)
	if err != nil || val == "" {
		return defaultVal
	}
	return val
}
