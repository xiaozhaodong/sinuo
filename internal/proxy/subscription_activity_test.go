package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sinuo/internal/monitor"
	"sinuo/internal/privacy"
	"sinuo/internal/proxy/subscriptionws"

	"github.com/coder/websocket"
)

func activitySubmissionState() (*subscriptionSubmissionState, *monitor.RequestActivity) {
	activity := &monitor.RequestActivity{}
	ctx := withSubscriptionRequestInfo(context.Background(), &subscriptionRequestInfo{activity: activity})
	return newTestSubscriptionSubmissionState(ctx, "ws", nil), activity
}

// handleUpstream 供不含传输层的协议单测使用，同步确认消息；生产路径由桥接确认写出。
func (s *subscriptionSubmissionState) handleUpstream(payload []byte) *ParseResult {
	result, forwarded, err := s.handleUpstreamForForward(payload)
	if err != nil {
		panic(err)
	}
	if forwarded != nil {
		forwarded()
	}
	return result
}

func TestSubscriptionActivity_WarmupAndTerminalForwarding(t *testing.T) {
	state, activity := activitySubmissionState()
	defer state.close("connection_closed")
	interceptor := &subscriptionWSInterceptor{submissions: state}
	beginTestSubscriptionCreate(t, state, []byte(`{"type":"response.create","generate":false}`))
	state.handleUpstream([]byte(`{"type":"response.created","response":{"id":"warmup"}}`))
	state.handleUpstream([]byte(`{"type":"response.completed","response":{"id":"warmup"}}`))
	if activity.Count() != 0 {
		t.Fatal("预热被算作生成请求")
	}
	beginTestSubscriptionCreate(t, state, []byte(`{"type":"response.create","input":[]}`))
	state.handleUpstream([]byte(`{"type":"response.created","response":{"id":"generation"}}`))
	result, err := interceptor.Intercept(context.Background(), subscriptionws.DirectionUpstreamToDownstream,
		[]byte(`{"type":"response.completed","response":{"id":"generation"}}`))
	if err != nil || result.Forwarded == nil || activity.Count() != 1 {
		t.Fatalf("读取终态提前释放请求: %+v %v count=%d", result, err, activity.Count())
	}
	if activity.BeginDrain("quit") != 1 {
		t.Fatal("排空未等待终态写出")
	}
	rejected, err := interceptor.Intercept(context.Background(), subscriptionws.DirectionDownstreamToUpstream,
		[]byte(`{"type":"response.create","stream_id":"next","input":[]}`))
	if err != nil || rejected.Forward != nil || len(rejected.Reply) == 0 || activity.Count() != 1 {
		t.Fatal("已有 WS 上的新提交未被拒绝")
	}
	var rejection struct {
		Status   int    `json:"status"`
		StreamID string `json:"stream_id"`
	}
	if err := json.Unmarshal(rejected.Reply, &rejection); err != nil || rejection.Status != 503 || rejection.StreamID != "next" {
		t.Fatalf("排空错误未保留状态或 stream: %s", rejected.Reply)
	}
	result.Forwarded()
	result.Forwarded()
	if activity.Count() != 0 {
		t.Fatal("成功写出后未归零")
	}
	activity.CancelDrain("quit")
	if submission, _ := beginTestSubscriptionCreate(t, state, []byte(`{"type":"response.create","input":[]}`)); submission == nil || activity.Count() != 1 {
		t.Fatal("取消排空后不能在原连接继续生成")
	}
}

func TestSubscriptionActivity_CloseReleasesUnwrittenAndPending(t *testing.T) {
	state, activity := activitySubmissionState()
	beginTestSubscriptionCreate(t, state, []byte(`{"type":"response.create"}`))
	beginTestSubscriptionCreate(t, state, []byte(`{"type":"response.create"}`))
	state.handleUpstream([]byte(`{"type":"response.created","response":{"id":"one"}}`))
	_, forwarded, err := state.handleUpstreamForForward([]byte(`{"type":"response.completed","response":{"id":"one"}}`))
	if err != nil || forwarded == nil || activity.Count() != 2 {
		t.Fatal("排队或待发送尾帧未计入")
	}
	state.close("connection_closed")
	state.close("connection_closed")
	forwarded()
	if activity.Count() != 0 {
		t.Fatal("断连后遗留或重复释放请求")
	}
}

func TestSubscriptionActivity_LocalRejectionWaitsForReply(t *testing.T) {
	state, activity := activitySubmissionState()
	defer state.close("connection_closed")
	submission, _ := beginTestSubscriptionCreate(t, state, []byte(`{"type":"response.create"}`))
	forwarded := state.reject(submission, &privacy.PolicyError{StatusCode: http.StatusUnprocessableEntity, Code: privacy.CodeScanFailed})
	if forwarded == nil || activity.Count() != 1 {
		t.Fatal("本地错误响应写出前释放了请求")
	}
	forwarded()
	if activity.Count() != 0 {
		t.Fatal("本地错误响应写出后未释放")
	}
}

func TestSubscriptionActivity_DrainRejectionLogsMetadataWithoutTracking(t *testing.T) {
	state, activity := activitySubmissionState()
	defer state.close("connection_closed")
	observer := &recordingSubmissionObserver{}
	state.observer = observer
	var logs bytes.Buffer
	interceptor := &subscriptionWSInterceptor{submissions: state, logger: slog.New(slog.NewJSONHandler(&logs, nil))}
	activity.BeginDrain("quit")
	result, err := interceptor.Intercept(context.Background(), subscriptionws.DirectionDownstreamToUpstream,
		[]byte(`{"type":"response.create","stream_id":"next","input":"SYNTHETIC-PRIVATE-BODY","model":"private-model"}`))
	if err != nil || result.Forward != nil || len(result.Reply) == 0 {
		t.Fatalf("排空拒绝=%+v err=%v", result, err)
	}
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &record); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"level": "WARN", "conn_id": "ws", "code": "codex_subscription_proxy_draining", "stream_id": "next", "msg": "⚠️ [Codex 订阅代理] WS 提交被本地拒绝"} {
		if record[key] != want {
			t.Fatalf("日志字段 %s=%v，期望 %s", key, record[key], want)
		}
	}
	if strings.Contains(logs.String(), "SYNTHETIC-PRIVATE-BODY") || strings.Contains(logs.String(), "private-model") {
		t.Fatal("拒绝日志包含请求正文")
	}
	if observer.count("started") != 0 || observer.count("rejected") != 0 || len(state.pending) != 0 || len(state.active) != 0 || activity.Count() != 0 {
		t.Fatal("排空拒绝改变了生成请求的追踪或计数口径")
	}
}

func TestSubscriptionActivity_ServerSuccessorAndDrain(t *testing.T) {
	state, activity := activitySubmissionState()
	defer state.close("connection_closed")
	_, _, err := state.handleUpstreamForForward([]byte(`{"type":"response.created","response":{"id":"successor"}}`))
	if err != nil || activity.Count() != 1 {
		t.Fatal("服务端 successor 未被计入")
	}
	activity.BeginDrain("quit")
	_, _, err = state.handleUpstreamForForward([]byte(`{"type":"response.created","response":{"id":"continuation"}}`))
	if err != nil || activity.Count() != 2 {
		t.Fatal("排空中断了已受理请求的服务端续接")
	}
	_, forwarded, err := state.handleUpstreamForForward([]byte(`{"type":"response.completed","response":{"id":"successor"}}`))
	if err != nil || forwarded == nil {
		t.Fatal("排空打断已受理的 successor")
	}
	forwarded()
	_, forwarded, err = state.handleUpstreamForForward([]byte(`{"type":"response.completed","response":{"id":"continuation"}}`))
	if err != nil || forwarded == nil {
		t.Fatal("服务端续接未结束")
	}
	forwarded()
	_, _, err = state.handleUpstreamForForward([]byte(`{"type":"response.created","response":{"id":"new-successor"}}`))
	if err != errSubscriptionDraining || activity.Count() != 0 {
		t.Fatal("排空期间受理了新的服务端生成")
	}
}

func TestSubscriptionActivity_RealWebSocketDrainsAndResumes(t *testing.T) {
	finish := make(chan struct{})
	var finishOnce sync.Once
	finishResponse := func() { finishOnce.Do(func() { close(finish) }) }
	t.Cleanup(finishResponse)
	var generations atomic.Int32
	env := newSubscriptionTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			_, body, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			var request struct {
				Generate *bool `json:"generate"`
			}
			if json.Unmarshal(body, &request) != nil {
				return
			}
			id := "warmup"
			generate := request.Generate == nil || *request.Generate
			if generate {
				id = fmt.Sprintf("generation-%d", generations.Add(1))
			}
			if conn.Write(r.Context(), websocket.MessageText, []byte(fmt.Sprintf(`{"type":"response.created","response":{"id":%q}}`, id))) != nil {
				return
			}
			if generate {
				select {
				case <-finish:
				case <-r.Context().Done():
					return
				}
			}
			if conn.Write(r.Context(), websocket.MessageText, []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":%q}}`, id))) != nil {
				return
			}
		}
	}, "subscription")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(env.server.URL, "http")+"/subscription/"+subscriptionTestSecret+"/backend-api/codex/responses", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": []string{"Bearer synthetic-client-token"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	read := func(want string) {
		t.Helper()
		_, payload, err := conn.Read(ctx)
		if err != nil || !strings.Contains(string(payload), want) {
			t.Fatalf("WS 响应 want=%s got=%s err=%v", want, payload, err)
		}
	}
	write := func(payload string) {
		t.Helper()
		if err := conn.Write(ctx, websocket.MessageText, []byte(payload)); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"type":"response.create","generate":false,"input":[]}`)
	read("response.created")
	read("response.completed")
	activity := env.handler.activity
	if activity.Count() != 0 {
		t.Fatal("握手或预热留下业务请求")
	}
	write(`{"type":"response.create","input":[]}`)
	read("response.created")
	if activity.BeginDrain("quit") != 1 {
		t.Fatal("真实 WS 生成没有阻挡退出")
	}
	write(`{"type":"response.create","input":[]}`)
	read("codex_subscription_proxy_draining")
	response := env.request(http.MethodGet, "models", nil)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatal("排空期间接受了新 HTTP 请求")
	}
	finishResponse()
	read("response.completed")
	for activity.Count() != 0 {
		select {
		case <-ctx.Done():
			t.Fatal("终态转发后未归零")
		case <-time.After(time.Millisecond):
		}
	}
	if env.handler.ActiveWebSockets() == 0 {
		t.Fatal("排空提前断开了可取消等待的连接")
	}
	activity.CancelDrain("quit")
	write(`{"type":"response.create","input":[]}`)
	read("response.created")
	read("response.completed")
	if generations.Load() != 2 {
		t.Fatalf("排空时的提交到达了上游: generations=%d", generations.Load())
	}
}
