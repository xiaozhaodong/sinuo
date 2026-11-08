package privacy

import (
	"bytes"
	"fmt"
	"math/rand"
	"runtime"
	"strings"
	"testing"
)

// 600 KiB 可读正文，冷缓存每轮走规则扫描，热缓存复用基础区间。
func BenchmarkRestoreRequest(b *testing.B) {
	data := strings.Repeat("ordinary content ", 36000)
	field := openAIKeyRule()
	field.Pattern = `password=(?P<redact>[a-zA-Z0-9]+)`
	rules, err := CompileRules([]Rule{field})
	if err != nil {
		b.Fatal(err)
	}
	for _, protocol := range []string{"claude", "codex"} {
		req := Request{Path: "/v1/messages", UpstreamType: UpstreamTypeEndpoint}
		raw := []byte(fmt.Sprintf(`{"stream":true,"messages":[{"role":"user","content":%q}]}`, data+"password=abcdefgh123456"))
		if protocol == "codex" {
			req = Request{Path: "/v1/responses", UpstreamType: UpstreamTypeAccount}
			raw = []byte(fmt.Sprintf(`{"stream":true,"input":[{"role":"user","content":%q}]}`, data+"password=abcdefgh123456"))
		}
		for _, on := range []bool{false, true} {
			for _, warm := range []bool{false, true} {
				b.Run(fmt.Sprintf("%s/restore=%t/hot=%t", protocol, on, warm), func(b *testing.B) {
					s := Snapshot{Settings: redactSettings(), Rules: rules, Dict: NewRestoreDict(), RestoreKey: bytes.Repeat([]byte{2}, 32)}
					s.Settings.ResponseRestoreEnabled = on
					if warm {
						s.Cache = NewSegmentCache(16)
						if _, e := s.Apply(req, raw); e != nil {
							b.Fatal(e)
						}
					}
					b.SetBytes(int64(len(raw)))
					b.ReportAllocs()
					b.ResetTimer()
					for b.Loop() {
						if _, e := s.Apply(req, raw); e != nil {
							b.Fatal(e)
						}
					}
				})
			}
		}
	}
}

// 比较不同驻留条目数下，单个工具结果按全局原值补遮的成本。
func BenchmarkRestoreContinuation(b *testing.B) {
	for _, entries := range []int{0, 128, 49000} {
		d := NewRestoreDict()
		for i := 0; i < entries; {
			v, e := d.Prepare()
			if e != nil {
				b.Fatal(e)
			}
			for end := min(i+500, entries); i < end; i++ {
				if _, e = v.add(bytes.Repeat([]byte{2}, 32), fmt.Sprintf("sample-%d-abcdef", i)); e != nil {
					b.Fatal(e)
				}
			}
			if e = v.Publish(); e != nil {
				b.Fatal(e)
			}
			v.Release()
		}
		seed, e := d.Prepare()
		if e != nil {
			b.Fatal(e)
		}
		if entries > 0 {
			_, _ = seed.add(bytes.Repeat([]byte{2}, 32), "sample-0-abcdef")
		}
		if e = seed.Publish(); e != nil {
			b.Fatal(e)
		}

		seed.Release()
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		b.Run(fmt.Sprintf("entries=%d", entries), func(b *testing.B) {
			s := Snapshot{Settings: redactSettings(), Dict: d, RestoreKey: bytes.Repeat([]byte{2}, 32)}
			s.Settings.ResponseRestoreEnabled = true
			raw := []byte(`{"previous_response_id":"prior","input":[{"type":"function_call_output","output":"sample-0-abcdef"}]}`)
			req := Request{Path: "/v1/responses", UpstreamType: UpstreamTypeAccount}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := s.Apply(req, raw); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(m.HeapAlloc)/(1<<20), "live_heap_MiB")
		})
	}
}

// 用固定种子的随机字典测单段冷热扫描，以及相同正文总量拆成 400 段历史后的复用。
// 单独报告重建，避免把排序成本摊入热字典的每次请求。
func BenchmarkRestoreLargeDictionary(b *testing.B) {
	const segmentSize = 600 * 1024
	const line = "func example() { return append(currentValue, nextValue) } "
	data := strings.Repeat(line, segmentSize/len(line)+1)[:segmentSize]
	raw := []byte(fmt.Sprintf(`{"stream":true,"messages":[{"role":"user","content":%q}]}`, data))
	suffix := bytes.LastIndexByte(raw, '"') - 16
	const historySegments = 400
	history := make([]string, historySegments)
	for i := range history {
		content := fmt.Sprintf("%s%016x", data[:segmentSize/historySegments-16], i)
		history[i] = fmt.Sprintf(`{"role":"user","content":%q}`, content)
	}
	longRequest := []byte(`{"stream":true,"messages":[` + strings.Join(history, ",") + `]}`)
	field := openAIKeyRule()
	field.Pattern = `password=(?P<redact>[a-zA-Z0-9]+)`
	rules, err := CompileRules([]Rule{field})
	if err != nil {
		b.Fatal(err)
	}
	for _, n := range []int{0, 1000, 10000, 49000} {
		d := NewRestoreDict()
		rng := rand.New(rand.NewSource(1337))
		const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
		for i := 0; i < n; {
			v := prepareDictionaryTest(b, d)
			for end := min(n, i+9000); i < end; i++ {
				var value [16]byte
				for j := range value {
					value[j] = alphabet[rng.Intn(len(alphabet))]
				}
				addDictionaryTest(b, v, string(value[:]))
			}
			publishDictionaryTest(b, v)
			v.Release()
		}
		snapshot := Snapshot{Settings: redactSettings(), Rules: rules, Dict: d, Cache: NewSegmentCache(DefaultSegmentCacheCapacity), RestoreKey: bytes.Repeat([]byte{1}, 32)}
		snapshot.Settings.ResponseRestoreEnabled = true
		apply := func(b *testing.B, body []byte) {
			r, err := snapshot.Apply(claudeRequest(), body)
			if err != nil || r.Changed || r.Truncated {
				b.Fatalf("合成请求扫描异常: %v", err)
			}
			r.Restore.Release()
		}
		counter := 0 // 多轮基准共享字典，每次冷扫描使用尚未访问的正文。
		for _, cold := range []bool{false, true} {
			b.Run(fmt.Sprintf("entries=%d/cold=%t", n, cold), func(b *testing.B) {
				apply(b, raw)
				b.ReportAllocs()
				for b.Loop() {
					if cold {
						counter++
						copy(raw[suffix:], fmt.Sprintf("%016x", counter))
					}
					apply(b, raw)
				}
			})
		}
		b.Run(fmt.Sprintf("entries=%d/hot-400", n), func(b *testing.B) {
			apply(b, longRequest)
			b.ReportAllocs()
			for b.Loop() {
				apply(b, longRequest)
			}
		})
		if n == 49000 {
			b.Run("rebuild/entries=49000", func(b *testing.B) {
				for b.Loop() {
					d.mu.Lock()
					d.index = nil
					d.mu.Unlock()
					v := prepareDictionaryTest(b, d)
					v.Release()
				}
			})
		}
	}
}
