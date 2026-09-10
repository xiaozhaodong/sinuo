package tracking

import (
	"context"
	"database/sql"
	"embed"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cc-forwarder/config"
	timezonepolicy "cc-forwarder/internal/timezone"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaFS embed.FS

// UsageStatsDetailed 详细的使用统计
type UsageStatsDetailed struct {
	TotalRequests   int64                   `json:"total_requests"`
	SuccessRequests int64                   `json:"success_requests"`
	ErrorRequests   int64                   `json:"error_requests"`
	TotalTokens     int64                   `json:"total_tokens"`
	TotalCost       float64                 `json:"total_cost"`
	ModelStats      map[string]ModelStat    `json:"model_stats"`
	EndpointStats   map[string]EndpointStat `json:"endpoint_stats"`
}

// ModelStat 模型统计
type ModelStat struct {
	RequestCount int64   `json:"request_count"`
	TotalCost    float64 `json:"total_cost"`
}

// EndpointStat 端点统计
type EndpointStat struct {
	RequestCount int64   `json:"request_count"`
	TotalCost    float64 `json:"total_cost"`
}

const (
	RequestFamilyClaude = "claude"
	RequestFamilyCodex  = "codex"
	RequestFamilyImage  = "image"
	RequestFamilyOther  = "other"

	RequestSourceLegacyProxy            = "legacy_proxy"
	RequestSourceCodexSubscriptionProxy = "codex_subscription_proxy"

	RequestKindLegacy     = "legacy"
	RequestKindGeneration = "generation"
	RequestKindCompaction = "compaction"
)

func normalizeRequestFamily(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case RequestFamilyClaude:
		return RequestFamilyClaude
	case RequestFamilyCodex:
		return RequestFamilyCodex
	case RequestFamilyImage:
		return RequestFamilyImage
	default:
		return RequestFamilyOther
	}
}

func normalizeRequestSource(value string) (string, error) {
	switch strings.TrimSpace(value) {
	case "", RequestSourceLegacyProxy:
		return RequestSourceLegacyProxy, nil
	case RequestSourceCodexSubscriptionProxy:
		return RequestSourceCodexSubscriptionProxy, nil
	default:
		return "", fmt.Errorf("invalid request source %q", value)
	}
}

func normalizeRequestKind(value string) (string, error) {
	switch strings.TrimSpace(value) {
	case "", RequestKindLegacy:
		return RequestKindLegacy, nil
	case RequestKindGeneration:
		return RequestKindGeneration, nil
	case RequestKindCompaction:
		return RequestKindCompaction, nil
	default:
		return "", fmt.Errorf("invalid request kind %q", value)
	}
}

func requestFamilyFromPath(path string) string {
	switch {
	case path == "/v1/messages" || path == "/v1/messages/count_tokens":
		return RequestFamilyClaude
	case path == "/v1/responses" || path == "/v1/responses/compact" || path == "/v1/alpha/search":
		return RequestFamilyCodex
	case strings.HasPrefix(path, "/v1/images/"):
		return RequestFamilyImage
	default:
		return RequestFamilyOther
	}
}

// RequestEvent 表示请求事件
type RequestEvent struct {
	Type      string      `json:"type"` // "start", "flexible_update", "success", "final_failure", "complete", "failed_request_tokens", "token_recovery"
	RequestID string      `json:"request_id"`
	Timestamp time.Time   `json:"timestamp"`
	Data      interface{} `json:"data"` // 根据Type不同而变化
}

// RequestStartData 请求开始事件数据
type RequestStartData struct {
	ClientIP           string `json:"client_ip"`
	UserAgent          string `json:"user_agent"`
	Method             string `json:"method"`
	Path               string `json:"path"`
	RequestFamily      string `json:"request_family"`
	IsStreaming        bool   `json:"is_streaming"`                   // 是否为流式请求
	UpstreamType       string `json:"upstream_type,omitempty"`        // endpoint/account
	UpstreamSourceName string `json:"upstream_source_name,omitempty"` // 订阅源名或固定来源
	UpstreamName       string `json:"upstream_name,omitempty"`        // 账号名或端点名
	UpstreamID         int64  `json:"upstream_id,omitempty"`          // 上游ID（账号ID）
	RequestSource      string `json:"request_source,omitempty"`
	RequestKind        string `json:"request_kind,omitempty"`
	UsageKnown         bool   `json:"usage_known"`
}

// RequestDimensions 是开始记录时必须原子写入的来源与分类维度。
type RequestDimensions struct {
	RequestSource      string
	RequestKind        string
	UpstreamType       string
	UpstreamSourceName string
	UpstreamName       string
	UpstreamID         int64
	UsageKnown         bool
}

// RequestSettlement 用于需要保留 unknown usage/HTTP 状态的独立结算路径。
// TokenUsage=nil 表示上游未报告用量，数据库 token 与 cost 字段必须保持 NULL。
type RequestSettlement struct {
	Status        string
	ModelName     string
	Path          string
	FailureReason string
	Duration      time.Duration
	HTTPStatus    *int
	TokenUsage    *TokenUsage
}

// RequestUpdateData 请求更新事件数据
type RequestUpdateData struct {
	EndpointName string `json:"endpoint_name"`
	Status       string `json:"status"`
	RetryCount   int    `json:"retry_count"`
	HTTPStatus   int    `json:"http_status"`
	// 🚀 [状态机重构] Phase 2: 新增失败原因和取消原因字段
	FailureReason string `json:"failure_reason,omitempty"`
	CancelReason  string `json:"cancel_reason,omitempty"`
}

// RequestCompleteData 请求完成事件数据
type RequestCompleteData struct {
	ModelName             string        `json:"model_name"`
	Path                  string        `json:"path,omitempty"`
	InputTokens           int64         `json:"input_tokens"`
	OutputTokens          int64         `json:"output_tokens"`
	CacheCreationTokens   int64         `json:"cache_creation_tokens"`
	CacheCreation5mTokens int64         `json:"cache_creation_5m_tokens"` // v5.0.1+
	CacheCreation1hTokens int64         `json:"cache_creation_1h_tokens"` // v5.0.1+
	CacheReadTokens       int64         `json:"cache_read_tokens"`
	Duration              time.Duration `json:"duration"`
	FailureReason         string        `json:"failure_reason,omitempty"` // 可选：失败原因
	CostOverrideUSD       *float64      `json:"cost_override_usd,omitempty"`
	HasTokenUsage         bool          `json:"has_token_usage"`
}

// TokenUsage token使用统计
type TokenUsage struct {
	InputTokens           int64
	OutputTokens          int64
	CacheCreationTokens   int64 // 总缓存创建 tokens（向后兼容，= 5m + 1h）
	CacheReadTokens       int64
	CacheCreation5mTokens int64 // 5分钟缓存创建 tokens (1.25x 定价)
	CacheCreation1hTokens int64 // 1小时缓存创建 tokens (2x 定价)
}

// ModelPricing 模型定价配置
type ModelPricing struct {
	Input           float64 `yaml:"input"`             // per 1M tokens
	Output          float64 `yaml:"output"`            // per 1M tokens
	CacheCreation   float64 `yaml:"cache_creation"`    // per 1M tokens (5分钟缓存创建，1.25x input)
	CacheCreation1h float64 `yaml:"cache_creation_1h"` // per 1M tokens (1小时缓存创建，2x input)
	CacheRead       float64 `yaml:"cache_read"`        // per 1M tokens (缓存读取)
}

// EndpointMultiplier 端点成本倍率（v5.0+ 支持端点级别的成本调整）
// 成本计算公式：模型基础定价 * 端点倍率
type EndpointMultiplier struct {
	CostMultiplier                float64 // 总体倍率（如果 > 0，会覆盖分项倍率）
	InputCostMultiplier           float64 // 输入成本倍率
	OutputCostMultiplier          float64 // 输出成本倍率
	CacheCreationCostMultiplier   float64 // 5分钟缓存创建成本倍率
	CacheCreationCostMultiplier1h float64 // 1小时缓存创建成本倍率
	CacheReadCostMultiplier       float64 // 缓存读取成本倍率
}

// DefaultEndpointMultiplier 返回默认端点倍率（全部为 1.0）
func DefaultEndpointMultiplier() EndpointMultiplier {
	return EndpointMultiplier{
		CostMultiplier:                1.0,
		InputCostMultiplier:           1.0,
		OutputCostMultiplier:          1.0,
		CacheCreationCostMultiplier:   1.0,
		CacheCreationCostMultiplier1h: 1.0,
		CacheReadCostMultiplier:       1.0,
	}
}

// CostBreakdown 成本分解结构
type CostBreakdown struct {
	InputCost           float64
	OutputCost          float64
	CacheCreationCost   float64 // 总缓存创建成本 (5m + 1h)
	CacheCreation5mCost float64 // 5分钟缓存创建成本
	CacheCreation1hCost float64 // 1小时缓存创建成本
	CacheReadCost       float64
	TotalCost           float64
}

// CalculateCostV2 统一的成本计算函数（v5.0+ 支持分开的缓存定价）
// 消除 archive_manager.go 和 tracker.go 中的重复代码
//
// 参数:
//   - usage: Token 使用量（包含分开的 5m/1h 缓存 tokens）
//   - pricing: 模型定价，nil 时返回零成本
//   - multiplier: 端点倍率，nil 时使用默认倍率 1.0
//   - requestPath: 可选，请求路径。对于 /v1/responses，input_tokens 已包含 cached_tokens，需要先扣除 cache_read_tokens 再按输入价格计费。
func CalculateCostV2(usage *TokenUsage, pricing *ModelPricing, multiplier *EndpointMultiplier, requestPath ...string) CostBreakdown {
	if pricing == nil || usage == nil {
		return CostBreakdown{}
	}

	path := ""
	if len(requestPath) > 0 {
		path = requestPath[0]
	}

	// 使用默认倍率
	var m EndpointMultiplier
	if multiplier != nil {
		m = *multiplier
	} else {
		m = DefaultEndpointMultiplier()
	}

	// 确保倍率有效（0 或负数视为 1.0）
	ensureMultiplier := func(v float64) float64 {
		if v <= 0 {
			return 1.0
		}
		return v
	}

	// 获取 1h 缓存定价（如果未设置，使用 2x input 作为默认）
	cacheCreation1hPrice := pricing.CacheCreation1h
	if cacheCreation1hPrice <= 0 {
		cacheCreation1hPrice = pricing.Input * 2.0 // 默认 2x input
	}

	// 基础成本计算（每百万 token）
	billableInputTokens := usage.InputTokens
	if shouldExcludeCacheReadFromInput(path) {
		billableInputTokens -= usage.CacheReadTokens
		if billableInputTokens < 0 {
			billableInputTokens = 0
		}
	}
	inputCost := float64(billableInputTokens) * pricing.Input / 1_000_000
	outputCost := float64(usage.OutputTokens) * pricing.Output / 1_000_000
	cacheReadCost := float64(usage.CacheReadTokens) * pricing.CacheRead / 1_000_000

	// 分开计算 5m 和 1h 缓存成本
	var cache5mCost, cache1hCost float64
	if usage.CacheCreation5mTokens > 0 || usage.CacheCreation1hTokens > 0 {
		// 有分开的缓存数据，分别计算
		cache5mCost = float64(usage.CacheCreation5mTokens) * pricing.CacheCreation / 1_000_000
		cache1hCost = float64(usage.CacheCreation1hTokens) * cacheCreation1hPrice / 1_000_000
	} else {
		// 向后兼容：只有总数，默认使用 5m 定价
		cache5mCost = float64(usage.CacheCreationTokens) * pricing.CacheCreation / 1_000_000
		cache1hCost = 0
	}
	cacheCreationCost := cache5mCost + cache1hCost

	// 应用端点倍率
	if m.CostMultiplier > 0 {
		// 总体倍率模式：所有成本统一乘以总体倍率
		totalMultiplier := ensureMultiplier(m.CostMultiplier)
		inputCost *= totalMultiplier
		outputCost *= totalMultiplier
		cache5mCost *= totalMultiplier
		cache1hCost *= totalMultiplier
		cacheCreationCost *= totalMultiplier
		cacheReadCost *= totalMultiplier
	} else {
		// 分项倍率模式：各项成本分别乘以对应倍率
		inputCost *= ensureMultiplier(m.InputCostMultiplier)
		outputCost *= ensureMultiplier(m.OutputCostMultiplier)
		cacheReadCost *= ensureMultiplier(m.CacheReadCostMultiplier)
		cache5mCost *= ensureMultiplier(m.CacheCreationCostMultiplier)
		cache1hCost *= ensureMultiplier(m.CacheCreationCostMultiplier1h)
		cacheCreationCost = cache5mCost + cache1hCost
	}

	return CostBreakdown{
		InputCost:           inputCost,
		OutputCost:          outputCost,
		CacheCreationCost:   cacheCreationCost,
		CacheCreation5mCost: cache5mCost,
		CacheCreation1hCost: cache1hCost,
		CacheReadCost:       cacheReadCost,
		TotalCost:           inputCost + outputCost + cacheCreationCost + cacheReadCost,
	}
}

func shouldExcludeCacheReadFromInput(path string) bool {
	normalized := strings.ToLower(strings.TrimSpace(path))
	if normalized == "" {
		return false
	}
	return normalized == "/v1/responses" ||
		strings.HasSuffix(normalized, "/v1/responses") ||
		strings.HasSuffix(normalized, "/responses") ||
		strings.HasSuffix(normalized, "/responses/compact")
}

// CalculateCost 统一的成本计算函数（向后兼容）
// Deprecated: 使用 CalculateCostV2 以支持分开的缓存定价
//
// 参数:
//   - inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens: Token 使用量
//   - pricing: 模型定价，nil 时返回零成本
//   - multiplier: 端点倍率，nil 时使用默认倍率 1.0
//   - use1hCache: 是否使用 1 小时缓存倍率（用于长效缓存场景）
func CalculateCost(inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens int64,
	pricing *ModelPricing, multiplier *EndpointMultiplier, use1hCache bool) CostBreakdown {

	// 构建 TokenUsage，根据 use1hCache 决定放入哪个字段
	usage := &TokenUsage{
		InputTokens:     inputTokens,
		OutputTokens:    outputTokens,
		CacheReadTokens: cacheReadTokens,
	}
	if use1hCache {
		usage.CacheCreation1hTokens = cacheCreationTokens
	} else {
		usage.CacheCreation5mTokens = cacheCreationTokens
	}
	usage.CacheCreationTokens = cacheCreationTokens // 保持总数

	return CalculateCostV2(usage, pricing, multiplier)
}

// Config 使用跟踪配置
type Config struct {
	Enabled bool `yaml:"enabled"`

	// 向后兼容：保留原有的 database_path 配置
	DatabasePath string `yaml:"database_path"`

	// 新增：数据库配置（优先级高于 DatabasePath）
	Database *config.DatabaseBackendConfig `yaml:"database,omitempty"`

	BufferSize      int                     `yaml:"buffer_size"`
	BatchSize       int                     `yaml:"batch_size"`
	FlushInterval   time.Duration           `yaml:"flush_interval"`
	MaxRetry        int                     `yaml:"max_retry"`
	RetentionDays   int                     `yaml:"retention_days"`
	CleanupInterval time.Duration           `yaml:"cleanup_interval"`
	ModelPricing    map[string]ModelPricing `yaml:"model_pricing"`
	DefaultPricing  ModelPricing            `yaml:"default_pricing"`

	// 🔥 v4.1 新增：热池配置
	HotPool *HotPoolSettings `yaml:"hot_pool,omitempty"`
}

// HotPoolSettings 热池配置
type HotPoolSettings struct {
	Enabled          bool          `yaml:"enabled"`            // 是否启用热池模式（默认true）
	MaxAge           time.Duration `yaml:"max_age"`            // 最大存活时间（默认30分钟）
	MaxSize          int           `yaml:"max_size"`           // 最大容量（默认10000）
	CleanupInterval  time.Duration `yaml:"cleanup_interval"`   // 清理间隔（默认1分钟）
	ArchiveOnCleanup bool          `yaml:"archive_on_cleanup"` // 清理时是否归档（默认true）
}

// WriteRequest 写操作请求
type WriteRequest struct {
	Query     string
	Args      []interface{}
	Response  chan error
	Context   context.Context
	EventType string // 用于调试和监控
}

// UpdateOptions 统一的请求更新选项
// 支持可选字段更新，只更新非nil的字段
type UpdateOptions struct {
	EndpointName       *string        // 端点名称
	RequestFamily      *string        // 请求类型 claude/codex/image/other
	UpstreamType       *string        // 上游类型 endpoint/account
	UpstreamSourceName *string        // 上游来源（订阅源）
	UpstreamName       *string        // 上游名称（账号名/端点名）
	UpstreamID         *int64         // 上游ID（账号ID）
	RequestSource      *string        // 入口来源 legacy_proxy/codex_subscription_proxy
	RequestKind        *string        // legacy/generation/compaction
	IsStreaming        *bool          // 是否为流式请求
	Status             *string        // 状态
	RetryCount         *int           // 重试次数
	HttpStatus         *int           // HTTP状态码
	ModelName          *string        // 模型名称
	EndTime            *time.Time     // 结束时间
	Duration           *time.Duration // 持续时间
	FirstTokenMs       *int64         // 上游请求写完到首个有效流式响应耗时(毫秒)
	CompletionMs       *int64         // 首个有效流式响应到流式完成耗时(毫秒)
	FailureReason      *string        // 失败原因（用于中间过程记录）
	RouteMode          *string        // Claude 路由模式
	RequestedEndpoint  *string        // 手动指定端点
	EffectiveEndpoint  *string        // 实际路由端点
	FallbackReason     *string        // fallback 或阻塞原因
	RouteDecisionAt    *time.Time     // 路由决策时间

	// v6.2 生命周期面板新增字段
	UpstreamWriteMs      *int64  // 上游请求写完相对 start_time 的偏移（毫秒），write-once
	ScheduleSnapshotJSON *string // 账号池/端点调度快照 JSON（裁剪 DTO），覆盖写
	PrivacyScanJSON      *string // 隐私扫描摘要 JSON，覆盖写
}

// UsageTracker 使用跟踪器
type UsageTracker struct {
	// 原有字段（兼容性）
	db           *sql.DB
	eventChan    chan RequestEvent
	config       *Config
	pricing      map[string]ModelPricing       // 模型定价缓存
	endpointMu   map[string]EndpointMultiplier // 端点倍率缓存
	accountMu    map[int64]EndpointMultiplier  // 账号倍率缓存
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	mu           sync.RWMutex
	errorHandler *ErrorHandler

	// 配置时区的共享原子快照。
	timezonePolicy *timezonepolicy.Policy

	// 新增：数据库适配器
	adapter DatabaseAdapter // 数据库适配器接口

	// 新增：读写分离组件（从适配器获取）
	readDB     *sql.DB           // 读连接池 (多连接)
	writeDB    *sql.DB           // 写连接 (单连接)
	writeQueue chan WriteRequest // 写操作队列
	writeMu    sync.Mutex        // 写操作保护锁
	writeWg    sync.WaitGroup    // 写处理器等待组

	summaryMu            sync.RWMutex
	summaryReadyTimezone string
	summaryStartDate     string
	summaryEndDate       string
	summaryRebuildMu     sync.Mutex
	summaryWg            sync.WaitGroup

	// 🔥 v4.1 新增：内存热池 + 归档架构
	hotPool        *HotPool        // 内存热池（活跃请求）
	archiveManager *ArchiveManager // 归档管理器（批量写入）
	hotPoolEnabled bool            // 是否启用热池模式
}

func (ut *UsageTracker) reconnectDatabases() error {
	if ut.config == nil || !ut.config.Enabled {
		return fmt.Errorf("usage tracker is not enabled")
	}

	ut.mu.RLock()
	config := ut.config
	ut.mu.RUnlock()

	dbConfig, err := buildDatabaseConfig(config)
	if err != nil {
		return fmt.Errorf("failed to build database config: %w", err)
	}

	adapter, err := NewDatabaseAdapter(dbConfig)
	if err != nil {
		return fmt.Errorf("failed to create database adapter: %w", err)
	}
	if err := adapter.Open(); err != nil {
		return fmt.Errorf("failed to open database adapter: %w", err)
	}
	if err := adapter.InitSchema(); err != nil {
		_ = adapter.Close()
		return fmt.Errorf("failed to initialize database schema after reconnect: %w", err)
	}

	readDB := adapter.GetReadDB()
	writeDB := adapter.GetWriteDB()
	if readDB == nil || writeDB == nil {
		_ = adapter.Close()
		return fmt.Errorf("database adapter returned nil read/write connection")
	}

	ut.writeMu.Lock()
	oldAdapter := ut.adapter

	ut.mu.Lock()
	ut.adapter = adapter
	ut.readDB = readDB
	ut.writeDB = writeDB
	ut.db = readDB
	ut.mu.Unlock()

	ut.writeMu.Unlock()

	if oldAdapter != nil {
		if err := oldAdapter.Close(); err != nil {
			slog.Warn("Failed to close previous database adapter during reconnect", "error", err)
		}
	}

	return nil
}

// NewUsageTracker 创建新的使用跟踪器
func NewUsageTracker(config *Config, globalTimezone ...string) (*UsageTracker, error) {
	name := "Asia/Shanghai"
	if len(globalTimezone) > 0 && strings.TrimSpace(globalTimezone[0]) != "" {
		name = strings.TrimSpace(globalTimezone[0])
	}
	policy, err := timezonepolicy.New(name)
	if err != nil {
		return nil, err
	}
	return NewUsageTrackerWithPolicy(config, policy)
}

func NewUsageTrackerWithPolicy(config *Config, policy *timezonepolicy.Policy) (*UsageTracker, error) {
	if config == nil || !config.Enabled {
		return &UsageTracker{config: config, timezonePolicy: policy}, nil
	}
	if policy == nil || policy.Name() == "" {
		return nil, fmt.Errorf("timezone policy is required")
	}

	// 设置默认值
	if config.BufferSize <= 0 {
		config.BufferSize = 1000
	}
	if config.BatchSize <= 0 {
		config.BatchSize = 100
	}
	if config.FlushInterval <= 0 {
		config.FlushInterval = 30 * time.Second
	}
	if config.MaxRetry <= 0 {
		config.MaxRetry = 3
	}
	if config.CleanupInterval <= 0 {
		config.CleanupInterval = 24 * time.Hour // 默认24小时清理一次
	}

	// 构建数据库配置。数据库存储格式固定为 UTC，不再携带展示时区。
	dbConfig, err := buildDatabaseConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to build database config: %w", err)
	}

	// 创建数据库适配器
	adapter, err := NewDatabaseAdapter(dbConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create database adapter: %w", err)
	}

	// 打开数据库连接
	if err := adapter.Open(); err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// 获取读写连接（从适配器）
	readDB := adapter.GetReadDB()
	writeDB := adapter.GetWriteDB()

	// 保持原有db字段兼容性（指向readDB）
	db := readDB

	ctx, cancel := context.WithCancel(context.Background())

	ut := &UsageTracker{
		// 原有字段（兼容性）
		db:        db, // 兼容性：指向readDB
		eventChan: make(chan RequestEvent, config.BufferSize),
		config:    config,
		pricing:   config.ModelPricing,
		ctx:       ctx,
		cancel:    cancel,

		timezonePolicy: policy,

		// 新增：数据库适配器
		adapter: adapter,

		// 读写分离组件（从适配器获取）
		readDB:     readDB,
		writeDB:    writeDB,
		writeQueue: make(chan WriteRequest, config.BufferSize), // 与事件队列容量一致
	}

	// 初始化错误处理器
	ut.errorHandler = NewErrorHandler(ut, slog.Default())

	// 初始化数据库Schema（使用适配器）
	if err := ut.initDatabaseWithAdapter(); err != nil {
		cancel()
		adapter.Close()
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}

	// 启动写操作处理器
	ut.writeWg.Add(1)
	go ut.processWriteQueue()

	// 启动异步事件处理器
	ut.wg.Add(1)
	go ut.processEvents()

	// 启动定期清理任务
	ut.wg.Add(1)
	go ut.periodicCleanup()

	// 启动定期备份任务
	ut.wg.Add(1)
	go ut.periodicBackup()

	// 🔥 v4.1 初始化热池架构
	ut.initHotPool()
	if err := ut.rebuildRecentUsageSummary(context.Background(), 7); err != nil {
		cancel()
		_ = ut.archiveManager.Close()
		_ = adapter.Close()
		return nil, fmt.Errorf("initialize usage summary cache: %w", err)
	}

	slog.Info("✅ 使用跟踪器初始化完成",
		"database_type", adapter.GetDatabaseType(),
		"buffer_size", config.BufferSize,
		"batch_size", config.BatchSize,
		"hot_pool_enabled", ut.hotPoolEnabled)

	return ut, nil
}

// TimezonePolicy 返回 tracker 使用的统一活动时区策略，供同进程 API 层复用。
func (ut *UsageTracker) TimezonePolicy() *timezonepolicy.Policy {
	if ut == nil {
		return nil
	}
	return ut.timezonePolicy
}

// initHotPool 初始化热池和归档管理器
func (ut *UsageTracker) initHotPool() {
	// 检查是否启用热池模式（默认启用）
	hotPoolEnabled := true
	var hotPoolConfig HotPoolConfig

	if ut.config.HotPool != nil {
		// 使用配置文件中的设置
		if !ut.config.HotPool.Enabled {
			hotPoolEnabled = false
		}
		hotPoolConfig = HotPoolConfig{
			MaxAge:           ut.config.HotPool.MaxAge,
			MaxSize:          ut.config.HotPool.MaxSize,
			CleanupInterval:  ut.config.HotPool.CleanupInterval,
			ArchiveOnCleanup: ut.config.HotPool.ArchiveOnCleanup,
		}
	} else {
		// 使用默认配置
		hotPoolConfig = DefaultHotPoolConfig()
	}

	if !hotPoolEnabled {
		slog.Info("🔥 热池模式已禁用，使用传统事件队列模式")
		ut.hotPoolEnabled = false
		return
	}

	// 创建热池
	ut.hotPool = NewHotPool(hotPoolConfig)

	// 创建归档管理器
	archiveConfig := ArchiveManagerConfig{
		ChannelSize:   ut.config.BufferSize,
		BatchSize:     ut.config.BatchSize,
		FlushInterval: ut.config.FlushInterval,
		MaxRetry:      ut.config.MaxRetry,
	}
	ut.archiveManager = NewArchiveManager(ut.adapter, archiveConfig, ut.pricing)

	// 设置双向引用：ArchiveManager 需要访问 HotPool 来清理归档缓存
	ut.archiveManager.SetHotPool(ut.hotPool)

	// 设置热池归档回调
	ut.hotPool.SetArchiveCallback(func(req *ActiveRequest) {
		if ut.archiveManager != nil {
			ut.archiveManager.Archive(req)
		}
	})

	ut.hotPoolEnabled = true
	slog.Info("🔥 热池模式已启用",
		"max_age", hotPoolConfig.MaxAge,
		"max_size", hotPoolConfig.MaxSize)
}

// now 返回 UTC 时刻；业务日期必须显式通过 timezonePolicy 计算。
func (ut *UsageTracker) now() time.Time {
	return time.Now().UTC()
}

// buildDatabaseConfig 从Config构建DatabaseConfig
// v4.1.0: 简化为仅支持 SQLite
func buildDatabaseConfig(config *Config) (DatabaseConfig, error) {
	var dbConfig DatabaseConfig

	// 设置数据库类型（v4.1+ 仅支持 SQLite）
	dbConfig.Type = "sqlite"

	// 优先使用新的Database配置
	if config.Database != nil {
		// 检查是否配置了 MySQL（已废弃）
		if config.Database.Type == "mysql" {
			slog.Warn("⚠️  MySQL 配置已废弃，将使用 SQLite",
				"reason", "v4.1.0 移除了 MySQL 支持",
				"suggestion", "请修改配置 type: sqlite 或删除 database.type 配置")
		}
		dbConfig.DatabasePath = config.Database.Path
	} else {
		// 向后兼容：使用原有的DatabasePath配置
		dbConfig.DatabasePath = config.DatabasePath
	}

	// 设置默认数据库路径 - 使用跨平台用户目录
	if dbConfig.DatabasePath == "" {
		// 使用 internal/utils 提供的跨平台目录
		// Windows: %APPDATA%\AI-Switchboard\data\usage.db
		// macOS: ~/Library/Application Support/AI-Switchboard/data/usage.db
		// Linux: ~/.local/share/ai-switchboard/data/usage.db
		dbConfig.DatabasePath = filepath.Join(getAppDataDir(), "data", "usage.db")
	}

	return dbConfig, nil
}

// getAppDataDir 获取应用数据目录（跨平台）
// 复制自 internal/utils/appdir.go，避免循环依赖
// Windows: %APPDATA%\AI-Switchboard
// macOS: ~/Library/Application Support/AI-Switchboard
// Linux: ~/.local/share/ai-switchboard
func getAppDataDir() string {
	var baseDir string

	switch runtime.GOOS {
	case "windows":
		baseDir = os.Getenv("APPDATA")
		if baseDir == "" {
			baseDir = filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Roaming")
		}
		return filepath.Join(baseDir, "AI-Switchboard")

	case "darwin":
		homeDir, _ := os.UserHomeDir()
		return filepath.Join(homeDir, "Library", "Application Support", "AI-Switchboard")

	case "linux":
		homeDir, _ := os.UserHomeDir()
		xdgDataHome := os.Getenv("XDG_DATA_HOME")
		if xdgDataHome != "" {
			return filepath.Join(xdgDataHome, "ai-switchboard")
		}
		return filepath.Join(homeDir, ".local", "share", "ai-switchboard")

	default:
		homeDir, _ := os.UserHomeDir()
		return filepath.Join(homeDir, ".ai-switchboard")
	}
}

// initDatabaseWithAdapter 使用适配器初始化数据库
func (ut *UsageTracker) initDatabaseWithAdapter() error {
	if ut.adapter == nil {
		return fmt.Errorf("database adapter not initialized")
	}

	// 使用适配器初始化Schema
	if err := ut.adapter.InitSchema(); err != nil {
		return fmt.Errorf("failed to initialize database schema: %w", err)
	}

	slog.Info("数据库Schema初始化完成",
		"database_type", ut.adapter.GetDatabaseType())

	return nil
}

// Close 关闭使用跟踪器
func (ut *UsageTracker) Close() error {
	if ut.config == nil || !ut.config.Enabled {
		return nil
	}

	// 先检查是否已经关闭
	ut.mu.RLock()
	if ut.cancel == nil {
		ut.mu.RUnlock()
		return nil // 已经关闭过
	}
	ut.mu.RUnlock()

	slog.Info("Shutting down usage tracker...")

	// 取消上下文（不需要持有锁）
	ut.cancel()

	// 等待所有协程完成（包括写处理器）
	ut.wg.Wait()
	ut.writeWg.Wait() // 等待写处理器完成
	ut.summaryWg.Wait()

	// 现在可以安全地持有写锁进行清理
	ut.mu.Lock()
	defer ut.mu.Unlock()

	ut.cancel = nil // 标记为已关闭

	// 关闭事件通道
	if ut.eventChan != nil {
		close(ut.eventChan)
		ut.eventChan = nil
	}

	// 关闭写操作队列
	if ut.writeQueue != nil {
		close(ut.writeQueue)
		ut.writeQueue = nil
	}

	// 🔥 v4.1 关闭热池和归档管理器
	if ut.hotPool != nil {
		if err := ut.hotPool.Close(); err != nil {
			slog.Error("Failed to close hot pool", "error", err)
		}
		ut.hotPool = nil
	}
	if ut.archiveManager != nil {
		if err := ut.archiveManager.Close(); err != nil {
			slog.Error("Failed to close archive manager", "error", err)
		}
		ut.archiveManager = nil
	}

	// 关闭数据库适配器（会自动处理所有连接）
	if ut.adapter != nil {
		if err := ut.adapter.Close(); err != nil {
			slog.Error("Failed to close database adapter", "error", err)
			return fmt.Errorf("failed to close database adapter: %w", err)
		}
		ut.adapter = nil
	}

	// 清理连接引用（这些现在由adapter管理）
	ut.readDB = nil
	ut.writeDB = nil
	ut.db = nil

	slog.Info("✅ 使用跟踪器关闭完成")
	return nil
}

// RecordRequestStart 记录请求开始
func (ut *UsageTracker) RecordRequestStart(requestID, clientIP, userAgent, method, path string, isStreaming bool) {
	ut.RecordRequestStartWithFamily(requestID, clientIP, userAgent, method, path, requestFamilyFromPath(path), isStreaming)
}

// RecordRequestStartWithFamily 记录请求开始；请求类型必须由入口显式传入。
func (ut *UsageTracker) RecordRequestStartWithFamily(requestID, clientIP, userAgent, method, path, requestFamily string, isStreaming bool) {
	_ = ut.RecordRequestStartAtWithDimensions(requestID, clientIP, userAgent, method, path, requestFamily, isStreaming, ut.now(), RequestDimensions{
		RequestSource: RequestSourceLegacyProxy,
		RequestKind:   RequestKindLegacy,
		UpstreamType:  "endpoint",
		UsageKnown:    true,
	})
}

// RecordRequestStartAtWithDimensions 原子登记请求的来源、种类和实际归属。
// 新订阅入口必须显式传 generation/compaction；非法值直接拒绝，不能降级为 legacy。
func (ut *UsageTracker) RecordRequestStartAtWithDimensions(requestID, clientIP, userAgent, method, path, requestFamily string, isStreaming bool, startedAt time.Time, dimensions RequestDimensions) error {
	if ut.config == nil || !ut.config.Enabled {
		return nil
	}
	requestFamily = normalizeRequestFamily(requestFamily)
	requestSource, err := normalizeRequestSource(dimensions.RequestSource)
	if err != nil {
		return err
	}
	requestKind, err := normalizeRequestKind(dimensions.RequestKind)
	if err != nil {
		return err
	}
	if requestSource == RequestSourceLegacyProxy && requestKind != RequestKindLegacy {
		return fmt.Errorf("request source %q only accepts kind %q", requestSource, RequestKindLegacy)
	}
	if requestSource == RequestSourceCodexSubscriptionProxy && requestKind == RequestKindLegacy {
		return fmt.Errorf("request source %q requires generation or compaction kind", requestSource)
	}
	if startedAt.IsZero() {
		startedAt = ut.now()
	}
	upstreamType := strings.TrimSpace(dimensions.UpstreamType)
	if upstreamType == "" {
		upstreamType = "endpoint"
	}
	dimensions.RequestSource = requestSource
	dimensions.RequestKind = requestKind
	dimensions.UpstreamType = upstreamType

	// 🔥 v4.1 热池模式：直接添加到内存热池
	if ut.hotPoolEnabled && ut.hotPool != nil {
		req := NewActiveRequestWithDimensions(requestID, clientIP, userAgent, method, path, requestFamily, isStreaming, dimensions)
		req.StartTime = startedAt
		if err := ut.hotPool.Add(req); err != nil {
			slog.Warn("🔥 热池添加请求失败，降级到事件队列模式",
				"request_id", requestID,
				"error", err)
			// 降级到传统模式
			ut.recordRequestStartLegacy(requestID, clientIP, userAgent, method, path, requestFamily, isStreaming, startedAt, dimensions)
		}
		return nil
	}

	// 传统模式：发送事件到队列
	ut.recordRequestStartLegacy(requestID, clientIP, userAgent, method, path, requestFamily, isStreaming, startedAt, dimensions)
	return nil
}

// recordRequestStartLegacy 传统模式记录请求开始
func (ut *UsageTracker) recordRequestStartLegacy(requestID, clientIP, userAgent, method, path, requestFamily string, isStreaming bool, startedAt time.Time, dimensions RequestDimensions) {
	event := RequestEvent{
		Type:      "start",
		RequestID: requestID,
		Timestamp: startedAt,
		Data: RequestStartData{
			ClientIP:           clientIP,
			UserAgent:          userAgent,
			Method:             method,
			Path:               path,
			RequestFamily:      requestFamily,
			IsStreaming:        isStreaming,
			UpstreamType:       dimensions.UpstreamType,
			UpstreamSourceName: dimensions.UpstreamSourceName,
			UpstreamName:       dimensions.UpstreamName,
			UpstreamID:         dimensions.UpstreamID,
			RequestSource:      dimensions.RequestSource,
			RequestKind:        dimensions.RequestKind,
			UsageKnown:         dimensions.UsageKnown,
		},
	}

	select {
	case ut.eventChan <- event:
		// 成功发送事件
	default:
		// 缓冲区满时的处理策略
		slog.Warn("Usage tracking event buffer full, dropping start event",
			"request_id", requestID)
	}
}

// RecordRequestUpdate 统一的请求更新方法
// 支持可选字段更新，只更新非nil的字段，适用于所有中间过程状态变更
func (ut *UsageTracker) RecordRequestUpdate(requestID string, opts UpdateOptions) {
	if ut.config == nil || !ut.config.Enabled {
		return
	}
	if opts.RequestSource != nil {
		value, err := normalizeRequestSource(*opts.RequestSource)
		if err != nil {
			slog.Warn("拒绝非法请求来源更新", "request_id", requestID, "error", err)
			return
		}
		opts.RequestSource = &value
	}
	if opts.RequestKind != nil {
		value, err := normalizeRequestKind(*opts.RequestKind)
		if err != nil {
			slog.Warn("拒绝非法请求种类更新", "request_id", requestID, "error", err)
			return
		}
		opts.RequestKind = &value
	}

	// 🔥 v4.1 热池模式：直接更新内存（核心优化点：无数据库写入）
	if ut.hotPoolEnabled && ut.hotPool != nil {
		err := ut.hotPool.Update(requestID, func(req *ActiveRequest) {
			// 只更新非nil的字段
			if opts.EndpointName != nil {
				req.EndpointName = *opts.EndpointName
			}
			if opts.RequestFamily != nil {
				req.RequestFamily = normalizeRequestFamily(*opts.RequestFamily)
			}
			if opts.UpstreamType != nil {
				req.UpstreamType = *opts.UpstreamType
			}
			if opts.UpstreamSourceName != nil {
				req.UpstreamSourceName = *opts.UpstreamSourceName
			}
			if opts.UpstreamName != nil {
				req.UpstreamName = *opts.UpstreamName
			}
			if opts.UpstreamID != nil {
				req.UpstreamID = *opts.UpstreamID
			}
			if opts.RequestSource != nil {
				req.RequestSource = *opts.RequestSource
			}
			if opts.RequestKind != nil {
				req.RequestKind = *opts.RequestKind
			}
			if opts.IsStreaming != nil {
				req.IsStreaming = *opts.IsStreaming
			}
			if opts.RouteMode != nil {
				req.RouteMode = *opts.RouteMode
			}
			if opts.RequestedEndpoint != nil {
				req.RequestedEndpoint = *opts.RequestedEndpoint
			}
			if opts.EffectiveEndpoint != nil {
				req.EffectiveEndpoint = *opts.EffectiveEndpoint
			}
			if opts.FallbackReason != nil {
				req.FallbackReason = *opts.FallbackReason
			}
			if opts.RouteDecisionAt != nil {
				decisionAt := *opts.RouteDecisionAt
				req.RouteDecisionAt = &decisionAt
			}
			if opts.Status != nil {
				req.Status = *opts.Status
			}
			if opts.RetryCount != nil {
				req.RetryCount = *opts.RetryCount
			}
			if opts.HttpStatus != nil {
				req.HTTPStatus = *opts.HttpStatus
				req.HTTPStatusKnown = true
			}
			if opts.ModelName != nil {
				req.ModelName = *opts.ModelName
			}
			if opts.FirstTokenMs != nil && *opts.FirstTokenMs >= 0 && req.FirstTokenMs == nil {
				firstTokenMs := *opts.FirstTokenMs
				req.FirstTokenMs = &firstTokenMs
			}
			if opts.CompletionMs != nil && *opts.CompletionMs >= 0 && req.CompletionMs == nil {
				completionMs := *opts.CompletionMs
				req.CompletionMs = &completionMs
			}
			if opts.FailureReason != nil {
				req.FailureReason = *opts.FailureReason
			}
			if opts.UpstreamWriteMs != nil && *opts.UpstreamWriteMs >= 0 && req.UpstreamWriteMs == nil {
				upstreamWriteMs := *opts.UpstreamWriteMs
				req.UpstreamWriteMs = &upstreamWriteMs
			}
			if opts.ScheduleSnapshotJSON != nil {
				req.ScheduleSnapshotJSON = *opts.ScheduleSnapshotJSON
			}
			if opts.PrivacyScanJSON != nil {
				req.PrivacyScanJSON = *opts.PrivacyScanJSON
			}
		})
		if err != nil {
			// 请求可能不在热池中（已归档或从未记录），降级到传统模式
			slog.Debug("🔥 热池更新请求失败，降级到事件队列模式",
				"request_id", requestID,
				"error", err)
			ut.recordRequestUpdateLegacy(requestID, opts)
		}
		return
	}

	// 传统模式：发送事件到队列
	ut.recordRequestUpdateLegacy(requestID, opts)
}

// recordRequestUpdateLegacy 传统模式更新请求
func (ut *UsageTracker) recordRequestUpdateLegacy(requestID string, opts UpdateOptions) {
	event := RequestEvent{
		Type:      "flexible_update",
		RequestID: requestID,
		Timestamp: ut.now(),
		Data:      opts,
	}

	select {
	case ut.eventChan <- event:
		// 成功发送事件
	default:
		slog.Warn("Usage tracking event buffer full, dropping flexible_update event",
			"request_id", requestID)
	}
}

// RecordRequestSuccess 记录请求成功完成
// 一次性更新所有成功相关字段：status='completed', end_time, duration_ms, Token和成本信息
func (ut *UsageTracker) RecordRequestSuccess(requestID, modelName string, tokens *TokenUsage, duration time.Duration) {
	ut.RecordRequestSuccessWithQuality(requestID, modelName, tokens, duration, "")
}

// RecordRequestSuccessWithQuality 记录请求成功完成（支持数据质量标记）
// 🔧 [方案A实现] 2025-12-20: 原子操作，在 CompleteAndArchive 中一次性设置所有字段包括 failureReason
// 一次性更新所有成功相关字段：status='completed', end_time, duration_ms, Token、成本信息和可选的 failure_reason
func (ut *UsageTracker) RecordRequestSuccessWithQuality(requestID, modelName string, tokens *TokenUsage, duration time.Duration, failureReason string) {
	ut.recordRequestSuccessWithQualityAndCost(requestID, modelName, tokens, duration, failureReason, nil)
}

// RecordRequestSuccessWithCost 以调用方提供的固定成本完成请求。
// 用于图像生成等不按 Token 计价的请求。
func (ut *UsageTracker) RecordRequestSuccessWithCost(requestID, modelName string, duration time.Duration, costUSD float64) {
	if costUSD < 0 {
		costUSD = 0
	}
	ut.recordRequestSuccessWithQualityAndCost(requestID, modelName, nil, duration, "", &costUSD)
}

func (ut *UsageTracker) recordRequestSuccessWithQualityAndCost(requestID, modelName string, tokens *TokenUsage, duration time.Duration, failureReason string, costOverrideUSD *float64) {
	if ut.config == nil || !ut.config.Enabled {
		return
	}

	// 🚀 [架构修复] 支持 nil tokens，确保耗时信息总是被记录
	var inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens int64
	var cacheCreation5mTokens, cacheCreation1hTokens int64 // v5.0.1+
	if tokens != nil {
		inputTokens = tokens.InputTokens
		outputTokens = tokens.OutputTokens
		cacheCreationTokens = tokens.CacheCreationTokens
		cacheCreation5mTokens = tokens.CacheCreation5mTokens
		cacheCreation1hTokens = tokens.CacheCreation1hTokens
		cacheReadTokens = tokens.CacheReadTokens

		// 🔧 [向后兼容修复] v5.0.1+: 如果 5m/1h 都是 0 但总数不为 0，
		// 将总数分配给 5m 字段，与成本计算逻辑保持一致（tracker.go:185-193）
		if cacheCreation5mTokens == 0 && cacheCreation1hTokens == 0 && cacheCreationTokens > 0 {
			cacheCreation5mTokens = cacheCreationTokens // 默认按 5m 缓存处理
			slog.Debug("🔄 [缓存Token分配] 向后兼容模式：将总缓存Token分配到5m字段",
				"request_id", requestID,
				"cache_creation_tokens", cacheCreationTokens)
		}
	}
	// 如果 tokens 为 nil，所有 token 字段都是 0，但 duration 仍然会被记录

	// 🔥 v4.1 热池模式：完成请求并归档（核心：单次数据库写入）
	if ut.hotPoolEnabled && ut.hotPool != nil {
		now := ut.now()
		err := ut.hotPool.CompleteAndArchive(requestID, func(req *ActiveRequest) {
			req.Status = "completed"
			req.ModelName = modelName
			if tokens != nil {
				req.InputTokens = inputTokens
				req.OutputTokens = outputTokens
				req.CacheCreationTokens = cacheCreationTokens
				req.CacheCreation5mTokens = cacheCreation5mTokens // v5.0.1+
				req.CacheCreation1hTokens = cacheCreation1hTokens // v5.0.1+
				req.CacheReadTokens = cacheReadTokens
				req.UsageKnown = true
				req.CostKnown = true
			}
			if costOverrideUSD != nil {
				req.CostKnown = true
			}
			req.EndTime = &now
			req.DurationMs = duration.Milliseconds()
			// 🔧 [方案A实现] 2025-12-20: 显式覆盖 failureReason（无论是否为空）
			// 避免之前中途错误设置的旧值残留，导致"成功但带失败原因"的误标
			req.FailureReason = failureReason
			req.CostOverrideUSD = costOverrideUSD
			// 成本在归档时计算
		})
		if err != nil {
			// 请求可能不在热池中，降级到传统模式
			slog.Debug("🔥 热池完成请求失败，降级到事件队列模式",
				"request_id", requestID,
				"error", err)
			ut.recordRequestSuccessLegacy(requestID, modelName, inputTokens, outputTokens, cacheCreationTokens, cacheCreation5mTokens, cacheCreation1hTokens, cacheReadTokens, duration, failureReason, costOverrideUSD, tokens != nil)
		}
		return
	}

	// 传统模式：发送事件到队列
	ut.recordRequestSuccessLegacy(requestID, modelName, inputTokens, outputTokens, cacheCreationTokens, cacheCreation5mTokens, cacheCreation1hTokens, cacheReadTokens, duration, failureReason, costOverrideUSD, tokens != nil)
}

// recordRequestSuccessLegacy 传统模式记录请求成功
// 🔧 [方案A实现] 2025-12-20: 增加 failureReason 参数支持
func (ut *UsageTracker) recordRequestSuccessLegacy(
	requestID, modelName string,
	inputTokens, outputTokens, cacheCreationTokens, cacheCreation5mTokens, cacheCreation1hTokens, cacheReadTokens int64,
	duration time.Duration,
	failureReason string,
	costOverrideUSD *float64,
	hasTokenUsage bool,
) {
	path := ""
	if ut.hotPoolEnabled && ut.hotPool != nil {
		if req, ok := ut.hotPool.Get(requestID); ok && req != nil {
			path = req.Path
		}
	}
	if path == "" {
		path = ut.lookupRequestPath(requestID)
	}

	event := RequestEvent{
		Type:      "success",
		RequestID: requestID,
		Timestamp: ut.now(),
		Data: RequestCompleteData{
			ModelName:             modelName,
			Path:                  path,
			InputTokens:           inputTokens,
			OutputTokens:          outputTokens,
			CacheCreationTokens:   cacheCreationTokens,
			CacheCreation5mTokens: cacheCreation5mTokens,
			CacheCreation1hTokens: cacheCreation1hTokens,
			CacheReadTokens:       cacheReadTokens,
			Duration:              duration,
			FailureReason:         failureReason,
			CostOverrideUSD:       costOverrideUSD,
			HasTokenUsage:         hasTokenUsage,
		},
	}

	select {
	case ut.eventChan <- event:
		// 成功发送事件
	default:
		slog.Warn("Usage tracking event buffer full, dropping success event",
			"request_id", requestID)
	}
}

// RecordRequestFinalFailure 记录请求最终失败或取消
// 一次性更新所有失败/取消相关字段：status, end_time, duration_ms, failure_reason/cancel_reason, http_status_code, 可选Token
// 🔧 [修复] 2025-12-11: 添加 modelName 参数，确保取消/失败请求能正确计算成本
func (ut *UsageTracker) RecordRequestFinalFailure(requestID, modelName, status, reason, errorDetail string, duration time.Duration, httpStatus int, tokens *TokenUsage) {
	if ut.config == nil || !ut.config.Enabled {
		return
	}

	// 处理Token信息（失败/取消时可能有也可能没有）
	var inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens int64
	var cacheCreation5mTokens, cacheCreation1hTokens int64 // 🔧 [修复] 2025-12-11: 添加 5m/1h 缓存字段
	if tokens != nil {
		inputTokens = tokens.InputTokens
		outputTokens = tokens.OutputTokens
		cacheCreationTokens = tokens.CacheCreationTokens
		cacheCreation5mTokens = tokens.CacheCreation5mTokens
		cacheCreation1hTokens = tokens.CacheCreation1hTokens
		cacheReadTokens = tokens.CacheReadTokens

		// 🔧 [向后兼容修复] v5.0.1+: 如果 5m/1h 都是 0 但总数不为 0，
		// 将总数分配给 5m 字段，与成本计算逻辑保持一致
		if cacheCreation5mTokens == 0 && cacheCreation1hTokens == 0 && cacheCreationTokens > 0 {
			cacheCreation5mTokens = cacheCreationTokens
		}
	}

	// 🔥 v4.1 热池模式：完成失败请求并归档
	if ut.hotPoolEnabled && ut.hotPool != nil {
		now := ut.now()
		err := ut.hotPool.CompleteAndArchive(requestID, func(req *ActiveRequest) {
			req.Status = status
			req.HTTPStatus = httpStatus
			req.HTTPStatusKnown = true
			// 🔧 [修复] 2025-12-11: 设置模型名，否则成本计算会失败
			if modelName != "" && modelName != "unknown" {
				req.ModelName = modelName
			}
			// 🔧 [修复] 2025-12-15: 只有当 tokens 参数不为 nil 时才更新 token 字段
			// 否则保留热池中已有的值（可能由 RecordFailedRequestTokens 设置）
			if tokens != nil {
				req.InputTokens = inputTokens
				req.OutputTokens = outputTokens
				req.CacheCreationTokens = cacheCreationTokens
				req.CacheCreation5mTokens = cacheCreation5mTokens
				req.CacheCreation1hTokens = cacheCreation1hTokens
				req.CacheReadTokens = cacheReadTokens
				req.UsageKnown = true
				req.CostKnown = true
			}
			req.EndTime = &now
			req.DurationMs = duration.Milliseconds()
			// 根据状态设置原因字段
			if status == "cancelled" {
				req.CancelReason = reason
			} else {
				req.FailureReason = reason
				if errorDetail != "" {
					req.FailureReason = reason + ": " + errorDetail
				}
			}
		})
		if err != nil {
			// 请求可能不在热池中，降级到传统模式
			slog.Debug("🔥 热池完成失败请求失败，降级到事件队列模式",
				"request_id", requestID,
				"error", err)
			ut.recordRequestFinalFailureLegacy(requestID, modelName, status, reason, errorDetail, duration, httpStatus, inputTokens, outputTokens, cacheCreationTokens, cacheCreation5mTokens, cacheCreation1hTokens, cacheReadTokens, tokens != nil)
		}
		return
	}

	// 传统模式：发送事件到队列
	ut.recordRequestFinalFailureLegacy(requestID, modelName, status, reason, errorDetail, duration, httpStatus, inputTokens, outputTokens, cacheCreationTokens, cacheCreation5mTokens, cacheCreation1hTokens, cacheReadTokens, tokens != nil)
}

// recordRequestFinalFailureLegacy 传统模式记录请求失败
// 🔧 [修复] 2025-12-11: 添加 modelName 和 5m/1h 缓存字段参数
func (ut *UsageTracker) recordRequestFinalFailureLegacy(requestID, modelName, status, reason, errorDetail string, duration time.Duration, httpStatus int, inputTokens, outputTokens, cacheCreationTokens, cacheCreation5mTokens, cacheCreation1hTokens, cacheReadTokens int64, hasTokenUsage bool) {
	event := RequestEvent{
		Type:      "final_failure",
		RequestID: requestID,
		Timestamp: ut.now(),
		Data: map[string]interface{}{
			"status":                   status, // "failed" or "cancelled"
			"reason":                   reason, // failure_reason or cancel_reason
			"error_detail":             errorDetail,
			"duration":                 duration,
			"http_status":              httpStatus, // HTTP状态码
			"model_name":               modelName,  // 🔧 [修复] 2025-12-11: 添加模型名
			"input_tokens":             inputTokens,
			"output_tokens":            outputTokens,
			"cache_creation_tokens":    cacheCreationTokens,
			"cache_creation_5m_tokens": cacheCreation5mTokens, // 🔧 [修复] 2025-12-11
			"cache_creation_1h_tokens": cacheCreation1hTokens, // 🔧 [修复] 2025-12-11
			"cache_read_tokens":        cacheReadTokens,
			"has_token_usage":          hasTokenUsage,
		},
	}

	select {
	case ut.eventChan <- event:
		// 成功发送事件
	default:
		slog.Warn("Usage tracking event buffer full, dropping final_failure event",
			"request_id", requestID)
	}
}

// RecordRequestSettlement 原子结算订阅入口请求，并保留 unknown usage 与 NULL HTTP 状态。
func (ut *UsageTracker) RecordRequestSettlement(requestID string, settlement RequestSettlement) error {
	if ut.config == nil || !ut.config.Enabled {
		return nil
	}
	if settlement.Status != "completed" && settlement.Status != "failed" && settlement.Status != "cancelled" {
		return fmt.Errorf("invalid request settlement status %q", settlement.Status)
	}
	if settlement.Duration < 0 {
		settlement.Duration = 0
	}

	if ut.hotPoolEnabled && ut.hotPool != nil {
		now := ut.now()
		err := ut.hotPool.CompleteAndArchive(requestID, func(req *ActiveRequest) {
			req.Status = settlement.Status
			if settlement.ModelName != "" && settlement.ModelName != "default" {
				req.ModelName = settlement.ModelName
			}
			req.FailureReason = settlement.FailureReason
			if settlement.HTTPStatus != nil {
				req.HTTPStatus = *settlement.HTTPStatus
				req.HTTPStatusKnown = true
			} else {
				req.HTTPStatus = 0
				req.HTTPStatusKnown = false
			}
			if settlement.TokenUsage != nil {
				req.InputTokens = settlement.TokenUsage.InputTokens
				req.OutputTokens = settlement.TokenUsage.OutputTokens
				req.CacheCreationTokens = settlement.TokenUsage.CacheCreationTokens
				req.CacheCreation5mTokens = settlement.TokenUsage.CacheCreation5mTokens
				req.CacheCreation1hTokens = settlement.TokenUsage.CacheCreation1hTokens
				req.CacheReadTokens = settlement.TokenUsage.CacheReadTokens
				req.UsageKnown = true
				req.CostKnown = true
			} else {
				req.UsageKnown = false
				req.CostKnown = false
			}
			req.EndTime = &now
			req.DurationMs = settlement.Duration.Milliseconds()
		})
		if err == nil {
			return nil
		}
		slog.Debug("🔥 热池订阅结算失败，降级到事件队列模式", "request_id", requestID, "error", err)
	}

	event := RequestEvent{
		Type:      "subscription_settlement",
		RequestID: requestID,
		Timestamp: ut.now(),
		Data:      settlement,
	}
	select {
	case ut.eventChan <- event:
		return nil
	default:
		return fmt.Errorf("usage tracking event buffer full while settling %s", requestID)
	}
}

// RecordFailedRequestTokens 记录失败请求的Token使用
// 只记录Token统计，不影响请求状态
func (ut *UsageTracker) RecordFailedRequestTokens(requestID, modelName string, tokens *TokenUsage, duration time.Duration, failureReason string) {
	if ut.config == nil || !ut.config.Enabled || tokens == nil {
		return
	}

	// 🔥 v4.1 热池模式：直接更新内存中的请求数据
	// 这样后续的 CompleteAndArchive 会将正确的 token 信息写入数据库
	if ut.hotPoolEnabled && ut.hotPool != nil {
		err := ut.hotPool.Update(requestID, func(req *ActiveRequest) {
			// 更新 Token 信息
			req.InputTokens = tokens.InputTokens
			req.OutputTokens = tokens.OutputTokens
			req.CacheCreationTokens = tokens.CacheCreationTokens
			req.CacheCreation5mTokens = tokens.CacheCreation5mTokens
			req.CacheCreation1hTokens = tokens.CacheCreation1hTokens
			req.CacheReadTokens = tokens.CacheReadTokens
			req.UsageKnown = true
			req.CostKnown = true
			// 更新模型名（如果有）
			if modelName != "" && modelName != "unknown" {
				req.ModelName = modelName
			}
			// 更新持续时间
			if duration > 0 {
				req.DurationMs = duration.Milliseconds()
			}
			// 记录失败原因（不改变状态）
			if failureReason != "" && req.FailureReason == "" {
				req.FailureReason = failureReason
			}
		})
		if err == nil {
			slog.Debug(fmt.Sprintf("🔥 [热池Token更新] [%s] 原因: %s, 模型: %s, 输入: %d, 输出: %d",
				requestID, failureReason, modelName, tokens.InputTokens, tokens.OutputTokens))
			return
		}
		// 请求不在热池中（可能已归档），降级到传统模式
		slog.Debug(fmt.Sprintf("🔥 [热池Token更新失败] [%s] 降级到事件队列模式, 错误: %v",
			requestID, err))
	}

	// 传统模式：发送事件到队列（UPDATE 已存在的数据库记录）
	event := RequestEvent{
		Type:      "failed_request_tokens",
		RequestID: requestID,
		Timestamp: ut.now(),
		Data: RequestCompleteData{
			ModelName:             modelName,
			Path:                  ut.lookupRequestPath(requestID),
			InputTokens:           tokens.InputTokens,
			OutputTokens:          tokens.OutputTokens,
			CacheCreationTokens:   tokens.CacheCreationTokens,
			CacheCreation5mTokens: tokens.CacheCreation5mTokens,
			CacheCreation1hTokens: tokens.CacheCreation1hTokens,
			CacheReadTokens:       tokens.CacheReadTokens,
			Duration:              duration,
			FailureReason:         failureReason,
			HasTokenUsage:         true,
		},
	}

	select {
	case ut.eventChan <- event:
		slog.Debug(fmt.Sprintf("💾 [失败Token事件] [%s] 原因: %s, 模型: %s", requestID, failureReason, modelName))
	default:
		slog.Warn("Usage tracking event buffer full, dropping failed request tokens event",
			"request_id", requestID)
	}
}

// RecoverRequestTokens 恢复请求的Token使用统计
// 🔧 [Fallback修复] 专用于debug文件恢复场景，仅更新Token字段，不触发状态变更
func (ut *UsageTracker) RecoverRequestTokens(requestID, modelName string, tokens *TokenUsage) {
	if ut.config == nil || !ut.config.Enabled || tokens == nil {
		return
	}

	// 🔥 v4.1 热池模式：如果请求还在热池中，先更新热池
	if ut.hotPoolEnabled && ut.hotPool != nil {
		err := ut.hotPool.Update(requestID, func(req *ActiveRequest) {
			// 更新 Token 信息
			req.InputTokens = tokens.InputTokens
			req.OutputTokens = tokens.OutputTokens
			req.CacheCreationTokens = tokens.CacheCreationTokens
			req.CacheCreation5mTokens = tokens.CacheCreation5mTokens
			req.CacheCreation1hTokens = tokens.CacheCreation1hTokens
			req.CacheReadTokens = tokens.CacheReadTokens
			req.UsageKnown = true
			req.CostKnown = true
			// 更新模型名（如果有）
			if modelName != "" && modelName != "unknown" {
				req.ModelName = modelName
			}
		})
		if err == nil {
			slog.Info(fmt.Sprintf("🔥 [热池Token恢复] [%s] 模型: %s, 输入: %d, 输出: %d",
				requestID, modelName, tokens.InputTokens, tokens.OutputTokens))
			return
		}
		// 请求不在热池中（已归档），继续使用传统模式更新数据库
		slog.Debug(fmt.Sprintf("🔥 [热池Token恢复失败] [%s] 请求已归档，使用UPDATE更新数据库",
			requestID))
	}

	// 传统模式：发送事件到队列（UPDATE 已存在的数据库记录）
	event := RequestEvent{
		Type:      "token_recovery",
		RequestID: requestID,
		Timestamp: ut.now(),
		Data: RequestCompleteData{
			ModelName:             modelName,
			Path:                  ut.lookupRequestPath(requestID),
			InputTokens:           tokens.InputTokens,
			OutputTokens:          tokens.OutputTokens,
			CacheCreationTokens:   tokens.CacheCreationTokens,
			CacheCreation5mTokens: tokens.CacheCreation5mTokens,
			CacheCreation1hTokens: tokens.CacheCreation1hTokens,
			CacheReadTokens:       tokens.CacheReadTokens,
			Duration:              0, // 不更新时间相关字段
			HasTokenUsage:         true,
		},
	}

	select {
	case ut.eventChan <- event:
		slog.Info(fmt.Sprintf("🔧 [Token恢复事件] [%s] 模型: %s, 输入: %d, 输出: %d",
			requestID, modelName, tokens.InputTokens, tokens.OutputTokens))
	default:
		slog.Warn("Usage tracking event buffer full, dropping token recovery event",
			"request_id", requestID)
	}
}

// UpdatePricing 更新模型定价（运行时动态更新）
func (ut *UsageTracker) UpdatePricing(pricing map[string]ModelPricing) {
	ut.mu.Lock()
	defer ut.mu.Unlock()

	ut.pricing = pricing

	// 同步到 ArchiveManager
	if ut.archiveManager != nil {
		ut.archiveManager.UpdatePricing(pricing)
	}

	slog.Info("Model pricing updated", "model_count", len(pricing))
}

// UpdateEndpointMultipliers 更新端点成本倍率
// 成本计算公式：模型基础定价 * 端点倍率
func (ut *UsageTracker) UpdateEndpointMultipliers(multipliers map[string]EndpointMultiplier) {
	ut.mu.Lock()
	defer ut.mu.Unlock()

	ut.endpointMu = cloneEndpointMultiplierMap(multipliers)

	// 同步到 ArchiveManager
	if ut.archiveManager != nil {
		ut.archiveManager.UpdateEndpointMultipliers(multipliers)
	}

	slog.Info("Endpoint multipliers updated", "endpoint_count", len(multipliers))
}

// UpdateAccountMultipliers 更新账号成本倍率
func (ut *UsageTracker) UpdateAccountMultipliers(multipliers map[int64]EndpointMultiplier) {
	ut.mu.Lock()
	defer ut.mu.Unlock()

	ut.accountMu = cloneAccountMultiplierMap(multipliers)

	if ut.archiveManager != nil {
		ut.archiveManager.UpdateAccountMultipliers(multipliers)
	}

	slog.Info("Account multipliers updated", "account_count", len(multipliers))
}

// GetEndpointMultiplier 获取端点倍率（用于成本计算）
func (ut *UsageTracker) GetEndpointMultiplier(endpointName string) EndpointMultiplier {
	ut.mu.RLock()
	defer ut.mu.RUnlock()

	if ut.endpointMu == nil {
		return EndpointMultiplier{
			CostMultiplier:                1.0,
			InputCostMultiplier:           1.0,
			OutputCostMultiplier:          1.0,
			CacheCreationCostMultiplier:   1.0,
			CacheCreationCostMultiplier1h: 1.0,
			CacheReadCostMultiplier:       1.0,
		}
	}

	if m, exists := ut.endpointMu[endpointName]; exists {
		return m
	}

	// 返回默认倍率 1.0
	return EndpointMultiplier{
		CostMultiplier:                1.0,
		InputCostMultiplier:           1.0,
		OutputCostMultiplier:          1.0,
		CacheCreationCostMultiplier:   1.0,
		CacheCreationCostMultiplier1h: 1.0,
		CacheReadCostMultiplier:       1.0,
	}
}

// GetAccountMultiplier 获取账号倍率（用于成本计算）
func (ut *UsageTracker) GetAccountMultiplier(upstreamID int64) EndpointMultiplier {
	ut.mu.RLock()
	defer ut.mu.RUnlock()

	if ut.accountMu == nil {
		return DefaultEndpointMultiplier()
	}

	if m, exists := ut.accountMu[upstreamID]; exists {
		return m
	}

	return DefaultEndpointMultiplier()
}

// GetDatabaseStats 获取数据库统计信息（包装方法）
func (ut *UsageTracker) GetDatabaseStats(ctx context.Context) (*DatabaseStats, error) {
	if ut.config == nil || !ut.config.Enabled {
		return nil, fmt.Errorf("usage tracking not enabled")
	}
	if ut.db == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	return ut.getDatabaseStatsInternal(ctx)
}

// HealthCheck 检查数据库连接状态和基本功能（使用读连接）
func (ut *UsageTracker) HealthCheck(ctx context.Context) error {
	if ut.config == nil || !ut.config.Enabled {
		return nil // 如果未启用，认为是健康的
	}

	if ut.readDB == nil {
		return fmt.Errorf("read database not initialized")
	}

	// 测试读数据库连接
	if err := ut.readDB.PingContext(ctx); err != nil {
		return fmt.Errorf("read database ping failed: %w", err)
	}

	// 测试写数据库连接
	if ut.writeDB != nil {
		if err := ut.writeDB.PingContext(ctx); err != nil {
			return fmt.Errorf("write database ping failed: %w", err)
		}
	}

	// 测试基本查询（使用读连接）
	var count int
	err := ut.readDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='table'").Scan(&count)
	if err != nil {
		return fmt.Errorf("database query test failed: %w", err)
	}

	// 检查表是否存在
	if count < 2 { // 至少应该有 request_logs 和 usage_summary 两个表
		return fmt.Errorf("database schema incomplete: expected at least 2 tables, found %d", count)
	}

	// 检查事件处理通道是否正常
	select {
	case <-ut.ctx.Done():
		return fmt.Errorf("usage tracker context cancelled")
	default:
		// 上下文正常
	}

	// 检查事件通道容量
	if ut.eventChan != nil {
		channelLoad := float64(len(ut.eventChan)) / float64(cap(ut.eventChan)) * 100
		if channelLoad > 90 {
			return fmt.Errorf("event channel overloaded: %.1f%% capacity used", channelLoad)
		}
	}

	// 检查写队列容量
	if ut.writeQueue != nil {
		writeQueueLoad := float64(len(ut.writeQueue)) / float64(cap(ut.writeQueue)) * 100
		if writeQueueLoad > 90 {
			return fmt.Errorf("write queue overloaded: %.1f%% capacity used", writeQueueLoad)
		}
	}

	return nil
}

// ForceFlush 强制刷新所有待处理事件
func (ut *UsageTracker) ForceFlush() error {
	if ut.config == nil || !ut.config.Enabled {
		return nil
	}

	// 尝试发送一个特殊事件来触发批处理
	flushEvent := RequestEvent{
		Type:      "flush",
		RequestID: "force-flush-" + ut.now().Format("20060102150405"),
		Timestamp: ut.now(),
		Data:      nil,
	}

	select {
	case ut.eventChan <- flushEvent:
		slog.Info("Force flush event sent")
		return nil
	default:
		return fmt.Errorf("event channel full, cannot force flush")
	}
}

// GetPricing 获取模型定价
func (ut *UsageTracker) GetPricing(modelName string) ModelPricing {
	ut.mu.RLock()
	defer ut.mu.RUnlock()

	if pricing, exists := ut.pricing[modelName]; exists {
		return pricing
	}
	return ut.config.DefaultPricing
}

// GetConfiguredModels 获取配置中的所有模型列表
func (ut *UsageTracker) GetConfiguredModels() []string {
	ut.mu.RLock()
	defer ut.mu.RUnlock()

	models := make([]string, 0, len(ut.pricing))
	for modelName := range ut.pricing {
		models = append(models, modelName)
	}

	return models
}

// GetUsageSummary 获取使用摘要（便利方法）
func (ut *UsageTracker) GetUsageSummary(ctx context.Context, startTime, endTime time.Time) ([]UsageSummary, error) {
	opts := &QueryOptions{
		StartDate: &startTime,
		EndDate:   &endTime,
		Limit:     100,
	}
	return ut.QueryUsageSummary(ctx, opts)
}

// GetRequestLogs 获取请求日志（便利方法）
func (ut *UsageTracker) GetRequestLogs(ctx context.Context, startTime, endTime time.Time, modelName, requestFamily, upstreamName string, limit, offset int) ([]RequestDetail, error) {
	opts := &QueryOptions{
		StartDate:     &startTime,
		EndDate:       &endTime,
		ModelName:     modelName,
		RequestFamily: requestFamily,
		UpstreamName:  upstreamName,
		Limit:         limit,
		Offset:        offset,
	}
	return ut.QueryRequestDetails(ctx, opts)
}

// GetUsageStats 获取使用统计（便利方法，使用读连接）
func (ut *UsageTracker) GetUsageStats(ctx context.Context, startTime, endTime time.Time) (*UsageStatsDetailed, error) {
	if ut.readDB == nil {
		return nil, fmt.Errorf("read database not initialized")
	}

	query := `SELECT 
		COUNT(*) as total_requests,
		SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END) as success_requests,
		SUM(CASE WHEN status IN ('error', 'failed') THEN 1 ELSE 0 END) as error_requests,
		SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens) as total_tokens,
		SUM(total_cost_usd) as total_cost
		FROM request_logs 
		WHERE start_time >= ? AND start_time < ?`

	var stats UsageStatsDetailed
	startArg := timezonepolicy.FormatStorage(startTime)
	endArg := timezonepolicy.FormatStorage(endTime)
	err := ut.readDB.QueryRowContext(ctx, query, startArg, endArg).Scan(
		&stats.TotalRequests,
		&stats.SuccessRequests,
		&stats.ErrorRequests,
		&stats.TotalTokens,
		&stats.TotalCost,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query detailed usage stats: %w", err)
	}

	// 获取模型统计（使用读连接）
	modelQuery := `SELECT model_name, COUNT(*), SUM(total_cost_usd)
		FROM request_logs 
		WHERE start_time >= ? AND start_time < ? AND model_name IS NOT NULL AND model_name != ''
		GROUP BY model_name`

	rows, err := ut.readDB.QueryContext(ctx, modelQuery, startArg, endArg)
	if err != nil {
		return nil, fmt.Errorf("failed to query model stats: %w", err)
	}
	defer rows.Close()

	stats.ModelStats = make(map[string]ModelStat)
	for rows.Next() {
		var modelName string
		var requests int64
		var cost float64
		if err := rows.Scan(&modelName, &requests, &cost); err != nil {
			continue
		}
		stats.ModelStats[modelName] = ModelStat{
			RequestCount: requests,
			TotalCost:    cost,
		}
	}

	// 获取端点统计（使用读连接）
	endpointQuery := `SELECT endpoint_name, COUNT(*), SUM(total_cost_usd)
		FROM request_logs 
		WHERE start_time >= ? AND start_time < ? AND endpoint_name IS NOT NULL AND endpoint_name != ''
		GROUP BY endpoint_name`

	rows2, err := ut.readDB.QueryContext(ctx, endpointQuery, startArg, endArg)
	if err != nil {
		return nil, fmt.Errorf("failed to query endpoint stats: %w", err)
	}
	defer rows2.Close()

	stats.EndpointStats = make(map[string]EndpointStat)
	for rows2.Next() {
		var endpointName string
		var requests int64
		var cost float64
		if err := rows2.Scan(&endpointName, &requests, &cost); err != nil {
			continue
		}
		stats.EndpointStats[endpointName] = EndpointStat{
			RequestCount: requests,
			TotalCost:    cost,
		}
	}

	return &stats, nil
}

// ExportToCSV 导出为CSV格式
func (ut *UsageTracker) ExportToCSV(ctx context.Context, startTime, endTime time.Time, modelName, requestFamily, upstreamName string) ([]byte, error) {
	logs, err := ut.GetRequestLogs(ctx, startTime, endTime, modelName, requestFamily, upstreamName, 10000, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to get request logs for CSV export: %w", err)
	}

	var output strings.Builder
	writer := csv.NewWriter(&output)
	if err := writer.Write([]string{"request_id", "client_ip", "user_agent", "method", "path", "request_family", "start_time", "end_time", "duration_ms", "endpoint_name", "upstream_type", "upstream_source_name", "upstream_name", "upstream_id", "route_mode", "requested_endpoint", "effective_endpoint", "fallback_reason", "model_name", "status", "http_status_code", "retry_count", "input_tokens", "output_tokens", "cache_creation_tokens", "cache_read_tokens", "input_cost_usd", "output_cost_usd", "cache_creation_cost_usd", "cache_read_cost_usd", "total_cost_usd", "created_at", "updated_at", "request_source", "request_kind", "usage_known", "cost_known"}); err != nil {
		return nil, err
	}

	// CSV rows
	for _, log := range logs {
		endTime := ""
		if log.EndTime != nil {
			endTime = ut.timezonePolicy.FormatDisplay(*log.EndTime)
		}

		durationMs := ""
		if log.DurationMs != nil {
			durationMs = fmt.Sprintf("%d", *log.DurationMs)
		}

		httpStatus := ""
		if log.HTTPStatusCode != nil {
			httpStatus = fmt.Sprintf("%d", *log.HTTPStatusCode)
		}
		// CSV 没有 NULL 类型，用空单元格加显式 known 标记区别未知与真实零。
		tokenText := func(value int64) string {
			if !log.UsageKnown {
				return ""
			}
			return strconv.FormatInt(value, 10)
		}
		costText := func(value float64) string {
			if !log.CostKnown {
				return ""
			}
			return strconv.FormatFloat(value, 'f', 6, 64)
		}

		if err := writer.Write([]string{
			log.RequestID, log.ClientIP, log.UserAgent, log.Method, log.Path, log.RequestFamily,
			ut.timezonePolicy.FormatDisplay(log.StartTime), endTime, durationMs, log.EndpointName,
			log.UpstreamType, log.UpstreamSourceName, log.UpstreamName, strconv.FormatInt(log.UpstreamID, 10),
			log.RouteMode, log.RequestedEndpoint, log.EffectiveEndpoint, log.FallbackReason,
			log.ModelName, log.Status, httpStatus, strconv.Itoa(log.RetryCount),
			tokenText(log.InputTokens), tokenText(log.OutputTokens), tokenText(log.CacheCreationTokens), tokenText(log.CacheReadTokens),
			costText(log.InputCostUSD), costText(log.OutputCostUSD), costText(log.CacheCreationCostUSD), costText(log.CacheReadCostUSD), costText(log.TotalCostUSD),
			ut.timezonePolicy.FormatDisplay(log.CreatedAt), ut.timezonePolicy.FormatDisplay(log.UpdatedAt),
			log.RequestSource, log.RequestKind, strconv.FormatBool(log.UsageKnown), strconv.FormatBool(log.CostKnown),
		}); err != nil {
			return nil, err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, err
	}
	return []byte(output.String()), nil
}

// ExportToJSON 导出为JSON格式
func (ut *UsageTracker) ExportToJSON(ctx context.Context, startTime, endTime time.Time, modelName, requestFamily, upstreamName string) ([]byte, error) {
	logs, err := ut.GetRequestLogs(ctx, startTime, endTime, modelName, requestFamily, upstreamName, 10000, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to get request logs for JSON export: %w", err)
	}

	payload := struct {
		DisplayTimezone string          `json:"display_timezone"`
		Requests        []RequestDetail `json:"requests"`
	}{DisplayTimezone: ut.timezonePolicy.Name(), Requests: logs}
	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal logs to JSON: %w", err)
	}

	return jsonBytes, nil
}

// processWriteQueue 启动写操作队列处理器（简化版，确保稳定性）
func (ut *UsageTracker) processWriteQueue() {
	defer ut.writeWg.Done()
	slog.Debug("Write processor started")

	for {
		select {
		case writeReq, ok := <-ut.writeQueue:
			if !ok {
				slog.Debug("Write queue closed, write processor stopped")
				return
			}
			err := ut.executeWriteSimple(writeReq)
			select {
			case writeReq.Response <- err:
			case <-ut.ctx.Done():
				return
			}

		case <-ut.ctx.Done():
			slog.Debug("Write processor stopped")
			return
		}
	}
}

// executeWriteSimple 执行简单写操作（避免复杂的批处理）
func (ut *UsageTracker) executeWriteSimple(req WriteRequest) error {
	ut.writeMu.Lock()
	defer ut.writeMu.Unlock()

	ctx, cancel := context.WithTimeout(req.Context, 30*time.Second)
	defer cancel()

	// 直接执行，不使用事务（对于简单INSERT/UPDATE，不一定需要事务）
	if req.EventType == "vacuum" {
		// VACUUM不能在事务中执行
		_, err := ut.writeDB.ExecContext(ctx, req.Query, req.Args...)
		return err
	}

	// 其他操作使用短事务
	tx, err := ut.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	committed := false
	defer func() {
		if !committed {
			if rbErr := tx.Rollback(); rbErr != nil {
				slog.Debug("Failed to rollback transaction", "error", rbErr, "event_type", req.EventType)
			}
		}
	}()

	_, err = tx.ExecContext(ctx, req.Query, req.Args...)
	if err != nil {
		return fmt.Errorf("failed to execute query: %w", err)
	}

	err = tx.Commit()
	if err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	committed = true
	return nil
}

// executeWrite 执行单个写操作
func (ut *UsageTracker) executeWrite(req WriteRequest) error {
	ut.writeMu.Lock()
	defer ut.writeMu.Unlock()

	ctx, cancel := context.WithTimeout(req.Context, 60*time.Second)
	defer cancel()

	// 安全的事务处理
	return ut.executeWriteTransaction(ctx, req)
}

// executeWriteTransaction 执行写事务（修复defer tx.Rollback()问题）
func (ut *UsageTracker) executeWriteTransaction(ctx context.Context, req WriteRequest) error {
	tx, err := ut.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	committed := false
	defer func() {
		if !committed {
			if rbErr := tx.Rollback(); rbErr != nil {
				slog.Error("Failed to rollback transaction", "error", rbErr, "event_type", req.EventType)
			}
		}
	}()

	_, err = tx.ExecContext(ctx, req.Query, req.Args...)
	if err != nil {
		return fmt.Errorf("failed to execute query: %w", err)
	}

	err = tx.Commit()
	if err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	committed = true // 标记已提交，避免重复Rollback
	return nil
}

// initDatabaseWithWriteDB 使用写连接初始化数据库（同步等待完成）
func (ut *UsageTracker) initDatabaseWithWriteDB() error {
	// 读取并执行 schema SQL
	schemaSQL, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		return fmt.Errorf("failed to read schema.sql: %w", err)
	}

	// 使用写连接直接执行 schema（同步方式，确保表创建完成）
	if _, err := ut.writeDB.Exec(string(schemaSQL)); err != nil {
		return fmt.Errorf("failed to execute schema: %w", err)
	}

	slog.Debug("Database schema initialized successfully with write connection")
	return nil
}

// ============================================================================
// 🔥 v4.1 热池相关方法
// ============================================================================

// GetActiveRequests 获取热池中的活跃请求
// 返回当前正在处理中的请求列表（内存中）
func (ut *UsageTracker) GetActiveRequests() []*ActiveRequest {
	if ut.hotPool == nil {
		return nil
	}
	return ut.hotPool.GetActive()
}

// GetActiveRequestCount 获取活跃请求数量
func (ut *UsageTracker) GetActiveRequestCount() int {
	if ut.hotPool == nil {
		return 0
	}
	return ut.hotPool.GetActiveCount()
}

// GetHotPoolStats 获取热池统计信息
func (ut *UsageTracker) GetHotPoolStats() *HotPoolStats {
	if ut.hotPool == nil {
		return nil
	}
	stats := ut.hotPool.GetStats()
	return &stats
}

// GetArchiveStats 获取归档管理器统计信息
func (ut *UsageTracker) GetArchiveStats() *ArchiveStats {
	if ut.archiveManager == nil {
		return nil
	}
	stats := ut.archiveManager.GetStats()
	return &stats
}

// IsHotPoolEnabled 检查热池模式是否启用
func (ut *UsageTracker) IsHotPoolEnabled() bool {
	return ut.hotPoolEnabled
}

// GetActiveRequest 获取单个活跃请求
func (ut *UsageTracker) GetActiveRequest(requestID string) (*ActiveRequest, bool) {
	if ut.hotPool == nil {
		return nil, false
	}
	return ut.hotPool.Get(requestID)
}

// IsRequestActive 检查请求是否在热池中
func (ut *UsageTracker) IsRequestActive(requestID string) bool {
	if ut.hotPool == nil {
		return false
	}
	return ut.hotPool.Exists(requestID)
}

// UpdateActiveRequestTokens 更新活跃请求的Token（流式请求累积）
func (ut *UsageTracker) UpdateActiveRequestTokens(requestID string, inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens int64) error {
	if ut.hotPool == nil {
		return fmt.Errorf("hot pool not enabled")
	}

	return ut.hotPool.Update(requestID, func(req *ActiveRequest) {
		req.InputTokens = inputTokens
		req.OutputTokens = outputTokens
		req.CacheCreationTokens = cacheCreationTokens
		req.CacheReadTokens = cacheReadTokens
	})
}

// GetHotPoolAndArchiveOverview 获取热池和归档概览（用于监控）
func (ut *UsageTracker) GetHotPoolAndArchiveOverview() map[string]interface{} {
	overview := map[string]interface{}{
		"hot_pool_enabled": ut.hotPoolEnabled,
	}

	if ut.hotPool != nil {
		stats := ut.hotPool.GetStats()
		overview["hot_pool"] = map[string]interface{}{
			"current_size":   stats.CurrentSize,
			"peak_size":      stats.PeakSize,
			"total_added":    stats.TotalAdded,
			"total_removed":  stats.TotalRemoved,
			"total_archived": stats.TotalArchived,
			"total_expired":  stats.TotalExpired,
			"total_overflow": stats.TotalOverflow,
		}
	}

	if ut.archiveManager != nil {
		stats := ut.archiveManager.GetStats()
		overview["archive_manager"] = map[string]interface{}{
			"total_received":     stats.TotalReceived,
			"total_archived":     stats.TotalArchived,
			"total_failed":       stats.TotalFailed,
			"total_dropped":      stats.TotalDropped,
			"total_batches":      stats.TotalBatches,
			"avg_batch_size":     stats.AvgBatchSize,
			"channel_length":     stats.ChannelLength,
			"last_flush_time":    stats.LastFlushTime,
			"last_flush_latency": stats.LastFlushLatency.String(),
		}
	}

	return overview
}

// ============================================================================
// 🔥 v4.1 双源查询方法（热池 + 数据库）
// ============================================================================

// ActiveRequestToDetail 将 ActiveRequest 转换为 RequestDetail
func (ut *UsageTracker) ActiveRequestToDetail(req *ActiveRequest) RequestDetail {
	var endTime *time.Time
	var durationMs *int64
	var firstTokenMs *int64
	var completionMs *int64
	var httpStatus *int

	if req.EndTime != nil {
		endTime = req.EndTime
	}
	if req.DurationMs > 0 {
		durationMs = &req.DurationMs
	}
	if req.FirstTokenMs != nil {
		firstTokenMs = req.FirstTokenMs
	}
	if req.CompletionMs != nil {
		completionMs = req.CompletionMs
	}
	if req.HTTPStatusKnown || (req.RequestSource == "" && req.HTTPStatus > 0) {
		httpStatus = &req.HTTPStatus
	}
	usageKnown := req.UsageKnown || req.RequestSource == ""
	costKnown := req.CostKnown || req.RequestSource == ""
	requestSource := req.RequestSource
	if requestSource == "" {
		requestSource = RequestSourceLegacyProxy
	}
	requestKind := req.RequestKind
	if requestKind == "" {
		requestKind = RequestKindLegacy
	}
	routeMode := req.RouteMode
	if routeMode == "" {
		routeMode = "auto"
	}

	// 计算成本（使用公共函数消除重复代码）
	var cost CostBreakdown
	if costKnown && req.CostOverrideUSD != nil {
		cost.TotalCost = *req.CostOverrideUSD
	} else if costKnown && ut.pricing != nil {
		pricing, exists := ut.pricing[req.ModelName]
		if !exists {
			pricing, exists = ut.pricing["_default"]
		}
		if exists {
			// 获取端点/账号倍率
			var multiplier *EndpointMultiplier
			if req.UpstreamType == "account" {
				if ut.accountMu != nil {
					if m, ok := ut.accountMu[req.UpstreamID]; ok {
						multiplier = &m
					}
				}
			} else if ut.endpointMu != nil {
				if m, ok := ut.endpointMu[req.EndpointName]; ok {
					multiplier = &m
				}
			}

			usage := &TokenUsage{
				InputTokens:           req.InputTokens,
				OutputTokens:          req.OutputTokens,
				CacheCreationTokens:   req.CacheCreationTokens,
				CacheCreation5mTokens: req.CacheCreation5mTokens,
				CacheCreation1hTokens: req.CacheCreation1hTokens,
				CacheReadTokens:       req.CacheReadTokens,
			}
			cost = CalculateCostV2(usage, &pricing, multiplier, req.Path)
		}
	}

	return RequestDetail{
		ID:                    0, // 热池中的请求还没有数据库ID
		RequestID:             req.RequestID,
		ClientIP:              req.ClientIP,
		UserAgent:             req.UserAgent,
		Method:                req.Method,
		Path:                  req.Path,
		StartTime:             req.StartTime,
		EndTime:               endTime,
		DurationMs:            durationMs,
		FirstTokenMs:          firstTokenMs,
		CompletionMs:          completionMs,
		EndpointName:          req.EndpointName,
		RequestFamily:         req.RequestFamily,
		RequestSource:         requestSource,
		RequestKind:           requestKind,
		UpstreamType:          req.UpstreamType,
		UpstreamSourceName:    req.UpstreamSourceName,
		UpstreamName:          req.UpstreamName,
		UpstreamID:            req.UpstreamID,
		RouteMode:             routeMode,
		RequestedEndpoint:     req.RequestedEndpoint,
		EffectiveEndpoint:     req.EffectiveEndpoint,
		FallbackReason:        req.FallbackReason,
		RouteDecisionAt:       req.RouteDecisionAt,
		ModelName:             req.ModelName,
		IsStreaming:           req.IsStreaming,
		Status:                req.Status,
		HTTPStatusCode:        httpStatus,
		RetryCount:            req.RetryCount,
		FailureReason:         req.FailureReason,
		LastFailureReason:     "",
		CancelReason:          req.CancelReason,
		InputTokens:           req.InputTokens,
		OutputTokens:          req.OutputTokens,
		CacheCreationTokens:   req.CacheCreationTokens,
		CacheCreation5mTokens: req.CacheCreation5mTokens, // v5.0.1+
		CacheCreation1hTokens: req.CacheCreation1hTokens, // v5.0.1+
		CacheReadTokens:       req.CacheReadTokens,
		UsageKnown:            usageKnown,
		InputCostUSD:          cost.InputCost,
		OutputCostUSD:         cost.OutputCost,
		CacheCreationCostUSD:  cost.CacheCreationCost,
		CacheReadCostUSD:      cost.CacheReadCost,
		TotalCostUSD:          cost.TotalCost,
		CostKnown:             costKnown,
		CreatedAt:             req.StartTime,
		UpdatedAt:             ut.now(),
	}
}

// QueryRequestDetailsWithHotPool 双源查询：热池 + 数据库
// 返回合并后的请求列表，热池中的活跃请求排在前面
// 🔧 [修复] 2025-12-11: 正确处理分页，确保返回数量不超过 limit
func (ut *UsageTracker) QueryRequestDetailsWithHotPool(ctx context.Context, opts *QueryOptions) ([]RequestDetail, int64, error) {
	if opts == nil {
		opts = &QueryOptions{}
	}

	// 获取分页参数
	limit := opts.Limit
	offset := opts.Offset
	if limit <= 0 {
		limit = 20 // 默认每页20条
	}
	if offset < 0 {
		offset = 0
	}
	results := make([]RequestDetail, 0, limit)

	// 1. 从热池获取所有符合过滤条件的活跃请求（用于计算总数和分页）
	allHotPoolRequests := ut.getFilteredHotPoolRequests(opts)
	hotPoolCount := len(allHotPoolRequests)

	// 2. 获取数据库总数（用于分页计算）
	dbCount, err := ut.CountRequestDetails(ctx, opts)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to count database requests: %w", err)
	}

	// 3. 热池数据按开始时间倒序排列（最新的在前）
	if hotPoolCount > 0 {
		sort.Slice(allHotPoolRequests, func(i, j int) bool {
			return allHotPoolRequests[i].StartTime.After(allHotPoolRequests[j].StartTime)
		})
	}

	// 创建热池请求ID集合用于去重
	hotPoolIDs := make(map[string]struct{}, hotPoolCount)
	hotPoolRequestIDs := make([]string, 0, hotPoolCount)
	for _, req := range allHotPoolRequests {
		if _, exists := hotPoolIDs[req.RequestID]; exists {
			continue
		}
		hotPoolIDs[req.RequestID] = struct{}{}
		hotPoolRequestIDs = append(hotPoolRequestIDs, req.RequestID)
	}

	// 4. 计算去重后的总数（DB + 热池唯一请求）
	overlapCount, err := ut.countRequestDetailsOverlapsByRequestIDs(ctx, opts, hotPoolRequestIDs)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to count hot pool overlap requests: %w", err)
	}
	if overlapCount > dbCount {
		overlapCount = dbCount
	}
	totalCount := int64(hotPoolCount + dbCount - overlapCount)
	if totalCount < 0 {
		totalCount = 0
	}

	// 5. 先填充热池数据（热池优先）
	if offset < hotPoolCount {
		hotPoolEnd := offset + limit
		if hotPoolEnd > hotPoolCount {
			hotPoolEnd = hotPoolCount
		}
		results = append(results, allHotPoolRequests[offset:hotPoolEnd]...)
	}

	// 6. 从数据库补齐（排除热池重复，并正确跳过 offset 对应的唯一 DB 记录）
	remaining := limit - len(results)
	if remaining > 0 {
		dbSkip := offset - hotPoolCount
		if dbSkip < 0 {
			dbSkip = 0
		}
		dbRequests, err := ut.queryRequestDetailsFromDBExcludingHotPool(ctx, opts, dbSkip, remaining, hotPoolIDs)
		if err != nil {
			return nil, 0, err
		}
		results = append(results, dbRequests...)
	}

	return results, totalCount, nil
}

func (ut *UsageTracker) queryRequestDetailsFromDBExcludingHotPool(
	ctx context.Context,
	opts *QueryOptions,
	skip int,
	limit int,
	hotPoolIDs map[string]struct{},
) ([]RequestDetail, error) {
	if limit <= 0 {
		return nil, nil
	}

	if opts == nil {
		opts = &QueryOptions{}
	}

	results := make([]RequestDetail, 0, limit)
	skipped := 0
	fetchOffset := 0
	chunkSize := limit + len(hotPoolIDs)
	if chunkSize < 64 {
		chunkSize = 64
	}
	if chunkSize > 512 {
		chunkSize = 512
	}

	for {
		dbOpts := *opts
		dbOpts.Offset = fetchOffset
		dbOpts.Limit = chunkSize

		batch, err := ut.QueryRequestDetails(ctx, &dbOpts)
		if err != nil {
			return nil, fmt.Errorf("failed to query database: %w", err)
		}
		if len(batch) == 0 {
			break
		}

		for _, req := range batch {
			if _, exists := hotPoolIDs[req.RequestID]; exists {
				continue
			}
			if skipped < skip {
				skipped++
				continue
			}
			results = append(results, req)
			if len(results) >= limit {
				return results, nil
			}
		}

		if len(batch) < chunkSize {
			break
		}
		fetchOffset += len(batch)
	}

	return results, nil
}

func requestStatusMatchesFilter(statusFilter string, requestStatus string) bool {
	if statusFilter == "" {
		return true
	}
	switch statusFilter {
	case "failed":
		switch requestStatus {
		case "failed", "error", "auth_error", "rate_limited", "server_error", "network_error", "stream_error", "timeout":
			return true
		default:
			return false
		}
	default:
		return requestStatus == statusFilter
	}
}

// getFilteredHotPoolRequests 获取过滤后的热池请求
func (ut *UsageTracker) getFilteredHotPoolRequests(opts *QueryOptions) []RequestDetail {
	if ut.hotPool == nil {
		return nil
	}

	activeRequests := ut.hotPool.GetActive()
	if len(activeRequests) == 0 {
		return nil
	}
	if err := validateQueryRequestKinds(opts); err != nil {
		return nil
	}
	allowedKinds := make(map[string]struct{})
	if opts != nil {
		for _, kind := range opts.RequestKinds {
			normalized, _ := normalizeRequestKind(kind)
			allowedKinds[normalized] = struct{}{}
		}
	}

	var filtered []RequestDetail
	for _, req := range activeRequests {
		// 应用过滤条件
		if opts != nil {
			if len(allowedKinds) > 0 {
				kind := req.RequestKind
				if kind == "" {
					kind = RequestKindLegacy
				}
				if _, ok := allowedKinds[kind]; !ok {
					continue
				}
			}
			// 状态过滤
			if !requestStatusMatchesFilter(opts.Status, req.Status) {
				continue
			}
			// 模型过滤
			if opts.ModelName != "" && req.ModelName != opts.ModelName {
				continue
			}
			if opts.RequestFamily != "" && req.RequestFamily != normalizeRequestFamily(opts.RequestFamily) {
				continue
			}
			// 端点过滤
			if opts.EndpointName != "" && req.EndpointName != opts.EndpointName {
				continue
			}
			// 上游类型过滤
			switch resolveRequestQuerySource(opts.UpstreamType) {
			case requestQuerySourceAccount:
				if req.UpstreamType != "account" {
					continue
				}
			case requestQuerySourceEndpoint:
				if req.UpstreamType != "endpoint" {
					continue
				}
			case requestQuerySourceSubscription:
				if req.UpstreamType != "subscription" {
					continue
				}
			case requestQuerySourceRaw:
				if opts.UpstreamType != "" && req.UpstreamType != opts.UpstreamType {
					continue
				}
			}
			if opts.UpstreamName != "" && req.UpstreamName != opts.UpstreamName {
				continue
			}
			// 时间范围过滤
			if opts.StartDate != nil && req.StartTime.Before(*opts.StartDate) {
				continue
			}
			if opts.EndDate != nil && !req.StartTime.Before(*opts.EndDate) {
				continue
			}
		}

		filtered = append(filtered, ut.ActiveRequestToDetail(req))
	}

	return filtered
}

// GetHotPoolRequestCount 获取热池中符合条件的请求数量
func (ut *UsageTracker) GetHotPoolRequestCount(opts *QueryOptions) int {
	filtered := ut.getFilteredHotPoolRequests(opts)
	return len(filtered)
}
