package codexidentity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestEditObjectAllDeletionCombinations(t *testing.T) {
	raw := []byte(" \n{ \"a\": 9007199254740993, \"b\" : 1.2300e+09, \"c\": \"cipher\\u0061\", \"d\": {\"nested\":true} } \n")
	keys := []string{"a", "b", "c", "d"}
	for mask := 0; mask < 16; mask++ {
		for _, add := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d-%v", mask, add), func(t *testing.T) {
				edits := map[string][]byte{}
				for i, k := range keys {
					if mask&(1<<i) != 0 {
						edits[k] = nil
					}
				}
				if add {
					edits["new"] = JSONString("value")
				}
				out, err := EditObject(raw, edits)
				if err != nil || !json.Valid(out) {
					t.Fatalf("编辑错误: %s %v", out, err)
				}
				object, err := parseObject(raw)
				if err != nil {
					t.Fatal(err)
				}
				cachedOut, views, err := object.edit(edits)
				var decoded map[string]json.RawMessage
				if err != nil || !bytes.Equal(cachedOut, out) || json.Unmarshal(out, &decoded) != nil || !reflect.DeepEqual(views, decoded) {
					t.Fatal("供指纹复用的字段视图与实际输出不一致", err)
				}
				got, _ := JSONFields(out)
				before, _ := JSONFields(raw)
				for i, k := range keys {
					if mask&(1<<i) != 0 {
						if got[k] != nil {
							t.Fatal("未删除", k)
						}
					} else if string(got[k]) != string(before[k]) {
						t.Fatal("改变原始字节", k)
					}
				}
			})
		}
	}
	for _, raw := range []string{` { } `, `{"a":1}`} {
		out, err := EditObject([]byte(raw), map[string][]byte{"a": nil, "new": []byte(`123`)})
		if err != nil || !json.Valid(out) {
			t.Fatalf("替换错误 %s %v", out, err)
		}
	}
	for _, raw := range []string{`{"a":1,"\u0061":2}`, `[]`, `null`, `{"a":1} {}`, strings.Repeat("[", 258) + "0" + strings.Repeat("]", 258)} {
		if _, err := EditObject([]byte(raw), nil); err == nil {
			t.Fatal("无效输入被接受")
		}
	}
}

func TestJSONScannerBoundarySemantics(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		valid     bool
	}{
		{"scalars", ` {"a":[true,false,null,-0,1.23e+9,"\\\"[]{}"],"b":{}} `, true},
		{"escaped_equal_key", `{"a":1,"\u0061":2}`, false},
		{"nested_duplicate", `{"input":[{"a":1,"a":2}]}`, false},
		{"surrogate_equal_key", `{"😀":1,"\ud83d\ude00":2}`, false},
		{"replacement_equal_key", `{"�":1,"\ud800":2}`, false},
		{"invalid_utf8_equal_key", "{\"\xff\":1,\"\\ufffd\":2}", false},
		{"unicode_distinct", `{"é":1,"e\u0301":2}`, true},
		{"depth_256", strings.Repeat("[", 256) + "0" + strings.Repeat("]", 256), true},
		{"depth_257", strings.Repeat("[", 257) + "0" + strings.Repeat("]", 257), false},
		{"empty_at_limit", strings.Repeat("[", 257) + strings.Repeat("]", 257), true},
		{"empty_over_limit", strings.Repeat("[", 258) + strings.Repeat("]", 258), false},
		{"multiple_documents", `{} {}`, false},
		{"incomplete_string", `{"a":"\`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidateJSON([]byte(tc.raw)) == nil; got != tc.valid {
				t.Fatalf("valid=%v want=%v", got, tc.valid)
			}
		})
	}
}

func TestJSONEditValidatesReplacementAtActualDepth(t *testing.T) {
	for _, value := range []string{
		`{"same":1,"\u0073ame":2}`,
		strings.Repeat("[", 256) + "0" + strings.Repeat("]", 256),
	} {
		if _, err := EditObject([]byte(`{}`), map[string][]byte{"value": []byte(value)}); err == nil {
			t.Fatal("替换字段绕过重复键或深度约束")
		}
	}
	if _, err := EditObject([]byte(`{}`), map[string][]byte{"\xff": []byte(`1`)}); err == nil {
		t.Fatal("无效 UTF-8 键可能编码为已有键，必须拒绝")
	}
}

// 测试用旧实现作语义参照；生产路径不再逐 token 分配用户值。
func referenceJSONValidation(raw []byte) error {
	if !json.Valid(raw) {
		return fmt.Errorf("invalid JSON")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 256 {
			return fmt.Errorf("too deep")
		}
		token, err := d.Token()
		if err != nil {
			return err
		}
		delim, container := token.(json.Delim)
		if !container {
			return nil
		}
		keys := map[string]bool{}
		for d.More() {
			if delim == '{' {
				token, err := d.Token()
				if err != nil || keys[token.(string)] {
					return fmt.Errorf("invalid or duplicate key")
				}
				keys[token.(string)] = true
			}
			if err := walk(depth + 1); err != nil {
				return err
			}
		}
		_, err = d.Token()
		return err
	}
	if err := walk(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("multiple documents")
	}
	return nil
}

func FuzzJSONValidationAndFieldSpans(f *testing.F) {
	for _, raw := range []string{
		`{}`, `[]`, `null`, `false`, `-1.2300e+99`, `"escaped\\\"value"`,
		`{"a":1,"b":[{},true,false,null,"text"],"c":{"nested":2}}`,
		`{"\u0061":9007199254740993,"escape":"cipher\\u0061"}`,
		`{"a":1,"\u0061":2}`, `{"�":1,"\ud800":2}`,
		"{\"\xff\":1,\"\\ufffd\":2}",
		strings.Repeat("[", 257) + "0" + strings.Repeat("]", 257),
	} {
		f.Add([]byte(raw))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		wantValid := referenceJSONValidation(raw) == nil
		if got := ValidateJSON(raw) == nil; got != wantValid {
			t.Fatalf("校验语义不一致: %q got=%v want=%v", raw, got, wantValid)
		}
		if !wantValid || bytes.TrimSpace(raw)[0] != '{' {
			return
		}
		var want map[string]json.RawMessage
		if err := json.Unmarshal(raw, &want); err != nil {
			t.Fatal(err)
		}
		got, err := JSONFields(raw)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("字段位置或原始表示不一致: %q %v", raw, err)
		}
		out, err := EditObject(raw, map[string][]byte{"__probe": []byte(`true`)})
		if err != nil || ValidateJSON(out) != nil {
			t.Fatalf("编辑后 JSON 无效: %q %v", out, err)
		}
		after, _ := JSONFields(out)
		for key, value := range want {
			if key != "__probe" && !bytes.Equal(after[key], value) {
				t.Fatalf("编辑改变了无关字段 %q", key)
			}
		}
	})
}

func TestSourceHeadersRejectAmbiguityAndInjection(t *testing.T) {
	h := make(map[string][]string)
	h["Session-Id"] = []string{"one", "two"}
	h["Thread-Id"] = []string{"bad\r\nvalue"}
	h["X-Codex-Window-Id"] = []string{strings.Repeat("a", 129)}
	s := CaptureSourceHeaders(h)
	if len(s.Invalid) != 3 || s.SessionID != "" || s.ThreadID != "" || s.WindowID != "" {
		t.Fatalf("无效头未拒绝: %+v", s)
	}
}
