package tracking

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	timezonepolicy "sinuo/internal/timezone"
	"sinuo/internal/tokenusage"
)

// 不按 request_kind 过滤：Token 与成本含独立压缩，与模型调用统计的用量口径一致。
// 进行中的请求还在热池、成本未结算，这里只读数据库。
const endpointCostsQuery = `SELECT
	request_family,
	COALESCE(upstream_type, '') as upstream_type,
	COALESCE(upstream_name, '') as upstream_name,
	COALESCE(upstream_id, 0) as upstream_id,
	COALESCE(SUM(` + tokenusage.TotalTokensSQL + `), 0) as total_tokens,
	COALESCE(SUM(total_cost_usd), 0.0) as total_cost_usd,
	COUNT(*) as request_count,
	SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END) as success_count,
	COALESCE(SUM(input_tokens), 0) as input_tokens,
	COALESCE(SUM(output_tokens), 0) as output_tokens,
	COALESCE(SUM(cache_creation_tokens), 0) as cache_creation_tokens,
	COALESCE(SUM(cache_read_tokens), 0) as cache_read_tokens,
	COALESCE(SUM(input_cost_usd), 0.0) as input_cost_usd,
	COALESCE(SUM(output_cost_usd), 0.0) as output_cost_usd,
	COALESCE(SUM(cache_creation_cost_usd), 0.0) as cache_creation_cost_usd,
	COALESCE(SUM(cache_read_cost_usd), 0.0) as cache_read_cost_usd
	FROM request_logs
	WHERE start_time >= ? AND start_time < ?
	GROUP BY request_family, upstream_type, upstream_name, upstream_id
	ORDER BY total_cost_usd DESC`

// GetEndpointCostsForDate 按业务日汇总各上游的 Token 与成本
func (ut *UsageTracker) GetEndpointCostsForDate(ctx context.Context, date string) ([]EndpointCostSummary, error) {
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return nil, fmt.Errorf("invalid date format, expected YYYY-MM-DD: %w", err)
	}
	startOfDay, endOfDay, err := ut.timezonePolicy.DayRange(date)
	if err != nil {
		return nil, err
	}
	return ut.GetEndpointCostsForRange(ctx, startOfDay, endOfDay)
}

// GetEndpointCostsForRange 按上游汇总 [start, end) 内的 Token 与成本（概览页上游成本排行）。
// 没有记录时返回空切片；查询失败返回错误，由调用方展示，不冒充「暂无数据」。
func (ut *UsageTracker) GetEndpointCostsForRange(ctx context.Context, start, end time.Time) ([]EndpointCostSummary, error) {
	if ut.readDB == nil {
		return nil, fmt.Errorf("read database not initialized")
	}
	if !end.After(start) {
		return nil, fmt.Errorf("end time must be after start time")
	}

	rows, err := ut.readDB.QueryContext(ctx, endpointCostsQuery, timezonepolicy.FormatStorage(start), timezonepolicy.FormatStorage(end))
	if err != nil {
		slog.Error("Failed to query endpoint costs", "error", err, "start_time", start, "end_time", end)
		return nil, fmt.Errorf("failed to query endpoint costs: %w", err)
	}
	defer rows.Close()

	costs := []EndpointCostSummary{}
	for rows.Next() {
		var cost EndpointCostSummary
		if err := rows.Scan(
			&cost.RequestFamily, &cost.UpstreamType, &cost.UpstreamName, &cost.UpstreamID, &cost.TotalTokens, &cost.TotalCostUSD,
			&cost.RequestCount, &cost.SuccessCount,
			&cost.InputTokens, &cost.OutputTokens,
			&cost.CacheCreationTokens, &cost.CacheReadTokens,
			&cost.InputCostUSD, &cost.OutputCostUSD,
			&cost.CacheCreationCostUSD, &cost.CacheReadCostUSD,
		); err != nil {
			return nil, fmt.Errorf("failed to scan endpoint cost: %w", err)
		}
		costs = append(costs, cost)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating endpoint cost rows: %w", err)
	}
	return costs, nil
}
