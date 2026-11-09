package privacyrestorebench_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"sinuo/internal/proxy/restorestream"
)

// 256 KiB 代码分成 128 段，每 16 段让一个占位符跨越帧边界。
func largeCodeParts() []string {
	const line = "if (path == \"C:\\\\tmp\") {\n\treturn \"example\";\n}\n"
	code := strings.Repeat(line, 2048/len(line)+1)[:2048]
	parts := make([]string, deltasPerStream)
	for i := range parts {
		s := code
		if i%16 == 0 {
			s += " " + productionToken + "."
		}
		parts[i] = fmt.Sprintf(`"k%d":%s`, i, jsonString(s))
		if i == 0 {
			parts[i] = "{" + parts[i]
		}
		if i == len(parts)-1 {
			parts[i] += "}"
		} else {
			parts[i] += ","
		}
	}
	for i := 0; i < len(parts); i += 16 {
		cut := strings.Index(parts[i], productionToken) + 11
		parts[i+1] = parts[i][cut:] + parts[i+1]
		parts[i] = parts[i][:cut]
	}
	return parts
}

// 只计响应写侧。WS 消息在读侧已经校验，SSE 写侧接收原始事件。
func BenchmarkLargeCodeToolOutput(b *testing.B) {
	dict := seedBenchDictionary(b)
	parts := largeCodeParts()
	for _, proto := range []string{"WebSocket", "CodexSSE", "ClaudeSSE"} {
		b.Run(proto, func(b *testing.B) {
			codex := proto != "ClaudeSSE"
			events := eventsForParts(codex, parts)
			var messages []restorestream.ValidatedMessage
			if proto == "WebSocket" {
				for _, event := range events {
					raw := bytes.TrimSpace(bytes.TrimPrefix(event, []byte("data: ")))
					msg, err := restorestream.ValidateMessage(raw)
					if err != nil {
						b.Fatal(err)
					}
					messages = append(messages, msg)
				}
			}
			b.ReportAllocs()
			for b.Loop() {
				dst := &sink{}
				w, view, err := benchResponseWriter(dst, codex, dict)
				if err != nil {
					b.Fatal(err)
				}
				if proto == "WebSocket" {
					send := func(p []byte) error {
						_, err := dst.Write(p)
						return err
					}
					for _, msg := range messages {
						if err := w.WriteValidatedMessage(msg, send); err != nil {
							b.Fatal(err)
						}
					}
				} else {
					for _, event := range events {
						if _, err := w.Write(event); err != nil {
							b.Fatal(err)
						}
						w.Flush()
					}
					if err := w.Finish(); err != nil {
						b.Fatal(err)
					}
				}
				if !w.Terminal() || w.Delivered() != (restorestream.DeliveryCounts{Tool: 8}) {
					b.Fatalf("回填未完成: %+v", w.Delivered())
				}
				w.Release()
				view.Release()
			}
		})
	}
}
