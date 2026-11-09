package proxy

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"sinuo/internal/proxy/subscriptionws"
	"sinuo/internal/tracking"

	"github.com/coder/websocket"
	"github.com/tidwall/gjson"
)

// 真实双跳：A 暂存占位符前缀、B 同时生成，第三个 create 的错误不能丢掉 A/B 的终态。
func TestSubscriptionWSRestoreErrorIsolation(t *testing.T) {
	env := newSubscriptionTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		up, e := websocket.Accept(w, r, nil)
		if e != nil {
			return
		}
		defer up.CloseNow()
		read := func() ([]byte, error) { _, b, e := up.Read(r.Context()); return b, e }
		write := func(raw string) error { return up.Write(r.Context(), websocket.MessageText, []byte(raw)) }
		a, e := read()
		if e != nil {
			return
		}
		token := string(wireToken.Find(a))
		if len(token) < 8 {
			t.Error("上游缺少占位符")
			return
		}
		if write(`{"type":"response.created","stream_id":"A","response":{"id":"A"}}`) != nil {
			return
		}
		if _, e = read(); e != nil {
			return
		}
		if write(`{"type":"response.created","stream_id":"B","response":{"id":"B"}}`) != nil {
			return
		}
		if write(fmt.Sprintf(`{"type":"response.output_text.delta","response_id":"A","output_index":0,"content_index":0,"delta":%q}`, "prefix "+token[:7])) != nil {
			return
		}
		if _, e = read(); e != nil {
			return
		}
		for _, raw := range []string{
			`{"type":"error","status":400,"error":{"type":"invalid_request_error","code":"invalid_stream_id","param":"stream_id","message":"sensitive-echo"},"debug":"sensitive-echo"}`,
			fmt.Sprintf(`{"type":"response.output_text.delta","response_id":"A","output_index":0,"content_index":0,"delta":%q}`, token[7:]),
			fmt.Sprintf(`{"type":"response.output_text.done","response_id":"A","output_index":0,"content_index":0,"text":%q}`, "prefix "+token),
			`{"type":"response.completed","response":{"id":"A","usage":{"input_tokens":1,"output_tokens":2}}}`,
			`{"type":"response.completed","response":{"id":"B","usage":{"input_tokens":1,"output_tokens":3}}}`,
			`{"type":"error","error":{"type":"invalid_request_error","code":"websocket_connection_limit_reached","message":"sensitive-echo"}}`,
		} {
			if write(raw) != nil {
				return
			}
		}
		_, _ = read()
	}, "subscription")
	env.handler.proxy.SetPrivacyFilter(newRestoreTestFilter(t))
	tracker := newLifecyclePanelTestTracker(t)
	env.handler.proxy.SetUsageTracker(tracker)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(env.server.URL, "http")+"/subscription/"+subscriptionTestSecret+"/backend-api/codex/responses", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer synthetic"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	for _, lane := range []string{"A", "B", "invalid lane"} {
		if err := c.Write(ctx, websocket.MessageText, []byte(fmt.Sprintf(`{"type":"response.create","stream_id":%q,"model":%q,"input":%q}`, lane, "model-"+lane, routeTestValue))); err != nil {
			t.Fatal(err)
		}
	}
	var visible strings.Builder
	var events []string
	for {
		_, b, e := c.Read(ctx)
		if e != nil {
			if ctx.Err() != nil {
				t.Fatal(e)
			}
			break
		}
		if strings.Contains(string(b), "sensitive-echo") {
			t.Fatal("上游错误回显泄漏", string(b))
		}
		switch gjson.GetBytes(b, "type").Str {
		case "error":
			code := gjson.GetBytes(b, "error.code").Str
			if code == "invalid_stream_id" && gjson.GetBytes(b, "status").Int() != 400 {
				t.Fatal(string(b))
			}
			events = append(events, code)
		case "response.output_text.delta":
			visible.WriteString(gjson.GetBytes(b, "delta").Str)
		case "response.completed":
			events = append(events, gjson.GetBytes(b, "response.id").Str)
		}
	}
	if strings.Join(events, ",") != "invalid_stream_id,A,B,websocket_connection_limit_reached" || visible.String() != "prefix "+routeTestValue {
		t.Fatalf("错误、终态或暂存内容丢失: %v %q", events, visible.String())
	}
	rows, _, err := tracker.QueryRequestDetailsWithHotPool(ctx, &tracking.QueryOptions{Limit: 10})
	if err != nil || len(rows) != 3 {
		t.Fatalf("三个提交没有独立结算: %d %v", len(rows), err)
	}
	seen := map[string]bool{}
	for _, row := range rows {
		d := timingLifecycleData(t, tracker, row.RequestID).Detail
		if seen[d.RequestModel] {
			t.Fatalf("请求重复结算 %s", d.RequestModel)
		}
		seen[d.RequestModel] = true
		switch d.RequestModel {
		case "model-A", "model-B":
			want := 2
			if d.RequestModel == "model-B" {
				want = 3
			}
			if d.Status != "completed" || int(d.OutputTokens) != want {
				t.Fatalf("正常请求结算错误 %s: %s %d", d.RequestModel, d.Status, d.OutputTokens)
			}
		case "model-invalid lane":
			if d.Status == "completed" || d.OutputTokens != 0 {
				t.Fatalf("上游拒绝的请求结算错误: %s %d", d.Status, d.OutputTokens)
			}
		default:
			t.Fatalf("未知请求 %q", d.RequestModel)
		}
	}
}

func TestSubscriptionWSRestoreErrorOwnership(t *testing.T) {
	state, _ := activitySubmissionState()
	defer state.close("test")
	i := &subscriptionWSInterceptor{submissions: state, filter: newRestoreTestFilter(t)}
	deliver := func(dir subscriptionws.Direction, raw string) string {
		t.Helper()
		r, e := i.Intercept(context.Background(), dir, []byte(raw))
		if e != nil {
			t.Fatal(e)
		}
		out := string(r.Forward)
		if r.Deliver != nil {
			if err := r.Deliver(func(b []byte) error { out = string(b); return nil }, nil); err != nil {
				t.Fatal(err)
			}
		}
		if r.Forwarded != nil {
			r.Forwarded()
		}
		return out
	}
	for _, lane := range []string{"A", "B"} {
		deliver(subscriptionws.DirectionDownstreamToUpstream, fmt.Sprintf(`{"type":"response.create","stream_id":%q,"input":[]}`, lane))
	}
	up := func(f string) string { return deliver(subscriptionws.DirectionUpstreamToDownstream, f) }
	a, b := state.active[0], state.active[1]
	if out := up(`{"type":"error","status":400,"error":{"message":"sensitive-echo"}}`); gjson.Get(out, "status").Int() != 400 || strings.Contains(out, "sensitive-echo") || a.settled || b.settled {
		t.Fatal("只有消息的错误应安全交付并保留两个待建请求", out)
	}
	frame := `{"type":"error","status":400,"error":{"code":"invalid_stream_id","message":"sensitive-echo"}`
	if out := up(frame + "}"); gjson.Get(out, "error.code").Str != "invalid_stream_id" || a.settled || b.settled {
		t.Fatal("无标识错误错误结算在途请求", out)
	}
	if out := up(frame + `,"stream_id":"B"}`); !b.settled || a.settled || gjson.Get(out, "stream_id").Str != "B" {
		t.Fatal("未准确结算 B", out)
	}
	up(`{"type":"response.created","stream_id":"A","response":{"id":"ra"}}`)
	if out := up(frame + "}"); a.settled || gjson.Get(out, "error.code").Str != "invalid_stream_id" {
		t.Fatal("新提交的错误终止了已绑定的响应", out)
	}
	if out := up(frame + `,"response_id":"ra","stream_id":"wrong"}`); a.settled || gjson.Get(out, "error.code").Str != "invalid_stream_id" {
		t.Fatal("冲突的路由标识终止了 A", out)
	}
	if out := up(frame + `,"response_id":"ra"}`); !a.settled || gjson.Get(out, "response_id").Str != "ra" {
		t.Fatal("明确的响应错误没有结算 A", out)
	}
}
