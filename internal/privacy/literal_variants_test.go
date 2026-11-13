package privacy

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func exactVariantRule(value string) Rule {
	rule := literalRule(-1, -1000, value, "[MASK]", ActionRedact)
	rule.Source = SourceExact
	return rule
}

func TestExactVariantsRejectOversizedDictionary(t *testing.T) {
	// 审核复现：原文 3.84 MB，低于导入原文上限，但派生词条约 89.9 MB。
	rules := make([]Rule, 64)
	for i := range rules {
		rules[i] = exactVariantRule(strings.Repeat("A+/:?&= ࠾࠿z", 4000) + fmt.Sprintf("%04d", i))
		rules[i].ID = int64(i + 1)
	}
	if compiled, err := CompileRules(rules); !errors.Is(err, ErrLiteralBudget) || compiled != nil {
		t.Fatal("编码词条超限应拒绝整个候选集合")
	}
	if _, err := CompileRule(exactVariantRule(strings.Repeat("x", maxExactLiteralBytes+1))); !errors.Is(err, ErrLiteralBudget) {
		t.Fatal("单条敏感值过大应在编码派生前拒绝")
	}
}

func TestLiteralPatternBudgetDeduplicatesWithoutDroppingRules(t *testing.T) {
	pattern := strings.Repeat("x", maxLiteralPatternBytes)
	rule := literalRule(1, 1, pattern, "[MASK]", ActionRedact)
	compiled, err := CompileRules([]Rule{rule, rule})
	if err != nil || len(compiled) != 2 {
		t.Fatal("重复词条不应重复占用派生预算或丢弃规则", err)
	}
	extra := literalRule(2, 2, "another", "[MASK]", ActionRedact)
	if _, err = CompileRules([]Rule{rule, extra}); !errors.Is(err, ErrLiteralBudget) {
		t.Fatal("超过总词条字节预算仍被接受")
	}
	b := literalPatternBuilder{patterns: make([]string, maxLiteralPatterns), indexes: make(map[string]int)}
	c, err := CompileRule(extra)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.add(&c); !errors.Is(err, ErrLiteralBudget) {
		t.Fatal("未限制词条数量")
	}
}

// 审核中的 60 KiB 原文复现，比较共享前缀计数前后的编译分配量。
func BenchmarkExactVariantSharedPrefix(b *testing.B) {
	rules := make([]Rule, 4)
	for i := range rules {
		rules[i] = exactVariantRule(strings.Repeat("A+/:?&= ࠾࠿z", 1000) + fmt.Sprintf("%04d", i))
		rules[i].ID = int64(i + 1)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := CompileRules(rules); err != nil {
			b.Fatal(err)
		}
	}
}

func TestExactLiteralPatternsSupportedForms(t *testing.T) {
	want := []string{
		"A+/:?&= Z!",
		"A%2B%2F%3A%3F%26%3D+Z%21", "A%2b%2f%3a%3f%26%3d+Z%21",
		"A%2B%2F%3A%3F%26%3D%20Z%21", "A%2b%2f%3a%3f%26%3d%20Z%21",
		"A+%2F:%3F&=%20Z%21", "A+%2f:%3f&=%20Z%21",
		"A+%2F%3A%3F&=%20Z%21", "A+%2f%3a%3f&=%20Z%21",
		"QSsvOj8mPSBaIQ==", "QSsvOj8mPSBaIQ",
	}
	if got := exactLiteralPatterns(want[0]); !reflect.DeepEqual(got, want) {
		t.Fatalf("编码形式不一致: got %q, want %q", got, want)
	}
	// 三字节 UTF-8 字符让标准 Base64 的 + 和 / 同时出现。
	for _, encoded := range []string{"4KC+4KC/YQ==", "4KC+4KC/YQ", "4KC-4KC_YQ==", "4KC-4KC_YQ"} {
		if !slices.Contains(exactLiteralPatterns("࠾࠿a"), encoded) {
			t.Fatal("缺少 Base64 形式", encoded)
		}
	}
}

func TestExactVariantsMatchOnceWithOriginalRuleIdentity(t *testing.T) {
	values := []string{"A+/:?&= Z!", "࠾࠿a", "  密码AbC9\n", "aaaa", "abcd"}
	for _, value := range values {
		rule := exactVariantRule(value)
		compiled, err := CompileRules([]Rule{rule})
		if err != nil {
			t.Fatal(err)
		}
		if len(compiled) != 1 || !reflect.DeepEqual(compiled[0].Rule, rule) {
			t.Fatal("编码形式不应增加规则或改变原规则")
		}
		for _, encoded := range exactLiteralPatterns(value) {
			for _, shared := range []bool{true, false} {
				c := compiled[0]
				if !shared {
					c.literals = nil // 单条 CompileRule 与共享自动机都必须支持变体。
				}
				s := &Snapshot{Settings: redactSettings(), Rules: []CompiledRule{c}}
				result := s.ApplyToText(claudeRequest(), "prefix|"+encoded+"|suffix")
				if string(result.Body) != "prefix|[MASK]|suffix" || result.HitCount != 1 || len(result.RuleHits) != 1 {
					t.Fatalf("匹配或填充未完整覆盖: encoded=%q shared=%t result=%+v", encoded, shared, result)
				}
				hit := result.RuleHits[0]
				if hit.RuleID != rule.ID || hit.Source != SourceExact || !reflect.DeepEqual(hit.Matches, []string{encoded}) {
					t.Fatal("变体应归属于原规则并记录实际命中文本", hit)
				}
			}
		}
	}
}

func TestExactVariantsRespectModeScopeAndCustomLiterals(t *testing.T) {
	rule := exactVariantRule("A+/:?&= Z!")
	encoded := "QSsvOj8mPSBaIQ=="
	body, _ := json.Marshal(map[string]any{"messages": []map[string]string{{"role": "user", "content": encoded}}})
	for _, mode := range []string{ModeDisabled, ModeDetect, ModeRedact} {
		settings := redactSettings()
		settings.Mode = mode
		s := newTestSnapshot(t, settings, rule)
		r, err := s.Apply(claudeRequest(), body)
		if err != nil || r.HitCount != map[string]int{ModeDisabled: 0, ModeDetect: 1, ModeRedact: 1}[mode] || r.Changed != (mode == ModeRedact) {
			t.Fatal("模式语义改变", mode, err, r)
		}
		if mode != ModeRedact && !bytes.Equal(r.Body, body) {
			t.Fatal("关闭或仅检测时修改了请求")
		}
	}
	for _, change := range []func(*Rule){
		func(r *Rule) { r.Enabled = false },
		func(r *Rule) { r.Source = SourceCustom },
		func(r *Rule) { r.Scope.EndpointNames = []string{"other-endpoint"} },
	} {
		changed := rule
		change(&changed)
		s := newTestSnapshot(t, redactSettings(), changed)
		if r := s.ApplyToText(claudeRequest(), encoded); r.Changed || r.HitCount != 0 {
			t.Fatal("变体绕过作用域或扩展到普通自定义字面量")
		}
	}
}

func TestExactVariantsCollisionsAndOverlaps(t *testing.T) {
	a := exactVariantRule("alpha9@ beta/")
	b := exactVariantRule(base64.StdEncoding.EncodeToString([]byte(a.Pattern)))
	b.ID, b.Priority, b.Placeholder = -2, -2000, "[SECOND]"
	s := newTestSnapshot(t, redactSettings(), a, b)
	if len(s.Rules) != 2 {
		t.Fatal("变体不应增加可见规则数")
	}
	set := s.Rules[0].literals
	if set == nil || set != s.Rules[1].literals {
		t.Fatal("未共用自动机")
	}
	unique := map[string]bool{}
	for _, pattern := range set.patterns {
		if unique[pattern] {
			t.Fatal("相同编码形式应共享一个自动机词条")
		}
		unique[pattern] = true
	}
	r := s.ApplyToText(claudeRequest(), b.Pattern)
	if string(r.Body) != "[SECOND]" || len(r.RuleHits) != 1 || r.RuleHits[0].RuleID != b.ID {
		t.Fatal("编码形式与另一原值重合时未遵守原优先级", r)
	}
}

func TestExactVariantsDoNotDecodeContainersOrOpaqueFields(t *testing.T) {
	rule := exactVariantRule("a@ b/9Z?")
	s := newTestSnapshot(t, redactSettings(), rule)
	for _, text := range []string{
		url.QueryEscape(url.QueryEscape(rule.Pattern)),
		base64.StdEncoding.EncodeToString([]byte("user:" + rule.Pattern)),
		base64.StdEncoding.EncodeToString([]byte(`{"password":"` + rule.Pattern + `"}`)),
	} {
		if r := s.ApplyToText(claudeRequest(), text); r.Changed {
			t.Fatal("不应递归解码或解析组合容器", text)
		}
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(rule.Pattern))
	for _, restore := range []bool{false, true} {
		s.Settings.ResponseRestoreEnabled = restore
		s.RestoreKey = bytes.Repeat([]byte{1}, 32)
		body, _ := json.Marshal(map[string]any{"messages": []any{map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "image", "source": map[string]string{"type": "base64", "media_type": "image/png", "data": encoded}},
			map[string]string{"type": "thinking", "thinking": "ordinary", "signature": encoded},
		}}}})
		r, err := s.Apply(claudeRequest(), body)
		if err != nil || !bytes.Equal(r.Body, body) {
			t.Fatal("变体匹配不应扩大协议字段扫描范围", err)
		}
		if r.Restore != nil {
			r.Restore.Release()
		}
	}
}

func TestExactVariantsSharedScannerMatchesSequential(t *testing.T) {
	rng := rand.New(rand.NewSource(927))
	for round := range 300 {
		var rules []Rule
		var inputs []string
		for i := range 8 {
			value := randomAlphabetString(rng, "ab +/?=%", 4, 18)
			r := exactVariantRule(value)
			r.ID, r.Priority = int64(i+1), i
			rules = append(rules, r)
			forms := exactLiteralPatterns(value)
			inputs = append(inputs, forms[rng.Intn(len(forms))])
		}
		s := newTestSnapshot(t, redactSettings(), rules...)
		text := strings.Join(inputs, "|") + randomAlphabetString(rng, "ab +/?=%", 0, 100)
		shared := s.scanText(s.candidateRules(claudeRequest(), nil), text)
		for i := range s.Rules {
			s.Rules[i].literals = nil
		}
		if sequential := s.scanText(s.candidateRules(claudeRequest(), nil), text); !reflect.DeepEqual(shared, sequential) {
			t.Fatalf("共享与逐条匹配不一致，round=%d", round)
		}
	}
}

func FuzzExactLiteralPatterns(f *testing.F) {
	for _, seed := range []string{"A+/:?&= Z!", "࠾࠿a", "AbC\n密钥", "aaaa", "%2FA+"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if value == "" || len(value) > 4096 {
			t.Skip()
		}
		patterns := exactLiteralPatterns(value)
		if len(patterns) > 13 || patterns[0] != value {
			t.Fatal("编码派生数量无界或原值改变")
		}
		for i, pattern := range patterns {
			if slices.Contains(patterns[:i], pattern) {
				t.Fatal("编码形式重复")
			}
			if pattern == value {
				continue
			}
			valid := false
			for _, decode := range []func(string) (string, error){url.QueryUnescape, url.PathUnescape} {
				decoded, err := decode(pattern)
				valid = valid || err == nil && decoded == value
			}
			for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
				decoded, err := encoding.DecodeString(pattern)
				valid = valid || err == nil && string(decoded) == value
			}
			if !valid {
				t.Fatal("变体无法准确还原为登记值")
			}
		}
	})
}

// 含保留字符和非 ASCII 值，测量编码扩展较多时的编译、冷热扫描开销。
func BenchmarkExactEncodedDictionary(b *testing.B) {
	for _, n := range []int{1000, 10000} {
		for _, encoded := range []bool{false, true} {
			b.Run(fmt.Sprintf("entries=%d/variants=%t", n, encoded), func(b *testing.B) {
				rules := make([]Rule, n)
				for i := range rules {
					rules[i] = exactVariantRule(fmt.Sprintf("pass-%08d+A@/?=࠾࠿ Z", i))
					rules[i].ID = int64(i + 1)
					if !encoded {
						rules[i].Source = SourceCustom
					}
				}
				b.Run("compile", func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						if _, err := CompileRules(rules); err != nil {
							b.Fatal(err)
						}
					}
				})
				compiled, err := CompileRules(rules)
				if err != nil {
					b.Fatal(err)
				}
				text := strings.Repeat("ordinary code and prose; ", 700) + rules[n-1].Pattern
				if encoded {
					text += "|" + url.QueryEscape(rules[n-1].Pattern)
				}
				body, _ := json.Marshal(map[string]any{"messages": []map[string]string{{"role": "user", "content": text}}})
				for _, warm := range []bool{false, true} {
					b.Run(fmt.Sprintf("scan-cached=%t", warm), func(b *testing.B) {
						s := Snapshot{Settings: redactSettings(), Rules: compiled}
						if warm {
							s.Cache = NewSegmentCache(16)
						}
						b.ReportAllocs()
						for b.Loop() {
							if _, err := s.Apply(claudeRequest(), body); err != nil {
								b.Fatal(err)
							}
						}
					})
				}
			})
		}
	}
}
