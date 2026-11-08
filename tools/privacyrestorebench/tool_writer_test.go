// 与吞吐基准复用生产恢复器；覆盖不能按整属性处理的合法分片和本地拒绝路径。
package privacyrestorebench_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"sinuo/internal/proxy"
	"sinuo/internal/proxy/restorestream"
)

type shortResponseWriter struct {
	*httptest.ResponseRecorder
	short bool
}

func (w *shortResponseWriter) Write(p []byte) (int, error) {
	if w.short {
		return w.ResponseRecorder.Write(p[:len(p)-1])
	}
	return w.ResponseRecorder.Write(p)
}

func TestDeliveredCounts(t *testing.T) {
	for _, codex := range []bool{false, true} {
		for _, c := range []struct {
			events [][]byte
			want   restorestream.DeliveryCounts
		}{
			{textEvents(codex), restorestream.DeliveryCounts{Text: 8}},
			{makeEvents(codex), restorestream.DeliveryCounts{Tool: 8}},
		} {
			w := restoring(httptest.NewRecorder(), codex)
			defer w.Release()
			for _, ev := range c.events {
				if _, err := w.Write(ev); err != nil {
					t.Fatal(err)
				}
			}
			if err := w.Finish(); err != nil {
				t.Fatal(err)
			}
			if got := w.Delivered(); got != c.want {
				t.Fatalf("codex=%t 交付计数 %+v，期望 %+v", codex, got, c.want)
			}
		}
	}
}

func TestDeliveryDoesNotCountUnwrittenEvent(t *testing.T) {
	for _, short := range []bool{false, true} {
		dst := &shortResponseWriter{ResponseRecorder: httptest.NewRecorder()}
		w := restoring(dst, true)
		defer w.Release()
		frame := func(s string, seq int) []byte {
			return data(fmt.Sprintf(`{"type":"response.custom_tool_call_input.delta","output_index":0,"delta":%s,"sequence_number":%d}`, jsonString(s), seq))
		}
		if _, err := w.Write(frame(productionToken, 0)); err != nil {
			t.Fatal(err)
		}
		next := productionToken + " {{SNU_unknown}}"
		target := restorestream.ErrDelivery
		if short {
			dst.short = true
			next = productionToken
			target = io.ErrShortWrite
		}
		ev := frame(next, 1)
		if !short {
			ev = bytes.Replace(ev, []byte(`"output_index":0`), []byte(`"output_index":0,"output_index":0`), 1)
		}
		if _, err := w.Write(ev); !errors.Is(err, target) {
			t.Fatal(err)
		}
		if got := w.Delivered(); got != (restorestream.DeliveryCounts{Tool: 1}) {
			t.Fatal("错误事件被计入写出", got)
		}
	}
}

func TestObservationMatchesOriginalStream(t *testing.T) {
	for _, codex := range []bool{false, true} {
		tools := eventsForParts(codex, []string{`{"one":"x",`, `"two":"` + productionToken[:10], productionToken[10:] + `",`, `"three":"42"}`})
		for _, events := range [][][]byte{tools, textEvents(codex)} {
			var reference []*proxy.ParseResult
			for _, on := range []bool{false, true} {
				rec := httptest.NewRecorder()
				var dst http.ResponseWriter = rec
				if on {
					w := restoring(dst, codex)
					defer w.Release()
					dst = w
				}
				sp := proxy.NewStreamProcessor(proxy.NewTokenParser(), nil, dst, rec, "observation", "local")
				var observed []*proxy.ParseResult
				sp.SetStreamPhaseObserver(func(r *proxy.ParseResult, _ int, _ time.Time) {
					observed = append(observed, r)
				})
				_, err := sp.ProcessStream(context.Background(), &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(&frameReader{events: events})})
				if err != nil {
					t.Fatal(err)
				}
				if on && !reflect.DeepEqual(reference, observed) {
					t.Fatalf("原始事件阶段或用量发生变化，codex=%t", codex)
				}
				reference = observed
			}
		}
	}
}

func fragmentedReplay(t *testing.T, codex bool, parts []string, bytewise bool) (string, error) {
	t.Helper()
	rec := httptest.NewRecorder()
	w := restoring(rec, codex)
	defer w.Release()
	for _, frame := range eventsForParts(codex, parts) {
		frame = bytes.ReplaceAll(frame, []byte("\n"), []byte("\r\n"))
		for len(frame) != 0 {
			n := len(frame)
			if bytewise {
				n = 1
			}
			if _, err := w.Write(frame[:n]); err != nil {
				return rec.Body.String(), err
			}
			frame = frame[n:]
		}
	}
	return rec.Body.String(), w.Finish()
}

func TestNestedAndFragmentedToolArguments(t *testing.T) {
	raw := `{"first":"plain","nested":[{"` + productionToken + `":"prefix ` + strings.Replace(productionToken, "{{", `\u007b{`, 1) + ` \uD83D\uDE80"}], "other":42}`
	want := map[string]any{"first": "plain", "nested": []any{map[string]any{productionToken: "prefix " + value + " 🚀"}}, "other": 42.0}
	for _, codex := range []bool{false, true} {
		for cut := 1; cut < len(raw); cut++ {
			wire, err := fragmentedReplay(t, codex, []string{raw[:cut], raw[cut:]}, cut%17 == 0)
			if err != nil {
				t.Fatalf("codex=%v cut=%d: %v", codex, cut, err)
			}
			var assembled strings.Builder
			for _, line := range strings.Split(wire, "\n") {
				if !strings.HasPrefix(line, "data:") {
					continue
				}
				data := strings.TrimSpace(line[5:])
				switch gjson.Get(data, "type").Str {
				case "content_block_delta":
					assembled.WriteString(gjson.Get(data, "delta.partial_json").Str)
				case "response.function_call_arguments.delta":
					assembled.WriteString(gjson.Get(data, "delta").Str)
				case "response.completed":
					if gjson.Get(data, "response.output.0.arguments").Str != assembled.String() {
						t.Fatal("终态与分片不一致", cut)
					}
				}
			}
			var got map[string]any
			if err := json.Unmarshal([]byte(assembled.String()), &got); err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("codex=%v cut=%d got=%v err=%v", codex, cut, got, err)
			}
		}
	}
}

func TestRejectIncompleteToolBeforeSuccess(t *testing.T) {
	for _, codex := range []bool{false, true} {
		for _, parts := range [][]string{
			{`{"simple":"done",`, `"simple"`, `:"again"}`},
			{`{"s":"z`, `\uD800"}`},
			{`{"s":"ok"`, `]`},
		} {
			wire, e := fragmentedReplay(t, codex, parts, true)
			if !errors.Is(e, restorestream.ErrDelivery) || strings.Contains(wire, `"type":"response.completed"`) || strings.Contains(wire, `"type":"content_block_stop"`) {
				t.Fatal("应在工具完成前终止", codex, parts, e)
			}
		}
	}
}
func TestPreserveMetadataWhenItDuplicatesData(t *testing.T) {
	for _, codex := range []bool{false, true} {
		ev := eventsForParts(codex, []string{`{"one":"` + productionToken + `"}`})
		rec := httptest.NewRecorder()
		w := restoring(rec, codex)
		defer w.Release()
		for i, frame := range ev {
			if i == 2 || codex && i >= 3 {
				_, raw, _ := bytes.Cut(frame, []byte("data: "))
				id := append([]byte("id: "), bytes.TrimSuffix(raw, []byte("\n\n"))...)
				frame = append(append(bytes.Clone(id), '\n'), frame...)
				if _, e := w.Write(frame); e != nil {
					t.Fatal(e)
				}
				if !bytes.Contains(rec.Body.Bytes(), append(id, '\n')) {
					t.Fatal("SSE id 发生变化", i)
				}
			} else if _, e := w.Write(frame); e != nil {
				t.Fatal(e)
			}
		}
		if e := w.Finish(); e != nil {
			t.Fatal(e)
		}
	}
}

func TestTextTailAndSequence(t *testing.T) {
	rec := httptest.NewRecorder()
	w := restoring(rec, true)
	defer w.Release()
	delta := func(v string, seq int) []byte {
		return data(fmt.Sprintf(`{"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"i","delta":%s,"sequence_number":%d}`, jsonString(v), seq))
	}
	_, e := w.Write(delta("start {", 0))
	if e != nil || !strings.Contains(rec.Body.String(), `"delta":"start "`) {
		t.Fatal("安全前缀未即时交付", e)
	}
	for _, frame := range [][]byte{data(`{"type":"response.output_text.done","output_index":0,"content_index":0,"item_id":"i","text":"start {","sequence_number":1}`), data(`{"type":"response.completed","response":{"output":[]},"sequence_number":2}`)} {
		if _, e = w.Write(frame); e != nil {
			t.Fatal(e)
		}
	}
	if e = w.Finish(); e != nil {
		t.Fatal(e)
	}
	var seq int64
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		v := gjson.Get(line[5:], "sequence_number")
		if !v.Exists() || v.Int() != seq {
			t.Fatal(seq, line)
		}
		if seq == 1 && gjson.Get(line[5:], "delta").Str != "{" {
			t.Fatal("遗漏合法尾部")
		}
		seq++
	}
	if seq != 4 {
		t.Fatal(seq)
	}
}

func TestInterleavedCustomToolsAndSnapshotIdentity(t *testing.T) {
	rec := httptest.NewRecorder()
	w := restoring(rec, true)
	defer w.Release()
	write := func(i int, id, typ, field, part string, seq int) error {
		ev := data(fmt.Sprintf(`{"type":%q,"output_index":%d,"item_id":%q,"call_id":%q,%q:%s,"sequence_number":%d}`, typ, i, id, "c"+id, field, jsonString(part), seq))
		_, err := w.Write(ev)
		return err
	}
	parts := []string{productionToken[:10], "ordinary ", productionToken[10:], "input"}
	for n, p := range parts {
		idx := n % 2
		if err := write(idx, fmt.Sprint(idx), "response.custom_tool_call_input.delta", "delta", p, n); err != nil {
			t.Fatal(err)
		}
	}
	for i, raw := range []string{productionToken, "ordinary input"} {
		if err := write(i, fmt.Sprint(i), "response.custom_tool_call_input.done", "input", raw, 4+i); err != nil {
			t.Fatal(err)
		}
	}
	terminal := []map[string]any{{"id": "0", "call_id": "c0", "type": "custom_tool_call", "input": productionToken}, {"id": "1", "call_id": "c1", "type": "custom_tool_call", "input": "ordinary input"}}
	raw, _ := json.Marshal(map[string]any{"type": "response.completed", "sequence_number": 6, "response": map[string]any{"output": terminal}})
	if _, err := w.Write(data(string(raw))); err != nil {
		t.Fatal(err)
	}
	if err := w.Finish(); err != nil {
		t.Fatal(err)
	}
	var got [2]strings.Builder
	for _, l := range strings.Split(rec.Body.String(), "\n") {
		if !strings.HasPrefix(l, "data:") {
			continue
		}
		raw := l[5:]
		switch gjson.Get(raw, "type").Str {
		case "response.custom_tool_call_input.delta":
			got[gjson.Get(raw, "output_index").Int()].WriteString(gjson.Get(raw, "delta").Str)
		case "response.completed":
			if gjson.Get(raw, "response.output.0.input").Str != value || gjson.Get(raw, "response.output.1.input").Str != "ordinary input" {
				t.Fatal("终态还原错误", raw)
			}
		}
	}
	if got[0].String() != value || got[1].String() != "ordinary input" {
		t.Fatal("交错的工具上下文混合")
	}

	other := restoring(httptest.NewRecorder(), true)
	defer other.Release()
	for i, id := range []string{"a", "b"} {
		_, err := other.Write(data(fmt.Sprintf(`{"type":"response.custom_tool_call_input.delta","output_index":0,"item_id":"item","call_id":%q,"delta":"part","sequence_number":%d}`, id, i)))
		if (i == 0 && err != nil) || (i == 1 && !errors.Is(err, restorestream.ErrDelivery)) {
			t.Fatal("未隔离调用 ID", i, err)
		}
	}
}

func TestUnknownAndTruncatedTextMarkers(t *testing.T) {
	unknownToken := fixtureRestoreToken("0123456789abcdef0123456789abcde0")
	for _, s := range []string{unknownToken, "{{SNU_0123456"} {
		rec := httptest.NewRecorder()
		w := restoring(rec, true)
		ev := data(fmt.Sprintf(`{"type":"response.output_text.done","output_index":0,"content_index":0,"text":%s,"sequence_number":1}`, jsonString(s)))
		_, err := w.Write(ev)
		if err != nil || w.Delivered().UnknownText != 1 || !strings.Contains(rec.Body.String(), s) {
			t.Fatal("未知或截断标识未原样交付并计数", err)
		}
		w.Release()
	}
}

func TestBytewiseMixedKnownUnknownAndTruncated(t *testing.T) {
	unknown := fixtureRestoreToken(strings.Repeat("b", 32))
	raw := productionToken + " " + unknown + " {{SNU_abc"
	want := value + " " + unknown + " {{SNU_abc"
	for _, role := range []string{"function", "text", "custom"} {
		for _, codex := range []bool{false, true} {
			if role == "custom" && !codex {
				continue
			}
			var events [][]byte
			s := raw
			if role == "function" {
				s = `{"value":` + jsonString(raw) + `}`
				events = eventsForParts(codex, strings.Split(s, ""))
			} else if codex {
				kind, field, extra := "output_text", "text", `,"content_index":0`
				if role == "custom" {
					kind, field, extra = "custom_tool_call_input", "input", ""
				}
				for i, ch := range strings.Split(raw, "") {
					events = append(events, data(fmt.Sprintf(`{"type":"response.%s.delta","output_index":0%s,"delta":%s,"sequence_number":%d}`, kind, extra, jsonString(ch), i)))
				}
				events = append(events, data(fmt.Sprintf(`{"type":"response.%s.done","output_index":0%s,"%s":%s,"sequence_number":%d}`, kind, extra, field, jsonString(raw), len(s))))
				item := `{"type":"message","content":[{"type":"output_text","text":` + jsonString(raw) + `}]}`
				if role == "custom" {
					item = `{"type":"custom_tool_call","input":` + jsonString(raw) + `}`
				}
				events = append(events, data(fmt.Sprintf(`{"type":"response.completed","response":{"output":[%s]},"sequence_number":%d}`, item, len(s)+1)))
			} else {
				events = textEvents(false)[:2:2]
				for _, ch := range strings.Split(raw, "") {
					events = append(events, data(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":`+jsonString(ch)+`}}`))
				}
				events = append(events, data(`{"type":"content_block_stop","index":0}`), data(`{"type":"message_stop"}`))
			}
			dst := httptest.NewRecorder()
			w := restoring(dst, codex)
			for _, ev := range events {
				for i := range ev {
					if _, err := w.Write(ev[i : i+1]); err != nil {
						t.Fatal(role, codex, err)
					}
				}
			}
			if err := w.Finish(); err != nil {
				t.Fatal(role, codex, err)
			}
			counts := restorestream.DeliveryCounts{Tool: 1, UnknownTool: 2}
			if role == "text" {
				counts = restorestream.DeliveryCounts{Text: 1, UnknownText: 2}
			}
			if w.Delivered() != counts {
				t.Fatal(role, codex, w.Delivered())
			}
			var deltas strings.Builder
			for _, line := range strings.Split(dst.Body.String(), "\n") {
				if !strings.HasPrefix(line, "data:") {
					continue
				}
				ev := gjson.Parse(line[5:])
				switch ev.Get("type").Str {
				case "content_block_delta":
					if role == "function" {
						deltas.WriteString(ev.Get("delta.partial_json").Str)
					} else {
						deltas.WriteString(ev.Get("delta.text").Str)
					}
				case "response.output_text.delta", "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
					deltas.WriteString(ev.Get("delta").Str)
				case "response.function_call_arguments.done", "response.output_text.done", "response.custom_tool_call_input.done":
					field := map[string]string{"function": "arguments", "text": "text", "custom": "input"}[role]
					if ev.Get(field).Str != deltas.String() {
						t.Fatal(role, "快照与实际交付的增量不一致")
					}
				}
			}
			actual := deltas.String()
			if role == "function" {
				actual = gjson.Get(actual, "value").Str
			}
			if actual != want {
				t.Fatal(role, codex, actual)
			}
			w.Release()
		}
	}
}

func TestUnfinishedDeclaredTool(t *testing.T) {
	for _, item := range []string{`{"type":"function_call","arguments":""}`, `{"type":"custom_tool_call","input":""}`} {
		rec := httptest.NewRecorder()
		w := restoring(rec, true)
		if _, err := w.Write(data(`{"type":"response.output_item.added","output_index":0,"item":` + item + `}`)); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data(`{"type":"response.completed","response":{"output":[]}}`)); !errors.Is(err, restorestream.ErrDelivery) || strings.Contains(rec.Body.String(), "response.completed") {
			t.Fatal("工具调用尚未收尾却交付成功", err)
		}
		w.Release()
	}
}
