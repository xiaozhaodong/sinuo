package restorestream

import "strings"

// commonJSONEscape 解码占用一个字节的 JSON 转义。Unicode 转义由原解码器处理。
func commonJSONEscape(c byte) (byte, bool) {
	switch c {
	case '"', '\\', '/':
		return c, true
	case 'b':
		return '\b', true
	case 'f':
		return '\f', true
	case 'n':
		return '\n', true
	case 'r':
		return '\r', true
	case 't':
		return '\t', true
	}
	return 0, false
}

// 对已验 UTF-8 的长 delta，同时检查字符串边界、控制字符和常见转义。
// 短串及含 Unicode 转义的串沿用 SSE 原有的 JSON 校验与解码。
func decodeCommonJSONString(literal []byte) (string, bool) {
	const minSize = 128
	if len(literal) < minSize || literal[0] != '"' || literal[len(literal)-1] != '"' {
		return "", false
	}
	body := literal[1 : len(literal)-1]
	var out strings.Builder
	start := 0
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c == '"' || c < 0x20 {
			return "", false
		}
		if c != '\\' {
			continue
		}
		if i+1 == len(body) {
			return "", false
		}
		decoded, ok := commonJSONEscape(body[i+1])
		if !ok {
			return "", false
		}
		if start == 0 {
			out.Grow(len(body))
		}
		out.Write(body[start:i])
		out.WriteByte(decoded)
		i++
		start = i + 1
	}
	if start == 0 {
		return string(body), true
	}
	out.Write(body[start:])
	return out.String(), true
}
