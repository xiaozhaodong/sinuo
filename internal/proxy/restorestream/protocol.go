package restorestream

import (
	"strings"
)

func (w *Writer) full(k fieldKey, m object, name string, next object) (bool, error) {
	raw, ok := m[name].(string)
	if !ok {
		return false, ErrDelivery
	}
	f := w.fields[k]
	snapshotOnly := f == nil || f.declared
	if snapshotOnly {
		var e error
		f, e = w.get(k)
		if e != nil {
			return false, e
		}
		if _, e = w.delta(k, m, name, next); e != nil {
			return false, e
		}
	}
	if f.raw.String() != raw {
		return false, ErrDelivery
	}
	if !f.done {
		// 完整字段本身带有尾部时无需补发增量。已有 delta 才需要在 done 前写出遗留尾部。
		if f.last != nil && snapshotOnly {
			f.last = nil
			tail, e := f.match.feed("", w.values(k.role), true)
			if e != nil {
				return false, e
			}
			if e = w.keep(f, "", tail); e != nil {
				return false, e
			}
		}
		if e := w.closeField(f, next); e != nil {
			return false, e
		}
	}
	out := f.output.String()
	m[name] = out
	return out != raw, nil
}
func (w *Writer) claude(m object, typ string) (bool, error) {
	i, hasIndex := index(m, "index")
	switch typ {
	case "message_start":
		blocks, _ := obj(m, "message")["content"].([]any)
		changed := false
		for i, entry := range blocks {
			block, ok := entry.(object)
			if !ok || str(block, "type") == "" {
				return false, ErrDelivery
			}
			c, err := w.startClaudeBlock(i, block)
			if err != nil {
				return false, err
			}
			changed = changed || c
		}
		return changed, nil
	case "content_block_start":
		if !hasIndex {
			return false, ErrDelivery
		}
		return w.startClaudeBlock(i, obj(m, "content_block"))
	case "content_block_delta":
		if !hasIndex {
			return false, ErrDelivery
		}
		d := obj(m, "delta")
		switch str(d, "type") {
		case "text_delta":
			return w.delta(fieldKey{i, 0, "text"}, d, "text", m)
		case "input_json_delta":
			return w.delta(fieldKey{i, 0, "arguments"}, d, "partial_json", m)
		}
	case "content_block_stop":
		if !hasIndex {
			return false, ErrDelivery
		}
		for _, f := range w.fields {
			if f.key.item == i {
				if e := w.closeField(f, m); e != nil {
					return false, e
				}
			}
		}
	case "error":
		w.terminal = true
	}
	return false, nil
}

// 初始快照和 block_start 都可携带可见内容，同一文本字段的后续 delta 继续使用原匹配器。
func (w *Writer) startClaudeBlock(i int, block object) (bool, error) {
	switch str(block, "type") {
	case "text":
		return w.delta(fieldKey{i, 0, "text"}, block, "text", nil)
	case "tool_use", "server_tool_use":
		if input := obj(block, "input"); input != nil {
			return w.tree(input)
		}
		return false, ErrDelivery
	}
	return false, nil
}

// 独立的完整 JSON 对象（如 Claude tool_use.input）只替换字符串叶子，保持字段名不变。
func (w *Writer) tree(value any) (bool, error) {
	var changed bool
	convert := func(v any) (any, error) {
		if s, ok := v.(string); ok {
			var t matcher
			x, e := t.feed(s, &w.toolValues, true)
			if len(x) > maxBytes-w.outputBytes {
				return nil, ErrLocalCapacity
			}
			w.outputBytes += len(x)
			changed = changed || x != s
			return x, e
		}
		c, e := w.tree(v)
		changed = changed || c
		return v, e
	}
	switch x := value.(type) {
	case object:
		for key, v := range x {
			out, e := convert(v)
			if e != nil {
				return false, e
			}
			x[key] = out
		}
	case []any:
		for i, v := range x {
			out, e := convert(v)
			if e != nil {
				return false, e
			}
			x[i] = out
		}
	}
	return changed, nil
}
func (w *Writer) responses(m object, typ string) (bool, error) {
	i, hasIndex := index(m, "output_index")
	part, hasPart := index(m, "content_index")
	if id := str(m, "item_id"); id != "" && hasIndex {
		if e := w.bindID(i, id); e != nil {
			return false, e
		}
	}
	role := ""
	switch typ {
	case "response.output_text.delta", "response.output_text.done":
		role = "text"
	case "response.function_call_arguments.delta", "response.function_call_arguments.done":
		role = "arguments"
	case "response.custom_tool_call_input.delta", "response.custom_tool_call_input.done":
		role = "custom"
	}
	if role != "" {
		if !hasIndex || role == "text" && !hasPart {
			return false, ErrDelivery
		}
		k := fieldKey{i, part, role}
		if strings.HasSuffix(typ, ".delta") {
			return w.delta(k, m, "delta", m)
		}
		attr := role
		if role == "custom" {
			attr = "input"
		}
		return w.full(k, m, attr, m)
	}
	switch typ {
	case "response.content_part.added", "response.content_part.done":
		if !hasIndex || !hasPart {
			return false, ErrDelivery
		}
		p := obj(m, "part")
		if str(p, "type") == "output_text" {
			return w.part(fieldKey{i, part, "text"}, p, "text", m, strings.HasSuffix(typ, ".done"))
		}
	case "response.output_item.added", "response.output_item.done":
		if !hasIndex {
			return false, ErrDelivery
		}
		return w.item(i, obj(m, "item"), m, strings.HasSuffix(typ, ".done"))
	case "response.failed", "response.cancelled", "response.canceled", "error":
		w.terminal = true
	case "response.created", "response.in_progress", "response.queued", "response.completed", "response.done", "response.incomplete":
		complete := typ == "response.completed" || typ == "response.done" || typ == "response.incomplete"
		var changed bool
		resp := obj(m, "response")
		outputs, _ := resp["output"].([]any)
		// 先检查完整快照的基本结构，不能让前部容量不足掩盖尾部的协议错误。
		for _, it := range outputs {
			if _, ok := it.(object); !ok {
				return false, ErrDelivery
			}
		}
		for idx, it := range outputs {
			item := it.(object)
			c, e := w.item(idx, item, m, complete)
			if e != nil {
				return false, e
			}
			changed = changed || c
		}
		if complete {
			for _, f := range w.fields {
				// max_output_tokens 可在最后一条文本 delta 后终止，不再有字段 done。
				if !f.done && typ == "response.incomplete" && f.key.role == "text" {
					if e := w.closeField(f, m); e != nil {
						return false, e
					}
				}
				if !f.done {
					return false, ErrDelivery
				}
			}
		}
		w.terminal = complete
		return changed, nil
	}
	return false, nil
}
func (w *Writer) bindID(i int, id string) error {
	if old, ok := w.ids[i]; ok {
		if old != id {
			return ErrDelivery
		}
		return nil
	}
	if len(w.ids) >= 256 {
		return ErrLocalCapacity
	}
	stored, err := w.retainID(id)
	if err != nil {
		return err
	}
	w.ids[i] = stored
	return nil
}
func (w *Writer) part(k fieldKey, m object, attr string, envelope object, done bool) (bool, error) {
	if done {
		return w.full(k, m, attr, envelope)
	}
	s, ok := m[attr].(string)
	if ok && s == "" && k.role != "text" && w.fields[k] == nil {
		return false, w.declareTool(k)
	}
	if !ok || s == "" {
		return false, nil
	}
	// 快照可能重复说明已有内容，不能把快照当作新 delta 追加。
	if f := w.fields[k]; f != nil {
		if f.raw.String() != s {
			return false, ErrDelivery
		}
		m[attr] = f.output.String()
		return str(m, attr) != s, nil
	}
	return w.delta(k, m, attr, envelope)
}
func (w *Writer) item(i int, m, next object, done bool) (bool, error) {
	if m == nil {
		return false, ErrDelivery
	}
	if e := w.bindCall(i, str(m, "call_id")); e != nil {
		return false, e
	}
	if id := str(m, "id"); id != "" {
		if e := w.bindID(i, id); e != nil {
			return false, e
		}
	}
	switch str(m, "type") {
	case "function_call":
		args := str(m, "arguments")
		changed, err := w.part(fieldKey{i, 0, "arguments"}, m, "arguments", next, done)
		if err == nil && changed && done {
			w.rememberFunctionReplay(str(m, "call_id"), str(m, "name"), str(m, "namespace"), str(m, "encrypted_content"), args)
		}
		return changed, err
	case "custom_tool_call":
		return w.part(fieldKey{i, 0, "custom"}, m, "input", next, done)
	case "message":
		content, _ := m["content"].([]any)
		var changed bool
		for j, v := range content {
			p, _ := v.(object)
			if str(p, "type") == "output_text" {
				c, e := w.part(fieldKey{i, j, "text"}, p, "text", next, done)
				if e != nil {
					return false, e
				}
				changed = changed || c
			}
		}
		return changed, nil
	}
	return false, nil
}

// declareTool 为新增工具预留收尾位置。成功终态必须有完整参数或实际完成快照。
func (w *Writer) declareTool(k fieldKey) error {
	if w.fields[k] != nil {
		return nil
	}
	f, err := w.get(k)
	if err == nil {
		f.declared = true
	}
	return err
}
