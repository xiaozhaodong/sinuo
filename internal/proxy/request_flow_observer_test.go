package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync"
	"testing"
	"time"

	"sinuo/internal/middleware"
	"sinuo/internal/proxy/handlers"
	"sinuo/internal/tracking"
)

type recordingFlushResponseWriter struct {
	header   http.Header
	status   int
	buf      bytes.Buffer
	writeErr error
	flushErr error
	flushes  int
}

func (w *recordingFlushResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *recordingFlushResponseWriter) WriteHeader(code int) {
	w.status = code
}

func (w *recordingFlushResponseWriter) Write(p []byte) (int, error) {
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	return w.buf.Write(p)
}

func (w *recordingFlushResponseWriter) FlushError() error {
	w.flushes++
	return w.flushErr
}

type spyFlowTraceRecorder struct {
	mu          sync.Mutex
	legacyConns int
	legacyWrite int
	getConn     int
	gotConn     int
	wroteHdrs   int
	wroteReq    int
	wroteErr    error
	firstByte   int
}

func (s *spyFlowTraceRecorder) RecordUpstreamGetConn(handlers.UpstreamConnAttempt) {
	s.mu.Lock()
	s.legacyConns++
	s.mu.Unlock()
}

func (s *spyFlowTraceRecorder) RecordUpstreamWrite(handlers.UpstreamWriteTiming) {
	s.mu.Lock()
	s.legacyWrite++
	s.mu.Unlock()
}

func (s *spyFlowTraceRecorder) RecordUpstreamFlowGetConn(_ int, _ handlers.UpstreamConnAttempt) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getConn++
	return s.getConn
}

func (s *spyFlowTraceRecorder) RecordUpstreamFlowGotConn(int, time.Time, httptrace.GotConnInfo) {
	s.mu.Lock()
	s.gotConn++
	s.mu.Unlock()
}

func (s *spyFlowTraceRecorder) RecordUpstreamFlowWroteHeaders(int, time.Time) {
	s.mu.Lock()
	s.wroteHdrs++
	s.mu.Unlock()
}

func (s *spyFlowTraceRecorder) RecordUpstreamFlowWroteRequest(_ int, _ handlers.UpstreamWriteTiming, writeErr error) {
	s.mu.Lock()
	s.wroteReq++
	s.wroteErr = writeErr
	s.mu.Unlock()
}

func (s *spyFlowTraceRecorder) RecordUpstreamFlowFirstResponseByte(int, time.Time) {
	s.mu.Lock()
	s.firstByte++
	s.mu.Unlock()
}

func TestDownstreamObservedResponseWriter_WriteAndFlushError(t *testing.T) {
	base := &recordingFlushResponseWriter{}

	var firstCount int

	wrapped, obs := wrapDownstreamObservedWriter(
		base,
		func(bytesWritten int64, at time.Time) {
			firstCount++
			if bytesWritten != 5 || at.IsZero() {
				t.Fatalf("unexpected first-write callback args: bytes=%d at=%v", bytesWritten, at)
			}
		},
	)

	if n, err := wrapped.Write([]byte("hello")); err != nil || n != 5 {
		t.Fatalf("first write failed: n=%d err=%v", n, err)
	}
	if n, err := wrapped.Write([]byte("-world")); err != nil || n != 6 {
		t.Fatalf("second write failed: n=%d err=%v", n, err)
	}

	if firstCount != 1 {
		t.Fatalf("expected onFirstWrite once, got %d", firstCount)
	}
	if stats := obs.Stats(); stats.BytesWritten != 11 || stats.WriteErrors != 0 || stats.FlushErrors != 0 {
		t.Fatalf("下行交付统计有误: %+v", stats)
	}

	wantFlushErr := errors.New("broken pipe flush")
	base.flushErr = wantFlushErr
	ctrlErr := http.NewResponseController(wrapped).Flush()
	if !errors.Is(ctrlErr, wantFlushErr) {
		t.Fatalf("expected FlushError %v, got %v", wantFlushErr, ctrlErr)
	}
	if stats := obs.Stats(); stats.FlushErrors != 1 {
		t.Fatalf("未记录下行刷新失败: %+v", stats)
	}

	// 验证 Write 错误计数。
	base.writeErr = errors.New("short write")
	if _, err := wrapped.Write([]byte("x")); err == nil {
		t.Fatal("expected injected write error")
	}
	if obs.Stats().WriteErrors != 1 {
		t.Fatalf("expected WriteErrors=1, got %+v", obs.Stats())
	}
}

func TestUpstreamFlowTraceRecorder_WithUpstreamTraceAndLifecycleManager(t *testing.T) {
	// 1. 验证 WithUpstreamTrace 能将 ClientTrace 回调完整分派给 UpstreamFlowTraceRecorder。
	spy := &spyFlowTraceRecorder{}
	spyCtx, _ := handlers.WithUpstreamTrace(context.Background(), spy)
	spyTrace := httptrace.ContextClientTrace(spyCtx)
	if spyTrace == nil {
		t.Fatal("expected non-nil ClientTrace in context")
	}
	spyTrace.GetConn("api.example.com:443")
	spyTrace.GotConn(httptrace.GotConnInfo{})
	spyTrace.WroteHeaders()
	spyTrace.WroteRequest(httptrace.WroteRequestInfo{})
	spyTrace.GotFirstResponseByte()

	if spy.legacyConns != 1 || spy.legacyWrite != 1 {
		t.Fatalf("expected legacy recorder hooks (1,1), got (%d,%d)", spy.legacyConns, spy.legacyWrite)
	}
	if spy.getConn != 1 || spy.gotConn != 1 || spy.wroteHdrs != 1 || spy.wroteReq != 1 || spy.firstByte != 1 || spy.wroteErr != nil {
		t.Fatalf("unexpected flow trace spy counts: %+v", spy)
	}

	// 2. 验证真实 RequestLifecycleManager 通过 WithUpstreamTrace 写入热池流路径里程碑。
	tracker := newLifecyclePanelTestTracker(t)
	rlm := NewRequestLifecycleManager(tracker, nil, "req-trace-flow", nil)
	rlm.StartRequest("127.0.0.1", "test-agent", "POST", "/v1/responses", true)
	rlm.RecordFlowLegSelected(tracking.RequestFlowTargetSnapshot{
		UpstreamType: "endpoint",
		UpstreamName: "ep-trace",
		EndpointName: "ep-trace",
	}, "initial_route", false)

	ctx, _ := handlers.WithUpstreamTrace(context.Background(), rlm)
	trace := httptrace.ContextClientTrace(ctx)
	trace.GetConn("api.example.com:443")
	trace.GotConn(httptrace.GotConnInfo{})
	trace.WroteHeaders()
	trace.WroteRequest(httptrace.WroteRequestInfo{})
	trace.GotFirstResponseByte()

	data, err := tracker.GetRequestLifecycleData(context.Background(), "req-trace-flow")
	if err != nil {
		t.Fatalf("GetRequestLifecycleData failed: %v", err)
	}
	if data == nil || data.Flow == nil || len(data.Flow.Contacts) != 1 {
		t.Fatalf("expected 1 flow contact recorded via WithUpstreamTrace, got data=%+v", data)
	}
	c := data.Flow.Contacts[0]
	if c.ConnReadyOffsetMs == nil || c.WriteStartOffsetMs == nil || c.WriteDoneOffsetMs == nil || c.FirstUpstreamByteOffsetMs == nil {
		t.Fatalf("expected all contact milestones populated, got %+v", c)
	}
}

func TestRequestFlow_ObservedTerminalIndependentOfSettlement(t *testing.T) {
	tracker := newLifecyclePanelTestTracker(t)
	const usageDelta = "event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":3}}\n\n"
	for _, tc := range []struct {
		name, path, stream string
		terminal           bool
	}{
		{"claude-delta", "/v1/messages", usageDelta, false},
		{"claude-stop", "/v1/messages", usageDelta + "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", true},
		{"responses-created", "/v1/responses", "event: response.created\ndata: {\"type\":\"response.created\"}\n\n", false},
		{"responses-completed", "/v1/responses", "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rlm := NewRequestLifecycleManager(tracker, nil, tc.name, nil)
			rlm.StartRequest("", "", "POST", tc.path, true)
			writer := &mockResponseWriter{}
			processor := NewStreamProcessor(NewTokenParser(), tracker, writer, writer, tc.name, "upstream")
			rlm.observeStream(processor)
			processor.parseTokensFromChunk([]byte(tc.stream))
			rlm.RecordStreamCompletion()
			rlm.CompleteRequestWithQuality(&tracking.TokenUsage{}, "incomplete_stream")
			data, err := tracker.GetRequestLifecycleData(context.Background(), tc.name)
			if err != nil || data == nil || data.Flow == nil {
				t.Fatalf("读取链路失败：%+v err=%v", data, err)
			}
			if data.Detail.Status != "completed" || data.Flow.UpstreamTerminalObserved != tc.terminal ||
				(data.Flow.UpstreamTerminalOffsetMs != nil) != tc.terminal {
				t.Fatalf("结算状态 %s，协议终态 %+v", data.Detail.Status, data.Flow)
			}
		})
	}
}

func TestRequestFlow_SubscriptionTerminalEvidence(t *testing.T) {
	tracker := newLifecyclePanelTestTracker(t)
	observer := newSubscriptionTrackingObserver(tracker, nil)
	for _, observed := range []bool{false, true} {
		id := fmt.Sprintf("subscription-terminal-%t", observed)
		handle := observer.StartHTTP(SubscriptionHTTPStart{
			RequestID: id, Path: "/v1/responses", Method: "POST", RequestKind: tracking.RequestKindGeneration,
		})
		at := handle.startedAt.Add(5 * time.Millisecond)
		if observed {
			result := &ParseResult{IsCompleted: true, Status: "completed"}
			handle.ObserveStreamResult(result, 0, at)
			handle.ApplyParseResult(result)
		} else {
			handle.settle("completed", "", nil, nil)
		}
		data, err := tracker.GetRequestLifecycleData(context.Background(), id)
		if err != nil || data == nil || data.Flow == nil || data.Flow.UpstreamTerminalObserved != observed {
			t.Fatalf("期望订阅终态 %t，得到 %+v，err=%v", observed, data, err)
		}
		if observed && (data.Flow.UpstreamTerminalOffsetMs == nil || *data.Flow.UpstreamTerminalOffsetMs != 5) {
			t.Fatalf("重复观察改变了首次终态的时间：%+v", data.Flow)
		}
	}
}

func TestRequestFlow_WSDetailUsesDeliveredBytes(t *testing.T) {
	tracker := newSubscriptionUnitTracker(t)
	observer := newSubscriptionTrackingObserver(tracker, nil)
	sub := &subscriptionSubmission{RequestID: "ws-detail-bytes", Generate: true, StartedAt: time.Now()}
	observer.SubmissionStarted(sub)
	observer.SubmissionOutbound(sub, 31, time.Now())
	observer.SubmissionFirstOutput(sub, 10, time.Now())
	observer.SubmissionResponseProgress(sub, 200)
	observer.SubmissionDownstreamDelivered(sub, 7, nil, time.Now())
	observer.SubmissionDownstreamDelivered(sub, 11, nil, time.Now())
	observer.SubmissionDownstreamDelivered(sub, 19, errors.New("write failed"), time.Now())
	observer.SubmissionTerminal(sub, &ParseResult{IsCompleted: true, Status: "completed"})
	waitSubscriptionDetail(t, tracker, sub.RequestID)
	data, err := tracker.GetRequestLifecycleData(context.Background(), sub.RequestID)
	if err != nil || data == nil || data.Flow == nil {
		t.Fatalf("未读到归档的 WS 轨迹: detail=%+v err=%v", data, err)
	}
	flow := data.Flow
	if flow.Transport != tracking.FlowTransportWS || len(flow.Legs) != 1 || len(flow.Contacts) != 1 ||
		flow.Contacts[0].FirstUpstreamByteOffsetMs == nil || flow.DownstreamBytesWritten != 18 || flow.DownstreamDeliveryOutcome != tracking.FlowDeliveryPartialOrFailed {
		t.Fatalf("WS 联系次数或实际交付记录有误: %+v", flow)
	}
}

func TestRequestFlow_ImageTerminalBeforeDownstreamWriteFailure(t *testing.T) {
	resp := mockResponse("event: image_generation.completed\ndata: {\"type\":\"image_generation.completed\",\"b64_json\":\"ZmluYWw=\"}\n\n", http.StatusOK)
	resp.Header.Set("Content-Type", "text/event-stream")
	writer := &recordingFlushResponseWriter{writeErr: errors.New("downstream closed")}
	var terminal, delivered bool
	result, err := relayValidatedImageAPIStream(writer, resp, openAIImagesGenerationsPath, imageAPIStreamTiming{
		OnUpstreamTerminal: func() { terminal = true },
		OnCompleted:        func() { delivered = true },
	})
	if err == nil || !result.WriteFailure || !terminal || delivered {
		t.Fatalf("图片终态或交付记录错误：terminal=%t delivered=%t result=%+v err=%v", terminal, delivered, result, err)
	}
}

func TestRequestFlow_SubscriptionHTTPDeliveryAfterSettlement(t *testing.T) {
	body := phaseFixture(t, "codex_hidden_reasoning")
	for _, hot := range []bool{true, false} {
		for _, authorized := range []bool{true, false} {
			t.Run(fmt.Sprintf("hot=%t/authorized=%t", hot, authorized), func(t *testing.T) {
				tracker := newTimingTestTracker(t, hot)
				env := newSubscriptionTestEnv(t, func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, body)
				}, "subscription")
				env.handler.proxy.SetUsageTracker(tracker)
				resp := env.request("POST", "responses", func(r *http.Request) {
					if authorized {
						r.Header.Set("Authorization", "Bearer synthetic-client")
					}
					r.Header.Set("Content-Type", "application/json")
					r.Body = io.NopCloser(strings.NewReader(`{"model":"gpt-5.5","input":[],"stream":true}`))
				})
				defer resp.Body.Close()
				received, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatal(err)
				}
				data := onlyPhaseRequest(t, tracker)
				// 客户端可能通过 Content-Length 提前读完，SQLite 也有一段异步提交时间。
				deadline := time.Now().Add(3 * time.Second)
				for data.Flow == nil || data.Flow.DownstreamDeliveryDoneOffsetMs == nil {
					if time.Now().After(deadline) {
						t.Fatalf("等待下游写出记录超时: %+v", data.Flow)
					}
					time.Sleep(5 * time.Millisecond)
					data = timingLifecycleData(t, tracker, data.Detail.RequestID)
				}
				if data.Flow.FirstClientWriteOffsetMs == nil || data.Flow.DownstreamDeliveryOutcome != tracking.FlowDeliveryDelivered ||
					len(received) == 0 || data.Flow.DownstreamBytesWritten != int64(len(received)) || data.Integrity.WriteGap {
					t.Fatalf("客户端收到 %d 字节，轨迹 %+v，完整性 %+v", len(received), data.Flow, data.Integrity)
				}
				if authorized {
					if resp.StatusCode != 200 || string(received) != body || data.Detail.Status != "completed" || data.Detail.InputTokens != 10 || data.Detail.OutputTokens != 500 {
						t.Fatalf("订阅响应或结算有误: HTTP %d, 详情 %+v", resp.StatusCode, data.Detail)
					}
				} else if resp.StatusCode != 401 || data.Detail.Status != "failed" || data.Detail.UsageKnown || len(data.Flow.Contacts) != 0 || len(env.upstreamRequests()) != 0 {
					t.Fatalf("本地拒绝产生了上游联系或用量: HTTP %d，详情 %+v", resp.StatusCode, data.Detail)
				}
			})
		}
	}
}

func TestRequestFlow_ImageDeliveryAndFixedPrice(t *testing.T) {
	const body = `{"created":1,"data":[{"b64_json":"aW1hZ2U="}]}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	defer upstream.Close()
	tracker := newLifecyclePanelTestTracker(t)
	h := newTrackedImageGenerationHandler(t, tracker, upstream.URL, 0.25)
	req := httptest.NewRequest("POST", openAIImagesGenerationsPath, strings.NewReader(`{"prompt":"test"}`))
	req = req.WithContext(context.WithValue(req.Context(), "conn_id", "image-delivery"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	data := onlyPhaseRequest(t, tracker)
	if rec.Code != 200 || rec.Body.String() != body || data.Detail.TotalCostUSD != 0.25 || data.Flow == nil ||
		data.Flow.DownstreamBytesWritten != int64(len(body)) || data.Flow.DownstreamDeliveryOutcome != tracking.FlowDeliveryDelivered {
		t.Fatalf("图片响应或定价有误: HTTP %d，响应 %q，详情 %+v", rec.Code, rec.Body.String(), data)
	}
}

func TestRequestFlow_LateTraceAndBodyStayWithOriginalContact(t *testing.T) {
	type recorder interface {
		handlers.UpstreamTraceRecorder
		SetUpstreamResponse(*http.Response)
	}
	for _, subscription := range []bool{false, true} {
		t.Run(fmt.Sprintf("subscription=%t", subscription), func(t *testing.T) {
			tracker := newLifecyclePanelTestTracker(t)
			const id = "late-contact"
			var handle recorder
			var cancel func()
			if subscription {
				h := newSubscriptionTrackingObserver(tracker, nil).StartHTTP(SubscriptionHTTPStart{RequestID: id, Method: "POST", Path: "/v1/responses", RequestKind: tracking.RequestKindGeneration})
				handle = h
				cancel = func() { code := 499; h.settle("cancelled", "subscription_client_closed", nil, &code) }
			} else {
				h := NewRequestLifecycleManager(tracker, nil, id, nil)
				h.StartRequest("", "", "POST", "/v1/responses", true)
				handle = h
				cancel = func() { h.CancelRequest("disconnect", nil) }
			}
			trace := func() *httptrace.ClientTrace {
				ctx, _ := handlers.WithUpstreamTrace(context.Background(), handle)
				return httptrace.ContextClientTrace(ctx)
			}
			a, b := trace(), trace()
			a.GetConn("a")
			a.GotConn(httptrace.GotConnInfo{})
			a.GetConn("a") // 未写请求头，重新取连接仍属第一次联系。
			a.GotConn(httptrace.GotConnInfo{})
			a.WroteHeaders()
			a.GetConn("a") // 已写请求头，传输层重发产生第二次联系。
			resp := mockResponse("old", 429)
			handle.SetUpstreamResponse(resp)
			a.WroteRequest(httptrace.WroteRequestInfo{Err: io.ErrClosedPipe})
			b.GetConn("b")
			a.WroteRequest(httptrace.WroteRequestInfo{Err: io.ErrClosedPipe})
			a.GetConn("a")
			a.GetConn("a")
			a.GotFirstResponseByte()
			if _, err := io.ReadAll(resp.Body); err != nil {
				t.Fatal(err)
			}
			b.WroteHeaders()
			b.WroteRequest(httptrace.WroteRequestInfo{})
			handle.SetUpstreamResponse(mockResponse("ok", 200))
			cancel()
			data := onlyPhaseRequest(t, tracker)
			if data.Detail.Status != "cancelled" || data.Flow == nil || len(data.Flow.Contacts) != 3 {
				t.Fatalf("结算或重试次数错误: %+v", data)
			}
			first, retry, last := data.Flow.Contacts[0], data.Flow.Contacts[1], data.Flow.Contacts[2]
			if len(data.Flow.Legs) != 1 || first.OutcomeCode != "transport_retry" || first.ConnReadyOffsetMs == nil ||
				!retry.TransportResend || retry.OutcomeCode != "write_error" || retry.WriteDoneOffsetMs != nil || last.TransportResend {
				t.Fatalf("取连接、传输重发或写失败的归属有误：%+v", data.Flow)
			}
			if retry.HTTPStatusCode == nil || *retry.HTTPStatusCode != 429 || last.HTTPStatusCode == nil || *last.HTTPStatusCode != 200 ||
				last.FirstUpstreamByteOffsetMs != nil || last.WriteDoneOffsetMs == nil {
				t.Fatalf("上游回调或状态串线: %+v", data.Flow.Contacts)
			}
		})
	}
}

func TestRequestFlow_FlushErrorAcrossLoggingAndSubscription(t *testing.T) {
	tracker := newLifecyclePanelTestTracker(t)
	handle := newSubscriptionTrackingObserver(tracker, nil).StartHTTP(SubscriptionHTTPStart{
		RequestID: "flush", Method: "POST", Path: "/v1/responses", RequestKind: tracking.RequestKindGeneration,
	})
	cause := errors.New("flush failed")
	socket := &recordingFlushResponseWriter{flushErr: cause}
	logMW := middleware.NewLoggingMiddleware(slog.Default())
	logMW.SetRequestIDGenerator(func(context.Context) (string, error) { return "flush", nil })
	logMW.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		entry := newSubscriptionResponseWriter(w)
		writer := handle.WrapDownstreamWriter(entry)
		obs := entry.ResponseWriter.(*downstreamObservedResponseWriter)
		handle.ApplyParseResult(&ParseResult{IsCompleted: true, HasTokenUsage: true, TokenUsage: &tracking.TokenUsage{OutputTokens: 5}})
		// 模拟慢客户端：用量已归档，最后一次写出只能通过 SQLite 更新轨迹。
		waitSubscriptionDetail(t, tracker, "flush")
		restored := &subscriptionRestoreWriter{ResponseWriter: writer, upstream: mockResponse("", 200), media: "text/event-stream"}
		if _, err := restored.Write([]byte("event")); err != nil {
			t.Fatalf("改变了订阅还原写出的返回值: %v", err)
		}
		if socket.flushes != 1 || obs.Stats().FlushErrors != 1 || entry.bytesWritten() != 5 {
			t.Fatalf("写出/刷新未正确观测: %+v", obs.Stats())
		}
		if err := obs.FlushError(); !errors.Is(err, cause) {
			t.Fatalf("未取得底层错误: %v", err)
		}
		handle.finishDownstreamDelivery("")
	})).ServeHTTP(socket, httptest.NewRequest("POST", "/v1/responses", nil))
	deadline := time.Now().Add(3 * time.Second)
	for {
		data := timingLifecycleData(t, tracker, "flush")
		if data.Flow != nil && data.Flow.DownstreamDeliveryDoneOffsetMs != nil {
			if data.Detail.Status != "completed" || data.Detail.OutputTokens != 5 || data.Flow.DownstreamBytesWritten != 5 ||
				!data.Flow.DownstreamFlushError || data.Flow.DownstreamDeliveryOutcome != tracking.FlowDeliveryPartialOrFailed {
				t.Fatalf("归档后交付更新改变结算或遗漏刷新失败: %+v", data)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("归档后未记录最终交付")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRequestFlow_ForwardedWSCancelledTerminal(t *testing.T) {
	tracker := newLifecyclePanelTestTracker(t)
	observer := newSubscriptionTrackingObserver(tracker, nil)
	sub := &subscriptionSubmission{RequestID: "cancel-delivered", Generate: true, StartedAt: time.Now()}
	observer.SubmissionStarted(sub)
	observer.SubmissionDownstreamDelivered(sub, 43, nil, time.Now())
	observer.SubmissionTerminal(sub, &ParseResult{IsCompleted: true, IsCancelled: true, HasTokenUsage: true, TokenUsage: &tracking.TokenUsage{OutputTokens: 5}})
	data := onlyPhaseRequest(t, tracker)
	if data.Detail.Status != "cancelled" || data.Detail.OutputTokens != 5 || data.Flow == nil ||
		data.Flow.DownstreamBytesWritten != 43 || data.Flow.DownstreamDeliveryOutcome != tracking.FlowDeliveryDelivered {
		t.Fatalf("取消帧交付或结算错误: %+v", data)
	}
}

func TestRequestFlow_DisconnectBeforeWSTerminal(t *testing.T) {
	tracker := newLifecyclePanelTestTracker(t)
	observer := newSubscriptionTrackingObserver(tracker, nil)
	sub := &subscriptionSubmission{RequestID: "disconnect", Generate: true, StartedAt: time.Now()}
	observer.SubmissionStarted(sub)
	observer.SubmissionDownstreamDelivered(sub, 11, nil, time.Now())
	observer.SubmissionInterrupted(sub, tracking.SubscriptionClientDisconnected)
	data := onlyPhaseRequest(t, tracker)
	if data.Detail.Status != "cancelled" || data.Detail.UsageKnown || data.Flow == nil || data.Flow.DownstreamBytesWritten != 11 ||
		data.Flow.DownstreamDeliveryOutcome != tracking.FlowDeliveryAborted {
		t.Fatalf("客户端提前断开时的结算或交付错误: %+v", data)
	}
}

func TestRequestFlow_SQLiteFailureWithoutRoutingDiagnostics(t *testing.T) {
	tracker := newTimingTestTracker(t, false)
	lm := NewRequestLifecycleManager(tracker, nil, "early-failure", nil)
	lm.StartRequest("", "", "POST", "/v1/responses", false)
	lm.FailRequest("account_pool_not_ready", "not ready", 503)
	data := onlyPhaseRequest(t, tracker)
	if data.Detail.FailureReason != "account_pool_not_ready" || data.Integrity.WriteGap {
		t.Fatalf("正常业务失败的记录有误: %+v", data)
	}
}
