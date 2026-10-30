package proxy

import (
	"net/http"
	"testing"
	"time"

	"sinuo/internal/events"
	"sinuo/internal/proxy/handlers"
	timezonepolicy "sinuo/internal/timezone"
	"sinuo/internal/tracking"
)

func TestStreamPhases_LifecycleCompleteFailureCancellation(t *testing.T) {
	for _, status := range []string{"completed", "failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			tracker := newLifecyclePanelTestTracker(t)
			bus := &subscriptionPresentationEventBus{}
			rlm := NewRequestLifecycleManager(tracker, nil, "synthetic-"+status, bus)
			rlm.SetStartTime(time.Now().Add(-time.Second))
			rlm.StartRequest("127.0.0.1", "synthetic", "POST", "/v1/messages", true)
			rlm.SetUpstreamResponse(&http.Response{Header: http.Header{"Request-Id": []string{"synthetic-first"}}})
			rlm.SetUpstreamResponse(&http.Response{Header: http.Header{"Request-Id": []string{"synthetic-final"}}})
			rlm.RecordUpstreamWrite(handlers.UpstreamWriteTiming{WroteAt: rlm.startTime.Add(10 * time.Millisecond)})
			rlm.RecordFirstToken()
			at := rlm.startTime.Add(200 * time.Millisecond)
			rlm.ObserveStreamResult(&ParseResult{ContentKind: ContentReasoning, ContentBytes: 10}, 10, at)
			rlm.ObserveStreamResult(&ParseResult{ContentKind: ContentText, ContentBytes: 10}, 10, at.Add(time.Millisecond))
			finishPhaseLifecycle(rlm, status)
			rlm.ObserveStreamResult(&ParseResult{ReasoningHidden: true, ContentKind: ContentText}, 100, at.Add(time.Hour))
			rlm.SetUpstreamResponse(&http.Response{Header: http.Header{"Request-Id": []string{"synthetic-late"}}})
			data := timingLifecycleData(t, tracker, rlm.requestID)
			requirePhaseMs(t, data.FirstContentMs, 200)
			requirePhaseMs(t, data.FirstTextMs, 201)
			if data.UpstreamRequestID == nil || *data.UpstreamRequestID != "synthetic-final" || *data.ReasoningHidden {
				t.Fatalf("%+v", data.StreamPhases)
			}
			items := timingEvents(bus.published, requestChangeFirstContent)
			if len(items) != 1 || items[0].Data["first_content_at"] != timezonepolicy.FormatStorage(at) {
				t.Fatalf("首内容事件=%+v", items)
			}
		})
	}
}

func finishPhaseLifecycle(rlm *RequestLifecycleManager, status string) {
	switch status {
	case "completed":
		rlm.RecordStreamCompletion()
		rlm.CompleteRequest(&tracking.TokenUsage{OutputTokens: 10})
	case "failed":
		rlm.FailRequest("synthetic_failure", "synthetic", 502)
	case "cancelled":
		rlm.CancelRequest("synthetic_cancel", nil)
	}
}

type streamPhaseReviewEventBus struct {
	events.EventBus
	onPublish func(events.Event)
}

func (b *streamPhaseReviewEventBus) Publish(event events.Event) {
	if b.onPublish != nil {
		b.onPublish(event)
	}
}

func TestStreamPhases_ReviewLifecyclePublishesAfterWriteAndUnlock(t *testing.T) {
	tracker := newLifecyclePanelTestTracker(t)
	bus := &streamPhaseReviewEventBus{}
	rlm := NewRequestLifecycleManager(tracker, nil, "synthetic-review-lifecycle", bus)
	rlm.SetStartTime(time.Now().Add(-time.Second))
	rlm.StartRequest("127.0.0.1", "synthetic", "POST", "/v1/messages", true)
	rlm.RecordFirstToken()
	firstEvents, completionEvents := 0, 0
	bus.onPublish = func(event events.Event) {
		change := event.Data["change_type"]
		if change != requestChangeFirstContent && change != requestChangeGenerationCompleted {
			return
		}
		if !rlm.timingMu.TryLock() {
			t.Fatal("发布事件时不能持有timingMu")
		}
		recorded := rlm.phasesRecorded
		rlm.timingMu.Unlock()
		data := timingLifecycleData(t, tracker, rlm.requestID)
		requirePhaseMs(t, data.FirstContentMs, 200)
		if change == requestChangeFirstContent {
			firstEvents++
		} else {
			completionEvents++
			if !recorded || data.Detail.CompletionMs == nil || *data.Detail.CompletionMs != event.Data["completion_ms"] {
				t.Fatal("完成事件必须晚于阶段快照和completion写入")
			}
		}
	}
	rlm.ObserveStreamResult(&ParseResult{ContentKind: ContentText, ContentBytes: 10}, 10, rlm.startTime.Add(200*time.Millisecond))
	rlm.RecordStreamCompletion()
	rlm.CompleteRequest(nil)
	rlm.RecordStreamCompletion()
	rlm.ObserveStreamResult(&ParseResult{ContentKind: ContentText}, 0, time.Now())
	if firstEvents != 1 || completionEvents != 1 {
		t.Fatalf("终态后不能发布晚到事件: first=%d completion=%d", firstEvents, completionEvents)
	}
}
