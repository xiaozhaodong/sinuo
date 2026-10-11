package proxy

import (
	"log/slog"
	"strings"
	"sync"
	"time"

	"cc-forwarder/internal/events"
	"cc-forwarder/internal/privacy"
	"cc-forwarder/internal/proxy/handlers"
	"cc-forwarder/internal/reasoning"
	"cc-forwarder/internal/tracking"
)

const (
	subscriptionTrackingUpstreamName = "chatgpt-subscription"
	subscriptionTrackingModelLimit   = 256
)

// SubscriptionHTTPStart 是订阅 HTTP 入口与追踪器之间的正文无关契约。
type SubscriptionHTTPStart struct {
	RequestID    string
	SessionID    string
	ConnectionID string
	StartedAt    time.Time
	ClientIP     string
	UserAgent    string
	Method       string
	Path         string
	RequestKind  string
	IsStreaming  bool
	// v6.3 请求侧模型与思考串，随 start 事件原子写入
	RequestModel     string
	RequestReasoning string
}

type subscriptionTrackingObserver struct {
	tracker  *tracking.UsageTracker
	eventBus events.EventBus

	mu          sync.Mutex
	submissions map[*subscriptionSubmission]*subscriptionRequestTracking
}

func newSubscriptionTrackingObserver(tracker *tracking.UsageTracker, eventBus events.EventBus) *subscriptionTrackingObserver {
	return &subscriptionTrackingObserver{
		tracker: tracker, eventBus: eventBus,
		submissions: make(map[*subscriptionSubmission]*subscriptionRequestTracking),
	}
}

// StartHTTP 只为 generation/compaction 建记录；空 kind 是合法辅助请求。
func (o *subscriptionTrackingObserver) StartHTTP(start SubscriptionHTTPStart) *subscriptionRequestTracking {
	handle := &subscriptionRequestTracking{observer: o, requestID: start.RequestID, connectionID: start.ConnectionID, startedAt: start.StartedAt, path: start.Path}
	if start.RequestKind == "" || o == nil || o.tracker == nil || start.RequestID == "" {
		return handle
	}
	if start.RequestKind != tracking.RequestKindGeneration && start.RequestKind != tracking.RequestKindCompaction {
		slog.Warn("订阅请求种类非法，拒绝追踪记录", "request_id", start.RequestID, "code", "subscription_tracking_invalid_kind")
		return handle
	}
	if handle.startedAt.IsZero() {
		handle.startedAt = time.Now()
	}
	err := o.tracker.RecordRequestStartAtWithDimensions(
		start.RequestID, start.ClientIP, start.UserAgent, start.Method, start.Path,
		tracking.RequestFamilyCodex, start.IsStreaming, handle.startedAt,
		tracking.RequestDimensions{
			RequestSource:      tracking.RequestSourceCodexSubscriptionProxy,
			RequestKind:        start.RequestKind,
			UpstreamType:       "subscription",
			UpstreamSourceName: subscriptionTrackingUpstreamName,
			UpstreamName:       subscriptionTrackingUpstreamName,
			UpstreamID:         0,
			UsageKnown:         false,
			RequestModel:       safeSubscriptionModel(start.RequestModel),
			RequestReasoning:   strings.TrimSpace(start.RequestReasoning),
			SessionID:          start.SessionID,
		},
	)
	if err != nil {
		slog.Warn("订阅请求追踪登记失败", "request_id", start.RequestID, "code", "subscription_tracking_start_failed")
		return handle
	}
	handle.active = true
	o.publish(events.EventRequestStarted, events.PriorityNormal, map[string]interface{}{
		"request_id": start.RequestID, "method": start.Method, "path": start.Path,
		"request_family": tracking.RequestFamilyCodex,
		"request_source": tracking.RequestSourceCodexSubscriptionProxy,
		"request_kind":   start.RequestKind, "is_streaming": start.IsStreaming,
		"upstream_type": "subscription", "upstream_source_name": subscriptionTrackingUpstreamName,
		"upstream_name": subscriptionTrackingUpstreamName, "upstream_id": int64(0),
		"change_type": "request_started",
	})
	return handle
}

func (o *subscriptionTrackingObserver) publish(eventType events.EventType, priority events.EventPriority, data map[string]interface{}) {
	if o == nil || o.eventBus == nil {
		return
	}
	o.eventBus.Publish(events.Event{Type: eventType, Source: "subscription_tracking", Priority: priority, Data: data})
}

type subscriptionRequestTracking struct {
	mu       sync.Mutex
	observer *subscriptionTrackingObserver
	active   bool
	settled  bool

	requestID     string
	connectionID  string
	startedAt     time.Time
	path          string
	model         string
	responseModel string                  // v6.3 仅由响应解析写入
	responseState reasoning.ResponseState // v6.3 响应思考按字段累计（created 的 effort + completed 的 reasoning_tokens）
	httpStatus    *int
	outboundAt    time.Time
	firstOutputAt time.Time
	requestBytes  int64
	responseBytes int64
	scanCount     int
}

func (t *subscriptionRequestTracking) SetModel(model string) {
	model = safeSubscriptionModel(model)
	if model == "" {
		return
	}
	t.mu.Lock()
	if !t.active || t.settled {
		t.mu.Unlock()
		return
	}
	t.model = model
	t.mu.Unlock()
	t.observer.tracker.RecordRequestUpdate(t.requestID, tracking.UpdateOptions{ModelName: &model})
}

// SetRequestProfile 补写请求侧模型与思考串（订阅 HTTP 入口在 start 之后才读到请求体）。非空才写。
func (t *subscriptionRequestTracking) SetRequestProfile(model, reasoning string) {
	model = safeSubscriptionModel(model)
	reasoning = strings.TrimSpace(reasoning)
	if model == "" && reasoning == "" {
		return
	}
	t.mu.Lock()
	if !t.active || t.settled {
		t.mu.Unlock()
		return
	}
	t.mu.Unlock()
	opts := tracking.UpdateOptions{}
	if model != "" {
		opts.RequestModel = &model
	}
	if reasoning != "" {
		opts.RequestReasoning = &reasoning
	}
	t.observer.tracker.RecordRequestUpdate(t.requestID, opts)
}

func (t *subscriptionRequestTracking) RecordPrivacy(result privacy.ApplyResult, policyErr *privacy.PolicyError) {
	t.mu.Lock()
	if !t.active || t.settled {
		t.mu.Unlock()
		return
	}
	t.scanCount++
	payload := handlers.BuildPrivacyScanJSON(subscriptionPrivacySummary(result), sanitizeSubscriptionPolicyError(policyErr), t.scanCount)
	t.mu.Unlock()
	if payload != "" {
		t.observer.tracker.RecordRequestUpdate(t.requestID, tracking.UpdateOptions{PrivacyScanJSON: &payload})
	}
}

func (t *subscriptionRequestTracking) SetStreaming(streaming bool) {
	t.mu.Lock()
	if !t.active || t.settled {
		t.mu.Unlock()
		return
	}
	t.mu.Unlock()
	t.observer.tracker.RecordRequestUpdate(t.requestID, tracking.UpdateOptions{IsStreaming: &streaming})
}

func (t *subscriptionRequestTracking) SetHTTPStatus(status int) {
	t.mu.Lock()
	if !t.active || t.settled {
		t.mu.Unlock()
		return
	}
	value := status
	t.httpStatus = &value
	t.mu.Unlock()
	t.observer.tracker.RecordRequestUpdate(t.requestID, tracking.UpdateOptions{HttpStatus: &status})
}

func (t *subscriptionRequestTracking) MarkOutbound(requestBytes int64, at time.Time) {
	if requestBytes < 0 {
		requestBytes = 0
	}
	t.mu.Lock()
	if !t.active || t.settled || !t.outboundAt.IsZero() {
		t.mu.Unlock()
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	t.outboundAt = at
	t.requestBytes = requestBytes
	ms := at.Sub(t.startedAt).Milliseconds()
	if ms < 0 {
		ms = 0
	}
	t.mu.Unlock()
	t.observer.tracker.RecordRequestUpdate(t.requestID, tracking.UpdateOptions{UpstreamWriteMs: &ms, FirstTokenStartAt: &at})
	t.observer.publish(events.EventRequestUpdated, events.PriorityNormal, upstreamSentEventData(t.requestID, at))
}

func (t *subscriptionRequestTracking) MarkFirstOutput(responseBytes int64, at time.Time) {
	if responseBytes < 0 {
		responseBytes = 0
	}
	t.mu.Lock()
	if !t.active || t.settled || !t.firstOutputAt.IsZero() {
		t.mu.Unlock()
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	t.firstOutputAt = at
	if responseBytes > t.responseBytes {
		t.responseBytes = responseBytes
	}
	base := t.startedAt
	if !t.outboundAt.IsZero() {
		base = t.outboundAt
	}
	ms := at.Sub(base).Milliseconds()
	if ms < 0 {
		ms = 0
	}
	t.mu.Unlock()
	t.observer.tracker.RecordRequestUpdate(t.requestID, tracking.UpdateOptions{FirstTokenMs: &ms, FirstTokenAt: &at})
	// 订阅代理在开始与结算之间没有别的事件，不补这一条首响位会一直跑到请求结束。
	t.observer.publish(events.EventRequestUpdated, events.PriorityNormal, firstResponseEventData(t.requestID, ms, at))
}

// MarkResponseBytes 更新截至当前的响应累计字节数，不产生逐帧事件。
func (t *subscriptionRequestTracking) MarkResponseBytes(responseBytes int64) {
	if responseBytes < 0 {
		return
	}
	t.mu.Lock()
	if t.active && !t.settled && responseBytes > t.responseBytes {
		t.responseBytes = responseBytes
	}
	t.mu.Unlock()
}

// MergeResponseFields 合并单个 Responses 事件的响应模型与思考字段：模型非空才覆盖，
// 思考字段按字段合并（缺失不覆盖），终态随结算原子落库。
// 订阅链路走无状态 ParseResponsesEventJSON，累计状态由本 handle 持有。
func (t *subscriptionRequestTracking) MergeResponseFields(model string, fields reasoning.CodexFields) {
	model = safeSubscriptionModel(model)
	if model == "" && fields.IsEmpty() {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.active || t.settled {
		return
	}
	if model != "" {
		t.responseModel = model
	}
	t.responseState.MergeCodexFields(fields)
}

func (t *subscriptionRequestTracking) ApplyParseResult(result *ParseResult) {
	if result == nil {
		return
	}
	t.SetModel(result.ModelName)
	t.MergeResponseFields(result.ResponseModel, result.ResponseReasoningFields)
	if !result.IsCompleted {
		return
	}
	status := "completed"
	failureCode := ""
	if result.IsCancelled {
		status = "cancelled"
		failureCode = "subscription_cancelled"
	} else if result.Status == StatusErrorAPI || result.ErrorInfo != nil {
		status = "failed"
		failureCode = "subscription_response_failed"
	}
	var usage *tracking.TokenUsage
	if result.HasTokenUsage && result.TokenUsage != nil {
		copy := *result.TokenUsage
		usage = &copy
	}
	t.settle(status, failureCode, usage, nil)
}

func (t *subscriptionRequestTracking) Complete(httpStatus *int, failureCode string) {
	if httpStatus != nil {
		t.SetHTTPStatus(*httpStatus)
	}
	failureCode = safeSubscriptionFailureCode(failureCode)
	if failureCode == "" {
		failureCode = "subscription_interrupted"
	}
	t.settle("failed", failureCode, nil, httpStatus)
}

func (t *subscriptionRequestTracking) settle(status, failureCode string, usage *tracking.TokenUsage, overrideHTTPStatus *int) {
	t.mu.Lock()
	if !t.active || t.settled {
		t.mu.Unlock()
		return
	}
	t.settled = true
	httpStatus := t.httpStatus
	if overrideHTTPStatus != nil {
		value := *overrideHTTPStatus
		httpStatus = &value
	}
	model := t.model
	diagnostics := &tracking.ResponseDiagnostics{ResponseModel: t.responseModel, ResponseReasoning: t.responseState.String()}
	responseBytes := t.responseBytes
	requestBytes := t.requestBytes
	settledAt := time.Now()
	duration := settledAt.Sub(t.startedAt)
	firstOutputAt := t.firstOutputAt
	requestID := t.requestID
	path := t.path
	t.mu.Unlock()

	if !firstOutputAt.IsZero() {
		completionMs := settledAt.Sub(firstOutputAt).Milliseconds()
		if completionMs < 0 {
			completionMs = 0
		}
		t.observer.tracker.RecordRequestUpdate(requestID, tracking.UpdateOptions{CompletionMs: &completionMs})
		t.observer.publish(events.EventRequestUpdated, events.PriorityNormal, generationCompletedEventData(requestID, completionMs))
	}
	if err := t.observer.tracker.RecordRequestSettlement(requestID, tracking.RequestSettlement{
		Status: status, ModelName: model, Path: path, FailureReason: failureCode,
		Duration: duration, HTTPStatus: httpStatus, TokenUsage: usage, Diagnostics: diagnostics,
	}); err != nil {
		slog.Warn("订阅请求追踪结算失败", "request_id", requestID, "code", "subscription_tracking_settlement_failed")
	}
	logArgs := []interface{}{
		"request_id", requestID, "status", status, "request_bytes", requestBytes,
		"response_bytes", responseBytes, "duration_ms", duration.Milliseconds(), "usage_known", usage != nil,
		"failure_reason", failureCode,
	}
	if t.connectionID != "" {
		logArgs = append(logArgs, "conn_id", t.connectionID)
	}
	if status == "completed" {
		slog.Debug("订阅请求追踪结算完成", logArgs...)
	} else {
		slog.Warn("订阅请求追踪结算结束", logArgs...)
	}
	priority := events.PriorityNormal
	changeType := "request_completed"
	if status == "cancelled" {
		changeType = "request_cancelled"
	} else if status != "completed" {
		priority = events.PriorityHigh
		changeType = "request_failed"
	}
	t.observer.publish(events.EventRequestUpdated, priority, map[string]interface{}{
		"request_id": requestID, "request_family": tracking.RequestFamilyCodex,
		"request_source": tracking.RequestSourceCodexSubscriptionProxy,
		"upstream_type":  "subscription", "upstream_name": subscriptionTrackingUpstreamName,
		"status": status, "model_name": model, "failure_reason": failureCode,
		"usage_known": usage != nil, "cost_known": usage != nil,
		"request_bytes": requestBytes, "response_bytes": responseBytes, "change_type": changeType,
	})
	t.observer.publish(events.EventRequestCompleted, priority, map[string]interface{}{
		"request_id": requestID, "status": status, "change_type": changeType,
	})
}

func safeSubscriptionModel(model string) string {
	model = strings.TrimSpace(model)
	if model == "" || model == "default" || strings.HasPrefix(model, "error:") || len(model) > subscriptionTrackingModelLimit {
		return ""
	}
	for _, char := range model {
		if char < 0x20 || char == 0x7f {
			return ""
		}
	}
	return model
}

func safeSubscriptionFailureCode(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return ""
	}
	for _, prefix := range []string{"subscription_", "codex_subscription_", "privacy_"} {
		if strings.HasPrefix(code, prefix) && len(code) <= 96 {
			for _, char := range code {
				if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '_' {
					return "subscription_failure"
				}
			}
			return code
		}
	}
	return "subscription_failure"
}

func (o *subscriptionTrackingObserver) SubmissionStarted(submission *subscriptionSubmission) {
	if submission == nil || !submission.Generate {
		return
	}
	start := SubscriptionHTTPStart{
		RequestID: submission.RequestID, StartedAt: submission.StartedAt,
		SessionID:    submission.SessionID,
		ConnectionID: submission.ConnectionID,
		Method:       "POST", Path: "/v1/responses", RequestKind: tracking.RequestKindGeneration, IsStreaming: true,
	}
	// 服务端主动 successor 没有客户端 create，submission.Model 来自响应；
	// 请求侧保持未知，否则前端会把同源的请求/响应模型误判为「一致」。
	if !submission.ServerInitiated {
		start.RequestModel = submission.Model
		start.RequestReasoning = submission.Reasoning
	}
	handle := o.StartHTTP(start)
	handle.SetModel(submission.Model)
	o.mu.Lock()
	o.submissions[submission] = handle
	o.mu.Unlock()
	if submission.PrivacyState != nil {
		submission.PrivacyState.SetResultRecorder(func(result privacy.ApplyResult, policyErr *privacy.PolicyError) {
			handle.RecordPrivacy(result, policyErr)
		})
	}
}

func (o *subscriptionTrackingObserver) SubmissionBound(submission *subscriptionSubmission, result *ParseResult) {
	handle := o.submissionHandle(submission, false)
	if handle == nil {
		return
	}
	handle.SetModel(submission.Model)
	if result != nil {
		handle.MergeResponseFields(result.ResponseModel, result.ResponseReasoningFields)
	}
}

func (o *subscriptionTrackingObserver) SubmissionTerminal(submission *subscriptionSubmission, result *ParseResult) {
	if handle := o.submissionHandle(submission, true); handle != nil {
		handle.ApplyParseResult(result)
	}
}

func (o *subscriptionTrackingObserver) SubmissionRejected(submission *subscriptionSubmission, policyErr *privacy.PolicyError) {
	if handle := o.submissionHandle(submission, true); handle != nil {
		code := "privacy_scan_failed"
		if policyErr != nil {
			code = safeSubscriptionFailureCode(policyErr.Code)
		}
		handle.Complete(nil, code)
	}
}

func (o *subscriptionTrackingObserver) SubmissionInterrupted(submission *subscriptionSubmission, reason string) {
	if handle := o.submissionHandle(submission, true); handle != nil {
		if reason == tracking.SubscriptionClientDisconnected {
			handle.settle("cancelled", reason, nil, nil)
			return
		}
		handle.Complete(nil, "subscription_interrupted")
	}
}

func (o *subscriptionTrackingObserver) SubmissionWarning(connectionID, code string) {
	switch code {
	case "submission_after_close", "response_created_without_id", "duplicate_response_id",
		"terminal_response_id_not_found", "terminal_without_submission", "duplicate_terminal":
		// 只保留状态机定义的告警，便于排查关联问题；不接受任意上游文本。
	default:
		code = "subscription_failure"
	}
	slog.Warn("订阅 WS 提交追踪告警", "conn_id", connectionID, "code", code)
}

func (o *subscriptionTrackingObserver) SubmissionOutbound(submission *subscriptionSubmission, size int64, at time.Time) {
	if handle := o.submissionHandle(submission, false); handle != nil {
		handle.MarkOutbound(size, at)
	}
}

func (o *subscriptionTrackingObserver) SubmissionFirstOutput(submission *subscriptionSubmission, size int64, at time.Time) {
	if handle := o.submissionHandle(submission, false); handle != nil {
		handle.MarkFirstOutput(size, at)
	}
}

func (o *subscriptionTrackingObserver) SubmissionResponseProgress(submission *subscriptionSubmission, size int64) {
	if handle := o.submissionHandle(submission, false); handle != nil {
		handle.MarkResponseBytes(size)
	}
}

func (o *subscriptionTrackingObserver) submissionHandle(submission *subscriptionSubmission, remove bool) *subscriptionRequestTracking {
	if o == nil || submission == nil {
		return nil
	}
	o.mu.Lock()
	handle := o.submissions[submission]
	if remove {
		delete(o.submissions, submission)
	}
	o.mu.Unlock()
	return handle
}
