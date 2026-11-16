package tracking

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestRequestFlow_TimeoutArchiveBackpressurePersistsSettlements(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.db")
	tr, err := NewUsageTracker(&Config{
		Enabled: true, DatabasePath: path, BufferSize: 1, BatchSize: 1, MaxRetry: 1,
		FlushInterval: time.Hour, CleanupInterval: time.Hour, RetentionDays: 30,
		HotPool: &HotPoolSettings{Enabled: true, MaxAge: time.Minute, MaxSize: 20, CleanupInterval: time.Hour, ArchiveOnCleanup: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	const count = 8
	ids := make([]string, count)
	for i := range ids {
		ids[i] = fmt.Sprintf("archive-backpressure-%d", i)
		if err := tr.RecordRequestStartAtWithDimensions(ids[i], "", "", "POST", "/v1/responses", RequestFamilyCodex, false, time.Now().Add(-time.Hour), RequestDimensions{RequestSource: RequestSourceCodexSubscriptionProxy, RequestKind: RequestKindGeneration}); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := tr.GetWriteDB().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	cleaned := make(chan struct{})
	go func() { defer close(cleaned); tr.hotPool.cleanup() }()
	deadline := time.Now().Add(3 * time.Second)
	for tr.archiveManager.GetChannelLength() != 1 || tr.hotPool.GetArchivingCount() != count {
		if time.Now().After(deadline) {
			t.Fatal("超时请求未填满归档队列")
		}
		time.Sleep(time.Millisecond)
	}
	for _, id := range ids {
		if err := tr.RecordRequestSettlement(id, RequestSettlement{Status: "completed", TokenUsage: &TokenUsage{OutputTokens: 67}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cleaned:
	case <-time.After(3 * time.Second):
		t.Fatal("归档清理未退出")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var rows, completed, total int
	if err := db.QueryRow("SELECT COUNT(*), COUNT(CASE WHEN status = 'completed' THEN 1 END), COALESCE(SUM(output_tokens), 0) FROM request_logs").Scan(&rows, &completed, &total); err != nil {
		t.Fatal(err)
	}
	if rows != count || completed != count || total != count*67 {
		t.Fatalf("重新打开 SQLite：共 %d 行，完成 %d 行，输出 %d tokens", rows, completed, total)
	}
}

func TestRequestFlow_TimeoutPreservesContactOutcomeAndTiming(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ended  bool
		offset int64
	}{{"等待重试", true, 2000}, {"联系仍在进行", false, (3 * time.Hour).Milliseconds()}} {
		t.Run(tc.name, func(t *testing.T) {
			tr := newRequestLifecycleTestTracker(t, true, time.Hour)
			const id = "timeout-contact"
			tr.hotPool.SetArchiveCallback(func(*ActiveRequest) {})
			if err := tr.RecordRequestStartAtWithDimensions(id, "", "", "POST", "/v1/responses", RequestFamilyCodex, true, time.Now().Add(-2*time.Hour), RequestDimensions{RequestSource: RequestSourceLegacyProxy, RequestKind: RequestKindLegacy}); err != nil {
				t.Fatal(err)
			}
			tr.RecordRequestUpdate(id, UpdateOptions{FlowEvents: []RequestFlowEvent{
				{Kind: FlowEventLegSelected, LegSeq: 1, Target: &RequestFlowTargetSnapshot{UpstreamName: "first"}},
				{Kind: FlowEventContactStart, LegSeq: 1, ContactSeq: 1},
			}})
			status, offset := 429, tc.offset
			finish := func() {
				tr.RecordRequestUpdate(id, UpdateOptions{FlowEvents: []RequestFlowEvent{{Kind: FlowEventContactEnd, ContactSeq: 1, HTTPStatusCode: &status, OffsetMs: &offset, OutcomeCode: "rate_limited"}}})
			}
			if tc.ended {
				finish()
			}
			tr.hotPool.cleanup()
			if !tc.ended {
				finish()
			}
			row, ok := tr.hotPool.SnapshotRequest(id)
			if !ok || row.Status != "timeout" || row.RequestFlow == nil || len(row.RequestFlow.Contacts) != 1 {
				t.Fatalf("超时记录不完整：%+v", row)
			}
			contact := row.RequestFlow.Contacts[0]
			if contact.EndOffsetMs == nil || *contact.EndOffsetMs != offset || contact.OutcomeCode != "rate_limited" || contact.HTTPStatusCode == nil || *contact.HTTPStatusCode != status {
				t.Fatalf("归档覆盖了实际联系，期望 %d ms / 429 / rate_limited，得到 %+v", offset, contact)
			}
		})
	}
}
