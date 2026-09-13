// app_api_usage.go - 使用统计 API (Wails Bindings)
// 包含使用统计摘要、请求记录查询等功能

package main

import (
	"context"
	"database/sql"
	"strings"
	"time"

	timezonepolicy "cc-forwarder/internal/timezone"
	"cc-forwarder/internal/tracking"
)

// ============================================================
// 使用统计 API
// ============================================================

// UsageSummary 使用统计摘要
type UsageSummary struct {
	TotalRequests        int64   `json:"total_requests"`          // 运行时请求数
	AllTimeTotalRequests int64   `json:"all_time_total_requests"` // 全部历史请求数（数据库）
	TodayRequests        int64   `json:"today_requests"`          // 今日请求数（数据库）
	SuccessRequests      int64   `json:"success_requests"`
	FailedRequests       int64   `json:"failed_requests"`
	TotalInputTokens     int64   `json:"total_input_tokens"`
	TotalOutputTokens    int64   `json:"total_output_tokens"`
	TotalCost            float64 `json:"total_cost"`            // 运行时成本
	TodayCost            float64 `json:"today_cost"`            // 今日成本（数据库）
	AllTimeTotalCost     float64 `json:"all_time_total_cost"`   // 全部历史成本（数据库）
	TodayTokens          int64   `json:"today_tokens"`          // 今日 tokens（数据库）
	AllTimeTotalTokens   int64   `json:"all_time_total_tokens"` // 全部历史 tokens（数据库）
}

// GetUsageSummary 获取使用统计摘要
// 当没有传递时间参数时，返回运行时统计（从内存）+ 全部历史请求总数（从数据库）
// 当传递时间参数时，返回历史数据（从数据库）
func (a *App) GetUsageSummary(startTimeStr, endTimeStr string) (UsageSummary, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	// 如果没有传递时间参数，返回运行时统计（与 Web 版本 /api/v1/connections 一致）
	if startTimeStr == "" && endTimeStr == "" {
		if a.monitoringMiddleware == nil {
			return UsageSummary{}, nil
		}

		// 从内存中获取运行时统计
		metrics := a.monitoringMiddleware.GetMetrics()
		stats := metrics.GetMetrics()

		// 计算总 Token
		totalInputTokens := stats.TotalTokenUsage.InputTokens
		totalOutputTokens := stats.TotalTokenUsage.OutputTokens

		// 查询数据库获取全部历史和今日统计
		var allTimeTotalCost float64
		var allTimeTotalTokens int64
		var allTimeTotal int64
		var todayCost float64
		var todayTokens int64
		var todayRequests int64

		if a.usageTracker != nil {
			ctx := context.Background()
			policy, err := a.activeTimezonePolicy()
			if err != nil {
				return UsageSummary{}, err
			}

			// 查询全部历史统计（endpoint + account）
			allTimeTotalCost, allTimeTotalTokens, allTimeTotal = a.queryStatsFromDB(ctx, time.Time{}, time.Now(), "all")

			// 查询今日统计（使用配置的时区）
			todayStart, todayEnd, err := policy.DayRange(policy.BusinessDate(time.Now()))
			if err != nil {
				return UsageSummary{}, err
			}
			todayCost, todayTokens, todayRequests = a.queryStatsFromDB(ctx, todayStart, todayEnd, "all")
		}

		return UsageSummary{
			TotalRequests:        stats.TotalRequests,
			AllTimeTotalRequests: allTimeTotal,
			TodayRequests:        todayRequests,
			SuccessRequests:      stats.SuccessfulRequests,
			FailedRequests:       stats.FailedRequests,
			TotalInputTokens:     totalInputTokens,
			TotalOutputTokens:    totalOutputTokens,
			TotalCost:            0, // 运行时统计不计算成本
			TodayCost:            todayCost,
			AllTimeTotalCost:     allTimeTotalCost,
			TodayTokens:          todayTokens,
			AllTimeTotalTokens:   allTimeTotalTokens,
		}, nil
	}

	// 传递了时间参数，从数据库查询历史数据
	if a.usageTracker == nil {
		return UsageSummary{}, nil
	}

	// 解析时间范围
	var startTime, endTime time.Time
	var err error

	policy, err := a.activeTimezonePolicy()
	if err != nil {
		return UsageSummary{}, err
	}
	if startTimeStr != "" {
		startTime, err = policy.ParseInput(startTimeStr)
		if err != nil {
			return UsageSummary{}, err
		}
	} else {
		startTime = time.Now().AddDate(0, 0, -7)
	}

	if endTimeStr != "" {
		endTime, err = policy.ParseInput(endTimeStr)
		if err != nil {
			return UsageSummary{}, err
		}
	} else {
		endTime = time.Now()
	}

	ctx := context.Background()
	summaries, err := a.usageTracker.GetUsageSummary(ctx, startTime, endTime)
	if err != nil {
		return UsageSummary{}, err
	}

	// 聚合所有摘要
	result := UsageSummary{}
	for _, s := range summaries {
		result.TotalRequests += int64(s.RequestCount)
		result.SuccessRequests += int64(s.SuccessCount)
		result.FailedRequests += int64(s.ErrorCount)
		result.TotalInputTokens += s.TotalInputTokens
		result.TotalOutputTokens += s.TotalOutputTokens
		result.TotalCost += s.TotalCostUSD
	}

	return result, nil
}

func normalizeSourceView(sourceView string) string {
	switch strings.ToLower(strings.TrimSpace(sourceView)) {
	case "account":
		return "account"
	case "subscription":
		return "subscription"
	case "all":
		return "all"
	default:
		// 向后兼容：未传或非法值都按 endpoint 处理
		return "endpoint"
	}
}

func sourceViewToUpstreamType(sourceView string) string {
	switch normalizeSourceView(sourceView) {
	case "account":
		return "account"
	case "subscription":
		return "subscription"
	case "all":
		return ""
	default:
		return "endpoint"
	}
}

func (a *App) queryStatsFromSingleTable(ctx context.Context, db *sql.DB, tableName string, startTime, endTime time.Time, upstreamType string) (cost float64, tokens int64, requests int64, err error) {
	query := "SELECT COALESCE(SUM(total_cost_usd), 0), COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0), COUNT(*) FROM " + tableName

	var conditions []string
	var args []interface{}

	if upstreamType != "" {
		conditions = append(conditions, "COALESCE(upstream_type, 'endpoint') = ?")
		args = append(args, upstreamType)
	}
	if !startTime.IsZero() {
		conditions = append(conditions, "start_time >= ? AND start_time < ?")
		args = append(args, timezonepolicy.FormatStorage(startTime), timezonepolicy.FormatStorage(endTime))
	}
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}

	err = db.QueryRowContext(ctx, query, args...).Scan(&cost, &tokens, &requests)
	return
}

// queryStatsFromDB 按 source_view 查询成本、tokens 和请求数：
// endpoint/account/subscription/all 统一基于 request_logs + upstream_type 查询
func (a *App) queryStatsFromDB(ctx context.Context, startTime, endTime time.Time, sourceView string) (cost float64, tokens int64, requests int64) {
	db := a.coreDB()
	if db == nil {
		return 0, 0, 0
	}

	switch normalizeSourceView(sourceView) {
	case "account":
		accountCost, accountTokens, accountRequests, err := a.queryStatsFromSingleTable(ctx, db, "request_logs", startTime, endTime, "account")
		if err != nil {
			if a.logger != nil {
				a.logger.Debug("查询 request_logs(account) 失败", "error", err)
			}
			return 0, 0, 0
		}
		return accountCost, accountTokens, accountRequests
	case "all":
		allCost, allTokens, allRequests, err := a.queryStatsFromSingleTable(ctx, db, "request_logs", startTime, endTime, "")
		if err != nil {
			if a.logger != nil {
				a.logger.Debug("查询 request_logs(all) 失败", "error", err)
			}
			return 0, 0, 0
		}
		return allCost, allTokens, allRequests
	default:
		// endpoint（默认）
		endpointCost, endpointTokens, endpointRequests, err := a.queryStatsFromSingleTable(ctx, db, "request_logs", startTime, endTime, "endpoint")
		if err != nil {
			if a.logger != nil {
				a.logger.Debug("查询 request_logs(endpoint) 失败", "error", err)
			}
			return 0, 0, 0
		}
		return endpointCost, endpointTokens, endpointRequests
	}
}

// RequestRecord 请求记录
type RequestRecord struct {
	ID                    string  `json:"id"`
	RequestID             string  `json:"request_id"`
	Timestamp             string  `json:"timestamp"`
	RequestFamily         string  `json:"request_family"`
	Endpoint              string  `json:"endpoint"` // Claude 路由诊断兼容字段
	Model                 string  `json:"model"`
	Status                string  `json:"status"`
	HTTPStatus            *int    `json:"http_status"`              // 无 HTTP 响应（如订阅 WS 内生成）为 null
	RetryCount            int     `json:"retry_count"`              // 重试次数
	FailureReason         string  `json:"failure_reason,omitempty"` // 失败原因
	CancelReason          string  `json:"cancel_reason,omitempty"`  // 取消原因
	Path                  string  `json:"path"`                     // 规范化请求路径
	Method                string  `json:"method"`                   // 请求方法（订阅 WS 内生成为 GET）
	RequestSource         string  `json:"request_source"`           // legacy_proxy / codex_subscription_proxy
	RequestKind           string  `json:"request_kind"`             // legacy / generation / compaction
	UsageKnown            bool    `json:"usage_known"`              // false 表示未收到用量，token 列的 0 是未知而非真实零
	CostKnown             bool    `json:"cost_known"`               // false 表示未收到成本，cost 的 0 是未知而非真实零
	UpstreamType          string  `json:"upstream_type"`            // endpoint/account/subscription
	UpstreamSourceName    string  `json:"upstream_source_name"`     // 订阅源
	UpstreamName          string  `json:"upstream_name"`            // 账号名/端点名
	UpstreamID            int64   `json:"upstream_id"`              // 账号ID（可空）
	RouteMode             string  `json:"route_mode,omitempty"`
	RequestedEndpoint     string  `json:"requested_endpoint,omitempty"`
	EffectiveEndpoint     string  `json:"effective_endpoint,omitempty"`
	FallbackReason        string  `json:"fallback_reason,omitempty"`
	RouteDecisionAt       string  `json:"route_decision_at,omitempty"`
	InputTokens           int64   `json:"input_tokens"`
	OutputTokens          int64   `json:"output_tokens"`
	CacheCreationTokens   int64   `json:"cache_creation_tokens"`    // 总缓存创建（向后兼容）
	CacheCreation5mTokens int64   `json:"cache_creation_5m_tokens"` // v5.0.1: 5分钟缓存
	CacheCreation1hTokens int64   `json:"cache_creation_1h_tokens"` // v5.0.1: 1小时缓存
	CacheReadTokens       int64   `json:"cache_read_tokens"`
	ResponseTime          int64   `json:"response_time"`
	FirstTokenMs          *int64  `json:"first_token_ms"`
	CompletionMs          *int64  `json:"completion_ms"` // 首个有效流式响应到流式完成耗时（毫秒）
	IsStreaming           bool    `json:"is_streaming"`
	Cost                  float64 `json:"cost"`
}

// RequestListResult 请求列表结果
type RequestListResult struct {
	Requests []RequestRecord `json:"requests"`
	Total    int             `json:"total"`
	Page     int             `json:"page"`
	PageSize int             `json:"page_size"`
}

// RequestQueryParams 请求查询参数
type RequestQueryParams struct {
	Page          int    `json:"page"`
	PageSize      int    `json:"page_size"`
	StartDate     string `json:"start_date"` // 格式：2025-12-05T00:00 或 2025-12-05T00:00:00+08:00
	EndDate       string `json:"end_date"`   // 格式：2025-12-05T23:59 或 2025-12-05T23:59:59+08:00
	Status        string `json:"status"`     // 可选：completed, failed, pending 等
	Model         string `json:"model"`      // 可选：模型名称
	RequestFamily string `json:"request_family"`
	UpstreamName  string `json:"upstream_name"`
	SourceView    string `json:"source_view"` // 可选：endpoint/account/subscription/all，默认 endpoint
	// IncludeCompaction 可选：显式 false 时列表只保留生成口径（legacy + generation）。
	// 概览的次数指标是生成口径，跳转时收窄列表避免行数对不上；缺省承载全部落库记录。
	IncludeCompaction *bool `json:"include_compaction"`
}

// GetRequests 获取请求记录列表（热池+数据库双源查询）
// 支持筛选参数：时间范围、状态、模型等
func (a *App) GetRequests(params RequestQueryParams) (RequestListResult, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.usageTracker == nil {
		return RequestListResult{}, nil
	}

	page := params.Page
	pageSize := params.PageSize
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	offset := (page - 1) * pageSize

	ctx := context.Background()

	policy, err := a.activeTimezonePolicy()
	if err != nil {
		return RequestListResult{}, err
	}

	var startTime, endTime time.Time

	// 解析开始时间
	if params.StartDate != "" {
		startTime, err = policy.ParseInput(params.StartDate)
		if err != nil {
			return RequestListResult{}, err
		}
	}
	// 解析结束时间
	if params.EndDate != "" {
		endTime, err = policy.ParseInput(params.EndDate)
		if err != nil {
			return RequestListResult{}, err
		}
	}

	// 如果没有传时间参数，默认查询今天
	if startTime.IsZero() {
		startTime, endTime, err = policy.DayRange(policy.BusinessDate(time.Now()))
		if err != nil {
			return RequestListResult{}, err
		}
	}
	if endTime.IsZero() {
		_, endTime, err = policy.DayRange(policy.BusinessDate(time.Now()))
		if err != nil {
			return RequestListResult{}, err
		}
	}

	// 使用热池+数据库双源查询（与 HTTP API 一致）
	opts := &tracking.QueryOptions{
		StartDate:     &startTime,
		EndDate:       &endTime,
		ModelName:     params.Model,
		RequestFamily: params.RequestFamily,
		UpstreamName:  params.UpstreamName,
		Status:        params.Status,
		Limit:         pageSize,
		Offset:        offset,
	}
	opts.UpstreamType = sourceViewToUpstreamType(params.SourceView)
	if params.IncludeCompaction != nil && !*params.IncludeCompaction {
		opts.RequestKinds = tracking.GenerationMetricRequestKinds()
	} else {
		opts.RequestKinds = tracking.UsageMetricRequestKinds()
	}

	requests, total, err := a.usageTracker.QueryRequestDetailsWithHotPool(ctx, opts)
	if err != nil {
		return RequestListResult{}, err
	}

	result := RequestListResult{
		Requests: make([]RequestRecord, 0, len(requests)),
		Page:     page,
		PageSize: pageSize,
		Total:    int(total),
	}

	for _, r := range requests {
		result.Requests = append(result.Requests, requestDetailToRecord(r))
	}

	return result, nil
}

// requestDetailToRecord 将 tracking.RequestDetail 映射为 Wails RequestRecord。
// 列表与生命周期详情两出口共用，字段演进天然同步。
// formatAPITime 的真实契约：零值返回空串，非零值经 FormatStorage 输出固定微秒精度 UTC ISO。
func requestDetailToRecord(r tracking.RequestDetail) RequestRecord {
	record := RequestRecord{
		RequestID:             r.RequestID,
		Timestamp:             formatAPITime(r.StartTime),
		RequestFamily:         r.RequestFamily,
		Endpoint:              r.EndpointName,
		Model:                 r.ModelName,
		Status:                r.Status,
		RetryCount:            r.RetryCount,
		FailureReason:         r.FailureReason,
		CancelReason:          r.CancelReason,
		UpstreamType:          r.UpstreamType,
		UpstreamSourceName:    r.UpstreamSourceName,
		UpstreamName:          r.UpstreamName,
		UpstreamID:            r.UpstreamID,
		RouteMode:             r.RouteMode,
		RequestedEndpoint:     r.RequestedEndpoint,
		EffectiveEndpoint:     r.EffectiveEndpoint,
		FallbackReason:        r.FallbackReason,
		Path:                  r.Path,
		Method:                r.Method,
		RequestSource:         r.RequestSource,
		RequestKind:           r.RequestKind,
		UsageKnown:            r.UsageKnown,
		CostKnown:             r.CostKnown,
		InputTokens:           r.InputTokens,
		OutputTokens:          r.OutputTokens,
		CacheCreationTokens:   r.CacheCreationTokens,
		CacheCreation5mTokens: r.CacheCreation5mTokens, // v5.0.1+
		CacheCreation1hTokens: r.CacheCreation1hTokens, // v5.0.1+
		CacheReadTokens:       r.CacheReadTokens,
		IsStreaming:           r.IsStreaming,
		Cost:                  r.TotalCostUSD,
	}

	// 处理指针字段
	record.HTTPStatus = r.HTTPStatusCode
	if r.DurationMs != nil {
		record.ResponseTime = *r.DurationMs
	}
	if r.RouteDecisionAt != nil {
		record.RouteDecisionAt = formatAPITime(*r.RouteDecisionAt)
	}
	record.FirstTokenMs = r.FirstTokenMs
	record.CompletionMs = r.CompletionMs
	return record
}

// RequestLifecycleDetail 请求生命周期详情（弹窗权威数据源）。
// 三个新字段不在列表 DTO 中，只在本接口返回。
type RequestLifecycleDetail struct {
	Found                bool           `json:"found"`
	Source               string         `json:"source,omitempty"` // hot_pool | database
	Request              *RequestRecord `json:"request,omitempty"`
	UpstreamWriteMs      *int64         `json:"upstream_write_ms,omitempty"`
	ScheduleSnapshotJSON string         `json:"schedule_snapshot_json,omitempty"`
	PrivacyScanJSON      string         `json:"privacy_scan_json,omitempty"`
}

// GetRequestLifecycleDetail 获取单请求生命周期详情（桌面专用）。
// 行不存在时 Found=false；Request 为弹窗权威行数据（status/tokens/耗时/失败原因等）。
func (a *App) GetRequestLifecycleDetail(requestID string) (RequestLifecycleDetail, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.usageTracker == nil || strings.TrimSpace(requestID) == "" {
		return RequestLifecycleDetail{}, nil
	}

	data, err := a.usageTracker.GetRequestLifecycleData(context.Background(), requestID)
	if err != nil {
		return RequestLifecycleDetail{}, err
	}
	if data == nil {
		return RequestLifecycleDetail{}, nil
	}

	record := requestDetailToRecord(data.Detail)
	return RequestLifecycleDetail{
		Found:                true,
		Source:               data.Source,
		Request:              &record,
		UpstreamWriteMs:      data.UpstreamWriteMs,
		ScheduleSnapshotJSON: data.ScheduleSnapshotJSON,
		PrivacyScanJSON:      data.PrivacyScanJSON,
	}, nil
}

// ============================================================
// 使用统计 API (与 HTTP API 格式一致)
// ============================================================

// UsageStatsData 使用统计数据（与 HTTP API /api/v1/usage/stats 格式一致）
type UsageStatsData struct {
	Period           string  `json:"period"`
	TotalRequests    int     `json:"total_requests"`
	SuccessRate      float64 `json:"success_rate"`
	AvgDurationMs    float64 `json:"avg_duration_ms"`
	TotalCostUSD     float64 `json:"total_cost_usd"`
	TotalTokens      int64   `json:"total_tokens"`
	FailedCount      int     `json:"failed_requests"`
	InterruptedCount int     `json:"interrupted_requests"`
}

// UsageStatsQueryParams 使用统计查询参数
type UsageStatsQueryParams struct {
	Period        string `json:"period"`     // 时间周期: "1h", "1d", "7d", "30d", "90d"
	StartDate     string `json:"start_date"` // 开始时间（优先于 period）
	EndDate       string `json:"end_date"`   // 结束时间（优先于 period）
	Status        string `json:"status"`     // 可选：状态筛选
	Model         string `json:"model"`      // 可选：模型筛选
	RequestFamily string `json:"request_family"`
	UpstreamName  string `json:"upstream_name"`
	SourceView    string `json:"source_view"` // 可选：endpoint/account/subscription/all，默认 endpoint
	// IncludeCompaction 可选：请求页把列表口径一起带进来，显式传值时次数与用量两项
	// 指标使用同一种范围（true=含独立压缩，false=仅生成），保证顶部汇总与列表一致；
	// 缺省（概览快照）才按指标分别取口径。
	IncludeCompaction *bool `json:"include_compaction"`
}

// resolveUsageStatsRange 解析统计查询的时间范围：显式 start/end 优先，否则按 period 计算。
// 未知 period 回退到 30 天，与 GetUsageStats 的既有口径一致。
func resolveUsageStatsRange(policy *timezonepolicy.Policy, period, startDateStr, endDateStr string) (time.Time, time.Time, string, error) {
	var startTime, endTime time.Time
	var useCustomRange bool

	if startDateStr != "" {
		parsed, err := policy.ParseInput(startDateStr)
		if err != nil {
			return startTime, endTime, period, err
		}
		startTime = parsed
		useCustomRange = true
	}
	if endDateStr != "" {
		parsed, err := policy.ParseInput(endDateStr)
		if err != nil {
			return startTime, endTime, period, err
		}
		endTime = parsed
		useCustomRange = true
	}
	if useCustomRange {
		return startTime, endTime, period, nil
	}

	endTime = time.Now()
	switch period {
	case "1h":
		startTime = endTime.Add(-1 * time.Hour)
	case "1d":
		startTime = endTime.AddDate(0, 0, -1)
	case "7d":
		startTime = endTime.AddDate(0, 0, -7)
	case "30d":
		startTime = endTime.AddDate(0, 0, -30)
	case "90d":
		startTime = endTime.AddDate(0, 0, -90)
	default:
		startTime = endTime.AddDate(0, 0, -30)
		period = "30d"
	}
	return startTime, endTime, period, nil
}

// GetUsageStats 获取使用统计（与 HTTP API 格式一致）
// 支持完整筛选参数，与前端 buildQueryParams() 配合
func (a *App) GetUsageStats(params UsageStatsQueryParams) (UsageStatsData, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	period := params.Period
	if period == "" {
		period = "30d"
	}
	normalizedSourceView := normalizeSourceView(params.SourceView)

	result := UsageStatsData{
		Period: period,
	}

	policy, err := a.activeTimezonePolicy()
	if err != nil {
		return result, err
	}

	// 优先使用自定义时间范围，否则按 period 计算
	startTime, endTime, normalizedPeriod, err := resolveUsageStatsRange(policy, period, params.StartDate, params.EndDate)
	if err != nil {
		return result, err
	}
	result.Period = normalizedPeriod

	// 如果有 usageTracker，使用数据库聚合 + 热池补偿，避免加载大量明细到内存。
	// 指标口径由后端统一定义，前端不传种类：同一接口混合两类指标，不能把全部字段
	// 套进同一个 kind 条件 —— 次数、成功率与生成耗时只计生成口径；Token 与成本计入
	// 独立压缩的已报告用量与可计算成本。
	if a.usageTracker != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		baseOpts := &tracking.QueryOptions{
			StartDate:     &startTime,
			EndDate:       &endTime,
			ModelName:     params.Model,
			RequestFamily: params.RequestFamily,
			UpstreamName:  params.UpstreamName,
			UpstreamType:  sourceViewToUpstreamType(normalizedSourceView),
			Status:        params.Status,
		}
		generationKinds := tracking.GenerationMetricRequestKinds()
		usageKinds := tracking.UsageMetricRequestKinds()
		if params.IncludeCompaction != nil {
			// 请求页：列表与顶部汇总同口径，不能只改 GetRequests。
			listKinds := tracking.UsageMetricRequestKinds()
			if !*params.IncludeCompaction {
				listKinds = tracking.GenerationMetricRequestKinds()
			}
			generationKinds, usageKinds = listKinds, listKinds
		}

		generationOpts := *baseOpts
		generationOpts.RequestKinds = generationKinds
		usageOpts := *baseOpts
		usageOpts.RequestKinds = usageKinds

		generationStats, err := a.usageTracker.QueryAggregatedRequestStatsWithHotPool(ctx, &generationOpts)
		if err != nil {
			return result, err
		}
		usageStats, err := a.usageTracker.QueryAggregatedRequestStatsWithHotPool(ctx, &usageOpts)
		if err != nil {
			return result, err
		}

		result.TotalRequests = int(generationStats.TotalRequests)
		result.FailedCount = int(generationStats.FailedRequests)
		result.InterruptedCount = int(generationStats.InterruptedRequests)
		if generationStats.TotalRequests > 0 {
			result.SuccessRate = float64(generationStats.SuccessRequests) / float64(generationStats.TotalRequests) * 100
		}
		if generationStats.DurationCount > 0 {
			result.AvgDurationMs = float64(generationStats.TotalDurationMs) / float64(generationStats.DurationCount)
		}
		// 只有独立压缩的窗口仍返回实际 Token 与成本，不因生成次数为 0 而清零。
		result.TotalTokens = usageStats.TotalTokens
		result.TotalCostUSD = usageStats.TotalCostUSD
		return result, nil
	}

	// 无 usageTracker（异常装配）时退回运行时统计；查询失败在上方直接返回错误，
	// 不能用不含种类信息的累计值兜底过滤结果。
	return a.getUsageStatsFromRuntime(period), nil
}

// getUsageStatsFromRuntime 从运行时内存获取统计数据
func (a *App) getUsageStatsFromRuntime(period string) UsageStatsData {
	result := UsageStatsData{
		Period: period,
	}

	if a.monitoringMiddleware == nil {
		return result
	}

	metrics := a.monitoringMiddleware.GetMetrics()
	if metrics == nil {
		return result
	}

	stats := metrics.GetMetrics()

	// 从运行时统计计算
	result.TotalRequests = int(stats.TotalRequests)
	result.FailedCount = int(stats.FailedRequests)

	// 计算成功率
	if stats.TotalRequests > 0 {
		result.SuccessRate = float64(stats.SuccessfulRequests) / float64(stats.TotalRequests) * 100
	}

	// 计算总 Token
	result.TotalTokens = stats.TotalTokenUsage.InputTokens + stats.TotalTokenUsage.OutputTokens +
		stats.TotalTokenUsage.CacheCreationTokens + stats.TotalTokenUsage.CacheReadTokens

	// 计算平均耗时（使用 Metrics 提供的方法）
	avgResponseTime := metrics.GetAverageResponseTime()
	result.AvgDurationMs = float64(avgResponseTime.Milliseconds())

	// 运行时不计算成本
	result.TotalCostUSD = 0

	return result
}

// ============================================================
// Token 使用统计 API
// ============================================================

// TokenUsageData Token 使用数据结构
type TokenUsageData struct {
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
}

// GetTokenUsage 获取当前 Token 使用统计（运行时内存数据）
func (a *App) GetTokenUsage() TokenUsageData {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.monitoringMiddleware == nil {
		return TokenUsageData{}
	}

	metrics := a.monitoringMiddleware.GetMetrics()
	if metrics == nil {
		return TokenUsageData{}
	}

	tokenStats := metrics.GetTotalTokenStats()

	return TokenUsageData{
		InputTokens:         tokenStats.InputTokens,
		OutputTokens:        tokenStats.OutputTokens,
		CacheCreationTokens: tokenStats.CacheCreationTokens,
		CacheReadTokens:     tokenStats.CacheReadTokens,
		TotalTokens:         tokenStats.InputTokens + tokenStats.OutputTokens + tokenStats.CacheCreationTokens + tokenStats.CacheReadTokens,
	}
}
