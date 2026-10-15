package proxy

import (
	"fmt"
	"strings"
	"testing"

	"sinuo/internal/reasoning"
)

func TestTokenParserCodexSpeedUsesTerminalOnly(t *testing.T) {
	for _, terminal := range []string{"response.completed", "response.done", "response.failed", "response.incomplete", "response.cancelled", "response.canceled"} {
		t.Run(terminal, func(t *testing.T) {
			tp := NewTokenParser()
			feedSSEV2(t, tp, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"service_tier\":\"priority\"}}\n\n")
			if got := reasoning.Parse(tp.GetResponseReasoning())["speed"]; got != "" {
				t.Fatalf("开场回显不应成为实际档位: %q", got)
			}
			feedSSEV2(t, tp, fmt.Sprintf("event: %s\ndata: {\"type\":%q,\"response\":{\"service_tier\":\"default\",\"usage\":{\"input_tokens\":2,\"output_tokens\":1}}}\n\n", terminal, terminal))
			if got := reasoning.Parse(tp.GetResponseReasoning())["speed"]; got != "default" {
				t.Fatalf("终态实际档位 = %q, want default", got)
			}
		})
	}
}

func TestParseResponsesSpeedBoundaryAndRawJSON(t *testing.T) {
	for _, tc := range []struct{ event, body, want string }{
		{"response.created", `{"response":{"service_tier":"priority"}}`, ""},
		{"response.in_progress", `{"response":{"service_tier":"priority"}}`, ""},
		{"response.created", `{"response":{"service_tier":"priority"}`, ""},
		{"response.completed", `{"response":{"service_tier":"auto"}}`, ""},
		{"response.completed", `{"service_tier":"ultrafast"}`, "ultrafast"},
		{"response.done", `{"response":{"service_tier":"flex"}`, "flex"},
		{"error", `{"service_tier":"priority","error":{"message":"synthetic"}}`, ""},
		{"", `{"type":"response.completed","response":{"service_tier":"priority"}}`, "priority"},
	} {
		result := ParseResponsesEventJSON(tc.event, []byte(tc.body))
		if result == nil || result.ResponseReasoningFields.Speed != tc.want {
			t.Errorf("%s: result=%+v, want speed=%q", tc.event, result, tc.want)
		}
	}
}

func TestTokenParserClaudeSpeedMergesUsage(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		tp := NewTokenParser()
		feed := func(body string) {
			for _, line := range strings.Split(body, "\n") {
				if legacy {
					tp.ParseSSELine(line)
				} else {
					tp.ParseSSELineV2(line)
				}
			}
		}
		feed("event: message_start\ndata: {\"message\":{\"model\":\"claude-test\",\"usage\":{\"input_tokens\":1,\"speed\":\"fast\",\"service_tier\":\"priority\"}}}\n\n")
		if got := reasoning.Parse(tp.GetResponseReasoning())["speed"]; got != "fast" {
			t.Fatalf("message_start speed = %q", got)
		}
		feed("event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2,\"speed\":\"standard\"}}\n\n")
		feed("event: message_delta\ndata: {\"usage\":{\"output_tokens\":2,\"service_tier\":\"priority\"}}\n\n")
		if got := reasoning.Parse(tp.GetResponseReasoning())["speed"]; got != "standard" {
			t.Fatalf("后到 usage.speed 应覆盖，缺失时保留: %q", got)
		}
	}
}

func TestAnalyzerJSONSpeed(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"object":"response","model":"gpt-test","service_tier":"priority","output":[],"usage":{"input_tokens":1,"output_tokens":2}}`, "priority"},
		{`{"object":"response","model":"gpt-test","service_tier":"auto","output":[]}`, ""},
		{`{"type":"message","model":"claude-test","content":[],"usage":{"input_tokens":1,"output_tokens":2,"speed":"fast","service_tier":"standard"}}`, "fast"},
		{`{"type":"message","model":"claude-test","content":[],"usage":{"service_tier":"priority"}}`, ""},
	} {
		analysis := newReasoningAnalyzer().AnalyzeResponseUnified([]byte(tc.body), "req-speed-json", "synthetic")
		if got := reasoning.Parse(analysis.ResponseReasoning)["speed"]; got != tc.want {
			t.Errorf("JSON speed = %q, want %q", got, tc.want)
		}
	}
	tp := NewTokenParser()
	tp.MergeResponseReasoningFromJSON(map[string]interface{}{"type": "response.created", "service_tier": "priority"})
	if tp.GetResponseReasoning() != "" {
		t.Fatal("非完整 response 对象不得提供实际档位")
	}
}

func TestTokenParserSpeedRawTailAndReset(t *testing.T) {
	tp := NewTokenParser()
	feedSSEV2(t, tp, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"service_tier\":\"priority\"}}\n\n")
	if tp.RecoverResponsesUsageFromRawTail(`{"type":"response.created","response":{"service_tier":"priority"}}`) {
		t.Fatal("中途断流不能按完整响应恢复")
	}
	if tp.GetResponseReasoning() != "" {
		t.Fatal("中途断流必须保持实际档位未知")
	}
	raw := `{"object":"response","status":"completed","service_tier":"default","usage":{"input_tokens":2,"output_tokens":1}}`
	if !tp.RecoverResponsesUsageFromRawTail(raw) || reasoning.Parse(tp.GetResponseReasoning())["speed"] != "default" {
		t.Fatalf("raw-tail 未恢复终态档位: %q", tp.GetResponseReasoning())
	}
	tp.Reset()
	feedSSEV2(t, tp, "event: response.created\ndata: {\"response\":{\"service_tier\":\"priority\"}}\n\n")
	if tp.GetResponseReasoning() != "" {
		t.Fatal("新 attempt 不应继承前次响应档位")
	}
}
