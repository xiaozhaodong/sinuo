package proxy

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"sinuo/internal/codexidentity"
	"sinuo/internal/store"
)

const (
	identityLogOAuthTarget  = "https://chatgpt.com/backend-api/codex/responses"
	identityLogRelayTarget  = "https://relay.test/v1/responses"
	identityLogTestRequest  = "req-identity-log"
	identityLogMetadataBody = `{"model":"gpt-5.4","prompt_cache_key":"source","client_metadata":{"session_id":"source"}}`
)

// prepareIdentityWithLog 执行一次账号身份准备，返回指定级别下捕获的日志。
func prepareIdentityWithLog(t *testing.T, level slog.Level, policy codexidentity.Policy, acc *store.UpstreamAccountRecord, target string) string {
	t.Helper()
	env := newSubscriptionPoolTestEnv(t, []*store.UpstreamAccountRecord{acc}, true)
	h := env.handler.proxy
	h.SetCodexIdentityProvider(identityProviderFunc(func(context.Context) (codexidentity.Policy, error) { return policy, nil }))
	r := identityRequestFixture(false, identityLogMetadataBody)
	r = r.WithContext(context.WithValue(r.Context(), "conn_id", identityLogTestRequest)) //nolint:staticcheck // 与 LoggingMiddleware 使用同一 key
	ctx, err := h.attachCodexIdentityRequest(r.Context(), r)
	if err != nil {
		t.Fatal(err)
	}

	originalLogger := slog.Default()
	var logBuffer bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuffer, &slog.HandlerOptions{Level: level})))
	t.Cleanup(func() { slog.SetDefault(originalLogger) })

	if _, _, _, err := h.prepareAccountIdentity(ctx, r, []byte(identityLogMetadataBody), acc, target); err != nil {
		t.Fatal(err)
	}
	return logBuffer.String()
}

func assertIdentityLogContains(t *testing.T, output string, parts ...string) {
	t.Helper()
	for _, part := range parts {
		if !strings.Contains(output, part) {
			t.Fatalf("日志缺少 %q:\n%s", part, output)
		}
	}
}

func TestAccountIdentityDecisionLogByEffect(t *testing.T) {
	autoPolicy := enabledIdentityPolicy(t)
	autoPolicy.Automatic = true

	cases := []struct {
		name    string
		policy  codexidentity.Policy
		acc     *store.UpstreamAccountRecord
		target  string
		want    []string
		without []string
	}{
		{
			name: "首方 OAuth 映射", policy: autoPolicy, acc: identityAccount(t, 1), target: identityLogOAuthTarget,
			want: []string{"已按账号映射身份", "effect=mapped", "mode=auto", "effective=true", "first_party=true",
				"target_host=chatgpt.com", "account=oauth-1", "operation=responses", "ua_source=", "Session-Id",
				"body_rewritten=true", "turn_state_action=absent"},
		},
		{
			name: "自动模式第三方透传", policy: autoPolicy, acc: identityAPIKeyAccount(2), target: identityLogRelayTarget,
			want:    []string{"未生效，原样透传", "effect=passthrough", "effective=false", "first_party=false", "target_host=relay.test"},
			without: []string{"ua_source", "set_headers", "body_rewritten"},
		},
		{
			name: "强制开启第三方清理", policy: enabledIdentityPolicy(t), acc: identityAPIKeyAccount(3), target: identityLogRelayTarget,
			want: []string{"已清理首方身份字段", "effect=third_party_cleanup", "mode=enabled", "effective=true",
				"first_party=false", "X-Client-Request-Id", "body_rewritten=true"},
			without: []string{"ua_source", "version="},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			output := prepareIdentityWithLog(t, slog.LevelDebug, tc.policy, tc.acc, tc.target)
			assertIdentityLogContains(t, output, append(tc.want, "[Codex 身份] ["+identityLogTestRequest+"]")...)
			for _, part := range tc.without {
				if strings.Contains(output, part) {
					t.Fatalf("不应输出 %q:\n%s", part, output)
				}
			}
		})
	}
}

func TestAccountIdentityDecisionLogSkippedWhenDebugDisabled(t *testing.T) {
	output := prepareIdentityWithLog(t, slog.LevelInfo, enabledIdentityPolicy(t), identityAccount(t, 1), identityLogOAuthTarget)
	if strings.Contains(output, "[Codex 身份]") {
		t.Fatalf("Debug 关闭时不应输出身份日志:\n%s", output)
	}
}
