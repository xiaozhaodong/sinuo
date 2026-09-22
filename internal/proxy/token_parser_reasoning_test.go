package proxy

import (
	"strings"
	"testing"

	"cc-forwarder/internal/proxy/response"
)

func feedSSEV2(t *testing.T, tp *TokenParser, body string) *ParseResult {
	t.Helper()
	var last *ParseResult
	for _, line := range strings.Split(body, "\n") {
		if result := tp.ParseSSELineV2(line); result != nil {
			last = result
		}
	}
	return last
}

const claudeStreamHead = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-4-5-20250929","usage":{"input_tokens":10,"output_tokens":1}}}

`

const claudeStreamTail = `event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":20}}

event: message_stop
data: {"type":"message_stop"}

`

func TestTokenParserClaudeThinkingThreeStates(t *testing.T) {
	thinkingBlock := `event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

`
	redactedBlock := strings.ReplaceAll(thinkingBlock, `"type":"thinking","thinking":""`, `"type":"redacted_thinking","data":"x"`)
	textBlock := strings.ReplaceAll(thinkingBlock, `"type":"thinking","thinking":""`, `"type":"text","text":""`)

	cases := []struct {
		name string
		body string
		want string
	}{
		{"present", claudeStreamHead + thinkingBlock + claudeStreamTail, "thinking:present"},
		{"redacted", claudeStreamHead + redactedBlock + claudeStreamTail, "thinking:present"},
		{"absent", claudeStreamHead + textBlock + claudeStreamTail, "thinking:absent"},
		{"incomplete", claudeStreamHead + textBlock, ""},
		{"incomplete-present", claudeStreamHead + thinkingBlock, "thinking:present"},
	}
	for _, tc := range cases {
		tp := NewTokenParser()
		feedSSEV2(t, tp, tc.body)
		if got := tp.GetResponseReasoning(); got != tc.want {
			t.Errorf("%s: reasoning = %q, want %q", tc.name, got, tc.want)
		}
		if got := tp.GetResponseModel(); got != "claude-sonnet-4-5-20250929" {
			t.Errorf("%s: response model = %q", tc.name, got)
		}
	}
}

func TestTokenParserCodexMergesEffortAndReasoningTokens(t *testing.T) {
	body := `event: response.created
data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.5","reasoning":{"effort":"xhigh"}}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"hi"}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.5","usage":{"input_tokens":5,"output_tokens":9,"output_tokens_details":{"reasoning_tokens":1234}}}}

`
	tp := NewTokenParser()
	last := feedSSEV2(t, tp, body)
	if got := tp.GetResponseReasoning(); got != "effort:xhigh reasoning_tokens:1234" {
		t.Fatalf("reasoning = %q", got)
	}
	if last == nil || last.ResponseReasoning != "effort:xhigh reasoning_tokens:1234" || last.ResponseModel != "gpt-5.5" {
		t.Fatalf("ParseResult must carry accumulated state: %+v", last)
	}
	if tp.GetFinalUsage() == nil || tp.GetFinalUsage().OutputTokens != 9 {
		t.Fatalf("usage regression: %+v", tp.GetFinalUsage())
	}
}

func TestTokenParserResponseModelFiltersPlaceholders(t *testing.T) {
	tp := NewTokenParser()
	if got := tp.GetResponseModel(); got != "" {
		t.Fatalf("empty parser response model = %q", got)
	}
	tp.SetModelName("default")
	if got := tp.GetResponseModel(); got != "" {
		t.Fatalf("default must be filtered, got %q", got)
	}
	tp.Reset()
	feedSSEV2(t, tp, `event: response.created
data: {"type":"response.created","response":{"reasoning":{"effort":"low"}}}

`)
	if got := tp.GetResponseReasoning(); got != "effort:low" {
		t.Fatalf("reasoning after reset+created = %q", got)
	}
	tp.Reset()
	if got := tp.GetResponseReasoning(); got != "" {
		t.Fatalf("Reset must clear reasoning state, got %q", got)
	}
}

func TestParseResponsesEventJSONCarriesPerEventFields(t *testing.T) {
	result := ParseResponsesEventJSON("response.created", []byte(`{"type":"response.created","response":{"model":"gpt-5.5","reasoning":{"effort":"high"}}}`))
	if result == nil || result.ResponseReasoningFields.Effort != "high" || result.ResponseModel != "gpt-5.5" {
		t.Fatalf("per-event fields missing: %+v", result)
	}
	raw := ParseResponsesEventJSON("response.completed", []byte(`{"type":"response.completed","response":{"model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":2,"output_tokens_details":{"reasoning_tokens":8}}`))
	if raw == nil || raw.ResponseReasoningFields.ReasoningTokens == nil || *raw.ResponseReasoningFields.ReasoningTokens != 8 {
		t.Fatalf("raw fallback fields missing: %+v", raw)
	}
}

func newReasoningAnalyzer() *response.TokenAnalyzer {
	return response.NewTokenAnalyzer(nil, nil, &TokenParserProviderImpl{})
}

// usage 在 content_block_start(thinking) 与 message_stop 之前出现，仍需得到 thinking:present。
func TestAnalyzerSSEConsumesAllLinesAfterUsage(t *testing.T) {
	body := claudeStreamHead + `event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":20,"cache_creation_input_tokens":3}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_stop
data: {"type":"message_stop"}

`
	analysis := newReasoningAnalyzer().AnalyzeResponseUnified([]byte(body), "req-1", "ep")
	if analysis.TokenUsage == nil || analysis.TokenUsage.OutputTokens != 20 || analysis.TokenUsage.CacheCreationTokens != 3 {
		t.Fatalf("token usage regression: %+v", analysis.TokenUsage)
	}
	if analysis.ModelName != "claude-sonnet-4-5-20250929" || analysis.ResponseModel != "claude-sonnet-4-5-20250929" {
		t.Fatalf("model = %q / %q", analysis.ModelName, analysis.ResponseModel)
	}
	if analysis.ResponseReasoning != "thinking:present" {
		t.Fatalf("reasoning = %q, want thinking:present", analysis.ResponseReasoning)
	}
	usage, model := newReasoningAnalyzer().AnalyzeResponseForTokensUnified([]byte(body), "req-1", "ep")
	if usage == nil || usage.OutputTokens != 20 || model != "claude-sonnet-4-5-20250929" {
		t.Fatalf("legacy wrapper regression: %+v %q", usage, model)
	}
}

// 无 usage 的 SSE 仍返回响应模型与思考信息；ModelName 保留旧占位语义。
func TestAnalyzerSSEWithoutUsageStillReportsModel(t *testing.T) {
	body := claudeStreamHead + `event: message_stop
data: {"type":"message_stop"}

`
	analysis := newReasoningAnalyzer().AnalyzeResponseUnified([]byte(body), "req-2", "ep")
	if analysis.TokenUsage != nil {
		t.Fatalf("expected nil usage, got %+v", analysis.TokenUsage)
	}
	if analysis.ModelName != "no_token_sse" {
		t.Fatalf("legacy model placeholder changed: %q", analysis.ModelName)
	}
	if analysis.ResponseModel != "claude-sonnet-4-5-20250929" || analysis.ResponseReasoning != "thinking:absent" {
		t.Fatalf("diagnostics = %q / %q", analysis.ResponseModel, analysis.ResponseReasoning)
	}
}

func TestAnalyzerJSONClaudeAndCodex(t *testing.T) {
	claude := `{"id":"msg","type":"message","model":"claude-opus-5","content":[{"type":"thinking","thinking":"..."},{"type":"text","text":"hi"}],"usage":{"input_tokens":4,"output_tokens":6}}`
	analysis := newReasoningAnalyzer().AnalyzeResponseUnified([]byte(claude), "req-3", "ep")
	if analysis.TokenUsage == nil || analysis.TokenUsage.OutputTokens != 6 {
		t.Fatalf("claude usage: %+v", analysis.TokenUsage)
	}
	if analysis.ResponseModel != "claude-opus-5" || analysis.ResponseReasoning != "thinking:present" {
		t.Fatalf("claude diagnostics = %q / %q", analysis.ResponseModel, analysis.ResponseReasoning)
	}

	codexNoUsage := `{"id":"resp","object":"response","model":"gpt-5.5","reasoning":{"effort":"medium"},"output":[]}`
	analysis = newReasoningAnalyzer().AnalyzeResponseUnified([]byte(codexNoUsage), "req-4", "ep")
	if analysis.TokenUsage != nil {
		t.Fatalf("codex no-usage must yield nil usage, got %+v", analysis.TokenUsage)
	}
	if analysis.ResponseModel != "gpt-5.5" || analysis.ResponseReasoning != "effort:medium" {
		t.Fatalf("codex diagnostics = %q / %q", analysis.ResponseModel, analysis.ResponseReasoning)
	}
}

// 心跳注释过多会让统一解析走宽松 SSE 兜底；即便拿不到 usage，响应模型 / 思考信息也不能丢。
func TestTokenAnalyzerFallbackKeepsDiagnosticsWithoutUsage(t *testing.T) {
	var body strings.Builder
	for i := 0; i < 20; i++ {
		body.WriteString(": keep-alive\n")
	}
	body.WriteString(`event: response.created
data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.5","reasoning":{"effort":"low"}}}

`)
	analyzer := response.NewTokenAnalyzer(nil, nil, &TokenParserProviderImpl{})
	analysis := analyzer.AnalyzeResponseUnified([]byte(body.String()), "req-fallback", "ep")
	if analysis.TokenUsage != nil {
		t.Fatalf("no usage expected, got %+v", analysis.TokenUsage)
	}
	if analysis.ResponseModel != "gpt-5.5" || analysis.ResponseReasoning != "effort:low" {
		t.Fatalf("fallback dropped diagnostics: model=%q reasoning=%q modelName=%q", analysis.ResponseModel, analysis.ResponseReasoning, analysis.ModelName)
	}
}
