package restorestream

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"unicode/utf8"

	"github.com/tidwall/gjson"

	"sinuo/internal/codexidentity"
)

// WriteMessage 处理一条完整的原生 Responses 消息。字段状态、工具 JSON 与终态快照
// 共用 SSE 的协议实现，每次 send 完成后才确认对应的交付计数。
func (w *Writer) WriteMessage(payload []byte, send func([]byte) error) error {
	if w.terminal || len(payload) > maxBytes || !utf8.Valid(payload) || codexidentity.ValidateJSON(payload) != nil {
		return ErrDelivery
	}
	if err := w.checkIdentity(payload); err != nil {
		return err
	}
	var m object
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()
	if dec.Decode(&m) != nil || str(m, "type") == "" {
		return ErrDelivery
	}
	if id := str(m, "stream_id"); id != "" && id != w.streamID {
		if w.streamID != "" {
			return ErrDelivery
		}
		var err error
		if w.streamID, err = w.retainID(id); err != nil {
			return err
		}
	}
	w.socketSend = send
	defer func() { w.socketSend = nil }()
	typ := str(m, "type")
	// 原生中断可在工具 JSON 仍未闭合时丢弃输出项。客户端收到中断事件，
	// 随后的 incomplete 不应把这段已经放弃的参数当成一次成功工具调用。
	why := str(obj(obj(m, "response"), "incomplete_details"), "reason")
	if typ == "response.output_item.interrupted" || typ == "response.incomplete" && (why == "interrupted" || why == "steered") {
		item, hasItem := index(m, "output_index")
		for _, f := range w.fields {
			if f.key.role != "text" && (typ == "response.incomplete" || hasItem && f.key.item == item) {
				f.done = true
			}
		}
	}
	changed, err := w.responses(m, typ)
	if err != nil {
		return err
	}
	if seq, ok := index(m, "sequence_number"); ok {
		w.lastSequence, w.hasSequence = int64(seq)+w.sequenceOffset, true
	}
	if changed {
		payload, err = json.Marshal(m)
		if err != nil || len(payload) > maxBytes {
			return ErrDelivery
		}
	}
	payload, err = OffsetWebSocketSequence(payload, w.sequenceOffset)
	if err != nil {
		return err
	}
	if err = send(payload); err == nil {
		w.confirmDelivery()
	}
	return err
}

// SequenceOffset 是该响应为补发增量占用的序号数，由同一写协程用于生成帧和控制回执。
func (w *Writer) SequenceOffset() int64 { return w.sequenceOffset }

// OffsetWebSocketSequence 仅改写原消息的序号，保留控制回执的其余字节和无序号帧。
func OffsetWebSocketSequence(payload []byte, offset int64) ([]byte, error) {
	if offset == 0 {
		return payload, nil
	}
	seq := gjson.GetBytes(payload, "sequence_number")
	if !seq.Exists() {
		return payload, nil
	}
	n, err := strconv.ParseInt(seq.Raw, 10, 64)
	if err != nil || n < 0 || n > math.MaxInt64-offset {
		return nil, ErrDelivery
	}
	var digits [20]byte
	replacement := strconv.AppendInt(digits[:0], n+offset, 10)
	size := len(payload) - len(seq.Raw) + len(replacement)
	if size > maxBytes {
		return nil, ErrDelivery
	}
	out := make([]byte, 0, size)
	out = append(out, payload[:seq.Index]...)
	out = append(out, replacement...)
	return append(out, payload[seq.Index+len(seq.Raw):]...), nil
}

// 补发的增量继承原生响应的路由标识；HTTP 则保留 SSE 事件封装。
func (w *Writer) sendTailDelta(m object) error {
	if w.socketSend != nil {
		if w.responseID != "" {
			m["response_id"] = w.responseID
		}
		if w.streamID != "" {
			m["stream_id"] = w.streamID
		}
	}
	payload, e := json.Marshal(m)
	if e != nil {
		return ErrDelivery
	}
	if w.socketSend != nil {
		return w.socketSend(payload)
	}
	return w.send([]byte("event: " + str(m, "type") + "\ndata: " + string(payload) + "\n\n"))
}
