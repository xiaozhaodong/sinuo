package tracking

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	timezonepolicy "sinuo/internal/timezone"
	"sinuo/internal/tokenusage"
)

// 会话查询只传输轻量用量记录；完整详情仍通过原请求接口读取。
type RequestSessionRow struct {
	RequestFamily     string   `json:"request_family"`
	SessionID         string   `json:"session_id"`
	RequestCount      int64    `json:"request_count"`
	TotalCostUSD      *float64 `json:"total_cost_usd"`
	MissingCosts      int64    `json:"missing_costs"`
	CacheRatio        *float64 `json:"cache_ratio"`
	MissingCacheUsage int64    `json:"missing_cache_usage"`
	LastActivity      string   `json:"last_activity"`
}

type RequestSessionResult struct {
	Rows          []RequestSessionRow `json:"rows"`
	Total         int                 `json:"total"`
	TotalRequests int64               `json:"total_requests"`
	Ungrouped     int64               `json:"ungrouped"`
	KnownCostUSD  float64             `json:"known_cost_usd"`
	MissingCosts  int64               `json:"missing_costs"`
	Page          int                 `json:"page"`
	PageSize      int                 `json:"page_size"`
}

type RequestSessionPoint struct {
	RequestID string   `json:"request_id"`
	StartTime string   `json:"start_time"`
	FullInput *int64   `json:"full_input"`
	CacheRead *int64   `json:"cache_read"`
	Cost      *float64 `json:"cost"`
	Previous  *int64   `json:"previous"`
	Ordinal   int      `json:"ordinal"`
}

type RequestSessionTrend struct {
	Points          []RequestSessionPoint `json:"points"`
	Total           int                   `json:"total"`
	Page            int                   `json:"page"`
	PageSize        int                   `json:"page_size"`
	SelectedID      string                `json:"selected_id"`
	SelectedMissing bool                  `json:"selected_missing"`
}

// sessionSource 在取数据库快照前捕获热池（含归档中记录）。无论热池记录是否
// 匹配筛选，都排除数据库中的同 ID 旧快照，再应用同一个公共筛选器。
// JSON 是绑定参数，避免大热池触发 SQLite 变量数量上限。
func (ut *UsageTracker) sessionSource(opts *QueryOptions) (string, []interface{}, error) {
	if ut.readDB == nil {
		return "", nil, fmt.Errorf("read database not initialized")
	}
	if err := validateQueryRequestKinds(opts); err != nil {
		return "", nil, err
	}
	columns := []string{"request_id", "session_id", "request_family", "start_time", "path", "request_source", "request_kind", "endpoint_name", "upstream_type", "upstream_name", "model_name", "status", "failure_reason", "input_tokens", "cache_creation_tokens", "cache_creation_5m_tokens", "cache_creation_1h_tokens", "cache_read_tokens", "total_cost_usd"}
	hot := make([]map[string]interface{}, 0)
	for _, req := range ut.requestHotPoolSnapshot() {
		requestSource, err := normalizeRequestSource(req.RequestSource)
		if err != nil {
			return "", nil, err
		}
		requestKind, err := normalizeRequestKind(req.RequestKind)
		if err != nil {
			return "", nil, err
		}
		row := map[string]interface{}{
			"request_id": req.RequestID, "session_id": req.SessionID, "request_family": req.RequestFamily,
			"start_time": timezonepolicy.FormatStorage(req.StartTime), "path": req.Path,
			"request_source": requestSource, "request_kind": requestKind,
			"endpoint_name": req.EndpointName, "upstream_type": req.UpstreamType,
			"upstream_name": req.UpstreamName, "model_name": req.ModelName,
			"status": req.Status, "failure_reason": req.FailureReason,
		}
		if req.UsageKnown {
			row["input_tokens"], row["cache_creation_tokens"] = req.InputTokens, req.CacheCreationTokens
			row["cache_creation_5m_tokens"], row["cache_creation_1h_tokens"] = req.CacheCreation5mTokens, req.CacheCreation1hTokens
			row["cache_read_tokens"] = req.CacheReadTokens
		}
		// CostKnown 在热池也可能只代表“具备用量、可计算”，不能据此重算历史价。
		// 显式覆盖已是请求记录的一部分；其余成本从数据库读取，未结算则保留 NULL。
		if req.CostKnown && req.CostOverrideUSD != nil {
			row["total_cost_usd"] = *req.CostOverrideUSD
		}
		hot = append(hot, row)
	}
	encoded, err := json.Marshal(hot)
	if err != nil {
		return "", nil, fmt.Errorf("encode hot pool usage snapshot: %w", err)
	}
	extracts := make([]string, len(columns))
	for i, column := range columns {
		extracts[i] = "json_extract(value, '$." + column + "') AS " + column
	}
	query := `WITH hot_raw AS (SELECT ` + strings.Join(extracts, ", ") + ` FROM json_each(?)),
		hot AS (SELECT ` + strings.Join(columns[:len(columns)-1], ", ") + `,
		COALESCE(total_cost_usd, (SELECT total_cost_usd FROM request_logs WHERE request_id = hot_raw.request_id)) AS total_cost_usd FROM hot_raw),
		session_records AS (
		SELECT ` + strings.Join(columns, ", ") + `, 0 AS hot_order FROM request_logs
		WHERE request_id NOT IN (SELECT request_id FROM hot)
		UNION ALL SELECT *, 1 AS hot_order FROM hot),
		scoped AS (SELECT *, ` + tokenusage.FullInputSQL + ` AS full_input FROM session_records WHERE 1=1`
	args := []interface{}{string(encoded)}
	source := requestQuerySourceAll
	if opts != nil {
		source = resolveRequestQuerySource(opts.UpstreamType)
	}
	query, args = appendRequestQueryFilters(query, args, opts, source)
	return query + ") ", args, nil
}

// scoped 只引用一次，让 SQLite 在合并热池前将日期和来源筛选下推到索引。
// 空 ID 仍按请求族汇总，用于计算未归组请求数，但不计入会话数。
const sessionSummarySQL = `, summary_groups AS (
	SELECT request_family, NULLIF(session_id, '') AS session_id, COUNT(*) AS request_count,
	SUM(total_cost_usd) AS known_cost, COUNT(*) - COUNT(total_cost_usd) AS missing_costs
	FROM scoped GROUP BY request_family, NULLIF(session_id, ''))
	SELECT COALESCE(SUM(request_count), 0),
	COALESCE(SUM(CASE WHEN session_id IS NULL THEN request_count ELSE 0 END), 0),
	COALESCE(SUM(known_cost), 0), COALESCE(SUM(missing_costs), 0), COUNT(session_id)
	FROM summary_groups`

// QueryRequestSessions 先对完整筛选集聚合，再排序和分页。所有金额来自历史记录。
func (ut *UsageTracker) QueryRequestSessions(ctx context.Context, opts *QueryOptions, page, size int, sortBy, direction string) (*RequestSessionResult, error) {
	if page < 1 || size < 1 || size > 100 {
		return nil, fmt.Errorf("invalid session pagination")
	}
	orders := map[string]string{"cost": "total_cost_usd", "count": "request_count", "cache": "cache_ratio", "recent": "last_activity"}
	order, ok := orders[sortBy]
	if !ok || (direction != "asc" && direction != "desc") {
		return nil, fmt.Errorf("invalid session sort")
	}
	base, args, err := ut.sessionSource(opts)
	if err != nil {
		return nil, err
	}
	tx, err := ut.readDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	result := &RequestSessionResult{Rows: []RequestSessionRow{}, Page: page, PageSize: size}
	err = tx.QueryRowContext(ctx, base+sessionSummarySQL, args...).Scan(
		&result.TotalRequests, &result.Ungrouped, &result.KnownCostUSD, &result.MissingCosts, &result.Total)
	if err != nil {
		return nil, fmt.Errorf("query session totals: %w", err)
	}
	query := base + `, ranked AS (SELECT request_family, session_id, COUNT(*) AS request_count,
		SUM(total_cost_usd) AS total_cost_usd,
		COUNT(*) - COUNT(total_cost_usd) AS missing_costs,
		1.0 * SUM(CASE WHEN full_input IS NOT NULL AND cache_read_tokens IS NOT NULL THEN cache_read_tokens END)
		/ NULLIF(SUM(CASE WHEN full_input IS NOT NULL AND cache_read_tokens IS NOT NULL THEN full_input END), 0) AS cache_ratio,
		SUM(CASE WHEN full_input IS NULL OR cache_read_tokens IS NULL THEN 1 ELSE 0 END) AS missing_cache_usage,
		MAX(start_time) AS last_activity FROM scoped WHERE COALESCE(session_id, '') <> ''
		GROUP BY request_family, session_id)
		SELECT request_family, session_id, request_count, total_cost_usd, missing_costs, cache_ratio, missing_cache_usage, last_activity
		FROM ranked ORDER BY ` + order + ` IS NULL, ` + order + ` ` + direction + `, request_family, session_id LIMIT ? OFFSET ?`
	pageArgs := append(append([]interface{}{}, args...), size, (page-1)*size)
	rows, err := tx.QueryContext(ctx, query, pageArgs...)
	if err != nil {
		return nil, fmt.Errorf("query session ranking: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var r RequestSessionRow
		if err := rows.Scan(&r.RequestFamily, &r.SessionID, &r.RequestCount, &r.TotalCostUSD, &r.MissingCosts, &r.CacheRatio, &r.MissingCacheUsage, &r.LastActivity); err != nil {
			return nil, err
		}
		result.Rows = append(result.Rows, r)
	}
	return result, rows.Err()
}

const sessionTrendWindows = `, ordered AS (SELECT *,
	ROW_NUMBER() OVER (ORDER BY start_time, request_id) AS ordinal,
	LAG(full_input) OVER (ORDER BY start_time, request_id) AS previous FROM scoped)`

// QueryRequestSessionTrend 输出最多 100 个点；默认突增在整个会话范围内选择，
// 不受当前走势页影响。显式翻页允许查看旧请求，每个点带全局序号与前值。
func (ut *UsageTracker) QueryRequestSessionTrend(ctx context.Context, opts *QueryOptions, selectedID string, page int) (*RequestSessionTrend, error) {
	if opts == nil || opts.SessionID == "" || opts.RequestFamily == "" || opts.SessionMissing || page < 0 {
		return nil, fmt.Errorf("session trend requires family and session ID")
	}
	base, args, err := ut.sessionSource(opts)
	if err != nil {
		return nil, err
	}
	base += sessionTrendWindows
	tx, err := ut.readDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	result := &RequestSessionTrend{Points: []RequestSessionPoint{}, PageSize: 100, Page: 1}
	if err := tx.QueryRowContext(ctx, base+` SELECT COUNT(*) FROM ordered`, args...).Scan(&result.Total); err != nil {
		return nil, err
	}
	if result.Total == 0 {
		result.SelectedMissing = selectedID != ""
		return result, nil
	}
	var ordinal int
	anchorQuery := base + ` SELECT request_id, ordinal FROM ordered`
	anchorArgs := append([]interface{}{}, args...)
	if page > 0 {
		maxPage := (result.Total-1)/result.PageSize + 1
		if page > maxPage {
			page = maxPage
		}
		anchorQuery += ` WHERE ordinal = ?`
		anchorArgs = append(anchorArgs, (page-1)*result.PageSize+1)
	} else if selectedID != "" {
		anchorQuery += ` WHERE request_id = ?`
		anchorArgs = append(anchorArgs, selectedID)
	} else {
		anchorQuery += ` ORDER BY CASE WHEN previous > 0 AND full_input >= 2 * previous
		AND full_input - previous >= 10000 THEN full_input - previous ELSE -1 END DESC, start_time DESC, request_id DESC LIMIT 1`
	}
	err = tx.QueryRowContext(ctx, anchorQuery, anchorArgs...).Scan(&result.SelectedID, &ordinal)
	if err == sql.ErrNoRows {
		result.SelectedMissing = true
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	result.Page = (ordinal-1)/result.PageSize + 1
	pointArgs := append(append([]interface{}{}, args...), result.PageSize, (result.Page-1)*result.PageSize)
	rows, err := tx.QueryContext(ctx, base+` SELECT request_id, start_time, full_input, cache_read_tokens, total_cost_usd, previous, ordinal
		FROM ordered ORDER BY ordinal LIMIT ? OFFSET ?`, pointArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r RequestSessionPoint
		if err := rows.Scan(&r.RequestID, &r.StartTime, &r.FullInput, &r.CacheRead, &r.Cost, &r.Previous, &r.Ordinal); err != nil {
			return nil, err
		}
		result.Points = append(result.Points, r)
	}
	return result, rows.Err()
}

// LocateRequestPage 与正式请求表保持热池优先、时间倒序、ID 倒序的稳定顺序。
// 目标不在范围内时明确报错，不能仅保留详情快照假装完成定位。
func (ut *UsageTracker) LocateRequestPage(ctx context.Context, opts *QueryOptions, requestID string, size int) (int, error) {
	if requestID == "" || size < 1 || size > 100 {
		return 0, fmt.Errorf("invalid request location")
	}
	base, args, err := ut.sessionSource(opts)
	if err != nil {
		return 0, err
	}
	query := base + `, positions AS (SELECT request_id, ROW_NUMBER() OVER
		(ORDER BY hot_order DESC, ` + requestDetailsOrderBy + `) AS position FROM scoped)
		SELECT position FROM positions WHERE request_id = ?`
	var position int
	err = ut.readDB.QueryRowContext(ctx, query, append(args, requestID)...).Scan(&position)
	if err == sql.ErrNoRows {
		return 0, fmt.Errorf("目标请求已不在当前筛选范围内")
	}
	if err != nil {
		return 0, err
	}
	return (position-1)/size + 1, nil
}
