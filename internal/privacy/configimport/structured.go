package configimport

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

func (p *parser) parseJSON(content string) error {
	if !validJSONUnicodeEscapes(content) {
		return errors.New("JSON 含未配对的 Unicode 转义；该文件未提取，避免改变实际值")
	}
	d := json.NewDecoder(strings.NewReader(content))
	d.UseNumber()
	if err := p.jsonValue(d, "", "", 0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("JSON 文件包含多余内容或语法错误")
	}
	return nil
}

// encoding/json 会把未配对的 UTF-16 转义替换为 U+FFFD；导入不能静默改变密码。
func validJSONUnicodeEscapes(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			continue
		}
		if i+1 >= len(s) {
			return false
		}
		if s[i+1] != 'u' {
			i++
			continue
		}
		if i+6 > len(s) {
			return false
		}
		n, err := strconv.ParseUint(s[i+2:i+6], 16, 16)
		if err != nil {
			return false
		}
		if n >= 0xDC00 && n <= 0xDFFF {
			return false
		}
		if n >= 0xD800 && n <= 0xDBFF {
			if i+12 > len(s) || s[i+6:i+8] != "\\u" {
				return false
			}
			m, err := strconv.ParseUint(s[i+8:i+12], 16, 16)
			if err != nil || m < 0xDC00 || m > 0xDFFF {
				return false
			}
			i += 6
		}
		i += 5
	}
	return true
}

// 逐个读取字段，重复键也会保留，避免 map 覆盖前面的秘密。
func (p *parser) jsonValue(d *json.Decoder, location, key string, depth int) error {
	if err := p.step(depth); err != nil {
		return err
	}
	token, err := d.Token()
	if err != nil {
		return errors.New("JSON 语法错误；该文件未提取，请检查引号、逗号和括号")
	}
	if delim, ok := token.(json.Delim); ok {
		switch delim {
		case '{':
			for d.More() {
				t, err := d.Token()
				if err != nil {
					return errors.New("JSON 字段语法错误")
				}
				k, ok := t.(string)
				if !ok {
					return errors.New("JSON 字段名必须是字符串")
				}
				if err := p.jsonValue(d, fieldPath(location, k), k, depth+1); err != nil {
					return err
				}
			}
		case '[':
			for i := 0; d.More(); i++ {
				if err := p.jsonValue(d, fmt.Sprintf("%s[%d]", location, i), key, depth+1); err != nil {
					return err
				}
			}
		default:
			return errors.New("JSON 容器语法错误")
		}
		if _, err := d.Token(); err != nil {
			return errors.New("JSON 容器未完整结束")
		}
		return nil
	}
	value, isString := token.(string)
	return p.value(location, key, value, isString, depth)
}
