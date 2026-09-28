package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// 订阅链路脱敏后的工具回放复验：原先把两份完整正文解码成 map[string]any 再逐条比对，
// 大正文上主要耗时都在这两次解码。两份正文此时都已通过 JSON 校验，这里改为按字节定位
// input 数组的各条目，只解码字节有变化的条目。

// validateSubscriptionReplayItems 工具回放中原本有效的嵌套 JSON 在脱敏后必须继续有效。
// original / rewritten 必须已通过 validateSubscriptionJSON。
func validateSubscriptionReplayItems(original, rewritten []byte) error {
	originalInput, rewrittenInput := subscriptionReplayInputs(original, rewritten)
	items := jsonArrayElements(originalInput)
	updatedItems := jsonArrayElements(rewrittenInput)
	if len(items) != len(updatedItems) {
		return fmt.Errorf("privacy replacement changed input structure")
	}
	for index, item := range items {
		if bytes.Equal(item, updatedItems[index]) {
			continue
		}
		if err := validateSubscriptionReplayItem(item, updatedItems[index]); err != nil {
			return err
		}
	}
	return nil
}

// subscriptionReplayInputs WS response.create 把请求包在 response 对象里；以原文的包形为准
func subscriptionReplayInputs(original, rewritten []byte) ([]byte, []byte) {
	originalFields := jsonObjectFields(original, "response", "input")
	rewrittenFields := jsonObjectFields(rewritten, "response", "input")
	if wrapped := originalFields["response"]; jsonKind(wrapped) == '{' {
		var rewrittenInput []byte
		if updated := rewrittenFields["response"]; jsonKind(updated) == '{' {
			rewrittenInput, _ = jsonObjectField(updated, "input")
		}
		originalInput, _ := jsonObjectField(wrapped, "input")
		return originalInput, rewrittenInput
	}
	return originalFields["input"], rewrittenFields["input"]
}

// validateSubscriptionReplayItem 字段按精确键名读取（与 map 解码语义一致，不做大小写不敏感匹配）
func validateSubscriptionReplayItem(before, after []byte) error {
	typeValue, ok := jsonObjectField(before, "type")
	if !ok {
		return nil // 非对象或无类型的条目不是工具回放
	}
	field := ""
	switch jsonStringValue(typeValue) {
	case "function_call":
		field = "arguments"
	case "custom_tool_call":
		field = "input"
	case "function_call_output", "custom_tool_call_output":
		field = "output"
	default:
		return nil
	}
	originalRaw, _ := jsonObjectField(before, field)
	replacementRaw, _ := jsonObjectField(after, field)
	original, replaced := jsonStringValue(originalRaw), jsonStringValue(replacementRaw)
	trimmed := strings.TrimSpace(original)
	if original == replaced || len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') || !json.Valid([]byte(original)) {
		return nil
	}
	if err := validateSubscriptionJSONDocument([]byte(replaced)); err != nil {
		return fmt.Errorf("privacy replacement invalidated nested tool JSON")
	}
	return nil
}

// jsonStringValue 字符串值解码后返回；非字符串或缺失时为空串
func jsonStringValue(raw []byte) string {
	if jsonKind(raw) != '"' {
		return ""
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

// jsonKind 返回值的首个非空白字节
func jsonKind(raw []byte) byte {
	if pos := skipJSONSpace(raw, 0); pos < len(raw) {
		return raw[pos]
	}
	return 0
}

// jsonObjectField 返回对象直属字段的原始值；raw 必须是已校验的 JSON
func jsonObjectField(raw []byte, key string) ([]byte, bool) {
	value, ok := jsonObjectFields(raw, key)[key]
	return value, ok
}

// jsonObjectFields 一次扫描取出对象的若干直属字段；非对象时为空。raw 必须是已校验的 JSON
func jsonObjectFields(raw []byte, keys ...string) map[string][]byte {
	fields := make(map[string][]byte, len(keys))
	pos := skipJSONSpace(raw, 0)
	if pos >= len(raw) || raw[pos] != '{' {
		return fields
	}
	pos = skipJSONSpace(raw, pos+1)
	for pos < len(raw) && raw[pos] != '}' && len(fields) < len(keys) {
		keyEnd := skipJSONString(raw, pos)
		name := decodeJSONKey(raw[pos:keyEnd])
		valueStart := skipJSONSpace(raw, skipJSONSpace(raw, keyEnd)+1) // 跳过冒号
		valueEnd := skipJSONValue(raw, valueStart)
		if slices.Contains(keys, name) {
			fields[name] = raw[valueStart:valueEnd]
		}
		pos = skipJSONSpace(raw, valueEnd)
		if pos < len(raw) && raw[pos] == ',' {
			pos = skipJSONSpace(raw, pos+1)
		}
	}
	return fields
}

// jsonArrayElements 返回数组各元素的原始字节；非数组时为 nil。raw 必须是已校验的 JSON
func jsonArrayElements(raw []byte) [][]byte {
	pos := skipJSONSpace(raw, 0)
	if pos >= len(raw) || raw[pos] != '[' {
		return nil
	}
	var elements [][]byte
	pos = skipJSONSpace(raw, pos+1)
	for pos < len(raw) && raw[pos] != ']' {
		end := skipJSONValue(raw, pos)
		elements = append(elements, raw[pos:end])
		pos = skipJSONSpace(raw, end)
		if pos < len(raw) && raw[pos] == ',' {
			pos = skipJSONSpace(raw, pos+1)
		}
	}
	return elements
}

// decodeJSONKey 键含转义时按标准库解码，保持与 map 解码相同的键语义
func decodeJSONKey(token []byte) string {
	if bytes.IndexByte(token, '\\') < 0 {
		return string(token[1 : len(token)-1])
	}
	var key string
	if json.Unmarshal(token, &key) != nil {
		return ""
	}
	return key
}

func skipJSONSpace(raw []byte, pos int) int {
	for pos < len(raw) {
		switch raw[pos] {
		case ' ', '\n', '\r', '\t':
			pos++
		default:
			return pos
		}
	}
	return pos
}

// skipJSONString pos 指向起始引号，返回结束引号之后的位置。
// 长字符串（工具输出）占正文大头，用 IndexByte 直接找引号，再按前面连续反斜杠的奇偶判断是否转义
func skipJSONString(raw []byte, pos int) int {
	for pos++; pos < len(raw); {
		idx := bytes.IndexByte(raw[pos:], '"')
		if idx < 0 {
			return len(raw)
		}
		quote := pos + idx
		backslashes := 0
		for i := quote - 1; i >= pos && raw[i] == '\\'; i-- {
			backslashes++
		}
		if backslashes%2 == 0 {
			return quote + 1
		}
		pos = quote + 1
	}
	return pos
}

// skipJSONValue 返回从 pos 开始的一个值之后的位置
func skipJSONValue(raw []byte, pos int) int {
	if pos >= len(raw) {
		return pos
	}
	switch raw[pos] {
	case '"':
		return skipJSONString(raw, pos)
	case '{', '[':
		depth := 0
		for pos < len(raw) {
			switch raw[pos] {
			case '"':
				pos = skipJSONString(raw, pos)
				continue
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return pos + 1
				}
			}
			pos++
		}
		return pos
	default:
		for pos < len(raw) {
			switch raw[pos] {
			case ',', ']', '}', ' ', '\t', '\r', '\n':
				return pos
			}
			pos++
		}
		return pos
	}
}
