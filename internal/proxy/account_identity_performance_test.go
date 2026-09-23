package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cc-forwarder/internal/accountauth"
	"cc-forwarder/internal/codexidentity"
	"cc-forwarder/internal/privacy"
	"cc-forwarder/internal/store"
)

type identityPrivacyMutator func([]byte) []byte

func (mutate identityPrivacyMutator) Apply(_ context.Context, _ privacy.Request, body []byte) (privacy.ApplyResult, error) {
	// 故意不声明 Changed，身份校验必须根据内容判断。
	return privacy.ApplyResult{Body: mutate(body), Action: privacy.ModeRedact}, nil
}

func (identityPrivacyMutator) SnapshotVersion() int64 { return 1 }

func TestAccountIdentityPrivacyValidationAfterCache(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate identityPrivacyMutator
		status int
	}{
		{"equal_copy", bytes.Clone, 200},
		{"inplace_text", func(body []byte) []byte {
			copy(body[bytes.Index(body, []byte(`hello`)):], "clean")
			return body
		}, 200},
		{"inplace_model", func(body []byte) []byte {
			body[bytes.Index(body, []byte(`gpt-5`))+4] = '6'
			return body
		}, 422},
		{"inplace_invalid", func(body []byte) []byte { body[0] = '['; return body }, 422},
		{"duplicate_key", func(body []byte) []byte {
			return append(body[:len(body)-1], []byte(`,"input":"other"}`)...)
		}, 422},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newSubscriptionPoolTestEnv(t, []*store.UpstreamAccountRecord{identityAccount(t, 1), identityAccount(t, 2)}, true)
			p := enabledIdentityPolicy(t)
			p.Automatic = true
			env.handler.proxy.SetCodexIdentityProvider(identityProviderFunc(func(context.Context) (codexidentity.Policy, error) { return p, nil }))
			env.handler.proxy.SetPrivacyFilter(tc.mutate)
			r := identityRequestFixture(false, `{"model":"gpt-5","prompt_cache_key":"source","input":"hello"}`)
			rec := httptest.NewRecorder()
			identityServe(env, false, rec, r)
			reqs, _ := env.transport.captured()
			if rec.Code != tc.status {
				t.Fatalf("code=%d want=%d body=%s", rec.Code, tc.status, rec.Body.String())
			}
			if tc.status == 422 && (len(reqs) != 0 || len(env.pool.transientCalls) != 0 || len(env.pool.softFailureCalls) != 0) {
				t.Fatal("隐私变化被放行或导致账号冷却")
			}
		})
	}
}

// 使用合成正文，不读取真实请求；大量小对象可暴露逐 token 解码的分配成本。
func benchmarkIdentityBody() []byte {
	item := `{"role":"user","content":[{"type":"input_text","text":"tool output source","number":9007199254740993,"ratio":1.2300e+09}]}`
	input := strings.TrimSuffix(strings.Repeat(item+",", 3_700_000/(len(item)+1)), ",")
	return []byte(`{"model":"gpt-5","prompt_cache_key":"source","client_metadata":{"session_id":"source","thread_id":"source"},"input":[` + input + `]}`)
}

func BenchmarkAccountIdentityLargeJSON(b *testing.B) {
	body := benchmarkIdentityBody()
	namespace, err := codexidentity.ParseNamespace("067cbe02-1334-45d1-85c0-df7f459055ef")
	if err != nil {
		b.Fatal(err)
	}
	credential, err := json.Marshal(map[string]string{
		"access_token": "at-fixture", "refresh_token": "rt-fixture",
		"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "chatgpt_account_id": "acct-fixture",
	})
	if err != nil {
		b.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		enabled  bool
		oauth    bool
		attempts int
		privacy  string
	}{
		{"disabled", false, true, 1, ""},
		{"auto-third-party", true, false, 1, ""},
		{"auto-oauth", true, true, 1, ""},
		{"auto-oauth-three-attempts", true, true, 3, ""},
		{"auto-oauth-privacy-copy", true, true, 1, "copy"},
		{"auto-oauth-privacy-redacted", true, true, 1, "redact"},
	} {
		b.Run(tc.name, func(b *testing.B) {
			h := &Handler{refreshTokenManager: accountauth.NewOpenAIRefreshTokenManager(nil)}
			switch tc.privacy {
			case "copy":
				h.SetPrivacyFilter(identityPrivacyMutator(bytes.Clone))
			case "redact":
				h.SetPrivacyFilter(identityPrivacyMutator(func(body []byte) []byte {
					return bytes.Replace(body, []byte("tool output source"), []byte("tool output clean"), 1)
				}))
			}
			policy := codexidentity.Policy{
				Enabled: tc.enabled, Automatic: true, Version: codexidentity.DefaultCompatVersion, Namespace: namespace,
			}
			h.SetCodexIdentityProvider(identityProviderFunc(func(context.Context) (codexidentity.Policy, error) { return policy, nil }))
			acc := &store.UpstreamAccountRecord{
				ID: 1, ProviderType: accountauth.ProviderChatGPTRefreshToken, CredentialRaw: string(credential),
			}
			target := "https://chatgpt.com/backend-api/codex/responses"
			if !tc.oauth {
				acc.ProviderType, acc.CredentialRaw = accountauth.ProviderAPIKey, "sk-fixture"
				target = "https://relay.test/v1/responses"
			}
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				r := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				r.Header.Set("Content-Type", "application/json")
				ctx, err := h.attachCodexIdentityRequest(r.Context(), r)
				if err != nil {
					b.Fatal(err)
				}
				for attempt := 0; attempt < tc.attempts; attempt++ {
					acc.ID = int64(1 + attempt%2)
					if _, _, _, err := h.prepareAccountIdentity(ctx, r, body, acc, target); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}
