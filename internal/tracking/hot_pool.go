package tracking

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// ActiveRequest 表示一个活跃的请求（完整状态）
// 请求在内存中保持活跃状态，只有完成时才归档到数据库
type ActiveRequest struct {
	SessionID string `json:"session_id,omitempty"`
	// 基础信息（创建时设置，不变）
	RequestID     string    `json:"request_id"`
	StartTime     time.Time `json:"start_time"`
	ClientIP      string    `json:"client_ip"`
	UserAgent     string    `json:"user_agent"`
	Method        string    `json:"method"`
	Path          string    `json:"path"`
	RequestFamily string    `json:"request_family"`
	RequestSource string    `json:"request_source"`
	RequestKind   string    `json:"request_kind"`
	IsStreaming   bool      `json:"is_streaming"`

	// 可变状态（频繁更新）
	Status             string `json:"status"`               // pending/forwarding/processing/completed/failed/cancelled
	EndpointName       string `json:"endpoint_name"`        // 当前使用的端点
	UpstreamType       string `json:"upstream_type"`        // endpoint/account
	UpstreamSourceName string `json:"upstream_source_name"` // 订阅源名（账号链路）或固定来源
	UpstreamName       string `json:"upstream_name"`        // 账号名或端点名
	UpstreamID         int64  `json:"upstream_id"`          // 上游ID（账号ID）
	RouteMode          string `json:"route_mode"`           // Claude 路由模式
	RequestedEndpoint  string `json:"requested_endpoint"`   // 用户手动指定的端点
	EffectiveEndpoint  string `json:"effective_endpoint"`   // 本次实际选中的端点
	FallbackReason     string `json:"fallback_reason"`      // fallback 或阻塞原因
	RetryCount         int    `json:"retry_count"`          // 重试次数
	HTTPStatus         int    `json:"http_status"`          // HTTP状态码
	HTTPStatusKnown    bool   `json:"http_status_known"`
	ModelName          string `json:"model_name"`     // 模型名称
	FailureReason      string `json:"failure_reason"` // 失败原因
	CancelReason       string `json:"cancel_reason"`  // 取消原因

	// Token 累积（流式请求实时更新）
	InputTokens           int64 `json:"input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	CacheCreationTokens   int64 `json:"cache_creation_tokens"`    // 总缓存创建（向后兼容）
	CacheCreation5mTokens int64 `json:"cache_creation_5m_tokens"` // 5分钟缓存 (v5.0.1+)
	CacheCreation1hTokens int64 `json:"cache_creation_1h_tokens"` // 1小时缓存 (v5.0.1+)
	CacheReadTokens       int64 `json:"cache_read_tokens"`
	UsageKnown            bool  `json:"usage_known"`
	CostKnown             bool  `json:"cost_known"`

	// 完成信息（只在结束时填充）
	EndTime         *time.Time `json:"end_time,omitempty"`
	DurationMs      int64      `json:"duration_ms"`
	FirstTokenMs    *int64     `json:"first_token_ms,omitempty"`
	CompletionMs    *int64     `json:"completion_ms,omitempty"`
	TotalCostUSD    float64    `json:"total_cost_usd"`
	CostOverrideUSD *float64   `json:"cost_override_usd,omitempty"`

	RouteDecisionAt *time.Time `json:"route_decision_at,omitempty"`

	// v6.2 生命周期面板新增字段（热池合并；归档 INSERT 时落库）
	UpstreamWriteMs      *int64 `json:"upstream_write_ms,omitempty"`
	ScheduleSnapshotJSON string `json:"schedule_snapshot_json,omitempty"`
	PrivacyScanJSON      string `json:"privacy_scan_json,omitempty"`

	// v6.3 模型一致性字段（请求两项随 start 写入；其余三项终态 finalizer 合并）
	RequestModel      string `json:"request_model,omitempty"`
	UpstreamModel     string `json:"upstream_model,omitempty"`
	ResponseModel     string `json:"response_model,omitempty"`
	RequestReasoning  string `json:"request_reasoning,omitempty"`
	ResponseReasoning string `json:"response_reasoning,omitempty"`

	// 内部字段
	mu               sync.RWMutex `json:"-"` // 保护单个请求的并发访问
	archivePersisted bool         `json:"-"` // 归档事务已提交；后续更新必须回退到数据库 UPDATE
}

// HotPoolConfig 热池配置
type HotPoolConfig struct {
	MaxAge           time.Duration // 最大存活时间，防止泄漏（默认30分钟）
	MaxSize          int           // 最大容量（默认10000）
	CleanupInterval  time.Duration // 清理间隔（默认1分钟）
	ArchiveOnCleanup bool          // 清理时是否归档（默认true）
}

// DefaultHotPoolConfig 返回默认配置
func DefaultHotPoolConfig() HotPoolConfig {
	return HotPoolConfig{
		MaxAge:           30 * time.Minute,
		MaxSize:          10000,
		CleanupInterval:  1 * time.Minute,
		ArchiveOnCleanup: true,
	}
}

// HotPool 管理所有活跃请求的内存状态
type HotPool struct {
	mu       sync.RWMutex
	requests map[string]*ActiveRequest

	// 归档中的请求（已完成但还没写入数据库）
	// 这个缓存确保请求在"热池→数据库"过渡期间不会丢失
	archiving map[string]*ActiveRequest

	config HotPoolConfig

	// 归档回调（当请求需要归档时调用）
	archiveCallback func(*ActiveRequest)

	// 统计信息；锁由 HotPool 持有，HotPoolStats 保持为可安全复制的纯数据快照。
	statsMu sync.RWMutex
	stats   HotPoolStats

	// 生命周期控制
	ctx    chan struct{}
	closed bool
}

// HotPoolStats 热池统计信息
type HotPoolStats struct {
	TotalAdded    int64 `json:"total_added"`    // 累计添加次数
	TotalRemoved  int64 `json:"total_removed"`  // 累计移除次数
	TotalArchived int64 `json:"total_archived"` // 累计归档次数
	TotalExpired  int64 `json:"total_expired"`  // 累计过期清理次数
	TotalOverflow int64 `json:"total_overflow"` // 累计溢出丢弃次数
	CurrentSize   int   `json:"current_size"`   // 当前大小
	PeakSize      int   `json:"peak_size"`      // 峰值大小
}

// NewHotPool 创建热池
func NewHotPool(config HotPoolConfig) *HotPool {
	if config.MaxAge <= 0 {
		config.MaxAge = 30 * time.Minute
	}
	if config.MaxSize <= 0 {
		config.MaxSize = 10000
	}
	if config.CleanupInterval <= 0 {
		config.CleanupInterval = 1 * time.Minute
	}

	pool := &HotPool{
		requests:  make(map[string]*ActiveRequest),
		archiving: make(map[string]*ActiveRequest),
		config:    config,
		ctx:       make(chan struct{}),
	}

	// 启动清理协程
	go pool.startCleaner()

	slog.Info("🔥 HotPool 初始化完成",
		"max_age", config.MaxAge,
		"max_size", config.MaxSize,
		"cleanup_interval", config.CleanupInterval)

	return pool
}

// SetArchiveCallback 设置归档回调函数
func (hp *HotPool) SetArchiveCallback(callback func(*ActiveRequest)) {
	hp.mu.Lock()
	defer hp.mu.Unlock()
	hp.archiveCallback = callback
}

// Add 添加新请求到热池
func (hp *HotPool) Add(req *ActiveRequest) error {
	hp.mu.Lock()
	defer hp.mu.Unlock()

	if hp.closed {
		return fmt.Errorf("hot pool is closed")
	}

	// 检查是否已存在
	if _, exists := hp.requests[req.RequestID]; exists {
		return fmt.Errorf("request %s already exists in hot pool", req.RequestID)
	}

	// 检查容量
	if len(hp.requests) >= hp.config.MaxSize {
		hp.statsMu.Lock()
		hp.stats.TotalOverflow++
		hp.statsMu.Unlock()
		slog.Warn("🔥 HotPool 容量已满，拒绝新请求",
			"request_id", req.RequestID,
			"current_size", len(hp.requests),
			"max_size", hp.config.MaxSize)
		return fmt.Errorf("hot pool is full (max: %d)", hp.config.MaxSize)
	}

	hp.requests[req.RequestID] = req

	// 更新统计
	hp.statsMu.Lock()
	hp.stats.TotalAdded++
	hp.stats.CurrentSize = len(hp.requests)
	if hp.stats.CurrentSize > hp.stats.PeakSize {
		hp.stats.PeakSize = hp.stats.CurrentSize
	}
	hp.statsMu.Unlock()

	return nil
}

// Update 更新请求状态（高频操作，纯内存）。
// 同时覆盖 requests 与 archiving 两个 map：CompleteAndArchive 后的终态快照写入
// 必须命中归档中的副本，由后续批量 INSERT 携带落库，避免退化为与 INSERT 竞速的异步 UPDATE。
func (hp *HotPool) Update(requestID string, updater func(*ActiveRequest)) error {
	hp.mu.RLock()
	req, exists := hp.requests[requestID]
	if !exists {
		req, exists = hp.archiving[requestID]
	}
	hp.mu.RUnlock()

	if !exists {
		return fmt.Errorf("request %s not found in hot pool", requestID)
	}

	// 使用请求级别的锁进行更新。归档事务提交后，内存副本不再是权威状态：
	// 返回错误让调用方降级为数据库 UPDATE，避免“更新成功但未进入已提交 INSERT”。
	req.mu.Lock()
	if req.archivePersisted {
		req.mu.Unlock()
		return fmt.Errorf("request %s archive already persisted", requestID)
	}
	updater(req)
	req.mu.Unlock()

	return nil
}

// Get 获取请求（只读）
func (hp *HotPool) Get(requestID string) (*ActiveRequest, bool) {
	hp.mu.RLock()
	defer hp.mu.RUnlock()

	req, exists := hp.requests[requestID]
	return req, exists
}

// cloneActiveRequest 逐字段构造副本（调用方必须已持 req.mu.RLock）。
// 不整体拷贝结构体，避免复制 sync.RWMutex（copylocks）与并发 Update 的数据竞争。
func cloneActiveRequest(req *ActiveRequest) *ActiveRequest {
	clone := &ActiveRequest{
		RequestID:             req.RequestID,
		SessionID:             req.SessionID,
		StartTime:             req.StartTime,
		ClientIP:              req.ClientIP,
		UserAgent:             req.UserAgent,
		Method:                req.Method,
		Path:                  req.Path,
		RequestFamily:         req.RequestFamily,
		RequestSource:         req.RequestSource,
		RequestKind:           req.RequestKind,
		IsStreaming:           req.IsStreaming,
		Status:                req.Status,
		EndpointName:          req.EndpointName,
		UpstreamType:          req.UpstreamType,
		UpstreamSourceName:    req.UpstreamSourceName,
		UpstreamName:          req.UpstreamName,
		UpstreamID:            req.UpstreamID,
		RouteMode:             req.RouteMode,
		RequestedEndpoint:     req.RequestedEndpoint,
		EffectiveEndpoint:     req.EffectiveEndpoint,
		FallbackReason:        req.FallbackReason,
		RetryCount:            req.RetryCount,
		HTTPStatus:            req.HTTPStatus,
		HTTPStatusKnown:       req.HTTPStatusKnown,
		ModelName:             req.ModelName,
		FailureReason:         req.FailureReason,
		CancelReason:          req.CancelReason,
		InputTokens:           req.InputTokens,
		OutputTokens:          req.OutputTokens,
		CacheCreationTokens:   req.CacheCreationTokens,
		CacheCreation5mTokens: req.CacheCreation5mTokens,
		CacheCreation1hTokens: req.CacheCreation1hTokens,
		CacheReadTokens:       req.CacheReadTokens,
		UsageKnown:            req.UsageKnown,
		CostKnown:             req.CostKnown,
		DurationMs:            req.DurationMs,
		TotalCostUSD:          req.TotalCostUSD,
		ScheduleSnapshotJSON:  req.ScheduleSnapshotJSON,
		PrivacyScanJSON:       req.PrivacyScanJSON,
		RequestModel:          req.RequestModel,
		UpstreamModel:         req.UpstreamModel,
		ResponseModel:         req.ResponseModel,
		RequestReasoning:      req.RequestReasoning,
		ResponseReasoning:     req.ResponseReasoning,
	}
	if req.EndTime != nil {
		value := *req.EndTime
		clone.EndTime = &value
	}
	if req.FirstTokenMs != nil {
		value := *req.FirstTokenMs
		clone.FirstTokenMs = &value
	}
	if req.CompletionMs != nil {
		value := *req.CompletionMs
		clone.CompletionMs = &value
	}
	if req.CostOverrideUSD != nil {
		value := *req.CostOverrideUSD
		clone.CostOverrideUSD = &value
	}
	if req.RouteDecisionAt != nil {
		value := *req.RouteDecisionAt
		clone.RouteDecisionAt = &value
	}
	if req.UpstreamWriteMs != nil {
		value := *req.UpstreamWriteMs
		clone.UpstreamWriteMs = &value
	}
	return clone
}

// SnapshotRequest 同时查询 requests 与尚未提交的 archiving 请求，
// 并在请求锁下逐字段构造新对象返回（不复制锁，规避 copylocks 与并发 Update 的数据竞争）。
// 归档事务已提交的副本视为 miss，由上层读取数据库权威记录。
func (hp *HotPool) SnapshotRequest(requestID string) (*ActiveRequest, bool) {
	hp.mu.RLock()
	req, exists := hp.requests[requestID]
	if !exists {
		req, exists = hp.archiving[requestID]
	}
	hp.mu.RUnlock()
	if !exists {
		return nil, false
	}

	req.mu.RLock()
	defer req.mu.RUnlock()
	// 事务已经提交但 ConfirmArchived 尚未移除 map 的极短窗口内，数据库才是权威来源。
	// 无论查 map 时请求还在 active 还是已经进入 archiving，都以锁下的提交状态为准。
	if req.archivePersisted {
		return nil, false
	}
	return cloneActiveRequest(req), true
}

// Remove 从热池中移除请求（归档时调用）
func (hp *HotPool) Remove(requestID string) *ActiveRequest {
	hp.mu.Lock()
	defer hp.mu.Unlock()

	req, exists := hp.requests[requestID]
	if !exists {
		return nil
	}

	delete(hp.requests, requestID)

	// 更新统计
	hp.statsMu.Lock()
	hp.stats.TotalRemoved++
	hp.stats.CurrentSize = len(hp.requests)
	hp.statsMu.Unlock()

	return req
}

// Archive 归档请求（移除并发送到归档通道）
func (hp *HotPool) Archive(requestID string) error {
	req := hp.Remove(requestID)
	if req == nil {
		return fmt.Errorf("request %s not found in hot pool", requestID)
	}

	// 更新统计
	hp.statsMu.Lock()
	hp.stats.TotalArchived++
	hp.statsMu.Unlock()

	// 调用归档回调
	hp.mu.RLock()
	callback := hp.archiveCallback
	hp.mu.RUnlock()

	if callback != nil {
		callback(req)
	}

	return nil
}

// CompleteAndArchive 完成请求并归档（一步完成）
// 请求会先移到 archiving 缓存，确保在写入数据库前不会丢失
func (hp *HotPool) CompleteAndArchive(requestID string, finalizer func(*ActiveRequest)) error {
	hp.mu.Lock()
	req, exists := hp.requests[requestID]
	if !exists {
		hp.mu.Unlock()
		return fmt.Errorf("request %s not found in hot pool", requestID)
	}
	// 从活跃列表移到归档中列表（而不是直接删除）
	delete(hp.requests, requestID)
	hp.archiving[requestID] = req
	callback := hp.archiveCallback
	currentSize := len(hp.requests)
	hp.mu.Unlock()

	// 应用最终更新
	req.mu.Lock()
	if finalizer != nil {
		finalizer(req)
	}
	// 确保结束时间已设置
	if req.EndTime == nil {
		now := time.Now()
		req.EndTime = &now
		req.DurationMs = now.Sub(req.StartTime).Milliseconds()
	}
	req.mu.Unlock()

	// 更新统计
	hp.statsMu.Lock()
	hp.stats.TotalRemoved++
	hp.stats.TotalArchived++
	hp.stats.CurrentSize = currentSize
	hp.statsMu.Unlock()

	// 调用归档回调
	if callback != nil {
		callback(req)
	}

	return nil
}

// GetActive 获取所有活跃请求的不可变快照（用于 Web 界面）。
// 包括正在处理的请求 + 正在归档中的请求；不得把解锁后的内部可变指针暴露给读取方。
func (hp *HotPool) GetActive() []*ActiveRequest {
	hp.mu.RLock()
	defer hp.mu.RUnlock()

	// 容量 = 活跃请求 + 归档中请求
	result := make([]*ActiveRequest, 0, len(hp.requests)+len(hp.archiving))

	// 添加活跃请求
	for _, req := range hp.requests {
		req.mu.RLock()
		result = append(result, cloneActiveRequest(req))
		req.mu.RUnlock()
	}

	// 添加归档中的请求（确保过渡期不丢失）
	for _, req := range hp.archiving {
		req.mu.RLock()
		result = append(result, cloneActiveRequest(req))
		req.mu.RUnlock()
	}

	return result
}

// GetActiveCount 获取活跃请求数量（包括归档中的）
func (hp *HotPool) GetActiveCount() int {
	hp.mu.RLock()
	defer hp.mu.RUnlock()
	return len(hp.requests) + len(hp.archiving)
}

// ConfirmArchived 确认请求已成功写入数据库，从归档缓存中移除
// 由 ArchiveManager 在批量写入成功后调用
func (hp *HotPool) ConfirmArchived(requestIDs []string) {
	hp.mu.Lock()
	defer hp.mu.Unlock()

	for _, id := range requestIDs {
		delete(hp.archiving, id)
	}
}

// GetArchivingCount 获取正在归档中的请求数量（调试用）
func (hp *HotPool) GetArchivingCount() int {
	hp.mu.RLock()
	defer hp.mu.RUnlock()
	return len(hp.archiving)
}

// GetStats 获取统计信息
func (hp *HotPool) GetStats() HotPoolStats {
	hp.statsMu.RLock()
	defer hp.statsMu.RUnlock()

	return HotPoolStats{
		TotalAdded:    hp.stats.TotalAdded,
		TotalRemoved:  hp.stats.TotalRemoved,
		TotalArchived: hp.stats.TotalArchived,
		TotalExpired:  hp.stats.TotalExpired,
		TotalOverflow: hp.stats.TotalOverflow,
		CurrentSize:   hp.stats.CurrentSize,
		PeakSize:      hp.stats.PeakSize,
	}
}

// Exists 检查请求是否存在于热池中
func (hp *HotPool) Exists(requestID string) bool {
	hp.mu.RLock()
	defer hp.mu.RUnlock()
	_, exists := hp.requests[requestID]
	return exists
}

// startCleaner 定期清理僵尸请求
func (hp *HotPool) startCleaner() {
	ticker := time.NewTicker(hp.config.CleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			hp.cleanup()
		case <-hp.ctx:
			return
		}
	}
}

// cleanup 清理过期请求
func (hp *HotPool) cleanup() {
	hp.mu.Lock()

	now := time.Now()
	expiredRequests := make([]*ActiveRequest, 0)

	for id, req := range hp.requests {
		if now.Sub(req.StartTime) > hp.config.MaxAge {
			// 标记为超时
			req.mu.Lock()
			req.Status = "timeout"
			req.FailureReason = "hot pool cleanup: request exceeded max age"
			req.EndTime = &now
			req.DurationMs = now.Sub(req.StartTime).Milliseconds()
			req.mu.Unlock()

			expiredRequests = append(expiredRequests, req)
			delete(hp.requests, id)
		}
	}

	if len(expiredRequests) > 0 {
		// 更新统计
		hp.statsMu.Lock()
		hp.stats.TotalExpired += int64(len(expiredRequests))
		hp.stats.TotalRemoved += int64(len(expiredRequests))
		hp.stats.CurrentSize = len(hp.requests)
		hp.statsMu.Unlock()

		slog.Warn("🔥 HotPool 清理过期请求",
			"expired_count", len(expiredRequests),
			"remaining", len(hp.requests))

	}
	archiveOnCleanup := hp.config.ArchiveOnCleanup
	callback := hp.archiveCallback
	hp.mu.Unlock()

	// 归档过期请求（如果启用），在锁外执行回调，避免全局锁被外部逻辑占用
	if archiveOnCleanup && callback != nil {
		for _, req := range expiredRequests {
			callback(req)
			hp.statsMu.Lock()
			hp.stats.TotalArchived++
			hp.statsMu.Unlock()
		}
	}
}

// Close 关闭热池
func (hp *HotPool) Close() error {
	hp.mu.Lock()

	if hp.closed {
		hp.mu.Unlock()
		return nil
	}

	hp.closed = true
	close(hp.ctx)

	// 归档所有剩余请求
	now := time.Now()
	remainingRequests := make([]*ActiveRequest, 0, len(hp.requests))
	callback := hp.archiveCallback
	for _, req := range hp.requests {
		req.mu.Lock()
		if req.EndTime == nil {
			req.EndTime = &now
			req.DurationMs = now.Sub(req.StartTime).Milliseconds()
		}
		if req.Status == "pending" || req.Status == "forwarding" || req.Status == "processing" {
			req.Status = "cancelled"
			req.CancelReason = "hot pool shutdown"
		}
		req.mu.Unlock()
		remainingRequests = append(remainingRequests, req)
	}

	remaining := len(hp.requests)
	hp.requests = make(map[string]*ActiveRequest)
	hp.mu.Unlock()

	for _, req := range remainingRequests {
		if callback != nil {
			callback(req)
		}
	}
	stats := hp.GetStats()

	slog.Info("🔥 HotPool 已关闭",
		"archived_remaining", remaining,
		"total_added", stats.TotalAdded,
		"total_archived", stats.TotalArchived)

	return nil
}

// NewActiveRequest 创建默认 other 类型的活跃请求，供不关心入口类型的内部测试使用。
func NewActiveRequest(requestID, clientIP, userAgent, method, path string, isStreaming bool) *ActiveRequest {
	return NewActiveRequestWithFamily(requestID, clientIP, userAgent, method, path, RequestFamilyOther, isStreaming)
}

// NewActiveRequestWithFamily 创建带显式请求类型的活跃请求。
func NewActiveRequestWithFamily(requestID, clientIP, userAgent, method, path, requestFamily string, isStreaming bool) *ActiveRequest {
	return NewActiveRequestWithDimensions(requestID, clientIP, userAgent, method, path, requestFamily, isStreaming, RequestDimensions{
		RequestSource: RequestSourceLegacyProxy,
		RequestKind:   RequestKindLegacy,
		UpstreamType:  "endpoint",
		UsageKnown:    true,
	})
}

// NewActiveRequestWithDimensions 创建带显式分类和 unknown usage 状态的活跃请求。
func NewActiveRequestWithDimensions(requestID, clientIP, userAgent, method, path, requestFamily string, isStreaming bool, dimensions RequestDimensions) *ActiveRequest {
	requestFamily = normalizeRequestFamily(requestFamily)
	return &ActiveRequest{
		RequestID:          requestID,
		StartTime:          time.Now(),
		ClientIP:           clientIP,
		UserAgent:          userAgent,
		Method:             method,
		Path:               path,
		RequestFamily:      requestFamily,
		RequestSource:      dimensions.RequestSource,
		RequestKind:        dimensions.RequestKind,
		IsStreaming:        isStreaming,
		Status:             "pending",
		UpstreamType:       dimensions.UpstreamType,
		UpstreamSourceName: dimensions.UpstreamSourceName,
		UpstreamName:       dimensions.UpstreamName,
		UpstreamID:         dimensions.UpstreamID,
		UsageKnown:         dimensions.UsageKnown,
		CostKnown:          dimensions.UsageKnown,
		RequestModel:       strings.TrimSpace(dimensions.RequestModel),
		RequestReasoning:   strings.TrimSpace(dimensions.RequestReasoning),
		SessionID:          dimensions.SessionID,
	}
}
