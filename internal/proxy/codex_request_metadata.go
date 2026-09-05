package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
)

const (
	codexInternalMetadataKey            = "internal_chat_message_metadata_passthrough"
	requestBodyCodeCodexMetadataInvalid = "codex_metadata_invalid_request"
)

// stripCodexInternalMetadata 只移除 input 条目的直属内部元数据，不修改原始请求字节。
// RawMessage 保留大整数、密文和未知字段；没有目标键时直接返回原报文。
func stripCodexInternalMetadata(body []byte) ([]byte, int, error) {
	var request map[string]json.RawMessage
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, 0, fmt.Errorf("解析 Responses 请求失败: %w", err)
	}
	if request == nil {
		return nil, 0, fmt.Errorf("Responses 请求必须是 JSON 对象")
	}
	input, exists := request["input"]
	if !exists {
		return body, 0, nil
	}
	input = bytes.TrimSpace(input)
	if bytes.Equal(input, []byte("null")) || len(input) > 0 && input[0] == '"' {
		return body, 0, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(input, &items); err != nil {
		return nil, 0, fmt.Errorf("input 必须是数组、字符串或 null: %w", err)
	}
	removed := 0
	for index, item := range items {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(item, &fields); err != nil {
			return nil, 0, fmt.Errorf("input[%d] 必须是 JSON 对象: %w", index, err)
		}
		if fields == nil {
			return nil, 0, fmt.Errorf("input[%d] 必须是 JSON 对象", index)
		}
		if _, exists := fields[codexInternalMetadataKey]; !exists {
			continue
		}
		delete(fields, codexInternalMetadataKey)
		encoded, err := encodeCodexMetadataJSON(fields)
		if err != nil {
			return nil, 0, err
		}
		items[index] = encoded
		removed++
	}
	if removed == 0 {
		return body, 0, nil
	}
	encodedInput, err := encodeCodexMetadataJSON(items)
	if err != nil {
		return nil, 0, err
	}
	request["input"] = encodedInput
	out, err := encodeCodexMetadataJSON(request)
	if err != nil {
		return nil, 0, err
	}
	return out, removed, nil
}

func encodeCodexMetadataJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	// 不改写 RawMessage 中字符串的 HTML 字符和既有转义表示。
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}
