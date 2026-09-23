package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cc-forwarder/internal/accountauth"
	"cc-forwarder/internal/transport"
)

const (
	openAIOAuthClientID             = "app_EMoamEEZ73f0CkXaXp7hrann"
	openAIOAuthAuthorizeURL         = "https://auth.openai.com/oauth/authorize"
	defaultOpenAIOAuthTokenURL      = "https://auth.openai.com/oauth/token"
	openAIOAuthDefaultRedirectURI   = "http://localhost:1455/auth/callback"
	openAIOAuthDefaultScopes        = "openid profile email offline_access api.connectors.read api.connectors.invoke"
	openAIOAuthDefaultOriginator    = "codex_cli_rs"
	openAIOAuthSessionTTL           = 10 * time.Minute
	openAIOAuthTokenExchangeTimeout = 25 * time.Second
	openAIOAuthResponseReadLimit    = 1024 * 1024
)

// openAIOAuthTokenURL 授权码兑换地址；测试中可替换为本地 httptest 服务。
var openAIOAuthTokenURL = defaultOpenAIOAuthTokenURL

type openAIOAuthSession struct {
	State        string
	CodeVerifier string
	RedirectURI  string
	ExpiresAt    time.Time
}

type GenerateChatGPTOAuthLinkResult struct {
	SessionID   string `json:"session_id"`
	AuthURL     string `json:"auth_url"`
	RedirectURI string `json:"redirect_uri"`
	ExpiresAt   string `json:"expires_at"`
}

type ExchangeChatGPTOAuthCallbackInput struct {
	SessionID   string `json:"session_id"`
	CallbackURL string `json:"callback_url"`
}

type ExchangeChatGPTOAuthCallbackResult struct {
	Success          bool   `json:"success"`
	RefreshToken     string `json:"refresh_token,omitempty"`
	AccessToken      string `json:"access_token,omitempty"`
	IDToken          string `json:"id_token,omitempty"`
	ExpiresAt        string `json:"expires_at,omitempty"`
	PlanType         string `json:"plan_type,omitempty"`
	ChatGPTAccountID string `json:"chatgpt_account_id,omitempty"`
	ChatGPTUserID    string `json:"chatgpt_user_id,omitempty"`
	OrganizationID   string `json:"organization_id,omitempty"`
	CredentialRaw    string `json:"credential_raw,omitempty"`
	Message          string `json:"message"`
}

type openAIOAuthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	IDToken      string `json:"id_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

// GenerateChatGPTOAuthLink 生成 OpenAI OAuth 授权链接（用于手动粘贴回调URL提取 RT）。
func (a *App) GenerateChatGPTOAuthLink() (GenerateChatGPTOAuthLinkResult, error) {
	sessionID, err := randomHex(16)
	if err != nil {
		return GenerateChatGPTOAuthLinkResult{}, fmt.Errorf("生成 session_id 失败: %w", err)
	}
	state, err := randomHex(32)
	if err != nil {
		return GenerateChatGPTOAuthLinkResult{}, fmt.Errorf("生成 state 失败: %w", err)
	}
	codeVerifier, err := randomHex(64)
	if err != nil {
		return GenerateChatGPTOAuthLinkResult{}, fmt.Errorf("生成 code_verifier 失败: %w", err)
	}

	redirectURI := openAIOAuthDefaultRedirectURI
	authURL := buildOpenAIOAuthURL(state, codeVerifier, redirectURI)
	expiresAt := time.Now().Add(openAIOAuthSessionTTL)

	a.oauthSessionMu.Lock()
	defer a.oauthSessionMu.Unlock()

	if a.oauthSessions == nil {
		a.oauthSessions = make(map[string]openAIOAuthSession)
	}
	a.cleanupExpiredOAuthSessionsLocked()
	a.oauthSessions[sessionID] = openAIOAuthSession{
		State:        state,
		CodeVerifier: codeVerifier,
		RedirectURI:  redirectURI,
		ExpiresAt:    expiresAt,
	}

	return GenerateChatGPTOAuthLinkResult{
		SessionID:   sessionID,
		AuthURL:     authURL,
		RedirectURI: redirectURI,
		ExpiresAt:   formatAPITime(expiresAt),
	}, nil
}

// ExchangeChatGPTOAuthCallback 用回调 URL 兑换 RT。
func (a *App) ExchangeChatGPTOAuthCallback(input ExchangeChatGPTOAuthCallbackInput) (ExchangeChatGPTOAuthCallbackResult, error) {
	sessionID := strings.TrimSpace(input.SessionID)
	callbackURL := strings.TrimSpace(input.CallbackURL)
	if sessionID == "" {
		return ExchangeChatGPTOAuthCallbackResult{}, fmt.Errorf("session_id 不能为空")
	}
	if callbackURL == "" {
		return ExchangeChatGPTOAuthCallbackResult{}, fmt.Errorf("callback_url 不能为空")
	}
	a.logOAuthInfo(
		"开始处理 OAuth 回调兑换请求",
		"session_id", maskSessionIDForLog(sessionID),
		"callback_len", len(callbackURL),
	)

	parsedURL, err := url.Parse(callbackURL)
	if err != nil {
		a.logOAuthWarn(
			"解析 OAuth 回调 URL 失败",
			"session_id", maskSessionIDForLog(sessionID),
			"callback_len", len(callbackURL),
			"error", err,
		)
		return ExchangeChatGPTOAuthCallbackResult{}, fmt.Errorf("解析回调 URL 失败: %w", err)
	}

	query := parsedURL.Query()
	queryError := strings.TrimSpace(query.Get("error"))
	queryErrorDesc := strings.TrimSpace(query.Get("error_description"))
	if queryError != "" {
		a.logOAuthWarn(
			"OAuth 回调返回授权错误",
			"session_id", maskSessionIDForLog(sessionID),
			"oauth_error", queryError,
			"oauth_error_description", queryErrorDesc,
		)
		return ExchangeChatGPTOAuthCallbackResult{}, buildOAuthCallbackError(queryError, queryErrorDesc)
	}
	a.logOAuthInfo(
		"OAuth 回调 URL 已解析",
		"session_id", maskSessionIDForLog(sessionID),
		"callback_url", summarizeCallbackURLForLog(parsedURL),
		"has_query_code", strings.TrimSpace(query.Get("code")) != "",
		"has_query_state", strings.TrimSpace(query.Get("state")) != "",
		"has_query_refresh_token", firstNonEmpty(strings.TrimSpace(query.Get("refresh_token")), strings.TrimSpace(query.Get("rt"))) != "",
		"has_query_error", queryError != "",
		"has_fragment", strings.TrimSpace(parsedURL.Fragment) != "",
	)
	if result, ok := buildOAuthCredentialResultFromValues(query, "已从回调 URL 中提取 OAuth 凭据"); ok {
		a.logOAuthInfo(
			"OAuth 回调 URL 已直接包含 OAuth 凭据",
			"session_id", maskSessionIDForLog(sessionID),
		)
		return result, nil
	}

	code := strings.TrimSpace(query.Get("code"))
	state := strings.TrimSpace(query.Get("state"))
	if code == "" && parsedURL.Fragment != "" {
		fragValues, _ := url.ParseQuery(parsedURL.Fragment)
		fragmentError := strings.TrimSpace(fragValues.Get("error"))
		fragmentErrorDesc := strings.TrimSpace(fragValues.Get("error_description"))
		if fragmentError != "" {
			a.logOAuthWarn(
				"OAuth 回调 Fragment 返回授权错误",
				"session_id", maskSessionIDForLog(sessionID),
				"oauth_error", fragmentError,
				"oauth_error_description", fragmentErrorDesc,
			)
			return ExchangeChatGPTOAuthCallbackResult{}, buildOAuthCallbackError(fragmentError, fragmentErrorDesc)
		}
		code = strings.TrimSpace(fragValues.Get("code"))
		if state == "" {
			state = strings.TrimSpace(fragValues.Get("state"))
		}
		a.logOAuthInfo(
			"已解析 OAuth 回调 Fragment",
			"session_id", maskSessionIDForLog(sessionID),
			"has_fragment_code", code != "",
			"has_fragment_state", state != "",
		)
		if result, ok := buildOAuthCredentialResultFromValues(fragValues, "已从回调 URL Fragment 中提取 OAuth 凭据"); ok {
			a.logOAuthInfo(
				"OAuth 回调 Fragment 已直接包含 OAuth 凭据",
				"session_id", maskSessionIDForLog(sessionID),
			)
			return result, nil
		}
	}
	if code == "" {
		a.logOAuthWarn(
			"OAuth 回调缺少 code 参数",
			"session_id", maskSessionIDForLog(sessionID),
		)
		return ExchangeChatGPTOAuthCallbackResult{}, fmt.Errorf("回调 URL 缺少 code 参数")
	}
	if state == "" {
		a.logOAuthWarn(
			"OAuth 回调缺少 state 参数",
			"session_id", maskSessionIDForLog(sessionID),
		)
		return ExchangeChatGPTOAuthCallbackResult{}, fmt.Errorf("回调 URL 缺少 state 参数")
	}

	a.oauthSessionMu.Lock()
	session, exists := a.oauthSessions[sessionID]
	if exists {
		delete(a.oauthSessions, sessionID)
	}
	a.cleanupExpiredOAuthSessionsLocked()
	a.oauthSessionMu.Unlock()

	if !exists {
		a.logOAuthWarn(
			"OAuth 授权会话不存在或已过期",
			"session_id", maskSessionIDForLog(sessionID),
		)
		return ExchangeChatGPTOAuthCallbackResult{}, fmt.Errorf("授权会话不存在或已过期，请重新生成授权链接")
	}
	if time.Now().After(session.ExpiresAt) {
		a.logOAuthWarn(
			"OAuth 授权会话已过期",
			"session_id", maskSessionIDForLog(sessionID),
			"expires_at", formatAPITime(session.ExpiresAt),
		)
		return ExchangeChatGPTOAuthCallbackResult{}, fmt.Errorf("授权会话已过期，请重新生成授权链接")
	}
	if session.State != state {
		a.logOAuthWarn(
			"OAuth 回调 state 校验失败",
			"session_id", maskSessionIDForLog(sessionID),
		)
		return ExchangeChatGPTOAuthCallbackResult{}, fmt.Errorf("state 校验失败，请重新生成授权链接")
	}

	ctx, cancel := context.WithTimeout(context.Background(), openAIOAuthTokenExchangeTimeout)
	defer cancel()
	tokenResp, err := a.exchangeOpenAIOAuthCode(ctx, code, session.CodeVerifier, session.RedirectURI)
	if err != nil {
		a.logOAuthWarn(
			"OAuth token 兑换失败",
			"session_id", maskSessionIDForLog(sessionID),
			"error", err,
		)
		return ExchangeChatGPTOAuthCallbackResult{}, err
	}

	if strings.TrimSpace(tokenResp.RefreshToken) == "" {
		a.logOAuthWarn(
			"OAuth token 兑换成功但未返回 refresh_token",
			"session_id", maskSessionIDForLog(sessionID),
		)
		return ExchangeChatGPTOAuthCallbackResult{}, fmt.Errorf("token 兑换成功但未返回 refresh_token")
	}
	a.logOAuthInfo(
		"OAuth 回调兑换成功",
		"session_id", maskSessionIDForLog(sessionID),
		"expires_in", tokenResp.ExpiresIn,
	)
	credentialResult := buildOAuthCredentialResult(tokenResp, "RT 提取成功")

	return credentialResult, nil
}

func (a *App) exchangeOpenAIOAuthCode(ctx context.Context, code, codeVerifier, redirectURI string) (*openAIOAuthTokenResponse, error) {
	formData := url.Values{}
	formData.Set("grant_type", "authorization_code")
	formData.Set("client_id", openAIOAuthClientID)
	formData.Set("code", code)
	formData.Set("redirect_uri", redirectURI)
	formData.Set("code_verifier", codeVerifier)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, openAIOAuthTokenURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return nil, fmt.Errorf("创建 token 兑换请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", accountauth.CodexBaseUserAgent())

	client, err := a.buildOAuthHTTPClient(openAIOAuthTokenExchangeTimeout)
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token 兑换请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := transport.ReadCodexResponseBody(resp, openAIOAuthResponseReadLimit)
	if err != nil {
		return nil, fmt.Errorf("读取 token 兑换响应失败 (HTTP %d): %w", resp.StatusCode, err)
	}
	bodyText := strings.TrimSpace(string(body))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if bodyText == "" {
			bodyText = "<empty>"
		}
		return nil, fmt.Errorf("token 兑换失败 (HTTP %d): %s", resp.StatusCode, bodyText)
	}
	if bodyText == "" {
		return nil, fmt.Errorf("token 兑换响应为空 (HTTP %d)", resp.StatusCode)
	}

	var tokenResp openAIOAuthTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("解析 token 响应失败: %w (len=%d, content_type=%s)", err, len(body), resp.Header.Get("Content-Type"))
	}
	return &tokenResp, nil
}

func (a *App) buildOAuthHTTPClient(timeout time.Duration) (*http.Client, error) {
	a.mu.RLock()
	cfg := a.config
	a.mu.RUnlock()

	client, err := transport.NewCodexHTTPClient(cfg, timeout)
	if err != nil {
		return nil, fmt.Errorf("创建 OAuth HTTP 客户端失败: %w", err)
	}
	return client, nil
}

func (a *App) cleanupExpiredOAuthSessionsLocked() {
	now := time.Now()
	for sessionID, session := range a.oauthSessions {
		if now.After(session.ExpiresAt) {
			delete(a.oauthSessions, sessionID)
		}
	}
}

func buildOpenAIOAuthURL(state, codeVerifier, redirectURI string) string {
	codeChallenge := sha256CodeChallenge(codeVerifier)
	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", openAIOAuthClientID)
	params.Set("redirect_uri", redirectURI)
	params.Set("scope", openAIOAuthDefaultScopes)
	params.Set("state", state)
	params.Set("code_challenge", codeChallenge)
	params.Set("code_challenge_method", "S256")
	params.Set("id_token_add_organizations", "true")
	params.Set("codex_cli_simplified_flow", "true")
	params.Set("originator", openAIOAuthDefaultOriginator)
	return openAIOAuthAuthorizeURL + "?" + params.Encode()
}

func buildOAuthCredentialResultFromValues(values url.Values, message string) (ExchangeChatGPTOAuthCallbackResult, bool) {
	refreshToken := firstNonEmpty(strings.TrimSpace(values.Get("refresh_token")), strings.TrimSpace(values.Get("rt")))
	accessToken := strings.TrimSpace(values.Get("access_token"))
	idToken := strings.TrimSpace(values.Get("id_token"))
	if refreshToken == "" {
		return ExchangeChatGPTOAuthCallbackResult{}, false
	}

	var expiresAt time.Time
	if rawExpiresAt := strings.TrimSpace(values.Get("expires_at")); rawExpiresAt != "" {
		if parsed, err := time.Parse(time.RFC3339, rawExpiresAt); err == nil {
			expiresAt = parsed
		}
	}
	if expiresAt.IsZero() {
		if rawExpiresIn := strings.TrimSpace(values.Get("expires_in")); rawExpiresIn != "" {
			if seconds, err := strconv.Atoi(rawExpiresIn); err == nil && seconds > 0 {
				expiresAt = time.Now().Add(time.Duration(seconds) * time.Second)
			}
		}
	}

	profile := accountauth.ExtractOpenAIAccountProfile(firstNonEmpty(idToken, values.Get("claims")))
	if profile.ChatGPTAccountID == "" {
		profile.ChatGPTAccountID = firstNonEmpty(
			strings.TrimSpace(values.Get("chatgpt_account_id")),
			strings.TrimSpace(values.Get("account_id")),
		)
	}
	if profile.PlanType == "" {
		profile.PlanType = accountauth.NormalizeOpenAIPlanType(firstNonEmpty(
			strings.TrimSpace(values.Get("chatgpt_plan_type")),
			strings.TrimSpace(values.Get("plan_type")),
			strings.TrimSpace(values.Get("planType")),
		))
	}
	if profile.ChatGPTUserID == "" {
		profile.ChatGPTUserID = firstNonEmpty(
			strings.TrimSpace(values.Get("chatgpt_user_id")),
			strings.TrimSpace(values.Get("user_id")),
			strings.TrimSpace(values.Get("userId")),
		)
	}
	if profile.OrganizationID == "" {
		profile.OrganizationID = firstNonEmpty(
			strings.TrimSpace(values.Get("organization_id")),
			strings.TrimSpace(values.Get("org_id")),
			strings.TrimSpace(values.Get("organizationId")),
		)
	}

	tokenResp := &openAIOAuthTokenResponse{
		AccessToken:  accessToken,
		IDToken:      idToken,
		RefreshToken: refreshToken,
	}
	if !expiresAt.IsZero() {
		tokenResp.ExpiresIn = int(time.Until(expiresAt).Seconds())
	}

	result := buildOAuthCredentialResult(tokenResp, message)
	if profile.ChatGPTAccountID != "" && result.ChatGPTAccountID == "" {
		result.ChatGPTAccountID = profile.ChatGPTAccountID
	}
	if profile.PlanType != "" && result.PlanType == "" {
		result.PlanType = profile.PlanType
	}
	if profile.ChatGPTUserID != "" && result.ChatGPTUserID == "" {
		result.ChatGPTUserID = profile.ChatGPTUserID
	}
	if profile.OrganizationID != "" && result.OrganizationID == "" {
		result.OrganizationID = profile.OrganizationID
	}
	mergedProfile := accountauth.OpenAIAccountProfile{
		PlanType:         result.PlanType,
		ChatGPTAccountID: result.ChatGPTAccountID,
		ChatGPTUserID:    result.ChatGPTUserID,
		OrganizationID:   result.OrganizationID,
	}
	result.CredentialRaw = buildStoredOAuthCredential(refreshToken, accessToken, idToken, mergedProfile, expiresAt)
	return result, true
}

func buildOAuthCredentialResult(tokenResp *openAIOAuthTokenResponse, message string) ExchangeChatGPTOAuthCallbackResult {
	if tokenResp == nil {
		return ExchangeChatGPTOAuthCallbackResult{Success: false, Message: message}
	}

	var expiresAt time.Time
	if tokenResp.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	}
	profile := accountauth.ExtractOpenAIAccountProfileFromIDToken(tokenResp.IDToken)
	credentialRaw := buildStoredOAuthCredential(tokenResp.RefreshToken, tokenResp.AccessToken, tokenResp.IDToken, profile, expiresAt)

	return ExchangeChatGPTOAuthCallbackResult{
		Success:          true,
		RefreshToken:     tokenResp.RefreshToken,
		AccessToken:      tokenResp.AccessToken,
		IDToken:          tokenResp.IDToken,
		ExpiresAt:        formatRFC3339(expiresAt),
		PlanType:         profile.PlanType,
		ChatGPTAccountID: profile.ChatGPTAccountID,
		ChatGPTUserID:    profile.ChatGPTUserID,
		OrganizationID:   profile.OrganizationID,
		CredentialRaw:    credentialRaw,
		Message:          message,
	}
}

func buildStoredOAuthCredential(refreshToken, accessToken, idToken string, profile accountauth.OpenAIAccountProfile, expiresAt time.Time) string {
	return accountauth.BuildStoredOpenAICredential(refreshToken, accessToken, idToken, profile, expiresAt)
}

func formatRFC3339(ts time.Time) string {
	if ts.IsZero() {
		return ""
	}
	return formatAPITime(ts)
}

func sha256CodeChallenge(codeVerifier string) string {
	sum := sha256.Sum256([]byte(codeVerifier))
	return strings.TrimRight(base64.URLEncoding.EncodeToString(sum[:]), "=")
}

func randomHex(nBytes int) (string, error) {
	raw := make([]byte, nBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func summarizeCallbackURLForLog(parsed *url.URL) string {
	if parsed == nil {
		return ""
	}
	return fmt.Sprintf("%s://%s%s", parsed.Scheme, parsed.Host, parsed.EscapedPath())
}

func maskSessionIDForLog(sessionID string) string {
	trim := strings.TrimSpace(sessionID)
	if len(trim) <= 6 {
		return "***"
	}
	return trim[:6] + "***"
}

func buildOAuthCallbackError(code, description string) error {
	msg := fmt.Sprintf("OAuth 授权失败: %s", strings.TrimSpace(code))
	desc := strings.TrimSpace(description)
	if desc != "" {
		msg += " - " + desc
	}
	if strings.EqualFold(strings.TrimSpace(code), "invalid_scope") {
		msg += "（当前 OAuth Client 不允许该 scope，请重新生成授权链接后重试）"
	}
	return fmt.Errorf(msg)
}

func (a *App) logOAuthInfo(msg string, args ...any) {
	if a != nil && a.logger != nil {
		a.logger.Info(msg, args...)
	}
}

func (a *App) logOAuthWarn(msg string, args ...any) {
	if a != nil && a.logger != nil {
		a.logger.Warn(msg, args...)
	}
}
