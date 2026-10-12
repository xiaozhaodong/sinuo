package main

import (
	"context"
	"math"
	"testing"
	"time"

	"cc-forwarder/internal/middleware"
	"cc-forwarder/internal/monitor"
	timezonepolicy "cc-forwarder/internal/timezone"
	"cc-forwarder/internal/tracking"
)

func TestCacheWriteUsageAPI_DeduplicatesTokensWithoutRepricingHistory(t *testing.T) {
	app := newUsageStatsTestApp(t)
	now := time.Now()
	for _, row := range []struct {
		id       string
		creation int64
		cost     float64
	}{
		{"cache-history", 0, 0.234567},
		{"cache-current", 2000, 0.417889},
	} {
		_, err := app.usageTracker.GetWriteDB().Exec(`INSERT INTO request_logs (
			request_id, start_time, status, request_family, path, upstream_type, model_name,
			input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens, total_cost_usd
		) VALUES (?, ?, 'completed', 'codex', '/v1/responses', 'account', 'gpt-6-astra', 272077, 1671, ?, 265159, ?)`,
			row.id, timezonepolicy.FormatStorage(now), row.creation, row.cost)
		if err != nil {
			t.Fatal(err)
		}
	}
	start, end := now.Add(-time.Hour), now.Add(time.Hour)
	stats, err := app.GetUsageStats(UsageStatsQueryParams{
		SourceView: "all", StartDate: start.Format(time.RFC3339Nano), EndDate: end.Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatal(err)
	}
	const wantCost = 0.234567 + 0.417889
	if stats.TotalTokens != 273748*2 || math.Abs(stats.TotalCostUSD-wantCost) > 1e-12 {
		t.Fatalf("请求统计改变了费用或重复累计缓存：%+v", stats)
	}
	cost, tokens, requests, err := app.queryStatsFromSingleTable(context.Background(), app.usageTracker.GetWriteDB(), "request_logs", start, end, "")
	if err != nil {
		t.Fatal(err)
	}
	if tokens != 273748*2 || requests != 2 || math.Abs(cost-wantCost) > 1e-12 {
		t.Fatalf("概览统计不一致：tokens=%d requests=%d cost=%f", tokens, requests, cost)
	}
	var historicalCreation int64
	var historicalCost float64
	if err := app.usageTracker.GetWriteDB().QueryRow(`SELECT cache_creation_tokens, total_cost_usd FROM request_logs WHERE request_id = ?`, "cache-history").Scan(&historicalCreation, &historicalCost); err != nil {
		t.Fatal(err)
	}
	if historicalCreation != 0 || historicalCost != 0.234567 {
		t.Fatal("历史记录被回填或重新计费")
	}
}

func TestCacheWriteUsageAPI_RuntimeTotalsKeepCacheAsDetails(t *testing.T) {
	app := newUsageStatsTestApp(t)
	usage := &monitor.TokenUsage{InputTokens: 272077, OutputTokens: 1671, CacheCreationTokens: 2000, CacheReadTokens: 265159}
	usage.SetProtocol("codex", "/v1/responses")
	app.monitoringMiddleware.GetMetrics().RecordTokenUsage("synthetic", "unknown", usage)
	got := app.GetTokenUsage()
	if got.InputTokens != 272077 || got.OutputTokens != 1671 || got.CacheCreationTokens != 2000 || got.CacheReadTokens != 265159 || got.TotalTokens != 273748 {
		t.Fatalf("运行时 API 不一致：%+v", got)
	}
	if stats := app.getUsageStatsFromRuntime("30d"); stats.TotalTokens != 273748 {
		t.Fatalf("运行时统计重复累计缓存：%+v", stats)
	}
}

func newUsageStatsTestApp(t *testing.T) *App {
	t.Helper()

	app := NewApp()
	app.monitoringMiddleware = middleware.NewMonitoringMiddleware(nil)

	tracker, err := tracking.NewUsageTracker(&tracking.Config{
		Enabled:         true,
		DatabasePath:    ":memory:",
		BufferSize:      16,
		BatchSize:       4,
		FlushInterval:   50 * time.Millisecond,
		MaxRetry:        3,
		CleanupInterval: 24 * time.Hour,
		RetentionDays:   30,
		HotPool: &tracking.HotPoolSettings{
			Enabled: true,
		},
	})
	if err != nil {
		t.Fatalf("failed to create usage tracker: %v", err)
	}
	t.Cleanup(func() {
		_ = tracker.Close()
	})

	app.usageTracker = tracker
	return app
}

func recordRuntimeStats(app *App, tokens *monitor.TokenUsage) {
	connID := app.monitoringMiddleware.RecordRequest("endpoint-a", "127.0.0.1", "codex-cli", "POST", "/v1/messages")
	app.monitoringMiddleware.RecordResponse(connID, 200, 250*time.Millisecond, 128, "endpoint-a")
	if tokens != nil {
		app.monitoringMiddleware.GetMetrics().RecordTokenUsage(connID, "endpoint-a", tokens)
	}
}

func TestGetUsageStats_ReturnsZeroForCurrentWindowWhenAggregateIsZero(t *testing.T) {
	app := newUsageStatsTestApp(t)
	recordRuntimeStats(app, &monitor.TokenUsage{
		InputTokens:  10,
		OutputTokens: 5,
	})

	now := time.Now()
	result, err := app.GetUsageStats(UsageStatsQueryParams{
		Period:     "30d",
		SourceView: "all",
		StartDate:  now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05-07:00"),
		EndDate:    now.Add(1 * time.Hour).Format("2006-01-02T15:04:05-07:00"),
	})
	if err != nil {
		t.Fatalf("GetUsageStats failed: %v", err)
	}

	if result.TotalRequests != 0 {
		t.Fatalf("expected zero aggregate to stay zero without runtime fallback, got %d requests", result.TotalRequests)
	}
	if result.TotalTokens != 0 {
		t.Fatalf("expected zero aggregate to stay zero without runtime fallback, got %d tokens", result.TotalTokens)
	}
}

func TestGetUsageStats_DoesNotFallbackToRuntimeForHistoricalWindow(t *testing.T) {
	app := newUsageStatsTestApp(t)
	recordRuntimeStats(app, &monitor.TokenUsage{
		InputTokens:  10,
		OutputTokens: 5,
	})

	now := time.Now()
	result, err := app.GetUsageStats(UsageStatsQueryParams{
		Period:     "30d",
		SourceView: "all",
		StartDate:  now.Add(-48 * time.Hour).Format("2006-01-02T15:04:05-07:00"),
		EndDate:    now.Add(-24 * time.Hour).Format("2006-01-02T15:04:05-07:00"),
	})
	if err != nil {
		t.Fatalf("GetUsageStats failed: %v", err)
	}

	if result.TotalRequests != 0 {
		t.Fatalf("expected historical zero result to stay zero, got %d", result.TotalRequests)
	}
	if result.TotalTokens != 0 {
		t.Fatalf("expected historical zero tokens, got %d", result.TotalTokens)
	}
}

func TestGetUsageStats_DoesNotFallbackToRuntimeWhenFiltered(t *testing.T) {
	app := newUsageStatsTestApp(t)
	recordRuntimeStats(app, &monitor.TokenUsage{
		InputTokens:  10,
		OutputTokens: 5,
	})

	now := time.Now()
	result, err := app.GetUsageStats(UsageStatsQueryParams{
		Period:     "30d",
		SourceView: "all",
		Model:      "gpt-5",
		StartDate:  now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05-07:00"),
		EndDate:    now.Add(1 * time.Hour).Format("2006-01-02T15:04:05-07:00"),
	})
	if err != nil {
		t.Fatalf("GetUsageStats failed: %v", err)
	}

	if result.TotalRequests != 0 {
		t.Fatalf("expected filtered zero result to stay zero, got %d", result.TotalRequests)
	}
	if result.TotalTokens != 0 {
		t.Fatalf("expected filtered zero tokens, got %d", result.TotalTokens)
	}
}

func TestGetModelUsageStats_ReturnsRowsAndSummary(t *testing.T) {
	app := newUsageStatsTestApp(t)
	db := app.usageTracker.GetWriteDB()
	if db == nil {
		t.Fatal("expected write db")
	}

	now := time.Now().UTC()
	rows := []struct {
		id       string
		model    string
		family   string
		upstream string
		status   string
		input    int64
		output   int64
		cost     float64
		duration int64
	}{
		{id: "mu-1", model: "claude-sonnet-4", family: "claude", upstream: "endpoint", status: "completed", input: 100, output: 50, cost: 0.5, duration: 200},
		{id: "mu-2", model: "claude-sonnet-4", family: "claude", upstream: "endpoint", status: "failed", input: 0, output: 0, cost: 0, duration: 0},
		{id: "mu-3", model: "gpt-5-codex", family: "codex", upstream: "account", status: "completed", input: 60, output: 20, cost: 0.75, duration: 600},
	}
	for _, row := range rows {
		if _, err := db.Exec(`INSERT INTO request_logs (
			request_id, start_time, status, request_family, upstream_type, upstream_name,
			model_name, input_tokens, output_tokens, total_cost_usd, duration_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			row.id,
			timezonepolicy.FormatStorage(now.Add(-5*time.Minute)),
			row.status,
			row.family,
			row.upstream,
			row.upstream+"-name",
			row.model,
			row.input,
			row.output,
			row.cost,
			row.duration,
		); err != nil {
			t.Fatalf("failed to seed %s: %v", row.id, err)
		}
	}

	result, err := app.GetModelUsageStats(ModelUsageQueryParams{
		Period:     "1d",
		SourceView: "all",
		StartDate:  now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05-07:00"),
		EndDate:    now.Add(1 * time.Hour).Format("2006-01-02T15:04:05-07:00"),
	})
	if err != nil {
		t.Fatalf("GetModelUsageStats failed: %v", err)
	}

	if len(result.Rows) != 2 {
		t.Fatalf("expected 2 model rows, got %d", len(result.Rows))
	}
	if result.Rows[0].ModelName != "claude-sonnet-4" || result.Rows[0].RequestCount != 2 {
		t.Fatalf("unexpected first row: %+v", result.Rows[0])
	}
	if result.Rows[0].SuccessRate != 50 {
		t.Fatalf("expected 50%% success rate, got %f", result.Rows[0].SuccessRate)
	}
	if result.TotalRequests != 3 || result.SuccessRequests != 2 || result.FailedRequests != 1 {
		t.Fatalf("unexpected summary counts: %+v", result)
	}
	if result.ClaudeCount != 2 || result.CodexCount != 1 {
		t.Fatalf("unexpected family counts: claude=%d codex=%d", result.ClaudeCount, result.CodexCount)
	}
	if result.TotalTokens != 230 || result.TotalCostUSD != 1.25 {
		t.Fatalf("unexpected token/cost summary: tokens=%d cost=%f", result.TotalTokens, result.TotalCostUSD)
	}
	if result.StartTime == "" || result.EndTime == "" || result.Period != "1d" {
		t.Fatalf("expected range metadata to be echoed back, got %+v", result)
	}
}

func TestGetModelUsageStats_SourceViewFiltersUpstreamType(t *testing.T) {
	app := newUsageStatsTestApp(t)
	db := app.usageTracker.GetWriteDB()
	now := time.Now().UTC()

	for _, row := range []struct{ id, upstream string }{
		{id: "src-endpoint", upstream: "endpoint"},
		{id: "src-account", upstream: "account"},
	} {
		if _, err := db.Exec(`INSERT INTO request_logs (
			request_id, start_time, status, request_family, upstream_type, upstream_name, model_name
		) VALUES (?, ?, 'completed', 'claude', ?, 'upstream', 'claude-sonnet-4')`,
			row.id, timezonepolicy.FormatStorage(now.Add(-5*time.Minute)), row.upstream); err != nil {
			t.Fatalf("failed to seed %s: %v", row.id, err)
		}
	}

	params := ModelUsageQueryParams{
		Period:    "1d",
		StartDate: now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05-07:00"),
		EndDate:   now.Add(1 * time.Hour).Format("2006-01-02T15:04:05-07:00"),
	}

	endpointResult, err := app.GetModelUsageStats(ModelUsageQueryParams{Period: params.Period, StartDate: params.StartDate, EndDate: params.EndDate, SourceView: "endpoint"})
	if err != nil {
		t.Fatalf("GetModelUsageStats(endpoint) failed: %v", err)
	}
	if endpointResult.TotalRequests != 1 || len(endpointResult.Rows) != 1 || endpointResult.Rows[0].RequestCount != 1 {
		t.Fatalf("unexpected endpoint view: %+v", endpointResult)
	}

	accountResult, err := app.GetModelUsageStats(ModelUsageQueryParams{Period: params.Period, StartDate: params.StartDate, EndDate: params.EndDate, SourceView: "account"})
	if err != nil {
		t.Fatalf("GetModelUsageStats(account) failed: %v", err)
	}
	if accountResult.TotalRequests != 1 || len(accountResult.Rows) != 1 || accountResult.Rows[0].RequestCount != 1 {
		t.Fatalf("unexpected account view: %+v", accountResult)
	}

	// 全部来源：同一模型走两种上游类型合并为一行
	allResult, err := app.GetModelUsageStats(ModelUsageQueryParams{Period: params.Period, StartDate: params.StartDate, EndDate: params.EndDate, SourceView: "all"})
	if err != nil {
		t.Fatalf("GetModelUsageStats(all) failed: %v", err)
	}
	if allResult.TotalRequests != 2 || len(allResult.Rows) != 1 || allResult.Rows[0].RequestCount != 2 {
		t.Fatalf("unexpected all view: %+v", allResult)
	}
}

func TestGetModelUsageStats_RejectsInvalidStartDate(t *testing.T) {
	app := newUsageStatsTestApp(t)

	if _, err := app.GetModelUsageStats(ModelUsageQueryParams{StartDate: "not-a-date"}); err == nil {
		t.Fatal("expected invalid start_date to return an error")
	}
}

func TestGetRequestsUsesConfiguredDSTDayRangeAndReturnsUTC(t *testing.T) {
	policy, err := timezonepolicy.New("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	tracker, err := tracking.NewUsageTrackerWithPolicy(&tracking.Config{
		Enabled: true, DatabasePath: ":memory:", BufferSize: 16, BatchSize: 4,
		FlushInterval: time.Hour, HotPool: &tracking.HotPoolSettings{Enabled: false},
	}, policy)
	if err != nil {
		t.Fatal(err)
	}
	defer tracker.Close()

	for _, row := range []struct{ id, start string }{
		{id: "dst-start", start: "2026-03-08T05:00:00.000000Z"},
		{id: "dst-last", start: "2026-03-09T03:59:59.999999Z"},
		{id: "dst-end-exclusive", start: "2026-03-09T04:00:00.000000Z"},
	} {
		if _, err := tracker.GetWriteDB().Exec(`INSERT INTO request_logs (
			request_id, start_time, status, request_family, upstream_type, upstream_name
		) VALUES (?, ?, 'completed', 'claude', 'endpoint', 'dst-endpoint')`, row.id, row.start); err != nil {
			t.Fatal(err)
		}
	}

	app := NewApp()
	app.timezonePolicy = policy
	app.usageTracker = tracker
	result, err := app.GetRequests(RequestQueryParams{
		StartDate: "2026-03-08T00:00", EndDate: "2026-03-09T00:00", Page: 1, PageSize: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 2 || len(result.Requests) != 2 {
		t.Fatalf("DST day requests = total:%d rows:%d", result.Total, len(result.Requests))
	}
	for _, request := range result.Requests {
		if request.RequestID == "dst-end-exclusive" {
			t.Fatal("half-open end boundary was included")
		}
		if request.Timestamp == "" || request.Timestamp[len(request.Timestamp)-1] != 'Z' {
			t.Fatalf("request timestamp is not UTC: %q", request.Timestamp)
		}
	}
}

// seededRequestLog 是 request_logs 播种行：先落库、再经 Wails API 读回，
// 覆盖来源/种类两个新维度在查询侧的口径。
type seededRequestLog struct {
	ID           string
	Kind         string // legacy / generation / compaction
	Source       string // legacy_proxy / codex_subscription_proxy
	UpstreamType string // endpoint / account / subscription
	UpstreamName string
	Model        string
	Status       string
	InputTokens  int64
	OutputTokens int64
	Cost         float64
}

func insertRequestLogRow(t *testing.T, app *App, row seededRequestLog) {
	t.Helper()
	if _, err := app.usageTracker.GetWriteDB().Exec(`INSERT INTO request_logs (
		request_id, start_time, status, request_family, request_source, request_kind,
		upstream_type, upstream_name, model_name,
		input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens, total_cost_usd
	) VALUES (?, ?, ?, 'codex', ?, ?, ?, ?, ?, ?, ?, 0, 0, ?)`,
		row.ID, timezonepolicy.FormatStorage(time.Now().UTC()), row.Status, row.Source, row.Kind,
		row.UpstreamType, row.UpstreamName, row.Model,
		row.InputTokens, row.OutputTokens, row.Cost); err != nil {
		t.Fatalf("failed to seed request log %s: %v", row.ID, err)
	}
}

func explicitUsageWindow() (string, string) {
	now := time.Now()
	return now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05-07:00"),
		now.Add(1 * time.Hour).Format("2006-01-02T15:04:05-07:00")
}

func TestGetUsageStatsSeparatesClientInterruption(t *testing.T) {
	app := newUsageStatsTestApp(t)
	for _, row := range []struct{ id, status, reason string }{
		{"done", "completed", ""},
		{"client-close", "cancelled", tracking.SubscriptionClientDisconnected},
		{"user-cancel", "cancelled", "subscription_cancelled"},
		{"upstream-error", "failed", "subscription_response_failed"},
		{"old-interruption", "failed", "subscription_interrupted"},
	} {
		insertRequestLogRow(t, app, seededRequestLog{
			ID: row.id, Kind: tracking.RequestKindGeneration, Source: tracking.RequestSourceCodexSubscriptionProxy,
			UpstreamType: "subscription", UpstreamName: "chatgpt-subscription", Model: "synthetic", Status: row.status,
		})
		if _, err := app.usageTracker.GetWriteDB().Exec("UPDATE request_logs SET failure_reason=? WHERE request_id=?", row.reason, row.id); err != nil {
			t.Fatal(err)
		}
	}
	start, end := explicitUsageWindow()
	for _, tc := range []struct {
		status                     string
		total, failed, interrupted int
		successRate                float64
	}{
		{"", 5, 2, 1, 20}, {"interrupted", 1, 0, 1, 0}, {"cancelled", 1, 0, 0, 0}, {"failed", 2, 2, 0, 0},
	} {
		result, err := app.GetUsageStats(UsageStatsQueryParams{SourceView: "subscription", StartDate: start, EndDate: end, Status: tc.status})
		if err != nil {
			t.Fatal(err)
		}
		if result.TotalRequests != tc.total || result.FailedCount != tc.failed || result.InterruptedCount != tc.interrupted || result.SuccessRate != tc.successRate {
			t.Fatalf("筛选 %q 的统计映射不符: %+v", tc.status, result)
		}
	}
}

func TestGetUsageStats_SplitsCountsAndUsageByRequestKind(t *testing.T) {
	app := newUsageStatsTestApp(t)
	insertRequestLogRow(t, app, seededRequestLog{
		ID: "gen-1", Kind: tracking.RequestKindGeneration, Source: tracking.RequestSourceCodexSubscriptionProxy,
		UpstreamType: "subscription", UpstreamName: "chatgpt-subscription", Model: "gpt-5",
		Status: "completed", InputTokens: 10, OutputTokens: 2, Cost: 0.3,
	})
	insertRequestLogRow(t, app, seededRequestLog{
		ID: "comp-1", Kind: tracking.RequestKindCompaction, Source: tracking.RequestSourceCodexSubscriptionProxy,
		UpstreamType: "subscription", UpstreamName: "chatgpt-subscription", Model: "gpt-5",
		Status: "completed", InputTokens: 100, OutputTokens: 5, Cost: 1.2,
	})

	startDate, endDate := explicitUsageWindow()
	result, err := app.GetUsageStats(UsageStatsQueryParams{Period: "1d", SourceView: "all", StartDate: startDate, EndDate: endDate})
	if err != nil {
		t.Fatalf("GetUsageStats failed: %v", err)
	}

	if result.TotalRequests != 1 || result.FailedCount != 0 {
		t.Fatalf("compaction must not enter the generation denominator: %+v", result)
	}
	if result.SuccessRate != 100 {
		t.Fatalf("success rate should be computed on generation only, got %f", result.SuccessRate)
	}
	if result.TotalTokens != 117 {
		t.Fatalf("token usage must include compaction, got %d", result.TotalTokens)
	}
	if result.TotalCostUSD != 1.5 {
		t.Fatalf("cost must include compaction, got %f", result.TotalCostUSD)
	}
}

func TestGetUsageStats_CompactionOnlyWindowKeepsUsage(t *testing.T) {
	app := newUsageStatsTestApp(t)
	insertRequestLogRow(t, app, seededRequestLog{
		ID: "comp-only", Kind: tracking.RequestKindCompaction, Source: tracking.RequestSourceCodexSubscriptionProxy,
		UpstreamType: "subscription", UpstreamName: "chatgpt-subscription", Model: "gpt-5",
		Status: "completed", InputTokens: 100, OutputTokens: 5, Cost: 1.2,
	})

	startDate, endDate := explicitUsageWindow()
	result, err := app.GetUsageStats(UsageStatsQueryParams{Period: "1d", SourceView: "all", StartDate: startDate, EndDate: endDate})
	if err != nil {
		t.Fatalf("GetUsageStats failed: %v", err)
	}

	if result.TotalRequests != 0 {
		t.Fatalf("compaction-only window must keep generation count at zero, got %d", result.TotalRequests)
	}
	if result.TotalTokens != 105 || result.TotalCostUSD != 1.2 {
		t.Fatalf("compaction-only window must keep reported usage, got tokens=%d cost=%f", result.TotalTokens, result.TotalCostUSD)
	}
}

func TestGetUsageStats_SubscriptionSourceView(t *testing.T) {
	app := newUsageStatsTestApp(t)
	insertRequestLogRow(t, app, seededRequestLog{
		ID: "sub-1", Kind: tracking.RequestKindGeneration, Source: tracking.RequestSourceCodexSubscriptionProxy,
		UpstreamType: "subscription", UpstreamName: "chatgpt-subscription", Model: "gpt-5",
		Status: "completed", InputTokens: 10, OutputTokens: 2, Cost: 0.3,
	})
	insertRequestLogRow(t, app, seededRequestLog{
		ID: "acc-1", Kind: tracking.RequestKindGeneration, Source: tracking.RequestSourceLegacyProxy,
		UpstreamType: "account", UpstreamName: "account-a", Model: "gpt-5",
		Status: "completed", InputTokens: 200, OutputTokens: 20, Cost: 4,
	})

	startDate, endDate := explicitUsageWindow()
	result, err := app.GetUsageStats(UsageStatsQueryParams{Period: "1d", SourceView: "subscription", StartDate: startDate, EndDate: endDate})
	if err != nil {
		t.Fatalf("GetUsageStats failed: %v", err)
	}

	if result.TotalRequests != 1 || result.TotalTokens != 12 || result.TotalCostUSD != 0.3 {
		t.Fatalf("subscription source view must only count direct-passthrough rows: %+v", result)
	}
}

func TestGetRequests_IncludeCompactionNarrowsToGeneration(t *testing.T) {
	app := newUsageStatsTestApp(t)
	insertRequestLogRow(t, app, seededRequestLog{
		ID: "gen-1", Kind: tracking.RequestKindGeneration, Source: tracking.RequestSourceCodexSubscriptionProxy,
		UpstreamType: "subscription", UpstreamName: "chatgpt-subscription", Model: "gpt-5", Status: "completed",
	})
	insertRequestLogRow(t, app, seededRequestLog{
		ID: "comp-1", Kind: tracking.RequestKindCompaction, Source: tracking.RequestSourceCodexSubscriptionProxy,
		UpstreamType: "subscription", UpstreamName: "chatgpt-subscription", Model: "gpt-5", Status: "completed",
	})

	startDate, endDate := explicitUsageWindow()
	base := RequestQueryParams{Page: 1, PageSize: 10, StartDate: startDate, EndDate: endDate, SourceView: "all"}

	all, err := app.GetRequests(base)
	if err != nil {
		t.Fatalf("GetRequests failed: %v", err)
	}
	if all.Total != 2 {
		t.Fatalf("default list must carry every record kind, got %d", all.Total)
	}

	generationOnly := base
	includeCompaction := false
	generationOnly.IncludeCompaction = &includeCompaction
	narrowed, err := app.GetRequests(generationOnly)
	if err != nil {
		t.Fatalf("GetRequests failed: %v", err)
	}
	if narrowed.Total != 1 || len(narrowed.Requests) != 1 || narrowed.Requests[0].RequestID != "gen-1" {
		t.Fatalf("include_compaction=false must narrow to generation records: %+v", narrowed)
	}
}

// 未知用量必须与真实零区分；分类与路径贯通桌面 DTO；无 HTTP 响应的记录保留 null。
func TestGetRequests_PreservesUnknownUsageAndClassification(t *testing.T) {
	app := newUsageStatsTestApp(t)
	started := timezonepolicy.FormatStorage(time.Now().UTC())
	// cache_creation_5m/1h_tokens 也要显式写 NULL：漏列会落到 DEFAULT 0，
	// 被 UsageKnown 的判定当成「已报告用量」。
	if _, err := app.usageTracker.GetWriteDB().Exec(`INSERT INTO request_logs (
		request_id, start_time, status, request_family, request_source, request_kind,
		upstream_type, upstream_name, model_name, method, path, http_status_code,
		input_tokens, output_tokens, cache_creation_tokens, cache_creation_5m_tokens,
		cache_creation_1h_tokens, cache_read_tokens, total_cost_usd
	) VALUES
		('ws-unknown', ?, 'failed', 'codex', 'codex_subscription_proxy', 'generation',
		 'subscription', 'chatgpt-subscription', 'gpt-5', 'GET', '/v1/responses', NULL,
		 NULL, NULL, NULL, NULL, NULL, NULL, NULL),
		('ws-zero', ?, 'completed', 'codex', 'codex_subscription_proxy', 'compaction',
		 'subscription', 'chatgpt-subscription', 'gpt-5', 'POST', '/v1/responses/compact', 200,
		 0, 0, 0, 0, 0, 0, 0)`, started, started); err != nil {
		t.Fatal(err)
	}

	startDate, endDate := explicitUsageWindow()
	result, err := app.GetRequests(RequestQueryParams{Page: 1, PageSize: 10, StartDate: startDate, EndDate: endDate, SourceView: "all"})
	if err != nil {
		t.Fatalf("GetRequests failed: %v", err)
	}
	byID := make(map[string]RequestRecord, len(result.Requests))
	for _, record := range result.Requests {
		byID[record.RequestID] = record
	}

	unknown, ok := byID["ws-unknown"]
	if !ok {
		t.Fatalf("missing ws-unknown record: %+v", result.Requests)
	}
	if unknown.UsageKnown || unknown.CostKnown {
		t.Fatalf("unknown usage must stay unknown: %+v", unknown)
	}
	if unknown.HTTPStatus != nil {
		t.Fatalf("missing HTTP response must stay null, got %v", unknown.HTTPStatus)
	}
	if unknown.Path != "/v1/responses" || unknown.Method != "GET" ||
		unknown.RequestKind != "generation" || unknown.RequestSource != "codex_subscription_proxy" {
		t.Fatalf("classification fields lost in DTO: %+v", unknown)
	}

	zero, ok := byID["ws-zero"]
	if !ok {
		t.Fatalf("missing ws-zero record: %+v", result.Requests)
	}
	if !zero.UsageKnown || !zero.CostKnown {
		t.Fatalf("explicit zero usage must stay known: %+v", zero)
	}
	if zero.HTTPStatus == nil || *zero.HTTPStatus != 200 {
		t.Fatalf("HTTP status must be preserved: %+v", zero.HTTPStatus)
	}
}

// 请求页把列表口径带进统计：显式下传时两项指标同口径；概览缺省保持按指标混合。
func TestGetUsageStats_IncludeCompactionMatchesListScope(t *testing.T) {
	app := newUsageStatsTestApp(t)
	insertRequestLogRow(t, app, seededRequestLog{
		ID: "gen-1", Kind: tracking.RequestKindGeneration, Source: tracking.RequestSourceCodexSubscriptionProxy,
		UpstreamType: "subscription", UpstreamName: "chatgpt-subscription", Model: "gpt-5",
		Status: "completed", InputTokens: 10, OutputTokens: 2, Cost: 0.3,
	})
	insertRequestLogRow(t, app, seededRequestLog{
		ID: "comp-1", Kind: tracking.RequestKindCompaction, Source: tracking.RequestSourceCodexSubscriptionProxy,
		UpstreamType: "subscription", UpstreamName: "chatgpt-subscription", Model: "gpt-5",
		Status: "completed", InputTokens: 100, OutputTokens: 5, Cost: 1.2,
	})

	startDate, endDate := explicitUsageWindow()
	includeCompaction := true
	all, err := app.GetUsageStats(UsageStatsQueryParams{
		Period: "1d", SourceView: "all", StartDate: startDate, EndDate: endDate, IncludeCompaction: &includeCompaction,
	})
	if err != nil {
		t.Fatalf("GetUsageStats(all) failed: %v", err)
	}
	if all.TotalRequests != 2 || all.TotalTokens != 117 || all.TotalCostUSD != 1.5 {
		t.Fatalf("list scope with compaction must count both records: %+v", all)
	}

	generationOnly := false
	narrowed, err := app.GetUsageStats(UsageStatsQueryParams{
		Period: "1d", SourceView: "all", StartDate: startDate, EndDate: endDate, IncludeCompaction: &generationOnly,
	})
	if err != nil {
		t.Fatalf("GetUsageStats(generation) failed: %v", err)
	}
	if narrowed.TotalRequests != 1 || narrowed.TotalTokens != 12 || narrowed.TotalCostUSD != 0.3 {
		t.Fatalf("generation-only scope must drop compaction from both metrics: %+v", narrowed)
	}

	mixed, err := app.GetUsageStats(UsageStatsQueryParams{Period: "1d", SourceView: "all", StartDate: startDate, EndDate: endDate})
	if err != nil {
		t.Fatalf("GetUsageStats(default) failed: %v", err)
	}
	if mixed.TotalRequests != 1 || mixed.TotalTokens != 117 || mixed.TotalCostUSD != 1.5 {
		t.Fatalf("overview default must keep per-metric scopes: %+v", mixed)
	}
}
