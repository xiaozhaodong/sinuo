package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// referenceSubscriptionNestedJSON 改造前的整体解码实现，作为差分参照
func referenceSubscriptionNestedJSON(originalBody, rewrittenBody []byte) error {
	var before, after any
	if err := json.Unmarshal(originalBody, &before); err != nil {
		return err
	}
	if err := json.Unmarshal(rewrittenBody, &after); err != nil {
		return err
	}
	original, _ := before.(map[string]any)
	rewritten, _ := after.(map[string]any)
	if wrapped, ok := original["response"].(map[string]any); ok {
		original = wrapped
		rewritten, _ = rewritten["response"].(map[string]any)
	}
	items, _ := original["input"].([]any)
	updatedItems, _ := rewritten["input"].([]any)
	if len(items) != len(updatedItems) {
		return fmt.Errorf("privacy replacement changed input structure")
	}
	for index, item := range items {
		entry, _ := item.(map[string]any)
		updated, _ := updatedItems[index].(map[string]any)
		field := ""
		switch entry["type"] {
		case "function_call":
			field = "arguments"
		case "custom_tool_call":
			field = "input"
		case "function_call_output", "custom_tool_call_output":
			field = "output"
		default:
			continue
		}
		value, _ := entry[field].(string)
		replacement, _ := updated[field].(string)
		trimmed := strings.TrimSpace(value)
		if value == replacement || len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') || !json.Valid([]byte(value)) {
			continue
		}
		if err := validateSubscriptionJSONDocument([]byte(replacement)); err != nil {
			return fmt.Errorf("privacy replacement invalidated nested tool JSON")
		}
	}
	return nil
}

func TestSubscriptionReplayValidationMatchesReference(t *testing.T) {
	cases := []struct{ name, original, rewritten string }{
		{"unchanged", `{"input":[{"type":"function_call","arguments":"{\"a\":\"k\"}"}]}`, `{"input":[{"type":"function_call","arguments":"{\"a\":\"k\"}"}]}`},
		{"nested json kept valid", `{"input":[{"type":"function_call","arguments":"{\"a\":\"sk-1\"}"}]}`, `{"input":[{"type":"function_call","arguments":"{\"a\":\"[X]\"}"}]}`},
		{"nested json broken", `{"input":[{"type":"function_call","arguments":"{\"a\":\"sk-1\"}"}]}`, `{"input":[{"type":"function_call","arguments":"{\"a\":[X]}"}]}`},
		{"custom tool input broken", `{"input":[{"type":"custom_tool_call","input":"[1,2]"}]}`, `{"input":[{"type":"custom_tool_call","input":"[1,[X]"}]}`},
		{"output broken", `{"input":[{"type":"function_call_output","output":"{\"k\":1}"}]}`, `{"input":[{"type":"function_call_output","output":"{\"k\":[X]"}]}`},
		{"plain text output", `{"input":[{"type":"function_call_output","output":"env sk-1"}]}`, `{"input":[{"type":"function_call_output","output":"env [X]"}]}`},
		{"message untouched by rule", `{"input":[{"type":"message","content":"{\"a\":1}"}]}`, `{"input":[{"type":"message","content":"{\"a\":[X]"}]}`},
		{"array output", `{"input":[{"type":"function_call_output","output":[{"type":"input_text","text":"sk-1"}]}]}`, `{"input":[{"type":"function_call_output","output":[{"type":"input_text","text":"[X]"}]}]}`},
		{"non-object item", `{"input":["{\"a\":1}",{"type":"function_call","arguments":"{}"}]}`, `{"input":["{\"a\":[X]",{"type":"function_call","arguments":"{}"}]}`},
		{"rewritten item not object", `{"input":[{"type":"function_call","arguments":"{\"a\":1}"}]}`, `{"input":["x"]}`},
		{"length mismatch", `{"input":[{"type":"message"},{"type":"message"}]}`, `{"input":[{"type":"message"}]}`},
		{"string input", `{"input":"sk-1"}`, `{"input":"[X]"}`},
		{"wrapped response", `{"type":"response.create","response":{"input":[{"type":"function_call","arguments":"{\"a\":\"sk\"}"}]}}`, `{"type":"response.create","response":{"input":[{"type":"function_call","arguments":"{\"a\":[X]}"}]}}`},
		{"wrapped rewritten not object", `{"response":{"input":[{"type":"message"}]}}`, `{"response":"x"}`},
		{"escaped keys", `{"\u0069nput":[{"\u0074ype":"function_call","arguments":"{\"a\":\"s\"}"}]}`, `{"\u0069nput":[{"\u0074ype":"function_call","arguments":"{\"a\":[X]}"}]}`},
		{"case variant keys", `{"input":[{"type":"message","TYPE":"function_call","arguments":"{\"a\":1}"}]}`, `{"input":[{"type":"message","TYPE":"function_call","arguments":"{\"a\":[X]"}]}`},
		{"non-string type", `{"input":[{"type":7,"arguments":"{\"a\":1}"}]}`, `{"input":[{"type":7,"arguments":"{\"a\":[X]"}]}`},
		{"whitespace layout broken", "{ \"input\" : [ { \"type\" : \"function_call\" , \"arguments\" : \"{\\\"a\\\":1}\" } ] }", "{ \"input\" : [ { \"type\" : \"function_call\" , \"arguments\" : \"{\\\"a\\\":[X]\" } ] }"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, body := range []string{tc.original, tc.rewritten} {
				if err := validateSubscriptionJSON([]byte(body)); err != nil {
					t.Fatalf("fixture must be valid subscription JSON: %v", err)
				}
			}
			want := referenceSubscriptionNestedJSON([]byte(tc.original), []byte(tc.rewritten))
			got := validateSubscriptionReplayItems([]byte(tc.original), []byte(tc.rewritten))
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("got %v, want %v", got, want)
			}
			wantErr := strings.Contains(tc.name, "broken") || strings.Contains(tc.name, "mismatch") ||
				strings.Contains(tc.name, "not object") || tc.name == "wrapped response" || tc.name == "escaped keys"
			if (got != nil) != wantErr {
				t.Fatalf("error = %v, want error: %v", got, wantErr)
			}
		})
	}
}

func randomJSONValue(r *rand.Rand, depth int) any {
	switch kind := r.Intn(6); {
	case depth <= 0 || kind == 0:
		pieces := []string{`"`, `\`, `\\`, `\"`, "中文", "\n", "a", "}", "]", ",", ":", " ", " "}
		var b strings.Builder
		for range r.Intn(8) {
			b.WriteString(pieces[r.Intn(len(pieces))])
		}
		return b.String()
	case kind == 1:
		return r.Intn(1000)
	case kind == 2:
		return r.Intn(2) == 0
	case kind == 3:
		items := make([]any, r.Intn(4))
		for i := range items {
			items[i] = randomJSONValue(r, depth-1)
		}
		return items
	default:
		object := map[string]any{}
		for i := range r.Intn(4) {
			object[fmt.Sprintf("k%d\"\\", i)] = randomJSONValue(r, depth-1)
		}
		return object
	}
}

func TestJSONScannerMatchesStandardLibrary(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	for range 3000 {
		items := make([]any, r.Intn(5))
		for i := range items {
			items[i] = randomJSONValue(r, 3)
		}
		body, _ := json.MarshalIndent(map[string]any{"input": items, "response": randomJSONValue(r, 2)}, "", " ")

		var decoded map[string]json.RawMessage
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatal(err)
		}
		fields := jsonObjectFields(body, "input", "response")
		for _, key := range []string{"input", "response"} {
			if !bytes.Equal(fields[key], decoded[key]) {
				t.Fatalf("field %s: got %s, want %s", key, fields[key], decoded[key])
			}
		}
		var elements []json.RawMessage
		if err := json.Unmarshal(decoded["input"], &elements); err != nil {
			t.Fatal(err)
		}
		got := jsonArrayElements(fields["input"])
		if len(got) != len(elements) {
			t.Fatalf("element count %d, want %d: %s", len(got), len(elements), body)
		}
		for i := range got {
			if !bytes.Equal(got[i], elements[i]) {
				t.Fatalf("element %d: got %s, want %s", i, got[i], elements[i])
			}
		}
	}
}
