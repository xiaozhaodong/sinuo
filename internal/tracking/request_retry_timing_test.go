package tracking

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func retryStorageUpdate(first, dispatch, conn, write int64, count int) UpdateOptions {
	opts := timingUpdate(dispatch, conn, write)
	opts.UpstreamFirstDispatchMs = &first
	opts.RetryCount = &count
	return opts
}

func applyRetryStorageUpdate(t *testing.T, tracker *UsageTracker, id string, hot bool, opts UpdateOptions) {
	t.Helper()
	if hot {
		tracker.RecordRequestUpdate(id, opts)
		return
	}
	query, args, err := tracker.buildFlexibleUpdateQuery(RequestEvent{RequestID: id, Data: opts})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tracker.readDB.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func seedRetryStorage(t *testing.T, tracker *UsageTracker, id string, hot bool) {
	t.Helper()
	if hot {
		tracker.RecordRequestStart(id, "127.0.0.1", "test", "POST", "/v1/messages", true)
		return
	}
	_, err := tracker.readDB.Exec("INSERT INTO request_logs (request_id, start_time) VALUES (?, ?)", id, "2026-10-30T00:00:00.000000Z")
	if err != nil {
		t.Fatal(err)
	}
}

func requireRetryStorage(t *testing.T, tracker *UsageTracker, id string, first *int64, count int) *RequestLifecycleData {
	t.Helper()
	data, err := tracker.GetRequestLifecycleData(context.Background(), id)
	if err != nil || data == nil {
		t.Fatalf("详情: %+v %v", data, err)
	}
	got := data.UpstreamFirstDispatchMs
	if (got == nil) != (first == nil) || (got != nil && *got != *first) || data.Detail.RetryCount != count {
		t.Fatalf("首次联系与重试数: %+v, count=%d", got, data.Detail.RetryCount)
	}
	row := retryListRow(t, tracker, id)
	got = row.UpstreamFirstDispatchMs
	if (got == nil) != (first == nil) || (got != nil && *got != *first) {
		t.Fatalf("列表首次联系: %+v, want %+v", got, first)
	}
	return data
}

// 列表有归档确认窗口，且热池旧实现不按 RequestID 过滤；按真实列表结果找目标行。
func retryListRow(t *testing.T, tracker *UsageTracker, id string) RequestDetail {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		rows, _, err := tracker.QueryRequestDetailsWithHotPool(context.Background(), &QueryOptions{Limit: 20})
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.RequestID == id {
				return row
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("列表中未出现 %s", id)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRequestRetryTiming_FirstDispatchWriteOnceFreezeAndList(t *testing.T) {
	for _, hot := range []bool{true, false} {
		t.Run(map[bool]string{true: "hot", false: "db"}[hot], func(t *testing.T) {
			tracker := newRequestLifecycleTestTracker(t, hot, time.Hour)
			const id = "retry-storage"
			seedRetryStorage(t, tracker, id, hot)
			applyRetryStorageUpdate(t, tracker, id, hot, retryStorageUpdate(10, 20, 30, 40, 0))
			applyRetryStorageUpdate(t, tracker, id, hot, retryStorageUpdate(50, 100, 120, 140, 1))
			first := int64(10)
			data := requireRetryStorage(t, tracker, id, &first, 1)
			requireTimingOffsets(t, data, 100, 120, 140)
			// 详情首次偏移与行字段都不能共享热池指针。
			*data.UpstreamFirstDispatchMs = 999
			data = requireRetryStorage(t, tracker, id, &first, 1)
			*data.Detail.UpstreamFirstDispatchMs = 999
			requireRetryStorage(t, tracker, id, &first, 1)
			token := int64(5)
			applyRetryStorageUpdate(t, tracker, id, hot, UpdateOptions{FirstTokenMs: &token})
			applyRetryStorageUpdate(t, tracker, id, hot, retryStorageUpdate(70, 160, 180, 200, 1))
			data = requireRetryStorage(t, tracker, id, &first, 1)
			requireTimingOffsets(t, data, 100, 120, 140)
		})
	}
}

func TestRequestRetryTiming_FirstDispatchLateOrSameUpdateAsFirstToken(t *testing.T) {
	for _, hot := range []bool{true, false} {
		t.Run(map[bool]string{true: "hot", false: "db"}[hot], func(t *testing.T) {
			tracker := newRequestLifecycleTestTracker(t, hot, time.Hour)
			token, first := int64(5), int64(10)
			seedRetryStorage(t, tracker, "late", hot)
			applyRetryStorageUpdate(t, tracker, "late", hot, UpdateOptions{FirstTokenMs: &token})
			applyRetryStorageUpdate(t, tracker, "late", hot, UpdateOptions{UpstreamFirstDispatchMs: &first})
			requireRetryStorage(t, tracker, "late", nil, 0)
			seedRetryStorage(t, tracker, "same", hot)
			applyRetryStorageUpdate(t, tracker, "same", hot, UpdateOptions{FirstTokenMs: &token, UpstreamFirstDispatchMs: &first})
			requireRetryStorage(t, tracker, "same", &first, 0)
		})
	}
}

func TestRequestRetryTiming_ArchiveAndDatabaseList(t *testing.T) {
	tracker := newRequestLifecycleTestTracker(t, true, 10*time.Millisecond)
	const id = "retry-archive"
	seedRetryStorage(t, tracker, id, true)
	first := int64(10)
	tracker.RecordRequestUpdate(id, retryStorageUpdate(first, 100, 120, 140, 1))
	tracker.RecordRequestSuccess(id, "claude-test", nil, 200*time.Millisecond)
	deadline := time.Now().Add(2 * time.Second)
	for {
		data := requireRetryStorage(t, tracker, id, &first, 1)
		if data.Source == "database" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("归档超时")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRequestRetryTiming_LegacySchemaAddsNullableFirstDispatch(t *testing.T) {
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
	if _, err := db.Exec("INSERT INTO request_logs (request_id, start_time) VALUES (?, ?)", "old", "2026-10-30T00:00:00.000000Z"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := adapter.InitSchema(); err != nil {
			t.Fatal(err)
		}
	}
	var first sql.NullInt64
	if err := db.QueryRow("SELECT upstream_first_dispatch_ms FROM request_logs WHERE request_id = ?", "old").Scan(&first); err != nil {
		t.Fatal(err)
	}
	if first.Valid {
		t.Fatal("旧行首次联系必须保持NULL")
	}
}
