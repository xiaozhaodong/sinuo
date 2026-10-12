package tracking

import (
	"context"
	"testing"
	"time"
)

func newRequestLifecycleTestTracker(t *testing.T, hotPoolEnabled bool, flushInterval time.Duration) *UsageTracker {
	t.Helper()
	tracker, err := NewUsageTracker(&Config{
		Enabled:         true,
		DatabasePath:    ":memory:",
		BufferSize:      100,
		BatchSize:       10,
		FlushInterval:   flushInterval,
		MaxRetry:        3,
		CleanupInterval: 24 * time.Hour,
		RetentionDays:   30,
		HotPool: &HotPoolSettings{
			Enabled:          hotPoolEnabled,
			MaxAge:           30 * time.Minute,
			MaxSize:          1000,
			CleanupInterval:  time.Minute,
			ArchiveOnCleanup: true,
		},
	})
	if err != nil {
		t.Fatalf("failed to create usage tracker: %v", err)
	}
	t.Cleanup(func() { _ = tracker.Close() })
	return tracker
}

// TestGetRequestLifecycleData_HotPoolPath 热池命中：首响前写完偏移覆盖，首响后冻结。
func TestGetRequestLifecycleData_HotPoolPath(t *testing.T) {
	tracker := newRequestLifecycleTestTracker(t, true, time.Hour)
	ctx := context.Background()

	const requestID = "req-lifecycle-hot"
	tracker.RecordRequestStart(requestID, "127.0.0.1", "agent", "POST", "/v1/responses", true)

	upstreamWriteMs := int64(321)
	scheduleJSON := `{"request_id":"req-lifecycle-hot","final_outcome":"success"}`
	privacyJSON := `{"action":"redact","hit_count":1}`

	tracker.RecordRequestUpdate(requestID, UpdateOptions{UpstreamWriteMs: &upstreamWriteMs})
	tracker.RecordRequestUpdate(requestID, UpdateOptions{ScheduleSnapshotJSON: &scheduleJSON})
	tracker.RecordRequestUpdate(requestID, UpdateOptions{PrivacyScanJSON: &privacyJSON})

	// 首响前第二次成功写完覆盖为最终尝试。
	lateWrite := int64(999)
	tracker.RecordRequestUpdate(requestID, UpdateOptions{UpstreamWriteMs: &lateWrite})
	firstTokenMs := int64(20)
	tracker.RecordRequestUpdate(requestID, UpdateOptions{FirstTokenMs: &firstTokenMs})
	afterFirstTokenWrite := int64(1500)
	tracker.RecordRequestUpdate(requestID, UpdateOptions{UpstreamWriteMs: &afterFirstTokenWrite})

	data, err := tracker.GetRequestLifecycleData(ctx, requestID)
	if err != nil {
		t.Fatalf("GetRequestLifecycleData failed: %v", err)
	}
	if data == nil {
		t.Fatal("expected lifecycle data in hot pool")
	}
	if data.Source != "hot_pool" {
		t.Fatalf("expected source hot_pool, got %q", data.Source)
	}
	if data.UpstreamWriteMs == nil || *data.UpstreamWriteMs != 999 {
		t.Fatalf("expected latest upstream_write_ms 999 frozen after first token, got %v", data.UpstreamWriteMs)
	}
	if data.ScheduleSnapshotJSON != scheduleJSON {
		t.Fatalf("expected schedule snapshot preserved, got %q", data.ScheduleSnapshotJSON)
	}
	if data.PrivacyScanJSON != privacyJSON {
		t.Fatalf("expected privacy scan preserved, got %q", data.PrivacyScanJSON)
	}
	if data.Detail.RequestID != requestID {
		t.Fatalf("expected detail request id %q, got %q", requestID, data.Detail.RequestID)
	}
}

// TestGetRequestLifecycleData_ArchivingWindow CompleteAndArchive 后、批量 INSERT 确认前仍可读。
func TestGetRequestLifecycleData_ArchivingWindow(t *testing.T) {
	// 长 FlushInterval 保证归档批量 INSERT 不会在断言前执行。
	tracker := newRequestLifecycleTestTracker(t, true, time.Hour)
	ctx := context.Background()

	const requestID = "req-lifecycle-archiving"
	tracker.RecordRequestStart(requestID, "127.0.0.1", "agent", "POST", "/v1/responses", true)

	snapshotJSON := `{"request_id":"req-lifecycle-archiving","final_outcome":"pending"}`
	tracker.RecordRequestUpdate(requestID, UpdateOptions{ScheduleSnapshotJSON: &snapshotJSON})

	tracker.RecordRequestSuccess(requestID, "claude-sonnet-4-20250514", nil, 50*time.Millisecond)

	data, err := tracker.GetRequestLifecycleData(ctx, requestID)
	if err != nil {
		t.Fatalf("GetRequestLifecycleData failed: %v", err)
	}
	if data == nil {
		t.Fatal("expected request readable during archiving window")
	}
	if data.Source != "hot_pool" {
		t.Fatalf("expected source hot_pool during archiving window, got %q", data.Source)
	}
	if data.ScheduleSnapshotJSON != snapshotJSON {
		t.Fatalf("expected snapshot preserved in archiving copy, got %q", data.ScheduleSnapshotJSON)
	}
	if data.Detail.Status != "completed" {
		t.Fatalf("expected completed status, got %q", data.Detail.Status)
	}
}

// TestGetRequestLifecycleData_DatabasePathAndMissing 落库后走 DB；不存在的行返回 (nil, nil)。
func TestGetRequestLifecycleData_DatabasePathAndMissing(t *testing.T) {
	tracker := newRequestLifecycleTestTracker(t, true, 50*time.Millisecond)
	ctx := context.Background()

	const requestID = "req-lifecycle-db"
	tracker.RecordRequestStart(requestID, "127.0.0.1", "agent", "POST", "/v1/responses", true)

	snapshotJSON := `{"request_id":"req-lifecycle-db","final_outcome":"success"}`
	tracker.RecordRequestUpdate(requestID, UpdateOptions{ScheduleSnapshotJSON: &snapshotJSON})
	tracker.RecordRequestSuccess(requestID, "claude-sonnet-4-20250514", nil, 30*time.Millisecond)

	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := tracker.GetRequestLifecycleData(ctx, requestID)
		if err != nil {
			t.Fatalf("GetRequestLifecycleData failed: %v", err)
		}
		if data != nil && data.Source == "database" {
			if data.ScheduleSnapshotJSON != snapshotJSON {
				t.Fatalf("expected snapshot preserved in DB, got %q", data.ScheduleSnapshotJSON)
			}
			if data.Detail.RequestID != requestID {
				t.Fatalf("expected detail request id %q", requestID)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for database source")
		}
		time.Sleep(20 * time.Millisecond)
	}

	missing, err := tracker.GetRequestLifecycleData(ctx, "req-does-not-exist")
	if err != nil {
		t.Fatalf("GetRequestLifecycleData for missing row failed: %v", err)
	}
	if missing != nil {
		t.Fatalf("expected nil for missing row, got %+v", missing)
	}
}

// TestGetRequestLifecycleData_HotPoolDisabled 热池禁用时直接走 DB（hotPool=nil 场景）。
func TestGetRequestLifecycleData_HotPoolDisabled(t *testing.T) {
	tracker := newRequestLifecycleTestTracker(t, false, 50*time.Millisecond)
	ctx := context.Background()

	const requestID = "req-lifecycle-legacy"
	tracker.RecordRequestStart(requestID, "127.0.0.1", "agent", "POST", "/v1/responses", false)
	tracker.RecordRequestUpdate(requestID, UpdateOptions{
		ScheduleSnapshotJSON: stringPtr(`{"request_id":"req-lifecycle-legacy"}`),
	})

	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := tracker.GetRequestLifecycleData(ctx, requestID)
		if err != nil {
			t.Fatalf("GetRequestLifecycleData failed: %v", err)
		}
		if data != nil {
			if data.Source != "database" {
				t.Fatalf("expected database source when hot pool disabled, got %q", data.Source)
			}
			if data.ScheduleSnapshotJSON == "" {
				t.Fatal("expected schedule snapshot written via legacy setParts")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for legacy path write")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestRecordRequestUpdateAfterArchiveLandsInDB 终态快照在 CompleteAndArchive 后写入：
// 必须命中 archiving map，立即可见且随归档 INSERT 落库，不退回 draft。
func TestRecordRequestUpdateAfterArchiveLandsInDB(t *testing.T) {
	tracker := newRequestLifecycleTestTracker(t, true, 50*time.Millisecond)
	ctx := context.Background()

	const requestID = "req-lifecycle-post-archive"
	tracker.RecordRequestStart(requestID, "127.0.0.1", "agent", "POST", "/v1/responses", true)

	draft := `{"request_id":"req-lifecycle-post-archive","final_outcome":"pending"}`
	tracker.RecordRequestUpdate(requestID, UpdateOptions{ScheduleSnapshotJSON: &draft})

	// 响应处理内部先 CompleteRequest（CompleteAndArchive），成功快照随后才写。
	tracker.RecordRequestSuccess(requestID, "claude-sonnet-4-20250514", nil, 30*time.Millisecond)

	final := `{"request_id":"req-lifecycle-post-archive","final_outcome":"success"}`
	tracker.RecordRequestUpdate(requestID, UpdateOptions{ScheduleSnapshotJSON: &final})

	// 立即读取：archiving 副本已携带终态快照。
	data, err := tracker.GetRequestLifecycleData(ctx, requestID)
	if err != nil {
		t.Fatalf("GetRequestLifecycleData failed: %v", err)
	}
	if data == nil || data.Source != "hot_pool" {
		t.Fatalf("expected hot_pool read during archiving window, got %+v", data)
	}
	if data.ScheduleSnapshotJSON != final {
		t.Fatalf("expected final snapshot visible immediately, got %q", data.ScheduleSnapshotJSON)
	}

	// 落库后仍是终态，不退回 draft。
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err = tracker.GetRequestLifecycleData(ctx, requestID)
		if err != nil {
			t.Fatalf("GetRequestLifecycleData failed: %v", err)
		}
		if data != nil && data.Source == "database" {
			if data.ScheduleSnapshotJSON != final {
				t.Fatalf("expected final snapshot persisted, got %q", data.ScheduleSnapshotJSON)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for database source")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
