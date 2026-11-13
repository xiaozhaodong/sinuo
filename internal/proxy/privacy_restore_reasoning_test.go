package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tidwall/gjson"

	"sinuo/internal/codexidentity"
	"sinuo/internal/privacy"
	"sinuo/internal/store"
)

// 用第一轮真实交付的 item 构造下一轮请求。上游 transport 完全在内存内。
func TestOAuthPoolRestoresReasoningReplay(t *testing.T) {
	env := newSubscriptionPoolTestEnv(t, []*store.UpstreamAccountRecord{identityAccount(t, 1)}, true)
	h := env.handler.proxy
	policy := enabledIdentityPolicy(t)
	h.SetCodexIdentityProvider(identityProviderFunc(func(context.Context) (codexidentity.Policy, error) { return policy, nil }))
	h.SetPrivacyFilter(newRestoreTestFilter(t))
	const ciphertext = "b3BhcXVlLXNlYWxlZC1wYXlsb2FkLw=="
	var firstToken, replayedItem string
	var attempts int
	env.transport.responder = func(r *http.Request) (*http.Response, error) {
		attempts++
		b, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		user := gjson.GetBytes(b, "input.0.content.0.text").Str
		switch attempts {
		case 1:
			if !wireToken.MatchString(user) {
				t.Fatal("首轮明文没有遮蔽")
			}
			firstToken = user
		case 2:
			if user != firstToken || gjson.GetBytes(b, "input.2.content.0.text").Str != firstToken {
				t.Fatal("重放的用户明文和已回填的助手明文未稳定遮蔽")
			}
		case 3:
			if user != "ordinary" {
				t.Fatal("无命中的明文被改写")
			}
		default:
			t.Fatal("意外的上游重试")
		}
		if attempts > 1 && gjson.GetBytes(b, "input.1").Raw != replayedItem {
			t.Fatal("出站改动了客户端重放的推理对象")
		}
		reason := map[string]any{"type": "reasoning", "id": "rs", "encrypted_content": ciphertext,
			"summary": []any{map[string]string{"type": "summary_text", "text": firstToken}}}
		msg := map[string]any{"type": "message", "role": "assistant", "id": "m", "content": []any{map[string]string{"type": "output_text", "text": firstToken}}}
		var stream strings.Builder
		seq := 0
		event := func(typ string, fields map[string]any) {
			fields["type"], fields["sequence_number"] = typ, seq
			seq++
			line, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&stream, "data: %s\n\n", line)
		}
		event("response.created", map[string]any{"response": map[string]any{"id": "r", "output": []any{}}})
		event("response.output_item.done", map[string]any{"output_index": 0, "item": reason})
		event("response.output_text.delta", map[string]any{"item_id": "m", "output_index": 1, "content_index": 0, "delta": firstToken})
		event("response.output_text.done", map[string]any{"item_id": "m", "output_index": 1, "content_index": 0, "text": firstToken})
		out := []any{reason, msg}
		if attempts > 1 {
			args := fmt.Sprintf(`{"value":%q}`, firstToken)
			event("response.function_call_arguments.delta", map[string]any{"output_index": 2, "item_id": "fc", "delta": args})
			event("response.function_call_arguments.done", map[string]any{"output_index": 2, "item_id": "fc", "arguments": args})
			out = append(out, map[string]string{"type": "function_call", "id": "fc", "name": "fixture", "arguments": args})
		}
		event("response.completed", map[string]any{"response": map[string]any{"id": "r", "status": "completed", "output": out, "usage": map[string]int{"input_tokens": 8, "output_tokens": 5}}})
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(stream.String())), Request: r}, nil
	}
	send := func(input string) string {
		t.Helper()
		req := identityRequestFixture(true, `{"model":"gpt-5","stream":true,"input":[`+input+`]}`)
		req.Header.Set("Accept", "text/event-stream")
		rec := httptest.NewRecorder()
		env.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("请求未进入流式交付 %d %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	completion := func(sse string) gjson.Result {
		t.Helper()
		for _, line := range strings.Split(sse, "\n") {
			if data, ok := strings.CutPrefix(line, "data: "); ok && gjson.Get(data, "type").Str == "response.completed" {
				return gjson.Get(data, "response.output")
			}
		}
		t.Fatal("缺少成功终态", sse)
		return gjson.Result{}
	}
	user := func(s string) string {
		return fmt.Sprintf(`{"type":"message","role":"user","content":[{"type":"input_text","text":%q}]}`, s)
	}
	first := completion(send(user(routeTestValue)))
	replayedItem = first.Get("0").Raw
	if first.Get("0.encrypted_content").Str != ciphertext || first.Get("0.summary.0.text").Str != firstToken || first.Get("1.content.0.text").Str != routeTestValue {
		t.Fatal("首轮应保留推理项并还原可见回答")
	}
	second := completion(send(strings.Join([]string{user(routeTestValue), replayedItem, first.Get("1").Raw, user("repeat")}, ",")))
	if second.Get("1.content.0.text").Str != routeTestValue || gjson.Get(second.Get("2.arguments").Str, "value").Str != routeTestValue {
		t.Fatal("第二轮文本或工具参数没有按本轮映射恢复")
	}
	foreign := send(strings.Join([]string{user("ordinary"), replayedItem, user("repeat")}, ","))
	if attempts != 3 || gjson.Get(completion(foreign).Get("2.arguments").Str, "value").Str != routeTestValue {
		t.Fatal("本轮零替换也应正常还原已知的工具值")
	}
}

// 从真实端点入口验证空签名思考出站已脱敏，普通回答仍可回填；有签名命中保持零出站。
func TestEndpointRestoreClaudeThinkingSignature(t *testing.T) {
	for _, tc := range []struct {
		name, signature string
		blocked         bool
	}{
		{"missing", "", false},
		{"empty", `,"signature":""`, false},
		{"signed", `,"signature":"opaque"`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				body, err := io.ReadAll(r.Body)
				token := gjson.GetBytes(body, "messages.0.content.0.thinking").Str
				if err != nil || !wireToken.MatchString(token) || strings.Contains(string(body), routeTestValue) {
					t.Error("空签名思考明文未在出站前遮蔽")
					http.Error(w, "fixture", 400)
					return
				}
				if gjson.GetBytes(body, "messages.0.content.0.signature").Raw != gjson.Get(`{`+strings.TrimPrefix(tc.signature, ",")+`}`, "signature").Raw {
					t.Error("签名字段被改写")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, routeSSE(token, false))
			}))
			t.Cleanup(up.Close)
			h, _ := newEndpointPipelineTestHandler(t, endpointPipelineConfig("thinking", up.URL, 1))
			filter := newRestoreTestFilter(t)
			filter.Settings.OnError = privacy.OnErrorFailClosed
			h.SetPrivacyFilter(filter)
			raw := fmt.Sprintf(`{"model":"claude-test","stream":true,"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":%q%s}]},{"role":"user","content":"continue"}]}`, routeTestValue, tc.signature)
			req := privacySummaryRequest("/v1/messages", raw, "req-thinking-test")
			req.Header.Set("Accept", "text/event-stream")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if tc.blocked {
				if rec.Code != 422 || calls.Load() != 0 || !strings.Contains(rec.Body.String(), "privacy_restore_rejected") {
					t.Fatal("有签名思考命中未保持零出站拒绝")
				}
				return
			}
			if rec.Code != http.StatusOK || calls.Load() != 1 || !strings.Contains(rec.Body.String(), "message_stop") {
				t.Fatalf("空签名思考请求未完成: status=%d calls=%d", rec.Code, calls.Load())
			}
			var text strings.Builder
			for _, line := range strings.Split(rec.Body.String(), "\n") {
				if data, ok := strings.CutPrefix(line, "data: "); ok && gjson.Get(data, "delta.type").Str == "text_delta" {
					text.WriteString(gjson.Get(data, "delta.text").Str)
				}
			}
			if text.String() != "before "+routeTestValue+" after" {
				t.Fatal("空签名思考映射未用于普通回答回填")
			}
		})
	}
}

func TestEndpointSignedThinkingFailOpen(t *testing.T) {
	var seen []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"type":"message","content":[{"type":"text","text":"{{SNU_0123456789abcdef0123456789abcdef}}"}]}`)
	}))
	defer up.Close()
	h, _ := newEndpointPipelineTestHandler(t, endpointPipelineConfig("thinking-open", up.URL, 1))
	h.SetPrivacyFilter(newRestoreTestFilter(t))
	raw := fmt.Sprintf(`{"model":"claude-test","messages":[{"role":"assistant","content":[{"type":"thinking","signature":"opaque","thinking":%q}]},{"role":"user","content":%q}]}`, routeTestValue, routeTestValue)
	req := privacySummaryRequest("/v1/messages", raw, "signed-open")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || string(seen) != raw || !strings.Contains(rec.Body.String(), "{{SNU_0123456789abcdef0123456789abcdef}}") {
		t.Fatal("原文放行时改写了正文或回填了响应", rec.Code, rec.Body.String())
	}
}
