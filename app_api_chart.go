// app_api_chart.go - 图表数据 API (Wails Bindings)
// 包含请求趋势、响应时间、端点健康状态、端点成本等图表数据

package main

import (
	"context"
	"strings"
	"time"

	"cc-forwarder/internal/tracking"
)

// ============================================================
// 图表数据 API
// ============================================================

// ChartDataPoint 图表数据点
type ChartDataPoint struct {
	Time      string  `json:"time"`
	Total     int64   `json:"total"`
	Success   int64   `json:"success"`
	Fail      int64   `json:"fail"`
	Avg       float64 `json:"avg"`
	Min       float64 `json:"min"`
	Max       float64 `json:"max"`
	Value     int64   `json:"value"`
	Timestamp string  `json:"timestamp"`
}

// ============================================================
// 请求趋势（按 request_logs 时间分桶）
// ============================================================

// RequestTrendQueryParams 请求趋势查询参数
type RequestTrendQueryParams struct {
	Period        string `json:"period"`     // 1h/1d/7d/30d/90d，默认 1d
	StartDate     string `json:"start_date"` // 优先于 period
	EndDate       string `json:"end_date"`   // 优先于 period
	Model         string `json:"model"`      // 可选：模型精确匹配
	RequestFamily string `json:"request_family"`
	SourceView    string `json:"source_view"` // endpoint/account/all
	UpstreamName  string `json:"upstream_name"`
}

// RequestTrendPoint 趋势数据点：一个时间桶的请求数 / 成功 / 失败。
type RequestTrendPoint struct {
	Time          string  `json:"time"` // 桶起点（UTC ISO）
	Total         int64   `json:"total"`
	Success       int64   `json:"success"`
	Fail          int64   `json:"fail"`
	AvgDurationMs float64 `json:"avg_duration_ms"`
	DurationCount int64   `json:"duration_count"`
}

// RequestTrendData 请求趋势结果
type RequestTrendData struct {
	Period      string              `json:"period"`
	Granularity string              `json:"granularity"` // minute/hour/day
	StartTime   string              `json:"start_time"`
	EndTime     string              `json:"end_time"`
	Points      []RequestTrendPoint `json:"points"`
}

// trendMaxBuckets 单次趋势查询的桶数上限，防止异常大的区间把查询拖垮。
const trendMaxBuckets = 400

func trendGranularityForPeriod(period string) tracking.TrendGranularity {
	switch period {
	case "1h":
		return tracking.TrendGranularityMinute
	case "1d":
		return tracking.TrendGranularityHour
	default:
		return tracking.TrendGranularityDay
	}
}

// GetRequestTrendData 获取请求趋势（按 request_logs 时间分桶，含热池补偿）。
// 桶边界按配置时区对齐：1h 用 5 分钟桶、1d 用小时桶、7d/30d/90d 用业务日桶。
func (a *App) GetRequestTrendData(params RequestTrendQueryParams) (RequestTrendData, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	result := RequestTrendData{Points: []RequestTrendPoint{}}

	policy, err := a.activeTimezonePolicy()
	if err != nil {
		return result, err
	}

	period := strings.TrimSpace(params.Period)
	if period == "" {
		period = "1d"
	}
	startTime, endTime, normalizedPeriod, err := resolveOverviewStatsRange(policy, period, params.StartDate, params.EndDate)
	if err != nil {
		return result, err
	}
	result.Period = normalizedPeriod
	result.StartTime = formatAPITime(startTime)
	result.EndTime = formatAPITime(endTime)

	granularity := trendGranularityForPeriod(normalizedPeriod)
	result.Granularity = string(granularity)

	if a.usageTracker == nil {
		return result, nil
	}

	buckets, err := tracking.BuildTrendBuckets(policy, startTime, endTime, granularity, trendMaxBuckets)
	if err != nil {
		return result, err
	}
	if len(buckets) == 0 {
		return result, nil
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

	stats, err := a.usageTracker.QueryRequestTrendBuckets(ctx, opts, buckets)
	if err != nil {
		return result, err
	}

	result.Points = make([]RequestTrendPoint, 0, len(stats))
	for _, bucket := range stats {
		result.Points = append(result.Points, RequestTrendPoint{
			Time:          formatAPITime(bucket.Start),
			Total:         bucket.Total,
			Success:       bucket.Success,
			Fail:          bucket.Failed,
			AvgDurationMs: bucket.AvgDurationMs,
			DurationCount: bucket.DurationCount,
		})
	}

	return result, nil
}

// GetRequestTrendChart 保留旧 Wails 接口的进程内累计采样行为。
// Deprecated: 概览请使用 GetRequestTrendData 查询真实历史分桶。
func (a *App) GetRequestTrendChart(minutes int) []ChartDataPoint {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.monitoringMiddleware == nil {
		if a.logger != nil {
			a.logger.Warn("GetRequestTrendChart: monitoringMiddleware is nil")
		}
		return []ChartDataPoint{}
	}

	metrics := a.monitoringMiddleware.GetMetrics()
	if metrics == nil {
		if a.logger != nil {
			a.logger.Warn("GetRequestTrendChart: metrics is nil")
		}
		return []ChartDataPoint{}
	}

	// 直接在原始 *Metrics 上调用，而不是获取副本
	requestHistory := metrics.GetChartDataForRequestHistory(minutes)

	if a.logger != nil {
		a.logger.Info("📊 GetRequestTrendChart",
			"minutes", minutes,
			"history_points", len(requestHistory))
	}

	result := make([]ChartDataPoint, len(requestHistory))
	for i, point := range requestHistory {
		timestamp := formatAPITime(point.Timestamp)
		result[i] = ChartDataPoint{
			Time:      timestamp,
			Total:     point.Total,
			Success:   point.Successful,
			Fail:      point.Failed,
			Timestamp: timestamp,
		}
	}

	return result
}

// GetResponseTimeChart 获取响应时间图表数据
func (a *App) GetResponseTimeChart(minutes int) []ChartDataPoint {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.monitoringMiddleware == nil {
		return []ChartDataPoint{}
	}

	metrics := a.monitoringMiddleware.GetMetrics()
	responseHistory := metrics.GetChartDataForResponseTime(minutes)

	result := make([]ChartDataPoint, len(responseHistory))
	for i, point := range responseHistory {
		timestamp := formatAPITime(point.Timestamp)
		result[i] = ChartDataPoint{
			Time:      timestamp,
			Avg:       float64(point.AverageTime) / float64(time.Millisecond),
			Min:       float64(point.MinTime) / float64(time.Millisecond),
			Max:       float64(point.MaxTime) / float64(time.Millisecond),
			Timestamp: timestamp,
		}
	}

	return result
}

// GetConnectionActivityChart 获取连接活动图表数据
func (a *App) GetConnectionActivityChart(minutes int) []ChartDataPoint {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.monitoringMiddleware == nil {
		return []ChartDataPoint{}
	}

	// 连接活动图表使用请求历史数据
	metrics := a.monitoringMiddleware.GetMetrics()
	requestHistory := metrics.GetChartDataForRequestHistory(minutes)

	result := make([]ChartDataPoint, len(requestHistory))
	for i, point := range requestHistory {
		timestamp := formatAPITime(point.Timestamp)
		result[i] = ChartDataPoint{
			Time:      timestamp,
			Value:     point.Total, // 使用总请求数作为连接活动指标
			Timestamp: timestamp,
		}
	}

	return result
}

// ============================================================
// 端点最近连通性状态图表 API
// ============================================================

// EndpointHealthData 端点最近连通性状态数据结构
type EndpointHealthData struct {
	Healthy   int `json:"healthy"`
	Unhealthy int `json:"unhealthy"`
	Total     int `json:"total"`
}

// GetEndpointHealthChart 获取端点最近连通性状态图表数据
func (a *App) GetEndpointHealthChart() EndpointHealthData {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.endpointManager == nil {
		return EndpointHealthData{}
	}

	endpoints := a.endpointManager.GetAllEndpoints()

	healthyCount := 0
	unhealthyCount := 0

	for _, endpoint := range endpoints {
		status := endpoint.GetStatus()
		if status.NeverChecked {
			continue
		}
		if status.Healthy {
			healthyCount++
		} else {
			unhealthyCount++
		}
	}

	return EndpointHealthData{
		Healthy:   healthyCount,
		Unhealthy: unhealthyCount,
		Total:     len(endpoints),
	}
}

// ============================================================
// 端点成本图表 API
// ============================================================

// EndpointCostItem 端点成本数据项（用于前端图表）
type EndpointCostItem struct {
	Name          string  `json:"name"`
	RequestFamily string  `json:"request_family"`
	Tokens        int64   `json:"tokens"`
	Cost          float64 `json:"cost"`
}

// GetEndpointCosts 获取当日端点成本数据
func (a *App) GetEndpointCosts() []EndpointCostItem {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.usageTracker == nil {
		return []EndpointCostItem{}
	}

	policy, err := a.activeTimezonePolicy()
	if err != nil {
		if a.logger != nil {
			a.logger.Error("获取活动时区失败", "error", err)
		}
		return []EndpointCostItem{}
	}
	date := policy.BusinessDate(time.Now())

	// 查询端点成本数据
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	costs, err := a.usageTracker.GetEndpointCostsForDate(ctx, date)
	if err != nil {
		if a.logger != nil {
			a.logger.Error("获取端点成本数据失败", "error", err)
		}
		return []EndpointCostItem{}
	}

	// 转换为前端期望的格式
	result := make([]EndpointCostItem, len(costs))
	for i, cost := range costs {
		name := cost.UpstreamName
		if name == "" {
			name = "未知上游"
		}

		result[i] = EndpointCostItem{
			Name:          name,
			RequestFamily: cost.RequestFamily,
			Tokens:        cost.TotalTokens,
			Cost:          cost.TotalCostUSD,
		}
	}

	return result
}
