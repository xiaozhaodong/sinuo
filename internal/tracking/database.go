package tracking

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	timezonepolicy "sinuo/internal/timezone"
)

// initDatabase 初始化数据库
func (ut *UsageTracker) initDatabase() error {
	// 使用适配器初始化数据库schema
	if err := ut.adapter.InitSchema(); err != nil {
		return fmt.Errorf("failed to initialize database schema: %w", err)
	}

	slog.Debug("Database schema initialized successfully")
	return nil
}

// processEvents 异步事件处理循环
func (ut *UsageTracker) processEvents() {
	defer ut.wg.Done()

	ticker := time.NewTicker(ut.config.FlushInterval)
	defer ticker.Stop()

	batch := make([]RequestEvent, 0, ut.config.BatchSize)

	slog.Debug("Usage tracking event processor started")

	for {
		select {
		case event := <-ut.eventChan:
			batch = append(batch, event)
			if len(batch) >= ut.config.BatchSize {
				ut.flushBatch(batch)
				batch = batch[:0] // 重置切片但保留容量
			}

		case <-ticker.C:
			if len(batch) > 0 {
				ut.flushBatch(batch)
				batch = batch[:0]
			}

		case <-ut.ctx.Done():
			// 优雅关闭，处理剩余事件
			slog.Debug("Processing remaining events before shutdown", "count", len(batch))
			if len(batch) > 0 {
				ut.flushBatch(batch)
			}

			// 处理通道中剩余的事件
			for {
				select {
				case event := <-ut.eventChan:
					batch = append(batch, event)
					if len(batch) >= ut.config.BatchSize {
						ut.flushBatch(batch)
						batch = batch[:0]
					}
				default:
					if len(batch) > 0 {
						ut.flushBatch(batch)
					}
					slog.Debug("Usage tracking event processor stopped")
					return
				}
			}
		}
	}
}

// flushBatch 批量写入事件到数据库
func (ut *UsageTracker) flushBatch(events []RequestEvent) {
	if len(events) == 0 {
		return
	}

	var retryCount int
	for retryCount < ut.config.MaxRetry {
		if err := ut.processBatch(events); err != nil {
			// 使用错误处理器处理错误
			if ut.errorHandler != nil && ut.errorHandler.HandleDatabaseError(err, "flushBatch") {
				// 错误已处理，重试操作
				retryCount++
				slog.Info("Database error handled successfully, retrying",
					"retry", retryCount,
					"batch_size", len(events))
				time.Sleep(time.Duration(retryCount) * time.Second)
				continue
			}

			retryCount++
			slog.Warn("Failed to flush batch, retrying",
				"error", err,
				"retry", retryCount,
				"max_retry", ut.config.MaxRetry,
				"batch_size", len(events))

			if retryCount < ut.config.MaxRetry {
				time.Sleep(time.Duration(retryCount) * time.Second)
			}
			continue
		}

		// 成功处理
		if retryCount > 0 {
			slog.Info("Batch processed successfully after retry",
				"retry_count", retryCount,
				"batch_size", len(events))
		}
		return
	}

	// 所有重试都失败
	slog.Error("Failed to process batch after all retries",
		"batch_size", len(events),
		"max_retry", ut.config.MaxRetry)
}

// processBatch 处理一批事件（重构为使用写队列）
func (ut *UsageTracker) processBatch(events []RequestEvent) error {
	successCount := 0

	for _, event := range events {
		// 特殊处理flush事件
		if event.Type == "flush" {
			// flush事件不需要处理，只是用来触发批处理
			successCount++
			continue
		}

		// 构建写操作请求
		query, args, err := ut.buildWriteQuery(event)
		if err != nil {
			slog.Error("Failed to build write query",
				"error", err,
				"event_type", event.Type,
				"request_id", event.RequestID)
			continue
		}

		writeReq := WriteRequest{
			Query:     query,
			Args:      args,
			Response:  make(chan error, 1),
			Context:   context.Background(),
			EventType: event.Type,
		}

		// 通过队列发送写操作
		select {
		case ut.writeQueue <- writeReq:
			var err error
			select {
			case err = <-writeReq.Response:
			case <-ut.ctx.Done():
				return ut.ctx.Err()
			}
			if err != nil {
				slog.Error("Write operation failed",
					"error", err,
					"event_type", event.Type,
					"request_id", event.RequestID)
				continue
			}
			successCount++

		case <-ut.ctx.Done():
			return ut.ctx.Err()
		}
	}

	if successCount < len(events) {
		slog.Warn("Some events failed to process",
			"success", successCount,
			"total", len(events))
	}

	return nil
}

// buildWriteQuery 构建写操作查询和参数
func (ut *UsageTracker) buildWriteQuery(event RequestEvent) (string, []interface{}, error) {
	switch event.Type {
	case "start":
		return ut.buildStartQuery(event)
	case "update":
		return ut.buildUpdateQuery(event)
	case "flexible_update": // 新增：统一的可选字段更新
		return ut.buildFlexibleUpdateQuery(event)
	case "success": // 新增：成功完成，替代"complete"
		return ut.buildSuccessQuery(event)
	case "final_failure": // 新增：失败/取消完成
		return ut.buildFinalFailureQuery(event)
	case "subscription_settlement":
		return ut.buildSubscriptionSettlementQuery(event)
	case "complete":
		// 对于complete事件，直接使用传入的持续时间，不需要查询数据库
		data, ok := event.Data.(RequestCompleteData)
		if !ok {
			return "", nil, fmt.Errorf("invalid complete event data type")
		}

		// 计算成本
		tokens := &TokenUsage{
			InputTokens:           data.InputTokens,
			OutputTokens:          data.OutputTokens,
			CacheCreationTokens:   data.CacheCreationTokens,
			CacheCreation5mTokens: data.CacheCreation5mTokens,
			CacheCreation1hTokens: data.CacheCreation1hTokens,
			CacheReadTokens:       data.CacheReadTokens,
		}

		pricing := ut.GetPricing(data.ModelName)
		costBreakdown := CalculateCostV2(tokens, &pricing, nil, data.Path)

		query := fmt.Sprintf(`UPDATE request_logs SET
			end_time = ?,
			duration_ms = ?,
			model_name = ?,
			input_tokens = ?,
			output_tokens = ?,
			cache_creation_tokens = ?,
			cache_creation_5m_tokens = ?,
			cache_creation_1h_tokens = ?,
			cache_read_tokens = ?,
			input_cost_usd = ?,
			output_cost_usd = ?,
			cache_creation_cost_usd = ?,
			cache_creation_5m_cost_usd = ?,
			cache_creation_1h_cost_usd = ?,
			cache_read_cost_usd = ?,
			total_cost_usd = ?,
			status = CASE WHEN status != 'completed' THEN 'completed' ELSE status END,
			updated_at = %s
		WHERE request_id = ?`, ut.adapter.BuildUTCDateTimeNow())

		args := []interface{}{
			timezonepolicy.FormatStorage(event.Timestamp),
			data.Duration.Milliseconds(), // 直接使用生命周期管理器计算的持续时间
			data.ModelName,
			data.InputTokens,
			data.OutputTokens,
			data.CacheCreationTokens,
			data.CacheCreation5mTokens,
			data.CacheCreation1hTokens,
			data.CacheReadTokens,
			costBreakdown.InputCost,
			costBreakdown.OutputCost,
			costBreakdown.CacheCreationCost,
			costBreakdown.CacheCreation5mCost,
			costBreakdown.CacheCreation1hCost,
			costBreakdown.CacheReadCost,
			costBreakdown.TotalCost,
			event.RequestID,
		}

		return query, args, nil
	case "failed_request_tokens":
		// 处理失败请求Token事件：只记录Token统计，不更新请求状态
		data, ok := event.Data.(RequestCompleteData)
		if !ok {
			return "", nil, fmt.Errorf("invalid failed_request_tokens event data type")
		}

		// 计算成本（使用完整的 5m/1h 字段）
		tokens := &TokenUsage{
			InputTokens:           data.InputTokens,
			OutputTokens:          data.OutputTokens,
			CacheCreationTokens:   data.CacheCreationTokens,
			CacheCreation5mTokens: data.CacheCreation5mTokens,
			CacheCreation1hTokens: data.CacheCreation1hTokens,
			CacheReadTokens:       data.CacheReadTokens,
		}

		pricing := ut.GetPricing(data.ModelName)
		costBreakdown := CalculateCostV2(tokens, &pricing, nil, data.Path)

		// 只更新Token相关字段和成本，不更新状态
		query := fmt.Sprintf(`UPDATE request_logs SET
			model_name = COALESCE(?, model_name),
			input_tokens = ?,
			output_tokens = ?,
			cache_creation_tokens = ?,
			cache_creation_5m_tokens = ?,
			cache_creation_1h_tokens = ?,
			cache_read_tokens = ?,
			input_cost_usd = ?,
			output_cost_usd = ?,
			cache_creation_cost_usd = ?,
			cache_creation_5m_cost_usd = ?,
			cache_creation_1h_cost_usd = ?,
			cache_read_cost_usd = ?,
			total_cost_usd = ?,
			duration_ms = COALESCE(?, duration_ms),
			updated_at = %s
		WHERE request_id = ?`, ut.adapter.BuildUTCDateTimeNow())

		args := []interface{}{
			data.ModelName,
			data.InputTokens,
			data.OutputTokens,
			data.CacheCreationTokens,
			data.CacheCreation5mTokens,
			data.CacheCreation1hTokens,
			data.CacheReadTokens,
			costBreakdown.InputCost,
			costBreakdown.OutputCost,
			costBreakdown.CacheCreationCost,
			costBreakdown.CacheCreation5mCost,
			costBreakdown.CacheCreation1hCost,
			costBreakdown.CacheReadCost,
			costBreakdown.TotalCost,
			data.Duration.Milliseconds(),
			event.RequestID,
		}

		return query, args, nil
	case "token_recovery":
		// 🔧 [Fallback修复] 处理Token恢复事件：只更新Token字段和成本，不更新状态
		data, ok := event.Data.(RequestCompleteData)
		if !ok {
			return "", nil, fmt.Errorf("invalid token_recovery event data type")
		}

		// 计算成本（使用完整的 5m/1h 字段）
		tokens := &TokenUsage{
			InputTokens:           data.InputTokens,
			OutputTokens:          data.OutputTokens,
			CacheCreationTokens:   data.CacheCreationTokens,
			CacheCreation5mTokens: data.CacheCreation5mTokens,
			CacheCreation1hTokens: data.CacheCreation1hTokens,
			CacheReadTokens:       data.CacheReadTokens,
		}

		pricing := ut.GetPricing(data.ModelName)
		costBreakdown := CalculateCostV2(tokens, &pricing, nil, data.Path)

		// 🔧 专用于恢复场景：更新任何状态的请求的Token字段，因为这是恢复不完整的数据
		query := fmt.Sprintf(`UPDATE request_logs SET
			model_name = COALESCE(?, model_name),
			input_tokens = ?,
			output_tokens = ?,
			cache_creation_tokens = ?,
			cache_creation_5m_tokens = ?,
			cache_creation_1h_tokens = ?,
			cache_read_tokens = ?,
			input_cost_usd = ?,
			output_cost_usd = ?,
			cache_creation_cost_usd = ?,
			cache_creation_5m_cost_usd = ?,
			cache_creation_1h_cost_usd = ?,
			cache_read_cost_usd = ?,
			total_cost_usd = ?,
			updated_at = %s
		WHERE request_id = ?`, ut.adapter.BuildUTCDateTimeNow())

		args := []interface{}{
			data.ModelName,
			data.InputTokens,
			data.OutputTokens,
			data.CacheCreationTokens,
			data.CacheCreation5mTokens,
			data.CacheCreation1hTokens,
			data.CacheReadTokens,
			costBreakdown.InputCost,
			costBreakdown.OutputCost,
			costBreakdown.CacheCreationCost,
			costBreakdown.CacheCreation5mCost,
			costBreakdown.CacheCreation1hCost,
			costBreakdown.CacheReadCost,
			costBreakdown.TotalCost,
			event.RequestID,
		}

		return query, args, nil
	default:
		return "", nil, fmt.Errorf("unknown event type: %s", event.Type)
	}
}

func (ut *UsageTracker) buildSubscriptionSettlementQuery(event RequestEvent) (string, []interface{}, error) {
	data, ok := event.Data.(RequestSettlement)
	if !ok {
		return "", nil, fmt.Errorf("invalid subscription settlement event data type")
	}
	if data.Status != "completed" && data.Status != "failed" && data.Status != "cancelled" {
		return "", nil, fmt.Errorf("invalid subscription settlement status %q", data.Status)
	}

	var input, output, creation, creation5m, creation1h, read interface{}
	var inputCost, outputCost, creationCost, creation5mCost, creation1hCost, readCost, totalCost interface{}
	if data.TokenUsage != nil {
		usage := data.TokenUsage
		pricing := ut.GetPricing(data.ModelName)
		cost := CalculateCostV2(usage, &pricing, nil, data.Path)
		input, output = usage.InputTokens, usage.OutputTokens
		creation, creation5m, creation1h = usage.CacheCreationTokens, usage.CacheCreation5mTokens, usage.CacheCreation1hTokens
		read = usage.CacheReadTokens
		inputCost, outputCost = cost.InputCost, cost.OutputCost
		creationCost, creation5mCost, creation1hCost = cost.CacheCreationCost, cost.CacheCreation5mCost, cost.CacheCreation1hCost
		readCost, totalCost = cost.CacheReadCost, cost.TotalCost
	}

	query := fmt.Sprintf(`UPDATE request_logs SET
		end_time = ?, duration_ms = ?, status = ?,
		model_name = COALESCE(NULLIF(NULLIF(?, ''), 'default'), model_name),
		failure_reason = ?, last_failure_reason = NULL, http_status_code = ?,
		input_tokens = ?, output_tokens = ?, cache_creation_tokens = ?,
		cache_creation_5m_tokens = ?, cache_creation_1h_tokens = ?, cache_read_tokens = ?,
		input_cost_usd = ?, output_cost_usd = ?, cache_creation_cost_usd = ?,
		cache_creation_5m_cost_usd = ?, cache_creation_1h_cost_usd = ?, cache_read_cost_usd = ?, total_cost_usd = ?,
		%s,
		updated_at = %s
	WHERE request_id = ?`, responseDiagnosticsSetClause, ut.adapter.BuildUTCDateTimeNow())
	args := []interface{}{
		timezonepolicy.FormatStorage(event.Timestamp), data.Duration.Milliseconds(), data.Status,
		data.ModelName, nullStringValue(data.FailureReason), nullIntValue(data.HTTPStatus),
		input, output, creation, creation5m, creation1h, read,
		inputCost, outputCost, creationCost, creation5mCost, creation1hCost, readCost, totalCost,
	}
	args = append(args, responseDiagnosticsArgs(data.Diagnostics)...)
	args = append(args, event.RequestID)
	return query, args, nil
}

func nullStringValue(value string) interface{} {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func nullIntValue(value *int) interface{} {
	if value == nil {
		return nil
	}
	return *value
}

// buildStartQuery 构建开始事件查询
func (ut *UsageTracker) buildStartQuery(event RequestEvent) (string, []interface{}, error) {
	data, ok := event.Data.(RequestStartData)
	if !ok {
		return "", nil, fmt.Errorf("invalid start event data type")
	}

	// 使用适配器构建INSERT OR REPLACE查询
	upstreamType := data.UpstreamType
	if upstreamType == "" {
		upstreamType = "endpoint"
	}
	requestSource, err := normalizeRequestSource(data.RequestSource)
	if err != nil {
		return "", nil, err
	}
	requestKind, err := normalizeRequestKind(data.RequestKind)
	if err != nil {
		return "", nil, err
	}
	columns := []string{"request_id", "client_ip", "user_agent", "method", "path", "request_family", "request_source", "request_kind", "start_time", "status", "is_streaming", "upstream_type", "upstream_source_name", "upstream_name", "upstream_id", "request_model", "request_reasoning", "session_id", "input_tokens", "output_tokens", "cache_creation_tokens", "cache_creation_5m_tokens", "cache_creation_1h_tokens", "cache_read_tokens", "input_cost_usd", "output_cost_usd", "cache_creation_cost_usd", "cache_creation_5m_cost_usd", "cache_creation_1h_cost_usd", "cache_read_cost_usd", "total_cost_usd", "updated_at"}
	placeholders := []string{"?", "?", "?", "?", "?", "?", "?", "?", "?", "'pending'", "?", "?", "?", "?", "?", "?", "?", "?", "?", "?", "?", "?", "?", "?", "?", "?", "?", "?", "?", "?", "?", ut.adapter.BuildUTCDateTimeNow()}

	query := ut.adapter.BuildInsertOrReplaceQuery("request_logs", columns, placeholders)

	args := []interface{}{
		event.RequestID,
		data.ClientIP,
		data.UserAgent,
		data.Method,
		data.Path,
		normalizeRequestFamily(data.RequestFamily),
		requestSource,
		requestKind,
		timezonepolicy.FormatStorage(event.Timestamp),
		data.IsStreaming,
		upstreamType,
		data.UpstreamSourceName,
		data.UpstreamName,
		data.UpstreamID,
		strings.TrimSpace(data.RequestModel),
		strings.TrimSpace(data.RequestReasoning),
		data.SessionID,
	}
	usageKnown := data.UsageKnown || data.RequestSource == "" || requestSource == RequestSourceLegacyProxy
	for i := 0; i < 13; i++ {
		if usageKnown {
			args = append(args, 0)
		} else {
			args = append(args, nil)
		}
	}

	return query, args, nil
}

// buildUpdateQuery 构建更新事件查询
func (ut *UsageTracker) buildUpdateQuery(event RequestEvent) (string, []interface{}, error) {
	data, ok := event.Data.(RequestUpdateData)
	if !ok {
		return "", nil, fmt.Errorf("invalid update event data type")
	}

	// 如果端点名为空，只更新状态相关字段
	if data.EndpointName == "" {
		query := fmt.Sprintf(`UPDATE request_logs SET
			status = ?,
			retry_count = ?,
			http_status_code = ?,
			updated_at = %s
		WHERE request_id = ?`, ut.adapter.BuildUTCDateTimeNow())

		args := []interface{}{
			data.Status,
			data.RetryCount,
			data.HTTPStatus,
			event.RequestID,
		}

		return query, args, nil
	}

	// 使用适配器构建INSERT OR REPLACE查询
	// 重新加入start_time，但在UPSERT时保护已有值不被覆盖
	columns := []string{"request_id", "endpoint_name", "status", "retry_count", "http_status_code", "start_time", "updated_at"}
	placeholders := []string{"?", "?", "?", "?", "?", "?", ut.adapter.BuildUTCDateTimeNow()}
	query := ut.adapter.BuildInsertOrReplaceQuery("request_logs", columns, placeholders)

	args := []interface{}{
		event.RequestID,
		data.EndpointName,
		data.Status,
		data.RetryCount,
		data.HTTPStatus,
		timezonepolicy.FormatStorage(event.Timestamp), // 提供start_time值用于插入新记录
	}

	return query, args, nil
}

// buildFlexibleUpdateQuery 构建统一的可选字段更新查询
// 支持可选字段更新，只更新非nil的字段
func (ut *UsageTracker) buildFlexibleUpdateQuery(event RequestEvent) (string, []interface{}, error) {
	opts, ok := event.Data.(UpdateOptions)
	if !ok {
		return "", nil, fmt.Errorf("invalid flexible_update event data type")
	}

	// 构建动态UPDATE语句
	var setParts []string
	var args []interface{}

	// 根据UpdateOptions中的非nil字段构建SET子句
	if opts.EndpointName != nil {
		setParts = append(setParts, "endpoint_name = ?")
		args = append(args, *opts.EndpointName)
	}
	if opts.RequestFamily != nil {
		setParts = append(setParts, "request_family = ?")
		args = append(args, normalizeRequestFamily(*opts.RequestFamily))
	}
	if opts.UpstreamType != nil {
		setParts = append(setParts, "upstream_type = ?")
		args = append(args, *opts.UpstreamType)
	}
	if opts.UpstreamSourceName != nil {
		setParts = append(setParts, "upstream_source_name = ?")
		args = append(args, *opts.UpstreamSourceName)
	}
	if opts.UpstreamName != nil {
		setParts = append(setParts, "upstream_name = ?")
		args = append(args, *opts.UpstreamName)
	}
	if opts.UpstreamID != nil {
		setParts = append(setParts, "upstream_id = ?")
		args = append(args, *opts.UpstreamID)
	}
	if opts.RequestSource != nil {
		value, err := normalizeRequestSource(*opts.RequestSource)
		if err != nil {
			return "", nil, err
		}
		setParts = append(setParts, "request_source = ?")
		args = append(args, value)
	}
	if opts.RequestKind != nil {
		value, err := normalizeRequestKind(*opts.RequestKind)
		if err != nil {
			return "", nil, err
		}
		setParts = append(setParts, "request_kind = ?")
		args = append(args, value)
	}
	if opts.IsStreaming != nil {
		setParts = append(setParts, "is_streaming = ?")
		args = append(args, *opts.IsStreaming)
	}
	if opts.Status != nil {
		setParts = append(setParts, "status = ?")
		args = append(args, *opts.Status)
	}
	if opts.RetryCount != nil {
		setParts = append(setParts, "retry_count = ?")
		args = append(args, *opts.RetryCount)
	}
	if opts.HttpStatus != nil {
		setParts = append(setParts, "http_status_code = ?")
		args = append(args, *opts.HttpStatus)
	}
	if opts.ModelName != nil {
		setParts = append(setParts, "model_name = ?")
		args = append(args, *opts.ModelName)
	}
	if opts.EndTime != nil {
		setParts = append(setParts, "end_time = ?")
		args = append(args, timezonepolicy.FormatStorage(*opts.EndTime))
	}
	if opts.Duration != nil {
		setParts = append(setParts, "duration_ms = ?")
		args = append(args, opts.Duration.Milliseconds())
	}
	if opts.FirstTokenMs != nil {
		setParts = append(setParts, "first_token_ms = COALESCE(first_token_ms, ?)")
		args = append(args, *opts.FirstTokenMs)
	}
	if opts.CompletionMs != nil {
		setParts = append(setParts, "completion_ms = COALESCE(completion_ms, ?)")
		args = append(args, *opts.CompletionMs)
	}
	if opts.FailureReason != nil {
		setParts = append(setParts, "failure_reason = ?")
		args = append(args, *opts.FailureReason)
	}
	if opts.RouteMode != nil {
		setParts = append(setParts, "route_mode = ?")
		args = append(args, *opts.RouteMode)
	}
	if opts.RequestedEndpoint != nil {
		setParts = append(setParts, "requested_endpoint = ?")
		args = append(args, *opts.RequestedEndpoint)
	}
	if opts.EffectiveEndpoint != nil {
		setParts = append(setParts, "effective_endpoint = ?")
		args = append(args, *opts.EffectiveEndpoint)
	}
	if opts.FallbackReason != nil {
		setParts = append(setParts, "fallback_reason = ?")
		args = append(args, *opts.FallbackReason)
	}
	if opts.RouteDecisionAt != nil {
		setParts = append(setParts, "route_decision_at = ?")
		args = append(args, timezonepolicy.FormatStorage(*opts.RouteDecisionAt))
	}
	if opts.UpstreamWriteMs != nil && *opts.UpstreamWriteMs >= 0 {
		setParts = append(setParts, "upstream_write_ms = CASE WHEN first_token_ms IS NULL THEN ? ELSE upstream_write_ms END")
		args = append(args, *opts.UpstreamWriteMs)
	}
	if opts.UpstreamDispatchMs != nil && *opts.UpstreamDispatchMs >= 0 {
		setParts = append(setParts, "upstream_dispatch_ms = CASE WHEN first_token_ms IS NULL THEN ? ELSE upstream_dispatch_ms END")
		args = append(args, *opts.UpstreamDispatchMs)
	}
	if opts.UpstreamConnMs != nil && *opts.UpstreamConnMs >= 0 {
		setParts = append(setParts, "upstream_conn_ms = CASE WHEN first_token_ms IS NULL THEN ? ELSE upstream_conn_ms END")
		args = append(args, *opts.UpstreamConnMs)
	}
	if opts.ScheduleSnapshotJSON != nil {
		setParts = append(setParts, "schedule_snapshot_json = ?")
		args = append(args, *opts.ScheduleSnapshotJSON)
	}
	if opts.PrivacyScanJSON != nil {
		setParts = append(setParts, "privacy_scan_json = ?")
		args = append(args, *opts.PrivacyScanJSON)
	}
	// v6.3：upstream_model 允许写空串；response_* 非空才覆盖
	if opts.UpstreamModel != nil {
		setParts = append(setParts, "upstream_model = ?")
		args = append(args, strings.TrimSpace(*opts.UpstreamModel))
	}
	if opts.ResponseModel != nil && strings.TrimSpace(*opts.ResponseModel) != "" {
		setParts = append(setParts, "response_model = ?")
		args = append(args, strings.TrimSpace(*opts.ResponseModel))
	}
	if opts.ResponseReasoning != nil && strings.TrimSpace(*opts.ResponseReasoning) != "" {
		setParts = append(setParts, "response_reasoning = ?")
		args = append(args, strings.TrimSpace(*opts.ResponseReasoning))
	}
	if opts.RequestModel != nil && strings.TrimSpace(*opts.RequestModel) != "" {
		setParts = append(setParts, "request_model = ?")
		args = append(args, strings.TrimSpace(*opts.RequestModel))
	}
	if opts.RequestReasoning != nil && strings.TrimSpace(*opts.RequestReasoning) != "" {
		setParts = append(setParts, "request_reasoning = ?")
		args = append(args, strings.TrimSpace(*opts.RequestReasoning))
	}

	// 如果没有字段需要更新，返回错误
	if len(setParts) == 0 {
		return "", nil, fmt.Errorf("no fields to update in flexible_update event")
	}

	// 总是更新updated_at字段
	setParts = append(setParts, fmt.Sprintf("updated_at = %s", ut.adapter.BuildUTCDateTimeNow()))

	// 构建完整的UPDATE语句
	query := fmt.Sprintf("UPDATE request_logs SET %s WHERE request_id = ?",
		strings.Join(setParts, ", "))

	// 添加WHERE条件的参数
	args = append(args, event.RequestID)

	return query, args, nil
}

// buildSuccessQuery 构建成功完成的查询
// 一次性更新所有成功相关字段：status='completed', end_time, duration_ms, Token和成本信息
// 🔧 [方案A补充] 2025-12-20: 增加 failure_reason 写入支持
func (ut *UsageTracker) buildSuccessQuery(event RequestEvent) (string, []interface{}, error) {
	data, ok := event.Data.(RequestCompleteData)
	if !ok {
		return "", nil, fmt.Errorf("invalid success event data type")
	}

	// 只有调用方明确报告 usage（或旧事件含非零 token）时才写 token/cost。
	tokens := &TokenUsage{
		InputTokens:           data.InputTokens,
		OutputTokens:          data.OutputTokens,
		CacheCreationTokens:   data.CacheCreationTokens,
		CacheCreation5mTokens: data.CacheCreation5mTokens,
		CacheCreation1hTokens: data.CacheCreation1hTokens,
		CacheReadTokens:       data.CacheReadTokens,
	}

	hasTokenUsage := data.HasTokenUsage || data.InputTokens != 0 || data.OutputTokens != 0 ||
		data.CacheCreationTokens != 0 || data.CacheCreation5mTokens != 0 || data.CacheCreation1hTokens != 0 || data.CacheReadTokens != 0
	pricing := ut.GetPricing(data.ModelName)
	multiplier, err := ut.requestAccountMultiplier(event.RequestID)
	if err != nil {
		return "", nil, err
	}
	costBreakdown := CalculateCostV2(tokens, &pricing, multiplier, data.Path)
	if data.CostOverrideUSD != nil {
		costBreakdown = CostBreakdown{TotalCost: *data.CostOverrideUSD}
	}
	var input, output, creation, creation5m, creation1h, read interface{}
	var inputCost, outputCost, creationCost, creation5mCost, creation1hCost, readCost, totalCost interface{}
	if hasTokenUsage {
		input, output = data.InputTokens, data.OutputTokens
		creation, creation5m, creation1h = data.CacheCreationTokens, data.CacheCreation5mTokens, data.CacheCreation1hTokens
		read = data.CacheReadTokens
		inputCost, outputCost = costBreakdown.InputCost, costBreakdown.OutputCost
		creationCost, creation5mCost, creation1hCost = costBreakdown.CacheCreationCost, costBreakdown.CacheCreation5mCost, costBreakdown.CacheCreation1hCost
		readCost, totalCost = costBreakdown.CacheReadCost, costBreakdown.TotalCost
	} else if data.CostOverrideUSD != nil {
		totalCost = costBreakdown.TotalCost
	}

	// 🔧 [方案A补充] 支持 failure_reason 写入（用于数据质量标记，如 stream_truncated）
	query := fmt.Sprintf(`UPDATE request_logs SET
		end_time = ?,
		duration_ms = ?,
		model_name = ?,
		input_tokens = COALESCE(?, input_tokens),
		output_tokens = COALESCE(?, output_tokens),
		cache_creation_tokens = COALESCE(?, cache_creation_tokens),
		cache_creation_5m_tokens = COALESCE(?, cache_creation_5m_tokens),
		cache_creation_1h_tokens = COALESCE(?, cache_creation_1h_tokens),
		cache_read_tokens = COALESCE(?, cache_read_tokens),
		input_cost_usd = COALESCE(?, input_cost_usd),
		output_cost_usd = COALESCE(?, output_cost_usd),
		cache_creation_cost_usd = COALESCE(?, cache_creation_cost_usd),
		cache_creation_5m_cost_usd = COALESCE(?, cache_creation_5m_cost_usd),
		cache_creation_1h_cost_usd = COALESCE(?, cache_creation_1h_cost_usd),
		cache_read_cost_usd = COALESCE(?, cache_read_cost_usd),
		total_cost_usd = COALESCE(?, total_cost_usd),
		failure_reason = ?,
		%s,
		http_status_code = CASE WHEN http_status_code IS NULL OR http_status_code = 0 THEN 200 ELSE http_status_code END,
		status = 'completed',
		updated_at = %s
	WHERE request_id = ?`, responseDiagnosticsSetClause, ut.adapter.BuildUTCDateTimeNow())

	args := []interface{}{
		timezonepolicy.FormatStorage(event.Timestamp),
		data.Duration.Milliseconds(),
		data.ModelName,
		input,
		output,
		creation,
		creation5m,
		creation1h,
		read,
		inputCost,
		outputCost,
		creationCost,
		creation5mCost,
		creation1hCost,
		readCost,
		totalCost,
		data.FailureReason, // 🔧 [方案A补充] 写入 failure_reason（可为空）
	}
	args = append(args, responseDiagnosticsArgs(data.Diagnostics)...)
	args = append(args, event.RequestID)

	return query, args, nil
}

// buildFinalFailureQuery 构建失败/取消完成的查询
// 一次性更新所有失败/取消相关字段：status, end_time, duration_ms, failure_reason/cancel_reason, 可选Token
// 🔧 [修复] 2025-12-11: 添加 model_name 和 5m/1h 缓存字段支持
func (ut *UsageTracker) buildFinalFailureQuery(event RequestEvent) (string, []interface{}, error) {
	data, ok := event.Data.(map[string]interface{})
	if !ok {
		return "", nil, fmt.Errorf("invalid final_failure event data type")
	}

	status, _ := data["status"].(string)
	reason, _ := data["reason"].(string)
	errorDetail, _ := data["error_detail"].(string)
	duration, _ := data["duration"].(time.Duration)
	httpStatus, _ := data["http_status"].(int)
	modelName, _ := data["model_name"].(string) // 🔧 [修复] 2025-12-11: 读取模型名
	inputTokens, _ := data["input_tokens"].(int64)
	outputTokens, _ := data["output_tokens"].(int64)
	cacheCreationTokens, _ := data["cache_creation_tokens"].(int64)
	cacheCreation5mTokens, _ := data["cache_creation_5m_tokens"].(int64) // 🔧 [修复] 2025-12-11
	cacheCreation1hTokens, _ := data["cache_creation_1h_tokens"].(int64) // 🔧 [修复] 2025-12-11
	cacheReadTokens, _ := data["cache_read_tokens"].(int64)
	hasTokenUsage, _ := data["has_token_usage"].(bool)
	diagnostics, _ := data["response_diagnostics"].(*ResponseDiagnostics) // v6.3
	hasTokenUsage = hasTokenUsage || inputTokens != 0 || outputTokens != 0 || cacheCreationTokens != 0 || cacheCreation5mTokens != 0 || cacheCreation1hTokens != 0 || cacheReadTokens != 0
	var input, output, creation, creation5m, creation1h, read interface{}
	var inputCost, outputCost, creationCost, creation5mCost, creation1hCost, readCost, totalCost interface{}
	if hasTokenUsage {
		usage := &TokenUsage{InputTokens: inputTokens, OutputTokens: outputTokens, CacheCreationTokens: cacheCreationTokens,
			CacheCreation5mTokens: cacheCreation5mTokens, CacheCreation1hTokens: cacheCreation1hTokens, CacheReadTokens: cacheReadTokens}
		pricing := ut.GetPricing(modelName)
		multiplier, err := ut.requestAccountMultiplier(event.RequestID)
		if err != nil {
			return "", nil, err
		}
		cost := CalculateCostV2(usage, &pricing, multiplier, ut.lookupRequestPath(event.RequestID))
		input, output, creation, creation5m, creation1h, read = inputTokens, outputTokens, cacheCreationTokens, cacheCreation5mTokens, cacheCreation1hTokens, cacheReadTokens
		inputCost, outputCost, creationCost = cost.InputCost, cost.OutputCost, cost.CacheCreationCost
		creation5mCost, creation1hCost, readCost, totalCost = cost.CacheCreation5mCost, cost.CacheCreation1hCost, cost.CacheReadCost, cost.TotalCost
	}

	// 根据状态设置相应的reason字段
	var query string
	var args []interface{}

	if status == "cancelled" {
		query = fmt.Sprintf(`UPDATE request_logs SET
			end_time = ?,
			duration_ms = ?,
			status = 'cancelled',
			cancel_reason = ?,
			http_status_code = ?,
			model_name = COALESCE(NULLIF(NULLIF(?, ''), 'unknown'), model_name),
			input_tokens = COALESCE(?, input_tokens),
			output_tokens = COALESCE(?, output_tokens),
			cache_creation_tokens = COALESCE(?, cache_creation_tokens),
			cache_creation_5m_tokens = COALESCE(?, cache_creation_5m_tokens),
			cache_creation_1h_tokens = COALESCE(?, cache_creation_1h_tokens),
			cache_read_tokens = COALESCE(?, cache_read_tokens),
			input_cost_usd = COALESCE(?, input_cost_usd), output_cost_usd = COALESCE(?, output_cost_usd),
			cache_creation_cost_usd = COALESCE(?, cache_creation_cost_usd),
			cache_creation_5m_cost_usd = COALESCE(?, cache_creation_5m_cost_usd),
			cache_creation_1h_cost_usd = COALESCE(?, cache_creation_1h_cost_usd),
			cache_read_cost_usd = COALESCE(?, cache_read_cost_usd), total_cost_usd = COALESCE(?, total_cost_usd),
			%s,
			updated_at = %s
		WHERE request_id = ?`, responseDiagnosticsSetClause, ut.adapter.BuildUTCDateTimeNow())

		args = []interface{}{
			timezonepolicy.FormatStorage(event.Timestamp),
			duration.Milliseconds(),
			reason,     // cancel_reason
			httpStatus, // http_status_code
			modelName,  // 🔧 [修复] 2025-12-11: model_name (空字符串和 'unknown' 都会被忽略)
			input, output, creation, creation5m, creation1h, read,
			inputCost, outputCost, creationCost, creation5mCost, creation1hCost, readCost, totalCost,
		}
	} else {
		// status == "failed"
		query = fmt.Sprintf(`UPDATE request_logs SET
			end_time = ?,
			duration_ms = ?,
			status = 'failed',
			failure_reason = ?,
			last_failure_reason = ?,
			http_status_code = ?,
			model_name = COALESCE(NULLIF(NULLIF(?, ''), 'unknown'), model_name),
			input_tokens = COALESCE(?, input_tokens),
			output_tokens = COALESCE(?, output_tokens),
			cache_creation_tokens = COALESCE(?, cache_creation_tokens),
			cache_creation_5m_tokens = COALESCE(?, cache_creation_5m_tokens),
			cache_creation_1h_tokens = COALESCE(?, cache_creation_1h_tokens),
			cache_read_tokens = COALESCE(?, cache_read_tokens),
			input_cost_usd = COALESCE(?, input_cost_usd), output_cost_usd = COALESCE(?, output_cost_usd),
			cache_creation_cost_usd = COALESCE(?, cache_creation_cost_usd),
			cache_creation_5m_cost_usd = COALESCE(?, cache_creation_5m_cost_usd),
			cache_creation_1h_cost_usd = COALESCE(?, cache_creation_1h_cost_usd),
			cache_read_cost_usd = COALESCE(?, cache_read_cost_usd), total_cost_usd = COALESCE(?, total_cost_usd),
			%s,
			updated_at = %s
		WHERE request_id = ?`, responseDiagnosticsSetClause, ut.adapter.BuildUTCDateTimeNow())

		args = []interface{}{
			timezonepolicy.FormatStorage(event.Timestamp),
			duration.Milliseconds(),
			reason,      // failure_reason
			errorDetail, // last_failure_reason
			httpStatus,  // http_status_code
			modelName,   // 🔧 [修复] 2025-12-11: model_name (空字符串和 'unknown' 都会被忽略)
			input, output, creation, creation5m, creation1h, read,
			inputCost, outputCost, creationCost, creation5mCost, creation1hCost, readCost, totalCost,
		}
	}
	args = append(args, responseDiagnosticsArgs(diagnostics)...)
	args = append(args, event.RequestID)

	return query, args, nil
}

func (ut *UsageTracker) requestAccountMultiplier(requestID string) (*EndpointMultiplier, error) {
	var upstreamType string
	var upstreamID int64
	err := ut.readDB.QueryRow(`SELECT COALESCE(upstream_type, ''), COALESCE(upstream_id, 0)
		FROM request_logs WHERE request_id = ?`, requestID).Scan(&upstreamType, &upstreamID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read request upstream for cost calculation: %w", err)
	}
	if upstreamType != "account" {
		return nil, nil
	}
	multiplier := ut.GetAccountMultiplier(upstreamID)
	return &multiplier, nil
}

// buildCompleteQueryWithTx 在事务中构建完成事件查询，可以查询start_time计算准确持续时间
func (ut *UsageTracker) buildCompleteQueryWithTx(ctx context.Context, tx *sql.Tx, event RequestEvent) (string, []interface{}, error) {
	data, ok := event.Data.(RequestCompleteData)
	if !ok {
		return "", nil, fmt.Errorf("invalid complete event data type")
	}

	// 首先获取请求的开始时间来计算正确的持续时间
	var startTime time.Time
	var durationMs int64

	queryStartTime := `SELECT CAST(start_time AS TEXT) FROM request_logs WHERE request_id = ?`
	var storedStart timezonepolicy.DBTime
	err := tx.QueryRowContext(ctx, queryStartTime, event.RequestID).Scan(&storedStart)
	if err != nil {
		if err == sql.ErrNoRows {
			// 记录不存在，使用估算的开始时间
			startTime = event.Timestamp.Add(-data.Duration)
			durationMs = data.Duration.Milliseconds()
		} else {
			return "", nil, fmt.Errorf("failed to get start time for duration calculation: %w", err)
		}
	} else {
		startTime = storedStart.Time
		// 计算正确的持续时间：end_time - start_time
		durationMs = event.Timestamp.Sub(startTime).Milliseconds()
	}

	// 计算成本
	tokens := &TokenUsage{
		InputTokens:         data.InputTokens,
		OutputTokens:        data.OutputTokens,
		CacheCreationTokens: data.CacheCreationTokens,
		CacheReadTokens:     data.CacheReadTokens,
	}

	inputCost, outputCost, cacheCost, readCost, totalCost := ut.calculateCost(data.ModelName, tokens, data.Path)

	// 使用计算出的准确持续时间
	query := fmt.Sprintf(`UPDATE request_logs SET
		end_time = ?,
		duration_ms = ?,
		model_name = ?,
		input_tokens = ?,
		output_tokens = ?,
		cache_creation_tokens = ?,
		cache_read_tokens = ?,
		input_cost_usd = ?,
		output_cost_usd = ?,
		cache_creation_cost_usd = ?,
		cache_read_cost_usd = ?,
		total_cost_usd = ?,
		status = CASE WHEN status != 'completed' THEN 'completed' ELSE status END,
		updated_at = %s
	WHERE request_id = ?`, ut.adapter.BuildUTCDateTimeNow())

	args := []interface{}{
		timezonepolicy.FormatStorage(event.Timestamp),
		durationMs, // 使用计算出的准确持续时间
		data.ModelName,
		data.InputTokens,
		data.OutputTokens,
		data.CacheCreationTokens,
		data.CacheReadTokens,
		inputCost,
		outputCost,
		cacheCost,
		readCost,
		totalCost,
		event.RequestID,
	}

	return query, args, nil
}

// insertRequestStart 插入请求开始记录
func (ut *UsageTracker) insertRequestStart(ctx context.Context, tx *sql.Tx, event RequestEvent) error {
	query, args, err := ut.buildStartQuery(event)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, query, args...)
	return err
}

// updateRequestStatus 更新请求状态
func (ut *UsageTracker) updateRequestStatus(ctx context.Context, tx *sql.Tx, event RequestEvent) error {
	data, ok := event.Data.(RequestUpdateData)
	if !ok {
		return fmt.Errorf("invalid update event data type")
	}

	// 如果端点名为空，只更新状态相关字段
	if data.EndpointName == "" {
		query := fmt.Sprintf(`UPDATE request_logs SET
			status = ?,
			retry_count = ?,
			http_status_code = ?,
			updated_at = %s
		WHERE request_id = ?`, ut.adapter.BuildUTCDateTimeNow())

		result, err := tx.ExecContext(ctx, query,
			data.Status,
			data.RetryCount,
			data.HTTPStatus,
			event.RequestID)

		if err != nil {
			return err
		}

		// 检查是否更新了记录
		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return err
		}

		if rowsAffected == 0 {
			// 记录不存在，先创建一个基本记录
			return ut.insertRequestStart(ctx, tx, RequestEvent{
				Type:      "start",
				RequestID: event.RequestID,
				Timestamp: event.Timestamp,
				Data: RequestStartData{
					ClientIP:    "unknown",
					UserAgent:   "unknown",
					Method:      "unknown",
					Path:        "unknown",
					IsStreaming: false, // 默认为非流式
				},
			})
		}

		return nil
	}

	// 正常更新所有字段
	query := fmt.Sprintf(`UPDATE request_logs SET
		endpoint_name = ?,
		status = ?,
		retry_count = ?,
		http_status_code = ?,
		updated_at = %s
	WHERE request_id = ?`, ut.adapter.BuildUTCDateTimeNow())

	result, err := tx.ExecContext(ctx, query,
		data.EndpointName,
		data.Status,
		data.RetryCount,
		data.HTTPStatus,
		event.RequestID)

	if err != nil {
		return err
	}

	// 检查是否更新了记录
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if rowsAffected == 0 {
		// 记录不存在，使用适配器构建INSERT OR REPLACE查询
		columns := []string{"request_id", "endpoint_name", "status", "retry_count", "http_status_code", "start_time", "updated_at"}
		placeholders := []string{"?", "?", "?", "?", "?", "?", ut.adapter.BuildUTCDateTimeNow()}
		insertQuery := ut.adapter.BuildInsertOrReplaceQuery("request_logs", columns, placeholders)

		_, err = tx.ExecContext(ctx, insertQuery,
			event.RequestID,
			data.EndpointName,
			data.Status,
			data.RetryCount,
			data.HTTPStatus,
			timezonepolicy.FormatStorage(event.Timestamp))
	}

	return err
}

// completeRequest 完成请求记录
func (ut *UsageTracker) completeRequest(ctx context.Context, tx *sql.Tx, event RequestEvent) error {
	data, ok := event.Data.(RequestCompleteData)
	if !ok {
		return fmt.Errorf("invalid complete event data type")
	}

	// 首先获取请求的开始时间来计算正确的持续时间
	var startTime time.Time
	var durationMs int64

	queryStartTime := `SELECT CAST(start_time AS TEXT) FROM request_logs WHERE request_id = ?`
	var storedStart timezonepolicy.DBTime
	err := tx.QueryRowContext(ctx, queryStartTime, event.RequestID).Scan(&storedStart)
	if err != nil {
		if err == sql.ErrNoRows {
			// 记录不存在，使用估算的开始时间
			startTime = event.Timestamp.Add(-data.Duration)
			durationMs = data.Duration.Milliseconds()
		} else {
			return fmt.Errorf("failed to get start time for duration calculation: %w", err)
		}
	} else {
		startTime = storedStart.Time
		// 计算正确的持续时间：end_time - start_time
		durationMs = event.Timestamp.Sub(startTime).Milliseconds()
	}

	// 计算成本
	tokens := &TokenUsage{
		InputTokens:           data.InputTokens,
		OutputTokens:          data.OutputTokens,
		CacheCreationTokens:   data.CacheCreationTokens,
		CacheCreation5mTokens: data.CacheCreation5mTokens,
		CacheCreation1hTokens: data.CacheCreation1hTokens,
		CacheReadTokens:       data.CacheReadTokens,
	}

	pricing := ut.GetPricing(data.ModelName)
	costBreakdown := CalculateCostV2(tokens, &pricing, nil, data.Path)

	query := fmt.Sprintf(`UPDATE request_logs SET
		end_time = ?,
		duration_ms = ?,
		model_name = ?,
		input_tokens = ?,
		output_tokens = ?,
		cache_creation_tokens = ?,
		cache_creation_5m_tokens = ?,
		cache_creation_1h_tokens = ?,
		cache_read_tokens = ?,
		input_cost_usd = ?,
		output_cost_usd = ?,
		cache_creation_cost_usd = ?,
		cache_creation_5m_cost_usd = ?,
		cache_creation_1h_cost_usd = ?,
		cache_read_cost_usd = ?,
		total_cost_usd = ?,
		%s,
		status = CASE WHEN status != 'completed' THEN 'completed' ELSE status END,
		updated_at = %s
	WHERE request_id = ?`, responseDiagnosticsSetClause, ut.adapter.BuildUTCDateTimeNow())

	completeArgs := []interface{}{
		timezonepolicy.FormatStorage(event.Timestamp),
		durationMs, // 使用计算出的正确持续时间
		data.ModelName,
		data.InputTokens,
		data.OutputTokens,
		data.CacheCreationTokens,
		data.CacheCreation5mTokens,
		data.CacheCreation1hTokens,
		data.CacheReadTokens,
		costBreakdown.InputCost,
		costBreakdown.OutputCost,
		costBreakdown.CacheCreationCost,
		costBreakdown.CacheCreation5mCost,
		costBreakdown.CacheCreation1hCost,
		costBreakdown.CacheReadCost,
		costBreakdown.TotalCost,
	}
	completeArgs = append(completeArgs, responseDiagnosticsArgs(data.Diagnostics)...)
	completeArgs = append(completeArgs, event.RequestID)
	result, err := tx.ExecContext(ctx, query, completeArgs...)

	if err != nil {
		return err
	}

	// 检查是否更新了记录
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if rowsAffected == 0 {
		// 记录不存在，创建完整记录
		insertQuery := `INSERT INTO request_logs (
			request_id, start_time, end_time, duration_ms, model_name,
			input_tokens, output_tokens, cache_creation_tokens, cache_creation_5m_tokens, cache_creation_1h_tokens, cache_read_tokens,
			input_cost_usd, output_cost_usd, cache_creation_cost_usd, cache_creation_5m_cost_usd, cache_creation_1h_cost_usd,
			cache_read_cost_usd, total_cost_usd, status
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'completed')`

		// 使用已计算的 startTime 和 durationMs
		_, err = tx.ExecContext(ctx, insertQuery,
			event.RequestID,
			timezonepolicy.FormatStorage(startTime),
			timezonepolicy.FormatStorage(event.Timestamp),
			durationMs,
			data.ModelName,
			data.InputTokens,
			data.OutputTokens,
			data.CacheCreationTokens,
			data.CacheCreation5mTokens,
			data.CacheCreation1hTokens,
			data.CacheReadTokens,
			costBreakdown.InputCost,
			costBreakdown.OutputCost,
			costBreakdown.CacheCreationCost,
			costBreakdown.CacheCreation5mCost,
			costBreakdown.CacheCreation1hCost,
			costBreakdown.CacheReadCost,
			costBreakdown.TotalCost)
	}

	return err
}

// calculateCost 计算请求成本
func (ut *UsageTracker) calculateCost(modelName string, tokens *TokenUsage, requestPath ...string) (inputCost, outputCost, cacheCost, readCost, totalCost float64) {
	pricing := ut.GetPricing(modelName)
	result := CalculateCostV2(tokens, &pricing, nil, requestPath...)
	return result.InputCost, result.OutputCost, result.CacheCreationCost, result.CacheReadCost, result.TotalCost
}

func (ut *UsageTracker) lookupRequestPath(requestID string) string {
	if strings.TrimSpace(requestID) == "" {
		return ""
	}

	db := ut.readDB
	if db == nil {
		db = ut.db
	}
	if db == nil {
		return ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	var path string
	if err := db.QueryRowContext(ctx, `SELECT path FROM request_logs WHERE request_id = ?`, requestID).Scan(&path); err != nil {
		return ""
	}
	return path
}

// periodicCleanup 定期清理历史数据
func (ut *UsageTracker) periodicCleanup() {
	defer ut.wg.Done()

	ticker := time.NewTicker(ut.config.CleanupInterval)
	defer ticker.Stop()

	slog.Debug("Periodic cleanup task started", "interval", ut.config.CleanupInterval)

	for {
		select {
		case <-ticker.C:
			if err := ut.cleanupOldRecords(); err != nil {
				slog.Error("Failed to cleanup old records", "error", err)
			}

		case <-ut.ctx.Done():
			slog.Debug("Periodic cleanup task stopped")
			return
		}
	}
}

// cleanupOldRecords 清理过期记录（使用写队列）
func (ut *UsageTracker) cleanupOldRecords() error {
	retentionDays := ut.getRetentionDays()
	if retentionDays <= 0 {
		return nil // 永久保留
	}

	cutoffTime := time.Now().AddDate(0, 0, -retentionDays)

	// 删除过期的请求记录（通过写队列）
	requestQuery := "DELETE FROM request_logs WHERE start_time < ?"
	requestWriteReq := WriteRequest{
		Query:     requestQuery,
		Args:      []interface{}{timezonepolicy.FormatStorage(cutoffTime)},
		Response:  make(chan error, 1),
		Context:   context.Background(),
		EventType: "cleanup_requests",
	}

	select {
	case ut.writeQueue <- requestWriteReq:
		var err error
		select {
		case err = <-requestWriteReq.Response:
		case <-ut.ctx.Done():
			return ut.ctx.Err()
		}
		if err != nil {
			return fmt.Errorf("failed to delete old request logs: %w", err)
		}
	case <-ut.ctx.Done():
		return ut.ctx.Err()
	}

	// 清理过期的汇总数据（通过写队列）
	summaryQuery := "DELETE FROM usage_summary WHERE date < ?"
	summaryWriteReq := WriteRequest{
		Query:     summaryQuery,
		Args:      []interface{}{ut.timezonePolicy.BusinessDate(cutoffTime)},
		Response:  make(chan error, 1),
		Context:   context.Background(),
		EventType: "cleanup_summaries",
	}

	select {
	case ut.writeQueue <- summaryWriteReq:
		var err error
		select {
		case err = <-summaryWriteReq.Response:
		case <-ut.ctx.Done():
			return ut.ctx.Err()
		}
		if err != nil {
			return fmt.Errorf("failed to delete old usage summary: %w", err)
		}
	case <-ut.ctx.Done():
		return ut.ctx.Err()
	}

	// 运行VACUUM以回收空间（通过写队列，仅对SQLite有效）
	if ut.adapter.GetDatabaseType() == "sqlite" {
		vacuumWriteReq := WriteRequest{
			Query:     "VACUUM",
			Args:      []interface{}{},
			Response:  make(chan error, 1),
			Context:   context.Background(),
			EventType: "vacuum",
		}

		select {
		case ut.writeQueue <- vacuumWriteReq:
			var err error
			select {
			case err = <-vacuumWriteReq.Response:
			case <-ut.ctx.Done():
				return ut.ctx.Err()
			}
			if err != nil {
				slog.Warn("Failed to vacuum database after cleanup", "error", err)
			}
		case <-ut.ctx.Done():
			return ut.ctx.Err()
		}
	}

	// 记录清理结果
	slog.Info("Cleaned up old records",
		"cutoff_date", ut.timezonePolicy.BusinessDate(cutoffTime),
		"retention_days", retentionDays)

	// 更新汇总统计（异步）
	go ut.updateUsageSummary()

	return nil
}

// vacuumDatabase 运行VACUUM命令回收数据库空间（仅SQLite需要）
func (ut *UsageTracker) vacuumDatabase(ctx context.Context) error {
	if ut.adapter.GetDatabaseType() != "sqlite" {
		slog.Debug("Skipping VACUUM - not a SQLite database")
		return nil
	}

	slog.Debug("Running database VACUUM to reclaim space...")

	// VACUUM不能在事务中运行
	_, err := ut.db.ExecContext(ctx, "VACUUM")
	if err != nil {
		return fmt.Errorf("failed to vacuum database: %w", err)
	}

	slog.Debug("Database VACUUM completed successfully")
	return nil
}

// updateUsageSummary 更新活动配置时区最近 7 个业务日的汇总缓存。
func (ut *UsageTracker) updateUsageSummary() {
	if err := ut.rebuildRecentUsageSummary(context.Background(), 7); err != nil {
		slog.Error("Failed to update usage summary", "error", err)
		return
	}
	slog.Info("Usage summary updated successfully", "timezone", ut.timezonePolicy.Name())
}

// GetDatabaseStats 获取数据库统计信息（使用读连接）
func (ut *UsageTracker) getDatabaseStatsInternal(ctx context.Context) (*DatabaseStats, error) {
	if ut.readDB == nil {
		return nil, fmt.Errorf("read database not initialized")
	}

	stats := &DatabaseStats{}

	// 获取请求记录总数（使用读连接）
	err := ut.readDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM request_logs").Scan(&stats.TotalRequests)
	if err != nil {
		return nil, fmt.Errorf("failed to get total requests count: %w", err)
	}

	// 获取汇总记录总数（使用读连接）
	err = ut.readDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_summary").Scan(&stats.TotalSummaries)
	if err != nil {
		return nil, fmt.Errorf("failed to get total summaries count: %w", err)
	}

	// 获取最早和最新的记录时间（使用读连接）
	var earliestStr, latestStr sql.NullString
	err = ut.readDB.QueryRowContext(ctx, "SELECT CAST(MIN(start_time) AS TEXT), CAST(MAX(start_time) AS TEXT) FROM request_logs").Scan(&earliestStr, &latestStr)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("failed to get record time range: %w", err)
	}

	if earliestStr.Valid {
		if t, err := timezonepolicy.ParseStorage(earliestStr.String); err == nil {
			stats.EarliestRecord = &t
		}
	}
	if latestStr.Valid {
		if t, err := timezonepolicy.ParseStorage(latestStr.String); err == nil {
			stats.LatestRecord = &t
		}
	}

	// 获取数据库文件大小（SQLite特有，使用读连接）
	if ut.adapter.GetDatabaseType() == "sqlite" {
		var pageCount, pageSize int64
		err = ut.readDB.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pageCount)
		if err == nil {
			err = ut.readDB.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize)
			if err == nil {
				stats.DatabaseSize = pageCount * pageSize
			}
		}
	} else {
		// MySQL数据库大小查询（可选）
		// 这里可以添加查询information_schema来获取表大小的逻辑
		stats.DatabaseSize = 0
	}

	// 获取总成本（使用读连接）
	err = ut.readDB.QueryRowContext(ctx, "SELECT COALESCE(SUM(total_cost_usd), 0) FROM request_logs WHERE total_cost_usd > 0").Scan(&stats.TotalCostUSD)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("failed to get total cost: %w", err)
	}

	return stats, nil
}

// periodicBackup 定期备份数据库
func (ut *UsageTracker) periodicBackup() {
	defer ut.wg.Done()

	ticker := time.NewTicker(6 * time.Hour) // 每6小时备份一次
	defer ticker.Stop()

	slog.Debug("Periodic backup task started")

	for {
		select {
		case <-ticker.C:
			if ut.errorHandler != nil {
				if err := ut.errorHandler.CreateBackup(); err != nil {
					slog.Error("Scheduled backup failed", "error", err)
				} else {
					slog.Debug("Scheduled backup completed successfully")
				}
			}

		case <-ut.ctx.Done():
			slog.Debug("Periodic backup task stopped")
			return
		}
	}
}

// DatabaseStats 数据库统计信息
type DatabaseStats struct {
	TotalRequests  int64      `json:"total_requests"`
	TotalSummaries int64      `json:"total_summaries"`
	EarliestRecord *time.Time `json:"earliest_record,omitempty"`
	LatestRecord   *time.Time `json:"latest_record,omitempty"`
	DatabaseSize   int64      `json:"database_size_bytes"`
	TotalCostUSD   float64    `json:"total_cost_usd"`
}
