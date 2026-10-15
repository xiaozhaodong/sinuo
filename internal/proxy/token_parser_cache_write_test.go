package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sinuo/internal/accountauth"
	"sinuo/internal/middleware"
	"sinuo/internal/proxy/handlers"
	"sinuo/internal/proxy/response"
	"sinuo/internal/store"
	"sinuo/internal/tracking"
)

func responsesCacheWriteFixture(t *testing.T, written *int64) string {
	t.Helper()
	details := map[string]any{"cached_tokens": 265159}
	if written != nil {
		details["cache_write_tokens"] = *written
	}
	body, err := json.Marshal(map[string]any{
		"id": "resp_cache_write", "object": "response", "model": "gpt-6-astra", "status": "completed",
		"output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{
			map[string]any{"type": "output_text", "text": "ok"},
		}}},
		"usage": map[string]any{"input_tokens": 272077, "output_tokens": 1671, "input_tokens_details": details},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func assertResponsesCacheWriteUsage(t *testing.T, got *tracking.TokenUsage, written int64) {
	t.Helper()
	want := tracking.TokenUsage{InputTokens: 272077, OutputTokens: 1671, CacheReadTokens: 265159, CacheCreationTokens: written, InputIncludesCache: true}
	if got == nil || *got != want {
		t.Fatalf("用量 = %+v，期望 %+v；不得推算 5m / 1h 分项", got, want)
	}
}

func TestResponsesCacheWriteTokens_JSONAndSSEShareUsage(t *testing.T) {
	zero, written := int64(0), int64(2000)
	for _, tc := range []struct {
		name  string
		value *int64
		want  int64
	}{
		{"缺失保持旧值", nil, 0}, {"明确零", &zero, 0}, {"实际写入", &written, written},
	} {
		for _, event := range []string{"response.completed", "response.done", "response.failed", "response.incomplete", "response.cancelled"} {
			t.Run(tc.name+"/"+event, func(t *testing.T) {
				payload := fmt.Sprintf(`{"type":%q,"response":%s}`, event, responsesCacheWriteFixture(t, tc.value))
				direct := ParseResponsesEventJSON(event, []byte(payload))
				if direct == nil || !direct.HasTokenUsage {
					t.Fatalf("共享 JSON / WS 解析未报告用量：%+v", direct)
				}
				assertResponsesCacheWriteUsage(t, direct.TokenUsage, tc.want)
				parser := NewTokenParser()
				parser.ParseSSELineV2("event: " + event)
				parser.ParseSSELineV2("data: " + payload)
				sse := parser.ParseSSELineV2("")
				if sse == nil || !sse.HasTokenUsage {
					t.Fatalf("SSE 解析未报告用量：%+v", sse)
				}
				assertResponsesCacheWriteUsage(t, sse.TokenUsage, tc.want)
			})
		}
	}
}

func TestResponsesCacheWriteTokens_CompatibilityRecovery(t *testing.T) {
	written := int64(2000)
	fixture := responsesCacheWriteFixture(t, &written)
	broken := strings.TrimSuffix(fixture, "}") + `,"broken": }`
	result := ParseResponsesEventJSON("response.completed", []byte(broken))
	if result == nil || !result.HasTokenUsage {
		t.Fatalf("兼容恢复未报告用量：%+v", result)
	}
	assertResponsesCacheWriteUsage(t, result.TokenUsage, written)

	parser := NewTokenParser()
	parser.ParseSSELineV2("event: response.in_progress")
	parser.ParseSSELineV2(`data: {"type":"response.in_progress","response":{"model":"gpt-6-astra"}}`)
	parser.ParseSSELineV2("")
	if !parser.RecoverResponsesUsageFromRawTail(fixture) {
		t.Fatal("尾部完整响应未被恢复")
	}
	assertResponsesCacheWriteUsage(t, parser.GetFinalUsage(), written)
}

func TestResponsesCacheWriteTokens_LegacyMonitorUsageIncludesCache(t *testing.T) {
	written := int64(2000)
	parser := NewTokenParser()
	parser.ParseSSELine("event: response.completed")
	parser.ParseSSELine("data: " + responsesCacheWriteFixture(t, &written))
	usage := parser.ParseSSELine("")
	if usage == nil || usage.CacheCreationTokens != written || usage.TotalTokens() != 273748 {
		t.Fatalf("运行时用量不正确：%+v", usage)
	}
}

func TestResponsesCacheWriteTokens_JSONMonitorKeepsParserProtocol(t *testing.T) {
	written := int64(2000)
	for _, tc := range []struct {
		name, path, body string
		wantTotal        int64
	}{
		{"标准路径", "/v1/responses", responsesCacheWriteFixture(t, &written), 273748},
		{"压缩路径", "/v1/responses/compact", responsesCacheWriteFixture(t, &written), 273748},
		{"仅解析器确认协议", "/custom", responsesCacheWriteFixture(t, &written), 273748},
		{"Claude独立缓存", "/v1/messages", `{"model":"claude-test","usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":10,"cache_read_input_tokens":20}}`, 180},
	} {
		t.Run(tc.name, func(t *testing.T) {
			monitoring := middleware.NewMonitoringMiddleware(nil)
			analyzer := response.NewTokenAnalyzer(nil, monitoring, &TokenParserProviderImpl{})
			req := httptest.NewRequest(http.MethodPost, tc.path, nil)
			req = req.WithContext(context.WithValue(req.Context(), "conn_id", "json-cache-write"))
			analyzer.AnalyzeResponseForTokensWithLifecycle(req.Context(), tc.body, "unknown", req, nil)
			usage := monitoring.GetMetrics().GetTotalTokenStats()
			if usage.TotalTokens() != tc.wantTotal {
				t.Fatalf("JSON 监控丢失协议语义：usage=%+v total=%d want=%d", usage, usage.TotalTokens(), tc.wantTotal)
			}
		})
	}
}

func TestAccountPipelineCacheWrites_PreserveBodyAndBaseCost(t *testing.T) {
	for _, tc := range []struct {
		name, path   string
		pool, stream bool
	}{
		{"旧入口JSON", "/v1/responses", false, false},
		{"旧入口SSE", "/v1/responses", false, true},
		{"订阅池JSON", "/v1/responses", true, false},
		{"订阅池SSE", "/v1/responses", true, true},
		{"独立压缩JSON", "/v1/responses/compact", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tracker := newLifecyclePanelTestTracker(t)
			price := tracking.ModelPricing{Input: 10, Output: 50, CacheRead: 1, CacheCreation: 12.5, CacheCreation1h: 20}
			tracker.UpdatePricing(map[string]tracking.ModelPricing{"gpt-6-astra": price, "_default": price})
			env := newSubscriptionPoolTestEnv(t, []*store.UpstreamAccountRecord{{
				ID: 11, AccountName: "cache-write-test", ProviderType: accountauth.ProviderAPIKey,
				CredentialRaw: "fixture-credential", BaseURL: "http://upstream.invalid", Enabled: true,
			}}, true)
			written := int64(2000)
			body := responsesCacheWriteFixture(t, &written)
			contentType := "application/json"
			if tc.stream {
				body = "event: response.completed\ndata: " + body + "\n\n"
				contentType = "text/event-stream"
			}
			env.transport.responder = func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}},
					Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
			}
			monitoring := middleware.NewMonitoringMiddleware(env.handler.proxy.endpointManager)
			env.handler.proxy.SetMonitoringMiddleware(monitoring)
			env.handler.proxy.SetUsageTracker(tracker)
			requestBody := fmt.Sprintf(`{"model":"gpt-6-astra","input":"synthetic","stream":%t}`, tc.stream)
			var rec *httptest.ResponseRecorder
			if tc.pool {
				rec = env.serve(t, http.MethodPost, "responses", func(req *http.Request) {
					req.Body = io.NopCloser(strings.NewReader(requestBody))
					req.ContentLength = int64(len(requestBody))
				})
			} else {
				req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(requestBody))
				req.Header.Set("Content-Type", "application/json")
				req = req.WithContext(context.WithValue(req.Context(), "conn_id", "cache-write-request"))
				rec = httptest.NewRecorder()
				env.handler.proxy.ServeHTTP(rec, req)
			}
			if rec.Code != http.StatusOK || rec.Body.String() != body {
				t.Fatalf("响应被改变：status=%d body=%q", rec.Code, rec.Body.String())
			}
			row := singleTrackedRequest(t, tracker)
			if !row.UsageKnown || row.InputTokens != 272077 || row.OutputTokens != 1671 || row.CacheReadTokens != 265159 || row.CacheCreationTokens != written || row.CacheCreation5mTokens != 0 || row.CacheCreation1hTokens != 0 {
				t.Fatalf("追踪记录用量不正确：%+v", row)
			}
			if !row.CostKnown || math.Abs(row.TotalCostUSD-0.417889) > 1e-12 || row.CacheCreationCostUSD != 0 {
				t.Fatalf("新增统计改变了基础费用：%+v", row)
			}
			usage := monitoring.GetMetrics().GetTotalTokenStats()
			if usage.CacheCreationTokens != written || usage.TotalTokens() != 273748 {
				t.Fatalf("运行时统计不一致：%+v，总量 %d", usage, usage.TotalTokens())
			}
		})
	}
}

func TestSubscriptionWSCacheWrites_PersistOnceWithBaseCost(t *testing.T) {
	tracker := newSubscriptionUnitTracker(t)
	price := tracking.ModelPricing{Input: 10, Output: 50, CacheRead: 1, CacheCreation: 12.5, CacheCreation1h: 20}
	tracker.UpdatePricing(map[string]tracking.ModelPricing{"gpt-6-astra": price})
	observer := newSubscriptionTrackingObserver(tracker, nil)
	submission := &subscriptionSubmission{
		RequestID: "ws-cache-write", Generate: true, StartedAt: time.Now(), Model: "gpt-6-astra",
		PrivacyState: handlers.NewPrivacyRequestState("ws-cache-write"),
	}
	observer.SubmissionStarted(submission)
	written := int64(2000)
	result := ParseResponsesEventJSON("response.completed", []byte(responsesCacheWriteFixture(t, &written)))
	observer.SubmissionTerminal(submission, result)
	observer.SubmissionTerminal(submission, result)
	row := waitSubscriptionDetail(t, tracker, submission.RequestID)
	if row.Status != "completed" || !row.UsageKnown || !row.CostKnown || row.CacheCreationTokens != written || row.CacheCreation5mTokens != 0 || row.CacheCreation1hTokens != 0 || math.Abs(row.TotalCostUSD-0.417889) > 1e-12 {
		t.Fatalf("WS 结算错误：%+v", row)
	}
	count, err := tracker.CountRequestDetails(context.Background(), &tracking.QueryOptions{RequestID: submission.RequestID, UpstreamType: "all"})
	if err != nil || count != 1 {
		t.Fatalf("WS 终态重复结算：count=%d err=%v", count, err)
	}
}
