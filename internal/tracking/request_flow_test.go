package tracking

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRequestFlow_ReducerTransitionsAndFailover(t *testing.T) {
	t0 := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	var flow *RequestFlow

	changed := ApplyRequestFlowEvents(&flow, t0, t0,
		RequestFlowEvent{
			Kind:      FlowEventInit,
			Transport: FlowTransportWS,
			Origin:    FlowOriginClientRequest,
		},
		RequestFlowEvent{
			Kind:       FlowEventLegSelected,
			LegSeq:     1,
			ReasonCode: "initial_route",
			At:         timePtr(t0.Add(10 * time.Millisecond)),
			Target: &RequestFlowTargetSnapshot{
				UpstreamType: "account",
				UpstreamName: "acct-a",
				UpstreamID:   101,
				RouteMode:    "pool",
			},
		},
		RequestFlowEvent{
			Kind:       FlowEventContactStart,
			LegSeq:     1,
			ContactSeq: 1,
			At:         timePtr(t0.Add(15 * time.Millisecond)),
		},
	)
	if !changed || flow == nil {
		t.Fatal("expected flow reducer to initialize and record leg 1 + contact 1")
	}

	// 同 ContactSeq 的重复 contact_start 应幂等忽略。
	dupChanged := ApplyRequestFlowEvents(&flow, t0, t0, RequestFlowEvent{
		Kind:       FlowEventContactStart,
		LegSeq:     1,
		ContactSeq: 1,
		At:         timePtr(t0.Add(16 * time.Millisecond)),
	})
	if dupChanged {
		t.Fatal("expected duplicate contact_start to be idempotent")
	}

	status502 := 502
	status200 := 200
	changed = ApplyRequestFlowEvents(&flow, t0, t0,
		RequestFlowEvent{Kind: FlowEventContactConnReady, ContactSeq: 1, At: timePtr(t0.Add(20 * time.Millisecond))},
		RequestFlowEvent{Kind: FlowEventContactWriteStart, ContactSeq: 1, At: timePtr(t0.Add(25 * time.Millisecond))},
		RequestFlowEvent{Kind: FlowEventContactWriteDone, ContactSeq: 1, At: timePtr(t0.Add(30 * time.Millisecond))},
		RequestFlowEvent{Kind: FlowEventContactResponseHeaders, ContactSeq: 1, At: timePtr(t0.Add(40 * time.Millisecond)), HTTPStatusCode: &status502},
		RequestFlowEvent{Kind: FlowEventContactFirstByte, ContactSeq: 1, At: timePtr(t0.Add(42 * time.Millisecond))},
		RequestFlowEvent{Kind: FlowEventContactEnd, ContactSeq: 1, At: timePtr(t0.Add(50 * time.Millisecond)), HTTPStatusCode: &status502, OutcomeCode: "upstream_5xx"},
		RequestFlowEvent{
			Kind:       FlowEventLegSelected,
			LegSeq:     2,
			ReasonCode: "failover",
			At:         timePtr(t0.Add(60 * time.Millisecond)),
			Target: &RequestFlowTargetSnapshot{
				UpstreamType: "account",
				UpstreamName: "acct-b",
				UpstreamID:   102,
				RouteMode:    "pool",
			},
		},
		RequestFlowEvent{Kind: FlowEventContactStart, LegSeq: 2, ContactSeq: 2, At: timePtr(t0.Add(65 * time.Millisecond))},
		RequestFlowEvent{Kind: FlowEventContactEnd, ContactSeq: 2, At: timePtr(t0.Add(120 * time.Millisecond)), HTTPStatusCode: &status200, OutcomeCode: "ok"},
		RequestFlowEvent{Kind: FlowEventDownstreamFirstWrite, At: timePtr(t0.Add(100 * time.Millisecond)), Bytes: 120},
		RequestFlowEvent{Kind: FlowEventDownstreamWrite, At: timePtr(t0.Add(110 * time.Millisecond)), Bytes: 380},
		RequestFlowEvent{Kind: FlowEventUpstreamTerminal, At: timePtr(t0.Add(115 * time.Millisecond))},
		RequestFlowEvent{
			Kind:            FlowEventDownstreamDone,
			At:              timePtr(t0.Add(125 * time.Millisecond)),
			Bytes:           500, // 完成事件包含首写 120 和后续增量 380。
			DeliveryOutcome: FlowDeliveryDelivered,
			FlushError:      true,
		},
		RequestFlowEvent{Kind: FlowEventMarkIncomplete, At: timePtr(t0.Add(126 * time.Millisecond)), IncompleteReason: "downstream_flush_failed"},
	)
	if !changed {
		t.Fatal("expected failover and delivery events to update flow")
	}

	if flow.Transport != FlowTransportWS || flow.Origin != FlowOriginClientRequest {
		t.Fatalf("unexpected transport/origin: %q / %q", flow.Transport, flow.Origin)
	}
	if flow.TotalLegs != 2 || len(flow.Legs) != 2 {
		t.Fatalf("expected 2 legs, got total=%d len=%d", flow.TotalLegs, len(flow.Legs))
	}
	if flow.TotalContacts != 2 || len(flow.Contacts) != 2 {
		t.Fatalf("expected 2 contacts, got total=%d len=%d", flow.TotalContacts, len(flow.Contacts))
	}
	if flow.Contacts[0].HTTPStatusCode == nil || *flow.Contacts[0].HTTPStatusCode != 502 {
		t.Fatalf("expected contact 1 status 502, got %v", flow.Contacts[0].HTTPStatusCode)
	}
	if flow.Contacts[1].LegSeq != 2 || flow.Contacts[1].OutcomeCode != "ok" {
		t.Fatalf("unexpected contact 2 state: %+v", flow.Contacts[1])
	}
	if flow.DownstreamBytesWritten != 500 {
		t.Fatalf("expected downstream bytes 500, got %d", flow.DownstreamBytesWritten)
	}
	if !flow.UpstreamTerminalObserved || flow.UpstreamTerminalOffsetMs == nil || *flow.UpstreamTerminalOffsetMs != 115 {
		t.Fatalf("unexpected upstream terminal state: observed=%v offset=%v", flow.UpstreamTerminalObserved, flow.UpstreamTerminalOffsetMs)
	}
	if flow.DownstreamDeliveryOutcome != FlowDeliveryDelivered || !flow.DownstreamFlushError {
		t.Fatalf("unexpected downstream delivery state: outcome=%q flush_err=%v", flow.DownstreamDeliveryOutcome, flow.DownstreamFlushError)
	}
	if flow.IncompleteReason != "downstream_flush_failed" {
		t.Fatalf("expected incomplete reason downstream_flush_failed, got %q", flow.IncompleteReason)
	}
}

func TestRequestFlow_BoundedTruncation(t *testing.T) {
	t0 := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	var flow *RequestFlow

	events := make([]RequestFlowEvent, 0, 120)
	for leg := 1; leg <= 70; leg++ {
		events = append(events, RequestFlowEvent{
			Kind:       FlowEventLegSelected,
			LegSeq:     leg,
			ReasonCode: "failover",
			At:         timePtr(t0.Add(time.Duration(leg) * time.Millisecond)),
			Target: &RequestFlowTargetSnapshot{
				UpstreamType: "endpoint",
				UpstreamName: "ep-" + string(rune('a'+(leg%26))),
				EndpointName: "ep-test",
			},
		})
	}
	for contact := 1; contact <= 40; contact++ {
		events = append(events, RequestFlowEvent{
			Kind:       FlowEventContactStart,
			LegSeq:     contact,
			ContactSeq: contact,
			At:         timePtr(t0.Add(time.Duration(contact*2) * time.Millisecond)),
		})
	}

	if !ApplyRequestFlowEvents(&flow, t0, t0, events...) {
		t.Fatal("expected flow events to apply")
	}
	if !flow.Truncated {
		t.Fatal("expected Truncated=true when exceeding MaxFlowLegs and MaxFlowContacts")
	}
	if flow.TotalLegs != 70 || flow.TotalContacts != 40 {
		t.Fatalf("expected totalLegs=70 totalContacts=40, got %d / %d", flow.TotalLegs, flow.TotalContacts)
	}
	if len(flow.Legs) > MaxFlowLegs || len(flow.Contacts) > MaxFlowContacts {
		t.Fatalf("expected bounded lengths <= (%d, %d), got (%d, %d)", MaxFlowLegs, MaxFlowContacts, len(flow.Legs), len(flow.Contacts))
	}
	if flow.OmittedLegs != 70-len(flow.Legs) || flow.OmittedLegs <= 0 {
		t.Fatalf("unexpected OmittedLegs=%d (len=%d)", flow.OmittedLegs, len(flow.Legs))
	}
	if flow.OmittedContacts != 40-len(flow.Contacts) || flow.OmittedContacts <= 0 {
		t.Fatalf("unexpected OmittedContacts=%d (len=%d)", flow.OmittedContacts, len(flow.Contacts))
	}

	raw, err := MarshalRequestFlowBounded(flow)
	if err != nil {
		t.Fatalf("MarshalRequestFlowBounded failed: %v", err)
	}
	if len(raw) > MaxFlowJSONBytes {
		t.Fatalf("expected marshaled JSON <= %d bytes, got %d", MaxFlowJSONBytes, len(raw))
	}
	parsed, integrity := ParseRequestFlowWhitelist(raw, true)
	if parsed == nil || integrity.CorruptFlowJSON || !integrity.Truncated {
		t.Fatalf("unexpected round-trip parse result: flow=%v integrity=%+v", parsed, integrity)
	}
}

func TestParseRequestFlowWhitelist_IntegrityAndPreservesSQLite(t *testing.T) {
	if flow, integrity := ParseRequestFlowWhitelist("", false); flow != nil || integrity.RecordMissingFlow {
		t.Fatalf("expected empty integrity when hasRow=false, got flow=%v integrity=%+v", flow, integrity)
	}
	if flow, integrity := ParseRequestFlowWhitelist("", true); flow != nil || !integrity.RecordMissingFlow {
		t.Fatalf("expected RecordMissingFlow when hasRow=true and raw empty, got flow=%v integrity=%+v", flow, integrity)
	}
	if flow, integrity := ParseRequestFlowWhitelist("{corrupt-json", true); flow != nil || !integrity.CorruptFlowJSON {
		t.Fatalf("expected CorruptFlowJSON for invalid JSON, got flow=%v integrity=%+v", flow, integrity)
	}
	if _, integrity := ParseRequestFlowWhitelist(`{"schema_version":999,"transport":"http"}`, true); !integrity.UnsupportedSchema {
		t.Fatalf("expected UnsupportedSchema for future schema version, got %+v", integrity)
	}

	tracker := newRequestLifecycleTestTracker(t, false, 50*time.Millisecond)
	ctx := context.Background()

	const requestID = "req-corrupt-flow"
	tracker.RecordRequestStart(requestID, "127.0.0.1", "agent", "POST", "/v1/messages", true)
	tracker.RecordRequestSuccessWithCost(requestID, "claude-sonnet-4-20250514", 35*time.Millisecond, 0.12)

	deadline := time.Now().Add(5 * time.Second)
	for {
		var count int
		err := tracker.readDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM request_logs WHERE request_id = ?", requestID).Scan(&count)
		if err == nil && count == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s in SQLite", requestID)
		}
		time.Sleep(20 * time.Millisecond)
	}

	const rawCorrupt = "{not-valid-flow-json"
	if _, err := tracker.writeDB.ExecContext(ctx, "UPDATE request_logs SET request_flow_json = ? WHERE request_id = ?", rawCorrupt, requestID); err != nil {
		t.Fatalf("failed to inject corrupt request_flow_json: %v", err)
	}

	detail, err := tracker.GetRequestLifecycleData(ctx, requestID)
	if err != nil {
		t.Fatalf("GetRequestLifecycleData failed: %v", err)
	}
	if detail == nil || !detail.Integrity.CorruptFlowJSON {
		t.Fatalf("expected CorruptFlowJSON in lifecycle detail, got %+v", detail)
	}

	var stored string
	if err := tracker.readDB.QueryRowContext(ctx, "SELECT COALESCE(request_flow_json, '') FROM request_logs WHERE request_id = ?", requestID).Scan(&stored); err != nil {
		t.Fatalf("failed to re-read request_flow_json: %v", err)
	}
	if stored != rawCorrupt {
		t.Fatalf("expected corrupt SQLite JSON to be preserved verbatim, got %q", stored)
	}
}

func TestRequestFlow_SQLiteDetailAndRevision(t *testing.T) {
	tr := newRequestLifecycleTestTracker(t, false, 50*time.Millisecond)
	const id = "sqlite-flow-revision"
	read := func(after int64) *RequestLifecycleData {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			data, err := tr.GetRequestLifecycleData(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if data != nil && data.RequestRevision > after {
				return data
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("等待 SQLite 详情版本超过 %d 超时", after)
		return nil
	}
	tr.RecordRequestStart(id, "", "", "POST", "/v1/responses", true)
	start := read(0)
	tr.RecordRequestUpdate(id, UpdateOptions{FlowEvents: []RequestFlowEvent{
		{Kind: FlowEventLegSelected, LegSeq: 1, Target: &RequestFlowTargetSnapshot{UpstreamType: "account", UpstreamID: 11}},
		{Kind: FlowEventContactStart, LegSeq: 1, ContactSeq: 1},
	}})
	update := read(start.RequestRevision)
	tr.RecordRequestSuccessWithCost(id, "test-model", time.Millisecond, 0.1)
	done := read(update.RequestRevision)
	if done.Source != "database" || done.Detail.Status != "completed" || done.Flow == nil || len(done.Flow.Contacts) != 1 || len(done.Flow.Legs) != 1 || done.Flow.Legs[0].TargetSnapshot.UpstreamID != 11 {
		t.Fatalf("SQLite 详情缺少结算或轨迹：%+v", done)
	}
}

func TestRequestFlow_OnlySingleRequestSnapshotCopiesTrail(t *testing.T) {
	tracker := newRequestLifecycleTestTracker(t, true, time.Hour)
	const id = "detail-only"
	tracker.RecordRequestStart(id, "", "", "POST", "/v1/messages", true)
	tracker.recordIntegrityIssue(id, "event_channel_full_update")
	rows := tracker.GetActiveRequests()
	if len(rows) != 1 || rows[0].RequestFlow != nil || rows[0].RequestFlowJSON != "" || rows[0].RequestRevision != 0 {
		t.Fatal("活跃列表复制了详情专用轨迹")
	}
	detail, err := tracker.GetRequestLifecycleData(context.Background(), id)
	if err != nil || detail == nil || detail.Flow == nil || detail.RequestRevision == 0 || !detail.Integrity.WriteGap {
		t.Fatalf("请求详情缺少热池轨迹: detail=%+v err=%v", detail, err)
	}
	detail.Flow.IncompleteReason = "modified-copy"
	next, err := tracker.GetRequestLifecycleData(context.Background(), id)
	if err != nil || next == nil || next.Flow == nil || next.Flow.IncompleteReason != "event_channel_full_update" {
		t.Fatalf("详情与热池共享了可变状态: detail=%+v err=%v", next, err)
	}
}

func TestRequestFlow_WriteGapStaysWithAffectedRequest(t *testing.T) {
	tracker := newRequestLifecycleTestTracker(t, false, 50*time.Millisecond)
	const affected = "request-with-gap"
	const other = "request-without-gap"
	const code = "event_channel_full_update"
	tracker.recordIntegrityIssue(affected, code)
	for _, id := range []string{affected, other} {
		tracker.RecordRequestStart(id, "", "", "POST", "/v1/messages", true)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		detail, err := tracker.GetRequestLifecycleData(context.Background(), affected)
		if err != nil {
			t.Fatal(err)
		}
		clean, err := tracker.GetRequestLifecycleData(context.Background(), other)
		if err != nil {
			t.Fatal(err)
		}
		if detail != nil && clean != nil && tracker.getPendingRequestGap(affected) == "" {
			if detail.Flow == nil || detail.Flow.IncompleteReason != code || !detail.Integrity.WriteGap || clean.Integrity.WriteGap {
				t.Fatalf("写入缺口归属有误: affected=%+v clean=%+v", detail, clean)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("等待写入缺口持久化超时")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func timePtr(t time.Time) *time.Time {
	return &t
}

func TestRequestFlow_HotPoolRevisionVisibleBeforeArchive(t *testing.T) {
	tracker := newRequestLifecycleTestTracker(t, true, time.Hour)
	const id = "live-revision"
	revision := func(status string) int64 {
		t.Helper()
		data, err := tracker.GetRequestLifecycleData(context.Background(), id)
		if err != nil || data == nil || data.Detail.Status != status {
			t.Fatalf("期望状态 %s，详情 %+v，错误 %v", status, data, err)
		}
		return data.RequestRevision
	}
	tracker.RecordRequestStart(id, "", "", "POST", "/v1/messages", true)
	start := revision("pending")
	opts := UpdateOptions{FlowEvents: []RequestFlowEvent{{Kind: FlowEventContactStart, ContactSeq: 1}}}
	tracker.RecordRequestUpdate(id, opts)
	updated := revision("pending")
	tracker.RecordRequestUpdate(id, opts)
	if start < 1 || updated <= start || revision("pending") != updated {
		t.Fatal("热池轨迹版本没有按实际变更递增")
	}
	tracker.RecordRequestSuccessWithCost(id, "model", time.Millisecond, 0.25)
	if revision("completed") <= updated {
		t.Fatal("结算未递增请求版本")
	}
}

func TestRequestFlow_ByteBudgetPreservesFirstLastAndReferencedLegs(t *testing.T) {
	at := time.Now()
	var flow *RequestFlow
	label := strings.Repeat("测", 150)
	for leg := 1; leg <= 42; leg++ {
		dest := RequestFlowTargetSnapshot{EndpointName: label, UpstreamName: label, UpstreamID: int64(leg)}
		ApplyRequestFlowEvents(&flow, at, at, RequestFlowEvent{Kind: FlowEventLegSelected, LegSeq: leg, Target: &dest})
		if leg > 1 && leg < 42 {
			ApplyRequestFlowEvents(&flow, at, at, RequestFlowEvent{Kind: FlowEventContactStart, ContactSeq: leg - 1, LegSeq: leg})
		}
	}
	raw, err := MarshalRequestFlowBounded(flow)
	if err != nil {
		t.Fatal(err)
	}
	saved, _ := ParseRequestFlowWhitelist(raw, true)
	if len(raw) > MaxFlowJSONBytes || saved == nil || !saved.Truncated || len(saved.Contacts) < 2 || len(saved.Legs) < 2 {
		t.Fatalf("轨迹压缩失败: %d 字节", len(raw))
	}
	if saved.Contacts[0].ContactSeq != 1 || saved.Contacts[len(saved.Contacts)-1].ContactSeq != 40 || saved.Legs[0].LegSeq != 1 || saved.Legs[len(saved.Legs)-1].LegSeq != 42 ||
		saved.OmittedContacts != 40-len(saved.Contacts) || saved.OmittedLegs != 42-len(saved.Legs) {
		t.Fatalf("压缩后首尾或计数有误: %+v", saved)
	}
	kept := map[int]bool{}
	for _, leg := range saved.Legs {
		kept[leg.LegSeq] = true
	}
	for _, c := range saved.Contacts {
		if !kept[c.LegSeq] {
			t.Fatalf("上游联系 %d 丢失目标段 %d", c.ContactSeq, c.LegSeq)
		}
	}
}

func TestRequestFlow_ByteBudgetWithOnlyRequiredLegs(t *testing.T) {
	label := strings.Repeat("测", MaxFlowJSONBytes/3+1)
	flow := &RequestFlow{
		IncompleteReason: label,
		Legs: []RequestFlowLeg{
			{LegSeq: 1},
			{LegSeq: 2, TargetSnapshot: RequestFlowTargetSnapshot{EndpointName: label}},
			{LegSeq: 3},
			{LegSeq: 4},
		},
		Contacts: []RequestFlowContact{{ContactSeq: 1, LegSeq: 2}, {ContactSeq: 2, LegSeq: 3}},
	}
	raw, err := MarshalRequestFlowBounded(flow)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > MaxFlowJSONBytes {
		t.Fatalf("剩余字段裁切后仍有 %d 字节", len(raw))
	}
	var saved RequestFlow
	if err := json.Unmarshal([]byte(raw), &saved); err != nil {
		t.Fatal(err)
	}
	if !saved.Truncated || saved.OmittedLegs != 0 || saved.OmittedContacts != 0 || len(saved.Legs) != 4 || len(saved.Contacts) != 2 {
		t.Fatal("字段裁切应标记截断并保留全部必需的选路和联系")
	}
	for i, leg := range saved.Legs {
		if leg.LegSeq != flow.Legs[i].LegSeq {
			t.Fatalf("第 %d 个选路段编号变成了 %d", i+1, leg.LegSeq)
		}
	}
	for i, contact := range saved.Contacts {
		if contact != flow.Contacts[i] {
			t.Fatalf("第 %d 条联系发生变化：%+v", i+1, contact)
		}
	}
	if saved.IncompleteReason != strings.Repeat("测", 128) || saved.Legs[1].TargetSnapshot.EndpointName != strings.Repeat("测", 160) {
		t.Fatal("持久化摘要未保留指定长度的字段前缀")
	}
}

func TestRequestFlow_TimeoutArchiveKeepsLateSettlement(t *testing.T) {
	tr := newRequestLifecycleTestTracker(t, true, time.Hour)
	const id = "timeout-settlement"
	err := tr.RecordRequestStartAtWithDimensions(id, "", "", "POST", "/v1/responses", RequestFamilyCodex, true, time.Now().Add(-time.Hour), RequestDimensions{
		RequestSource: RequestSourceCodexSubscriptionProxy, RequestKind: RequestKindGeneration, FlowTransport: FlowTransportWS,
	})
	if err != nil {
		t.Fatal(err)
	}
	var expired *ActiveRequest
	tr.hotPool.SetArchiveCallback(func(req *ActiveRequest) { expired = req })
	tr.hotPool.cleanup()
	snapshot, ok := tr.hotPool.SnapshotRequest(id)
	if expired == nil || !ok || snapshot.Status != "timeout" || snapshot.RequestRevision < 2 {
		t.Fatalf("超时归档期间的版本错误：%+v", snapshot)
	}
	tr.RecordRequestUpdate(id, UpdateOptions{FlowEvents: []RequestFlowEvent{{Kind: FlowEventDownstreamDone, Bytes: 92, DeliveryOutcome: FlowDeliveryDelivered}}})
	if err := tr.RecordRequestSettlement(id, RequestSettlement{Status: "completed", TokenUsage: &TokenUsage{OutputTokens: 67}}); err != nil {
		t.Fatal(err)
	}
	if err := tr.archiveManager.flushBatch([]*ArchiveEvent{{Request: expired, Timestamp: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	data, err := tr.GetRequestLifecycleData(context.Background(), id)
	if err != nil || data == nil || data.Source != "database" || data.Detail.Status != "completed" || !data.Detail.UsageKnown || data.Detail.OutputTokens != 67 || data.Flow == nil || data.Flow.DownstreamBytesWritten != 92 || data.RequestRevision <= snapshot.RequestRevision {
		t.Fatalf("迟到结算与交付未随归档提交：%+v，%v", data, err)
	}
}

func TestRequestFlow_RegistrationFullOrClosed(t *testing.T) {
	tr, err := NewUsageTracker(&Config{Enabled: true, DatabasePath: ":memory:", BufferSize: 1, BatchSize: 1, FlushInterval: time.Hour, MaxRetry: 1, CleanupInterval: time.Hour, RetentionDays: 30, HotPool: &HotPoolSettings{Enabled: false}})
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	db := tr.GetWriteDB()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	waitCount := db.Stats().WaitCount
	tr.RecordRequestStart("first", "", "", "POST", "/v1/responses", true)
	deadline := time.Now().Add(2 * time.Second)
	for db.Stats().WaitCount == waitCount {
		if time.Now().After(deadline) {
			t.Fatal("消费者没有等待 SQLite 写连接")
		}
		time.Sleep(time.Millisecond)
	}
	register := func(id string) error {
		return tr.RecordRequestStartAtWithDimensions(id, "", "", "POST", "/v1/responses", RequestFamilyCodex, true, time.Now(), RequestDimensions{RequestSource: RequestSourceLegacyProxy, RequestKind: RequestKindLegacy})
	}
	if err := register("queued"); err != nil {
		t.Fatal(err)
	}
	if err := register("excess"); err != errEventBufferFull {
		t.Fatalf("队列满时登记错误 = %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	if err := register("closed"); err == nil {
		t.Fatal("追踪器关闭后仍然接受登记")
	}
}

func TestRequestFlow_ByteBudgetPreservesRecentContactsAndTheirLegs(t *testing.T) {
	now := time.Now()
	label := strings.Repeat("测", 150)
	var flow *RequestFlow
	for seq := 1; seq <= 40; seq++ {
		target := RequestFlowTargetSnapshot{EndpointName: label, UpstreamName: label, UpstreamID: int64(seq)}
		ApplyRequestFlowEvents(&flow, now, now, RequestFlowEvent{Kind: FlowEventLegSelected, LegSeq: seq, Target: &target}, RequestFlowEvent{Kind: FlowEventContactStart, LegSeq: seq, ContactSeq: seq})
	}
	raw, err := MarshalRequestFlowBounded(flow)
	if err != nil {
		t.Fatal(err)
	}
	saved, integrity := ParseRequestFlowWhitelist(raw, true)
	if len(raw) > MaxFlowJSONBytes || saved == nil || !integrity.Truncated || len(saved.Contacts) < 16 || saved.Contacts[0].ContactSeq != 1 || saved.Contacts[len(saved.Contacts)-1].ContactSeq != 40 || integrity.OmittedContacts != 40-len(saved.Contacts) || integrity.OmittedLegs != 40-len(saved.Legs) {
		t.Fatalf("裁剪错误：%d 字节，%+v", len(raw), integrity)
	}
	legs := make(map[int]bool)
	for _, leg := range saved.Legs {
		legs[leg.LegSeq] = true
	}
	for _, contact := range saved.Contacts {
		if !legs[contact.LegSeq] {
			t.Fatalf("联系缺少选路：%+v", contact)
		}
	}
}
