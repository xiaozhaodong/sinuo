package proxy

import (
	"encoding/json"
	"strings"

	"sinuo/internal/proxy/restorestream"
)

// 在同一请求内复用来自上游原文的已解析增量事件。恢复器逐字节核对信封，仅允许
// 非空的文本或参数片段和 sequence_number 改变。两个字段均不承载用量；每条事件
// 的内容长度、阶段、模型、协议状态和诊断日志仍按原解析器更新。
type deltaObservation struct {
	template  *restorestream.DeltaEvent
	claude    MessageDelta
	responses *ParseResult
}

func (sp *StreamProcessor) observeOriginalDelta(hint *restorestream.DeltaEvent, data []byte) bool {
	tp := sp.tokenParser
	if tp.collectingData || tp.eventBuffer.Len() != 0 {
		return false
	}
	line := strings.TrimSpace(string(data[hint.DataLine : len(data)-2]))
	raw := tp.extractSSEDataContent(line)
	c := &sp.deltaObservation
	if c.template != hint {
		if hint.DataLine > 0 {
			name := strings.TrimSpace(strings.TrimPrefix(string(data[:hint.DataLine-1]), "event:"))
			if name != hint.Type {
				return false
			}
		} else if name, ok := inferSSEEventTypeFromPayload(string(data[:hint.ValueOffset])); !ok || name != hint.Type {
			return false
		}
		*c = deltaObservation{}
		switch hint.Type {
		case "content_block_delta":
			if json.Unmarshal([]byte(raw), &c.claude) != nil {
				return false
			}
		case "response.output_text.delta", "response.custom_tool_call_input.delta", "response.function_call_arguments.delta":
			// 与传输无关的解析结果不含序号或参数内容。含非标准扩展时保留完整逐事件解析。
			var fields map[string]json.RawMessage
			if json.Unmarshal([]byte(raw), &fields) != nil {
				return false
			}
			for key := range fields {
				switch key {
				case "type", "delta", "sequence_number", "output_index", "content_index", "item_id", "call_id", "response_id":
				default:
					return false
				}
			}
			c.responses = ParseResponsesEventJSON(hint.Type, []byte(raw))
			if c.responses == nil {
				return false
			}
		default:
			return false
		}
		c.template = hint
	}
	if hint.DataLine > 0 {
		sp.appendDebugLine(strings.TrimSpace(string(data[:hint.DataLine-1])))
		sp.observeParseResult(nil)
	}
	sp.appendDebugLine(line)
	sp.recordFirstStreamResponse()
	sp.observeParseResult(nil)
	sp.appendDebugLine("")
	tp.currentEvent = ""
	var result *ParseResult
	if c.responses != nil {
		current := *c.responses
		current.ContentBytes = len(raw)
		tp.applyResponsesParseResult(hint.Type, &current)
		result = &current
	} else {
		result = tp.applyMessageDeltaV2(hint.Type, &c.claude, len(raw))
	}
	sp.observeParseResult(result)
	return true
}
