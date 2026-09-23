package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cc-forwarder/internal/accountauth"
	"cc-forwarder/internal/codexidentity"
	svc "cc-forwarder/internal/service"
	"cc-forwarder/internal/store"
)

type identityProviderFunc func(context.Context) (codexidentity.Policy, error)

func (f identityProviderFunc) CodexIdentityPolicy(ctx context.Context) (codexidentity.Policy, error) {
	return f(ctx)
}
func enabledIdentityPolicy(t *testing.T) codexidentity.Policy {
	t.Helper()
	n, err := codexidentity.ParseNamespace("067cbe02-1334-45d1-85c0-df7f459055ef")
	if err != nil {
		t.Fatal(err)
	}
	return codexidentity.Policy{Enabled: true, Version: codexidentity.DefaultCompatVersion, Namespace: n}
}
func identityAccount(t *testing.T, id int64) *store.UpstreamAccountRecord {
	t.Helper()
	return &store.UpstreamAccountRecord{ID: id, AccountName: fmt.Sprintf("oauth-%d", id), ProviderType: accountauth.ProviderChatGPTRefreshToken, CredentialRaw: oauthCredentialJSON(t, "at-fixture", fmt.Sprintf("acct-%d", id)), BaseURL: "https://chatgpt.com/backend-api/codex", Enabled: true}
}
func identityRequestFixture(subscription bool, body string) *http.Request {
	url := "http://localhost/v1/responses"
	if subscription {
		url = fmt.Sprintf("http://%s:%d/subscription/%s/backend-api/codex/responses", poolTestListenHost, poolTestListenPort, poolTestSecret)
	}
	r := httptest.NewRequest(http.MethodPost, url, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("User-Agent", "codex_cli_rs/0.155.1 (Mac OS 27.0.0; arm64) unknown")
	r.Header.Set("originator", "codex_cli_rs")
	r.Header.Set("version", "0.155.1")
	r.Header.Set("session-id", "source")
	r.Header.Set("thread-id", "source")
	r.Header.Set("x-client-request-id", "source")
	r.Header.Set("x-codex-window-id", "source:0")
	r.Header.Set("Authorization", "Bearer source-secret")
	r.Header.Set("chatgpt-account-id", "source-account")
	r.Header.Set("x-oai-attestation", "source-proof")
	return r
}
func identityServe(env *poolTestEnv, subscription bool, w http.ResponseWriter, r *http.Request) {
	if subscription {
		env.handler.ServeHTTP(w, r)
	} else {
		env.handler.proxy.ServeHTTP(w, r)
	}
}

func TestAccountIdentityTwoEntryTargetMatrix(t *testing.T) {
	for _, subscription := range []bool{false, true} {
		for _, target := range []string{"oauth", "api-key", "oauth-relay"} {
			t.Run(fmt.Sprintf("subscription=%v/%s", subscription, target), func(t *testing.T) {
				acc := identityAccount(t, 1)
				if target == "api-key" {
					acc.ProviderType = accountauth.ProviderAPIKey
					acc.CredentialRaw = "sk-fixture"
					acc.BaseURL = "https://relay.test/v1"
					acc.ModelRewriteRules = testGPT54ModelRewriteRules
				}
				if target == "oauth-relay" {
					acc.BaseURL = "https://relay.test/v1"
				}
				env := newSubscriptionPoolTestEnv(t, []*store.UpstreamAccountRecord{acc}, true)
				policy := enabledIdentityPolicy(t)
				env.handler.proxy.SetCodexIdentityProvider(identityProviderFunc(func(context.Context) (codexidentity.Policy, error) { return policy, nil }))
				body := ` { "model":"gpt-5.4", "prompt_cache_key":"source", "client_metadata":{"session_id":"source","thread_id":"source","local_path":"/private/workspace"}, "metadata":{"business":9007199254740993}, "input":[{"text":"source","encrypted_content":"abc\\xyz","id":"server-id","number":1.2300e+19}] } `
				r := identityRequestFixture(subscription, body)
				r.Header["connection"] = []string{"Authorization, User-Agent", "Originator, version, X-Drop"}
				r.Header.Set("X-Drop", "source")
				r.Header.Set("Cookie", "private")
				r.Header.Set("Forwarded", "private")
				r.Header.Set("X-Forwarded-For", "private")
				rec := httptest.NewRecorder()
				identityServe(env, subscription, rec, r)
				if rec.Code != 200 {
					t.Fatalf("%d %s", rec.Code, rec.Body.String())
				}
				reqs, bodies := env.transport.captured()
				if len(reqs) != 1 {
					t.Fatalf("请求数 %d", len(reqs))
				}
				got := reqs[0]
				for _, header := range []string{"Cookie", "Forwarded", "X-Forwarded-For", "X-Drop", "X-Oai-Attestation"} {
					if got.Header.Get(header) != "" {
						t.Fatal("泄漏", header)
					}
				}
				if strings.Contains(got.Header.Get("Authorization"), "source") || got.Header.Get("Authorization") == "" || got.Header.Get("chatgpt-account-id") == "source-account" {
					t.Fatal("来源凭据混入目标")
				}
				fields, _ := codexidentity.JSONFields(bodies[0])
				before, _ := codexidentity.JSONFields([]byte(body))
				if !bytes.Equal(fields["input"], before["input"]) || !bytes.Equal(fields["metadata"], before["metadata"]) {
					t.Fatal("用户原始表示变化")
				}
				if target != "api-key" {
					var cache string
					_ = json.Unmarshal(fields["prompt_cache_key"], &cache)
					if cache == "source" || cache != got.Header.Get("session-id") || cache != got.Header.Get("thread-id") || got.Header.Get("chatgpt-account-id") != "acct-1" {
						t.Fatalf("OAuth 映射错误 %v %s", got.Header, bodies[0])
					}
					if !strings.Contains(got.UserAgent(), "codex_cli_rs/0.155.1") || got.Header.Get("OpenAI-Beta") != "" {
						t.Fatal("HTTP 描述错误")
					}
				} else {
					if fields["client_metadata"] != nil || string(fields["prompt_cache_key"]) != `"source"` || got.Header.Get("session-id") != "" {
						t.Fatal("第三方契约未隔离")
					}
					if target == "api-key" && string(fields["model"]) != `"gpt-5.5"` {
						t.Fatal("模型改写丢失")
					}
				}
			})
		}
	}
}

func TestAccountIdentitySnapshotRetryAndFreshOriginalBody(t *testing.T) {
	policy := enabledIdentityPolicy(t)
	reads := 0
	a, b := identityAccount(t, 1), identityAccount(t, 2)
	env := newSubscriptionPoolTestEnv(t, []*store.UpstreamAccountRecord{a, b}, true)
	env.handler.proxy.SetCodexIdentityProvider(identityProviderFunc(func(context.Context) (codexidentity.Policy, error) { reads++; return policy, nil }))
	first := true
	env.transport.responder = func(req *http.Request) (*http.Response, error) {
		status, body := 200, `{"id":"resp_1","status":"completed","output":[]}`
		if first {
			first = false
			status = 500
			body = `{"error":{"message":"temporary backend failure"}}`
			policy.Enabled = false
			policy.Version = "0.156.0"
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	}
	body := `{"model":"gpt-5","prompt_cache_key":"source","client_metadata":{"session_id":"source","thread_id":"source"},"input":"source"}`
	r := identityRequestFixture(false, body)
	rec := httptest.NewRecorder()
	identityServe(env, false, rec, r)
	if rec.Code != 200 {
		t.Fatalf("重试失败 %d %s", rec.Code, rec.Body.String())
	}
	reqs, bodies := env.transport.captured()
	if len(reqs) != 2 || reads != 1 {
		t.Fatalf("策略在重试重读: requests=%d reads=%d", len(reqs), reads)
	}
	if reqs[0].Header.Get("version") != "0.155.1" || reqs[1].Header.Get("version") != "0.155.1" || reqs[0].Header.Get("session-id") == reqs[1].Header.Get("session-id") {
		t.Fatal("重试策略或账号隔离错误")
	}
	// A -> B -> A 使用同一来源和原始正文，回到 A 的字节与第一次完全一致。
	resp, cancel, _, err := env.handler.proxy.forwardRequestToAccount(r.Context(), r, []byte(body), a, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cancel != nil {
		cancel()
	}
	_ = resp.Body.Close()
	_, after := env.transport.captured()
	if !bytes.Equal(after[2], bodies[0]) {
		t.Fatal("重复映射或来源正文污染")
	}
	newReq := identityRequestFixture(false, body)
	rec = httptest.NewRecorder()
	identityServe(env, false, rec, newReq)
	if rec.Code != 200 || reads != 2 {
		t.Fatal("后续请求未读取新策略")
	}
	_, after = env.transport.captured()
	if !bytes.Equal(after[len(after)-1], []byte(body)) {
		t.Fatal("关闭后未恢复原始关联语义")
	}
	// Forward 的 attempt 子 context 必须保留其 deadline/cancellation。
	ctx, cancel := context.WithCancel(r.Context())
	cancel()
	ctx, err = env.handler.proxy.attachCodexIdentityRequest(ctx, r)
	if err != nil || ctx.Err() != context.Canceled {
		t.Fatal("策略快照覆盖了 attempt 取消信号")
	}
}

func TestAccountIdentityDisabledPreservesThirdPartyTurnState(t *testing.T) {
	acc := identityAccount(t, 1)
	acc.ProviderType = accountauth.ProviderAPIKey
	acc.CredentialRaw = "sk-fixture"
	acc.BaseURL = "https://relay.test/v1"
	env := newSubscriptionPoolTestEnv(t, []*store.UpstreamAccountRecord{acc}, true)
	policy := enabledIdentityPolicy(t)
	policy.Enabled = false
	env.handler.proxy.SetCodexIdentityProvider(identityProviderFunc(func(context.Context) (codexidentity.Policy, error) { return policy, nil }))
	env.transport.responder = func(req *http.Request) (*http.Response, error) {
		h := make(http.Header)
		h.Set(codexidentity.HeaderTurnState, "upstream-state")
		h.Set("Content-Type", "application/json")
		return &http.Response{StatusCode: http.StatusOK, Header: h, Body: io.NopCloser(strings.NewReader(`{"id":"resp_1"}`)), Request: req}, nil
	}
	body := `{"model":"gpt-5","client_metadata":{"x-codex-turn-state":"source-state"},"input":"hello"}`
	r := identityRequestFixture(false, body)
	r.Header.Set("x-codex-turn-state", "source-state")
	resp, cancel, _, err := env.handler.proxy.forwardRequestToAccount(r.Context(), r, []byte(body), acc, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cancel != nil {
		cancel()
	}
	defer resp.Body.Close()
	requests, bodies := env.transport.captured()
	if len(requests) != 1 || requests[0].Header.Get(codexidentity.HeaderTurnState) != "source-state" {
		t.Fatalf("关闭开关后请求状态头未透传: %+v", requests)
	}
	bodyHasState := strings.Contains(string(bodies[0]), `"x-codex-turn-state":"source-state"`)
	responseState := resp.Header.Get(codexidentity.HeaderTurnState)
	if !bodyHasState || responseState != "upstream-state" {
		t.Fatalf("关闭开关后状态语义被改写: body=%s body_has_state=%v response_state=%q response=%v", bodies[0], bodyHasState, responseState, resp.Header)
	}
}

func TestAccountIdentityLocalErrorsDoNotFailover(t *testing.T) {
	for _, subscription := range []bool{false, true} {
		for _, kind := range []string{"conflict", "namespace", "privacy", "feature"} {
			t.Run(fmt.Sprintf("%v/%s", subscription, kind), func(t *testing.T) {
				env := newSubscriptionPoolTestEnv(t, []*store.UpstreamAccountRecord{identityAccount(t, 1), identityAccount(t, 2)}, true)
				p := enabledIdentityPolicy(t)
				body := `{"model":"gpt-5","client_metadata":{"session_id":"source"},"input":"hello"}`
				switch kind {
				case "conflict":
					body = `{"model":"gpt-5","client_metadata":{"session_id":"other"}}`
				case "namespace":
					p.Namespace = codexidentity.Namespace{}
				case "privacy":
					env.handler.proxy.SetPrivacyFilter(&stubPrivacyFilter{redactTo: []byte(`{"model":"gpt-5","client_metadata":{"session_id":"redacted"},"input":"hello"}`)})
				}
				env.handler.proxy.SetCodexIdentityProvider(identityProviderFunc(func(context.Context) (codexidentity.Policy, error) { return p, nil }))
				r := identityRequestFixture(subscription, body)
				if kind == "feature" {
					// 未知 beta 值已改为透传；只有无法按协议解释的取值才是本地错误。
					r.Header.Set(codexidentity.HeaderResponsesLite, "maybe")
				}
				rec := httptest.NewRecorder()
				identityServe(env, subscription, rec, r)
				requests, _ := env.transport.captured()
				if rec.Code < 400 || len(requests) != 0 || len(env.pool.softFailureCalls) != 0 || len(env.pool.authFailCalls) != 0 || len(env.pool.transientCalls) != 0 || len(env.pool.completeCalls) != 1 {
					t.Fatalf("本地错误触发上游/故障转移 %d %s", rec.Code, rec.Body.String())
				}
				if !strings.Contains(rec.Body.String(), "codex_identity_") {
					t.Fatal("缺少明确错误码")
				}
			})
		}
	}
}

// 自动模式官方 OAuth：功能声明不看客户端描述原样出站，未识别首方扩展头与 metadata 键在两个入口都不出站。
func TestAccountIdentityForwardsFeatureDeclarationsAndDropsUnknownExtensions(t *testing.T) {
	for _, entry := range []string{"global", "subscription"} {
		t.Run(entry, func(t *testing.T) {
			env := identityEntryEnv(t, entry, []*store.UpstreamAccountRecord{identityAccount(t, 1)})
			p := enabledIdentityPolicy(t)
			p.Automatic = true
			env.handler.proxy.SetCodexIdentityProvider(identityProviderFunc(func(context.Context) (codexidentity.Policy, error) { return p, nil }))
			env.transport.responder = func(req *http.Request) (*http.Response, error) {
				return identityStateResponse(req, 200), nil
			}
			subscription := entry == "subscription"
			r := identityRequestFixture(subscription, `{"model":"gpt-5","client_metadata":{"session_id":"source","x-codex-future-id":"raw-body-id","plain_key":"keep"},"input":"hello"}`)
			r.Header.Set("User-Agent", "my-tool/1.0")
			r.Header.Set(codexidentity.HeaderBetaFeatures, "remote_compaction_v2,future_flag")
			r.Header.Set(codexidentity.HeaderResponsesLite, "true")
			r.Header.Set("X-Codex-Future-Id", "raw-header-id")
			rec := httptest.NewRecorder()
			identityServe(env, subscription, rec, r)
			if rec.Code != 200 {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
			reqs, bodies := env.transport.captured()
			if len(reqs) != 1 {
				t.Fatalf("请求数=%d", len(reqs))
			}
			got := reqs[0]
			if got.Header.Get(codexidentity.HeaderBetaFeatures) != "remote_compaction_v2,future_flag" || got.Header.Get(codexidentity.HeaderResponsesLite) != "true" {
				t.Fatalf("功能声明未原样出站: %v", got.Header)
			}
			if got.Header.Get("X-Codex-Future-Id") != "" || strings.Contains(string(bodies[0]), "raw-body-id") || !strings.Contains(string(bodies[0]), `"plain_key":"keep"`) {
				t.Fatalf("未识别首方扩展处理错误: header=%v body=%s", got.Header, bodies[0])
			}
		})
	}
}

func TestIdentityExtensionWarningRateLimit(t *testing.T) {
	now := time.Now()
	key := fmt.Sprintf("test:%d", now.UnixNano())
	if !allowIdentityExtensionWarning(key, now) || allowIdentityExtensionWarning(key, now.Add(time.Minute)) {
		t.Fatal("同一名称未限频")
	}
	if !allowIdentityExtensionWarning(key, now.Add(identityExtensionWarnInterval)) {
		t.Fatal("限频窗口过后未恢复告警")
	}
	for i := range identityExtensionWarnCapacity + 1 {
		allowIdentityExtensionWarning(fmt.Sprintf("%s:%d", key, i), now)
	}
	identityExtensionWarnings.Lock()
	size := len(identityExtensionWarnings.last)
	identityExtensionWarnings.Unlock()
	if size > identityExtensionWarnCapacity {
		t.Fatalf("告警记录表未限量: %d", size)
	}
}

func TestAccountIdentityOperationContracts(t *testing.T) {
	acc := identityAccount(t, 1)
	env := newSubscriptionPoolTestEnv(t, []*store.UpstreamAccountRecord{acc}, true)
	p := enabledIdentityPolicy(t)
	env.handler.proxy.SetCodexIdentityProvider(identityProviderFunc(func(context.Context) (codexidentity.Policy, error) { return p, nil }))
	for _, path := range []string{"/v1/responses/compact", "/v1/models", "/v1/alpha/search"} {
		r := identityRequestFixture(false, `{"model":"gpt-5"}`)
		r.URL.Path = path
		body := []byte(`{"model":"gpt-5"}`)
		if path == "/v1/models" {
			r.Method = http.MethodGet
			body = nil
			r.URL.RawQuery = "client_version=old&keep=yes"
		}
		resp, cancel, _, err := env.handler.proxy.forwardRequestToAccount(r.Context(), r, body, acc, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		if cancel != nil {
			cancel()
		}
		_ = resp.Body.Close()
	}
	reqs, _ := env.transport.captured()
	for _, r := range reqs {
		if r.Header.Get("OpenAI-Beta") != "" || r.Header.Get("x-codex-routing-hint") != "" {
			t.Fatal("Response 专用头泄漏到辅助操作", r.URL)
		}
	}
	if reqs[0].Header.Get("Accept") != "application/json" || reqs[1].Header.Get("Accept") != "*/*" || reqs[2].Header.Get("Accept") != "application/json" {
		t.Fatalf("辅助操作 Accept 契约错误: %q %q %q", reqs[0].Header.Get("Accept"), reqs[1].Header.Get("Accept"), reqs[2].Header.Get("Accept"))
	}
	if reqs[1].URL.Query().Get("client_version") != "0.155.1" || reqs[1].URL.Query().Get("keep") != "yes" || reqs[1].Header.Get("session-id") != "" {
		t.Fatal("models 契约错误")
	}
}

func identityEntryEnv(t *testing.T, entry string, accounts []*store.UpstreamAccountRecord) *poolTestEnv {
	t.Helper()
	env := newSubscriptionPoolTestEnv(t, accounts, true)
	if strings.Contains(entry, "model-routing") {
		env.pool.accounts = nil // 路由必须提供候选账号，不能误走全局池。
		env.handler.proxy.SetCodexModelRouter(&mockCodexModelRouter{
			enabled: true,
			result: svc.CodexModelRouteResult{
				Accounts: accounts,
				Scope: svc.CodexModelRouteScope{
					Kind: "upstream_group", GroupID: 1, Revision: 1,
					SnapshotVersion: 1, Mode: "auto", Model: "gpt-5",
				},
			},
		})
	}
	return env
}

func identityAPIKeyAccount(id int64) *store.UpstreamAccountRecord {
	return &store.UpstreamAccountRecord{
		ID: id, AccountName: "relay", ProviderType: accountauth.ProviderAPIKey,
		CredentialRaw: "sk-fixture", BaseURL: "https://relay.test/v1", Enabled: true,
	}
}

func TestAccountIdentityAutomaticEntryTargets(t *testing.T) {
	for _, entry := range []string{"global", "subscription", "model-routing", "subscription-model-routing"} {
		for _, oauth := range []bool{false, true} {
			for _, mode := range []codexidentity.IdentityMode{
				codexidentity.IdentityModeAuto, codexidentity.IdentityModeEnabled, codexidentity.IdentityModeDisabled,
			} {
				t.Run(fmt.Sprintf("%s/oauth=%v/%s", entry, oauth, mode), func(t *testing.T) {
					acc := identityAPIKeyAccount(1)
					if oauth {
						acc = identityAccount(t, 1)
					}
					env := identityEntryEnv(t, entry, []*store.UpstreamAccountRecord{acc})
					p := enabledIdentityPolicy(t)
					p.Enabled, p.Automatic = mode != codexidentity.IdentityModeDisabled, mode == codexidentity.IdentityModeAuto
					env.handler.proxy.SetCodexIdentityProvider(identityProviderFunc(func(context.Context) (codexidentity.Policy, error) {
						return p, nil
					}))
					env.transport.responder = func(req *http.Request) (*http.Response, error) {
						return identityStateResponse(req, 200), nil
					}
					body := ` {"model":"gpt-5","prompt_cache_key":"source","client_metadata":{"session_id":"source"},"input":"hello"} `
					subscription := strings.HasPrefix(entry, "subscription")
					r := identityRequestFixture(subscription, body)
					r.Header.Set("User-Agent", "codex_exec/0.155.1 (Mac OS 27.0.0; arm64) unknown")
					r.Header.Set("originator", "codex_exec")
					r.Header.Set("x-codex-beta-features", "remote_compaction_v2")
					r.Header.Set("responses-lite", "true")
					r.Header.Set(codexidentity.HeaderTurnState, "source-state")
					rec := httptest.NewRecorder()
					identityServe(env, subscription, rec, r)
					if rec.Code != 200 {
						t.Fatalf("%d %s", rec.Code, rec.Body.String())
					}
					reqs, bodies := env.transport.captured()
					if len(reqs) != 1 {
						t.Fatalf("请求数=%d", len(reqs))
					}
					mapped := mode == codexidentity.IdentityModeEnabled || mode == codexidentity.IdentityModeAuto && oauth
					assertIdentityModeBody(t, reqs[0], bodies[0], body, oauth, mapped)
					wantState := ""
					if !mapped && (!subscription || mode == codexidentity.IdentityModeAuto) {
						wantState = "source-state"
					}
					if got := reqs[0].Header.Get(codexidentity.HeaderTurnState); got != wantState {
						t.Fatalf("出站 turn-state=%q want=%q", got, wantState)
					}
					wantResponseState := "upstream-state"
					if mapped && !oauth {
						wantResponseState = ""
					}
					if got := rec.Header().Get(codexidentity.HeaderTurnState); got != wantResponseState {
						t.Fatalf("响应 turn-state=%q want=%q", got, wantResponseState)
					}
				})
			}
		}
	}
}

func identityStateResponse(req *http.Request, status int) *http.Response {
	h := make(http.Header)
	h.Set("Content-Type", "application/json")
	h.Set(codexidentity.HeaderTurnState, "upstream-state")
	body := `{"id":"resp_1","status":"completed","output":[]}`
	if status != 200 {
		body = `{"error":{"message":"temporary backend failure"}}`
	}
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body)), Request: req}
}

func assertIdentityModeBody(t *testing.T, req *http.Request, body []byte, original string, oauth, mapped bool) {
	t.Helper()
	fields, err := codexidentity.JSONFields(body)
	if err != nil {
		t.Fatal(err)
	}
	if !mapped && string(body) != original {
		t.Fatalf("未启用映射的正文发生变化: %s", body)
	}
	if mapped && oauth {
		var session string
		_ = json.Unmarshal(fields["prompt_cache_key"], &session)
		if session == "" || session == "source" || session != req.Header.Get("session-id") {
			t.Fatalf("首方未映射或头/正文不一致: %s %v", body, req.Header)
		}
	}
	if mapped && !oauth && fields["client_metadata"] != nil {
		t.Fatalf("强制开启未清理第三方 metadata: %s", body)
	}
	if req.Header.Get("Authorization") == "Bearer source-secret" || req.Header.Get("x-oai-attestation") != "" {
		t.Fatal("来源凭据或证明泄漏")
	}
}

func TestAccountIdentityAutomaticRetryReevaluatesActualTarget(t *testing.T) {
	for _, entry := range []string{"global", "subscription", "model-routing", "subscription-model-routing"} {
		for _, oauthFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/oauth_first=%v", entry, oauthFirst), func(t *testing.T) {
				accounts := []*store.UpstreamAccountRecord{identityAPIKeyAccount(1), identityAccount(t, 2)}
				if oauthFirst {
					accounts[0], accounts[1] = accounts[1], accounts[0]
				}
				env := identityEntryEnv(t, entry, accounts)
				p := enabledIdentityPolicy(t)
				p.Automatic = true
				reads, attempts := 0, 0
				env.handler.proxy.SetCodexIdentityProvider(identityProviderFunc(func(context.Context) (codexidentity.Policy, error) {
					reads++
					return p, nil
				}))
				env.transport.responder = func(req *http.Request) (*http.Response, error) {
					attempts++
					if attempts == 1 {
						p.Enabled = false // 在途请求仍使用已捕获的自动策略。
						return identityStateResponse(req, 500), nil
					}
					return identityStateResponse(req, 200), nil
				}
				body := `{"model":"gpt-5","prompt_cache_key":"source","client_metadata":{"session_id":"source"},"input":"hello"}`
				subscription := strings.HasPrefix(entry, "subscription")
				r := identityRequestFixture(subscription, body)
				r.Header.Set(codexidentity.HeaderTurnState, "source-state")
				rec := httptest.NewRecorder()
				identityServe(env, subscription, rec, r)
				reqs, bodies := env.transport.captured()
				if rec.Code != 200 || len(reqs) != 2 || reads != 1 {
					t.Fatalf("换号或快照错误: code=%d requests=%d reads=%d body=%s", rec.Code, len(reqs), reads, rec.Body.String())
				}
				for i, req := range reqs {
					oauth := accountauth.IsChatGPTOAuthProvider(accounts[i].ProviderType)
					assertIdentityModeBody(t, req, bodies[i], body, oauth, oauth)
					wantState := "source-state"
					if oauth {
						wantState = ""
					}
					if req.Header.Get(codexidentity.HeaderTurnState) != wantState {
						t.Fatal("换号沿用了上一账号的状态策略")
					}
				}
			})
		}
	}
}

func TestAccountIdentityAutomaticThirdPartySkipsIdentityValidation(t *testing.T) {
	acc := identityAPIKeyAccount(1)
	env := identityEntryEnv(t, "global", []*store.UpstreamAccountRecord{acc})
	p := enabledIdentityPolicy(t)
	p.Automatic = true
	p.Namespace, p.NamespaceError = codexidentity.Namespace{}, fmt.Errorf("namespace unavailable")
	env.handler.proxy.SetCodexIdentityProvider(identityProviderFunc(func(context.Context) (codexidentity.Policy, error) { return p, nil }))
	for _, body := range []string{`{"model":"gpt-5","model":"gpt-5"}`, `[]`, `{"data":` + strings.Repeat(`[`, 257) + `0` + strings.Repeat(`]`, 257) + `}`} {
		r := identityRequestFixture(false, body)
		r.Header.Set("x-codex-beta-features", "unknown_feature")
		resp, cancel, _, err := env.handler.proxy.forwardRequestToAccount(r.Context(), r, []byte(body), acc, false, nil)
		if err != nil {
			t.Fatal("第三方被身份校验阻断", err)
		}
		_ = resp.Body.Close()
		if cancel != nil {
			cancel()
		}
		_, bodies := env.transport.captured()
		if string(bodies[len(bodies)-1]) != body {
			t.Fatal("第三方原始正文发生变化")
		}
	}
	// 同一策略对官方 OAuth 仍报告 namespace 错误，不能静默跳过隔离。
	r := identityRequestFixture(false, `{"model":"gpt-5"}`)
	_, _, _, err := env.handler.proxy.forwardRequestToAccount(r.Context(), r, []byte(`{"model":"gpt-5"}`), identityAccount(t, 2), false, nil)
	if identityErr := codexIdentityError(err); identityErr == nil || identityErr.Code != codexidentity.CodeNamespaceUnavailable {
		t.Fatalf("首方 namespace 错误被忽略: %v", err)
	}
}

func TestAccountIdentityAutomaticUsesCredentialAndOrigin(t *testing.T) {
	p := enabledIdentityPolicy(t)
	p.Automatic = true
	for _, tc := range []struct {
		target string
		oauth  bool
		want   bool
	}{
		{"https://chatgpt.com/backend-api/codex", true, true},
		{"https://chatgpt.com:443/backend-api/codex", true, true},
		{"https://chatgpt.com/backend-api/codex", false, false},
		{"https://relay.test/v1", true, false},
		{"https://api.openai.com/v1", false, false},
		{"http://chatgpt.com", true, false},
		{"https://chatgpt.com:8443", true, false},
		{"https://chatgpt.com.relay.test", true, false},
	} {
		scope, err := codexidentity.NewAccountScope(1, "test", tc.target, "account", tc.oauth)
		if err != nil {
			t.Fatal(err)
		}
		if got := p.EffectiveEnabled(scope); got != tc.want {
			t.Fatalf("%s oauth=%v enabled=%v want=%v", tc.target, tc.oauth, got, tc.want)
		}
	}
}

func TestAccountIdentityAutomaticSubscriptionStateHonorsConnection(t *testing.T) {
	env := identityEntryEnv(t, "subscription", []*store.UpstreamAccountRecord{identityAPIKeyAccount(1)})
	p := enabledIdentityPolicy(t)
	p.Automatic = true
	env.handler.proxy.SetCodexIdentityProvider(identityProviderFunc(func(context.Context) (codexidentity.Policy, error) { return p, nil }))
	r := identityRequestFixture(true, `{"model":"gpt-5","input":"hello"}`)
	r.Header.Set(codexidentity.HeaderTurnState, "source-state")
	r.Header.Set("Connection", "X-Codex-Turn-State")
	rec := httptest.NewRecorder()
	identityServe(env, true, rec, r)
	reqs, _ := env.transport.captured()
	if rec.Code != 200 || len(reqs) != 1 || reqs[0].Header.Get(codexidentity.HeaderTurnState) != "" {
		t.Fatal("Connection 点名状态经 context 回流", rec.Code)
	}
}
