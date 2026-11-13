package proxy

import "github.com/tidwall/gjson"

// 调用方持有 s.mu。校验放行归属与真正绑定 created 必须采用同一选择规则。
func (s *subscriptionSubmissionState) pendingCreatedIndexLocked(streamID string) int {
	selected := -1
	for n, candidate := range s.pending {
		if candidate.settled || candidate.terminalRead != nil || !candidate.dispatched {
			continue
		}
		if streamID == "" || streamID == candidate.StreamID {
			return n
		}
		if selected == -1 && candidate.StreamID == "" {
			selected = n
		}
	}
	return selected
}

func (s *subscriptionSubmissionState) markRestoreBypassed(sub *subscriptionSubmission) {
	if sub == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed && !sub.restoreBypassed && !sub.settled {
		sub.restoreBypassed = true
		s.bypassed.Add(1)
	}
}

// 连接计数仅辅助无归属事件的保守处理，不能决定已绑定响应的能力限制。
func (s *subscriptionSubmissionState) submissionRestores(sub *subscriptionSubmission) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return sub != nil && sub.restore != nil
}

// 预判与实际观测共用错误分类，不能把已绑定响应算进创建失败的候选集合。
func subscriptionWSErrorDisposition(raw []byte) (pendingOnly, connectionExpired bool) {
	detail := gjson.ParseBytes(raw)
	if nested := detail.Get("error"); nested.IsObject() {
		detail = nested
	}
	switch detail.Get("code").Str {
	case "invalid_stream_id", "websocket_stream_limit_reached", "previous_response_not_found", "previous_response_id_not_found":
		return true, false
	case "websocket_connection_limit_reached":
		return false, true
	}
	return false, false
}
