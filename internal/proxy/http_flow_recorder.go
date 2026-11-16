package proxy

import (
	"net/http"
	"time"

	"sinuo/internal/proxy/handlers"
	"sinuo/internal/tracking"
)

// httpFlowState 保存两条 HTTP 链路共同的联系序号和里程碑，受所属请求的锁保护。
// 订阅 WS 使用同一份联系状态，以便沿用订阅请求的结算逻辑。
type httpFlowState struct {
	legSeq                 int
	contactSeq             int
	contactActive          bool
	contactConnReady       bool
	contactWriteStart      bool
	contactWriteDone       bool
	contactResponseHeaders bool
	contactFirstByte       bool
	downstreamFirstWrite   bool
}

func (s *httpFlowState) resetContactLocked() {
	s.contactActive = false
	s.contactConnReady = false
	s.contactWriteStart = false
	s.contactWriteDone = false
	s.contactResponseHeaders = false
	s.contactFirstByte = false
}

// lockHTTPFlow 只在请求仍接受事件时返回持锁的状态，空值表示调用方无需解锁。
type httpFlowOwner interface {
	lockHTTPFlow() *httpFlowState
	unlockHTTPFlow()
	emitFlowEvents(...tracking.RequestFlowEvent)
}

type httpFlowRecorder struct {
	httpFlowOwner
	// 普通代理在还没有选路时补建首段。订阅入口已在登记时创建首段。
	ensureLegLocked func(time.Time, *[]tracking.RequestFlowEvent)
}

func flowEventTime(at time.Time) time.Time {
	if at.IsZero() {
		return time.Now()
	}
	return at
}

func (r httpFlowRecorder) startContactLocked(s *httpFlowState, at time.Time, resend bool, events []tracking.RequestFlowEvent) []tracking.RequestFlowEvent {
	if s.legSeq == 0 && r.ensureLegLocked != nil {
		r.ensureLegLocked(at, &events)
	}
	s.contactSeq++
	s.contactActive = true
	return append(events, tracking.RequestFlowEvent{
		Kind: tracking.FlowEventContactStart, At: &at,
		LegSeq: s.legSeq, ContactSeq: s.contactSeq, TransportResend: resend,
	})
}

func (r httpFlowRecorder) getConn(previous int, attempt handlers.UpstreamConnAttempt) int {
	at := flowEventTime(attempt.At)
	s := r.lockHTTPFlow()
	if s == nil {
		return 0
	}
	seq := s.contactSeq
	if previous != 0 && previous != seq {
		r.unlockHTTPFlow()
		return 0
	}
	if attempt.SameContact && s.contactActive {
		s.contactConnReady = false
		r.unlockHTTPFlow()
		return seq
	}
	var events []tracking.RequestFlowEvent
	if s.contactActive {
		reason := "reconnect"
		if attempt.TransportRetry {
			reason = "transport_retry"
		}
		events = append(events, tracking.RequestFlowEvent{
			Kind: tracking.FlowEventContactEnd, At: &at, ContactSeq: seq, OutcomeCode: reason,
		})
		s.resetContactLocked()
	}
	events = r.startContactLocked(s, at, attempt.TransportRetry, events)
	seq = s.contactSeq
	r.unlockHTTPFlow()
	r.emitFlowEvents(events...)
	return seq
}

func (r httpFlowRecorder) lockContact(contact int) *httpFlowState {
	s := r.lockHTTPFlow()
	if s != nil && (!s.contactActive || s.contactSeq != contact) {
		r.unlockHTTPFlow()
		return nil
	}
	return s
}

// markMilestoneLocked 在所属请求的锁内去重，解锁后上报事件。
func (r httpFlowRecorder) markMilestoneLocked(seen *bool, kind tracking.FlowEventKind, contact int, at time.Time) {
	if *seen {
		r.unlockHTTPFlow()
		return
	}
	*seen = true
	r.unlockHTTPFlow()
	at = flowEventTime(at)
	r.emitFlowEvents(tracking.RequestFlowEvent{Kind: kind, At: &at, ContactSeq: contact})
}

func (r httpFlowRecorder) gotConn(contact int, at time.Time) {
	if s := r.lockContact(contact); s != nil {
		r.markMilestoneLocked(&s.contactConnReady, tracking.FlowEventContactConnReady, contact, at)
	}
}

func (r httpFlowRecorder) wroteHeaders(contact int, at time.Time) {
	if s := r.lockContact(contact); s != nil {
		r.markMilestoneLocked(&s.contactWriteStart, tracking.FlowEventContactWriteStart, contact, at)
	}
}

func (r httpFlowRecorder) wroteRequest(contact int, timing handlers.UpstreamWriteTiming, writeErr error) {
	s := r.lockContact(contact)
	if s == nil {
		return
	}
	if writeErr == nil {
		r.markMilestoneLocked(&s.contactWriteDone, tracking.FlowEventContactWriteDone, contact, timing.WroteAt)
		return
	}
	s.resetContactLocked()
	r.unlockHTTPFlow()
	at := flowEventTime(timing.WroteAt)
	r.emitFlowEvents(tracking.RequestFlowEvent{
		Kind: tracking.FlowEventContactEnd, At: &at, ContactSeq: contact, OutcomeCode: "write_error",
	})
}

func (r httpFlowRecorder) firstResponseByte(contact int, at time.Time) {
	if s := r.lockContact(contact); s != nil {
		r.markMilestoneLocked(&s.contactFirstByte, tracking.FlowEventContactFirstByte, contact, at)
	}
}

func (r httpFlowRecorder) observeResponse(resp *http.Response) {
	if resp == nil {
		return
	}
	at := time.Now()
	s := r.lockHTTPFlow()
	if s == nil {
		return
	}
	var events []tracking.RequestFlowEvent
	if !s.contactActive {
		events = r.startContactLocked(s, at, false, events)
	}
	seq := s.contactSeq
	if !s.contactResponseHeaders {
		s.contactResponseHeaders = true
		code := resp.StatusCode
		events = append(events, tracking.RequestFlowEvent{
			Kind: tracking.FlowEventContactResponseHeaders, At: &at, ContactSeq: seq, HTTPStatusCode: &code,
		})
	}
	r.unlockHTTPFlow()
	r.emitFlowEvents(events...)
	wrapUpstreamObservedBody(resp, func(at time.Time) { r.firstResponseByte(seq, at) })
}

func (r httpFlowRecorder) downstreamFirstWrite(bytes int64, at time.Time) {
	s := r.lockHTTPFlow()
	if s == nil {
		return
	}
	if s.downstreamFirstWrite {
		r.unlockHTTPFlow()
		return
	}
	s.downstreamFirstWrite = true
	r.unlockHTTPFlow()
	at = flowEventTime(at)
	r.emitFlowEvents(tracking.RequestFlowEvent{Kind: tracking.FlowEventDownstreamFirstWrite, At: &at, Bytes: bytes})
}
