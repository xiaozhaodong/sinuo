package endpoint

import (
	"context"
	"strings"
)

// Claude Code 入口的接入模式：只约束经入口进来的请求，主端口请求始终走端点
const (
	// ClaudeCodeEntryModeSubscription 订阅直通：带客户端自己的 claude.ai 登录直连官方，不进端点调度
	ClaudeCodeEntryModeSubscription = "subscription"
	// ClaudeCodeEntryModeEndpoint 端点：与主端口相同，按全局路由或模型路由使用第三方端点
	ClaudeCodeEntryModeEndpoint = "endpoint"

	// legacyClaudeCodeEntryModeAuto 旧版「按模型路由混用订阅与第三方」，已取消，读取时按订阅直通处理
	legacyClaudeCodeEntryModeAuto = "auto"
	// legacyClaudeCodeEntryModeAPI 旧版「仅 API」，等价于现在的端点模式
	legacyClaudeCodeEntryModeAPI = "api"
)

// NormalizeClaudeCodeEntryMode 去空白、转小写；空值与旧版 auto 归一为订阅直通，旧版 api 归一为端点。
// 非法值原样返回，由调用方校验
func NormalizeClaudeCodeEntryMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "", legacyClaudeCodeEntryModeAuto:
		return ClaudeCodeEntryModeSubscription
	case legacyClaudeCodeEntryModeAPI:
		return ClaudeCodeEntryModeEndpoint
	}
	return mode
}

// IsValidClaudeCodeEntryMode 是否为已知模式（需先归一化）
func IsValidClaudeCodeEntryMode(mode string) bool {
	return mode == ClaudeCodeEntryModeSubscription || mode == ClaudeCodeEntryModeEndpoint
}

// ClaudeCodeEntryRequest 入口为单个请求带下来的约束
type ClaudeCodeEntryRequest struct {
	// Mode 本次请求的接入模式
	Mode string
}

// claudeCodeEntryKey 标记请求来自 Claude Code 专用 loopback 入口。
// 放在 endpoint 包而非 proxy 包：proxy 与 proxy/handlers 都要读取，且 handlers 不能反向依赖 proxy。
type claudeCodeEntryKey struct{}

// WithClaudeCodeEntry 给请求上下文打上 Claude Code 入口标记与接入模式
func WithClaudeCodeEntry(ctx context.Context, entry ClaudeCodeEntryRequest) context.Context {
	entry.Mode = NormalizeClaudeCodeEntryMode(entry.Mode)
	return context.WithValue(ctx, claudeCodeEntryKey{}, entry)
}

// ClaudeCodeEntryFromContext 读取入口标记；ok=false 表示不是入口请求
func ClaudeCodeEntryFromContext(ctx context.Context) (ClaudeCodeEntryRequest, bool) {
	if ctx == nil {
		return ClaudeCodeEntryRequest{}, false
	}
	entry, ok := ctx.Value(claudeCodeEntryKey{}).(ClaudeCodeEntryRequest)
	return entry, ok
}

// IsClaudeCodeEntry 判断请求是否来自 Claude Code 专用入口
func IsClaudeCodeEntry(ctx context.Context) bool {
	_, ok := ClaudeCodeEntryFromContext(ctx)
	return ok
}

// IsClaudeCodeSubscription 判断请求是否来自处于订阅直通模式的 Claude Code 入口
func IsClaudeCodeSubscription(ctx context.Context) bool {
	entry, ok := ClaudeCodeEntryFromContext(ctx)
	return ok && entry.Mode == ClaudeCodeEntryModeSubscription
}
