package proxy

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sinuo/internal/proxy/response"
	"sinuo/internal/tracking"
)

var phaseBenchmarkResult *ParseResult

// 使用同一公开入口比较优化前后，覆盖每条增量的解析和分配。
func BenchmarkStreamPhases_ClaudeDelta(b *testing.B) {
	for _, size := range []int{16, 128, 4096} {
		b.Run(fmt.Sprintf("text_%d", size), func(b *testing.B) {
			parser := NewTokenParser()
			parser.partialUsage = &tracking.TokenUsage{InputTokens: 1}
			parser.contentBlockStates = map[int]bool{0: false}
			line := `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + strings.Repeat("x", size) + `"}}`
			b.ReportAllocs()
			b.SetBytes(int64(len(line)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				parser.ParseSSELineV2("event: content_block_delta")
				parser.ParseSSELineV2(line)
				phaseBenchmarkResult = parser.ParseSSELineV2("")
			}
		})
	}
}

// 本地非流式处理：含读取、统一分析和写回，不包含网络、数据库与日志 I/O。
func BenchmarkStreamPhases_RegularResponse(b *testing.B) {
	logger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})))
	b.Cleanup(func() { slog.SetDefault(logger) })
	handler := &Handler{responseProcessor: response.NewProcessor(), tokenAnalyzer: newReasoningAnalyzer()}
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	for _, size := range []int{64 << 10, 1 << 20} {
		body := `{"object":"response","model":"synthetic-model","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"` + strings.Repeat("x", size) + `"}]}],"usage":{"input_tokens":10,"output_tokens":100}}`
		b.Run(fmt.Sprintf("bytes_%d", size), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			for i := 0; i < b.N; i++ {
				upstream := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
				lifecycle := NewRequestLifecycleManager(nil, nil, "", nil)
				if err := handler.processAccountRegularResponse(httptest.NewRecorder(), upstream, lifecycle, "synthetic", request); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

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
		if typ == "thinking" {
			if result == nil || !result.ThinkingBlock || result.ContentKind != ContentNone || result.ReasoningHidden {
				t.Fatalf("思考块只提供观测信号: %+v", result)
			}
			if result.HasVisibleText || result.HasStreamOutput || result.HasFallbackOutput {
				t.Fatal("旧标志被改写")
			}
			continue
		}
		if typ == "text" {
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
		`{"status":"incomplete","incomplete_details":{"reason":"content_filter"}}`:                 "incomplete:content_filter",
		`{"response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`: "incomplete:max_output_tokens",
		`{"stop_reason":"end_turn","status":"completed"}`:                                          "end_turn",
		`{"object":"response","output":[]}`:                                                        "",
		`{"status":"completed"`:                                                                    "",
		`{"stop_reason":12,"content":[]}`:                                                          "",
	} {
		if got := newReasoningAnalyzer().AnalyzeResponseUnified([]byte(raw), "", "synthetic").StopReason; got != want {
			t.Fatalf("%q != %q", got, want)
		}
	}
}

func TestStreamPhases_ClaudeSingleDecodeKeepsProtocolErrors(t *testing.T) {
	for _, tc := range []struct {
		event, payload string
		kind           ContentKind
	}{
		{"content_block_start", `{"index":"bad","content_block":{"type":"tool_use"}}`, ContentTool},
		{"content_block_delta", `{"index":0,"delta":{"type":"text_delta","text":"sample"},"usage":{"output_tokens":"bad"}}`, ContentText},
		{"content_block_delta", `{"index":0,"delta":{"type":"thinking_delta","thinking":"sample"}}`, ContentReasoning},
	} {
		for _, flush := range []bool{false, true} {
			parser := NewTokenParser()
			feedSSEV2(t, parser, claudeStreamHead)
			parser.ParseSSELineV2("event: " + tc.event)
			parser.ParseSSELineV2("data: " + tc.payload)
			var result *ParseResult
			if flush {
				result = parser.FlushPendingEvent()
			} else {
				result = parser.ParseSSELineV2("")
			}
			if result == nil || result.ContentKind != tc.kind || result.ContentBytes != len(tc.payload) {
				t.Fatalf("独立观测丢失: event=%s flush=%v result=%+v", tc.event, flush, result)
			}
			if result.HasVisibleText || result.HasStreamOutput || result.HasFallbackOutput || result.TokenUsage != nil || parser.contentBlockProtocolError == "" {
				t.Fatalf("协议错误被掩盖或输出标志变化: %+v", result)
			}
			if parser.FlushPendingEvent() != nil {
				t.Fatal("事件已消费，不应再次 flush")
			}
		}
	}
	if result := phaseEvent(t, "content_block_delta", `{"delta":{"text":"unfinished"}`); result != nil {
		t.Fatalf("坏 JSON 不应产生阶段: %+v", result)
	}
}

func TestStreamPhases_RegularDiagnosticsBeforeClientWrite(t *testing.T) {
	for _, tc := range []struct{ route, path, body, stop, model string }{
		{"endpoint", "/v1/messages", `{"model":"claude-synthetic","content":[],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":8}}`, "end_turn", "claude-synthetic"},
		{"account", "/v1/responses", `{"object":"response","model":"gpt-synthetic","output":[],"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":3,"output_tokens":8}}`, "incomplete:max_output_tokens", "gpt-synthetic"},
		{"endpoint", "/v1/messages", `{"model":"claude-synthetic","content":[],"stop_reason":"tool_use"}`, "tool_use", "claude-synthetic"},
		{"account", "/v1/responses", `{"object":"response","model":"gpt-synthetic","output":[],"status":"completed"}`, "completed", "gpt-synthetic"},
	} {
		for _, writeFails := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%s/write_fails_%v", tc.route, tc.stop, writeFails), func(t *testing.T) {
				handler := &Handler{responseProcessor: response.NewProcessor(), tokenAnalyzer: newReasoningAnalyzer()}
				lifecycle := NewRequestLifecycleManager(nil, nil, "", nil)
				request := httptest.NewRequest(http.MethodPost, tc.path, nil)
				upstream := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(tc.body))}
				recorder := httptest.NewRecorder()
				var writer http.ResponseWriter = recorder
				if writeFails {
					writer = &failingBodyWriter{ResponseRecorder: recorder}
				}
				var failed bool
				if tc.route == "account" {
					failed = handler.processAccountRegularResponse(writer, upstream, lifecycle, "synthetic", request) != nil
				} else {
					outcome, _ := handler.processEndpointRegularResponse(writer, request, upstream, lifecycle, "synthetic")
					failed = outcome == ProcessOutcomeFailedAfterCommit
				}
				if failed != writeFails {
					t.Fatalf("写出结果变化: failed=%v", failed)
				}
				if !writeFails && (recorder.Body.String() != tc.body || recorder.Header().Get("Content-Type") != "application/json") {
					t.Fatal("响应内容或头被改变")
				}
				if lifecycle.phaseStopReason == nil || *lifecycle.phaseStopReason != tc.stop || lifecycle.responseModel != tc.model {
					t.Fatalf("诊断缺失: stop=%v model=%s", lifecycle.phaseStopReason, lifecycle.responseModel)
				}
			})
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

func claudeThinkingToolStream(blockType, delta string) string {
	body := `event: message_start
data: {"type":"message_start","message":{"id":"synthetic-thinking-tool","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}

`
	if blockType != "" {
		body += "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"" + blockType + "\"}}\n\n"
		if delta != "" {
			body += "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":" + delta + "}\n\n"
		}
		body += "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"
	}
	return body + `event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"synthetic-tool-call","name":"synthetic_lookup","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":807}}

event: message_stop
data: {"type":"message_stop"}

`
}

func collectClaudeThinkingStream(body string) tracking.StreamPhases {
	parser, start := NewTokenParser(), time.Unix(0, 0)
	collector := newStreamPhaseCollector(start)
	for i, line := range strings.Split(body, "\n") {
		collector.Observe(parser.ParseSSELineV2(line), len(line)+1, start.Add(time.Duration(i)*time.Millisecond))
	}
	return collector.Snapshot(0)
}

func TestStreamPhases_ClaudeThinkingVisibility(t *testing.T) {
	for _, tc := range []struct {
		name, blockType, delta string
		hidden, visible        bool
	}{
		{"signature-only", "thinking", `{"type":"signature_delta","signature":"synthetic"}`, true, false},
		{"empty-thinking", "thinking", `{"type":"thinking_delta","thinking":""}`, true, false},
		{"visible-thinking", "thinking", `{"type":"thinking_delta","thinking":"synthetic thought"}`, false, true},
		{"redacted", "redacted_thinking", "", true, false},
		{"redacted-with-visible", "redacted_thinking", `{"type":"thinking_delta","thinking":"synthetic thought"}`, true, true},
		{"no-thinking", "", "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			phases := collectClaudeThinkingStream(claudeThinkingToolStream(tc.blockType, tc.delta))
			if phases.ReasoningHidden == nil || *phases.ReasoningHidden != tc.hidden || (phases.FirstReasoningMs != nil) != tc.visible {
				t.Fatalf("思考可见性: %+v", phases)
			}
			if phases.FirstContentMs == nil || phases.FirstTextMs != nil || phases.StopReason == nil || *phases.StopReason != "tool_use" {
				t.Fatalf("工具输出与终态: %+v", phases)
			}
		})
	}
}

func TestStreamPhases_ThinkingBlockFlushAndFinalVisibility(t *testing.T) {
	parser, start := NewTokenParser(), time.Unix(0, 0)
	feedSSEV2(t, parser, claudeStreamHead)
	parser.ParseSSELineV2("event: content_block_start")
	parser.ParseSSELineV2(`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`)
	result := parser.FlushPendingEvent()
	if result == nil || !result.ThinkingBlock || result.ContentKind != ContentNone || result.ReasoningHidden || result.HasVisibleText || result.HasStreamOutput || result.HasFallbackOutput {
		t.Fatalf("flush丢失思考块或改变旧标志: %+v", result)
	}
	collector := newStreamPhaseCollector(start)
	if at := collector.Observe(result, 0, start); at != nil {
		t.Fatal("思考块不应提前记录首内容")
	}
	before := collector.Snapshot(0)
	if before.ReasoningHidden == nil || !*before.ReasoningHidden || before.FirstContentMs != nil || before.StreamFlowMs != nil {
		t.Fatalf("没有可见思考时的快照: %+v", before)
	}
	visible := feedSSEV2(t, parser, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"synthetic\"}}\n\n")
	collector.Observe(visible, 0, start.Add(time.Second))
	after := collector.Snapshot(0)
	if after.ReasoningHidden == nil || *after.ReasoningHidden || after.FirstReasoningMs == nil || !*before.ReasoningHidden {
		t.Fatalf("最终可见性或快照隔离: before=%+v, after=%+v", before, after)
	}
}

func TestStreamPhases_ClaudeSignatureOnlyEndToEnd(t *testing.T) {
	body := claudeThinkingToolStream("thinking", `{"type":"signature_delta","signature":"synthetic"}`)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}))
	defer upstream.Close()
	for _, route := range []string{"endpoint", "claude_subscription"} {
		t.Run(route, func(t *testing.T) {
			var tracker *tracking.UsageTracker
			var rec *httptest.ResponseRecorder
			if route == "endpoint" {
				h, _ := newEndpointPipelineTestHandler(t, endpointPipelineConfig("synthetic-thinking", upstream.URL, 1))
				tracker = attachRetryTracker(t, h)
				rec = performEndpointPipelineRequest(t, h, true)
			} else {
				h, _, usageTracker := newClaudeSubscriptionTestHandler(t, upstream.URL)
				tracker = usageTracker
				rec = performClaudeSubscriptionRequest(t, h, claudeSubscriptionRequest{streaming: true})
			}
			if rec.Code != http.StatusOK || rec.Body.String() != body {
				t.Fatalf("转发改变: %d %s", rec.Code, rec.Body.String())
			}
			data := onlyPhaseRequest(t, tracker)
			if data.Detail.Status != "completed" || data.ReasoningHidden == nil || !*data.ReasoningHidden || data.FirstReasoningMs != nil || data.FirstTextMs != nil || data.FirstContentMs == nil {
				t.Fatalf("最终阶段观测: %+v", data)
			}
			if data.Detail.ResponseReasoning != "thinking:present" || data.Detail.OutputTokens != 807 || data.StopReason == nil || *data.StopReason != "tool_use" {
				t.Fatalf("思考、用量或完整性改变: %+v", data)
			}
		})
	}
}
