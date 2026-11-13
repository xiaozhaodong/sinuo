package privacy

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// walkJSONText 边校验 JSON 语法边定位原始字符串区间，避免 Decoder.Token 的逐值分配。
// 普通扫描沿用 Decoder 的多文档与重复键语义；可逆模式只接受单个对象，
// 拒绝解码后重复的键，并沿用 256 层值路径上限。路径、匹配与 replay 观察共用这次遍历。
func walkJSONText(body []byte, match segmentMatcher, replay *restoreInput) ([]textSegment, error) {
	w := jsonTextWalker{body: body, match: match, replay: replay}
	for {
		w.space()
		if len(w.stack) == 0 {
			if w.pos == len(body) {
				if replay != nil && !w.seen {
					return nil, fmt.Errorf("empty request object")
				}
				return w.segments, nil
			}
			if err := w.value(); err != nil {
				return nil, err
			}
			continue
		}
		if w.pos == len(body) {
			return nil, fmt.Errorf("unexpected end of JSON")
		}
		f := &w.stack[len(w.stack)-1]
		switch f.state {
		case jsonExpectFirst, jsonExpectNext:
			end := byte(']')
			if f.object {
				end = '}'
			}
			if body[w.pos] == end && f.state == jsonExpectFirst {
				w.pos++
				w.stack = w.stack[:len(w.stack)-1]
				w.advance()
				continue
			}
			if !f.object {
				if err := w.value(); err != nil {
					return nil, err
				}
				continue
			}
			if body[w.pos] != '"' {
				return nil, fmt.Errorf("JSON object key required")
			}
			raw, err := w.stringToken()
			if err != nil {
				return nil, err
			}
			key := decodeValidatedJSONString(raw)
			if replay != nil {
				if f.keys == nil {
					f.keys = make(map[string]struct{})
				}
				if _, ok := f.keys[key]; ok {
					return nil, fmt.Errorf("duplicate object key")
				}
				f.keys[key] = struct{}{}
			}
			f.key, f.state = key, jsonExpectColon
		case jsonExpectColon:
			if body[w.pos] != ':' {
				return nil, fmt.Errorf("JSON colon required")
			}
			w.pos++
			f.state = jsonExpectValue
		case jsonExpectValue:
			if err := w.value(); err != nil {
				return nil, err
			}
		case jsonExpectSeparator:
			end := byte(']')
			if f.object {
				end = '}'
			}
			switch body[w.pos] {
			case end:
				w.pos++
				w.stack = w.stack[:len(w.stack)-1]
				w.advance()
			case ',':
				w.pos++
				f.state = jsonExpectNext
			default:
				return nil, fmt.Errorf("JSON comma or container end required")
			}
		}
	}
}

// 对象先读键与冒号，数组直接读值；值结束后统一等待逗号或右括号。
const (
	jsonExpectFirst uint8 = iota
	jsonExpectNext
	jsonExpectColon
	jsonExpectValue
	jsonExpectSeparator
)

type jsonTextFrame struct {
	keys   map[string]struct{}
	key    string
	index  int
	state  uint8
	object bool
}

type jsonTextWalker struct {
	body     []byte
	pos      int
	seen     bool
	match    segmentMatcher
	replay   *restoreInput
	stack    []jsonTextFrame
	path     []pathElem
	segments []textSegment
}

func (w *jsonTextWalker) space() {
	for w.pos < len(w.body) {
		switch w.body[w.pos] {
		case ' ', '\t', '\r', '\n':
			w.pos++
		default:
			return
		}
	}
}

func (w *jsonTextWalker) currentPath() []pathElem {
	w.path = w.path[:0]
	for _, f := range w.stack {
		if f.object {
			w.path = append(w.path, pathElem{Key: f.key, Index: -1})
		} else {
			w.path = append(w.path, pathElem{Index: f.index})
		}
	}
	return w.path
}

func (w *jsonTextWalker) advance() {
	if len(w.stack) > 0 {
		f := &w.stack[len(w.stack)-1]
		f.state = jsonExpectSeparator
		if !f.object {
			f.index++
		}
	}
}

func (w *jsonTextWalker) value() error {
	if w.pos >= len(w.body) {
		return fmt.Errorf("JSON value required")
	}
	c := w.body[w.pos]
	if w.replay != nil {
		if len(w.stack) == 0 && (w.seen || c != '{') || len(w.stack) > 256 {
			return fmt.Errorf("invalid request object or nesting")
		}
	}
	if len(w.stack) == 0 {
		w.seen = true
	}
	// 普通扫描只为字符串匹配构造路径，避免深层非文本容器的平方级遍历。
	var path []pathElem
	if w.replay != nil || c == '"' && w.match != nil {
		path = w.currentPath()
	}
	start := w.pos
	var token any
	var text string
	matched := c == '"' && w.match != nil && w.match(path)
	switch c {
	case '{', '[':
		w.pos++
		token = json.Delim(c)
	case '"':
		raw, err := w.stringToken()
		if err != nil {
			return err
		}
		if matched || w.replay != nil {
			text = decodeValidatedJSONString(raw)
			token = text
		}
	case 't', 'f', 'n':
		literal := "null"
		if c == 't' {
			literal, token = "true", true
		}
		if c == 'f' {
			literal, token = "false", false
		}
		if !bytes.HasPrefix(w.body[w.pos:], []byte(literal)) {
			return fmt.Errorf("invalid JSON literal")
		}
		w.pos += len(literal)
	default:
		if err := w.number(); err != nil {
			return err
		}
		if w.replay != nil {
			token = json.Number(string(w.body[start:w.pos]))
		}
	}
	if w.replay != nil {
		if err := w.replay.observe(path, token); err != nil {
			return err
		}
	}
	if c == '{' || c == '[' {
		w.stack = append(w.stack, jsonTextFrame{object: c == '{'})
		return nil
	}
	if matched {
		w.segments = append(w.segments, textSegment{
			Start: start, End: w.pos, Value: text, IsJSON: true,
			Path: append([]pathElem(nil), path...),
		})
	}
	w.advance()
	return nil
}

func (w *jsonTextWalker) stringToken() ([]byte, error) {
	start := w.pos
	w.pos++
	for w.pos < len(w.body) {
		c := w.body[w.pos]
		w.pos++
		switch {
		case c == '"':
			return w.body[start:w.pos], nil
		case c < 0x20:
			return nil, fmt.Errorf("invalid JSON string control byte")
		case c == '\\':
			if w.pos == len(w.body) {
				return nil, fmt.Errorf("incomplete JSON escape")
			}
			c = w.body[w.pos]
			w.pos++
			switch c {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
			case 'u':
				if len(w.body)-w.pos < 4 {
					return nil, fmt.Errorf("incomplete JSON unicode escape")
				}
				for end := w.pos + 4; w.pos < end; w.pos++ {
					c = w.body[w.pos]
					if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
						return nil, fmt.Errorf("invalid JSON unicode escape")
					}
				}
			default:
				return nil, fmt.Errorf("invalid JSON escape")
			}
		}
	}
	return nil, fmt.Errorf("unterminated JSON string")
}

func (w *jsonTextWalker) number() error {
	start := w.pos
	if w.body[w.pos] == '-' {
		w.pos++
	}
	if w.pos == len(w.body) {
		return fmt.Errorf("invalid JSON number")
	}
	if w.body[w.pos] == '0' {
		w.pos++
	} else {
		if w.body[w.pos] < '1' || w.body[w.pos] > '9' {
			return fmt.Errorf("invalid JSON value")
		}
		for w.pos < len(w.body) && w.body[w.pos] >= '0' && w.body[w.pos] <= '9' {
			w.pos++
		}
	}
	if w.pos < len(w.body) && w.body[w.pos] == '.' {
		w.pos++
		digits := w.pos
		for w.pos < len(w.body) && w.body[w.pos] >= '0' && w.body[w.pos] <= '9' {
			w.pos++
		}
		if w.pos == digits {
			return fmt.Errorf("invalid JSON fraction")
		}
	}
	if w.pos < len(w.body) && (w.body[w.pos] == 'e' || w.body[w.pos] == 'E') {
		w.pos++
		if w.pos < len(w.body) && (w.body[w.pos] == '+' || w.body[w.pos] == '-') {
			w.pos++
		}
		digits := w.pos
		for w.pos < len(w.body) && w.body[w.pos] >= '0' && w.body[w.pos] <= '9' {
			w.pos++
		}
		if w.pos == digits {
			return fmt.Errorf("invalid JSON exponent")
		}
	}
	if w.pos == start {
		return fmt.Errorf("invalid JSON number")
	}
	return nil
}
