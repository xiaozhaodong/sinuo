package privacy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// 同时覆盖长安全区间、短安全区间及连续转义，避免只用普通文本验收编码快路径。
func jsonEncodingSamples(size int) []struct{ name, text string } {
	var samples []struct{ name, text string }
	for _, sample := range []struct{ name, unit string }{
		{"ascii", "ordinary text <>& "},
		{"unicode", "中文🙂\u2028\u2029"},
		{"escaped", "line\n\"quoted\"\\path\tmore\n"},
		{"short-runs", "a\"bc\\def\n"},
		{"quotes", "\"\\"},
		{"mixed-escapes", "\n\"\\\t"},
		{"named-controls", "\b\f\n\r\t"},
		{"controls", "\x00\x01\x02\b\f\n\r\t\x1f"},
		{"sparse", strings.Repeat("ordinary text ", 256) + "\n"},
	} {
		samples = append(samples, struct{ name, text string }{sample.name, strings.Repeat(sample.unit, size/len(sample.unit)+1)})
	}
	for _, n := range []int{1, 2, 3, 4, 8, 16} {
		unit := strings.Repeat("a", n) + "\""
		samples = append(samples, struct{ name, text string }{fmt.Sprintf("run-%d", n), strings.Repeat(unit, size/len(unit)+1)})
	}
	return samples
}

func BenchmarkJSONStringEncoding(b *testing.B) {
	for _, sample := range jsonEncodingSamples(600 << 10) {
		for _, encoder := range []struct {
			name string
			fn   func([]byte, string) []byte
		}{{"current", appendJSONStringBody}, {"reference", referenceAppendJSONStringBody}} {
			b.Run(sample.name+"/"+encoder.name, func(b *testing.B) {
				buf := make([]byte, 0, len(sample.text)*6)
				b.SetBytes(int64(len(sample.text)))
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					buf = encoder.fn(buf[:0], sample.text)
				}
			})
		}
	}
}

func BenchmarkPrivacyHotPath(b *testing.B) {
	var fixtures []jsonWalkerFixture
	for _, f := range jsonWalkerFixtures() {
		switch f.name {
		case "claude-ascii-1KiB", "claude-ascii-64KiB", "claude-ascii-600KiB", "codex-ascii-600KiB", "claude-unicode-600KiB", "claude-escaped-600KiB", "claude-history400", "codex-tool-json":
			fixtures = append(fixtures, f)
		}
	}
	for _, sample := range jsonEncodingSamples(600 << 10) {
		switch sample.name {
		case "short-runs", "quotes", "controls", "sparse":
			value, err := json.Marshal(sample.text + " password=abcdefgh123456")
			if err != nil {
				b.Fatal(err)
			}
			body := []byte(`{"messages":[{"role":"user","content":` + string(value) + `}]}`)
			fixtures = append(fixtures, jsonWalkerFixture{"claude-" + sample.name, "/v1/messages", body})
		}
	}
	for _, fixture := range fixtures {
		b.Run(fixture.name, func(b *testing.B) { benchmarkPrivacyHotApply(b, fixture, 1) })
	}
	for _, n := range []int{16, 64, 65, 256} {
		b.Run(fmt.Sprintf("claude-ascii-1KiB-rules%d", n), func(b *testing.B) {
			benchmarkPrivacyHotApply(b, fixtures[0], n)
		})
	}
}

func benchmarkPrivacyHotApply(b *testing.B, fixture jsonWalkerFixture, n int) {
	var rawRules []Rule
	for i := 0; i < n; i++ {
		rule := openAIKeyRule()
		rule.ID = int64(i + 1)
		rule.Pattern = fmt.Sprintf(`nonexistent%d=(?P<redact>[a-zA-Z0-9]+)`, i)
		if i == 0 {
			rule.Pattern = `password=(?P<redact>[a-zA-Z0-9]+)`
		}
		rawRules = append(rawRules, rule)
	}
	rules, err := CompileRules(rawRules)
	if err != nil {
		b.Fatal(err)
	}
	s := Snapshot{Settings: redactSettings(), Rules: rules, Dict: NewRestoreDict(), RestoreKey: bytes.Repeat([]byte{2}, 32), Cache: NewSegmentCache(DefaultSegmentCacheCapacity)}
	s.Settings.ResponseRestoreEnabled = true
	req := Request{Path: fixture.path, UpstreamType: UpstreamTypeEndpoint}
	if fixture.path == "/v1/responses" {
		req.UpstreamType = UpstreamTypeAccount
	}
	apply := func() {
		r, err := s.Apply(req, fixture.body)
		if err != nil || r.Truncated {
			b.Fatalf("apply: %v truncated=%v", err, r.Truncated)
		}
		if r.Restore != nil {
			r.Restore.Release()
		}
	}
	apply()
	b.SetBytes(int64(len(fixture.body)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		apply()
	}
}
