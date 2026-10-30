package proxy

import (
	"encoding/json"
	"strings"
)

// ContentKind 与流完整性、可见输出标志独立；只用于阶段观测。
type ContentKind string

const (
	ContentNone      ContentKind = ""
	ContentText      ContentKind = "text"
	ContentReasoning ContentKind = "reasoning"
	ContentTool      ContentKind = "tool"
)

// ParseSSELineV2 在旧解析入口外附加观测，不改变其完整性与输出标志。
func (tp *TokenParser) ParseSSELineV2(line string) *ParseResult {
	var phase *ParseResult
	if strings.TrimSpace(line) == "" {
		phase = tp.pendingStreamPhase()
	}
	return attachStreamPhase(tp.parseSSELineV2(line), phase)
}

// FlushPendingEvent 保留原来的flush语义，同时覆盖缺终止空行的阶段观测。
func (tp *TokenParser) FlushPendingEvent() *ParseResult {
	phase := tp.pendingStreamPhase()
	return attachStreamPhase(tp.flushPendingEvent(), phase)
}

func (tp *TokenParser) pendingStreamPhase() *ParseResult {
	if !tp.collectingData || tp.eventBuffer.Len() == 0 || strings.HasPrefix(tp.currentEvent, "response.") {
		return nil
	}
	return parseStreamPhaseJSON(tp.currentEvent, []byte(tp.eventBuffer.String()))
}

func attachStreamPhase(result, phase *ParseResult) *ParseResult {
	if phase == nil {
		return result
	}
	if result == nil {
		if phase.ContentKind == ContentNone && !phase.ReasoningHidden && phase.StopReason == "" {
			return nil
		}
		result = &ParseResult{}
	}
	result.ContentKind, result.ContentBytes = phase.ContentKind, phase.ContentBytes
	result.ReasoningHidden, result.StopReason = phase.ReasoningHidden, phase.StopReason
	return result
}

func parseStreamPhaseJSON(event string, data []byte) *ParseResult {
	var payload map[string]interface{}
	if json.Unmarshal(data, &payload) != nil {
		return nil
	}
	return streamPhasePayload(event, payload, len(data))
}

func streamPhasePayload(event string, p map[string]interface{}, size int) *ParseResult {
	result := &ParseResult{ContentBytes: size}
	if event == "" {
		event, _ = p["type"].(string)
	}
	switch event {
	case "content_block_delta":
		result.ContentKind = claudeContentKind(p)
	case "content_block_start":
		block, _ := p["content_block"].(map[string]interface{})
		typ, _ := block["type"].(string)
		result.ReasoningHidden = typ == "redacted_thinking"
		if typ == "tool_use" || typ == "server_tool_use" {
			result.ContentKind = ContentTool
		}
	case "message_delta":
		delta, _ := p["delta"].(map[string]interface{})
		result.StopReason, _ = delta["stop_reason"].(string)
	default:
		responsesContentKind(event, p, result)
	}
	return result
}

func claudeContentKind(p map[string]interface{}) ContentKind {
	delta, _ := p["delta"].(map[string]interface{})
	for _, field := range []struct {
		key  string
		kind ContentKind
	}{
		{"text", ContentText}, {"thinking", ContentReasoning}, {"partial_json", ContentTool},
	} {
		if value, _ := delta[field.key].(string); value != "" {
			return field.kind
		}
	}
	return ContentNone
}

func responsesContentKind(event string, p map[string]interface{}, result *ParseResult) {
	if event == "response.output_item.added" {
		item, _ := p["item"].(map[string]interface{})
		typ, _ := item["type"].(string)
		encrypted, _ := item["encrypted_content"].(string)
		result.ReasoningHidden = encrypted != ""
		if typ == "function_call" || typ == "custom_tool_call" {
			result.ContentKind = ContentTool
		}
	}
	if delta, _ := p["delta"].(string); delta != "" {
		switch event {
		case "response.output_text.delta", "response.refusal.delta":
			result.ContentKind = ContentText
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			result.ContentKind = ContentReasoning
		case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
			result.ContentKind = ContentTool
		}
	}
	if strings.HasPrefix(event, "response.") && isResponsesTerminalEvent(event) {
		result.StopReason = responseStopReason(event, p)
	}
}

func responseStopReason(event string, p map[string]interface{}) string {
	response, ok := p["response"].(map[string]interface{})
	if !ok {
		response = p
	}
	status, _ := response["status"].(string)
	if status == "" {
		status = strings.TrimPrefix(event, "response.")
		if status == "done" {
			status = "completed"
		}
	}
	if status == "incomplete" {
		details, _ := response["incomplete_details"].(map[string]interface{})
		if reason, _ := details["reason"].(string); reason != "" {
			return status + ":" + reason
		}
	}
	return status
}
