package main

import (
	"testing"
	"time"

	"cc-forwarder/internal/middleware"
	"cc-forwarder/internal/monitor"
	timezonepolicy "cc-forwarder/internal/timezone"
	"cc-forwarder/internal/tracking"
)

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
	if endpointResult.TotalRequests != 1 || len(endpointResult.Rows) != 1 || endpointResult.Rows[0].UpstreamType != "endpoint" {
		t.Fatalf("unexpected endpoint view: %+v", endpointResult)
	}

	accountResult, err := app.GetModelUsageStats(ModelUsageQueryParams{Period: params.Period, StartDate: params.StartDate, EndDate: params.EndDate, SourceView: "account"})
	if err != nil {
		t.Fatalf("GetModelUsageStats(account) failed: %v", err)
	}
	if accountResult.TotalRequests != 1 || len(accountResult.Rows) != 1 || accountResult.Rows[0].UpstreamType != "account" {
		t.Fatalf("unexpected account view: %+v", accountResult)
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
