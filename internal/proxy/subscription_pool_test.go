package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"cc-forwarder/internal/accountauth"
	"cc-forwarder/internal/proxy/handlers"
	svc "cc-forwarder/internal/service"
	"cc-forwarder/internal/store"
)

const (
	poolTestListenHost = "127.0.0.1"
	poolTestListenPort = 18089
	poolTestSecret     = "pool-mode-secret"
)

// captureTransport 记录账号池链路的真实出站请求。
type captureTransport struct {
	mu        sync.Mutex
	requests  []*http.Request
	bodies    [][]byte
	responder func(req *http.Request) (*http.Response, error)
}

func (c *captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
		_ = req.Body.Close()
		req.Body = io.NopCloser(strings.NewReader(string(body)))
	}
	c.mu.Lock()
	c.requests = append(c.requests, req.Clone(req.Context()))
	c.bodies = append(c.bodies, body)
	responder := c.responder
	c.mu.Unlock()

	if responder != nil {
		return responder(req)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"resp_pool","status":"completed","output":[]}`)),
		Request:    req,
	}, nil
}

func (c *captureTransport) captured() ([]*http.Request, [][]byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	requests := make([]*http.Request, len(c.requests))
	copy(requests, c.requests)
	bodies := make([][]byte, len(c.bodies))
	copy(bodies, c.bodies)
	return requests, bodies
}

// poolTestEnv 订阅代理池模式的最小环境。
type poolTestEnv struct {
	handler    *CodexSubscriptionProxyHandler
	transport  *captureTransport
	pool       *mockAccountPoolService
	jsonLogger *slog.Logger
}

func newSubscriptionPoolTestEnv(t *testing.T, accounts []*store.UpstreamAccountRecord, poolEnabled bool) *poolTestEnv {
	t.Helper()
	pool := &mockAccountPoolService{accounts: accounts}
	proxyHandler := newAccountPipelineTestHandlerWithEnabled(t, "http://fallback.invalid", pool, poolEnabled)

	transport := &captureTransport{}
	proxyHandler.accountHTTPInitOnce.Do(func() {})
	proxyHandler.accountHTTPClient = &http.Client{Transport: transport}
	proxyHandler.accountSSEHTTPClient = proxyHandler.accountHTTPClient

	snapshot := &SubscriptionProxySnapshot{
		Revision:      3,
		Enabled:       true,
		Mode:          svc.CodexSubscriptionProxyModePool,
		ListenHost:    poolTestListenHost,
		ListenPort:    poolTestListenPort,
		WSCompression: svc.CodexSubscriptionProxyCompressionAuto,
		AccessHash:    svc.HashCodexSubscriptionProxyAccessSecret(poolTestSecret),
	}
	handler := NewCodexSubscriptionProxyHandler(SubscriptionProxyHandlerOptions{
		Proxy:    proxyHandler,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Snapshot: func() *SubscriptionProxySnapshot { return snapshot },
	})
	return &poolTestEnv{handler: handler, transport: transport, pool: pool}
}

// serve 走完整的新入口路径：Host → capability → 路由 → 池适配器。
func (e *poolTestEnv) serve(t *testing.T, method, operation string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	target := fmt.Sprintf("http://%s:%d/subscription/%s/backend-api/codex/%s",
		poolTestListenHost, poolTestListenPort, poolTestSecret, operation)
	req := httptest.NewRequest(method, target, strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	if mutate != nil {
		mutate(req)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

// clientIdentityHeaders 客户端身份头；池模式下必须一个都不出站。
func clientIdentityHeaders() map[string]string {
	return map[string]string{
		"Authorization":                          "Bearer client-subscription-token",
		"chatgpt-account-id":                     "acct-client",
		"x-oai-attestation":                      "attestation-client",
		"x-openai-subagent":                      "guardian",
		"x-codex-turn-metadata":                  `{"turn":"client"}`,
		"x-codex-beta-features":                  "remote_compaction_v2",
		"thread-id":                              "thread-client",
		"session-id":                             "session-client",
		"x-client-request-id":                    "client-request-id",
		"x-codex-window-id":                      "window-client:0",
		"x-codex-parent-thread-id":               "parent-client",
		"x-codex-routing-hint":                   "model=client",
		"originator":                             "Codex Desktop",
		"version":                                "0.153.4",
		"User-Agent":                             "Codex Desktop/0.153.4",
		"OpenAI-Beta":                            "responses_websockets=2026-02-06",
		"x-openai-internal-codex-responses-lite": "true",
		"x-codex-installation-id":                "installation-client",
		"x-openai-memgen-request":                "memgen-client",
		"Cookie":                                 "session=client-cookie",
		"x-api-key":                              "sk-client",
		"X-Forwarded-For":                        "203.0.113.7",
		"Proxy-Authorization":                    "Basic client",
		"X-Unknown-Client-Header":                "unknown-value",
	}
}

func applyClientIdentityHeaders(req *http.Request) {
	for key, value := range clientIdentityHeaders() {
		req.Header.Set(key, value)
	}
}

func assertNoClientIdentityLeak(t *testing.T, req *http.Request, context string) {
	t.Helper()
	for key, value := range clientIdentityHeaders() {
		if got := req.Header.Get(key); got == value {
			t.Fatalf("%s: 客户端身份头 %s 不应出站: %q", context, key, got)
		}
	}
	for _, key := range []string{"X-Oai-Attestation", "X-Openai-Subagent", "X-Codex-Turn-Metadata", "Thread-Id", "Session-Id", "X-Client-Request-Id", "X-Codex-Window-Id", "X-Codex-Parent-Thread-Id", "X-Codex-Routing-Hint", "Cookie", "X-Api-Key", "X-Forwarded-For", "Proxy-Authorization", "X-Unknown-Client-Header", "X-Codex-Installation-Id", "X-Openai-Memgen-Request"} {
		if got := req.Header.Get(key); got != "" {
			t.Fatalf("%s: 不应出站的头 %s: %q", context, key, got)
		}
	}
	if got := req.Header.Get("Authorization"); strings.Contains(got, "client-subscription-token") {
		t.Fatalf("%s: 客户端 token 泄漏: %q", context, got)
	}
	if got := req.Header.Get("Chatgpt-Account-Id"); got == "acct-client" {
		t.Fatalf("%s: 客户端账号 ID 泄漏: %q", context, got)
	}
	if got := req.Header.Get("Originator"); got == "Codex Desktop" {
		t.Fatalf("%s: 客户端 originator 泄漏: %q", context, got)
	}
	if got := req.Header.Get("User-Agent"); strings.Contains(got, "Codex Desktop") {
		t.Fatalf("%s: 客户端 UA 泄漏: %q", context, got)
	}
}

func TestSubscriptionPoolModeAnswers426WithoutUpstream(t *testing.T) {
	env := newSubscriptionPoolTestEnv(t, []*store.UpstreamAccountRecord{
		{ID: 1, AccountName: "pool-a", ProviderType: accountauth.ProviderAPIKey, CredentialRaw: "sk-account-a", BaseURL: "http://upstream.invalid", Enabled: true},
	}, true)

	rec := env.serve(t, http.MethodGet, "responses", func(r *http.Request) {
		applyClientIdentityHeaders(r)
		r.Header.Set("Upgrade", "websocket")
		r.Header.Set("Connection", "Upgrade")
		r.Header.Set("Sec-WebSocket-Version", "13")
		r.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	})

	if rec.Code != http.StatusUpgradeRequired {
		t.Fatalf("池模式 WS 应返回 426: %d", rec.Code)
	}
	requests, _ := env.transport.captured()
	if len(requests) != 0 {
		t.Fatalf("426 不应产生任何上游请求: %d", len(requests))
	}
	if len(env.pool.prepareCalls) != 0 {
		t.Fatalf("426 不应进入账号池调度: %d", len(env.pool.prepareCalls))
	}
}

func TestSubscriptionPoolIsolatesClientHeadersForAPIKey(t *testing.T) {
	env := newSubscriptionPoolTestEnv(t, []*store.UpstreamAccountRecord{
		{ID: 11, AccountName: "pool-api-key", ProviderType: accountauth.ProviderAPIKey, CredentialRaw: "sk-pool-account", BaseURL: "http://upstream.invalid", Enabled: true},
	}, true)

	rec := env.serve(t, http.MethodPost, "responses", applyClientIdentityHeaders)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码不符: %d body=%s", rec.Code, rec.Body.String())
	}

	requests, bodies := env.transport.captured()
	if len(requests) != 1 {
		t.Fatalf("出站请求数不符: %d", len(requests))
	}
	assertNoClientIdentityLeak(t, requests[0], "API Key 池请求")
	if got := requests[0].Header.Get("Authorization"); got != "Bearer sk-pool-account" {
		t.Fatalf("应使用池账号凭据: %q", got)
	}
	if got := requests[0].Header.Get("Chatgpt-Account-Id"); got != "" {
		t.Fatalf("API Key 目标不应带账号 ID: %q", got)
	}
	if got := requests[0].Header.Get("Accept"); got != "application/json" && got != "" {
		t.Fatalf("Accept 应只来自客户端白名单: %q", got)
	}
	if got := requests[0].Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type 未保留: %q", got)
	}
	if !strings.Contains(string(bodies[0]), `"model":"gpt-5"`) {
		t.Fatalf("请求正文不符: %s", string(bodies[0]))
	}
}

func TestSubscriptionPoolIsolatesClientHeadersForOAuth(t *testing.T) {
	credential := oauthCredentialJSON(t, "at-pool-account", "acct-pool-oauth")
	env := newSubscriptionPoolTestEnv(t, []*store.UpstreamAccountRecord{
		{ID: 21, AccountName: "pool-oauth", ProviderType: accountauth.ProviderChatGPTRefreshToken, CredentialRaw: credential, BaseURL: "http://upstream.invalid", Enabled: true},
	}, true)

	rec := env.serve(t, http.MethodPost, "responses", applyClientIdentityHeaders)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码不符: %d body=%s", rec.Code, rec.Body.String())
	}

	requests, _ := env.transport.captured()
	if len(requests) != 1 {
		t.Fatalf("出站请求数不符: %d", len(requests))
	}
	assertNoClientIdentityLeak(t, requests[0], "OAuth 池请求")
	if got := requests[0].Header.Get("Authorization"); got != "Bearer at-pool-account" {
		t.Fatalf("应使用池账号 AT: %q", got)
	}
	if got := requests[0].Header.Get("Chatgpt-Account-Id"); got != "acct-pool-oauth" {
		t.Fatalf("应使用池账号自己的账号 ID: %q", got)
	}
	if got := requests[0].Header.Get("Openai-Beta"); got != openAIBetaResponsesValue {
		t.Fatalf("OAuth 协议头应由账号 helper 生成: %q", got)
	}
	if got := requests[0].Header.Get("Originator"); got != defaultOAuthOriginator {
		t.Fatalf("originator 应由账号 helper 生成: %q", got)
	}
}

func TestSubscriptionPoolIsolationHoldsOnFailover(t *testing.T) {
	firstAttempts := 0
	env := newSubscriptionPoolTestEnv(t, []*store.UpstreamAccountRecord{
		{ID: 31, AccountName: "pool-first", ProviderType: accountauth.ProviderAPIKey, CredentialRaw: "sk-first-account", BaseURL: "http://upstream.invalid", Enabled: true},
		{ID: 32, AccountName: "pool-second", ProviderType: accountauth.ProviderAPIKey, CredentialRaw: "sk-second-account", BaseURL: "http://upstream.invalid", Enabled: true},
	}, true)
	env.transport.responder = func(req *http.Request) (*http.Response, error) {
		firstAttempts++
		if firstAttempts == 1 {
			return &http.Response{
				StatusCode: http.StatusUnauthorized,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"invalid token"}}`)),
				Request:    req,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"resp_pool","status":"completed","output":[]}`)),
			Request:    req,
		}, nil
	}

	rec := env.serve(t, http.MethodPost, "responses", applyClientIdentityHeaders)
	if rec.Code != http.StatusOK {
		t.Fatalf("failover 后应成功: %d body=%s", rec.Code, rec.Body.String())
	}

	requests, _ := env.transport.captured()
	if len(requests) != 2 {
		t.Fatalf("应有两次尝试: %d", len(requests))
	}
	assertNoClientIdentityLeak(t, requests[0], "failover 第一次尝试")
	assertNoClientIdentityLeak(t, requests[1], "failover 第二次尝试")
	if got := requests[0].Header.Get("Authorization"); got != "Bearer sk-first-account" {
		t.Fatalf("第一次尝试凭据不符: %q", got)
	}
	if got := requests[1].Header.Get("Authorization"); got != "Bearer sk-second-account" {
		t.Fatalf("第二次尝试应换成第二个账号: %q", got)
	}
	if !strings.Contains(requests[1].Header.Get("Authorization"), "sk-second-account") {
		t.Fatal("failover 后仍在使用第一个账号凭据")
	}
}

func TestSubscriptionPoolNormalizesZstdBodyBeforeIsolation(t *testing.T) {
	env := newSubscriptionPoolTestEnv(t, []*store.UpstreamAccountRecord{
		{ID: 41, AccountName: "pool-zstd", ProviderType: accountauth.ProviderAPIKey, CredentialRaw: "sk-zstd-account", BaseURL: "http://upstream.invalid", Enabled: true},
	}, true)

	plain := []byte(`{"model":"gpt-5","input":"pool zstd"}`)
	compressed := zstdCompress(t, plain)
	rec := env.serve(t, http.MethodPost, "responses", func(r *http.Request) {
		applyClientIdentityHeaders(r)
		r.Header.Set("Content-Encoding", "zstd")
		r.Body = io.NopCloser(strings.NewReader(string(compressed)))
		r.ContentLength = int64(len(compressed))
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码不符: %d body=%s", rec.Code, rec.Body.String())
	}

	requests, bodies := env.transport.captured()
	if len(requests) != 1 {
		t.Fatalf("出站请求数不符: %d", len(requests))
	}
	if string(bodies[0]) != string(plain) {
		t.Fatalf("池链路正文应为规范化明文: %q", string(bodies[0]))
	}
	if got := requests[0].Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("客户端入站压缩标记不应出站: %q", got)
	}
}

func TestSubscriptionPoolUnavailableDoesNotFallBackToSubscription(t *testing.T) {
	env := newSubscriptionPoolTestEnv(t, []*store.UpstreamAccountRecord{
		{ID: 51, AccountName: "pool-disabled", ProviderType: accountauth.ProviderAPIKey, CredentialRaw: "sk-disabled", BaseURL: "http://upstream.invalid", Enabled: true},
	}, false)

	rec := env.serve(t, http.MethodPost, "responses", applyClientIdentityHeaders)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("池未启用应返回 503: %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析错误响应失败: %v", err)
	}
	errObj, _ := payload["error"].(map[string]any)
	if errObj["type"] != "account_pool_disabled" {
		t.Fatalf("错误类型不符: %v", errObj["type"])
	}
	requests, _ := env.transport.captured()
	if len(requests) != 0 {
		t.Fatalf("池不可用时不应有任何上游请求: %d", len(requests))
	}
}

func TestSubscriptionPoolKeepsAuthFailuresFromClientCredentials(t *testing.T) {
	env := newSubscriptionPoolTestEnv(t, []*store.UpstreamAccountRecord{}, true)

	rec := env.serve(t, http.MethodPost, "responses", applyClientIdentityHeaders)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("无可用账号应返回 503: %d", rec.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析错误响应失败: %v", err)
	}
	errObj, _ := payload["error"].(map[string]any)
	if errObj["type"] != "account_pool_unavailable" {
		t.Fatalf("错误类型不符: %v", errObj["type"])
	}
}

// oauthCredentialJSON 构造未过期的本地 OAuth 凭据，避免真实刷新请求。
func oauthCredentialJSON(t *testing.T, accessToken, accountID string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"access_token":       accessToken,
		"refresh_token":      "rt-pool-account",
		"expires_at":         time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
		"chatgpt_account_id": accountID,
	})
	if err != nil {
		t.Fatalf("构造 OAuth 凭据失败: %v", err)
	}
	return string(payload)
}

// 保证测试引用了 handlers 包（PrivacyRequestState 后续由 P2 接入）。
var _ = handlers.NewPrivacyRequestState
