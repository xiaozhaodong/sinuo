package privacy

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// 合成负载供对拍和基准共用，不读取用户请求或真实凭据。
type jsonWalkerFixture struct {
	name, path string
	body       []byte
}

func jsonWalkerFixtures() []jsonWalkerFixture {
	var fixtures []jsonWalkerFixture
	for _, size := range []int{1024, 64 << 10, 600 << 10} {
		for _, kind := range []string{"ascii", "unicode", "escaped"} {
			if size != 600<<10 && kind != "ascii" {
				continue
			}
			line := "ordinary content "
			if kind == "unicode" {
				line = "中文内容与隐私验证，测试消息和工具响应。"
			}
			if kind == "escaped" {
				line = "line\n\"quoted\"\\path\tmore\n"
			}
			content := strings.Repeat(line, size/len(line)+1)[:size] + " password=abcdefgh123456"
			for _, protocol := range []string{"claude", "codex"} {
				path := "/v1/messages"
				body := []byte(fmt.Sprintf(`{"stream":true,"messages":[{"role":"user","content":%q}]}`, content))
				if protocol == "codex" {
					path = "/v1/responses"
					body = []byte(fmt.Sprintf(`{"stream":true,"input":[{"role":"user","content":%q}]}`, content))
				}
				fixtures = append(fixtures, jsonWalkerFixture{fmt.Sprintf("%s-%s-%dKiB", protocol, kind, size/1024), path, body})
			}
		}
	}
	history := make([]string, 400)
	for i := range history {
		content := strings.Repeat("ordinary content ", 90) + fmt.Sprintf(" %04d", i)
		history[i] = fmt.Sprintf(`{"role":"user","content":%q}`, content)
	}
	fixtures = append(fixtures, jsonWalkerFixture{"claude-history400", "/v1/messages", []byte(`{"stream":true,"messages":[` + strings.Join(history, ",") + `]}`)})
	arguments := fmt.Sprintf(`{"query":%q,"nested":{"text":%q},"count":9007199254740993}`, strings.Repeat("ordinary content ", 18000)+" password=abcdefgh123456", strings.Repeat("tool text ", 30000))
	fixtures = append(fixtures, jsonWalkerFixture{"codex-tool-json", "/v1/responses", []byte(fmt.Sprintf(`{"input":[{"type":"function_call","call_id":"call_1","name":"example","arguments":%q}],"stream":true}`, arguments))})
	return fixtures
}

func BenchmarkJSONWalkerApply(b *testing.B) {
	rule := openAIKeyRule()
	rule.Pattern = `password=(?P<redact>[a-zA-Z0-9]+)`
	rules, err := CompileRules([]Rule{rule})
	if err != nil {
		b.Fatal(err)
	}
	for _, fixture := range jsonWalkerFixtures() {
		if strings.Contains(fixture.name, "600KiB") && strings.HasPrefix(fixture.name, "claude-ascii") {
			continue
		}
		if strings.Contains(fixture.name, "600KiB") && strings.HasPrefix(fixture.name, "codex-ascii") {
			continue
		}
		for _, restore := range []bool{false, true} {
			// 大单段 on/off/cold/hot 由仓库既有 BenchmarkRestoreRequest 覆盖。
			// 补充负载固定热缓存，观察避开规则扫描后 JSON 遍历的剩余成本。
			b.Run(fmt.Sprintf("%s/restore=%t", fixture.name, restore), func(b *testing.B) {
				s := Snapshot{Settings: redactSettings(), Rules: rules, Dict: NewRestoreDict(), RestoreKey: bytes.Repeat([]byte{2}, 32), Cache: NewSegmentCache(DefaultSegmentCacheCapacity)}
				s.Settings.ResponseRestoreEnabled = restore
				req := Request{Path: fixture.path, UpstreamType: UpstreamTypeEndpoint}
				if fixture.path == "/v1/responses" {
					req.UpstreamType = UpstreamTypeAccount
				}
				apply := func() {
					r, e := s.Apply(req, fixture.body)
					if e != nil || r.Truncated {
						b.Fatalf("apply: %v truncated=%v", e, r.Truncated)
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
			})
		}
	}
}

// 保留可直接复跑的新旧遍历对照，隔离规则匹配、字典与重编码的成本。
func BenchmarkJSONWalkerTraversal(b *testing.B) {
	for _, fixture := range jsonWalkerFixtures() {
		for _, restore := range []bool{false, true} {
			for _, impl := range []struct {
				name string
				walk func([]byte, segmentMatcher, *restoreInput) ([]textSegment, error)
			}{
				{"token", referenceWalkJSONText},
				{"bytes", walkJSONText},
			} {
				b.Run(fmt.Sprintf("%s/restore=%t/%s", fixture.name, restore, impl.name), func(b *testing.B) {
					match := walkerForPath(fixture.path)
					b.SetBytes(int64(len(fixture.body)))
					b.ReportAllocs()
					for b.Loop() {
						var replay *restoreInput
						if restore {
							replay = walkerReplay(isClaudeMessagesPath(fixture.path), false)
						}
						if _, err := impl.walk(fixture.body, match, replay); err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		}
	}
}

// 深层负载只有容器与非文本叶子，前后保留需扫描的正文以检查路径恢复。
func deepNonTextJSON(path, shape string, depth int, leaf string) []byte {
	var open, close strings.Builder
	for i := 0; i < depth; i++ {
		if shape == "objects" || shape == "mixed" && i%2 != 0 {
			open.WriteString(`{"next":`)
			close.WriteByte('}')
		} else {
			open.WriteByte('[')
			close.WriteByte(']')
		}
	}
	ending := []byte(close.String())
	for i, j := 0, len(ending)-1; i < j; i, j = i+1, j-1 {
		ending[i], ending[j] = ending[j], ending[i]
	}
	prefix := `{"messages":[{"content":"before fixture-secret"}],"metadata":{"unused":`
	suffix := `},"system":"after fixture-secret"}`
	if path == "/v1/responses" {
		prefix = `{"input":[{"role":"user","content":"before fixture-secret"}],"metadata":{"unused":`
		suffix = `},"instructions":"after fixture-secret"}`
	}
	return []byte(prefix + open.String() + leaf + string(ending) + suffix)
}

// 全请求基准可配合旧 Token / 修复前 P1 的 overlay，验证深层输入的实际开销。
func BenchmarkJSONWalkerDeepNonTextApply(b *testing.B) {
	rules, err := CompileRules([]Rule{literalRule(1, 1, "fixture-secret", "[redacted]", ActionRedact)})
	if err != nil {
		b.Fatal(err)
	}
	for _, protocol := range []string{"claude", "codex"} {
		req := claudeRequest()
		if protocol == "codex" {
			req.Path, req.UpstreamType = "/v1/responses", UpstreamTypeAccount
		}
		for _, shape := range []string{"arrays", "objects", "mixed"} {
			for _, depth := range []int{256, 1000, 5000, 10000} {
				b.Run(fmt.Sprintf("%s/%s/depth=%d", protocol, shape, depth), func(b *testing.B) {
					body := bytes.ReplaceAll(deepNonTextJSON(req.Path, shape, depth, "0"), []byte("fixture-secret"), []byte("ordinary"))
					s := Snapshot{Settings: redactSettings(), Rules: rules}
					b.SetBytes(int64(len(body)))
					b.ReportAllocs()
					b.ResetTimer()
					for b.Loop() {
						r, err := s.Apply(req, body)
						if err != nil || r.Changed || r.Truncated || r.SkippedReason != "" {
							b.Fatalf("apply: err=%v changed=%t truncated=%t skipped=%s", err, r.Changed, r.Truncated, r.SkippedReason)
						}
					}
				})
			}
		}
	}
}

func BenchmarkJSONWalkerDeepNonTextTraversal(b *testing.B) {
	for _, depth := range []int{256, 1000, 5000, 10000} {
		body := deepNonTextJSON("/v1/messages", "arrays", depth, "0")
		for _, impl := range []struct {
			name string
			walk func([]byte, segmentMatcher, *restoreInput) ([]textSegment, error)
		}{
			{"token", referenceWalkJSONText},
			{"bytes", walkJSONText},
		} {
			b.Run(fmt.Sprintf("depth=%d/%s", depth, impl.name), func(b *testing.B) {
				b.SetBytes(int64(len(body)))
				b.ReportAllocs()
				for b.Loop() {
					if _, err := impl.walk(body, matchClaudeMessagesTextPath, nil); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
