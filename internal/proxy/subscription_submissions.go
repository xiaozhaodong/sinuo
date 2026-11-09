package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"sinuo/internal/monitor"
	"sinuo/internal/privacy"
	"sinuo/internal/proxy/handlers"
	"sinuo/internal/proxy/restorestream"

	"github.com/tidwall/gjson"
)

// subscriptionSubmissionObserver 是 P2b 的同步生命周期接缝。
// 回调参数不含请求正文；实现不得在回调中再次解析 Responses 事件。
type subscriptionSubmissionObserver interface {
	SubmissionStarted(*subscriptionSubmission)
	// SubmissionBound 携带 response.created 的解析结果（响应模型与回显的 reasoning.effort）。
	SubmissionBound(*subscriptionSubmission, *ParseResult)
	SubmissionTerminal(*subscriptionSubmission, *ParseResult)
	SubmissionRejected(*subscriptionSubmission, *privacy.PolicyError)
	SubmissionInterrupted(*subscriptionSubmission, string)
	SubmissionWarning(connectionID, code string)
}

// subscriptionSubmission 表示一条 WS 上的逻辑 response，而不是整条连接。
type subscriptionSubmission struct {
	RequestID          string
	SessionID          string
	ConnectionID       string
	Sequence           uint64
	Model              string
	Reasoning          string // v6.3 请求思考串
	PreviousResponseID string
	StreamID           string
	ResponseID         string
	Generate           bool
	ServerInitiated    bool
	StartedAt          time.Time
	PrivacyState       *handlers.PrivacyRequestState
	Context            context.Context
	restore            *restorestream.Writer
	restoreStatus      string
	terminalRead       *ParseResult
	successorView      *privacy.RestoreView
	dispatched         bool

	settled       bool
	responseBytes int64
	firstOutput   bool
}

type subscriptionSubmissionState struct {
	restoring    atomic.Int64 // 已发布且尚未结束的回填响应数量
	mu           sync.Mutex
	connectionID string
	sessionID    string
	baseContext  context.Context
	observer     subscriptionSubmissionObserver
	sequence     uint64
	closed       bool
	pending      []*subscriptionSubmission
	active       []*subscriptionSubmission
	byResponseID map[string]*subscriptionSubmission
	recentIDs    map[string]subscriptionCompletedResponse
	recentOrder  []string
	activity     *monitor.RequestActivity
	requestIDs   monitor.RequestIDGenerator
	inFlight     map[*subscriptionSubmission]*monitor.RequestLease
}

const subscriptionRecentResponseIDLimit = 256

// 最近终态只保留去重和迟到回执所需的数据，不持有请求或回填字典。
type subscriptionCompletedResponse struct {
	streamID       string
	restored       bool
	sequenceOffset int64
}

func newSubscriptionSubmissionState(ctx context.Context, connectionID string, observer subscriptionSubmissionObserver) *subscriptionSubmissionState {
	if ctx == nil {
		ctx = context.Background()
	}
	state := &subscriptionSubmissionState{
		connectionID: connectionID,
		sessionID:    clientSessionFromContext(ctx),
		baseContext:  ctx,
		observer:     observer,
		byResponseID: make(map[string]*subscriptionSubmission),
		recentIDs:    make(map[string]subscriptionCompletedResponse),
	}
	if info := subscriptionRequestInfoFromContext(ctx); info != nil {
		state.activity = info.activity
		state.requestIDs = info.requestIDs
	}
	return state
}

// beginCreate 在隐私扫描前登记提交；generate 缺省为 true。
func (s *subscriptionSubmissionState) beginCreate(payload []byte) (*subscriptionSubmission, bool, error) {
	create, ok := parseSubscriptionCreate(payload)
	if !ok {
		return nil, false, nil
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.warn("submission_after_close")
		return nil, true, nil
	}
	requestID, err := s.requestIDs.Next(s.baseContext)
	if err != nil {
		s.mu.Unlock()
		return nil, true, err
	}
	s.sequence++
	privacyState := newSubscriptionPrivacyRequestState(requestID, nil)
	privacyState.SessionID = s.sessionID
	ctx := context.WithValue(s.baseContext, "conn_id", requestID) //nolint:staticcheck // 与旧入口请求 ID key 一致
	ctx = handlers.WithPrivacyRequestState(ctx, privacyState)
	submission := &subscriptionSubmission{
		RequestID:          requestID,
		SessionID:          s.sessionID,
		ConnectionID:       s.connectionID,
		Sequence:           s.sequence,
		Model:              create.Model,
		Reasoning:          create.Reasoning,
		PreviousResponseID: create.PreviousResponseID,
		StreamID:           create.StreamID,
		Generate:           create.Generate,
		StartedAt:          time.Now(),
		PrivacyState:       privacyState,
		Context:            ctx,
	}
	if !s.admitActivityLocked(submission) {
		s.mu.Unlock()
		return nil, true, nil
	}
	s.pending = append(s.pending, submission)
	s.active = append(s.active, submission)
	s.mu.Unlock()

	if s.observer != nil {
		s.observer.SubmissionStarted(submission)
	}
	return submission, true, nil
}

// 观察原帧时固定归属和用量。回填路径等下游写出后才用这份收据结算。
type subscriptionResponseEvent struct {
	typ, responseID   string
	submission        *subscriptionSubmission
	result            *ParseResult
	terminal          bool
	metadata          bool
	controlReceipt    bool
	duplicate         bool
	errorFrame        []byte
	connectionExpired bool
}

func (s *subscriptionSubmissionState) observeUpstream(payload []byte, restoring bool) (subscriptionResponseEvent, error) {
	ev := subscriptionResponseEvent{typ: responsesEventType(payload)}
	ev.controlReceipt = subscriptionWSControlReceipt(ev.typ)
	ev.metadata = ev.typ == "responsesapi.websocket_timing" || strings.HasPrefix(ev.typ, "codex.")
	if ev.typ == "" || ev.controlReceipt || ev.metadata {
		return ev, nil
	}
	ev.result = ParseResponsesEventJSON(ev.typ, payload)
	pendingOnly := false
	if restoring && ev.typ == "error" {
		frame, err := restoreWSErrorFrame(payload)
		if err != nil {
			return ev, err
		}
		ev.errorFrame = frame
		switch gjson.GetBytes(frame, "error.code").Str {
		case "websocket_connection_limit_reached":
			ev.connectionExpired = true
			return ev, nil
		case "invalid_stream_id", "websocket_stream_limit_reached", "previous_response_not_found", "previous_response_id_not_found":
			pendingOnly = true
		}
		// 结算仍采用原始用量，但错误详情不能持久化上游回显的输入。
		ev.result.ErrorInfo = ParseResponsesEventJSON("error", frame).ErrorInfo
	}
	ev.responseID = responsesEventID(payload)
	streamID := gjson.GetBytes(payload, "stream_id").Str
	if ev.typ == "response.created" {
		if err := s.bindCreated(ev.responseID, streamID, ev.result, restoring); err != nil {
			return ev, err
		}
	}
	ev.submission = s.deliveryTarget(ev.responseID, streamID, ev.typ, restoring, pendingOnly)
	ev.terminal = ev.result != nil && ev.result.IsCompleted && isResponsesTerminalEvent(ev.typ)
	if ev.terminal && ev.submission == nil && s.duplicateRestoreTerminal(ev.responseID, streamID, restoring) {
		ev.duplicate = true
		return ev, nil
	}
	s.responseProgress(ev.submission, ev.result, int64(len(payload)))
	if ev.terminal && restoring && ev.submission != nil {
		s.mu.Lock()
		ev.submission.terminalRead = ev.result
		s.mu.Unlock()
	} else if ev.typ != "response.created" && !ev.terminal {
		s.mergeIntermediateDiagnostics(ev.submission, ev.result)
	}
	return ev, nil
}

// mergeIntermediateDiagnostics 把非 created、非终态事件携带的响应模型 / 思考字段合并进对应 submission。
func (s *subscriptionSubmissionState) mergeIntermediateDiagnostics(submission *subscriptionSubmission, result *ParseResult) {
	if result == nil || s.observer == nil || submission == nil {
		return
	}
	if result.ResponseModel == "" && result.ResponseReasoningFields.IsEmpty() {
		return
	}
	s.mu.Lock()
	if submission.settled {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.observer.SubmissionBound(submission, result)
}

// responseProgress 只向观察者传有界关联与字节/时序，不保存帧或逐帧推送 UI。
func (s *subscriptionSubmissionState) responseProgress(submission *subscriptionSubmission, result *ParseResult, size int64) {
	s.mu.Lock()
	if submission == nil || submission.settled {
		s.mu.Unlock()
		return
	}
	submission.responseBytes += size
	first := !submission.firstOutput
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
		SubmissionStreamResult(*subscriptionSubmission, *ParseResult, int64, time.Time)
	}); ok {
		observer.SubmissionStreamResult(submission, result, size, time.Now())
	}
	if observer, ok := s.observer.(interface {
		SubmissionResponseProgress(*subscriptionSubmission, int64)
	}); ok {
		observer.SubmissionResponseProgress(submission, total)
	}
}

func (s *subscriptionSubmissionState) bindCreated(responseID, streamID string, result *ParseResult, strict bool) error {
	var submission *subscriptionSubmission
	var started bool
	if responseID == "" {
		s.warn("response_created_without_id")
		if strict {
			return restorestream.ErrDelivery
		}
		return nil
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errSubscriptionDraining
	}
	// 判重必须发生在消费 pending 或创建 successor 之前。
	if _, completed := s.recentIDs[responseID]; completed || s.byResponseID[responseID] != nil {
		s.mu.Unlock()
		s.warn("duplicate_response_id")
		return nil
	}
	selected := -1
	for n, candidate := range s.pending {
		if candidate.settled || candidate.terminalRead != nil || !candidate.dispatched {
			continue
		}
		if streamID == "" || streamID == candidate.StreamID {
			selected = n
			break
		}
		if selected == -1 && candidate.StreamID == "" {
			selected = n
		}
	}
	if selected != -1 {
		submission = s.pending[selected]
		s.pending = slices.Delete(s.pending, selected, selected+1)
	}
	if submission == nil {
		// 服务端 steering successor 没有下游 create，仍需独立生命周期。
		requestID, err := s.requestIDs.Next(s.baseContext)
		if err != nil {
			s.mu.Unlock()
			return fmt.Errorf("allocate successor request ID: %w", err)
		}
		s.sequence++
		submission = &subscriptionSubmission{
			RequestID:       requestID,
			SessionID:       s.sessionID,
			ConnectionID:    s.connectionID,
			Sequence:        s.sequence,
			Generate:        true,
			ServerInitiated: true,
			dispatched:      true,
			PrivacyState:    handlers.NewPrivacyRequestState(requestID),
			StartedAt:       time.Now(),
			Context:         context.WithValue(s.baseContext, "conn_id", requestID), //nolint:staticcheck
		}
		if result != nil && result.ModelName != "default" {
			submission.Model = result.ModelName
		}
		if !s.admitActivityLocked(submission) {
			s.mu.Unlock()
			return errSubscriptionDraining
		}
		s.active = append(s.active, submission)
		started = true
	}
	submission.ResponseID = responseID
	if streamID != "" {
		submission.StreamID = streamID
	}
	s.byResponseID[responseID] = submission
	s.mu.Unlock()

	if s.observer != nil {
		if started {
			s.observer.SubmissionStarted(submission)
		}
		s.observer.SubmissionBound(submission, result)
	}
	return nil
}

func (s *subscriptionSubmissionState) finishTerminal(submission *subscriptionSubmission, responseID string, result *ParseResult) func() {
	if submission == nil {
		if responseID == "" {
			s.warn("terminal_without_submission")
		} else {
			s.warn("terminal_response_id_not_found")
		}
		return nil
	}
	s.mu.Lock()
	if submission.settled {
		s.mu.Unlock()
		s.warn("duplicate_terminal")
		return nil
	}
	submission.settled = true
	if submission.ResponseID != "" {
		s.rememberCompletedLocked(submission)
		delete(s.byResponseID, submission.ResponseID)
	}
	s.removeSubmissionLocked(submission)
	s.mu.Unlock()

	if s.observer != nil {
		s.observer.SubmissionTerminal(submission, result)
	}
	releaseSubscriptionSubmission(submission)
	return s.activityForwarded(submission)
}

func (s *subscriptionSubmissionState) reject(submission *subscriptionSubmission, policyErr *privacy.PolicyError) func() {
	if submission == nil {
		return nil
	}
	s.mu.Lock()
	if submission.settled {
		s.mu.Unlock()
		return nil
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
	return s.activityForwarded(submission)
}

// close 一次性中断所有未终结提交；重复 close/terminal 不会重复回调。
func (s *subscriptionSubmissionState) close(reason string) {
	defer s.releaseActivity()
	var interrupted []*subscriptionSubmission
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.restoring.Store(0)
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

	for _, sub := range interrupted {
		sub.reportRestore("interrupted", "subscription_response_interrupted")
		if s.observer != nil {
			s.observer.SubmissionInterrupted(sub, reason)
		}
		releaseSubscriptionSubmission(sub)
	}
}

func (s *subscriptionSubmissionState) removeSubmissionLocked(target *subscriptionSubmission) {
	if target.restore != nil {
		s.restoring.Add(-1)
	}
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

func (s *subscriptionSubmissionState) rememberCompletedLocked(sub *subscriptionSubmission) {
	receipt := subscriptionCompletedResponse{streamID: sub.StreamID, restored: sub.restore != nil}
	if receipt.restored {
		receipt.sequenceOffset = sub.restore.SequenceOffset()
	}
	s.recentIDs[sub.ResponseID] = receipt
	s.recentOrder = append(s.recentOrder, sub.ResponseID)
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
	if submission.restore != nil {
		submission.reportRestore("interrupted", "subscription_response_interrupted")
		submission.restore.Release()
		submission.restore = nil
	}
	if submission.successorView != nil {
		submission.successorView.Release()
		submission.successorView = nil
	}
	if submission.PrivacyState != nil {
		submission.PrivacyState.ReleaseRestore()
	}
	submission.Context = nil
	submission.PrivacyState = nil
}

func (s *subscriptionSubmissionState) warn(code string) {
	if s.observer != nil {
		s.observer.SubmissionWarning(s.connectionID, code)
	}
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
