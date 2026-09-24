package endpoint

import (
	"context"
	"testing"
)

func TestClaudeCodeEntryContextMarker(t *testing.T) {
	if IsClaudeCodeEntry(context.Background()) {
		t.Fatal("未标记的上下文不应视为 Claude Code 入口")
	}
	if !IsClaudeCodeEntry(WithClaudeCodeEntry(context.Background(), ClaudeCodeEntryRequest{})) {
		t.Fatal("标记后应识别为 Claude Code 入口")
	}
	var nilCtx context.Context
	if IsClaudeCodeEntry(nilCtx) {
		t.Fatal("nil 上下文不应视为 Claude Code 入口")
	}
}

func TestIsClaudeCodeSubscription(t *testing.T) {
	cases := []struct {
		name string
		ctx  context.Context
		want bool
	}{
		{"非入口请求", context.Background(), false},
		{"空模式按订阅直通", WithClaudeCodeEntry(context.Background(), ClaudeCodeEntryRequest{}), true},
		{"订阅直通", WithClaudeCodeEntry(context.Background(), ClaudeCodeEntryRequest{Mode: ClaudeCodeEntryModeSubscription}), true},
		{"端点模式", WithClaudeCodeEntry(context.Background(), ClaudeCodeEntryRequest{Mode: ClaudeCodeEntryModeEndpoint}), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsClaudeCodeSubscription(tc.ctx); got != tc.want {
				t.Fatalf("IsClaudeCodeSubscription = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNormalizeClaudeCodeEntryMode(t *testing.T) {
	cases := map[string]string{
		"":               ClaudeCodeEntryModeSubscription,
		" Subscription ": ClaudeCodeEntryModeSubscription,
		"auto":           ClaudeCodeEntryModeSubscription, // 旧版按模型混用已取消
		"endpoint":       ClaudeCodeEntryModeEndpoint,
		"API":            ClaudeCodeEntryModeEndpoint, // 旧版仅 API
		"pool":           "pool",
	}
	for input, want := range cases {
		got := NormalizeClaudeCodeEntryMode(input)
		if got != want {
			t.Fatalf("NormalizeClaudeCodeEntryMode(%q) = %q, want %q", input, got, want)
		}
		if IsValidClaudeCodeEntryMode(got) != (want != "pool") {
			t.Fatalf("IsValidClaudeCodeEntryMode(%q) 判定错误", got)
		}
	}
}
