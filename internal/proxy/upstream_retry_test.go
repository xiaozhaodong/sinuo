package proxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sinuo/internal/accountauth"
	"sinuo/internal/endpoint"
	"sinuo/internal/middleware"
	"sinuo/internal/proxy/handlers"
	"sinuo/internal/store"
	timezonepolicy "sinuo/internal/timezone"
	"sinuo/internal/tracking"
)

func attachRetryTracker(t *testing.T, h *Handler) *tracking.UsageTracker {
	t.Helper()
	tracker := newLifecyclePanelTestTracker(t)
	h.SetUsageTracker(tracker)
	h.SetMonitoringMiddleware(middleware.NewMonitoringMiddleware(h.endpointManager))
	return tracker
}

func performRetryResponsesRequest(t *testing.T, h *Handler) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-4.1","input":[]}`))
	req = req.WithContext(context.WithValue(req.Context(), "conn_id", "retry-account"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func requireRetryObservation(t *testing.T, data *tracking.RequestLifecycleData, count int) {
	t.Helper()
	if data.UpstreamFirstDispatchMs == nil || data.Detail.UpstreamFirstDispatchMs == nil ||
		data.Detail.RetryCount != count {
		t.Fatalf("新口径首次联系与重试数不完整，want retries=%d: %+v", count, data)
	}
}

func requireOnlyRetryObservation(t *testing.T, tracker *tracking.UsageTracker, count int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		rows, _, err := tracker.QueryRequestDetailsWithHotPool(context.Background(), &tracking.QueryOptions{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 1 {
			requireRetryObservation(t, timingLifecycleData(t, tracker, rows[0].RequestID), count)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("请求行: %+v", rows)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestUpstreamRetry_ContactWriteAndStatusAuthority(t *testing.T) {
	tracker := newLifecyclePanelTestTracker(t)
	bus := &subscriptionPresentationEventBus{}
	rlm := NewRequestLifecycleManager(tracker, nil, "retry-authority", bus)
	rlm.SetStartTime(time.Now().Add(-time.Second))
	rlm.StartRequest("127.0.0.1", "test", "POST", "/v1/messages", true)
	first, latest := rlm.startTime.Add(10*time.Millisecond), rlm.startTime.Add(100*time.Millisecond)
	rlm.RecordUpstreamGetConn(handlers.UpstreamConnAttempt{At: first})
	rlm.RecordUpstreamGetConn(handlers.UpstreamConnAttempt{At: latest})
	// GetConn 不访问 tracker；首次联系与计数要等写完时同笔写入。
	before := timingLifecycleData(t, tracker, rlm.requestID)
	if before.UpstreamFirstDispatchMs != nil || before.Detail.RetryCount != 0 {
		t.Fatal("GetConn 不得落库")
	}
	rlm.RecordUpstreamWrite(handlers.UpstreamWriteTiming{ConnStartAt: latest, ConnReadyAt: latest, WroteAt: latest.Add(time.Millisecond)})
	data := timingLifecycleData(t, tracker, rlm.requestID)
	requireRetryObservation(t, data, 1)
	if *data.UpstreamFirstDispatchMs != 10 || *data.UpstreamDispatchMs != 100 || rlm.persistedContacts != 2 {
		t.Fatalf("首次/最终计时: %+v", data)
	}
	rlm.IncrementAttempt()
	rlm.IncrementRetry()
	rlm.UpdateStatus("processing", 99, 200)
	requireRetryObservation(t, timingLifecycleData(t, tracker, rlm.requestID), 1)
	events := timingEvents(bus.published, requestChangeUpstreamRetry)
	if len(events) != 1 || events[0].Data["retry_count"] != 1 ||
		events[0].Data["retry_started_at"] != timezonepolicy.FormatStorage(latest) {
		t.Fatalf("重试事件: %+v", events)
	}
	rlm.RecordFirstToken()
	rlm.RecordUpstreamGetConn(handlers.UpstreamConnAttempt{At: time.Now()})
	if rlm.GetRetryCount() != 1 {
		t.Fatal("首响后计数必须冻结")
	}
}

func TestUpstreamRetry_TerminalFlushWithoutSuccessfulWrite(t *testing.T) {
	for _, hot := range []bool{true, false} {
		for _, terminal := range []string{"failed", "cancelled", "completed", "quality", "cost"} {
			t.Run(fmt.Sprintf("%t/%s", hot, terminal), func(t *testing.T) {
				tracker := newTimingTestTracker(t, hot)
				rlm := NewRequestLifecycleManager(tracker, nil, "retry-terminal", nil)
				rlm.StartRequest("127.0.0.1", "test", "POST", "/v1/messages", true)
				for range 2 {
					rlm.RecordUpstreamGetConn(handlers.UpstreamConnAttempt{At: time.Now()})
				}
				settleRetryLifecycle(rlm, terminal)
				data := waitRetryTerminal(t, tracker, rlm.requestID)
				requireRetryObservation(t, data, 1)
				if data.UpstreamWriteMs != nil {
					t.Fatal("未成功写出，不得制造写完偏移")
				}
				rlm.RecordUpstreamGetConn(handlers.UpstreamConnAttempt{At: time.Now()})
				if rlm.GetRetryCount() != 1 {
					t.Fatal("终态后不能继续计数")
				}
			})
		}
	}
}

func settleRetryLifecycle(rlm *RequestLifecycleManager, terminal string) {
	switch terminal {
	case "failed":
		rlm.FailRequest("network_error", "dial failed", 502)
	case "cancelled":
		rlm.CancelRequest("client disconnected", nil)
	case "completed":
		rlm.CompleteRequest(nil)
	case "quality":
		rlm.CompleteRequestWithQuality(nil, "incomplete_stream")
	case "cost":
		rlm.CompleteRequestWithCost(0)
	}
}

func waitRetryTerminal(t *testing.T, tracker *tracking.UsageTracker, id string) *tracking.RequestLifecycleData {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		data, err := tracker.GetRequestLifecycleData(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if data != nil && data.UpstreamFirstDispatchMs != nil && data.Detail.Status != "pending" {
			return data
		}
		if time.Now().After(deadline) {
			t.Fatal("等待终态超时")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestUpstreamRetry_FirstResponseBeforeWriteAndFinalFailure(t *testing.T) {
	tracker := newLifecyclePanelTestTracker(t)
	rlm := NewRequestLifecycleManager(tracker, nil, "retry-early-response", nil)
	rlm.StartRequest("127.0.0.1", "test", "POST", "/v1/messages", true)
	for range 2 {
		rlm.RecordUpstreamGetConn(handlers.UpstreamConnAttempt{At: time.Now()})
	}
	rlm.RecordFirstToken()
	requireRetryObservation(t, timingLifecycleData(t, tracker, rlm.requestID), 1)
	ctx := &ErrorContext{RequestID: rlm.requestID, AttemptCount: 99, OriginalError: errors.New("interrupted")}
	if err := rlm.errorRecovery.ExecuteRetry(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	requireRetryObservation(t, timingLifecycleData(t, tracker, rlm.requestID), 1)
	rlm.errorRecovery.HandleFinalFailure(ctx)
	requireRetryObservation(t, timingLifecycleData(t, tracker, rlm.requestID), 1)
}

func TestUpstreamRetry_ConcurrentContactWriteAndFirstResponse(t *testing.T) {
	tracker := newLifecyclePanelTestTracker(t)
	rlm := NewRequestLifecycleManager(tracker, nil, "retry-concurrent", nil)
	rlm.StartRequest("127.0.0.1", "test", "POST", "/v1/messages", true)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			at := time.Now()
			rlm.RecordUpstreamGetConn(handlers.UpstreamConnAttempt{At: at})
			rlm.RecordUpstreamWrite(handlers.UpstreamWriteTiming{ConnStartAt: at, ConnReadyAt: at, WroteAt: time.Now()})
			rlm.UpdateStatus("processing", i, 200)
		}()
	}
	wg.Wait()
	rlm.RecordFirstToken()
	frozen := rlm.GetRetryCount()
	rlm.RecordUpstreamGetConn(handlers.UpstreamConnAttempt{At: time.Now()})
	rlm.CompleteRequest(nil)
	requireRetryObservation(t, timingLifecycleData(t, tracker, rlm.requestID), frozen)
	if frozen != 19 {
		t.Fatalf("并发取连接只计一次: %d", frozen)
	}
}

func TestUpstreamRetry_SubscriptionHTTPAndWS(t *testing.T) {
	for _, hot := range []bool{true, false} {
		t.Run(fmt.Sprint(hot), func(t *testing.T) {
			tracker := newTimingTestTracker(t, hot)
			bus := &subscriptionPresentationEventBus{}
			observer := newSubscriptionTrackingObserver(tracker, bus)
			handle := observer.StartHTTP(SubscriptionHTTPStart{RequestID: "retry-subscription", StartedAt: time.Now(), Method: "POST", Path: "/v1/responses", RequestKind: tracking.RequestKindGeneration})
			for range 2 {
				handle.RecordUpstreamGetConn(handlers.UpstreamConnAttempt{At: time.Now()})
			}
			handle.Complete(nil, "subscription_interrupted")
			requireRetryObservation(t, waitRetryTerminal(t, tracker, handle.requestID), 1)
			if len(timingEvents(bus.published, requestChangeUpstreamRetry)) != 1 {
				t.Fatal("订阅HTTP缺少重试事件")
			}
			submission := &subscriptionSubmission{RequestID: "retry-ws", StartedAt: time.Now(), Generate: true}
			observer.SubmissionStarted(submission)
			observer.SubmissionOutbound(submission, 10, time.Now())
			observer.SubmissionInterrupted(submission, "subscription_interrupted")
			data := waitRetryWS(t, tracker, submission.RequestID)
			if data.UpstreamFirstDispatchMs != nil || data.Detail.RetryCount != 0 {
				t.Fatal("WS不能制造联系上游计数")
			}
		})
	}
}

func waitRetryWS(t *testing.T, tracker *tracking.UsageTracker, id string) *tracking.RequestLifecycleData {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		data, err := tracker.GetRequestLifecycleData(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if data != nil && data.Detail.Status == "failed" {
			return data
		}
		if time.Now().After(deadline) {
			t.Fatal("等待WS结算超时")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestUpstreamRetry_TransportLogLevels(t *testing.T) {
	var logs retryLogBuffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	for _, elapsed := range []time.Duration{time.Millisecond, time.Second} {
		logTransportRetry("request", "upstream", 2, handlers.UpstreamConnAttempt{
			TransportRetry: true, Previous: handlers.UpstreamAttemptSummary{Elapsed: elapsed, GotConn: true, Reused: true, WasIdle: true, IdleTime: time.Millisecond, WroteHeaders: true, WroteRequest: true},
		})
	}
	for _, part := range []string{"level=INFO", "level=WARN", "prev_reused=true", "prev_was_idle=true", "prev_wrote_request=true"} {
		if !strings.Contains(logs.String(), part) {
			t.Fatalf("缺少日志摘要 %s: %s", part, logs.String())
		}
	}
}

func TestUpstreamRetry_OAuthLocalFailureDoesNotCount(t *testing.T) {
	var authHits atomic.Int32
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		authHits.Add(1)
		http.Error(w, "invalid_grant", 401)
	}))
	t.Cleanup(auth.Close)
	old := accountauth.CurrentOpenAIRefreshTokenURLForTest()
	accountauth.SetOpenAIRefreshTokenURLForTest(auth.URL)
	t.Cleanup(func() { accountauth.SetOpenAIRefreshTokenURLForTest(old) })
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"response","status":"completed","output":[]}`))
	}))
	t.Cleanup(upstream.Close)
	service := &mockAccountPoolService{accounts: []*store.UpstreamAccountRecord{
		{ID: 1, AccountName: "local-failure", ProviderType: "chatgpt_refresh_token", CredentialRaw: `{"refresh_token":"fixture-refresh","chatgpt_account_id":"fixture"}`, BaseURL: upstream.URL, Enabled: true},
		{ID: 2, AccountName: "backup", ProviderType: "api_key", CredentialRaw: "fixture", BaseURL: upstream.URL, Enabled: true},
	}}
	handler := newAccountPipelineTestHandler(t, upstream.URL, service)
	t.Cleanup(handler.endpointManager.Stop)
	tracker := attachRetryTracker(t, handler)
	if rec := performRetryResponsesRequest(t, handler); rec.Code != http.StatusOK {
		t.Fatalf("换号失败: %d %s", rec.Code, rec.Body.String())
	}
	requireOnlyRetryObservation(t, tracker, 0)
	if authHits.Load() != 1 {
		t.Fatalf("未触发预期的本地OAuth刷新失败: auth_requests=%d", authHits.Load())
	}
}

func TestUpstreamRetry_AdmissionSkipDoesNotCount(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(timingClaudeResponse))
	}))
	t.Cleanup(upstream.Close)
	h, manager := newEndpointPipelineTestHandler(t,
		endpointPipelineConfig(temporaryVenlacyEndpointName, upstream.URL, 1),
		endpointPipelineConfig("backup", upstream.URL, 2))
	tracker := attachRetryTracker(t, h)
	h.endpointDispatchPacer = newEndpointDispatchPacer(time.Millisecond)
	<-h.endpointDispatchPacer.turn // 阻住发送，让计划冻结后再改变 admission。
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-test","messages":[]}`))
	req = req.WithContext(context.WithValue(ctx, "conn_id", "retry-admission"))
	rec, done := httptest.NewRecorder(), make(chan struct{})
	go func() { defer close(done); h.ServeHTTP(rec, req) }()
	t.Cleanup(func() { cancel(); <-done })
	waitRetrySchedule(t, manager)
	manager.SetPendingAvailabilityGate(temporaryVenlacyEndpointName, true)
	h.endpointDispatchPacer.turn <- struct{}{}
	<-done
	if rec.Code != http.StatusOK {
		t.Fatalf("备选失败: %d %s", rec.Code, rec.Body.String())
	}
	requireOnlyRetryObservation(t, tracker, 0)
	snapshot := manager.GetLatestEndpointScheduleSnapshot()
	if snapshot.Decisions[0].RuntimeError != "pending_disable_gate" {
		t.Fatalf("没有走 admission 跳过: %+v", snapshot)
	}
}

func waitRetrySchedule(t *testing.T, manager *endpoint.Manager) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for manager.GetLatestEndpointScheduleSnapshot() == nil {
		if time.Now().After(deadline) {
			t.Fatal("等待冻结计划超时")
		}
		time.Sleep(time.Millisecond)
	}
}
