package tracking

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
	"time"
)

func phaseTestPtr[T any](value T) *T { return &value }

func sampleStreamPhases() StreamPhases {
	return StreamPhases{
		FirstContentMs: phaseTestPtr(int64(0)), FirstTextMs: phaseTestPtr(int64(20)),
		FirstReasoningMs: phaseTestPtr(int64(0)), ReasoningHidden: phaseTestPtr(false),
		StreamFlowMs: phaseTestPtr(int64(500)), StopReason: phaseTestPtr("end_turn"),
		UpstreamRequestID: phaseTestPtr("synthetic-first"),
	}
}

func requireStoredPhases(t *testing.T, tracker *UsageTracker, id string, want StreamPhases) *RequestLifecycleData {
	t.Helper()
	data, err := tracker.GetRequestLifecycleData(context.Background(), id)
	if err != nil || data == nil {
		t.Fatalf("详情=%+v, err=%v", data, err)
	}
	if !reflect.DeepEqual(data.StreamPhases, want) {
		t.Fatalf("阶段=%+v, want %+v", data.StreamPhases, want)
	}
	row := retryListRow(t, tracker, id)
	if !reflect.DeepEqual(row.FirstContentMs, want.FirstContentMs) || !reflect.DeepEqual(row.StopReason, want.StopReason) {
		t.Fatalf("列表丢字段: %+v", row)
	}
	return data
}

func TestStreamPhases_StorageWriteOnceAndFinalAttempt(t *testing.T) {
	for _, hot := range []bool{true, false} {
		t.Run(map[bool]string{true: "hot", false: "db"}[hot], func(t *testing.T) {
			tracker := newRequestLifecycleTestTracker(t, hot, time.Hour)
			seedRetryStorage(t, tracker, "synthetic-phase-storage", hot)
			want := sampleStreamPhases()
			at := time.Now()
			applyRetryStorageUpdate(t, tracker, "synthetic-phase-storage", hot, UpdateOptions{StreamPhases: want, FirstContentAt: &at})
			// 调用者指针与返回的快照都不能修改热池。
			*want.FirstTextMs = 999
			want.FirstTextMs = phaseTestPtr(int64(20))
			data := requireStoredPhases(t, tracker, "synthetic-phase-storage", want)
			*data.FirstContentMs = 999
			next := StreamPhases{FirstContentMs: phaseTestPtr(int64(900)), ReasoningHidden: phaseTestPtr(true),
				StreamFlowMs: phaseTestPtr(int64(1)), StopReason: phaseTestPtr("max_tokens"), UpstreamRequestID: phaseTestPtr("")}
			applyRetryStorageUpdate(t, tracker, "synthetic-phase-storage", hot, UpdateOptions{StreamPhases: next})
			want.StopReason, want.UpstreamRequestID = next.StopReason, next.UpstreamRequestID
			requireStoredPhases(t, tracker, "synthetic-phase-storage", want)
		})
	}
}

func TestStreamPhases_ArchivePreservesAllColumns(t *testing.T) {
	tracker := newRequestLifecycleTestTracker(t, true, 10*time.Millisecond)
	seedRetryStorage(t, tracker, "synthetic-phase-archive", true)
	want := sampleStreamPhases()
	tracker.RecordRequestUpdate("synthetic-phase-archive", UpdateOptions{StreamPhases: want})
	tracker.RecordRequestSuccess("synthetic-phase-archive", "claude-test", nil, time.Second)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if data := requireStoredPhases(t, tracker, "synthetic-phase-archive", want); data.Source == "database" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("归档超时")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestStreamPhases_LegacySchemaNullableAndIdempotent(t *testing.T) {
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
	if _, err := db.Exec("INSERT INTO request_logs (request_id,start_time) VALUES (?,?)", "synthetic-legacy", "2026-10-30T00:00:00.000000Z"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := adapter.InitSchema(); err != nil {
			t.Fatal(err)
		}
	}
	var content, text, reasoning, hidden, flow sql.NullInt64
	var stop, id sql.NullString
	if err := db.QueryRow("SELECT first_content_ms,first_text_ms,first_reasoning_ms,reasoning_hidden,stream_flow_ms,stop_reason,upstream_request_id FROM request_logs").Scan(&content, &text, &reasoning, &hidden, &flow, &stop, &id); err != nil {
		t.Fatal(err)
	}
	if content.Valid || text.Valid || reasoning.Valid || hidden.Valid || flow.Valid || stop.Valid || id.Valid {
		t.Fatal("旧记录不能回填")
	}
}

func TestStreamPhases_CompleteDatabaseBackupPreservesColumns(t *testing.T) {
	tracker := newBackupTestTracker(t)
	seedRetryStorage(t, tracker, "synthetic-phase-backup", false)
	want := sampleStreamPhases()
	applyRetryStorageUpdate(t, tracker, "synthetic-phase-backup", false, UpdateOptions{StreamPhases: want})
	if err := tracker.errorHandler.CreateBackup(); err != nil {
		t.Fatal(err)
	}
	backup := openBackupTestDatabase(t, tracker.config.DatabasePath+".backup")
	var got StreamPhases
	err := backup.QueryRow("SELECT first_content_ms,first_text_ms,first_reasoning_ms,reasoning_hidden,stream_flow_ms,stop_reason,upstream_request_id FROM request_logs WHERE request_id=?", "synthetic-phase-backup").Scan(
		&got.FirstContentMs, &got.FirstTextMs, &got.FirstReasoningMs, &got.ReasoningHidden, &got.StreamFlowMs, &got.StopReason, &got.UpstreamRequestID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("快照丢列: %+v", got)
	}
}
