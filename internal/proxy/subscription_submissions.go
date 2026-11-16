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
	"unicode/utf8"

	"sinuo/internal/monitor"
	"sinuo/internal/privacy"
	"sinuo/internal/proxy/handlers"
	"sinuo/internal/proxy/restorestream"
)

// subscriptionSubmissionObserver 是 P2b 的同步生命周期接缝。
// 回调参数不含请求正文；实现不得在回调中再次解析 Responses 事件。
type subscriptionSubmissionObserver interface {
	SubmissionStarted(*subscriptionSubmission)
	// BindPrivacyState 在扫描或回填前独立调用；追加输入没有 submission，预热也无需生成记录。
	BindPrivacyState(*handlers.PrivacyRequestState, *subscriptionSubmission)
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
	ClientIP           string
	UserAgent          string
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
	restoreBypassed    bool
	restoreDecided     bool // 每条响应只决定一次，当前连接配置不覆盖已发布的结果。
	restoreRejected    bool // 首帧拒绝后，已经读入的后续帧也不能变成普通透传。

	settled       bool
	responseBytes int64
	firstOutput   bool
}

type subscriptionSubmissionState struct {
	restoring       atomic.Int64 // 已发布且尚未结束的回填响应数量
	bypassed        atomic.Int64 // 原文放行的在途响应与有界去重收据数量
	mu              sync.Mutex
	connectionID    string
	sessionID       string
	clientIP        string
	userAgent       string
	baseContext     context.Context
	observer        subscriptionSubmissionObserver
	sequence        uint64
	closed          bool
	rejectedReplies map[*subscriptionSubmission]func(int64)
	pending         []*subscriptionSubmission
	active          []*subscriptionSubmission
	byResponseID    map[string]*subscriptionSubmission
	recentIDs       map[string]subscriptionCompletedResponse
	recentOrder     []string
	activity        *monitor.RequestActivity
	requestIDs      monitor.RequestIDGenerator
	inFlight        map[*subscriptionSubmission]*monitor.RequestLease
}

const subscriptionRecentResponseIDLimit = 256

// 最近终态只保留去重和迟到回执所需的数据，不持有请求或回填字典。
type subscriptionCompletedResponse struct {
	streamID       string
	restored       bool
	bypassed       bool
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
		state.clientIP = info.ClientIP
		state.userAgent = info.UserAgent
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
		ClientIP:           s.clientIP,
		UserAgent:          s.userAgent,
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
		s.observer.BindPrivacyState(privacyState, submission)
	}
	return submission, true, nil
}

// 观察原帧时固定归属和用量。回填路径等下游写出后才用这份收据结算。
type subscriptionResponseEvent struct {
	typ, responseID   string
	streamID          string
	submission        *subscriptionSubmission
	result            *ParseResult
	terminal          bool
	metadata          bool
	controlReceipt    bool
	duplicate         bool
	errorFrame        []byte
	connectionExpired bool
}

func (s *subscriptionSubmissionState) observeUpstream(payload []byte, msg restorestream.ValidatedMessage) (subscriptionResponseEvent, error) {
	ev, valid := readSubscriptionWSEvent(payload, msg)
	return s.observeDecodedUpstream(payload, msg, ev, valid, msg.Valid(), msg.Valid())
}

// 路由只在入口解码一次；归属确定后，保护限制取自该响应自己的已发布状态。
func (s *subscriptionSubmissionState) observeDecodedUpstream(payload []byte, msg restorestream.ValidatedMessage, ev subscriptionResponseEvent, validRoute, strict, protectUnowned bool) (subscriptionResponseEvent, error) {
	if !validRoute {
		if !strict {
			// 未参与回填的连接保持原样透传；歧义帧不用于归属、结算或序号改写。
			return subscriptionResponseEvent{}, nil
		}
		if ev.typ != "error" {
			return ev, restorestream.ErrDelivery
		}
	}
	if ev.result == nil {
		return ev, nil
	}
	pendingOnly := false
	if ev.typ == "error" {
		pendingOnly, ev.connectionExpired = subscriptionWSErrorDisposition(payload)
	}
	if validRoute && ev.typ == "response.created" {
		if err := s.bindCreated(ev.responseID, ev.streamID, ev.result, strict); err != nil {
			return ev, err
		}
	}
	if validRoute {
		ev.submission = s.deliveryTarget(ev.responseID, ev.streamID, ev.typ, strict, pendingOnly)
	}
	ev.terminal = ev.result != nil && ev.result.IsCompleted && isResponsesTerminalEvent(ev.typ)
	if validRoute && ev.terminal && ev.submission == nil && s.duplicateRestoreTerminal(ev.responseID, ev.streamID) {
		ev.duplicate = true
		return ev, nil
	}
	protect := protectUnowned
	if ev.submission != nil {
		if ev.submission.restoreRejected {
			return ev, restorestream.ErrDelivery
		}
		protect = s.submissionRestores(ev.submission)
	}
	if protect && !msg.Valid() {
		return ev, restorestream.ErrDelivery
	}
	if ev.typ == "error" {
		if protect || !validRoute {
			frame, owned, err := restoreWSErrorFrameForRoute(payload, subscriptionWSRoute{ev.typ, ev.responseID, ev.streamID}, validRoute)
			if err != nil {
				return ev, err
			}
			ev.errorFrame = frame
			// 结算仍采用原始用量，但错误详情不能持久化上游回显的输入。
			ev.result.ErrorInfo = ParseResponsesEventJSON("error", frame).ErrorInfo
			if !owned {
				ev.submission = nil
				return ev, nil
			}
		}
		if ev.connectionExpired {
			return ev, nil
		}
	}
	s.responseProgress(ev.submission, ev.result, int64(len(payload)))
	if ev.terminal && ev.submission != nil {
		s.mu.Lock()
		ev.submission.terminalRead = ev.result
		s.mu.Unlock()
	} else if ev.typ != "response.created" && !ev.terminal {
		s.mergeIntermediateDiagnostics(ev.submission, ev.result)
	}
	return ev, nil
}

// 回填数据帧复用读侧解码对象校验归属；队列只保存原帧和观测结果。
// 控制回执也校验路由，但不计为生成内容，不改变既有用量与生命周期观测。
func readSubscriptionWSEvent(raw []byte, msg restorestream.ValidatedMessage) (subscriptionResponseEvent, bool) {
	var ev subscriptionResponseEvent
	if !msg.Valid() && !utf8.Valid(raw) {
		return ev, false
	}
	var decoded map[string]any
	var route subscriptionWSRoute
	var validRoute bool
	if msg.Valid() {
		decoded, _ = msg.DecodeForObservation()
	} else if validateSubscriptionJSON(raw) == nil {
		// 未通过回填深度检查不等于协议非法；普通观测沿用标准库数字语义。
		if err := json.Unmarshal(raw, &decoded); err != nil {
			decoded = nil
		}
	}
	if decoded != nil {
		route, validRoute = canonicalSubscriptionWSRoute(decoded)
	} else {
		// 数值溢出只影响用量观测，路由仍由标准解码的原始数值版本确定。
		route, validRoute = decodeSubscriptionWSRoute(raw)
	}
	ev.typ, ev.responseID, ev.streamID = route.typ, route.responseID, route.streamID
	ev.controlReceipt = subscriptionWSControlReceipt(ev.typ)
	ev.metadata = ev.typ == "responsesapi.websocket_timing" || strings.HasPrefix(ev.typ, "codex.")
	if ev.typ == "" || ev.controlReceipt || ev.metadata {
		return ev, validRoute
	}
	if decoded != nil {
		ev.result = observeDecodedResponsesEvent(ev.typ, decoded, len(raw))
	} else {
		ev.result = ParseResponsesEventJSON(ev.typ, raw)
	}
	return ev, validRoute
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
	selected := s.pendingCreatedIndexLocked(streamID)
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
			ClientIP:        s.clientIP,
			UserAgent:       s.userAgent,
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
			s.observer.BindPrivacyState(submission.PrivacyState, submission)
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
	return s.rejectWithReply(submission, policyErr, 0)
}

func (s *subscriptionSubmissionState) rejectWithReply(submission *subscriptionSubmission, policyErr *privacy.PolicyError, replyBytes int) func() {
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
	policyErr = sanitizeSubscriptionPolicyError(policyErr)
	observer, tracksDelivery := s.observer.(interface {
		SubmissionRejectionDelivered(*subscriptionSubmission, *privacy.PolicyError, int64)
	})
	awaitReply := tracksDelivery && replyBytes > 0
	if awaitReply {
		if s.rejectedReplies == nil {
			s.rejectedReplies = make(map[*subscriptionSubmission]func(int64))
		}
		s.rejectedReplies[submission] = func(n int64) { observer.SubmissionRejectionDelivered(submission, policyErr, n) }
	}
	s.mu.Unlock()
	if !awaitReply && s.observer != nil {
		s.observer.SubmissionRejected(submission, policyErr)
	}
	releaseSubscriptionSubmission(submission)
	release := s.activityForwarded(submission)
	return func() {
		s.mu.Lock()
		finish := s.rejectedReplies[submission]
		delete(s.rejectedReplies, submission)
		s.mu.Unlock()
		if finish != nil {
			finish(int64(replyBytes))
		}
		release()
	}
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
	replies := s.rejectedReplies
	s.rejectedReplies = nil
	s.restoring.Store(0)
	s.bypassed.Store(0)
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

	// 桥接已经退出，剩余的本地错误帧没有成功写出。
	for _, finish := range replies {
		finish(0)
	}
	for _, sub := range interrupted {
		sub.reportRestore("interrupted", "subscription_response_interrupted")
		if s.observer != nil {
			s.observer.SubmissionInterrupted(sub, reason)
		}
		releaseSubscriptionSubmission(sub)
	}
}

func (s *subscriptionSubmissionState) removeSubmissionLocked(target *subscriptionSubmission) {
	if target.restoreBypassed {
		s.bypassed.Add(-1)
	}
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
	receipt := subscriptionCompletedResponse{streamID: sub.StreamID, restored: sub.restore != nil, bypassed: sub.restoreBypassed}
	if receipt.bypassed {
		s.bypassed.Add(1)
	}
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
	if s.recentIDs[oldest].bypassed {
		s.bypassed.Add(-1)
	}
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
