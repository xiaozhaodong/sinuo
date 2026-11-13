// Package restorestream 在协议可见字段中按进程字典还原占位符，并统计实际交付。
package restorestream

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"sinuo/internal/privacy"
)

const maxBytes = 4 << 20

var ErrDelivery = errors.New("privacy_restore_failed: 响应回填不完整")

// ErrLocalCapacity 仅表示本地预算或字典能力不足，不能用于协议与归属错误。
// 只有尚未交付的服务端后继首帧可以据此撤销回填并应用失败策略。
var ErrLocalCapacity = fmt.Errorf("%w: 本地回填能力不足", ErrDelivery)

type Dictionary interface{ Lookup(string) (string, bool) }

// feed 只扣留还可能组成占位符的后缀；没有跨事件状态或标识时返回原字符串。
type matcher struct {
	tail string
}

func (m *matcher) feed(s string, d *countedValues, final bool) (string, error) {
	if m.tail != "" {
		s = m.tail + s
		m.tail = ""
	}
	var out strings.Builder
	for {
		i := strings.Index(s, privacy.RestorePrefix)
		if i < 0 {
			break
		}
		out.WriteString(s[:i])
		s = s[i:]
		n := len(privacy.RestorePrefix)
		for n < len(s) && n < privacy.RestoreTokenSize-2 && ((s[n] >= 'a' && s[n] <= 'f') || (s[n] >= '0' && s[n] <= '9')) {
			n++
		}
		// 仅对完整的 32 位小写十六进制标识查表。
		end := n
		for n < len(s) && n < end+2 && s[n] == '}' {
			n++
		}
		if n == privacy.RestoreTokenSize && n == end+2 {
			id := s[:n]
			v, ok, err := d.lookup(id)
			if err != nil {
				return "", err
			}
			if !ok {
				d.unknown++
				v = id
			}
			if len(v) > d.limit-out.Len() {
				return "", ErrLocalCapacity
			}
			out.WriteString(v)
			s = s[n:]
			continue
		}
		if n == len(s) && !final {
			m.tail = strings.Clone(s)
			return out.String(), nil
		}
		d.unknown++
		// 未知或被模型改写的文本原样保留，继续寻找下一枚合法标识。
		out.WriteString(s[:n])
		s = s[n:]
	}
	if !final {
		for n := min(len(s), len(privacy.RestorePrefix)-1); n > 0; n-- {
			if strings.HasSuffix(s, privacy.RestorePrefix[:n]) {
				m.tail = strings.Clone(s[len(s)-n:])
				s = s[:len(s)-n]
				break
			}
		}
	}
	if len(s) > d.limit-out.Len() {
		return "", ErrLocalCapacity
	}
	if out.Len() == 0 {
		return s, nil
	}
	out.WriteString(s)
	return out.String(), nil
}

func quoteBody(s string) string {
	// 可直接嵌入 JSON 字符串的可打印 ASCII 沿用原串，其余交给标准库。
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < ' ' || c > '~' || c == '"' || c == '\\' || c == '&' || c == '<' || c == '>' {
			q, _ := json.Marshal(s)
			return string(q[1 : len(q)-1])
		}
	}
	return s
}
