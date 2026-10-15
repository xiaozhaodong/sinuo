package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"sinuo/internal/accountauth"
	"sinuo/internal/store"
	"sinuo/internal/transport"
)

const (
	defaultChatGPTWhamUsageURL = "https://chatgpt.com/backend-api/wham/usage"
	quotaResponseReadLimit     = 1024 * 1024

	quotaStatusOK                   = "ok"
	quotaStatusUnavailable          = "unavailable"
	quotaStatusExhausted            = "exhausted"
	quotaStatusWorkspaceDeactivated = "workspace_deactivated"
	quotaStatusAuthInvalid          = "auth_invalid"
)

var chatGPTWhamUsageURL = defaultChatGPTWhamUsageURL

// AccountProfileRefreshResult 表示账号画像刷新结果。
type AccountProfileRefreshResult struct {
	Success     bool   `json:"success"`
	Message     string `json:"message"`
	QuotaStatus string `json:"quota_status,omitempty"`
}

type quotaSnapshot struct {
	PlanType               string
	Quota5HUsedPercent     *float64
	Quota5HResetAt         *time.Time
	QuotaWeeklyUsedPercent *float64
	QuotaWeeklyResetAt     *time.Time
}

func (q quotaSnapshot) hasAnyQuota() bool {
	return q.Quota5HUsedPercent != nil || q.Quota5HResetAt != nil || q.QuotaWeeklyUsedPercent != nil || q.QuotaWeeklyResetAt != nil
}

// RefreshAccountProfile 刷新单个 OAuth 账号画像与 quota 信息。
func (s *AccountPoolService) RefreshAccountProfile(ctx context.Context, id int64) (AccountProfileRefreshResult, error) {
	if err := s.ensureRuntimeCache(ctx); err != nil {
		return AccountProfileRefreshResult{}, err
	}
	acc, err := s.GetAccount(ctx, id)
	if err != nil {
		return AccountProfileRefreshResult{}, fmt.Errorf("获取账号失败: %w", err)
	}
	if acc == nil {
		return AccountProfileRefreshResult{}, fmt.Errorf("账号不存在: %d", id)
	}
	if !accountauth.IsChatGPTOAuthProvider(acc.ProviderType) {
		return AccountProfileRefreshResult{
			Success:     false,
			Message:     "仅 ChatGPT OAuth 账号支持刷新账号信息",
			QuotaStatus: quotaStatusUnavailable,
		}, nil
	}

	return s.refreshChatGPTOAuthProfile(ctx, acc)
}

// persistAccountAuthFailureProfile 仅串行本地状态发布，不在账号状态锁内请求 OAuth 或 quota 接口。
func (s *AccountPoolService) persistAccountAuthFailureProfile(ctx context.Context, acc *store.UpstreamAccountRecord, reason string, failedAt time.Time) error {
	return s.runAccountStateWrite(ctx, acc.ID, func() error {
		if err := s.store.MarkAccountAuthFailedWithProfile(ctx, acc, reason); err != nil {
			return err
		}
		if ok, _ := s.runtimeCache.markAuthFailed(acc.ID, reason, failedAt); !ok {
			return fmt.Errorf("账号 %d 鉴权状态已落库，但运行态不存在", acc.ID)
		}
		if s.softFailureTracker != nil {
			s.softFailureTracker.Clear(acc.ID)
		}
		s.runtimeCache.mergeRecordPreservingRuntimeState(acc)
		return nil
	})
}

func (s *AccountPoolService) populateAccountProfile(record *store.UpstreamAccountRecord) {
	if record == nil {
		return
	}

	profile := openAIProfileFromRecord(record)
	profile = overlayOpenAIProfile(profile, accountauth.ExtractOpenAIAccountProfile(record.CredentialRaw))
	applyOpenAIProfileToRecord(record, profile)
	if accountauth.IsChatGPTOAuthProvider(record.ProviderType) && record.PlanType == "" {
		record.PlanType = "unknown"
	}
}

func (s *AccountPoolService) refreshChatGPTOAuthProfile(ctx context.Context, acc *store.UpstreamAccountRecord) (AccountProfileRefreshResult, error) {
	now := time.Now()
	profile := openAIProfileFromRecord(acc)
	profile = overlayOpenAIProfile(profile, accountauth.ExtractOpenAIAccountProfile(acc.CredentialRaw))

	resolved, err := s.refreshTokenManager.ResolveAccessTokenDetails(ctx, acc.CredentialRaw)
	if err != nil {
		status := quotaStatusUnavailable
		authResolveErr := err
		message := fmt.Sprintf("刷新失败：%s", authResolveErr.Error())
		if looksLikeOAuthInvalid(authResolveErr.Error()) {
			status = quotaStatusAuthInvalid
		}
		profileUpdateTime(acc, now, status)
		applyOpenAIProfileToRecord(acc, profile)
		if status == quotaStatusAuthInvalid {
			if storeErr := s.persistAccountAuthFailureProfile(ctx, acc, authResolveErr.Error(), now); storeErr != nil {
				return AccountProfileRefreshResult{}, fmt.Errorf("写回账号鉴权失效画像失败: %w", storeErr)
			}
		} else {
			if updateErr := s.store.UpdateAccountProfile(ctx, acc); updateErr != nil {
				return AccountProfileRefreshResult{}, fmt.Errorf("写回账号画像失败: %w", updateErr)
			}
			s.runtimeCache.mergeRecordPreservingRuntimeState(acc)
		}
		return AccountProfileRefreshResult{
			Success:     false,
			Message:     message,
			QuotaStatus: status,
		}, nil
	}

	resolvedProfile := accountauth.ExtractOpenAIAccountProfileFromIDToken(resolved.IDToken)
	if resolved.ChatGPTAccountID != "" {
		resolvedProfile.ChatGPTAccountID = resolved.ChatGPTAccountID
	}
	profile = overlayOpenAIProfile(profile, resolvedProfile)
	if profile.PlanType == "" {
		profile.PlanType = "unknown"
	}

	storedCredential := accountauth.BuildStoredOpenAICredential(
		resolved.RefreshToken,
		resolved.AccessToken,
		resolved.IDToken,
		profile,
		resolved.ExpiresAt,
	)
	if storedCredential != "" {
		acc.CredentialRaw = storedCredential
	}
	applyOpenAIProfileToRecord(acc, profile)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, chatGPTWhamUsageURL, nil)
	if err != nil {
		return AccountProfileRefreshResult{}, fmt.Errorf("创建 quota 请求失败: %w", err)
	}
	req.Host = "chatgpt.com"
	req.Header.Set("Host", "chatgpt.com")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", accountauth.CodexBaseUserAgent())
	req.Header.Set("Authorization", "Bearer "+resolved.AccessToken)
	if profile.ChatGPTAccountID != "" && accountauth.NormalizeOpenAIPlanType(profile.PlanType) != "free" {
		req.Header.Set("chatgpt-account-id", profile.ChatGPTAccountID)
	}

	client, err := s.buildHTTPClient(20 * time.Second)
	if err != nil {
		return AccountProfileRefreshResult{}, fmt.Errorf("创建 quota 客户端失败: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		profileUpdateTime(acc, now, quotaStatusUnavailable)
		if updateErr := s.store.UpdateAccountProfile(ctx, acc); updateErr != nil {
			return AccountProfileRefreshResult{}, fmt.Errorf("写回账号画像失败: %w", updateErr)
		}
		s.runtimeCache.mergeRecordPreservingRuntimeState(acc)
		return AccountProfileRefreshResult{
			Success:     false,
			Message:     fmt.Sprintf("刷新失败：%s", err.Error()),
			QuotaStatus: quotaStatusUnavailable,
		}, nil
	}
	defer resp.Body.Close()

	// 关闭自动 gzip 后由此处按 Content-Encoding 解码一次；读取失败只丢正文，仍按状态码与响应头结算。
	body, readErr := transport.ReadCodexResponseBody(resp, quotaResponseReadLimit)
	if readErr != nil {
		body = nil
	}
	bodyText := strings.TrimSpace(string(body))

	headerQuota, headerErr := parseQuotaHeaders(resp.Header, now)
	bodyQuota, bodyErr := parseQuotaBody(body, now)
	mergedQuota := mergeQuotaSnapshot(bodyQuota, headerQuota)
	if mergedQuota.PlanType == "" {
		mergedQuota.PlanType = accountauth.NormalizeOpenAIPlanType(profile.PlanType)
	}
	if mergedQuota.PlanType == "free" {
		if mergedQuota.QuotaWeeklyUsedPercent == nil && mergedQuota.Quota5HUsedPercent != nil {
			mergedQuota.QuotaWeeklyUsedPercent = mergedQuota.Quota5HUsedPercent
		}
		if mergedQuota.QuotaWeeklyResetAt == nil && mergedQuota.Quota5HResetAt != nil {
			mergedQuota.QuotaWeeklyResetAt = mergedQuota.Quota5HResetAt
		}
		mergedQuota.Quota5HUsedPercent = nil
		mergedQuota.Quota5HResetAt = nil
	}
	if mergedQuota.PlanType != "" {
		profile.PlanType = mergedQuota.PlanType
	}
	applyOpenAIProfileToRecord(acc, profile)
	applyQuotaSnapshotToRecord(acc, mergedQuota)
	profileUpdateTime(acc, now, quotaStatusUnavailable)

	upstreamMessage := extractQuotaErrorMessage(bodyText)

	switch resp.StatusCode {
	case http.StatusOK:
		if mergedQuota.hasAnyQuota() {
			status := quotaStatusOK
			message := "账号信息已刷新"
			if hasExhaustedQuotaReset(acc, now) {
				status = quotaStatusExhausted
				message = "账号信息已刷新，当前额度已耗尽"
			}
			profileUpdateTime(acc, now, status)
			if err := s.store.UpdateAccountProfile(ctx, acc); err != nil {
				return AccountProfileRefreshResult{}, fmt.Errorf("写回账号画像失败: %w", err)
			}
			s.runtimeCache.mergeRecordPreservingRuntimeState(acc)
			return AccountProfileRefreshResult{
				Success:     true,
				Message:     message,
				QuotaStatus: status,
			}, nil
		}

		msg := "刷新失败：未获取到有效额度信息"
		if readErr != nil {
			msg += fmt.Sprintf("（响应体读取失败：%s）", readErr.Error())
		} else if bodyErr != nil {
			msg += "（响应体解析失败）"
		} else if headerErr != nil {
			msg += "（响应头解析失败）"
		}
		if err := s.store.UpdateAccountProfile(ctx, acc); err != nil {
			return AccountProfileRefreshResult{}, fmt.Errorf("写回账号画像失败: %w", err)
		}
		s.runtimeCache.mergeRecordPreservingRuntimeState(acc)
		return AccountProfileRefreshResult{
			Success:     false,
			Message:     msg,
			QuotaStatus: quotaStatusUnavailable,
		}, nil
	case http.StatusUnauthorized:
		profileUpdateTime(acc, now, quotaStatusAuthInvalid)
		authReason := firstNonEmptyString(upstreamMessage, "OAuth 已失效")
		if err := s.persistAccountAuthFailureProfile(ctx, acc, authReason, now); err != nil {
			return AccountProfileRefreshResult{}, fmt.Errorf("写回账号鉴权失效画像失败: %w", err)
		}
		return AccountProfileRefreshResult{
			Success:     false,
			Message:     fmt.Sprintf("刷新失败：OAuth 已失效%s", formatUpstreamMessage(upstreamMessage)),
			QuotaStatus: quotaStatusAuthInvalid,
		}, nil
	case http.StatusPaymentRequired:
		mergedQuota = normalizeExhaustedQuotaSnapshot(mergedQuota, now)
		applyQuotaSnapshotToRecord(acc, mergedQuota)
		if looksLikeWorkspaceDeactivated(upstreamMessage) || looksLikeAccountDeactivated(upstreamMessage) {
			profileUpdateTime(acc, now, quotaStatusWorkspaceDeactivated)
			authReason := firstNonEmptyString(upstreamMessage, "工作区已停用")
			if err := s.persistAccountAuthFailureProfile(ctx, acc, authReason, now); err != nil {
				return AccountProfileRefreshResult{}, fmt.Errorf("写回工作区停用画像失败: %w", err)
			}
			return AccountProfileRefreshResult{
				Success:     false,
				Message:     fmt.Sprintf("刷新失败：工作区已停用%s", formatUpstreamMessage(upstreamMessage)),
				QuotaStatus: quotaStatusWorkspaceDeactivated,
			}, nil
		}

		if !mergedQuota.hasAnyQuota() {
			if accountauth.NormalizeOpenAIPlanType(profile.PlanType) == "free" {
				mergedQuota.QuotaWeeklyUsedPercent = float64Ptr(100)
				mergedQuota.QuotaWeeklyResetAt = nil
			} else {
				mergedQuota.Quota5HUsedPercent = float64Ptr(100)
				mergedQuota.Quota5HResetAt = nil
				mergedQuota.QuotaWeeklyUsedPercent = float64Ptr(100)
				mergedQuota.QuotaWeeklyResetAt = nil
			}
			applyQuotaSnapshotToRecord(acc, mergedQuota)
		}
		profileUpdateTime(acc, now, quotaStatusExhausted)
		if err := s.store.UpdateAccountProfile(ctx, acc); err != nil {
			return AccountProfileRefreshResult{}, fmt.Errorf("写回账号画像失败: %w", err)
		}
		s.runtimeCache.mergeRecordPreservingRuntimeState(acc)
		return AccountProfileRefreshResult{
			Success:     true,
			Message:     "账号信息已刷新，当前额度已耗尽",
			QuotaStatus: quotaStatusExhausted,
		}, nil
	default:
		if err := s.store.UpdateAccountProfile(ctx, acc); err != nil {
			return AccountProfileRefreshResult{}, fmt.Errorf("写回账号画像失败: %w", err)
		}
		s.runtimeCache.mergeRecordPreservingRuntimeState(acc)
		return AccountProfileRefreshResult{
			Success:     false,
			Message:     fmt.Sprintf("刷新失败：quota 接口返回状态码 %d%s", resp.StatusCode, formatUpstreamMessage(upstreamMessage)),
			QuotaStatus: quotaStatusUnavailable,
		}, nil
	}
}

func openAIProfileFromRecord(record *store.UpstreamAccountRecord) accountauth.OpenAIAccountProfile {
	if record == nil {
		return accountauth.OpenAIAccountProfile{}
	}
	return accountauth.OpenAIAccountProfile{
		PlanType:         accountauth.NormalizeOpenAIPlanType(record.PlanType),
		ChatGPTAccountID: strings.TrimSpace(record.ChatGPTAccountID),
		ChatGPTUserID:    strings.TrimSpace(record.ChatGPTUserID),
		OrganizationID:   strings.TrimSpace(record.OrganizationID),
	}
}

func overlayOpenAIProfile(base, incoming accountauth.OpenAIAccountProfile) accountauth.OpenAIAccountProfile {
	if incoming.PlanType != "" {
		base.PlanType = accountauth.NormalizeOpenAIPlanType(incoming.PlanType)
	}
	if incoming.ChatGPTAccountID != "" {
		base.ChatGPTAccountID = strings.TrimSpace(incoming.ChatGPTAccountID)
	}
	if incoming.ChatGPTUserID != "" {
		base.ChatGPTUserID = strings.TrimSpace(incoming.ChatGPTUserID)
	}
	if incoming.OrganizationID != "" {
		base.OrganizationID = strings.TrimSpace(incoming.OrganizationID)
	}
	return base
}

func applyOpenAIProfileToRecord(record *store.UpstreamAccountRecord, profile accountauth.OpenAIAccountProfile) {
	if record == nil {
		return
	}
	record.PlanType = accountauth.NormalizeOpenAIPlanType(profile.PlanType)
	record.ChatGPTAccountID = strings.TrimSpace(profile.ChatGPTAccountID)
	record.ChatGPTUserID = strings.TrimSpace(profile.ChatGPTUserID)
	record.OrganizationID = strings.TrimSpace(profile.OrganizationID)
}

func applyQuotaSnapshotToRecord(record *store.UpstreamAccountRecord, snapshot quotaSnapshot) {
	if record == nil {
		return
	}
	record.Quota5HUsedPercent = snapshot.Quota5HUsedPercent
	record.Quota5HResetAt = snapshot.Quota5HResetAt
	record.QuotaWeeklyUsedPercent = snapshot.QuotaWeeklyUsedPercent
	record.QuotaWeeklyResetAt = snapshot.QuotaWeeklyResetAt
}

func profileUpdateTime(record *store.UpstreamAccountRecord, now time.Time, status string) {
	record.QuotaStatus = status
	record.QuotaRefreshedAt = &now
}

func normalizeExhaustedQuotaSnapshot(snapshot quotaSnapshot, now time.Time) quotaSnapshot {
	planType := accountauth.NormalizeOpenAIPlanType(snapshot.PlanType)
	if planType == "free" {
		if snapshot.QuotaWeeklyResetAt != nil && snapshot.QuotaWeeklyResetAt.After(now) {
			snapshot.QuotaWeeklyUsedPercent = float64Ptr(100)
		}
		return snapshot
	}

	if snapshot.Quota5HResetAt != nil && snapshot.Quota5HResetAt.After(now) &&
		(snapshot.Quota5HUsedPercent == nil || *snapshot.Quota5HUsedPercent >= 99.999) {
		snapshot.Quota5HUsedPercent = float64Ptr(100)
	}
	if snapshot.QuotaWeeklyResetAt != nil && snapshot.QuotaWeeklyResetAt.After(now) &&
		(snapshot.QuotaWeeklyUsedPercent == nil || *snapshot.QuotaWeeklyUsedPercent >= 99.999) {
		snapshot.QuotaWeeklyUsedPercent = float64Ptr(100)
	}
	return snapshot
}

func mergeQuotaSnapshot(primary, fallback quotaSnapshot) quotaSnapshot {
	if primary.PlanType == "" {
		primary.PlanType = fallback.PlanType
	}
	if primary.Quota5HUsedPercent == nil {
		primary.Quota5HUsedPercent = fallback.Quota5HUsedPercent
	}
	if primary.Quota5HResetAt == nil {
		primary.Quota5HResetAt = fallback.Quota5HResetAt
	}
	if primary.QuotaWeeklyUsedPercent == nil {
		primary.QuotaWeeklyUsedPercent = fallback.QuotaWeeklyUsedPercent
	}
	if primary.QuotaWeeklyResetAt == nil {
		primary.QuotaWeeklyResetAt = fallback.QuotaWeeklyResetAt
	}
	return primary
}

func parseQuotaBody(body []byte, now time.Time) (quotaSnapshot, error) {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return quotaSnapshot{}, nil
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return quotaSnapshot{}, err
	}

	rateLimit, _ := payload["rate_limit"].(map[string]any)
	primaryWindow, _ := rateLimit["primary_window"].(map[string]any)
	secondaryWindow, _ := rateLimit["secondary_window"].(map[string]any)

	planType := accountauth.NormalizeOpenAIPlanType(firstNonEmptyString(
		coerceString(payload["plan_type"]),
		coerceString(payload["planType"]),
	))

	result := quotaSnapshot{PlanType: planType}
	if len(secondaryWindow) > 0 && planType != "free" {
		result.Quota5HUsedPercent = parseOptionalFloat(primaryWindow["used_percent"])
		result.Quota5HResetAt = parseResetAt(primaryWindow, now)
		result.QuotaWeeklyUsedPercent = parseOptionalFloat(secondaryWindow["used_percent"])
		result.QuotaWeeklyResetAt = parseResetAt(secondaryWindow, now)
		return result, nil
	}

	if planType == "free" {
		result.QuotaWeeklyUsedPercent = parseOptionalFloat(primaryWindow["used_percent"])
		result.QuotaWeeklyResetAt = parseResetAt(primaryWindow, now)
		return result, nil
	}

	result.Quota5HUsedPercent = parseOptionalFloat(primaryWindow["used_percent"])
	result.Quota5HResetAt = parseResetAt(primaryWindow, now)
	return result, nil
}

func parseQuotaHeaders(headers http.Header, now time.Time) (quotaSnapshot, error) {
	if headers == nil || len(headers) == 0 {
		return quotaSnapshot{}, nil
	}
	normalized := map[string]string{}
	for key, values := range headers {
		if len(values) == 0 {
			continue
		}
		normalized[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(values[0])
	}
	if len(normalized) == 0 {
		return quotaSnapshot{}, nil
	}

	planType := accountauth.NormalizeOpenAIPlanType(normalized["x-codex-plan-type"])
	primary := map[string]any{
		"used_percent":        normalized["x-codex-primary-used-percent"],
		"reset_after_seconds": normalized["x-codex-primary-reset-after-seconds"],
		"reset_at":            normalized["x-codex-primary-reset-at"],
	}
	secondary := map[string]any{
		"used_percent":        normalized["x-codex-secondary-used-percent"],
		"reset_after_seconds": normalized["x-codex-secondary-reset-after-seconds"],
		"reset_at":            normalized["x-codex-secondary-reset-at"],
	}

	result := quotaSnapshot{PlanType: planType}
	if hasAnyQuotaValue(secondary) && planType != "free" {
		result.Quota5HUsedPercent = parseOptionalFloat(primary["used_percent"])
		result.Quota5HResetAt = parseResetAt(primary, now)
		result.QuotaWeeklyUsedPercent = parseOptionalFloat(secondary["used_percent"])
		result.QuotaWeeklyResetAt = parseResetAt(secondary, now)
		return result, nil
	}

	if planType == "free" {
		result.QuotaWeeklyUsedPercent = parseOptionalFloat(primary["used_percent"])
		result.QuotaWeeklyResetAt = parseResetAt(primary, now)
		return result, nil
	}

	result.Quota5HUsedPercent = parseOptionalFloat(primary["used_percent"])
	result.Quota5HResetAt = parseResetAt(primary, now)
	return result, nil
}

func parseResetAt(window map[string]any, now time.Time) *time.Time {
	if len(window) == 0 {
		return nil
	}
	if resetAt := parseOptionalUnix(window["reset_at"]); resetAt != nil {
		return resetAt
	}
	resetAfter := parseOptionalInt(window["reset_after_seconds"])
	if resetAfter == nil || *resetAfter <= 0 {
		return nil
	}
	resetTime := now.Add(time.Duration(*resetAfter) * time.Second)
	return &resetTime
}

func parseOptionalFloat(value any) *float64 {
	switch typed := value.(type) {
	case nil:
		return nil
	case float64:
		return &typed
	case float32:
		converted := float64(typed)
		return &converted
	case int:
		converted := float64(typed)
		return &converted
	case int64:
		converted := float64(typed)
		return &converted
	case json.Number:
		if parsed, err := typed.Float64(); err == nil {
			return &parsed
		}
	case string:
		trim := strings.TrimSpace(typed)
		if trim == "" {
			return nil
		}
		if parsed, err := strconv.ParseFloat(trim, 64); err == nil {
			return &parsed
		}
	}
	return nil
}

func parseOptionalInt(value any) *int64 {
	switch typed := value.(type) {
	case nil:
		return nil
	case int:
		converted := int64(typed)
		return &converted
	case int64:
		return &typed
	case float64:
		converted := int64(typed)
		return &converted
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return &parsed
		}
	case string:
		trim := strings.TrimSpace(typed)
		if trim == "" {
			return nil
		}
		if parsed, err := strconv.ParseInt(trim, 10, 64); err == nil {
			return &parsed
		}
	}
	return nil
}

func parseOptionalUnix(value any) *time.Time {
	seconds := parseOptionalInt(value)
	if seconds == nil || *seconds <= 0 {
		return nil
	}
	ts := time.Unix(*seconds, 0).UTC()
	return &ts
}

func hasAnyQuotaValue(window map[string]any) bool {
	for _, value := range window {
		if strings.TrimSpace(coerceString(value)) != "" {
			return true
		}
	}
	return false
}

func coerceString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return ""
	}
}

func extractQuotaErrorMessage(bodyText string) string {
	if strings.TrimSpace(bodyText) == "" {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(bodyText), &payload); err == nil {
		if errField, ok := payload["error"].(map[string]any); ok {
			if msg := strings.TrimSpace(coerceString(errField["message"])); msg != "" {
				return msg
			}
		}
		if msg := strings.TrimSpace(coerceString(payload["message"])); msg != "" {
			return msg
		}
		if errMsg := strings.TrimSpace(coerceString(payload["error"])); errMsg != "" {
			return errMsg
		}
	}
	return truncateString(bodyText, 300)
}

func looksLikeOAuthInvalid(message string) bool {
	lower := strings.ToLower(strings.TrimSpace(message))
	return strings.Contains(lower, "http 401") ||
		strings.Contains(lower, "invalid_grant") ||
		strings.Contains(lower, "token has been invalidated") ||
		strings.Contains(lower, "authentication token has been invalidated")
}

func looksLikeWorkspaceDeactivated(message string) bool {
	lower := strings.ToLower(strings.TrimSpace(message))
	return strings.Contains(lower, "deactivated_workspace") ||
		(strings.Contains(lower, "workspace") && strings.Contains(lower, "deactivated"))
}

func looksLikeAccountDeactivated(message string) bool {
	lower := strings.ToLower(strings.TrimSpace(message))
	return strings.Contains(lower, "account has been deactivated") ||
		strings.Contains(lower, "account deactivated")
}

func formatUpstreamMessage(message string) string {
	trim := strings.TrimSpace(message)
	if trim == "" {
		return ""
	}
	return "：" + trim
}

func truncateString(text string, max int) string {
	trim := strings.TrimSpace(text)
	if max <= 0 || len(trim) <= max {
		return trim
	}
	return trim[:max]
}

func float64Ptr(value float64) *float64 {
	return &value
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		trim := strings.TrimSpace(value)
		if trim != "" {
			return trim
		}
	}
	return ""
}
