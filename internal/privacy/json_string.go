package privacy

import (
	"bytes"
	"unicode/utf16"
	"unicode/utf8"
)

// decodeValidatedJSONString 只解码 stringToken 已完整校验的 JSON 字符串 token。
// 转义语法由 stringToken 单独负责；这里不再重复执行 JSON 语法校验。
// 非法 UTF-8 和孤立代理项沿用 encoding/json 的逐项替换语义。
func decodeValidatedJSONString(raw []byte) string {
	value := raw[1 : len(raw)-1]
	if bytes.IndexByte(value, '\\') < 0 && utf8.Valid(value) {
		return string(value)
	}
	out := make([]byte, 0, len(value))
	for pos := 0; pos < len(value); {
		start := pos
		for pos < len(value) && value[pos] != '\\' {
			pos++
		}
		run := value[start:pos]
		if utf8.Valid(run) {
			out = append(out, run...)
		} else {
			for len(run) > 0 {
				r, size := utf8.DecodeRune(run)
				if r == utf8.RuneError && size == 1 {
					out = utf8.AppendRune(out, utf8.RuneError)
				} else {
					out = append(out, run[:size]...)
				}
				run = run[size:]
			}
		}
		if pos == len(value) {
			break
		}
		pos++ // 反斜杠；其后的转义已由 stringToken 校验。
		c := value[pos]
		pos++
		switch c {
		case '"', '\\', '/':
			out = append(out, c)
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'u':
			r := decodedJSONHex4(value[pos : pos+4])
			pos += 4
			if utf16.IsSurrogate(r) {
				if pos+6 <= len(value) && value[pos] == '\\' && value[pos+1] == 'u' {
					next := decodedJSONHex4(value[pos+2 : pos+6])
					if combined := utf16.DecodeRune(r, next); combined != utf8.RuneError {
						r = combined
						pos += 6
					} else {
						r = utf8.RuneError
					}
				} else {
					r = utf8.RuneError
				}
			}
			out = utf8.AppendRune(out, r)
		}
	}
	return string(out)
}

// decodedJSONHex4 的输入固定为 stringToken 已校验的四位十六进制转义。
func decodedJSONHex4(raw []byte) rune {
	var value rune
	for _, c := range raw {
		value <<= 4
		switch {
		case c <= '9':
			value |= rune(c - '0')
		case c <= 'F':
			value |= rune(c - 'A' + 10)
		default:
			value |= rune(c - 'a' + 10)
		}
	}
	return value
}
