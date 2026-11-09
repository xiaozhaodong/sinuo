package restorestream

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

func TestSSECommonJSONString(t *testing.T) {
	prefix := strings.Repeat("x", 126)
	for _, tc := range []struct {
		name, literal string
		fast          bool
	}{
		{"short", `"` + prefix[1:] + `"`, false},
		{"threshold", `"` + prefix + `"`, true},
		{"common", `"` + prefix + `中文🙂\"\\\/\b\f\n\r\t"`, true},
		{"literal-backslash-u", `"` + prefix + `\\u0041"`, true},
		{"unicode", `"` + prefix + `\u0041\ud83d\ude00"`, false},
		{"surrogate", `"` + prefix + `\ud800"`, false},
		{"control", `"` + prefix + "\x01\"", false},
		{"interior-quote", `"` + prefix + `"x"`, false},
		{"extra-value", `"` + prefix + `","x"`, false},
		{"unknown-escape", `"` + prefix + `\z"`, false},
		{"open-escape", `"` + prefix + `\"`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := decodeCommonJSONString([]byte(tc.literal))
			if ok != tc.fast {
				t.Fatalf("期望快速解码 %t，实际 %t", tc.fast, ok)
			}
			if ok {
				var want string
				if err := json.Unmarshal([]byte(tc.literal), &want); err != nil || got != want {
					t.Fatal("字符串解码与标准库不一致", err)
				}
			}
		})
	}
}

func FuzzSSECommonJSONString(f *testing.F) {
	for _, s := range []string{"", `\/`, `\n\t\"\\`, `\u0041`, "\x00", "🙂"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if !utf8.ValidString(s) {
			return // fragment 在调用解码器前统一拒绝无效 UTF-8。
		}
		literal := []byte(`"` + strings.Repeat("x", 128) + s + `"`)
		if got, ok := decodeCommonJSONString(literal); ok {
			var want string
			if err := json.Unmarshal(literal, &want); err != nil || got != want || got != gjson.ParseBytes(literal).Str {
				t.Fatal("快速解码与标准库或原 SSE 解码结果不一致", err)
			}
		}
	})
}
