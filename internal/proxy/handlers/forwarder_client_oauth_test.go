package handlers

import (
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
	return src
}

func TestForwarder_CopyAttemptHeaders_ClientOAuthPassesClientToken(t *testing.T) {
	cfg := &config.Config{}
	manager := endpoint.NewManager(cfg)
	t.Cleanup(manager.Stop)
	forwarder := NewForwarder(cfg, manager)
	target := acquireForwarderTestTarget(t, manager, config.EndpointConfig{
		Name:     "subscription",
		URL:      "https://api.anthropic.com",
		AuthMode: config.EndpointAuthModeClientOAuth,
		Timeout:  time.Second,
		Headers:  map[string]string{"Authorization": "Bearer should-not-override", "X-Extra": "1"},
	})

	dst := httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	forwarder.CopyAttemptHeaders(newClaudeCodeClientRequest(), dst, target)

	want := map[string]string{
		"Authorization":            "Bearer sk-ant-oat01-client-token",
		"Anthropic-Version":        "2023-06-01",
		"Anthropic-Beta":           "oauth-2025-04-20,interleaved-thinking-2025-05-14",
		"X-Claude-Code-Session-Id": "session-1",
		"X-Stainless-Lang":         "js",
		"X-Extra":                  "1",
	}
	for key, value := range want {
		if got := dst.Header.Get(key); got != value {
			t.Errorf("%s = %q, want %q", key, got, value)
		}
	}
	for _, key := range []string{"X-Api-Key", "Cookie", "Proxy-Authorization", "Forwarded", "X-Forwarded-For", "Origin", "Referer"} {
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

func TestForwarder_ClientOAuthBetaStrippedOnlyForEntryToConfigured(t *testing.T) {
	cfg := &config.Config{}
	// 每个目标用独立 manager：同一 manager 内认证边界只保留首个候选的同类端点
	newManager := func() *endpoint.Manager {
		manager := endpoint.NewManager(cfg)
		t.Cleanup(manager.Stop)
		return manager
	}
	forwarder := NewForwarder(cfg, newManager())
	vendor := acquireForwarderTestTarget(t, newManager(), config.EndpointConfig{
		Name: "vendor", URL: "https://vendor.example.com", ApiKey: "vendor-key", Timeout: time.Second,
	})
	subscription := acquireForwarderTestTarget(t, newManager(), config.EndpointConfig{
		Name: "subscription", URL: "https://api.anthropic.com", AuthMode: config.EndpointAuthModeClientOAuth, Timeout: time.Second,
	})
	if subscriptionCfg, vendorCfg := subscription.Config(), vendor.Config(); !subscriptionCfg.IsClientOAuth() || vendorCfg.IsClientOAuth() {
		t.Fatal("测试目标认证方式与预期不符")
	}
	const beta = "oauth-2025-04-20,interleaved-thinking-2025-05-14"

	copyWith := func(target *endpoint.EndpointAttemptTarget, fromEntry bool, betaValue string) http.Header {
		src := newClaudeCodeClientRequest()
		src.Header.Set("Anthropic-Beta", betaValue)
		if fromEntry {
			src = src.WithContext(endpoint.WithClaudeCodeEntry(src.Context(), endpoint.ClaudeCodeEntryRequest{}))
		}
		dst := httptest.NewRequest(http.MethodPost, "https://upstream.example.com/v1/messages", nil)
		forwarder.CopyAttemptHeaders(src, dst, target)
		return dst.Header
	}

	if got := copyWith(vendor, true, beta).Get("Anthropic-Beta"); got != "interleaved-thinking-2025-05-14" {
		t.Fatalf("入口请求转发给第三方应删掉 oauth- 值、保留其余值，got %q", got)
	}
	if got := copyWith(vendor, false, beta).Get("Anthropic-Beta"); got != beta {
		t.Fatalf("主端口请求不改动 anthropic-beta，got %q", got)
	}
	if got := copyWith(subscription, true, beta).Get("Anthropic-Beta"); got != beta {
		t.Fatalf("订阅直通端点必须原样保留 OAuth capability，got %q", got)
	}
	if values := copyWith(vendor, true, "OAuth-2026-01-01").Values("Anthropic-Beta"); len(values) != 0 {
		t.Fatalf("只有 OAuth 值时删掉后应移除整个头（前缀大小写不敏感），got %v", values)
	}
	if got := copyWith(vendor, true, "a-1, b-2").Get("Anthropic-Beta"); got != "a-1, b-2" {
		t.Fatalf("没有可删的值时保持原样，got %q", got)
	}
}
