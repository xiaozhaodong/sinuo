package proxy

import (
	"log/slog"
	"time"

	"sinuo/internal/events"
	"sinuo/internal/proxy/handlers"
	"sinuo/internal/tracking"
)

// RecordUpstreamGetConn 与生命周期管理器共用联系上游口径；只更新内存并发布事件。
func (t *subscriptionRequestTracking) RecordUpstreamGetConn(attempt handlers.UpstreamConnAttempt) {
	t.mu.Lock()
	if !t.active || t.settled || !t.firstOutputAt.IsZero() {
		t.mu.Unlock()
		return
	}
	t.recordContact(attempt.At)
	contacts := t.upstreamContacts
	if contacts > 1 {
		t.observer.publish(events.EventRequestUpdated, events.PriorityNormal,
			upstreamRetryEventData(t.requestID, contacts-1, attempt.At))
	}
	t.mu.Unlock()
	logTransportRetry(t.requestID, subscriptionTrackingUpstreamName, contacts, attempt)
}

func (t *subscriptionRequestTracking) setOutboundBytes(size int64) {
	t.mu.Lock()
	t.requestBytes = max(size, 0)
	t.mu.Unlock()
}

// RecordUpstreamWrite 实现 HTTP 追踪接口；WS 仍直接调用 MarkOutbound。
func (t *subscriptionRequestTracking) RecordUpstreamWrite(timing handlers.UpstreamWriteTiming) {
	t.mu.Lock()
	size := t.requestBytes
	t.mu.Unlock()
	t.MarkOutbound(size, timing)
}

// MarkOutbound 与成功写完同笔记录联系次数；WS 没有 GetConn，不写首次联系与重试数。
func (t *subscriptionRequestTracking) MarkOutbound(requestBytes int64, timing handlers.UpstreamWriteTiming) {
	t.mu.Lock()
	if !t.active || t.settled || !t.firstOutputAt.IsZero() {
		t.mu.Unlock()
		return
	}
	at := timing.WroteAt
	if at.IsZero() {
		at = time.Now()
	}
	t.outboundAt = at
	t.requestBytes = max(requestBytes, 0)
	opts := tracking.UpdateOptions{}
	if t.upstreamContacts > 0 {
		opts = t.contactUpdate(t.startedAt)
	}
	opts.UpstreamWriteMs = upstreamOffset(t.startedAt, at)
	opts.UpstreamConnMs = upstreamOffset(t.startedAt, timing.ConnReadyAt)
	opts.UpstreamDispatchMs = upstreamOffset(t.startedAt, timing.ConnStartAt)
	opts.FirstTokenStartAt = &at
	t.observer.tracker.RecordRequestUpdate(t.requestID, opts)
	t.persistedContacts = t.upstreamContacts
	t.mu.Unlock()
	t.observer.publish(events.EventRequestUpdated, events.PriorityNormal, upstreamSentEventData(t.requestID, at))
}

// flushUpstreamContactsLocked 在 settle 持有 mu、归档之前补齐未写完的 HTTP 尝试。
func (t *subscriptionRequestTracking) flushUpstreamContactsLocked() {
	if t.persistedContacts == t.upstreamContacts {
		return
	}
	t.observer.tracker.RecordRequestUpdate(t.requestID, t.contactUpdate(t.startedAt))
	t.persistedContacts = t.upstreamContacts
}

// subscriptionSettlementNotice 保存一次结算的观测快照，避免日志与事件读取并发状态。
type subscriptionSettlementNotice struct {
	requestID, status, failureCode, model string
	requestBytes, responseBytes           int64
	duration                              time.Duration
	usageKnown                            bool
	retryCount                            int
}

func (t *subscriptionRequestTracking) publishSettlement(n subscriptionSettlementNotice) {
	logArgs := []interface{}{
		"request_id", n.requestID, "status", n.status, "request_bytes", n.requestBytes,
		"response_bytes", n.responseBytes, "duration_ms", n.duration.Milliseconds(), "usage_known", n.usageKnown,
		"failure_reason", n.failureCode,
	}
	if t.connectionID != "" {
		logArgs = append(logArgs, "conn_id", t.connectionID)
	}
	if n.status == "completed" {
		slog.Debug("订阅请求追踪结算完成", logArgs...)
	} else {
		slog.Warn("订阅请求追踪结算结束", logArgs...)
	}
	priority := events.PriorityNormal
	changeType := "request_completed"
	if n.status == "cancelled" {
		changeType = "request_cancelled"
	} else if n.status != "completed" {
		priority = events.PriorityHigh
		changeType = "request_failed"
	}
	t.observer.publish(events.EventRequestUpdated, priority, map[string]interface{}{
		"request_id": n.requestID, "request_family": tracking.RequestFamilyCodex,
		"request_source": tracking.RequestSourceCodexSubscriptionProxy,
		"upstream_type":  "subscription", "upstream_name": subscriptionTrackingUpstreamName,
		"status": n.status, "model_name": n.model, "failure_reason": n.failureCode, "retry_count": n.retryCount,
		"usage_known": n.usageKnown, "cost_known": n.usageKnown,
		"request_bytes": n.requestBytes, "response_bytes": n.responseBytes, "change_type": changeType,
	})
	t.observer.publish(events.EventRequestCompleted, priority, map[string]interface{}{
		"request_id": n.requestID, "status": n.status, "change_type": changeType, "retry_count": n.retryCount,
	})
}
