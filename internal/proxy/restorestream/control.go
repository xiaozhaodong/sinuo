package restorestream

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// 一条 SSE 只对应一个响应，每个输出位置至多对应一个工具调用。
func (w *Writer) checkIdentity(raw []byte) error {
	for _, path := range []string{"response_id", "response.id"} {
		if err := w.checkResponseID(gjson.GetBytes(raw, path).Str); err != nil {
			return err
		}
	}
	idx := gjson.GetBytes(raw, "output_index")
	if !idx.Exists() {
		return nil
	}
	for _, path := range []string{"call_id", "item.call_id"} {
		if id := gjson.GetBytes(raw, path).Str; id != "" {
			i, err := strconv.Atoi(idx.Raw)
			if err != nil || i < 0 || i >= 1<<30 {
				return ErrDelivery
			}
			if e := w.bindCall(i, id); e != nil {
				return e
			}
		}
	}
	return nil
}

// WS 归属和正文必须采用同一解码对象，尤其不能再次用快速解析解释 Unicode 标识。
func (w *Writer) checkDecodedIdentity(m object) error {
	for _, id := range []string{str(m, "response_id"), str(obj(m, "response"), "id")} {
		if err := w.checkResponseID(id); err != nil {
			return err
		}
	}
	if _, present := m["output_index"]; present {
		for _, id := range []string{str(m, "call_id"), str(obj(m, "item"), "call_id")} {
			if id == "" {
				continue
			}
			i, valid := index(m, "output_index")
			if !valid {
				return ErrDelivery
			}
			if err := w.bindCall(i, id); err != nil {
				return err
			}
		}
	}
	return nil
}

func (w *Writer) checkResponseID(id string) error {
	if id == "" {
		return nil
	}
	if w.responseID != "" && w.responseID != id {
		return ErrDelivery
	}
	if w.responseID == "" {
		stored, err := w.retainID(id)
		if err != nil {
			return err
		}
		w.responseID = stored
	}
	return nil
}

func (w *Writer) bindCall(i int, id string) error {
	if id == "" {
		return nil
	}
	if old := w.callIDs[i]; old != "" {
		if old != id {
			return ErrDelivery
		}
		return nil
	}
	if len(w.callIDs) >= 256 {
		return ErrLocalCapacity
	}
	stored, err := w.retainID(id)
	if err != nil {
		return err
	}
	if w.callIDs == nil {
		w.callIDs = make(map[int]string)
	}
	w.callIDs[i] = stored
	return nil
}

// 常规控制事件只读取必要字段。调用前已严格校验整份 JSON（含重复键）。
func (w *Writer) control(wire, raw []byte, typ, name string) (bool, error) {
	if name != "" && name != "message" && name != typ {
		return true, ErrDelivery
	}
	if w.codex {
		if w.sequenceOffset != 0 {
			return false, nil
		}
		switch typ {
		case "response.created", "response.in_progress", "response.queued":
			if v := gjson.GetBytes(raw, "response.output"); v.IsArray() && len(v.Array()) != 0 {
				return false, nil
			}
			return true, w.send(wire)
		case "response.output_item.added":
			if gjson.GetBytes(raw, "item.type").Str == "function_call" && gjson.GetBytes(raw, "item.arguments").Raw == "\"\"" {
				i, e := strconv.Atoi(gjson.GetBytes(raw, "output_index").Raw)
				if e != nil || i < 0 || i >= 1<<30 {
					return true, ErrDelivery
				}
				if id := gjson.GetBytes(raw, "item.id").Str; id != "" {
					if e = w.bindID(i, id); e != nil {
						return true, e
					}
				}
				if err := w.declareTool(fieldKey{i, 0, "arguments"}); err != nil {
					return true, err
				}
				return true, w.send(wire)
			}
		}
		return false, nil
	}
	switch typ {
	case "message_start":
		if content := gjson.GetBytes(raw, "message.content"); content.IsArray() && len(content.Array()) > 0 {
			return false, nil // 初始内容按 Claude 完整字段处理。
		}
		return true, w.send(wire)
	case "content_block_start", "content_block_stop":
		i, e := strconv.Atoi(gjson.GetBytes(raw, "index").Raw)
		if e != nil || i < 0 || i >= 1<<30 {
			return true, ErrDelivery
		}
		if typ == "content_block_start" {
			if w.blocks[i] != "" || len(w.blocks) >= 256 {
				return true, ErrDelivery
			}
			kind := gjson.GetBytes(raw, "content_block.type").Str
			if kind == "" {
				return true, ErrDelivery
			}
			w.blocks[i] = kind
			w.claudeTail.Start(i)
			if kind == "tool_use" || kind == "server_tool_use" {
				if gjson.GetBytes(raw, "content_block.input").Raw == "{}" {
					return true, w.send(wire)
				}
			}
		} else {
			// 即使上游省略了开始事件，也要收尾可能已接收的文本 delta。
			if _, open := w.blocks[i]; open {
				delete(w.blocks, i)
				w.claudeTail.Stop(i)
			}
			if f := w.fast; f != nil && f.key.item == i && f.closed {
				if err := w.closeField(f.partial, nil); err != nil {
					return true, err
				}
				w.releaseFast()
				return true, w.send(wire)
			}
		}
	case "message_delta":
		w.claudeTail.MessageDelta(gjson.GetBytes(raw, "delta.stop_reason").Str)
		if w.claudeTail.CutAtOutputLimit() {
			// 尾部仍属于内容块，必须在停止原因之前交付。工具参数可以是半截 JSON。
			if err := w.flushHeldFields(); err != nil {
				return true, err
			}
		}
		return true, w.send(wire)
	case "ping":
		return true, w.send(wire)
	case "message_stop":
		if !w.claudeTail.CutAtOutputLimit() && w.hasOpenTool() {
			return true, ErrDelivery
		}
		if err := w.flushHeldFields(); err != nil {
			return true, err
		}
		w.releaseFast()
		w.terminal = true
		return true, w.send(wire)
	}
	return false, nil
}

// 输出、调用与响应的身份字段要留到终态交叉校验；与逻辑文本预算分开核算。
func (w *Writer) retainID(id string) (string, error) {
	if len(id) > maxBytes-w.identityBytes {
		return "", ErrLocalCapacity
	}
	w.identityBytes += len(id)
	return strings.Clone(id), nil
}
