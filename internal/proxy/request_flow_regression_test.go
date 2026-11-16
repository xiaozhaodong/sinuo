package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"sinuo/internal/privacy"
	"sinuo/internal/proxy/handlers"
	"sinuo/internal/proxy/subscriptionws"
	"sinuo/internal/tracking"
)

// 用一个活跃请求占满热池，让随后登记的请求走 SQLite。
func fullFlowHotPool(t *testing.T) *tracking.UsageTracker {
	t.Helper()
	tracker, err := tracking.NewUsageTracker(&tracking.Config{
		Enabled: true, DatabasePath: ":memory:", BufferSize: 10, BatchSize: 1,
		FlushInterval: time.Hour, MaxRetry: 1, CleanupInterval: time.Hour, RetentionDays: 30,
		HotPool: &tracking.HotPoolSettings{Enabled: true, MaxSize: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tracker.Close() })
	tracker.RecordRequestStart("occupy-hot-pool", "", "", "POST", "/v1/responses", true)
	return tracker
}

// 占用 SQLite 唯一写连接，以消费者真正等待连接作为队列积压的同步点。
func lockFlowWriter(t *testing.T, tracker *tracking.UsageTracker) (waitForWriter, release func()) {
	t.Helper()
	db := tracker.GetWriteDB()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	before := db.Stats().WaitCount
	release = sync.OnceFunc(func() { _ = tx.Rollback() })
	t.Cleanup(release)
	return func() {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for db.Stats().WaitCount == before {
			if time.Now().After(deadline) {
				t.Fatal("消费者没有等待 SQLite 写连接")
			}
			time.Sleep(time.Millisecond)
		}
	}, release
}

func waitForFlowSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatal("等待追踪事件超时")
	}
}

func TestRequestFlow_OverflowCoalescesEventsBeforeSettlement(t *testing.T) {
	for _, route := range []string{"subscription", "proxy"} {
		t.Run(route, func(t *testing.T) {
			tracker := fullFlowHotPool(t)
			wait, release := lockFlowWriter(t, tracker)
			var recorder handlers.UpstreamTraceRecorder
			var settle func()
			usage := &tracking.TokenUsage{OutputTokens: 33}
			if route == "subscription" {
				handle := newSubscriptionTrackingObserver(tracker, nil).StartHTTP(SubscriptionHTTPStart{
					RequestID: route, Method: "POST", Path: "/v1/responses", RequestKind: tracking.RequestKindGeneration,
				})
				handle.setRequestProfile("test-model", "", false)
				recorder = handle
				settle = func() {
					handle.SetUpstreamResponse(mockResponse("done", 200))
					handle.SetHTTPStatus(200)
					handle.ApplyParseResult(&ParseResult{IsCompleted: true, HasTokenUsage: true, TokenUsage: usage})
				}
			} else {
				rlm := NewRequestLifecycleManager(tracker, nil, route, nil)
				rlm.StartRequest("", "", "POST", "/v1/responses", true)
				recorder = rlm
				settle = func() {
					rlm.SetUpstreamResponse(mockResponse("done", 200))
					rlm.MarkFlowUpstreamTerminal()
					rlm.CompleteRequest(usage)
				}
			}
			wait()
			ctx, _ := handlers.WithUpstreamTrace(context.Background(), recorder)
			trace := httptrace.ContextClientTrace(ctx)
			trace.GetConn("example.test")
			trace.GotConn(httptrace.GotConnInfo{})
			trace.WroteHeaders()
			trace.WroteRequest(httptrace.WroteRequestInfo{})
			trace.GotFirstResponseByte()
			// 合并后，一次正常请求的跟踪和结算可以全部进入 10 槽缓冲。
			finished := make(chan struct{})
			go func() { defer close(finished); settle() }()
			waitForFlowSignal(t, finished)
			release()
			waitSubscriptionDetail(t, tracker, route)
			data := timingLifecycleData(t, tracker, route)
			if data.Source != "database" || data.Detail.Status != "completed" || !data.Detail.UsageKnown ||
				data.Detail.OutputTokens != 33 || data.Integrity.WriteGap || data.Flow == nil || !data.Flow.UpstreamTerminalObserved ||
				len(data.Flow.Contacts) != 1 || data.Flow.Contacts[0].FirstUpstreamByteOffsetMs == nil {
				t.Fatalf("回退请求丢失结算或路径：%+v", data)
			}
		})
	}
}

func TestRequestFlow_FullQueueRetainsSettlement(t *testing.T) {
	tracker := fullFlowHotPool(t)
	const id = "full-queue"
	wait, release := lockFlowWriter(t, tracker)
	tracker.RecordRequestStart(id, "", "", "POST", "/v1/responses", true)
	wait()
	for i := 0; i < 10; i++ {
		tracker.RecordRequestUpdate(id, tracking.UpdateOptions{RetryCount: &i})
	}
	register := func() error {
		return tracker.RecordRequestStartAtWithDimensions("next", "", "", "POST", "/v1/responses", tracking.RequestFamilyCodex, true,
			time.Now(), tracking.RequestDimensions{RequestSource: tracking.RequestSourceLegacyProxy, RequestKind: tracking.RequestKindLegacy})
	}
	if err := register(); err == nil {
		t.Fatal("队列满时新登记应返回错误")
	}
	settled := make(chan error, 1)
	go func() {
		settled <- tracker.RecordRequestSettlement(id, tracking.RequestSettlement{Status: "completed", TokenUsage: &tracking.TokenUsage{OutputTokens: 17}})
	}()
	select {
	case err := <-settled:
		t.Fatalf("队列尚满时结算提前返回：%v", err)
	case <-time.After(30 * time.Millisecond):
	}
	release()
	select {
	case err := <-settled:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("队列排空后结算仍未完成入队")
	}
	data := waitSubscriptionDetail(t, tracker, id)
	if data.Status != "completed" || data.OutputTokens != 17 || !data.UsageKnown {
		t.Fatalf("已接收的结算未落库：%+v", data)
	}
	if err := register(); err != nil {
		t.Fatal(err)
	}
	tracker.RecordRequestSuccess("next", "", &tracking.TokenUsage{OutputTokens: 9}, time.Millisecond)
	if next := waitSubscriptionDetail(t, tracker, "next"); next.Status != "completed" || next.OutputTokens != 9 {
		t.Fatalf("队列恢复后的请求未正常结算：%+v", next)
	}
}

type cancelAfterWriteRecorder struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
}

func (w *cancelAfterWriteRecorder) Write(p []byte) (int, error) {
	n, err := w.ResponseRecorder.Write(p)
	w.cancel()
	return n, err
}

func TestRequestFlow_CancelledSSESeparatesDeliveryFromBilling(t *testing.T) {
	for _, tc := range []struct{ name, prefix, outcome string }{
		{"before-first-write", "", tracking.FlowDeliveryAborted},
		{"after-first-write", "data: {\"type\":\"response.in_progress\"}\n\n", tracking.FlowDeliveryPartialOrFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tracker := newLifecyclePanelTestTracker(t)
			rlm := NewRequestLifecycleManager(tracker, nil, tc.name, nil)
			rlm.StartRequest("", "", "POST", "/v1/responses", true)
			rec := httptest.NewRecorder()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var sink http.ResponseWriter = rec
			resp := mockResponse(phaseFixture(t, "codex_hidden_reasoning"), 200)
			if tc.prefix != "" {
				// 在第一段 SSE 数据实际写出后取消，使下一段上游数据只被 drain。
				terminal := resp.Body
				resp.Body = io.NopCloser(io.MultiReader(strings.NewReader(tc.prefix), terminal))
				sink = &cancelAfterWriteRecorder{ResponseRecorder: rec, cancel: cancel}
			} else {
				cancel()
			}
			w := rlm.WrapDownstreamWriter(sink)
			processor := NewStreamProcessor(NewTokenParser(), tracker, w, w.(http.Flusher), tc.name, "endpoint")
			rlm.observeStream(processor)
			processor.EnableDownstreamTailDrain(time.Second, nil)
			usage, _, err := processor.ProcessStreamWithRetry(ctx, resp)
			if err != nil {
				t.Fatal(err)
			}
			rlm.CompleteRequest(usage)
			rlm.finishDownstreamDelivery()
			data := timingLifecycleData(t, tracker, tc.name)
			if rec.Body.String() != tc.prefix || data.Detail.Status != "completed" || data.Detail.OutputTokens != 500 ||
				data.Flow == nil || !data.Flow.UpstreamTerminalObserved || data.Flow.DownstreamBytesWritten != int64(len(tc.prefix)) ||
				data.Flow.DownstreamDeliveryOutcome != tc.outcome {
				t.Fatalf("写出 %d 字节，结算 %s，输出 %d，路径 %+v", rec.Body.Len(), data.Detail.Status, data.Detail.OutputTokens, data.Flow)
			}
		})
	}
}

func TestRequestFlow_RawWSWriteFailure(t *testing.T) {
	tracker := newLifecyclePanelTestTracker(t)
	state := newTestSubscriptionSubmissionState(context.Background(), "raw-ws", newSubscriptionTrackingObserver(tracker, nil))
	sub, ok := beginTestSubscriptionCreate(t, state, []byte(`{"type":"response.create","input":"x"}`))
	if !ok {
		t.Fatal("请求未登记")
	}
	interceptor := &subscriptionWSInterceptor{submissions: state}
	frame := boundaryRead(t, interceptor, []byte(`{"type":"response.created","response":{"id":"r"}}`))
	want := errors.New("模拟下游写超时")
	if err := boundaryWrite(frame, func([]byte) error { return want }); !errors.Is(err, want) {
		t.Fatalf("写入错误 = %v", err)
	}
	state.close("connection_closed")
	data := timingLifecycleData(t, tracker, sub.RequestID)
	if data.Flow == nil || data.Flow.DownstreamDeliveryOutcome != tracking.FlowDeliveryPartialOrFailed ||
		data.Flow.DownstreamBytesWritten != 0 || data.Flow.FirstClientWriteOffsetMs != nil {
		t.Fatalf("失败的首帧交付记录错误：%+v", data.Flow)
	}
}

func TestRequestFlow_WSOriginVisibleAtRegistration(t *testing.T) {
	tracker := fullFlowHotPool(t)
	observer := newSubscriptionTrackingObserver(tracker, nil)
	for _, tc := range []struct {
		origin    string
		successor bool
	}{
		{tracking.FlowOriginClientRequest, false}, {tracking.FlowOriginServerSuccessor, true},
	} {
		t.Run(tc.origin, func(t *testing.T) {
			sub := &subscriptionSubmission{RequestID: tc.origin, StartedAt: time.Now(), ServerInitiated: tc.successor, Generate: true}
			observer.SubmissionStarted(sub)
			var data *tracking.RequestLifecycleData
			deadline := time.Now().Add(3 * time.Second)
			for data == nil {
				var err error
				data, err = tracker.GetRequestLifecycleData(context.Background(), sub.RequestID)
				if err != nil {
					t.Fatal(err)
				}
				if time.Now().After(deadline) {
					t.Fatal("WS 登记未写入数据库")
				}
				if data == nil {
					time.Sleep(time.Millisecond)
				}
			}
			if data.Source != "database" || data.Detail.Status != "pending" || data.Flow == nil ||
				data.Flow.Transport != tracking.FlowTransportWS || data.Flow.Origin != tc.origin {
				t.Fatalf("登记后首个版本的路径错误：%+v", data.Flow)
			}
		})
	}
}

func TestRequestFlow_ByteBudgetMatchesArchivedDetail(t *testing.T) {
	tracker := newLifecyclePanelTestTracker(t)
	const id = "byte-budget"
	rlm := NewRequestLifecycleManager(tracker, nil, id, nil)
	rlm.StartRequest("", "", "POST", "/v1/responses", true)
	label := strings.Repeat("测", 155)
	for seq := 1; seq <= 40; seq++ {
		rlm.RecordFlowLegSelected(tracking.RequestFlowTargetSnapshot{UpstreamID: int64(seq), UpstreamName: label, EndpointName: label}, "", false)
	}
	hot := timingLifecycleData(t, tracker, id)
	if hot.Source != "hot_pool" || hot.Flow == nil || !hot.Integrity.Truncated || hot.Integrity.OmittedLegs == 0 ||
		hot.Flow.TotalLegs != 40 || hot.Integrity.OmittedLegs != 40-len(hot.Flow.Legs) {
		t.Fatalf("热池详情未执行字节预算：%+v", hot.Integrity)
	}
	rlm.CompleteRequest(&tracking.TokenUsage{})
	deadline := time.Now().Add(3 * time.Second)
	for {
		cold := timingLifecycleData(t, tracker, id)
		if cold.Source == "database" {
			if !reflect.DeepEqual(hot.Flow, cold.Flow) || hot.Integrity != cold.Integrity {
				t.Fatalf("归档改变了路径或完整性：热池 %+v，数据库 %+v", hot.Integrity, cold.Integrity)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("路径未归档")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRequestFlow_CancelledIngressStopsBeforeForwarding(t *testing.T) {
	tracker := fullFlowHotPool(t)
	for _, route := range []string{"http", "subscription-http", "ws-create", "ws-successor"} {
		t.Run(route, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var run func() error
			switch route {
			case "http":
				server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("已取消的请求到达上游") }))
				defer server.Close()
				h, _ := newEndpointPipelineTestHandler(t, endpointPipelineConfig("test", server.URL, 1))
				h.SetUsageTracker(tracker)
				req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"claude-test","messages":[]}`))
				req = req.WithContext(context.WithValue(ctx, "conn_id", route))
				run = func() error { h.ServeHTTP(httptest.NewRecorder(), req); return nil }
			case "subscription-http":
				env := newSubscriptionTestEnv(t, func(http.ResponseWriter, *http.Request) { t.Error("已取消的订阅请求到达上游") }, "subscription")
				env.handler.proxy.SetUsageTracker(tracker)
				req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"input":"hello"}`)).WithContext(ctx)
				req.Header.Set("Authorization", "Bearer test")
				target, _ := resolveSubscriptionRoute(req.Method, "responses", false)
				run = func() error { env.handler.passthrough.serve(httptest.NewRecorder(), req, target, route); return nil }
			default:
				state := newTestSubscriptionSubmissionState(context.Background(), route, newSubscriptionTrackingObserver(tracker, nil))
				defer state.close("connection_closed")
				i := &subscriptionWSInterceptor{submissions: state}
				direction, frame := subscriptionws.DirectionDownstreamToUpstream, `{"type":"response.create","input":"hello"}`
				if route == "ws-successor" {
					direction, frame = subscriptionws.DirectionUpstreamToDownstream, `{"type":"response.created","response":{"id":"successor"}}`
				}
				run = func() error {
					res, err := i.Intercept(ctx, direction, []byte(frame))
					if res.Forward != nil {
						return errors.New("取消后仍转发帧")
					}
					return err
				}
			}
			cancel()
			if err := run(); strings.HasPrefix(route, "ws-") && !errors.Is(err, context.Canceled) {
				t.Fatalf("取消结果 = %v", err)
			}
		})
	}
}

func TestRequestFlow_SSEDeliveryUsesProtocolEnd(t *testing.T) {
	for _, tc := range []struct {
		name, body, status, outcome string
		terminal                    bool
	}{
		{"truncated", "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"1\",\"model\":\"claude-test\",\"usage\":{\"input_tokens\":2}}}\n\n", "completed", tracking.FlowDeliveryPartialOrFailed, false},
		{"error", "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"try later\"}}\n\n", "failed", tracking.FlowDeliveryDelivered, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, tc.body)
			}))
			defer up.Close()
			h, _ := newEndpointPipelineTestHandler(t, endpointPipelineConfig("test", up.URL, 1))
			tr := attachTracking(t, h)
			w := performTrackedClaudeRequest(t, h, tc.name, `{"model":"claude-test","messages":[],"stream":true}`, true)
			data := timingLifecycleData(t, tr, tc.name)
			if !strings.Contains(w.Body.String(), tc.body) || data.Detail.Status != tc.status || data.Flow == nil || data.Flow.UpstreamTerminalObserved != tc.terminal || data.Flow.DownstreamDeliveryOutcome != tc.outcome || data.Flow.DownstreamBytesWritten != int64(w.Body.Len()) {
				t.Fatalf("客户端收到 %d 字节，业务状态 %s，路径 %+v", w.Body.Len(), data.Detail.Status, data.Flow)
			}
		})
	}
}

func TestRequestFlow_CompleteUpstreamWhenClientWriteFails(t *testing.T) {
	for _, route := range []string{"endpoint-400", "tool-json", "tool-truncated-json"} {
		t.Run(route, func(t *testing.T) {
			w := &recordingFlushResponseWriter{writeErr: errors.New("客户端写失败")}
			var data *tracking.RequestLifecycleData
			if route == "endpoint-400" {
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(400)
					io.WriteString(w, `{"error":{"message":"invalid request"}}`)
				}))
				defer up.Close()
				h, _ := newEndpointPipelineTestHandler(t, endpointPipelineConfig("test", up.URL, 1))
				tr := attachTracking(t, h)
				req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"claude-test","messages":[]}`))
				h.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), "conn_id", route)))
				data = timingLifecycleData(t, tr, route)
			} else {
				env := newSubscriptionTestEnv(t, func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if route == "tool-truncated-json" {
						w.Header().Set("Content-Length", "1024")
					}
					io.WriteString(w, `{"data":[{"b64_json":"aW1hZ2U="}]}`)
				}, "subscription")
				tr := newLifecyclePanelTestTracker(t)
				env.handler.proxy.SetUsageTracker(tr)
				req := httptest.NewRequest("POST", "/v1/images/generations", strings.NewReader(`{"model":"gpt-image-1","prompt":"x"}`))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer test")
				target, _ := resolveSubscriptionRoute(req.Method, "images/generations", false)
				env.handler.passthrough.serve(w, req, target, route)
				data = timingLifecycleData(t, tr, route)
			}
			if data.Flow == nil || data.Flow.UpstreamTerminalObserved != (route != "tool-truncated-json") || data.Flow.DownstreamDeliveryOutcome != tracking.FlowDeliveryPartialOrFailed || data.Flow.DownstreamBytesWritten != 0 {
				t.Fatalf("写失败后上下游状态错误：%+v", data.Flow)
			}
		})
	}
}

func TestRequestFlow_FailoverKeepsRewrittenModelOnItsLeg(t *testing.T) {
	var primaryHits atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if primaryHits.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(400)
		io.WriteString(w, `{"error":{"message":"model not found"}}`)
	}))
	defer primary.Close()
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"type":"message","model":"claude-test","content":[],"usage":{"input_tokens":3,"output_tokens":2}}`)
	}))
	defer backup.Close()
	cfg := endpointPipelineConfig("primary", primary.URL, 1)
	cfg.ModelRewriteRules = `[{"paths":["/v1/messages"],"match":"exact","from":"claude-test","to":"provider-sonnet"}]`
	h, _ := newEndpointPipelineTestHandler(t, cfg, endpointPipelineConfig("backup", backup.URL, 2))
	tr := attachTracking(t, h)
	rec := performTrackedClaudeRequest(t, h, "rewrite", `{"model":"claude-test","messages":[]}`, false)
	d := timingLifecycleData(t, tr, "rewrite")
	if rec.Code != 200 || d.Detail.UpstreamModel != "" || d.Flow == nil || len(d.Flow.Legs) != 2 || d.Flow.TotalLegs != 2 || d.Flow.Legs[0].TargetSnapshot.UpstreamModel != "provider-sonnet" || d.Flow.Legs[1].TargetSnapshot.UpstreamModel != "" {
		t.Fatalf("故障转移后模型归属错误：%+v", d.Flow)
	}
	if d.Flow.Legs[0].ReasonCode != "" || d.Flow.Legs[1].ReasonCode != FailoverReasonModelUnsupported {
		t.Fatalf("端点换路原因归属错误：%+v", d.Flow.Legs)
	}
	contacts := d.Flow.Contacts
	if len(contacts) != 3 || contacts[0].OutcomeCode != "rate_limit_retry" ||
		contacts[0].LegSeq != d.Flow.Legs[0].LegSeq || contacts[1].LegSeq != d.Flow.Legs[0].LegSeq ||
		contacts[2].LegSeq != d.Flow.Legs[1].LegSeq {
		t.Fatalf("同端点短重试和跨端点切换的联系归属错误：%+v", contacts)
	}
}

func TestRequestFlow_AccountUsageLimitLegReason(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":{"type":"usage_limit_reached","resets_in_seconds":3600}}`)
			return
		}
		io.WriteString(w, `{"id":"backup","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer upstream.Close()
	h, tr := phaseAccountHandler(t, upstream.URL, 2)
	rec := performPhaseAccountRequest(h, false)
	flow := timingLifecycleData(t, tr, "synthetic-phase-account").Flow
	if rec.Code != http.StatusOK || flow == nil || len(flow.Legs) != 2 || len(flow.Contacts) != 2 {
		t.Fatalf("未完成预期换号：HTTP %d，轨迹 %+v", rec.Code, flow)
	}
	if flow.Legs[0].ReasonCode != "" || flow.Legs[1].ReasonCode != FailoverReasonUsageLimit ||
		flow.Contacts[0].OutcomeCode != FailoverReasonRateLimited {
		t.Fatalf("原联系结果和实际换号原因归属错误：%+v", flow)
	}
}

func TestRequestFlow_WSUnownedFramePreservesWireAndRequestBytes(t *testing.T) {
	tr := newLifecyclePanelTestTracker(t)
	state := newTestSubscriptionSubmissionState(context.Background(), "parallel", newSubscriptionTrackingObserver(tr, nil))
	i := &subscriptionWSInterceptor{submissions: state}
	defer state.close("end")
	var wire []string
	send := func(p []byte) error { wire = append(wire, string(p)); return nil }
	write := func(frame string) {
		t.Helper()
		if err := boundaryWrite(boundaryRead(t, i, []byte(frame)), send); err != nil {
			t.Fatal(err)
		}
	}
	first, _ := beginTestSubscriptionCreate(t, state, []byte(`{"type":"response.create","input":"x","stream_id":"first"}`))
	a := `{"type":"response.created","stream_id":"first","response":{"id":"r1"}}`
	write(a)
	second, _ := beginTestSubscriptionCreate(t, state, []byte(`{"type":"response.create","input":"y","stream_id":"second"}`))
	b := `{"type":"response.created","stream_id":"second","response":{"id":"r2"}}`
	unknown := `{"type":"response.output_text.delta","delta":"unknown"}`
	// 一个已绑定、另一个等待 created 时，无标识增量也不能算到第一个请求。
	write(unknown)
	write(b)
	known := `{"type":"response.output_text.delta","stream_id":"second","delta":"known"}`
	write(unknown)
	write(known)
	state.close("end")
	d1 := timingLifecycleData(t, tr, first.RequestID)
	d2 := timingLifecycleData(t, tr, second.RequestID)
	if !reflect.DeepEqual(wire, []string{a, unknown, b, unknown, known}) || d1.Flow == nil || d2.Flow == nil || d1.Flow.DownstreamBytesWritten != int64(len(a)) || d2.Flow.DownstreamBytesWritten != int64(len(b)+len(known)) {
		t.Fatalf("并发帧交付归属错误：%+v，%+v，客户端收到 %d 帧", d1.Flow, d2.Flow, len(wire))
	}
}

func TestRequestFlow_WSLocalRejectionCountsClientReply(t *testing.T) {
	tr := newLifecyclePanelTestTracker(t)
	env := newSubscriptionTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{})
		if err != nil {
			return
		}
		defer c.CloseNow()
		_, _, _ = c.Read(r.Context())
	}, "subscription")
	env.handler.proxy.SetUsageTracker(tr)
	env.handler.proxy.SetPrivacyFilter(&p2aPrivacyFilter{version: 1, err: &privacy.PolicyError{StatusCode: 422, Code: privacy.CodeScanFailed}})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(env.server.URL, "http")+"/subscription/"+subscriptionTestSecret+"/backend-api/codex/responses", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer synthetic-client-token"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	if err := c.Write(ctx, websocket.MessageText, []byte(`{"type":"response.create","input":"x"}`)); err != nil {
		t.Fatal(err)
	}
	_, reply, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	d := onlyPhaseRequest(t, tr)
	if !strings.Contains(string(reply), privacy.CodeScanFailed) || d.Detail.Status != "failed" || d.Flow == nil || d.Flow.UpstreamTerminalObserved || d.Flow.DownstreamDeliveryOutcome != tracking.FlowDeliveryDelivered || d.Flow.DownstreamBytesWritten != int64(len(reply)) {
		t.Fatalf("WS 客户端收到 %d 字节，交付记录 %+v", len(reply), d.Flow)
	}
}

func TestRequestFlow_WSPublishRejectionWaitsForWrite(t *testing.T) {
	for _, drop := range []bool{false, true} {
		name := "forwarded"
		if drop {
			name = "dropped"
		}
		t.Run(name, func(t *testing.T) {
			tr := newLifecyclePanelTestTracker(t)
			state := newTestSubscriptionSubmissionState(context.Background(), name, newSubscriptionTrackingObserver(tr, nil))
			defer state.close("closed")
			filter := newRestoreTestFilter(t)
			filter.Settings.OnError = privacy.OnErrorFailClosed
			i := &subscriptionWSInterceptor{filter: filter, submissions: state}
			res, err := i.Intercept(context.Background(), subscriptionws.DirectionDownstreamToUpstream, []byte(fmt.Sprintf(`{"type":"response.create","model":"gpt-5","input":%q}`, routeTestValue)))
			if err != nil || res.Deliver == nil {
				t.Fatalf("未进入待发布状态：%v", err)
			}
			filter.Dict.Retire()
			var after func()
			var frame []byte
			err = res.Deliver(func([]byte) error { return errors.New("被拒请求发往上游") }, func(p []byte, done func()) error { frame = p; after = done; return nil })
			if err != nil || after == nil || len(frame) == 0 {
				t.Fatalf("未生成拒绝回复：%v", err)
			}
			outcome, written := tracking.FlowDeliveryDelivered, int64(len(frame))
			if drop {
				state.close("closed")
				outcome, written = tracking.FlowDeliveryAborted, 0
			}
			after()
			state.close("closed")
			d := onlyPhaseRequest(t, tr)
			if d.Detail.Status != "failed" || d.Flow == nil || d.Flow.UpstreamTerminalObserved || d.Flow.DownstreamDeliveryOutcome != outcome || d.Flow.DownstreamBytesWritten != written {
				t.Fatalf("拒绝回复交付记录：%+v", d.Flow)
			}
		})
	}
}
