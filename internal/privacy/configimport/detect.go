package configimport

import (
	"encoding/pem"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// 仅在导入时使用的保守格式集；长度与字符集参考 Gitleaks 对应规则。
// https://github.com/gitleaks/gitleaks/tree/5f6ec56cfa6f8c67648d863582c7ae2f206d5c1c/cmd/generate/config/rules
// 不做熵推断、在线有效性验证或运行期规则开启。
var credentialFormats = []struct {
	name, keyword string
	re            *regexp.Regexp
}{
	{"npm 凭据格式", "npm_", regexp.MustCompile(`npm_[A-Za-z0-9]{36}`)},
	{"Stripe 私密凭据格式", "k_", regexp.MustCompile(`(?:sk|rk)_(?:live|test|prod)_[A-Za-z0-9]{10,99}`)},
	// SendGrid 选用 SG.<22 位 ID>.<43 位秘密>；比上游的 66 位宽字符模式更严格。
	{"SendGrid 凭据格式", "SG.", regexp.MustCompile(`SG\.[A-Za-z0-9_-]{22}\.[A-Za-z0-9_-]{43}`)},
	{"GitLab PAT/deploy/runner 凭据格式", "gl", regexp.MustCompile(`(?:glpat|gldt|glrt)-[A-Za-z0-9_-]{20}`)},
	{"GitLab 可路由 PAT/runner 凭据格式", "gl", regexp.MustCompile(`(?:glpat-|glrt-t[0-9]_)[A-Za-z0-9_-]{27,300}\.[a-z0-9]{9}`)},
	{"GitLab runner 注册凭据格式", "GR1348941", regexp.MustCompile(`GR1348941[A-Za-z0-9_-]{20}`)},
	{"GitHub 凭据格式", "gh", regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{36}`)},
	{"GitHub 细粒度 PAT 格式", "github_pat_", regexp.MustCompile(`github_pat_[A-Za-z0-9_]{82}`)},
	{"OpenAI 凭据格式", "T3BlbkFJ", regexp.MustCompile(`sk-(?:proj|svcacct|admin)-(?:[A-Za-z0-9_-]{74}|[A-Za-z0-9_-]{58})T3BlbkFJ(?:[A-Za-z0-9_-]{74}|[A-Za-z0-9_-]{58})|sk-[A-Za-z0-9]{20}T3BlbkFJ[A-Za-z0-9]{20}`)},
	{"Anthropic 凭据格式", "sk-ant-", regexp.MustCompile(`sk-ant-(?:api03|admin01)-[A-Za-z0-9_-]{93}AA`)},
}

func fieldCategory(key string) (string, bool) {
	var b strings.Builder
	runes := []rune(key)
	for i, r := range runes {
		// 连续大写缩写整体保留：APIKey -> API_Key，dbAPIKey -> db_API_Key。
		if unicode.IsUpper(r) && i > 0 && (unicode.IsLower(runes[i-1]) || unicode.IsDigit(runes[i-1]) ||
			(unicode.IsUpper(runes[i-1]) && i+1 < len(runes) && unicode.IsLower(runes[i+1]))) {
			b.WriteByte('_')
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		} else {
			b.WriteByte('_')
		}
	}
	words := strings.FieldsFunc(b.String(), func(r rune) bool { return r == '_' })
	normal := "_" + strings.Join(words, "_")
	for _, suffix := range []string{"_password", "_passwd", "_pwd", "_passphrase", "_client_secret", "_secret", "_private_key", "_密码", "_密钥"} {
		if strings.HasSuffix(normal, suffix) {
			return "password", false
		}
	}
	if strings.HasSuffix(normal, "_api_key") || strings.HasSuffix(normal, "_apikey") {
		return "api_key", false
	}
	for _, suffix := range []string{"_token", "_authorization", "_凭据"} {
		if strings.HasSuffix(normal, suffix) {
			return "token", false
		}
	}
	for _, suffix := range []string{"_host", "_hostname", "_address", "_domain", "_username", "_user", "_account", "_database", "_dbname", "_database_name", "_client_id"} {
		if strings.HasSuffix(normal, suffix) {
			return "custom", true
		}
	}
	return "", false
}

func (p *parser) value(location, key, value string, isString bool, depth int) error {
	if err := p.step(depth); err != nil {
		return err
	}
	location = boundedFieldPath(location)
	p.result.Scanned++
	category, optional := fieldCategory(key)
	add := func(value, category, reason string, optional, excluded bool) error {
		return p.addFinding(Finding{Path: location, Category: category, Reason: reason, Value: value, Optional: optional, Excluded: excluded})
	}
	if !isString {
		if category != "" {
			return add("", category, "非字符串值，未收录；需要保护的数值请在配置中明确写为字符串", false, true)
		}
		return nil
	}
	if len(value) > MaxValueBytes {
		return add("", category, "单个值超过 64 KiB，未检查或收录", false, true)
	}
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		if category != "" {
			return add("", category, "空值，未收录", false, true)
		}
		return nil
	}
	if unresolved(value) {
		if category != "" || strings.Contains(value, "://") {
			return add("", category, "包含变量引用、模板或命令表达式；本版不求值，也不提取默认值", false, true)
		}
		return nil
	}
	if strings.Contains(value, "PRIVATE KEY-----") {
		block, rest := pem.Decode([]byte(trimmed))
		if block != nil && len(block.Bytes) > 0 && len(rest) == 0 && privateKeyType(block.Type) && strings.HasPrefix(trimmed, "-----BEGIN "+block.Type+"-----") {
			return add(value, "password", "完整 PEM 私钥（保留换行和首尾空白）", false, false)
		}
		return add("", "password", "私钥块不完整或格式不支持，未收录；不会只登记头部", false, true)
	}
	if u, err := url.Parse(strings.TrimPrefix(trimmed, "jdbc:")); err == nil && u.Host != "" && supportedScheme(u.Scheme) {
		if u.User != nil {
			if password, ok := u.User.Password(); ok && password != "" {
				if err := p.value(fieldPath(location, "URL密码"), "password", password, true, depth+1); err != nil {
					return err
				}
			}
		}
		query, queryErr := url.ParseQuery(u.RawQuery)
		if queryErr != nil {
			if err := add("", "custom", "连接 URL 参数语法不受支持，未提取参数中的凭据；完整连接串可按需选择", false, true); err != nil {
				return err
			}
		} else {
			keys := make([]string, 0, len(query))
			for k := range query {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if kind, optional := fieldCategory(k); kind != "" && !optional {
					for _, v := range query[k] {
						if err := p.value(fieldPath(location, "连接参数"), k, v, true, depth+1); err != nil {
							return err
						}
					}
				}
			}
		}
		return add(value, "custom", "完整连接 URL；可按需选择", true, false)
	}
	if category != "" {
		reason := "敏感字段中的实际字符串"
		if optional {
			reason = "账号或地址字段；可能影响普通文本，请按需选择"
		}
		if commonValue(trimmed) {
			optional = true
			reason = "常见值可能误伤普通文本，请检查后选择"
		}
		return add(value, category, reason, optional, false)
	}
	for _, format := range credentialFormats {
		if !strings.Contains(value, format.keyword) {
			continue
		}
		for _, loc := range format.re.FindAllStringIndex(value, MaxValues+1) {
			if (loc[0] > 0 && credentialChar(value[loc[0]-1])) || (loc[1] < len(value) && (credentialChar(value[loc[1]]) || value[loc[1]] == '=')) {
				continue
			}
			if err := add(value[loc[0]:loc[1]], "token", format.name, false, false); err != nil {
				return err
			}
		}
	}
	return nil
}

func unresolved(value string) bool {
	return strings.Contains(value, "${") || strings.Contains(value, "{{") || strings.Contains(value, "$(") || strings.Contains(value, "`") ||
		strings.HasPrefix(strings.TrimSpace(value), "process.env.") || strings.HasPrefix(strings.TrimSpace(value), "os.getenv(") ||
		strings.HasPrefix(strings.TrimSpace(value), "config.") || (strings.HasPrefix(value, "$") && envKey(strings.TrimPrefix(value, "$")))
}

func commonValue(value string) bool {
	switch strings.ToLower(value) {
	case "root", "admin", "test", "password", "password123", "changeme", "12345678", "your_api_key", "your_token", "your_password", "localhost":
		return true
	}
	return false
}

func privateKeyType(t string) bool {
	switch t {
	case "PRIVATE KEY", "RSA PRIVATE KEY", "EC PRIVATE KEY", "OPENSSH PRIVATE KEY", "ENCRYPTED PRIVATE KEY":
		return true
	}
	return false
}

func supportedScheme(s string) bool {
	switch s {
	case "postgres", "postgresql", "mysql", "mongodb", "mongodb+srv", "redis", "rediss", "amqp", "amqps":
		return true
	}
	return false
}

func credentialChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_.-/+", rune(c))
}
