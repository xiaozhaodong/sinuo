package tracking

import (
	"context"
	"testing"
	"time"
)

// modelConsistencyColumns 是 v6.3 模型一致性监控新增的 5 列。
var modelConsistencyColumns = []string{"request_model", "upstream_model", "response_model", "request_reasoning", "response_reasoning"}

type modelConsistencyRow struct {
	RequestModel, UpstreamModel, ResponseModel, RequestReasoning, ResponseReasoning string
	Status                                                                          string
}

func readModelConsistencyRow(t *testing.T, tracker *UsageTracker, requestID string) (modelConsistencyRow, bool) {
	t.Helper()
	var row modelConsistencyRow
	err := tracker.GetReadDB().QueryRow(`SELECT COALESCE(request_model,''), COALESCE(upstream_model,''), COALESCE(response_model,''),
		COALESCE(request_reasoning,''), COALESCE(response_reasoning,''), status FROM request_logs WHERE request_id = ?`, requestID).
		Scan(&row.RequestModel, &row.UpstreamModel, &row.ResponseModel, &row.RequestReasoning, &row.ResponseReasoning, &row.Status)
	if err != nil {
		return row, false
	}
	return row, true
}

func waitModelConsistencyRow(t *testing.T, tracker *UsageTracker, requestID string, ready func(modelConsistencyRow) bool) modelConsistencyRow {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		row, ok := readModelConsistencyRow(t, tracker, requestID)
		if ok && ready(row) {
			return row
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for request %s row, last=%+v ok=%v", requestID, row, ok)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func startWithRequestProfile(t *testing.T, tracker *UsageTracker, requestID, path, model, reasoning string) {
	t.Helper()
	err := tracker.RecordRequestStartAtWithDimensions(requestID, "127.0.0.1", "agent", "POST", path, requestFamilyFromPath(path), false, time.Time{}, RequestDimensions{
		RequestSource:    RequestSourceLegacyProxy,
		RequestKind:      RequestKindLegacy,
		UpstreamType:     "endpoint",
		UsageKnown:       true,
		RequestModel:     model,
		RequestReasoning: reasoning,
	})
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}
}

// TestInitSchemaAddsModelConsistencyColumnsToLegacyDB 旧库经 InitSchema 后补齐 5 列，重复执行幂等。
func TestInitSchemaAddsModelConsistencyColumnsToLegacyDB(t *testing.T) {
	adapter, err := NewSQLiteAdapter(DatabaseConfig{DatabasePath: ":memory:"})
	if err != nil {
		t.Fatalf("create adapter failed: %v", err)
	}
	if err := adapter.Open(); err != nil {
		t.Fatalf("open adapter failed: %v", err)
	}
	t.Cleanup(func() { _ = adapter.Close() })

	db := adapter.GetDB()
	if _, err := db.Exec(legacyRequestLogsWithoutLifecycleColumns); err != nil {
		t.Fatalf("create legacy request_logs failed: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO request_logs (request_id, start_time) VALUES ('legacy-row', '2026-09-22T00:00:00.000000Z')`); err != nil {
		t.Fatalf("insert legacy row failed: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := adapter.InitSchema(); err != nil {
			t.Fatalf("InitSchema #%d failed: %v", i+1, err)
		}
	}
	for _, column := range modelConsistencyColumns {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('request_logs') WHERE name = ?`, column).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("column %s missing after migration", column)
		}
	}
	var requestModel string
	if err := db.QueryRow(`SELECT COALESCE(request_model, '<null>') FROM request_logs WHERE request_id = 'legacy-row'`).Scan(&requestModel); err != nil {
		t.Fatal(err)
	}
	if requestModel != "" {
		t.Fatalf("legacy row request_model = %q, want default ''", requestModel)
	}
}

// TestModelConsistency_HotPoolStartAndSuccessDiagnostics 热池：start 带请求两项，success finalizer 合并诊断，归档落库。
func TestModelConsistency_HotPoolStartAndSuccessDiagnostics(t *testing.T) {
	tracker := newRequestLifecycleTestTracker(t, true, 50*time.Millisecond)
	const requestID = "req-mc-hot-success"
	startWithRequestProfile(t, tracker, requestID, "/v1/messages", "claude-sonnet-4-5", "thinking:adaptive effort:xhigh")

	// 中间态：attempt A 改写，attempt B 未改写（写回空串）
	tracker.RecordRequestUpdate(requestID, UpdateOptions{UpstreamModel: stringPtr("claude-sonnet-4-5-20250929")})
	tracker.RecordRequestUpdate(requestID, UpdateOptions{UpstreamModel: stringPtr("")})
	// 空 response 不覆盖
	tracker.RecordRequestUpdate(requestID, UpdateOptions{ResponseModel: stringPtr("")})

	req, ok := tracker.GetActiveRequest(requestID)
	if !ok {
		t.Fatal("expected active request")
	}
	if req.RequestModel != "claude-sonnet-4-5" || req.RequestReasoning != "thinking:adaptive effort:xhigh" {
		t.Fatalf("start dimensions not applied: %+v", req)
	}
	if req.UpstreamModel != "" {
		t.Fatalf("upstream_model should be overwritten by empty string, got %q", req.UpstreamModel)
	}

	tracker.RecordRequestSuccessWithDiagnostics(requestID, "claude-sonnet-4-5-20250929", &TokenUsage{InputTokens: 10, OutputTokens: 5}, 40*time.Millisecond, "", &ResponseDiagnostics{
		UpstreamModel:     "",
		ResponseModel:     "claude-sonnet-4-5-20250929",
		ResponseReasoning: "thinking:present",
	})

	row := waitModelConsistencyRow(t, tracker, requestID, func(r modelConsistencyRow) bool { return r.Status == "completed" })
	want := modelConsistencyRow{RequestModel: "claude-sonnet-4-5", ResponseModel: "claude-sonnet-4-5-20250929", RequestReasoning: "thinking:adaptive effort:xhigh", ResponseReasoning: "thinking:present", Status: "completed"}
	if row != want {
		t.Fatalf("archived row = %+v, want %+v", row, want)
	}

	details, err := tracker.GetRequestLogs(context.Background(), time.Now().Add(-time.Hour), time.Now().Add(time.Hour), "", "", "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(details) != 1 || details[0].ResponseModel != "claude-sonnet-4-5-20250929" || details[0].RequestReasoning != "thinking:adaptive effort:xhigh" {
		t.Fatalf("GetRequestLogs did not surface new columns: %+v", details)
	}
}

// TestModelConsistency_LegacyStartAndFinalFailureDiagnostics legacy 队列：start INSERT 含请求两项，final_failure UPDATE 含诊断。
func TestModelConsistency_LegacyStartAndFinalFailureDiagnostics(t *testing.T) {
	tracker := newRequestLifecycleTestTracker(t, false, 30*time.Millisecond)
	const requestID = "req-mc-legacy-failure"
	startWithRequestProfile(t, tracker, requestID, "/v1/responses", "gpt-5.4", "effort:xhigh")

	waitModelConsistencyRow(t, tracker, requestID, func(r modelConsistencyRow) bool {
		return r.RequestModel == "gpt-5.4" && r.RequestReasoning == "effort:xhigh"
	})

	tracker.RecordRequestUpdate(requestID, UpdateOptions{UpstreamModel: stringPtr("gpt-5.5"), ResponseReasoning: stringPtr("effort:medium")})
	waitModelConsistencyRow(t, tracker, requestID, func(r modelConsistencyRow) bool { return r.UpstreamModel == "gpt-5.5" })

	tracker.RecordRequestFinalFailureWithDiagnostics(requestID, "gpt-5.5", "failed", "server_error", "boom", 20*time.Millisecond, 502, nil, &ResponseDiagnostics{
		UpstreamModel:     "gpt-5.5",
		ResponseModel:     "gpt-5.5",
		ResponseReasoning: "effort:medium reasoning_tokens:12",
	})
	row := waitModelConsistencyRow(t, tracker, requestID, func(r modelConsistencyRow) bool { return r.Status == "failed" })
	want := modelConsistencyRow{RequestModel: "gpt-5.4", UpstreamModel: "gpt-5.5", ResponseModel: "gpt-5.5", RequestReasoning: "effort:xhigh", ResponseReasoning: "effort:medium reasoning_tokens:12", Status: "failed"}
	if row != want {
		t.Fatalf("legacy row = %+v, want %+v", row, want)
	}
}

// TestModelConsistency_LegacySuccessNilDiagnosticsKeepsColumns 旧签名（nil 诊断）不得清空中间态已写的列。
func TestModelConsistency_LegacySuccessNilDiagnosticsKeepsColumns(t *testing.T) {
	tracker := newRequestLifecycleTestTracker(t, false, 30*time.Millisecond)
	const requestID = "req-mc-legacy-nil-diag"
	startWithRequestProfile(t, tracker, requestID, "/v1/messages", "claude-opus-5", "")
	tracker.RecordRequestUpdate(requestID, UpdateOptions{UpstreamModel: stringPtr("claude-opus-5-x"), ResponseModel: stringPtr("claude-opus-5-x")})
	waitModelConsistencyRow(t, tracker, requestID, func(r modelConsistencyRow) bool { return r.ResponseModel == "claude-opus-5-x" })

	tracker.RecordRequestSuccess(requestID, "claude-opus-5-x", nil, 10*time.Millisecond)
	row := waitModelConsistencyRow(t, tracker, requestID, func(r modelConsistencyRow) bool { return r.Status == "completed" })
	if row.UpstreamModel != "claude-opus-5-x" || row.ResponseModel != "claude-opus-5-x" {
		t.Fatalf("nil diagnostics must not clear columns: %+v", row)
	}
}

// TestModelConsistency_SettlementCarriesDiagnostics 订阅结算路径（热池 + legacy）携带诊断。
func TestModelConsistency_SettlementCarriesDiagnostics(t *testing.T) {
	for _, hotPool := range []bool{true, false} {
		tracker := newRequestLifecycleTestTracker(t, hotPool, 30*time.Millisecond)
		requestID := "req-mc-settlement-hot"
		if !hotPool {
			requestID = "req-mc-settlement-legacy"
		}
		err := tracker.RecordRequestStartAtWithDimensions(requestID, "127.0.0.1", "agent", "POST", "/v1/responses", RequestFamilyCodex, true, time.Time{}, RequestDimensions{
			RequestSource:    RequestSourceCodexSubscriptionProxy,
			RequestKind:      RequestKindGeneration,
			UpstreamType:     "subscription",
			RequestModel:     "gpt-5.5",
			RequestReasoning: "effort:high",
		})
		if err != nil {
			t.Fatal(err)
		}
		status := 200
		if err := tracker.RecordRequestSettlement(requestID, RequestSettlement{
			Status: "completed", ModelName: "gpt-5.5", Path: "/v1/responses", HTTPStatus: &status,
			Diagnostics: &ResponseDiagnostics{ResponseModel: "gpt-5.5", ResponseReasoning: "effort:high reasoning_tokens:99"},
		}); err != nil {
			t.Fatal(err)
		}
		row := waitModelConsistencyRow(t, tracker, requestID, func(r modelConsistencyRow) bool { return r.Status == "completed" })
		want := modelConsistencyRow{RequestModel: "gpt-5.5", RequestReasoning: "effort:high", ResponseModel: "gpt-5.5", ResponseReasoning: "effort:high reasoning_tokens:99", Status: "completed"}
		if row != want {
			t.Fatalf("hotPool=%v settlement row = %+v, want %+v", hotPool, row, want)
		}
	}
}
