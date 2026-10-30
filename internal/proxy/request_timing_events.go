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
	requestChangeUpstreamRetry       = "upstream_retry"
	requestChangeUpstreamSent        = "upstream_sent"
	requestChangeFirstResponse       = "first_response"
	requestChangeFirstContent        = "first_content"
	requestChangeGenerationCompleted = "generation_completed"
)

func firstContentEventData(requestID string, at time.Time) map[string]interface{} {
	return map[string]interface{}{
		"request_id": requestID, "change_type": requestChangeFirstContent,
		"first_content_at": timezonepolicy.FormatStorage(at),
	}
}

// upstreamSentEventData 上游请求写完：首响从这一刻起算，重试时随新的尝试后移。
func upstreamSentEventData(requestID string, startAt time.Time) map[string]interface{} {
	return map[string]interface{}{
		"request_id":           requestID,
		"change_type":          requestChangeUpstreamSent,
		"first_token_start_at": timezonepolicy.FormatStorage(startAt),
	}
}

// upstreamRetryEventData 每次再次联系上游时即时通知；取连接阶段只更新内存与事件。
func upstreamRetryEventData(requestID string, retries int, at time.Time) map[string]interface{} {
	return map[string]interface{}{
		"request_id": requestID, "change_type": requestChangeUpstreamRetry,
		"retry_count": retries, "retry_started_at": timezonepolicy.FormatStorage(at),
	}
}

// firstResponseEventData 首个事件落定；旧口径从此开始生成，新口径继续等待首个内容。
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
