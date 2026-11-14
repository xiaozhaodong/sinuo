package configimport

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// AWS 的 profile 与 npm registry 仅用于来源定位；不执行变量展开或读取 keyfile。
func (p *parser) parseCredentialAssignments(content, format string) error {
	section := ""
	for i, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		if err := p.step(0); err != nil {
			return err
		}
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				return lineError(format, i+1)
			}
			section = strings.TrimSpace(line[1 : len(line)-1])
			if section == "" {
				return lineError(format, i+1)
			}
			continue
		}
		key, raw, ok := strings.Cut(line, "=")
		key, raw = strings.TrimSpace(key), strings.TrimSpace(raw)
		if !ok && format == "npmrc" {
			// npm/ini 将无等号的配置项解析为布尔 true，不包含可导入的字符串凭据。
			p.result.Scanned++
			continue
		}
		if !ok || key == "" || format == "aws" && section == "" {
			return lineError(format, i+1)
		}
		value := raw
		if format == "npmrc" {
			var err error
			value, err = npmValue(raw)
			if err != nil {
				return lineError(format, i+1)
			}
		}
		location := fieldPath(section, key)
		field := key
		if format == "aws" {
			switch strings.ToLower(key) {
			case "aws_access_key_id":
				field = "api_key"
			case "aws_secret_access_key":
				field = "secret"
			case "aws_session_token":
				field = "token"
			}
		} else {
			if colon := strings.LastIndexByte(key, ':'); colon >= 0 {
				field = key[colon+1:]
			}
			switch strings.ToLower(field) {
			case "_authtoken":
				field = "token"
			case "_auth", "_password":
				// npm 以 Base64 保存 Basic 认证或密码；登记逻辑值后已有精确匹配器派生编码形式。
				if unresolved(value) {
					field = "password"
					break
				}
				decoded, err := base64.StdEncoding.DecodeString(value)
				if err != nil || !utf8.Valid(decoded) || strings.ContainsRune(string(decoded), 0) {
					return lineError("npm 认证编码", i+1)
				}
				if strings.EqualFold(field, "_auth") {
					_, password, found := strings.Cut(string(decoded), ":")
					if !found {
						return lineError("npm Basic 认证", i+1)
					}
					if err := p.value(location+".password", "password", password, true, 0); err != nil {
						return err
					}
					field = "token"
				} else {
					field = "password"
				}
				value = string(decoded)
			case "keyfile", "certfile":
				p.result.Scanned++
				if err := p.addFinding(Finding{Path: location, Excluded: true, Reason: "仅为文件引用，不读取所指文件"}); err != nil {
					return err
				}
				continue
			}
		}
		if err := p.value(location, field, value, true, 0); err != nil {
			return err
		}
	}
	return nil
}

// npm/ini 的引号值保留分隔符；非引号值支持转义的 #、;、反斜杠。
func npmValue(raw string) (string, error) {
	if strings.HasPrefix(raw, "\"") {
		var value string
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			return "", err
		}
		return value, nil
	}
	if strings.HasPrefix(raw, "'") {
		if len(raw) < 2 || !strings.HasSuffix(raw, "'") {
			return "", fmt.Errorf("unclosed quote")
		}
		return raw[1 : len(raw)-1], nil
	}
	var out strings.Builder
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c == '#' || c == ';' {
			break
		}
		if c == '\\' && i+1 < len(raw) && strings.ContainsRune("\\#;", rune(raw[i+1])) {
			i++
			c = raw[i]
		}
		out.WriteByte(c)
	}
	return strings.TrimSpace(out.String()), nil
}

// netrc 按 token 读取，支持 curl 双引号及转义。宏明确报错，避免扫描宏正文产生假凭据。
func (p *parser) parseNetrc(content string) error {
	position, machine := 0, ""
	next := func() (string, error) { return netrcToken(content, &position) }
	for position < len(content) {
		if err := p.step(0); err != nil {
			return err
		}
		key, err := next()
		if err != nil {
			return lineError("netrc", 1)
		}
		if key == "" && position == len(content) {
			break
		}
		if key == "default" {
			machine = "default"
			continue
		}
		if key == "macdef" {
			return fmt.Errorf("netrc 宏语法暂不支持；该文件未提取")
		}
		if key != "machine" && key != "login" && key != "password" && key != "account" {
			return lineError("netrc", 1)
		}
		value, err := next()
		if err != nil || value == "" {
			return lineError("netrc", 1)
		}
		if key == "machine" {
			machine = value
			continue
		}
		if machine == "" {
			return lineError("netrc", 1)
		}
		field := key
		if key == "login" {
			field = "username"
		}
		if err := p.value(fieldPath(machine, key), field, value, true, 0); err != nil {
			return err
		}
	}
	return nil
}

func netrcToken(text string, pos *int) (string, error) {
	for *pos < len(text) {
		if strings.ContainsRune(" \r\n\t", rune(text[*pos])) {
			*pos++
			continue
		}
		if text[*pos] != '#' {
			break
		}
		for *pos < len(text) && text[*pos] != '\n' {
			*pos++
		}
	}
	if *pos == len(text) {
		return "", nil
	}
	quoted := text[*pos] == '"'
	if quoted {
		*pos++
	}
	var out strings.Builder
	for *pos < len(text) {
		c := text[*pos]
		*pos++
		if quoted && c == '"' {
			if *pos < len(text) && !strings.ContainsRune(" \r\n\t", rune(text[*pos])) {
				return "", fmt.Errorf("quote boundary")
			}
			return out.String(), nil
		}
		if !quoted && strings.ContainsRune(" \r\n\t", rune(c)) {
			return out.String(), nil
		}
		if quoted && c == '\\' {
			if *pos == len(text) {
				return "", fmt.Errorf("escape")
			}
			c = text[*pos]
			*pos++
			switch c {
			case 'n':
				c = '\n'
			case 'r':
				c = '\r'
			case 't':
				c = '\t'
			case '"', '\\':
			default:
				return "", fmt.Errorf("escape")
			}
		}
		out.WriteByte(c)
	}
	if quoted {
		return "", fmt.Errorf("unclosed quote")
	}
	return out.String(), nil
}
