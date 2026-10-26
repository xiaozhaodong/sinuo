package tracking

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	timezonepolicy "sinuo/internal/timezone"
)

// QueryUsageHeatmapDaily 缓存前天及更早的库内日用量，昨天和今天现算。
// policy 与 now 必须和建桶时一致；热池补偿始终覆盖整段且不持久化。
func (ut *UsageTracker) QueryUsageHeatmapDaily(ctx context.Context, policy *timezonepolicy.Policy, buckets []TimeBucket, now time.Time) ([]DailyUsage, error) {
	if ut.readDB == nil || ut.writeDB == nil {
		return nil, fmt.Errorf("daily usage databases not initialized")
	}
	if policy == nil || policy.Name() == "" {
		return nil, fmt.Errorf("timezone policy snapshot is not initialized")
	}
	if err := validateDailyUsageBuckets(buckets); err != nil {
		return nil, err
	}
	if len(buckets) == 0 {
		return []DailyUsage{}, nil
	}
	today, err := time.Parse(time.DateOnly, policy.BusinessDate(now))
	if err != nil {
		return nil, err
	}
	cutoff, _, err := policy.DayRange(today.AddDate(0, 0, -1).Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	sealed := 0
	for i, bucket := range buckets {
		// 只接受完整业务日，保证缓存区间互不重叠，不能用局部桶污染日缓存。
		start, end, err := policy.DayRange(policy.BusinessDate(bucket.Start))
		if err != nil {
			return nil, err
		}
		if !bucket.Start.Equal(start) || !bucket.End.Equal(end) {
			return nil, fmt.Errorf("incomplete business day bucket at index %d", i)
		}
		if !bucket.End.After(cutoff) {
			sealed++
		}
	}

	result, missing, err := readUsageDailyCache(ctx, ut.readDB, policy, buckets[:sealed])
	if err != nil {
		return nil, err
	}
	if len(missing) > 0 {
		cached, err := ut.sealUsageDaily(ctx, policy, buckets[:sealed])
		if err != nil {
			var writeErr *usageDailyWriteError
			if !errors.As(err, &writeErr) {
				return nil, err
			}
			slog.Warn("热力图日缓存写入失败，回退现算缺失日", "error", err)
			// sealUsageDaily 返回前已经回滚并释放 writeMu/连接；单连接下不能提前回退。
			fresh, err := queryDailyUsage(ctx, ut.readDB, missing, nil)
			if err != nil {
				return nil, err
			}
			mergeUsageDaily(result, buckets[:sealed], fresh, missing)
		} else {
			result = cached
		}
	}
	live, err := queryDailyUsage(ctx, ut.readDB, buckets[sealed:], nil)
	if err != nil {
		return nil, err
	}
	return ut.compensateDailyUsageHotPool(ctx, buckets, append(result, live...))
}

// 仅缓存写失败允许回退；缓存读取、库内聚合失败必须向上传递。
type usageDailyWriteError struct{ error }

func (ut *UsageTracker) sealUsageDaily(ctx context.Context, policy *timezonepolicy.Policy, buckets []TimeBucket) ([]DailyUsage, error) {
	ut.writeMu.Lock()
	defer ut.writeMu.Unlock()
	tx, err := ut.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, &usageDailyWriteError{fmt.Errorf("begin daily usage cache: %w", err)}
	}
	defer tx.Rollback()
	// 等待写锁时其他请求可能已填表，或触发器已删掉首次读取时的命中日，故整段重读。
	result, missing, err := readUsageDailyCache(ctx, tx, policy, buckets)
	if err != nil {
		return nil, err
	}
	// immediate 事务从开始即持有写锁；查询必须走 tx，不能等待自己占用的 readDB 连接。
	fresh, err := queryDailyUsage(ctx, tx, missing, nil)
	if err != nil {
		return nil, err
	}
	for i, bucket := range missing {
		usage := fresh[i]
		_, err := tx.ExecContext(ctx, `INSERT INTO usage_daily (
			timezone_name, date, start_utc, end_utc, request_count, input_tokens, output_tokens,
			cache_creation_tokens, cache_read_tokens, total_tokens, total_cost_usd
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(timezone_name, date) DO UPDATE SET
			start_utc=excluded.start_utc, end_utc=excluded.end_utc, request_count=excluded.request_count,
			input_tokens=excluded.input_tokens, output_tokens=excluded.output_tokens,
			cache_creation_tokens=excluded.cache_creation_tokens, cache_read_tokens=excluded.cache_read_tokens,
			total_tokens=excluded.total_tokens, total_cost_usd=excluded.total_cost_usd`,
			policy.Name(), policy.BusinessDate(bucket.Start), timezonepolicy.FormatStorage(bucket.Start), timezonepolicy.FormatStorage(bucket.End),
			usage.RequestCount, usage.InputTokens, usage.OutputTokens, usage.CacheCreationTokens,
			usage.CacheReadTokens, usage.TotalTokens, usage.TotalCostUSD)
		if err != nil {
			return nil, &usageDailyWriteError{fmt.Errorf("store daily usage cache: %w", err)}
		}
	}
	// 与填表同一事务提交，任何已提交状态仅含一个时区，维护 MAX(start_utc) 失效定位不变式。
	if _, err := tx.ExecContext(ctx, `DELETE FROM usage_daily WHERE timezone_name <> ?`, policy.Name()); err != nil {
		return nil, &usageDailyWriteError{fmt.Errorf("remove other daily usage timezones: %w", err)}
	}
	if err := tx.Commit(); err != nil {
		return nil, &usageDailyWriteError{fmt.Errorf("commit daily usage cache: %w", err)}
	}
	mergeUsageDaily(result, buckets, fresh, missing)
	return result, nil
}

func readUsageDailyCache(ctx context.Context, querier usageSummaryQuerier, policy *timezonepolicy.Policy, buckets []TimeBucket) ([]DailyUsage, []TimeBucket, error) {
	result := make([]DailyUsage, len(buckets))
	if len(buckets) == 0 {
		return result, nil, nil
	}
	indices := make(map[string]int, len(buckets))
	for i, bucket := range buckets {
		indices[policy.BusinessDate(bucket.Start)] = i
		result[i].Start = bucket.Label
	}
	rows, err := querier.QueryContext(ctx, `SELECT date, start_utc, end_utc, request_count,
		input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens, total_tokens, total_cost_usd
		FROM usage_daily WHERE timezone_name = ? AND date >= ? AND date <= ?`,
		policy.Name(), policy.BusinessDate(buckets[0].Start), policy.BusinessDate(buckets[len(buckets)-1].Start))
	if err != nil {
		return nil, nil, fmt.Errorf("read daily usage cache: %w", err)
	}
	defer rows.Close()
	hit := make([]bool, len(buckets))
	for rows.Next() {
		var date, start, end string
		var usage DailyUsage
		if err := rows.Scan(&date, &start, &end, &usage.RequestCount, &usage.InputTokens, &usage.OutputTokens,
			&usage.CacheCreationTokens, &usage.CacheReadTokens, &usage.TotalTokens, &usage.TotalCostUSD); err != nil {
			return nil, nil, fmt.Errorf("scan daily usage cache: %w", err)
		}
		i, ok := indices[date]
		if !ok || start != timezonepolicy.FormatStorage(buckets[i].Start) || end != timezonepolicy.FormatStorage(buckets[i].End) {
			continue // tzdata 日界线变化时该日必须重新聚合。
		}
		usage.Start = buckets[i].Label
		result[i], hit[i] = usage, true
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	var missing []TimeBucket
	for i, bucket := range buckets {
		if !hit[i] {
			missing = append(missing, bucket)
		}
	}
	return result, missing, nil
}

// 两段均按时间排序；缺失日允许不连续。
func mergeUsageDaily(result []DailyUsage, buckets []TimeBucket, fresh []DailyUsage, missing []TimeBucket) {
	next := 0
	for i, bucket := range buckets {
		if next < len(missing) && bucket.Start.Equal(missing[next].Start) {
			result[i] = fresh[next]
			next++
		}
	}
}
