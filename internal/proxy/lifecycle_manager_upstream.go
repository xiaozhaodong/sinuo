package proxy

import (
	"fmt"
	"log/slog"
	"sinuo/internal/store"
	"time"

	"sinuo/internal/events"
	"sinuo/internal/proxy/handlers"
	"sinuo/internal/tracking"
)

// UpdateStatus 更新请求状态；重试次数统一按联系上游次数计算。
func (rlm *RequestLifecycleManager) UpdateStatus(status string, httpStatus int) {
	state := rlm.snapshotState()
	currentModel := rlm.GetModelName()
	rlm.modelUpdateMu.Lock()
	shouldUpdateModel := currentModel != "" && currentModel != "unknown" && !rlm.modelUpdatedInDB
	if shouldUpdateModel {
		rlm.modelUpdatedInDB = true
	}
	rlm.modelUpdateMu.Unlock()

	rlm.timingMu.Lock()
	retries := max(rlm.upstreamContacts-1, 0)
	opts := tracking.UpdateOptions{
		StreamPhases: tracking.StreamPhases{StopReason: rlm.phaseStopReason, UpstreamRequestID: rlm.phaseRequestID},
		EndpointName: &state.endpointName, RequestFamily: &state.requestFamily,
		UpstreamType: &state.upstreamType, UpstreamSourceName: &state.upstreamSourceName,
		UpstreamName: &state.upstreamName, UpstreamID: &state.upstreamID,
		Status: &status, RetryCount: &retries, HttpStatus: &httpStatus,
	}
	if shouldUpdateModel {
		opts.ModelName = &currentModel
	}
	if rlm.usageTracker != nil && rlm.requestID != "" {
		rlm.attachRouteDiagnostics(&opts, state)
		rlm.attachResponseDiagnostics(&opts)
		rlm.usageTracker.RecordRequestUpdate(rlm.requestID, opts)
	}
	rlm.timingMu.Unlock()
	rlm.notifyStatusChange(status, httpStatus)
}

// notifyStatusChange 统一的状态通知方法
// 负责更新内部状态、发布事件通知和记录状态变更日志
// 这个方法被 UpdateStatus、CompleteRequest、FailRequest、CancelRequest 统一调用
func (rlm *RequestLifecycleManager) notifyStatusChange(status string, httpStatus int) {
	retryCount := rlm.GetRetryCount()
	// 更新内部状态
	rlm.stateMu.Lock()
	rlm.lastStatus = status
	state := lifecycleStateSnapshot{
		requestFamily:      rlm.requestFamily,
		requestSource:      rlm.requestSource,
		requestKind:        rlm.requestKind,
		endpointName:       rlm.endpointName,
		upstreamType:       rlm.upstreamType,
		upstreamSourceName: rlm.upstreamSourceName,
		upstreamName:       rlm.upstreamName,
		upstreamID:         rlm.upstreamID,
		retryCount:         retryCount,
		lastStatus:         rlm.lastStatus,
	}
	rlm.stateMu.Unlock()

	rlm.publishStatusChange(status, httpStatus, state)
	rlm.logStatusChange(status, httpStatus, state)
}

func (rlm *RequestLifecycleManager) publishStatusChange(status string, httpStatus int, state lifecycleStateSnapshot) {
	retryCount := state.retryCount
	// 发布请求状态更新事件
	if rlm.eventBus != nil {
		// 根据状态确定优先级
		priority := events.PriorityNormal
		changeType := "status_changed"

		switch status {
		case "error", "timeout":
			priority = events.PriorityHigh
			changeType = "error_response"
		case "suspended":
			changeType = "suspended_change"
		case "retry":
			changeType = "retry_attempt"
		case "completed":
			changeType = "request_completed"
		case "failed":
			priority = events.PriorityHigh
			changeType = "request_failed"
		case "cancelled":
			changeType = "request_cancelled"
		}

		rlm.eventBus.Publish(events.Event{
			Type:     events.EventRequestUpdated,
			Source:   "lifecycle_manager",
			Priority: priority,
			Data: map[string]interface{}{
				"request_id":           rlm.requestID,
				"request_family":       state.requestFamily,
				"request_source":       state.requestSource,
				"request_kind":         state.requestKind,
				"endpoint_name":        state.endpointName,
				"upstream_type":        state.upstreamType,
				"upstream_source_name": state.upstreamSourceName,
				"upstream_name":        state.upstreamName,
				"upstream_id":          state.upstreamID,
				"status":               status,
				"retry_count":          retryCount,
				"http_status":          httpStatus,
				"model_name":           rlm.GetModelName(),
				"change_type":          changeType,
			},
		})
	}

}

func (rlm *RequestLifecycleManager) logStatusChange(status string, httpStatus int, state lifecycleStateSnapshot) {
	retryCount := state.retryCount
	// 记录状态变更日志
	switch status {
	case "forwarding":
		slog.Info(fmt.Sprintf("🎯 [请求转发] [%s] 选择端点: %s",
			rlm.requestID, state.endpointName))
	case "retry":
		slog.Info(fmt.Sprintf("🔄 [需要重试] [%s] 端点: %s (重试次数: %d)",
			rlm.requestID, state.endpointName, retryCount))
	case "processing":
		slog.Info(fmt.Sprintf("⚙️ [请求处理] [%s] 端点: %s, 状态码: %d",
			rlm.requestID, state.endpointName, httpStatus))
	case "suspended":
		slog.Warn(fmt.Sprintf("⏸️ [请求挂起] [%s] 端点: %s",
			rlm.requestID, state.endpointName))
	case "cancelled":
		// 取消日志已在 CancelRequest 方法中记录完整信息，此处跳过避免重复
	case "error":
		slog.Error(fmt.Sprintf("❌ [请求错误] [%s] 端点: %s, 状态码: %d",
			rlm.requestID, state.endpointName, httpStatus))
	case "timeout":
		slog.Error(fmt.Sprintf("⏰ [请求超时] [%s] 端点: %s",
			rlm.requestID, state.endpointName))
	case "completed":
		slog.Info(fmt.Sprintf("✅ [请求完成] [%s] 端点: %s",
			rlm.requestID, state.endpointName))
	case "failed":
		// 失败日志已在 FailRequest 方法中记录完整信息，此处跳过避免重复
	}
}

// RecordFirstToken 记录首个有效响应到达耗时（首响）。
// 流式请求以首个有效 SSE 事件为准；非流请求以完整响应可用为准。
func (rlm *RequestLifecycleManager) RecordFirstToken() {
	rlm.RecordFirstTokenAndReturn()
}

// RecordFirstTokenAndReturn 记录首响并返回本次调用是否首次记录成功。
func (rlm *RequestLifecycleManager) RecordFirstTokenAndReturn() bool {
	recorded := false
	rlm.firstTokenOnce.Do(func() {
		if rlm.usageTracker == nil || rlm.requestID == "" {
			return
		}

		now := time.Now()
		rlm.timingMu.Lock()
		start := rlm.firstTokenStartTime
		if start.IsZero() {
			start = rlm.startTime
		}
		firstTokenMs := now.Sub(start).Milliseconds()
		if firstTokenMs < 0 {
			firstTokenMs = 0
		}
		rlm.firstTokenAt = now
		// 极快响应可能先于写完钩子；同笔补齐首次联系，避免被首响冻结规则拒绝。
		opts := rlm.contactUpdate(rlm.startTime)
		opts.FirstTokenMs, opts.FirstTokenAt = &firstTokenMs, &now
		rlm.usageTracker.RecordRequestUpdate(rlm.requestID, opts)
		rlm.persistedContacts = rlm.upstreamContacts
		rlm.timingMu.Unlock()
		recorded = true
		rlm.publishTimingEvent(firstResponseEventData(rlm.requestID, firstTokenMs, now))

		slog.Info(fmt.Sprintf("📝 [首响耗时] [%s] 记录上游首个有效响应耗时: %dms",
			rlm.requestID, firstTokenMs))
	})
	return recorded
}

// SetFirstTokenStartTime 测试辅助：只有写完时刻时复用最终尝试的记录规则。
func (rlm *RequestLifecycleManager) SetFirstTokenStartTime(start time.Time) {
	rlm.RecordUpstreamWrite(handlers.UpstreamWriteTiming{WroteAt: start})
}

// RecordUpstreamGetConn 在首响前计数；此钩子不得访问数据库。
func (rlm *RequestLifecycleManager) RecordUpstreamGetConn(attempt handlers.UpstreamConnAttempt) {
	if attempt.SameContact {
		logSameContactReconnect(rlm.requestID, rlm.snapshotState().upstreamName, attempt)
		return
	}
	rlm.timingMu.Lock()
	if !rlm.firstTokenAt.IsZero() || rlm.upstreamSettled {
		rlm.timingMu.Unlock()
		return
	}
	rlm.recordContact(attempt.At)
	contacts := rlm.upstreamContacts
	if contacts > 1 {
		rlm.publishTimingEvent(upstreamRetryEventData(rlm.requestID, contacts-1, attempt.At))
	}
	rlm.timingMu.Unlock()
	state := rlm.snapshotState()
	logTransportRetry(rlm.requestID, state.upstreamName, contacts, attempt)
}

// RecordUpstreamWrite 与最终尝试计时同笔写入首次联系时间与重试数，首响后冻结。
func (rlm *RequestLifecycleManager) RecordUpstreamWrite(timing handlers.UpstreamWriteTiming) {
	rlm.recordAttemptWrite(timing, rlm.billingChannel(), 0)
}

type attemptTrace struct {
	*RequestLifecycleManager
	channel  store.BillingChannel
	sequence uint64
}

func (rlm *RequestLifecycleManager) billingChannel() store.BillingChannel {
	rlm.stateMu.RLock()
	defer rlm.stateMu.RUnlock()
	return store.BillingChannel{Type: rlm.upstreamType, ID: rlm.upstreamID, Name: rlm.upstreamName, SubscriptionAccount: rlm.subscriptionAccount}
}
func (rlm *RequestLifecycleManager) bindAttemptTrace() *attemptTrace {
	channel := rlm.billingChannel()
	rlm.timingMu.Lock()
	defer rlm.timingMu.Unlock()
	rlm.attemptSequence++
	return &attemptTrace{rlm, channel, rlm.attemptSequence}
}
func (a *attemptTrace) RecordUpstreamWrite(timing handlers.UpstreamWriteTiming) {
	a.recordAttemptWrite(timing, a.channel, a.sequence)
}

func (rlm *RequestLifecycleManager) recordAttemptWrite(timing handlers.UpstreamWriteTiming, channel store.BillingChannel, seq uint64) {
	if timing.WroteAt.IsZero() {
		return
	}
	rlm.timingMu.Lock()
	if !rlm.firstTokenAt.IsZero() || rlm.upstreamSettled {
		rlm.timingMu.Unlock()
		return
	}
	if seq != 0 && seq < rlm.writtenSequence {
		rlm.timingMu.Unlock()
		return
	}
	rlm.writtenSequence = seq
	rlm.firstTokenStartTime = timing.WroteAt
	if rlm.usageTracker == nil || rlm.requestID == "" {
		rlm.timingMu.Unlock()
		return
	}
	opts := rlm.contactUpdate(rlm.startTime)
	opts.BillingChannel = channel
	opts.FirstTokenStartAt = &timing.WroteAt
	opts.UpstreamWriteMs = upstreamOffset(rlm.startTime, timing.WroteAt)
	opts.UpstreamConnMs = upstreamOffset(rlm.startTime, timing.ConnReadyAt)
	opts.UpstreamDispatchMs = upstreamOffset(rlm.startTime, timing.ConnStartAt)
	rlm.usageTracker.RecordRequestUpdate(rlm.requestID, opts)
	rlm.persistedContacts = rlm.upstreamContacts
	rlm.timingMu.Unlock()
	rlm.publishTimingEvent(upstreamSentEventData(rlm.requestID, timing.WroteAt))
}

// finishUpstreamContacts 在终态归档之前补齐尚未写完的尝试，并拒绝晚到的连接回调。
func (rlm *RequestLifecycleManager) finishUpstreamContacts() {
	rlm.timingMu.Lock()
	defer rlm.timingMu.Unlock()
	rlm.upstreamSettled = true
	rlm.flushStreamPhasesLocked()
	if rlm.persistedContacts == rlm.upstreamContacts || rlm.usageTracker == nil || rlm.requestID == "" {
		return
	}
	rlm.usageTracker.RecordRequestUpdate(rlm.requestID, rlm.contactUpdate(rlm.startTime))
	rlm.persistedContacts = rlm.upstreamContacts
}

// GetRetryCount 返回联系上游次数减一。
func (rlm *RequestLifecycleManager) GetRetryCount() int {
	rlm.timingMu.RLock()
	defer rlm.timingMu.RUnlock()
	return max(rlm.upstreamContacts-1, 0)
}

func (rlm *RequestLifecycleManager) upstreamContactCount() int {
	rlm.timingMu.RLock()
	defer rlm.timingMu.RUnlock()
	return rlm.upstreamContacts
}
