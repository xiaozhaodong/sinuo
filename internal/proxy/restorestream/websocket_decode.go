package restorestream

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"

	"github.com/tidwall/gjson"
)

const largeWSFrame = 64 << 10

type wsDecodeMode uint8

const (
	wsDelivery wsDecodeMode = iota
	wsObservation
)

func (msg ValidatedMessage) decodeObject() (object, error) {
	if m, ok := msg.fastObject(wsDelivery); ok {
		return m, nil
	}
	var m object
	dec := json.NewDecoder(bytes.NewReader(msg.raw))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return nil, ErrDelivery
	}
	return m, nil
}

// DecodeForObservation 为读侧提供与 json.Unmarshal 相同的数字类型和溢出语义。
// 观测只同步使用这棵树，队列持有的 ValidatedMessage 仍然只含原始帧。
func (msg ValidatedMessage) DecodeForObservation() (map[string]any, error) {
	if m, ok := msg.fastObject(wsObservation); ok {
		return m, nil
	}
	var m object
	if err := json.Unmarshal(msg.raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// 原文中出现 \u（含字面量 \\u）就整帧走标准库，保留键和代理项的解码语义。
func (msg ValidatedMessage) fastObject(mode wsDecodeMode) (object, bool) {
	if len(msg.raw) < largeWSFrame || bytes.Contains(msg.raw, []byte("\\u")) {
		return nil, false
	}
	value, ok := mode.value(gjson.Parse(string(msg.raw)), 0)
	if !ok {
		return nil, false
	}
	m, ok := value.(object)
	return m, ok
}

// ForEach 会扫描子容器的边界；达到八层时整帧改用标准库，避免深层载荷反复扫描。
func (mode wsDecodeMode) value(v gjson.Result, depth int) (any, bool) {
	switch v.Type {
	case gjson.String:
		// 未转义的字符串引用整帧。观测可能长期持有响应 ID、模型名或错误详情。
		if mode == wsObservation && len(v.Str)+2 == len(v.Raw) {
			return strings.Clone(v.Str), true
		}
		return v.Str, true
	case gjson.Number:
		if mode == wsObservation {
			return v.Num, !math.IsInf(v.Num, 0)
		}
		// 写侧重编码必须保留上游数字的原始字面量。
		return json.Number(v.Raw), true
	case gjson.True:
		return true, true
	case gjson.False:
		return false, true
	case gjson.Null:
		return nil, true
	}
	if depth >= 8 {
		return nil, false
	}
	ok := true
	if v.IsArray() {
		a := []any{}
		v.ForEach(func(_, child gjson.Result) bool {
			var value any
			value, ok = mode.value(child, depth+1)
			if ok {
				a = append(a, value)
			}
			return ok
		})
		return a, ok
	}
	m := object{}
	v.ForEach(func(key, child gjson.Result) bool {
		m[key.Str], ok = mode.value(child, depth+1)
		return ok
	})
	return m, ok
}
