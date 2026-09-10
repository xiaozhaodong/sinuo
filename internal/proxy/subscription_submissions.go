package proxy

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"cc-forwarder/internal/monitor"
	"cc-forwarder/internal/privacy"
	"cc-forwarder/internal/proxy/handlers"
)

// subscriptionSubmissionObserver 是 P2b 的同步生命周期接缝。
// 回调参数不含请求正文；实现不得在回调中再次解析 Responses 事件。
type subscriptionSubmissionObserver interface {
	SubmissionStarted(*subscriptionSubmission)
	SubmissionBound(*subscriptionSubmission)
	SubmissionTerminal(*subscriptionSubmission, *ParseResult)
	SubmissionRejected(*subscriptionSubmission, *privacy.PolicyError)
	SubmissionInterrupted(*subscriptionSubmission, string)
	SubmissionWarning(connectionID, code string)
}

// subscriptionSubmission 表示一条 WS 上的逻辑 response，而不是整条连接。
type subscriptionSubmission struct {
	RequestID          string
	ConnectionID       string
	Sequence           uint64
	Model              string
	PreviousResponseID string
	StreamID           string
	ResponseID         string
	Generate           bool
	ServerInitiated    bool
	StartedAt          time.Time
	PrivacyState       *handlers.PrivacyRequestState
	Context            context.Context

	settled       bool
	responseBytes int64
	firstOutput   bool
}

type subscriptionSubmissionState struct {
	mu           sync.Mutex
	connectionID string
	baseContext  context.Context
	observer     subscriptionSubmissionObserver
	sequence     uint64
	closed       bool
	pending      []*subscriptionSubmission
	active       []*subscriptionSubmission
	byResponseID map[string]*subscriptionSubmission
	recentIDs    map[string]struct{}
	recentOrder  []string
}

const subscriptionRecentResponseIDLimit = 256

func newSubscriptionSubmissionState(ctx context.Context, connectionID string, observer subscriptionSubmissionObserver) *subscriptionSubmissionState {
	if ctx == nil {
		ctx = context.Background()
	}
	return &subscriptionSubmissionState{
		connectionID: connectionID,
		baseContext:  ctx,
		observer:     observer,
		byResponseID: make(map[string]*subscriptionSubmission),
		recentIDs:    make(map[string]struct{}),
	}
}

// beginCreate 在隐私扫描前登记提交；generate 缺省为 true。
func (s *subscriptionSubmissionState) beginCreate(payload []byte) (*subscriptionSubmission, bool) {
	create, ok := parseSubscriptionCreate(payload)
	if !ok {
		return nil, false
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.warn("submission_after_close")
		return nil, true
	}
	s.sequence++
	requestID := monitor.NewConnectionID()
	privacyState := newSubscriptionPrivacyRequestState(requestID, nil)
	ctx := context.WithValue(s.baseContext, "conn_id", requestID) //nolint:staticcheck // 与旧入口请求 ID key 一致
	ctx = handlers.WithPrivacyRequestState(ctx, privacyState)
	submission := &subscriptionSubmission{
		RequestID:          requestID,
		ConnectionID:       s.connectionID,
		Sequence:           s.sequence,
		Model:              create.Model,
		PreviousResponseID: create.PreviousResponseID,
		StreamID:           create.StreamID,
		Generate:           create.Generate,
		StartedAt:          time.Now(),
		PrivacyState:       privacyState,
		Context:            ctx,
	}
	s.pending = append(s.pending, submission)
	s.active = append(s.active, submission)
	s.mu.Unlock()

	if s.observer != nil {
		s.observer.SubmissionStarted(submission)
	}
	return submission, true
}

// handleUpstream 关联 response.created 与终态。终态/token 只来自 ParseResponsesEventJSON。
func (s *subscriptionSubmissionState) handleUpstream(payload []byte) *ParseResult {
	eventType := responsesEventType(payload)
	if eventType == "" {
		return nil
	}
	result := ParseResponsesEventJSON(eventType, payload)
	responseID := responsesEventID(payload)

	if eventType == "response.created" {
		s.bindCreated(responseID, result)
		s.responseProgress(responseID, eventType, int64(len(payload)))
		return result
	}
	s.responseProgress(responseID, eventType, int64(len(payload)))
	if result == nil || !result.IsCompleted || !isResponsesTerminalEvent(eventType) {
		return result
	}
	s.finishTerminal(responseID, eventType, result)
	return result
}

// responseProgress 只向观察者传有界关联与字节/时序，不保存帧或逐帧推送 UI。
func (s *subscriptionSubmissionState) responseProgress(responseID, eventType string, size int64) {
	s.mu.Lock()
	submission := s.byResponseID[responseID]
	if responseID == "" && len(s.active) > 0 {
		submission = s.active[0]
	}
	if submission == nil || submission.settled {
		s.mu.Unlock()
		return
	}
	submission.responseBytes += size
	first := !submission.firstOutput && isSubscriptionOutputEvent(eventType)
	if first {
		submission.firstOutput = true
	}
	total := submission.responseBytes
	s.mu.Unlock()
	if first {
		if observer, ok := s.observer.(interface {
			SubmissionFirstOutput(*subscriptionSubmission, int64, time.Time)
		}); ok {
			observer.SubmissionFirstOutput(submission, total, time.Now())
		}
	}
	if observer, ok := s.observer.(interface {
		SubmissionResponseProgress(*subscriptionSubmission, int64)
	}); ok {
		observer.SubmissionResponseProgress(submission, total)
	}
}

func isSubscriptionOutputEvent(eventType string) bool {
	switch eventType {
	case "response.output_text.delta", "response.function_call_arguments.delta", "response.custom_tool_call_input.delta",
		"response.reasoning_summary_text.delta", "response.reasoning_text.delta", "response.output_item.added", "response.content_part.added":
		return true
	default:
		return false
	}
}

func (s *subscriptionSubmissionState) bindCreated(responseID string, result *ParseResult) {
	var submission *subscriptionSubmission
	var started bool
	if responseID == "" {
		s.warn("response_created_without_id")
		return
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	// 判重必须发生在消费 pending 或创建 successor 之前。
	if _, duplicate := s.recentIDs[responseID]; duplicate {
		s.mu.Unlock()
		s.warn("duplicate_response_id")
		return
	}
	for len(s.pending) > 0 {
		candidate := s.pending[0]
		s.pending = s.pending[1:]
		if !candidate.settled {
			submission = candidate
			break
		}
	}
	if submission == nil {
		// 服务端 steering successor 没有下游 create，仍需独立生命周期。
		s.sequence++
		requestID := monitor.NewConnectionID()
		submission = &subscriptionSubmission{
			RequestID:       requestID,
			ConnectionID:    s.connectionID,
			Sequence:        s.sequence,
			Generate:        true,
			ServerInitiated: true,
			StartedAt:       time.Now(),
			Context:         context.WithValue(s.baseContext, "conn_id", requestID), //nolint:staticcheck
		}
		if result != nil && result.ModelName != "default" {
			submission.Model = result.ModelName
		}
		s.active = append(s.active, submission)
		started = true
	}
	submission.ResponseID = responseID
	s.byResponseID[responseID] = submission
	s.rememberResponseIDLocked(responseID)
	s.mu.Unlock()

	if s.observer != nil {
		if started {
			s.observer.SubmissionStarted(submission)
		}
		s.observer.SubmissionBound(submission)
	}
}

func (s *subscriptionSubmissionState) finishTerminal(responseID, eventType string, result *ParseResult) {
	var submission *subscriptionSubmission

	s.mu.Lock()
	if responseID != "" {
		submission = s.byResponseID[responseID]
		if submission == nil {
			s.mu.Unlock()
			s.warn("terminal_response_id_not_found")
			return
		}
	} else {
		for _, candidate := range s.active {
			if !candidate.settled {
				submission = candidate
				break
			}
		}
		if submission == nil {
			s.mu.Unlock()
			s.warn("terminal_without_submission")
			return
		}
	}
	if submission.settled {
		s.mu.Unlock()
		s.warn("duplicate_terminal")
		return
	}
	submission.settled = true
	if submission.ResponseID != "" {
		delete(s.byResponseID, submission.ResponseID)
	}
	s.removeSubmissionLocked(submission)
	s.mu.Unlock()

	_ = eventType
	if s.observer != nil {
		s.observer.SubmissionTerminal(submission, result)
	}
	releaseSubscriptionSubmission(submission)
}

func (s *subscriptionSubmissionState) reject(submission *subscriptionSubmission, policyErr *privacy.PolicyError) {
	if submission == nil {
		return
	}
	s.mu.Lock()
	if submission.settled {
		s.mu.Unlock()
		return
	}
	submission.settled = true
	if submission.ResponseID != "" {
		delete(s.byResponseID, submission.ResponseID)
	}
	s.removeSubmissionLocked(submission)
	s.mu.Unlock()
	if s.observer != nil {
		s.observer.SubmissionRejected(submission, sanitizeSubscriptionPolicyError(policyErr))
	}
	releaseSubscriptionSubmission(submission)
}

// close 一次性中断所有未终结提交；重复 close/terminal 不会重复回调。
func (s *subscriptionSubmissionState) close(reason string) {
	var interrupted []*subscriptionSubmission
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	for _, submission := range s.active {
		if submission.settled {
			continue
		}
		submission.settled = true
		interrupted = append(interrupted, submission)
	}
	s.pending = nil
	s.active = nil
	s.byResponseID = make(map[string]*subscriptionSubmission)
	s.mu.Unlock()

	if s.observer != nil {
		for _, submission := range interrupted {
			s.observer.SubmissionInterrupted(submission, reason)
			releaseSubscriptionSubmission(submission)
		}
	} else {
		for _, submission := range interrupted {
			releaseSubscriptionSubmission(submission)
		}
	}
}

func (s *subscriptionSubmissionState) removeSubmissionLocked(target *subscriptionSubmission) {
	remove := func(items []*subscriptionSubmission) []*subscriptionSubmission {
		for i, item := range items {
			if item == target {
				copy(items[i:], items[i+1:])
				items[len(items)-1] = nil
				return items[:len(items)-1]
			}
		}
		return items
	}
	s.pending = remove(s.pending)
	s.active = remove(s.active)
}

func (s *subscriptionSubmissionState) rememberResponseIDLocked(responseID string) {
	if responseID == "" {
		return
	}
	s.recentIDs[responseID] = struct{}{}
	s.recentOrder = append(s.recentOrder, responseID)
	if len(s.recentOrder) <= subscriptionRecentResponseIDLimit {
		return
	}
	oldest := s.recentOrder[0]
	s.recentOrder = s.recentOrder[1:]
	delete(s.recentIDs, oldest)
}

func releaseSubscriptionSubmission(submission *subscriptionSubmission) {
	if submission == nil {
		return
	}
	submission.Context = nil
	submission.PrivacyState = nil
}

func (s *subscriptionSubmissionState) warn(code string) {
	if s.observer != nil {
		s.observer.SubmissionWarning(s.connectionID, code)
	}
}

type subscriptionCreate struct {
	Generate           bool
	Model              string
	PreviousResponseID string
	StreamID           string
}

func parseSubscriptionCreate(payload []byte) (subscriptionCreate, bool) {
	var envelope struct {
		Type               string          `json:"type"`
		Generate           *bool           `json:"generate"`
		Model              string          `json:"model"`
		PreviousResponseID string          `json:"previous_response_id"`
		StreamID           string          `json:"stream_id"`
		Response           json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil || envelope.Type != "response.create" {
		return subscriptionCreate{}, false
	}
	create := subscriptionCreate{
		Generate:           envelope.Generate == nil || *envelope.Generate,
		Model:              envelope.Model,
		PreviousResponseID: envelope.PreviousResponseID,
		StreamID:           envelope.StreamID,
	}
	if len(envelope.Response) > 0 && string(envelope.Response) != "null" {
		var nested struct {
			Generate           *bool  `json:"generate"`
			Model              string `json:"model"`
			PreviousResponseID string `json:"previous_response_id"`
			StreamID           string `json:"stream_id"`
		}
		if json.Unmarshal(envelope.Response, &nested) == nil {
			if nested.Generate != nil {
				create.Generate = *nested.Generate
			}
			if nested.Model != "" {
				create.Model = nested.Model
			}
			if nested.PreviousResponseID != "" {
				create.PreviousResponseID = nested.PreviousResponseID
			}
			if nested.StreamID != "" {
				create.StreamID = nested.StreamID
			}
		}
	}
	return create, true
}

func responsesEventType(payload []byte) string {
	var event struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(payload, &event) != nil {
		return ""
	}
	return strings.TrimSpace(event.Type)
}

func responsesEventID(payload []byte) string {
	var event struct {
		Response struct {
			ID string `json:"id"`
		} `json:"response"`
		ResponseID string `json:"response_id"`
	}
	if json.Unmarshal(payload, &event) != nil {
		return ""
	}
	if event.Response.ID != "" {
		return event.Response.ID
	}
	return event.ResponseID
}
