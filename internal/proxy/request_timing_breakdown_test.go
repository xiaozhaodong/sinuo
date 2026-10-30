package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"sinuo/internal/accountauth"
	"sinuo/internal/middleware"
	"sinuo/internal/proxy/handlers"
	"sinuo/internal/store"
	"sinuo/internal/tracking"
)

func timingLifecycleData(t *testing.T, tracker *tracking.UsageTracker, id string) *tracking.RequestLifecycleData {
	t.Helper()
	data, err := tracker.GetRequestLifecycleData(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if data == nil {
		t.Fatalf("缺少生命周期详情：%s", id)
	}
	return data
}

// requireHTTPConnectionTiming 调度型与直通 HTTP 链路都必须记录单调的取连接、连接就绪与写完偏移。
func requireHTTPConnectionTiming(t *testing.T, data *tracking.RequestLifecycleData) {
	t.Helper()
	if data.UpstreamConnMs == nil || data.UpstreamWriteMs == nil || *data.UpstreamConnMs < 0 || *data.UpstreamConnMs > *data.UpstreamWriteMs {
		t.Fatalf("HTTP 连接与写完偏移缺失或不单调：%+v", data)
	}
	if data.UpstreamDispatchMs == nil || *data.UpstreamDispatchMs < 0 || *data.UpstreamDispatchMs > *data.UpstreamConnMs {
		t.Fatalf("必须记录取连接偏移（本地处理段终点）：%+v", data)
	}
}

func TestRequestTiming_LifecycleFinalAttemptAndFirstTokenFreeze(t *testing.T) {
	tracker := newLifecyclePanelTestTracker(t)
	const id = "timing-lifecycle"
	rlm := NewRequestLifecycleManager(tracker, nil, id, nil)
	rlm.StartRequest("127.0.0.1", "agent", "POST", "/v1/messages", true)
	rlm.startTime = time.Now().Add(-2 * time.Second)
	rlm.RecordUpstreamWrite(handlers.UpstreamWriteTiming{
		ConnStartAt: rlm.startTime.Add(100 * time.Millisecond),
		ConnReadyAt: rlm.startTime.Add(200 * time.Millisecond),
		WroteAt:     rlm.startTime.Add(300 * time.Millisecond),
	})
	latest := handlers.UpstreamWriteTiming{
		ConnStartAt: rlm.startTime.Add(1600 * time.Millisecond),
		ConnReadyAt: rlm.startTime.Add(1700 * time.Millisecond),
		WroteAt:     time.Now().Add(-50 * time.Millisecond),
	}
	rlm.RecordUpstreamWrite(latest)
	data := timingLifecycleData(t, tracker, id)
	requireHTTPConnectionTiming(t, data)
	wantWrite := latest.WroteAt.Sub(rlm.startTime).Milliseconds()
	if *data.UpstreamDispatchMs != 1600 || *data.UpstreamConnMs != 1700 || *data.UpstreamWriteMs != wantWrite {
		t.Fatalf("应同时覆盖最终尝试的三个偏移：%+v", data)
	}
	if !rlm.firstTokenStartTime.Equal(latest.WroteAt) {
		t.Fatal("首字计时起点必须是最终尝试写完时刻")
	}
	rlm.RecordFirstToken()
	rlm.RecordUpstreamWrite(handlers.UpstreamWriteTiming{ConnStartAt: time.Now(), ConnReadyAt: time.Now(), WroteAt: time.Now()})
	data = timingLifecycleData(t, tracker, id)
	if *data.UpstreamDispatchMs != 1600 || *data.UpstreamConnMs != 1700 || *data.UpstreamWriteMs != wantWrite {
		t.Fatal("首响后回调不得覆盖计时")
	}
	if data.Detail.FirstTokenMs == nil || *data.Detail.FirstTokenMs >= 500 || !rlm.firstTokenStartTime.Equal(latest.WroteAt) {
		t.Fatal("首字计时必须继续使用最终尝试写完时刻")
	}
}

func TestRequestTiming_LifecycleZeroMissingAndNegativeTimes(t *testing.T) {
	tracker := newLifecyclePanelTestTracker(t)
	const id = "timing-boundary"
	rlm := NewRequestLifecycleManager(tracker, nil, id, nil)
	rlm.StartRequest("127.0.0.1", "agent", "POST", "/v1/messages", true)
	rlm.RecordUpstreamWrite(handlers.UpstreamWriteTiming{ConnStartAt: time.Now(), ConnReadyAt: time.Now()})
	data := timingLifecycleData(t, tracker, id)
	if data.UpstreamWriteMs != nil || data.UpstreamConnMs != nil || data.UpstreamDispatchMs != nil {
		t.Fatal("WroteAt 为零必须忽略整个回调")
	}
	rlm.RecordUpstreamWrite(handlers.UpstreamWriteTiming{WroteAt: rlm.startTime.Add(-time.Second)})
	data = timingLifecycleData(t, tracker, id)
	if data.UpstreamWriteMs == nil || *data.UpstreamWriteMs != 0 || data.UpstreamConnMs != nil || data.UpstreamDispatchMs != nil {
		t.Fatal("缺失连接时刻不更新对应列，负写完偏移钳为零")
	}
	before := rlm.startTime.Add(-time.Second)
	rlm.RecordUpstreamWrite(handlers.UpstreamWriteTiming{ConnStartAt: before, ConnReadyAt: before, WroteAt: before})
	data = timingLifecycleData(t, tracker, id)
	if *data.UpstreamDispatchMs != 0 || *data.UpstreamConnMs != 0 || *data.UpstreamWriteMs != 0 {
		t.Fatal("三个负偏移都必须钳为零")
	}
}

const timingClaudeResponse = `{"id":"msg_timing","type":"message","role":"assistant","model":"claude-test","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`

func TestRequestTiming_EndpointAndClaudeSubscriptionHTTP(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, timingClaudeResponse)
	}))
	defer upstream.Close()
	t.Run("endpoint", func(t *testing.T) {
		handler, manager := newEndpointPipelineTestHandler(t, endpointPipelineConfig("timing-endpoint", upstream.URL, 1))
		tracker := newLifecyclePanelTestTracker(t)
		handler.SetUsageTracker(tracker)
		handler.SetMonitoringMiddleware(middleware.NewMonitoringMiddleware(manager))
		if rec := performEndpointPipelineRequest(t, handler, false); rec.Code != http.StatusOK {
			t.Fatalf("端点请求失败：%d %s", rec.Code, rec.Body.String())
		}
		requireHTTPConnectionTiming(t, timingLifecycleData(t, tracker, "req-endpoint-pipeline"))
		requireOnlyRetryObservation(t, tracker, 0)
	})
	t.Run("claude_subscription", func(t *testing.T) {
		handler, _, tracker := newClaudeSubscriptionTestHandler(t, upstream.URL)
		if rec := performClaudeSubscriptionRequest(t, handler, claudeSubscriptionRequest{}); rec.Code != http.StatusOK {
			t.Fatalf("订阅请求失败：%d %s", rec.Code, rec.Body.String())
		}
		requireHTTPConnectionTiming(t, timingLifecycleData(t, tracker, "req-claude-subscription"))
		requireOnlyRetryObservation(t, tracker, 0)
	})
}

func TestRequestTiming_AccountPoolResponsesCompactAndSearch(t *testing.T) {
	for _, path := range []string{"/v1/responses", "/v1/responses/compact", codexStandaloneSearchPath} {
		t.Run(path, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"object":"response","model":"gpt-4.1","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)
			}))
			defer upstream.Close()
			account := &store.UpstreamAccountRecord{ID: 1, AccountName: "timing-account", ProviderType: accountauth.ProviderAPIKey, CredentialRaw: "synthetic-key", BaseURL: upstream.URL, Enabled: true}
			if path == codexStandaloneSearchPath {
				account.ProviderType = accountauth.ProviderChatGPTRefreshToken
				account.CredentialRaw = oauthCredentialJSON(t, "synthetic-access", "synthetic-account")
			}
			handler := newAccountPipelineTestHandler(t, upstream.URL, &mockAccountPoolService{accounts: []*store.UpstreamAccountRecord{account}})
			t.Cleanup(handler.endpointManager.Stop)
			tracker := newLifecyclePanelTestTracker(t)
			handler.SetUsageTracker(tracker)
			handler.SetMonitoringMiddleware(middleware.NewMonitoringMiddleware(handler.endpointManager))
			// OAuth search URL 固定；只在测试 transport 中指向本地服务器，保留真实 HTTP trace。
			target, err := url.Parse(upstream.URL)
			if err != nil {
				t.Fatal(err)
			}
			handler.accountHTTPInitOnce.Do(func() {})
			testTransport := &http.Transport{}
			t.Cleanup(testTransport.CloseIdleConnections)
			handler.accountHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				copy := r.Clone(r.Context())
				copy.URL.Scheme, copy.URL.Host = target.Scheme, target.Host
				return testTransport.RoundTrip(copy)
			})}
			handler.accountSSEHTTPClient = handler.accountHTTPClient
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"gpt-4.1","input":[]}`))
			req = req.WithContext(context.WithValue(req.Context(), "conn_id", "timing-account"))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("账号池请求失败：%d %s", rec.Code, rec.Body.String())
			}
			requireHTTPConnectionTiming(t, timingLifecycleData(t, tracker, "timing-account"))
			requireOnlyRetryObservation(t, tracker, 0)
		})
	}
}

func TestRequestTiming_ImageHTTPIsDirect(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"created":1,"data":[{"b64_json":"aW1hZ2U="}]}`)
	}))
	defer upstream.Close()
	handler, manager := newEndpointPipelineTestHandler(t)
	tracker := newLifecyclePanelTestTracker(t)
	handler.SetUsageTracker(tracker)
	handler.SetMonitoringMiddleware(middleware.NewMonitoringMiddleware(manager))
	handler.SetImageGenerationConfigProvider(staticImageGenerationConfigProvider{config: ImageGenerationConfig{
		Enabled: true, EndpointURL: upstream.URL + openAIImagesGenerationsPath, APIKey: "synthetic-key", Model: "gpt-image-2", Timeout: time.Second,
	}})
	req := httptest.NewRequest(http.MethodPost, openAIImagesGenerationsPath, strings.NewReader(`{"prompt":"test"}`))
	req = req.WithContext(context.WithValue(req.Context(), "conn_id", "timing-image"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("图像请求失败：%d %s", rec.Code, rec.Body.String())
	}
	requireHTTPConnectionTiming(t, timingLifecycleData(t, tracker, "timing-image"))
	requireOnlyRetryObservation(t, tracker, 0)
}

func TestRequestTiming_CodexSubscriptionHTTPIsDirect(t *testing.T) {
	tracker := newLifecyclePanelTestTracker(t)
	env := newSubscriptionTestEnv(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"response","model":"synthetic","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)
	}, "subscription")
	env.handler.proxy.SetUsageTracker(tracker)
	resp := env.request(http.MethodPost, "responses", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer synthetic-client")
		r.Header.Set("Content-Type", "application/json")
		r.Body = io.NopCloser(strings.NewReader(`{"model":"synthetic","input":[]}`))
	})
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Codex 订阅状态 = %d", resp.StatusCode)
	}
	rows, _, err := tracker.QueryRequestDetailsWithHotPool(context.Background(), &tracking.QueryOptions{Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("订阅详情 = %v，err = %v", rows, err)
	}
	requireHTTPConnectionTiming(t, timingLifecycleData(t, tracker, rows[0].RequestID))
	requireOnlyRetryObservation(t, tracker, 0)
}

// TestRequestTiming_SubscriptionTrackingDatabasePath 热池关闭时，Codex 订阅的三列经事件队列落库，
// 最终尝试覆盖、首响后冻结与热池路径一致。
func TestRequestTiming_SubscriptionTrackingDatabasePath(t *testing.T) {
	tracker := newTimingTestTracker(t, false)
	observer := newSubscriptionTrackingObserver(tracker, nil)
	started := time.Now().Add(-time.Second)
	const id = "timing-http-database"
	handle := observer.StartHTTP(SubscriptionHTTPStart{RequestID: id, StartedAt: started, Method: "POST", Path: "/v1/responses", RequestKind: tracking.RequestKindGeneration})
	attempt := func(dispatch, conn, wrote time.Duration) handlers.UpstreamWriteTiming {
		return handlers.UpstreamWriteTiming{ConnStartAt: started.Add(dispatch), ConnReadyAt: started.Add(conn), WroteAt: started.Add(wrote)}
	}
	handle.MarkOutbound(10, attempt(5*time.Millisecond, 10*time.Millisecond, 20*time.Millisecond))
	handle.MarkOutbound(20, attempt(80*time.Millisecond, 100*time.Millisecond, 200*time.Millisecond))
	handle.MarkFirstOutput(10, started.Add(250*time.Millisecond))
	handle.MarkOutbound(30, attempt(280*time.Millisecond, 300*time.Millisecond, 400*time.Millisecond))

	deadline := time.Now().Add(2 * time.Second)
	for {
		data, err := tracker.GetRequestLifecycleData(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if data != nil && data.Detail.FirstTokenMs != nil {
			if data.Source != "database" {
				t.Fatalf("source = %s，want database", data.Source)
			}
			requireHTTPConnectionTiming(t, data)
			if *data.UpstreamDispatchMs != 80 || *data.UpstreamConnMs != 100 || *data.UpstreamWriteMs != 200 || *data.Detail.FirstTokenMs != 50 {
				t.Fatalf("直写路径应保留首响前最终尝试：%+v", data)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("等待事件队列落库超时")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRequestTiming_SubscriptionTrackingRetryAndWebSocket(t *testing.T) {
	tracker := newLifecyclePanelTestTracker(t)
	observer := newSubscriptionTrackingObserver(tracker, nil)
	started := time.Now().Add(-time.Second)
	handle := observer.StartHTTP(SubscriptionHTTPStart{RequestID: "timing-http-retry", StartedAt: started, Method: "POST", Path: "/v1/responses", RequestKind: tracking.RequestKindGeneration})
	attempt := func(dispatch, conn, wrote time.Duration) handlers.UpstreamWriteTiming {
		return handlers.UpstreamWriteTiming{ConnStartAt: started.Add(dispatch), ConnReadyAt: started.Add(conn), WroteAt: started.Add(wrote)}
	}
	handle.MarkOutbound(10, attempt(5*time.Millisecond, 10*time.Millisecond, 20*time.Millisecond))
	handle.MarkOutbound(20, attempt(80*time.Millisecond, 100*time.Millisecond, 200*time.Millisecond))
	handle.MarkFirstOutput(10, started.Add(250*time.Millisecond))
	handle.MarkOutbound(30, attempt(280*time.Millisecond, 300*time.Millisecond, 400*time.Millisecond))
	data := timingLifecycleData(t, tracker, "timing-http-retry")
	requireHTTPConnectionTiming(t, data)
	if *data.UpstreamDispatchMs != 80 || *data.UpstreamConnMs != 100 || *data.UpstreamWriteMs != 200 || data.Detail.FirstTokenMs == nil || *data.Detail.FirstTokenMs != 50 {
		t.Fatal("订阅 HTTP 重试应覆盖最终尝试并在首响后冻结")
	}
	submission := &subscriptionSubmission{RequestID: "timing-ws", ConnectionID: "ws", StartedAt: started, Generate: true}
	observer.SubmissionStarted(submission)
	observer.SubmissionOutbound(submission, 20, started.Add(100*time.Millisecond))
	data = timingLifecycleData(t, tracker, "timing-ws")
	if data.UpstreamDispatchMs != nil || data.UpstreamConnMs != nil || data.UpstreamWriteMs == nil || *data.UpstreamWriteMs != 100 {
		t.Fatal("WS 只能记录写完偏移，不能制造本地处理或建连阶段")
	}
}

// slowFirstReadBody 首次读取前阻塞，模拟客户端上传或大正文读取耗时。
type slowFirstReadBody struct {
	delay  time.Duration
	once   sync.Once
	reader io.Reader
}

func (b *slowFirstReadBody) Read(p []byte) (int, error) {
	b.once.Do(func() { time.Sleep(b.delay) })
	return b.reader.Read(p)
}

// TestRequestTiming_CodexPoolModeStartsBeforeBodyRead 池模式在进入 Handler 前读取正文，
// 请求开始时刻必须取适配器收到请求的时刻，读取耗时计入本地处理而不是被漏掉。
func TestRequestTiming_CodexPoolModeStartsBeforeBodyRead(t *testing.T) {
	const readDelay = 50 * time.Millisecond
	env := newSubscriptionPoolTestEnv(t, []*store.UpstreamAccountRecord{
		{ID: 31, AccountName: "pool-timing", ProviderType: accountauth.ProviderAPIKey, CredentialRaw: "sk-pool-timing", BaseURL: "http://upstream.invalid", Enabled: true},
	}, true)
	tracker := newLifecyclePanelTestTracker(t)
	env.handler.proxy.SetUsageTracker(tracker)
	before := time.Now()
	rec := env.serve(t, http.MethodPost, "responses", func(r *http.Request) {
		r.Body = io.NopCloser(&slowFirstReadBody{delay: readDelay, reader: strings.NewReader(`{"model":"gpt-5","input":"hello"}`)})
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("池模式请求失败：%d %s", rec.Code, rec.Body.String())
	}
	rows, _, err := tracker.QueryRequestDetailsWithHotPool(context.Background(), &tracking.QueryOptions{Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("池模式详情 = %v，err = %v", rows, err)
	}
	startedAt := timingLifecycleData(t, tracker, rows[0].RequestID).Detail.StartTime
	if startedAt.Before(before.Add(-time.Millisecond)) || !startedAt.Before(before.Add(readDelay)) {
		t.Fatalf("开始时刻 = %s，应在收到请求时（%s 起 %s 内），正文读取耗时不得漏计", startedAt, before, readDelay)
	}
}
