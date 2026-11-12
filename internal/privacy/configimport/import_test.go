package configimport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"
)

func parseOne(t *testing.T, name, content string) Result {
	t.Helper()
	out, err := Parse(context.Background(), []File{{Name: name, Content: content}})
	if err != nil {
		t.Fatal(err)
	}
	return out[0]
}

func TestConfigImport_CandidateLimit_AllowsTrailingNonCandidates(t *testing.T) {
	for _, format := range []string{"env", "properties", "json", "yaml"} {
		for _, count := range []int{MaxValues - 1, MaxValues, MaxValues + 1} {
			t.Run(fmt.Sprintf("%s/%d", format, count), func(t *testing.T) {
				var content strings.Builder
				if format == "json" {
					content.WriteByte('{')
				}
				for i := 0; i < count; i++ {
					key := fmt.Sprintf("DB_%d_PASSWORD", i)
					switch format {
					case "json":
						fmt.Fprintf(&content, "%q:%q,", key, "synthetic-passphrase")
					case "yaml":
						fmt.Fprintf(&content, "%s: synthetic-passphrase\n", key)
					default:
						fmt.Fprintf(&content, "%s=synthetic-passphrase\n", key)
					}
				}
				switch format {
				case "json":
					content.WriteString(`"ORDINARY":"ordinary text"}`)
				case "yaml":
					content.WriteString("\n# 注释\nORDINARY: ordinary text\n")
				default:
					content.WriteString("\n# 注释\nORDINARY=ordinary text\n")
				}
				r := parseOne(t, "config."+format, content.String())
				if count > MaxValues {
					if !strings.Contains(r.Error, "候选数超过") || len(r.Findings) != 0 {
						t.Fatal("超限应拒绝整个文件并丢弃部分候选", r.Error)
					}
					return
				}
				if r.Error != "" || len(r.Findings) != count || r.Scanned != count+1 {
					t.Fatalf("上限内应继续处理普通字段、注释和换行: error=%s findings=%d scanned=%d", r.Error, len(r.Findings), r.Scanned)
				}
			})
		}
	}
}

func TestConfigImportAcronymFields(t *testing.T) {
	for _, key := range []string{"APIKey", "apiKey", "apiKEY", "DBApiKey", "dbAPIKey", "API_KEY", "APIKEY", "db.api-key"} {
		for _, tc := range []struct{ name, content string }{
			{"a.json", fmt.Sprintf(`{"%s":"synthetic-credential-123456"}`, key)},
			{"a.yml", key + ": synthetic-credential-123456"},
			{"a.properties", key + "=synthetic-credential-123456"},
		} {
			r := parseOne(t, tc.name, tc.content)
			if r.Error != "" || len(r.Findings) != 1 || r.Findings[0].Category != "api_key" || r.Findings[0].Optional {
				t.Fatal("缩写字段漏识别", key, tc.name, r.Error)
			}
		}
	}
	for key, want := range map[string]string{"DBPassword": "password", "accessTOKEN": "token", "OAuthToken": "token", "DB_PASSWORD": "password", "ClientID": "custom", "privateKeyPath": "", "APIKeyCount": ""} {
		if got, _ := fieldCategory(key); got != want {
			t.Fatalf("%s 分类为 %q，期望 %q", key, got, want)
		}
	}
}

func TestConfigImportURLCredentialsShareClassification(t *testing.T) {
	for _, tc := range []struct {
		value              string
		optional, excluded bool
	}{
		{"password123", true, false}, {"${DB_PASSWORD}", false, true},
		{"$DB_PASSWORD", false, true}, {"  actual-passphrase  ", false, false},
	} {
		inputs := []string{tc.value, "postgres://u:" + url.QueryEscape(tc.value) + "@db.invalid/x", "postgres://db.invalid/x?password=" + url.QueryEscape(tc.value)}
		// Userinfo 中的 + 不代表空格，使用 URL 密码段编码保留前后空白。
		inputs[1] = "postgres://" + url.UserPassword("u", tc.value).String() + "@db.invalid/x"
		for _, input := range inputs {
			raw, _ := json.Marshal(map[string]string{"password": input})
			r := parseOne(t, "a.json", string(raw))
			found := false
			for _, f := range r.Findings {
				if f.Category != "password" {
					continue
				}
				found = true
				if f.Optional != tc.optional || f.Excluded != tc.excluded || (!tc.excluded && f.Value != tc.value) {
					t.Fatal("URL 密码与普通字段分类或实际值不一致", f)
				}
			}
			if r.Error != "" || !found {
				t.Fatal("没有给出密码候选分类", r.Error)
			}
		}
	}
}

func TestConfigImportURLDepthAndPathBudget(t *testing.T) {
	for _, depth := range []int{10, 60, 65, 2500} {
		value := strings.Repeat("postgres://h?password=", depth) + "synthetic-passphrase"
		raw, _ := json.Marshal(map[string]string{"password": value})
		r := parseOne(t, "a.json", string(raw))
		if depth > maxDepth {
			if r.Error == "" || len(r.Findings) != 0 {
				t.Fatal("嵌套 URL 应触发层数限制并丢弃文件候选", depth)
			}
			continue
		}
		if r.Error != "" || len(r.Findings) != depth+1 {
			t.Fatal("受支持深度的 URL 解析失败", depth, r.Error)
		}
		for _, f := range r.Findings {
			if len(f.Path) > maxPathBytes || !utf8.ValidString(f.Path) {
				t.Fatal("来源路径超限或破坏 UTF-8")
			}
		}
	}
	// JSON/YAML 的结构深度与 URL 派生深度不能分别重新计数。
	value := strings.Repeat("postgres://h?password=", 60) + "synthetic-passphrase"
	raw, _ := json.Marshal(value)
	for _, tc := range []struct{ name, content string }{
		{"a.json", strings.Repeat(`{"nested":`, 5) + `{"password":` + string(raw) + `}` + strings.Repeat("}", 5)},
		{"a.yml", "a:\n  b:\n    c:\n      d:\n        password: " + value},
	} {
		if r := parseOne(t, tc.name, tc.content); r.Error == "" || len(r.Findings) != 0 {
			t.Fatal("组合深度未统一受限", tc.name)
		}
	}
}

func TestConfigImportFindingLimitDuringURLUnwind(t *testing.T) {
	items := make([]string, MaxValues-1)
	for i := range items {
		items[i] = `"password":"synthetic-passphrase"`
	}
	items = append(items, `"url":"postgres://u:other-passphrase@h/db"`)
	out, err := Parse(context.Background(), []File{{Name: "too-many.json", Content: "{" + strings.Join(items, ",") + "}"}, {Name: "good.json", Content: `{"password":"still-valid-value"}`}})
	if err != nil || len(out) != 2 || out[0].Error == "" || len(out[0].Findings) != 0 || len(out[1].Findings) != 1 {
		t.Fatal("递归返回时追加候选应逐项检查，超限只废弃错误文件", err)
	}
}

func values(result Result) []string {
	var out []string
	for _, f := range result.Findings {
		if !f.Excluded {
			out = append(out, f.Value)
		}
	}
	return out
}

func TestConfigImportDecodedValues(t *testing.T) {
	cases := []struct{ name, input, value string }{
		{".env", "export DB_PASSWORD=\"  alpha9 beta:42,#  \" # note", "  alpha9 beta:42,#  "},
		{".env.production", "DB_PASSWORD='alpha\\beta 42'", "alpha\\beta 42"},
		{"dev.env", "DB_PASSWORD=alpha9beta42 # comment", "alpha9beta42"},
		{"a.json", `{"nested":{"passw\u006frd":"  alpha9\\beta\n42  "}}`, "  alpha9\\beta\n42  "},
		{"a.yaml", "db:\n  password: |\n    alpha9 beta\n    42\n", "alpha9 beta\n42\n"},
		{"a.yml", "db:\n  password: >-\n    alpha9 beta\n    42\n", "alpha9 beta 42"},
		{"a.properties", "spring.datasource.password : alpha\\ beta\\:\\\n  42\\u0021 ", "alpha beta:42! "},
		{"a.properties", `password=abc12345\uD83D\uDE00`, "abc12345😀"},
		{"a.json", `{"password":"abc12345\ud83d\ude00"}`, "abc12345😀"},
		{"a.json", `{"password":"abc12345\\ud800"}`, `abc12345\ud800`},
		{"a.properties", "# comment\rpassword=abc12345\r", "abc12345"},
		{".env", "DB_PASSWORD=\"alpha9\\nline42\"", "alpha9\nline42"},
		{".env", "DB_PASSWORD='alpha9\nline42'", "alpha9\nline42"},
	}
	for _, tc := range cases {
		t.Run(tc.name+tc.input[:8], func(t *testing.T) {
			r := parseOne(t, tc.name, tc.input)
			v := values(r)
			if r.Error != "" || len(v) != 1 || v[0] != tc.value {
				t.Fatalf("结果不保真: %+v values=%q want=%q", r, v, tc.value)
			}
		})
	}
}

func TestConfigImport_EnvHash_PreservesValuesAndComments(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"password", "DB_PASSWORD=abc#123", "abc#123"},
		{"export_token", "export TOKEN=x9#y8", "x9#y8"},
		{"leading_hash", "DB_PASSWORD=#abc123", "#abc123"},
		{"repeated_hash", "DB_PASSWORD=abc##123", "abc##123"},
		{"space_comment", "DB_PASSWORD=abc # comment", "abc"},
		{"tab_comment", "DB_PASSWORD=abc\t# comment", "abc"},
		{"unicode_space_comment", "DB_PASSWORD=abc\u3000# comment", "abc"},
		{"hash_then_comment", "DB_PASSWORD=abc#123 # comment # tail", "abc#123"},
		{"spaces_in_value", "DB_PASSWORD=alpha beta#123 # comment", "alpha beta#123"},
		{"trailing_space", "DB_PASSWORD=abc#123 \t", "abc#123"},
		{"double_quoted", "DB_PASSWORD=\"abc #123\" # comment", "abc #123"},
		{"single_quoted", "DB_PASSWORD='abc #123' # comment", "abc #123"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := parseOne(t, ".env", tc.input)
			got := values(r)
			if r.Error != "" || len(got) != 1 || got[0] != tc.want {
				t.Fatalf("# 注释边界改变实际值: error=%s values=%q want=%q", r.Error, got, tc.want)
			}
		})
	}
}

func TestConfigImportDuplicateKeysAndArrays(t *testing.T) {
	r := parseOne(t, "a.json", `{"password":"first-value","password":"second-value","tokens":[{"access_token":"third-long-value"}]}`)
	if got := values(r); len(got) != 3 || got[0] != "first-value" || got[1] != "second-value" {
		t.Fatal(got, r.Error)
	}
	r = parseOne(t, "a.yml", "password: first-value\npassword: second-value\n---\npassword: third-value")
	if len(values(r)) != 3 {
		t.Fatal(r)
	}
}

func TestConfigImportReferencesAndNonSecrets(t *testing.T) {
	for _, value := range []string{"${DB_PASSWORD}", "${DB_PASSWORD:-literal-default}", "{{ DB_PASSWORD }}", "process.env.DB_PASSWORD", "$(read_secret)", "os.getenv(\"DB_PASSWORD\")"} {
		b, _ := json.Marshal(map[string]string{"password": value})
		r := parseOne(t, "a.json", string(b))
		if len(values(r)) != 0 || len(r.Findings) != 1 || !r.Findings[0].Excluded {
			t.Fatal(r)
		}
	}
	r := parseOne(t, "a.json", `{"password":12345678,"token":null,"client_secret":true,"empty_password":"","max_tokens":3000,"token_count":"999999999999","passwordInput":"input-widget","public_key":"ssh-rsa example","key_id":"0123456789abcdef","private_key_path":"/not/read.pem","sha":"aabbccddeeff0123456789","uuid":"17aaddbb-efcc-4f22-aa22-303030303030"}`)
	if len(values(r)) != 0 || len(r.Findings) != 4 {
		t.Fatal(r)
	}
	r = parseOne(t, "a.json", `{"dbPassword":"actual-passphrase","API_KEY":"actual-api-key-value","refreshToken":"actual-refresh-token","username":"operator","host":"10.1.2.3","password":"password"}`)
	if len(values(r)) != 6 {
		t.Fatal(r)
	}
	for _, f := range r.Findings {
		if (f.Value == "operator" || f.Value == "10.1.2.3" || f.Value == "password") && !f.Optional {
			t.Fatal("不应默认选择", f)
		}
	}
}

func TestConfigImportCredentialsWholeBoundaries(t *testing.T) {
	prefixes := []string{"npm_", "sk_live_", "rk_test_", "glpat-", "gldt-", "glrt-", "ghp_"}
	for _, prefix := range prefixes {
		n := 36
		if strings.HasPrefix(prefix, "gl") {
			n = 20
		}
		for i := 0; i < 10; i++ {
			value := prefix + strings.Repeat(string(rune('a'+i)), n)
			b, _ := json.Marshal(map[string]string{"credential": "prefix " + value + " suffix"})
			r := parseOne(t, "a.json", string(b))
			if len(values(r)) != 1 || values(r)[0] != value {
				t.Fatal(prefix, r)
			}
			for _, bad := range []string{"x" + value, value + strings.Repeat("x", 110), value + "_trailing", "prefix/" + value, value + ".tail"} {
				b, _ := json.Marshal(map[string]string{"credential": bad})
				r := parseOne(t, "a.json", string(b))
				if len(values(r)) != 0 {
					t.Fatal("不能截断凭据", prefix, r)
				}
			}
		}
	}
	r := parseOne(t, "a.json", `{"credential":"pk_live_abcdefghijklmnopqrstuvwxyz0123456789"}`)
	if len(values(r)) != 0 {
		t.Fatal("公开 Stripe key 不应识别", r)
	}
}

func TestConfigImportPrivateKeyAndDatabaseURL(t *testing.T) {
	key := "-----BEGIN PRIVATE KEY-----\nYWJjZGVmZ2hpamts\n-----END PRIVATE KEY-----\n"
	b, _ := json.Marshal(map[string]string{"credential": key})
	r := parseOne(t, "a.json", string(b))
	if len(values(r)) != 1 || values(r)[0] != key {
		t.Fatal(r)
	}
	for _, bad := range []string{"-----BEGIN PRIVATE KEY-----", "-----BEGIN PRIVATE KEY-----\nYWJjZGVmZ2hpamts", "-----BEGIN PRIVATE KEY-----\ninvalid!\n-----END PRIVATE KEY-----"} {
		b, _ := json.Marshal(map[string]string{"private_key": bad})
		r := parseOne(t, "a.json", string(b))
		if len(values(r)) != 0 || len(r.Findings) != 1 || !r.Findings[0].Excluded {
			t.Fatal(r)
		}
	}
	r = parseOne(t, "a.json", `{"url":"postgresql://user:alpha%40beta%3A42@db.local/db"}`)
	if len(values(r)) != 2 || values(r)[0] != "alpha@beta:42" || r.Findings[0].Optional || !r.Findings[1].Optional {
		t.Fatal(r)
	}
	r = parseOne(t, "a.properties", `db.url=jdbc:mysql://db.local/app?user=operator&password=alpha%40beta%3A42`)
	if len(values(r)) != 2 || values(r)[0] != "alpha@beta:42" || !r.Findings[1].Optional {
		t.Fatal("JDBC URL 参数密码未提取", r)
	}
}

func TestConfigImportErrorsAreBoundedAndDoNotLeak(t *testing.T) {
	const secret = "must-not-appear-in-errors"
	cases := []File{
		{"a.json", `{"password":"good-value", "password":"` + secret},
		{"a.json", `{"password":"secret-\ud800"}`},
		{"a.json", `{"password":"secret-\udc00"}`},
		{"a.json", `{"password":"secret-\ud800\u0000"}`},
		{"a.yaml", "password: good-value\nx: [" + secret},
		{"a.yml", "x: &credential {password: good-value, self: *credential}"},
		{"a.yml", "password: !custom " + secret},
		{".env", "PASSWORD=good-value\nTOKEN=\"" + secret},
		{"a.properties", `password=good-value` + "\n" + `token=\uD800`},
		{"a.js", "password: " + secret},
		{"a.json", strings.Repeat("[", 70) + `"` + secret + `"` + strings.Repeat("]", 70)},
		{".env", "PASSWORD=" + string([]byte{0xff})},
	}
	for _, f := range cases {
		r := parseOne(t, f.Name, f.Content)
		if r.Error == "" || len(r.Findings) != 0 || strings.Contains(r.Error, secret) {
			t.Fatal(r)
		}
	}
	out, err := Parse(context.Background(), []File{{"good.json", `{"password":"good-value"}`}, cases[0]})
	if err != nil || len(out[0].Findings) != 1 || out[1].Error == "" {
		t.Fatal(out, err)
	}
	_, err = Parse(context.Background(), []File{{".env", strings.Repeat("a", MaxFileBytes+1)}})
	if err == nil {
		t.Fatal("超长文件被接受")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = Parse(ctx, []File{{"a.json", "{}"}}); err == nil {
		t.Fatal("取消无效")
	}
}

func FuzzConfigImport(f *testing.F) {
	for _, seed := range []string{`{"password":"example-password"}`, "PASSWORD='alpha9 beta42'", "password: |\n  alpha9beta42", `password=alpha\u0020beta42`, "a: &a {password: synthetic-secret}\nb: {<<: *a}", "a: &a [*a]"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > MaxFileBytes {
			t.Skip()
		}
		for _, name := range []string{"a.json", "a.yaml", ".env", "a.properties"} {
			r := parseOne(t, name, s)
			if r.Error != "" && len(r.Findings) != 0 {
				t.Fatal("错误文件留下部分候选")
			}
		}
	})
}
