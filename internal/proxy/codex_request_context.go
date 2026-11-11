package proxy

import (
	"sinuo/internal/codexidentity"

	"github.com/tidwall/gjson"
)

const requestBodyCodeCodexContextInvalid = "codex_context_invalid_request"

// normalizeCodexHTTPContext 将 untrusted_as_tool_output 生成的客户端上下文还原为
// 旧版 Codex 的 external 标签用户消息。账号池使用 HTTP，不能发送没有 call_id 的工具结果。
// 仅识别 agent 命名空间下带名称和文本的无调用上下文；真实工具结果和加密重放不改动。
// 不修改原始正文；未命中时直接返回原字节，供压缩帧复用和换号重试使用。
func normalizeCodexHTTPContext(body []byte) ([]byte, int, error) {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, 0, nil
	}
	var result []byte
	pos, converted := 0, 0
	var editErr error
	input.ForEach(func(_, item gjson.Result) bool {
		if !isCodexClientContext(item) {
			return true
		}
		name := item.Get("name").Str
		text := "<external_" + name + ">" + item.Get("output").Str + "</external_" + name + ">"
		content := append([]byte(`[{"type":"input_text","text":`), codexidentity.JSONString(text)...)
		content = append(content, '}', ']')
		updated, err := codexidentity.EditObject([]byte(item.Raw), map[string][]byte{
			"type": []byte(`"message"`), "role": []byte(`"user"`), "content": content,
			"id": nil, "name": nil, "namespace": nil, "output": nil,
		})
		if err != nil {
			editErr = err
			return false
		}
		// GetBytes 的条目位置相对于完整请求；切片使用 input 内的相对位置。
		start := item.Index - input.Index
		result = append(result, input.Raw[pos:start]...)
		result = append(result, updated...)
		pos = start + len(item.Raw)
		converted++
		return true
	})
	if editErr != nil {
		return nil, 0, editErr
	}
	if converted == 0 {
		return body, 0, nil
	}
	result = append(result, input.Raw[pos:]...)
	// 替换前严格校验整个原始 JSON，拒绝重复键和截断，避免按歧义字段改写。
	out, err := codexidentity.EditObject(body, map[string][]byte{"input": result})
	if err != nil {
		return nil, 0, err
	}
	return out, converted, nil
}

func isCodexClientContext(item gjson.Result) bool {
	if !item.IsObject() || item.Get("type").Str != "function_call_output" ||
		item.Get("namespace").Str != "agent" || item.Get("call_id").Exists() ||
		item.Get(codexEncryptedContentKey).Exists() || item.Get("output").Type != gjson.String {
		return false
	}
	name := item.Get("name")
	if name.Type != gjson.String || name.Str == "" {
		return false
	}
	// 名称会进入 external 标签；不把任意字符串拼接成标签结构。
	for _, c := range name.Str {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}
