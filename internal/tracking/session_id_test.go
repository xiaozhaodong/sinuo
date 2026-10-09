package tracking

import (
	"context"
	"encoding/csv"
	"math"
	"strings"
	"testing"
	"time"

	timezonepolicy "cc-forwarder/internal/timezone"
)

func TestSessionIDPersistsWithAndWithoutHotPool(t *testing.T) {
	for _, hotPool := range []bool{true, false} {
		t.Run(map[bool]string{true: "热池", false: "直写"}[hotPool], func(t *testing.T) {
			tracker := newRequestLifecycleTestTracker(t, hotPool, 10*time.Millisecond)
			const requestID, sessionID = "req-session-persist", "synthetic-session"
			err := tracker.RecordRequestStartAtWithDimensions(requestID, "127.0.0.1", "test", "POST", "/v1/responses", RequestFamilyCodex, true, time.Now(), RequestDimensions{
				SessionID: sessionID, RequestModel: "gpt-test", RequestReasoning: "speed:priority",
			})
			if err != nil {
				t.Fatal(err)
			}
			if hotPool {
				req, ok := tracker.GetActiveRequest(requestID)
				if !ok || req.SessionID != sessionID || tracker.ActiveRequestToDetail(req).SessionID != sessionID {
					t.Fatal("热池副本丢失会话")
				}
			} else {
				waitModelConsistencyRow(t, tracker, requestID, func(row modelConsistencyRow) bool { return row.Status == "pending" })
			}
			tracker.RecordRequestSuccessWithDiagnostics(requestID, "gpt-test", &TokenUsage{InputTokens: 3, OutputTokens: 2}, time.Millisecond, "", &ResponseDiagnostics{ResponseReasoning: "speed:default"})
			waitModelConsistencyRow(t, tracker, requestID, func(row modelConsistencyRow) bool { return row.Status == "completed" })
			rows, err := tracker.QueryRequestDetails(context.Background(), &QueryOptions{SessionID: sessionID})
			if err != nil || len(rows) != 1 || rows[0].SessionID != sessionID || rows[0].RequestReasoning != "speed:priority" || rows[0].ResponseReasoning != "speed:default" {
				t.Fatalf("落库读取不一致: rows=%+v err=%v", rows, err)
			}
			assertSessionCSV(t, tracker, sessionID)
		})
	}
}

func assertSessionCSV(t *testing.T, tracker *UsageTracker, sessionID string) {
	t.Helper()
	data, err := tracker.ExportToCSV(context.Background(), time.Now().Add(-time.Hour), time.Now().Add(time.Hour), "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(strings.NewReader(string(data))).ReadAll()
	if err != nil || len(rows) != 2 {
		t.Fatalf("CSV rows=%v err=%v", rows, err)
	}
	for i, column := range rows[0] {
		if column == "session_id" {
			if rows[1][i] != sessionID {
				t.Fatal("CSV 会话列与行未对齐")
			}
			return
		}
	}
	t.Fatal("CSV 缺少会话列")
}

func TestSessionFilterListCountAndAggregateCompensation(t *testing.T) {
	tracker := newRequestLifecycleTestTracker(t, true, time.Hour)
	now := time.Now()
	seedSessionStats(t, tracker, now)
	for _, tc := range []struct {
		session       string
		count, tokens int64
		cost          float64
	}{
		{"session-a", 3, 69, 0.6}, {"session-b", 2, 121, 1.1},
		{"", 5, 190, 1.7}, {"missing", 0, 0, 0}, {"' OR 1=1 --", 0, 0, 0},
	} {
		start, end := now.Add(-time.Hour), now.Add(time.Hour)
		opts := &QueryOptions{SessionID: tc.session, StartDate: &start, EndDate: &end, RequestFamily: RequestFamilyCodex, UpstreamType: "account", Limit: 100}
		rows, count, err := tracker.QueryRequestDetailsWithHotPool(context.Background(), opts)
		if err != nil || count != tc.count || int64(len(rows)) != tc.count {
			t.Fatalf("列表与总数不一致: count=%d rows=%d err=%v", count, len(rows), err)
		}
		for _, row := range rows {
			if tc.session != "" && row.SessionID != tc.session {
				t.Fatal("列表混入其他会话")
			}
		}
		opts.Limit = 1
		_, paginatedCount, err := tracker.QueryRequestDetailsWithHotPool(context.Background(), opts)
		if err != nil || paginatedCount != tc.count {
			t.Fatalf("分页改变总数: %d err=%v", paginatedCount, err)
		}
		stats, err := tracker.QueryAggregatedRequestStatsWithHotPool(context.Background(), opts)
		if err != nil || stats.TotalRequests != tc.count || stats.TotalTokens != tc.tokens || math.Abs(stats.TotalCostUSD-tc.cost) > 1e-9 {
			t.Fatalf("库内扣减与热池补偿不一致: stats=%+v err=%v", stats, err)
		}
	}
}

func seedSessionStats(t *testing.T, tracker *UsageTracker, now time.Time) {
	t.Helper()
	for _, row := range []struct {
		id, session   string
		input, output int64
		cost          float64
	}{
		{"db-a", "session-a", 10, 2, 0.1}, {"overlap-a", "session-a", 100, 100, 10}, {"db-b", "session-b", 50, 5, 0.5},
	} {
		_, err := tracker.GetWriteDB().Exec(`INSERT INTO request_logs (request_id, session_id, start_time, status, request_family, path, upstream_type, input_tokens, output_tokens, total_cost_usd)
			VALUES (?, ?, ?, 'completed', 'codex', '/v1/responses', 'account', ?, ?, ?)`, row.id, row.session, timezonepolicy.FormatStorage(now), row.input, row.output, row.cost)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct {
		id, session   string
		input, output int64
		cost          float64
	}{
		{"hot-a", "session-a", 20, 3, 0.2}, {"overlap-a", "session-a", 30, 4, 0.3}, {"hot-b", "session-b", 60, 6, 0.6},
	} {
		req := NewActiveRequestWithDimensions(row.id, "", "", "POST", "/v1/responses", RequestFamilyCodex, false, RequestDimensions{
			SessionID: row.session, RequestSource: RequestSourceCodexSubscriptionProxy, RequestKind: RequestKindGeneration, UpstreamType: "account", UsageKnown: true,
		})
		req.StartTime, req.Status = now, "processing"
		req.InputTokens, req.OutputTokens = row.input, row.output
		cost := row.cost
		req.CostOverrideUSD = &cost
		if err := tracker.hotPool.Add(req); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { tracker.hotPool.Remove(req.RequestID) })
	}
}

func TestInitSchemaMigratesLegacySessionColumnBeforeIndex(t *testing.T) {
	adapter, err := NewSQLiteAdapter(DatabaseConfig{DatabasePath: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	db := adapter.GetDB()
	if _, err := db.Exec(legacyRequestLogsWithoutLifecycleColumns); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO request_logs (request_id, start_time) VALUES ('legacy-session', '2026-10-01T00:00:00.000000Z')`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := adapter.InitSchema(); err != nil {
			t.Fatalf("旧表补列前不能创建会话索引: %v", err)
		}
	}
	var session, start, columns string
	if err := db.QueryRow(`SELECT session_id, CAST(start_time AS TEXT) FROM request_logs WHERE request_id = 'legacy-session'`).Scan(&session, &start); err != nil {
		t.Fatal(err)
	}
	if session != "" || start != "2026-10-01T00:00:00.000000Z" {
		t.Fatal("旧记录应保持空会话与原时间")
	}
	if err := db.QueryRow(`SELECT group_concat(name, ',') FROM pragma_index_info('idx_request_logs_session_time')`).Scan(&columns); err != nil || columns != "session_id,start_time" {
		t.Fatalf("会话索引缺失或列顺序错误: %q err=%v", columns, err)
	}
}
