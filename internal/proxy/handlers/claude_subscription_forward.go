package handlers

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"cc-forwarder/internal/privacy"
)

const (
	// ClaudeSubscriptionUpstreamURL Claude Code 订阅直通的固定上游
	ClaudeSubscriptionUpstreamURL = "https://api.anthropic.com"
	// claudeSubscriptionUsagePath 官方订阅额度查询路径，与 Claude Code /usage 相同
	claudeSubscriptionUsagePath = "/api/oauth/usage"
	// claudeOAuthBeta claude.ai 登录凭据访问官方管理接口需要的 beta 标记
	claudeOAuthBeta = "oauth-2025-04-20"
)

// claudeSubscriptionSkipHeaders 订阅直通复制客户端请求头时剔除的头：保留客户端 Authorization（OAuth token），
// 其余沿用黑名单复制，另外剔除来源类头（isClientSourceHeader）。Claude Code 直连官方时本来就会发出其余头，
// 原样转发等价于直连
var claudeSubscriptionSkipHeaders = map[string]bool{
	"host":      true,
	"x-api-key": true,
}

// clientSourceHeaders Claude Code 入口请求不向任何上游转发的 Cookie、代理与来源类头；
// 订阅直通与入口端点模式共用这条边界，X-Forwarded-* 另按前缀匹配
var clientSourceHeaders = map[string]bool{
	"cookie":              true,
	"proxy-authorization": true,
	"forwarded":           true,
	"origin":              true,
	"referer":             true,
}

// isClientSourceHeader lower 为小写头名
func isClientSourceHeader(lower string) bool {
	return clientSourceHeaders[lower] || strings.HasPrefix(lower, "x-forwarded-")
}

// claudeSubscriptionHopByHopHeaders 逐跳头不向上游转发
var claudeSubscriptionHopByHopHeaders = []string{
	"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
	"Te", "Trailers", "Transfer-Encoding", "Upgrade",
}

// ForwardClaudeSubscription 把订阅直通请求带着客户端自己的 OAuth 凭据发往 Anthropic 官方。
// 不经过端点调度：没有端点凭据、自定义头与模型改写；隐私规则按 subscription 上游类型匹配。
// baseURL 为空时使用官方地址（测试注入本地上游）。
func (f *Forwarder) ForwardClaudeSubscription(ctx context.Context, r *http.Request, bodyBytes []byte, baseURL string, isSSE, detachUpstream bool, onWroteRequest func(UpstreamWriteTiming)) (*http.Response, context.CancelFunc, *UpstreamTraceState, error) {
	bodyBytes, err := ApplyPrivacyFilter(f.privacyFilter, r, privacy.Request{
		Path:         r.URL.Path,
		Method:       r.Method,
		UpstreamType: privacy.UpstreamTypeSubscription,
		ContentType:  r.Header.Get("Content-Type"),
	}, bodyBytes)
	if err != nil {
		return nil, nil, nil, err
	}

	if baseURL == "" {
		baseURL = ClaudeSubscriptionUpstreamURL
	}
	requestTimeout := time.Duration(0)
	if !isSSE {
		requestTimeout = f.config.GlobalTimeout
	}
	return f.sendUpstream(ctx, r, bodyBytes, upstreamSend{
		baseURL:        baseURL,
		timeout:        requestTimeout,
		detach:         detachUpstream,
		onWroteRequest: onWroteRequest,
		copyHeaders:    func(dst *http.Request) { copyClaudeSubscriptionHeaders(r, dst, baseURL) },
	})
}

// copyClaudeSubscriptionHeaders 按订阅直通策略复制客户端请求头，并把 Host 指向上游
func copyClaudeSubscriptionHeaders(src, dst *http.Request, baseURL string) {
	for key, values := range src.Header {
		lower := strings.ToLower(key)
		if claudeSubscriptionSkipHeaders[lower] || isClientSourceHeader(lower) {
			continue
		}
		for _, value := range values {
			dst.Header.Add(key, value)
		}
	}
	for _, header := range claudeSubscriptionHopByHopHeaders {
		dst.Header.Del(header)
	}
	if u, err := url.Parse(baseURL); err == nil {
		dst.Header.Set("Host", u.Host)
		dst.Host = u.Host
	}
}

// FetchClaudeSubscriptionUsage 带订阅直通请求留下的 OAuth 凭据查询官方订阅额度，与订阅直通共用出站 transport。
// 请求头对齐 Claude Code 自己的 /usage：Bearer 凭据 + OAuth beta；UA 沿用客户端的。调用方负责关闭响应体。
func (f *Forwarder) FetchClaudeSubscriptionUsage(ctx context.Context, baseURL, authorization, userAgent string) (*http.Response, error) {
	if baseURL == "" {
		baseURL = ClaudeSubscriptionUpstreamURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+claudeSubscriptionUsagePath, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", authorization)
	req.Header.Set(anthropicBetaHeader, claudeOAuthBeta)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	httpTransport, err := f.buildStreamingTransport()
	if err != nil {
		return nil, err
	}
	return (&http.Client{Transport: httpTransport}).Do(req)
}

// clientOAuthBetaPrefix Claude Code 订阅登录时 anthropic-beta 携带的 OAuth capability 前缀（值带日期后缀）
const clientOAuthBetaPrefix = "oauth-"

// stripClientOAuthBetaValues 删除 anthropic-beta 中 oauth- 开头的值，其余值保持原顺序；
// 删空则移除整个头；没有可删的值时不改写，保持原样
func stripClientOAuthBetaValues(header http.Header) {
	values := header.Values(anthropicBetaHeader)
	kept := make([]string, 0, len(values))
	removed := false
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if strings.HasPrefix(strings.ToLower(part), clientOAuthBetaPrefix) {
				removed = true
				continue
			}
			kept = append(kept, part)
		}
	}
	if !removed {
		return
	}
	header.Del(anthropicBetaHeader)
	if len(kept) > 0 {
		header.Set(anthropicBetaHeader, strings.Join(kept, ","))
	}
}
