package proxy

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"sinuo/internal/tracking"

	"github.com/coder/websocket"
)

// TestSubscriptionWSSteerBridge 覆盖真实桥接、上游拒绝后的继续运行和自动/显式续答。
func TestSubscriptionWSSteerBridge(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(fmt.Sprintf("pending=%t", pending), func(t *testing.T) {
			create := `{"type":"response.create","model":"synthetic-model","stream_id":"lane","input":[]}`
			steer := `{"type":"response.steer","previous_response_id":"parent","input":[{"role":"user","content":"synthetic update"}]}`
			continuation := `{"type":"response.create","model":"synthetic-model","stream_id":"lane","previous_response_id":"parent","input":[{"type":"function_call_output","call_id":"call_1","output":"saved result"}]}`
			failed := `{"type":"response.steer.failed","sequence_number":2,"steer":{"previous_response_id":"parent","input":[{"role":"user","content":"synthetic update"}]},"error":{"type":"invalid_request_error","code":"response_not_active","message":"synthetic rejection"}}`
			accepted := `{"type":"response.steer.accepted","sequence_number":3,"stream_id":"lane","steer":{"id":"steer_1","previous_response_id":"parent"}}`
			created := func(id string) string {
				return `{"type":"response.created","stream_id":"lane","response":{"id":"` + id + `","model":"synthetic-model"}}`
			}
			completed := func(id string) string {
				return `{"type":"response.completed","stream_id":"lane","response":{"id":"` + id + `","usage":{"input_tokens":3,"output_tokens":2}}}`
			}
			parentEnd := `{"type":"response.incomplete","stream_id":"lane","response":{"id":"parent","incomplete_details":{"reason":"steered"},"usage":{"input_tokens":3,"output_tokens":2}}}`
			pendingEvent := `{"type":"response.steer.pending","sequence_number":5,"stream_id":"lane","steer":{"id":"steer_1","previous_response_id":"parent"},"reason":"waiting_for_required_input","required_input":[{"type":"function_call_output","call_id":"call_1"}]}`
			if pending {
				parentEnd = completed("parent")
			}
			tracker := newLifecyclePanelTestTracker(t)
			env := newSubscriptionTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
				upstream, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
				if err != nil {
					t.Error(err)
					return
				}
				defer upstream.CloseNow()
				read := func(want string) bool {
					_, payload, err := upstream.Read(r.Context())
					if err != nil || string(payload) != want {
						t.Errorf("上游收到错误正文：got=%s want=%s err=%v", payload, want, err)
						return false
					}
					return true
				}
				write := func(body string) bool {
					if err := upstream.Write(r.Context(), websocket.MessageText, []byte(body)); err != nil {
						t.Error(err)
						return false
					}
					return true
				}
				if !read(create) || !write(created("parent")) || !read(steer) || !write(failed) ||
					!read(steer) || !write(accepted) || !write(parentEnd) {
					return
				}
				if pending && (!write(pendingEvent) || !read(continuation)) {
					return
				}
				if !write(created("successor")) || !write(completed("successor")) ||
					!read(create) || !write(created("next")) || !write(completed("next")) {
					return
				}
				// 等待客户端主动关闭，避免上游提前断开掩盖尾帧投递问题。
				_, _, _ = upstream.Read(r.Context())
			}, "subscription")
			env.handler.proxy.SetUsageTracker(tracker)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(env.server.URL, "http")+"/subscription/"+subscriptionTestSecret+"/backend-api/codex/responses",
				&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": []string{"Bearer synthetic-client-token"}}})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.CloseNow()
			write := func(body string) {
				t.Helper()
				if err := conn.Write(ctx, websocket.MessageText, []byte(body)); err != nil {
					t.Fatal(err)
				}
			}
			read := func(want string) {
				t.Helper()
				_, payload, err := conn.Read(ctx)
				if err != nil || string(payload) != want {
					t.Fatalf("客户端未收到原始事件：got=%s want=%s err=%v", payload, want, err)
				}
			}
			write(create)
			read(created("parent"))
			write(steer)
			read(failed)
			write(steer)
			read(accepted)
			read(parentEnd)
			if pending {
				read(pendingEvent)
				write(continuation)
			}
			read(created("successor"))
			read(completed("successor"))
			write(create)
			read(created("next"))
			read(completed("next"))
			_ = conn.Close(websocket.StatusNormalClosure, "done")
			if err := tracker.ForceFlush(); err != nil {
				t.Fatal(err)
			}
			rows, _, err := tracker.QueryRequestDetailsWithHotPool(ctx, &tracking.QueryOptions{Limit: 10})
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 3 {
				t.Fatalf("生成请求重复或遗漏：%+v", rows)
			}
			for _, row := range rows {
				if row.InputTokens != 3 || row.OutputTokens != 2 || row.RequestKind != tracking.RequestKindGeneration || row.UpstreamType != "subscription" {
					t.Fatalf("续答请求统计异常：%+v", row)
				}
			}
			if strings.Contains(env.logs.String(), subscriptionUnsupportedWSEventCode) {
				t.Fatal("合法追加输入仍被本地拒绝")
			}
		})
	}
}
