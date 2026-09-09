package tracking

import (
	"context"
	"fmt"
	"time"

	timezonepolicy "cc-forwarder/internal/timezone"
)

// TrendGranularity 趋势桶粒度。
type TrendGranularity string

const (
	TrendGranularityMinute TrendGranularity = "minute"
	TrendGranularityHour   TrendGranularity = "hour"
	TrendGranularityDay    TrendGranularity = "day"
)

// trendBucketStep 返回分钟 / 小时粒度的桶宽；天粒度按业务日走 DayRange，返回 0。
func trendBucketStep(granularity TrendGranularity) time.Duration {
	switch granularity {
	case TrendGranularityMinute:
		return 5 * time.Minute
	case TrendGranularityHour:
		return time.Hour
	default:
		return 0
	}
}

// TimeBucket 表示一个半开时间区间 [Start, End)。
type TimeBucket struct {
	Label time.Time // 对齐业务时区的桶标签，独立于首尾裁剪后的查询边界
	Start time.Time
	End   time.Time
}

// RequestTrendBucket 单个时间桶内的请求统计。
type RequestTrendBucket struct {
	Start         time.Time
	Total         int64
	Success       int64
	Failed        int64
	AvgDurationMs float64
	DurationCount int64
}

// BuildTrendBuckets 生成覆盖 [start, end) 的时间桶，边界对齐配置时区：
//   - 天粒度按业务日切分，DST 日自然得到 23/25 小时；
//   - 分钟 / 小时粒度从起点所在业务日的 0 点按固定步长推进（跨 DST 时绝对时长
//     仍然正确，只有跳变的那一小时墙钟标签会跳号）。
//
// 首尾桶按 [start, end) 裁剪；超过上限立即报错，避免静默丢失窗口数据。
func BuildTrendBuckets(policy *timezonepolicy.Policy, start, end time.Time, granularity TrendGranularity, maxBuckets int) ([]TimeBucket, error) {
	if policy == nil {
		return nil, fmt.Errorf("timezone policy is nil")
	}
	if start.IsZero() || end.IsZero() {
		return nil, fmt.Errorf("trend range requires start and end")
	}
	if granularity != TrendGranularityMinute && granularity != TrendGranularityHour && granularity != TrendGranularityDay {
		return nil, fmt.Errorf("invalid trend granularity: %s", granularity)
	}
	if maxBuckets <= 0 {
		maxBuckets = 400
	}
	if !end.After(start) {
		return nil, nil
	}
	buckets := make([]TimeBucket, 0, 32)
	appendBucket := func(bucketStart, bucketEnd time.Time) error {
		if len(buckets) >= maxBuckets {
			return fmt.Errorf("trend range exceeds %d buckets", maxBuckets)
		}
		label := bucketStart
		if bucketStart.Before(start) {
			bucketStart = start
		}
		if bucketEnd.After(end) {
			bucketEnd = end
		}
		buckets = append(buckets, TimeBucket{Label: label, Start: bucketStart, End: bucketEnd})
		return nil
	}

	if granularity == TrendGranularityDay {
		startDate, err := time.Parse(time.DateOnly, policy.BusinessDate(start))
		if err != nil {
			return nil, err
		}
		lastDate, err := time.Parse(time.DateOnly, policy.BusinessDate(end.Add(-time.Nanosecond)))
		if err != nil {
			return nil, err
		}

		for date := startDate; !date.After(lastDate); date = date.AddDate(0, 0, 1) {
			dayStart, dayEnd, err := policy.DayRange(date.Format(time.DateOnly))
			if err != nil {
				return nil, err
			}
			if err := appendBucket(dayStart, dayEnd); err != nil {
				return nil, err
			}
		}
		return buckets, nil
	}

	step := trendBucketStep(granularity)
	dayStart, _, err := policy.DayRange(policy.BusinessDate(start))
	if err != nil {
		return nil, err
	}
	offset := start.Sub(dayStart)
	if offset < 0 {
		offset = 0
	}

	for cursor := dayStart.Add((offset / step) * step); cursor.Before(end); cursor = cursor.Add(step) {
		if err := appendBucket(cursor, cursor.Add(step)); err != nil {
			return nil, err
		}
	}
	return buckets, nil
}

// QueryRequestTrendBuckets 按给定时间桶统计请求数 / 成功 / 失败（数据库 + 热池）。
// 复用 QueryAggregatedRequestStatsWithHotPool，保证与概览 KPI、请求页同一口径。
func (ut *UsageTracker) QueryRequestTrendBuckets(ctx context.Context, opts *QueryOptions, buckets []TimeBucket) ([]RequestTrendBucket, error) {
	if ut.readDB == nil {
		return nil, fmt.Errorf("read database not initialized")
	}
	if opts == nil {
		opts = &QueryOptions{}
	}
	if len(buckets) == 0 {
		return nil, nil
	}

	result := make([]RequestTrendBucket, 0, len(buckets))
	for _, bucket := range buckets {
		bucketOpts := *opts
		start, end := bucket.Start, bucket.End
		if opts.StartDate != nil && start.Before(*opts.StartDate) {
			start = *opts.StartDate
		}
		if opts.EndDate != nil && end.After(*opts.EndDate) {
			end = *opts.EndDate
		}
		if !end.After(start) {
			continue
		}
		bucketOpts.StartDate = &start
		bucketOpts.EndDate = &end
		bucketOpts.Limit = 0
		bucketOpts.Offset = 0

		stats, err := ut.QueryAggregatedRequestStatsWithHotPool(ctx, &bucketOpts)
		if err != nil {
			return nil, fmt.Errorf("failed to query request trend bucket starting %s: %w", timezonepolicy.FormatStorage(start), err)
		}
		var avgDurationMs float64
		if stats.DurationCount > 0 {
			avgDurationMs = float64(stats.TotalDurationMs) / float64(stats.DurationCount)
		}
		result = append(result, RequestTrendBucket{
			Start:         bucket.Label,
			Total:         stats.TotalRequests,
			Success:       stats.SuccessRequests,
			Failed:        stats.FailedRequests,
			AvgDurationMs: avgDurationMs,
			DurationCount: stats.DurationCount,
		})
	}
	return result, nil
}
