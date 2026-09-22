package handlers

import (
	"context"
	"crypto/tls"
	"net/http/httptrace"
	"sync"
	"time"
)

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

func (s *UpstreamTraceState) markGotConn() {
	s.mu.Lock()
	s.gotConn = true
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
	s.mu.Unlock()
}

func (s *UpstreamTraceState) markWroteRequest(err error) {
	s.mu.Lock()
	s.wroteRequest = true
	s.wroteRequestErr = err
	s.mu.Unlock()
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

// WithUpstreamTrace 在 ctx 上挂载 GotConn / TLSHandshakeDone / WroteHeaders /
// WroteRequest 四事件 trace，返回记录事件的状态结构。
// onWroteRequest 保留首字计时用途（可为 nil）。
func WithUpstreamTrace(ctx context.Context, onWroteRequest func(time.Time)) (context.Context, *UpstreamTraceState) {
	state := &UpstreamTraceState{}
	trace := &httptrace.ClientTrace{
		GotConn: func(httptrace.GotConnInfo) {
			state.markGotConn()
		},
		TLSHandshakeDone: func(tls.ConnectionState, error) {
			state.markTLSHandshakeDone()
		},
		WroteHeaders: func() {
			state.markWroteHeaders()
		},
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			state.markWroteRequest(info.Err)
			// 只有完整写出成功才形成有效 upstream_write_ms；写出阶段报错时
			// 请求可能只发送了部分内容，不能伪造“上游写完”时间。
			if info.Err == nil && onWroteRequest != nil {
				onWroteRequest(time.Now())
			}
		},
	}
	return httptrace.WithClientTrace(ctx, trace), state
}
