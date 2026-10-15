package proxy

import (
	"bytes"
	"fmt"

	"sinuo/internal/codexidentity"

	"github.com/tidwall/gjson"
)

const (
	codexInternalMetadataKey            = "internal_chat_message_metadata_passthrough"
	requestBodyCodeCodexMetadataInvalid = "codex_metadata_invalid_request"
)

// stripCodexInternalMetadata 只移除 input 条目的直属内部元数据，不修改原始请求字节。
// RawMessage 保留大整数、密文和未知字段；没有目标键时直接返回原报文。
func stripCodexInternalMetadata(body []byte) ([]byte, int, error) {
	fields, err := codexidentity.JSONFields(body)
	if err != nil {
		return nil, 0, err
	}
	raw, exists := fields["input"]
	if !exists {
		return body, 0, nil
	}
	input := gjson.ParseBytes(raw)
	if input.Type == gjson.Null || input.Type == gjson.String {
		return body, 0, nil
	}
	if !input.IsArray() {
		return nil, 0, fmt.Errorf("input 必须是数组、字符串或 null")
	}
	var result []byte
	pos, removed := 0, 0
	var editErr error
	input.ForEach(func(_, item gjson.Result) bool {
		if !item.IsObject() {
			editErr = fmt.Errorf("input 条目必须是对象")
			return false
		}
		updated, err := codexidentity.EditObject([]byte(item.Raw), map[string][]byte{codexInternalMetadataKey: nil})
		if err != nil {
			editErr = err
			return false
		}
		if !bytes.Equal(updated, []byte(item.Raw)) {
			result = append(result, raw[pos:item.Index]...)
			result = append(result, updated...)
			pos = item.Index + len(item.Raw)
			removed++
		}
		return true
	})
	if editErr != nil {
		return nil, 0, editErr
	}
	if removed == 0 {
		return body, 0, nil
	}
	result = append(result, raw[pos:]...)
	out, err := codexidentity.EditObject(body, map[string][]byte{"input": result})
	return out, removed, err
}
