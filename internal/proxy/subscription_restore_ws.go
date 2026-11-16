package proxy

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/tidwall/gjson"

	"sinuo/internal/privacy"
	"sinuo/internal/proxy/handlers"
	"sinuo/internal/proxy/restorestream"
	"sinuo/internal/proxy/subscriptionws"
)

func (i *subscriptionWSInterceptor) popPendingWrite() *subscriptionSubmission {
	i.writeMu.Lock()
	defer i.writeMu.Unlock()
	if len(i.pendingWrites) == 0 {
		return nil
	}
	submission := i.pendingWrites[0]
	i.pendingWrites[0] = nil
	i.pendingWrites = i.pendingWrites[1:]
	return submission
}

func submissionStreamID(sub *subscriptionSubmission) string {
	if sub == nil {
		return ""
	}
	return sub.StreamID
}

func (s *subscriptionSubmissionState) markDispatched(sub *subscriptionSubmission, view *privacy.RestoreView) {
	if sub == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sub.dispatched = true
	sub.restoreDecided = true
	if view != nil {
		sub.restore = restorestream.New(nil, "/v1/responses", view)
		s.restoring.Add(1)
	}
}

// 无 response_id 的消息只能归入唯一的在途响应。带 stream_id 时先限定所属 lane。
// 创建请求被拒时只考虑未绑定的提交，其他错误也只能结算唯一匹配项。
func (s *subscriptionSubmissionState) deliveryTarget(id, streamID, typ string, strict, pendingOnly bool) *subscriptionSubmission {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != "" {
		sub := s.byResponseID[id]
		if sub == nil || sub.terminalRead != nil {
			return nil
		}
		// 已知响应的错误不能用另一个命名流来结算，包括原响应位于默认流的情况。
		checkLane := sub.StreamID != "" || strict && (typ == "error" || subscriptionWSControlReceipt(typ))
		if checkLane && streamID != "" && sub.StreamID != streamID {
			return nil
		}
		return sub
	}
	var target, pending *subscriptionSubmission
	pendingCount := 0
	for _, sub := range s.active {
		if !sub.dispatched || sub.settled || sub.terminalRead != nil || streamID != "" && sub.StreamID != streamID {
			continue
		}
		if !strict || typ == "error" {
			// 普通透传模式中，尚未绑定的 create 按发送顺序接收服务端拒绝。
			if !strict && typ == "error" && sub.ResponseID == "" && len(s.pending) == len(s.active) {
				return sub
			}
			if pendingOnly && sub.ResponseID != "" {
				continue
			}
			if target != nil {
				return nil
			}
			target = sub
			continue
		}
		if sub.ResponseID == "" {
			if pending == nil {
				pending = sub
			}
			pendingCount++
			continue
		}
		if target != nil {
			return nil
		}
		target = sub
	}
	if target != nil {
		return target
	}
	if pendingCount == 1 {
		return pending
	}
	return nil
}

// 上游错误可能回显请求参数。对下游和请求详情都只提供结构化诊断，保留可操作的错误码。
func restoreWSErrorFrameForRoute(raw []byte, route subscriptionWSRoute, validRoute bool) (frame []byte, _ bool, err error) {
	root := gjson.ParseBytes(raw)
	detail := root.Get("error")
	kind := ""
	if detail.IsObject() {
		kind = detail.Get("type").Str
	} else {
		// 扁平事件的顶层 type 是事件名，诊断信息位于同级的 code 和 message。
		detail = root
	}
	code := detail.Get("code").Str
	recognized := wsErrorIdentifier(code) || wsErrorIdentifier(kind) || detail.Get("message").Type == gjson.String
	if !wsErrorIdentifier(code) {
		code = "upstream_error"
	}
	if !wsErrorIdentifier(kind) {
		kind = "upstream_error"
	}
	msg := "the upstream rejected the WebSocket request"
	if code == "websocket_connection_limit_reached" {
		msg = "the upstream WebSocket connection has expired; reconnect to continue"
	}
	info := map[string]string{"type": kind, "code": code, "message": msg}
	switch param := detail.Get("param").Str; param {
	case "stream_id", "previous_response_id", "model", "input":
		info["param"] = param
	}
	out := map[string]any{"type": "error", "error": info}
	for _, field := range []string{"status", "sequence_number"} {
		if v := root.Get(field); v.Exists() {
			n, err := strconv.ParseInt(v.Raw, 10, 64)
			if err == nil && n >= 0 && (field != "status" || n >= 400 && n <= 599) {
				out[field] = n
			}
		}
	}
	// 错误的归属字段可能会把错误结算给另一条在途响应。仍交付安全诊断，跳过归属。
	if validRoute && route.responseID != "" {
		out["response_id"] = route.responseID
	}
	if validRoute && route.streamID != "" {
		out["stream_id"] = route.streamID
	}
	if !validRoute {
		delete(out, "response_id")
		delete(out, "stream_id")
	}
	frame, err = json.Marshal(out)
	return frame, validRoute && (recognized || out["response_id"] != nil || out["stream_id"] != nil), err
}

func wsErrorIdentifier(s string) bool {
	if len(s) == 0 || len(s) > 256 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}

func (s *subscriptionSubmissionState) duplicateRestoreTerminal(id, streamID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sub := s.byResponseID[id]; sub != nil {
		return sub.terminalRead != nil && (streamID == "" || sub.StreamID == "" || streamID == sub.StreamID)
	}
	end, ok := s.recentIDs[id]
	return ok && (streamID == "" || end.streamID == "" || streamID == end.streamID)
}

// 控制回执可在终态前后到达。序号偏移必须在串行写协程读取，此时队列前面的补发已经完成。
func (i *subscriptionWSInterceptor) controlReceipt(payload []byte, route subscriptionWSRoute) subscriptionws.InterceptResult {
	s := i.submissions
	res := subscriptionws.InterceptResult{Forward: payload}
	id := s.controlRouteID(route)
	if id == "" {
		return res
	}
	if gjson.GetBytes(payload, "sequence_number").Exists() {
		res.Deliver = func(send func([]byte) error, _ func([]byte, func()) error) error {
			out, err := restorestream.OffsetWebSocketSequence(payload, s.controlSequenceOffset(id, route.streamID))
			if err != nil {
				return rejectRestoreMessage(send, nil)
			}
			return send(out)
		}
	}
	sub := s.deliveryTarget(id, route.streamID, route.typ, true, false)
	return i.observeDownstreamWSDelivery(sub, res)
}

// 没有目标 ID 的回执只关联唯一可识别的响应。终态可能已经入队，但尚未交付。
func (s *subscriptionSubmissionState) controlResponseID(streamID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var id string
	for _, sub := range s.byResponseID {
		if streamID != "" && sub.StreamID != streamID {
			continue
		}
		if id != "" {
			return ""
		}
		id = sub.ResponseID
	}
	if streamID == "" {
		return id
	}
	// 同一 lane 可被后继响应复用；没有 response_id 时不能猜测回执属于哪一轮。
	for responseID, end := range s.recentIDs {
		if end.streamID == streamID {
			if id != "" {
				return ""
			}
			id = responseID
		}
	}
	return id
}

func (s *subscriptionSubmissionState) controlSequenceOffset(id, streamID string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sub := s.byResponseID[id]; sub != nil {
		if sub.restore != nil && (streamID == "" || sub.StreamID == "" || streamID == sub.StreamID) {
			return sub.restore.SequenceOffset()
		}
		return 0
	}
	end := s.recentIDs[id]
	if streamID != "" && end.streamID != "" && streamID != end.streamID {
		return 0
	}
	return end.sequenceOffset
}

func (i *subscriptionWSInterceptor) currentSnapshot() *privacy.Snapshot {
	filter := i.filter
	if wrapped, ok := filter.(subscriptionPrivacyFilter); ok {
		filter = wrapped.inner
	}
	if source, ok := filter.(interface{ CurrentSnapshot() *privacy.Snapshot }); ok {
		return source.CurrentSnapshot()
	}
	return nil
}

// 队列中保留原始消息。读侧关联并计量原文，写侧逐消息回填和确认交付。
func (i *subscriptionWSInterceptor) prepareUpstream(payload []byte) (subscriptionws.InterceptResult, error) {
	res := subscriptionws.InterceptResult{Forward: payload}
	snapshot := i.currentSnapshot()
	enabled := snapshot != nil && snapshot.Settings.Mode == privacy.ModeRedact && snapshot.Settings.ResponseRestoreEnabled
	// 结构凭证只说明本地回填是否可处理，不能按连接开关直接拒绝某条响应。
	msg, _ := restorestream.ValidateMessage(payload)
	ev, valid := readSubscriptionWSEvent(payload, msg)
	protectUnowned := enabled || i.submissions.restoring.Load() > 0
	strict := protectUnowned || i.submissions.bypassed.Load() > 0
	ev, err := i.submissions.observeDecodedUpstream(payload, msg, ev, valid, strict, protectUnowned)
	if errors.Is(err, restorestream.ErrDelivery) {
		res.Deliver = rejectRestoreMessage
		return res, nil
	}
	if err != nil {
		return subscriptionws.InterceptResult{}, err
	}
	if ev.duplicate {
		return subscriptionws.InterceptResult{}, nil
	}
	if ev.errorFrame != nil {
		// 清洗后的错误是新帧，不能沿用原始错误的校验结果。
		payload, res.Forward = ev.errorFrame, ev.errorFrame
		msg, err = restorestream.ValidateMessage(payload)
		if err != nil {
			res.Deliver = rejectRestoreMessage
			return res, nil
		}
		if ev.submission == nil && !ev.connectionExpired {
			return res, nil
		}
	}
	if ev.connectionExpired {
		res.Deliver = func(send func([]byte) error, _ func([]byte, func()) error) error {
			if err := send(payload); err != nil {
				return err
			}
			return errors.New("upstream WebSocket connection expired")
		}
		return res, nil
	}
	if ev.controlReceipt {
		return i.controlReceipt(payload, subscriptionWSRoute{ev.typ, ev.responseID, ev.streamID}), nil
	}
	if ev.metadata {
		return res, nil
	}
	sub := ev.submission
	if sub == nil || ev.typ == "" {
		if protectUnowned {
			res.Deliver = rejectRestoreMessage
		}
		return res, nil
	}
	if sub.ServerInitiated && !sub.restoreDecided {
		// 只在首个 created 上决定策略；重复 created 与后来切换配置不能重新开启保护。
		opening, err := i.prepareSuccessor(sub, snapshot, msg)
		if err != nil {
			res.Deliver = rejectRestoreMessage
			return res, nil
		}
		if opening != nil {
			writer := sub.restore
			res.Deliver = func(send func([]byte) error, _ func([]byte, func()) error) error {
				return writer.DeliverPreparedMessage(opening, send)
			}
			return i.observeDownstreamWSDelivery(sub, res), nil
		}
	}
	if sub.restore != nil {
		res.Deliver = func(send func([]byte) error, _ func([]byte, func()) error) error {
			err := sub.restore.WriteValidatedMessage(msg, send)
			if errors.Is(err, restorestream.ErrDelivery) {
				sub.reportRestore("failed", "privacy_restore_failed")
				return rejectRestoreMessage(send, nil)
			}
			return err
		}
	}
	if ev.terminal {
		res.Forwarded = func() {
			status := "completed"
			if ev.result.IsCancelled || ev.result.ErrorInfo != nil || ev.result.Status == StatusErrorAPI {
				status = "upstream_error"
			}
			sub.reportRestore(status, "")
			if after := i.submissions.finishTerminal(sub, ev.responseID, ev.result); after != nil {
				after()
			}
		}
	}
	return i.observeDownstreamWSDelivery(sub, res), nil
}

func (i *subscriptionWSInterceptor) observeDownstreamWSDelivery(sub *subscriptionSubmission, res subscriptionws.InterceptResult) subscriptionws.InterceptResult {
	if sub == nil || i.submissions == nil || i.submissions.observer == nil || res.Forward == nil {
		return res
	}
	observer, ok := i.submissions.observer.(interface {
		SubmissionDownstreamDelivered(*subscriptionSubmission, int64, error, time.Time)
	})
	if !ok {
		return res
	}
	deliver, payload := res.Deliver, res.Forward
	res.Deliver = func(send func([]byte) error, reply func([]byte, func()) error) error {
		observedSend := func(data []byte) error {
			err := send(data)
			observer.SubmissionDownstreamDelivered(sub, int64(len(data)), err, time.Now())
			return err
		}
		if deliver != nil {
			return deliver(observedSend, reply)
		}
		return observedSend(payload)
	}
	return res
}

// 首帧在读侧完成全部预处理，写侧只发送准备好的帧并确认交付。
// 因而同响应后续帧即使已读入队列，也能看到唯一且固定的保护/放行状态。
func (i *subscriptionWSInterceptor) prepareSuccessor(sub *subscriptionSubmission, snapshot *privacy.Snapshot, msg restorestream.ValidatedMessage) ([]byte, error) {
	sub.restoreDecided = true
	view, result, err := snapshot.OpenResponseRestoreView(msg.RestoreSupported())
	var opening []byte
	if view != nil {
		view.BindToolReplayScope(privacy.Request{UpstreamType: privacy.UpstreamTypeSubscription, SessionID: sub.SessionID})
		writer := restorestream.New(nil, "/v1/responses", view)
		opening, err = writer.PrepareOpeningMessage(msg)
		if err == nil {
			// 只有全部处理成功才发布写入器；字典交付计数仍等实际发送成功。
			i.submissions.mu.Lock()
			sub.successorView, sub.restore = view, writer
			i.submissions.restoring.Add(1)
			i.submissions.mu.Unlock()
			return opening, nil
		}
		writer.Release()
		view.Release()
		if errors.Is(err, restorestream.ErrLocalCapacity) {
			result, err = result.ResolveResponseRestoreFailure()
		}
	}
	if err != nil {
		sub.restoreRejected = true
		sub.PrivacyState.RecordRestore(handlers.RestoreSummary{Status: "failed", Reason: "privacy_restore_failed"})
		return nil, err
	}
	if result.FailOpen {
		i.submissions.markRestoreBypassed(sub)
		sub.PrivacyState.RecordRestore(handlers.RestoreSummary{Status: "bypassed", Reason: result.SafeFailureMessage()})
		i.warn("privacy_restore_bypassed", "request_id", sub.RequestID, "message", "已按配置原文放行，隐私保护未完成", "reason", result.SafeFailureMessage())
	}
	return nil, nil
}

func rejectRestoreMessage(send func([]byte) error, _ func([]byte, func()) error) error {
	if err := send(subscriptionWSErrorReply(http.StatusBadGateway, "privacy_restore_failed", "unable to deliver the upstream response")); err != nil {
		return err
	}
	return restorestream.ErrDelivery
}

func (sub *subscriptionSubmission) reportRestore(status, reason string) {
	if sub.restore == nil || sub.restoreStatus != "" {
		return
	}
	sub.restoreStatus = status
	c := sub.restore.Delivered()
	summary := handlers.RestoreSummary{Status: status, Reason: reason, Text: c.Text, Tool: c.Tool, UnknownText: c.UnknownText, UnknownTool: c.UnknownTool}
	sub.PrivacyState.RecordRestore(summary)
	logPrivacyDelivery(sub.RequestID, "/v1/responses", "websocket", summary)
}
