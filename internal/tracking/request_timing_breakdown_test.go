package tracking

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func timingUpdate(dispatch, conn, write int64) UpdateOptions {
	return UpdateOptions{UpstreamDispatchMs: &dispatch, UpstreamConnMs: &conn, UpstreamWriteMs: &write}
}

func requireTimingOffsets(t *testing.T, data *RequestLifecycleData, dispatch, conn, write int64) {
	t.Helper()
	if data == nil || data.UpstreamDispatchMs == nil || data.UpstreamConnMs == nil || data.UpstreamWriteMs == nil {
		t.Fatalf("缺少最终尝试计时：%+v", data)
	}
	if *data.UpstreamDispatchMs != dispatch || *data.UpstreamConnMs != conn || *data.UpstreamWriteMs != write {
		t.Fatalf("计时 = %d/%d/%d，want %d/%d/%d", *data.UpstreamDispatchMs, *data.UpstreamConnMs, *data.UpstreamWriteMs, dispatch, conn, write)
	}
}

func TestRequestTiming_HotPoolMergeCloneAndFirstTokenFreeze(t *testing.T) {
	tracker := newRequestLifecycleTestTracker(t, true, time.Hour)
	const id = "timing-hot"
	tracker.RecordRequestStart(id, "127.0.0.1", "agent", "POST", "/v1/messages", true)
	tracker.RecordRequestUpdate(id, timingUpdate(10, 20, 30))
	tracker.RecordRequestUpdate(id, timingUpdate(100, 120, 150))
	data, err := tracker.GetRequestLifecycleData(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	requireTimingOffsets(t, data, 100, 120, 150)
	if data.Source != "hot_pool" {
		t.Fatalf("source = %s，want hot_pool", data.Source)
	}
	// 详情快照不得共享热池内的指针。
	*data.UpstreamDispatchMs, *data.UpstreamConnMs, *data.UpstreamWriteMs = 999, 999, 999
	first := int64(40)
	tracker.RecordRequestUpdate(id, UpdateOptions{FirstTokenMs: &first})
	tracker.RecordRequestUpdate(id, timingUpdate(200, 220, 250))
	data, err = tracker.GetRequestLifecycleData(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	requireTimingOffsets(t, data, 100, 120, 150)
}

func TestRequestTiming_DatabaseMergeAndFirstTokenFreeze(t *testing.T) {
	tracker := newRequestLifecycleTestTracker(t, false, time.Hour)
	const id = "timing-db"
	// 直接执行传统写入查询，验证真实 SQLite 的 CASE 语义与详情读取，无异步等待。
	if _, err := tracker.readDB.Exec(`INSERT INTO request_logs (request_id, start_time) VALUES (?, ?)`, id, "2026-10-12T00:00:00.000000Z"); err != nil {
		t.Fatal(err)
	}
	apply := func(opts UpdateOptions) {
		t.Helper()
		query, args, err := tracker.buildFlexibleUpdateQuery(RequestEvent{RequestID: id, Data: opts})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tracker.readDB.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	apply(timingUpdate(10, 20, 30))
	apply(timingUpdate(100, 120, 150))
	data, err := tracker.GetRequestLifecycleData(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	requireTimingOffsets(t, data, 100, 120, 150)
	if data.Source != "database" {
		t.Fatalf("source = %s，want database", data.Source)
	}
	first := int64(40)
	apply(UpdateOptions{FirstTokenMs: &first})
	apply(timingUpdate(200, 220, 250))
	data, err = tracker.GetRequestLifecycleData(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	requireTimingOffsets(t, data, 100, 120, 150)
}

func TestRequestTiming_SameUpdateAsFirstToken(t *testing.T) {
	for _, hot := range []bool{true, false} {
		t.Run(map[bool]string{true: "hot_pool", false: "database"}[hot], func(t *testing.T) {
			tracker := newRequestLifecycleTestTracker(t, hot, time.Hour)
			const id = "timing-same-update"
			opts := timingUpdate(10, 20, 30)
			first := int64(40)
			opts.FirstTokenMs = &first
			if hot {
				tracker.RecordRequestStart(id, "127.0.0.1", "agent", "POST", "/v1/messages", true)
				tracker.RecordRequestUpdate(id, opts)
			} else {
				if _, err := tracker.readDB.Exec(`INSERT INTO request_logs (request_id, start_time) VALUES (?, ?)`, id, "2026-10-12T00:00:00.000000Z"); err != nil {
					t.Fatal(err)
				}
				query, args, err := tracker.buildFlexibleUpdateQuery(RequestEvent{RequestID: id, Data: opts})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := tracker.readDB.Exec(query, args...); err != nil {
					t.Fatal(err)
				}
			}
			data, err := tracker.GetRequestLifecycleData(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			requireTimingOffsets(t, data, 10, 20, 30)
			if data.Detail.FirstTokenMs == nil || *data.Detail.FirstTokenMs != 40 {
				t.Fatal("同次更新应写入首响并冻结三个偏移")
			}
		})
	}
}

func TestRequestTiming_ArchivePersistsFinalAttempt(t *testing.T) {
	tracker := newRequestLifecycleTestTracker(t, true, 10*time.Millisecond)
	const id = "timing-archive"
	tracker.RecordRequestStart(id, "127.0.0.1", "agent", "POST", "/v1/messages", true)
	tracker.RecordRequestUpdate(id, timingUpdate(10, 20, 30))
	tracker.RecordRequestUpdate(id, timingUpdate(100, 120, 150))
	first := int64(40)
	tracker.RecordRequestUpdate(id, UpdateOptions{FirstTokenMs: &first})
	tracker.RecordRequestSuccess(id, "claude-test", nil, 200*time.Millisecond)
	deadline := time.Now().Add(2 * time.Second)
	for {
		data, err := tracker.GetRequestLifecycleData(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		requireTimingOffsets(t, data, 100, 120, 150)
		if data.Source == "database" {
			var dispatch, conn, write int64
			if err := tracker.readDB.QueryRow(`SELECT upstream_dispatch_ms, upstream_conn_ms, upstream_write_ms FROM request_logs WHERE request_id = ?`, id).Scan(&dispatch, &conn, &write); err != nil {
				t.Fatal(err)
			}
			if dispatch != 100 || conn != 120 || write != 150 {
				t.Fatalf("归档丢失最终尝试：%d/%d/%d", dispatch, conn, write)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("等待归档超时")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRequestTiming_LegacySchemaAddsNullableColumns(t *testing.T) {
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
	if _, err := db.Exec(`INSERT INTO request_logs (request_id, start_time) VALUES (?, ?)`, "legacy-timing", "2026-10-12T00:00:00.000000Z"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := adapter.InitSchema(); err != nil {
			t.Fatal(err)
		}
	}
	var dispatch, conn sql.NullInt64
	if err := db.QueryRow(`SELECT upstream_dispatch_ms, upstream_conn_ms FROM request_logs WHERE request_id = ?`, "legacy-timing").Scan(&dispatch, &conn); err != nil {
		t.Fatal(err)
	}
	if dispatch.Valid || conn.Valid {
		t.Fatalf("旧数据不得回填：%v/%v", dispatch, conn)
	}
}
