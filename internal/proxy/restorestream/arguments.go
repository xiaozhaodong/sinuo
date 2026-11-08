package restorestream

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"sinuo/internal/codexidentity"
)

// 工具参数是 SSE 字段中的第二层 JSON。键、数值和结构符保留原样，只有字符串值拥有恢复权限。
type arguments struct {
	containers  []byte
	previous    byte
	quoted, key bool
	escape      string
	token       matcher
}

func (a *arguments) feed(s string, d *countedValues) (string, error) {
	if a.escape != "" {
		s = a.escape + s
		a.escape = ""
	}
	var out strings.Builder
	out.Grow(len(s))
	for len(s) > 0 {
		if out.Len() > d.limit {
			return "", ErrDelivery
		}
		if !a.quoted {
			i := strings.IndexByte(s, '"')
			n := len(s)
			if i >= 0 {
				n = i
			}
			for j := 0; j < n; j++ {
				c := s[j]
				switch c {
				case '{', '[':
					if len(a.containers) >= 256 {
						return "", ErrDelivery
					}
					a.containers = append(a.containers, c)
				case '}', ']':
					if len(a.containers) == 0 {
						return "", ErrDelivery
					}
					a.containers = a.containers[:len(a.containers)-1]
				case ' ', '\n', '\t', '\r':
					continue
				}
				a.previous = c
			}
			out.WriteString(s[:n])
			s = s[n:]
			if len(s) == 0 {
				break
			}
			a.quoted = true
			a.key = len(a.containers) > 0 && a.containers[len(a.containers)-1] == '{' && (a.previous == '{' || a.previous == ',')
			out.WriteByte('"')
			s = s[1:]
			continue
		}
		i := strings.IndexAny(s, "\\\"")
		if i < 0 {
			i = len(s)
		}
		if i > 0 {
			plain := s[:i]
			s = s[i:]
			if !a.key {
				part, e := a.token.feed(plain, d, false)
				if e != nil {
					return "", e
				}
				if part == plain {
					out.WriteString(plain)
				} else {
					out.WriteString(quoteBody(part))
				}
			} else {
				out.WriteString(plain)
			}
		}
		if len(s) == 0 {
			break
		}
		if s[0] == '"' {
			if !a.key {
				part, e := a.token.feed("", d, true)
				if e != nil {
					return "", e
				}
				if part != "" {
					out.WriteString(quoteBody(part))
				}
			}
			out.WriteByte('"')
			a.quoted = false
			a.previous = '"'
			s = s[1:]
			continue
		}
		n := 2
		if len(s) >= 2 && s[1] == 'u' {
			n = 6
			if len(s) >= 6 {
				r, e := strconv.ParseUint(s[2:6], 16, 16)
				if e != nil {
					return "", ErrDelivery
				}
				if utf16.IsSurrogate(rune(r)) {
					if r >= 0xdc00 {
						return "", ErrDelivery
					}
					n = 12
					if len(s) >= 12 {
						if s[6:8] != "\\u" {
							return "", ErrDelivery
						}
						low, e := strconv.ParseUint(s[8:12], 16, 16)
						if e != nil || low < 0xdc00 || low > 0xdfff {
							return "", ErrDelivery
						}
					}
				}
			}
		}
		if len(s) < n {
			a.escape = s
			break
		}
		literal := s[:n]
		s = s[n:]
		if n == 2 && strings.ContainsRune(`"\/bfnrt`, rune(literal[1])) && (a.key || a.token.tail == "") {
			out.WriteString(literal)
			continue
		}
		var decoded string
		if json.Unmarshal([]byte("\""+literal+"\""), &decoded) != nil || !utf8.ValidString(decoded) {
			return "", ErrDelivery
		}
		if a.key {
			out.WriteString(literal)
		} else {
			part, e := a.token.feed(decoded, d, false)
			if e != nil {
				return "", e
			}
			out.WriteString(quoteBody(part))
		}
	}
	if out.Len() > d.limit {
		return "", ErrDelivery
	}
	return out.String(), nil
}
func (a *arguments) finish(original string) error {
	if a.quoted || a.escape != "" || len(a.containers) > 0 || codexidentity.ValidateJSON([]byte(original)) != nil {
		return ErrDelivery
	}
	return nil
}
