package privacy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"
)

// 固定旧编码器作独立对照，覆盖非 HTML 转义及逐个坏字节替换的历史语义。
func TestJSONStringEncodingMatchesReference(t *testing.T) {
	cases := []string{"", "plain <>&", "中文🙂\u2028\u2029", "\x00\x01\b\f\n\r\t\"\\", "\xff\xfe\xc0\xaf", "\xed\xa0\x80", "\xf4\x90\x80\x80", strings.Repeat("safe text ", 1024), strings.Repeat("\"\\\n\t", 1024)}
	var all []byte
	for i := 0; i < 256; i++ {
		all = append(all, byte(i))
	}
	cases = append(cases, string(all))
	for _, n := range []int{0, 1, 2, 3, 4, 7, 8, 15, 16, 31, 32, 63, 64, 255, 256, 1024, 64 << 10} {
		for _, tail := range []string{"\"\\\n", "中文🙂\tend", "\xff\xfe", "\x00\x1f", "\b\f\r\t"} {
			cases = append(cases, strings.Repeat("a", n)+tail)
		}
	}
	rng := rand.New(rand.NewSource(8035))
	for i := 0; i < 10000; i++ {
		raw := make([]byte, rng.Intn(512))
		rng.Read(raw)
		cases = append(cases, string(raw))
	}
	for i, s := range cases {
		for _, capacity := range []int{0, len(s)*6 + 32} {
			dst := append(make([]byte, 0, capacity), "prefix:"...)
			got := appendJSONStringBody(dst, s)
			want := referenceAppendJSONStringBody([]byte("prefix:"), s)
			if !bytes.Equal(got, want) {
				t.Fatalf("case=%d capacity=%d: encoding differs", i, capacity)
			}
		}
		if !json.Valid(encodeJSONString(s)) {
			t.Fatalf("case=%d: invalid JSON output", i)
		}
	}
}

func FuzzJSONStringEncoding(f *testing.F) {
	for _, s := range []string{"", "ordinary text", "中文🙂", "\"\\\n\x00", "\xff\xed\xa0\x80", "<>&\u2028\u2029"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if got, want := appendJSONStringBody([]byte("prefix:"), s), referenceAppendJSONStringBody([]byte("prefix:"), s); !bytes.Equal(got, want) {
			t.Fatal("encoding differs from reference")
		}
		if !json.Valid(encodeJSONString(s)) {
			t.Fatal("invalid JSON output")
		}
	})
}

func referenceAppendJSONStringBody(dst []byte, s string) []byte {
	for i := 0; i < len(s); {
		b := s[i]
		if b < 0x20 {
			switch b {
			case '\b':
				dst = append(dst, '\\', 'b')
			case '\f':
				dst = append(dst, '\\', 'f')
			case '\n':
				dst = append(dst, '\\', 'n')
			case '\r':
				dst = append(dst, '\\', 'r')
			case '\t':
				dst = append(dst, '\\', 't')
			default:
				dst = fmt.Appendf(dst, "\\u%04x", b)
			}
			i++
			continue
		}
		if b == '"' || b == '\\' {
			dst = append(dst, '\\', b)
			i++
			continue
		}
		if b < utf8.RuneSelf {
			dst = append(dst, b)
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			// 无效 UTF-8 字节按 � 输出，避免产生非法 JSON
			dst = append(dst, []byte(`�`)...)
			i++
			continue
		}
		dst = append(dst, s[i:i+size]...)
		i += size
	}
	return dst
}
