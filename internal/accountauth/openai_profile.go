package accountauth

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	timezonepolicy "sinuo/internal/timezone"
)

// OpenAIAccountProfile 表示从 ChatGPT OAuth 凭据中提取的账号画像。
type OpenAIAccountProfile struct {
	PlanType         string
	ChatGPTAccountID string
	ChatGPTUserID    string
	OrganizationID   string
}

// NormalizeOpenAIPlanType 统一账号类型格式，空值返回空字符串。
func NormalizeOpenAIPlanType(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return ""
	}
	return normalized
}

// ExtractOpenAIAccountProfile 从凭据原文提取账号画像。
func ExtractOpenAIAccountProfile(raw string) OpenAIAccountProfile {
	trim := strings.TrimSpace(raw)
	if trim == "" {
		return OpenAIAccountProfile{}
	}

	if strings.HasPrefix(trim, "{") {
		var payload map[string]any
		if err := json.Unmarshal([]byte(trim), &payload); err == nil {
			profile := extractProfileFromMap(payload)
			if nested, ok := payload["credentials"].(map[string]any); ok {
				profile = mergeOpenAIAccountProfile(profile, extractProfileFromMap(nested))
			}
			if idToken := stringFromAnyMap(payload, "id_token", "idToken"); idToken != "" {
				profile = mergeOpenAIAccountProfile(profile, ExtractOpenAIAccountProfileFromIDToken(idToken))
			}
			return profile
		}
	}

	if strings.Count(trim, ".") == 2 {
		return ExtractOpenAIAccountProfileFromIDToken(trim)
	}

	return OpenAIAccountProfile{}
}

// ExtractOpenAIAccountProfileFromIDToken 从 id_token 中提取账号画像。
func ExtractOpenAIAccountProfileFromIDToken(idToken string) OpenAIAccountProfile {
	claims, ok := decodeJWTClaims(idToken)
	if !ok {
		return OpenAIAccountProfile{}
	}
	return extractProfileFromMap(claims)
}

// BuildStoredOpenAICredential 将 OAuth 凭据序列化为存库 JSON。
func BuildStoredOpenAICredential(refreshToken, accessToken, idToken string, profile OpenAIAccountProfile, expiresAt time.Time) string {
	payload := map[string]string{}
	if strings.TrimSpace(refreshToken) != "" {
		payload["refresh_token"] = strings.TrimSpace(refreshToken)
	}
	if strings.TrimSpace(accessToken) != "" {
		payload["access_token"] = strings.TrimSpace(accessToken)
	}
	if strings.TrimSpace(idToken) != "" {
		payload["id_token"] = strings.TrimSpace(idToken)
	}
	if strings.TrimSpace(profile.PlanType) != "" {
		payload["plan_type"] = NormalizeOpenAIPlanType(profile.PlanType)
	}
	if strings.TrimSpace(profile.ChatGPTAccountID) != "" {
		payload["chatgpt_account_id"] = strings.TrimSpace(profile.ChatGPTAccountID)
	}
	if strings.TrimSpace(profile.ChatGPTUserID) != "" {
		payload["chatgpt_user_id"] = strings.TrimSpace(profile.ChatGPTUserID)
	}
	if strings.TrimSpace(profile.OrganizationID) != "" {
		payload["organization_id"] = strings.TrimSpace(profile.OrganizationID)
	}
	if !expiresAt.IsZero() {
		payload["expires_at"] = timezonepolicy.FormatStorage(expiresAt)
	}
	if len(payload) == 0 {
		return ""
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func mergeOpenAIAccountProfile(primary, fallback OpenAIAccountProfile) OpenAIAccountProfile {
	if primary.PlanType == "" {
		primary.PlanType = fallback.PlanType
	}
	if primary.ChatGPTAccountID == "" {
		primary.ChatGPTAccountID = fallback.ChatGPTAccountID
	}
	if primary.ChatGPTUserID == "" {
		primary.ChatGPTUserID = fallback.ChatGPTUserID
	}
	if primary.OrganizationID == "" {
		primary.OrganizationID = fallback.OrganizationID
	}
	return primary
}

func extractProfileFromMap(payload map[string]any) OpenAIAccountProfile {
	profile := OpenAIAccountProfile{
		PlanType: NormalizeOpenAIPlanType(firstNonEmptyString(
			stringFromAnyMap(payload, "https://api.openai.com/auth.chatgpt_plan_type"),
			stringFromAnyMap(payload, "chatgpt_plan_type", "plan_type", "planType"),
		)),
		ChatGPTAccountID: strings.TrimSpace(firstNonEmptyString(
			stringFromAnyMap(payload, "https://api.openai.com/auth.chatgpt_account_id"),
			stringFromAnyMap(payload, "chatgpt_account_id", "chatgptAccountId", "account_id", "accountId"),
		)),
		ChatGPTUserID: strings.TrimSpace(firstNonEmptyString(
			stringFromAnyMap(payload, "https://api.openai.com/auth.chatgpt_user_id"),
			stringFromAnyMap(payload, "chatgpt_user_id", "chatgptUserId", "user_id", "userId"),
		)),
		OrganizationID: strings.TrimSpace(firstNonEmptyString(
			stringFromAnyMap(payload, "https://api.openai.com/auth.organization_id"),
			stringFromAnyMap(payload, "organization_id", "organizationId", "org_id", "orgId"),
		)),
	}

	if nested, ok := payload["https://api.openai.com/auth"].(map[string]any); ok {
		profile = mergeOpenAIAccountProfile(profile, OpenAIAccountProfile{
			PlanType: NormalizeOpenAIPlanType(firstNonEmptyString(
				stringFromAnyMap(nested, "chatgpt_plan_type", "plan_type", "planType"),
			)),
			ChatGPTAccountID: strings.TrimSpace(firstNonEmptyString(
				stringFromAnyMap(nested, "chatgpt_account_id", "chatgptAccountId", "account_id", "accountId"),
			)),
			ChatGPTUserID: strings.TrimSpace(firstNonEmptyString(
				stringFromAnyMap(nested, "chatgpt_user_id", "chatgptUserId", "user_id", "userId"),
			)),
			OrganizationID: strings.TrimSpace(firstNonEmptyString(
				stringFromAnyMap(nested, "organization_id", "organizationId", "org_id", "orgId"),
			)),
		})
	}

	return profile
}

func decodeJWTClaims(idToken string) (map[string]any, bool) {
	trim := strings.TrimSpace(idToken)
	if trim == "" {
		return nil, false
	}

	parts := strings.Split(trim, ".")
	if len(parts) != 3 {
		return nil, false
	}

	payload := parts[1]
	switch len(payload) % 4 {
	case 2:
		payload += "=="
	case 3:
		payload += "="
	}

	decoded, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		decoded, err = base64.StdEncoding.DecodeString(payload)
		if err != nil {
			return nil, false
		}
	}

	var claims map[string]any
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return nil, false
	}
	return claims, true
}
