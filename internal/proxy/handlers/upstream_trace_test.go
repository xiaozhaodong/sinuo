package handlers

import (
	"context"
	"errors"
	"net/http/httptrace"
	"testing"
	"time"
)

type testUpstreamRecorder struct {
	onGet   func(UpstreamConnAttempt)
	onWrite func(UpstreamWriteTiming)
}

func (r *testUpstreamRecorder) RecordUpstreamGetConn(attempt UpstreamConnAttempt) {
	if r.onGet != nil {
		r.onGet(attempt)
	}
}

func (r *testUpstreamRecorder) RecordUpstreamWrite(timing UpstreamWriteTiming) {
	if r.onWrite != nil {
		r.onWrite(timing)
	}
}

func TestUpstreamTrace_OrderedTimingAndFailedWrite(t *testing.T) {
	var writes []UpstreamWriteTiming
	var state *UpstreamTraceState
	var ctx context.Context
	ctx, state = WithUpstreamTrace(context.Background(), &testUpstreamRecorder{onWrite: func(timing UpstreamWriteTiming) {
		// 回调在锁外执行，允许读取重放安全状态。
		if !state.WroteHeaders() {
			t.Error("成功写完前应已写出请求头")
		}
		writes = append(writes, timing)
	}})
	trace := httptrace.ContextClientTrace(ctx)
	trace.GetConn("localhost")
	trace.GotConn(httptrace.GotConnInfo{})
	trace.WroteHeaders()
	trace.WroteRequest(httptrace.WroteRequestInfo{Err: errors.New("partial write")})
	if len(writes) != 0 || state.WroteRequestErr() == nil || !state.WroteHeaders() {
		t.Fatal("写出错误不能回调，也不能清除请求头已写出的安全分界")
	}
	trace.WroteRequest(httptrace.WroteRequestInfo{})
	if len(writes) != 1 {
		t.Fatalf("成功写完应回调一次，got %d", len(writes))
	}
	timing := writes[0]
	if timing.ConnStartAt.IsZero() || timing.ConnReadyAt.Before(timing.ConnStartAt) || timing.WroteAt.Before(timing.ConnReadyAt) {
		t.Fatalf("计时时刻必须非零且按序：%+v", timing)
	}
}

func TestUpstreamTrace_TransportRetryUsesLatestConnection(t *testing.T) {
	var writes []UpstreamWriteTiming
	ctx, _ := WithUpstreamTrace(context.Background(), &testUpstreamRecorder{onWrite: func(timing UpstreamWriteTiming) {
		writes = append(writes, timing)
	}})
	trace := httptrace.ContextClientTrace(ctx)
	trace.GetConn("first")
	trace.GotConn(httptrace.GotConnInfo{})
	trace.WroteRequest(httptrace.WroteRequestInfo{})
	latestStart := time.Now()
	trace.GetConn("retry")
	latestReady := time.Now()
	trace.GotConn(httptrace.GotConnInfo{Reused: true})
	trace.WroteRequest(httptrace.WroteRequestInfo{})
	if len(writes) != 2 || writes[1].ConnStartAt.Before(latestStart) || writes[1].ConnReadyAt.Before(latestReady) {
		t.Fatalf("重试应回调最后一次连接时刻：%+v", writes)
	}
	if writes[1].ConnStartAt.After(writes[1].ConnReadyAt) || writes[1].ConnReadyAt.After(writes[1].WroteAt) {
		t.Fatalf("重试三时刻不单调：%+v", writes[1])
	}
}

// 再次取连接时，只有上一次已把请求写给上游才算传输层重发；
// 没写出就重取（连接池换掉失效连接）是同一次联系（真机 req-5d918340：两次 GetConn 相隔 6µs）。
func TestUpstreamTrace_ReconnectWithoutWriteIsSameContact(t *testing.T) {
	var attempts []UpstreamConnAttempt
	ctx, _ := WithUpstreamTrace(context.Background(), &testUpstreamRecorder{onGet: func(attempt UpstreamConnAttempt) {
		attempts = append(attempts, attempt)
	}})
	trace := httptrace.ContextClientTrace(ctx)
	trace.GetConn("pool")
	trace.GetConn("dial")
	trace.GotConn(httptrace.GotConnInfo{})
	trace.WroteHeaders()
	trace.GetConn("retry")
	if len(attempts) != 3 {
		t.Fatalf("尝试次数: %d", len(attempts))
	}
	if first := attempts[0]; first.TransportRetry || first.SameContact {
		t.Fatalf("首次取连接不应带标记: %+v", first)
	}
	if reconnect := attempts[1]; reconnect.TransportRetry || !reconnect.SameContact || reconnect.Previous.GotConn {
		t.Fatalf("未写出即重取应为同一次联系: %+v", reconnect)
	}
	if retry := attempts[2]; !retry.TransportRetry || retry.SameContact || !retry.Previous.WroteHeaders {
		t.Fatalf("写出请求头后再取连接应为传输层重发: %+v", retry)
	}
}

// 每次 GetConn 摘要只描述上一次尝试，重放安全边界则跨尝试保持粘滞。
func TestUpstreamTrace_ConnectionAttemptSummary(t *testing.T) {
	var attempts []UpstreamConnAttempt
	ctx, state := WithUpstreamTrace(context.Background(), &testUpstreamRecorder{onGet: func(attempt UpstreamConnAttempt) {
		attempts = append(attempts, attempt)
	}})
	trace := httptrace.ContextClientTrace(ctx)
	trace.GetConn("upstream")
	trace.GotConn(httptrace.GotConnInfo{Reused: true, WasIdle: true, IdleTime: 2 * time.Second})
	trace.WroteHeaders()
	writeErr := errors.New("partial write")
	trace.WroteRequest(httptrace.WroteRequestInfo{Err: writeErr})
	trace.GetConn("upstream")
	if len(attempts) != 2 || attempts[0].TransportRetry || !attempts[1].TransportRetry {
		t.Fatalf("尝试: %+v", attempts)
	}
	prev := attempts[1].Previous
	if !prev.GotConn || !prev.Reused || !prev.WasIdle || prev.IdleTime != 2*time.Second ||
		!prev.WroteHeaders || !prev.WroteRequest || prev.WriteError != writeErr || prev.Elapsed <= 0 {
		t.Fatalf("上一次摘要不完整: %+v", prev)
	}
	trace.GetConn("upstream")
	prev = attempts[2].Previous
	if prev.GotConn || prev.WroteHeaders || prev.WroteRequest || prev.WriteError != nil {
		t.Fatalf("摘要未重置: %+v", prev)
	}
	if !state.WroteHeaders() {
		t.Fatal("跨重发的请求头安全边界必须保持true")
	}
}
