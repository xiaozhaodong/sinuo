package main

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	timezonepolicy "sinuo/internal/timezone"
	"sinuo/internal/tracking"
)

func TestRequestAPIsFilterSessionAndKeepUpstreamOptions(t *testing.T) {
	app := newUsageStatsTestApp(t)
	now := time.Now()
	seedAPISessions(t, app, now)
	start, end := now.Add(-time.Hour).Format(time.RFC3339Nano), now.Add(time.Hour).Format(time.RFC3339Nano)
	includeCompaction := true
	for _, tc := range []struct {
		session string
		count   int
		tokens  int64
		cost    float64
	}{
		{"session-a", 2, 132, 1.5}, {"session-b", 1, 1098, 5}, {"missing", 0, 0, 0},
	} {
		params := RequestQueryParams{SessionID: tc.session, StartDate: start, EndDate: end, Page: 1, PageSize: 20, SourceView: "all"}
		list, err := app.GetRequests(params)
		if err != nil || list.Total != tc.count || len(list.Requests) != tc.count {
			t.Fatalf("GetRequests 会话筛选失败: list=%+v err=%v", list, err)
		}
		for _, row := range list.Requests {
			if row.SessionID != tc.session {
				t.Fatal("RequestRecord 丢失或混入其他会话")
			}
		}
		stats, err := app.GetUsageStats(UsageStatsQueryParams{
			SessionID: tc.session, StartDate: start, EndDate: end, SourceView: "all", IncludeCompaction: &includeCompaction,
		})
		if err != nil || stats.TotalRequests != tc.count || stats.TotalTokens != tc.tokens || math.Abs(stats.TotalCostUSD-tc.cost) > 1e-9 {
			t.Fatalf("GetUsageStats 与列表口径不一致: stats=%+v err=%v", stats, err)
		}
	}
	options, err := app.GetRequestUpstreamOptions(RequestQueryParams{
		SessionID: "session-a", StartDate: start, EndDate: end, SourceView: "all",
		Model: "not-selected", Status: "failed", UpstreamName: "not-selected",
	})
	if err != nil || len(options) != 3 {
		t.Fatalf("候选上游不应被会话、模型、状态或上游收窄: %+v err=%v", options, err)
	}
}

func seedAPISessions(t *testing.T, app *App, now time.Time) {
	t.Helper()
	for _, row := range []struct {
		id, session, kind, upstream string
		input, output               int64
		cost                        float64
	}{
		{"api-a-generation", "session-a", "generation", "upstream-a", 100, 10, 1},
		{"api-a-compaction", "session-a", "compaction", "upstream-a", 20, 2, 0.5},
		{"api-b-generation", "session-b", "generation", "upstream-b", 999, 99, 5},
		{"api-legacy", "", "legacy", "upstream-legacy", 1, 1, 0.1},
	} {
		_, err := app.usageTracker.GetWriteDB().Exec(`INSERT INTO request_logs
			(request_id, session_id, start_time, request_kind, request_family, upstream_type, upstream_name, status, input_tokens, output_tokens, total_cost_usd)
			VALUES (?, ?, ?, ?, 'codex', 'account', ?, 'completed', ?, ?, ?)`,
			row.id, row.session, timezonepolicy.FormatStorage(now), row.kind, row.upstream, row.input, row.output, row.cost)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestRequestRecordSessionJSON(t *testing.T) {
	for _, session := range []string{"synthetic-session", ""} {
		record := requestDetailToRecord(tracking.RequestDetail{RequestID: "req-session-dto", SessionID: session})
		if record.SessionID != session {
			t.Fatal("会话 DTO 映射错误")
		}
		data, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		var payload map[string]interface{}
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatal(err)
		}
		got, present := payload["session_id"]
		if present != (session != "") || (present && got != session) {
			t.Fatalf("session_id JSON 值或 omitempty 不正确: %v", got)
		}
	}
}
