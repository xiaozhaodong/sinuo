package tracking

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	timezonepolicy "sinuo/internal/timezone"
)

func TestClientInterruptionFiltersAndStatsAcrossDatabaseAndHotPool(t *testing.T) {
	tracker, err := NewUsageTracker(&Config{
		Enabled: true, DatabasePath: filepath.Join(t.TempDir(), "usage.db"),
		BufferSize: 100, BatchSize: 10, FlushInterval: time.Hour,
		CleanupInterval: 24 * time.Hour, RetentionDays: 30,
		HotPool: &HotPoolSettings{Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tracker.Close()
	now := time.Now()
	for _, row := range []struct{ name, status, source, reason string }{
		{"interrupted", "cancelled", RequestSourceCodexSubscriptionProxy, SubscriptionClientDisconnected},
		{"cancelled", "cancelled", RequestSourceCodexSubscriptionProxy, "subscription_cancelled"},
		{"legacy_source", "cancelled", RequestSourceLegacyProxy, SubscriptionClientDisconnected},
		{"legacy_unknown", "failed", RequestSourceCodexSubscriptionProxy, "subscription_interrupted"},
		{"upstream_error", "failed", RequestSourceCodexSubscriptionProxy, "subscription_response_failed"},
		{"completed", "completed", RequestSourceCodexSubscriptionProxy, ""},
	} {
		kind, upstream := RequestKindGeneration, "subscription"
		if row.source == RequestSourceLegacyProxy {
			kind, upstream = RequestKindLegacy, "endpoint"
		}
		for _, location := range []string{"db", "hot", "overlap"} {
			id := fmt.Sprintf("%s-%s", row.name, location)
			if location != "hot" {
				_, err := tracker.GetWriteDB().Exec(`INSERT INTO request_logs
					(request_id, start_time, status, request_family, request_source, request_kind, upstream_type, failure_reason)
					VALUES (?, ?, ?, 'codex', ?, ?, ?, ?)`,
					id, timezonepolicy.FormatStorage(now), row.status, row.source, kind, upstream, row.reason)
				if err != nil {
					t.Fatal(err)
				}
			}
			if location != "db" {
				err := tracker.RecordRequestStartAtWithDimensions(id, "127.0.0.1", "synthetic", "POST", "/v1/responses", RequestFamilyCodex, true, now, RequestDimensions{
					RequestSource: row.source, RequestKind: kind, UpstreamType: upstream,
				})
				if err != nil {
					t.Fatal(err)
				}
				if err := tracker.hotPool.Update(id, func(req *ActiveRequest) {
					req.Status, req.FailureReason = row.status, row.reason
				}); err != nil {
					t.Fatal(err)
				}
				defer tracker.hotPool.Remove(id)
			}
		}
	}
	for _, tc := range []struct {
		filter                              string
		total, failed, interrupted, success int64
	}{
		{"", 18, 6, 3, 3}, {"interrupted", 3, 0, 3, 0},
		{"cancelled", 6, 0, 0, 0}, {"failed", 6, 6, 0, 0}, {"completed", 3, 0, 0, 3},
	} {
		t.Run(tc.filter, func(t *testing.T) {
			opts := &QueryOptions{Status: tc.filter, UpstreamType: "all", Limit: 2}
			rows, count, err := tracker.QueryRequestDetailsWithHotPool(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			if count != tc.total || len(rows) != 2 {
				t.Fatalf("筛选和分页不一致: count=%d rows=%d want=%d", count, len(rows), tc.total)
			}
			for _, row := range rows {
				if tc.filter == "interrupted" && !isClientInterruption(row.Status, row.RequestSource, row.FailureReason) {
					t.Fatalf("其他来源或原因被误判为中断: %+v", row)
				}
			}
			stats, err := tracker.QueryAggregatedRequestStatsWithHotPool(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			if stats.TotalRequests != tc.total || stats.FailedRequests != tc.failed || stats.InterruptedRequests != tc.interrupted || stats.SuccessRequests != tc.success {
				t.Fatalf("数据库、热池及重叠去重的统计不一致: %+v", stats)
			}
		})
	}
}

func TestQueryAggregatedRequestStatsWithHotPool(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "usage.db")

	config := &Config{
		Enabled:         true,
		DatabasePath:    dbPath,
		BufferSize:      100,
		BatchSize:       10,
		FlushInterval:   50 * time.Millisecond,
		MaxRetry:        3,
		CleanupInterval: 24 * time.Hour,
		RetentionDays:   30,
	}

	tracker, err := NewUsageTracker(config)
	if err != nil {
		t.Fatalf("Failed to create usage tracker: %v", err)
	}
	defer tracker.Close()

	tracker.UpdatePricing(map[string]ModelPricing{
		"test-model": {
			Input:  1000000,
			Output: 1000000,
		},
	})

	requestID := "req-aggregated-completed"
	tracker.RecordRequestStart(requestID, "127.0.0.1", "stats-agent", "POST", "/v1/messages", false)
	tracker.RecordRequestSuccess(requestID, "test-model", &TokenUsage{
		InputTokens:  100,
		OutputTokens: 50,
	}, 200*time.Millisecond)

	activeID := "req-aggregated-hot"
	tracker.RecordRequestStart(activeID, "127.0.0.1", "stats-agent", "POST", "/v1/messages", false)
	if err := tracker.hotPool.Update(activeID, func(req *ActiveRequest) {
		req.Status = "processing"
		req.ModelName = "test-model"
		req.InputTokens = 25
		req.OutputTokens = 5
		req.TotalCostUSD = 1.25
		d := int64(150)
		req.DurationMs = d
	}); err != nil {
		t.Fatalf("UpdateActiveRequest failed: %v", err)
	}

	time.Sleep(150 * time.Millisecond)

	stats, err := tracker.QueryAggregatedRequestStatsWithHotPool(context.Background(), &QueryOptions{
		StartDate: func() *time.Time {
			t := time.Now().Add(-1 * time.Hour)
			return &t
		}(),
		EndDate: func() *time.Time {
			t := time.Now().Add(1 * time.Hour)
			return &t
		}(),
	})
	if err != nil {
		t.Fatalf("QueryAggregatedRequestStatsWithHotPool failed: %v", err)
	}

	if stats.TotalRequests != 2 {
		t.Fatalf("expected 2 total requests, got %d", stats.TotalRequests)
	}
	if stats.SuccessRequests != 2 {
		t.Fatalf("expected 2 success requests, got %d", stats.SuccessRequests)
	}
	if stats.FailedRequests != 0 {
		t.Fatalf("expected 0 failed requests, got %d", stats.FailedRequests)
	}
	if stats.TotalTokens != 180 {
		t.Fatalf("expected total tokens 180, got %d", stats.TotalTokens)
	}
	if stats.TotalCostUSD <= 0 {
		t.Fatalf("expected total cost to include priced records, got %f", stats.TotalCostUSD)
	}
	if stats.DurationCount == 0 {
		t.Fatal("expected duration count to be greater than zero")
	}
}

func TestQueryAggregatedRequestStatsWithHotPool_DeduplicatesOverlap(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "usage.db")

	config := &Config{
		Enabled:         true,
		DatabasePath:    dbPath,
		BufferSize:      100,
		BatchSize:       10,
		FlushInterval:   50 * time.Millisecond,
		MaxRetry:        3,
		CleanupInterval: 24 * time.Hour,
		RetentionDays:   30,
	}

	tracker, err := NewUsageTracker(config)
	if err != nil {
		t.Fatalf("Failed to create usage tracker: %v", err)
	}
	defer tracker.Close()

	now := time.Now()
	db := tracker.GetWriteDB()
	if db == nil {
		t.Fatal("expected write db")
	}

	_, err = db.Exec(`
		INSERT INTO request_logs (
			request_id, start_time, status, upstream_type,
			model_name, input_tokens, output_tokens, total_cost_usd, duration_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"req-overlap",
		timezonepolicy.FormatStorage(now.Add(-2*time.Minute)),
		"completed",
		"endpoint",
		"test-model",
		100,
		50,
		2.5,
		200,
	)
	if err != nil {
		t.Fatalf("failed to seed db request: %v", err)
	}

	tracker.RecordRequestStart("req-overlap", "127.0.0.1", "stats-agent", "POST", "/v1/messages", false)
	status := "processing"
	modelName := "test-model"
	tracker.RecordRequestUpdate("req-overlap", UpdateOptions{
		Status:    &status,
		ModelName: &modelName,
	})
	if err := tracker.hotPool.Update("req-overlap", func(req *ActiveRequest) {
		req.InputTokens = 25
		req.OutputTokens = 5
		req.TotalCostUSD = 1.25
		d := int64(150)
		req.DurationMs = d
	}); err != nil {
		t.Fatalf("UpdateActiveRequest failed: %v", err)
	}

	stats, err := tracker.QueryAggregatedRequestStatsWithHotPool(context.Background(), &QueryOptions{
		StartDate: func() *time.Time {
			t := now.Add(-1 * time.Hour)
			return &t
		}(),
		EndDate: func() *time.Time {
			t := now.Add(1 * time.Hour)
			return &t
		}(),
	})
	if err != nil {
		t.Fatalf("QueryAggregatedRequestStatsWithHotPool failed: %v", err)
	}

	if stats.TotalRequests != 1 {
		t.Fatalf("expected overlapping request to be counted once, got %d", stats.TotalRequests)
	}
	if stats.SuccessRequests != 1 {
		t.Fatalf("expected hot-pool status to win and count as success, got %d", stats.SuccessRequests)
	}
	if stats.TotalTokens != 30 {
		t.Fatalf("expected hot-pool tokens to replace db tokens, got %d", stats.TotalTokens)
	}
	if stats.DurationCount != 1 || stats.TotalDurationMs != 150 {
		t.Fatalf("expected hot-pool duration to replace db duration, got count=%d total=%d", stats.DurationCount, stats.TotalDurationMs)
	}
	_ = tracker.hotPool.Remove("req-overlap")
}
