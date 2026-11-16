package proxy

import (
	"net/http"
	"net/http/httptrace"
	"sync"
	"time"

	"sinuo/internal/proxy/handlers"
	"sinuo/internal/tracking"
)

// lifecycleFlowState 维护单条请求的路径事件去重状态、当前 leg/contact 序号和下游包装器引用。
type lifecycleFlowState struct {
	mu          sync.Mutex
	initialized bool
	deferEvents bool
	finalized   bool
	httpFlowState
	upstreamTerminalObserved bool

	currentTarget    tracking.RequestFlowTargetSnapshot
	downstreamWriter *downstreamObservedResponseWriter
	deferredEvents   []tracking.RequestFlowEvent
}

// WrapDownstreamWriter 在最外层包装 http.ResponseWriter（位于 restorestream 与流式包装器之外）。
func (rlm *RequestLifecycleManager) WrapDownstreamWriter(w http.ResponseWriter) http.ResponseWriter {
	if rlm == nil || rlm.usageTracker == nil || w == nil {
		return w
	}
	wrapped, dw := wrapDownstreamObservedWriter(w, rlm.httpFlow().downstreamFirstWrite)
	rlm.flowState.mu.Lock()
	rlm.flowState.downstreamWriter = dw
	rlm.flowState.mu.Unlock()
	return wrapped
}

func (rlm *RequestLifecycleManager) emitFlowEvents(events ...tracking.RequestFlowEvent) {
	if rlm == nil || rlm.usageTracker == nil || rlm.requestID == "" || len(events) == 0 {
		return
	}
	rlm.flowState.mu.Lock()
	if rlm.flowState.deferEvents && !rlm.flowState.finalized {
		rlm.flowState.deferredEvents = append(rlm.flowState.deferredEvents, events...)
		rlm.flowState.mu.Unlock()
		return
	}
	rlm.flowState.mu.Unlock()
	rlm.usageTracker.RecordRequestUpdate(rlm.requestID, tracking.UpdateOptions{FlowEvents: events})
}

// recordFlowStart 在 StartRequest 成功登记后记录初始请求路径元信息。
func (rlm *RequestLifecycleManager) recordFlowStart() {
	if rlm == nil || rlm.requestID == "" {
		return
	}
	rlm.flowState.mu.Lock()
	rlm.flowState.deferEvents = !rlm.usageTracker.IsRequestActive(rlm.requestID)
	if rlm.flowState.initialized {
		rlm.flowState.mu.Unlock()
		return
	}
	rlm.flowState.initialized = true
	startAt := rlm.startTime
	rlm.flowState.mu.Unlock()

	rlm.emitFlowEvents(tracking.RequestFlowEvent{
		Kind:      tracking.FlowEventInit,
		At:        &startAt,
		Transport: tracking.FlowTransportHTTP,
		Origin:    tracking.FlowOriginClientRequest,
	})
}

// RecordFlowLegSelected 记录选中的候选目标（endpoint / account / subscription）；同目标重试不重复拆 leg。
func (rlm *RequestLifecycleManager) RecordFlowLegSelected(target tracking.RequestFlowTargetSnapshot, reasonCode string, forceNewLeg bool) {
	if rlm == nil || rlm.requestID == "" {
		return
	}
	now := time.Now()
	events := make([]tracking.RequestFlowEvent, 0, 2)

	rlm.flowState.mu.Lock()
	if rlm.flowState.finalized {
		rlm.flowState.mu.Unlock()
		return
	}
	if !rlm.flowState.initialized {
		rlm.flowState.initialized = true
		startAt := rlm.startTime
		events = append(events, tracking.RequestFlowEvent{
			Kind:      tracking.FlowEventInit,
			At:        &startAt,
			Transport: tracking.FlowTransportHTTP,
			Origin:    tracking.FlowOriginClientRequest,
		})
	}
	sameTarget := rlm.flowState.legSeq > 0 &&
		rlm.flowState.currentTarget.UpstreamType == target.UpstreamType &&
		rlm.flowState.currentTarget.UpstreamSourceName == target.UpstreamSourceName &&
		rlm.flowState.currentTarget.UpstreamName == target.UpstreamName &&
		rlm.flowState.currentTarget.UpstreamID == target.UpstreamID &&
		rlm.flowState.currentTarget.EndpointName == target.EndpointName &&
		rlm.flowState.currentTarget.RouteMode == target.RouteMode &&
		(rlm.flowState.currentTarget.UpstreamModel == target.UpstreamModel || target.UpstreamType == "endpoint" && target.UpstreamModel == "")

	if !forceNewLeg && sameTarget {
		rlm.flowState.mu.Unlock()
		return
	}

	if rlm.flowState.contactActive {
		endEvt := tracking.RequestFlowEvent{
			Kind:        tracking.FlowEventContactEnd,
			At:          &now,
			ContactSeq:  rlm.flowState.contactSeq,
			OutcomeCode: firstNonEmptyString(reasonCode, "leg_switched"),
		}
		events = append(events, endEvt)
		rlm.flowState.resetContactLocked()
	}

	rlm.flowState.legSeq++
	rlm.flowState.currentTarget = target
	tCopy := target
	events = append(events, tracking.RequestFlowEvent{
		Kind:        tracking.FlowEventLegSelected,
		At:          &now,
		LegSeq:      rlm.flowState.legSeq,
		ForceNewLeg: forceNewLeg,
		Target:      &tCopy,
		ReasonCode:  reasonCode,
	})
	rlm.flowState.mu.Unlock()

	rlm.emitFlowEvents(events...)
}

// 模型改写在发出请求时确定，补到同一次选路的快照。
func (rlm *RequestLifecycleManager) recordFlowUpstreamModel(model string) {
	state := &rlm.flowState
	state.mu.Lock()
	if state.legSeq == 0 || state.finalized || state.currentTarget.UpstreamModel == model {
		state.mu.Unlock()
		return
	}
	state.currentTarget.UpstreamModel = model
	seq, target := state.legSeq, state.currentTarget
	state.mu.Unlock()
	rlm.emitFlowEvents(tracking.RequestFlowEvent{Kind: tracking.FlowEventLegSelected, LegSeq: seq, Target: &target})
}

func (rlm *RequestLifecycleManager) ensureCurrentTargetLocked() tracking.RequestFlowTargetSnapshot {
	if rlm.flowState.currentTarget.UpstreamName != "" || rlm.flowState.currentTarget.EndpointName != "" || rlm.flowState.currentTarget.UpstreamType != "" {
		return rlm.flowState.currentTarget
	}
	state := rlm.snapshotState()
	rlm.modelMu.RLock()
	upstreamModel := rlm.upstreamModel
	rlm.modelMu.RUnlock()
	return tracking.RequestFlowTargetSnapshot{
		UpstreamType:       state.upstreamType,
		UpstreamSourceName: state.upstreamSourceName,
		UpstreamName:       state.upstreamName,
		UpstreamID:         state.upstreamID,
		EndpointName:       state.endpointName,
		RouteMode:          state.routeMode,
		UpstreamModel:      upstreamModel,
	}
}

func (rlm *RequestLifecycleManager) ensureLegLocked(at time.Time, events *[]tracking.RequestFlowEvent) {
	if !rlm.flowState.initialized {
		rlm.flowState.initialized = true
		startAt := rlm.startTime
		*events = append(*events, tracking.RequestFlowEvent{
			Kind:      tracking.FlowEventInit,
			At:        &startAt,
			Transport: tracking.FlowTransportHTTP,
			Origin:    tracking.FlowOriginClientRequest,
		})
	}
	if rlm.flowState.legSeq == 0 {
		rlm.flowState.legSeq = 1
		target := rlm.ensureCurrentTargetLocked()
		rlm.flowState.currentTarget = target
		*events = append(*events, tracking.RequestFlowEvent{
			Kind:   tracking.FlowEventLegSelected,
			At:     &at,
			LegSeq: rlm.flowState.legSeq,
			Target: &target,
		})
	}
}

func (rlm *RequestLifecycleManager) httpFlow() httpFlowRecorder {
	return httpFlowRecorder{httpFlowOwner: rlm, ensureLegLocked: rlm.ensureLegLocked}
}

func (rlm *RequestLifecycleManager) lockHTTPFlow() *httpFlowState {
	if rlm == nil || rlm.requestID == "" {
		return nil
	}
	rlm.flowState.mu.Lock()
	if rlm.flowState.finalized {
		rlm.flowState.mu.Unlock()
		return nil
	}
	return &rlm.flowState.httpFlowState
}

func (rlm *RequestLifecycleManager) unlockHTTPFlow() {
	rlm.flowState.mu.Unlock()
}

func (rlm *RequestLifecycleManager) RecordUpstreamFlowGetConn(previous int, attempt handlers.UpstreamConnAttempt) int {
	return rlm.httpFlow().getConn(previous, attempt)
}

func (rlm *RequestLifecycleManager) RecordUpstreamFlowGotConn(contact int, at time.Time, _ httptrace.GotConnInfo) {
	rlm.httpFlow().gotConn(contact, at)
}

func (rlm *RequestLifecycleManager) RecordUpstreamFlowWroteHeaders(contact int, at time.Time) {
	rlm.httpFlow().wroteHeaders(contact, at)
}

func (rlm *RequestLifecycleManager) RecordUpstreamFlowWroteRequest(contact int, timing handlers.UpstreamWriteTiming, writeErr error) {
	rlm.httpFlow().wroteRequest(contact, timing, writeErr)
}

func (rlm *RequestLifecycleManager) RecordUpstreamFlowFirstResponseByte(contact int, at time.Time) {
	rlm.httpFlow().firstResponseByte(contact, at)
}

func (rlm *RequestLifecycleManager) ObserveUpstreamFlowResponse(resp *http.Response) {
	rlm.httpFlow().observeResponse(resp)
}

func (rlm *RequestLifecycleManager) finishDownstreamDelivery() {
	if rlm == nil {
		return
	}
	rlm.flowState.mu.Lock()
	writer := rlm.flowState.downstreamWriter
	rlm.flowState.mu.Unlock()
	writer.finishDelivery(rlm.usageTracker, rlm.requestID, "")
}

// RecordFlowAttemptEnded 在单次候选/重试失败切换之前收口当前上游 contact。
func (rlm *RequestLifecycleManager) RecordFlowAttemptEnded(statusCode int, outcomeCode string) {
	if rlm == nil || rlm.requestID == "" {
		return
	}
	now := time.Now()

	rlm.flowState.mu.Lock()
	if rlm.flowState.finalized || !rlm.flowState.contactActive {
		rlm.flowState.mu.Unlock()
		return
	}
	seq := rlm.flowState.contactSeq
	hasHeaders := rlm.flowState.contactResponseHeaders
	rlm.flowState.resetContactLocked()
	rlm.flowState.mu.Unlock()

	evt := tracking.RequestFlowEvent{
		Kind:        tracking.FlowEventContactEnd,
		At:          &now,
		ContactSeq:  seq,
		OutcomeCode: firstNonEmptyString(outcomeCode, "attempt_failed"),
	}
	if !hasHeaders && statusCode > 0 {
		evt.HTTPStatusCode = &statusCode
	}
	rlm.emitFlowEvents(evt)
}

// MarkFlowUpstreamTerminal 标记已观测到完整的上游终态响应或事件。
func (rlm *RequestLifecycleManager) MarkFlowUpstreamTerminal() {
	if rlm == nil || rlm.requestID == "" {
		return
	}
	now := time.Now()

	rlm.flowState.mu.Lock()
	if rlm.flowState.finalized || rlm.flowState.upstreamTerminalObserved {
		rlm.flowState.mu.Unlock()
		return
	}
	rlm.flowState.upstreamTerminalObserved = true
	rlm.flowState.mu.Unlock()

	rlm.emitFlowEvents(tracking.RequestFlowEvent{
		Kind: tracking.FlowEventUpstreamTerminal,
		At:   &now,
	})
}

// finalizeFlowTerminal 在请求结算前收口上游联系，HTTP 交付由入口在实际写出后记录。
func (rlm *RequestLifecycleManager) finalizeFlowTerminal(status, reason string) {
	if rlm == nil || rlm.requestID == "" {
		return
	}
	now := time.Now()
	events := make([]tracking.RequestFlowEvent, 0, 5)

	rlm.flowState.mu.Lock()
	if rlm.flowState.finalized {
		rlm.flowState.mu.Unlock()
		return
	}
	rlm.flowState.finalized = true
	dw := rlm.flowState.downstreamWriter
	if rlm.flowState.contactActive {
		endEvt := tracking.RequestFlowEvent{
			Kind:        tracking.FlowEventContactEnd,
			At:          &now,
			ContactSeq:  rlm.flowState.contactSeq,
			OutcomeCode: firstNonEmptyString(reason, status, "completed"),
		}
		events = append(events, endEvt)
		rlm.flowState.resetContactLocked()
	}

	if dw != nil {
		dw.recordSettlement(status, reason)
	}
	if len(rlm.flowState.deferredEvents) > 0 {
		merged := make([]tracking.RequestFlowEvent, 0, len(rlm.flowState.deferredEvents)+len(events))
		merged = append(merged, rlm.flowState.deferredEvents...)
		merged = append(merged, events...)
		events = merged
		rlm.flowState.deferredEvents = nil
	}
	tracker := rlm.usageTracker
	requestID := rlm.requestID
	rlm.flowState.mu.Unlock()

	if tracker != nil && requestID != "" && len(events) > 0 {
		tracker.RecordRequestUpdate(requestID, tracking.UpdateOptions{FlowEvents: events})
	}
}
