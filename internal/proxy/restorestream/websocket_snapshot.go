package restorestream

import (
	"bytes"
	"encoding/json"

	"github.com/tidwall/gjson"
)

func (w *Writer) decodeWebSocketEvent(msg ValidatedMessage) (object, error) {
	if m, envelope := w.decodeToolSnapshot(msg); m != nil {
		return m, w.checkIdentity(envelope)
	}
	if err := w.checkIdentity(msg.raw); err != nil {
		return nil, err
	}
	return msg.decodeObject()
}

// 单工具的大快照可直接复用已累积的参数。核对内容和字段位置后，只需在剩余
// 信封内解析路由与元数据。交付端仍在原帧上执行预算检查、协议收尾和回放登记。
func (w *Writer) decodeToolSnapshot(msg ValidatedMessage) (object, []byte) {
	const key = `"arguments":"`
	const maxEnvelope = 8 << 10
	raw := msg.raw
	if len(raw) < largeWSFrame || len(w.fields) != 1 {
		return nil, nil
	}
	var source string
	for _, f := range w.fields {
		if f.key.role == "arguments" {
			source = f.raw.String()
		}
	}
	if len(source) < largeWSFrame {
		return nil, nil
	}
	start := bytes.Index(raw[:min(len(raw), maxEnvelope)], []byte(key))
	if start < 0 || bytes.Contains(raw, []byte(`\u`)) {
		return nil, nil
	}
	start += len(key) - 1 // 指向值的起始引号
	size, ok := matchJSONString(raw[start:], source)
	if !ok || len(raw)-size+2 > maxEnvelope {
		return nil, nil
	}
	envelope := make([]byte, 0, len(raw)-size+2)
	envelope = append(envelope, raw[:start+1]...)
	envelope = append(envelope, raw[start+size-1:]...)
	if !json.Valid(envelope) {
		return nil, nil
	}
	m, err := (ValidatedMessage{raw: envelope}).decodeObject()
	if err != nil {
		return nil, nil
	}
	owner, path := snapshotArguments(m)
	if owner == nil {
		return nil, nil
	}
	// 即便扩展字段也叫 arguments，也只有协议字段在该字节位置时才能复用。
	slot := gjson.GetBytes(envelope, path)
	if slot.Index != start || slot.Raw != `""` {
		return nil, nil
	}
	owner["arguments"] = source
	return m, envelope
}

func snapshotArguments(m object) (object, string) {
	switch str(m, "type") {
	case "response.function_call_arguments.done":
		return m, "arguments"
	case "response.output_item.done":
		if item := obj(m, "item"); str(item, "type") == "function_call" {
			return item, "item.arguments"
		}
	case "response.completed", "response.done":
		outputs, _ := obj(m, "response")["output"].([]any)
		if len(outputs) == 1 {
			item, _ := outputs[0].(object)
			if str(item, "type") == "function_call" {
				return item, "response.output.0.arguments"
			}
		}
	}
	return nil, ""
}

// 比较已校验帧中从起始引号开始的字符串，返回包含两侧引号的原文字节数。
func matchJSONString(literal []byte, expected string) (int, bool) {
	pos := 1
	for i := 0; i < len(expected); i++ {
		if pos >= len(literal) || literal[pos] == '"' {
			return 0, false
		}
		c := literal[pos]
		pos++
		if c == '\\' {
			if pos >= len(literal) {
				return 0, false
			}
			var ok bool
			c, ok = commonJSONEscape(literal[pos])
			if !ok {
				return 0, false
			}
			pos++
		}
		if c != expected[i] {
			return 0, false
		}
	}
	return pos + 1, pos < len(literal) && literal[pos] == '"'
}
