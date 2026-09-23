// Package accountauth 提供账号鉴权辅助能力
package accountauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"cc-forwarder/config"
	"cc-forwarder/internal/transport"
)

const (
	defaultOpenAIRefreshTokenURL  = "https://auth.openai.com/oauth/token"
	openAIOAuthClientID           = "app_EMoamEEZ73f0CkXaXp7hrann"
	defaultExchangeTimeout        = 20 * time.Second
	defaultCachedTokenLifetime    = 45 * time.Minute
	refreshSkewWindow             = 90 * time.Second
	refreshTokenResponseReadLimit = 1024 * 1024
)

var openAIRefreshTokenURL = defaultOpenAIRefreshTokenURL

// CurrentOpenAIRefreshTokenURLForTest 返回当前 refresh token 端点，仅用于测试覆盖。
func CurrentOpenAIRefreshTokenURLForTest() string {
	return openAIRefreshTokenURL
}

// SetOpenAIRefreshTokenURLForTest 覆盖 refresh token 端点，仅用于测试。
func SetOpenAIRefreshTokenURLForTest(rawURL string) {
	openAIRefreshTokenURL = rawURL
}

// OpenAIRefreshTokenManager 负责将 ChatGPT refresh token 交换为 access token，并做短期缓存。
type OpenAIRefreshTokenManager struct {
	cfg   *config.Config
	mu    sync.Mutex
	cache map[string]cachedAccessToken
}

type cachedAccessToken struct {
	accessToken      string
	refreshToken     string
	idToken          string
	chatGPTAccountID string
	expiresAt        time.Time
}

type refreshTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int    `json:"expires_in"`
}

// ResolvedAccessToken 表示 RT 换取后的完整鉴权信息。
type ResolvedAccessToken struct {
	AccessToken      string
	RefreshToken     string
	IDToken          string
	ChatGPTAccountID string
	ExpiresAt        time.Time
}

type storedOpenAICredential struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	IDToken          string `json:"id_token"`
	ExpiresAt        string `json:"expires_at"`
	ChatGPTAccountID string `json:"chatgpt_account_id"`
	AccountID        string `json:"account_id"`
	PlanType         string `json:"plan_type"`
	ChatGPTUserID    string `json:"chatgpt_user_id"`
	OrganizationID   string `json:"organization_id"`
}

// NewOpenAIRefreshTokenManager 创建 RT 管理器。
func NewOpenAIRefreshTokenManager(cfg *config.Config) *OpenAIRefreshTokenManager {
	return &OpenAIRefreshTokenManager{
		cfg:   cfg,
		cache: make(map[string]cachedAccessToken),
	}
}

// ResolveAccessToken 使用 refresh token 获取可用 access token。
func (m *OpenAIRefreshTokenManager) ResolveAccessToken(ctx context.Context, refreshTokenRaw string) (string, error) {
	resolved, err := m.ResolveAccessTokenDetails(ctx, refreshTokenRaw)
	if err != nil {
		return "", err
	}
	return resolved.AccessToken, nil
}

// ResolveAccessTokenDetails 使用 refresh token 获取可用 access token 及账号附加信息。
func (m *OpenAIRefreshTokenManager) ResolveAccessTokenDetails(ctx context.Context, refreshTokenRaw string) (*ResolvedAccessToken, error) {
	if token, expiresAt, ok := extractStoredAccessToken(refreshTokenRaw); ok && time.Until(expiresAt) > refreshSkewWindow {
		refreshToken := extractRefreshToken(refreshTokenRaw)
		if refreshToken != "" {
			m.mu.Lock()
			m.cache[refreshToken] = cachedAccessToken{
				accessToken:      token,
				refreshToken:     refreshToken,
				idToken:          extractStoredIDToken(refreshTokenRaw),
				chatGPTAccountID: ExtractChatGPTAccountID(refreshTokenRaw),
				expiresAt:        expiresAt,
			}
			m.mu.Unlock()
		}
		return &ResolvedAccessToken{
			AccessToken:      token,
			RefreshToken:     refreshToken,
			IDToken:          extractStoredIDToken(refreshTokenRaw),
			ChatGPTAccountID: ExtractChatGPTAccountID(refreshTokenRaw),
			ExpiresAt:        expiresAt,
		}, nil
	}

	refreshToken := extractRefreshToken(refreshTokenRaw)
	if refreshToken == "" {
		return nil, fmt.Errorf("refresh token 为空")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if cached, ok := m.cache[refreshToken]; ok && time.Until(cached.expiresAt) > refreshSkewWindow {
		return &ResolvedAccessToken{
			AccessToken:      cached.accessToken,
			RefreshToken:     firstNonEmptyString(cached.refreshToken, refreshToken),
			IDToken:          cached.idToken,
			ChatGPTAccountID: firstNonEmptyString(cached.chatGPTAccountID, ExtractChatGPTAccountID(refreshTokenRaw)),
			ExpiresAt:        cached.expiresAt,
		}, nil
	}

	tokenResp, err := exchangeRefreshToken(ctx, m.cfg, refreshToken)
	if err != nil {
		return nil, err
	}
	if tokenResp.AccessToken == "" {
		return nil, fmt.Errorf("刷新成功但 access_token 为空")
	}

	exp := time.Now().Add(defaultCachedTokenLifetime)
	if tokenResp.ExpiresIn > 0 {
		exp = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	}
	rotatedRefreshToken := firstNonEmptyString(tokenResp.RefreshToken, refreshToken)
	chatGPTAccountID := firstNonEmptyString(
		ExtractChatGPTAccountID(refreshTokenRaw),
		ExtractChatGPTAccountIDFromIDToken(tokenResp.IDToken),
	)

	entry := cachedAccessToken{
		accessToken:      tokenResp.AccessToken,
		refreshToken:     rotatedRefreshToken,
		idToken:          tokenResp.IDToken,
		chatGPTAccountID: chatGPTAccountID,
		expiresAt:        exp,
	}
	m.cache[refreshToken] = entry
	if rotatedRefreshToken != refreshToken {
		m.cache[rotatedRefreshToken] = entry
	}
	return &ResolvedAccessToken{
		AccessToken:      tokenResp.AccessToken,
		RefreshToken:     rotatedRefreshToken,
		IDToken:          tokenResp.IDToken,
		ChatGPTAccountID: chatGPTAccountID,
		ExpiresAt:        exp,
	}, nil
}

func exchangeRefreshToken(ctx context.Context, cfg *config.Config, refreshToken string) (*refreshTokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", openAIOAuthClientID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, openAIRefreshTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("创建 RT 刷新请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", CodexBaseUserAgent())

	client, err := buildHTTPClient(cfg, defaultExchangeTimeout)
	if err != nil {
		return nil, fmt.Errorf("创建 RT 刷新客户端失败: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("RT 刷新请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := transport.ReadCodexResponseBody(resp, refreshTokenResponseReadLimit)
	if err != nil {
		return nil, fmt.Errorf("读取 RT 刷新响应失败 (HTTP %d): %w", resp.StatusCode, err)
	}
	bodyText := strings.TrimSpace(string(body))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if bodyText == "" {
			bodyText = "<empty>"
		}
		return nil, fmt.Errorf("RT 刷新失败 (HTTP %d): %s", resp.StatusCode, bodyText)
	}
	if bodyText == "" {
		return nil, fmt.Errorf("RT 刷新响应为空 (HTTP %d)", resp.StatusCode)
	}

	var parsed refreshTokenResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("解析 RT 刷新响应失败: %w (len=%d, content_type=%s)", err, len(body), resp.Header.Get("Content-Type"))
	}
	return &parsed, nil
}

// buildHTTPClient 统一走 Codex 出站 client 工厂：继承代理配置、关闭自动 gzip。
func buildHTTPClient(cfg *config.Config, timeout time.Duration) (*http.Client, error) {
	return transport.NewCodexHTTPClient(cfg, timeout)
}

func extractRefreshToken(raw string) string {
	trim := strings.TrimSpace(raw)
	if trim == "" {
		return ""
	}

	if strings.HasPrefix(trim, "{") {
		var payload map[string]any
		if err := json.Unmarshal([]byte(trim), &payload); err == nil {
			for _, key := range []string{"refresh_token", "rt", "token"} {
				if val, ok := payload[key].(string); ok && strings.TrimSpace(val) != "" {
					return strings.TrimSpace(val)
				}
			}
		}
	}

	for _, chunk := range strings.FieldsFunc(trim, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ',' || r == ';'
	}) {
		candidate := strings.Trim(strings.TrimSpace(chunk), "\"'")
		if candidate == "" {
			continue
		}
		lower := strings.ToLower(candidate)
		switch {
		case strings.HasPrefix(lower, "refresh_token="):
			return strings.Trim(strings.TrimSpace(candidate[len("refresh_token="):]), "\"'")
		case strings.HasPrefix(lower, "rt="):
			return strings.Trim(strings.TrimSpace(candidate[len("rt="):]), "\"'")
		case strings.HasPrefix(lower, "rt-"), strings.HasPrefix(lower, "rt_"):
			return candidate
		}
	}

	return strings.Trim(strings.TrimSpace(trim), "\"'")
}

func extractStoredAccessToken(raw string) (string, time.Time, bool) {
	trim := strings.TrimSpace(raw)
	if trim == "" || !strings.HasPrefix(trim, "{") {
		return "", time.Time{}, false
	}

	var payload storedOpenAICredential
	if err := json.Unmarshal([]byte(trim), &payload); err != nil {
		return "", time.Time{}, false
	}

	accessToken := strings.TrimSpace(payload.AccessToken)
	if accessToken == "" {
		return "", time.Time{}, false
	}

	expiresAt, err := time.Parse(time.RFC3339, strings.TrimSpace(payload.ExpiresAt))
	if err != nil {
		return "", time.Time{}, false
	}

	return accessToken, expiresAt, true
}

func extractStoredIDToken(raw string) string {
	trim := strings.TrimSpace(raw)
	if trim == "" || !strings.HasPrefix(trim, "{") {
		return ""
	}

	var payload storedOpenAICredential
	if err := json.Unmarshal([]byte(trim), &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.IDToken)
}

// ExtractChatGPTAccountID 从凭据中提取 chatgpt-account-id（可选）。
// 支持 JSON（chatgpt_account_id/account_id）以及 key=value 形式。
func ExtractChatGPTAccountID(raw string) string {
	trim := strings.TrimSpace(raw)
	if trim == "" {
		return ""
	}

	if strings.HasPrefix(trim, "{") {
		var payload map[string]any
		if err := json.Unmarshal([]byte(trim), &payload); err == nil {
			if accountID := stringFromAnyMap(payload, "chatgpt_account_id", "chatgptAccountId", "account_id", "accountId"); accountID != "" {
				return accountID
			}
			if idToken := stringFromAnyMap(payload, "id_token", "idToken"); idToken != "" {
				if accountID := ExtractChatGPTAccountIDFromIDToken(idToken); accountID != "" {
					return accountID
				}
			}
			if nested, ok := payload["credentials"].(map[string]any); ok {
				if accountID := stringFromAnyMap(nested, "chatgpt_account_id", "chatgptAccountId", "account_id", "accountId"); accountID != "" {
					return accountID
				}
				if idToken := stringFromAnyMap(nested, "id_token", "idToken"); idToken != "" {
					if accountID := ExtractChatGPTAccountIDFromIDToken(idToken); accountID != "" {
						return accountID
					}
				}
			}
		}
	}

	for _, chunk := range strings.FieldsFunc(trim, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ',' || r == ';' || r == '&'
	}) {
		candidate := strings.Trim(strings.TrimSpace(chunk), "\"'")
		if candidate == "" {
			continue
		}
		lower := strings.ToLower(candidate)
		switch {
		case strings.HasPrefix(lower, "chatgpt_account_id="):
			return strings.Trim(strings.TrimSpace(candidate[len("chatgpt_account_id="):]), "\"'")
		case strings.HasPrefix(lower, "chatgptaccountid="):
			return strings.Trim(strings.TrimSpace(candidate[len("chatgptaccountid="):]), "\"'")
		case strings.HasPrefix(lower, "account_id="):
			return strings.Trim(strings.TrimSpace(candidate[len("account_id="):]), "\"'")
		case strings.HasPrefix(lower, "accountid="):
			return strings.Trim(strings.TrimSpace(candidate[len("accountid="):]), "\"'")
		}
	}

	return ""
}

func ExtractChatGPTAccountIDFromIDToken(idToken string) string {
	return ExtractOpenAIAccountProfileFromIDToken(idToken).ChatGPTAccountID
}

func stringFromAnyMap(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if val, ok := m[key]; ok {
			if str, ok := val.(string); ok {
				trim := strings.TrimSpace(str)
				if trim != "" {
					return trim
				}
			}
		}
	}
	return ""
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
