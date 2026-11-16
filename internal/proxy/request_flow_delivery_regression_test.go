package proxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"sinuo/internal/events"
	"sinuo/internal/tracking"
)

func stalledSubscriptionTracker(t *testing.T) (*tracking.UsageTracker, func()) {
	t.Helper()
	tr := fullFlowHotPool(t)
	wait, release := lockFlowWriter(t, tr)
	tr.RecordRequestStart("busy", "", "", "POST", "/v1/responses", true)
	wait()
	for i := 0; i < 10; i++ {
		tr.RecordRequestUpdate("busy", tracking.UpdateOptions{RetryCount: &i})
	}
	return tr, release
}

func dialFlowTestClient(t *testing.T, ctx context.Context, serverURL string) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(serverURL, "http") + "/subscription/" + subscriptionTestSecret + "/backend-api/codex/responses"
	client, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer synthetic"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.CloseNow() })
	return client
}

func TestRequestFlow_WSForwardsWhenRegistrationFull(t *testing.T) {
	const create = `{"type":"response.create","input":"hello"}`
	const follow = `{"type":"response.interrupt","response_id":"r"}`
	const created = `{"type":"response.created","response":{"id":"r"}}`
	const completed = `{"type":"response.completed","response":{"id":"r","status":"completed","usage":{"output_tokens":3}}}`
	for _, successor := range []bool{false, true} {
		t.Run(fmt.Sprintf("server_successor=%t", successor), func(t *testing.T) {
			tr, release := stalledSubscriptionTracker(t)
			env := newSubscriptionTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer conn.CloseNow()
				if !successor {
					for _, want := range []string{create, follow} {
						_, data, err := conn.Read(r.Context())
						if err != nil || string(data) != want {
							t.Errorf("上游收到 %q，错误 %v", data, err)
							return
						}
					}
				}
				for _, frame := range []string{created, completed} {
					if err := conn.Write(r.Context(), websocket.MessageText, []byte(frame)); err != nil {
						return
					}
				}
				_ = conn.Close(websocket.StatusNormalClosure, "complete")
			}, "subscription")
			defer release()
			env.handler.proxy.SetUsageTracker(tr)
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			client := dialFlowTestClient(t, ctx, env.server.URL)
			if !successor {
				for _, frame := range []string{create, follow} {
					if err := client.Write(ctx, websocket.MessageText, []byte(frame)); err != nil {
						t.Fatal(err)
					}
				}
			}
			for _, want := range []string{created, completed} {
				_, data, err := client.Read(ctx)
				if err != nil || string(data) != want {
					t.Fatalf("客户端收到 %q，错误 %v", data, err)
				}
			}
			_, _, err := client.Read(ctx)
			if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
				t.Fatalf("WS 关闭：%v", err)
			}
			deadline := time.Now().Add(2 * time.Second)
			for env.handler.wsRegistry.count() != 0 || env.handler.activity.Count() != 0 {
				if time.Now().After(deadline) {
					t.Fatal("连接关闭后仍有活动请求")
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}

func TestRequestFlow_HTTPForwardsWhenRegistrationFull(t *testing.T) {
	const body = `{"id":"r","type":"message","object":"response","status":"completed","output":[],"usage":{"output_tokens":3}}`
	for _, route := range []string{"endpoint", "account", "subscription"} {
		t.Run(route, func(t *testing.T) {
			tr, release := stalledSubscriptionTracker(t)
			defer release()
			var calls atomic.Int32
			upstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, body)
			})
			var run func() *httptest.ResponseRecorder
			switch route {
			case "subscription":
				env := newSubscriptionTestEnv(t, upstream, "subscription")
				env.handler.proxy.SetUsageTracker(tr)
				run = func() *httptest.ResponseRecorder {
					rec := httptest.NewRecorder()
					r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":[]}`))
					r.Header.Set("Authorization", "Bearer test")
					target, _ := resolveSubscriptionRoute(r.Method, "responses", false)
					env.handler.passthrough.serve(rec, r, target, "saturated")
					return rec
				}
			case "account":
				up := httptest.NewServer(upstream)
				defer up.Close()
				h, _ := phaseAccountHandler(t, up.URL, 1)
				h.SetUsageTracker(tr)
				run = func() *httptest.ResponseRecorder { return performPhaseAccountRequest(h, false) }
			case "endpoint":
				up := httptest.NewServer(upstream)
				defer up.Close()
				h, _ := newEndpointPipelineTestHandler(t, endpointPipelineConfig("test", up.URL, 1))
				h.SetUsageTracker(tr)
				run = func() *httptest.ResponseRecorder {
					return performTrackedClaudeRequest(t, h, "saturated", `{"model":"claude-test","messages":[]}`, false)
				}
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- run() }()
			select {
			case rec := <-done:
				if calls.Load() != 1 || rec.Code != 200 || rec.Body.String() != body {
					t.Fatalf("上游调用 %d，响应 %d %q", calls.Load(), rec.Code, rec.Body.String())
				}
			case <-time.After(3 * time.Second):
				release()
				<-done
				t.Fatal("登记队列已满时转发仍等待 SQLite")
			}
		})
	}
}

func TestRequestFlow_CancelWhileReadingAccountJSON(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"output":`)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer up.Close()
	h, tr := phaseAccountHandler(t, up.URL, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const id = "cancel-during-json-read"
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"gpt-5.5","input":[]}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(ctx, "conn_id", id))
	done := make(chan struct{})
	go func() { defer close(done); h.ServeHTTP(httptest.NewRecorder(), req) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		data, err := tr.GetRequestLifecycleData(ctx, id)
		if err == nil && data != nil && data.Flow != nil && data.Detail.Status == "processing" && len(data.Flow.Contacts) == 1 && data.Flow.Contacts[0].FirstUpstreamByteOffsetMs != nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			waitForFlowSignal(t, done)
			t.Fatal("请求未开始读取上游正文")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	waitForFlowSignal(t, done)
	data := timingLifecycleData(t, tr, id)
	if data.Detail.Status != "cancelled" || data.Flow.UpstreamTerminalObserved || data.Flow.DownstreamDeliveryOutcome != tracking.FlowDeliveryAborted || data.Flow.DownstreamBytesWritten != 0 {
		t.Fatalf("取消后的结算：%s，路径 %+v", data.Detail.Status, data.Flow)
	}
}

func TestRequestFlow_EmptyAccountResponseThenFailover(t *testing.T) {
	const body = `{"id":"r","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":7}}`
	for _, count := range []int{1, 2} {
		t.Run(fmt.Sprintf("%d 个账号", count), func(t *testing.T) {
			var calls atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if calls.Add(1) > 1 {
					_, _ = io.WriteString(w, body)
				}
			}))
			defer up.Close()
			h, tr := phaseAccountHandler(t, up.URL, count)
			w := performPhaseAccountRequest(h, false)
			data := timingLifecycleData(t, tr, "synthetic-phase-account")
			if count == 1 {
				if w.Code != http.StatusServiceUnavailable || calls.Load() != 1 || data.Detail.Status != "failed" || data.Flow == nil || len(data.Flow.Contacts) != 1 || data.Flow.Contacts[0].OutcomeCode != FailoverReasonEmptyResponse {
					t.Fatalf("没有候选账号时未保留空响应原因：HTTP %d，状态 %s，路径 %+v", w.Code, data.Detail.Status, data.Flow)
				}
				return
			}
			if w.Code != 200 || w.Body.String() != body || calls.Load() != 2 || data.Detail.Status != "completed" || data.Detail.OutputTokens != 7 {
				t.Fatalf("换号交付：HTTP=%d，调用=%d，状态=%s，tokens=%d", w.Code, calls.Load(), data.Detail.Status, data.Detail.OutputTokens)
			}
			f := data.Flow
			if f == nil || len(f.Legs) != 2 || len(f.Contacts) != 2 || !f.UpstreamTerminalObserved || f.DownstreamDeliveryOutcome != tracking.FlowDeliveryDelivered || f.DownstreamBytesWritten != int64(len(body)) {
				t.Fatalf("换号后的路径和实际写出不一致：%+v", f)
			}
			if f.Contacts[0].LegSeq == f.Contacts[1].LegSeq || f.Contacts[0].EndOffsetMs == nil || f.Contacts[0].OutcomeCode != FailoverReasonEmptyResponse {
				t.Fatalf("两次尝试未分别结算：%+v", f.Contacts)
			}
		})
	}
}

func TestRequestFlow_TruncatedAccountResponse(t *testing.T) {
	const success = `{"id":"r","object":"response","status":"completed","output":[],"usage":{"output_tokens":7}}`
	for _, count := range []int{1, 2} {
		t.Run(fmt.Sprintf("%d 个账号", count), func(t *testing.T) {
			var calls atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if calls.Add(1) == 1 {
					w.Header().Set("Content-Length", "100")
					_, _ = io.WriteString(w, "{")
					return
				}
				_, _ = io.WriteString(w, success)
			}))
			defer up.Close()
			h, tr := phaseAccountHandler(t, up.URL, count)
			client := performPhaseAccountRequest(h, false)
			row := timingLifecycleData(t, tr, "synthetic-phase-account").Detail
			if calls.Load() != int32(count) || row.HTTPStatusCode == nil || *row.HTTPStatusCode != client.Code {
				t.Fatalf("尝试数 %d，客户端 HTTP %d，追踪记录 %+v", calls.Load(), client.Code, row)
			}
			if count == 1 {
				if client.Code != http.StatusServiceUnavailable || row.Status != "failed" || !strings.HasPrefix(row.FailureReason, "account_response_read_error:") {
					t.Fatalf("读取失败的最终结算：HTTP %d，状态 %s，原因 %s", client.Code, row.Status, row.FailureReason)
				}
			} else if client.Code != http.StatusOK || client.Body.String() != success || row.Status != "completed" || row.OutputTokens != 7 {
				t.Fatalf("读取失败后换号：HTTP %d，状态 %s，输出 %d", client.Code, row.Status, row.OutputTokens)
			}
		})
	}
}

func TestRequestFlow_StoredDetailWithoutTrackingBroadcasts(t *testing.T) {
	for _, mode := range []string{"hot_pool", "sqlite"} {
		t.Run(mode, func(t *testing.T) {
			tr := newTimingTestTracker(t, mode == "hot_pool")
			var broadcasts atomic.Int32
			h := &Handler{}
			h.SetEventBus(&streamPhaseReviewEventBus{onPublish: func(events.Event) { broadcasts.Add(1) }})
			h.SetUsageTracker(tr)

			const id = "flow-without-broadcast"
			ctx := context.Background()
			err := tr.RecordRequestStartAtWithDimensions(id, "", "", "POST", "/v1/responses", tracking.RequestFamilyCodex, true, time.Now(), tracking.RequestDimensions{
				RequestSource: tracking.RequestSourceCodexSubscriptionProxy, RequestKind: tracking.RequestKindGeneration,
			})
			if err != nil {
				t.Fatal(err)
			}
			tr.RecordRequestUpdate(id, tracking.UpdateOptions{FlowEvents: []tracking.RequestFlowEvent{{Kind: tracking.FlowEventContactStart, ContactSeq: 1}}})
			status := "processing"
			tr.RecordRequestUpdate(id, tracking.UpdateOptions{Status: &status, FlowEvents: []tracking.RequestFlowEvent{{Kind: tracking.FlowEventUpstreamTerminal}}})
			if err := tr.RecordRequestSettlement(id, tracking.RequestSettlement{Status: "completed"}); err != nil {
				t.Fatal(err)
			}

			waitForDetail := func(bytes int64) {
				t.Helper()
				deadline := time.Now().Add(3 * time.Second)
				for {
					data, err := tr.GetRequestLifecycleData(ctx, id)
					if err != nil {
						t.Fatal(err)
					}
					if data != nil && data.Source == "database" && data.Detail.Status == "completed" &&
						data.Flow != nil && data.Flow.TotalContacts == 1 && data.Flow.UpstreamTerminalObserved && data.Flow.DownstreamBytesWritten == bytes {
						return
					}
					if time.Now().After(deadline) {
						t.Fatalf("未读到预期的归档轨迹（下行 %d 字节）：%+v", bytes, data)
					}
					time.Sleep(5 * time.Millisecond)
				}
			}
			waitForDetail(0)
			tr.RecordRequestUpdate(id, tracking.UpdateOptions{FlowEvents: []tracking.RequestFlowEvent{{Kind: tracking.FlowEventDownstreamDone, Bytes: 150}}})
			waitForDetail(150)
			if err := tr.Close(); err != nil {
				t.Fatal(err)
			}
			if got := broadcasts.Load(); got != 0 {
				t.Fatalf("追踪写入产生了 %d 条前端广播", got)
			}
		})
	}
}

func TestRequestFlow_SubscriptionTerminalAndDeliveryKeepOffsets(t *testing.T) {
	tr := newLifecyclePanelTestTracker(t)
	handle := newSubscriptionTrackingObserver(tr, nil).StartHTTP(SubscriptionHTTPStart{
		RequestID: "terminal-lock", Method: "POST", Path: "/v1/responses",
		RequestKind: tracking.RequestKindGeneration, Transport: tracking.FlowTransportWS,
	})
	at := handle.startedAt.Add(time.Millisecond)
	handle.markFlowUpstreamTerminal(at)
	handle.recordFlowWSDownstreamDelivered(42, nil, at)
	handle.settle("completed", "", nil, nil)
	data := timingLifecycleData(t, tr, handle.requestID)
	if data.Detail.Status != "completed" || data.Flow == nil || data.Flow.UpstreamTerminalOffsetMs == nil || *data.Flow.UpstreamTerminalOffsetMs != 1 || data.Flow.DownstreamBytesWritten != 42 {
		t.Fatalf("结算丢失协议终态或 WS 写回：%+v", data)
	}
}

func TestRequestFlow_SubscriptionJSONTruncatedAtValidObject(t *testing.T) {
	const partial = `{"id":"r","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":3}}`
	env := newSubscriptionTestEnv(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "1024")
		_, _ = io.WriteString(w, partial)
	}, "subscription")
	tr := newLifecyclePanelTestTracker(t)
	env.handler.proxy.SetUsageTracker(tr)
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"gpt-test","input":"ok"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer synthetic")
	target, status := resolveSubscriptionRoute(req.Method, "responses", false)
	if target == nil {
		t.Fatalf("缺少订阅路由：HTTP %d", status)
	}
	w := httptest.NewRecorder()
	env.handler.passthrough.serve(w, req, target, "truncated-json")
	d := timingLifecycleData(t, tr, "truncated-json")
	if w.Body.String() != partial || d.Detail.Status != "failed" || d.Detail.UsageKnown || d.Flow == nil || d.Flow.UpstreamTerminalObserved || d.Flow.DownstreamDeliveryOutcome != tracking.FlowDeliveryPartialOrFailed || d.Flow.DownstreamBytesWritten != int64(len(partial)) {
		t.Fatalf("截断后的结算：%s，usage_known=%t，路径 %+v", d.Detail.Status, d.Detail.UsageKnown, d.Flow)
	}
}

func TestRequestFlow_WSControlReceiptCountsActualDelivery(t *testing.T) {
	tr := newLifecyclePanelTestTracker(t)
	state := newTestSubscriptionSubmissionState(context.Background(), "receipt", newSubscriptionTrackingObserver(tr, nil))
	defer state.close("closed")
	i := &subscriptionWSInterceptor{submissions: state}
	sub, _ := beginTestSubscriptionCreate(t, state, []byte(`{"type":"response.create","stream_id":"s","input":"x"}`))
	var wireBytes int64
	send := func(b []byte) error { wireBytes += int64(len(b)); return nil }
	for _, frame := range []string{
		`{"type":"response.created","stream_id":"s","response":{"id":"r"}}`,
		`{"type":"response.steer.accepted","stream_id":"s","response_id":"r","sequence_number":1}`,
		`{"type":"response.completed","stream_id":"s","response":{"id":"r","status":"completed","usage":{"output_tokens":3}}}`,
	} {
		if err := boundaryWrite(boundaryRead(t, i, []byte(frame)), send); err != nil {
			t.Fatal(err)
		}
	}
	d := timingLifecycleData(t, tr, sub.RequestID)
	if wireBytes != 269 || d.Flow == nil || d.Flow.DownstreamBytesWritten != wireBytes || d.Detail.Status != "completed" {
		t.Fatalf("实际写出 %d 字节，结算 %s，路径 %+v", wireBytes, d.Detail.Status, d.Flow)
	}
}
