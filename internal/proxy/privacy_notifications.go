package proxy

import (
	"sync"

	"sinuo/internal/events"
	"sinuo/internal/privacy"
	"sinuo/internal/proxy/handlers"
	"sinuo/internal/tracking"
)

// BindPrivacyState 独立于生成记录登记，为每次 WS 隐私处理绑定唯一摘要出口。
// 生成请求复用已登记的摘要；预热和追加输入只通知，不创建记录或借用父响应的摘要。
func (o *subscriptionTrackingObserver) BindPrivacyState(state *handlers.PrivacyRequestState, submission *subscriptionSubmission) {
	if o == nil || state == nil {
		return
	}
	if handle := o.submissionHandle(submission, false); handle != nil {
		state.SetSummaryRecorder(handle.recordPrivacySummary)
		return
	}
	state.SetSummaryRecorder(newPrivacySummaryWriter(nil, o.eventBus, state.RequestID))
}

// newPrivacySummaryWriter 先保存权威摘要，再通知 UI；每请求每异常码仅推送一次。
// 复用请求摘要的生命周期，避免全局无限增长的 requestID 去重表。
func newPrivacySummaryWriter(tracker *tracking.UsageTracker, bus events.EventBus, requestID string) func(string) {
	var mu sync.Mutex
	var seen []string
	return func(payload string) {
		if payload == "" || requestID == "" {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if tracker != nil {
			tracker.RecordRequestUpdate(requestID, tracking.UpdateOptions{PrivacyScanJSON: &payload})
		}
		current := privacy.WarningsFromSummary(payload)
		merged := privacy.MergeWarnings(seen, current)
		if len(merged) == len(seen) {
			return
		}
		seen = merged
		if bus != nil {
			bus.Publish(events.Event{Type: events.EventPrivacyWarning, Source: "privacy_summary", Priority: events.PriorityHigh,
				Data: map[string]interface{}{"kind": "privacy_warning", "request_id": requestID, "warning_codes": append([]string(nil), seen...)}})
			bus.Publish(events.Event{Type: events.EventRequestUpdated, Source: "privacy_summary", Priority: events.PriorityNormal,
				Data: map[string]interface{}{"request_id": requestID, "change_type": "privacy_warning"}})
		}
	}
}
