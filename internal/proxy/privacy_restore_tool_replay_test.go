package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/tidwall/gjson"

	"sinuo/internal/privacy"
	"sinuo/internal/proxy/restorestream"
)

// 使用首轮真正交付给客户端的工具项续接，检查第二轮实际送往上游的参数和密文。
func TestEncryptedFunctionReplay(t *testing.T) {
	for _, mode := range []string{"JSON", "SSE", "WS", "WS-wrapped"} {
		t.Run(mode, func(t *testing.T) {
			filter := newRestoreTestFilter(t).Snapshot
			req := privacy.Request{Path: "/v1/responses", SessionID: "session-A", UpstreamType: privacy.UpstreamTypeSubscription, WebSocket: strings.HasPrefix(mode, "WS")}
			if mode == "SSE" {
				req.UpstreamType = privacy.UpstreamTypeAccount
				req.AccountID = 12
			}
			encode := func(v any) string {
				b, err := json.Marshal(v)
				if err != nil {
					t.Fatal(err)
				}
				return string(b)
			}
			apply := func(r privacy.Request, input string) (privacy.ApplyResult, error) {
				body := `{"input":` + input + `}`
				if mode == "WS-wrapped" {
					body = `{"type":"response.create","response":` + body + `}`
				}
				return filter.Apply(r, []byte(body))
			}
			first, err := apply(req, encode(routeTestValue))
			if err != nil {
				t.Fatal(err)
			}
			token := string(wireToken.Find(first.Body))
			if token == "" {
				t.Fatal("首轮没有遮蔽")
			}
			if e := first.Restore.Publish(); e != nil {
				t.Fatal(e)
			}
			defer first.Restore.Release()
			arguments := func(value string) string { return encode(map[string]string{"v": value}) }
			item := func(args, cipher, call string) string {
				return `{"type":"function_call","name":"fixture","namespace":"tools","call_id":` + encode(call) + `,"arguments":` + encode(args) + `,"encrypted_content":` + encode(cipher) + `}`
			}
			original := item(arguments(token), "opaque-A", "call-A")
			resp := `{"id":"r","status":"completed","output":[` + original + `]}`
			event := `{"type":"response.completed","response":` + resp + `}`
			var delivered string
			switch mode {
			case "JSON":
				d, e := restorestream.RestoreJSON(req.Path, []byte(resp), first.Restore)
				if e != nil {
					t.Fatal(e)
				}
				d.Confirm()
				delivered = gjson.GetBytes(d.Body, "output.0").Raw
			case "SSE":
				rec := httptest.NewRecorder()
				w := restorestream.New(rec, req.Path, first.Restore)
				defer w.Release()
				if _, e := fmt.Fprintf(w, "data: %s\n\n", event); e != nil {
					t.Fatal(e)
				}
				if e := w.Finish(); e != nil {
					t.Fatal(e)
				}
				delivered = gjson.Get(strings.TrimSpace(strings.TrimPrefix(rec.Body.String(), "data: ")), "response.output.0").Raw
			default:
				w := restorestream.New(nil, req.Path, first.Restore)
				defer w.Release()
				if e := w.WriteMessage([]byte(event), func(raw []byte) error { delivered = gjson.GetBytes(raw, "response.output.0").Raw; return nil }); e != nil {
					t.Fatal(e)
				}
			}
			if gjson.Get(gjson.Get(delivered, "arguments").Str, "v").Str != routeTestValue {
				t.Fatalf("客户端没有收到完整参数 %s", delivered)
			}
			result, err := apply(req, "["+delivered+"]")
			if err != nil {
				t.Fatalf("原工具调用重新遮蔽后被拒绝: %v", err)
			}
			defer result.Restore.Release()
			path := "input.0"
			if mode == "WS-wrapped" {
				path = "response." + path
			}
			actual := gjson.GetBytes(result.Body, path)
			if actual.Get("arguments").Str != arguments(token) || actual.Get("encrypted_content").Str != "opaque-A" {
				t.Fatalf("上游工具调用与首轮不符 %s", actual.Raw)
			}
			clean := item(`{ "v":"ordinary" }`, "external", "call-external")
			r, e := apply(req, "["+clean+"]")
			if e != nil {
				t.Fatal(e)
			}
			defer r.Restore.Release()
			if !bytes.Contains(r.Body, []byte(clean)) {
				t.Fatal("未命中工具项未按原字节发送")
			}
			otherAccount, otherSession, noSession := req, req, req
			otherAccount.AccountID++
			otherSession.SessionID = "session-B"
			noSession.SessionID = ""
			for _, other := range []privacy.Request{otherAccount, otherSession, noSession} {
				r, err := apply(other, "["+delivered+"]")
				if err != nil {
					t.Fatal("换号或会话变化后拒绝了原工具历史", err)
				}
				r.Restore.Release()
				actual := gjson.GetBytes(r.Body, path)
				if actual.Get("encrypted_content").Str != "opaque-A" || actual.Get("arguments").Str != arguments(token) {
					t.Fatal("跨路由重放改变了上游原件")
				}
			}
			// 另一会话可独立使用相同调用 ID 和不同密文。
			r, e = apply(otherSession, "["+item(arguments(routeTestValue), "unseen-cipher", "call-A")+"]")
			if e != nil {
				t.Fatal("跨会话的独立调用发生冲突", e)
			}
			r.Restore.Release()
			for _, tc := range []struct {
				r       privacy.Request
				history string
			}{
				{otherAccount, item(arguments(routeTestValue+"!"), "opaque-A", "call-A")}, {req, strings.Replace(delivered, "opaque-A", "opaque-B", 1)}, {req, strings.Replace(delivered, "call-A", "call-B", 1)},
				{req, item(arguments(routeTestValue+"!"), "opaque-A", "call-A")},
				{req, item(`{"v":`, "opaque-A", "call-A")}, {req, `{"type":"function_call","call_id":"call-A","name":"fixture","encrypted_content":"opaque-A"}`},
				{req, strings.Replace(clean, "function_call", "custom_tool_call", 1)}, {req, strings.TrimSuffix(clean, "}") + `,"encrypted_function_args":"opaque"}`},
				{req, `{"type":"custom_tool_call","call_id":"call-A","name":"fixture","input":{},"encrypted_content":"opaque"}`},
				{req, `{"type":"message","role":"user","content":"ordinary","encrypted_content":"opaque"}`},
				{req, `{"type":"message","role":"assistant","content":{},"encrypted_content":"opaque"}`},
			} {
				if _, err := apply(tc.r, "["+tc.history+"]"); err == nil {
					t.Fatal("不应允许重放", tc.history)
				}
			}
			// 重启后只保留原密钥与规则。即使另一个调用先填充了新字典，也能重发旧历史。
			filter = newRestoreTestFilter(t).Snapshot
			later, err := apply(req, encode("ordinary"))
			if err != nil {
				t.Fatal(err)
			}
			if e := later.Restore.Publish(); e != nil {
				t.Fatal(e)
			}
			later.Restore.RememberFunctionReplay("another-call", "fixture", "tools", "another-cipher", arguments("another-value"))
			later.Restore.Release()
			replayed, err := apply(req, "["+delivered+"]")
			if err != nil {
				t.Fatalf("重启后重发旧工具调用被拒绝: %v", err)
			}
			defer replayed.Restore.Release()
			actual = gjson.GetBytes(replayed.Body, path)
			if actual.Get("arguments").Str != arguments(token) || actual.Get("encrypted_content").Str != "opaque-A" {
				t.Fatal("重启后未重建首轮参数或密文被改写", actual.Raw)
			}
		})
	}
}

// 模拟所有账号共用相同扫描规则的尝试缓存。
type globalToolReplayFilter struct{ liveRestoreFilter }

func (globalToolReplayFilter) PrivacyScopeFingerprint(privacy.Request, int64) string { return "global" }

func TestEncryptedToolReplayAccountFailover(t *testing.T) {
	var attempts atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		token := wireToken.Find(body)
		if err != nil || token == nil {
			t.Error("账号请求缺少遮蔽值", err)
			w.WriteHeader(400)
			return
		}
		args := fmt.Sprintf(`{"v":%q}`, token)
		call := "call-first"
		w.Header().Set("Content-Type", "application/json")
		switch attempts.Add(1) {
		case 2:
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":{"code":"rate_limit_exceeded"}}`)
			return
		case 3:
			item := gjson.GetBytes(body, "input.0")
			if item.Get("call_id").Str != call || item.Get("encrypted_content").Str != "cipher-"+call || item.Get("arguments").Str != args {
				t.Error("备选账号收到的工具历史与原件不一致")
			}
			call = "call-next"
		}
		fmt.Fprintf(w, `{"id":"r","status":"completed","output":[{"type":"function_call","call_id":%q,"name":"demo","arguments":%q,"encrypted_content":%q}]}`, call, args, "cipher-"+call)
	}))
	defer up.Close()
	h, _ := phaseAccountHandler(t, up.URL, 2)
	filter := globalToolReplayFilter{newRestoreTestFilter(t)}
	h.SetPrivacyFilter(filter)
	post := func(id, body string) gjson.Result {
		t.Helper()
		req := privacySummaryRequest("/v1/responses", body, id)
		req.Header.Set("Session-Id", "tool-retry")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		item := gjson.Get(rec.Body.String(), "output.0")
		if rec.Code != 200 || gjson.Get(item.Get("arguments").Str, "v").Str != routeTestValue {
			t.Fatalf("工具历史交付失败：%d %s", rec.Code, rec.Body.String())
		}
		return item
	}
	first := post("tool-first", strings.Replace(routeBody(true), `"stream":true`, `"stream":false`, 1))
	next := post("tool-retry", `{"model":"gpt-5","input":[`+first.Raw+"]}")
	if attempts.Load() != 3 || next.Get("call_id").Str != "call-next" {
		t.Fatal("没有完成 429 换号")
	}
	// 新证明须绑定成功的第二个账号；同一账号下篡改此次调用的密文应被拒。
	req := privacy.Request{Path: "/v1/responses", UpstreamType: privacy.UpstreamTypeAccount, AccountID: 2, SessionID: "tool-retry"}
	changed := strings.Replace(next.Raw, "cipher-call-next", "unrecorded", 1)
	if _, err := filter.Snapshot.Apply(req, []byte(`{"input":[`+changed+"]}")); err == nil {
		t.Fatal("重试复用了首个账号的回填视图")
	}
}

func TestEncryptedToolHistoryEmptyArrays(t *testing.T) {
	filter := newRestoreTestFilter(t).Snapshot
	for _, field := range []string{"encrypted_function_args", "encrypted_output"} {
		for _, ws := range []bool{false, true} {
			req := privacy.Request{Path: "/v1/responses", UpstreamType: privacy.UpstreamTypeAccount, WebSocket: ws}
			for _, value := range []string{"null", `""`, "[]", "[ ]", `["opaque"]`, "[null]", "[[]]", "[{}]", `"opaque"`, "{}", "0"} {
				item := `{"type":"function_call_output","call_id":"test","output":"ok"}`
				if field == "encrypted_function_args" {
					item = `{"type":"function_call","call_id":"test","name":"test","arguments":"{}"}`
				}
				body := `{"input":[` + strings.TrimSuffix(item, "}") + `,"` + field + `":` + value + `}]}`
				if ws {
					body = `{"type":"response.create","response":` + body + "}"
				}
				result, err := filter.Apply(req, []byte(body))
				switch value {
				case "null", `""`, "[]", "[ ]":
					if err != nil || string(result.Body) != body {
						t.Fatalf("%s=%s, WS=%t: %v", field, value, ws, err)
					}
					result.Restore.Release()
				default:
					if policy, ok := err.(*privacy.PolicyError); !ok || policy.StatusCode != 400 {
						t.Fatalf("%s=%s 应在出站前拒绝, WS=%t: %v", field, value, ws, err)
					}
				}
			}
		}
	}
}

// 用交付给客户端的完整输出作历史，不混入 function_call，以覆盖只有消息与自定义工具的会话。
func TestEncryptedMessageAndCustomToolHistory(t *testing.T) {
	filter := newRestoreTestFilter(t)
	req := privacy.Request{Path: "/v1/responses", UpstreamType: privacy.UpstreamTypeSubscription}
	first, err := filter.Apply(context.Background(), req, []byte(routeBody(true)))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Restore.Release()
	if e := first.Restore.Publish(); e != nil {
		t.Fatal(e)
	}
	token := string(wireToken.Find(first.Body))
	message := fmt.Sprintf(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"found %s"}],"encrypted_content":"msg-cipher"}`, token)
	custom := fmt.Sprintf(`{"type":"custom_tool_call","name":"apply_patch","call_id":"call-a","input":"patch %s","encrypted_content":"call-cipher"}`, token)
	output := "[" + message + "," + custom + "]"
	delivery, err := restorestream.RestoreJSON(req.Path, []byte(`{"output":`+output+"}"), first.Restore)
	if err != nil {
		t.Fatal(err)
	}
	delivery.Confirm()
	items := gjson.GetBytes(delivery.Body, "output")
	if items.Get("0.content.0.text").Str != "found "+routeTestValue || items.Get("1.input").Str != "patch "+routeTestValue {
		t.Fatal("客户端未收到还原的正文和自定义工具输入")
	}
	for _, tc := range []struct{ name, path, field string }{
		{"HTTP", "/v1/responses", "input"}, {"compact", "/v1/responses/compact", "input"}, {"WS", "/v1/responses", "response.input"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := req
			req.Path, req.WebSocket = tc.path, tc.name == "WS"
			raw := `{"input":` + items.Raw + "}"
			if req.WebSocket {
				raw = `{"type":"response.create","response":` + raw + "}"
			}
			restarted := newRestoreTestFilter(t)
			if tc.name == "HTTP" {
				// 下游应收到合成上游的实际判定，而不是被入口的冷字典拒绝。
				const upstreamError = `{"error":{"code":"fixture_replay_rejected"}}`
				env := newSubscriptionTestEnv(t, func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusConflict)
					io.WriteString(w, upstreamError)
				}, "subscription")
				env.handler.proxy.SetPrivacyFilter(restarted)
				resp := env.request("POST", "responses", func(r *http.Request) {
					r.Header.Set("Authorization", "Bearer synthetic")
					r.Header.Set("Content-Type", "application/json")
					r.Body = io.NopCloser(strings.NewReader(raw))
					r.ContentLength = int64(len(raw))
				})
				defer resp.Body.Close()
				body, err := io.ReadAll(resp.Body)
				sent := env.upstreamRequests()
				if err != nil || resp.StatusCode != http.StatusConflict || string(body) != upstreamError ||
					len(sent) != 1 || gjson.GetBytes(sent[0].Body, "input").Raw != output {
					t.Fatal("订阅 HTTP 未保留重放历史或上游错误", resp.Status, err)
				}
				return
			}
			result, err := restarted.Apply(context.Background(), req, []byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			defer result.Restore.Release()
			if actual := gjson.GetBytes(result.Body, tc.field).Raw; actual != output {
				t.Fatalf("整段历史没有恢复为上游输出：%s", actual)
			}
		})
	}
}

// 先经订阅 HTTP/SSE 收到真实下游工具项，再通过同会话原生 WS 重发完整历史。
// 分片、arguments.done、item.done 和终态覆盖 SSE 单工具快照的优化路径。
func TestSubscriptionEncryptedFunctionReplayAcrossTransports(t *testing.T) {
	var env *subscriptionTestEnv
	seen := make(chan []byte, 1)
	env = newSubscriptionTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer conn.CloseNow()
			_, raw, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			seen <- raw
			_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.created","response":{"id":"second"}}`))
			_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.completed","response":{"id":"second","output":[]}}`))
			_, _, _ = conn.Read(r.Context())
			return
		}
		sent := env.upstreamRequests()[0].Body
		token := string(wireToken.Find(sent))
		if token == "" {
			t.Error("首轮未遮蔽")
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		args := fmt.Sprintf(`{"v":%q}`, token)
		item := fmt.Sprintf(`{"type":"function_call","name":"fixture","call_id":"call-cross","id":"fc","arguments":%q,"encrypted_content":"opaque-cross"}`, args)
		frames := []string{
			`{"type":"response.created","response":{"id":"first","output":[]}}`,
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc","call_id":"call-cross","arguments":""}}`,
		}
		for _, part := range []string{args[:12], args[12:]} {
			frames = append(frames, fmt.Sprintf(`{"type":"response.function_call_arguments.delta","item_id":"fc","output_index":0,"delta":%q}`, part))
		}
		frames = append(frames, fmt.Sprintf(`{"type":"response.function_call_arguments.done","item_id":"fc","output_index":0,"arguments":%q}`, args),
			fmt.Sprintf(`{"type":"response.output_item.done","output_index":0,"item":%s}`, item),
			fmt.Sprintf(`{"type":"response.completed","response":{"id":"first","output":[%s]}}`, item))
		for n, frame := range frames {
			fmt.Fprintf(w, "data: %s,\"sequence_number\":%d}\n\n", strings.TrimSuffix(frame, "}"), n)
		}
	}, "subscription")
	env.handler.proxy.SetPrivacyFilter(newRestoreTestFilter(t))
	body := fmt.Sprintf(`{"model":"m","stream":true,"input":%q}`, routeTestValue)
	headers := http.Header{"Authorization": {"Bearer synthetic"}, "Session-Id": {"session-cross"}}
	response := env.request("POST", "responses", func(r *http.Request) {
		r.Header = headers.Clone()
		r.Header.Set("Content-Type", "application/json")
		r.Body = io.NopCloser(strings.NewReader(body))
		r.ContentLength = int64(len(body))
	})
	defer response.Body.Close()
	output, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != 200 {
		t.Fatal(response.Status, err)
	}
	var tool string
	for _, line := range strings.Split(string(output), "\n") {
		event, ok := strings.CutPrefix(line, "data: ")
		if ok && gjson.Get(event, "type").Str == "response.completed" {
			tool = gjson.Get(event, "response.output.0").Raw
		}
	}
	if gjson.Get(gjson.Get(tool, "arguments").Str, "v").Str != routeTestValue {
		t.Fatalf("工具参数未交付 %s", output)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(env.server.URL, "http")+"/subscription/"+subscriptionTestSecret+"/backend-api/codex/responses", &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	const result = `{"type":"function_call_output","call_id":"call-cross","output":"ok"}`
	err = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"response.create","response":{"model":"m","input":[`+tool+","+result+`]}}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"response.created", "response.completed"} {
		_, raw, e := conn.Read(ctx)
		if e != nil || gjson.GetBytes(raw, "type").Str != want {
			t.Fatalf("历史未续接: %s %v", raw, e)
		}
	}
	select {
	case raw := <-seen:
		args := gjson.GetBytes(raw, "response.input.0.arguments").Str
		token := string(wireToken.Find(env.upstreamRequests()[0].Body))
		if gjson.Get(args, "v").Str != token || gjson.GetBytes(raw, "response.input.0.encrypted_content").Str != "opaque-cross" || gjson.GetBytes(raw, "response.input.1.call_id").Str != "call-cross" {
			t.Fatal("上游没有收到原参数、密文和工具结果", string(raw))
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
