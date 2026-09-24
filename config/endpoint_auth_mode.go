package config

import (
	"fmt"
	"net/url"
	"strings"
)

// 端点认证方式：决定转发时凭据从哪里来。
const (
	// EndpointAuthModeConfigured 使用端点配置的 Token / API Key（旧行为；空值等价于它）
	EndpointAuthModeConfigured = "configured"
	// EndpointAuthModeClientOAuth 透传 Claude Code 本机 claude.ai 登录的 OAuth 凭据；
	// 端点本身不保存任何凭据，只接受来自 Claude Code 专用入口的请求
	EndpointAuthModeClientOAuth = "client_oauth"

	// ClaudeOAuthUpstreamURL 订阅直通端点唯一允许的上游
	ClaudeOAuthUpstreamURL = "https://api.anthropic.com"
)

// claudeOAuthProtectedHeaders 订阅直通端点的自定义头不得覆盖的认证类头（小写）
var claudeOAuthProtectedHeaders = map[string]struct{}{
	"authorization":       {},
	"x-api-key":           {},
	"cookie":              {},
	"proxy-authorization": {},
}

// NormalizeEndpointAuthMode 去空白并把空值归一为 configured；非法值原样返回，由校验拒绝
func NormalizeEndpointAuthMode(mode string) string {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		return EndpointAuthModeConfigured
	}
	return mode
}

// IsClientOAuth 端点是否为 Claude Code 订阅直通
func (c *EndpointConfig) IsClientOAuth() bool {
	return c != nil && c.AuthMode == EndpointAuthModeClientOAuth
}

// EndpointAuthSettings 认证相关的待校验字段
type EndpointAuthSettings struct {
	AuthMode string
	URL      string
	Token    string
	ApiKey   string
	Headers  map[string]string
}

// ValidateEndpointAuthSettings 校验认证方式与凭据、URL、自定义头的组合（AuthMode 需已归一化）
func ValidateEndpointAuthSettings(settings EndpointAuthSettings) error {
	switch settings.AuthMode {
	case EndpointAuthModeConfigured:
		return nil
	case EndpointAuthModeClientOAuth:
		return validateClientOAuthSettings(settings)
	default:
		return fmt.Errorf("不支持的端点认证方式: %q", settings.AuthMode)
	}
}

func validateClientOAuthSettings(settings EndpointAuthSettings) error {
	if !isClaudeOAuthUpstreamURL(settings.URL) {
		return fmt.Errorf("订阅直通端点的 URL 必须是 %s", ClaudeOAuthUpstreamURL)
	}
	if strings.TrimSpace(settings.Token) != "" || strings.TrimSpace(settings.ApiKey) != "" {
		return fmt.Errorf("订阅直通端点使用 Claude Code 本机登录，不能填写 Token 或 API Key")
	}
	for key := range settings.Headers {
		if _, protected := claudeOAuthProtectedHeaders[strings.ToLower(strings.TrimSpace(key))]; protected {
			return fmt.Errorf("订阅直通端点的自定义请求头不能包含 %s", key)
		}
	}
	return nil
}

// isClaudeOAuthUpstreamURL 只接受官方地址本身（允许末尾斜杠），不接受路径、查询或其他主机
func isClaudeOAuthUpstreamURL(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	official, _ := url.Parse(ClaudeOAuthUpstreamURL)
	return strings.EqualFold(parsed.Scheme, official.Scheme) &&
		strings.EqualFold(parsed.Host, official.Host) &&
		(parsed.Path == "" || parsed.Path == "/") &&
		parsed.RawQuery == "" && parsed.Fragment == "" && parsed.User == nil
}
