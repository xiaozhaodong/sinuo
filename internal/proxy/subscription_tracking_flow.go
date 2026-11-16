package proxy

import (
	"net/http"
	"net/http/httptrace"
	"time"

	"sinuo/internal/proxy/handlers"
	"sinuo/internal/tracking"
)

// subscriptionFlowState 维护订阅直通 HTTP 与 WS 请求的实时路径状态（受 subscriptionRequestTracking.mu 保护）。
type subscriptionFlowState struct {
	initialized bool
	deferEvents bool
	finalized   bool
	transport   string
	origin      string
	httpFlowState
	upstreamTerminalObserved bool
	pendingTerminalAt        *time.Time // 锁外上报期间，结算可带上同一个幂等终态事件。
	wsBytesWritten           int64
	downstreamWriteErr       bool
	wsTerminalDelivered      bool
	downstreamWriter         *downstreamObservedResponseWriter
	deferredEvents           []tracking.RequestFlowEvent
}

func defaultSubscriptionFlowTarget() tracking.RequestFlowTargetSnapshot {
	return tracking.RequestFlowTargetSnapshot{
		UpstreamType:       "subscription",
		UpstreamSourceName: subscriptionTrackingUpstreamName,
		UpstreamName:       subscriptionTrackingUpstreamName,
	}
}

func (t *subscriptionRequestTracking) emitFlowEvents(events ...tracking.RequestFlowEvent) {
	if t == nil || t.observer == nil || t.observer.tracker == nil || !t.active || t.requestID == "" || len(events) == 0 {
		return
	}
	t.mu.Lock()
	if t.flow.deferEvents && !t.flow.finalized {
		t.flow.deferredEvents = append(t.flow.deferredEvents, events...)
		t.mu.Unlock()
		return
	}
	t.mu.Unlock()
	t.observer.tracker.RecordRequestUpdate(t.requestID, tracking.UpdateOptions{FlowEvents: events})
}

func (t *subscriptionRequestTracking) initFlow(transport, origin string) {
	if t == nil || !t.active || t.requestID == "" {
		return
	}
	if transport == "" {
		transport = tracking.FlowTransportHTTP
	}
	if origin == "" {
		origin = tracking.FlowOriginClientRequest
	}
	target := defaultSubscriptionFlowTarget()

	t.mu.Lock()
	if t.flow.initialized {
		t.mu.Unlock()
		return
	}
	t.flow.initialized = true
	// 热池已满时本请求直接登记到 SQLite，过程事件随结算合并写入。
	t.flow.deferEvents = !t.observer.tracker.IsRequestActive(t.requestID)
	t.flow.transport = transport
	t.flow.origin = origin
	t.flow.legSeq = 1
	startedAt := t.startedAt
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	events := []tracking.RequestFlowEvent{
		{
			Kind:      tracking.FlowEventInit,
			At:        &startedAt,
			Transport: transport,
			Origin:    origin,
		},
		{
			Kind:   tracking.FlowEventLegSelected,
			At:     &startedAt,
			LegSeq: 1,
			Target: &target,
		},
	}
	if transport == tracking.FlowTransportWS && origin == tracking.FlowOriginServerSuccessor {
		// 服务端 steering successor 没有客户端 create，直接建立隐式上游联系与写出完成点。
		t.flow.contactSeq = 1
		t.flow.contactActive = true
		t.flow.contactConnReady = true
		t.flow.contactWriteStart = true
		t.flow.contactWriteDone = true
		events = append(events, tracking.RequestFlowEvent{
			Kind:       tracking.FlowEventContactStart,
			At:         &startedAt,
			LegSeq:     1,
			ContactSeq: 1,
		})
	}
	t.mu.Unlock()

	t.emitFlowEvents(events...)
}

// WrapDownstreamWriter 在订阅 HTTP 直通最外层包装 http.ResponseWriter（位于 subscriptionRestoreWriter 之外）。
func (t *subscriptionRequestTracking) WrapDownstreamWriter(w http.ResponseWriter) http.ResponseWriter {
	if t == nil || !t.active || w == nil {
		return w
	}
	wrapped, dw := wrapDownstreamObservedWriter(w, t.httpFlow().downstreamFirstWrite)
	t.mu.Lock()
	t.flow.downstreamWriter = dw
	t.mu.Unlock()
	return wrapped
}

func (t *subscriptionRequestTracking) httpFlow() httpFlowRecorder {
	return httpFlowRecorder{httpFlowOwner: t}
}

func (t *subscriptionRequestTracking) lockHTTPFlow() *httpFlowState {
	if t == nil || !t.active || t.requestID == "" {
		return nil
	}
	t.mu.Lock()
	if t.settled || t.flow.finalized {
		t.mu.Unlock()
		return nil
	}
	return &t.flow.httpFlowState
}

func (t *subscriptionRequestTracking) unlockHTTPFlow() {
	t.mu.Unlock()
}

func (t *subscriptionRequestTracking) RecordUpstreamFlowGetConn(previous int, attempt handlers.UpstreamConnAttempt) int {
	return t.httpFlow().getConn(previous, attempt)
}

func (t *subscriptionRequestTracking) RecordUpstreamFlowGotConn(contact int, at time.Time, _ httptrace.GotConnInfo) {
	t.httpFlow().gotConn(contact, at)
}

func (t *subscriptionRequestTracking) RecordUpstreamFlowWroteHeaders(contact int, at time.Time) {
	t.httpFlow().wroteHeaders(contact, at)
}

func (t *subscriptionRequestTracking) RecordUpstreamFlowWroteRequest(contact int, timing handlers.UpstreamWriteTiming, writeErr error) {
	t.httpFlow().wroteRequest(contact, timing, writeErr)
}

func (t *subscriptionRequestTracking) RecordUpstreamFlowFirstResponseByte(contact int, at time.Time) {
	t.httpFlow().firstResponseByte(contact, at)
}

func (t *subscriptionRequestTracking) observeUpstreamHTTPResponse(resp *http.Response) {
	t.httpFlow().observeResponse(resp)
}

func (t *subscriptionRequestTracking) finishDownstreamDelivery(copyOutcome string) {
	if t == nil || t.observer == nil {
		return
	}
	t.mu.Lock()
	writer := t.flow.downstreamWriter
	t.mu.Unlock()
	writer.finishDelivery(t.observer.tracker, t.requestID, copyOutcome)
}

func (t *subscriptionRequestTracking) recordFlowWSOutbound(at time.Time) {
	if t == nil || !t.active || t.requestID == "" {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	events := make([]tracking.RequestFlowEvent, 0, 3)

	t.mu.Lock()
	if !t.active || t.settled || t.flow.finalized {
		t.mu.Unlock()
		return
	}
	if !t.flow.contactActive {
		t.flow.contactSeq++
		t.flow.contactActive = true
		events = append(events, tracking.RequestFlowEvent{
			Kind:       tracking.FlowEventContactStart,
			At:         &at,
			LegSeq:     t.flow.legSeq,
			ContactSeq: t.flow.contactSeq,
		})
	}
	seq := t.flow.contactSeq
	if !t.flow.contactWriteStart {
		t.flow.contactWriteStart = true
		events = append(events, tracking.RequestFlowEvent{
			Kind:       tracking.FlowEventContactWriteStart,
			At:         &at,
			ContactSeq: seq,
		})
	}
	if !t.flow.contactWriteDone {
		t.flow.contactWriteDone = true
		events = append(events, tracking.RequestFlowEvent{
			Kind:       tracking.FlowEventContactWriteDone,
			At:         &at,
			ContactSeq: seq,
		})
	}
	t.mu.Unlock()

	t.emitFlowEvents(events...)
}

// wsFirstByteEventsLocked 仅在当前 WS 联系首次收到字节时产生详情里程碑。
func (t *subscriptionRequestTracking) wsFirstByteEventsLocked(at time.Time) []tracking.RequestFlowEvent {
	if t.flow.transport != tracking.FlowTransportWS || t.flow.finalized || t.flow.contactFirstByte || t.responseBytes == 0 {
		return nil
	}
	if at.IsZero() {
		at = time.Now()
	}
	var events []tracking.RequestFlowEvent
	if !t.flow.contactActive {
		t.flow.contactSeq++
		t.flow.contactActive = true
		events = append(events, tracking.RequestFlowEvent{
			Kind:       tracking.FlowEventContactStart,
			At:         &at,
			LegSeq:     t.flow.legSeq,
			ContactSeq: t.flow.contactSeq,
		})
	}
	t.flow.contactFirstByte = true
	return append(events, tracking.RequestFlowEvent{
		Kind:       tracking.FlowEventContactFirstByte,
		At:         &at,
		ContactSeq: t.flow.contactSeq,
	})
}

func (t *subscriptionRequestTracking) recordFlowWSDownstreamDelivered(bytes int64, writeErr error, at time.Time) {
	if t == nil || !t.active || t.requestID == "" {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}

	t.mu.Lock()
	if !t.active || t.settled || t.flow.finalized {
		t.mu.Unlock()
		return
	}
	if writeErr != nil {
		t.flow.downstreamWriteErr = true
		t.mu.Unlock()
		return
	}
	if bytes <= 0 {
		t.mu.Unlock()
		return
	}
	t.flow.wsBytesWritten += bytes
	first := !t.flow.downstreamFirstWrite
	if first {
		t.flow.downstreamFirstWrite = true
	}
	t.mu.Unlock()

	if first {
		t.emitFlowEvents(tracking.RequestFlowEvent{
			Kind:  tracking.FlowEventDownstreamFirstWrite,
			At:    &at,
			Bytes: bytes,
		})
	}
}

// finalizeFlowBeforeSettle 在结算前记录最后一次联系和 WS 交付，HTTP 交付由入口收口。
func (t *subscriptionRequestTracking) finalizeFlowBeforeSettle(status, failureCode string) {
	if t == nil || !t.active || t.requestID == "" {
		return
	}
	now := time.Now()
	events := make([]tracking.RequestFlowEvent, 0, 5)

	t.mu.Lock()
	if t.flow.finalized {
		t.mu.Unlock()
		return
	}
	t.flow.finalized = true
	dw := t.flow.downstreamWriter
	if t.flow.contactActive {
		endEvt := tracking.RequestFlowEvent{
			Kind:        tracking.FlowEventContactEnd,
			At:          &now,
			ContactSeq:  t.flow.contactSeq,
			OutcomeCode: firstNonEmptyString(failureCode, status, "completed"),
		}
		events = append(events, endEvt)
		t.flow.resetContactLocked()
	}

	if dw != nil {
		dw.recordSettlement(status, failureCode)
	} else if t.flow.transport == tracking.FlowTransportWS {
		outcome := tracking.FlowDeliveryAborted
		switch {
		case t.flow.downstreamWriteErr || failureCode == "privacy_restore_failed":
			outcome = tracking.FlowDeliveryPartialOrFailed
		case t.flow.wsTerminalDelivered:
			outcome = tracking.FlowDeliveryDelivered
		case status != "cancelled" && t.flow.wsBytesWritten > 0:
			outcome = tracking.FlowDeliveryPartialOrFailed
		}
		events = append(events, tracking.RequestFlowEvent{Kind: tracking.FlowEventDownstreamDone,
			At: &now, Bytes: t.flow.wsBytesWritten, DeliveryOutcome: outcome})
	}
	deferred := t.flow.deferEvents
	if deferred {
		t.flow.deferredEvents = append(t.flow.deferredEvents, events...)
	}
	t.mu.Unlock()

	if !deferred {
		t.emitFlowEvents(events...)
	}
}

// SubmissionDownstreamDelivered 由 WS 拦截器在消息写回下游后触发，记录实际写出的字节。
func (o *subscriptionTrackingObserver) SubmissionDownstreamDelivered(submission *subscriptionSubmission, size int64, writeErr error, at time.Time) {
	if handle := o.submissionHandle(submission, false); handle != nil {
		handle.recordFlowWSDownstreamDelivered(size, writeErr, at)
	}
}
