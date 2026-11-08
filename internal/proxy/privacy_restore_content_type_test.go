package proxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tidwall/gjson"

	"sinuo/internal/codexidentity"
	"sinuo/internal/store"
)

func TestPrivacyRestoreCodexOAuthResponseFormat(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, invalidBody string
		compressed                     bool
	}{
		{name: "无类型的官方流"},
		{name: "无类型的压缩流", compressed: true},
		{name: "缺少类型的JSON", invalidBody: "{\n\n\"private\":\"upstream-only\"}"},
		{name: "注释后跟错误页", invalidBody: ": upstream-only\n\n<html>bad gateway</html>"},
		{name: "错误协议的事件", invalidBody: "data: {\"type\":\"message_start\",\"private\":\"upstream-only\"}\n\n"},
		{name: "声明JSON", contentType: "application/json", invalidBody: "{\"private\":\"upstream-only\"}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := &syncBuffer{}
			prev := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
			t.Cleanup(func() { slog.SetDefault(prev) })

			env := newSubscriptionPoolTestEnv(t, []*store.UpstreamAccountRecord{identityAccount(t, 1)}, true)
			h := env.handler.passthrough.proxy
			policy := enabledIdentityPolicy(t)
			h.SetCodexIdentityProvider(identityProviderFunc(func(context.Context) (codexidentity.Policy, error) { return policy, nil }))
			h.SetPrivacyFilter(newRestoreTestFilter(t))
			env.transport.responder = func(r *http.Request) (*http.Response, error) {
				requestBody, _ := io.ReadAll(r.Body)
				id := wireToken.Find(requestBody)
				if id == nil || r.Header.Get("Accept") != "text/event-stream" {
					t.Fatal("测试请求没有完成流式协商和出站遮蔽")
				}
				body := tc.invalidBody
				if body == "" {
					body = ": keepalive\r\n\r\nevent: response.created\r\n" + routeSSE(string(id), true)
				}
				headers := make(http.Header)
				if tc.contentType != "" {
					headers.Set("Content-Type", tc.contentType)
				}
				var src io.Reader = strings.NewReader(body)
				if tc.compressed {
					var buf bytes.Buffer
					z := gzip.NewWriter(&buf)
					_, _ = io.WriteString(z, body)
					if err := z.Close(); err != nil {
						t.Fatal(err)
					}
					src = &buf
					headers.Set("Content-Encoding", "gzip")
				}
				return &http.Response{StatusCode: http.StatusOK, Header: headers, Body: io.NopCloser(src), Request: r}, nil
			}
			req := identityRequestFixture(true, routeBody(true))
			req.Header.Set("Accept", "text/event-stream")
			w := httptest.NewRecorder()
			env.handler.ServeHTTP(w, req)
			body := w.Body.String()
			if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "text/event-stream" {
				t.Fatalf("下游 SSE 握手错误 %d %q", w.Code, w.Header().Get("Content-Type"))
			}
			if tc.invalidBody != "" {
				if !strings.Contains(body, "privacy_restore_failed") || strings.Contains(body, "upstream-only") || strings.Contains(logs.String(), "upstream-only") ||
					!strings.Contains(logs.String(), "reason=unexpected_response_format") {
					t.Fatal("格式错误应在本地终止并记录原因，避免输出上游内容", body)
				}
				return
			}
			var text strings.Builder
			for _, line := range strings.Split(body, "\n") {
				if data, ok := strings.CutPrefix(line, "data: "); ok && gjson.Get(data, "type").Str == "response.output_text.delta" {
					text.WriteString(gjson.Get(data, "delta").Str)
				}
			}
			if text.String() != "before "+routeTestValue+" after" || !strings.Contains(body, "response.completed") ||
				strings.Count(body, ": keepalive") != 1 || strings.Contains(body, "privacy_restore_failed") ||
				!strings.Contains(logs.String(), "已写出 文本=1 工具参数=0") {
				t.Fatal("账号池未交付完整回填流", body)
			}
		})
	}
}

func TestPrivacyExpectedStreamBillsUnexpectedRegularJSON(t *testing.T) {
	for _, tc := range []struct {
		name, body, contentType string
		counted                 bool
	}{
		{"明确压缩JSON", `{"object":"response","output":[{"type":"message","content":[{"type":"output_text","text":"upstream-only"}]}],"usage":{"input_tokens":8,"output_tokens":3,"input_tokens_details":{"cached_tokens":2}}}`, "application/json; charset=utf-8", true},
		{"重复usage", `{"output":[],"usage":{"input_tokens":8},"usage":{"input_tokens":80}}`, "application/json", false},
		{"未声明JSON", `{"output":[],"usage":{"input_tokens":8}}`, "text/plain", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var contacts atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				contacts.Add(1)
				w.Header().Set("Content-Type", tc.contentType)
				w.Header().Set("Content-Encoding", "gzip")
				z := gzip.NewWriter(w)
				_, _ = io.WriteString(z, tc.body)
				if err := z.Close(); err != nil {
					t.Error(err)
				}
			}))
			defer up.Close()
			h, tracker := phaseAccountHandler(t, up.URL, 1)
			h.SetPrivacyFilter(newRestoreTestFilter(t))
			const reqID = "mistyped-response"
			req := privacySummaryRequest("/v1/responses", `{"model":"gpt-test","stream":true,"input":"hi"}`, reqID)
			req.Header.Set("Accept", "text/event-stream")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			data := timingLifecycleData(t, tracker, reqID)
			if contacts.Load() != 1 || !strings.Contains(w.Body.String(), "privacy_restore_failed") || strings.Contains(w.Body.String(), "upstream-only") || data.Detail.Status != "failed" {
				t.Fatal("错格式成功响应未在本地终止", data.Detail.Status, w.Body.String())
			}
			if tc.counted {
				if data.Detail.InputTokens != 8 || data.Detail.OutputTokens != 3 || data.Detail.CacheReadTokens != 2 {
					t.Fatalf("原始上游用量漏计或多计 %+v", data.Detail)
				}
			} else if data.Detail.InputTokens != 0 || data.Detail.OutputTokens != 0 {
				t.Fatal("对错误格式或歧义正文产生了账单", data.Detail.InputTokens)
			}
		})
	}
}

func TestUnexpectedClaudeRegularUsageWithCache(t *testing.T) {
	body := `{"type":"message","content":[],"usage":{"input_tokens":8,"output_tokens":3,"cache_read_input_tokens":2,"cache_creation":{"ephemeral_5m_input_tokens":9,"ephemeral_1h_input_tokens":4}}}`
	resp := &http.Response{Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
	sp := &StreamProcessor{tokenParser: NewTokenParser()}
	usage := sp.unexpectedRegularUsage(resp, "/v1/messages")
	if usage == nil || usage.InputTokens != 8 || usage.OutputTokens != 3 || usage.CacheReadTokens != 2 || usage.CacheCreationTokens != 13 || usage.CacheCreation5mTokens != 9 || usage.CacheCreation1hTokens != 4 {
		t.Fatalf("错格式响应的 Claude 缓存用量不完整 %+v", usage)
	}
}
