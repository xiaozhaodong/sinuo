package configimport

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestConfigImportYAMLAliasesPreserveValueAndFieldContext(t *testing.T) {
	input := "plain: &value '  synthetic pass:42  '\npassword: *value\n" +
		"list: &list [first-synthetic-secret, second-synthetic-secret]\naccess_token: *list\n" +
		"defaults: &defaults {APIKey: synthetic-api-value}\ncopy: *defaults\n" +
		"field: &field client_secret\n*field: synthetic-client-secret\n" +
		"multi: &multi |\n  synthetic line1\n  line2\nprivate_key: *multi\n"
	r := parseOne(t, "a.yml", input)
	want := map[string]string{
		"文档1.password":        "  synthetic pass:42  ",
		"文档1.access_token[0]": "first-synthetic-secret", "文档1.access_token[1]": "second-synthetic-secret",
		"文档1.defaults.APIKey": "synthetic-api-value", "文档1.copy.APIKey": "synthetic-api-value",
		"文档1.client_secret": "synthetic-client-secret", "文档1.private_key": "synthetic line1\nline2\n",
	}
	got := make(map[string]string)
	for _, f := range r.Findings {
		if f.Excluded || f.Optional {
			t.Fatal("实际字符串不应被排除或降级", f.Path)
		}
		got[f.Path] = f.Value
	}
	if r.Error != "" || !reflect.DeepEqual(got, want) {
		t.Fatalf("别名值或来源字段不保真: error=%s got=%v", r.Error, got)
	}
}

func TestConfigImportYAMLMergePrecedence(t *testing.T) {
	input := `first: &first {password: first-password, api_key: first-api-value}
second: &second {password: second-password, access_token: second-token-value}
merged: &merged {<<: [*first, *second], client_secret: merged-client-secret}
result:
  <<: *merged
  password: explicit-password
before:
  password: before-password
  <<: [*second, *first]
inline: {<<: {password: inline-password}}
list: &list [*first, *second]
from_list: {<<: *list}
empty: {<<: []}
quoted: {"<<": ordinary-value, password: quoted-password}
`
	r := parseOne(t, "a.yaml", input)
	got := make(map[string]string)
	for _, f := range r.Findings {
		got[f.Path] = f.Value
	}
	for path, value := range map[string]string{
		"文档1.first.password": "first-password", "文档1.second.password": "second-password",
		"文档1.merged.password": "first-password", "文档1.merged.access_token": "second-token-value",
		"文档1.result.password": "explicit-password", "文档1.result.api_key": "first-api-value",
		"文档1.result.access_token": "second-token-value", "文档1.result.client_secret": "merged-client-secret",
		"文档1.before.password": "before-password", "文档1.before.api_key": "first-api-value",
		"文档1.inline.password": "inline-password", "文档1.from_list.password": "first-password",
		"文档1.quoted.password": "quoted-password",
	} {
		if r.Error != "" || got[path] != value {
			t.Fatal("merge 覆盖优先级或原始锚点候选错误", path, r.Error)
		}
	}
	for _, f := range r.Findings {
		if f.Path == "文档1.result.password" && f.Value != "explicit-password" {
			t.Fatal("被覆盖的值错误归属于目标字段")
		}
	}
}

func TestConfigImportYAMLStandardTagsAndDocuments(t *testing.T) {
	r := parseOne(t, "a.yml", "password: &a !!str 0123456789\ncopy_password: *a\ncount_password: !!int 12345678\n---\npassword: &a second-document-secret\ncopy_password: *a")
	if r.Error != "" || !reflect.DeepEqual(values(r), []string{"0123456789", "0123456789", "second-document-secret", "second-document-secret"}) || !r.Findings[2].Excluded {
		t.Fatal("显式字符串、非字符串分类或文档内锚点失败", r.Error)
	}
}

func TestConfigImportYAMLRejectsUnsafeOrAmbiguousReferences(t *testing.T) {
	for _, input := range []string{
		"a: &a {password: visible-secret, nested: *a}",
		"a: &a [*a]",
		"a: &a {<<: *a}",
		"a: &a {<<: *a, password: explicit-secret}",
		"a: *missing",
		"a: &a 1\n---\npassword: *a",
		"a: {<<: scalar}", "a: {<<: [1]}", "a: {<<: [[{}]]}",
		"a: {<<: {}, <<: {}}",
		"a: {<<: {password: !env SECRET_NAME}, password: explicit-secret}",
		"a: &a !include elsewhere.yml",
		"a: !<tag:example.com,2026:private> value",
		"a: !!set {password: null}",
		"a: !!unknown value",
		"? [a, b]\n: synthetic-secret",
	} {
		t.Run(input, func(t *testing.T) {
			out, err := Parse(context.Background(), []File{
				{Name: "bad.yml", Content: "password: must-not-leak\n" + input},
				{Name: "good.yml", Content: "password: still-valid-secret"},
			})
			if err != nil || out[0].Error == "" || len(out[0].Findings) != 0 || strings.Contains(out[0].Error, "must-not-leak") || len(out[1].Findings) != 1 {
				t.Fatal("错误文件不应留下部分候选或影响其他文件", err)
			}
		})
	}
}

func TestConfigImportYAMLAliasBudgets(t *testing.T) {
	for _, merge := range []bool{false, true} {
		var b strings.Builder
		b.WriteString("a0: &a0 {ordinary: synthetic-value}\n")
		for i := 1; i < 18; i++ {
			if merge {
				fmt.Fprintf(&b, "a%d: &a%d {<<: [*a%d, *a%d]}\n", i, i, i-1, i-1)
			} else {
				fmt.Fprintf(&b, "a%d: &a%d [*a%d, *a%d]\n", i, i, i-1, i-1)
			}
		}
		// 即使没有敏感字段或合并后只有一个键，每次展开也必须消耗节点预算。
		r := parseOne(t, "a.yml", b.String())
		if !strings.Contains(r.Error, "上限") || len(r.Findings) != 0 {
			t.Fatal("引用放大未被限制", merge, r.Error)
		}
	}
	for _, input := range []string{
		"a: &a " + strings.Repeat("[", 64) + "ordinary" + strings.Repeat("]", 64),
		"a: &a '" + strings.Repeat("postgres://h?password=", 60) + "synthetic-password'\nb: {c: {d: {password: *a}}}",
		"a: &a {password: synthetic-password}\nlist: [" + strings.Repeat("*a,", MaxValues) + "*a]",
	} {
		if r := parseOne(t, "a.yml", input); !strings.Contains(r.Error, "上限") || len(r.Findings) != 0 {
			t.Fatal("结构、URL 和引用的深度或候选未统一受限", r.Error)
		}
	}
}

func BenchmarkConfigImportYAMLAliases(b *testing.B) {
	input := "defaults: &d {password: synthetic-password, ordinary: visible}\nitems: [" + strings.Repeat("*d,", 999) + "*d]"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		out, err := Parse(context.Background(), []File{{Name: "a.yml", Content: input}})
		if err != nil || out[0].Error != "" || len(out[0].Findings) != 1001 {
			b.Fatal("千次别名解析失败", err)
		}
	}
}
