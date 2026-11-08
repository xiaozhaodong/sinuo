package restorestream

import (
	"bytes"
	"encoding/json"
	"unicode/utf8"

	"github.com/tidwall/gjson"

	"sinuo/internal/codexidentity"
)

// MaxJSONBytes 是非流式响应解压后及回填后的整包上限。
const MaxJSONBytes = 16 << 20

// JSONDelivery 的计数和关联在调用方完整写出 Body 后确认。
type JSONDelivery struct {
	Body   []byte
	ledger deliveryLedger
}

// Confirm 可重复读取同一收据，字典计数只更新一次。
func (d *JSONDelivery) Confirm() DeliveryCounts {
	d.ledger.confirmDelivery()
	return d.ledger.delivered
}

type jsonRestore struct {
	*deliveryLedger
	raw    []byte
	out    bytes.Buffer
	cursor int
}

// RestoreJSON 校验完整响应和嵌套工具参数，按原字段位置替换，保留 usage、密文及其他字段的原字节。
func RestoreJSON(path string, body []byte, dict Dictionary) (*JSONDelivery, error) {
	if len(body) > MaxJSONBytes || !utf8.Valid(body) || codexidentity.ValidateJSON(body) != nil {
		return nil, ErrDelivery
	}
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return nil, ErrDelivery
	}
	delivery := &JSONDelivery{Body: body, ledger: newLedger(dict, MaxJSONBytes)}
	r := jsonRestore{deliveryLedger: &delivery.ledger, raw: body}
	var err error
	switch path {
	case "/v1/messages":
		err = visitObjects(root.Get("content"), r.claudeBlock)
	case "/v1/responses":
		err = visitObjects(root.Get("output"), r.item)
	default:
		return nil, ErrDelivery
	}
	if err != nil {
		return nil, err
	}
	if r.cursor > 0 {
		if len(body)-r.cursor > MaxJSONBytes-r.out.Len() {
			return nil, ErrDelivery
		}
		r.out.Write(body[r.cursor:])
		delivery.Body = r.out.Bytes()
	}
	return delivery, nil
}

func visitObjects(list gjson.Result, visit func(gjson.Result) error) error {
	if !list.IsArray() {
		return ErrDelivery
	}
	var err error
	list.ForEach(func(_, v gjson.Result) bool {
		if !v.IsObject() || v.Get("type").Str == "" {
			err = ErrDelivery
		} else {
			err = visit(v)
		}
		return err == nil
	})
	return err
}

func (r *jsonRestore) claudeBlock(block gjson.Result) error {
	switch block.Get("type").Str {
	case "text":
		return r.field(block.Get("text"), "text")
	case "tool_use", "server_tool_use":
		input := block.Get("input")
		if !input.IsObject() {
			return ErrDelivery
		}
		return r.toolTree(input)
	}
	return nil
}

func (r *jsonRestore) toolTree(v gjson.Result) error {
	if v.Type == gjson.String {
		return r.field(v, "custom")
	}
	var err error
	if v.IsObject() || v.IsArray() {
		v.ForEach(func(_, child gjson.Result) bool {
			err = r.toolTree(child)
			return err == nil
		})
	}
	return err
}

func (r *jsonRestore) item(item gjson.Result) error {
	switch item.Get("type").Str {
	case "message":
		return visitObjects(item.Get("content"), func(part gjson.Result) error {
			if part.Get("type").Str == "output_text" {
				return r.field(part.Get("text"), "text")
			}
			return nil
		})
	case "function_call":
		return r.field(item.Get("arguments"), "arguments")
	case "custom_tool_call":
		return r.field(item.Get("input"), "custom")
	}
	return nil
}

func (r *jsonRestore) field(v gjson.Result, role string) error {
	if v.Type != gjson.String {
		return ErrDelivery
	}
	d := r.values(role)
	before := d.count
	var out string
	var err error
	if role == "arguments" {
		var a arguments
		out, err = a.feed(v.Str, d)
		if err == nil {
			err = a.finish(v.Str)
		}
	} else {
		var m matcher
		out, err = m.feed(v.Str, d, true)
	}
	if err != nil || before == d.count {
		return err
	}
	encoded, err := json.Marshal(out)
	if err != nil || v.Index < r.cursor || v.Index-r.cursor+len(encoded) > MaxJSONBytes-r.out.Len() {
		return ErrDelivery
	}
	r.out.Write(r.raw[r.cursor:v.Index])
	r.out.Write(encoded)
	r.cursor = v.Index + len(v.Raw)
	return nil
}
