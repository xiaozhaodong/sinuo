package restorestream

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"sinuo/internal/privacy"
)

// 对照标准库的完整值树，包括转义键、空容器和数字的原始表示。
func TestWebSocketDecodeMatchesStandardLibrary(t *testing.T) {
	for name, fields := range map[string]string{
		"common":  `"键\"\\\/\b\f\n\r\t":"中文🙂\"\\\/\b\f\n\r\t","all":[{},[],null,true,false,-0,9007199254740993,1.2300e+999]`,
		"unicode": `"\u952e":"\u0061\ud83d\ude00\ud800\u0041\udc00","literal":"\\u1234"`,
		"deep":    `"nested":` + strings.Repeat("[", 40) + `{"key":9007199254740993}` + strings.Repeat("]", 40),
	} {
		t.Run(name, func(t *testing.T) {
			body := []byte("{" + fields + "}")
			for _, size := range []int{len(body), largeWSFrame - 1, largeWSFrame, largeWSFrame + 1} {
				raw := append(bytes.Repeat([]byte(" "), size-len(body)), body...)
				msg, err := ValidateMessage(raw)
				if err != nil {
					t.Fatal(err)
				}
				var want object
				dec := json.NewDecoder(bytes.NewReader(raw))
				dec.UseNumber()
				if err := dec.Decode(&want); err != nil {
					t.Fatal(err)
				}
				got, err := msg.decodeObject()
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("%d 字节解码不同: got=%#v want=%#v err=%v", size, got, want, err)
				}
			}
		})
	}
}

func TestWebSocketToolSnapshotMatchesFullDecode(t *testing.T) {
	source := strings.Repeat("a\n\t/中文🙂\"\\", largeWSFrame/8)
	quoted, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	field := &field{key: fieldKey{role: "arguments"}}
	field.raw.WriteString(source)
	w := New(nil, "/v1/responses", nil)
	w.fields[field.key] = field
	defer w.Release()

	const done = `"type":"response.function_call_arguments.done","output_index":0`
	const completed = `"type":"response.completed","response":{"output":`
	args := `"arguments":` + string(quoted)
	item := `{"type":"function_call",` + args + `}`
	for _, tc := range []struct {
		name, raw string
		reuse     bool
	}{
		{"arguments", `{` + done + `,` + args + `}`, true},
		{"item", `{"type":"response.output_item.done","item":` + item + `}`, true},
		{"completed", `{` + completed + `[` + item + `],"id":"r"}}`, true},
		{"escaped-slash", `{` + done + `,` + strings.ReplaceAll(args, "/", `\/`) + `}`, true},
		{"different", `{` + done + `,` + strings.Replace(args, `:"a`, `:"b`, 1) + `}`, false},
		{"unicode", `{` + done + `,` + strings.Replace(args, "中文", `\u4e2d文`, 1) + `}`, false},
		{"decoy", `{"extra":{` + args + `},` + done + `,"arguments":"other"}`, false},
		{"multiple-outputs", `{` + completed + `[` + item + `,{}]}}`, false},
		{"object-output", `{` + completed + `{"#":1,"0":` + item + `}}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg, err := ValidateMessage([]byte(tc.raw))
			if err != nil {
				t.Fatal(err)
			}
			if got, _ := w.decodeToolSnapshot(msg); (got != nil) != tc.reuse {
				t.Fatalf("期望复用快照 %t", tc.reuse)
			}
			want, err := msg.decodeObject()
			if err != nil {
				t.Fatal(err)
			}
			got, err := w.decodeWebSocketEvent(msg)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatal("解码结果与完整帧不符", err)
			}
		})
	}
}

func TestLargeWebSocketEscapedDelivery(t *testing.T) {
	token := privacy.RestorePrefix + strings.Repeat("a", 32) + "}}"
	unknown := privacy.RestorePrefix + strings.Repeat("b", 32) + "}}"
	const original = "值含引号\"、反斜杠\\和换行\n"
	const line = "if (path == \"C:\\\\tmp\") {\n\treturn \"中文/🙂\";\n}\n"
	source := strings.Repeat(line, largeWSFrame/len(line)+1) + token + " " + unknown
	for _, role := range []string{"output_text", "function_call_arguments"} {
		t.Run(role, func(t *testing.T) {
			encode := func(v any) []byte {
				b, err := json.Marshal(v)
				if err != nil {
					t.Fatal(err)
				}
				return b
			}
			wire, want := source, strings.Replace(source, token, original, 1)
			attr, snapshot := "text", "response.output.0.content.0.text"
			item := object{"type": "message", "id": "m", "content": []any{object{"type": "output_text", "text": wire}}}
			counts := DeliveryCounts{Text: 1, UnknownText: 1}
			if role == "function_call_arguments" {
				wire = string(encode(object{"code": wire}))
				want = string(encode(object{"code": want}))
				attr, snapshot = "arguments", "response.output.0.arguments"
				item = object{"type": "function_call", "id": "m", "call_id": "c", "name": "run", "arguments": wire}
				counts = DeliveryCounts{Tool: 1, UnknownTool: 1}
			}
			frame := func(event, field, value string) []byte {
				return encode(object{"type": "response." + role + "." + event, "output_index": 0, "content_index": 0, "item_id": "m", "call_id": "c", field: value})
			}
			cut := strings.Index(wire, token) + len(privacy.RestorePrefix)/2
			d := &recordingDictionary{token: token, original: original}
			w := New(nil, "/v1/responses", d)
			defer w.Release()
			var assembled strings.Builder
			var full, completed string
			for _, p := range [][]byte{
				frame("delta", "delta", wire[:cut]), frame("delta", "delta", wire[cut:]),
				frame("done", attr, wire),
				encode(object{"type": "response.completed", "response": object{"output": []any{item}, "usage": object{"input_tokens": json.Number("9007199254740993")}}}),
			} {
				if err := w.WriteMessage(p, func(out []byte) error {
					switch gjson.GetBytes(out, "type").Str {
					case "response." + role + ".delta":
						assembled.WriteString(gjson.GetBytes(out, "delta").Str)
					case "response." + role + ".done":
						full = gjson.GetBytes(out, attr).Str
					case "response.completed":
						completed = gjson.GetBytes(out, snapshot).Str
						if gjson.GetBytes(out, "response.usage.input_tokens").Raw != "9007199254740993" {
							t.Fatal("用量失真")
						}
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			if assembled.String() != want || full != want || completed != want {
				t.Fatal("增量、完成快照或终态正文与预期不符")
			}
			if !w.Terminal() || w.Delivered() != counts || d.text != counts.Text || d.tool != counts.Tool {
				t.Fatalf("交付记录不符: %+v", w.Delivered())
			}

			failed := New(nil, "/v1/responses", d)
			defer failed.Release()
			err := failed.WriteMessage(frame("delta", "delta", wire), func([]byte) error { return io.ErrShortWrite })
			if !errors.Is(err, io.ErrShortWrite) || failed.Delivered() != (DeliveryCounts{}) || d.text != counts.Text || d.tool != counts.Tool {
				t.Fatal("写出失败却确认了交付", err)
			}
		})
	}
}
