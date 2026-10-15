package proxy

import (
	"context"
	"strings"
	"testing"
	"time"

	"sinuo/internal/proxy/handlers"
	"sinuo/internal/reasoning"
	timezonepolicy "sinuo/internal/timezone"
	"sinuo/internal/tracking"
)

func TestSubscriptionHTTPTrackingKnownAndUnknownSettlement(t *testing.T) {
	tracker := newSubscriptionUnitTracker(t)
	observer := newSubscriptionTrackingObserver(tracker, nil)
	started := time.Now().Add(-20 * time.Millisecond)

	known := observer.StartHTTP(SubscriptionHTTPStart{
		RequestID: "http-known", StartedAt: started, Method: "POST", Path: "/v1/responses",
		RequestKind: tracking.RequestKindGeneration, IsStreaming: false,
	})
	known.SetModel("synthetic-model")
	known.SetStreaming(true)
	known.SetHTTPStatus(201)
	known.MarkOutbound(41, handlers.UpstreamWriteTiming{WroteAt: started.Add(2 * time.Millisecond)})
	known.MarkFirstOutput(17, started.Add(7*time.Millisecond))
	known.MarkResponseBytes(123)
	known.ApplyParseResult(&ParseResult{
		ModelName: "synthetic-model", IsCompleted: true, Status: "completed",
		HasTokenUsage: true, TokenUsage: &tracking.TokenUsage{},
	})

	detail := waitSubscriptionDetail(t, tracker, "http-known")
	if detail.Status != "completed" || detail.RequestSource != tracking.RequestSourceCodexSubscriptionProxy ||
		detail.RequestKind != tracking.RequestKindGeneration || detail.UpstreamType != "subscription" ||
		detail.UpstreamName != subscriptionTrackingUpstreamName || detail.UpstreamID != 0 {
		t.Fatalf("known dimensions = %+v", detail)
	}
	if !detail.UsageKnown || !detail.CostKnown || detail.HTTPStatusCode == nil || *detail.HTTPStatusCode != 201 {
		t.Fatalf("known state = %+v", detail)
	}
	if detail.FirstTokenMs == nil || detail.CompletionMs == nil {
		t.Fatalf("known timing = first:%v completion:%v", detail.FirstTokenMs, detail.CompletionMs)
	}

	unknown := observer.StartHTTP(SubscriptionHTTPStart{
		RequestID: "http-unknown", StartedAt: time.Now(), Method: "POST", Path: "/v1/responses/compact",
		RequestKind: tracking.RequestKindCompaction,
	})
	unknown.Complete(nil, "SYNTHETIC-SECRET")
	unknownDetail := waitSubscriptionDetail(t, tracker, "http-unknown")
	if unknownDetail.Status != "failed" || unknownDetail.FailureReason != "subscription_failure" ||
		unknownDetail.HTTPStatusCode != nil || unknownDetail.UsageKnown || unknownDetail.CostKnown {
		t.Fatalf("unknown state = %+v", unknownDetail)
	}
	if strings.Contains(unknownDetail.FailureReason, "SECRET") {
		t.Fatalf("unsafe failure persisted: %q", unknownDetail.FailureReason)
	}

	externalError := observer.StartHTTP(SubscriptionHTTPStart{
		RequestID: "http-error", StartedAt: time.Now(), Method: "POST", Path: "/v1/responses",
		RequestKind: tracking.RequestKindGeneration,
	})
	externalError.ApplyParseResult(&ParseResult{
		IsCompleted: true, Status: StatusErrorAPI,
		ErrorInfo: &ErrorInfo{Type: "SYNTHETIC-SECRET", Message: "SYNTHETIC-SECRET"},
	})
	errorDetail := waitSubscriptionDetail(t, tracker, "http-error")
	if errorDetail.FailureReason != "subscription_response_failed" ||
		strings.Contains(errorDetail.FailureReason, "SECRET") || strings.Contains(errorDetail.ModelName, "SECRET") {
		t.Fatalf("unsafe parse error metadata persisted: %+v", errorDetail)
	}

	observer.StartHTTP(SubscriptionHTTPStart{RequestID: "auxiliary", Path: "/v1/models"}).Complete(nil, "")
	time.Sleep(20 * time.Millisecond)
	count, err := tracker.CountRequestDetails(context.Background(), &tracking.QueryOptions{RequestID: "auxiliary", UpstreamType: "all"})
	if err != nil || count != 0 {
		t.Fatalf("auxiliary count = %d, err=%v", count, err)
	}
}

func TestSubscriptionWSObserverCancellationRemovesHandle(t *testing.T) {
	tracker := newSubscriptionUnitTracker(t)
	observer := newSubscriptionTrackingObserver(tracker, nil)
	warmup := &subscriptionSubmission{RequestID: "warmup", Generate: false, StartedAt: time.Now()}
	observer.SubmissionStarted(warmup)

	submission := &subscriptionSubmission{
		RequestID: "ws-cancelled", Generate: true, StartedAt: time.Now(), Model: "synthetic-model",
		PrivacyState: handlers.NewPrivacyRequestState("ws-cancelled"),
	}
	observer.SubmissionStarted(submission)
	observer.SubmissionOutbound(submission, 31, time.Now())
	observer.SubmissionFirstOutput(submission, 11, time.Now())
	observer.SubmissionResponseProgress(submission, 79)
	observer.SubmissionTerminal(submission, &ParseResult{IsCompleted: true, IsCancelled: true, Status: StatusErrorAPI})

	observer.mu.Lock()
	remaining := len(observer.submissions)
	observer.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("settled WS handles = %d, want 0", remaining)
	}
	detail := waitSubscriptionDetail(t, tracker, "ws-cancelled")
	if detail.Status != "cancelled" || detail.FailureReason != "subscription_cancelled" ||
		detail.HTTPStatusCode != nil || detail.UsageKnown || detail.CostKnown {
		t.Fatalf("cancelled WS detail = %+v", detail)
	}
	count, err := tracker.CountRequestDetails(context.Background(), &tracking.QueryOptions{RequestID: "warmup", UpstreamType: "all"})
	if err != nil || count != 0 {
		t.Fatalf("warmup count = %d, err=%v", count, err)
	}
}

func newSubscriptionUnitTracker(t *testing.T) *tracking.UsageTracker {
	t.Helper()
	tracker, err := tracking.NewUsageTracker(&tracking.Config{
		Enabled: true, DatabasePath: ":memory:", BufferSize: 50, BatchSize: 1,
		FlushInterval: time.Hour, MaxRetry: 1, CleanupInterval: time.Hour, RetentionDays: 30,
		HotPool: &tracking.HotPoolSettings{Enabled: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tracker.Close() })
	return tracker
}

func waitSubscriptionDetail(t *testing.T, tracker *tracking.UsageTracker, requestID string) tracking.RequestDetail {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		details, err := tracker.QueryRequestDetails(context.Background(), &tracking.QueryOptions{RequestID: requestID, UpstreamType: "all"})
		if err == nil && len(details) == 1 && details[0].Status != "pending" {
			return details[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("request %s did not settle", requestID)
	return tracking.RequestDetail{}
}

// v6.3：订阅链路走无状态 ParseResponsesEventJSON，响应思考串必须由追踪 handle 按字段累计。
func TestSubscriptionTrackingMergesReasoningAcrossCreatedAndCompleted(t *testing.T) {
	tracker := newSubscriptionUnitTracker(t)
	observer := newSubscriptionTrackingObserver(tracker, nil)
	state := newSubscriptionSubmissionState(context.Background(), "ws-reasoning", observer)

	// 场景 1：created 回显 effort，completed 只带 reasoning_tokens → 两者合并
	merged, ok := state.beginCreate([]byte(`{"type":"response.create","model":"gpt-5.4","reasoning":{"effort":"high"},"input":"x"}`))
	if !ok {
		t.Fatal("beginCreate failed")
	}
	state.handleUpstream([]byte(`{"type":"response.created","response":{"id":"resp-m","model":"gpt-5.4","reasoning":{"effort":"high"}}}`))
	state.handleUpstream([]byte(`{"type":"response.completed","response":{"id":"resp-m","model":"gpt-5.4","usage":{"input_tokens":3,"output_tokens":9,"output_tokens_details":{"reasoning_tokens":7}}}}`))
	detail := waitSubscriptionDetail(t, tracker, merged.RequestID)
	if detail.RequestReasoning != "effort:high" {
		t.Fatalf("request_reasoning = %q", detail.RequestReasoning)
	}
	if detail.ResponseReasoning != "effort:high reasoning_tokens:7" {
		t.Fatalf("response_reasoning = %q, want merged created+completed", detail.ResponseReasoning)
	}
	if detail.ResponseModel != "gpt-5.4" || detail.RequestModel != "gpt-5.4" {
		t.Fatalf("models = request:%q response:%q", detail.RequestModel, detail.ResponseModel)
	}

	// 场景 2：created 未回显 effort → 只有 reasoning_tokens，前端判「上游未回显」
	noEcho, ok := state.beginCreate([]byte(`{"type":"response.create","model":"gpt-5.4","reasoning":{"effort":"low"},"input":"y"}`))
	if !ok {
		t.Fatal("beginCreate failed")
	}
	state.handleUpstream([]byte(`{"type":"response.created","response":{"id":"resp-n","model":"gpt-5.4"}}`))
	state.handleUpstream([]byte(`{"type":"response.completed","response":{"id":"resp-n","model":"gpt-5.4","usage":{"input_tokens":1,"output_tokens":1,"output_tokens_details":{"reasoning_tokens":0}}}}`))
	noEchoDetail := waitSubscriptionDetail(t, tracker, noEcho.RequestID)
	if noEchoDetail.ResponseReasoning != "reasoning_tokens:0" {
		t.Fatalf("no-echo response_reasoning = %q", noEchoDetail.ResponseReasoning)
	}
}

// handle 级：终态事件缺 effort 时不得把 created 记下的 effort 清空。
func TestSubscriptionTrackingMergeResponseFieldsKeepsEarlierEffort(t *testing.T) {
	tracker := newSubscriptionUnitTracker(t)
	observer := newSubscriptionTrackingObserver(tracker, nil)
	handle := observer.StartHTTP(SubscriptionHTTPStart{
		RequestID: "http-merge", StartedAt: time.Now(), Method: "POST", Path: "/v1/responses",
		RequestKind: tracking.RequestKindGeneration, RequestModel: "gpt-5.4", RequestReasoning: "effort:xhigh",
	})
	handle.MergeResponseFields("gpt-5.4", reasoning.CodexFields{Effort: "xhigh"})
	tokens := int64(42)
	handle.ApplyParseResult(&ParseResult{
		ModelName: "gpt-5.4", ResponseModel: "gpt-5.4", IsCompleted: true, Status: "completed",
		HasTokenUsage: true, TokenUsage: &tracking.TokenUsage{},
		ResponseReasoningFields: reasoning.CodexFields{ReasoningTokens: &tokens},
	})
	detail := waitSubscriptionDetail(t, tracker, "http-merge")
	if detail.ResponseReasoning != "effort:xhigh reasoning_tokens:42" {
		t.Fatalf("response_reasoning = %q", detail.ResponseReasoning)
	}
}

// 服务端主动 successor 没有客户端 create，submission.Model 来自响应；请求侧必须保持空，
// 否则前端会把同源的请求 / 响应模型误判为「一致」。
func TestSubscriptionTrackingServerInitiatedKeepsRequestModelUnknown(t *testing.T) {
	tracker := newSubscriptionUnitTracker(t)
	observer := newSubscriptionTrackingObserver(tracker, nil)
	state := newSubscriptionSubmissionState(context.Background(), "ws-successor", observer)

	// 没有 pending 的 create，created 直接触发 successor
	state.handleUpstream([]byte(`{"type":"response.created","response":{"id":"resp-s","model":"gpt-5.4","reasoning":{"effort":"high"}}}`))
	state.handleUpstream([]byte(`{"type":"response.completed","response":{"id":"resp-s","model":"gpt-5.4","usage":{"input_tokens":1,"output_tokens":1}}}`))

	var rows []tracking.RequestDetail
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		var err error
		rows, _, err = tracker.QueryRequestDetailsWithHotPool(context.Background(), &tracking.QueryOptions{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 1 && rows[0].Status == "completed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(rows) != 1 || rows[0].Status != "completed" {
		t.Fatalf("settled row not found: %+v", rows)
	}
	row := rows[0]
	if row.RequestModel != "" || row.RequestReasoning != "" {
		t.Fatalf("server-initiated request profile must stay empty: model=%q reasoning=%q", row.RequestModel, row.RequestReasoning)
	}
	if row.ResponseModel != "gpt-5.4" || row.ModelName != "gpt-5.4" || row.ResponseReasoning != "effort:high" {
		t.Fatalf("response side lost: %+v", row)
	}
}

// effort 只在 response.in_progress 回显（created 不带、终态只带 usage）时，也必须按 response ID 合并进落库结果。
func TestSubscriptionTrackingMergesReasoningFromInProgressEvent(t *testing.T) {
	tracker := newSubscriptionUnitTracker(t)
	observer := newSubscriptionTrackingObserver(tracker, nil)
	state := newSubscriptionSubmissionState(context.Background(), "ws-in-progress", observer)

	submission, ok := state.beginCreate([]byte(`{"type":"response.create","model":"gpt-5.4","reasoning":{"effort":"xhigh"},"input":"x"}`))
	if !ok {
		t.Fatal("beginCreate failed")
	}
	state.handleUpstream([]byte(`{"type":"response.created","response":{"id":"resp-ip","model":"gpt-5.4"}}`))
	state.handleUpstream([]byte(`{"type":"response.in_progress","response":{"id":"resp-ip","model":"gpt-5.4","reasoning":{"effort":"low"}}}`))
	state.handleUpstream([]byte(`{"type":"response.completed","response":{"id":"resp-ip","model":"gpt-5.4","usage":{"input_tokens":3,"output_tokens":9,"output_tokens_details":{"reasoning_tokens":5}}}}`))
	detail := waitSubscriptionDetail(t, tracker, submission.RequestID)
	if detail.ResponseReasoning != "effort:low reasoning_tokens:5" {
		t.Fatalf("response_reasoning = %q, want in_progress effort merged", detail.ResponseReasoning)
	}
	if detail.RequestReasoning != "effort:xhigh" || detail.ResponseModel != "gpt-5.4" {
		t.Fatalf("request_reasoning=%q response_model=%q", detail.RequestReasoning, detail.ResponseModel)
	}
}

// 订阅代理在开始与结算之间只靠计时事件推进跑秒：写出、首响、生成各一条，数值与热池一致。
func TestSubscriptionTrackingPublishesTimingEvents(t *testing.T) {
	tracker := newLifecyclePanelTestTracker(t)
	bus := &subscriptionPresentationEventBus{}
	observer := newSubscriptionTrackingObserver(tracker, bus)
	started := time.Now().Add(-20 * time.Millisecond)

	handle := observer.StartHTTP(SubscriptionHTTPStart{
		RequestID: "http-timing-events", StartedAt: started, Method: "POST", Path: "/v1/responses",
		RequestKind: tracking.RequestKindGeneration, IsStreaming: true,
	})
	outboundAt := started.Add(2 * time.Millisecond)
	handle.MarkOutbound(41, handlers.UpstreamWriteTiming{WroteAt: outboundAt})
	firstOutputAt := started.Add(7 * time.Millisecond)
	handle.MarkFirstOutput(17, firstOutputAt)
	handle.MarkFirstOutput(30, started.Add(9*time.Millisecond))

	sent := timingEvents(bus.published, requestChangeUpstreamSent)
	if len(sent) != 1 || sent[0].Data["first_token_start_at"] != timezonepolicy.FormatStorage(outboundAt) {
		t.Fatalf("upstream_sent events = %+v", sent)
	}
	firstResponses := timingEvents(bus.published, requestChangeFirstResponse)
	if len(firstResponses) != 1 || firstResponses[0].Data["first_token_ms"] != int64(5) ||
		firstResponses[0].Data["first_token_at"] != timezonepolicy.FormatStorage(firstOutputAt) {
		t.Fatalf("first_response events = %+v", firstResponses)
	}
	detail := queryHotPoolDetail(t, tracker, "http-timing-events")
	if detail.FirstTokenStartAt == nil || !detail.FirstTokenStartAt.Equal(outboundAt) ||
		detail.FirstTokenAt == nil || !detail.FirstTokenAt.Equal(firstOutputAt) {
		t.Fatalf("hot pool anchors = start:%v first:%v", detail.FirstTokenStartAt, detail.FirstTokenAt)
	}

	handle.ApplyParseResult(&ParseResult{
		ModelName: "synthetic-model", IsCompleted: true, Status: "completed",
		HasTokenUsage: true, TokenUsage: &tracking.TokenUsage{},
	})
	completed := timingEvents(bus.published, requestChangeGenerationCompleted)
	if len(completed) != 1 {
		t.Fatalf("generation_completed events = %+v", completed)
	}
	if ms, ok := completed[0].Data["completion_ms"].(int64); !ok || ms < 0 {
		t.Fatalf("completion_ms = %#v", completed[0].Data["completion_ms"])
	}
}
