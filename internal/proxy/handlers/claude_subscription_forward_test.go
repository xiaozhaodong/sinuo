package handlers

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cc-forwarder/config"
	"cc-forwarder/internal/endpoint"
)

func newClaudeCodeClientRequest() *http.Request {
	src := httptest.NewRequest(http.MethodPost, "/v1/messages?beta=true", nil)
	src.Header.Set("Authorization", "Bearer sk-ant-oat01-client-token")
	src.Header.Set("X-Api-Key", "sk-client-api-key")
	src.Header.Set("Anthropic-Version", "2023-06-01")
	src.Header.Set("Anthropic-Beta", "oauth-2025-04-20,interleaved-thinking-2025-05-14")
	src.Header.Set("X-Claude-Code-Session-Id", "session-1")
	src.Header.Set("X-Stainless-Lang", "js")
	src.Header.Set("Cookie", "sid=1")
	src.Header.Set("Proxy-Authorization", "Basic x")
	src.Header.Set("Forwarded", "for=1.2.3.4")
	src.Header.Set("X-Forwarded-For", "1.2.3.4")
	src.Header.Set("Origin", "https://evil.example.com")
	src.Header.Set("Referer", "https://evil.example.com/page")
	src.Header.Set("Connection", "keep-alive")
	return src
}

func TestCopyClaudeSubscriptionHeaders_PassesClientOAuth(t *testing.T) {
	dst := httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	dst.Header = http.Header{}
	copyClaudeSubscriptionHeaders(newClaudeCodeClientRequest(), dst, ClaudeSubscriptionUpstreamURL)

	want := map[string]string{
		"Authorization":            "Bearer sk-ant-oat01-client-token",
		"Anthropic-Version":        "2023-06-01",
		"Anthropic-Beta":           "oauth-2025-04-20,interleaved-thinking-2025-05-14",
		"X-Claude-Code-Session-Id": "session-1",
		"X-Stainless-Lang":         "js",
	}
	for key, value := range want {
		if got := dst.Header.Get(key); got != value {
			t.Errorf("%s = %q, want %q", key, got, value)
		}
	}
	for _, key := range []string{"X-Api-Key", "Cookie", "Proxy-Authorization", "Forwarded", "X-Forwarded-For", "Origin", "Referer", "Connection"} {
		if got := dst.Header.Get(key); got != "" {
			t.Errorf("订阅直通不得转发 %s，got %q", key, got)
		}
	}
	if dst.Host != "api.anthropic.com" {
		t.Errorf("Host = %q", dst.Host)
	}
}

func TestForwarder_CopyAttemptHeaders_ConfiguredNeverLeaksClientToken(t *testing.T) {
	cfg := &config.Config{}
	manager := endpoint.NewManager(cfg)
	t.Cleanup(manager.Stop)
	forwarder := NewForwarder(cfg, manager)
	target := acquireForwarderTestTarget(t, manager, config.EndpointConfig{
		Name:    "vendor",
		URL:     "https://vendor.example.com",
		ApiKey:  "vendor-key",
		Timeout: time.Second,
	})

	dst := httptest.NewRequest(http.MethodPost, "https://vendor.example.com/v1/messages", nil)
	forwarder.CopyAttemptHeaders(newClaudeCodeClientRequest(), dst, target)

	if got := dst.Header.Get("Authorization"); got != "" {
		t.Fatalf("端点凭据模式绝不能把客户端 OAuth token 发给第三方，got %q", got)
	}
	if got := dst.Header.Get("X-Api-Key"); got != "vendor-key" {
		t.Fatalf("应使用端点 API Key，got %q", got)
	}
}

func TestForwarder_EntryRequestStripsOAuthBetaForEndpoints(t *testing.T) {
	cfg := &config.Config{}
	manager := endpoint.NewManager(cfg)
	t.Cleanup(manager.Stop)
	forwarder := NewForwarder(cfg, manager)
	vendor := acquireForwarderTestTarget(t, manager, config.EndpointConfig{
		Name: "vendor", URL: "https://vendor.example.com", ApiKey: "vendor-key", Timeout: time.Second,
	})
	const beta = "oauth-2025-04-20,interleaved-thinking-2025-05-14"

	copyWith := func(fromEntry bool, betaValue string) http.Header {
		src := newClaudeCodeClientRequest()
		src.Header.Set("Anthropic-Beta", betaValue)
		if fromEntry {
			src = src.WithContext(endpoint.WithClaudeCodeEntry(src.Context(), endpoint.ClaudeCodeEntryRequest{Mode: endpoint.ClaudeCodeEntryModeEndpoint}))
		}
		dst := httptest.NewRequest(http.MethodPost, "https://vendor.example.com/v1/messages", nil)
		forwarder.CopyAttemptHeaders(src, dst, vendor)
		return dst.Header
	}

	if got := copyWith(true, beta).Get("Anthropic-Beta"); got != "interleaved-thinking-2025-05-14" {
		t.Fatalf("入口请求转发给第三方应删掉 oauth- 值、保留其余值，got %q", got)
	}
	if got := copyWith(false, beta).Get("Anthropic-Beta"); got != beta {
		t.Fatalf("主端口请求不改动 anthropic-beta，got %q", got)
	}
	if values := copyWith(true, "OAuth-2026-01-01").Values("Anthropic-Beta"); len(values) != 0 {
		t.Fatalf("只有 OAuth 值时删掉后应移除整个头（前缀大小写不敏感），got %v", values)
	}
	if got := copyWith(true, "a-1, b-2").Get("Anthropic-Beta"); got != "a-1, b-2" {
		t.Fatalf("没有可删的值时保持原样，got %q", got)
	}
}

func TestForwarder_EntryRequestDropsClientSourceHeadersForEndpoints(t *testing.T) {
	cfg := &config.Config{}
	manager := endpoint.NewManager(cfg)
	t.Cleanup(manager.Stop)
	forwarder := NewForwarder(cfg, manager)
	vendor := acquireForwarderTestTarget(t, manager, config.EndpointConfig{
		Name: "vendor", URL: "https://vendor.example.com", ApiKey: "vendor-key", Timeout: time.Second,
	})
	sourceHeaders := []string{"Cookie", "Forwarded", "X-Forwarded-For", "X-Forwarded-Proto", "Origin", "Referer"}

	copyWith := func(fromEntry bool) http.Header {
		src := newClaudeCodeClientRequest()
		src.Header.Set("X-Forwarded-Proto", "https")
		if fromEntry {
			src = src.WithContext(endpoint.WithClaudeCodeEntry(src.Context(), endpoint.ClaudeCodeEntryRequest{Mode: endpoint.ClaudeCodeEntryModeEndpoint}))
		}
		dst := httptest.NewRequest(http.MethodPost, "https://vendor.example.com/v1/messages", nil)
		forwarder.CopyAttemptHeaders(src, dst, vendor)
		return dst.Header
	}

	entry := copyWith(true)
	for _, key := range sourceHeaders {
		if got := entry.Get(key); got != "" {
			t.Errorf("入口端点模式不得把 %s 转发给第三方，got %q", key, got)
		}
	}
	if entry.Get("X-Claude-Code-Session-Id") != "session-1" || entry.Get("X-Api-Key") != "vendor-key" {
		t.Errorf("入口端点模式应保留其余客户端头并写入端点凭据，got %v", entry)
	}
	mainPort := copyWith(false)
	for _, key := range sourceHeaders {
		if mainPort.Get(key) == "" {
			t.Errorf("主端口保持原有透传，%s 不应被删除", key)
		}
	}
}

func TestForwarder_EntryRequestKeepsConfiguredOAuthBeta(t *testing.T) {
	cfg := &config.Config{}
	manager := endpoint.NewManager(cfg)
	t.Cleanup(manager.Stop)
	forwarder := NewForwarder(cfg, manager)
	vendor := acquireForwarderTestTarget(t, manager, config.EndpointConfig{
		Name: "vendor", URL: "https://vendor.example.com", ApiKey: "vendor-key", Timeout: time.Second,
		Headers: map[string]string{"anthropic-beta": "oauth-2025-04-20"},
	})

	src := newClaudeCodeClientRequest()
	src = src.WithContext(endpoint.WithClaudeCodeEntry(src.Context(), endpoint.ClaudeCodeEntryRequest{Mode: endpoint.ClaudeCodeEntryModeEndpoint}))
	dst := httptest.NewRequest(http.MethodPost, "https://vendor.example.com/v1/messages", nil)
	forwarder.CopyAttemptHeaders(src, dst, vendor)

	if got := dst.Header.Get("Anthropic-Beta"); got != "oauth-2025-04-20" {
		t.Fatalf("只清客户端带来的 oauth- 值，端点配置显式写的 anthropic-beta 以配置为准，got %q", got)
	}
}

func TestForwardClaudeSubscription_SendsToFixedUpstreamWithClientToken(t *testing.T) {
	var gotPath, gotAuth, gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := &config.Config{GlobalTimeout: 5 * time.Second}
	manager := endpoint.NewManager(cfg)
	t.Cleanup(manager.Stop)
	forwarder := NewForwarder(cfg, manager)

	body := []byte(`{"model":"claude-sonnet-5","messages":[]}`)
	src := newClaudeCodeClientRequest()
	resp, release, _, err := forwarder.ForwardClaudeSubscription(context.Background(), src, body, upstream.URL, false, false, nil)
	if err != nil {
		t.Fatalf("ForwardClaudeSubscription: %v", err)
	}
	if release != nil {
		release()
	}
	_ = resp.Body.Close()

	if gotPath != "/v1/messages?beta=true" {
		t.Fatalf("上游路径应保留原路径与查询，got %q", gotPath)
	}
	if gotAuth != "Bearer sk-ant-oat01-client-token" {
		t.Fatalf("上游应收到客户端 OAuth token，got %q", gotAuth)
	}
	if !bytes.Equal([]byte(gotBody), body) {
		t.Fatalf("无隐私规则时正文应原样转发，got %s", gotBody)
	}
}
