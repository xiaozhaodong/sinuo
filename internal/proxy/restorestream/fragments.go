package restorestream

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

// 外层信封与路由均经验证。布局相同但参数跨属性、转义或嵌套边界时，
// 只解码 delta 字符串，用现有增量状态机解释内层 JSON；完成事件仍校验完整参数。
func (f *deltaFrame) fragment(ev []byte, a, b int) error {
	p := ev[a-1 : b+1]
	if !utf8.Valid(p) {
		return ErrDelivery
	}
	raw, ok := decodeCommonJSONString(p)
	if !ok {
		if !json.Valid(p) {
			return ErrDelivery
		}
		raw = gjson.ParseBytes(p).Str
	}
	field := f.partial
	if f.key.role != "arguments" {
		if f.key.role == "text" && strings.TrimSpace(raw) == "" {
			f.nonempty = false
		}
		payload := object{"delta": raw}
		changed, err := f.parent.delta(f.key, payload, "delta", f.route)
		if err != nil {
			return err
		}
		if !changed {
			return f.parent.send(ev)
		}
		q, _ := json.Marshal(payload["delta"])
		dst := append(f.scratch[:0], ev[:a-1]...)
		dst = append(dst, q...)
		dst = append(dst, ev[b+1:]...)
		if f.codex {
			f.scratch = dst
		}
		return f.parent.send(dst)
	}
	field.declared = false
	out, err := field.args.feed(raw, &f.parent.toolValues)
	if err != nil {
		return err
	}
	if err := f.parent.keep(field, raw, out); err != nil {
		return err
	}
	f.closed = !field.args.quoted && len(field.args.containers) == 0 && field.args.previous != 0
	pos := f.argsBytes
	f.argsBytes += len(p) - 2
	if f.argsBytes > maxBytes {
		f.completeSource = false
	}
	if f.completeSource {
		if f.sourceSlab == nil {
			f.sourceSlab, f.source = lend()
		}
		f.source = append(f.source, ev[a:b]...)
	}
	if raw == out {
		f.outputBytes += len(p) - 2
		if f.outputBytes > maxBytes {
			f.completeSource = false
		}
		return f.parent.send(ev)
	}
	q, err := json.Marshal(out)
	if err != nil {
		return ErrDelivery
	}
	q = q[1 : len(q)-1]
	f.outputBytes += len(q)
	// 变化频繁的极小分片可能让编辑记录本身大于文本，改走已有的原文快照比较。
	if f.outputBytes > maxBytes || len(f.edits) >= maxBytes/64 {
		f.completeSource = false
	}
	if f.completeSource {
		f.edits = append(f.edits, argumentEdit{pos, f.argsBytes, q})
	}
	dst := append(f.scratch[:0], ev[:a]...)
	dst = append(dst, q...)
	f.scratch = append(dst, ev[b:]...)
	return f.parent.send(f.scratch)
}
