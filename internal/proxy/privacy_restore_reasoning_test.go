package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"sinuo/internal/codexidentity"
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
