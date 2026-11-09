package proxy

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"unicode/utf8"

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
		checkLane := sub.StreamID != "" || strict && typ == "error"
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
		if !strict {
			return sub
		}
		if typ == "error" {
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
func restoreWSErrorFrame(raw []byte) ([]byte, error) {
	root := gjson.ParseBytes(raw)
	detail := root.Get("error")
	if !detail.IsObject() {
		return nil, restorestream.ErrDelivery
	}
	code := detail.Get("code").Str
	kind := detail.Get("type").Str
	if !wsErrorIdentifier(code) && !wsErrorIdentifier(kind) && detail.Get("message").Type != gjson.String {
		return nil, restorestream.ErrDelivery
	}
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
			if err != nil || n < 0 || field == "status" && (n < 400 || n > 599) {
				return nil, restorestream.ErrDelivery
			}
			out[field] = n
		}
	}
	for _, field := range []string{"response_id", "stream_id"} {
		if v := root.Get(field); v.Exists() {
			if v.Type != gjson.String {
				return nil, restorestream.ErrDelivery
			}
			if wsErrorIdentifier(v.Str) {
				out[field] = v.Str
			}
		}
	}
	if nested := root.Get("response.id"); nested.Exists() {
		if nested.Type != gjson.String || !wsErrorIdentifier(nested.Str) || root.Get("response_id").Exists() && root.Get("response_id").Str != nested.Str {
			return nil, restorestream.ErrDelivery
		}
		out["response_id"] = nested.Str
	}
	return json.Marshal(out)
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

func (s *subscriptionSubmissionState) duplicateRestoreTerminal(id, streamID string, restoring bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sub := s.byResponseID[id]; sub != nil {
		return sub.terminalRead != nil && (streamID == "" || sub.StreamID == "" || streamID == sub.StreamID)
	}
	end, ok := s.recentIDs[id]
	return ok && (restoring || end.restored) && (streamID == "" || end.streamID == "" || streamID == end.streamID)
}

// 控制回执可在终态前后到达。序号偏移必须在串行写协程读取，此时队列前面的补发已经完成。
func (s *subscriptionSubmissionState) controlReceipt(payload []byte) subscriptionws.InterceptResult {
	res := subscriptionws.InterceptResult{Forward: payload}
	if !gjson.GetBytes(payload, "sequence_number").Exists() {
		return res
	}
	id := gjson.GetBytes(payload, "response_id").Str
	if id == "" {
		id = gjson.GetBytes(payload, "steer.previous_response_id").Str
	}
	streamID := gjson.GetBytes(payload, "stream_id").Str
	if id == "" {
		id = s.controlResponseID(streamID)
	}
	if id == "" {
		return res
	}
	res.Deliver = func(send func([]byte) error, _ func([]byte, func()) error) error {
		out, err := restorestream.OffsetWebSocketSequence(payload, s.controlSequenceOffset(id, streamID))
		if err != nil {
			return rejectRestoreMessage(send, nil)
		}
		return send(out)
	}
	return res
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

func (i *subscriptionWSInterceptor) shouldRestoreMessages() bool {
	if i.submissions.restoring.Load() > 0 {
		return true
	}
	s := i.currentSnapshot()
	return s != nil && s.Settings.Mode == privacy.ModeRedact && s.Settings.ResponseRestoreEnabled
}

// 队列中保留原始消息。读侧关联并计量原文，写侧逐消息回填和确认交付。
func (i *subscriptionWSInterceptor) prepareUpstream(payload []byte) (subscriptionws.InterceptResult, error) {
	res := subscriptionws.InterceptResult{Forward: payload}
	restoring := i.shouldRestoreMessages()
	if restoring && (!utf8.Valid(payload) || validateSubscriptionJSON(payload) != nil) {
		res.Deliver = rejectRestoreMessage
		return res, nil
	}
	ev, err := i.submissions.observeUpstream(payload, restoring)
	if errors.Is(err, restorestream.ErrDelivery) {
		res.Deliver = rejectRestoreMessage
		return res, nil
	}
	if err != nil {
		return res, err
	}
	if ev.duplicate {
		return subscriptionws.InterceptResult{}, nil
	}
	if ev.errorFrame != nil {
		payload, res.Forward = ev.errorFrame, ev.errorFrame
		if ev.connectionExpired {
			res.Deliver = func(send func([]byte) error, _ func([]byte, func()) error) error {
				if err := send(payload); err != nil {
					return err
				}
				return errors.New("upstream WebSocket connection expired")
			}
			return res, nil
		}
		if ev.submission == nil {
			return res, nil
		}
	}
	if ev.controlReceipt {
		return i.submissions.controlReceipt(payload), nil
	}
	if ev.metadata {
		return res, nil
	}
	sub := ev.submission
	if !restoring {
		if ev.terminal {
			res.Forwarded = i.submissions.finishTerminal(sub, ev.responseID, ev.result)
		}
		return res, nil
	}
	if sub == nil || ev.typ == "" {
		res.Deliver = rejectRestoreMessage
		return res, nil
	}
	if sub.ServerInitiated && ev.typ == "response.created" && sub.restore == nil {
		// 服务端后继没有客户端 create。独立固定新响应的字典代际。
		view, err := i.currentSnapshot().OpenResponseRestoreView()
		if err != nil {
			res.Deliver = rejectRestoreMessage
			return res, nil
		}
		if view != nil {
			view.BindToolReplayScope(privacy.Request{UpstreamType: privacy.UpstreamTypeSubscription, SessionID: sub.SessionID})
			sub.successorView = view
			i.submissions.markDispatched(sub, view)
		}
	}
	if sub.restore != nil {
		res.Deliver = func(send func([]byte) error, _ func([]byte, func()) error) error {
			err := sub.restore.WriteMessage(payload, send)
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
	return res, nil
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
