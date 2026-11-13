package proxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tidwall/gjson"

	"sinuo/internal/endpoint"
	"sinuo/internal/privacy"
	"sinuo/internal/proxy/handlers"
	svc "sinuo/internal/service"
	"sinuo/internal/store"
	"sinuo/internal/tracking"
)

type liveRestoreFilter struct{ *privacy.Snapshot }

func (f liveRestoreFilter) SnapshotVersion() int64 { return f.Version }
func (f liveRestoreFilter) Apply(_ context.Context, r privacy.Request, b []byte) (privacy.ApplyResult, error) {
	return f.Snapshot.Apply(r, b)
}

const routeTestValue = "private-quote\"backslash\\end"

func newRestoreTestFilter(t *testing.T) liveRestoreFilter {
	s := privacy.DefaultSettings()
	s.Mode = privacy.ModeRedact
	s.ResponseRestoreEnabled = true
	rules, e := privacy.CompileRules([]privacy.Rule{{ID: 1, Name: "精确值", Enabled: true, MatchType: privacy.MatchTypeLiteral, Pattern: routeTestValue, Placeholder: "[MASK]", Action: privacy.ActionRedact}})
	if e != nil {
		t.Fatal(e)
	}
	return liveRestoreFilter{&privacy.Snapshot{Version: 1, Dict: privacy.NewRestoreDict(), RestoreKey: bytes.Repeat([]byte{1}, 32), Settings: s, Rules: rules}}
}
func routeBody(codex bool) string {
	s, _ := json.Marshal(routeTestValue)
	if codex {
		return fmt.Sprintf(`{"model":"gpt-5","input":%s,"stream":true}`, s)
	}
	return fmt.Sprintf(`{"model":"claude-test","messages":[{"role":"user","content":%s}],"stream":true}`, s)
}
func routeSSE(id string, codex bool) string {
	var out strings.Builder
	event := func(s string) { fmt.Fprintf(&out, "data: %s\n\n", s) }
	if codex {
		event(`{"type":"response.created","response":{"id":"r","output":[]},"sequence_number":0}`)
		for i, p := range []string{"before " + id[:4], id[4:] + " after"} {
			fmt.Fprintf(&out, "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"m\",\"output_index\":0,\"content_index\":0,\"sequence_number\":%d,\"delta\":%q}\n\n", i+1, p)
		}
		final := "before " + id + " after"
		event(fmt.Sprintf(`{"type":"response.output_text.done","item_id":"m","output_index":0,"content_index":0,"sequence_number":3,"text":%q}`, final))
		event(fmt.Sprintf(`{"type":"response.completed","sequence_number":4,"response":{"id":"r","status":"completed","output":[{"type":"message","id":"m","content":[{"type":"output_text","text":%q}]}],"usage":{"input_tokens":10,"output_tokens":11}}}`, final))
	} else {
		event(`{"type":"message_start","message":{"model":"claude-test","content":[],"usage":{"input_tokens":10}}}`)
		event(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		for _, p := range []string{"before " + id[:4], id[4:] + " after"} {
			event(fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}`, p))
		}
		event(`{"type":"content_block_stop","index":0}`)
		event(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":11}}`)
		event(`{"type":"message_stop"}`)
	}
	return out.String()
}

var wireToken = regexp.MustCompile(`\{\{SNU_[0-9a-f]{32}\}\}`)

func TestRestoreLogsConfirmWrittenValues(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		hit, echo  bool
	}{
		{"出站无命中", `{"model":"claude-test","messages":[{"role":"user","content":"hello"}],"stream":true}`, false, false},
		{"出站命中但响应无占位符", routeBody(false), true, false},
		{"响应写出回填文本", routeBody(false), true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := &syncBuffer{}
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				id := wireToken.Find(body)
				if err != nil || (id != nil) != tc.hit {
					t.Error("出站占位符与预期不符")
					http.Error(w, "fixture", 400)
					return
				}
				if !tc.echo {
					id = []byte("plain")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, routeSSE(string(id), false))
			}))
			t.Cleanup(up.Close)
			h, _ := newEndpointPipelineTestHandler(t, endpointPipelineConfig("logs", up.URL, 1))
			h.SetPrivacyFilter(newRestoreTestFilter(t))
			r := privacySummaryRequest("/v1/messages", tc.body, "req-log-test")
			r.Header.Set("Accept", "text/event-stream")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "message_stop") {
				t.Fatal("响应未成功交付")
			}
			output := logs.String()
			prefix := "[响应回填] [req-log-test]"
			if strings.Contains(output, "changed=true") != tc.hit {
				t.Fatal("出站日志与实际命中不符")
			}
			if !tc.echo {
				if strings.Contains(output, prefix) {
					t.Fatal("正常完成且回填计数全零时不应打印系统日志")
				}
				return
			}
			if strings.Count(output, prefix) != 1 || !strings.Contains(output, "🔄 "+prefix) {
				t.Fatalf("应能按请求关联出站和回填日志: %s", output)
			}
			start := strings.Index(output, prefix)
			line, _, _ := strings.Cut(output[start:], "\n")
			if !strings.Contains(line, "文本=1 工具参数=0") || strings.Contains(line, "private-quote") || strings.Contains(line, "{{SNU_") {
				t.Fatalf("回填摘要异常: %s", line)
			}
		})
	}
}

func TestRestoreLogOnlyForDeliveredReplacements(t *testing.T) {
	logs := &syncBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	logPrivacyDelivery("unknown-only", "/v1/responses", "websocket", handlers.RestoreSummary{Status: "completed", UnknownText: 1, UnknownTool: 2})
	logPrivacyDelivery("before-write", "/v1/responses", "websocket", handlers.RestoreSummary{Status: "interrupted"})
	if strings.Contains(logs.String(), "[响应回填]") {
		t.Fatal("仅检测到未知占位符或写出前中断时不应打印回填日志")
	}

	logPrivacyDelivery("after-write", "/v1/responses", "websocket", handlers.RestoreSummary{Status: "interrupted", Tool: 1})
	if !strings.Contains(logs.String(), "🔄 [响应回填] [after-write] interrupted，已写出 文本=0 工具参数=1") {
		t.Fatal("中断前已写出的工具参数应打印回填日志")
	}
}

func TestNonPassthroughRestoration(t *testing.T) {
	for _, route := range []string{"endpoint", "account", "pool-adapter"} {
		t.Run(route, func(t *testing.T) {
			codex := route != "endpoint"
			path := "/v1/messages"
			if codex {
				path = "/v1/responses"
			}
			callback := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				id := wireToken.Find(b)
				if len(id) == 0 || bytes.Contains(b, []byte("private-quote")) {
					t.Errorf("出站未正确遮蔽: %s", b)
					http.Error(w, "privacy", 400)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Content-Encoding", "gzip")
				z := gzip.NewWriter(w)
				_, _ = io.WriteString(z, routeSSE(string(id), codex))
				_ = z.Close()
			})
			server := httptest.NewServer(callback)
			defer server.Close()
			acc := &store.UpstreamAccountRecord{ID: 1, AccountName: "test", ProviderType: "api_key", CredentialRaw: "test", BaseURL: server.URL, Enabled: true}
			var rec *httptest.ResponseRecorder
			if route == "pool-adapter" {
				env := newSubscriptionPoolTestEnv(t, []*store.UpstreamAccountRecord{acc}, true)
				env.handler.passthrough.proxy.SetPrivacyFilter(newRestoreTestFilter(t))
				env.transport.responder = func(req *http.Request) (*http.Response, error) {
					r := httptest.NewRecorder()
					callback.ServeHTTP(r, req)
					return r.Result(), nil
				}
				rec = env.serve(t, http.MethodPost, "responses", func(r *http.Request) {
					b := routeBody(true)
					r.Body = io.NopCloser(strings.NewReader(b))
					r.ContentLength = int64(len(b))
					r.Header.Set("Accept", "text/event-stream")
				})
			} else {
				var h *Handler
				if codex {
					h = newAccountPipelineTestHandler(t, server.URL, &mockAccountPoolService{accounts: []*store.UpstreamAccountRecord{acc}})
				} else {
					h, _ = newEndpointPipelineTestHandler(t, endpointPipelineConfig("ordinary", server.URL, 1))
				}
				h.SetPrivacyFilter(newRestoreTestFilter(t))
				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(routeBody(codex)))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Accept", "text/event-stream")
				rec = httptest.NewRecorder()
				h.ServeHTTP(rec, req)
			}
			var visible strings.Builder
			for _, l := range strings.Split(rec.Body.String(), "\n") {
				if !strings.HasPrefix(l, "data:") {
					continue
				}
				raw := strings.TrimSpace(l[5:])
				switch gjson.Get(raw, "type").Str {
				case "response.output_text.delta":
					visible.WriteString(gjson.Get(raw, "delta").Str)
				case "content_block_delta":
					visible.WriteString(gjson.Get(raw, "delta.text").Str)
				case "response.completed":
					if got := gjson.Get(raw, "response.output.0.content.0.text").Str; got != "before "+routeTestValue+" after" {
						t.Fatalf("快照 %q", got)
					}
				}
			}
			if rec.Code != 200 || visible.String() != "before "+routeTestValue+" after" {
				t.Fatalf("交付失败 %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}
func TestSubscriptionUsesFixedMaskWhenRestoreDisabled(t *testing.T) {
	filter := newRestoreTestFilter(t)
	filter.Settings.ResponseRestoreEnabled = false
	h, _, _ := newClaudeSubscriptionTestHandler(t, func() string {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			if !bytes.Contains(b, []byte("[MASK]")) || wireToken.Match(b) {
				t.Errorf("Claude 直通正文 %s", b)
			}
			_, _ = io.WriteString(w, `{"type":"message","content":[]}`)
		}))
		t.Cleanup(srv.Close)
		return srv.URL
	}())
	h.SetPrivacyFilter(filter)
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(strings.Replace(routeBody(false), `"stream":true`, `"stream":false`, 1)))
	req = req.WithContext(endpoint.WithClaudeCodeEntry(req.Context(), endpoint.ClaudeCodeEntryRequest{Mode: endpoint.ClaudeCodeEntryModeSubscription}))
	req.Header.Set("Authorization", claudeSubscriptionTestToken)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	env := newSubscriptionTestEnv(t, nil, svc.CodexSubscriptionProxyModeSubscription)
	env.handler.passthrough.proxy.SetPrivacyFilter(filter)
	resp := env.request("POST", "responses", func(r *http.Request) {
		b := strings.Replace(routeBody(true), `"stream":true`, `"previous_response_id":{"bad":"type"}`, 1)
		r.Body = io.NopCloser(strings.NewReader(b))
		r.ContentLength = int64(len(b))
		r.Header.Set("Authorization", "Bearer test")
		r.Header.Set("Content-Type", "application/json")
	})
	defer resp.Body.Close()
	rs := env.upstreamRequests()
	if resp.StatusCode != 200 || len(rs) != 1 || !bytes.Contains(rs[0].Body, []byte("[MASK]")) {
		t.Fatal("Codex 直通误入回填路径", resp.StatusCode)
	}
}

func TestAccountRestoreFailureStopsLocally(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+`{"type":"response.function_call_arguments.delta","output_index":0,"item_id":"i","delta":"{\"key\":\"{{SNU_unknown}}\"}","sequence_number":0}`+"\n\n")
		_, _ = io.WriteString(w, "data: "+`{"type":"response.completed","response":{"output":[],"usage":{"input_tokens":10,"output_tokens":11}},"sequence_number":1}`+"\n\n")
	}))
	t.Cleanup(up.Close)
	acc := &store.UpstreamAccountRecord{ID: 1, AccountName: "test", ProviderType: "api_key", CredentialRaw: "test", BaseURL: up.URL, Enabled: true}
	pool := &mockAccountPoolService{accounts: []*store.UpstreamAccountRecord{acc}}
	h := newAccountPipelineTestHandler(t, up.URL, pool)
	h.SetPrivacyFilter(newRestoreTestFilter(t))
	request := func(b string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	raw := routeBody(true)
	rec := request(strings.Replace(raw, `"stream":true`, `"stream":true,"previous_response_id":{"bad":"type"}`, 1))
	if calls.Load() != 0 || !strings.Contains(rec.Body.String(), privacy.CodeInvalidRequest) {
		t.Fatal("无效引用已出站", calls.Load(), rec.Body.String())
	}
	rec = request(raw)
	if calls.Load() != 1 || !strings.Contains(rec.Body.String(), "privacy_restore_failed") || strings.Contains(rec.Body.String(), "response.completed") {
		t.Fatal("本地工具错误被当作成功", calls.Load(), rec.Body.String())
	}
	if len(pool.transientCalls)+len(pool.authFailCalls)+len(pool.softFailureCalls)+len(pool.successCalls) != 0 {
		t.Fatal("本地恢复失败影响上游调度")
	}
}

func TestNonPassthroughToolRestoration(t *testing.T) {
	for _, codex := range []bool{false, true} {
		t.Run(map[bool]string{true: "account", false: "endpoint"}[codex], func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				token := string(wireToken.Find(b))
				if token == "" {
					t.Error("出站没有可逆标识")
					w.WriteHeader(400)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				event := func(payload map[string]any) { data, _ := json.Marshal(payload); fmt.Fprintf(w, "data: %s\n\n", data) }
				if !codex {
					event(map[string]any{"type": "message_start", "message": map[string]any{"content": []any{}, "usage": map[string]int{"input_tokens": 2}}})
					event(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "name": "fixture", "input": map[string]any{}}})
				}
				parts := []string{`{"argument":["` + token[:7], token[7:] + `"]}`}
				for i, p := range parts {
					if codex {
						event(map[string]any{"type": "response.function_call_arguments.delta", "output_index": 0, "item_id": "fc", "delta": p, "sequence_number": i})
					} else {
						event(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": p}})
					}
				}
				if codex {
					args := strings.Join(parts, "")
					event(map[string]any{"type": "response.function_call_arguments.done", "output_index": 0, "item_id": "fc", "arguments": args, "sequence_number": 2})
					event(map[string]any{"type": "response.completed", "sequence_number": 3, "response": map[string]any{"output": []any{map[string]any{"type": "function_call", "id": "fc", "arguments": args}}, "usage": map[string]int{"input_tokens": 2, "output_tokens": 3}}})
				} else {
					event(map[string]any{"type": "content_block_stop", "index": 0})
					event(map[string]any{"type": "message_delta", "delta": map[string]string{"stop_reason": "tool_use"}, "usage": map[string]int{"output_tokens": 3}})
					event(map[string]any{"type": "message_stop"})
				}
			}))
			t.Cleanup(up.Close)
			var h *Handler
			path := "/v1/messages"
			if codex {
				path = "/v1/responses"
				acc := &store.UpstreamAccountRecord{ID: 1, AccountName: "tool", ProviderType: "api_key", CredentialRaw: "test", BaseURL: up.URL, Enabled: true}
				h = newAccountPipelineTestHandler(t, up.URL, &mockAccountPoolService{accounts: []*store.UpstreamAccountRecord{acc}})
			} else {
				h, _ = newEndpointPipelineTestHandler(t, endpointPipelineConfig("tool", up.URL, 1))
			}
			h.SetPrivacyFilter(newRestoreTestFilter(t))
			req := httptest.NewRequest("POST", path, strings.NewReader(routeBody(codex)))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "text/event-stream")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			var args strings.Builder
			done := false
			for _, l := range strings.Split(rec.Body.String(), "\n") {
				if !strings.HasPrefix(l, "data:") {
					continue
				}
				p := strings.TrimSpace(l[5:])
				switch gjson.Get(p, "type").Str {
				case "content_block_delta":
					args.WriteString(gjson.Get(p, "delta.partial_json").Str)
				case "response.function_call_arguments.delta":
					args.WriteString(gjson.Get(p, "delta").Str)
				case "response.completed":
					done = gjson.Get(p, "response.output.0.arguments").Str == args.String()
				case "message_stop":
					done = true
				}
			}
			if !done || !json.Valid([]byte(args.String())) || gjson.Get(args.String(), "argument.0").Str != routeTestValue {
				t.Fatal("未能向客户端交付正确的工具调用", rec.Body.String())
			}
		})
	}
}

func TestRetryPublishesOnlyDispatchedBody(t *testing.T) {
	filter := newRestoreTestFilter(t)
	filter.Rules[0].Scope.EndpointNames = []string{"first"}
	state := handlers.NewPrivacyRequestState("retry")
	t.Cleanup(state.ReleaseRestore)
	base := httptest.NewRequest("POST", "/v1/messages", nil)
	base = base.WithContext(handlers.WithPrivacyRequestState(base.Context(), state))
	run := func(name string, status int) ([]byte, *privacy.RestoreView) {
		attempt := handlers.WithPrivacyAttempt(base, true)
		req := privacy.Request{Path: base.URL.Path, Method: "POST", UpstreamType: privacy.UpstreamTypeEndpoint, EndpointName: name}
		body, err := handlers.ApplyPrivacyFilter(filter, attempt, req, []byte(routeBody(false)))
		if err != nil {
			t.Fatal(err)
		}
		if status == 200 {
			if err := handlers.PublishPrivacyAttempt(attempt); err != nil {
				t.Fatal(err)
			}
		}
		resp := &http.Response{StatusCode: status, Body: http.NoBody}
		handlers.BindPrivacyResponse(attempt, resp)
		_, view := handlers.ResponseRestore(resp)
		return body, view
	}
	first, failed := run("first", 503)
	if failed != nil || !wireToken.Match(first) {
		t.Fatal("失败的上游回复绑定了恢复表")
	}
	next, view := run("second", 200)
	if view == nil || wireToken.Match(next) {
		t.Fatal("换端点后沿用了其他 scope 的正文或恢复表")
	}
	if _, ok := view.Lookup(string(wireToken.Find(first))); ok {
		t.Fatal("已放弃的 attempt 泄漏映射")
	}
	retry, view := run("first", 200)
	if view == nil || !bytes.Equal(retry, first) {
		t.Fatal("重试缓存未同步返回正文与恢复表")
	}
	if v, ok := view.Lookup(string(wireToken.Find(retry))); !ok || v != routeTestValue {
		t.Fatal("重试映射错误")
	}
}

func TestRestoreUpstreamTruncation(t *testing.T) {
	for _, account := range []bool{false, true} {
		for _, stream := range []bool{true, false} {
			for _, enabled := range []bool{false, true} {
				t.Run(fmt.Sprintf("account=%t/stream=%t/restore=%t", account, stream, enabled), func(t *testing.T) {
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						if !stream {
							w.Header().Set("Content-Type", "application/json")
							w.Header().Set("Content-Length", "1024")
							io.WriteString(w, "{")
							return
						}
						w.Header().Set("Content-Type", "text/event-stream")
						if account {
							io.WriteString(w, "event: response.created\ndata: "+`{"type":"response.created","response":{"id":"r","output":[]}}`+"\n\n")
						} else {
							io.WriteString(w, "event: message_start\ndata: "+`{"type":"message_start","message":{"model":"test","content":[],"usage":{"input_tokens":7}}}`+"\n\n")
						}
					}))
					defer srv.Close()
					var h *Handler
					var tracker *tracking.UsageTracker
					if account {
						h, tracker = phaseAccountHandler(t, srv.URL, 1)
					} else {
						h, _ = newEndpointPipelineTestHandler(t, endpointPipelineConfig("test", srv.URL, 1))
						tracker = attachRetryTracker(t, h)
					}
					filter := newRestoreTestFilter(t)
					filter.Settings.ResponseRestoreEnabled = enabled
					h.SetPrivacyFilter(filter)
					var rec *httptest.ResponseRecorder
					if account {
						rec = performPhaseAccountRequest(h, stream)
					} else {
						rec = performEndpointPipelineRequest(t, h, stream)
					}
					data := onlyPhaseRequest(t, tracker)
					if enabled && gjson.Get(data.PrivacyScanJSON, "restore.status").Str != "interrupted" {
						t.Fatalf("回填摘要未标中断: %s", data.PrivacyScanJSON)
					}
					if stream {
						if data.Detail.Status != "completed" || data.Detail.FailureReason != "stream_truncated" || strings.Contains(rec.Body.String(), "event: error") {
							t.Fatalf("截断 SSE 状态错误 %s %s %s", data.Detail.Status, data.Detail.FailureReason, rec.Body)
						}
					} else if data.Detail.Status != "failed" || !strings.Contains(data.Detail.FailureReason, "response_read_error") {
						t.Fatalf("上游读取错误归因错误 %s %s", data.Detail.Status, data.Detail.FailureReason)
					}
					if account {
						pool := h.accountPoolService.(*mockAccountPoolService)
						want := svc.AccountScheduleOutcomeSuccess
						if !stream {
							want = svc.AccountScheduleOutcomeTransientFailure
						}
						if len(pool.completeCalls) == 0 || pool.completeCalls[0].outcome != want || (!stream && len(pool.softFailureCalls) != 1) {
							t.Fatalf("截断的账号结算错误 %+v soft=%d", pool.completeCalls, len(pool.softFailureCalls))
						}
					}
				})
			}
		}
	}
}

func TestAccountToolTailTruncation(t *testing.T) {
	const piece = `{"code":"func main() {`
	for _, sentinel := range []string{"", "data: [DONE]\n\n"} {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("done=%t/restore=%t", sentinel != "", enabled), func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: "+`{"type":"response.created","response":{"id":"r","output":[]}}`+"\n\n")
					fmt.Fprintf(w, "data: "+`{"type":"response.function_call_arguments.delta","output_index":0,"item_id":"fc","delta":%q,"sequence_number":1}`+"\n\n", piece)
					io.WriteString(w, sentinel)
				}))
				defer srv.Close()
				h, tracker := phaseAccountHandler(t, srv.URL, 1)
				filter := newRestoreTestFilter(t)
				filter.Settings.ResponseRestoreEnabled = enabled
				h.SetPrivacyFilter(filter)
				rec := performPhaseAccountRequest(h, true)
				data := onlyPhaseRequest(t, tracker)
				body := rec.Body.String()
				if rec.Code != http.StatusOK || data.Detail.FailureReason != "stream_truncated" || strings.Contains(body, "event: error") {
					t.Fatalf("工具截断结算错误: %d %s %s", rec.Code, data.Detail.FailureReason, body)
				}
				if enabled && gjson.Get(data.PrivacyScanJSON, "restore.status").Str != "interrupted" {
					t.Fatalf("回填摘要应记录中断: %s", data.PrivacyScanJSON)
				}
				if sentinel != "" && !strings.HasSuffix(body, sentinel) {
					t.Fatalf("[DONE] 应最后交付: %q", body)
				}
				var got strings.Builder
				for _, line := range strings.Split(body, "\n") {
					if raw, ok := strings.CutPrefix(line, "data: "); ok {
						got.WriteString(gjson.Get(raw, "delta").Str)
					}
				}
				if got.String() != piece {
					t.Fatalf("客户端收到的工具参数 = %q", got.String())
				}
			})
		}
	}
}
