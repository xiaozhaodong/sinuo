package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func countTokensClaudeCodeBody(t *testing.T, repeats int) []byte {
	t.Helper()
	req := CountTokensRequest{
		Model:  "claude-test",
		System: []any{map[string]any{"type": "text", "text": strings.Repeat("系统", repeats)}},
		Messages: []map[string]any{{
			"role": "user",
			"content": []any{
				map[string]any{"type": "text", "text": strings.Repeat("消息", repeats)},
				map[string]any{"type": "tool_use", "id": "tool-1", "name": "query", "input": map[string]any{"query": strings.Repeat("调用", repeats)}},
				map[string]any{"type": "tool_result", "tool_use_id": "tool-1", "content": []any{map[string]any{"type": "text", "text": strings.Repeat("结果", repeats)}}},
				map[string]any{"type": "tool_result", "tool_use_id": "tool-2", "content": strings.Repeat("输出", repeats)},
			},
		}},
		Tools: []any{map[string]any{
			"name": "query", "description": strings.Repeat("定义", repeats),
			"input_schema": map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}},
		}},
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestCountTokensEstimate_ClaudeCodeBlocksGrowWithText(t *testing.T) {
	handler := &CountTokensHandler{}
	short, err := handler.estimateTokens(countTokensClaudeCodeBody(t, 10))
	if err != nil {
		t.Fatal(err)
	}
	long, err := handler.estimateTokens(countTokensClaudeCodeBody(t, 1000))
	if err != nil {
		t.Fatal(err)
	}
	if long <= short || long <= 3*(50+100) {
		t.Fatalf("块数组估算应随文本增长并明显超过旧固定开销: short=%d long=%d", short, long)
	}
}

func TestCountTokensEstimate_CountsSupportedShapesByRune(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{"旧消息字符串", `{"messages":[{"content":"hello world"}]}`, 52},
		{"Unicode 消息及系统字符串", `{"messages":[{"content":"你好abc🙂"}],"system":"系统xy"}`, 52},
		{"text 块", `{"messages":[{"content":[{"type":"text","text":"你好世界abcd"}]}]}`, 52},
		{"tool_use 的 JSON 输入", `{"messages":[{"content":[{"type":"tool_use","input":{"查询":"你好"}}]}]}`, 52},
		{"tool_result 字符串", `{"messages":[{"content":[{"type":"tool_result","content":"返回结果abcd"}]}]}`, 52},
		{"tool_result 文本块", `{"messages":[{"content":[{"type":"tool_result","content":[{"type":"text","text":"你好"},{"type":"text","text":"世界abcd"}]}]}]}`, 52},
		{"系统混合数组", `{"system":["字符串",{"type":"text","text":"系统abc"}]}`, 52},
		{"工具定义序列化", `{"tools":[{"name":"查"}]}`, 53},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := (&CountTokensHandler{}).estimateTokens([]byte(tc.body))
			if err != nil || got != tc.want {
				t.Fatalf("字符统计不正确: want=%d got=%d err=%v", tc.want, got, err)
			}
		})
	}
}

func TestCountTokensEstimate_IgnoresNonTextBlocks(t *testing.T) {
	body := []byte(`{
		"system":[{"type":"thinking","text":"忽略这些文本"}],
		"messages":[{"content":[
			{"type":"image","text":"忽略这些文本","source":{"data":"base64-image"}},
			{"type":"document","text":"忽略这些文本"},
			{"type":"thinking","thinking":"忽略这些文本"},
			{"type":"tool_result","content":[{"type":"image","text":"忽略这些文本"}]},
			{"type":"tool_use","name":"missing-input"}
		]}]
	}`)
	got, err := (&CountTokensHandler{}).estimateTokens(body)
	if err != nil || got != 50 {
		t.Fatalf("非文本块应仅保留基础开销: tokens=%d err=%v", got, err)
	}
}

func TestCountTokensEstimate_InvalidJSONReturnsError(t *testing.T) {
	handler := &CountTokensHandler{}
	body := []byte(`{"messages":`)
	if _, err := handler.estimateTokens(body); err == nil {
		t.Fatal("非法 JSON 应返回错误")
	}
	recorder := httptest.NewRecorder()
	handler.respondWithEstimation(recorder, body, "estimate-invalid", estimationReasonNoEligibleEndpoint)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("非法 JSON 的估算响应应为 400，得到 %d", recorder.Code)
	}
}

func TestCountTokensEstimate_FallbackCountsClaudeCodeBlocks(t *testing.T) {
	handler, _ := newCountTokensBaselineHandler(t)
	body := countTokensClaudeCodeBody(t, 1000)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", bytes.NewReader(body))
	recorder := httptest.NewRecorder()
	handler.Handle(context.Background(), recorder, req, body, "estimate-claude-code")
	requireCountTokensEstimation(t, recorder)
	var response CountTokensResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.InputTokens <= 3*(50+100) {
		t.Fatalf("实际降级响应不应漏算块数组: %d", response.InputTokens)
	}
}
