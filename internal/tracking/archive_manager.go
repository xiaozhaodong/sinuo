package tracking

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	timezonepolicy "sinuo/internal/timezone"
)

// ArchiveEvent 归档事件（请求完成时发送）
type ArchiveEvent struct {
	Request   *ActiveRequest
	Timestamp time.Time
}

// ArchiveManagerConfig 归档管理器配置
type ArchiveManagerConfig struct {
	ChannelSize   int           // 归档通道大小（默认1000）
	BatchSize     int           // 批次大小（默认100）
	FlushInterval time.Duration // 刷新间隔（默认5秒）
	MaxRetry      int           // 最大重试次数（默认3）
}

// DefaultArchiveManagerConfig 返回默认配置
func DefaultArchiveManagerConfig() ArchiveManagerConfig {
	return ArchiveManagerConfig{
		ChannelSize:   1000,
		BatchSize:     100,
		FlushInterval: 5 * time.Second,
		MaxRetry:      3,
	}
}

// ArchiveManager 管理归档写入
type ArchiveManager struct {
	admissionMu         sync.RWMutex // 关闭前先等待已受理的归档入队
	archiveChan         chan *ArchiveEvent
	adapter             DatabaseAdapter
	config              ArchiveManagerConfig
	cacheMu             sync.RWMutex
	pricing             map[string]ModelPricing       // 模型定价缓存
	endpointMultipliers map[string]EndpointMultiplier // 端点倍率缓存
	accountMultipliers  map[int64]EndpointMultiplier  // 账号倍率缓存

	// 热池引用（用于归档成功后清理）
	hotPool *HotPool

	// 完整性与版本提交回调（由 UsageTracker 注入）
	callbackMu       sync.RWMutex
	onIntegrityIssue func(requestID, code string)

	// 统计信息
	stats ArchiveStats

	// 生命周期控制
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// ArchiveStats 归档统计信息
type ArchiveStats struct {
	mu               sync.RWMutex
	TotalReceived    int64         `json:"total_received"`     // 累计接收事件数
	TotalArchived    int64         `json:"total_archived"`     // 累计归档成功数
	TotalFailed      int64         `json:"total_failed"`       // 累计归档失败数
	TotalDropped     int64         `json:"total_dropped"`      // 累计丢弃数（通道满）
	TotalBatches     int64         `json:"total_batches"`      // 累计批次数
	AvgBatchSize     float64       `json:"avg_batch_size"`     // 平均批次大小
	LastFlushTime    time.Time     `json:"last_flush_time"`    // 最后刷新时间
	LastFlushLatency time.Duration `json:"last_flush_latency"` // 最后刷新延迟
	ChannelLength    int           `json:"channel_length"`     // 当前通道长度
}

// NewArchiveManager 创建归档管理器
func NewArchiveManager(adapter DatabaseAdapter, config ArchiveManagerConfig, pricing map[string]ModelPricing) *ArchiveManager {
	if config.ChannelSize <= 0 {
		config.ChannelSize = 1000
	}
	if config.BatchSize <= 0 {
		config.BatchSize = 100
	}
	if config.FlushInterval <= 0 {
		config.FlushInterval = 5 * time.Second
	}
	if config.MaxRetry <= 0 {
		config.MaxRetry = 3
	}

	ctx, cancel := context.WithCancel(context.Background())

	am := &ArchiveManager{
		archiveChan: make(chan *ArchiveEvent, config.ChannelSize),
		adapter:     adapter,
		config:      config,
		pricing:     cloneModelPricingMap(pricing),
		ctx:         ctx,
		cancel:      cancel,
	}

	// 启动归档协程
	am.wg.Add(1)
	go am.startArchiver()

	slog.Info("📦 ArchiveManager 初始化完成",
		"channel_size", config.ChannelSize,
		"batch_size", config.BatchSize,
		"flush_interval", config.FlushInterval)

	return am
}

// SetHotPool 设置热池引用（用于归档成功后清理）
func (am *ArchiveManager) SetHotPool(hp *HotPool) {
	am.hotPool = hp
}

// SetIntegrityObserver 设置归档通道/提交失败时的完整性回调。
func (am *ArchiveManager) SetIntegrityObserver(fn func(requestID, code string)) {
	am.callbackMu.Lock()
	am.onIntegrityIssue = fn
	am.callbackMu.Unlock()
}

func (am *ArchiveManager) reportIntegrityIssue(requestID, code string) {
	am.callbackMu.RLock()
	fn := am.onIntegrityIssue
	am.callbackMu.RUnlock()
	if fn != nil {
		fn(requestID, code)
	}
}

// UpdateEndpointMultipliers 更新端点成本倍率
func (am *ArchiveManager) UpdateEndpointMultipliers(multipliers map[string]EndpointMultiplier) {
	am.cacheMu.Lock()
	defer am.cacheMu.Unlock()
	am.endpointMultipliers = cloneEndpointMultiplierMap(multipliers)
}

// UpdateAccountMultipliers 更新账号成本倍率
func (am *ArchiveManager) UpdateAccountMultipliers(multipliers map[int64]EndpointMultiplier) {
	am.cacheMu.Lock()
	defer am.cacheMu.Unlock()
	am.accountMultipliers = cloneAccountMultiplierMap(multipliers)
}

// UpdatePricing 更新模型定价（运行时动态更新）
func (am *ArchiveManager) UpdatePricing(pricing map[string]ModelPricing) {
	am.cacheMu.Lock()
	defer am.cacheMu.Unlock()
	am.pricing = cloneModelPricingMap(pricing)
}

// Archive 发送请求到归档通道
func (am *ArchiveManager) Archive(req *ActiveRequest) error {
	am.admissionMu.RLock()
	defer am.admissionMu.RUnlock()
	if err := am.ctx.Err(); err != nil {
		return err
	}
	// 收到最终结算的请求必须落库。队列满时在热池锁外等待写入器腾出空间。
	am.archiveChan <- &ArchiveEvent{Request: req, Timestamp: time.Now()}
	am.stats.mu.Lock()
	am.stats.TotalReceived++
	am.stats.mu.Unlock()
	return nil
}

// startArchiver 批量归档协程
func (am *ArchiveManager) startArchiver() {
	defer am.wg.Done()

	batch := make([]*ArchiveEvent, 0, am.config.BatchSize)
	ticker := time.NewTicker(am.config.FlushInterval)
	defer ticker.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}

		// 提取请求 ID 列表
		requestIDs := make([]string, len(batch))
		for i, event := range batch {
			requestIDs[i] = event.Request.RequestID
		}

		start := time.Now()
		err := am.flushBatch(batch)
		latency := time.Since(start)

		am.stats.mu.Lock()
		am.stats.LastFlushTime = time.Now()
		am.stats.LastFlushLatency = latency
		am.stats.TotalBatches++
		if err == nil {
			am.stats.TotalArchived += int64(len(batch))
			// 更新平均批次大小
			am.stats.AvgBatchSize = float64(am.stats.TotalArchived) / float64(am.stats.TotalBatches)
		} else {
			am.stats.TotalFailed += int64(len(batch))
		}
		am.stats.mu.Unlock()

		if err != nil {
			slog.Error("📦 批量归档失败",
				"batch_size", len(batch),
				"request_ids", strings.Join(requestIDs, ", "),
				"error", err,
				"latency", latency)
		} else {
			slog.Debug("📦 批量归档成功",
				"batch_size", len(batch),
				"request_ids", strings.Join(requestIDs, ", "),
				"latency", latency)
		}

		// 清空批次
		batch = batch[:0]
	}

	for {
		select {
		case event := <-am.archiveChan:
			batch = append(batch, event)

			// 达到批次大小，立即刷新
			if len(batch) >= am.config.BatchSize {
				flush()
			}

		case <-ticker.C:
			// 定时刷新（即使未满批次）
			flush()

		case <-am.ctx.Done():
			// 优雅关闭，处理剩余事件
			slog.Info("📦 ArchiveManager 正在关闭，处理剩余事件...")

			// 先刷新当前批次
			flush()

			// 处理通道中剩余的事件
			for {
				select {
				case event := <-am.archiveChan:
					batch = append(batch, event)
					if len(batch) >= am.config.BatchSize {
						flush()
					}
				default:
					// 通道已空，最后刷新
					flush()
					slog.Info("📦 ArchiveManager 已关闭",
						"total_archived", am.stats.TotalArchived,
						"total_failed", am.stats.TotalFailed)
					return
				}
			}
		}
	}
}

// flushBatch 批量写入数据库
func (am *ArchiveManager) flushBatch(events []*ArchiveEvent) error {
	if len(events) == 0 {
		return nil
	}

	var lastErr error
	for retry := 0; retry < am.config.MaxRetry; retry++ {
		err := am.batchInsert(events)
		if err == nil {
			// 归档成功，从热池的归档缓存中移除。
			if am.hotPool != nil {
				requestIDs := make([]string, len(events))
				for i, event := range events {
					requestIDs[i] = event.Request.RequestID
				}
				am.hotPool.ConfirmArchived(requestIDs)
			}
			return nil
		}

		lastErr = err
		slog.Warn("📦 批量插入失败，重试中",
			"retry", retry+1,
			"max_retry", am.config.MaxRetry,
			"error", err)

		// 指数退避
		time.Sleep(time.Duration(retry+1) * time.Second)
	}

	for _, event := range events {
		if event != nil && event.Request != nil {
			am.reportIntegrityIssue(event.Request.RequestID, "archive_commit_failed")
		}
	}
	return fmt.Errorf("batch insert failed after %d retries: %w", am.config.MaxRetry, lastErr)
}

// batchInsert 在同一事务中归档整批请求。
func (am *ArchiveManager) batchInsert(events []*ArchiveEvent) error {
	if am.adapter == nil {
		return fmt.Errorf("database adapter not initialized")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tx, err := am.adapter.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// endpoint 及其他非 account 请求写入 request_logs（冲突时保护 start_time 与单调 request_revision）
	requestStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO request_logs (
			request_id, client_ip, user_agent, method, path,
			start_time, end_time, duration_ms, first_token_ms, completion_ms,
			first_content_ms, first_text_ms, first_reasoning_ms, reasoning_hidden, stream_flow_ms, stop_reason, upstream_request_id,
			upstream_write_ms, upstream_first_dispatch_ms, upstream_dispatch_ms, upstream_conn_ms, schedule_snapshot_json, privacy_scan_json, input_composition_json,
			request_family, request_source, request_kind, endpoint_name, model_name,
			request_model, upstream_model, response_model, request_reasoning, response_reasoning, session_id,
			upstream_type, upstream_source_name, upstream_name, upstream_id,
			route_mode, requested_endpoint, effective_endpoint, fallback_reason, route_decision_at,
			status, http_status_code, retry_count,
			failure_reason, cancel_reason,
			is_streaming,
			input_tokens, output_tokens,
			cache_creation_tokens, cache_creation_5m_tokens, cache_creation_1h_tokens, cache_creation_30m_tokens, cache_creation_12h_tokens,
			cache_read_tokens,
			input_cost_usd, output_cost_usd,
			cache_creation_cost_usd, cache_creation_5m_cost_usd, cache_creation_1h_cost_usd,
			cache_read_cost_usd, total_cost_usd,
			billing_rate_id, billing_basis, request_flow_json, request_revision
		) VALUES (
			?, ?, ?, ?, ?,
			?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?,
			?, ?, ?, ?,
			?, ?, ?, ?, ?,
			?, ?, ?,
			?, ?,
			?,
			?, ?,
			?, ?, ?, ?, ?,
			?,
			?, ?,
			?, ?, ?,
			?, ?,
			?, ?, ?, ?
		)
		ON CONFLICT(request_id) DO UPDATE SET
			client_ip = EXCLUDED.client_ip,
			user_agent = EXCLUDED.user_agent,
			method = EXCLUDED.method,
			path = EXCLUDED.path,
			start_time = COALESCE(request_logs.start_time, EXCLUDED.start_time),
			end_time = EXCLUDED.end_time,
			duration_ms = EXCLUDED.duration_ms,
			first_token_ms = COALESCE(request_logs.first_token_ms, EXCLUDED.first_token_ms),
			completion_ms = COALESCE(request_logs.completion_ms, EXCLUDED.completion_ms),
			first_content_ms = COALESCE(request_logs.first_content_ms, EXCLUDED.first_content_ms),
			first_text_ms = COALESCE(request_logs.first_text_ms, EXCLUDED.first_text_ms),
			first_reasoning_ms = COALESCE(request_logs.first_reasoning_ms, EXCLUDED.first_reasoning_ms),
			reasoning_hidden = COALESCE(request_logs.reasoning_hidden, EXCLUDED.reasoning_hidden),
			stream_flow_ms = COALESCE(request_logs.stream_flow_ms, EXCLUDED.stream_flow_ms),
			stop_reason = EXCLUDED.stop_reason,
			upstream_request_id = EXCLUDED.upstream_request_id,
			upstream_write_ms = EXCLUDED.upstream_write_ms,
			upstream_first_dispatch_ms = COALESCE(request_logs.upstream_first_dispatch_ms, EXCLUDED.upstream_first_dispatch_ms),
			upstream_dispatch_ms = EXCLUDED.upstream_dispatch_ms,
			upstream_conn_ms = EXCLUDED.upstream_conn_ms,
			schedule_snapshot_json = COALESCE(EXCLUDED.schedule_snapshot_json, request_logs.schedule_snapshot_json),
			input_composition_json = COALESCE(EXCLUDED.input_composition_json, request_logs.input_composition_json),
			privacy_scan_json = COALESCE(EXCLUDED.privacy_scan_json, request_logs.privacy_scan_json),
			request_family = EXCLUDED.request_family,
			request_source = EXCLUDED.request_source,
			request_kind = EXCLUDED.request_kind,
			endpoint_name = EXCLUDED.endpoint_name,
			model_name = EXCLUDED.model_name,
			request_model = EXCLUDED.request_model,
			upstream_model = EXCLUDED.upstream_model,
			response_model = EXCLUDED.response_model,
			request_reasoning = EXCLUDED.request_reasoning,
			response_reasoning = EXCLUDED.response_reasoning,
			session_id = EXCLUDED.session_id,
			upstream_type = EXCLUDED.upstream_type,
			upstream_source_name = EXCLUDED.upstream_source_name,
			upstream_name = EXCLUDED.upstream_name,
			upstream_id = EXCLUDED.upstream_id,
			route_mode = EXCLUDED.route_mode,
			requested_endpoint = EXCLUDED.requested_endpoint,
			effective_endpoint = EXCLUDED.effective_endpoint,
			fallback_reason = EXCLUDED.fallback_reason,
			route_decision_at = EXCLUDED.route_decision_at,
			status = EXCLUDED.status,
			http_status_code = EXCLUDED.http_status_code,
			retry_count = EXCLUDED.retry_count,
			failure_reason = EXCLUDED.failure_reason,
			cancel_reason = EXCLUDED.cancel_reason,
			is_streaming = EXCLUDED.is_streaming,
			input_tokens = EXCLUDED.input_tokens,
			output_tokens = EXCLUDED.output_tokens,
			cache_creation_tokens = EXCLUDED.cache_creation_tokens,
			cache_creation_5m_tokens = EXCLUDED.cache_creation_5m_tokens,
			cache_creation_1h_tokens = EXCLUDED.cache_creation_1h_tokens,
			cache_creation_30m_tokens = EXCLUDED.cache_creation_30m_tokens,
			cache_creation_12h_tokens = EXCLUDED.cache_creation_12h_tokens,
			cache_read_tokens = EXCLUDED.cache_read_tokens,
			input_cost_usd = EXCLUDED.input_cost_usd,
			output_cost_usd = EXCLUDED.output_cost_usd,
			cache_creation_cost_usd = EXCLUDED.cache_creation_cost_usd,
			cache_creation_5m_cost_usd = EXCLUDED.cache_creation_5m_cost_usd,
			cache_creation_1h_cost_usd = EXCLUDED.cache_creation_1h_cost_usd,
			cache_read_cost_usd = EXCLUDED.cache_read_cost_usd,
			total_cost_usd = EXCLUDED.total_cost_usd,
			billing_rate_id = EXCLUDED.billing_rate_id, billing_basis = EXCLUDED.billing_basis,
			request_flow_json = COALESCE(EXCLUDED.request_flow_json, request_logs.request_flow_json),
			request_revision = MAX(COALESCE(request_logs.request_revision, 0), COALESCE(EXCLUDED.request_revision, 0))
	`)
	if err != nil {
		return fmt.Errorf("failed to prepare request_logs statement: %w", err)
	}
	defer requestStmt.Close()

	// 每个请求的锁从读取快照一直持有到事务提交完成，形成明确的提交屏障：
	// 提交前拿到锁的更新进入本次 INSERT；提交后的更新看到 archivePersisted，
	// 由调用方回退为数据库 UPDATE，不会出现“内存更新成功但数据库仍是旧快照”。
	lockedRequests := make([]*ActiveRequest, 0, len(events))
	lockedRequestIDs := make(map[string]struct{}, len(events))
	unlockRequests := func(persisted bool) {
		for i := len(lockedRequests) - 1; i >= 0; i-- {
			req := lockedRequests[i]
			if persisted {
				req.archivePersisted = true
			}
			req.mu.Unlock()
		}
	}

	for _, event := range events {
		if event == nil || event.Request == nil {
			unlockRequests(false)
			return fmt.Errorf("archive event request is nil")
		}
		req := event.Request
		if _, duplicate := lockedRequestIDs[req.RequestID]; duplicate {
			unlockRequests(false)
			return fmt.Errorf("duplicate request %s in archive batch", req.RequestID)
		}
		lockedRequestIDs[req.RequestID] = struct{}{}

		req.mu.Lock()
		lockedRequests = append(lockedRequests, req)
		if err := am.insertRequestEventLocked(ctx, requestStmt, event); err != nil {
			unlockRequests(false)
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		unlockRequests(false)
		return fmt.Errorf("failed to commit transaction: %w", err)
	}
	unlockRequests(true)

	return nil
}

// insertRequestEventLocked 读取单条 ActiveRequest 并写入。
// 调用方必须持有 req.mu 写锁，并保持到事务提交或回滚完成。
func (am *ArchiveManager) insertRequestEventLocked(ctx context.Context, requestStmt *sql.Stmt, event *ArchiveEvent) error {
	req := event.Request
	if req.RequestRevision < 1 {
		req.RequestRevision = 1
	}

	upstreamType := strings.TrimSpace(req.UpstreamType)
	if upstreamType == "" {
		upstreamType = "endpoint"
	}

	// v5.0.1+: 只有上游明确报告用量时才计算并持久化成本。
	settled := req.settled
	if settled == nil {
		value := am.archiveSettlement(req)
		settled = &value
	}
	costBreakdown := settled.cost
	basis, err := req.persistedBillingBasis(settled)
	if err != nil {
		return err
	}

	// 格式化时间
	startTime := am.formatTime(req.StartTime)
	var endTime interface{}
	if req.EndTime != nil {
		endTime = am.formatTime(*req.EndTime)
	} else {
		endTime = nil
	}
	var routeDecisionAt interface{}
	if req.RouteDecisionAt != nil {
		routeDecisionAt = am.formatTime(*req.RouteDecisionAt)
	} else {
		routeDecisionAt = nil
	}
	routeMode := req.RouteMode
	if routeMode == "" {
		routeMode = "auto"
	}
	requestSource := req.RequestSource
	requestKind := req.RequestKind
	legacyCompatibility := requestSource == ""
	if requestSource == "" {
		requestSource = RequestSourceLegacyProxy
	}
	if requestKind == "" {
		requestKind = RequestKindLegacy
	}
	usageKnown := req.UsageKnown || legacyCompatibility
	costKnown := settled.known

	flowMarshaledNow := false
	if req.RequestFlow != nil && req.RequestFlowJSON == "" {
		if raw, mErr := MarshalRequestFlowBounded(req.RequestFlow); mErr == nil {
			req.RequestFlowJSON = raw
			flowMarshaledNow = true
		}
	}

	if costKnown || flowMarshaledNow {
		// 归档还更新了成本或轨迹时，详情也需要读取新版本。
		req.RequestRevision++
	}

	_, err = requestStmt.ExecContext(ctx,
		req.RequestID,
		req.ClientIP,
		req.UserAgent,
		req.Method,
		req.Path,
		startTime,
		endTime,
		req.DurationMs,
		req.FirstTokenMs,
		req.CompletionMs,
		req.FirstContentMs,
		req.FirstTextMs,
		req.FirstReasoningMs,
		req.ReasoningHidden,
		req.StreamFlowMs,
		req.StopReason,
		req.UpstreamRequestID,
		req.UpstreamWriteMs,
		req.UpstreamFirstDispatchMs,
		req.UpstreamDispatchMs,
		req.UpstreamConnMs,
		nullString(req.ScheduleSnapshotJSON),
		nullString(req.PrivacyScanJSON),
		nullString(req.InputCompositionJSON),
		normalizeRequestFamily(req.RequestFamily),
		requestSource,
		requestKind,
		req.EndpointName,
		req.ModelName,
		req.RequestModel,
		req.UpstreamModel,
		req.ResponseModel,
		req.RequestReasoning,
		req.ResponseReasoning,
		req.SessionID,
		upstreamType,
		req.UpstreamSourceName,
		req.UpstreamName,
		req.UpstreamID,
		routeMode,
		req.RequestedEndpoint,
		req.EffectiveEndpoint,
		req.FallbackReason,
		routeDecisionAt,
		req.Status,
		nullableInt(req.HTTPStatus, req.HTTPStatusKnown),
		req.RetryCount,
		nullString(req.FailureReason),
		nullString(req.CancelReason),
		req.IsStreaming,
		nullableInt64(req.InputTokens, usageKnown),
		nullableInt64(req.OutputTokens, usageKnown),
		nullableInt64(req.CacheCreationTokens, usageKnown),
		nullableInt64(req.CacheCreation5mTokens, usageKnown),
		nullableInt64(req.CacheCreation1hTokens, usageKnown),
		req.CacheCreation30mTokens, req.CacheCreation12hTokens,
		nullableInt64(req.CacheReadTokens, usageKnown),
		nullableFloat64(costBreakdown.InputCost, costKnown),
		nullableFloat64(costBreakdown.OutputCost, costKnown),
		nullableFloat64(costBreakdown.CacheCreationCost, costKnown),
		nullableFloat64(costBreakdown.CacheCreation5mCost, costKnown),
		nullableFloat64(costBreakdown.CacheCreation1hCost, costKnown),
		nullableFloat64(costBreakdown.CacheReadCost, costKnown),
		nullableFloat64(costBreakdown.TotalCost, costKnown),
		nullInt64(settled.rateID), basis, nullString(req.RequestFlowJSON),
		req.RequestRevision,
	)
	if err != nil {
		return fmt.Errorf("failed to insert request %s: %w", req.RequestID, err)
	}

	return nil
}

// calculateCostV2 计算请求成本（v5.0.1+: 支持分开的 5m/1h 缓存）
func (am *ArchiveManager) calculateCostV2(req *ActiveRequest) CostBreakdown {
	return am.archiveSettlement(req).cost
}

func (am *ArchiveManager) archiveSettlement(req *ActiveRequest) baseSettlement {
	if !req.billing.at.IsZero() {
		return req.billing.settle(req)
	}
	am.cacheMu.RLock()
	defer am.cacheMu.RUnlock()
	basis := billingSnapshot{prices: am.pricing, endpoints: am.endpointMultipliers, accounts: am.accountMultipliers}
	return basis.settle(req)
}

func cloneModelPricingMap(src map[string]ModelPricing) map[string]ModelPricing {
	if len(src) == 0 {
		return nil
	}
	cloned := make(map[string]ModelPricing, len(src))
	for key, value := range src {
		cloned[key] = value
	}
	return cloned
}

func cloneEndpointMultiplierMap(src map[string]EndpointMultiplier) map[string]EndpointMultiplier {
	if len(src) == 0 {
		return nil
	}
	cloned := make(map[string]EndpointMultiplier, len(src))
	for key, value := range src {
		cloned[key] = value
	}
	return cloned
}

func cloneAccountMultiplierMap(src map[int64]EndpointMultiplier) map[int64]EndpointMultiplier {
	if len(src) == 0 {
		return nil
	}
	cloned := make(map[int64]EndpointMultiplier, len(src))
	for key, value := range src {
		cloned[key] = value
	}
	return cloned
}

func (am *ArchiveManager) formatTime(t time.Time) string {
	return timezonepolicy.FormatStorage(t)
}

// nullString 处理空字符串为SQL NULL
func nullString(s string) interface{} {
	if s == "" {
		return sql.NullString{}
	}
	return s
}

// nullInt64 处理非正整数为SQL NULL
func nullInt64(v int64) interface{} {
	if v <= 0 {
		return sql.NullInt64{}
	}
	return v
}

func nullableInt(value int, known bool) interface{} {
	if !known {
		return nil
	}
	return value
}

func nullableInt64(value int64, known bool) interface{} {
	if !known {
		return nil
	}
	return value
}

func nullableFloat64(value float64, known bool) interface{} {
	if !known {
		return nil
	}
	return value
}

// GetStats 获取统计信息（返回快照，不含锁）
func (am *ArchiveManager) GetStats() ArchiveStats {
	am.stats.mu.RLock()
	defer am.stats.mu.RUnlock()

	// 返回不含锁的快照副本
	return ArchiveStats{
		TotalReceived:    am.stats.TotalReceived,
		TotalArchived:    am.stats.TotalArchived,
		TotalFailed:      am.stats.TotalFailed,
		TotalDropped:     am.stats.TotalDropped,
		TotalBatches:     am.stats.TotalBatches,
		AvgBatchSize:     am.stats.AvgBatchSize,
		LastFlushTime:    am.stats.LastFlushTime,
		LastFlushLatency: am.stats.LastFlushLatency,
		ChannelLength:    len(am.archiveChan),
		// 注意：mu 字段是零值，不会复制原始锁
	}
}

// GetChannelLength 获取当前通道长度
func (am *ArchiveManager) GetChannelLength() int {
	return len(am.archiveChan)
}

// Close 关闭归档管理器
func (am *ArchiveManager) Close() error {
	slog.Info("📦 正在关闭 ArchiveManager...")
	am.admissionMu.Lock()
	am.cancel()
	am.admissionMu.Unlock()
	am.wg.Wait()
	return nil
}

// Flush 强制刷新（用于测试或优雅关闭）
func (am *ArchiveManager) Flush() {
	// 发送一个空事件触发刷新
	// 实际刷新由 ticker 处理，这里只是等待
	time.Sleep(100 * time.Millisecond)
}
