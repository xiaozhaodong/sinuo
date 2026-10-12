package handlers

import (
	"context"
	"errors"
	"net/http/httptrace"
	"testing"
	"time"
)

func TestUpstreamTrace_OrderedTimingAndFailedWrite(t *testing.T) {
	var writes []UpstreamWriteTiming
	var state *UpstreamTraceState
	var ctx context.Context
	ctx, state = WithUpstreamTrace(context.Background(), func(timing UpstreamWriteTiming) {
		// 回调在锁外执行，允许读取重放安全状态。
		if !state.WroteHeaders() {
			t.Error("成功写完前应已写出请求头")
		}
		writes = append(writes, timing)
	})
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
	ctx, _ := WithUpstreamTrace(context.Background(), func(timing UpstreamWriteTiming) {
		writes = append(writes, timing)
	})
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
