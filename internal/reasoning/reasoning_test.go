package reasoning

import (
	"encoding/json"
	"reflect"
	"testing"
)

func mustJSON(t *testing.T, raw string) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestParseClaudeRequest(t *testing.T) {
	cases := map[string]string{
		`{"thinking":{"type":"adaptive"},"output_config":{"effort":"xhigh"}}`: "thinking:adaptive effort:xhigh",
		`{"output_config":{"effort":"High"}}`:                                 "effort:high",
		`{"thinking":{"type":"enabled","budget_tokens":16000}}`:               "thinking:enabled budget:16000",
		`{"thinking":{"type":"enabled","budget_tokens":"1.5"}}`:               "thinking:enabled",
		`{"thinking":{"type":"enabled","budget_tokens":-3}}`:                  "thinking:enabled",
		`{"thinking":{"type":"adaptive","budget_tokens":100}}`:                "thinking:adaptive",
		`{"thinking":{"type":"disabled"}}`:                                    "thinking:disabled",
		`{"thinking":{"type":"weird"}}`:                                       "",
		`{"thinking":"enabled"}`:                                              "",
		`{"model":"claude-sonnet-4-5"}`:                                       "",
		`{"output_config":{"effort":"x high"}}`:                               "",
	}
	for raw, want := range cases {
		if got := ParseClaudeRequest(mustJSON(t, raw)); got != want {
			t.Errorf("%s => %q, want %q", raw, got, want)
		}
	}
}

func TestParseCodexRequest(t *testing.T) {
	cases := map[string]string{
		`{"reasoning":{"effort":"xhigh"}}`: "effort:xhigh",
		`{"reasoning":{"effort":""}}`:      "",
		`{"reasoning":{"summary":"auto"}}`: "",
		`{"reasoning":"high"}`:             "",
		`{}`:                               "",
	}
	for raw, want := range cases {
		if got := ParseCodexRequest(mustJSON(t, raw)); got != want {
			t.Errorf("%s => %q, want %q", raw, got, want)
		}
	}
}

func TestParseRequestSpeed(t *testing.T) {
	cases := []struct{ path, body, want string }{
		{"/v1/responses", `{"service_tier":"priority"}`, "speed:priority"},
		{"/v1/responses", `{"reasoning":{"effort":"high"},"service_tier":"ultrafast"}`, "effort:high speed:ultrafast"},
		{"/v1/responses/compact", `{"service_tier":" Flex "}`, "speed:flex"},
		{"/v1/responses", `{"service_tier":"auto"}`, "speed:auto"},
		{"/v1/responses", `{"service_tier":"default"}`, "speed:default"},
		{"/v1/responses", `{"speed":"fast"}`, ""},
		{"/v1/responses", `{}`, ""},
		{"/v1/responses", `{"service_tier":"bad tier"}`, ""},
		{"/v1/responses", `{"service_tier":3}`, ""},
		{"/v1/messages", `{"thinking":{"type":"adaptive"},"speed":" Fast "}`, "thinking:adaptive speed:fast"},
		{"/v1/messages", `{"speed":"standard","service_tier":"priority"}`, "speed:standard"},
		{"/v1/messages/count_tokens", `{"speed":"fast"}`, "speed:fast"},
		{"/v1/messages", `{"service_tier":"priority"}`, ""},
		{"/v1/images/generations", `{"speed":"fast","service_tier":"priority"}`, ""},
	}
	for _, tc := range cases {
		if _, got := ParseRequest(tc.path, []byte(tc.body)); got != tc.want {
			t.Errorf("%s %s => %q, want %q", tc.path, tc.body, got, tc.want)
		}
	}
}

func TestResponseStateSpeedMergeAndOrder(t *testing.T) {
	var state ResponseState
	state.MergeCodexFields(CodexFields{Speed: "AUTO"})
	if !state.IsEmpty() || !(CodexFields{}).IsEmpty() || (CodexFields{Speed: "priority"}).IsEmpty() {
		t.Fatal("speed 字段的空状态不正确")
	}
	state.MergeCodexFields(ExtractCodexFields(mustJSON(t, `{"response":{"service_tier":"priority","reasoning":{"effort":"high"}}}`)))
	state.MergeCodexFields(ExtractCodexFields(mustJSON(t, `{"response":{"service_tier":"default","usage":{"output_tokens_details":{"reasoning_tokens":3}}}}`)))
	state.MergeCodexFields(CodexFields{Speed: "auto"})
	state.MergeCodexFields(CodexFields{})
	state.MarkThinkingBlock()
	if got := state.String(); got != "thinking:present effort:high reasoning_tokens:3 speed:default" {
		t.Fatalf("终态覆盖、空字段保留或序列化顺序错误: %q", got)
	}
	var claude ResponseState
	claude.MergeClaudeSpeed(" Fast ")
	claude.MarkClaudeContent(mustJSON(t, `{"content":[],"usage":{"speed":"standard","service_tier":"priority"}}`))
	claude.MergeClaudeSpeed("")
	if got := claude.String(); got != "thinking:absent speed:standard" {
		t.Fatalf("Claude 必须只合并 usage.speed: %q", got)
	}
}

func TestExtractCodexRawSpeedTakesLastNonAuto(t *testing.T) {
	for raw, want := range map[string]string{
		`{"service_tier":"priority"}{"service_tier":"default"}{"service_tier":"auto"}`: "default",
		`{"service_tier":"priority"}{"service_tier":"auto"}`:                           "priority",
		`{"service_tier":"auto"}`:     "",
		`{"service_tier":"Ultrafast"`: "ultrafast",
		`{"service_tier":"bad tier"}`: "",
		`{}`:                          "",
	} {
		if got := ExtractCodexFieldsFromRaw(raw).Speed; got != want {
			t.Errorf("raw speed = %q, want %q", got, want)
		}
	}
}

func TestParseRequestByPath(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","reasoning":{"effort":"high"},"thinking":{"type":"adaptive"}}`)
	model, reasoning := ParseRequest("/v1/responses", body)
	if model != "gpt-5.4" || reasoning != "effort:high" {
		t.Fatalf("codex: got %q %q", model, reasoning)
	}
	model, reasoning = ParseRequest("/v1/messages", body)
	if model != "gpt-5.4" || reasoning != "thinking:adaptive" {
		t.Fatalf("claude: got %q %q", model, reasoning)
	}
	model, reasoning = ParseRequest("/v1/images/generations", body)
	if model != "gpt-5.4" || reasoning != "" {
		t.Fatalf("other: got %q %q", model, reasoning)
	}
	if model, reasoning = ParseRequest("/v1/messages", []byte("not json")); model != "" || reasoning != "" {
		t.Fatalf("invalid json must yield empty, got %q %q", model, reasoning)
	}
}

func TestResponseStateMergeByField(t *testing.T) {
	var state ResponseState
	state.MergeCodexFields(ExtractCodexFields(mustJSON(t, `{"type":"response.created","response":{"reasoning":{"effort":"xhigh"}}}`)))
	if got := state.String(); got != "effort:xhigh" {
		t.Fatalf("after created: %q", got)
	}
	state.MergeCodexFields(ExtractCodexFields(mustJSON(t, `{"type":"response.completed","response":{"usage":{"output_tokens_details":{"reasoning_tokens":1234}}}}`)))
	if got := state.String(); got != "effort:xhigh reasoning_tokens:1234" {
		t.Fatalf("after completed: %q", got)
	}
	// 缺失字段不覆盖
	state.MergeCodexFields(ExtractCodexFields(mustJSON(t, `{"type":"response.in_progress","response":{}}`)))
	if got := state.String(); got != "effort:xhigh reasoning_tokens:1234" {
		t.Fatalf("missing fields must not clear: %q", got)
	}
	// 顶层对象（非流式）
	var flat ResponseState
	flat.MergeCodexFields(ExtractCodexFields(mustJSON(t, `{"object":"response","reasoning":{"effort":"medium"},"usage":{"output_tokens_details":{"reasoning_tokens":7}}}`)))
	if got := flat.String(); got != "effort:medium reasoning_tokens:7" {
		t.Fatalf("flat: %q", got)
	}
}

func TestResponseStateClaudeThreeStates(t *testing.T) {
	var incomplete ResponseState
	if got := incomplete.String(); got != "" {
		t.Fatalf("incomplete without block must be empty, got %q", got)
	}
	var absent ResponseState
	absent.MarkComplete()
	if got := absent.String(); got != "thinking:absent" {
		t.Fatalf("absent: %q", got)
	}
	var present ResponseState
	present.MarkThinkingBlock()
	if got := present.String(); got != "thinking:present" {
		t.Fatalf("present (incomplete): %q", got)
	}
	present.MarkComplete()
	if got := present.String(); got != "thinking:present" {
		t.Fatalf("present (complete): %q", got)
	}
	var scanned ResponseState
	scanned.MarkClaudeContent(mustJSON(t, `{"content":[{"type":"text","text":"hi"},{"type":"redacted_thinking","data":"x"}]}`))
	if got := scanned.String(); got != "thinking:present" {
		t.Fatalf("scanned: %q", got)
	}
}

func TestExtractCodexFieldsFromRaw(t *testing.T) {
	fields := ExtractCodexFieldsFromRaw(`{"type":"response.completed","response":{"reasoning":{"effort":"Low"},"usage":{"output_tokens_details":{"reasoning_tokens":42}}`)
	if fields.Effort != "low" || fields.ReasoningTokens == nil || *fields.ReasoningTokens != 42 {
		t.Fatalf("raw extract = %+v", fields)
	}
}

// 尾部窗口串联多个事件时取最后一个匹配：created 回显 high、后续均为 low → 终值 low。
func TestExtractCodexFieldsFromRawTakesLastMatch(t *testing.T) {
	raw := `{"type":"response.created","response":{"reasoning":{"effort":"high"}}}` +
		`{"type":"response.in_progress","response":{"reasoning":{"effort":"low"}}}` +
		`{"id":"resp_1","status":"completed","reasoning":{"effort":"low"},"usage":{"output_tokens_details":{"reasoning_tokens":7}}}`
	fields := ExtractCodexFieldsFromRaw(raw)
	if fields.Effort != "low" || fields.ReasoningTokens == nil || *fields.ReasoningTokens != 7 {
		t.Fatalf("raw extract must take last match, got %+v", fields)
	}
}

func TestParseRoundTrip(t *testing.T) {
	got := Parse("thinking:adaptive effort:xhigh bogus  :x y:")
	want := map[string]string{"thinking": "adaptive", "effort": "xhigh"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Parse = %v (keys %v), want %v", got, sortedKeys(got), want)
	}
}

func TestParseRequestEdgeCases(t *testing.T) {
	cases := []struct {
		name, path, body, wantModel, wantReasoning string
	}{
		{"model not string", "/v1/messages", `{"model":123,"thinking":{"type":"adaptive"}}`, "", "thinking:adaptive"},
		{"thinking is string", "/v1/messages", `{"model":"m","thinking":"enabled"}`, "m", ""},
		{"reasoning is string", "/v1/responses", `{"model":"m","reasoning":"high"}`, "m", ""},
		{"budget as string", "/v1/messages", `{"model":"m","thinking":{"type":"enabled","budget_tokens":"2048"}}`, "m", "thinking:enabled budget:2048"},
		{"model with spaces", "/v1/messages", `{"model":"  m  "}`, "m", ""},
		{"top-level array", "/v1/messages", `[1,2,3]`, "", ""},
		{"trailing garbage", "/v1/messages", `{"model":"m"} x`, "m", ""},
	}
	for _, tc := range cases {
		model, reasoning := ParseRequest(tc.path, []byte(tc.body))
		if model != tc.wantModel || reasoning != tc.wantReasoning {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", tc.name, model, reasoning, tc.wantModel, tc.wantReasoning)
		}
	}
}

// largeClaudeBody 构造带大段 tool_result 的请求体，模拟真实 Claude Code 对话（数百 KB）。
func largeClaudeBody(t testing.TB, blocks int) []byte {
	t.Helper()
	chunk := make([]byte, 4096)
	for i := range chunk {
		chunk[i] = 'a' + byte(i%26)
	}
	content := make([]map[string]any, 0, blocks)
	for i := 0; i < blocks; i++ {
		content = append(content, map[string]any{
			"type":        "tool_result",
			"tool_use_id": "toolu_x",
			"content":     string(chunk),
		})
	}
	body, err := json.Marshal(map[string]any{
		"model":         "claude-sonnet-4-5",
		"max_tokens":    8192,
		"thinking":      map[string]any{"type": "adaptive"},
		"output_config": map[string]any{"effort": "high"},
		"messages":      []map[string]any{{"role": "user", "content": content}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestParseRequestLargeBody(t *testing.T) {
	body := largeClaudeBody(t, 100)
	model, reasoning := ParseRequest("/v1/messages", body)
	if model != "claude-sonnet-4-5" || reasoning != "thinking:adaptive effort:high" {
		t.Fatalf("got %q %q (body %d bytes)", model, reasoning, len(body))
	}
}

// BenchmarkParseRequest 对比 gjson 单趟扫描与 v6.3 原全量 map 解码在约 400KB 请求体上的差异。
// largeClaudeBody 经 json.Marshal 按 key 排序，messages 排在 model / thinking 之前，属于必须跳过大块内容的最差情况。
func BenchmarkParseRequest(b *testing.B) {
	body := largeClaudeBody(b, 100)
	b.Run("gjson", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(body)))
		for i := 0; i < b.N; i++ {
			ParseRequest("/v1/messages", body)
		}
	})
	b.Run("full-map", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(body)))
		for i := 0; i < b.N; i++ {
			var payload map[string]any
			if err := json.Unmarshal(body, &payload); err != nil {
				b.Fatal(err)
			}
			_ = stringField(payload, "model")
			_ = ParseClaudeRequest(payload)
		}
	})
}
