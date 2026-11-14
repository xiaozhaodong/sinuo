package restorestream

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"

	"sinuo/internal/codexidentity"
	"sinuo/internal/proxy/response"
)

type fieldKey struct {
	item, part int
	role       string
}
type field struct {
	key         fieldKey
	raw, output strings.Builder
	match       matcher
	args        arguments
	heldOrder   uint64 // 当前待发尾部的到达顺序。
	last        map[string]any
	done        bool
	declared    bool // 工具 added 事件带空初值，尚未收到增量或实参快照。
}
type Writer struct {
	http.ResponseWriter
	socketSend func([]byte) error
	streamID   string
	fast       *deltaFrame
	frameSlab  *workSlab
	lineHint   int
	deliveryLedger
	codex          bool
	pending        []byte
	pos            int
	fields         map[fieldKey]*field
	ids            map[int]string
	callIDs        map[int]string
	responseID     string
	blocks         map[int]string
	claudeTail     response.ClaudeBlockTail
	outputBytes    int
	rawBytes       int
	identityBytes  int
	heldFields     int
	tailOrder      uint64
	lastSequence   int64
	hasSequence    bool
	sequenceOffset int64
	terminal       bool
	failure        error
}

func New(w http.ResponseWriter, path string, d Dictionary) *Writer {
	return &Writer{ResponseWriter: w, deliveryLedger: newLedger(d, maxBytes), codex: path == "/v1/responses", fields: map[fieldKey]*field{}, ids: map[int]string{}, blocks: map[int]string{}}
}

func (w *Writer) Delivered() DeliveryCounts { return w.delivered }

func (w *Writer) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (w *Writer) send(p []byte) error {
	n, e := w.ResponseWriter.Write(p)
	if e == nil && n != len(p) {
		return io.ErrShortWrite
	}
	if e == nil && w.codex && w.heldFields > 0 {
		// 仅在有待发尾部时记录序号。无后续 done 事件也能给补发 delta 正确编号。
		if frame, err := parseSSEFields(p, false); err == nil {
			if seq := gjson.GetBytes(frame.payload(), "sequence_number"); seq.Type == gjson.Number {
				w.lastSequence = seq.Int()
				w.hasSequence = true
			}
		}
	}
	return e
}

// eventEnd 按 SSE 空行切分，兼容 CRLF、LF、CR。末尾单独的 CR 在下一次读或 EOF 时确定。
func eventEnd(p []byte, start int, eof bool) (int, int) {
	if i := bytes.Index(p[start:], []byte("\n\n")); i >= 0 && bytes.IndexByte(p[start:start+i], '\r') < 0 {
		return start + i + 2, 0
	}
	for i := start; i < len(p); {
		n := bytes.IndexAny(p[i:], "\r\n")
		if n < 0 {
			return 0, max(0, len(p)-3)
		}
		i += n
		first := i
		if p[i] == '\r' {
			i++
			if i == len(p) && !eof {
				return 0, first
			}
			if i < len(p) && p[i] == '\n' {
				i++
			}
		} else {
			i++
		}
		if i == len(p) {
			return 0, first
		}
		switch p[i] {
		case '\n':
			return i + 1, 0
		case '\r':
			i++
			if i == len(p) && !eof {
				return 0, first
			}
			if i < len(p) && p[i] == '\n' {
				i++
			}
			return i, 0
		}
	}
	return 0, max(0, len(p)-3)
}
func (w *Writer) Write(p []byte) (int, error) {
	if w.failure != nil {
		return 0, w.failure
	}
	n, err := w.write(p)
	if err != nil {
		w.failure = err
	}
	return n, err
}

func (w *Writer) write(p []byte) (int, error) {
	w.lineHint = 0
	size := len(p)
	if len(w.pending) == 0 && w.fast != nil {
		if ok, e := w.writeMatchingDelta(p); ok {
			if e == nil {
				w.confirmDelivery()
				w.lineHint = w.fast.dataLine + 1
			}
			return size, e
		}
	}
	for len(p) > 0 {
		if len(w.pending) > 0 {
			n := min(len(p), maxBytes+1-len(w.pending))
			if n <= 0 {
				return 0, ErrDelivery
			}
			w.pending = append(w.pending, p[:n]...)
			p = p[n:]
			end, pos := eventEnd(w.pending, w.pos, false)
			w.pos = pos
			if end == 0 {
				if len(w.pending) > maxBytes {
					return 0, ErrDelivery
				}
				continue
			}
			for end > 0 {
				if end > maxBytes {
					return 0, ErrDelivery
				}
				if e := w.event(w.pending[:end]); e != nil {
					return 0, e
				}
				w.pending = w.pending[end:]
				end, w.pos = eventEnd(w.pending, 0, false)
			}
			if len(w.pending) == 0 {
				w.pending = nil
			}
			continue
		}
		end, pos := eventEnd(p, 0, false)
		if end == 0 {
			if len(p) > maxBytes {
				return 0, ErrDelivery
			}
			if w.frameSlab == nil {
				w.frameSlab, w.pending = lend()
			} else if cap(w.pending) < len(p) && len(p) <= len(w.frameSlab) {
				w.pending = w.frameSlab[:0]
			}
			w.pending = append(w.pending, p...)
			w.pos = pos
			break
		}
		if end > maxBytes {
			return 0, ErrDelivery
		}
		if e := w.event(p[:end]); e != nil {
			return 0, e
		}
		p = p[end:]
	}
	return size, nil
}
func (w *Writer) Finish() error {
	if w.failure != nil {
		return w.failure
	}
	if len(w.pending) > 0 {
		end, _ := eventEnd(w.pending, w.pos, true)
		// 代理在终态交付后主动停读，同一次读取中附带的下一帧残片无需交付。
		if end == 0 && w.terminal {
			w.pending = nil
			return nil
		}
		if end == 0 {
			return w.finishTruncated()
		}
		if end != len(w.pending) {
			return ErrDelivery
		}
		if e := w.event(w.pending); e != nil {
			return e
		}
		w.pending = nil
	}
	if !w.terminal {
		return w.finishTruncated()
	}
	return nil
}

type object = map[string]any

func obj(m object, k string) object { v, _ := m[k].(map[string]any); return v }
func str(m object, k string) string { s, _ := m[k].(string); return s }
func index(m object, k string) (int, bool) {
	v, ok := m[k].(json.Number)
	if !ok {
		return 0, false
	}
	n, e := v.Int64()
	return int(n), e == nil && n >= 0 && n < 1<<30
}
func (w *Writer) event(wire []byte) error {
	if err := w.writeEvent(wire); err != nil {
		return err
	}
	w.confirmDelivery()
	return nil
}

type sseFields struct {
	name       string
	metadata   [][]byte
	lines      [][]byte
	dataOffset int
}

func (f *sseFields) payload() []byte {
	if len(f.lines) == 1 {
		return f.lines[0]
	}
	return bytes.Join(f.lines, []byte("\n"))
}

func parseSSEFields(wire []byte, verifyOpening bool) (sseFields, error) {
	var f sseFields
	for p := wire; len(p) > 0; {
		lineOffset := len(wire) - len(p)
		i := bytes.IndexAny(p, "\r\n")
		if i < 0 {
			return f, ErrDelivery
		}
		line := p[:i]
		n := i + 1
		if p[i] == '\r' && n < len(p) && p[n] == '\n' {
			n++
		}
		p = p[n:]
		if len(line) == 0 {
			continue
		}
		key, val, _ := bytes.Cut(line, []byte(":"))
		val = bytes.TrimPrefix(val, []byte(" "))
		switch string(key) {
		case "data":
			if len(f.lines) == 0 {
				f.dataOffset = lineOffset + len(line) - len(val)
			}
			f.lines = append(f.lines, val)
		case "event":
			f.name = string(val)
			f.metadata = append(f.metadata, line)
		default:
			if verifyOpening && len(key) != 0 && string(key) != "id" && string(key) != "retry" {
				return f, ErrResponseFormat
			}
			f.metadata = append(f.metadata, line)
		}
	}
	return f, nil
}

// 心跳没有可回填字段，可以立即交付，字段尾部留给下一帧增量继续匹配。
func isHeartbeatData(name string, raw []byte) bool {
	switch name {
	case "ping", "heartbeat", "keepalive", "keep-alive":
		return true
	case "", "message":
		p := bytes.TrimSpace(raw)
		if len(p) == 0 {
			return true
		}
		if p[0] == '{' {
			return p[len(p)-1] == '}' && len(bytes.TrimSpace(p[1:len(p)-1])) == 0
		}
		return p[0] != '['
	}
	return false
}

func (w *Writer) sendUntrackedFrame(wire, raw []byte, name string) error {
	if w.heldFields != 0 && !isHeartbeatData(name, raw) {
		return ErrDelivery
	}
	return w.send(wire)
}

func (w *Writer) writeEvent(wire []byte) error {
	if w.fast != nil {
		if ok, e := w.writeMatchingDelta(wire); ok {
			return e
		}
	}
	frame, err := parseSSEFields(wire, false)
	if err != nil {
		return err
	}
	name, lines, dataOffset := frame.name, frame.lines, frame.dataOffset
	if len(lines) == 0 {
		return w.send(wire)
	}
	raw := frame.payload()
	if bytes.Equal(bytes.TrimSpace(raw), []byte("[DONE]")) {
		// [DONE] 只结束字节流，缺少协议终态时由 Finish 按截断处理。
		if !w.terminal {
			if err := w.flushHeldFields(); err != nil {
				return err
			}
		}
		return w.send(wire)
	}
	if w.terminal {
		return w.sendUntrackedFrame(wire, raw, name)
	}
	if w.fast != nil && len(lines) == 1 {
		if ok, e := w.writeMatchingSnapshot(wire, raw, dataOffset, name); ok {
			return e
		}
	}
	if !utf8.Valid(raw) {
		return ErrDelivery
	}
	if codexidentity.ValidateJSON(raw) != nil {
		if !json.Valid(raw) {
			return w.sendUntrackedFrame(wire, raw, name)
		}
		return ErrDelivery // 语法合法但有重复键等歧义，仍按协议错误处理。
	}
	eventType := gjson.GetBytes(raw, "type")
	if !eventType.Exists() {
		return w.sendUntrackedFrame(wire, raw, name)
	}
	typ := eventType.Str
	if w.codex {
		if e := w.checkIdentity(raw); e != nil {
			return e
		}
	}
	if !w.codex && typ == "content_block_delta" {
		n := gjson.GetBytes(raw, "index")
		kind := w.blocks[int(n.Int())]
		delta := gjson.GetBytes(raw, "delta.type").Str
		if kind == "" && delta == "text_delta" {
			// 部分中转只发送文本 delta。字段路由与内容仍走正常回填。
			kind = "text"
		}
		if !n.Exists() || kind == "" || delta == "text_delta" && kind != "text" ||
			delta == "input_json_delta" && kind != "tool_use" && kind != "server_tool_use" {
			return ErrDelivery
		}
	}
	if ok, e := w.control(wire, raw, typ, name); ok {
		return e
	}
	var m object
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if dec.Decode(&m) != nil || m == nil {
		return ErrDelivery
	}
	typ = str(m, "type")
	if typ == "" || (name != "" && name != "message" && name != typ) {
		return ErrDelivery
	}
	w.releaseFast()
	if len(lines) == 1 && strings.HasSuffix(typ, "delta") {
		if ok, e := w.startDeltaFrame(wire, raw, dataOffset, m, typ); ok {
			return e
		}
	}
	var changed bool
	var e error
	if w.codex {
		changed, e = w.responses(m, typ)
	} else {
		changed, e = w.claude(m, typ)
	}
	if e != nil {
		return e
	}
	if w.codex && w.sequenceOffset != 0 {
		seq, ok := index(m, "sequence_number")
		if !ok {
			return ErrDelivery
		}
		m["sequence_number"] = int64(seq) + w.sequenceOffset
		changed = true
	}
	if !changed {
		return w.send(wire)
	}
	payload, e := json.Marshal(m)
	if e != nil {
		return ErrDelivery
	}
	var out bytes.Buffer
	for _, l := range frame.metadata {
		out.Write(l)
		out.WriteByte('\n')
	}
	out.WriteString("data: ")
	out.Write(payload)
	out.WriteString("\n\n")
	return w.send(out.Bytes())
}
func (w *Writer) get(k fieldKey) (*field, error) {
	if f := w.fields[k]; f != nil {
		return f, nil
	}
	if len(w.fields) >= 256 {
		return nil, ErrLocalCapacity
	}
	f := &field{key: k}
	w.fields[k] = f
	return f, nil
}
func (w *Writer) delta(k fieldKey, parent object, attr string, envelope object) (bool, error) {
	s, ok := parent[attr].(string)
	if !ok {
		return false, ErrDelivery
	}
	f, e := w.get(k)
	if e != nil {
		return false, e
	}
	if f.done {
		return false, ErrDelivery
	}
	f.declared = false
	if (w.codex || k.role == "arguments") && len(s) > maxBytes-w.rawBytes {
		return false, ErrLocalCapacity
	}
	var v string
	if k.role == "arguments" {
		v, e = f.args.feed(s, &w.toolValues)
	} else {
		v, e = f.match.feed(s, w.values(k.role), false)
	}
	if e != nil {
		return false, e
	}
	if e := w.keep(f, s, v); e != nil {
		return false, e
	}
	// 补发只需要原 delta 的路由字段，不持有整个输入片段。
	if f.match.tail != "" {
		f.last = object{"index": k.item, "type": "content_block_delta"}
		if w.codex {
			typ := "response.output_text.delta"
			if k.role == "custom" {
				typ = "response.custom_tool_call_input.delta"
			}
			f.last = object{"type": typ, "output_index": k.item}
			if k.role == "text" {
				f.last["content_index"] = k.part
			}
			if id := w.ids[k.item]; id != "" {
				f.last["item_id"] = id
			}
			if c := envelope["call_id"]; c != nil {
				// 解码值可能引用整帧；待补发的路由字段单独持有其 JSON 表示。
				id, err := json.Marshal(c)
				if err != nil {
					return false, ErrDelivery
				}
				f.last["call_id"] = json.RawMessage(id)
			}
		}
	} else {
		f.last = nil
	}
	if v == s {
		return false, nil
	}
	parent[attr] = v
	return true, nil
}

// 每个逻辑字段的交付字节只计一次；仅保存协议收尾仍需比较或校验的原文和快照。
func (w *Writer) keep(f *field, raw, out string) error {
	w.trackTail(f)
	if len(out) > maxBytes-w.outputBytes {
		return ErrLocalCapacity
	}
	w.outputBytes += len(out)
	if w.codex || f.key.role == "arguments" {
		if len(raw) > maxBytes-w.rawBytes {
			return ErrLocalCapacity
		}
		w.rawBytes += len(raw)
		f.raw.WriteString(raw)
	}
	if w.codex {
		f.output.WriteString(out)
	}
	return nil
}

func (w *Writer) closeField(f *field, next object) error {
	if f.done {
		return nil
	}
	f.done = true
	if f.key.role == "arguments" {
		err := f.args.finish(f.raw.String())
		if !w.codex {
			w.rawBytes -= f.raw.Len()
			f.raw.Reset()
			f.args = arguments{}
		}
		return err
	}
	return w.flushTail(f, next)
}

// 尾部补发独立确认，不能顺带确认外层快照。
func (w *Writer) flushTail(f *field, next object) error {
	tailValues := countedValues{limit: maxBytes}
	match := &f.match
	if f.key.role == "arguments" {
		match = &f.args.token
	}
	v, e := match.feed("", &tailValues, true)
	if f.key.role == "arguments" {
		v = quoteBody(v) + f.args.escape
		f.args.escape = ""
	}
	if e != nil || v == "" {
		return e
	}
	m := f.last
	if f.key.role == "arguments" {
		m = object{"type": "content_block_delta", "index": f.key.item}
		if w.codex {
			m = object{"type": "response.function_call_arguments.delta", "output_index": f.key.item}
			if id := w.ids[f.key.item]; id != "" {
				m["item_id"] = id
			}
			if id := w.callIDs[f.key.item]; id != "" {
				m["call_id"] = id
			}
		}
	}
	if m == nil {
		return ErrDelivery
	}
	if e = w.keep(f, "", v); e != nil {
		return e
	}
	if w.codex {
		if next == nil {
			if w.hasSequence {
				w.lastSequence++
				m["sequence_number"] = w.lastSequence
			}
		} else {
			seq, ok := index(next, "sequence_number")
			if !ok && w.socketSend == nil {
				return ErrDelivery
			}
			if ok {
				m["sequence_number"] = int64(seq) + w.sequenceOffset
				w.sequenceOffset++
			}
		}
		m["delta"] = v
	} else if f.key.role == "arguments" {
		m["delta"] = object{"type": "input_json_delta", "partial_json": v}
	} else {
		m["delta"] = object{"type": "text_delta", "text": v}
	}
	if err := w.sendTailDelta(m); err != nil {
		return err
	}
	w.values(f.key.role).unknown += tailValues.unknown
	if f.key.role == "text" {
		w.delivered.UnknownText += tailValues.unknown
	} else {
		w.delivered.UnknownTool += tailValues.unknown
	}
	return nil
}

func (w *Writer) Terminal() bool { return w.terminal }

// UnrestoredText 返回已完整交付的文本中保留的未知和半截标识次数。
func (w *Writer) UnrestoredText() int { return w.delivered.UnknownText }

// DeltaEvent 标识逐字节相同的增量事件信封；只有参数字符串和 Responses 序号可变。
type DeltaEvent struct {
	Type                  string
	DataLine, ValueOffset int
}

func (w *Writer) TakeOriginalDeltaEvent() *DeltaEvent {
	v := w.lineHint
	w.lineHint = 0
	if v == 0 || w.fast == nil || !w.fast.nonempty {
		return nil
	}
	return &w.fast.hint
}
