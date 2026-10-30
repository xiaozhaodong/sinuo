package proxy

import (
	"strings"
	"testing"
)

func phaseEvent(t *testing.T, event, payload string) *ParseResult {
	t.Helper()
	p := NewTokenParser()
	feedSSEV2(t, p, claudeStreamHead)
	return feedSSEV2(t, p, "event: "+event+"\ndata: "+payload+"\n\n")
}

func TestStreamPhases_ClaudeClassificationKeepsOutputFlags(t *testing.T) {
	for _, tc := range []struct {
		delta   string
		kind    ContentKind
		visible bool
	}{
		{`{"type":"text_delta","text":"sample"}`, ContentText, true},
		{`{"type":"thinking_delta","thinking":"sample"}`, ContentReasoning, false},
		{`{"type":"input_json_delta","partial_json":"{}"}`, ContentTool, false},
		{`{"type":"text_delta","text":""}`, ContentNone, false},
		{`{"type":"signature_delta","signature":"synthetic"}`, ContentNone, false},
	} {
		payload := `{"type":"content_block_delta","index":0,"delta":` + tc.delta + `}`
		result := phaseEvent(t, "content_block_delta", payload)
		if tc.kind == ContentNone && result == nil {
			continue
		}
		if result == nil || result.ContentKind != tc.kind || result.ContentBytes != len(payload) {
			t.Fatalf("分类=%+v, want %s", result, tc.kind)
		}
		if result.HasVisibleText != tc.visible || result.HasStreamOutput || result.HasFallbackOutput {
			t.Fatalf("旧输出标志变化: %+v", result)
		}
	}
}

func TestStreamPhases_ClaudeStartClassification(t *testing.T) {
	for _, typ := range []string{"tool_use", "server_tool_use", "redacted_thinking", "thinking", "text"} {
		payload := `{"type":"content_block_start","index":0,"content_block":{"type":"` + typ + `"}}`
		result := phaseEvent(t, "content_block_start", payload)
		if typ == "thinking" || typ == "text" {
			if result != nil {
				t.Fatalf("空块不是内容: %+v", result)
			}
			continue
		}
		kind := ContentTool
		if typ == "redacted_thinking" {
			kind = ContentNone
		}
		if result == nil || result.ContentKind != kind || result.ContentBytes != len(payload) || result.ReasoningHidden != (typ == "redacted_thinking") {
			t.Fatalf("%s: %+v", typ, result)
		}
		if result.HasVisibleText || result.HasStreamOutput || result.HasFallbackOutput {
			t.Fatal("旧标志被改写")
		}
	}
}

func TestStreamPhases_ResponsesClassificationAndSSEParity(t *testing.T) {
	for _, tc := range []struct {
		event   string
		kind    ContentKind
		visible bool
	}{
		{"response.output_text.delta", ContentText, true}, {"response.refusal.delta", ContentText, false},
		{"response.reasoning_summary_text.delta", ContentReasoning, false}, {"response.reasoning_text.delta", ContentReasoning, false},
		{"response.function_call_arguments.delta", ContentTool, false}, {"response.custom_tool_call_input.delta", ContentTool, false},
	} {
		payload := `{"type":"` + tc.event + `","delta":"sample"}`
		direct := ParseResponsesEventJSON(tc.event, []byte(payload))
		viaSSE := phaseEvent(t, tc.event, payload)
		for _, result := range []*ParseResult{direct, viaSSE} {
			if result == nil || result.ContentKind != tc.kind || result.ContentBytes != len(payload) || !result.HasStreamOutput {
				t.Fatalf("%s: %+v", tc.event, result)
			}
			if result.HasVisibleText != tc.visible {
				t.Fatalf("%s 旧可见标志=%v", tc.event, result.HasVisibleText)
			}
		}
		empty := ParseResponsesEventJSON(tc.event, []byte(strings.Replace(payload, `"sample"`, `""`, 1)))
		if empty.ContentKind != ContentNone {
			t.Fatal("空增量不能记首个内容")
		}
	}
}

func TestStreamPhases_ResponsesItems(t *testing.T) {
	for _, tc := range []struct {
		item   string
		kind   ContentKind
		hidden bool
	}{
		{`{"type":"function_call"}`, ContentTool, false}, {`{"type":"custom_tool_call"}`, ContentTool, false},
		{`{"type":"reasoning","encrypted_content":"synthetic"}`, ContentNone, true},
		{`{"type":"reasoning","encrypted_content":""}`, ContentNone, false},
		{`{"type":"message","content":[{"type":"output_text","text":"sample"}]}`, ContentNone, false},
	} {
		payload := `{"type":"response.output_item.added","item":` + tc.item + `}`
		result := ParseResponsesEventJSON("response.output_item.added", []byte(payload))
		if result.ContentKind != tc.kind || result.ReasoningHidden != tc.hidden || result.ContentBytes != len(payload) {
			t.Fatalf("%+v", result)
		}
		if strings.Contains(tc.item, "sample") && !result.HasVisibleText {
			t.Fatal("保留已有message item正文提取语义")
		}
	}
}

func TestStreamPhases_StopReasons(t *testing.T) {
	claude := phaseEvent(t, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":1}}`)
	if claude.StopReason != "max_tokens" {
		t.Fatalf("%+v", claude)
	}
	for _, tc := range []struct{ event, payload, want string }{
		{"response.completed", `{"response":{"status":"completed"}}`, "completed"},
		{"response.incomplete", `{"response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`, "incomplete:max_output_tokens"},
		{"response.completed", `{"response":{}}`, "completed"},
	} {
		if result := ParseResponsesEventJSON(tc.event, []byte(tc.payload)); result.StopReason != tc.want {
			t.Fatalf("%+v", result)
		}
	}
	for raw, want := range map[string]string{
		`{"stop_reason":"tool_use"}`: "tool_use", `{"status":"completed"}`: "completed",
		`{"status":"incomplete","incomplete_details":{"reason":"content_filter"}}`: "incomplete:content_filter",
	} {
		if got := regularStopReason([]byte(raw)); got != want {
			t.Fatalf("%q != %q", got, want)
		}
	}
}

func TestStreamPhases_FlushMissingBlankLine(t *testing.T) {
	p := NewTokenParser()
	feedSSEV2(t, p, claudeStreamHead)
	p.ParseSSELineV2("event: content_block_delta")
	p.ParseSSELineV2(`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"synthetic"}}`)
	if result := p.FlushPendingEvent(); result == nil || result.ContentKind != ContentReasoning || result.ContentBytes == 0 || result.HasVisibleText || result.HasStreamOutput {
		t.Fatalf("flush丢失分类或改写旧标志: %+v", result)
	}
	p.ParseSSELineV2("event: message_delta")
	p.ParseSSELineV2(`data: {"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":1}}`)
	if result := p.FlushPendingEvent(); result == nil || result.StopReason != "max_tokens" {
		t.Fatalf("flush丢失结束原因: %+v", result)
	}
}
