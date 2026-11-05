package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"sinuo/internal/tracking"
)

func TestSubscriptionToolTrackingHTTP(t *testing.T) {
	for _, hot := range []bool{false, true} {
		for _, tc := range []struct {
			name, operation, response, contentType, wantStatus, wantFailure string
			statusCode                                                      int
			multipart, stream, truncated, unauthorized, pool                bool
		}{
			{name: "搜索", operation: "alpha/search", response: `{"output":"SYNTHETIC-SECRET","results":[]}`},
			{name: "图片生成", operation: "images/generations", response: `{"data":[{"b64_json":"aW1hZ2U="}]}`},
			{name: "图片编辑JSON", operation: "images/edits", response: `{"data":[{"url":"https://example.invalid/image"}]}`},
			{name: "图片编辑表单", operation: "images/edits", multipart: true, response: `{"data":[{"b64_json":"aW1hZ2U="}]}`},
			{name: "池入口图片仍为直通", operation: "images/generations", pool: true, response: `{"data":[{"b64_json":"aW1hZ2U="}]}`},
			{name: "图片流", operation: "images/generations", stream: true, contentType: "text/event-stream", response: "event: image_generation.partial_image\ndata: {\"type\":\"image_generation.partial_image\"}\n\nevent: image_generation.completed\ndata: {\"type\":\"image_generation.completed\",\"model\":\"response-image-model\"}\n\n"},
			{name: "编辑表单流", operation: "images/edits", multipart: true, stream: true, contentType: "text/event-stream", response: "data: {\"type\":\"image_edit.completed\"}\n\n"},
			{name: "HTTP错误", operation: "alpha/search", statusCode: 429, response: `{"error":{"message":"SYNTHETIC-SECRET"}}`, wantStatus: "failed", wantFailure: "subscription_http_error"},
			{name: "JSON业务错误", operation: "images/edits", response: `{"error":{"message":"SYNTHETIC-SECRET"}}`, wantStatus: "failed", wantFailure: "subscription_response_failed"},
			{name: "流错误", operation: "images/generations", stream: true, contentType: "text/event-stream", response: "event: error\ndata: {\"error\":{\"message\":\"SYNTHETIC-SECRET\"}}\n\n", wantStatus: "failed", wantFailure: "subscription_response_failed"},
			{name: "流缺终态", operation: "images/edits", stream: true, contentType: "text/event-stream", response: "event: image_edit.partial_image\ndata: {\"type\":\"image_edit.partial_image\"}\n\n", wantStatus: "failed", wantFailure: "subscription_response_incomplete"},
			{name: "空完成事件", operation: "images/generations", stream: true, contentType: "text/event-stream", response: "event: image_generation.completed\n\n", wantStatus: "failed", wantFailure: "subscription_response_incomplete"},
			{name: "响应中断", operation: "alpha/search", response: `{"results":[]}`, truncated: true, wantStatus: "failed", wantFailure: "subscription_response_interrupted"},
			{name: "无效JSON", operation: "images/generations", response: `{"data":[`, wantStatus: "failed", wantFailure: "subscription_response_invalid"},
			{name: "缺少凭据", operation: "images/generations", unauthorized: true, wantStatus: "failed", wantFailure: "subscription_unauthorized"},
		} {
			t.Run(fmt.Sprintf("热池=%t/%s", hot, tc.name), func(t *testing.T) {
				tracker := newTimingTestTracker(t, hot)
				mode := "subscription"
				if tc.pool {
					mode = "pool"
				}
				env := newSubscriptionTestEnv(t, func(w http.ResponseWriter, _ *http.Request) {
					contentType := tc.contentType
					if contentType == "" {
						contentType = "application/json"
					}
					w.Header().Set("Content-Type", contentType)
					if tc.truncated {
						w.Header().Set("Content-Length", strconv.Itoa(len(tc.response)+100))
					}
					status := tc.statusCode
					if status == 0 {
						status = http.StatusOK
					}
					w.WriteHeader(status)
					_, _ = io.WriteString(w, tc.response)
				}, mode)
				env.handler.proxy.SetUsageTracker(tracker)
				body := []byte(fmt.Sprintf(`{"model":"request-tool-model","prompt":"SYNTHETIC-SECRET","stream":%t,"commands":{"search_query":[{"q":"SYNTHETIC-SECRET"}]},"images":[]}`, tc.stream))
				contentType := "application/json"
				if tc.multipart {
					body, contentType = buildImageEditMultipartBody(t, map[string]string{
						"model": "request-tool-model", "prompt": "SYNTHETIC-SECRET", "stream": strconv.FormatBool(tc.stream),
					}, []imageEditTestFile{{Field: "image", Filename: "test.png", Content: []byte("SYNTHETIC-SECRET-image")}})
				}
				resp := env.request(http.MethodPost, tc.operation, func(r *http.Request) {
					if !tc.unauthorized {
						r.Header.Set("Authorization", "Bearer synthetic-client-token")
					}
					r.Header.Set("Content-Type", contentType)
					r.Header.Set("session-id", "tool-session")
					r.Body = io.NopCloser(bytes.NewReader(body))
				})
				output, readErr := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if readErr != nil && !tc.truncated {
					t.Fatal(readErr)
				}
				if !tc.unauthorized && string(output) != tc.response {
					t.Fatal("追踪修改了响应正文")
				}
				row := waitSubscriptionToolDetail(t, tracker, "tool-session")
				wantStatus := tc.wantStatus
				if wantStatus == "" {
					wantStatus = "completed"
				}
				wantFamily := tracking.RequestFamilyCodex
				if strings.HasPrefix(tc.operation, "images/") {
					wantFamily = tracking.RequestFamilyImage
				}
				if row.Status != wantStatus || row.FailureReason != tc.wantFailure || row.RequestFamily != wantFamily ||
					row.Path != "/v1/"+tc.operation || row.RequestSource != tracking.RequestSourceCodexSubscriptionProxy ||
					row.RequestKind != tracking.RequestKindGeneration || row.UpstreamType != "subscription" || row.UpstreamID != 0 {
					t.Fatalf("追踪归属或终态不符: %+v", row)
				}
				if row.HTTPStatusCode == nil || *row.HTTPStatusCode != resp.StatusCode || row.UsageKnown || row.CostKnown {
					t.Fatalf("HTTP 状态丢失或伪造工具费用: %+v", row)
				}
				if !tc.unauthorized {
					requests := env.upstreamRequests()
					if len(requests) != 1 || !bytes.Equal(requests[0].Body, body) || requests[0].Header.Get("Content-Type") != contentType {
						t.Fatal("追踪改变了请求正文或发送次数")
					}
					if row.RequestModel != "request-tool-model" || row.IsStreaming != tc.stream {
						t.Fatalf("请求模型或流式标记丢失: %+v", row)
					}
				} else if env.upstreamCount() != 0 {
					t.Fatal("缺少凭据的请求发给了上游")
				}
				if strings.Contains(tc.response, "response-image-model") && row.ResponseModel != "response-image-model" {
					t.Fatalf("响应模型丢失: %+v", row)
				}
				encoded, err := json.Marshal(row)
				if err != nil {
					t.Fatal(err)
				}
				if bytes.Contains(encoded, []byte("SYNTHETIC-SECRET")) || strings.Contains(env.logs.String(), "SYNTHETIC-SECRET") {
					t.Fatal("工具正文写入了追踪或日志")
				}
			})
		}
	}
}

func waitSubscriptionToolDetail(t *testing.T, tracker *tracking.UsageTracker, sessionID string) tracking.RequestDetail {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		rows, _, err := tracker.QueryRequestDetailsWithHotPool(context.Background(), &tracking.QueryOptions{SessionID: sessionID, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) > 1 {
			t.Fatalf("一个工具调用产生多条记录: %d", len(rows))
		}
		if len(rows) == 1 && (rows[0].Status == "completed" || rows[0].Status == "failed" || rows[0].Status == "cancelled") {
			return rows[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("工具请求未完成追踪结算")
	return tracking.RequestDetail{}
}

func TestSubscriptionToolTrackingLargeImageEvent(t *testing.T) {
	for _, tc := range []struct {
		name, prefix, suffix, wantStatus string
		tailCancelled                    bool
	}{
		{"显式事件", "event: image_generation.completed\ndata: {\"b64_json\":\"", "\"}\n\n", "completed", false},
		{"数据内类型", "data: {\"type\":\"image_generation.completed\",\"b64_json\":\"", "\"}\n\n", "completed", false},
		{"终态后的取消", "data: {\"type\":\"image_generation.completed\",\"b64_json\":\"", "\"}\n\n", "completed", true},
		{"终态前的取消", "event: image_generation.completed\ndata: {\"b64_json\":\"", "", "cancelled", true},
		{"缺事件边界", "event: image_generation.completed\ndata: {\"b64_json\":\"", "\"}", "failed", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tracker := newSubscriptionUnitTracker(t)
			track := newSubscriptionTrackingObserver(tracker, nil).StartHTTP(SubscriptionHTTPStart{
				RequestID: "large-tool", SessionID: "large-session", Path: openAIImagesGenerationsPath,
				RequestKind: tracking.RequestKindGeneration, RequestFamily: tracking.RequestFamilyImage,
			})
			response := &subscriptionToolResponse{path: openAIImagesGenerationsPath, track: track}
			capture := &subscriptionResponseCapture{
				parsePayload: response.parsePayload, onResult: response.observe,
				onOversizedEvent: func(event string) { response.observe(response.parseEvent(event)) },
			}
			_, _ = capture.Write([]byte(tc.prefix))
			chunk := bytes.Repeat([]byte("a"), 65536)
			for range subscriptionCaptureLimit/len(chunk) + 1 {
				_, _ = capture.Write(chunk)
			}
			_, _ = capture.Write([]byte(tc.suffix))
			capture.Finish()
			var copyErr, contextErr error
			if tc.tailCancelled {
				copyErr, contextErr = context.Canceled, context.Canceled
			}
			response.finish(200, capture.mode, copyErr, contextErr)
			row := waitSubscriptionToolDetail(t, tracker, "large-session")
			if row.Status != tc.wantStatus || row.UsageKnown || row.CostKnown {
				t.Fatalf("大图终态或尾部取消处理错误: %+v", row)
			}
		})
	}
}

func TestSubscriptionToolTrackingJSONFirstResponse(t *testing.T) {
	for _, hot := range []bool{false, true} {
		for _, large := range []bool{false, true} {
			t.Run(fmt.Sprintf("热池=%t/大图=%t", hot, large), func(t *testing.T) {
				tracker := newTimingTestTracker(t, hot)
				release := make(chan struct{})
				finishBody := sync.OnceFunc(func() { close(release) })
				defer finishBody()
				prefix := `{"data":[{"b64_json":"`
				tail := "aW1hZ2U="
				if large {
					tail = strings.Repeat("a", subscriptionCaptureLimit+4)
				}
				tail += `"}]}`
				env := newSubscriptionTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, prefix)
					_ = http.NewResponseController(w).Flush()
					select {
					case <-release:
						_, _ = io.WriteString(w, tail)
					case <-r.Context().Done():
					}
				}, "subscription")
				env.handler.proxy.SetUsageTracker(tracker)
				resp := env.request(http.MethodPost, "images/generations", func(r *http.Request) {
					r.Header.Set("Authorization", "Bearer synthetic-client-token")
					r.Header.Set("Content-Type", "application/json")
					r.Header.Set("session-id", "json-first-response")
					r.Body = io.NopCloser(strings.NewReader(`{"model":"test-image","prompt":"test"}`))
				})
				defer resp.Body.Close()
				// 上游尚未发送完整 JSON，首响必须已经可见，且请求不能提前结束。
				var firstMs *int64
				for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
					rows, _, err := tracker.QueryRequestDetailsWithHotPool(context.Background(), &tracking.QueryOptions{SessionID: "json-first-response", Limit: 10})
					if err != nil {
						t.Fatal(err)
					}
					if len(rows) == 1 && rows[0].FirstTokenMs != nil {
						if rows[0].Status == "completed" || rows[0].Status == "failed" || rows[0].Status == "cancelled" {
							t.Fatal("收到首段正文不能提前结算")
						}
						firstMs = rows[0].FirstTokenMs
						break
					}
					time.Sleep(5 * time.Millisecond)
				}
				if firstMs == nil {
					t.Fatal("完整 JSON 到达前未记录首响")
				}
				finishBody()
				output, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatal(err)
				}
				if string(output) != prefix+tail {
					t.Fatal("首响观察改变了透传正文")
				}
				row := waitSubscriptionToolDetail(t, tracker, "json-first-response")
				if row.Status != "completed" || row.IsStreaming || row.FirstTokenMs == nil || *row.FirstTokenMs != *firstMs || row.CompletionMs == nil || row.UsageKnown || row.CostKnown {
					t.Fatalf("JSON 首响、终态或用量语义错误: %+v", row)
				}
				if large {
					assertSubscriptionToolObservationLog(t, env.logs.String(), "subscription_response_capture_limit", "DEBUG")
				}
			})
		}
	}
}

func TestSubscriptionToolTrackingObservationWarning(t *testing.T) {
	tracker := newSubscriptionUnitTracker(t)
	env := newSubscriptionTestEnv(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, "upstream unavailable")
	}, "subscription")
	env.handler.proxy.SetUsageTracker(tracker)
	resp := env.request(http.MethodPost, "images/generations", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer synthetic-client-token")
		r.Header.Set("session-id", "tool-warning")
	})
	_, err := io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	row := waitSubscriptionToolDetail(t, tracker, "tool-warning")
	if row.Status != "failed" {
		t.Fatalf("异常响应未保留失败状态: %+v", row)
	}
	assertSubscriptionToolObservationLog(t, env.logs.String(), "subscription_response_format_unknown", "WARN")
}

func assertSubscriptionToolObservationLog(t *testing.T, logs, code, level string) {
	t.Helper()
	for _, line := range strings.Split(logs, "\n") {
		if !strings.Contains(line, "code="+code) {
			continue
		}
		if !strings.Contains(line, "level="+level) || !strings.Contains(line, "响应元数据观察受限") || strings.Contains(line, "用量解析") {
			t.Fatalf("工具观察日志级别或文案错误: %s", line)
		}
		return
	}
	t.Fatalf("缺少观察限制日志 %s", code)
}
