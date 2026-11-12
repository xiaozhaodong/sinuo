package configimport

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// 仅生成固定结构的合成值，不使用真实凭据，也不因低熵或 test 前缀跳过。
func TestConfigImportSelectedCredentialFormats(t *testing.T) {
	cases := []struct{ name, value string }{
		{"npm", "npm_" + strings.Repeat("A", 36)},
		{"SendGrid", "SG." + strings.Repeat("A", 21) + "_." + strings.Repeat("b", 42) + "-"},
		{"GitHub fine-grained", "github_pat_" + strings.Repeat("A_", 41)},
		{"OpenAI legacy", "sk-" + strings.Repeat("A", 20) + "T3BlbkFJ" + strings.Repeat("b", 20)},
		{"GitLab registration", "GR1348941" + strings.Repeat("A", 20)},
	}
	for _, prefix := range []string{"sk_live_", "rk_live_", "sk_test_", "rk_test_", "sk_prod_", "rk_prod_"} {
		for _, n := range []int{10, 99} {
			cases = append(cases, struct{ name, value string }{fmt.Sprintf("%s%d", prefix, n), prefix + strings.Repeat("A", n)})
		}
	}
	for _, prefix := range []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_", "glpat-", "gldt-", "glrt-"} {
		n := 36
		if strings.HasPrefix(prefix, "gl") {
			n = 20
		}
		cases = append(cases, struct{ name, value string }{prefix, prefix + strings.Repeat("B", n)})
	}
	for _, prefix := range []string{"glpat-", "glrt-t1_"} {
		for _, n := range []int{27, 300} {
			cases = append(cases, struct{ name, value string }{fmt.Sprintf("%s%d", prefix, n), prefix + strings.Repeat("A", n-1) + "-.ab1234567"})
		}
	}
	for _, prefix := range []string{"proj", "svcacct", "admin"} {
		for _, left := range []int{58, 74} {
			for _, right := range []int{58, 74} {
				cases = append(cases, struct{ name, value string }{fmt.Sprintf("OpenAI %s %d/%d", prefix, left, right), "sk-" + prefix + "-" + strings.Repeat("A", left-1) + "_T3BlbkFJ" + strings.Repeat("b", right-1) + "-"})
			}
		}
	}
	for _, prefix := range []string{"api03", "admin01"} {
		cases = append(cases, struct{ name, value string }{"Anthropic " + prefix, "sk-ant-" + prefix + "-" + strings.Repeat("A", 91) + "_-AA"})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, surrounding := range []string{"%s", "credential=%s", "Bearer %s", "[%s]", "\"%s\""} {
				raw, _ := json.Marshal(map[string]string{"ordinary": fmt.Sprintf(surrounding, tc.value)})
				r := parseOne(t, "a.json", string(raw))
				if r.Error != "" || len(values(r)) != 1 || values(r)[0] != tc.value || r.Findings[0].Optional {
					t.Fatal("普通字段中未完整识别所选格式", r.Error)
				}
			}
			for _, bad := range []string{"x" + tc.value, "prefix/" + tc.value, tc.value + "_trailing", tc.value + strings.Repeat("x", 301), tc.value + ".tail", tc.value + "=", tc.value + "/tail"} {
				raw, _ := json.Marshal(map[string]string{"ordinary": bad})
				if r := parseOne(t, "a.json", string(raw)); r.Error != "" || len(values(r)) != 0 {
					t.Fatal("不能把较长凭据的部分内容当成完整匹配", r.Error)
				}
			}
		})
	}
}

func TestConfigImportCredentialInvalidShapes(t *testing.T) {
	for _, value := range []string{
		"SG." + strings.Repeat("a", 66),
		"SG." + strings.Repeat("a", 21) + "." + strings.Repeat("b", 44),
		"SG." + strings.Repeat("a", 22) + "." + strings.Repeat("b", 42),
		"SG." + strings.Repeat("a", 22) + "." + strings.Repeat("b", 44),
		"SG." + strings.Repeat("a", 22) + "." + strings.Repeat("b", 42) + "+",
		"github_pat_" + strings.Repeat("a", 81), "github_pat_" + strings.Repeat("a", 83),
		"sk-ant-api03-" + strings.Repeat("a", 93) + "BB",
		"sk-ant-api03-" + strings.Repeat("a", 92) + "AA",
		"sk-proj-" + strings.Repeat("a", 74) + "WRONGSIG" + strings.Repeat("b", 74),
		"sk-" + strings.Repeat("a", 19) + "T3BlbkFJ" + strings.Repeat("b", 20),
		"glpat-" + strings.Repeat("a", 26) + ".ab1234567",
		"glrt-tx_" + strings.Repeat("a", 27) + ".ab1234567",
		"glpat-" + strings.Repeat("a", 27) + ".AB1234567",
		"GR1348941" + strings.Repeat("a", 19),
		"npm_" + strings.Repeat("a", 35), "sk_test_" + strings.Repeat("a", 9),
		"pk_live_" + strings.Repeat("a", 36), "pk_test_" + strings.Repeat("a", 36),
		"3b01519c-819a-4983-b1dc-601deafaf946", strings.Repeat("0123456789abcdef", 4),
	} {
		raw, _ := json.Marshal(map[string]string{"ordinary": value})
		if r := parseOne(t, "a.json", string(raw)); r.Error != "" || len(values(r)) != 0 {
			t.Fatal("不应识别错误格式、公开凭据或普通标识", value, r.Error)
		}
	}
	// 陌生格式在明确敏感字段中仍按实际字符串提取，不受厂商格式集限制。
	r := parseOne(t, "a.json", `{"api_key":"unknown-vendor-synthetic-secret"}`)
	if r.Error != "" || len(values(r)) != 1 {
		t.Fatal("专用格式不应限制字段语义提取", r.Error)
	}
}

func TestConfigImportAllSelectedPrivateKeyLabels(t *testing.T) {
	for _, label := range []string{"PRIVATE KEY", "RSA PRIVATE KEY", "EC PRIVATE KEY", "OPENSSH PRIVATE KEY", "ENCRYPTED PRIVATE KEY", "PUBLIC KEY", "CERTIFICATE"} {
		value := "-----BEGIN " + label + "-----\nYWJjZGVmZ2hpamts\n-----END " + label + "-----\n"
		raw, _ := json.Marshal(map[string]string{"ordinary": value})
		r := parseOne(t, "a.json", string(raw))
		want := 1
		if label == "PUBLIC KEY" || label == "CERTIFICATE" {
			want = 0
		}
		if r.Error != "" || len(values(r)) != want || (want == 1 && values(r)[0] != value) {
			t.Fatal("私钥标签覆盖或完整值提取错误", label, r.Error)
		}
	}
}
