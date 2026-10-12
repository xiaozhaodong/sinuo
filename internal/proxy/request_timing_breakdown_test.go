package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"cc-forwarder/internal/accountauth"
	"cc-forwarder/internal/middleware"
	"cc-forwarder/internal/proxy/handlers"
	"cc-forwarder/internal/store"
	"cc-forwarder/internal/tracking"
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

func requireHTTPConnectionTiming(t *testing.T, data *tracking.RequestLifecycleData, scheduled bool) {
	t.Helper()
	if data.UpstreamConnMs == nil || data.UpstreamWriteMs == nil || *data.UpstreamConnMs < 0 || *data.UpstreamConnMs > *data.UpstreamWriteMs {
		t.Fatalf("HTTP 连接与写完偏移缺失或不单调：%+v", data)
	}
	if scheduled {
		if data.UpstreamDispatchMs == nil || *data.UpstreamDispatchMs < 0 || *data.UpstreamDispatchMs > *data.UpstreamConnMs {
			t.Fatalf("调度型请求必须记录取连接偏移：%+v", data)
		}
	} else if data.UpstreamDispatchMs != nil {
		t.Fatalf("直通请求不得记录排队偏移：%d", *data.UpstreamDispatchMs)
	}
}

func TestRequestTiming_LifecycleFinalAttemptAndFirstTokenFreeze(t *testing.T) {
	for _, direct := range []bool{false, true} {
		t.Run(map[bool]string{true: "direct", false: "scheduled"}[direct], func(t *testing.T) {
			tracker := newLifecyclePanelTestTracker(t)
			const id = "timing-lifecycle"
			rlm := NewRequestLifecycleManager(tracker, nil, id, nil)
			rlm.StartRequest("127.0.0.1", "agent", "POST", "/v1/messages", true)
			rlm.startTime = time.Now().Add(-2 * time.Second)
			record := rlm.RecordUpstreamWrite
			if direct {
				record = rlm.RecordDirectUpstreamWrite
			}
			record(handlers.UpstreamWriteTiming{
				ConnStartAt: rlm.startTime.Add(100 * time.Millisecond),
				ConnReadyAt: rlm.startTime.Add(200 * time.Millisecond),
				WroteAt:     rlm.startTime.Add(300 * time.Millisecond),
			})
			latest := handlers.UpstreamWriteTiming{
				ConnStartAt: rlm.startTime.Add(1600 * time.Millisecond),
				ConnReadyAt: rlm.startTime.Add(1700 * time.Millisecond),
				WroteAt:     time.Now().Add(-50 * time.Millisecond),
			}
			record(latest)
			data := timingLifecycleData(t, tracker, id)
			requireHTTPConnectionTiming(t, data, !direct)
			wantWrite := latest.WroteAt.Sub(rlm.startTime).Milliseconds()
			if *data.UpstreamConnMs != 1700 || *data.UpstreamWriteMs != wantWrite || (!direct && *data.UpstreamDispatchMs != 1600) {
				t.Fatalf("应同时覆盖最终尝试的三个偏移：%+v", data)
			}
			if !rlm.firstTokenStartTime.Equal(latest.WroteAt) {
				t.Fatal("首字计时起点必须是最终尝试写完时刻")
			}
			rlm.RecordFirstToken()
			record(handlers.UpstreamWriteTiming{ConnStartAt: time.Now(), ConnReadyAt: time.Now(), WroteAt: time.Now()})
			data = timingLifecycleData(t, tracker, id)
			if *data.UpstreamConnMs != 1700 || *data.UpstreamWriteMs != wantWrite || (!direct && *data.UpstreamDispatchMs != 1600) {
				t.Fatal("首响后回调不得覆盖计时")
			}
			if data.Detail.FirstTokenMs == nil || *data.Detail.FirstTokenMs >= 500 || !rlm.firstTokenStartTime.Equal(latest.WroteAt) {
				t.Fatal("首字计时必须继续使用最终尝试写完时刻")
			}
		})
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
		requireHTTPConnectionTiming(t, timingLifecycleData(t, tracker, "req-endpoint-pipeline"), true)
	})
	t.Run("claude_subscription", func(t *testing.T) {
		handler, _, tracker := newClaudeSubscriptionTestHandler(t, upstream.URL)
		if rec := performClaudeSubscriptionRequest(t, handler, claudeSubscriptionRequest{}); rec.Code != http.StatusOK {
			t.Fatalf("订阅请求失败：%d %s", rec.Code, rec.Body.String())
		}
		requireHTTPConnectionTiming(t, timingLifecycleData(t, tracker, "req-claude-subscription"), false)
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
			requireHTTPConnectionTiming(t, timingLifecycleData(t, tracker, "timing-account"), true)
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
	requireHTTPConnectionTiming(t, timingLifecycleData(t, tracker, "timing-image"), false)
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
	requireHTTPConnectionTiming(t, timingLifecycleData(t, tracker, rows[0].RequestID), false)
}

func TestRequestTiming_SubscriptionTrackingRetryAndWebSocket(t *testing.T) {
	tracker := newLifecyclePanelTestTracker(t)
	observer := newSubscriptionTrackingObserver(tracker, nil)
	started := time.Now().Add(-time.Second)
	handle := observer.StartHTTP(SubscriptionHTTPStart{RequestID: "timing-http-retry", StartedAt: started, Method: "POST", Path: "/v1/responses", RequestKind: tracking.RequestKindGeneration})
	handle.MarkOutbound(10, started.Add(10*time.Millisecond), started.Add(20*time.Millisecond))
	handle.MarkOutbound(20, started.Add(100*time.Millisecond), started.Add(200*time.Millisecond))
	handle.MarkFirstOutput(10, started.Add(250*time.Millisecond))
	handle.MarkOutbound(30, started.Add(300*time.Millisecond), started.Add(400*time.Millisecond))
	data := timingLifecycleData(t, tracker, "timing-http-retry")
	requireHTTPConnectionTiming(t, data, false)
	if *data.UpstreamConnMs != 100 || *data.UpstreamWriteMs != 200 || data.Detail.FirstTokenMs == nil || *data.Detail.FirstTokenMs != 50 {
		t.Fatal("订阅 HTTP 重试应覆盖最终尝试并在首响后冻结")
	}
	submission := &subscriptionSubmission{RequestID: "timing-ws", ConnectionID: "ws", StartedAt: started, Generate: true}
	observer.SubmissionStarted(submission)
	observer.SubmissionOutbound(submission, 20, started.Add(100*time.Millisecond))
	data = timingLifecycleData(t, tracker, "timing-ws")
	if data.UpstreamDispatchMs != nil || data.UpstreamConnMs != nil || data.UpstreamWriteMs == nil || *data.UpstreamWriteMs != 100 {
		t.Fatal("WS 只能记录写完偏移，不能制造连接或排队阶段")
	}
}
