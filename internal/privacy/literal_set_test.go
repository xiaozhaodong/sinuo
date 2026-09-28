package privacy

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

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
		set := newLiteralSet(patterns)
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

func TestLiteralSetFallsBackWhenTooFewOrTooLarge(t *testing.T) {
	if newLiteralSet([]string{"a", "b"}) != nil {
		t.Fatal("too few patterns should fall back to strings.Index")
	}
	huge := make([]string, 8)
	for i := range huge {
		huge[i] = strings.Repeat(string(rune('a'+i)), maxLiteralSetTableEntries/4)
	}
	if newLiteralSet(huge) != nil {
		t.Fatal("oversized table should fall back to strings.Index")
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
