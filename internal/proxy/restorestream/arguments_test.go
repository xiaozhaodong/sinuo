package restorestream

import (
	"encoding/json"
	"strings"
	"testing"

	"sinuo/internal/privacy"
)

func TestToolArgumentEscapeAcrossFrames(t *testing.T) {
	token := privacy.RestorePrefix + strings.Repeat("a", 32) + "}}"
	incomplete := privacy.RestorePrefix + "ab"
	dict := &recordingDictionary{token: token, original: "中文<&>\n\""}
	quoted, err := json.Marshal(dict.original)
	if err != nil {
		t.Fatal(err)
	}
	for _, escape := range []string{`\"`, `\\`, `\/`, `\b`, `\f`, `\n`, `\r`, `\t`} {
		t.Run(escape, func(t *testing.T) {
			// 键和普通值保留原转义；可能的占位符后缀按原交付语义收尾。
			afterTail := escape
			if escape == `\/` {
				afterTail = "/"
			}
			start := `{"key{` + escape + `":"` + escape
			input := start + "|{" + escape + "|" + incomplete + escape + "|" + token + `"}`
			want := start + "|{" + afterTail + "|" + incomplete + afterTail + "|" + string(quoted[1:len(quoted)-1]) + `"}`
			for cut := 0; cut <= len(input); cut++ {
				var args arguments
				counts := countedValues{dict: dict, limit: maxBytes}
				var out strings.Builder
				for _, part := range []string{input[:cut], input[cut:]} {
					value, err := args.feed(part, &counts)
					if err != nil {
						t.Fatalf("切在 %d: %v", cut, err)
					}
					out.WriteString(value)
				}
				if err := args.finish(input); err != nil {
					t.Fatal(err)
				}
				if out.String() != want || counts.count != 1 || counts.unknown != 1 {
					t.Fatalf("切在 %d: 得到 %q，预期 %q，命中 %d，未知 %d", cut, out.String(), want, counts.count, counts.unknown)
				}
			}
		})
	}
}

func TestQuoteBodyMatchesStandardJSON(t *testing.T) {
	check := func(s string) {
		t.Helper()
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if got := quoteBody(s); got != string(b[1:len(b)-1]) {
			t.Fatalf("%q 编码为 %q，预期 %s", s, got, b)
		}
	}
	for i := 0; i < 256; i++ {
		check(string([]byte{'a', byte(i), 'b'}))
	}
	check("中文🙂\u2028\u2029")
}
