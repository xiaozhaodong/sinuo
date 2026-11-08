package restorestream

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"sinuo/internal/privacy"
)

func TestFinishWithTrailingFragment(t *testing.T) {
	for _, tc := range []struct{ path, terminal string }{
		{"/v1/messages", `{"type":"message_stop"}`},
		{"/v1/responses", `{"type":"response.completed","response":{"output":[]}}`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			frame := "data: " + tc.terminal + "\n\n"
			for _, tail := range []string{"\n", "\r\n", ": keepalive", "data: [DO"} {
				for _, split := range []int{0, len(frame) - 1, len(frame)} {
					dst := httptest.NewRecorder()
					w := New(dst, tc.path, nil)
					wire := frame + tail
					for _, part := range []string{wire[:split], wire[split:]} {
						if _, err := io.WriteString(w, part); err != nil {
							t.Fatal(err)
						}
					}
					err := w.Finish()
					w.Release()
					if err != nil || dst.Body.String() != frame {
						t.Fatalf("尾部 %q 分片位置 %d: err=%v, body=%q", tail, split, err, dst.Body.String())
					}
				}
			}
		})
	}
}

func TestFinishRequiresDeliveredTerminal(t *testing.T) {
	const stop = "data: " + `{"type":"message_stop"}` + "\n\n"
	cases := []struct {
		name, wire string
		short      bool
		want       error
	}{
		{"终态尚未收全", stop[:len(stop)-1], false, ErrTruncated},
		{"工具块未关闭", "data: " + `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","input":{}}}` + "\n\n" + stop, false, ErrDelivery},
		{"终态写出失败", stop, true, io.ErrShortWrite},
		{"重复完整终态", stop + stop, false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := New(&failingEventWriter{ResponseRecorder: httptest.NewRecorder(), short: tc.short}, "/v1/messages", nil)
			defer w.Release()
			_, _ = io.WriteString(w, tc.wire)
			if err := w.Finish(); !errors.Is(err, tc.want) {
				t.Fatalf("得到 %v，期望 %v", err, tc.want)
			}
		})
	}
}

func TestTruncatedStreamFlushesHeldDeltas(t *testing.T) {
	const half = "{{SNU_ab"
	for _, codex := range []bool{false, true} {
		for _, tool := range []bool{false, true} {
			t.Run(fmt.Sprintf("codex=%t/tool=%t", codex, tool), func(t *testing.T) {
				path := "/v1/messages"
				if codex {
					path = "/v1/responses"
				}
				dst := httptest.NewRecorder()
				w := New(dst, path, nil)
				defer w.Release()
				event := func(m object) {
					raw, err := json.Marshal(m)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = fmt.Fprintf(w, "data: %s\n\n", raw); err != nil {
						t.Fatal(err)
					}
				}
				kind, delta, property := "text", "text_delta", "text"
				piece := "begin " + half
				if tool {
					kind, delta, property = "tool_use", "input_json_delta", "partial_json"
					piece = `{"value":"` + half
				}
				if codex {
					typ := "response.output_text.delta"
					if tool {
						typ = "response.function_call_arguments.delta"
					}
					event(object{"type": typ, "output_index": 0, "content_index": 0, "item_id": "r", "delta": piece, "sequence_number": 5})
					event(object{"type": "response.in_progress", "sequence_number": 9})
				} else {
					event(object{"type": "content_block_start", "index": 0, "content_block": object{"type": kind, "text": "", "input": object{}}})
					event(object{"type": "content_block_delta", "index": 0, "delta": object{"type": delta, property: piece}})
				}
				if strings.Contains(dst.Body.String(), half) {
					t.Fatal("半截标识提前写出")
				}
				for range 2 {
					if err := w.Finish(); !errors.Is(err, ErrTruncated) {
						t.Fatal(err)
					}
				}
				var got strings.Builder
				for _, line := range strings.Split(dst.Body.String(), "\n") {
					if raw, ok := strings.CutPrefix(line, "data: "); ok {
						field := "delta." + property
						if codex {
							field = "delta"
						}
						part := gjson.Get(raw, field).Str
						got.WriteString(part)
						if codex && part == half && gjson.Get(raw, "sequence_number").Int() != 10 {
							t.Fatal("补发序号错误", raw)
						}
					}
				}
				counts := w.Delivered()
				if got.String() != piece || counts.UnknownText+counts.UnknownTool != 1 || w.Terminal() {
					t.Fatalf("补发错误 %q %+v", got.String(), counts)
				}
			})
		}
	}
}

func TestUntrackedFramesPreserveWire(t *testing.T) {
	for _, tc := range []struct{ path, stop, trailer string }{
		{"/v1/messages", `{"type":"message_stop"}`, `{"type":"ping"}`},
		{"/v1/responses", `{"type":"response.completed","response":{"output":[]}}`, `{"type":"response.in_progress"}`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			dst := httptest.NewRecorder()
			w := New(dst, tc.path, nil)
			defer w.Release()
			wire := "data: {}\n\ndata:\n\nid: 7\r\nevent: ping\r\ndata: keep-alive\r\n\r\n" +
				"data: " + tc.stop + "\n\ndata: " + tc.trailer + "\n\n"
			for _, part := range []string{wire[:8], wire[8:]} {
				if _, err := io.WriteString(w, part); err != nil {
					t.Fatal(err)
				}
			}
			if err := w.Finish(); err != nil || !w.Terminal() || dst.Body.String() != wire || w.Delivered() != (DeliveryCounts{}) {
				t.Fatalf("杂帧或终态交付异常: %v %q %+v", err, dst.Body, w.Delivered())
			}
		})
	}
}

func TestHeartbeatsBetweenTextDeltas(t *testing.T) {
	token := privacy.RestorePrefix + strings.Repeat("a", 32) + "}}"
	for _, tc := range []struct{ path, delta, field, terminal string }{
		{"/v1/messages", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}`, "delta.text", `{"type":"message_stop"}`},
		{"/v1/responses", `{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":%q}`, "delta", `{"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":%q}]}]}}`},
	} {
		for _, splitToken := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/token=%t", tc.path, splitToken), func(t *testing.T) {
				const prefix = "func main() "
				next, want := "\n}", "{\n}"
				var counts DeliveryCounts
				if splitToken {
					next, want = token[1:], "restored"
					counts.Text = 1
				}
				dst := httptest.NewRecorder()
				w := New(dst, tc.path, &recordingDictionary{token: token, original: "restored"})
				defer w.Release()
				writeDelta := func(value string) string {
					t.Helper()
					start := dst.Body.Len()
					if _, err := fmt.Fprintf(w, "data: "+tc.delta+"\n\n", value); err != nil {
						t.Fatal(err)
					}
					frame, err := parseSSEFields(dst.Body.Bytes()[start:], false)
					if err != nil {
						t.Fatal(err)
					}
					return gjson.GetBytes(frame.payload(), tc.field).Str
				}
				if got := writeDelta(prefix + "{"); got != prefix {
					t.Fatalf("暂扣前缀时交付了 %q", got)
				}
				for _, beat := range []string{
					"data: { }\n\n", "data:\n\n", "data: keepalive\n\n",
					"event: ping\r\ndata: alive\r\n\r\n", ": heartbeat\n\n",
					"data: " + `{"type":"ping"}` + "\n\n", "data: " + `{"type":"custom.event"}` + "\n\n",
				} {
					start := dst.Body.Len()
					if _, err := io.WriteString(w, beat); err != nil || dst.Body.String()[start:] != beat {
						t.Fatalf("心跳应立即原样交付: %q %v", beat, err)
					}
				}
				if got := writeDelta(next); got != want {
					t.Fatalf("心跳后的字段 = %q，期望 %q", got, want)
				}
				stop := tc.terminal
				if tc.path == "/v1/responses" {
					stop = fmt.Sprintf(stop, prefix+"{"+next)
				}
				if _, err := fmt.Fprintf(w, "data: %s\n\n", stop); err != nil {
					t.Fatal(err)
				}
				if err := w.Finish(); err != nil || !w.Terminal() || w.Delivered() != counts {
					t.Fatalf("终态或计数异常: %v %+v", err, w.Delivered())
				}
			})
		}
	}
}

func TestMalformedDeltaWithHeldField(t *testing.T) {
	delta := func(value string) string {
		return "data: " + fmt.Sprintf(`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":%q,"sequence_number":1}`, value) + "\n\n"
	}
	bad := strings.Replace(delta("bad"), "bad", `\q`, 1)
	for _, value := range []string{"ordinary", "prefix {"} {
		dst := httptest.NewRecorder()
		w := New(dst, "/v1/responses", nil)
		if _, err := io.WriteString(w, delta(value)); err != nil {
			t.Fatal(err)
		}
		_, err := io.WriteString(w, bad)
		w.Release()
		if value == "ordinary" {
			if err != nil || !strings.HasSuffix(dst.Body.String(), bad) {
				t.Fatal("没有待发片段时应保留上游原帧", err)
			}
		} else if !errors.Is(err, ErrDelivery) || strings.HasSuffix(dst.Body.String(), bad) {
			t.Fatal("损坏的增量事件被当作心跳", err)
		}
	}
}

func TestDoneWithOpenToolRequiresProtocolTerminal(t *testing.T) {
	const done = "data: [DONE]\n\n"
	for _, tail := range []string{"", "{", "{{SNU_ab", `\u00`} {
		t.Run("tail="+tail, func(t *testing.T) {
			value := `{"name":"` + tail
			wire := "data: " + fmt.Sprintf(`{"type":"response.function_call_arguments.delta","output_index":0,"delta":%q,"sequence_number":1}`, value) + "\n\n"
			var eofBody string
			for _, sentinel := range []bool{false, true} {
				dst := httptest.NewRecorder()
				w := New(dst, "/v1/responses", nil)
				defer w.Release()
				if _, err := io.WriteString(w, wire); err != nil {
					t.Fatal(err)
				}
				if sentinel {
					if _, err := io.WriteString(w, done); err != nil || dst.Body.String() != eofBody+done {
						t.Fatalf("[DONE] 前补发与 EOF 不一致: %v %q", err, dst.Body)
					}
				}
				if err := w.Finish(); !errors.Is(err, ErrTruncated) || w.Terminal() {
					t.Fatal("未闭合工具应按截断处理", err)
				}
				if !sentinel {
					eofBody = dst.Body.String()
				} else if dst.Body.String() != eofBody+done {
					t.Fatal("Finish 重复补发了尾部")
				}
			}
		})
	}
}

func TestDoneAfterUpstreamErrorWithHeldToolTail(t *testing.T) {
	dst := httptest.NewRecorder()
	w := New(dst, "/v1/responses", nil)
	defer w.Release()
	first := "data: " + fmt.Sprintf(`{"type":"response.function_call_arguments.delta","output_index":0,"delta":%q}`, `{"code":"{`) + "\n\n"
	if _, err := io.WriteString(w, first); err != nil {
		t.Fatal(err)
	}
	start := dst.Body.Len()
	end := "data: " + `{"type":"response.failed","response":{"status":"failed"}}` + "\n\ndata: [DONE] \r\n\r\n"
	if _, err := io.WriteString(w, end); err != nil {
		t.Fatal(err)
	}
	if err := w.Finish(); err != nil || !w.Terminal() || dst.Body.String()[start:] != end {
		t.Fatalf("上游错误终态后应只交付结束标记: %v %q", err, dst.Body)
	}
}

func TestClaudeNonstandardTextFrames(t *testing.T) {
	token := privacy.RestorePrefix + strings.Repeat("a", 32) + "}}"
	d := &recordingDictionary{token: token, original: "restored"}
	initial := func(value string) string {
		return "data: " + fmt.Sprintf(`{"type":"message_start","message":{"content":[{"type":"text","text":%q}]}}`, value) + "\n\n"
	}
	start := "data: " + `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n"
	stop := "data: " + `{"type":"message_stop"}` + "\n\n"
	for _, value := range []string{"clean", token} {
		dst := httptest.NewRecorder()
		w := New(dst, "/v1/messages", d)
		wire := initial(value) + start + stop
		_, err := io.WriteString(w, wire)
		if err == nil {
			err = w.Finish()
		}
		w.Release()
		if err != nil || !w.Terminal() {
			t.Fatal("普通文本上游未关闭块", err)
		}
		if value == "clean" && dst.Body.String() != wire {
			t.Fatal("干净事件发生改写")
		}
		if value == token && (strings.Contains(dst.Body.String(), token) || !strings.Contains(dst.Body.String(), "restored") || w.Delivered().Text != 1) {
			t.Fatal("初始快照未回填")
		}
	}
}

func TestClaudeInitialContentContinuesAcrossDelta(t *testing.T) {
	token := privacy.RestorePrefix + strings.Repeat("b", 32) + "}}"
	d := &recordingDictionary{token: token, original: "restored"}
	dst := httptest.NewRecorder()
	w := New(dst, "/v1/messages", d)
	defer w.Release()
	for _, raw := range []string{
		fmt.Sprintf(`{"type":"message_start","message":{"content":[{"type":"text","text":%q}]}}`, token[:10]),
		fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}`, token[10:]),
		`{"type":"message_stop"}`,
	} {
		if _, err := fmt.Fprintf(w, "data: %s\n\n", raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Finish(); err != nil || d.text != 1 || !strings.Contains(dst.Body.String(), `"text":"restored"`) {
		t.Fatal("初始快照跨片回填失败", err)
	}
}
