package proxy

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestStripCodexInternalMetadata_PreservesOtherContent(t *testing.T) {
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
	out, removed, err := stripCodexInternalMetadata(body)
	if err != nil || removed != 4 {
		t.Fatalf("strip result: removed=%d err=%v", removed, err)
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
	repeated, removed, err := stripCodexInternalMetadata(out)
	if err != nil || removed != 0 || !bytes.Equal(repeated, out) {
		t.Fatal("filter must be idempotent and preserve bytes on a no-op")
	}
}

func TestStripCodexInternalMetadata_NoChangePreservesBytes(t *testing.T) {
	for _, raw := range []string{
		"  {\"model\":\"fixture\"} \n",
		`{ "input" : null }`,
		`{ "input" : "internal_chat_message_metadata_passthrough is text" }`,
		`{ "input" : [] }`,
		`{ "internal_chat_message_metadata_passthrough": true, "input":[{"type":"future_item","nested":{"internal_chat_message_metadata_passthrough":true}}] }`,
	} {
		out, removed, err := stripCodexInternalMetadata([]byte(raw))
		if err != nil || removed != 0 || string(out) != raw {
			t.Fatalf("no-op request changed: removed=%d err=%v", removed, err)
		}
	}
}

func TestStripCodexInternalMetadata_RejectsInvalidContainersAndDocuments(t *testing.T) {
	for _, raw := range []string{
		"", "{", "{} {}", "{} trailing", "[]", "null", "1", "true", `"request"`,
		`{"input":{}}`, `{"input":1}`, `{"input":true}`,
		`{"input":[null]}`, `{"input":[1]}`, `{"input":["item"]}`, `{"input":[[]]}`,
		`{"input":[{"internal_chat_message_metadata_passthrough":true},null]}`,
	} {
		t.Run(raw, func(t *testing.T) {
			out, removed, err := stripCodexInternalMetadata([]byte(raw))
			if err == nil || out != nil || removed != 0 {
				t.Fatalf("invalid request must fail without a partial body: removed=%d err=%v", removed, err)
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
