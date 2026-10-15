package proxy

import (
	"time"

	"sinuo/internal/events"
	timezonepolicy "sinuo/internal/timezone"
)

// 请求计时事件：首响计时起点、首响、生成耗时落定的那一刻，把后端的精确值随 request:update 推给请求页。
// 只发通知的话，请求页要等 250ms 合并刷新 + 查询才知道，跑秒会先跑过头再往回跳。
// 字段名与列表接口一致，端点管线与订阅代理共用。
const (
	requestChangeUpstreamSent        = "upstream_sent"
	requestChangeFirstResponse       = "first_response"
	requestChangeGenerationCompleted = "generation_completed"
)

// upstreamSentEventData 上游请求写完：首响从这一刻起算，重试时随新的尝试后移。
func upstreamSentEventData(requestID string, startAt time.Time) map[string]interface{} {
	return map[string]interface{}{
		"request_id":           requestID,
		"change_type":          requestChangeUpstreamSent,
		"first_token_start_at": timezonepolicy.FormatStorage(startAt),
	}
}

// firstResponseEventData 首响落定：首响位定格，生成位从首字时刻起算。
func firstResponseEventData(requestID string, firstTokenMs int64, firstTokenAt time.Time) map[string]interface{} {
	return map[string]interface{}{
		"request_id":     requestID,
		"change_type":    requestChangeFirstResponse,
		"first_token_ms": firstTokenMs,
		"first_token_at": timezonepolicy.FormatStorage(firstTokenAt),
	}
}

// generationCompletedEventData 生成耗时落定：生成位定格，不等终态查询。
func generationCompletedEventData(requestID string, completionMs int64) map[string]interface{} {
	return map[string]interface{}{
		"request_id":    requestID,
		"change_type":   requestChangeGenerationCompleted,
		"completion_ms": completionMs,
	}
}

// publishTimingEvent 发布计时事件；计时不改变 status，所以不走 notifyStatusChange。
func (rlm *RequestLifecycleManager) publishTimingEvent(data map[string]interface{}) {
	if rlm.eventBus == nil {
		return
	}
	rlm.eventBus.Publish(events.Event{
		Type:     events.EventRequestUpdated,
		Source:   "lifecycle_manager",
		Priority: events.PriorityNormal,
		Data:     data,
	})
}
