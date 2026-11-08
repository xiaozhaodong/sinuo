package restorestream

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"sinuo/internal/privacy"
)

type recordingDictionary struct {
	token, original string
	text, tool      int
	lookupErr       error
}

func (d *recordingDictionary) Lookup(token string) (string, bool) {
	return d.original, token == d.token
}
func (d *recordingDictionary) LookupError() error { return d.lookupErr }
func (d *recordingDictionary) RecordDelivery(token string, tool bool, n int) {
	if token != d.token {
		panic("记录了未知映射")
	}
	if tool {
		d.tool += n
	} else {
		d.text += n
	}
}

func TestRegularMixedValuesAndReceipt(t *testing.T) {
	token := privacy.RestorePrefix + strings.Repeat("a", 32) + "}}"
	unknown := privacy.RestorePrefix + strings.Repeat("b", 32) + "}}"
	half := privacy.RestorePrefix + "abc"
	s := unknown + " " + token + " " + half
	original := "合成\"值\\带\n换行"
	want := strings.Replace(s, token, original, 1)
	encoded, _ := json.Marshal(s)
	nested, _ := json.Marshal(map[string]string{token: s})
	nestedEncoded, _ := json.Marshal(string(nested))
	for _, path := range []string{"/v1/messages", "/v1/responses"} {
		d := &recordingDictionary{token: token, original: original}
		raw := `   {"id":"r","usage": { "large": 9007199254740993 },"content":[{"type":"text","text":` + string(encoded) + `},{"type":"tool_use","input":` + string(nested) + `},{"type":"thinking","thinking":` + string(encoded) + `}]} `
		counts := DeliveryCounts{Text: 1, Tool: 1, UnknownText: 2, UnknownTool: 2}
		if path == "/v1/responses" {
			raw = `   {"id":"r","usage": { "large": 9007199254740993 },"output":[{"type":"message","id":"i","content":[{"type":"output_text","text":` + string(encoded) + `}]},{"type":"function_call","arguments":` + string(nestedEncoded) + `},{"type":"custom_tool_call","input":` + string(encoded) + `},{"type":"reasoning","encrypted_content":` + string(encoded) + `}]} `
			counts.Tool, counts.UnknownTool = 2, 4
		}
		delivery, err := RestoreJSON(path, []byte(raw), d)
		if err != nil || d.text+d.tool != 0 {
			t.Fatal(path, err, "校验阶段记录了交付")
		}
		if !bytes.Contains(delivery.Body, []byte(`"usage": { "large": 9007199254740993 }`)) || !json.Valid(delivery.Body) {
			t.Fatal(path, string(delivery.Body))
		}
		root := gjson.ParseBytes(delivery.Body)
		if path == "/v1/responses" {
			var inner map[string]string
			if err := json.Unmarshal([]byte(root.Get("output.1.arguments").Str), &inner); err != nil {
				t.Fatal(err)
			}
			if inner[token] != want || root.Get("output.2.input").Str != want || root.Get("output.0.content.0.text").Str != want || root.Get("output.3.encrypted_content").Str != s {
				t.Fatal("非流式字段回填错误", string(delivery.Body))
			}
		} else {
			var inner map[string]string
			if err := json.Unmarshal([]byte(root.Get("content.1.input").Raw), &inner); err != nil {
				t.Fatal(err)
			}
			if root.Get("content.0.text").Str != want || inner[token] != want || root.Get("content.2.thinking").Str != s {
				t.Fatal("Claude 字段回填错误", string(delivery.Body))
			}
		}
		if delivery.Confirm() != counts || delivery.Confirm() != counts || d.text != counts.Text || d.tool != counts.Tool {
			t.Fatal("交付收据重复计数或漏计", delivery.Confirm(), d)
		}

	}
}

func TestRegularPreservesUnknownOnlyAndRejectsInvalid(t *testing.T) {
	raw := []byte(`{"output":[{"type":"function_call","arguments":"{ \"p\": \"\\u007b{SNU_abc\" }"}]}`)
	d, err := RestoreJSON("/v1/responses", raw, nil)
	if err != nil || !bytes.Equal(d.Body, raw) || d.Confirm().UnknownTool != 1 {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{
		[]byte(`{"output":[{"type":"function_call","arguments":"{"}]}`),
		[]byte(`{"output":[{"type":"function_call","arguments":"{\"p\":1,\"p\":2}"}]}`),
		[]byte(`{"output":[]} {}`), []byte(`{"output":[],"output":[]}`),
		append([]byte(`{"output":"`), 0xff, '"', '}'),
	} {
		if d, err := RestoreJSON("/v1/responses", raw, nil); err == nil || d != nil {
			t.Fatal("接受了无效响应", string(raw))
		}
	}
	large := strings.Repeat("a", 5<<20)
	raw = []byte(`{"content":[{"type":"text","text":"` + large + `"}]}`)
	if _, err = RestoreJSON("/v1/messages", raw, nil); err != nil {
		t.Fatal("非流式错误使用流式的 4 MiB 预算", err)
	}
}

func TestStreamBudgetAndClaudeTextRetention(t *testing.T) {
	d := &recordingDictionary{token: privacy.RestorePrefix + strings.Repeat("a", 32) + "}}", original: "x"}
	for _, path := range []string{"/v1/messages", "/v1/responses"} {
		w := New(httptest.NewRecorder(), path, d)
		f, _ := w.get(fieldKey{role: "text"})
		if _, err := w.delta(f.key, object{"text": strings.Repeat("a", 3<<20)}, "text", nil); err != nil {
			t.Fatal(path, err)
		}
		if w.outputBytes != 3<<20 {
			t.Fatal(path, "逻辑交付重复计量")
		}
		if !w.codex && (f.raw.Len() != 0 || f.output.Len() != 0 || w.rawBytes != 0) {
			t.Fatal("Claude 正文仍然常驻")
		}
		if _, err := w.delta(f.key, object{"text": strings.Repeat("a", (1<<20)+1)}, "text", nil); !errors.Is(err, ErrDelivery) {
			t.Fatal("超过逻辑交付上限", err)
		}
		w.Release()
	}
	d.lookupErr = ErrDelivery
	if _, err := RestoreJSON("/v1/messages", []byte(`{"content":[{"type":"text","text":"`+d.token+`"}]}`), d); !errors.Is(err, ErrDelivery) {
		t.Fatal("误把查表容量错误统计成未知标识", err)
	}
}

type failingEventWriter struct {
	*httptest.ResponseRecorder
	short bool
}

func (w *failingEventWriter) Write(p []byte) (int, error) {
	if w.short {
		return w.ResponseRecorder.Write(p[:len(p)-1])
	}
	return w.ResponseRecorder.Write(p)
}

func TestStreamCountsRequireCompleteWrite(t *testing.T) {
	d := &recordingDictionary{token: privacy.RestorePrefix + strings.Repeat("a", 32) + "}}", original: "safe"}
	dst := &failingEventWriter{ResponseRecorder: httptest.NewRecorder()}
	w := New(dst, "/v1/responses", d)
	defer w.Release()
	send := func(s string) error { _, err := w.Write([]byte("data: " + s + "\n\n")); return err }
	raw := `{"type":"response.completed","response":{"id":"r","output":[{"type":"message","id":"m","content":[{"type":"output_text","text":"` + d.token + `"}]},{"type":"reasoning","encrypted_content":"synthetic"}]}}`
	dst.short = true
	if err := send(raw); err == nil || d.text != 0 || w.Delivered() != (DeliveryCounts{}) {
		t.Fatal("短写事件提交了交付计数", err, d)
	}
	dst.short = false
	if err := send(raw); err == nil || d.text != 0 {
		t.Fatal("已失败的写入器仍可确认之前的事件", err, d)
	}

	accepted := New(httptest.NewRecorder(), "/v1/responses", d)
	defer accepted.Release()
	if _, err := accepted.Write([]byte("data: " + raw + "\n\n")); err != nil {
		t.Fatal(err)
	}
	if err := accepted.Finish(); err != nil || d.text != 1 {
		t.Fatal("交付成功未保存准确的字典统计", err, d)
	}
}

func TestIncompleteTextFlushesHalfMarker(t *testing.T) {
	dst := httptest.NewRecorder()
	w := New(dst, "/v1/responses", nil)
	defer w.Release()
	half := privacy.RestorePrefix + "abc"
	for _, s := range []string{
		`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"prefix ` + half + `","sequence_number":0}`,
		`{"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"}},"sequence_number":1}`,
	} {
		if _, err := w.Write([]byte("data: " + s + "\n\n")); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Finish(); err != nil || w.Delivered().UnknownText != 1 || !strings.Contains(dst.Body.String(), `"delta":"`+half+`"`) || !strings.Contains(dst.Body.String(), "max_output_tokens") {
		t.Fatal("文本截在占位符中间时不能遗漏已收到的字符", err, dst.Body.String())
	}
}
