package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"sinuo/internal/tracking"
)

func requirePhaseMs(t *testing.T, got *int64, want int64) {
	t.Helper()
	if got == nil || *got != want {
		t.Fatalf("毫秒数=%v, want %d", got, want)
	}
}

func TestStreamPhases_FlowQuantiles(t *testing.T) {
	start := time.Unix(0, 0)
	c := newStreamPhaseCollector(start)
	for i := int64(1); i <= 10; i++ {
		c.Observe(&ParseResult{ContentKind: ContentText, ContentBytes: 100}, 110, start.Add(time.Duration(i*100)*time.Millisecond))
	}
	// 1000字节：10%在100ms，90%在900ms；(900-100)*10/8=1000。
	requirePhaseMs(t, c.Snapshot(0).StreamFlowMs, 1000)
	burst := newStreamPhaseCollector(start)
	for range 10 {
		burst.Observe(&ParseResult{ContentKind: ContentText, ContentBytes: 100}, 100, start.Add(time.Second))
	}
	if len(burst.flow) != 1 {
		t.Fatal("同毫秒必须合并")
	}
	requirePhaseMs(t, burst.Snapshot(0).StreamFlowMs, 1)
}

func TestStreamPhases_ReasoningStartAndCaps(t *testing.T) {
	start := time.Unix(0, 0)
	c := newStreamPhaseCollector(start)
	for i := int64(1); i <= 15; i++ {
		kind := ContentReasoning
		if i > 5 {
			kind = ContentText
		}
		c.Observe(&ParseResult{ContentKind: kind, ContentBytes: 100}, 100, start.Add(time.Duration(i*100)*time.Millisecond))
	}
	// 正文前500字节扣掉，正文10%在600、90%在1400；跨度800*10/8=1000。
	requirePhaseMs(t, c.Snapshot(10).StreamFlowMs, 1000)
	requirePhaseMs(t, c.Snapshot(0).StreamFlowMs, 1500)
	c.Observe(nil, streamPhaseReadLimit, start)
	if c.Snapshot(0).StreamFlowMs != nil {
		t.Fatal("超过8MB必须为NULL")
	}
	requirePhaseMs(t, c.Snapshot(0).FirstTextMs, 600)
	marks := newStreamPhaseCollector(start)
	for i := 0; i <= streamPhaseMarkLimit; i++ {
		marks.Observe(&ParseResult{ContentKind: ContentTool, ContentBytes: 1}, 0, start.Add(time.Duration(i)*time.Millisecond))
	}
	if marks.Snapshot(0).StreamFlowMs != nil {
		t.Fatal("超过65536采样点必须为NULL")
	}
	marks.Observe(&ParseResult{ContentKind: ContentText, ContentBytes: 1}, 0, start.Add(time.Hour))
	requirePhaseMs(t, marks.Snapshot(0).FirstTextMs, 3600000)
}

type phaseReplay struct {
	At                                        []int64 `json:"at_ms"`
	FirstContentMs, FirstTextMs, StreamFlowMs int64
	FirstReasoningMs                          *int64
	ReasoningHidden                           bool
}

func phaseFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile("../../tests/testdata/stream_phases/" + name + ".sse")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func replayPhaseFixture(t *testing.T, name string, times []int64) tracking.StreamPhases {
	t.Helper()
	parser, start := NewTokenParser(), time.Unix(0, 0)
	c := newStreamPhaseCollector(start)
	events := strings.Split(strings.TrimSpace(phaseFixture(t, name)), "\n\n")
	if len(events) != len(times) {
		t.Fatalf("事件数=%d, 时间点=%d", len(events), len(times))
	}
	for i, event := range events {
		for _, line := range strings.Split(event+"\n", "\n") {
			c.Observe(parser.ParseSSELineV2(line), len(line)+1, start.Add(time.Duration(times[i])*time.Millisecond))
		}
	}
	return c.Snapshot(streamReasoningTokens(parser.GetResponseReasoning()))
}

func TestStreamPhases_SyntheticSSEFixtureReplay(t *testing.T) {
	data, err := os.ReadFile("../../tests/testdata/stream_phases/replay_timing.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures map[string]json.RawMessage
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claude_thinking", "codex_hidden_reasoning"} {
		var expected phaseReplay
		if err := json.Unmarshal(fixtures[name], &expected); err != nil {
			t.Fatal(err)
		}
		p := replayPhaseFixture(t, name, expected.At)
		requirePhaseMs(t, p.FirstContentMs, expected.FirstContentMs)
		requirePhaseMs(t, p.FirstTextMs, expected.FirstTextMs)
		requirePhaseMs(t, p.StreamFlowMs, expected.StreamFlowMs)
		if !reflect.DeepEqual(p.FirstReasoningMs, expected.FirstReasoningMs) || p.ReasoningHidden == nil || *p.ReasoningHidden != expected.ReasoningHidden {
			t.Fatalf("%s 思考观测: %+v", name, p)
		}
		raw, _ := json.Marshal(p)
		t.Logf("%s: %s", name, raw)
	}
}

func TestStreamPhases_ProcessorCountsRawBytesAndFlushesContent(t *testing.T) {
	collector := newStreamPhaseCollector(time.Now())
	w := httptest.NewRecorder()
	sp := NewStreamProcessor(NewTokenParser(), nil, w, w, "synthetic-cap", "synthetic")
	sp.SetStreamPhaseObserver(func(result *ParseResult, n int, at time.Time) { collector.Observe(result, n, at) })
	// 行首空白在旧解析器会被TrimSpace；读取上限必须在裁剪前计算。
	body := strings.Repeat(" ", streamPhaseReadLimit+1) + "\n" + strings.TrimSpace(phaseFixture(t, "codex_hidden_reasoning"))
	usage, _, err := sp.ProcessStreamWithRetry(context.Background(), &http.Response{
		Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body)),
	})
	if err != nil || usage == nil || usage.OutputTokens != 500 {
		t.Fatalf("边界观测不得改变转发/用量: %+v %v", usage, err)
	}
	p := collector.Snapshot(0)
	if p.FirstContentMs == nil || p.FirstTextMs == nil || p.StreamFlowMs != nil {
		t.Fatalf("raw上限或flush错误: %+v", p)
	}
}
