package proxy

import (
	"context"
	"errors"
	"net/http/httptrace"
	"testing"
	"time"

	"sinuo/internal/endpoint"
	"sinuo/internal/events"
	"sinuo/internal/proxy/handlers"
	timezonepolicy "sinuo/internal/timezone"
	"sinuo/internal/tracking"
)

// newLifecyclePanelTestTracker 构造启用热池的内存 tracker。
func newLifecyclePanelTestTracker(t *testing.T) *tracking.UsageTracker {
	t.Helper()
	return newTimingTestTracker(t, true)
}

// newTimingTestTracker 热池关闭时更新经事件队列异步落 SQLite，用于验证直写路径。
func newTimingTestTracker(t *testing.T, hotPoolEnabled bool) *tracking.UsageTracker {
	t.Helper()
	tracker, err := tracking.NewUsageTracker(&tracking.Config{
		Enabled:         true,
		DatabasePath:    ":memory:",
		BufferSize:      10,
		BatchSize:       5,
		FlushInterval:   50 * time.Millisecond,
		MaxRetry:        3,
		CleanupInterval: 24 * time.Hour,
		RetentionDays:   30,
		HotPool: &tracking.HotPoolSettings{
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

// TestUpstreamWriteMs_PersistedBeforeFirstToken 首字前收到写完回调 → 立即持久化偏移量。
func TestUpstreamWriteMs_PersistedBeforeFirstToken(t *testing.T) {
	tracker := newLifecyclePanelTestTracker(t)

	rlm := NewRequestLifecycleManager(tracker, nil, "req-upstream-write-ok", nil)
	rlm.StartRequest("127.0.0.1", "test-agent", "POST", "/v1/responses", true)

	time.Sleep(10 * time.Millisecond)
	rlm.SetFirstTokenStartTime(time.Now())

	beforeFirstToken, err := tracker.GetRequestLifecycleData(context.Background(), "req-upstream-write-ok")
	if err != nil {
		t.Fatalf("GetRequestLifecycleData before first token failed: %v", err)
	}
	if beforeFirstToken == nil || beforeFirstToken.UpstreamWriteMs == nil {
		t.Fatalf("expected upstream_write_ms persisted before first token, got %+v", beforeFirstToken)
	}
	if beforeFirstToken.Detail.FirstTokenMs != nil {
		t.Fatalf("first_token_ms must still be nil before first token, got %d", *beforeFirstToken.Detail.FirstTokenMs)
	}

	rlm.RecordFirstToken()

	data, err := tracker.GetRequestLifecycleData(context.Background(), "req-upstream-write-ok")
	if err != nil {
		t.Fatalf("GetRequestLifecycleData failed: %v", err)
	}
	if data == nil {
		t.Fatal("expected lifecycle data in hot pool")
	}
	if data.Source != "hot_pool" {
		t.Fatalf("expected hot_pool source, got %q", data.Source)
	}
	if data.Detail.FirstTokenMs == nil {
		t.Fatal("expected first_token_ms recorded")
	}
	if data.UpstreamWriteMs == nil {
		t.Fatal("expected upstream_write_ms retained after first token")
	}
	if *data.UpstreamWriteMs < 0 {
		t.Fatalf("upstream_write_ms must be >= 0, got %d", *data.UpstreamWriteMs)
	}
}

// TestFirstTokenStartTime_RetryUsesLatestWrite 首响前重试同时更新 FRT 起点与最终写完偏移。
func TestFirstTokenStartTime_RetryUsesLatestWrite(t *testing.T) {
	tracker := newLifecyclePanelTestTracker(t)
	const requestID = "req-upstream-write-retry"

	rlm := NewRequestLifecycleManager(tracker, nil, requestID, nil)
	rlm.StartRequest("127.0.0.1", "test-agent", "POST", "/v1/responses", true)

	// 用可控时间差明确区分首次尝试（约 1.9s 前）和最终尝试（约 50ms 前）。
	rlm.startTime = time.Now().Add(-2 * time.Second)
	firstWrite := rlm.startTime.Add(100 * time.Millisecond)
	rlm.SetFirstTokenStartTime(firstWrite)

	firstData, err := tracker.GetRequestLifecycleData(context.Background(), requestID)
	if err != nil {
		t.Fatalf("GetRequestLifecycleData after first write failed: %v", err)
	}
	if firstData == nil || firstData.UpstreamWriteMs == nil {
		t.Fatalf("expected first upstream_write_ms, got %+v", firstData)
	}
	firstUpstreamWriteMs := *firstData.UpstreamWriteMs
	if firstUpstreamWriteMs != 100 {
		t.Fatalf("expected first upstream_write_ms to stay at 100ms, got %d", firstUpstreamWriteMs)
	}

	latestWrite := time.Now().Add(-50 * time.Millisecond)
	rlm.SetFirstTokenStartTime(latestWrite)

	rlm.timingMu.RLock()
	actualStart := rlm.firstTokenStartTime
	rlm.timingMu.RUnlock()
	if !actualStart.Equal(latestWrite) {
		t.Fatalf("expected latest successful write as FRT start, got %s want %s", actualStart, latestWrite)
	}

	latestData, err := tracker.GetRequestLifecycleData(context.Background(), requestID)
	if err != nil {
		t.Fatalf("GetRequestLifecycleData after retry write failed: %v", err)
	}
	if latestData == nil || latestData.UpstreamWriteMs == nil {
		t.Fatalf("expected upstream_write_ms retained, got %+v", latestData)
	}
	if want := latestWrite.Sub(rlm.startTime).Milliseconds(); *latestData.UpstreamWriteMs != want {
		t.Fatalf("retry must overwrite upstream_write_ms before first token, got %d want %d", *latestData.UpstreamWriteMs, want)
	}

	rlm.RecordFirstToken()
	finalData, err := tracker.GetRequestLifecycleData(context.Background(), requestID)
	if err != nil {
		t.Fatalf("GetRequestLifecycleData after first token failed: %v", err)
	}
	if finalData == nil || finalData.Detail.FirstTokenMs == nil {
		t.Fatalf("expected first_token_ms recorded, got %+v", finalData)
	}
	if *finalData.Detail.FirstTokenMs >= 500 {
		t.Fatalf("first_token_ms must use latest retry write, got %dms", *finalData.Detail.FirstTokenMs)
	}
}

// TestUpstreamWriteMs_PersistedWhenRequestFailsBeforeFirstToken 写完后首字前失败仍保留连接阶段数据。
func TestUpstreamWriteMs_PersistedWhenRequestFailsBeforeFirstToken(t *testing.T) {
	tracker := newLifecyclePanelTestTracker(t)
	const requestID = "req-upstream-write-failed"

	rlm := NewRequestLifecycleManager(tracker, nil, requestID, nil)
	rlm.StartRequest("127.0.0.1", "test-agent", "POST", "/v1/responses", true)
	rlm.SetFirstTokenStartTime(time.Now())
	rlm.FailRequest("upstream_error", "failed before first token", 502)

	data, err := tracker.GetRequestLifecycleData(context.Background(), requestID)
	if err != nil {
		t.Fatalf("GetRequestLifecycleData failed: %v", err)
	}
	if data == nil || data.UpstreamWriteMs == nil {
		t.Fatalf("expected upstream_write_ms on pre-first-token failure, got %+v", data)
	}
	if data.Detail.FirstTokenMs != nil {
		t.Fatalf("expected first_token_ms nil on pre-first-token failure, got %d", *data.Detail.FirstTokenMs)
	}
}

// TestUpstreamWriteMs_IgnoresFailedWroteRequest 写请求报错不能被当作完整写出。
func TestUpstreamWriteMs_IgnoresFailedWroteRequest(t *testing.T) {
	tracker := newLifecyclePanelTestTracker(t)
	const requestID = "req-upstream-write-error"

	rlm := NewRequestLifecycleManager(tracker, nil, requestID, nil)
	rlm.StartRequest("127.0.0.1", "test-agent", "POST", "/v1/responses", true)
	traceCtx, _ := handlers.WithUpstreamTrace(context.Background(), rlm.RecordUpstreamWrite)
	trace := httptrace.ContextClientTrace(traceCtx)
	trace.WroteRequest(httptrace.WroteRequestInfo{Err: errors.New("partial write")})

	data, err := tracker.GetRequestLifecycleData(context.Background(), requestID)
	if err != nil {
		t.Fatalf("GetRequestLifecycleData failed: %v", err)
	}
	if data == nil {
		t.Fatal("expected lifecycle data")
	}
	if data.UpstreamWriteMs != nil {
		t.Fatalf("expected failed WroteRequest to leave upstream_write_ms nil, got %d", *data.UpstreamWriteMs)
	}
}

// TestUpstreamWriteMs_NilWhenCallbackNeverReceived 未收到写完回调 → 不带出，避免误写约 0ms。
func TestUpstreamWriteMs_NilWhenCallbackNeverReceived(t *testing.T) {
	tracker := newLifecyclePanelTestTracker(t)

	rlm := NewRequestLifecycleManager(tracker, nil, "req-upstream-write-miss", nil)
	rlm.StartRequest("127.0.0.1", "test-agent", "POST", "/v1/responses", true)

	rlm.RecordFirstToken()

	data, err := tracker.GetRequestLifecycleData(context.Background(), "req-upstream-write-miss")
	if err != nil {
		t.Fatalf("GetRequestLifecycleData failed: %v", err)
	}
	if data == nil {
		t.Fatal("expected lifecycle data in hot pool")
	}
	if data.Detail.FirstTokenMs == nil {
		t.Fatal("expected first_token_ms recorded")
	}
	if data.UpstreamWriteMs != nil {
		t.Fatalf("expected upstream_write_ms nil, got %d", *data.UpstreamWriteMs)
	}
}

// TestUpstreamWriteMs_LateCallbackIgnored 首字后异常晚到回调 → 忽略且不补写。
func TestUpstreamWriteMs_LateCallbackIgnored(t *testing.T) {
	tracker := newLifecyclePanelTestTracker(t)

	rlm := NewRequestLifecycleManager(tracker, nil, "req-upstream-write-late", nil)
	rlm.StartRequest("127.0.0.1", "test-agent", "POST", "/v1/responses", true)

	rlm.RecordFirstToken()
	rlm.SetFirstTokenStartTime(time.Now()) // 晚于首字，保守忽略

	data, err := tracker.GetRequestLifecycleData(context.Background(), "req-upstream-write-late")
	if err != nil {
		t.Fatalf("GetRequestLifecycleData failed: %v", err)
	}
	if data == nil {
		t.Fatal("expected lifecycle data in hot pool")
	}
	if data.UpstreamWriteMs != nil {
		t.Fatalf("expected upstream_write_ms nil after late callback, got %d", *data.UpstreamWriteMs)
	}
}

// TestRouteDecisionAt_IsRequestScoped 两个请求交错更新时，各自只能持久化自己的决策快照。
func TestRouteDecisionAt_IsRequestScoped(t *testing.T) {
	tracker := newLifecyclePanelTestTracker(t)
	ctx := context.Background()

	requestA := NewRequestLifecycleManager(tracker, nil, "req-route-a", nil)
	requestB := NewRequestLifecycleManager(tracker, nil, "req-route-b", nil)
	requestA.StartRequest("127.0.0.1", "test-agent", "POST", "/v1/messages", true)
	requestB.StartRequest("127.0.0.1", "test-agent", "POST", "/v1/messages", true)

	decisionA := time.Now()
	requestA.SetEndpointAttempt("fallback-a", 1)
	requestA.SetRouteDecision(endpoint.RouteOverrideState{
		Mode:           endpoint.RouteModeManualPreferred,
		EndpointName:   "preferred-a",
		LastDecisionAt: decisionA,
	}, "fallback-a")

	decisionB := decisionA.Add(25 * time.Millisecond)
	requestB.SetEndpointAttempt("endpoint-b", 2)
	requestB.SetRouteDecision(endpoint.RouteOverrideState{
		Mode:           endpoint.RouteModeAuto,
		LastDecisionAt: decisionB,
	}, "endpoint-b")

	// B 后写，A 再更新状态；A 仍必须使用自己的请求级决策时间。
	requestB.UpdateStatus("forwarding", 0, 0)
	requestA.UpdateStatus("forwarding", 0, 0)

	dataA, err := tracker.GetRequestLifecycleData(ctx, "req-route-a")
	if err != nil {
		t.Fatalf("GetRequestLifecycleData A failed: %v", err)
	}
	dataB, err := tracker.GetRequestLifecycleData(ctx, "req-route-b")
	if err != nil {
		t.Fatalf("GetRequestLifecycleData B failed: %v", err)
	}
	if dataA == nil || dataA.Detail.RouteDecisionAt == nil || !dataA.Detail.RouteDecisionAt.Equal(decisionA) {
		t.Fatalf("expected request A decision %s, got %+v", decisionA, dataA)
	}
	if dataB == nil || dataB.Detail.RouteDecisionAt == nil || !dataB.Detail.RouteDecisionAt.Equal(decisionB) {
		t.Fatalf("expected request B decision %s, got %+v", decisionB, dataB)
	}
	if dataA.Detail.RouteMode != endpoint.RouteModeManualPreferred ||
		dataA.Detail.RequestedEndpoint != "preferred-a" ||
		dataA.Detail.EffectiveEndpoint != "fallback-a" ||
		dataA.Detail.FallbackReason != "manual_preferred_fallback" {
		t.Fatalf("unexpected request A route diagnostics: %+v", dataA.Detail)
	}
}

// timingEvents 按变更类型筛出计时事件。
func timingEvents(published []events.Event, changeType string) []events.Event {
	var matched []events.Event
	for _, event := range published {
		if event.Type == events.EventRequestUpdated && event.Data["change_type"] == changeType {
			matched = append(matched, event)
		}
	}
	return matched
}

// queryHotPoolDetail 从热池读取进行中请求的列表口径数据。
func queryHotPoolDetail(t *testing.T, tracker *tracking.UsageTracker, requestID string) tracking.RequestDetail {
	t.Helper()
	rows, _, err := tracker.QueryRequestDetailsWithHotPool(context.Background(), &tracking.QueryOptions{RequestID: requestID, UpstreamType: "all", Limit: 1})
	if err != nil || len(rows) != 1 {
		t.Fatalf("query hot pool detail: count=%d err=%v", len(rows), err)
	}
	return rows[0]
}

// TestSetFirstTokenStartTime_TracksLatestAttemptAndPublishes 首响起点随重试后移并逐次通知；首响落定后不再变。
func TestSetFirstTokenStartTime_TracksLatestAttemptAndPublishes(t *testing.T) {
	tracker := newLifecyclePanelTestTracker(t)
	bus := &subscriptionPresentationEventBus{}
	const requestID = "req-upstream-sent-event"

	rlm := NewRequestLifecycleManager(tracker, nil, requestID, bus)
	rlm.StartRequest("127.0.0.1", "test-agent", "POST", "/v1/messages", true)
	rlm.SetFirstTokenStartTime(time.Now().Add(-time.Second))
	latestWrite := time.Now().Add(-100 * time.Millisecond)
	rlm.SetFirstTokenStartTime(latestWrite)

	sent := timingEvents(bus.published, requestChangeUpstreamSent)
	if len(sent) != 2 || sent[1].Data["first_token_start_at"] != timezonepolicy.FormatStorage(latestWrite) {
		t.Fatalf("upstream_sent events should follow each attempt, got %+v", sent)
	}
	if detail := queryHotPoolDetail(t, tracker, requestID); detail.FirstTokenStartAt == nil || !detail.FirstTokenStartAt.Equal(latestWrite) {
		t.Fatalf("first_token_start_at = %v, want latest write %s", detail.FirstTokenStartAt, latestWrite)
	}

	rlm.RecordFirstToken()
	rlm.SetFirstTokenStartTime(time.Now())
	if got := len(timingEvents(bus.published, requestChangeUpstreamSent)); got != 2 {
		t.Fatalf("write callback after first token must be ignored, upstream_sent events = %d", got)
	}
	detail := queryHotPoolDetail(t, tracker, requestID)
	if detail.FirstTokenStartAt == nil || !detail.FirstTokenStartAt.Equal(latestWrite) {
		t.Fatalf("first_token_start_at moved after first token: %v", detail.FirstTokenStartAt)
	}
	// 首响 = 首字时刻 - 起点：前端拿这两个时刻跑秒，定格时才不会跳
	if detail.FirstTokenMs == nil || detail.FirstTokenAt == nil || detail.FirstTokenAt.Sub(latestWrite).Milliseconds() != *detail.FirstTokenMs {
		t.Fatalf("first token timing inconsistent with start anchor: ms=%v at=%v", detail.FirstTokenMs, detail.FirstTokenAt)
	}
}

// TestRecordFirstToken_PublishesOnceWithTiming 首响只通知一次，事件里的数值与热池一致。
func TestRecordFirstToken_PublishesOnceWithTiming(t *testing.T) {
	tracker := newLifecyclePanelTestTracker(t)
	bus := &subscriptionPresentationEventBus{}
	const requestID = "req-first-response-event"

	rlm := NewRequestLifecycleManager(tracker, nil, requestID, bus)
	rlm.StartRequest("127.0.0.1", "test-agent", "POST", "/v1/messages", true)
	rlm.SetFirstTokenStartTime(time.Now())
	rlm.RecordFirstToken()
	rlm.RecordFirstToken()

	firstResponses := timingEvents(bus.published, requestChangeFirstResponse)
	if len(firstResponses) != 1 {
		t.Fatalf("first_response events = %d, want 1", len(firstResponses))
	}
	detail := queryHotPoolDetail(t, tracker, requestID)
	if detail.FirstTokenMs == nil || detail.FirstTokenAt == nil {
		t.Fatalf("expected first token timing in hot pool, got ms=%v at=%v", detail.FirstTokenMs, detail.FirstTokenAt)
	}
	data := firstResponses[0].Data
	if data["first_token_ms"] != *detail.FirstTokenMs || data["first_token_at"] != timezonepolicy.FormatStorage(*detail.FirstTokenAt) {
		t.Fatalf("event timing %+v differs from hot pool ms=%d at=%s", data, *detail.FirstTokenMs, detail.FirstTokenAt)
	}
}

// TestRecordStreamCompletion_PublishesCompletionMs 生成耗时随事件推送，数值与热池一致。
func TestRecordStreamCompletion_PublishesCompletionMs(t *testing.T) {
	tracker := newLifecyclePanelTestTracker(t)
	bus := &subscriptionPresentationEventBus{}
	const requestID = "req-generation-completed-event"

	rlm := NewRequestLifecycleManager(tracker, nil, requestID, bus)
	rlm.StartRequest("127.0.0.1", "test-agent", "POST", "/v1/messages", true)
	rlm.RecordFirstToken()
	time.Sleep(5 * time.Millisecond)
	rlm.RecordStreamCompletion()

	completed := timingEvents(bus.published, requestChangeGenerationCompleted)
	detail := queryHotPoolDetail(t, tracker, requestID)
	if len(completed) != 1 || detail.CompletionMs == nil || completed[0].Data["completion_ms"] != *detail.CompletionMs {
		t.Fatalf("generation_completed events = %+v, hot pool completion = %v", completed, detail.CompletionMs)
	}
}
