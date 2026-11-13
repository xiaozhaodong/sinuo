package privacy

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"testing"
)

// 直接对照标准库字符串解码，专门覆盖本轮移除 Unmarshal 后的行为边界。
func checkJSONStringDecode(t *testing.T, raw []byte) {
	t.Helper()
	var want string
	referenceErr := json.Unmarshal(raw, &want)
	walker := jsonTextWalker{body: raw}
	token, err := walker.stringToken()
	if (referenceErr == nil) != (err == nil && walker.pos == len(raw)) {
		t.Fatalf("string acceptance differs: raw=%q reference=%v scanner=%v end=%d", raw, referenceErr, err, walker.pos)
	}
	if referenceErr != nil {
		return
	}
	got := decodeValidatedJSONString(token)
	if got != want {
		t.Fatalf("string decode differs: raw=%q got=%q want=%q", raw, got, want)
	}
}

func TestJSONStringDecoderUnicodeAndInvalidUTF8(t *testing.T) {
	cases := []string{
		`""`, `"plain"`, `"\\\"\/\b\f\n\r\t"`, `"中文🙂"`,
		`"\uD800"`, `"\uDC00"`, `"\uD800\uD800\uDC00"`,
		`"\uDC00\uD800\uDC00"`, `"\uD800\n\uDC00"`,
		`"\uD83D\uDE00"`, `"\u0000\u007F\u0080\uFFFF"`,
		`"\q"`, `"\u00XZ"`, `"\u123"`, `"unterminated`,
		"\"\xff\xff\\n\xfe\xc0\xaf\xed\xa0\x80\"",
		"\"a\\tb\xe2\x82\\nc\xf0\x9f\x99\x82\"",
	}
	for _, raw := range cases {
		checkJSONStringDecode(t, []byte(raw))
	}
	for value := 0; value < 1<<16; value++ {
		checkJSONStringDecode(t, []byte(fmt.Sprintf(`"\u%04x"`, value)))
	}
	rng := rand.New(rand.NewSource(62026))
	for i := 0; i < 20000; i++ {
		a, b, c := rng.Intn(1<<16), rng.Intn(1<<16), rng.Intn(1<<16)
		checkJSONStringDecode(t, []byte(fmt.Sprintf(`"x\u%04X\u%04x\u%04X\n"`, a, b, c)))
		prefix := []byte(`"before\t`)
		for j, n := 0, rng.Intn(32); j < n; j++ {
			v := byte(rng.Intn(256))
			if v == '"' || v == '\\' {
				prefix = append(prefix, '\\', v)
			} else if v < 0x20 {
				prefix = append(prefix, fmt.Sprintf(`\u%04x`, v)...)
			} else {
				prefix = append(prefix, v)
			}
		}
		checkJSONStringDecode(t, append(prefix, []byte(`\nafter"`)...))
	}
	t.Log("全部 65536 个 UTF-16 码元 + 20000 组混合转义/代理项 + 20000 组含任意字节的字符串，与 encoding/json 解码一致")
}
