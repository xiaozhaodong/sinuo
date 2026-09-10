package main

import (
	timezonepolicy "cc-forwarder/internal/timezone"
	"cc-forwarder/internal/tracking"
	"context"
	"fmt"
	"strings"
	"time"
)

// ============================================================
// 模型维度调用统计 API（概览页「模型调用情况」）
// ============================================================

// ModelUsageRowData 单个模型的调用统计
type ModelUsageRowData struct {
	ModelName     string  `json:"model_name"`     // 请求模型名，空值原样返回（前端标记为未知模型）
	RequestFamily string  `json:"request_family"` // claude/codex/image/other
	UpstreamType  string  `json:"upstream_type"`  // endpoint/account/subscription
	RequestCount  int64   `json:"request_count"`
	SuccessCount  int64   `json:"success_count"` // completed + processing，与 GetUsageStats 同口径
	FailedCount   int64   `json:"failed_count"`
	SuccessRate   float64 `json:"success_rate"`    // 百分比，0-100
	AvgDurationMs float64 `json:"avg_duration_ms"` // 仅统计有效耗时（>0）
	DurationCount int64   `json:"duration_count"`  // 参与均值计算的样本数，0 表示无有效耗时
	TotalTokens   int64   `json:"total_tokens"`    // input + output + cache creation + cache read
	TotalCostUSD  float64 `json:"total_cost_usd"`
}

// ModelUsageStatsData 模型调用统计结果
type ModelUsageStatsData struct {
	Period              string              `json:"period"`
	StartTime           string              `json:"start_time"`
	EndTime             string              `json:"end_time"`
	Rows                []ModelUsageRowData `json:"rows"`
	Truncated           bool                `json:"truncated"` // Rows 是否被 Top N 截断
	TotalRequests       int64               `json:"total_requests"`
	SuccessRequests     int64               `json:"success_requests"`
	FailedRequests      int64               `json:"failed_requests"`
	TotalTokens         int64               `json:"total_tokens"`
	InputTokens         int64               `json:"input_tokens"`
	OutputTokens        int64               `json:"output_tokens"`
	CacheCreationTokens int64               `json:"cache_creation_tokens"`
	CacheReadTokens     int64               `json:"cache_read_tokens"`
	TotalCostUSD        float64             `json:"total_cost_usd"`
	ClaudeCount         int64               `json:"claude_count"` // claude 请求族请求数
	CodexCount          int64               `json:"codex_count"`  // codex 请求族请求数
}

// ModelUsageQueryParams 模型调用统计查询参数
type ModelUsageQueryParams struct {
	Period        string `json:"period"`     // 1h/1d/7d/30d/90d，默认 1d
	StartDate     string `json:"start_date"` // 优先于 period
	EndDate       string `json:"end_date"`   // 优先于 period
	Model         string `json:"model"`      // 可选：模型精确匹配
	RequestFamily string `json:"request_family"`
	SourceView    string `json:"source_view"` // endpoint/account/subscription/all，默认 endpoint
	UpstreamName  string `json:"upstream_name"`
	Limit         int    `json:"limit"` // Top N，默认 8，上限 100
}

const (
	modelUsageDefaultPeriod = "1d"
	modelUsageMaxLimit      = 100
)

// resolveOverviewStatsRange 为概览统计校验完整、非空的半开区间。
// 复用已有解析口径，不让缺省的一侧变成公元 1 年或空时间。
func resolveOverviewStatsRange(policy *timezonepolicy.Policy, period, startDate, endDate string) (time.Time, time.Time, string, error) {
	if (startDate == "") != (endDate == "") {
		return time.Time{}, time.Time{}, period, fmt.Errorf("start_date and end_date must be provided together")
	}
	start, end, normalizedPeriod, err := resolveUsageStatsRange(policy, period, startDate, endDate)
	if err == nil && !end.After(start) {
		err = fmt.Errorf("end_date must be after start_date")
	}
	return start, end, normalizedPeriod, err
}

// GetModelUsageStats 获取模型维度调用统计（概览页模型调用表与 Token 分布）。
// 指标口径由后端统一定义：次数、成功率与生成耗时只计生成口径（legacy + generation），
// Token 与成本含独立压缩（legacy + generation + compaction）；压缩专用模型保留
// 0 次调用行，不因次数为 0 丢行。
// 汇总字段覆盖全部模型，Rows 只保留请求数最高的 Limit 个。
func (a *App) GetModelUsageStats(params ModelUsageQueryParams) (ModelUsageStatsData, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	result := ModelUsageStatsData{Rows: []ModelUsageRowData{}}

	policy, err := a.activeTimezonePolicy()
	if err != nil {
		return result, err
	}

	period := strings.TrimSpace(params.Period)
	if period == "" {
		period = modelUsageDefaultPeriod
	}
	startTime, endTime, normalizedPeriod, err := resolveOverviewStatsRange(policy, period, params.StartDate, params.EndDate)
	if err != nil {
		return result, err
	}
	result.Period = normalizedPeriod
	result.StartTime = formatAPITime(startTime)
	result.EndTime = formatAPITime(endTime)

	if a.usageTracker == nil {
		return result, nil
	}

	limit := params.Limit
	if limit <= 0 {
		limit = tracking.DefaultModelUsageLimit
	}
	if limit > modelUsageMaxLimit {
		limit = modelUsageMaxLimit
	}

	opts := &tracking.QueryOptions{
		StartDate:     &startTime,
		EndDate:       &endTime,
		ModelName:     params.Model,
		RequestFamily: params.RequestFamily,
		UpstreamName:  params.UpstreamName,
		UpstreamType:  sourceViewToUpstreamType(params.SourceView),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stats, err := a.usageTracker.QueryModelUsageStatsWithHotPool(ctx, opts, limit)
	if err != nil {
		return result, err
	}
	if stats == nil {
		return result, nil
	}

	result.Truncated = stats.Truncated
	result.TotalRequests = stats.TotalRequests
	result.SuccessRequests = stats.SuccessRequests
	result.FailedRequests = stats.FailedRequests
	result.TotalTokens = stats.TotalTokens
	result.InputTokens = stats.InputTokens
	result.OutputTokens = stats.OutputTokens
	result.CacheCreationTokens = stats.CacheCreationTokens
	result.CacheReadTokens = stats.CacheReadTokens
	result.TotalCostUSD = stats.TotalCostUSD
	result.ClaudeCount = stats.ClaudeCount
	result.CodexCount = stats.CodexCount

	result.Rows = make([]ModelUsageRowData, 0, len(stats.Rows))
	for _, row := range stats.Rows {
		result.Rows = append(result.Rows, ModelUsageRowData{
			ModelName:     row.ModelName,
			RequestFamily: row.RequestFamily,
			UpstreamType:  row.UpstreamType,
			RequestCount:  row.RequestCount,
			SuccessCount:  row.SuccessCount,
			FailedCount:   row.FailedCount,
			SuccessRate:   row.SuccessRate,
			AvgDurationMs: row.AvgDurationMs,
			DurationCount: row.DurationCount,
			TotalTokens:   row.TotalTokens,
			TotalCostUSD:  row.TotalCostUSD,
		})
	}

	return result, nil
}
