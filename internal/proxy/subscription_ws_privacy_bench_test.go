package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"sinuo/internal/privacy"
	"sinuo/internal/proxy/subscriptionws"
)

// 每轮包含 create、完整上游事件及同步交付；首轮核对透传字节或跨帧重组的工具正文。
func BenchmarkSubscriptionWSPrivacy(b *testing.B) {
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.DiscardHandler))
	defer slog.SetDefault(prev)
	settings := privacy.DefaultSettings()
	settings.Mode, settings.ResponseRestoreEnabled = privacy.ModeRedact, true
	filter := liveRestoreFilter{&privacy.Snapshot{Version: 1, Dict: privacy.NewRestoreDict(), RestoreKey: make([]byte, 32), Settings: settings}}
	ctx := context.Background()
	create := []byte(`{"type":"response.create","model":"bench","input":"ordinary"}`)
	const line = "if (path === \"C:\\\\tmp\\\\code\") {\n\treturn JSON.stringify({text: \"value\"});\n}\n"
	code := strings.Repeat(line, (256<<10)/len(line)+1)[:256<<10]
	toolJSON, err := json.Marshal(map[string]any{"commands": []any{map[string]string{"code": code}}})
	if err != nil {
		b.Fatal(err)
	}
	for _, tc := range []struct {
		name, content string
		chunks        int
		tool          bool
	}{
		{"short128", strings.Repeat("ordinary visible content", 128), 128, false},
		{"plain256k", strings.Repeat("a", 256<<10), 1, false},
		{"code256k", code, 1, false},
		{"tool256k", string(toolJSON), 1, true},
		{"tool256k-split", string(toolJSON), 128, true},
	} {
		frames := wsPrivacyBenchFrames(b, tc.content, tc.chunks, tc.tool)
		b.Run(tc.name, func(b *testing.B) {
			total := 0
			for _, f := range frames {
				total += len(f)
			}
			run := func(check bool) {
				var assembled strings.Builder
				state, _ := activitySubmissionState()
				defer state.close("closed")
				i := &subscriptionWSInterceptor{filter: filter, submissions: state}
				deliver := func(dir subscriptionws.Direction, raw []byte) {
					r, e := i.Intercept(ctx, dir, raw)
					if e != nil || r.Reply != nil {
						b.Fatalf("帧被拒绝: %v", e)
					}
					calls := 0
					send := func(out []byte) error {
						calls++
						if check && tc.tool && tc.chunks > 1 && bytes.Contains(raw, []byte(`"type":"response.function_call_arguments.delta"`)) {
							// 正好切在反斜杠处时，未闭合的转义会延迟到下一帧交付。
							var before, after map[string]any
							if json.Unmarshal(raw, &before) != nil || json.Unmarshal(out, &after) != nil {
								b.Fatal("无效工具帧")
							}
							piece, ok := after["delta"].(string)
							if !ok {
								b.Fatal("缺少工具增量")
							}
							assembled.WriteString(piece)
							delete(before, "delta")
							delete(after, "delta")
							if !reflect.DeepEqual(before, after) {
								b.Fatal("工具增量路由或序号改变")
							}
						} else if check && !bytes.Equal(raw, out) {
							b.Fatal("未命中帧发生改变")
						}
						return nil
					}
					if r.Deliver != nil {
						if e := r.Deliver(send, nil); e != nil {
							b.Fatal(e)
						}
					} else if r.Forward != nil {
						_ = send(r.Forward)
					}
					if calls != 1 {
						b.Fatal("交付次数错误", calls)
					}
					if r.Forwarded != nil {
						r.Forwarded()
					}
					if dir == subscriptionws.DirectionDownstreamToUpstream {
						i.Forwarded(dir, int64(len(raw)), time.Now())
					}
				}
				deliver(subscriptionws.DirectionDownstreamToUpstream, create)
				if check && state.restoring.Load() != 1 {
					b.Fatal("未启用回填")
				}
				for _, raw := range frames {
					deliver(subscriptionws.DirectionUpstreamToDownstream, raw)
				}
				if check && tc.tool && tc.chunks > 1 && assembled.String() != tc.content {
					b.Fatal("工具参数拼接不一致")
				}
			}
			run(true)
			b.ReportAllocs()
			b.SetBytes(int64(total))
			for b.Loop() {
				run(false)
			}
		})
	}
}

func wsPrivacyBenchFrames(b *testing.B, content string, chunks int, tool bool) [][]byte {
	b.Helper()
	frames := [][]byte{[]byte(`{"type":"response.created","response":{"id":"r","output":[]},"sequence_number":0}`)}
	quote := func(s string) string {
		p, e := json.Marshal(s)
		if e != nil {
			b.Fatal(e)
		}
		return string(p)
	}
	var delta, done, item string
	if tool {
		delta = "response.function_call_arguments.delta"
		done = "response.function_call_arguments.done"
		item = `{"id":"m","type":"function_call","name":"example","call_id":"c","arguments":` + quote(content) + `}`
	} else {
		delta = "response.output_text.delta"
		done = "response.output_text.done"
		item = `{"id":"m","type":"message","content":[{"type":"output_text","text":` + quote(content) + `}]}`
	}
	for j := range chunks {
		piece := content[j*len(content)/chunks : (j+1)*len(content)/chunks]
		frames = append(frames, []byte(fmt.Sprintf(`{"type":%q,"sequence_number":%d,"response_id":"r","item_id":"m","output_index":0,"content_index":0,"delta":%s}`, delta, j+1, quote(piece))))
	}
	field := "text"
	if tool {
		field = "arguments"
	}
	return append(frames, []byte(fmt.Sprintf(`{"type":%q,"sequence_number":%d,"response_id":"r","item_id":"m","output_index":0,"content_index":0,"%s":%s}`, done, chunks+1, field, quote(content))),
		[]byte(fmt.Sprintf(`{"type":"response.completed","sequence_number":%d,"response":{"id":"r","status":"completed","output":[%s],"usage":{"input_tokens":8,"output_tokens":128}}}`, chunks+2, item)))
}
