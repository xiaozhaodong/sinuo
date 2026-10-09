package tracking

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"cc-forwarder/internal/tokenusage"
)

// ============================================================
// 模型维度聚合（概览页「模型调用情况」）
// ============================================================

// DefaultModelUsageLimit 模型聚合的默认 Top N。
const DefaultModelUsageLimit = 8

// ModelUsageRow 表示按「模型 + 请求族 + 上游类型」聚合的请求统计。
// 成功率口径与 AggregatedRequestStats 保持一致：completed/processing 计入成功分子，
// 分母为全部请求，避免概览与请求页出现两套成功率。
type ModelUsageRow struct {
	ModelName     string  `json:"model_name"`
	RequestFamily string  `json:"request_family"`
	UpstreamType  string  `json:"upstream_type"`
	RequestCount  int64   `json:"request_count"`
	SuccessCount  int64   `json:"success_count"`
	FailedCount   int64   `json:"failed_count"`
	SuccessRate   float64 `json:"success_rate"`
	AvgDurationMs float64 `json:"avg_duration_ms"`
	DurationCount int64   `json:"duration_count"`
	TotalTokens   int64   `json:"total_tokens"`
	TotalCostUSD  float64 `json:"total_cost_usd"`
}

// ModelUsageStats 模型维度聚合结果：Top N 模型行 + 全量汇总。
// 汇总字段始终按全部模型计算，不受 Rows 截断影响。
type ModelUsageStats struct {
	Rows                []ModelUsageRow `json:"rows"`
	Truncated           bool            `json:"truncated"`
	TotalRequests       int64           `json:"total_requests"`
	SuccessRequests     int64           `json:"success_requests"`
	FailedRequests      int64           `json:"failed_requests"`
	TotalTokens         int64           `json:"total_tokens"`
	InputTokens         int64           `json:"input_tokens"`
	OutputTokens        int64           `json:"output_tokens"`
	CacheCreationTokens int64           `json:"cache_creation_tokens"`
	CacheReadTokens     int64           `json:"cache_read_tokens"`
	TotalCostUSD        float64         `json:"total_cost_usd"`
	ClaudeCount         int64           `json:"claude_count"`
	CodexCount          int64           `json:"codex_count"`
}

type modelUsageKey struct {
	modelName     string
	requestFamily string
	upstreamType  string
}

type modelUsageGroup struct {
	modelName           string
	requestFamily       string
	upstreamType        string
	requestCount        int64
	successCount        int64
	failedCount         int64
	inputTokens         int64
	outputTokens        int64
	cacheCreationTokens int64
	cacheReadTokens     int64
	totalTokens         int64
	totalCostUSD        float64
	totalDurationMs     int64
	durationCount       int64
}

func (g modelUsageGroup) key() modelUsageKey {
	return modelUsageKey{modelName: g.modelName, requestFamily: g.requestFamily, upstreamType: g.upstreamType}
}

func (g *modelUsageGroup) add(other modelUsageGroup) {
	g.requestCount += other.requestCount
	g.successCount += other.successCount
	g.failedCount += other.failedCount
	g.inputTokens += other.inputTokens
	g.outputTokens += other.outputTokens
	g.cacheCreationTokens += other.cacheCreationTokens
	g.cacheReadTokens += other.cacheReadTokens
	g.totalTokens += other.totalTokens
	g.totalCostUSD += other.totalCostUSD
	g.totalDurationMs += other.totalDurationMs
	g.durationCount += other.durationCount
}

func (g *modelUsageGroup) subtract(other modelUsageGroup) {
	g.requestCount -= other.requestCount
	g.successCount -= other.successCount
	g.failedCount -= other.failedCount
	g.inputTokens -= other.inputTokens
	g.outputTokens -= other.outputTokens
	g.cacheCreationTokens -= other.cacheCreationTokens
	g.cacheReadTokens -= other.cacheReadTokens
	g.totalTokens -= other.totalTokens
	g.totalCostUSD -= other.totalCostUSD
	g.totalDurationMs -= other.totalDurationMs
	g.durationCount -= other.durationCount

	if g.requestCount < 0 {
		g.requestCount = 0
	}
	if g.successCount < 0 {
		g.successCount = 0
	}
	if g.failedCount < 0 {
		g.failedCount = 0
	}
	if g.inputTokens < 0 {
		g.inputTokens = 0
	}
	if g.outputTokens < 0 {
		g.outputTokens = 0
	}
	if g.cacheCreationTokens < 0 {
		g.cacheCreationTokens = 0
	}
	if g.cacheReadTokens < 0 {
		g.cacheReadTokens = 0
	}
	if g.totalTokens < 0 {
		g.totalTokens = 0
	}
	if g.totalCostUSD < 0 {
		g.totalCostUSD = 0
	}
	if g.totalDurationMs < 0 {
		g.totalDurationMs = 0
	}
	if g.durationCount < 0 {
		g.durationCount = 0
	}
}

func (g modelUsageGroup) toRow() ModelUsageRow {
	row := ModelUsageRow{
		ModelName:     g.modelName,
		RequestFamily: g.requestFamily,
		UpstreamType:  g.upstreamType,
		RequestCount:  g.requestCount,
		SuccessCount:  g.successCount,
		FailedCount:   g.failedCount,
		TotalTokens:   g.totalTokens,
		TotalCostUSD:  g.totalCostUSD,
		DurationCount: g.durationCount,
	}
	if g.requestCount > 0 {
		row.SuccessRate = float64(g.successCount) / float64(g.requestCount) * 100
	}
	if g.durationCount > 0 {
		row.AvgDurationMs = float64(g.totalDurationMs) / float64(g.durationCount)
	}
	return row
}

const modelUsageStatsSelectBase = `SELECT
	COALESCE(model_name, '') as model_name,
	COALESCE(request_family, 'other') as request_family,
	COALESCE(upstream_type, 'endpoint') as upstream_type,
	COUNT(*) as request_count,
	COALESCE(SUM(CASE WHEN status IN ('completed', 'processing') THEN 1 ELSE 0 END), 0) as success_count,
	COALESCE(SUM(CASE WHEN status IN (` + FailedRequestStatusesSQLList + `) THEN 1 ELSE 0 END), 0) as failed_count,
	COALESCE(SUM(input_tokens), 0) as input_tokens,
	COALESCE(SUM(output_tokens), 0) as output_tokens,
	COALESCE(SUM(cache_creation_tokens), 0) as cache_creation_tokens,
	COALESCE(SUM(cache_read_tokens), 0) as cache_read_tokens,
	COALESCE(SUM(` + tokenusage.TotalTokensSQL + `), 0) as total_tokens,
	COALESCE(SUM(total_cost_usd), 0.0) as total_cost_usd,
	COALESCE(SUM(CASE WHEN duration_ms IS NOT NULL AND duration_ms > 0 THEN duration_ms ELSE 0 END), 0) as total_duration_ms,
	COALESCE(SUM(CASE WHEN duration_ms IS NOT NULL AND duration_ms > 0 THEN 1 ELSE 0 END), 0) as duration_count
	FROM request_logs WHERE 1=1`

const modelUsageStatsGroupByClause = ` GROUP BY COALESCE(model_name, ''), COALESCE(request_family, 'other'), COALESCE(upstream_type, 'endpoint')`

// normalizeModelUsageUpstreamType 与 SQL 侧 COALESCE(upstream_type, 'endpoint') 对齐。
func normalizeModelUsageUpstreamType(value string) string {
	if strings.TrimSpace(value) == "" {
		return "endpoint"
	}
	return value
}

func scanModelUsageGroups(rows *sql.Rows) ([]modelUsageGroup, error) {
	var groups []modelUsageGroup
	for rows.Next() {
		var g modelUsageGroup
		if err := rows.Scan(
			&g.modelName, &g.requestFamily, &g.upstreamType,
			&g.requestCount, &g.successCount, &g.failedCount,
			&g.inputTokens, &g.outputTokens, &g.cacheCreationTokens, &g.cacheReadTokens,
			&g.totalTokens,
			&g.totalCostUSD, &g.totalDurationMs, &g.durationCount,
		); err != nil {
			return nil, fmt.Errorf("failed to scan model usage group: %w", err)
		}
		groups = append(groups, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating model usage groups: %w", err)
	}
	return groups, nil
}

func (ut *UsageTracker) queryModelUsageGroupsBySource(ctx context.Context, opts *QueryOptions, source requestQuerySource) ([]modelUsageGroup, error) {
	query := modelUsageStatsSelectBase
	args := make([]interface{}, 0, 16)
	query, args = appendRequestQueryFilters(query, args, opts, source)
	query += modelUsageStatsGroupByClause

	rows, err := ut.readDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query model usage groups: %w", err)
	}
	defer rows.Close()

	return scanModelUsageGroups(rows)
}

func (ut *UsageTracker) queryModelUsageGroupsBySourceAndRequestIDs(ctx context.Context, opts *QueryOptions, source requestQuerySource, requestIDs []string) ([]modelUsageGroup, error) {
	if len(requestIDs) == 0 {
		return nil, nil
	}

	query := modelUsageStatsSelectBase
	args := make([]interface{}, 0, 16+len(requestIDs))
	query, args = appendRequestQueryFilters(query, args, opts, source)
	query, args = appendRequestIDInFilter(query, args, requestIDs)
	query += modelUsageStatsGroupByClause

	rows, err := ut.readDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query model usage groups by request IDs: %w", err)
	}
	defer rows.Close()

	return scanModelUsageGroups(rows)
}

func modelUsageGroupsFromRequestDetails(details []RequestDetail) map[modelUsageKey]modelUsageGroup {
	groups := make(map[modelUsageKey]modelUsageGroup, len(details))
	for _, req := range details {
		key := modelUsageKey{
			modelName:     req.ModelName,
			requestFamily: normalizeRequestFamily(req.RequestFamily),
			upstreamType:  normalizeModelUsageUpstreamType(req.UpstreamType),
		}
		group := groups[key]
		group.modelName = key.modelName
		group.requestFamily = key.requestFamily
		group.upstreamType = key.upstreamType
		group.requestCount++
		switch {
		case req.Status == "completed" || req.Status == "processing":
			group.successCount++
		case isFailedRequestStatus(req.Status):
			group.failedCount++
		}
		group.inputTokens += req.InputTokens
		group.outputTokens += req.OutputTokens
		group.cacheCreationTokens += req.CacheCreationTokens
		group.cacheReadTokens += req.CacheReadTokens
		group.totalTokens += tokenusage.TotalTokens(req.RequestFamily, req.Path, req.InputTokens, req.OutputTokens, req.CacheCreationTokens, req.CacheReadTokens)
		group.totalCostUSD += req.TotalCostUSD
		if req.DurationMs != nil && *req.DurationMs > 0 {
			group.totalDurationMs += *req.DurationMs
			group.durationCount++
		}
		groups[key] = group
	}
	return groups
}

// QueryModelUsageStatsWithHotPool 查询模型维度聚合统计（数据库 + 热池）。
// 计数、成功率与生成耗时使用生成口径（legacy + generation）；Token 与成本使用含独立
// 压缩的用量口径（legacy + generation + compaction）。压缩专用模型可以调用次数为 0、
// 用量或成本非零，必须保留为 0 次调用行，不能丢行或清零。
// limit <= 0 时使用 DefaultModelUsageLimit；Rows 只保留请求数最高的 limit 个模型，
// Truncated 标记是否截断，汇总字段始终按全部模型计算。
func (ut *UsageTracker) QueryModelUsageStatsWithHotPool(ctx context.Context, opts *QueryOptions, limit int) (*ModelUsageStats, error) {
	if ut.readDB == nil {
		return nil, fmt.Errorf("read database not initialized")
	}
	if opts == nil {
		opts = &QueryOptions{}
	}
	if limit <= 0 {
		limit = DefaultModelUsageLimit
	}

	generationRows, generationStats, err := ut.aggregateModelUsageByKinds(ctx, opts, GenerationMetricRequestKinds())
	if err != nil {
		return nil, err
	}
	usageRows, usageStats, err := ut.aggregateModelUsageByKinds(ctx, opts, UsageMetricRequestKinds())
	if err != nil {
		return nil, err
	}

	// 合并两个口径：行键取并集，计数/成功率/耗时来自生成口径，Token/成本来自用量口径。
	rowsByKey := make(map[modelUsageKey]*ModelUsageRow, len(generationRows)+len(usageRows))
	for _, row := range generationRows {
		copied := row
		rowsByKey[modelUsageKey{modelName: row.ModelName, requestFamily: row.RequestFamily, upstreamType: row.UpstreamType}] = &copied
	}
	for _, row := range usageRows {
		key := modelUsageKey{modelName: row.ModelName, requestFamily: row.RequestFamily, upstreamType: row.UpstreamType}
		existing, ok := rowsByKey[key]
		if !ok {
			// 只出现在用量口径的模型（压缩专用）：保留 0 次调用行。
			rowsByKey[key] = &ModelUsageRow{
				ModelName:     row.ModelName,
				RequestFamily: row.RequestFamily,
				UpstreamType:  row.UpstreamType,
				TotalTokens:   row.TotalTokens,
				TotalCostUSD:  row.TotalCostUSD,
			}
			continue
		}
		existing.TotalTokens = row.TotalTokens
		existing.TotalCostUSD = row.TotalCostUSD
	}

	allRows := make([]ModelUsageRow, 0, len(rowsByKey))
	for _, row := range rowsByKey {
		allRows = append(allRows, *row)
	}

	sort.Slice(allRows, func(i, j int) bool {
		if allRows[i].RequestCount != allRows[j].RequestCount {
			return allRows[i].RequestCount > allRows[j].RequestCount
		}
		if allRows[i].ModelName != allRows[j].ModelName {
			return allRows[i].ModelName < allRows[j].ModelName
		}
		if allRows[i].RequestFamily != allRows[j].RequestFamily {
			return allRows[i].RequestFamily < allRows[j].RequestFamily
		}
		return allRows[i].UpstreamType < allRows[j].UpstreamType
	})

	stats := &ModelUsageStats{
		TotalRequests:       generationStats.TotalRequests,
		SuccessRequests:     generationStats.SuccessRequests,
		FailedRequests:      generationStats.FailedRequests,
		TotalTokens:         usageStats.TotalTokens,
		InputTokens:         usageStats.InputTokens,
		OutputTokens:        usageStats.OutputTokens,
		CacheCreationTokens: usageStats.CacheCreationTokens,
		CacheReadTokens:     usageStats.CacheReadTokens,
		TotalCostUSD:        usageStats.TotalCostUSD,
		ClaudeCount:         generationStats.ClaudeCount,
		CodexCount:          generationStats.CodexCount,
	}

	if len(allRows) > limit {
		stats.Truncated = true
		allRows = allRows[:limit]
	}
	stats.Rows = allRows

	return stats, nil
}

// aggregateModelUsageByKinds 执行单一指标口径（kinds 白名单）的模型聚合：
// 数据库 + 热池去重补偿，返回全部模型行（未排序、未截断）与该口径的汇总。
// opts 必须非 nil，由 QueryModelUsageStatsWithHotPool 统一构造。
func (ut *UsageTracker) aggregateModelUsageByKinds(ctx context.Context, opts *QueryOptions, kinds []string) ([]ModelUsageRow, ModelUsageStats, error) {
	kindOpts := *opts
	kindOpts.RequestKinds = kinds

	source := resolveRequestQuerySource(kindOpts.UpstreamType)
	dbGroups, err := ut.queryModelUsageGroupsBySource(ctx, &kindOpts, source)
	if err != nil {
		return nil, ModelUsageStats{}, err
	}

	groupsByKey := make(map[modelUsageKey]modelUsageGroup, len(dbGroups))
	for _, group := range dbGroups {
		groupsByKey[group.key()] = group
	}

	hotPoolRequests := ut.getFilteredHotPoolRequests(&kindOpts)
	if len(hotPoolRequests) > 0 {
		hotPoolIDs := make(map[string]struct{}, len(hotPoolRequests))
		uniqueHotPoolRequests := make([]RequestDetail, 0, len(hotPoolRequests))
		hotPoolRequestIDs := make([]string, 0, len(hotPoolRequests))
		for _, req := range hotPoolRequests {
			if _, exists := hotPoolIDs[req.RequestID]; exists {
				continue
			}
			hotPoolIDs[req.RequestID] = struct{}{}
			uniqueHotPoolRequests = append(uniqueHotPoolRequests, req)
			hotPoolRequestIDs = append(hotPoolRequestIDs, req.RequestID)
		}

		overlapGroups, err := ut.queryModelUsageGroupsBySourceAndRequestIDs(ctx, &kindOpts, source, hotPoolRequestIDs)
		if err != nil {
			return nil, ModelUsageStats{}, err
		}
		for _, overlap := range overlapGroups {
			key := overlap.key()
			if existing, ok := groupsByKey[key]; ok {
				existing.subtract(overlap)
				groupsByKey[key] = existing
			}
		}

		for key, hotPoolGroup := range modelUsageGroupsFromRequestDetails(uniqueHotPoolRequests) {
			// 只存在于热池、数据库里还没有记录的模型：零值 group 的
			// modelName/requestFamily/upstreamType 都是空串，直接 add 会丢掉维度，
			// 所以未命中时整体以热池分组初始化。
			existing, ok := groupsByKey[key]
			if !ok {
				groupsByKey[key] = hotPoolGroup
				continue
			}
			existing.add(hotPoolGroup)
			groupsByKey[key] = existing
		}
	}

	allRows := make([]ModelUsageRow, 0, len(groupsByKey))
	stats := &ModelUsageStats{}
	for _, group := range groupsByKey {
		// 热池请求在库里的快照可能落在另一个键下（起始记录还没写入 model_name），
		// subtract 后该键只剩一个空壳；不过滤会输出「未知模型 · 0 请求」的幽灵行。
		if group.requestCount <= 0 {
			continue
		}
		row := group.toRow()
		allRows = append(allRows, row)
		stats.TotalRequests += row.RequestCount
		stats.SuccessRequests += row.SuccessCount
		stats.FailedRequests += row.FailedCount
		stats.TotalTokens += row.TotalTokens
		stats.InputTokens += group.inputTokens
		stats.OutputTokens += group.outputTokens
		stats.CacheCreationTokens += group.cacheCreationTokens
		stats.CacheReadTokens += group.cacheReadTokens
		stats.TotalCostUSD += row.TotalCostUSD
		switch row.RequestFamily {
		case RequestFamilyClaude:
			stats.ClaudeCount += row.RequestCount
		case RequestFamilyCodex:
			stats.CodexCount += row.RequestCount
		}
	}

	return allRows, *stats, nil
}
