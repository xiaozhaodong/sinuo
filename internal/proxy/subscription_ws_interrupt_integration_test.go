package proxy

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"sinuo/internal/tracking"

	"github.com/coder/websocket"
)

// TestSubscriptionWSInterruptBridge 重放官方 Codex 的 steering/mailbox 协议序列。
// 使用真实 WS 两跳，覆盖回执竞态、已有尾部用量、原连接增量续答及退出排空。
func TestSubscriptionWSInterruptBridge(t *testing.T) {
	for _, scenario := range []string{
		"accepted", "terminal_before_ack", "missing_ack", "failed_ack",
		"late_interrupt", "upstream_failure", "draining",
	} {
		t.Run(scenario, func(t *testing.T) {
			warmup := `{"type":"response.create","generate":false,"model":"synthetic-model","input":[]}`
			create := `{"type":"response.create","model":"synthetic-model","previous_response_id":"warm","input":[{"role":"user","content":"first prompt"}]}`
			// 追加输入和子代理消息由 Codex 排队到下一次 create；保留工具返回与 previous_response_id。
			followup := `{"type":"response.create","model":"synthetic-followup","previous_response_id":"parent","input":[{"type":"function_call_output","call_id":"buffered-tool","output":"aborted before dispatch"},{"role":"user","content":"second prompt"},{"role":"user","content":"synthetic worker update"}]}`
			created := func(id string) string {
				return `{"type":"response.created","response":{"id":"` + id + `","model":"synthetic-model"}}`
			}
			completed := func(id string) string {
				return `{"type":"response.completed","response":{"id":"` + id + `","usage":{"input_tokens":9,"output_tokens":2}}}`
			}
			ack := `{"type":"response.interrupt.accepted","response_id":"parent","sequence_number":3}`
			item := `{"type":"response.output_item.interrupted","response_id":"parent","item_id":"reason-1","output_index":0,"sequence_number":4}`
			terminal := `{"type":"response.incomplete","response":{"id":"parent","status":"incomplete","incomplete_details":{"reason":"interrupted"},"usage":{"input_tokens":9,"output_tokens":2}}}`
			wantStatus, wantFailure := "cancelled", "subscription_cancelled"
			switch scenario {
			case "missing_ack", "late_interrupt":
				terminal, wantStatus, wantFailure = completed("parent"), "completed", ""
			case "failed_ack":
				ack = `{"type":"response.interrupt.failed","response_id":"parent","error":{"code":"interrupt_not_supported"}}`
				terminal, wantStatus, wantFailure = completed("parent"), "completed", ""
			case "upstream_failure":
				terminal = `{"type":"response.failed","response":{"id":"parent","error":{"type":"server_error","message":"synthetic failure"},"usage":{"input_tokens":9,"output_tokens":2}}}`
				wantStatus, wantFailure = "failed", "subscription_response_failed"
			}
			var tail []string
			switch scenario {
			case "missing_ack":
				tail = []string{terminal}
			case "terminal_before_ack":
				tail = []string{item, terminal, ack}
			case "late_interrupt":
				tail = []string{ack}
			default:
				tail = []string{ack, item, terminal}
			}
			tracker := newLifecyclePanelTestTracker(t)
			env := newSubscriptionTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
				for key, want := range map[string]string{
					"x-openai-internal-codex-responses-lite": "true", "x-openai-subagent": "synthetic-worker",
					"OpenAI-Beta": "responses_websockets=2026-02-06", "x-codex-turn-state": "synthetic-state",
				} {
					if r.Header.Get(key) != want {
						t.Errorf("WS 握手能力头丢失：%s", key)
					}
				}
				upstream, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
				if err != nil {
					t.Error(err)
					return
				}
				defer upstream.CloseNow()
				read := func(want string) bool {
					_, body, err := upstream.Read(r.Context())
					if err != nil || string(body) != want {
						t.Errorf("上游收到错误协议帧：got=%s want=%s err=%v", body, want, err)
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
				if !read(warmup) || !write(created("warm")) || !write(completed("warm")) ||
					!read(create) || !write(created("parent")) {
					return
				}
				if scenario == "late_interrupt" && !write(terminal) {
					return
				}
				if !read(subscriptionTestInterrupt) {
					return
				}
				for _, body := range tail {
					if !write(body) {
						return
					}
				}
				if !read(followup) || !write(created("follow-up")) || !write(completed("follow-up")) {
					return
				}
				_, _, _ = upstream.Read(r.Context())
			}, "subscription")
			env.handler.proxy.SetUsageTracker(tracker)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(env.server.URL, "http")+"/subscription/"+subscriptionTestSecret+"/backend-api/codex/responses", &websocket.DialOptions{HTTPHeader: http.Header{
				"Authorization": {"Bearer synthetic-client-token"}, "X-Openai-Internal-Codex-Responses-Lite": {"true"},
				"X-Openai-Subagent": {"synthetic-worker"}, "Openai-Beta": {"responses_websockets=2026-02-06"},
				"X-Codex-Turn-State": {"synthetic-state"},
			}})
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
				_, body, err := conn.Read(ctx)
				if err != nil || string(body) != want {
					t.Fatalf("客户端未收到原始事件：got=%s want=%s err=%v", body, want, err)
				}
			}
			write(warmup)
			read(created("warm"))
			read(completed("warm"))
			write(create)
			read(created("parent"))
			if scenario == "late_interrupt" {
				read(terminal)
			}
			if scenario == "draining" && env.handler.activity.BeginDrain("quit") != 1 {
				t.Fatal("排空没有保留已有生成")
			}
			write(subscriptionTestInterrupt)
			for _, body := range tail {
				read(body)
			}
			if scenario == "draining" {
				env.handler.activity.CancelDrain("quit")
			}
			write(followup)
			read(created("follow-up"))
			read(completed("follow-up"))
			_ = conn.Close(websocket.StatusNormalClosure, "done")
			if err := tracker.ForceFlush(); err != nil {
				t.Fatal(err)
			}
			rows, _, err := tracker.QueryRequestDetailsWithHotPool(ctx, &tracking.QueryOptions{Limit: 10})
			if err != nil || len(rows) != 2 || env.upstreamCount() != 1 {
				t.Fatalf("生成重复/丢失或连接被重建：rows=%+v connections=%d err=%v", rows, env.upstreamCount(), err)
			}
			seenParent := false
			for _, row := range rows {
				if row.RequestModel == "synthetic-model" {
					seenParent = true
					if row.Status != wantStatus || row.FailureReason != wantFailure {
						t.Fatalf("中断请求结算错误：%+v", row)
					}
					if wantStatus == "cancelled" && (row.StopReason == nil || *row.StopReason != "incomplete:interrupted") {
						t.Fatalf("中断的真实结束原因丢失：%+v", row)
					}
				} else if row.RequestModel != "synthetic-followup" || row.Status != "completed" || row.FailureReason != "" {
					t.Fatalf("续答结算错误：%+v", row)
				}
				if row.InputTokens != 9 || row.OutputTokens != 2 || row.RequestKind != tracking.RequestKindGeneration || row.UpstreamType != "subscription" {
					t.Fatalf("尾部用量或请求分类错误：%+v", row)
				}
			}
			if !seenParent || strings.Contains(env.logs.String(), subscriptionUnsupportedWSEventCode) {
				t.Fatal("中断仍被拒绝或没有生成记录")
			}
		})
	}
}
