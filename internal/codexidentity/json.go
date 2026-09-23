package codexidentity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"unicode/utf8"
)

// ValidateJSON 拒绝重复键、多个文档和过深嵌套，不把数字转成浮点数。
func ValidateJSON(raw []byte) error {
	_, err := scanJSON(raw, 0, false)
	return err
}

// JSONFields 读取对象直属字段；值保留原始字节表示。
func JSONFields(raw []byte) (map[string]json.RawMessage, error) {
	object, err := parseObject(raw)
	if err != nil {
		return nil, err
	}
	// 保留原 API 的独立字节所有权；请求内的映射使用只读字段视图避免复制大正文。
	fields := make(map[string]json.RawMessage, len(object.fields))
	for key, value := range object.fields {
		fields[key] = bytes.Clone(value)
	}
	return fields, nil
}

// jsonObject 只保存已校验正文的直属字段位置，不解码用户 input、数字或密文。
// raw 必须保持只读；请求缓存持有自己的副本，不能用调用方的可变切片充当校验凭证。
type jsonObject struct {
	raw    []byte
	spans  []jsonFieldSpan
	fields map[string]json.RawMessage
}

func parseObject(raw []byte) (*jsonObject, error) {
	spans, err := scanJSON(raw, 0, true)
	if err != nil {
		return nil, err
	}
	if bytes.TrimSpace(raw)[0] != '{' {
		return nil, fmt.Errorf("JSON object required")
	}
	object := &jsonObject{raw: raw, spans: spans, fields: make(map[string]json.RawMessage, len(spans))}
	for _, span := range spans {
		object.fields[span.name] = raw[span.valueStart:span.end]
	}
	return object, nil
}

// 语法由标准库校验；第二步只扫描容器、键和字节位置，避免 Decoder.Token 对每个值分配对象。
// escaped / 非 UTF-8 键仍用标准库解码，保持 Unicode 等价键的重复检测语义。
type jsonScanner struct {
	raw       []byte
	pos       int
	indexRoot bool
	spans     []jsonFieldSpan
}

func scanJSON(raw []byte, depth int, indexRoot bool) ([]jsonFieldSpan, error) {
	if !json.Valid(raw) {
		return nil, fmt.Errorf("invalid JSON")
	}
	s := jsonScanner{raw: raw, indexRoot: indexRoot}
	err := s.value(depth)
	return s.spans, err
}

func (s *jsonScanner) space() {
	for s.pos < len(s.raw) {
		switch s.raw[s.pos] {
		case ' ', '\n', '\r', '\t':
			s.pos++
		default:
			return
		}
	}
}

func (s *jsonScanner) stringEnd() {
	s.pos++ // 起始引号；仅在 json.Valid 成功后调用。
	for s.raw[s.pos] != '"' {
		if s.raw[s.pos] == '\\' {
			s.pos++
		}
		s.pos++
	}
	s.pos++
}

func (s *jsonScanner) value(depth int) error {
	if depth > 256 {
		return fmt.Errorf("JSON nesting exceeds 256")
	}
	s.space()
	switch s.raw[s.pos] {
	case '{':
		return s.object(depth)
	case '[':
		s.pos++
		s.space()
		for s.raw[s.pos] != ']' {
			if err := s.value(depth + 1); err != nil {
				return err
			}
			s.space()
			if s.raw[s.pos] == ',' {
				s.pos++
			}
		}
		s.pos++
	case '"':
		s.stringEnd()
	default:
		for s.pos < len(s.raw) {
			switch s.raw[s.pos] {
			case ',', ']', '}', ' ', '\t', '\r', '\n':
				return nil
			}
			s.pos++
		}
	}
	return nil
}

func (s *jsonScanner) object(depth int) error {
	s.pos++
	s.space()
	keys := make(map[string]struct{})
	for s.raw[s.pos] != '}' {
		start := s.pos
		s.stringEnd()
		encoded := s.raw[start:s.pos]
		key := string(encoded[1 : len(encoded)-1])
		if bytes.IndexByte(encoded, '\\') >= 0 || !utf8.Valid(encoded) {
			if err := json.Unmarshal(encoded, &key); err != nil {
				return err
			}
		}
		if _, exists := keys[key]; exists {
			return fmt.Errorf("duplicate JSON key")
		}
		keys[key] = struct{}{}
		s.space()
		s.pos++ // 冒号已由 json.Valid 校验。
		s.space()
		valueStart := s.pos
		if err := s.value(depth + 1); err != nil {
			return err
		}
		if depth == 0 && s.indexRoot {
			s.spans = append(s.spans, jsonFieldSpan{key, start, valueStart, s.pos})
		}
		s.space()
		if s.raw[s.pos] == ',' {
			s.pos++
			s.space()
		}
	}
	s.pos++
	return nil
}

type jsonSpan struct {
	start, end int
	value      []byte
}
type jsonFieldSpan struct {
	name                   string
	start, valueStart, end int
}

// EditObject 只替换/删除指定直属字段；nil 表示删除。其他字段的表示逐字节保留。
// 同一 helper 供身份映射、模型改写及 input 内部 metadata 清理使用。
func EditObject(raw []byte, changes map[string][]byte) ([]byte, error) {
	object, err := parseObject(raw)
	if err != nil {
		return nil, err
	}
	updated, _, err := object.edit(changes)
	return updated, err
}

// edit 复用原文校验和位置，只校验替换值；返回字段视图供指纹使用，无需重读生成后的大正文。
func (o *jsonObject) edit(changes map[string][]byte) ([]byte, map[string]json.RawMessage, error) {
	raw, fields := o.raw, o.spans
	updatedFields := make(map[string]json.RawMessage, len(o.fields)+len(changes))
	for key, value := range o.fields {
		updatedFields[key] = value
	}
	for key, value := range changes {
		if !utf8.ValidString(key) {
			return nil, nil, fmt.Errorf("invalid replacement JSON key")
		}
		if value == nil {
			delete(updatedFields, key)
			continue
		}
		// 替换值从根对象的下一层开始，不能绕过深度或重复键约束。
		if _, err := scanJSON(value, 1, false); err != nil {
			return nil, nil, fmt.Errorf("invalid replacement JSON: %w", err)
		}
		updatedFields[key] = value
	}
	var edits []jsonSpan
	seen := map[string]bool{}
	for i := 0; i < len(fields); {
		f := fields[i]
		value, exists := changes[f.name]
		seen[f.name] = true
		if !exists {
			i++
			continue
		}
		if value != nil {
			if !bytes.Equal(raw[f.valueStart:f.end], value) {
				edits = append(edits, jsonSpan{f.valueStart, f.end, value})
			}
			i++
			continue
		}
		j := i
		for j+1 < len(fields) {
			next, exists := changes[fields[j+1].name]
			if !exists || next != nil {
				break
			}
			j++
			seen[fields[j].name] = true
		}
		start, end := f.start, fields[j].end
		if j+1 < len(fields) {
			end = fields[j+1].start
		} else if i > 0 {
			start = fields[i-1].end
		}
		edits = append(edits, jsonSpan{start, end, nil})
		i = j + 1
	}
	var additions []string
	for key, value := range changes {
		if !seen[key] && value != nil {
			additions = append(additions, key)
		}
	}
	sort.Strings(additions)
	if len(additions) > 0 {
		kept := 0
		for _, f := range fields {
			v, exists := changes[f.name]
			if !exists || v != nil {
				kept++
			}
		}
		var extra []byte
		for _, key := range additions {
			if kept > 0 {
				extra = append(extra, ',')
			}
			kept++
			extra = append(extra, JSONString(key)...)
			extra = append(extra, ':')
			extra = append(extra, changes[key]...)
		}
		closeAt := bytes.LastIndexByte(raw, '}')
		edits = append(edits, jsonSpan{closeAt, closeAt, extra})
	}
	if len(edits) == 0 {
		return raw, updatedFields, nil
	}
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	size := len(raw)
	for _, e := range edits {
		size += len(e.value) - (e.end - e.start)
	}
	out := make([]byte, 0, size)
	pos := 0
	for _, e := range edits {
		if e.start < pos {
			return nil, nil, fmt.Errorf("overlapping JSON edits")
		}
		out = append(out, raw[pos:e.start]...)
		out = append(out, e.value...)
		pos = e.end
	}
	return append(out, raw[pos:]...), updatedFields, nil
}

// JSONString 将新字符串编码成 JSON；未编辑的原字段不经过重新编码。
func JSONString(value string) []byte { raw, _ := json.Marshal(value); return raw }
