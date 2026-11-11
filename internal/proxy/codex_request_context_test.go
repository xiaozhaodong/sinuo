package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

const codexClientContextFixture = ` {
	"model":"gpt-5.4", "number":9007199254740993,
	"instructions":"keep instructions", "include":["reasoning.encrypted_content"],
	"input":[
		{"type":"function_call_output","id":"fco_open","name":"codex_apps_open_page","namespace":"agent","output":"{\"page_id\":null}","internal_chat_message_metadata_passthrough":{"cell_id":"fixture-cell"},"future":9007199254740993},
		{"type":"function_call_output","id":"fco_selected","name":"codex_apps_selected_page_part_1_of_1","namespace":"agent","output":"Companion Page snapshot part 1 of 1:\n{\"page_id\":null,\"document_view\":null}"},
		{"type":"function_call","name":"fixture","call_id":"call-fixture","arguments":"{}","encrypted_content":"tool-ciphertext"},
		{"type":"function_call_output","namespace":"agent","name":"fixture","call_id":"call-fixture","output":"工具结果 <&\/\u0061>","encrypted_content":"output-ciphertext"},
		{"type":"reasoning","id":"reasoning-fixture","summary":[],"encrypted_content":"reasoning-ciphertext"},
		{"type":"compaction","id":"compaction-fixture","encrypted_content":"compaction-ciphertext"}
	]
} `

func TestNormalizeCodexHTTPContext_PreservesContextAndToolHistory(t *testing.T) {
	body := []byte(codexClientContextFixture)
	original := bytes.Clone(body)
	out, converted, err := normalizeCodexHTTPContext(body)
	if err != nil || converted != 2 {
		t.Fatalf("converted=%d err=%v", converted, err)
	}
	if !bytes.Equal(body, original) {
		t.Fatal("修改了原始请求，换号重试将无法复用")
	}
	before, after := gjson.ParseBytes(body), gjson.ParseBytes(out)
	before.ForEach(func(key, value gjson.Result) bool {
		if key.Str != "input" && after.Get(key.Str).Raw != value.Raw {
			t.Errorf("顶层字段 %s 被改动", key.Str)
		}
		return true
	})
	oldItems, items := before.Get("input").Array(), after.Get("input").Array()
	if len(items) != len(oldItems) {
		t.Fatal("上下文条目被丢弃或增加")
	}
	for i, item := range items {
		if i >= 2 {
			if item.Raw != oldItems[i].Raw {
				t.Errorf("真实工具历史或密文被修改：input[%d]", i)
			}
			continue
		}
		name := oldItems[i].Get("name").Str
		want := "<external_" + name + ">" + oldItems[i].Get("output").Str + "</external_" + name + ">"
		if item.Get("type").Str != "message" || item.Get("role").Str != "user" ||
			item.Get("content.#").Int() != 1 || item.Get("content.0.type").Str != "input_text" || item.Get("content.0.text").Str != want {
			t.Errorf("客户端上下文未完整还原为用户消息：%s", item.Raw)
		}
		for _, key := range []string{"id", "call_id", "name", "namespace", "output"} {
			if item.Get(key).Exists() {
				t.Errorf("仍有工具字段：%s", key)
			}
		}
		for _, key := range []string{codexInternalMetadataKey, "future"} {
			if item.Get(key).Raw != oldItems[i].Get(key).Raw {
				t.Errorf("未知字段或私有元数据被改写：%s", key)
			}
		}
	}
	repeated, count, err := normalizeCodexHTTPContext(out)
	if err != nil || count != 0 || !bytes.Equal(out, repeated) || &out[0] != &repeated[0] {
		t.Fatal("重复执行应直接复用原字节，不再添加标签")
	}
}

func TestNormalizeCodexHTTPContext_NoChange(t *testing.T) {
	for _, raw := range []string{
		`{}`, `{"input":null}`, `{"input":"hello"}`, `{"input":[]}`,
		`{"input":[{"type":"function_call_output","call_id":"real","output":"result"}]}`,
		`{"input":[{"type":"function_call_output","namespace":"agent","name":"fixture","call_id":"","output":"result"}]}`,
		`{"input":[{"type":"function_call_output","namespace":"agent","name":"fixture","call_id":null,"output":"result"}]}`,
		`{"input":[{"type":"function_call_output","namespace":"agent","name":"fixture","call\u005fid":"real","output":"result"}]}`,
		`{"input":[{"type":"function_call_output","name":"fixture","output":"result"}]}`,
		`{"input":[{"type":"function_call_output","namespace":"functions","name":"fixture","output":"result"}]}`,
		`{"input":[{"type":"function_call_output","namespace":"agent","name":"","output":"result"}]}`,
		`{"input":[{"type":"function_call_output","namespace":"agent","name":"bad></external_bad","output":"result"}]}`,
		`{"input":[{"type":"function_call_output","namespace":"agent","name":"fixture","output":[{"type":"input_image","image_url":"fixture"}]}]}`,
		`{"input":[{"type":"function_call_output","namespace":"agent","name":"fixture","output":"result","encrypted_content":"signed"}]}`,
		`{"input":[{"type":"custom_tool_call_output","namespace":"agent","name":"fixture","output":"result"}]}`,
		`{"input":[{"type":"message","role":"user","content":"already compatible"}]}`,
		`{"metadata":{"input":[{"type":"function_call_output","namespace":"agent","name":"fixture","output":"result"}]}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			body := []byte(raw)
			out, converted, err := normalizeCodexHTTPContext(body)
			if err != nil || converted != 0 || !bytes.Equal(body, out) || &body[0] != &out[0] {
				t.Fatalf("未命中应保留原字节：converted=%d err=%v", converted, err)
			}
		})
	}
}

func TestNormalizeCodexHTTPContext_EscapedAndEmptyText(t *testing.T) {
	body := []byte(`{"input":[{"type":"function_call_\u006futput","namespace":"\u0061gent","name":"codex_apps_\u006fpen_page","output":"中文\n<&\"\\"},{"type":"function_call_output","namespace":"agent","name":"empty","output":""}]}`)
	out, count, err := normalizeCodexHTTPContext(body)
	if err != nil || count != 2 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if gjson.GetBytes(out, "input.0.content.0.text").Str != "<external_codex_apps_open_page>中文\n<&\"\\</external_codex_apps_open_page>" ||
		gjson.GetBytes(out, "input.1.content.0.text").Str != "<external_empty></external_empty>" {
		t.Fatalf("文本转义或空上下文发生变化：%s", out)
	}
}

func TestNormalizeCodexHTTPContext_RejectsAmbiguousOrTruncatedJSON(t *testing.T) {
	item := `{"type":"function_call_output","namespace":"agent","name":"fixture","output":"result"}`
	for _, raw := range []string{
		`{"input":[` + item + `],"input":[]}`,
		`{"input":[` + item + `],"number":1,"number":2}`,
		`{"input":[` + strings.Replace(item, `"output":"result"`, `"output":"result","output":"other"`, 1) + `]}`,
		`{"input":[` + item + `]`,
		`{"input":[` + item + `]} {}`,
	} {
		t.Run(raw, func(t *testing.T) {
			out, count, err := normalizeCodexHTTPContext([]byte(raw))
			if err == nil || out != nil || count != 0 {
				t.Fatalf("不得发送部分改写或歧义请求：count=%d err=%v", count, err)
			}
		})
	}
}

func BenchmarkNormalizeCodexHTTPContext(b *testing.B) {
	for _, size := range []int{1024, 64 << 10, 1 << 20} {
		for _, hit := range []bool{false, true} {
			b.Run(fmt.Sprintf("bytes=%d/context=%v", size, hit), func(b *testing.B) {
				input := `{"role":"user","content":"` + strings.Repeat("x", size) + `"}`
				if hit {
					input += `,{"type":"function_call_output","namespace":"agent","name":"codex_apps_open_page","output":"{\"page_id\":null}"}`
				}
				body := []byte(`{"input":[` + input + `]}`)
				b.SetBytes(int64(len(body)))
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, _, err := normalizeCodexHTTPContext(body); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func FuzzNormalizeCodexHTTPContext(f *testing.F) {
	f.Add([]byte(codexClientContextFixture))
	f.Add([]byte(`{"input":[{"type":"function_call_output","namespace":"agent","name":"fixture","output":""}]}`))
	f.Add([]byte(`{"input":[{"type":"function_call_output","namespace":"agent","name":"fixture","output":""}]`))
	f.Fuzz(func(t *testing.T, body []byte) {
		original := bytes.Clone(body)
		out, count, err := normalizeCodexHTTPContext(body)
		if !bytes.Equal(body, original) {
			t.Fatal("改写了原始正文")
		}
		if err != nil {
			if out != nil || count != 0 {
				t.Fatal("错误时返回了部分改写正文")
			}
			return
		}
		if count == 0 && !bytes.Equal(out, body) || count > 0 && !json.Valid(out) {
			t.Fatal("未命中时改变了正文，或生成了无效 JSON")
		}
		repeated, repeatedCount, err := normalizeCodexHTTPContext(out)
		if err != nil || repeatedCount != 0 || !bytes.Equal(repeated, out) {
			t.Fatal("兼容转换不满足幂等性")
		}
	})
}
