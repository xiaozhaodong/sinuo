package restorestream

import (
	"errors"
	"sort"
)

// ErrTruncated 表示上游在终态前结束，已能交付的字段尾部已经写出。
var ErrTruncated = errors.New("上游 SSE 缺少完整终态事件")

func (w *Writer) trackTail(f *field) {
	held := f.match.tail != "" || f.args.token.tail != "" || f.args.escape != ""
	if held && f.heldOrder == 0 {
		w.tailOrder++
		f.heldOrder = w.tailOrder
		w.heldFields++
	} else if !held && f.heldOrder != 0 {
		f.heldOrder = 0
		w.heldFields--
	}
}
func (w *Writer) flushHeldFields() error {
	if w.heldFields == 0 {
		return nil
	}
	fields := make([]*field, 0, w.heldFields)
	for _, f := range w.fields {
		if f.heldOrder != 0 {
			fields = append(fields, f)
		}
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].heldOrder < fields[j].heldOrder })
	for _, f := range fields {
		if err := w.flushTail(f, nil); err != nil {
			return err
		}
	}
	return nil
}

// 已开始的工具不能靠缺少字段终态的消息或 [DONE] 声明成功。
func (w *Writer) hasOpenTool() bool {
	for _, kind := range w.blocks {
		if kind == "tool_use" || kind == "server_tool_use" {
			return true
		}
	}
	for _, f := range w.fields {
		if f.key.role != "text" && !f.done {
			return true
		}
	}
	return false
}

func (w *Writer) finishTruncated() error {
	if err := w.flushHeldFields(); err != nil {
		return err
	}
	// 保留干净流的截断边界。涉及未闭合工具或需核对的 Responses 快照时只交付已解析内容。
	if len(w.pending) > 0 && w.delivered.Text == 0 && w.delivered.Tool == 0 && !w.hasOpenTool() && !(w.codex && len(w.fields) > 0) {
		if err := w.send(w.pending); err != nil {
			return err
		}
	}
	w.pending = nil
	return ErrTruncated
}
