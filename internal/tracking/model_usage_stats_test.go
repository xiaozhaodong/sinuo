package tracking

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	timezonepolicy "cc-forwarder/internal/timezone"
)

type seededModelUsageRequest struct {
	requestID     string
	modelName     string
	requestFamily string
	upstreamType  string
	upstreamName  string
	status        string
	inputTokens   int64
	outputTokens  int64
	cacheCreation int64
	cacheRead     int64
	cost          float64
	durationMs    int64
	startTime     time.Time
}

func newModelUsageTestTracker(t *testing.T) *UsageTracker {
	t.Helper()

	tempDir := t.TempDir()
	tracker, err := NewUsageTracker(&Config{
		Enabled:         true,
		DatabasePath:    filepath.Join(tempDir, "usage.db"),
		BufferSize:      100,
		BatchSize:       10,
		FlushInterval:   50 * time.Millisecond,
		MaxRetry:        3,
		CleanupInterval: 24 * time.Hour,
		RetentionDays:   30,
	})
	if err != nil {
		t.Fatalf("Failed to create usage tracker: %v", err)
	}
	t.Cleanup(func() { tracker.Close() })
	return tracker
}

func seedModelUsageRequests(t *testing.T, tracker *UsageTracker, rows []seededModelUsageRequest) {
	t.Helper()

	db := tracker.GetWriteDB()
	if db == nil {
		t.Fatal("expected write db")
	}
	for _, row := range rows {
		_, err := db.Exec(`INSERT INTO request_logs (
			request_id, start_time, status, request_family, upstream_type, upstream_name,
			model_name, input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens,
			total_cost_usd, duration_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			row.requestID,
			timezonepolicy.FormatStorage(row.startTime),
			row.status,
			row.requestFamily,
			row.upstreamType,
			row.upstreamName,
			row.modelName,
			row.inputTokens,
			row.outputTokens,
			row.cacheCreation,
			row.cacheRead,
			row.cost,
			row.durationMs,
		)
		if err != nil {
			t.Fatalf("failed to seed request %s: %v", row.requestID, err)
		}
	}
}

func modelUsageQueryRange(now time.Time) *QueryOptions {
	start := now.Add(-1 * time.Hour)
	end := now.Add(1 * time.Hour)
	return &QueryOptions{StartDate: &start, EndDate: &end}
}

func TestQueryModelUsageStatsWithHotPool_GroupsByModelFamilyAndUpstream(t *testing.T) {
	tracker := newModelUsageTestTracker(t)
	now := time.Now()

	seedModelUsageRequests(t, tracker, []seededModelUsageRequest{
		{requestID: "req-mu-1", modelName: "claude-sonnet-4", requestFamily: "claude", upstreamType: "endpoint", upstreamName: "duckcoding", status: "completed", inputTokens: 100, outputTokens: 50, cacheCreation: 10, cacheRead: 20, cost: 0.5, durationMs: 200, startTime: now.Add(-10 * time.Minute)},
		{requestID: "req-mu-2", modelName: "claude-sonnet-4", requestFamily: "claude", upstreamType: "endpoint", upstreamName: "duckcoding", status: "failed", inputTokens: 0, outputTokens: 0, cost: 0, durationMs: 0, startTime: now.Add(-9 * time.Minute)},
		{requestID: "req-mu-3", modelName: "gpt-5-codex", requestFamily: "codex", upstreamType: "account", upstreamName: "acct-1", status: "processing", inputTokens: 40, outputTokens: 10, cost: 0.25, durationMs: 400, startTime: now.Add(-8 * time.Minute)},
		{requestID: "req-mu-4", modelName: "gpt-5-codex", requestFamily: "codex", upstreamType: "account", upstreamName: "acct-2", status: "completed", inputTokens: 60, outputTokens: 20, cost: 0.75, durationMs: 600, startTime: now.Add(-7 * time.Minute)},
		{requestID: "req-mu-5", modelName: "", requestFamily: "other", upstreamType: "endpoint", upstreamName: "", status: "cancelled", inputTokens: 5, outputTokens: 5, cost: 0.1, durationMs: 0, startTime: now.Add(-6 * time.Minute)},
	})

	stats, err := tracker.QueryModelUsageStatsWithHotPool(context.Background(), modelUsageQueryRange(now), 8)
	if err != nil {
		t.Fatalf("QueryModelUsageStatsWithHotPool failed: %v", err)
	}

	if len(stats.Rows) != 3 {
		t.Fatalf("expected 3 model groups, got %d", len(stats.Rows))
	}
	if stats.Truncated {
		t.Fatal("expected no truncation with limit 8")
	}

	// 按请求数降序：claude-sonnet-4(2) > gpt-5-codex/account(2) > 未知模型(1)
	if stats.Rows[0].ModelName != "claude-sonnet-4" {
		t.Fatalf("expected claude-sonnet-4 first, got %q", stats.Rows[0].ModelName)
	}
	first := stats.Rows[0]
	if first.RequestFamily != "claude" || first.UpstreamType != "endpoint" {
		t.Fatalf("unexpected first row source: family=%q upstream=%q", first.RequestFamily, first.UpstreamType)
	}
	if first.RequestCount != 2 || first.SuccessCount != 1 || first.FailedCount != 1 {
		t.Fatalf("unexpected first row counts: %+v", first)
	}
	if first.SuccessRate != 50 {
		t.Fatalf("expected 50%% success rate, got %f", first.SuccessRate)
	}
	if first.TotalTokens != 180 {
		t.Fatalf("expected 180 tokens, got %d", first.TotalTokens)
	}
	if first.DurationCount != 1 || first.AvgDurationMs != 200 {
		t.Fatalf("expected avg duration 200ms from 1 sample, got %+v", first)
	}

	// 空模型名保留为独立分组，不静默丢弃
	unknown := stats.Rows[2]
	if unknown.ModelName != "" {
		t.Fatalf("expected empty model name to be preserved, got %q", unknown.ModelName)
	}
	if unknown.RequestCount != 1 {
		t.Fatalf("expected unknown model group count 1, got %d", unknown.RequestCount)
	}
	if unknown.DurationCount != 0 || unknown.AvgDurationMs != 0 {
		t.Fatalf("expected no duration sample for cancelled request, got %+v", unknown)
	}

	if stats.TotalRequests != 5 {
		t.Fatalf("expected 5 total requests, got %d", stats.TotalRequests)
	}
	if stats.SuccessRequests != 3 || stats.FailedRequests != 1 {
		t.Fatalf("unexpected summary counts: success=%d failed=%d", stats.SuccessRequests, stats.FailedRequests)
	}
	if stats.ClaudeCount != 2 || stats.CodexCount != 2 {
		t.Fatalf("unexpected family counts: claude=%d codex=%d", stats.ClaudeCount, stats.CodexCount)
	}
	if stats.InputTokens != 205 || stats.OutputTokens != 85 || stats.CacheCreationTokens != 10 || stats.CacheReadTokens != 20 {
		t.Fatalf("unexpected token breakdown: %+v", stats)
	}
	if stats.TotalTokens != 320 {
		t.Fatalf("expected 320 total tokens, got %d", stats.TotalTokens)
	}
	if stats.TotalCostUSD != 1.6 {
		t.Fatalf("expected 1.6 total cost, got %f", stats.TotalCostUSD)
	}
}

func TestQueryModelUsageStatsWithHotPool_TruncatesToTopN(t *testing.T) {
	tracker := newModelUsageTestTracker(t)
	now := time.Now()

	rows := make([]seededModelUsageRequest, 0, 5)
	for i := 0; i < 5; i++ {
		row := seededModelUsageRequest{
			requestID:     "req-topn-" + string(rune('a'+i)),
			modelName:     "model-" + string(rune('a'+i)),
			requestFamily: "claude",
			upstreamType:  "endpoint",
			status:        "completed",
			inputTokens:   10,
			startTime:     now.Add(-time.Duration(i+1) * time.Minute),
		}
		rows = append(rows, row)
	}
	// model-a 再补一条，确保排序稳定
	rows = append(rows, seededModelUsageRequest{
		requestID:     "req-topn-a-2",
		modelName:     "model-a",
		requestFamily: "claude",
		upstreamType:  "endpoint",
		status:        "completed",
		inputTokens:   10,
		startTime:     now.Add(-6 * time.Minute),
	})
	seedModelUsageRequests(t, tracker, rows)

	stats, err := tracker.QueryModelUsageStatsWithHotPool(context.Background(), modelUsageQueryRange(now), 2)
	if err != nil {
		t.Fatalf("QueryModelUsageStatsWithHotPool failed: %v", err)
	}

	if !stats.Truncated {
		t.Fatal("expected truncated flag for limit 2")
	}
	if len(stats.Rows) != 2 {
		t.Fatalf("expected 2 rows after truncation, got %d", len(stats.Rows))
	}
	if stats.Rows[0].ModelName != "model-a" || stats.Rows[0].RequestCount != 2 {
		t.Fatalf("expected model-a first with 2 requests, got %+v", stats.Rows[0])
	}
	// 汇总仍然按全部模型计算
	if stats.TotalRequests != 6 {
		t.Fatalf("expected summary to cover all models (6), got %d", stats.TotalRequests)
	}
}

func TestQueryModelUsageStatsWithHotPool_DeduplicatesOverlap(t *testing.T) {
	tracker := newModelUsageTestTracker(t)
	now := time.Now()

	seedModelUsageRequests(t, tracker, []seededModelUsageRequest{
		{requestID: "req-mu-overlap", modelName: "claude-sonnet-4", requestFamily: "claude", upstreamType: "endpoint", status: "completed", inputTokens: 100, outputTokens: 50, cost: 2.5, durationMs: 200, startTime: now.Add(-2 * time.Minute)},
	})

	tracker.RecordRequestStart("req-mu-overlap", "127.0.0.1", "stats-agent", "POST", "/v1/messages", false)
	status := "processing"
	modelName := "claude-sonnet-4"
	tracker.RecordRequestUpdate("req-mu-overlap", UpdateOptions{
		Status:    &status,
		ModelName: &modelName,
	})
	if err := tracker.hotPool.Update("req-mu-overlap", func(req *ActiveRequest) {
		req.InputTokens = 25
		req.OutputTokens = 5
		req.TotalCostUSD = 1.25
		d := int64(150)
		req.DurationMs = d
	}); err != nil {
		t.Fatalf("UpdateActiveRequest failed: %v", err)
	}

	stats, err := tracker.QueryModelUsageStatsWithHotPool(context.Background(), modelUsageQueryRange(now), 8)
	if err != nil {
		t.Fatalf("QueryModelUsageStatsWithHotPool failed: %v", err)
	}

	if len(stats.Rows) != 1 {
		t.Fatalf("expected single model group, got %d", len(stats.Rows))
	}
	row := stats.Rows[0]
	if row.RequestCount != 1 {
		t.Fatalf("expected overlapping request counted once, got %d", row.RequestCount)
	}
	if row.TotalTokens != 30 {
		t.Fatalf("expected hot-pool tokens to replace db tokens, got %d", row.TotalTokens)
	}
	if row.DurationCount != 1 || row.AvgDurationMs != 150 {
		t.Fatalf("expected hot-pool duration to win, got %+v", row)
	}
	if stats.TotalRequests != 1 || stats.TotalTokens != 30 {
		t.Fatalf("unexpected summary after dedupe: %+v", stats)
	}
	_ = tracker.hotPool.Remove("req-mu-overlap")
}

func TestQueryModelUsageStatsWithHotPool_KeepsDimensionsForHotPoolOnlyModel(t *testing.T) {
	tracker := newModelUsageTestTracker(t)
	now := time.Now()

	// 数据库里没有任何记录，请求只存在于热池：分组必须带上模型名 / 请求族 / 上游类型，
	// 否则模型表会显示空模型、空来源，Claude/Codex 汇总也会漏计。
	tracker.RecordRequestStart("req-hot-only", "127.0.0.1", "stats-agent", "POST", "/v1/responses", false)
	status := "processing"
	modelName := "gpt-6-astra"
	tracker.RecordRequestUpdate("req-hot-only", UpdateOptions{
		Status:    &status,
		ModelName: &modelName,
	})
	if err := tracker.hotPool.Update("req-hot-only", func(req *ActiveRequest) {
		req.UpstreamType = "account"
		req.UpstreamName = "acct-1"
		req.InputTokens = 40
		req.OutputTokens = 10
		req.TotalCostUSD = 0.5
		d := int64(900)
		req.DurationMs = d
	}); err != nil {
		t.Fatalf("UpdateActiveRequest failed: %v", err)
	}

	stats, err := tracker.QueryModelUsageStatsWithHotPool(context.Background(), modelUsageQueryRange(now), 8)
	if err != nil {
		t.Fatalf("QueryModelUsageStatsWithHotPool failed: %v", err)
	}

	if len(stats.Rows) != 1 {
		t.Fatalf("expected single hot-pool-only group, got %d", len(stats.Rows))
	}
	row := stats.Rows[0]
	if row.ModelName != "gpt-6-astra" {
		t.Fatalf("expected hot pool model name to survive, got %q", row.ModelName)
	}
	if row.RequestFamily != "codex" {
		t.Fatalf("expected codex family from request path, got %q", row.RequestFamily)
	}
	if row.UpstreamType != "account" {
		t.Fatalf("expected account upstream type, got %q", row.UpstreamType)
	}
	if row.RequestCount != 1 || row.TotalTokens != 50 || row.AvgDurationMs != 900 {
		t.Fatalf("unexpected hot-pool-only row: %+v", row)
	}
	if stats.ClaudeCount != 0 || stats.CodexCount != 1 {
		t.Fatalf("expected hot-pool-only request counted as codex, got claude=%d codex=%d", stats.ClaudeCount, stats.CodexCount)
	}
	_ = tracker.hotPool.Remove("req-hot-only")
}

func TestQueryModelUsageStatsWithHotPool_MatchesAggregatedStatsTotals(t *testing.T) {
	tracker := newModelUsageTestTracker(t)
	now := time.Now()

	seedModelUsageRequests(t, tracker, []seededModelUsageRequest{
		{requestID: "req-cross-1", modelName: "claude-sonnet-4", requestFamily: "claude", upstreamType: "endpoint", status: "completed", inputTokens: 100, outputTokens: 50, cacheCreation: 10, cacheRead: 20, cost: 0.5, durationMs: 200, startTime: now.Add(-10 * time.Minute)},
		{requestID: "req-cross-2", modelName: "gpt-5-codex", requestFamily: "codex", upstreamType: "account", status: "failed", inputTokens: 30, outputTokens: 10, cost: 0.25, durationMs: 300, startTime: now.Add(-9 * time.Minute)},
		{requestID: "req-cross-3", modelName: "claude-haiku-4-5", requestFamily: "claude", upstreamType: "endpoint", status: "processing", inputTokens: 5, outputTokens: 5, cost: 0.05, startTime: now.Add(-8 * time.Minute)},
	})

	opts := modelUsageQueryRange(now)
	modelStats, err := tracker.QueryModelUsageStatsWithHotPool(context.Background(), opts, 8)
	if err != nil {
		t.Fatalf("QueryModelUsageStatsWithHotPool failed: %v", err)
	}
	aggregated, err := tracker.QueryAggregatedRequestStatsWithHotPool(context.Background(), opts)
	if err != nil {
		t.Fatalf("QueryAggregatedRequestStatsWithHotPool failed: %v", err)
	}

	// 概览模型表与现有统计接口必须能对账，否则页面之间会出现两套数字
	if modelStats.TotalRequests != aggregated.TotalRequests {
		t.Fatalf("total requests mismatch: model=%d aggregated=%d", modelStats.TotalRequests, aggregated.TotalRequests)
	}
	if modelStats.SuccessRequests != aggregated.SuccessRequests || modelStats.FailedRequests != aggregated.FailedRequests {
		t.Fatalf("status counts mismatch: model=(%d,%d) aggregated=(%d,%d)",
			modelStats.SuccessRequests, modelStats.FailedRequests, aggregated.SuccessRequests, aggregated.FailedRequests)
	}
	if modelStats.TotalTokens != aggregated.TotalTokens {
		t.Fatalf("total tokens mismatch: model=%d aggregated=%d", modelStats.TotalTokens, aggregated.TotalTokens)
	}
	if modelStats.TotalCostUSD != aggregated.TotalCostUSD {
		t.Fatalf("total cost mismatch: model=%f aggregated=%f", modelStats.TotalCostUSD, aggregated.TotalCostUSD)
	}

	// 行级汇总也必须等于整体汇总
	var rowRequests, rowTokens int64
	var rowCost float64
	for _, row := range modelStats.Rows {
		rowRequests += row.RequestCount
		rowTokens += row.TotalTokens
		rowCost += row.TotalCostUSD
	}
	if rowRequests != modelStats.TotalRequests || rowTokens != modelStats.TotalTokens || rowCost != modelStats.TotalCostUSD {
		t.Fatalf("row sums do not match summary: rows=(%d,%d,%f) summary=(%d,%d,%f)",
			rowRequests, rowTokens, rowCost, modelStats.TotalRequests, modelStats.TotalTokens, modelStats.TotalCostUSD)
	}
}

func TestQueryModelUsageStatsWithHotPool_EmptyDatabaseReturnsEmptyRows(t *testing.T) {
	tracker := newModelUsageTestTracker(t)

	stats, err := tracker.QueryModelUsageStatsWithHotPool(context.Background(), modelUsageQueryRange(time.Now()), 8)
	if err != nil {
		t.Fatalf("QueryModelUsageStatsWithHotPool failed: %v", err)
	}
	if len(stats.Rows) != 0 {
		t.Fatalf("expected no rows for empty database, got %d", len(stats.Rows))
	}
	if stats.TotalRequests != 0 || stats.TotalTokens != 0 || stats.TotalCostUSD != 0 {
		t.Fatalf("expected zero summary for empty database, got %+v", stats)
	}
}
