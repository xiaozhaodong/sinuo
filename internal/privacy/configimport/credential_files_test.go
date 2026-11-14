package configimport

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
)

func TestCredentialFilesFormats(t *testing.T) {
	password := "synthetic secret+#42"
	for _, tc := range []struct {
		name, content string
		want          []string
	}{
		{"credentials", "[default]\naws_access_key_id=synthetic-access-key\naws_secret_access_key=synthetic-secret-key\naws_session_token=synthetic-session-token\n[second]\naws_secret_access_key=second-secret-key", []string{"synthetic-access-key", "synthetic-secret-key", "synthetic-session-token", "second-secret-key"}},
		{".npmrc", "# comment\nregistry=https://registry.example\n//registry.example:4873/path/:_authToken=synthetic-token-123456\n//registry.example/:_password=" + base64.StdEncoding.EncodeToString([]byte(password)), []string{"synthetic-token-123456", password}},
		{"npmrc", "_auth=" + base64.StdEncoding.EncodeToString([]byte("someone:"+password)), []string{password, "someone:" + password}},
		{".netrc", "# comment\nmachine example.invalid login sample password \"synthetic secret+#42\"\ndefault login anonymous password another-password", []string{"sample", password, "anonymous", "another-password"}},
		{"_netrc", "machine example.invalid password \"synthetic\\tvalue\\nnext\\\"end\"", []string{"synthetic\tvalue\nnext\"end"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := Parse(context.Background(), []File{{Name: tc.name, Content: tc.content}})
			if err != nil || out[0].Error != "" {
				t.Fatal(err, out)
			}
			var actual []string
			for _, f := range out[0].Findings {
				if !f.Excluded {
					actual = append(actual, f.Value)
				}
			}
			if strings.Join(actual, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("unexpected findings: %#v", actual)
			}
		})
	}
}

func TestCredentialFilesRejectPartialAndReferences(t *testing.T) {
	for _, tc := range []File{
		{Name: "credentials", Content: "[default]\naws_secret_access_key=first-secret-value\ninvalid"},
		{Name: ".npmrc", Content: "_authToken=first-secret-value\n_password=not-valid-base64!"},
		{Name: ".netrc", Content: "machine a password first-secret-value\nmacdef init\npassword fake-secret-value"},
		{Name: ".netrc", Content: "machine a password \"unclosed-secret-value"},
	} {
		out, err := Parse(context.Background(), []File{tc})
		if err != nil || out[0].Error == "" || len(out[0].Findings) != 0 || strings.Contains(out[0].Error, "secret-value") {
			t.Fatal("部分解析或错误原文泄漏", out, err)
		}
	}
	out, err := Parse(context.Background(), []File{{Name: ".npmrc", Content: "_authToken=${NPM_TOKEN}\nkeyfile=/private/key.pem\n_password=${NPM_PASSWORD}"}})
	if err != nil || len(out[0].Findings) != 3 {
		t.Fatal(out, err)
	}
	for _, f := range out[0].Findings {
		if !f.Excluded || f.Value != "" {
			t.Fatal("引用不应作为实际值", f)
		}
	}
}

func TestCredentialFilesNpmBooleanShorthand(t *testing.T) {
	const token = "synthetic-npm-boolean-token"
	content := "strict-ssl\nfund ; comment\n_password\n//registry.example/:_authToken=" + token
	out, err := Parse(context.Background(), []File{{Name: ".npmrc", Content: content}})
	if err != nil || out[0].Error != "" || len(out[0].Findings) != 1 || out[0].Findings[0].Value != token {
		t.Fatalf("npm 布尔配置不应阻止凭据提取或被当作字符串凭据: %+v, %v", out, err)
	}
	// 无等号语义只属于 npm/ini，不能放宽 AWS 凭据文件的语法校验。
	out, err = Parse(context.Background(), []File{{Name: "credentials", Content: "[default]\naws_secret_access_key=" + token + "\nstrict-ssl"}})
	if err != nil || out[0].Error == "" || len(out[0].Findings) != 0 {
		t.Fatalf("AWS 坏文件应继续整文件拒绝: %+v, %v", out, err)
	}
}
