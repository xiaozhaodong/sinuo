package restorestream

import (
	"bytes"
	"encoding/json"

	"github.com/tidwall/gjson"
)

const largeWSFrame = 64 << 10

// 只展开已严格校验的大帧。原文中出现 \u（含字面量 \\u）就整帧走标准库，
// 使键、孤立代理项和代理对都沿用 encoding/json 的解码语义。
func (msg ValidatedMessage) decodeObject() (object, error) {
	raw := msg.raw
	if len(raw) >= largeWSFrame && !bytes.Contains(raw, []byte("\\u")) {
		if value, ok := wsJSONValue(gjson.Parse(string(raw)), 0); ok {
			if m, ok := value.(object); ok {
				return m, nil
			}
			return nil, ErrDelivery
		}
	}
	var m object
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return nil, ErrDelivery
	}
	return m, nil
}

// ForEach 会扫描子容器的边界；达到八层时整帧改用标准库，避免深层载荷反复扫描。
// 数字始终取原始字面量，重编码时不经过 float64。
func wsJSONValue(v gjson.Result, depth int) (any, bool) {
	switch v.Type {
	case gjson.String:
		return v.Str, true
	case gjson.Number:
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
			value, ok = wsJSONValue(child, depth+1)
			if ok {
				a = append(a, value)
			}
			return ok
		})
		return a, ok
	}
	m := object{}
	v.ForEach(func(key, child gjson.Result) bool {
		m[key.Str], ok = wsJSONValue(child, depth+1)
		return ok
	})
	return m, ok
}
