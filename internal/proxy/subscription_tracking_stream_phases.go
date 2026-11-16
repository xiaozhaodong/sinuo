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
	if t.active && !t.settled {
		t.phaseRequestID = &id
	}
	t.mu.Unlock()
	t.observeUpstreamHTTPResponse(resp)
}

// markFlowUpstreamTerminal 在结算前保留首个协议终态的观测时刻。
func (t *subscriptionRequestTracking) markFlowUpstreamTerminal(at time.Time) {
	t.mu.Lock()
	if !t.active || t.flow.finalized || t.flow.upstreamTerminalObserved {
		t.mu.Unlock()
		return
	}
	t.flow.upstreamTerminalObserved = true
	event := tracking.RequestFlowEvent{Kind: tracking.FlowEventUpstreamTerminal, At: &at}
	if t.flow.deferEvents {
		t.flow.deferredEvents = append(t.flow.deferredEvents, event)
		t.mu.Unlock()
		return
	}
	t.flow.pendingTerminalAt = &at
	t.mu.Unlock()
	t.observer.tracker.RecordRequestUpdate(t.requestID, tracking.UpdateOptions{FlowEvents: []tracking.RequestFlowEvent{event}})
	t.mu.Lock()
	t.flow.pendingTerminalAt = nil
	t.mu.Unlock()
}

func (t *subscriptionRequestTracking) ObserveStreamResult(result *ParseResult, readBytes int, at time.Time) {
	if result != nil && result.IsCompleted {
		t.markFlowUpstreamTerminal(at)
	}
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
	var flowEvents []tracking.RequestFlowEvent
	if len(t.flow.deferredEvents) > 0 {
		flowEvents = append([]tracking.RequestFlowEvent(nil), t.flow.deferredEvents...)
		t.flow.deferredEvents = nil
	}
	// 上报与结算并发时，先把同一观测时刻并入结算前的更新。reducer 只接受首次终态。
	if at := t.flow.pendingTerminalAt; at != nil {
		flowEvents = append(flowEvents, tracking.RequestFlowEvent{Kind: tracking.FlowEventUpstreamTerminal, At: at})
	}
	return tracking.UpdateOptions{
		StreamPhases: phases,
		FlowEvents:   flowEvents,
	}
}

func (o *subscriptionTrackingObserver) SubmissionStreamResult(submission *subscriptionSubmission, result *ParseResult, size int64, at time.Time) {
	if handle := o.submissionHandle(submission, false); handle != nil {
		handle.ObserveStreamResult(result, int(size), at)
	}
}
