package privacy

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func mustCollect(t *testing.T, body string, requestPath string) []textSegment {
	t.Helper()
	matcher := walkerForPath(requestPath)
	if matcher == nil {
		t.Fatalf("no walker for path %s", requestPath)
	}
	segments, err := collectJSONTextSegments([]byte(body), matcher)
	if err != nil {
		t.Fatalf("collect segments failed: %v", err)
	}
	return segments
}

func segmentValues(segments []textSegment) []string {
	values := make([]string, 0, len(segments))
	for _, seg := range segments {
		values = append(values, seg.Value)
	}
	return values
}

func TestClaudeWalkerCollectsExpectedFields(t *testing.T) {
	body := `{
		"model": "claude-sonnet-4",
		"system": "system text",
		"messages": [
			{"role": "user", "content": "plain content"},
			{"role": "user", "content": [
				{"type": "text", "text": "block text"},
				{"type": "tool_result", "tool_use_id": "tu_1", "content": "tool result string"},
				{"type": "tool_result", "tool_use_id": "tu_2", "content": [
					{"type": "text", "text": "nested tool text"}
				]}
			]}
		],
		"tools": [{"name": "secret_tool", "description": "tool desc"}],
		"metadata": {"user_id": "meta-user"},
		"stream": true
	}`

	segments := mustCollect(t, body, "/v1/messages")
	got := segmentValues(segments)
	want := []string{"system text", "plain content", "block text", "tool result string", "nested tool text"}
	if len(got) != len(want) {
		t.Fatalf("segment count = %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("segment[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestClaudeWalkerSystemArray(t *testing.T) {
	body := `{"system": [{"type": "text", "text": "sys block"}], "messages": []}`
	segments := mustCollect(t, body, "/v1/messages")
	got := segmentValues(segments)
	if len(got) != 1 || got[0] != "sys block" {
		t.Fatalf("got %v, want [sys block]", got)
	}
}

func TestClaudeWalkerDoesNotScanToolsOrMetadata(t *testing.T) {
	body := `{
		"model": "m",
		"tools": [{"name": "t", "description": "secret-in-tools", "input_schema": {"type": "object"}}],
		"tool_choice": {"type": "tool", "name": "secret-choice"},
		"metadata": {"user_id": "secret-meta"},
		"messages": [{"role": "user", "content": [
			{"type": "tool_use", "id": "secret-id", "name": "secret-name", "input": {}}
		]}]
	}`
	segments := mustCollect(t, body, "/v1/messages")
	if len(segments) != 0 {
		t.Fatalf("expected no segments, got %v", segmentValues(segments))
	}
}

func TestClaudeWalkerScansToolUseInputStringLeaves(t *testing.T) {
	body := `{"messages": [{"role": "assistant", "content": [
		{"type": "tool_use", "id": "t1", "name": "Bash", "input": {"command": "curl -H token", "timeout": 5, "run_in_background": false}},
		{"type": "tool_use", "id": "t2", "name": "MultiEdit", "input": {"edits": [{"old_string": "old", "new_string": "new"}], "tags": ["a"]}},
		{"type": "server_tool_use", "id": "t3", "name": "web_search", "input": {"query": "q"}}
	]}]}`
	segments := mustCollect(t, body, "/v1/messages")
	got := segmentValues(segments)
	want := []string{"curl -H token", "old", "new", "a", "q"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("segments = %v, want %v", got, want)
	}
}

func TestCodexWalkerCollectsExpectedFields(t *testing.T) {
	body := `{
		"model": "gpt-5.4",
		"instructions": "instr text",
		"input": [
			{"type": "message", "role": "user", "content": [
				{"type": "input_text", "text": "input text block"}
			]},
			{"type": "message", "role": "user", "content": "string content"},
			{"type": "function_call_output", "call_id": "c1", "output": "tool output text"}
		],
		"tools": [{"type": "function", "name": "fn", "description": "tool-desc"}],
		"reasoning": {"effort": "medium"},
		"stream": true
	}`
	segments := mustCollect(t, body, "/v1/responses")
	got := segmentValues(segments)
	want := []string{"instr text", "input text block", "string content", "tool output text", "tool-desc"}
	if len(got) != len(want) {
		t.Fatalf("segment count = %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("segment[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestCodexWalkerInputString(t *testing.T) {
	body := `{"model": "gpt-5.4", "input": "raw input string"}`
	segments := mustCollect(t, body, "/v1/responses/compact")
	got := segmentValues(segments)
	if len(got) != 1 || got[0] != "raw input string" {
		t.Fatalf("got %v, want [raw input string]", got)
	}
}

func TestCodexWalkerCollectsToolArraysAndReplayText(t *testing.T) {
	body := `{
		"model":"gpt-6-astra",
		"input":[
			{"type":"function_call","name":"lookup","arguments":"{\"query\":\"ACME-SECRET-2\"}","encrypted_function_args":"opaque-secret"},
			{"type":"custom_tool_call","name":"shell","input":"echo ACME-SECRET-3"},
			{"type":"function_call_output","output":[{"type":"output_text","text":"tool result"},{"type":"computer_image","image_url":"data:image/png;base64,AAAA"}],"encrypted_output":"opaque-output"}
		],
		"tools":[{"type":"function","name":"lookup","description":"lookup description","parameters":{"type":"object","description":"root schema description","default":"root schema default","properties":{"query":{"type":"string","description":"query description","default":"default query","examples":["example query"]}},"required":["query"]}}],
		"additional_tools":[{"type":"function","name":"extra","description":"extra description","parameters":{"type":"object","properties":{"mode":{"type":"string","enum":["safe","fast"]}}}}]
	}`
	got := segmentValues(mustCollect(t, body, "/v1/responses"))
	want := []string{
		`{"query":"ACME-SECRET-2"}`,
		"echo ACME-SECRET-3",
		"tool result",
		"lookup description",
		"root schema description",
		"root schema default",
		"query description",
		"default query",
		"example query",
		"extra description",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("segment[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestCodexWalkerPreservesKnownOpaqueAndContractFields(t *testing.T) {
	body := `{"input":[{"encrypted_content":"looks-readable","encrypted_output":"opaque","encrypted_function_args":"opaque","content":[{"type":"input_image","image_url":"data:image/png;base64,SECRET"}]}],"tools":[{"name":"SECRET_NAME","parameters":{"properties":{"SECRET_KEY":{"enum":["SECRET_ENUM"]}}}}]}`
	if got := segmentValues(mustCollect(t, body, "/v1/responses")); len(got) != 0 {
		t.Fatalf("opaque/contract fields must stay outside scanning, got %v", got)
	}
}

func TestCodexWalkerSupportsWebSocketResponseCreateEnvelope(t *testing.T) {
	body := `{"type":"response.create","response":{"model":"gpt-6-astra","instructions":"system secret","input":[{"type":"message","content":[{"type":"input_text","text":"user secret"}]}]}}`
	got := segmentValues(mustCollect(t, body, "/v1/responses"))
	want := []string{"system secret", "user secret"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestImageGenerationWalkerOnlyCollectsPrompt(t *testing.T) {
	body := `{"model":"gpt-image-2","prompt":"sensitive prompt","metadata":{"note":"not scanned"}}`
	segments := mustCollect(t, body, "/v1/images/generations")
	got := segmentValues(segments)
	if len(got) != 1 || got[0] != "sensitive prompt" {
		t.Fatalf("got %v, want [sensitive prompt]", got)
	}
}

func TestImageEditJSONWalkerOnlyCollectsPrompt(t *testing.T) {
	body := `{"model":"gpt-image-2","prompt":"sensitive edit prompt","images":[{"image_url":"https://example.com/source.png"}],"metadata":{"note":"not scanned"}}`
	segments := mustCollect(t, body, "/v1/images/edits")
	if len(segments) != 1 || segments[0].Value != "sensitive edit prompt" {
		t.Fatalf("unexpected image edit segments: %+v", segments)
	}
}

func TestReplaceSegmentsKeepsUntouchedBytesIdentical(t *testing.T) {
	body := `{"model":"m-1.5",  "big": 9007199254740993, "messages":[{"role":"user","content":"secret"}], "stream":false, "temp": 0.30}`
	segments := mustCollect(t, body, "/v1/messages")
	if len(segments) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(segments))
	}
	out := replaceSegments([]byte(body), segments, map[int]string{0: "[已脱敏]"})

	want := strings.Replace(body, `"secret"`, `"[已脱敏]"`, 1)
	if string(out) != want {
		t.Fatalf("replaced body mismatch:\n got: %s\nwant: %s", out, want)
	}
	// 大整数与数字格式不被改写
	if !bytes.Contains(out, []byte("9007199254740993")) {
		t.Error("big integer was rewritten")
	}
	if !bytes.Contains(out, []byte("0.30")) {
		t.Error("number format was rewritten")
	}
}

func TestReplaceSegmentsNoHTMLEscape(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"keep <script> & a&b secret-token here"}]}`
	segments := mustCollect(t, body, "/v1/messages")
	out := replaceSegments([]byte(body), segments, map[int]string{0: "keep <script> & a&b [已脱敏] here"})
	if !bytes.Contains(out, []byte("<script>")) {
		t.Errorf("'<script>' was HTML-escaped: %s", out)
	}
	if !bytes.Contains(out, []byte("a&b")) {
		t.Errorf("'a&b' was HTML-escaped: %s", out)
	}
	if bytes.Contains(out, []byte("\\u003c")) || bytes.Contains(out, []byte("\\u0026")) {
		t.Errorf("unexpected HTML escaping in output: %s", out)
	}
}

func TestReplaceSegmentsEscapesSpecialCharacters(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"x"}]}`
	segments := mustCollect(t, body, "/v1/messages")
	out := replaceSegments([]byte(body), segments, map[int]string{0: "line1\nline2\t\"quoted\" \\slash 中文"})

	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("replaced body is invalid json: %v\n%s", err, out)
	}
	messages := decoded["messages"].([]any)
	content := messages[0].(map[string]any)["content"].(string)
	if content != "line1\nline2\t\"quoted\" \\slash 中文" {
		t.Errorf("roundtrip mismatch: %q", content)
	}
}

func TestWalkerHandlesEscapedStringsOffsets(t *testing.T) {
	// 命中段自身含转义字符时，span 定位必须覆盖完整 token
	body := `{"system": "pre \"mid\" post", "messages":[{"role":"user","content":"a\\b secret"}]}`
	segments := mustCollect(t, body, "/v1/messages")
	if len(segments) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(segments))
	}
	if segments[0].Value != `pre "mid" post` {
		t.Errorf("decoded value mismatch: %q", segments[0].Value)
	}
	out := replaceSegments([]byte(body), segments, map[int]string{1: `a\b [已脱敏]`})
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("replaced body is invalid json: %v\n%s", err, out)
	}
	content := decoded["messages"].([]any)[0].(map[string]any)["content"].(string)
	if content != `a\b [已脱敏]` {
		t.Errorf("content = %q", content)
	}
	if system := decoded["system"].(string); system != `pre "mid" post` {
		t.Errorf("system mutated: %q", system)
	}
}

func TestWalkerInvalidJSONReturnsError(t *testing.T) {
	matcher := walkerForPath("/v1/messages")
	if _, err := collectJSONTextSegments([]byte(`{"messages": [`), matcher); err == nil {
		t.Fatal("expected error for invalid json")
	}
}

func TestWalkerForUnsupportedPath(t *testing.T) {
	if walkerForPath("/v1/models") != nil {
		t.Fatal("unexpected walker for unsupported path")
	}
}

func TestCountTokensReusesClaudeWalker(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"user","content":"count me"}]}`
	segments := mustCollect(t, body, "/v1/messages/count_tokens")
	got := segmentValues(segments)
	if len(got) != 1 || got[0] != "count me" {
		t.Fatalf("got %v, want [count me]", got)
	}
}

// ---------------------------------------------------------------------------
// Codex /v1/alpha/search（订阅代理 P2a 新增分支）
// ---------------------------------------------------------------------------

func TestSearchWalkerCollectsInputAndCommandText(t *testing.T) {
	body := `{
		"id": "search-1",
		"model": "gpt-6-astra",
		"reasoning": {"effort": "low"},
		"input": [
			{"type": "message", "role": "user", "content": [
				{"type": "input_text", "text": "user asks about ACME-SECRET-1"}
			]},
			{"type": "message", "role": "assistant", "content": [
				{"type": "output_text", "text": "assistant recap"}
			]},
			{"type": "message", "role": "user", "content": "plain string content"}
		],
		"commands": {
			"search_query": [{"q": "first query", "recency": 7, "domains": ["example.com"]}, {"q": "second query"}],
			"image_query": [{"q": "image query"}],
			"open": [{"ref_id": "turn0search0", "lineno": 12}],
			"click": [{"ref_id": "turn0search1", "id": 3}],
			"find": [{"ref_id": "https://example.com/docs", "pattern": "install token"}],
			"response_length": "medium"
		},
		"settings": {"tenant": "should-not-scan"},
		"max_output_tokens": 4096
	}`
	segments := mustCollect(t, body, "/v1/alpha/search")
	got := segmentValues(segments)
	want := []string{
		"user asks about ACME-SECRET-1",
		"assistant recap",
		"plain string content",
		"first query",
		"second query",
		"image query",
		"install token",
	}
	if len(got) != len(want) {
		t.Fatalf("segment count = %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("segment[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSearchWalkerInputStringVariant(t *testing.T) {
	body := `{"id":"s","model":"m","input":"raw search context","commands":{"search_query":[{"q":"q1"}]}}`
	got := segmentValues(mustCollect(t, body, "/v1/alpha/search"))
	want := []string{"raw search context", "q1"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSearchWalkerKeepsReferencesAndControlFieldsUntouched(t *testing.T) {
	// 引用、ID、模型与设置不进入扫描；看似 base64 / 高熵的 ref_id 同样不匹配。
	body := `{
		"id": "aGlnaC1lbnRyb3B5LWlkPT0=",
		"model": "gpt-6-astra",
		"input": [],
		"commands": {
			"open": [{"ref_id": "aHR0cHM6Ly9leGFtcGxlLmNvbQ=="}],
			"click": [{"ref_id": "turn0search0", "id": 1}],
			"find": [{"ref_id": "turn0search2", "pattern": "needle"}],
			"finance": [{"ticker": "AAPL"}],
			"weather": [{"location": "Shanghai"}],
			"time": [{"utc_offset": "+08:00"}]
		},
		"settings": {"tenant": "t"}
	}`
	got := segmentValues(mustCollect(t, body, "/v1/alpha/search"))
	if len(got) != 1 || got[0] != "needle" {
		t.Fatalf("only find[].pattern should be collected, got %v", got)
	}
}

func TestSearchWalkerNestedJSONTextAndBigIntStayValidAfterReplace(t *testing.T) {
	// 文本里嵌套 JSON 字符串与大整数：替换后整包仍是合法 JSON，未命中字节保持一致。
	body := `{"id":"s","model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"{\"token\":\"SECRET\"}"}]}],"commands":{"search_query":[{"q":"SECRET","recency":18446744073709551615}]},"max_output_tokens":18446744073709551615}`
	segments := mustCollect(t, body, "/v1/alpha/search")
	if len(segments) != 2 {
		t.Fatalf("segment count = %d, want 2", len(segments))
	}
	replaced := replaceSegments([]byte(body), segments, map[int]string{
		0: `{"token":"[已脱敏]"}`,
		1: "[已脱敏]",
	})
	if !json.Valid(replaced) {
		t.Fatalf("replaced body is not valid JSON: %s", replaced)
	}
	if !bytes.Contains(replaced, []byte(`18446744073709551615`)) {
		t.Fatalf("big integer must stay byte-identical: %s", replaced)
	}
	if strings.Contains(string(replaced), "SECRET") {
		t.Fatalf("secret leaked after replace: %s", replaced)
	}
	var decoded struct {
		Input []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
		Commands struct {
			SearchQuery []struct {
				Q string `json:"q"`
			} `json:"search_query"`
		} `json:"commands"`
	}
	if err := json.Unmarshal(replaced, &decoded); err != nil {
		t.Fatalf("decode replaced body: %v", err)
	}
	if decoded.Input[0].Content[0].Text != `{"token":"[已脱敏]"}` || decoded.Commands.SearchQuery[0].Q != "[已脱敏]" {
		t.Fatalf("unexpected replaced values: %+v", decoded)
	}
}

func TestSearchWalkerArrayOutputTextBlocksInInput(t *testing.T) {
	// 数组文本块：多个 content 项都应被收集，非文本项（图片/密文）保持不匹配。
	body := `{"id":"s","model":"m","input":[{"type":"message","role":"user","content":[
		{"type":"input_text","text":"first"},
		{"type":"input_image","image_url":"data:image/png;base64,AAAA"},
		{"type":"input_text","text":"second"}
	]}],"commands":{"search_query":[{"q":"q"}]}}`
	got := segmentValues(mustCollect(t, body, "/v1/alpha/search"))
	want := []string{"first", "second", "q"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestWalkerForPathSearchRegistered(t *testing.T) {
	if walkerForPath("/v1/alpha/search") == nil {
		t.Fatal("search walker must be registered")
	}
}
