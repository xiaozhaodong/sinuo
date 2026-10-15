package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"sinuo/config"
	svc "sinuo/internal/service"

	"github.com/klauspost/compress/zstd"
)

const (
	subscriptionTestSecret  = "test-access-secret"
	subscriptionDefaultPath = "/backend-api/codex"
	subscriptionCodexHeader = "X-Codex-Turn-State"
)

// recordedUpstreamRequest 上游收到的请求快照。
type recordedUpstreamRequest struct {
	Method string
	Path   string
	Query  string
	Header http.Header
	Body   []byte
}

type subscriptionTestEnv struct {
	t        *testing.T
	upstream *httptest.Server
	server   *httptest.Server
	handler  *CodexSubscriptionProxyHandler

	mu       sync.Mutex
	requests []recordedUpstreamRequest
	logs     *syncBuffer
}

// syncBuffer 并发安全的日志收集器：日志由服务端 goroutine 写入，测试线程读取。
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// newSubscriptionTestEnv 搭建“客户端 → 新入口 → 假上游”的最小链路。
func newSubscriptionTestEnv(t *testing.T, upstream http.HandlerFunc, mode string) *subscriptionTestEnv {
	t.Helper()
	env := &subscriptionTestEnv{t: t, logs: &syncBuffer{}}
	if upstream == nil {
		upstream = func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}
	env.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		env.mu.Lock()
		env.requests = append(env.requests, recordedUpstreamRequest{
			Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Header: r.Header.Clone(), Body: body,
		})
		env.mu.Unlock()
		upstream(w, r)
	}))
	t.Cleanup(env.upstream.Close)

	logger := slog.New(slog.NewTextHandler(env.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	handler := NewCodexSubscriptionProxyHandler(SubscriptionProxyHandlerOptions{
		Proxy:             NewHandler(nil, &config.Config{}),
		Logger:            logger,
		UpstreamBaseURL:   env.upstream.URL + subscriptionDefaultPath,
		UpstreamWSBaseURL: "ws" + strings.TrimPrefix(env.upstream.URL, "http") + subscriptionDefaultPath,
	})
	env.handler = handler

	env.server = httptest.NewServer(handler)
	t.Cleanup(env.server.Close)

	host, portText, err := net.SplitHostPort(strings.TrimPrefix(env.server.URL, "http://"))
	if err != nil {
		t.Fatalf("解析测试服务器地址失败: %v", err)
	}
	port, err := net.LookupPort("tcp", portText)
	if err != nil {
		t.Fatalf("解析测试服务器端口失败: %v", err)
	}
	env.setSnapshot(&SubscriptionProxySnapshot{
		Revision:      1,
		Enabled:       true,
		Mode:          mode,
		ListenHost:    host,
		ListenPort:    port,
		WSCompression: svc.CodexSubscriptionProxyCompressionAuto,
		AccessHash:    svc.HashCodexSubscriptionProxyAccessSecret(subscriptionTestSecret),
	})
	return env
}

// setSnapshot 必须在发出任何请求之前调用。
func (e *subscriptionTestEnv) setSnapshot(snap *SubscriptionProxySnapshot) {
	value := snap
	e.handler.snapshot = func() *SubscriptionProxySnapshot { return value }
}

// request 构造并发起一次新入口请求。
func (e *subscriptionTestEnv) request(method, operation string, mutate func(*http.Request)) *http.Response {
	e.t.Helper()
	target := fmt.Sprintf("%s/subscription/%s/backend-api/codex/%s", e.server.URL, subscriptionTestSecret, operation)
	req, err := http.NewRequest(method, target, nil)
	if err != nil {
		e.t.Fatalf("构造请求失败: %v", err)
	}
	if mutate != nil {
		mutate(req)
	}
	resp, err := e.server.Client().Do(req)
	if err != nil {
		e.t.Fatalf("请求失败: %v", err)
	}
	return resp
}

func (e *subscriptionTestEnv) upstreamRequests() []recordedUpstreamRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]recordedUpstreamRequest, len(e.requests))
	copy(out, e.requests)
	return out
}

func (e *subscriptionTestEnv) upstreamCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.requests)
}

func (e *subscriptionTestEnv) logOutput() string {
	return e.logs.String()
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取响应体失败: %v", err)
	}
	return string(data)
}

func zstdCompress(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatalf("创建 zstd writer 失败: %v", err)
	}
	if _, err := writer.Write(payload); err != nil {
		t.Fatalf("zstd 压缩失败: %v", err)
	}
	writer.Close()
	return buf.Bytes()
}

func TestSubscriptionHandlerRejectsInvalidAccess(t *testing.T) {
	env := newSubscriptionTestEnv(t, nil, svc.CodexSubscriptionProxyModeSubscription)

	cases := []struct {
		name      string
		operation string
		secret    string
		mutate    func(*http.Request)
		want      int
	}{
		{
			name: "错误访问密钥", operation: "responses", secret: "wrong-secret",
			mutate: func(r *http.Request) { r.Method = http.MethodPost }, want: http.StatusForbidden,
		},
		{
			name: "Host 不匹配", operation: "responses", secret: subscriptionTestSecret,
			mutate: func(r *http.Request) {
				r.Method = http.MethodPost
				r.Host = "127.0.0.1:1"
			},
			want: http.StatusForbidden,
		},
		{
			name: "带 Origin", operation: "responses", secret: subscriptionTestSecret,
			mutate: func(r *http.Request) {
				r.Method = http.MethodPost
				r.Header.Set("Origin", "https://example.com")
			},
			want: http.StatusForbidden,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := fmt.Sprintf("%s/subscription/%s/backend-api/codex/%s", env.server.URL, tc.secret, tc.operation)
			req, err := http.NewRequest(http.MethodGet, target, nil)
			if err != nil {
				t.Fatalf("构造请求失败: %v", err)
			}
			tc.mutate(req)
			resp, err := env.server.Client().Do(req)
			if err != nil {
				t.Fatalf("请求失败: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("状态码不符: 期望 %d，实际 %d", tc.want, resp.StatusCode)
			}
		})
	}

	if got := env.upstreamCount(); got != 0 {
		t.Fatalf("被拒绝的请求不应触达上游: %d", got)
	}
	if strings.Contains(env.logOutput(), subscriptionTestSecret) {
		t.Fatal("日志中不应出现访问密钥")
	}
}

func TestSubscriptionHandlerStripsCapabilityPathAndMovesHeaders(t *testing.T) {
	env := newSubscriptionTestEnv(t, nil, svc.CodexSubscriptionProxyModeSubscription)

	resp := env.request(http.MethodPost, "responses?stream=true", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer client-token")
		r.Header.Set("chatgpt-account-id", "acct-1")
		r.Header.Set("OpenAI-Beta", "responses_websockets=2026-02-06")
		r.Header.Set("OpenAI-Alpha", "quicksilver=v2")
		r.Header.Set("originator", "Codex Desktop")
		r.Header.Set(subscriptionCodexHeader, "turn-state")
		r.Header.Set("Cookie", "session=secret")
		r.Header.Set("x-api-key", "should-not-leak")
		r.Header.Set("X-Forwarded-For", "203.0.113.9")
		r.Header.Set("Proxy-Authorization", "Basic abc")
		r.Header.Set("X-Unknown-Metadata", "should-not-leak")
		r.Body = io.NopCloser(strings.NewReader(`{"model":"gpt-5"}`))
		r.ContentLength = int64(len(`{"model":"gpt-5"}`))
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码不符: %d", resp.StatusCode)
	}

	requests := env.upstreamRequests()
	if len(requests) != 1 {
		t.Fatalf("上游请求数不符: %d", len(requests))
	}
	got := requests[0]
	if got.Path != "/backend-api/codex/responses" {
		t.Fatalf("capability 前缀未剥离: %q", got.Path)
	}
	if got.Query != "stream=true" {
		t.Fatalf("query 未保留: %q", got.Query)
	}
	for _, key := range []string{"Authorization", "Chatgpt-Account-Id", "Openai-Beta", "Originator", subscriptionCodexHeader} {
		if got.Header.Get(key) == "" {
			t.Fatalf("应透传的请求头缺失: %s", key)
		}
	}
	for _, key := range []string{"Cookie", "X-Api-Key", "X-Forwarded-For", "Proxy-Authorization", "X-Unknown-Metadata", "OpenAI-Alpha"} {
		if value := got.Header.Get(key); value != "" {
			t.Fatalf("不应出站的请求头 %s: %q", key, value)
		}
	}
	if !strings.Contains(env.logOutput(), "/v1/responses") {
		t.Fatal("日志应记录规范化路径")
	}
	if strings.Contains(env.logOutput(), subscriptionTestSecret) || strings.Contains(env.logOutput(), "/subscription/") {
		t.Fatal("日志不应包含 capability 前缀或访问密钥")
	}
}

func TestSubscriptionHandlerRelaysModelsWithCacheHeaders(t *testing.T) {
	env := newSubscriptionTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `W/"etag-1"` {
			w.Header().Set("x-models-etag", `W/"etag-1"`)
			w.Header().Set("Set-Cookie", "should-not-relay=1")
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.WriteHeader(http.StatusOK)
	}, svc.CodexSubscriptionProxyModeSubscription)

	resp := env.request(http.MethodGet, "models?limit=5", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer client-token")
		r.Header.Set("If-None-Match", `W/"etag-1"`)
		r.Header.Set("If-Modified-Since", "Wed, 21 Oct 2015 07:28:00 GMT")
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("状态码不符: %d", resp.StatusCode)
	}
	if got := resp.Header.Get("x-models-etag"); got != `W/"etag-1"` {
		t.Fatalf("x-models-etag 未回传: %q", got)
	}
	if got := resp.Header.Get("Set-Cookie"); got != "" {
		t.Fatalf("Set-Cookie 不应回传: %q", got)
	}

	requests := env.upstreamRequests()
	if len(requests) != 1 || requests[0].Query != "limit=5" {
		t.Fatalf("models query 未保留: %+v", requests)
	}
	if requests[0].Header.Get("If-None-Match") == "" || requests[0].Header.Get("If-Modified-Since") == "" {
		t.Fatal("models 条件请求头未透传")
	}
}

func TestSubscriptionHandlerRequiresClientAuthorization(t *testing.T) {
	env := newSubscriptionTestEnv(t, nil, svc.CodexSubscriptionProxyModeSubscription)

	resp := env.request(http.MethodPost, "responses", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("缺少客户端授权应返回 401: %d", resp.StatusCode)
	}
	if got := env.upstreamCount(); got != 0 {
		t.Fatalf("缺授权的请求不应触达上游: %d", got)
	}
}

func TestSubscriptionHandlerRejectsUnsupportedRoute(t *testing.T) {
	env := newSubscriptionTestEnv(t, nil, svc.CodexSubscriptionProxyModeSubscription)

	cases := []struct {
		name      string
		method    string
		operation string
		want      int
	}{
		{"未知操作", http.MethodPost, "alpha/unknown", http.StatusNotFound},
		{"未列入白名单的 history 操作", http.MethodPost, "alpha/history/v2/delete_all", http.StatusNotFound},
		{"未列入白名单的 notes 操作", http.MethodPost, "alpha/notes/v2/delete_file", http.StatusNotFound},
		{"responses 用了 GET", http.MethodGet, "responses", http.StatusMethodNotAllowed},
		{"models 用了 POST", http.MethodPost, "models", http.StatusMethodNotAllowed},
		{"compact 用了 GET", http.MethodGet, "responses/compact", http.StatusMethodNotAllowed},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := env.request(tc.method, tc.operation, func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer client-token")
			})
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("状态码不符: 期望 %d，实际 %d", tc.want, resp.StatusCode)
			}
		})
	}
	if got := env.upstreamCount(); got != 0 {
		t.Fatalf("不支持的路由不应触达上游: %d", got)
	}
}

func TestSubscriptionHandlerForwardsHistoryAndNotesWhitelist(t *testing.T) {
	env := newSubscriptionTestEnv(t, nil, svc.CodexSubscriptionProxyModeSubscription)

	for _, operation := range []string{
		"alpha/history/v2/list_windows",
		"alpha/history/v2/read_item",
		"alpha/notes/v2/write_file",
		"alpha/notes/v2/thread_hint",
	} {
		resp := env.request(http.MethodPost, operation, func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer client-token")
			r.Header.Set("x-openai-tool-output-truncation-policy", "auto")
			r.Body = io.NopCloser(strings.NewReader(`{"input":[]}`))
			r.ContentLength = int64(len(`{"input":[]}`))
		})
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s 状态码不符: %d", operation, resp.StatusCode)
		}
	}

	requests := env.upstreamRequests()
	if len(requests) != 4 {
		t.Fatalf("上游请求数不符: %d", len(requests))
	}
	for _, got := range requests {
		if got.Path != "/backend-api/codex/"+strings.TrimPrefix(got.Path, "/backend-api/codex/") {
			t.Fatalf("路径异常: %q", got.Path)
		}
		if got.Header.Get("X-Openai-Tool-Output-Truncation-Policy") != "auto" {
			t.Fatalf("%s 缺少截断策略头", got.Path)
		}
	}
}

func TestSubscriptionHandlerForwardsRealtimeCallWithLocation(t *testing.T) {
	const sdp = "v=0\r\no=- 0 0 IN IP4 127.0.0.1\r\ns=-\r\n"
	env := newSubscriptionTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://api.openai.com/v1/realtime/calls/call_123")
		w.Header().Set("Content-Type", "application/sdp")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("answer-sdp"))
	}, svc.CodexSubscriptionProxyModeSubscription)

	resp := env.request(http.MethodPost, "realtime/calls?foo=bar", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer client-token")
		r.Header.Set("Content-Type", "application/sdp")
		r.Body = io.NopCloser(strings.NewReader(sdp))
		r.ContentLength = int64(len(sdp))
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("状态码不符: %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "https://api.openai.com/v1/realtime/calls/call_123" {
		t.Fatalf("Location 未回传: %q", got)
	}
	if got := readBody(t, resp); got != "answer-sdp" {
		t.Fatalf("响应体不符: %q", got)
	}

	requests := env.upstreamRequests()
	if len(requests) != 1 {
		t.Fatalf("上游请求数不符: %d", len(requests))
	}
	if requests[0].Query != "foo=bar" {
		t.Fatalf("语音建连 query 未保留: %q", requests[0].Query)
	}
	if string(requests[0].Body) != sdp {
		t.Fatalf("SDP 正文被改写: %q", string(requests[0].Body))
	}
	if got := requests[0].Header.Get("Content-Type"); got != "application/sdp" {
		t.Fatalf("语音 Content-Type 未保留: %q", got)
	}
}

func TestSubscriptionHandlerForwardsImageGenerationBody(t *testing.T) {
	env := newSubscriptionTestEnv(t, nil, svc.CodexSubscriptionProxyModeSubscription)

	payload := map[string]any{"model": "gpt-image", "prompt": strings.Repeat("x", 3<<20)}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("构造生图请求失败: %v", err)
	}

	resp := env.request(http.MethodPost, "images/generations", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer client-token")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("x-codex-image-turn-id", "turn-9")
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码不符: %d", resp.StatusCode)
	}

	requests := env.upstreamRequests()
	if len(requests) != 1 {
		t.Fatalf("上游请求数不符: %d", len(requests))
	}
	if len(requests[0].Body) != len(raw) {
		t.Fatalf("生图正文长度不符: %d != %d", len(requests[0].Body), len(raw))
	}
	if requests[0].Header.Get("X-Codex-Image-Turn-Id") != "turn-9" {
		t.Fatal("生图轮次头未透传")
	}
}

func TestSubscriptionHandlerNormalizesZstdRequestBody(t *testing.T) {
	env := newSubscriptionTestEnv(t, nil, svc.CodexSubscriptionProxyModeSubscription)

	plain := []byte(`{"model":"gpt-5","input":"hello"}`)
	compressed := zstdCompress(t, plain)

	resp := env.request(http.MethodPost, "responses", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer client-token")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Content-Encoding", "zstd")
		r.Body = io.NopCloser(bytes.NewReader(compressed))
		r.ContentLength = int64(len(compressed))
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码不符: %d", resp.StatusCode)
	}

	requests := env.upstreamRequests()
	if len(requests) != 1 {
		t.Fatalf("上游请求数不符: %d", len(requests))
	}
	// 客户端用 zstd，出站仍为 zstd；正文未变时复用入站压缩帧，解压后必须等于规范化明文。
	if got := requests[0].Header.Get("Content-Encoding"); got != "zstd" {
		t.Fatalf("出站压缩标记不符: %q", got)
	}
	decoded := zstdDecompress(t, requests[0].Body)
	if !bytes.Equal(decoded, plain) {
		t.Fatalf("上游收到的正文不等于规范化明文: %q", string(decoded))
	}
}

// 订阅直通：隐私过滤器原地修改正文时，不能因切片相等而复用含原文的客户端压缩帧。
func TestSubscriptionHandlerRecompressesAfterInPlacePrivacyEdit(t *testing.T) {
	env := newSubscriptionTestEnv(t, nil, svc.CodexSubscriptionProxyModeSubscription)
	env.handler.proxy.SetPrivacyFilter(identityPrivacyMutator(func(raw []byte) []byte {
		if at := bytes.Index(raw, []byte("SECRET")); at >= 0 {
			copy(raw[at:], "XXXXXX")
		}
		return raw
	}))
	compressed := zstdCompress(t, []byte(`{"model":"gpt-5","input":"SECRET"}`))
	resp := env.request(http.MethodPost, "responses", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer client-token")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Content-Encoding", "zstd")
		r.Body = io.NopCloser(bytes.NewReader(compressed))
		r.ContentLength = int64(len(compressed))
	})
	defer resp.Body.Close()
	requests := env.upstreamRequests()
	if resp.StatusCode != http.StatusOK || len(requests) != 1 {
		t.Fatalf("状态码=%d 上游请求数=%d", resp.StatusCode, len(requests))
	}
	if bytes.Equal(requests[0].Body, compressed) || bytes.Contains(zstdDecompress(t, requests[0].Body), []byte("SECRET")) {
		t.Fatal("原地脱敏后仍复用了原始压缩帧")
	}
}

func zstdDecompress(t *testing.T, payload []byte) []byte {
	t.Helper()
	reader, err := zstd.NewReader(bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("创建 zstd reader 失败: %v", err)
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("zstd 解压失败: %v", err)
	}
	return data
}

func TestSubscriptionHandlerStreamsSSEWithoutBuffering(t *testing.T) {
	firstEventWritten := make(chan struct{})
	release := make(chan struct{})
	env := newSubscriptionTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: one\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(firstEventWritten)
		<-release
		_, _ = w.Write([]byte("data: two\n\n"))
	}, svc.CodexSubscriptionProxyModeSubscription)

	resp := env.request(http.MethodPost, "responses", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer client-token")
		r.Header.Set("Accept", "text/event-stream")
		r.Body = io.NopCloser(strings.NewReader(`{"stream":true}`))
		r.ContentLength = int64(len(`{"stream":true}`))
	})
	defer resp.Body.Close()

	select {
	case <-firstEventWritten:
	case <-time.After(5 * time.Second):
		t.Fatal("上游未写入首个事件")
	}

	reader := newSSEReader(t, resp.Body)
	if got := reader.next(t, 3*time.Second); got != "data: one\n\n" {
		t.Fatalf("首个事件内容不符: %q", got)
	}
	close(release)
	if got := reader.next(t, 5*time.Second); got != "data: two\n\n" {
		t.Fatalf("第二个事件内容不符: %q", got)
	}
}

// sseReader 按块读取，用于验证流式下行没有被缓冲。
type sseReader struct {
	body io.Reader
}

func newSSEReader(t *testing.T, body io.Reader) *sseReader {
	t.Helper()
	return &sseReader{body: body}
}

func (r *sseReader) next(t *testing.T, timeout time.Duration) string {
	t.Helper()
	type result struct {
		data []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		buf := make([]byte, 64)
		n, err := r.body.Read(buf)
		ch <- result{data: buf[:n], err: err}
	}()
	select {
	case res := <-ch:
		if res.err != nil && len(res.data) == 0 {
			t.Fatalf("读取流式响应失败: %v", res.err)
		}
		return string(res.data)
	case <-time.After(timeout):
		t.Fatal("流式响应被缓冲，未及时下行")
		return ""
	}
}

func TestSubscriptionHandlerRejectsMalformedCapabilityPath(t *testing.T) {
	env := newSubscriptionTestEnv(t, nil, svc.CodexSubscriptionProxyModeSubscription)

	paths := []string{
		"/subscription",
		"/subscription//backend-api/codex/responses",
		"/subscription/secret/backend-api/codex/../responses",
		"/subscription/secret/other-prefix/responses",
		"/v1/responses",
	}
	for _, path := range paths {
		resp, err := env.server.Client().Get(env.server.URL + path)
		if err != nil {
			t.Fatalf("请求失败: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s 状态码不符: %d", path, resp.StatusCode)
		}
	}
	if got := env.upstreamCount(); got != 0 {
		t.Fatalf("非法路径不应触达上游: %d", got)
	}
}

func TestSubscriptionHandlerReturnsStoppedWhenSnapshotMissing(t *testing.T) {
	env := newSubscriptionTestEnv(t, nil, svc.CodexSubscriptionProxyModeSubscription)
	env.setSnapshot(nil)

	resp := env.request(http.MethodPost, "responses", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer client-token")
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("未运行时应返回 503: %d", resp.StatusCode)
	}
}

func TestSubscriptionHandlerMarksUpstreamFailureSafely(t *testing.T) {
	env := newSubscriptionTestEnv(t, nil, svc.CodexSubscriptionProxyModeSubscription)
	env.upstream.Close()

	resp := env.request(http.MethodPost, "responses", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer client-token")
		r.Body = io.NopCloser(strings.NewReader(`{}`))
		r.ContentLength = 2
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("上游不可达应返回 502: %d", resp.StatusCode)
	}
	body := readBody(t, resp)
	if !strings.Contains(body, "codex_subscription_upstream_unreachable") {
		t.Fatalf("错误类型不符: %s", body)
	}
	if strings.Contains(body, env.upstream.URL) {
		t.Fatal("错误信息不应泄露上游地址")
	}
}
