// 仅用于回放工具参数，全部代码仅在 go test 中编译。
package privacyrestorebench_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"sinuo/internal/privacy"
	"sinuo/internal/proxy"
	"sinuo/internal/proxy/restorestream"
	"sinuo/internal/tracking"
)

const (
	value           = "demo-quote\"slash\\newline\nend"
	deltasPerStream = 128
	streamsPerBatch = 256
)

func fixtureRestoreToken(id string) string { return "{{SNU_" + id + "}}" }

var productionToken = syntheticToken()
var values = map[string]string{productionToken: value}

func data(s string) []byte       { return []byte("data: " + s + "\n\n") }
func jsonString(s string) string { encoded, _ := json.Marshal(s); return string(encoded) }
func makeEvents(codex bool) [][]byte {
	return eventsForParts(codex, toolParts(productionToken))
}

func toolParts(id string) []string {
	parts := make([]string, deltasPerStream)
	for i := range parts {
		part := fmt.Sprintf(`"k%d":%s`, i, jsonString(expectedValue(i, id)))
		if i == 0 {
			part = "{" + part
		}
		if i == len(parts)-1 {
			part += "}"
		} else {
			part += ","
		}
		parts[i] = part
	}
	return parts
}

func eventsForParts(codex bool, parts []string) [][]byte {
	var ev [][]byte
	if codex {
		ev = append(ev, data(`{"type":"response.created","response":{"id":"r","output":[],"status":"in_progress"},"sequence_number":0}`), data(`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"c","name":"dry_run","arguments":""},"sequence_number":1}`))
	} else {
		ev = append(ev, data(`{"type":"message_start","message":{"type":"message","role":"assistant","model":"synthetic","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}`), data(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"synthetic_tool","name":"dry_run","input":{}}}`))
	}
	var args strings.Builder
	for i, part := range parts {
		args.WriteString(part)
		if codex {
			ev = append(ev, data(fmt.Sprintf(`{"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":%s,"sequence_number":%d}`, jsonString(part), i+2)))
		} else {
			raw := fmt.Sprintf(`{"delta":{"partial_json":%s,"type":"input_json_delta"},"index":0,"type":"content_block_delta"}`, jsonString(part))
			ev = append(ev, []byte("event: content_block_delta\ndata: "+raw+"\n\n"))
		}
	}
	if codex {
		v := jsonString(args.String())
		item := fmt.Sprintf(`{"type":"function_call","id":"fc_1","call_id":"c","name":"dry_run","arguments":%s}`, v)
		seq := len(parts) + 2
		ev = append(ev, data(fmt.Sprintf(`{"type":"response.function_call_arguments.done","item_id":"fc_1","output_index":0,"arguments":%s,"sequence_number":%d}`, v, seq)), data(fmt.Sprintf(`{"type":"response.output_item.done","output_index":0,"item":%s,"sequence_number":%d}`, item, seq+1)), data(fmt.Sprintf(`{"type":"response.completed","response":{"id":"r","status":"completed","output":[%s],"usage":{"input_tokens":10,"output_tokens":10}},"sequence_number":%d}`, item, seq+2)))
	} else {
		ev = append(ev, data(`{"type":"content_block_stop","index":0}`), data(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":10}}`), data(`{"type":"message_stop"}`))
	}
	return ev
}

func expectedValue(i int, replacement string) string {
	s := strings.Repeat("a", 48) + "\"C:\\demo\\file\""
	if i%16 == 0 {
		s += " " + replacement + "."
	}
	return s
}

type frameReader struct {
	events        [][]byte
	event, offset int
}

func (r *frameReader) Read(p []byte) (int, error) {
	if r.event == len(r.events) {
		return 0, io.EOF
	}
	ev := r.events[r.event]
	n := copy(p, ev[r.offset:])
	r.offset += n
	if r.offset == len(ev) {
		r.event++
		r.offset = 0
	}
	return n, nil
}

type sink struct {
	headers http.Header
	size    int
}

func (w *sink) Header() http.Header         { return w.headers }
func (w *sink) WriteHeader(int)             {}
func (w *sink) Write(p []byte) (int, error) { w.size += len(p); return len(p), nil }
func (w *sink) Flush()                      {}

type benchDict map[string]string

func (d benchDict) Lookup(key string) (string, bool) { v, ok := d[key]; return v, ok }
func restoring(w http.ResponseWriter, codex bool) *restorestream.Writer {
	path := "/v1/messages"
	if codex {
		path = "/v1/responses"
	}
	return restorestream.New(w, path, benchDict(values))
}
func replay(events [][]byte, dst http.ResponseWriter, codex bool) (*tracking.TokenUsage, error) {
	sp := proxy.NewStreamProcessor(proxy.NewTokenParser(), nil, dst, dst.(http.Flusher), "bench", "local")
	restore, _ := dst.(*restorestream.Writer)
	var body io.Reader = &frameReader{events: events}
	if restore != nil {
		defer restore.Release()
		if codex {
			// 回放直接注入回填 writer；补入官方无 Content-Type 路径的一次首事件检查。
			var err error
			body, err = restorestream.OpenUntypedResponses(context.Background(), bufio.NewReader(body))
			if err != nil {
				return nil, err
			}
		}
	}
	usage, err := sp.ProcessStream(context.Background(), &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(body)})
	if err == nil && restore != nil {
		err = restore.Finish()
	}
	return usage, err
}

// 基准使用生产格式标识，固定整条 Claude 字节流，防止对照两侧的负载漂移。
func TestFixtureDigest(t *testing.T) {
	got := fmt.Sprintf("%x", sha256.Sum256(bytes.Join(makeEvents(false), nil)))
	if got != "277dbae18323086efca3d763222dc99708c587ffb4aedfc26e463d614bb56ad5" {
		t.Fatal("Claude 基准数据发生变化", got)
	}
}

func TestToolReplay(t *testing.T) {
	for _, codex := range []bool{false, true} {
		events := makeEvents(codex)
		rec := httptest.NewRecorder()
		w := restoring(rec, codex)
		usage, err := replay(events, w, codex)
		if err != nil {
			t.Fatal(err)
		}
		if usage == nil || usage.InputTokens != 10 || usage.OutputTokens != 10 {
			t.Fatalf("上游用量变化: %+v", usage)
		}
		if !w.Terminal() {
			t.Fatal("工具流未收尾")
		}
		var assembled strings.Builder
		lines := bytes.Split(bytes.TrimSuffix(rec.Body.Bytes(), []byte("\n\n")), []byte("\n\n"))
		if len(lines) != len(events) {
			t.Fatalf("事件数 %d, 期望 %d", len(lines), len(events))
		}
		for i, line := range lines {
			_, raw, ok := bytes.Cut(line, []byte("data: "))
			if !ok || !json.Valid(raw) {
				t.Fatal("无效事件", i)
			}
			if codex && gjson.GetBytes(raw, "sequence_number").Int() != int64(i) {
				t.Fatal("序号错误", i)
			}
			switch gjson.GetBytes(raw, "type").Str {
			case "content_block_delta":
				assembled.WriteString(gjson.GetBytes(raw, "delta.partial_json").Str)
			case "response.function_call_arguments.delta":
				assembled.WriteString(gjson.GetBytes(raw, "delta").Str)
			case "response.function_call_arguments.done", "response.output_item.done", "response.completed":
				path := []string{"arguments", "item.arguments", "response.output.0.arguments"}[i-deltasPerStream-2]
				if gjson.GetBytes(raw, path).Str != assembled.String() {
					t.Fatal("终态与增量不一致", i)
				}
			}
		}
		var args map[string]string
		if err := json.Unmarshal([]byte(assembled.String()), &args); err != nil || len(args) != deltasPerStream {
			t.Fatal("拼接参数不完整", err)
		}
		for i := 0; i < deltasPerStream; i++ {
			if args[fmt.Sprintf("k%d", i)] != expectedValue(i, value) {
				t.Fatal("实参错误", i)
			}
		}
		// 在 SSE 分隔空行正中切开，再把余下多个事件合并在一次 Write 里。
		rec2 := httptest.NewRecorder()
		w2 := restoring(rec2, codex)
		defer w2.Release()
		wire := bytes.Join(events, nil)
		cut := len(events[0]) - 1
		for _, p := range [][]byte{wire[:cut], wire[cut:]} {
			if _, err := w2.Write(p); err != nil {
				t.Fatal(err)
			}
		}
		if !bytes.Equal(rec.Body.Bytes(), rec2.Body.Bytes()) {
			t.Fatal("网络分片改变回填结果")
		}
	}
}

func TestIncompleteTool(t *testing.T) {
	for _, codex := range []bool{false, true} {
		events := makeEvents(codex)
		incomplete := restoring(httptest.NewRecorder(), codex)
		for _, ev := range events[:3] {
			if _, err := incomplete.Write(ev); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := incomplete.Write(events[len(events)-3]); err == nil {
			t.Fatal("交付了未闭合工具")
		}
		incomplete.Release()
		w := restoring(httptest.NewRecorder(), codex)
		for _, ev := range events[:3] {
			if _, err := w.Write(ev); err != nil {
				t.Fatal(err)
			}
		}
		duplicate := append([][]byte(nil), events[3:]...)
		duplicate[0] = bytes.Replace(duplicate[0], []byte("k1"), []byte("k0"), 1)
		var invalid error
		for _, frame := range duplicate {
			if _, invalid = w.Write(frame); invalid != nil {
				break
			}
		}
		if invalid == nil {
			t.Fatal("接受了重复键")
		}
		w.Release()

	}
}

func throughput(b *testing.B, events [][]byte, codex bool, workers int, dict *privacy.RestoreDict) float64 {
	runtime.GC()
	start := time.Now()
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range streamsPerBatch / workers {
				d := &sink{headers: make(http.Header)}
				if err := benchReplay(events, d, codex, dict); err != nil {
					b.Error(err)
					return
				}
				if d.size == 0 {
					b.Error("空流")
					return
				}
			}
		}()
	}
	wg.Wait()
	return streamsPerBatch * deltasPerStream / time.Since(start).Seconds()
}

// 每对交替顺序，按对计算损耗再取中位数。明确使用 -benchtime=35x 可重放此次决策。
func BenchmarkToolRestore(b *testing.B) {
	benchTools(b, makeEvents)
}

// 保持属性、命中率和事件数，将边界移进字符串及占位符内部。
func BenchmarkSplitToolRestore(b *testing.B) {
	benchTools(b, func(codex bool) [][]byte {
		parts := toolParts(productionToken)
		var carry string
		for i, s := range parts[:len(parts)-1] {
			at := len(s) / 2
			if n := strings.Index(s, productionToken); n >= 0 {
				at = n + 12
			}
			parts[i], carry = carry+s[:at], s[at:]
		}
		parts[len(parts)-1] = carry + parts[len(parts)-1]
		return eventsForParts(codex, parts)
	})
}

func benchTools(b *testing.B, fixture func(bool) [][]byte) {
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.DiscardHandler))
	defer slog.SetDefault(prev)
	dicts := [2]*privacy.RestoreDict{nil, seedBenchDictionary(b)}
	for _, proto := range []string{"Claude", "Codex"} {
		codex := proto == "Codex"
		events := fixture(codex)
		for _, workers := range []int{1, 8} {
			b.Run(fmt.Sprintf("%s/%d", proto, workers), func(b *testing.B) {
				var losses, off, on []float64
				for i := 0; i < b.N; i++ {
					var pair [2]float64
					for n := 0; n < 2; n++ {
						mode := (i + n) % 2
						pair[mode] = throughput(b, events, codex, workers, dicts[mode])
					}
					losses = append(losses, 100*(1-pair[1]/pair[0]))
					off = append(off, pair[0])
					on = append(on, pair[1])
				}

				b.ReportMetric(median(losses), "loss-%")
				b.ReportMetric(median(off), "off-delta/s")
				b.ReportMetric(median(on), "on-delta/s")
				var pass int
				for _, loss := range losses {
					if loss <= 5 {
						pass++
					}
				}
				b.ReportMetric(float64(pass), "pairs-in-budget")
			})
		}
	}
}

func median(s []float64) float64 { slices.Sort(s); return (s[(len(s)-1)/2] + s[len(s)/2]) / 2 }

// 文本流用生产格式的标识，分布为无命中、相邻两条共同组成标识和纯空白。
func textEvents(codex bool) [][]byte {
	ev := makeEvents(codex)[:1:1]
	if codex {
		ev = append(ev, data(`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"i","content":[]},"sequence_number":1}`))
	} else {
		ev = append(ev, data(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`))
	}
	var all strings.Builder
	for i := range deltasPerStream {
		s := "visible content"
		switch i % 16 {
		case 0:
			s += " " + productionToken[:11]
		case 1:
			s = productionToken[11:] + " text"
		case 2:
			s = " "
		}
		all.WriteString(s)
		if codex {
			ev = append(ev, data(fmt.Sprintf(`{"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"i","delta":%s,"sequence_number":%d}`, jsonString(s), i+2)))
		} else {
			ev = append(ev, []byte("event: content_block_delta\ndata: "+fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%s}}`, jsonString(s))+"\n\n"))
		}
	}
	if codex {
		v := jsonString(all.String())
		item := fmt.Sprintf(`{"type":"message","id":"i","content":[{"type":"output_text","text":%s}]}`, v)
		ev = append(ev, data(fmt.Sprintf(`{"type":"response.output_text.done","output_index":0,"content_index":0,"item_id":"i","text":%s,"sequence_number":130}`, v)), data(fmt.Sprintf(`{"type":"response.completed","response":{"id":"r","status":"completed","output":[%s],"usage":{"input_tokens":10,"output_tokens":10}},"sequence_number":131}`, item)))
	} else {
		ev = append(ev, data(`{"type":"content_block_stop","index":0}`), data(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":10}}`), data(`{"type":"message_stop"}`))
	}
	return ev
}

func BenchmarkTextRestore(b *testing.B) { benchTools(b, textEvents) }

// 仅测 SSE 写入、恢复与收尾，不经过用量解析器或代理的快路径观察。
func BenchmarkRestoreWriter(b *testing.B) {
	dict := seedBenchDictionary(b)
	for _, proto := range []string{"Claude", "Codex"} {
		codex := proto == "Codex"
		for _, fixture := range []struct {
			name   string
			events [][]byte
		}{{"Text", textEvents(codex)}, {"Tool", makeEvents(codex)}} {
			b.Run(proto+"/"+fixture.name, func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(bytes.Join(fixture.events, nil))))
				b.ResetTimer()
				for range b.N {
					w, view, err := benchResponseWriter(&sink{}, codex, dict)
					if err != nil {
						b.Fatal(err)
					}
					for _, event := range fixture.events {
						if _, err := w.Write(event); err != nil {
							b.Fatal(err)
						}
					}
					if err := w.Finish(); err != nil {
						b.Fatal(err)
					}
					w.Release()
					view.Release()
				}
			})
		}
	}
}

// 首段文本延迟从启动本地响应处理到首次写出可见正文，不包含网络或模型生成。
func BenchmarkFirstVisibleText(b *testing.B) {
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.DiscardHandler))
	defer slog.SetDefault(prev)
	dicts := [2]*privacy.RestoreDict{nil, seedBenchDictionary(b)}
	for _, proto := range []string{"Claude", "Codex"} {
		codex := proto == "Codex"
		events := textEvents(codex)
		for _, workers := range []int{1, 8} {
			b.Run(fmt.Sprintf("%s/%d", proto, workers), func(b *testing.B) {
				var off, on []float64
				for i := range b.N {
					for n := range 2 {
						mode := (i + n) % 2
						var wg sync.WaitGroup
						delays := make([]float64, workers)
						for j := range delays {
							wg.Add(1)
							go func() {
								defer wg.Done()
								delay, err := firstTextDelay(events, codex, dicts[mode])
								if err != nil {
									b.Error(err)
									return
								}
								delays[j] = delay
							}()
						}
						wg.Wait()
						if mode == 0 {
							off = append(off, delays...)
						} else {
							on = append(on, delays...)
						}
					}
				}
				b.ReportMetric(median(off), "first-off-us")
				b.ReportMetric(median(on), "first-on-us")
			})
		}
	}
}
