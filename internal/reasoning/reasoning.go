// Package reasoning 是「模型与思考等级一致性监控」的纯引擎：
// 从 Claude / Codex 请求体与响应对象里提取模型名与思考配置，序列化为家族无关的归一化 token 串。
//
// 文法（§3.2）：以空格分隔的 key:value，key 固定小写，value 小写；
// key ∈ thinking / budget / effort / reasoning_tokens / speed。不做默认值推断，字段缺失即省略。
package reasoning

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// 归一化串里的 key。
const (
	KeyThinking        = "thinking"
	KeyBudget          = "budget"
	KeyEffort          = "effort"
	KeyReasoningTokens = "reasoning_tokens"
	KeySpeed           = "speed"
)

// Claude 响应侧 thinking 取值。
const (
	ThinkingPresent = "present"
	ThinkingAbsent  = "absent"
)

// Family 是请求家族。
type Family string

const (
	FamilyClaude Family = "claude"
	FamilyCodex  Family = "codex"
	FamilyOther  Family = "other"
)

// FamilyForPath 按精确路径分家族；其余路径只取 model，不解析思考配置。
func FamilyForPath(path string) Family {
	switch strings.TrimSpace(path) {
	case "/v1/messages", "/v1/messages/count_tokens":
		return FamilyClaude
	case "/v1/responses", "/v1/responses/compact":
		return FamilyCodex
	default:
		return FamilyOther
	}
}

// ParseRequest 用 gjson 只取模型、思考配置与速度档位的顶层字段，
// 不校验、不构建 messages / input 等大块内容的树；找齐目标 key 即提前停止。
// 顶层非对象或 key 缺失时相应字段为空。
func ParseRequest(path string, body []byte) (model, reasoning string) {
	if len(body) == 0 {
		return "", ""
	}
	results := gjson.GetManyBytes(body, "model", "thinking", "output_config", "reasoning", "service_tier", "speed")
	if results[0].Type == gjson.String {
		model = strings.TrimSpace(results[0].Str)
	}
	switch FamilyForPath(path) {
	case FamilyClaude:
		reasoning = ParseClaudeRequest(map[string]any{
			"thinking":      results[1].Value(),
			"output_config": results[2].Value(),
			"speed":         results[5].Value(),
		})
	case FamilyCodex:
		reasoning = ParseCodexRequest(map[string]any{
			"reasoning":    results[3].Value(),
			"service_tier": results[4].Value(),
		})
	}
	return model, reasoning
}

// ParseClaudeRequest 解析 thinking.type / thinking.budget_tokens / output_config.effort / speed。
func ParseClaudeRequest(payload map[string]any) string {
	var parts []string
	thinkingType := ""
	var budget *int64
	if thinking, ok := payload["thinking"].(map[string]any); ok {
		thinkingType = normalizeToken(stringField(thinking, "type"))
		if thinkingType == "enabled" {
			budget = intField(thinking, "budget_tokens")
		}
	}
	switch thinkingType {
	case "adaptive", "enabled", "disabled":
		parts = append(parts, KeyThinking+":"+thinkingType)
	}
	if budget != nil {
		parts = append(parts, KeyBudget+":"+strconv.FormatInt(*budget, 10))
	}
	if outputConfig, ok := payload["output_config"].(map[string]any); ok {
		if effort := normalizeToken(stringField(outputConfig, "effort")); effort != "" {
			parts = append(parts, KeyEffort+":"+effort)
		}
	}
	if speed := normalizeToken(stringField(payload, "speed")); speed != "" {
		parts = append(parts, KeySpeed+":"+speed)
	}
	return strings.Join(parts, " ")
}

// ParseCodexRequest 解析 reasoning.effort / service_tier。
func ParseCodexRequest(payload map[string]any) string {
	var parts []string
	if reasoning, ok := payload["reasoning"].(map[string]any); ok {
		if effort := normalizeToken(stringField(reasoning, "effort")); effort != "" {
			parts = append(parts, KeyEffort+":"+effort)
		}
	}
	if speed := normalizeToken(stringField(payload, "service_tier")); speed != "" {
		parts = append(parts, KeySpeed+":"+speed)
	}
	return strings.Join(parts, " ")
}

// Parse 把归一化串还原成 map；非法片段忽略。后端仅测试 / 日志用，前端有同文法实现。
func Parse(s string) map[string]string {
	fields := map[string]string{}
	for _, part := range strings.Fields(s) {
		key, value, ok := strings.Cut(part, ":")
		if !ok || key == "" || value == "" {
			continue
		}
		fields[key] = value
	}
	return fields
}

// IsThinkingBlockType 判断 Claude content block 类型是否为思考块。
func IsThinkingBlockType(blockType string) bool {
	switch strings.TrimSpace(blockType) {
	case "thinking", "redacted_thinking":
		return true
	}
	return false
}

// normalizeToken 把 value 归一为小写、无空白、无冒号的 token；不满足则返回空。
func normalizeToken(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || strings.ContainsAny(value, " \t\r\n:") {
		return ""
	}
	return value
}

func stringField(m map[string]any, key string) string {
	value, _ := m[key].(string)
	return strings.TrimSpace(value)
}

// intField 只接受非负整数（JSON number 或数字字符串）。
func intField(m map[string]any, key string) *int64 {
	switch raw := m[key].(type) {
	case float64:
		if raw < 0 || raw != float64(int64(raw)) {
			return nil
		}
		value := int64(raw)
		return &value
	case json.Number:
		value, err := raw.Int64()
		if err != nil || value < 0 {
			return nil
		}
		return &value
	case string:
		value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil || value < 0 {
			return nil
		}
		return &value
	}
	return nil
}

// sortedKeys 仅供测试比较用。
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
