package proxy

import (
	"fmt"
	"log/slog"
	"time"

	"sinuo/internal/endpoint"
	"sinuo/internal/events"
	"sinuo/internal/proxy/handlers"
	"sinuo/internal/tracking"
)

// UpdateStatus 更新请求状态；保留次数参数以兼容调用，参数已忽略，统一按联系上游次数计算。
func (rlm *RequestLifecycleManager) UpdateStatus(status string, _ int, httpStatus int) {
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
	rlm.notifyStatusChange(status, retries, httpStatus)
}

// notifyStatusChange 统一的状态通知方法
// 负责更新内部状态、发布事件通知和记录状态变更日志
// 这个方法被 UpdateStatus、CompleteRequest、FailRequest、CancelRequest 统一调用
func (rlm *RequestLifecycleManager) notifyStatusChange(status string, _ int, httpStatus int) {
	retryCount := rlm.GetRetryCount()
	// 更新内部状态
	rlm.stateMu.Lock()
	rlm.retryCount = retryCount
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
		retryCount:         rlm.retryCount,
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
	if timing.WroteAt.IsZero() {
		return
	}
	rlm.timingMu.Lock()
	if !rlm.firstTokenAt.IsZero() || rlm.upstreamSettled {
		rlm.timingMu.Unlock()
		return
	}
	rlm.firstTokenStartTime = timing.WroteAt
	if rlm.usageTracker == nil || rlm.requestID == "" {
		rlm.timingMu.Unlock()
		return
	}
	opts := rlm.contactUpdate(rlm.startTime)
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
	if rlm.persistedContacts == rlm.upstreamContacts || rlm.usageTracker == nil || rlm.requestID == "" {
		return
	}
	rlm.usageTracker.RecordRequestUpdate(rlm.requestID, rlm.contactUpdate(rlm.startTime))
	rlm.persistedContacts = rlm.upstreamContacts
}

// GetRetryCount 返回联系上游次数减一；旧 attemptCounter 不参与。
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

// IncrementRetry 增加重试计数
func (rlm *RequestLifecycleManager) IncrementRetry() {
	rlm.stateMu.Lock()
	rlm.retryCount++
	retryCount := rlm.retryCount
	rlm.stateMu.Unlock()
	slog.Info(fmt.Sprintf("🔄 [重试计数] [%s] 重试次数: %d", rlm.requestID, retryCount))
}

// IncrementAttempt 线程安全地增加尝试计数
// 用于统一重试计数语义，每次端点切换或重试时调用
func (rlm *RequestLifecycleManager) IncrementAttempt() int {
	rlm.attemptMu.Lock()
	defer rlm.attemptMu.Unlock()
	rlm.attemptCounter++
	slog.Debug(fmt.Sprintf("🔢 [尝试计数] [%s] 当前尝试次数: %d", rlm.requestID, rlm.attemptCounter))
	return rlm.attemptCounter
}

// GetAttemptCount 线程安全地获取当前尝试次数
// 返回真实的尝试次数，用于数据库记录和监控
func (rlm *RequestLifecycleManager) GetAttemptCount() int {
	rlm.attemptMu.Lock()
	defer rlm.attemptMu.Unlock()
	return rlm.attemptCounter
}

// OnRetryDecision 处理重试决策结果
func (rlm *RequestLifecycleManager) OnRetryDecision(decision RetryDecision, httpStatus int) {
	actualRetryCount := rlm.GetAttemptCount()

	if decision.RetrySameEndpoint {
		rlm.UpdateStatus("retry", actualRetryCount, httpStatus)
	} else if decision.FinalStatus != "" {
		rlm.UpdateStatus(decision.FinalStatus, actualRetryCount, httpStatus)
	}

	// 记录决策原因
	slog.Debug(fmt.Sprintf("📋 [重试决策记录] [%s] 状态: %s, 原因: %s",
		rlm.requestID, decision.FinalStatus, decision.Reason))
}

// GetRetryContext 获取重试上下文信息
func (rlm *RequestLifecycleManager) GetRetryContext(endpoint *endpoint.Endpoint, err error, attempt int) RetryContext {
	errorRecovery := rlm.errorRecovery
	state := rlm.snapshotState()
	errorCtx := errorRecovery.ClassifyError(err, rlm.requestID, state.endpointName, attempt-1)

	return RetryContext{
		RequestID:     rlm.requestID,
		Endpoint:      endpoint,
		Attempt:       attempt,
		AttemptGlobal: rlm.GetAttemptCount(),
		Error:         errorCtx,
		IsStreaming:   false, // 由调用方设置
	}
}
