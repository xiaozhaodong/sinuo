package configimport

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
)

func lineError(format string, line int) error {
	return fmt.Errorf("%s 第 %d 行语法不受支持；该文件未提取", format, line)
}

func (p *parser) parseEnv(content string) error {
	lines := strings.Split(strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(content), "\n")
	for i := 0; i < len(lines); i++ {
		if err := p.step(0); err != nil {
			return err
		}
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, rest, found := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !found || !envKey(key) {
			return lineError(".env", i+1)
		}
		startLine := i + 1
		rest = strings.TrimSpace(rest)
		value := rest
		if len(rest) > 0 && (rest[0] == '\'' || rest[0] == '"') {
			quote := rest[0]
			var b strings.Builder
			closed := false
			for j := 1; ; j++ {
				if j >= len(rest) {
					i++
					if i >= len(lines) {
						return lineError(".env", startLine)
					}
					b.WriteByte('\n')
					rest = lines[i]
					j = -1
					continue
				}
				c := rest[j]
				if c == quote {
					tail := strings.TrimSpace(rest[j+1:])
					if tail != "" && !strings.HasPrefix(tail, "#") {
						return lineError(".env", startLine)
					}
					closed = true
					break
				}
				if c == '\\' && quote == '"' {
					j++
					if j >= len(rest) {
						return lineError(".env", startLine)
					}
					switch rest[j] {
					case 'n':
						c = '\n'
					case 'r':
						c = '\r'
					case 't':
						c = '\t'
					case '\\', '"':
						c = rest[j]
					default:
						return lineError(".env 转义", startLine)
					}
				}
				b.WriteByte(c)
			}
			if !closed {
				return lineError(".env", startLine)
			}
			value = b.String()
		} else {
			// 未加引号值只有空白后的 # 才开始注释；值内部的 # 保留。
			var previous rune
			for index, current := range rest {
				if current == '#' && unicode.IsSpace(previous) {
					value = rest[:index]
					break
				}
				previous = current
			}
			value = strings.TrimSpace(value)
		}
		if err := p.value(fmt.Sprintf("%s（第%d行）", key, startLine), key, value, true, 0); err != nil {
			return err
		}
	}
	return nil
}

func envKey(key string) bool {
	if key == "" {
		return false
	}
	for i, c := range key {
		if c != '_' && !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func (p *parser) parseProperties(content string) error {
	lines := strings.Split(strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(content), "\n")
	for i := 0; i < len(lines); i++ {
		if err := p.step(0); err != nil {
			return err
		}
		line := strings.TrimLeft(lines[i], " \t\f")
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		startLine := i + 1
		if trailingSlashes(line)%2 == 1 {
			var joined strings.Builder
			for trailingSlashes(line)%2 == 1 {
				if err := p.step(0); err != nil {
					return err
				}
				joined.WriteString(line[:len(line)-1])
				i++
				if i >= len(lines) {
					return lineError("properties 续行", startLine)
				}
				line = strings.TrimLeft(lines[i], " \t\f")
			}
			joined.WriteString(line)
			line = joined.String()
		}
		end, escaped := len(line), false
		for j := 0; j < len(line); j++ {
			if escaped {
				escaped = false
				continue
			}
			if line[j] == '\\' {
				escaped = true
				continue
			}
			if strings.ContainsRune("=: \t\f", rune(line[j])) {
				end = j
				break
			}
		}
		rest := strings.TrimLeft(line[end:], " \t\f")
		if len(rest) > 0 && (rest[0] == '=' || rest[0] == ':') {
			rest = rest[1:]
		}
		rest = strings.TrimLeft(rest, " \t\f")
		key, err := decodeProperty(line[:end])
		if err != nil {
			return lineError("properties 键转义", startLine)
		}
		value, err := decodeProperty(rest)
		if err != nil {
			return lineError("properties 值转义", startLine)
		}
		if err := p.value(fmt.Sprintf("%s（第%d行）", fieldPath("", key), startLine), key, value, true, 0); err != nil {
			return err
		}
	}
	return nil
}

func trailingSlashes(s string) int {
	n := 0
	for i := len(s) - 1; i >= 0 && s[i] == '\\'; i-- {
		n++
	}
	return n
}

// Java properties 的未知转义去掉反斜杠；Unicode 必须完整并正确配对。
func decodeProperty(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		i++
		if i >= len(s) {
			return "", errors.New("incomplete escape")
		}
		switch s[i] {
		case 't':
			b.WriteByte('\t')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 'f':
			b.WriteByte('\f')
		case 'u':
			if i+4 >= len(s) {
				return "", errors.New("incomplete unicode")
			}
			n, err := strconv.ParseUint(s[i+1:i+5], 16, 16)
			if err != nil {
				return "", err
			}
			i += 4
			r := rune(n)
			if utf16.IsSurrogate(r) {
				if i+6 >= len(s) || s[i+1:i+3] != "\\u" {
					return "", errors.New("unpaired unicode")
				}
				m, err := strconv.ParseUint(s[i+3:i+7], 16, 16)
				if err != nil {
					return "", err
				}
				r = utf16.DecodeRune(r, rune(m))
				if r == '\ufffd' {
					return "", errors.New("unpaired unicode")
				}
				i += 6
			}
			b.WriteRune(r)
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String(), nil
}
