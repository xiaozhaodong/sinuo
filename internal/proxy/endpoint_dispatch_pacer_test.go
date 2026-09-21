package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestEndpointDispatchPacerSpacesConcurrentDispatches(t *testing.T) {
	const interval = 35 * time.Millisecond
	pacer := newEndpointDispatchPacer(interval)
	start := make(chan struct{})
	results := make(chan time.Time, 3)
	errors := make(chan error, 3)

	for i := 0; i < 3; i++ {
		go func() {
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err := pacer.Wait(ctx)
			if err == nil {
				results <- time.Now()
			}
			errors <- err
		}()
	}
	close(start)

	times := make([]time.Time, 0, 3)
	for i := 0; i < 3; i++ {
		if err := <-errors; err != nil {
			t.Fatalf("并发放行失败: %v", err)
		}
		times = append(times, <-results)
	}
	sort.Slice(times, func(i, j int) bool { return times[i].Before(times[j]) })
	for i := 1; i < len(times); i++ {
		if gap := times[i].Sub(times[i-1]); gap < interval-5*time.Millisecond {
			t.Fatalf("相邻出站间隔过短: gap=%s interval=%s", gap, interval)
		}
	}
}

func TestEndpointDispatchPacerWaitIsContextCancelable(t *testing.T) {
	pacer := newEndpointDispatchPacer(250 * time.Millisecond)
	if _, err := pacer.Wait(context.Background()); err != nil {
		t.Fatalf("首次放行失败: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	startedAt := time.Now()
	if _, err := pacer.Wait(ctx); err != context.DeadlineExceeded {
		t.Fatalf("等待取消应返回 deadline exceeded，got %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed > 150*time.Millisecond {
		t.Fatalf("取消没有及时中断等待: %s", elapsed)
	}
}

func TestWaitForEndpointDispatchOnlyTargetsVenlacy(t *testing.T) {
	handler := &Handler{endpointDispatchPacer: newEndpointDispatchPacer(200 * time.Millisecond)}
	if _, err := handler.waitForEndpointDispatch(context.Background(), temporaryVenlacyEndpointName); err != nil {
		t.Fatalf("Venlacy 首次放行失败: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if waited, err := handler.waitForEndpointDispatch(ctx, "other-endpoint"); err != nil || waited != 0 {
		t.Fatalf("其他端点不应进入临时限速: waited=%s err=%v", waited, err)
	}
}

func TestEndpointPipelineVenlacyRetryUsesSharedDispatchPacer(t *testing.T) {
	const interval = 40 * time.Millisecond
	var hits int32
	var timesMu sync.Mutex
	var hitTimes []time.Time
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		timesMu.Lock()
		hitTimes = append(hitTimes, time.Now())
		timesMu.Unlock()
		if atomic.AddInt32(&hits, 1) == 1 {
			http.Error(w, `{"error":{"type":"rate_limit_error"}}`, http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(upstream.Close)

	handler, _ := newEndpointPipelineTestHandler(t,
		endpointPipelineConfig(temporaryVenlacyEndpointName, upstream.URL, 1),
	)
	handler.endpointDispatchPacer = newEndpointDispatchPacer(interval)

	recorder := performEndpointPipelineRequest(t, handler, false)
	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 Venlacy 限流后重试成功，got status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Fatalf("期望两次上游 attempt，got %d", got)
	}
	timesMu.Lock()
	defer timesMu.Unlock()
	if gap := hitTimes[1].Sub(hitTimes[0]); gap < interval-5*time.Millisecond {
		t.Fatalf("429 重试绕过临时限速: gap=%s interval=%s", gap, interval)
	}
}
