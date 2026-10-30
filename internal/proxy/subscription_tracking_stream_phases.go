package proxy

import (
	"net/http"
	"time"

	"sinuo/internal/events"
	"sinuo/internal/proxy/handlers"
	"sinuo/internal/tracking"
)

func (t *subscriptionRequestTracking) SetUpstreamResponse(resp *http.Response) {
	if resp == nil {
		return
	}
	id := handlers.UpstreamRequestID(resp.Header)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.active && !t.settled {
		t.phaseRequestID = &id
	}
}

func (t *subscriptionRequestTracking) ObserveStreamResult(result *ParseResult, readBytes int, at time.Time) {
	t.mu.Lock()
	if !t.active || t.settled {
		t.mu.Unlock()
		return
	}
	if t.phaseCollector == nil {
		t.phaseCollector = newStreamPhaseCollector(t.startedAt)
	}
	firstAt := t.phaseCollector.Observe(result, readBytes, at)
	if firstAt == nil {
		t.mu.Unlock()
		return
	}
	t.observer.tracker.RecordRequestUpdate(t.requestID, tracking.UpdateOptions{
		StreamPhases:   tracking.StreamPhases{FirstContentMs: upstreamOffset(t.startedAt, *firstAt)},
		FirstContentAt: firstAt,
	})
	t.mu.Unlock()
	t.observer.publish(events.EventRequestUpdated, events.PriorityNormal, firstContentEventData(t.requestID, *firstAt))
}

func (t *subscriptionRequestTracking) recordStopReason(reason string) {
	if reason == "" {
		return
	}
	t.mu.Lock()
	if t.active && !t.settled {
		t.phaseStopReason = &reason
	}
	t.mu.Unlock()
}

// streamPhaseUpdateLocked 在 settle 设置终态前取快照；与 completion 同笔更新。
func (t *subscriptionRequestTracking) streamPhaseUpdateLocked() tracking.UpdateOptions {
	phases := tracking.StreamPhases{}
	if t.phaseCollector != nil {
		phases = t.phaseCollector.Snapshot(streamReasoningTokens(t.responseState.String()))
	}
	phases.StopReason, phases.UpstreamRequestID = t.phaseStopReason, t.phaseRequestID
	return tracking.UpdateOptions{StreamPhases: phases}
}

func (o *subscriptionTrackingObserver) SubmissionStreamResult(submission *subscriptionSubmission, result *ParseResult, size int64, at time.Time) {
	if handle := o.submissionHandle(submission, false); handle != nil {
		handle.ObserveStreamResult(result, int(size), at)
	}
}
