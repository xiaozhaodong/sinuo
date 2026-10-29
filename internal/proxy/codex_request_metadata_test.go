package proxy

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestStripCodexPrivateFields_PreservesOtherContent(t *testing.T) {
	body := []byte(`{
		"model":"gpt-fixture", "number":9007199254740993,
		"metadata":{"internal_chat_message_metadata_passthrough":"business metadata"},
		"client_metadata":{"keep":true}, "stream":true, "store":false,
		"tools":[{"type":"function","name":"fixture"}], "instructions":"keep instructions",
		"input":[
			{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"keep text"}],"internal_chat_message_metadata_passthrough":{"cell_id":"cell-fixture","turn_id":"turn-fixture","content_item_kinds":["text"],"future_field":true}},
			{"type":"function_call","name":"fixture","call_id":"call-fixture","arguments":"{\"internal_chat_message_metadata_passthrough\":true}","encrypted_function_args":"args<&\/\u0061>","internal_chat_message_metadata_passthrough":null},
			{"type":"function_call_output","call_id":"call-fixture","output":"internal_chat_message_metadata_passthrough is business output"},
			{"type":"reasoning","id":"reasoning-fixture","summary":[{"type":"summary_text","text":"keep summary"}],"encrypted_content":"reasoning<&\/\u0061>","internal_chat_message_metadata_passthrough":{}},
			{"type":"compaction","id":"compaction-fixture","encrypted_content":"compaction<&\/\u0061>","internal_chat_message_metadata_passthrough":0},
			{"type":"future_item","nested":{"internal_chat_message_metadata_passthrough":"business field"},"number":9007199254740993}
		]
	}`)
	original := bytes.Clone(body)
	out, stats, err := stripCodexPrivateFields(body)
	if err != nil || stats != (codexPrivateFieldStats{MetadataItems: 4}) {
		t.Fatalf("strip result: stats=%+v err=%v", stats, err)
	}
	if !bytes.Equal(body, original) {
		t.Fatal("filter mutated the original request")
	}
	var before, after map[string]json.RawMessage
	if err := json.Unmarshal(body, &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out, &after); err != nil {
		t.Fatal(err)
	}
	for key, value := range before {
		if key != "input" && !bytes.Equal(compactMetadataJSON(t, value), compactMetadataJSON(t, after[key])) {
			t.Fatalf("top-level %q changed", key)
		}
	}
	var beforeItems, afterItems []map[string]json.RawMessage
	if err := json.Unmarshal(before["input"], &beforeItems); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(after["input"], &afterItems); err != nil {
		t.Fatal(err)
	}
	if len(afterItems) != len(beforeItems) {
		t.Fatal("input item count changed")
	}
	for index, item := range beforeItems {
		if _, exists := afterItems[index]["internal_chat_message_metadata_passthrough"]; exists {
			t.Fatalf("metadata remains on input[%d]", index)
		}
		for key, value := range item {
			if key != "internal_chat_message_metadata_passthrough" && !bytes.Equal(compactMetadataJSON(t, value), compactMetadataJSON(t, afterItems[index][key])) {
				t.Fatalf("input[%d].%s changed", index, key)
			}
		}
	}
	repeated, stats, err := stripCodexPrivateFields(out)
	if err != nil || stats != (codexPrivateFieldStats{}) || !bytes.Equal(repeated, out) {
		t.Fatal("filter must be idempotent and preserve bytes on a no-op")
	}
}

func TestStripCodexPrivateFields_RemovesOutputReplay(t *testing.T) {
	body := []byte(`{
		"model":"gpt-fixture", "number":9007199254740993,
		"include":["reasoning.encrypted_content","output.encrypted_content",{"future":true}],
		"input":[
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"keep text"}],"encrypted_content":"message-replay"},
			{"type":"custom_tool_call","call_id":"call-fixture","name":"exec","input":"keep input","encrypted_content":"tool-replay","internal_chat_message_metadata_passthrough":{"turn_id":"turn-fixture"}},
			{"type":"reasoning","id":"reasoning-fixture","summary":[],"encrypted_content":"reasoning-keep"},
			{"type":"compaction","id":"compaction-fixture","encrypted_content":"compaction-keep"}
		]
	}`)
	original := bytes.Clone(body)
	out, stats, err := stripCodexPrivateFields(body)
	want := codexPrivateFieldStats{MetadataItems: 1, ReplayItems: 2, IncludeRemoved: 1}
	if err != nil || stats != want {
		t.Fatalf("strip result: stats=%+v err=%v", stats, err)
	}
	if !bytes.Equal(body, original) {
		t.Fatal("filter mutated the original request")
	}
	var after struct {
		Number  json.Number                  `json:"number"`
		Include []json.RawMessage            `json:"include"`
		Input   []map[string]json.RawMessage `json:"input"`
	}
	decoder := json.NewDecoder(bytes.NewReader(out))
	decoder.UseNumber()
	if err := decoder.Decode(&after); err != nil {
		t.Fatal(err)
	}
	if after.Number != "9007199254740993" || len(after.Include) != 2 ||
		string(after.Include[0]) != `"reasoning.encrypted_content"` || string(after.Include[1]) != `{"future":true}` {
		t.Fatalf("unexpected top-level fields: %s", out)
	}
	for index, key := range []string{"message", "custom_tool_call"} {
		if _, exists := after.Input[index]["encrypted_content"]; exists {
			t.Fatalf("replay ciphertext remains on %s", key)
		}
	}
	if string(after.Input[1]["input"]) != `"keep input"` || len(after.Input[1]) != 4 {
		t.Fatalf("custom_tool_call changed beyond private fields: %v", after.Input[1])
	}
	if string(after.Input[2]["encrypted_content"]) != `"reasoning-keep"` || string(after.Input[3]["encrypted_content"]) != `"compaction-keep"` {
		t.Fatal("reasoning or compaction ciphertext must be preserved")
	}
	repeated, stats, err := stripCodexPrivateFields(out)
	if err != nil || stats != (codexPrivateFieldStats{}) || !bytes.Equal(repeated, out) {
		t.Fatal("filter must be idempotent and preserve bytes on a no-op")
	}
}

func TestStripCodexPrivateFields_NoChangePreservesBytes(t *testing.T) {
	for _, raw := range []string{
		"  {\"model\":\"fixture\"} \n",
		`{ "input" : null }`,
		`{ "input" : "internal_chat_message_metadata_passthrough is text" }`,
		`{ "input" : [] }`,
		`{ "internal_chat_message_metadata_passthrough": true, "input":[{"type":"future_item","nested":{"internal_chat_message_metadata_passthrough":true}}] }`,
		`{ "include" : [ "reasoning.encrypted_content" ], "input":[{"type":"reasoning","encrypted_content":"keep"}] }`,
		`{ "include" : null, "input":[{"type":"compaction","encrypted_content":"keep"}] }`,
		`{ "include" : "output.encrypted_content" }`,
		`{ "input":[{"type":"message","content":[{"type":"output_text","text":"x","encrypted_content":"nested"}]}] }`,
	} {
		out, stats, err := stripCodexPrivateFields([]byte(raw))
		if err != nil || stats != (codexPrivateFieldStats{}) || string(out) != raw {
			t.Fatalf("no-op request changed: stats=%+v err=%v", stats, err)
		}
	}
}

func TestStripCodexPrivateFields_RejectsInvalidContainersAndDocuments(t *testing.T) {
	for _, raw := range []string{
		"", "{", "{} {}", "{} trailing", "[]", "null", "1", "true", `"request"`,
		`{"input":{}}`, `{"input":1}`, `{"input":true}`,
		`{"input":[null]}`, `{"input":[1]}`, `{"input":["item"]}`, `{"input":[[]]}`,
		`{"input":[{"internal_chat_message_metadata_passthrough":true},null]}`,
	} {
		t.Run(raw, func(t *testing.T) {
			out, stats, err := stripCodexPrivateFields([]byte(raw))
			if err == nil || out != nil || stats != (codexPrivateFieldStats{}) {
				t.Fatalf("invalid request must fail without a partial body: stats=%+v err=%v", stats, err)
			}
		})
	}
}

func compactMetadataJSON(t *testing.T, value []byte) []byte {
	t.Helper()
	var compact bytes.Buffer
	if err := json.Compact(&compact, value); err != nil {
		t.Fatal(err)
	}
	return compact.Bytes()
}
