package privacy

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/maphash"
	"strings"
	"testing"
)

func TestCandidateFingerprintBufferBoundaries(t *testing.T) {
	c := NewSegmentCache(16)
	for _, n := range []int{0, 1, 16, 63, 64, 65, 256, 1024} {
		candidates := make([]int, n)
		var raw []byte
		for i := range candidates {
			candidates[i] = i*3 + 1
			raw = binary.LittleEndian.AppendUint32(raw, uint32(candidates[i]))
		}
		if c.candidateFingerprint(candidates) != maphash.Bytes(c.seedA, raw) {
			t.Fatalf("candidate count %d: fingerprint changed", n)
		}
	}
}

// 分配数是 P5 的目标：容量内的候选筛选与指纹计算均不再申请临时堆对象。
func TestCandidateScratchAllocations(t *testing.T) {
	c := NewSegmentCache(16)
	for _, n := range []int{1, 16, 64} {
		s := Snapshot{Rules: make([]CompiledRule, n)}
		for i := range s.Rules {
			s.Rules[i].Enabled = true
		}
		req := claudeRequest()
		var fingerprint uint64
		if got := testing.AllocsPerRun(100, func() {
			var storage [localCandidateCapacity]int
			candidates := s.candidateRules(req, storage[:0])
			if len(candidates) != n {
				t.Fatal("candidate count changed")
			}
			fingerprint = c.candidateFingerprint(candidates)
		}); got != 0 {
			t.Fatalf("rules=%d: scratch allocations=%g", n, got)
		}
		if fingerprint != c.candidateFingerprint(s.candidateRules(req, nil)) {
			t.Fatal("scratch fingerprint differs from allocated candidates")
		}
	}
}

// 末条规则跨越局部缓冲边界；同一正文交替命中/不命中作用域，冷、热结果都须一致。
// 工具参数同时经过内外两层 JSON 编码，未命中字段仍保留原始字节。
func TestPrivacyHotPathScopesAndCache(t *testing.T) {
	const secret = "AbcDEF123456xyz789"
	text := strings.Repeat("中文<>&\"\\\n\t\x01", 40) + " " + secret + " tail"
	const untouched = `"metadata": {"opaque":1.00,"escaped":"\u0041"}`
	for _, n := range []int{1, 16, 64, 65, 256} {
		for _, restore := range []bool{false, true} {
			for _, protocol := range []string{"claude", "codex"} {
				t.Run(fmt.Sprintf("rules=%d/restore=%t/%s", n, restore, protocol), func(t *testing.T) {
					t.Parallel()
					var rules []Rule
					for i := 0; i < n; i++ {
						rules = append(rules, literalRule(int64(i+1), i, fmt.Sprintf("unmatched-%d", i), "[R]", ActionRedact))
					}
					rules[n-1].Pattern = secret
					rules[n-1].Scope = Scope{EndpointNames: []string{"selected"}}
					s := cachedSnapshot(t, redactSettings(), rules...)
					s.Settings.ResponseRestoreEnabled = restore
					s.RestoreKey = bytes.Repeat([]byte{7}, 32)
					req := Request{Path: "/v1/messages", UpstreamType: UpstreamTypeEndpoint}
					var payload any = map[string]any{"messages": []any{map[string]any{"role": "user", "content": text}}}
					if protocol == "codex" {
						req.Path, req.UpstreamType = "/v1/responses", UpstreamTypeAccount
						arguments, err := json.Marshal(map[string]string{"query": text})
						if err != nil {
							t.Fatal(err)
						}
						payload = map[string]any{"input": []any{map[string]any{"type": "function_call", "call_id": "call_1", "name": "example", "arguments": string(arguments)}}}
					}
					raw, err := json.Marshal(payload)
					if err != nil {
						t.Fatal(err)
					}
					body := append([]byte("{"+untouched+","), raw[1:]...)
					for _, endpoint := range []string{"selected", "excluded", "selected", "excluded"} {
						req.EndpointName = endpoint
						r, err := s.Apply(req, body)
						if r.Restore != nil {
							defer r.Restore.Release()
						}
						matched := endpoint == "selected"
						if err != nil || r.Truncated || r.Changed != matched {
							t.Fatalf("endpoint=%s changed=%t truncated=%t err=%v", endpoint, r.Changed, r.Truncated, err)
						}
						if !bytes.Contains(r.Body, []byte(untouched)) {
							t.Fatal("unmatched JSON bytes changed")
						}
						var out struct {
							Messages []struct{ Content string }
							Input    []struct{ Arguments string }
						}
						if err := json.Unmarshal(r.Body, &out); err != nil {
							t.Fatal(err)
						}
						var got string
						if protocol == "claude" {
							got = out.Messages[0].Content
						} else {
							var inner struct{ Query string }
							if err := json.Unmarshal([]byte(out.Input[0].Arguments), &inner); err != nil {
								t.Fatal(err)
							}
							got = inner.Query
						}
						want := text
						if matched {
							replacement := "[R]"
							if restore {
								if r.Restore == nil || len(r.Restore.values) != 1 {
									t.Fatal("missing restore mapping")
								}
								for token, value := range r.Restore.values {
									if value != secret {
										t.Fatal("wrong restore value")
									}
									replacement = token
								}
							}
							want = strings.ReplaceAll(text, secret, replacement)
						}
						if got != want {
							t.Fatal("decoded text or scope result changed")
						}
						if preview := s.ApplyToText(req, text); string(preview.Body) != want || preview.Changed != matched || preview.Restore != nil {
							t.Fatal("text preview or scope result changed")
						}
					}
				})
			}
		}
	}
}
