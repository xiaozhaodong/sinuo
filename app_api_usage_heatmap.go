package main

import (
	"context"
	"time"

	timezonepolicy "sinuo/internal/timezone"
	"sinuo/internal/tracking"
)

// UsageHeatmapQueryParams 周数范围为 1..53，越界时按 53 周。
type UsageHeatmapQueryParams struct {
	Weeks int `json:"weeks"`
}

// UsageHeatmapDay 一个业务日的用量及请求页交接边界。
type UsageHeatmapDay struct {
	Date                string  `json:"date"`
	StartTime           string  `json:"start_time"`
	EndTime             string  `json:"end_time"`
	RequestCount        int64   `json:"request_count"`
	TotalTokens         int64   `json:"total_tokens"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	TotalCostUSD        float64 `json:"total_cost_usd"`
}

// UsageHeatmapData 从第一列周一到今天的连续业务日快照。
type UsageHeatmapData struct {
	Timezone      string            `json:"timezone"`
	StartDate     string            `json:"start_date"`
	EndDate       string            `json:"end_date"`
	DataStartDate string            `json:"data_start_date"`
	Days          []UsageHeatmapDay `json:"days"`
}

func usageHeatmapRange(policy *timezonepolicy.Policy, now time.Time, weeks int) (string, string, []tracking.TimeBucket, error) {
	if weeks <= 0 || weeks > 53 {
		weeks = 53
	}
	today := policy.BusinessDate(now)
	date, err := time.Parse(time.DateOnly, today)
	if err != nil {
		return "", "", nil, err
	}
	offset := (int(date.Weekday()) + 6) % 7
	startDate := date.AddDate(0, 0, -offset-7*(weeks-1)).Format(time.DateOnly)
	start, _, err := policy.DayRange(startDate)
	if err != nil {
		return "", "", nil, err
	}
	_, end, err := policy.DayRange(today)
	if err != nil {
		return "", "", nil, err
	}
	buckets, err := tracking.BuildTrendBuckets(policy, start, end, tracking.TrendGranularityDay, trendMaxBuckets)
	return startDate, today, buckets, err
}

// GetUsageHeatmap 查询近一年用量；独立于概览短时间范围快照，不写入 summary 缓存。
func (a *App) GetUsageHeatmap(params UsageHeatmapQueryParams) (UsageHeatmapData, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	result := UsageHeatmapData{Days: []UsageHeatmapDay{}}
	policy, err := a.activeTimezonePolicy()
	if err != nil {
		return result, err
	}
	now := time.Now()
	startDate, today, buckets, err := usageHeatmapRange(policy, now, params.Weeks)
	if err != nil {
		return result, err
	}
	result.Timezone, result.StartDate, result.EndDate = policy.Name(), startDate, today
	if a.usageTracker == nil {
		return result, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	usage, err := a.usageTracker.QueryUsageHeatmapDaily(ctx, policy, buckets, now)
	if err != nil {
		return result, err
	}
	earliest, ok, err := a.usageTracker.QueryEarliestRequestTime(ctx)
	if err != nil {
		return result, err
	}
	if ok {
		result.DataStartDate = policy.BusinessDate(earliest)
	} else if a.usageTracker.GetHotPoolRequestCount(&tracking.QueryOptions{}) > 0 {
		result.DataStartDate = today
	}
	result.Days = make([]UsageHeatmapDay, len(buckets))
	for i, bucket := range buckets {
		day := usage[i]
		result.Days[i] = UsageHeatmapDay{
			Date: policy.BusinessDate(bucket.Label), StartTime: formatAPITime(bucket.Start), EndTime: formatAPITime(bucket.End),
			RequestCount: day.RequestCount, TotalTokens: day.TotalTokens,
			InputTokens: day.InputTokens, OutputTokens: day.OutputTokens,
			CacheCreationTokens: day.CacheCreationTokens, CacheReadTokens: day.CacheReadTokens,
			TotalCostUSD: day.TotalCostUSD,
		}
	}
	return result, nil
}
