package privacy

import (
	"math/rand"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func prefilterLiterals(t *testing.T, pattern string) ([]string, bool) {
	t.Helper()
	prefilter := buildRulePrefilter(pattern)
	if prefilter == nil {
		return nil, false
	}
	literals := append([]string(nil), prefilter.literals...)
	sort.Strings(literals)
	return literals, prefilter.fold
}

func TestBuildRulePrefilterExtractsRequiredLiterals(t *testing.T) {
	cases := []struct {
		pattern string
		want    []string
		fold    bool
	}{
		{`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`, []string{"AKIA", "ASIA"}, false},
		{`\bAIza[0-9A-Za-z_-]{35}\b`, []string{"AIza"}, false},
		{`(?i)\bBearer\s+(?P<redact>[A-Za-z0-9._~+/=-]{20,})`, []string{"bearer"}, true},
		{`(?i)\b(?:postgres(?:ql)?|mysql|redis)://[^:'"\s/@]+:[^@'"\s]+@[^'"\s]+`, []string{"mysql", "postgres", "redis"}, true},
		{`\b\d{8,10}:[A-Za-z0-9_-]{35}\b`, []string{":"}, false},
		{`(?i)(?:password|passwd|pwd|passphrase|密码|口令)['"]?\s*[:=：]\s*(?P<redact>\S{6,})`, []string{"passphrase", "passwd", "password", "pwd", "口令", "密码"}, true},
		{`.*secret`, []string{"secret"}, false},
	}
	for _, tc := range cases {
		got, fold := prefilterLiterals(t, tc.pattern)
		sort.Strings(tc.want)
		if !reflect.DeepEqual(got, tc.want) || fold != tc.fold {
			t.Errorf("%s: literals=%v fold=%v, want %v fold=%v", tc.pattern, got, fold, tc.want, tc.fold)
		}
	}
	for _, pattern := range []string{`[a-z]{10}`, `\d{13,19}`, `(?:abc)?def|x*`} {
		if prefilter := buildRulePrefilter(pattern); prefilter != nil {
			t.Errorf("%s: expected no prefilter, got %v", pattern, prefilter.literals)
		}
	}
}

// 预设与典型自定义正则：凡是正则能命中的文本，预过滤都必须放行（只能多放，不能漏）
func TestRulePrefilterNeverRejectsActualMatch(t *testing.T) {
	var patterns []string
	for _, preset := range Presets() {
		for _, rule := range preset.Rules {
			if rule.MatchType == MatchTypeRegex {
				patterns = append(patterns, rule.Pattern)
			}
		}
	}
	patterns = append(patterns, `(?i)key|kelvin`, `(?i)ss+h`, `中文(?:密钥|口令)`)
	samples := []string{
		"password = hunter2hunter", "PASSWD: abcdef12", "DB_PASSWORD=Xy9!Xy9!", "密码：中文Secret123456",
		"OPENAI_API_KEY=Zx9Qw8Er7Ty6Ui5Op4", `"apiKey": "Zx9Qw8Er7Ty6Ui5Op4"`, "export GITHUB_TOKEN=Zx9Qw8Er7Ty6Ui5Op4As3",
		"AKIAABCDEFGHIJKLMNOP", "AIza" + strings.Repeat("b", 35), "authorization: bearer abcdefghijklmnopqrstu",
		"postgres://u:p@h", "123456789:" + strings.Repeat("A", 35), "eyJabcdefghijk.abcdefghijk.abcde",
		"-----BEGIN RSA PRIVATE KEY-----", "10.0.0.1", "xoxb-1234567890", "hf_" + strings.Repeat("a", 20),
		"paſſword=abcdefgh", "Kelvin", "SſH", "中文口令",
	}
	r := rand.New(rand.NewSource(3))
	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		prefilter := buildRulePrefilter(pattern)
		for _, sample := range samples {
			text := randomAlphabetString(r, "xyz \n", 0, 6) + sample + randomAlphabetString(r, "xyz \n", 0, 6)
			if re.MatchString(text) && !prefilter.mayMatch(&segmentText{text: text}) {
				t.Fatalf("prefilter of %s rejected matching text %q", pattern, text)
			}
		}
	}
}

func TestRulePrefilterSkipsUnrelatedText(t *testing.T) {
	prefilter := buildRulePrefilter(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)
	if prefilter.mayMatch(&segmentText{text: strings.Repeat("func main() { fmt.Println(1) }\n", 100)}) {
		t.Fatal("text without AKIA/ASIA should be skipped")
	}
	fold := buildRulePrefilter(`(?i)bearer\s+\S+`)
	if !fold.mayMatch(&segmentText{text: "Authorization: BEARER abc"}) {
		t.Fatal("case-insensitive literal should match uppercase text")
	}
}

// randomRegex 随机拼出含大小写折叠、字符类、量词、分组与零宽断言的正则
func randomRegex(r *rand.Rand, depth int) string {
	atoms := []string{"a", "b", "ab", "k", "s", "K", "ſ", ":", "[ab]", "[kK]", "[:=]", `\b`, "^", "$", "."}
	if depth <= 0 {
		return atoms[r.Intn(len(atoms))]
	}
	switch r.Intn(8) {
	case 0:
		return randomRegex(r, depth-1) + randomRegex(r, depth-1)
	case 1:
		return "(?:" + randomRegex(r, depth-1) + "|" + randomRegex(r, depth-1) + ")"
	case 2:
		return "(?i:" + randomRegex(r, depth-1) + ")"
	case 3:
		return "(?:" + randomRegex(r, depth-1) + ")" + []string{"?", "+", "*", "{2}", "{1,3}", "{0,2}"}[r.Intn(6)]
	case 4:
		return "(" + randomRegex(r, depth-1) + ")"
	default:
		return randomRegex(r, depth-1) + randomRegex(r, depth-1) + randomRegex(r, depth-1)
	}
}

func TestRulePrefilterSoundOnRandomRegexes(t *testing.T) {
	r := rand.New(rand.NewSource(11))
	checked := 0
	for range 4000 {
		pattern := randomRegex(r, 3)
		re, err := regexp.Compile(pattern)
		if err != nil {
			continue
		}
		prefilter := buildRulePrefilter(pattern)
		if prefilter == nil {
			continue
		}
		for range 60 {
			text := randomAlphabetString(r, "abABkKsS: \nſK", 0, 12)
			if re.MatchString(text) {
				checked++
				if !prefilter.mayMatch(&segmentText{text: text}) {
					t.Fatalf("prefilter %v (fold=%v) of %q rejected matching text %q", prefilter.literals, prefilter.fold, pattern, text)
				}
			}
		}
	}
	if checked < 1000 {
		t.Fatalf("too few matching samples: %d", checked)
	}
}

func TestPresetFieldRulesAreLineScoped(t *testing.T) {
	scoped := map[string]bool{}
	for _, preset := range Presets() {
		for _, rule := range preset.Rules {
			if rule.MatchType != MatchTypeRegex {
				continue
			}
			compiled, err := CompileRule(rule)
			if err != nil {
				t.Fatal(err)
			}
			scoped[rule.Name] = compiled.lineScoped
		}
	}
	for _, name := range []string{"密码字段", "通用密钥字段", "高德地图 Key", "Bearer Token", "含凭据连接串", "Telegram Bot Token", "IPv4 地址"} {
		if !scoped[name] {
			t.Errorf("%s should be line scoped", name)
		}
	}
	for _, pattern := range []string{`a\sb`, `(?s)a.b`, `^token`, `token$`, "a\nb"} {
		compiled, err := CompileRule(Rule{Name: "x", Pattern: pattern, MatchType: MatchTypeRegex, Action: ActionDetect})
		if err != nil {
			t.Fatal(err)
		}
		if compiled.lineScoped {
			t.Errorf("%q may span lines or anchor to text edges; must not be line scoped", pattern)
		}
	}
}

// 按行开窗与全文扫描逐项一致：预设正则 + 随机正则，在随机多行文本上比对
func TestLineScopedMatchingEqualsWholeText(t *testing.T) {
	r := rand.New(rand.NewSource(17))
	var rules []CompiledRule
	for _, preset := range Presets() {
		for _, rule := range preset.Rules {
			if rule.MatchType == MatchTypeRegex {
				compiled, _ := CompileRule(rule)
				rules = append(rules, compiled)
			}
		}
	}
	for len(rules) < 400 {
		pattern := "(?m)" + randomRegex(r, 3)
		compiled, err := CompileRule(Rule{Name: "r", Pattern: pattern, MatchType: MatchTypeRegex, Action: ActionDetect})
		if err == nil && compiled.lineScoped {
			rules = append(rules, compiled)
		}
	}
	snippets := []string{
		"password = hunter2hunter", "OPENAI_API_KEY=Zx9Qw8Er7Ty6Ui5Op4", "token: abcdefghijklmnop\r", "Bearer abcdefghijklmnopqrstuvwx",
		"postgres://u:p@h/db", "123456789:" + strings.Repeat("A", 35), "10.0.0.1", "ab", "ks", "K", "ſ", ":", "", " ",
	}
	compared := 0
	for range 3000 {
		var b strings.Builder
		for range 1 + r.Intn(6) {
			b.WriteString(randomAlphabetString(r, "abkKs: .", 0, 5))
			b.WriteString(snippets[r.Intn(len(snippets))])
			b.WriteString([]string{"\n", " ", "", "\n\n", "\r\n"}[r.Intn(5)])
		}
		text := b.String()
		for i := range rules {
			rule := &rules[i]
			seg := &segmentText{text: text}
			if !rule.prefilter.mayMatch(seg) {
				continue
			}
			got := findRegexMatchesInLiteralLines(rule, seg)
			want := findRegexMatches(rule, text)
			if len(got) == 0 && len(want) == 0 {
				continue
			}
			compared++
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("pattern %q on %q: lines=%v whole=%v", rule.Pattern, text, got, want)
			}
		}
	}
	if compared < 1000 {
		t.Fatalf("too few non-empty comparisons: %d", compared)
	}
}
