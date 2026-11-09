package proxy

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sinuo/internal/endpoint"
	"sinuo/internal/privacy"
	"sinuo/internal/tracking"

	"github.com/tidwall/gjson"
)

func TestSubscriptionHTTPRestore(t *testing.T) {
	for _, codex := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("codex=%t/stream=%t", codex, stream), func(t *testing.T) {
				body := strings.Replace(routeBody(codex), `"stream":true`, fmt.Sprintf(`"stream":%t`, stream), 1)
				serve := func(w http.ResponseWriter, r *http.Request, sent []byte) {
					token := wireToken.Find(sent)
					if token == nil || bytes.Contains(sent, []byte("private-quote")) || r.Header.Get("Authorization") != "Bearer synthetic-subscription" {
						t.Error("订阅请求的脱敏或凭据有误")
						http.Error(w, "fixture", 400)
						return
					}
					w.Header().Set("X-RateLimit-Test", "123")
					w.Header().Set("ETag", "original")
					w.Header().Set("Content-Encoding", "gzip")
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						if codex {
							// 官方兼容路径可能省略 Content-Type。
							w.Header()["Content-Type"] = nil
						}
					} else {
						w.Header().Set("Content-Type", "application/json")
					}
					z := gzip.NewWriter(w)
					if stream {
						if codex {
							io.WriteString(z, "\ufeff: keepalive\n\n")
						}
						fmt.Fprint(z, routeSSE(string(token), codex))
					} else if codex {
						fmt.Fprintf(z, `{"id":"r","model":"gpt-test","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":%q}]},{"type":"function_call","arguments":%q}],"usage":{"input_tokens":10,"output_tokens":11}}`, token, fmt.Sprintf(`{"value":%q}`, token))
					} else {
						fmt.Fprintf(z, `{"type":"message","model":"claude-test","content":[{"type":"text","text":%q},{"type":"tool_use","input":{"value":%q}}],"usage":{"input_tokens":10,"output_tokens":11}}`, token, token)
					}
					if err := z.Close(); err != nil {
						t.Error(err)
					}
				}
				var header http.Header
				var output []byte
				var tracker *tracking.UsageTracker
				filter := newRestoreTestFilter(t)
				if codex {
					var env *subscriptionTestEnv
					env = newSubscriptionTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
						requests := env.upstreamRequests()
						serve(w, r, requests[len(requests)-1].Body)
					}, "subscription")
					tracker = newLifecyclePanelTestTracker(t)
					env.handler.proxy.SetUsageTracker(tracker)
					env.handler.proxy.SetPrivacyFilter(filter)
					resp := env.request("POST", "responses", func(r *http.Request) {
						r.Body = io.NopCloser(strings.NewReader(body))
						r.ContentLength = int64(len(body))
						r.Header.Set("Content-Type", "application/json")
						r.Header.Set("Authorization", "Bearer synthetic-subscription")
					})
					defer resp.Body.Close()
					var err error
					output, err = io.ReadAll(resp.Body)
					if err != nil || resp.StatusCode != 200 {
						t.Fatal(resp.StatusCode, err, string(output))
					}
					header = resp.Header
				} else {
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						sent, _ := io.ReadAll(r.Body)
						serve(w, r, sent)
					}))
					defer server.Close()
					h, _, tr := newClaudeSubscriptionTestHandler(t, server.URL)
					tracker = tr
					h.SetPrivacyFilter(filter)
					r := privacySummaryRequest("/v1/messages", body, "subscription-restore")
					r = r.WithContext(endpoint.WithClaudeCodeEntry(r.Context(), endpoint.ClaudeCodeEntryRequest{Mode: endpoint.ClaudeCodeEntryModeSubscription}))
					r.Header.Set("Authorization", "Bearer synthetic-subscription")
					w := httptest.NewRecorder()
					h.ServeHTTP(w, r)
					output, header = w.Body.Bytes(), w.Header()
					if w.Code != 200 {
						t.Fatal(w.Code, string(output))
					}
				}
				if bytes.Contains(output, []byte(privacy.RestorePrefix)) || header.Get("Content-Encoding") != "" || header.Get("ETag") != "" || header.Get("X-RateLimit-Test") != "123" {
					t.Fatalf("下游表示或响应头错误: %v %s", header, output)
				}
				if stream {
					var visible strings.Builder
					for _, line := range strings.Split(string(output), "\n") {
						raw, ok := strings.CutPrefix(line, "data: ")
						if !ok {
							continue
						}
						switch gjson.Get(raw, "type").Str {
						case "response.output_text.delta":
							visible.WriteString(gjson.Get(raw, "delta").Str)
						case "content_block_delta":
							visible.WriteString(gjson.Get(raw, "delta.text").Str)
						}
					}
					if visible.String() != "before "+routeTestValue+" after" {
						t.Fatalf("增量正文错误: %q", visible.String())
					}
				} else if codex {
					args := gjson.GetBytes(output, "output.1.arguments").Str
					if !json.Valid([]byte(args)) || gjson.Get(args, "value").Str != routeTestValue || gjson.GetBytes(output, "output.0.content.0.text").Str != routeTestValue {
						t.Fatal(string(output))
					}
				} else if gjson.GetBytes(output, "content.0.text").Str != routeTestValue || gjson.GetBytes(output, "content.1.input.value").Str != routeTestValue {
					t.Fatal(string(output))
				}
				data := onlyPhaseRequest(t, tracker)
				delivery := gjson.Get(data.PrivacyScanJSON, "restore")
				if data.Detail.Status != "completed" || data.Detail.OutputTokens != 11 || delivery.Get("text").Int() != 1 || delivery.Get("status").Str != "completed" || !stream && delivery.Get("tool").Int() != 1 {
					t.Fatalf("结算或回填摘要异常: %s %d %s", data.Detail.Status, data.Detail.OutputTokens, delivery.Raw)
				}
			})
		}
	}
}

func TestSubscriptionHTTPRestoreRejectsInvalidDelivery(t *testing.T) {
	env := newSubscriptionTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"completed","output":[{"type":"function_call","arguments":"broken"}],"usage":{"input_tokens":12,"output_tokens":7}}`)
	}, "subscription")
	tracker := newLifecyclePanelTestTracker(t)
	env.handler.proxy.SetUsageTracker(tracker)
	env.handler.proxy.SetPrivacyFilter(newRestoreTestFilter(t))
	resp := env.request("POST", "responses", func(r *http.Request) {
		body := `{"input":"clean"}`
		r.Body = io.NopCloser(strings.NewReader(body))
		r.ContentLength = int64(len(body))
		r.Header.Set("Authorization", "Bearer synthetic")
		r.Header.Set("Content-Type", "application/json")
	})
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	data := onlyPhaseRequest(t, tracker)
	if resp.StatusCode != 502 || bytes.Contains(body, []byte("broken")) || data.Detail.Status != "failed" || data.Detail.OutputTokens != 7 || gjson.Get(data.PrivacyScanJSON, "restore.status").Str != "failed" {
		t.Fatalf("错误交付或结算: %d %s %s %d", resp.StatusCode, body, data.PrivacyScanJSON, data.Detail.OutputTokens)
	}
}

func TestSubscriptionHTTPRestoreWireFormats(t *testing.T) {
	for _, format := range []string{"deflate-zlib", "deflate-raw", "deflate-bad-checksum", "sse-cr"} {
		t.Run(format, func(t *testing.T) {
			stream := format == "sse-cr"
			var env *subscriptionTestEnv
			env = newSubscriptionTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
				requests := env.upstreamRequests()
				token := wireToken.Find(requests[len(requests)-1].Body)
				if token == nil {
					t.Error("上游未收到脱敏值")
					return
				}
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, strings.ReplaceAll(routeSSE(string(token), true), "\n", "\r"))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Content-Encoding", "deflate")
				var buf bytes.Buffer
				var z io.WriteCloser
				if format == "deflate-raw" {
					var err error
					z, err = flate.NewWriter(&buf, flate.DefaultCompression)
					if err != nil {
						t.Error(err)
						return
					}
				} else {
					z = zlib.NewWriter(&buf)
				}
				fmt.Fprintf(z, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":%q}]}],"usage":{"input_tokens":10,"output_tokens":11}}`, token)
				if err := z.Close(); err != nil {
					t.Error(err)
					return
				}
				wire := buf.Bytes()
				if format == "deflate-bad-checksum" {
					wire[len(wire)-1] ^= 1
				}
				_, _ = w.Write(wire)
			}, "subscription")
			tracker := newLifecyclePanelTestTracker(t)
			env.handler.proxy.SetUsageTracker(tracker)
			env.handler.proxy.SetPrivacyFilter(newRestoreTestFilter(t))
			resp := env.request("POST", "responses", func(r *http.Request) {
				body := fmt.Sprintf(`{"input":%q,"stream":%t}`, routeTestValue, stream)
				r.Body = io.NopCloser(strings.NewReader(body))
				r.ContentLength = int64(len(body))
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("Authorization", "Bearer synthetic")
			})
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			row := onlyPhaseRequest(t, tracker)
			if format == "deflate-bad-checksum" {
				if resp.StatusCode != 502 || row.Detail.Status != "failed" || bytes.Contains(body, []byte("private-quote")) {
					t.Fatalf("损坏的压缩响应被交付: %d %s %s", resp.StatusCode, row.Detail.Status, body)
				}
				return
			}
			if resp.StatusCode != 200 || resp.Header.Get("Content-Encoding") != "" || wireToken.Match(body) ||
				row.Detail.Status != "completed" || row.Detail.OutputTokens != 11 || gjson.Get(row.PrivacyScanJSON, "restore.text").Int() != 1 {
				t.Fatalf("状态 %d / %s，用量 %d，回填 %s", resp.StatusCode, row.Detail.Status, row.Detail.OutputTokens, row.PrivacyScanJSON)
			}
			if !stream && gjson.GetBytes(body, "output.0.content.0.text").Str != routeTestValue {
				t.Fatalf("正文未还原: %s", body)
			}
		})
	}
}

func TestSubscriptionSSECaptureSplitLineEndings(t *testing.T) {
	for _, sep := range []string{"\r", "\n", "\r\n"} {
		var results []*ParseResult
		capture := subscriptionResponseCapture{mode: "sse", onResult: func(r *ParseResult) {
			if r != nil && r.IsCompleted {
				results = append(results, r)
			}
		}}
		event := strings.Join([]string{"event: response.completed", `data: {"type":"response.completed","response":`, `data: {"usage":{"output_tokens":7}}}`, "", ""}, sep)
		for j := range len(event) {
			_, _ = capture.Write([]byte(event[j : j+1]))
		}
		capture.Finish()
		if len(results) != 1 || !results[0].HasTokenUsage || results[0].TokenUsage.OutputTokens != 7 {
			t.Fatalf("逐字节分隔符 %q，终态结果 %+v", sep, results)
		}
	}
}

// 独立压缩与计数只要求请求占位符能在后续字典视图查到。
func TestSubscriptionRequestOnlyPrivacyPaths(t *testing.T) {
	filter := newRestoreTestFilter(t)
	for _, tc := range []struct {
		path  string
		codex bool
	}{{"/v1/responses/compact", true}, {"/v1/messages/count_tokens", false}} {
		apply, err := filter.Apply(context.Background(), privacy.Request{UpstreamType: privacy.UpstreamTypeSubscription, Path: tc.path}, []byte(routeBody(tc.codex)))
		if err != nil || apply.Restore == nil || apply.RestoreStatus != "not_applicable" || !wireToken.Match(apply.Body) {
			t.Fatal(tc.path, err)
		}
		apply.Restore.Release()
	}
}
