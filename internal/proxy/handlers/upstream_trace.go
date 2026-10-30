package handlers

import (
	"context"
	"crypto/tls"
	"net/http/httptrace"
	"sync"
	"time"
)

// UpstreamWriteTiming 是同一次成功写出尝试的三个时刻。
type UpstreamWriteTiming struct {
	ConnStartAt time.Time
	ConnReadyAt time.Time
	WroteAt     time.Time
}

// UpstreamTraceRecorder 接收每次取连接与成功写完；取连接回调不得写库。
type UpstreamTraceRecorder interface {
	RecordUpstreamGetConn(UpstreamConnAttempt)
	RecordUpstreamWrite(UpstreamWriteTiming)
}

// UpstreamAttemptSummary 是同一追踪内上一次连接尝试的摘要。
type UpstreamAttemptSummary struct {
	Elapsed      time.Duration
	GotConn      bool
	Reused       bool
	WasIdle      bool
	IdleTime     time.Duration
	WroteHeaders bool
	WroteRequest bool
	WriteError   error
}

// UpstreamConnAttempt 标识一次取连接；TransportRetry 仅表示同一追踪内的重发。
type UpstreamConnAttempt struct {
	At             time.Time
	TransportRetry bool
	Previous       UpstreamAttemptSummary
}

// UpstreamTraceState 记录单次上游转发的连接级事件（端点侧与账号池侧共用）。
// 以 WroteHeaders 是否触发作为「重放安全 / 歧义」分界：
// WroteHeaders 前失败 = 请求确定未发出（DNS / refused / dial 超时 / TLS 握手失败），
// 可安全换候选重放；触发后的任何失败均视为歧义，不得据此重放。
type UpstreamTraceState struct {
	mu               sync.Mutex
	gotConn          bool
	tlsHandshakeDone bool
	wroteHeaders     bool
	wroteRequest     bool
	wroteRequestErr  error
	writeTiming      UpstreamWriteTiming
	attemptSummary   UpstreamAttemptSummary
	upstreamModel    string // v6.3 本 attempt 改写后实际发出的模型；未改写为空
}

func (s *UpstreamTraceState) setUpstreamModel(model string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.upstreamModel = model
	s.mu.Unlock()
}

// UpstreamModel 返回本 attempt 实际发给上游的模型；未发生改写时为空串（= 与请求相同）。
func (s *UpstreamTraceState) UpstreamModel() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.upstreamModel
}

func (s *UpstreamTraceState) markGetConn() UpstreamConnAttempt {
	s.mu.Lock()
	defer s.mu.Unlock()
	at := time.Now()
	attempt := UpstreamConnAttempt{At: at}
	if !s.writeTiming.ConnStartAt.IsZero() {
		attempt.TransportRetry = true
		attempt.Previous = s.attemptSummary
		attempt.Previous.Elapsed = at.Sub(s.writeTiming.ConnStartAt)
	}
	// 最终尝试计时与摘要重置；重放安全边界 wroteHeaders 始终保持粘滞。
	s.writeTiming = UpstreamWriteTiming{ConnStartAt: at}
	s.attemptSummary = UpstreamAttemptSummary{}
	return attempt
}

func (s *UpstreamTraceState) markGotConn(info httptrace.GotConnInfo) {
	s.mu.Lock()
	s.gotConn = true
	s.attemptSummary.GotConn = true
	s.attemptSummary.Reused = info.Reused
	s.attemptSummary.WasIdle = info.WasIdle
	s.attemptSummary.IdleTime = info.IdleTime
	s.writeTiming.ConnReadyAt = time.Now()
	s.mu.Unlock()
}

func (s *UpstreamTraceState) markTLSHandshakeDone() {
	s.mu.Lock()
	s.tlsHandshakeDone = true
	s.mu.Unlock()
}

func (s *UpstreamTraceState) markWroteHeaders() {
	s.mu.Lock()
	s.wroteHeaders = true
	s.attemptSummary.WroteHeaders = true
	s.mu.Unlock()
}

func (s *UpstreamTraceState) markWroteRequest(err error) UpstreamWriteTiming {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wroteRequest = true
	s.wroteRequestErr = err
	s.attemptSummary.WroteRequest = true
	s.attemptSummary.WriteError = err
	s.writeTiming.WroteAt = time.Now()
	return s.writeTiming
}

// WroteHeaders 返回请求头是否已写向上游。
func (s *UpstreamTraceState) WroteHeaders() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wroteHeaders
}

// WroteRequestErr 返回请求体写出阶段记录的错误（未写出或成功时为 nil）。
func (s *UpstreamTraceState) WroteRequestErr() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wroteRequestErr
}

// MarkWroteHeadersForTest 测试辅助：标记 WroteHeaders 已触发。
func (s *UpstreamTraceState) MarkWroteHeadersForTest() {
	s.markWroteHeaders()
}

// WithUpstreamTrace 挂载连接与写出事件；成功写完后在锁外回调同次尝试的时刻。
func WithUpstreamTrace(ctx context.Context, recorder UpstreamTraceRecorder) (context.Context, *UpstreamTraceState) {
	state := &UpstreamTraceState{}
	trace := &httptrace.ClientTrace{
		GetConn: func(string) {
			attempt := state.markGetConn()
			if recorder != nil {
				recorder.RecordUpstreamGetConn(attempt)
			}
		},
		GotConn: func(info httptrace.GotConnInfo) {
			state.markGotConn(info)
		},
		TLSHandshakeDone: func(tls.ConnectionState, error) {
			state.markTLSHandshakeDone()
		},
		WroteHeaders: func() {
			state.markWroteHeaders()
		},
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			timing := state.markWroteRequest(info.Err)
			// 只有完整写出成功才形成有效 upstream_write_ms；写出阶段报错时
			// 请求可能只发送了部分内容，不能伪造“上游写完”时间。
			if info.Err == nil && recorder != nil {
				recorder.RecordUpstreamWrite(timing)
			}
		},
	}
	return httptrace.WithClientTrace(ctx, trace), state
}
