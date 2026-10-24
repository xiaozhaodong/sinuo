package tracking

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	timezonepolicy "sinuo/internal/timezone"
	"sinuo/internal/tokenusage"
)

// DailyUsage 一个业务日桶的用量；Start 为业务日零点标签。
type DailyUsage struct {
	Start               time.Time
	RequestCount        int64
	InputTokens         int64
	OutputTokens        int64
	CacheCreationTokens int64
	CacheReadTokens     int64
	TotalTokens         int64
	TotalCostUSD        float64
}

// dailyUsageQuery 所有桶共用一条范围 JOIN；重叠扣减复用相同查询及库内时间归桶。
func dailyUsageQuery(buckets []TimeBucket, requestIDs []string) (string, []interface{}, error) {
	values := make([]string, len(buckets))
	args := make([]interface{}, 0, len(buckets)*3+5+len(requestIDs))
	for i, bucket := range buckets {
		values[i] = "(?, ?, ?)"
		args = append(args, i, timezonepolicy.FormatStorage(bucket.Start), timezonepolicy.FormatStorage(bucket.End))
	}
	generationCondition, generationArgs, err := RequestKindSQLFilter("r.request_kind", GenerationMetricRequestKinds())
	if err != nil {
		return "", nil, err
	}
	usageCondition, usageArgs, err := RequestKindSQLFilter("r.request_kind", UsageMetricRequestKinds())
	if err != nil {
		return "", nil, err
	}
	query := `WITH buckets(bucket_index, bucket_start, bucket_end) AS (VALUES ` + strings.Join(values, ", ") + `)
		SELECT b.bucket_index,
			COALESCE(SUM(CASE WHEN ` + generationCondition + ` THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_creation_tokens), 0), COALESCE(SUM(cache_read_tokens), 0),
			COALESCE(SUM(` + tokenusage.TotalTokensSQL + `), 0), COALESCE(SUM(total_cost_usd), 0.0)
		FROM buckets b JOIN request_logs r ON r.start_time >= b.bucket_start AND r.start_time < b.bucket_end
		WHERE ` + usageCondition
	args = append(args, generationArgs...)
	args = append(args, usageArgs...)
	if requestIDs != nil {
		query, args = appendRequestIDInFilter(query, args, requestIDs)
	}
	return query + " GROUP BY b.bucket_index", args, nil
}

func (ut *UsageTracker) queryDailyUsage(ctx context.Context, buckets []TimeBucket, requestIDs []string) ([]DailyUsage, error) {
	query, args, err := dailyUsageQuery(buckets, requestIDs)
	if err != nil {
		return nil, err
	}
	rows, err := ut.readDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query daily usage: %w", err)
	}
	defer rows.Close()
	result := make([]DailyUsage, len(buckets))
	for i, bucket := range buckets {
		result[i].Start = bucket.Label
	}
	for rows.Next() {
		var index int
		var usage DailyUsage
		if err := rows.Scan(&index, &usage.RequestCount, &usage.InputTokens, &usage.OutputTokens,
			&usage.CacheCreationTokens, &usage.CacheReadTokens, &usage.TotalTokens, &usage.TotalCostUSD); err != nil {
			return nil, fmt.Errorf("scan daily usage: %w", err)
		}
		usage.Start = buckets[index].Label
		result[index] = usage
	}
	return result, rows.Err()
}

func (usage *DailyUsage) subtract(overlap DailyUsage) {
	usage.RequestCount = max(usage.RequestCount-overlap.RequestCount, 0)
	usage.InputTokens = max(usage.InputTokens-overlap.InputTokens, 0)
	usage.OutputTokens = max(usage.OutputTokens-overlap.OutputTokens, 0)
	usage.CacheCreationTokens = max(usage.CacheCreationTokens-overlap.CacheCreationTokens, 0)
	usage.CacheReadTokens = max(usage.CacheReadTokens-overlap.CacheReadTokens, 0)
	usage.TotalTokens = max(usage.TotalTokens-overlap.TotalTokens, 0)
	usage.TotalCostUSD = math.Max(usage.TotalCostUSD-overlap.TotalCostUSD, 0)
}

// QueryDailyUsageWithHotPool 按天聚合数据库，并以去重后的热池快照替换库内重叠记录。
func (ut *UsageTracker) QueryDailyUsageWithHotPool(ctx context.Context, buckets []TimeBucket) ([]DailyUsage, error) {
	if ut.readDB == nil {
		return nil, fmt.Errorf("read database not initialized")
	}
	if len(buckets) == 0 {
		return []DailyUsage{}, nil
	}
	for i, bucket := range buckets {
		if bucket.Start.IsZero() || !bucket.End.After(bucket.Start) || (i > 0 && bucket.Start.Before(buckets[i-1].End)) {
			return nil, fmt.Errorf("invalid daily usage bucket at index %d", i)
		}
	}
	result, err := ut.queryDailyUsage(ctx, buckets, nil)
	if err != nil {
		return nil, err
	}
	start, end := buckets[0].Start, buckets[len(buckets)-1].End
	hotRequests := ut.getFilteredHotPoolRequests(&QueryOptions{
		StartDate: &start, EndDate: &end, RequestKinds: UsageMetricRequestKinds(),
	})
	if len(hotRequests) == 0 {
		return result, nil
	}
	seen := make(map[string]struct{}, len(hotRequests))
	unique := make([]RequestDetail, 0, len(hotRequests))
	ids := make([]string, 0, len(hotRequests))
	for _, req := range hotRequests {
		if _, exists := seen[req.RequestID]; exists {
			continue
		}
		seen[req.RequestID] = struct{}{}
		unique = append(unique, req)
		ids = append(ids, req.RequestID)
	}
	overlaps, err := ut.queryDailyUsage(ctx, buckets, ids)
	if err != nil {
		return nil, err
	}
	for i, overlap := range overlaps {
		result[i].subtract(overlap)
	}
	generationKinds := make(map[string]bool)
	for _, kind := range GenerationMetricRequestKinds() {
		generationKinds[kind] = true
	}
	for _, req := range unique {
		index := sort.Search(len(buckets), func(i int) bool { return buckets[i].End.After(req.StartTime) })
		if index == len(buckets) || req.StartTime.Before(buckets[index].Start) {
			continue
		}
		usage := &result[index]
		kind := req.RequestKind
		if kind == "" {
			kind = RequestKindLegacy
		}
		if generationKinds[kind] {
			usage.RequestCount++
		}
		usage.InputTokens += req.InputTokens
		usage.OutputTokens += req.OutputTokens
		usage.CacheCreationTokens += req.CacheCreationTokens
		usage.CacheReadTokens += req.CacheReadTokens
		usage.TotalTokens += tokenusage.TotalTokens(req.RequestFamily, req.Path, req.InputTokens, req.OutputTokens, req.CacheCreationTokens, req.CacheReadTokens)
		usage.TotalCostUSD += req.TotalCostUSD
	}
	return result, nil
}

// QueryEarliestRequestTime 空库返回 ok=false；时间显式转为文本，避免驱动自动解析。
func (ut *UsageTracker) QueryEarliestRequestTime(ctx context.Context) (time.Time, bool, error) {
	if ut.readDB == nil {
		return time.Time{}, false, fmt.Errorf("read database not initialized")
	}
	var earliest timezonepolicy.NullDBTime
	if err := ut.readDB.QueryRowContext(ctx, `SELECT CAST(MIN(start_time) AS TEXT) FROM request_logs`).Scan(&earliest); err != nil {
		return time.Time{}, false, fmt.Errorf("query earliest request time: %w", err)
	}
	return earliest.Time, earliest.Valid, nil
}
