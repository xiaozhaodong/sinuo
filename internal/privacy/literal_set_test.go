package privacy

import (
	"errors"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

func testLiteralSet(t *testing.T, patterns []string) *literalSet {
	t.Helper()
	set, err := newLiteralSet(patterns)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func randomAlphabetString(r *rand.Rand, alphabet string, minLen, maxLen int) string {
	var b strings.Builder
	for range minLen + r.Intn(maxLen-minLen+1) {
		b.WriteByte(alphabet[r.Intn(len(alphabet))])
	}
	return b.String()
}

// 小字母表制造大量共享前缀、互为子串与重叠出现，确认结果与逐个 strings.Index 完全一致
func TestLiteralSetMatchesSequentialIndex(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	for round := range 3000 {
		patterns := make([]string, minLiteralSetPatterns+r.Intn(6))
		for i := range patterns {
			patterns[i] = randomAlphabetString(r, "abc", 1, 5)
		}
		set := testLiteralSet(t, patterns)
		if set == nil {
			t.Fatalf("round %d: set not built", round)
		}
		text := randomAlphabetString(r, "abcd", 0, 80)
		starts := set.findAll(text)
		for i, pattern := range patterns {
			var occurrences []int
			if starts != nil {
				occurrences = starts[i]
			}
			got := nonOverlappingOccurrences(occurrences, len(pattern))
			want := findLiteralMatches(pattern, text)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("round %d pattern %q text %q: got %v, want %v", round, pattern, text, got, want)
			}
		}
	}
}

func TestLiteralSetUsesSparseRepresentationForLargeDictionaries(t *testing.T) {
	if testLiteralSet(t, []string{"a", "b"}) != nil {
		t.Fatal("too few patterns should fall back to strings.Index")
	}
	huge := make([]string, 8)
	for i := range huge {
		huge[i] = strings.Repeat(string(rune('a'+i)), maxLiteralSetTableEntries/16)
	}
	set := testLiteralSet(t, huge)
	if set == nil || set.sparse == nil || len(set.trans) != 0 {
		t.Fatal("大词典应使用紧凑自动机")
	}
	if starts := set.findAll("prefix " + huge[3] + " suffix"); len(starts) != len(huge) || !reflect.DeepEqual(starts[3], []int{7}) {
		t.Fatal("大字面量匹配不完整")
	}
}

func TestSparseLiteralSetMatchesDenseAndSequential(t *testing.T) {
	r := rand.New(rand.NewSource(123))
	for round := 0; round < 3000; round++ {
		patterns := []string{"a", "aa", "aaa", "aa", "baaa", "中文", "文"}
		for i := 0; i < 10; i++ {
			patterns = append(patterns, randomAlphabetString(r, "abc", 1, 12))
		}
		text := "中文" + randomAlphabetString(r, "abcd", 0, 250) + "aaaa"
		dense := testLiteralSet(t, patterns)
		sparse := &literalSet{patterns: patterns}
		total := 1
		for _, p := range patterns {
			total += len(p)
		}
		sparse.buildSparse(total)
		got, want := sparse.findAll(text), dense.findAll(text)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("紧凑与稠密自动机不一致，round=%d", round)
		}
		for i, p := range patterns {
			var matches []int
			if got != nil {
				matches = got[i]
			}
			if !reflect.DeepEqual(nonOverlappingOccurrences(matches, len(p)), findLiteralMatches(p, text)) {
				t.Fatal("紧凑自动机与字面查找不一致", round, i)
			}
		}
	}
}

func TestLiteralSetBuildBudgetAndSharedPrefixCapacity(t *testing.T) {
	// 输入词条总长在 8 MiB 内，但没有共享前缀，节点数组超过 128 MiB。
	patterns := make([]string, 8)
	for i := range patterns {
		patterns[i] = strings.Repeat(string(rune('a'+i)), 600000)
	}
	if set, err := newLiteralSet(patterns); !errors.Is(err, ErrLiteralBudget) || set != nil {
		t.Fatal("构建预算超限应在分配前报错")
	}
	// 长公共前缀不应为每条词重复预分配状态。
	prefix := strings.Repeat("shared-prefix-", 20000)
	for i := range patterns {
		patterns[i] = prefix + string(rune('a'+i))
	}
	set := testLiteralSet(t, patterns)
	if set == nil || set.sparse == nil {
		t.Fatal("长词条应使用紧凑自动机")
	}
	want := 1 + len(prefix) + len(patterns)
	if len(set.sparse.nodes) != want || cap(set.sparse.nodes) != want || cap(set.sparse.edges) != want-1 {
		t.Fatalf("未按共享后的实际容量构建: nodes=%d/%d want=%d", len(set.sparse.nodes), cap(set.sparse.nodes), want)
	}
	starts := set.findAll("!" + patterns[3] + "!")
	if len(starts) != len(patterns) || !reflect.DeepEqual(starts[3], []int{1}) {
		t.Fatal("共享前缀预算改变了匹配结果")
	}
}

// 精确敏感值走共享自动机时，优先级与重叠语义和逐条查找一致：更长的值先占位
func TestApplyExactLiteralsViaSharedSet(t *testing.T) {
	values := []string{"cr_secret_long_value_0001", "cr_secret_long", "token-alpha-9", "token-beta-7", "hunter2hunter"}
	var rules []Rule
	for i, value := range values {
		rules = append(rules, Rule{
			ID: int64(i + 1), Enabled: true, Name: "exact", Priority: -1000 + i,
			MatchType: MatchTypeLiteral, Pattern: value, Placeholder: "[敏感值]", Action: ActionRedact, Source: SourceExact,
		})
	}
	snapshot := newTestSnapshot(t, redactSettings(), rules...)
	if snapshot.Rules[0].literals == nil {
		t.Fatal("literal rules should share an automaton")
	}
	text := "a cr_secret_long_value_0001 b cr_secret_long c token-alpha-9token-beta-7 hunter2hunter2hunter"
	result := snapshot.ApplyToText(claudeRequest(), text)
	want := "a [敏感值] b [敏感值] c [敏感值][敏感值] [敏感值]2hunter"
	if string(result.Body) != want {
		t.Fatalf("body = %q, want %q", result.Body, want)
	}
	if result.HitCount != 5 {
		t.Fatalf("hit count = %d, want 5", result.HitCount)
	}
}
