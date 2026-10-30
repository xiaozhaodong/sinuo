package proxy

import (
	"strings"

	"sinuo/internal/proxy/response"
	"sinuo/internal/reasoning"
)

// ContentKind 与流完整性、可见输出标志独立；只用于阶段观测。
type ContentKind string

const (
	ContentNone      ContentKind = ""
	ContentText      ContentKind = "text"
	ContentReasoning ContentKind = "reasoning"
	ContentTool      ContentKind = "tool"
)

func attachStreamPhase(result, phase *ParseResult) *ParseResult {
	if phase == nil {
		return result
	}
	if result == nil {
		if phase.ContentKind == ContentNone && !phase.ThinkingBlock && !phase.ReasoningHidden && phase.StopReason == "" {
			return nil
		}
		result = &ParseResult{}
	}
	result.ContentKind, result.ContentBytes = phase.ContentKind, phase.ContentBytes
	result.ThinkingBlock = phase.ThinkingBlock
	result.ReasoningHidden, result.StopReason = phase.ReasoningHidden, phase.StopReason
	return result
}

func streamPhasePayload(event string, p map[string]interface{}, size int) *ParseResult {
	result := &ParseResult{ContentBytes: size}
	if event == "" {
		event, _ = p["type"].(string)
	}
	responsesContentKind(event, p, result)
	return result
}

// 直接复用已经解码的 delta，补充阶段字段，不改写完整性和输出标志。
func attachClaudeDeltaPhase(result *ParseResult, event string, data interface{}, size int) *ParseResult {
	delta, _ := data.(map[string]interface{})
	var kind ContentKind
	var stop string
	if event == "content_block_delta" {
		kind = claudeContentKind(delta)
	} else if event == "message_delta" {
		stop, _ = delta["stop_reason"].(string)
	}
	if result == nil {
		if kind == ContentNone && stop == "" {
			return nil
		}
		result = &ParseResult{}
	}
	result.ContentKind, result.ContentBytes, result.StopReason = kind, size, stop
	return result
}

func claudeBlockPhase(event, typ string, size int) *ParseResult {
	if event != "content_block_start" {
		return nil
	}
	thinking := reasoning.IsThinkingBlockType(typ)
	kind := ContentNone
	if typ == "tool_use" || typ == "server_tool_use" {
		kind = ContentTool
	}
	if !thinking && kind == ContentNone {
		return nil
	}
	return &ParseResult{ContentKind: kind, ContentBytes: size, ThinkingBlock: thinking, ReasoningHidden: typ == "redacted_thinking"}
}

func claudeContentKind(delta map[string]interface{}) ContentKind {
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
		result.StopReason = response.ResponsesStopReason(event, p)
	}
}
