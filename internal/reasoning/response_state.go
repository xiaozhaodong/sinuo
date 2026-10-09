package reasoning

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	rawEffortRe          = regexp.MustCompile(`"effort"\s*:\s*"([^"]+)"`)
	rawReasoningTokensRe = regexp.MustCompile(`"reasoning_tokens"\s*:\s*(\d+)`)
	rawServiceTierRe     = regexp.MustCompile(`"service_tier"\s*:\s*"([^"]+)"`)
)

// ResponseState 累计上游响应里可观测的思考信息；按字段合并，序列化只在读取时发生。
//
//   - Codex：response.created 回显 effort、response.completed 只给 reasoning_tokens，各自「有则更新、无则保留」
//   - Claude：见到 thinking / redacted_thinking 块即 present；完整收尾且未见块为 absent；未收尾且未见块不断言（空串）
type ResponseState struct {
	Effort          string
	ReasoningTokens *int64
	SawThinking     bool
	Complete        bool
	Speed           string
}

// CodexFields 是单个 Responses 事件里可提取的思考字段（无状态解析产物）。
type CodexFields struct {
	Effort          string
	ReasoningTokens *int64
	Speed           string
}

// IsEmpty 表示该事件没有任何思考字段。
func (f CodexFields) IsEmpty() bool {
	return f.Effort == "" && f.ReasoningTokens == nil && f.Speed == ""
}

// ExtractCodexFields 从 Responses 事件 payload（顶层或 response 对象）提取 reasoning.effort 与
// usage.output_tokens_details.reasoning_tokens / service_tier；档位仅供终态调用方采用。
func ExtractCodexFields(payload map[string]any) CodexFields {
	var fields CodexFields
	if payload == nil {
		return fields
	}
	candidates := []map[string]any{payload}
	if responseObj, ok := payload["response"].(map[string]any); ok {
		candidates = append([]map[string]any{responseObj}, candidates...)
	}
	for _, obj := range candidates {
		if fields.Speed == "" {
			fields.Speed = normalizeCodexSpeed(stringField(obj, "service_tier"))
		}
		if fields.Effort == "" {
			if reasoning, ok := obj["reasoning"].(map[string]any); ok {
				fields.Effort = normalizeToken(stringField(reasoning, "effort"))
			}
		}
		if fields.ReasoningTokens == nil {
			if usage, ok := obj["usage"].(map[string]any); ok {
				if details, ok := usage["output_tokens_details"].(map[string]any); ok {
					fields.ReasoningTokens = intField(details, "reasoning_tokens")
				}
			}
		}
	}
	return fields
}

// ExtractCodexFieldsFromRaw 从可能残缺的原始 JSON 文本里用正则兜底提取（raw-tail 恢复路径）。
// 尾部窗口可能串联多个事件（created → in_progress → 最终 JSON），按时序取最后一个匹配作为终值，
// 避免用早期回显覆盖已累计的降级结果。
func ExtractCodexFieldsFromRaw(raw string) CodexFields {
	var fields CodexFields
	if match := lastSubmatch(rawEffortRe, raw); match != "" {
		fields.Effort = normalizeToken(match)
	}
	if match := lastSubmatch(rawReasoningTokensRe, raw); match != "" {
		if value, err := strconv.ParseInt(match, 10, 64); err == nil && value >= 0 {
			fields.ReasoningTokens = &value
		}
	}
	matches := rawServiceTierRe.FindAllStringSubmatch(raw, -1)
	for i := len(matches) - 1; i >= 0; i-- {
		if speed := normalizeCodexSpeed(matches[i][1]); speed != "" {
			fields.Speed = speed
			break
		}
	}
	return fields
}

func normalizeCodexSpeed(value string) string {
	value = normalizeToken(value)
	if value == "auto" {
		return ""
	}
	return value
}

// lastSubmatch 返回正则在文本中最后一次匹配的首个捕获组，无匹配返回空串。
func lastSubmatch(re *regexp.Regexp, raw string) string {
	matches := re.FindAllStringSubmatch(raw, -1)
	if len(matches) == 0 {
		return ""
	}
	last := matches[len(matches)-1]
	if len(last) < 2 {
		return ""
	}
	return last[1]
}

// MergeCodexFields 按字段合并单事件提取结果。
func (s *ResponseState) MergeCodexFields(fields CodexFields) {
	if fields.Effort != "" {
		s.Effort = fields.Effort
	}
	if fields.ReasoningTokens != nil {
		value := *fields.ReasoningTokens
		s.ReasoningTokens = &value
	}
	if speed := normalizeCodexSpeed(fields.Speed); speed != "" {
		s.Speed = speed
	}
}

// MergeClaudeSpeed 按事件顺序合并 usage.speed，缺失值不覆盖已有档位。
func (s *ResponseState) MergeClaudeSpeed(speed string) {
	if speed = normalizeToken(speed); speed != "" {
		s.Speed = speed
	}
}

// MarkThinkingBlock 记录见到了 Claude 思考块。
func (s *ResponseState) MarkThinkingBlock() { s.SawThinking = true }

// MarkComplete 记录 Claude 流已完整收尾（message_stop / 非流式完整 JSON）。
func (s *ResponseState) MarkComplete() { s.Complete = true }

// MarkClaudeContent 扫描非流式 Claude 响应的 content[] 并标记完成。
func (s *ResponseState) MarkClaudeContent(payload map[string]any) {
	if usage, ok := payload["usage"].(map[string]any); ok {
		s.MergeClaudeSpeed(stringField(usage, "speed"))
	}
	blocks, _ := payload["content"].([]any)
	for _, block := range blocks {
		if obj, ok := block.(map[string]any); ok && IsThinkingBlockType(stringField(obj, "type")) {
			s.MarkThinkingBlock()
		}
	}
	s.MarkComplete()
}

// IsEmpty 表示尚无任何可序列化信息。
func (s ResponseState) IsEmpty() bool {
	return s.String() == ""
}

// String 统一序列化；Claude 未 Complete 且未见块 → 不输出 thinking 键。
func (s ResponseState) String() string {
	var parts []string
	switch {
	case s.SawThinking:
		parts = append(parts, KeyThinking+":"+ThinkingPresent)
	case s.Complete:
		parts = append(parts, KeyThinking+":"+ThinkingAbsent)
	}
	if s.Effort != "" {
		parts = append(parts, KeyEffort+":"+s.Effort)
	}
	if s.ReasoningTokens != nil {
		parts = append(parts, KeyReasoningTokens+":"+strconv.FormatInt(*s.ReasoningTokens, 10))
	}
	if s.Speed != "" {
		parts = append(parts, KeySpeed+":"+s.Speed)
	}
	return strings.Join(parts, " ")
}
