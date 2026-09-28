package privacy

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func claudeMessagesBody(t *testing.T, contents ...string) []byte {
	t.Helper()
	messages := make([]map[string]string, 0, len(contents))
	for _, content := range contents {
		messages = append(messages, map[string]string{"role": "user", "content": content})
	}
	body, err := json.Marshal(map[string]any{"model": "m", "messages": messages})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func literalRule(id int64, priority int, pattern, placeholder, action string) Rule {
	return Rule{ID: id, Enabled: true, Name: pattern, Priority: priority, MatchType: MatchTypeLiteral,
		Pattern: pattern, Placeholder: placeholder, Action: action}
}

// 低优先级命中与高优先级部分重叠时，旧逻辑整段丢弃会让密钥尾部原样发出；现在脱敏区域取并集
func TestScanRedactsUnionOfOverlappingMatches(t *testing.T) {
	high := literalRule(1, 10, "ABCDEF12", "[A]", ActionRedact)
	low := Rule{ID: 2, Enabled: true, Name: "digits", Priority: 20, MatchType: MatchTypeRegex,
		Pattern: `\d{6}`, Placeholder: "[B]", Action: ActionRedact}
	snapshot := newTestSnapshot(t, redactSettings(), high, low)
	result := snapshot.ApplyToText(claudeRequest(), "key=ABCDEF123456 end")
	if string(result.Body) != "key=[A] end" {
		t.Fatalf("body = %q, want union redacted with higher priority placeholder", result.Body)
	}
	if result.HitCount != 1 || result.RuleHits[0].RuleID != 1 {
		t.Fatalf("hit accounting should keep priority semantics: %+v", result.RuleHits)
	}
}

func TestScanDetectRuleDoesNotShieldRedactRule(t *testing.T) {
	detect := literalRule(1, 10, "secret-token-xyz", "", ActionDetect)
	redact := literalRule(2, 20, "token-xyz", "[R]", ActionRedact)
	snapshot := newTestSnapshot(t, redactSettings(), detect, redact)
	result, err := snapshot.Apply(claudeRequest(), claudeMessagesBody(t, "a secret-token-xyz b"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(result.Body, []byte("a secret-[R] b")) {
		t.Fatalf("redact rule must still apply under an overlapping detect rule: %s", result.Body)
	}
}

func TestScanAdjacentMatchesKeepSeparatePlaceholders(t *testing.T) {
	snapshot := newTestSnapshot(t, redactSettings(),
		literalRule(1, 10, "AAAA", "[A]", ActionRedact), literalRule(2, 20, "BBBB", "[B]", ActionRedact))
	result := snapshot.ApplyToText(claudeRequest(), "AAAABBBB")
	if string(result.Body) != "[A][B]" {
		t.Fatalf("body = %q", result.Body)
	}
}

// 超出扫描上限时优先扫描最新内容：新贴入的敏感信息不能因为历史太长而漏脱敏
func TestScanPrefixPrefersNewestSegments(t *testing.T) {
	settings := redactSettings()
	settings.ScanMaxBytes = 120
	snapshot := newTestSnapshot(t, settings, openAIKeyRule())
	older := strings.Repeat("a", 100) + " " + claudeSecret
	newer := strings.Repeat("b", 60) + " " + claudeSecret
	result, err := snapshot.Apply(claudeRequest(), claudeMessagesBody(t, older, newer))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || result.SkippedReason != SkippedScanTruncated {
		t.Fatalf("expected truncation, got %+v", result)
	}
	if !bytes.Contains(result.Body, []byte(strings.Repeat("b", 60)+" [OpenAI密钥]")) {
		t.Fatalf("newest segment must be scanned first: %s", result.Body)
	}
	if !bytes.Contains(result.Body, []byte(strings.Repeat("a", 100)+" "+claudeSecret)) {
		t.Fatalf("older segment beyond budget should pass through: %s", result.Body)
	}
}

func cachedSnapshot(t *testing.T, settings Settings, rules ...Rule) *Snapshot {
	t.Helper()
	snapshot := newTestSnapshot(t, settings, rules...)
	snapshot.Cache = NewSegmentCache(0)
	return snapshot
}

func TestSegmentCacheResultsMatchUncachedScan(t *testing.T) {
	rules := []Rule{openAIKeyRule(), literalRule(2, 5, "hunter2hunter", "[口令]", ActionRedact)}
	plain := newTestSnapshot(t, redactSettings(), rules...)
	cached := cachedSnapshot(t, redactSettings(), rules...)
	history := []string{
		strings.Repeat("历史消息 ", 80) + claudeSecret,
		strings.Repeat("tool output line\n", 40) + "password hunter2hunter",
		"short " + claudeSecret,
	}
	for turn := 1; turn <= len(history); turn++ {
		body := claudeMessagesBody(t, history[:turn]...)
		for pass := 0; pass < 2; pass++ {
			want, _ := plain.Apply(claudeRequest(), body)
			got, _ := cached.Apply(claudeRequest(), body)
			if !bytes.Equal(got.Body, want.Body) || got.HitCount != want.HitCount || !reflect.DeepEqual(got.RuleHits, want.RuleHits) {
				t.Fatalf("turn %d pass %d: cached result diverges\n got %+v\nwant %+v", turn, pass, got, want)
			}
		}
	}
	if len(cached.Cache.current) == 0 {
		t.Fatal("long segments should be cached")
	}
}

// 缓存段不计入扫描上限：历史已完整扫描过，只有新增内容受上限约束
func TestSegmentCacheExemptsCachedBytesFromLimit(t *testing.T) {
	settings := redactSettings()
	settings.ScanMaxBytes = 700
	settings.OverLimitAction = OverLimitFailClosed
	snapshot := cachedSnapshot(t, settings, openAIKeyRule())
	first := strings.Repeat("x", 300) + claudeSecret
	second := strings.Repeat("y", 300)
	if _, err := snapshot.Apply(claudeRequest(), claudeMessagesBody(t, first, second)); err != nil {
		t.Fatalf("first turn within limit: %v", err)
	}
	result, err := snapshot.Apply(claudeRequest(), claudeMessagesBody(t, first, second, "new "+claudeSecret))
	if err != nil {
		t.Fatalf("only the uncached tail counts toward the limit: %v", err)
	}
	if bytes.Contains(result.Body, []byte(claudeSecret)) {
		t.Fatalf("all secrets must be redacted: %s", result.Body)
	}
}

// 截断扫描的段不进缓存，下一轮会补扫，结果收敛为完整脱敏
func TestSegmentCacheSkipsTruncatedSegments(t *testing.T) {
	settings := redactSettings()
	settings.ScanMaxBytes = 500
	snapshot := cachedSnapshot(t, settings, openAIKeyRule())
	body := claudeMessagesBody(t, strings.Repeat("o", 360)+" "+claudeSecret, strings.Repeat("n", 360))
	first, _ := snapshot.Apply(claudeRequest(), body)
	if !first.Truncated || !bytes.Contains(first.Body, []byte(claudeSecret)) {
		t.Fatalf("first pass should truncate the older segment: %+v", first)
	}
	second, _ := snapshot.Apply(claudeRequest(), body)
	if second.Truncated || bytes.Contains(second.Body, []byte(claudeSecret)) {
		t.Fatalf("second pass should complete the scan: truncated=%v body=%s", second.Truncated, second.Body)
	}
}

func TestSegmentCacheKeyedByCandidateRules(t *testing.T) {
	scoped := openAIKeyRule()
	scoped.Scope = Scope{EndpointNames: []string{"ep-a"}}
	snapshot := cachedSnapshot(t, redactSettings(), scoped)
	body := claudeMessagesBody(t, strings.Repeat("z", 300)+" "+claudeSecret)
	inScope, _ := snapshot.Apply(claudeRequest(), body)
	other := claudeRequest()
	other.EndpointName = "ep-b"
	outOfScope, _ := snapshot.Apply(other, body)
	if !inScope.Changed || outOfScope.Changed {
		t.Fatalf("scope must still apply with cache: in=%v out=%v", inScope.Changed, outOfScope.Changed)
	}
}

func TestSegmentCacheRotatesGenerations(t *testing.T) {
	cache := NewSegmentCache(2)
	keys := []segmentKey{{hashA: 1}, {hashA: 2}, {hashA: 3}}
	cache.storeMany(keys, make([]segmentOutcome, len(keys)))
	if len(cache.current) != 1 || len(cache.previous) != 2 {
		t.Fatalf("unexpected generations: current=%d previous=%d", len(cache.current), len(cache.previous))
	}
	outcomes := make([]segmentOutcome, 1)
	found := make([]bool, 1)
	cache.lookupMany(keys[:1], outcomes, found)
	if !found[0] {
		t.Fatal("entry in previous generation should still hit")
	}
}

func TestSegmentCacheConcurrentApply(t *testing.T) {
	snapshot := cachedSnapshot(t, redactSettings(), openAIKeyRule(), literalRule(2, 5, "hunter2hunter", "[口令]", ActionRedact))
	history := []string{strings.Repeat("甲", 200) + claudeSecret, strings.Repeat("乙", 200) + "hunter2hunter"}
	want, _ := newTestSnapshot(t, redactSettings(), openAIKeyRule(), literalRule(2, 5, "hunter2hunter", "[口令]", ActionRedact)).
		Apply(claudeRequest(), claudeMessagesBody(t, history...))
	done := make(chan []byte, 16)
	for range 16 {
		go func() {
			var last []byte
			for range 50 {
				result, _ := snapshot.Apply(claudeRequest(), claudeMessagesBody(t, history...))
				last = result.Body
			}
			done <- last
		}()
	}
	for range 16 {
		if got := <-done; !bytes.Equal(got, want.Body) {
			t.Fatalf("concurrent cached apply diverged: %s", got)
		}
	}
}
