package proxy

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"sinuo/internal/events"
	"sinuo/internal/tracking"
)

func TestStreamPhases_SubscriptionCaptureFirstEventBeforeContent(t *testing.T) {
	tracker := newLifecyclePanelTestTracker(t)
	bus := &subscriptionPresentationEventBus{}
	track := newSubscriptionTrackingObserver(tracker, bus).StartHTTP(SubscriptionHTTPStart{
		RequestID: "synthetic-capture", StartedAt: time.Now().Add(-time.Second), Method: "POST", Path: "/v1/responses", RequestKind: tracking.RequestKindGeneration, IsStreaming: true,
	})
	capture := &subscriptionResponseCapture{mode: "sse", onResult: track.ApplyParseResult, phaseObserver: track.ObserveStreamResult,
		onOutput: func(at time.Time) { track.MarkFirstOutput(1, at) },
	}
	_, _ = capture.Write([]byte("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"synthetic-response\"}}\n\n"))
	if len(timingEvents(bus.published, requestChangeFirstResponse)) != 1 || len(timingEvents(bus.published, requestChangeFirstContent)) != 0 {
		t.Fatal("首个事件必须先记首响，不能伪造首个内容")
	}
	_, _ = capture.Write([]byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"synthetic\"}\n\n"))
	_, _ = capture.Write([]byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"synthetic\"}\n\n"))
	track.Complete(nil, "subscription_interrupted")
	data := onlyPhaseRequest(t, tracker)
	if data.FirstContentMs == nil || data.FirstTextMs == nil || len(timingEvents(bus.published, requestChangeFirstContent)) != 1 {
		t.Fatal("中断前必须补齐内容，且只发一次")
	}
	track.ObserveStreamResult(&ParseResult{ReasoningHidden: true}, 100, time.Now())
	if *timingLifecycleData(t, tracker, track.requestID).ReasoningHidden {
		t.Fatal("终态后更新必须拒绝")
	}
}

func TestStreamPhases_CodexSubscriptionHTTPEndToEnd(t *testing.T) {
	body := phaseFixture(t, "codex_hidden_reasoning")
	tracker := newLifecyclePanelTestTracker(t)
	env := newSubscriptionTestEnv(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Request-Id", "synthetic-final-upstream")
		_, _ = io.WriteString(w, body)
	}, "subscription")
	env.handler.proxy.SetUsageTracker(tracker)
	resp := env.request("POST", "responses", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer synthetic-client")
		r.Header.Set("Content-Type", "application/json")
		r.Body = io.NopCloser(strings.NewReader(`{"model":"gpt-5.5","input":[],"stream":true}`))
	})
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	requireObservedPipeline(t, onlyPhaseRequest(t, tracker), true)
}

func syntheticWSPhaseUpstream(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		if _, _, err := conn.Read(r.Context()); err != nil {
			return
		}
		for _, line := range strings.Split(body, "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			if err := conn.Write(r.Context(), websocket.MessageText, []byte(strings.TrimPrefix(line, "data: "))); err != nil {
				return
			}
		}
		_, _, _ = conn.Read(r.Context())
	}
}

func TestStreamPhases_CodexSubscriptionWebSocketEndToEnd(t *testing.T) {
	tracker := newLifecyclePanelTestTracker(t)
	env := newSubscriptionTestEnv(t, syntheticWSPhaseUpstream(phaseFixture(t, "codex_hidden_reasoning")), "subscription")
	env.handler.proxy.SetUsageTracker(tracker)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(env.server.URL, "http")+"/subscription/"+subscriptionTestSecret+"/backend-api/codex/responses", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": []string{"Bearer synthetic-client"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"response.create","model":"gpt-5.5","input":[]}`)); err != nil {
		t.Fatal(err)
	}
	for range 12 {
		if _, _, err := conn.Read(ctx); err != nil {
			t.Fatal(err)
		}
	}
	_ = conn.Close(websocket.StatusNormalClosure, "done")
	data := onlyPhaseRequest(t, tracker)
	if data.FirstContentMs == nil || data.FirstTextMs == nil || data.ReasoningHidden == nil || !*data.ReasoningHidden || *data.StopReason != "completed" || data.UpstreamRequestID != nil || data.Detail.FirstTokenMs == nil {
		t.Fatalf("%+v", data)
	}
}

func TestStreamPhases_SubscriptionTrackingSQLiteCancellation(t *testing.T) {
	tracker := newTimingTestTracker(t, false)
	track := newSubscriptionTrackingObserver(tracker, nil).StartHTTP(SubscriptionHTTPStart{
		RequestID: "synthetic-subscription-db", StartedAt: time.Now().Add(-time.Second), Method: "POST", Path: "/v1/responses", RequestKind: tracking.RequestKindGeneration, IsStreaming: true,
	})
	track.MarkFirstOutput(1, time.Now())
	track.ObserveStreamResult(&ParseResult{ContentKind: ContentTool, ContentBytes: 50}, 50, time.Now())
	track.settle("cancelled", "subscription_cancelled", nil, nil)
	data := onlyPhaseRequest(t, tracker)
	if data.FirstContentMs == nil || data.FirstTextMs != nil || data.StreamFlowMs == nil || data.Detail.Status != "cancelled" {
		t.Fatalf("%+v", data)
	}
}

func TestStreamPhases_SubscriptionCaptureCountsPreambleBytes(t *testing.T) {
	collector := newStreamPhaseCollector(time.Now())
	capture := &subscriptionResponseCapture{phaseObserver: func(result *ParseResult, n int, at time.Time) { collector.Observe(result, n, at) }}
	_, _ = capture.Write([]byte(strings.Repeat(" ", streamPhaseReadLimit+1)))
	_, _ = capture.Write([]byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"synthetic\"}\n\n"))
	p := collector.Snapshot(0)
	if p.FirstContentMs == nil || p.StreamFlowMs != nil {
		t.Fatalf("前导空白不能绕过8MB上限: %+v", p)
	}
}

func TestStreamPhases_ReviewSubscriptionPublishesAfterWriteAndUnlock(t *testing.T) {
	tracker := newLifecyclePanelTestTracker(t)
	bus := &streamPhaseReviewEventBus{}
	track := newSubscriptionTrackingObserver(tracker, bus).StartHTTP(SubscriptionHTTPStart{
		RequestID: "synthetic-review-subscription", StartedAt: time.Now().Add(-time.Second),
		Method: "POST", Path: "/v1/responses", RequestKind: tracking.RequestKindGeneration, IsStreaming: true,
	})
	track.MarkFirstOutput(1, track.startedAt.Add(100*time.Millisecond))
	firstEvents, completionEvents := 0, 0
	bus.onPublish = func(event events.Event) {
		change := event.Data["change_type"]
		if change != requestChangeFirstContent && change != requestChangeGenerationCompleted {
			return
		}
		if !track.mu.TryLock() {
			t.Fatal("发布事件时不能持有订阅追踪锁")
		}
		track.mu.Unlock()
		data := timingLifecycleData(t, tracker, track.requestID)
		requirePhaseMs(t, data.FirstContentMs, 200)
		if change == requestChangeFirstContent {
			firstEvents++
		} else {
			completionEvents++
			if data.Detail.CompletionMs == nil || *data.Detail.CompletionMs != event.Data["completion_ms"] || data.StreamFlowMs == nil {
				t.Fatal("完成事件必须晚于completion与阶段快照写入")
			}
		}
	}
	track.ObserveStreamResult(&ParseResult{ContentKind: ContentText, ContentBytes: 10}, 10, track.startedAt.Add(200*time.Millisecond))
	track.settle("cancelled", "subscription_cancelled", nil, nil)
	track.ObserveStreamResult(&ParseResult{ContentKind: ContentText}, 0, time.Now())
	track.settle("cancelled", "subscription_cancelled", nil, nil)
	if firstEvents != 1 || completionEvents != 1 {
		t.Fatalf("事件只能发布一次: first=%d completion=%d", firstEvents, completionEvents)
	}
}
