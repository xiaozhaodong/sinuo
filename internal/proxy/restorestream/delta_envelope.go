package restorestream

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"
	"unicode/utf8"

	"github.com/tidwall/gjson"

	"sinuo/internal/codexidentity"
)

// 对已校验的外层事件缓存不可变的两侧字节。内容布局一变即无损切回支持任意 JSON 分片的状态机。
func (w *Writer) writeMatchingDelta(ev []byte) (bool, error) {
	f := w.fast
	if f == nil || !f.active || w.terminal || len(ev) > maxBytes || !bytes.HasPrefix(ev, f.prefix) {
		return false, nil
	}
	a, b := len(f.prefix), f.endValue(ev)
	if b < a || !bytes.HasPrefix(ev[b:], f.suffix) || bytes.IndexByte(ev[a:b], '\n') >= 0 {
		return false, nil
	}
	if f.hasCR && bytes.IndexByte(ev[a:b], '\r') >= 0 {
		return false, nil
	}
	f.nonempty = b > a
	err := f.fragment(ev, a, b)
	if err == ErrDelivery && !json.Valid(ev[a-1:b+1]) {
		// 信封两侧相同，中间却不是一个 JSON 字符串。fragment 尚未处理字段，
		// 让整帧解析区分信封布局变化、重复键和上游非 JSON 数据。
		return false, nil
	}
	return true, err
}
func (w *Writer) startDeltaFrame(wire, raw []byte, off int, m object, typ string) (bool, error) {
	if w.sequenceOffset != 0 || len(w.fields) >= 256 {
		return false, nil
	}
	var k fieldKey
	var path string
	var ok bool
	k.role = "arguments"
	if w.codex {
		switch typ {
		case "response.function_call_arguments.delta":
		case "response.output_text.delta":
			k.role = "text"
			k.part, ok = index(m, "content_index")
			if !ok {
				return false, nil
			}
		case "response.custom_tool_call_input.delta":
			k.role = "custom"
		default:
			return false, nil
		}
		k.item, ok = index(m, "output_index")
		path = "delta"
		if id := str(m, "item_id"); id != "" {
			if e := w.bindID(k.item, id); e != nil {
				return true, e
			}
		}
	} else if typ == "content_block_delta" {
		k.item, ok = index(m, "index")
		switch str(obj(m, "delta"), "type") {
		case "input_json_delta":
			path = "delta.partial_json"
		case "text_delta":
			path = "delta.text"
			k.role = "text"
		default:
			return false, nil
		}
	}
	if !ok {
		return false, nil
	}
	field := gjson.GetBytes(raw, path)
	if field.Type != gjson.String {
		return false, nil
	}
	a := off + field.Index + 1
	b := a + len(field.Raw) - 2
	end := len(wire)
	if w.codex {
		seq := gjson.GetBytes(raw, "sequence_number")
		if seq.Type != gjson.Number || off+seq.Index <= b || !bytes.Equal(wire[off+seq.Index+len(seq.Raw):], []byte("}\n\n")) {
			return false, nil
		}
		end = off + seq.Index
	}
	if a+len(wire)-b > 1024 {
		return false, nil
	}
	f := &deltaFrame{key: k, parent: w, codex: w.codex, active: true, prefix: bytes.Clone(wire[:a]), suffix: bytes.Clone(wire[b:end])}
	f.partial, _ = w.get(k)
	f.completeSource = w.codex && k.role == "arguments" && f.partial.raw.Len() == 0
	if f.partial.done {
		return true, ErrDelivery
	}
	if k.role != "arguments" {
		f.route = object{"call_id": m["call_id"]}
	}
	f.dataLine = -1
	if bytes.HasSuffix(wire, []byte("\n\n")) && bytes.IndexByte(wire, '\r') < 0 {
		head := wire[:len(wire)-2]
		first := bytes.IndexByte(head, '\n')
		if first < 0 && bytes.HasPrefix(head, []byte("data:")) {
			f.dataLine = 0
		}
		if first >= 0 && bytes.HasPrefix(head, []byte("event:")) && bytes.HasPrefix(head[first+1:], []byte("data:")) && bytes.IndexByte(head[first+1:], '\n') < 0 {
			f.dataLine = first + 1
		}
	}
	w.fast = f
	f.hasCR = bytes.IndexByte(wire, '\r') >= 0
	f.hint = DeltaEvent{Type: typ, DataLine: f.dataLine, ValueOffset: a}
	return w.writeMatchingDelta(wire)
}
func (f *deltaFrame) applyEdits(dst, p []byte) []byte {
	last := 0
	for _, e := range f.edits {
		dst = append(dst, p[last:e.start]...)
		dst = append(dst, e.value...)
		last = e.end
	}
	return append(dst, p[last:]...)
}
func (w *Writer) releaseFast() {
	if w.fast != nil {
		w.fast.release()
		w.fast = nil
	}
}
func (w *Writer) Release() {
	w.releaseFast()
	if w.frameSlab != nil {
		clear(w.frameSlab[:])
		slabPool.Put(w.frameSlab)
		w.frameSlab = nil
	}
}

// 单工具快照用原始参数逐字节作证，再按同一组替换位置写回；含其他输出项时交回通用路径。
func (w *Writer) writeMatchingSnapshot(wire, raw []byte, offset int, name string) (bool, error) {
	f := w.fast
	if !f.completeSource || !f.closed || len(w.fields) != 1 || len(w.ids) > 1 || w.sequenceOffset != 0 {
		return false, nil
	}
	i := bytes.Index(raw, f.source)
	if i < 1 || i+len(f.source) >= len(raw) || raw[i-1] != '"' || raw[i+len(f.source)] != '"' {
		return false, nil
	}
	stub := append(bytes.Clone(raw[:i]), raw[i+len(f.source):]...)
	if !utf8.Valid(stub) || codexidentity.ValidateJSON(stub) != nil {
		return false, nil
	}
	typ := gjson.GetBytes(stub, "type").Str
	var path, idpath string
	done := false
	switch typ {
	case "response.function_call_arguments.done":
		if !f.active {
			return true, ErrDelivery
		}
		path, idpath = "arguments", "item_id"
	case "response.output_item.done":
		if f.active || gjson.GetBytes(stub, "item.type").Str != "function_call" {
			return false, nil
		}
		path, idpath = "item.arguments", "item.id"
	case "response.completed", "response.done":
		if f.active || f.key.item != 0 || gjson.GetBytes(stub, "response.output.#").Int() != 1 || gjson.GetBytes(stub, "response.output.0.type").Str != "function_call" {
			return false, nil
		}
		path, idpath = "response.output.0.arguments", "response.output.0.id"
		done = true
	default:
		return false, nil
	}
	if name != "" && name != "message" && typ != name {
		return true, ErrDelivery
	}
	if e := w.checkIdentity(stub); e != nil {
		return true, e
	}
	if !done {
		idx := gjson.GetBytes(stub, "output_index")
		v, e := strconv.ParseInt(idx.Raw, 10, 32)
		if e != nil || int64(f.key.item) != v {
			return false, nil
		}
	}
	if done {
		if e := w.bindCall(f.key.item, gjson.GetBytes(stub, "response.output.0.call_id").Str); e != nil {
			return true, e
		}
	}
	if id := gjson.GetBytes(stub, idpath).Str; id != "" {
		if e := w.bindID(f.key.item, id); e != nil {
			return true, e
		}
	}
	field := gjson.GetBytes(stub, path)
	if field.Index != i-1 || field.Raw != "\"\"" {
		return false, nil
	}
	if !f.partial.done {
		if err := w.closeField(f.partial, nil); err != nil {
			return true, err
		}
	}
	f.active = false
	w.terminal = done
	if len(f.edits) == 0 {
		return true, w.send(wire)
	}
	a := offset + i
	if f.scratchSlab == nil {
		f.scratchSlab, f.scratch = lend()
	}
	dst := slices.Grow(f.scratch[:0], len(wire)-len(f.source)+f.outputBytes)
	dst = append(dst, wire[:a]...)
	dst = f.applyEdits(dst, f.source)
	f.scratch = append(dst, wire[a+len(f.source):]...)
	return true, w.send(f.scratch)
}
