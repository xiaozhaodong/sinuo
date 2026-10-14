package handlers

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

const estimatedCharsPerToken = 4.0

// estimateTokens 按文本字符数估算 Token，保留请求基础开销。
func (h *CountTokensHandler) estimateTokens(bodyBytes []byte) (int, error) {
	var req CountTokensRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		return 0, fmt.Errorf("invalid request body: %w", err)
	}
	totalChars := estimatedSystemChars(req.System)
	for _, message := range req.Messages {
		chars, err := estimatedMessageChars(message["content"])
		if err != nil {
			return 0, err
		}
		totalChars += chars
	}
	for _, tool := range req.Tools {
		chars, err := estimatedJSONChars(tool)
		if err != nil {
			return 0, err
		}
		totalChars += chars
	}
	return int(float64(totalChars)/estimatedCharsPerToken) + 50, nil
}

func estimatedMessageChars(content any) (int, error) {
	if text, ok := content.(string); ok {
		return utf8.RuneCountInString(text), nil
	}
	blocks, _ := content.([]any)
	totalChars := 0
	for _, value := range blocks {
		block, ok := value.(map[string]any)
		if !ok {
			continue
		}
		switch block["type"] {
		case "text":
			totalChars += estimatedTextBlockChars(block)
		case "tool_use":
			input, exists := block["input"]
			if !exists {
				continue
			}
			chars, err := estimatedJSONChars(input)
			if err != nil {
				return 0, err
			}
			totalChars += chars
		case "tool_result":
			totalChars += estimatedTextChars(block["content"])
		}
	}
	return totalChars, nil
}

// estimatedTextChars 统计纯字符串或数组中的 text 块，忽略其他块类型。
func estimatedTextChars(content any) int {
	if text, ok := content.(string); ok {
		return utf8.RuneCountInString(text)
	}
	blocks, _ := content.([]any)
	totalChars := 0
	for _, block := range blocks {
		totalChars += estimatedTextBlockChars(block)
	}
	return totalChars
}

func estimatedTextBlockChars(value any) int {
	block, ok := value.(map[string]any)
	if !ok || block["type"] != "text" {
		return 0
	}
	text, _ := block["text"].(string)
	return utf8.RuneCountInString(text)
}

// estimatedSystemChars 系统提示数组还兼容旧版的纯字符串项。
func estimatedSystemChars(system any) int {
	if text, ok := system.(string); ok {
		return utf8.RuneCountInString(text)
	}
	items, _ := system.([]any)
	totalChars := 0
	for _, item := range items {
		if text, ok := item.(string); ok {
			totalChars += utf8.RuneCountInString(text)
		} else {
			totalChars += estimatedTextBlockChars(item)
		}
	}
	return totalChars
}

func estimatedJSONChars(value any) (int, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return 0, fmt.Errorf("failed to serialize token estimation input: %w", err)
	}
	return utf8.RuneCount(data), nil
}
