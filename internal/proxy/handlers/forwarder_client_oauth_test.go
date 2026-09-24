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
