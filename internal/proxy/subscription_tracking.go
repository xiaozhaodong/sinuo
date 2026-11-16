package proxy

import (
	"log/slog"
	"strings"
	"sync"
	"time"

	"sinuo/internal/events"
	"sinuo/internal/privacy"
	"sinuo/internal/proxy/handlers"
	"sinuo/internal/reasoning"
	"sinuo/internal/tracking"
)

const (
	subscriptionTrackingUpstreamName = "chatgpt-subscription"
	subscriptionTrackingModelLimit   = 256
)

// SubscriptionHTTPStart 是订阅 HTTP 入口与追踪器之间的正文无关契约。
type SubscriptionHTTPStart struct {
	RequestID     string
	SessionID     string
	ConnectionID  string
	StartedAt     time.Time
	ClientIP      string
	UserAgent     string
	Method        string
	Path          string
	RequestKind   string
	RequestFamily string
	IsStreaming   bool
	// v6.3 请求侧模型与思考串，随 start 事件原子写入
	RequestModel     string
	RequestReasoning string
	Transport        string
	Origin           string
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

// StartHTTP 为业务调用/压缩建记录；空 kind 是不需登记的辅助请求。
func (o *subscriptionTrackingObserver) StartHTTP(start SubscriptionHTTPStart) *subscriptionRequestTracking {
	handle := &subscriptionRequestTracking{observer: o, requestID: start.RequestID, connectionID: start.ConnectionID, startedAt: start.StartedAt, path: start.Path, streaming: start.IsStreaming}
	if start.RequestKind == "" || o == nil || start.RequestID == "" {
		return handle
	}
	if start.RequestKind != tracking.RequestKindGeneration && start.RequestKind != tracking.RequestKindCompaction {
		slog.Warn("订阅请求种类非法，拒绝追踪记录", "request_id", start.RequestID, "code", "subscription_tracking_invalid_kind")
		return handle
	}
	// 追踪关闭或登记失败时仍发隐私通知，只跳过没有记录可更新的持久化。
	handle.privacySummary = newPrivacySummaryWriter(nil, o.eventBus, start.RequestID)
	if o.tracker == nil {
		return handle
	}
	if handle.startedAt.IsZero() {
		handle.startedAt = time.Now()
	}
	family := start.RequestFamily
	if family == "" {
		family = tracking.RequestFamilyCodex
	}
	handle.requestFamily = family
	err := o.tracker.RecordRequestStartAtWithDimensions(
		start.RequestID, start.ClientIP, start.UserAgent, start.Method, start.Path,
		family, start.IsStreaming, handle.startedAt,
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
			FlowTransport:      start.Transport,
			FlowOrigin:         start.Origin,
		},
	)
	if err != nil {
		slog.Warn("订阅请求追踪登记失败", "request_id", start.RequestID, "code", "subscription_tracking_start_failed", "error", err)
		return handle
	}
	handle.active = true
	handle.initFlow(start.Transport, start.Origin)
	handle.privacySummary = newPrivacySummaryWriter(o.tracker, o.eventBus, start.RequestID)
	o.publish(events.EventRequestStarted, events.PriorityNormal, map[string]interface{}{
		"request_id": start.RequestID, "method": start.Method, "path": start.Path,
		"request_family": family,
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
	privacySummary  func(string)
	phaseCollector  *StreamPhaseCollector // 在mu下维护；非流式不创建
	phaseRequestID  *string               // 最终尝试响应头请求ID，在 mu 下覆盖
	phaseStopReason *string               // 上游结束原因原文，在 mu 下维护

	upstreamContactState // 在 mu 下维护；WS 不产生取连接事件
	mu                   sync.Mutex
	observer             *subscriptionTrackingObserver
	active               bool
	settled              bool

	requestID     string
	connectionID  string
	startedAt     time.Time
	path          string
	requestFamily string
	model         string
	streaming     bool
	responseModel string                  // v6.3 仅由响应解析写入
	responseState reasoning.ResponseState // v6.3 响应思考按字段累计（created 的 effort + completed 的 reasoning_tokens）
	httpStatus    *int
	outboundAt    time.Time
	firstOutputAt time.Time
	requestBytes  int64
	responseBytes int64
	flow          subscriptionFlowState
}

func (t *subscriptionRequestTracking) SetModel(model string) {
	model = safeSubscriptionModel(model)
	if model == "" {
		return
	}
	t.mu.Lock()
	if !t.active || t.settled || t.model == model {
		t.mu.Unlock()
		return
	}
	t.model = model
	t.mu.Unlock()
	t.observer.tracker.RecordRequestUpdate(t.requestID, tracking.UpdateOptions{ModelName: &model})
}

// 请求体一次解析出的维度共用一次追踪更新，避免短流的重复元数据写入挤占结算队列。
func (t *subscriptionRequestTracking) setRequestProfile(model, reasoning string, streaming bool) {
	model = safeSubscriptionModel(model)
	reasoning = strings.TrimSpace(reasoning)
	t.mu.Lock()
	if !t.active || t.settled {
		t.mu.Unlock()
		return
	}
	if model != "" {
		t.model = model
	}
	t.streaming = streaming
	t.mu.Unlock()
	opts := tracking.UpdateOptions{IsStreaming: &streaming}
	if model != "" {
		opts.RequestModel = &model
		opts.ModelName = &model
	}
	if reasoning != "" {
		opts.RequestReasoning = &reasoning
	}
	t.observer.tracker.RecordRequestUpdate(t.requestID, opts)
}

func (t *subscriptionRequestTracking) recordPrivacySummary(payload string) {
	if t.privacySummary != nil {
		t.privacySummary(payload)
	}
}

func (t *subscriptionRequestTracking) SetStreaming(streaming bool) {
	t.mu.Lock()
	if !t.active || t.settled || t.streaming == streaming {
		t.mu.Unlock()
		return
	}
	t.streaming = streaming
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

// MarkFirstOutput 沿用历史名称，现记录首个非空事件；首个内容由采集器单独记录。
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
	opts := tracking.UpdateOptions{}
	if t.upstreamContacts > 0 {
		opts = t.contactUpdate(t.startedAt)
	}
	opts.FirstTokenMs, opts.FirstTokenAt = &ms, &at
	t.observer.tracker.RecordRequestUpdate(t.requestID, opts)
	t.persistedContacts = t.upstreamContacts
	flowEvents := t.wsFirstByteEventsLocked(at)
	t.mu.Unlock()
	t.emitFlowEvents(flowEvents...)
	// 订阅代理在开始与结算之间没有别的事件，不补这一条首响位会一直跑到请求结束。
	t.observer.publish(events.EventRequestUpdated, events.PriorityNormal, firstResponseEventData(t.requestID, ms, at))
}

// MarkResponseBytes 更新截至当前的响应累计字节数，不产生逐帧事件。
func (t *subscriptionRequestTracking) MarkResponseBytes(responseBytes int64) {
	if responseBytes < 0 {
		return
	}
	t.mu.Lock()
	var flowEvents []tracking.RequestFlowEvent
	if t.active && !t.settled && responseBytes > t.responseBytes {
		t.responseBytes = responseBytes
		flowEvents = t.wsFirstByteEventsLocked(time.Time{})
	}
	t.mu.Unlock()
	if len(flowEvents) > 0 {
		t.emitFlowEvents(flowEvents...)
	}
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
	t.observeResponseFields(result)
	if !result.IsCompleted {
		return
	}
	t.markFlowUpstreamTerminal(time.Now())
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

func (t *subscriptionRequestTracking) observeResponseFields(result *ParseResult) {
	t.SetModel(result.ModelName)
	t.MergeResponseFields(result.ResponseModel, result.ResponseReasoningFields)
	t.recordStopReason(result.StopReason)
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
	t.finalizeFlowBeforeSettle(status, failureCode)
	t.mu.Lock()
	if !t.active || t.settled {
		t.mu.Unlock()
		return
	}
	t.flushUpstreamContactsLocked()
	retryCount := max(t.upstreamContacts-1, 0)
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
	phaseUpdate := t.streamPhaseUpdateLocked()
	t.mu.Unlock()

	if !firstOutputAt.IsZero() {
		completionMs := settledAt.Sub(firstOutputAt).Milliseconds()
		if completionMs < 0 {
			completionMs = 0
		}
		phaseUpdate.CompletionMs = &completionMs
	}
	t.observer.tracker.RecordRequestUpdate(requestID, phaseUpdate)
	if phaseUpdate.CompletionMs != nil {
		t.observer.publish(events.EventRequestUpdated, events.PriorityNormal, generationCompletedEventData(requestID, *phaseUpdate.CompletionMs))
	}
	if err := t.observer.tracker.RecordRequestSettlement(requestID, tracking.RequestSettlement{
		Status: status, ModelName: model, Path: path, FailureReason: failureCode,
		Duration: duration, HTTPStatus: httpStatus, TokenUsage: usage, Diagnostics: diagnostics,
	}); err != nil {
		slog.Warn("订阅请求追踪结算失败", "request_id", requestID, "code", "subscription_tracking_settlement_failed")
	}
	t.publishSettlement(subscriptionSettlementNotice{
		requestID: requestID, status: status, failureCode: failureCode, model: model,
		requestBytes: requestBytes, responseBytes: responseBytes, duration: duration,
		usageKnown: usage != nil, retryCount: retryCount,
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
	origin := tracking.FlowOriginClientRequest
	if submission.ServerInitiated {
		origin = tracking.FlowOriginServerSuccessor
	}
	start := SubscriptionHTTPStart{
		RequestID: submission.RequestID, StartedAt: submission.StartedAt,
		SessionID:    submission.SessionID,
		ConnectionID: submission.ConnectionID,
		ClientIP:     submission.ClientIP,
		UserAgent:    submission.UserAgent,
		Method:       "POST", Path: "/v1/responses", RequestKind: tracking.RequestKindGeneration, IsStreaming: true,
		Transport: tracking.FlowTransportWS,
		Origin:    origin,
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
		handle.mu.Lock()
		handle.flow.wsTerminalDelivered = result != nil && result.IsCompleted
		handle.mu.Unlock()
		handle.ApplyParseResult(result)
	}
}

func (o *subscriptionTrackingObserver) SubmissionRejected(submission *subscriptionSubmission, policyErr *privacy.PolicyError) {
	o.SubmissionRejectionDelivered(submission, policyErr, 0)
}

// 本地拒绝和上游终态分别记账。只有写帧成功才确认错误回复已交付。
func (o *subscriptionTrackingObserver) SubmissionRejectionDelivered(submission *subscriptionSubmission, policyErr *privacy.PolicyError, written int64) {
	if handle := o.submissionHandle(submission, true); handle != nil {
		if written > 0 {
			handle.recordFlowWSDownstreamDelivered(written, nil, time.Now())
			handle.mu.Lock()
			handle.flow.wsTerminalDelivered = true
			handle.mu.Unlock()
		}
		code := "privacy_scan_failed"
		if policyErr != nil {
			code = safeSubscriptionFailureCode(policyErr.Code)
		}
		handle.Complete(nil, code)
	}
}

func (o *subscriptionTrackingObserver) SubmissionInterrupted(submission *subscriptionSubmission, reason string) {
	if handle := o.submissionHandle(submission, true); handle != nil {
		var usage *tracking.TokenUsage
		if result := submission.terminalRead; result != nil {
			handle.observeResponseFields(result)
			if result.HasTokenUsage {
				usage = result.TokenUsage
			}
		}
		if submission.restoreStatus == "failed" {
			handle.settle("failed", "privacy_restore_failed", usage, nil)
			return
		}
		if reason == tracking.SubscriptionClientDisconnected {
			handle.settle("cancelled", reason, usage, nil)
			return
		}
		handle.settle("failed", "subscription_interrupted", usage, nil)
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
		handle.MarkOutbound(size, handlers.UpstreamWriteTiming{WroteAt: at})
		handle.recordFlowWSOutbound(at)
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
