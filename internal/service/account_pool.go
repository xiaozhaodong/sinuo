// Package service 提供账号池业务服务
package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cc-forwarder/config"
	"cc-forwarder/internal/accountauth"
	"cc-forwarder/internal/store"
	"cc-forwarder/internal/transport"
)

const (
	defaultOpenAIResponsesURL            = "https://api.openai.com/v1/responses"
	defaultChatGPTCodexTestURL           = "https://chatgpt.com/backend-api/codex/responses"
	defaultOpenAIBetaHeader              = "responses=experimental"
	defaultOAuthOriginatorValue          = "codex_cli_rs"
	usageLimitResetMatchWindow           = 15 * time.Minute
	accountPoolSettingsCategory          = "account_pool"
	accountPoolPinnedAccountIDSettingKey = "pinned_account_id"
)

type usageLimitQuotaWindow string

const (
	usageLimitQuotaWindowUnknown usageLimitQuotaWindow = ""
	usageLimitQuotaWindow5H      usageLimitQuotaWindow = "5h"
	usageLimitQuotaWindowWeekly  usageLimitQuotaWindow = "weekly"
)

var chatGPTCodexTestURL = defaultChatGPTCodexTestURL

// AccountPoolService 账号池服务
type AccountPoolService struct {
	store                         store.AccountPoolStore
	config                        *config.Config
	settingsService               *SettingsService
	refreshTokenManager           *accountauth.OpenAIRefreshTokenManager
	scheduleSnapshots             *latestAccountScheduleSnapshotStore
	softFailureTracker            *accountSoftFailureTracker
	quotaRefreshDispatcher        *accountPoolQuotaRefreshDispatcher
	quotaProbeScheduler           *accountPoolQuotaProbeScheduler
	runtimeCache                  *accountRuntimeCache
	runtimeWriter                 *accountPoolRuntimeWriter
	startupConnectivityTestRunner func(context.Context, int64) error
	accountStateMu                sync.Mutex // 串行状态变更；锁序为本锁 → runtimeWriter.flushMu → cache/store 内部锁
	runtimeInitMu                 sync.Mutex
	runtimeInitialized            atomic.Bool
}

// NewAccountPoolService 创建账号池服务
func NewAccountPoolService(st store.AccountPoolStore, cfg *config.Config) *AccountPoolService {
	svc := &AccountPoolService{
		store:               st,
		config:              cfg,
		refreshTokenManager: accountauth.NewOpenAIRefreshTokenManager(cfg),
		scheduleSnapshots:   newLatestAccountScheduleSnapshotStore(),
		softFailureTracker:  newAccountSoftFailureTracker(),
		runtimeCache:        newAccountRuntimeCache(),
	}
	svc.runtimeWriter = newAccountPoolRuntimeWriter(context.Background(), st, svc.runtimeCache.matchesStateVersion)
	svc.quotaRefreshDispatcher = newAccountPoolQuotaRefreshDispatcher(context.Background(), svc.RefreshAccountProfile)
	if cfg != nil && cfg.AccountPool.Enabled {
		svc.quotaProbeScheduler = newAccountPoolQuotaProbeScheduler(context.Background(), func(ctx context.Context) ([]*store.UpstreamAccountRecord, error) {
			return svc.ListAccounts(ctx, false)
		}, svc.TryEnqueueQuotaRefresh)
	}
	return svc
}

func (s *AccountPoolService) SetSettingsService(settingsSvc *SettingsService) {
	if s == nil {
		return
	}
	s.settingsService = settingsSvc
}

// ===== 账号 =====

func (s *AccountPoolService) ListAccounts(ctx context.Context, includeDisabled bool) ([]*store.UpstreamAccountRecord, error) {
	if err := s.ensureRuntimeCache(ctx); err != nil {
		return nil, err
	}
	return s.runtimeCache.list(includeDisabled), nil
}

func (s *AccountPoolService) GetAccount(ctx context.Context, id int64) (*store.UpstreamAccountRecord, error) {
	if err := s.ensureRuntimeCache(ctx); err != nil {
		return nil, err
	}
	record, ok := s.runtimeCache.get(id)
	if !ok {
		return nil, nil
	}
	return record, nil
}

func (s *AccountPoolService) GetActiveSelectionAccountID(ctx context.Context) (int64, bool, error) {
	if err := s.ensureRuntimeCache(ctx); err != nil {
		return 0, false, err
	}
	if s.runtimeCache == nil {
		return 0, false, nil
	}
	accountID, ok := s.runtimeCache.activeSelectionAccountID()
	return accountID, ok, nil
}

func (s *AccountPoolService) GetGroupPreferredAccountIDs(ctx context.Context) (map[string]int64, error) {
	if err := s.ensureRuntimeCache(ctx); err != nil {
		return nil, err
	}
	if s.runtimeCache == nil {
		return nil, nil
	}
	return s.runtimeCache.groupPreferredAccountIDs(), nil
}

func (s *AccountPoolService) PinAccountSelection(ctx context.Context, id int64) (bool, error) {
	if err := s.ensureRuntimeCache(ctx); err != nil {
		return false, err
	}
	if id <= 0 {
		return false, fmt.Errorf("无效的账号 ID: %d", id)
	}

	record, ok := s.runtimeCache.get(id)
	if !ok || record == nil {
		return false, fmt.Errorf("账号不存在: %d", id)
	}

	changed := s.runtimeCache.selectAccount(id)
	_ = s.persistPinnedAccountSelection(ctx, id)
	if changed && s.softFailureTracker != nil {
		s.softFailureTracker.Clear(id)
	}
	if changed && s.scheduleSnapshots != nil {
		s.scheduleSnapshots.clear()
	}
	return changed, nil
}

func (s *AccountPoolService) EnableAutomaticAccountSelection(ctx context.Context) (bool, error) {
	if err := s.ensureRuntimeCache(ctx); err != nil {
		return false, err
	}

	changed := false
	if s.runtimeCache != nil {
		changed = s.runtimeCache.clearPinnedSelection()
	}
	if err := s.clearPersistedPinnedAccountSelection(ctx); err != nil {
		return false, err
	}
	if changed && s.scheduleSnapshots != nil {
		s.scheduleSnapshots.clear()
	}
	return changed, nil
}

func (s *AccountPoolService) SetGroupActiveAccount(ctx context.Context, groupKey string, id int64) (bool, error) {
	if err := s.ensureRuntimeCache(ctx); err != nil {
		return false, err
	}
	if id <= 0 {
		return false, fmt.Errorf("无效的账号 ID: %d", id)
	}

	record, ok := s.runtimeCache.get(id)
	if !ok || record == nil {
		return false, fmt.Errorf("账号不存在: %d", id)
	}

	groupKey = normalizeAccountGroupKey(groupKey)
	if groupKey == "" {
		return false, fmt.Errorf("无效的组别: %s", groupKey)
	}
	if inferAccountGroupKey(record) != groupKey {
		return false, fmt.Errorf("账号 %d 不属于%s", id, accountGroupLabel(groupKey))
	}

	changed := s.runtimeCache.setPreferredAccountInGroup(groupKey, id)
	if changed && s.scheduleSnapshots != nil {
		s.scheduleSnapshots.clear()
	}
	return changed, nil
}

func (s *AccountPoolService) CreateAccount(ctx context.Context, rec *store.UpstreamAccountRecord) (*store.UpstreamAccountRecord, error) {
	if err := s.ensureRuntimeCache(ctx); err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, fmt.Errorf("account record is nil")
	}
	if err := config.ValidateSoftFailureOverrides(rec.SoftFailureWindowMinutes, rec.SoftFailureThreshold); err != nil {
		return nil, err
	}
	rec.AccountName = strings.TrimSpace(rec.AccountName)
	rec.ProviderType = strings.TrimSpace(rec.ProviderType)
	rec.CredentialRaw = strings.TrimSpace(rec.CredentialRaw)
	if rec.AccountName == "" {
		return nil, fmt.Errorf("账号名称不能为空")
	}
	if rec.CredentialRaw == "" {
		return nil, fmt.Errorf("账号凭据不能为空")
	}
	if rec.ProviderType == "" {
		rec.ProviderType = accountauth.InferProviderType(rec.CredentialRaw)
	}
	s.populateAccountProfile(rec)
	created, err := s.store.CreateAccount(ctx, rec)
	if err != nil {
		return nil, err
	}
	s.runtimeCache.upsert(created)
	return cloneUpstreamAccountRecord(created), nil
}

func (s *AccountPoolService) UpdateAccount(ctx context.Context, rec *store.UpstreamAccountRecord) error {
	s.accountStateMu.Lock()
	defer s.accountStateMu.Unlock()
	if s.runtimeWriter != nil {
		s.runtimeWriter.flushMu.Lock()
		defer s.runtimeWriter.flushMu.Unlock()
		if err := s.runtimeWriter.checkOpen(); err != nil {
			return err
		}
	}
	if err := s.ensureRuntimeCache(ctx); err != nil {
		return err
	}
	if rec == nil {
		return fmt.Errorf("account record is nil")
	}
	rec.AccountName = strings.TrimSpace(rec.AccountName)
	rec.ProviderType = strings.TrimSpace(rec.ProviderType)
	rec.CredentialRaw = strings.TrimSpace(rec.CredentialRaw)
	if rec.AccountName == "" {
		return fmt.Errorf("账号名称不能为空")
	}
	if rec.CredentialRaw == "" {
		return fmt.Errorf("账号凭据不能为空")
	}
	if rec.ProviderType == "" {
		rec.ProviderType = accountauth.InferProviderType(rec.CredentialRaw)
	}
	if err := config.ValidateSoftFailureOverrides(rec.SoftFailureWindowMinutes, rec.SoftFailureThreshold); err != nil {
		return err
	}
	current, err := s.GetAccount(ctx, rec.ID)
	if err != nil {
		return err
	}
	if current == nil {
		return fmt.Errorf("账号不存在: %d", rec.ID)
	}
	shouldToggle := rec.Enabled != current.Enabled
	if shouldToggle && s.runtimeWriter != nil {
		if err := s.runtimeWriter.flushAccountLocked(ctx, rec.ID); err != nil {
			return err
		}
	}
	persistedCurrent, err := s.store.GetAccount(ctx, rec.ID)
	if err != nil {
		return err
	}
	if persistedCurrent == nil {
		return fmt.Errorf("账号不存在: %d", rec.ID)
	}
	policyChanged := !config.SameSoftFailureOverrides(current.SoftFailureWindowMinutes, current.SoftFailureThreshold, rec.SoftFailureWindowMinutes, rec.SoftFailureThreshold)
	var toggledBeforePersist bool
	var toggledVersion uint64
	var previousRuntimeState accountRuntimeStateSnapshot
	merged := mergeEditableAccountRecord(persistedCurrent, rec)
	if shouldToggle {
		changedAt := time.Now()
		if ok, version, snapshot := s.runtimeCache.applyToggle(rec.ID, rec.Enabled, changedAt); !ok {
			return fmt.Errorf("账号不存在: %d", rec.ID)
		} else {
			toggledVersion = version
			previousRuntimeState = snapshot
		}
		toggledBeforePersist = true
		applyEnabledTransitionForUpdate(merged, rec.Enabled)
	}
	s.populateAccountProfile(merged)
	if err := s.store.UpdateAccount(ctx, merged); err != nil {
		if toggledBeforePersist {
			_ = s.runtimeCache.restoreRuntimeStateIfVersion(rec.ID, toggledVersion, previousRuntimeState)
		}
		return err
	}
	if policyChanged {
		s.softFailureTracker.Clear(rec.ID)
	}
	if !s.runtimeCache.mergeRecordPreservingRuntimeState(merged) {
		if err := s.reloadAccountIntoCache(ctx, rec.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s *AccountPoolService) DeleteAccount(ctx context.Context, id int64) error {
	s.accountStateMu.Lock()
	defer s.accountStateMu.Unlock()
	if s.runtimeWriter != nil {
		s.runtimeWriter.flushMu.Lock()
		defer s.runtimeWriter.flushMu.Unlock()
		if err := s.runtimeWriter.checkOpen(); err != nil {
			return err
		}
	}
	if err := s.ensureRuntimeCache(ctx); err != nil {
		return err
	}
	if err := s.store.DeleteAccount(ctx, id); err != nil {
		return err
	}
	s.softFailureTracker.Clear(id)
	_ = s.clearPersistedPinnedAccountSelectionIfMatches(ctx, id)
	if s.runtimeWriter != nil {
		s.runtimeWriter.Drop(id)
	}
	s.runtimeCache.remove(id)
	return nil
}

// DeleteAccountWithModelGuard 在删除前检查 Codex 模型绑定引用。
func (s *AccountPoolService) DeleteAccountWithModelGuard(ctx context.Context, id int64, modelsUsing func(context.Context, int64) ([]string, error)) error {
	if modelsUsing != nil {
		models, err := modelsUsing(ctx, id)
		if err != nil {
			return fmt.Errorf("检查 Codex 模型路由账号引用失败: %w", err)
		}
		if len(models) > 0 {
			return fmt.Errorf("%s 仍被 Codex 模型 %s 使用，请先在「模型路由」中取消绑定",
				s.accountDisplayLabel(ctx, id), strings.Join(models, "、"))
		}
	}
	return s.DeleteAccount(ctx, id)
}

// accountDisplayLabel 取账号展示名，读不到就退回 ID，保证删除守卫的提示始终可读。
func (s *AccountPoolService) accountDisplayLabel(ctx context.Context, id int64) string {
	record, err := s.store.GetAccount(ctx, id)
	if err != nil || record == nil || strings.TrimSpace(record.AccountName) == "" {
		return fmt.Sprintf("账号 %d", id)
	}
	return fmt.Sprintf("账号「%s」", record.AccountName)
}

func (s *AccountPoolService) ToggleAccount(ctx context.Context, id int64, enabled bool) error {
	if err := s.ensureRuntimeCache(ctx); err != nil {
		return err
	}
	return s.runAccountStateWrite(ctx, id, func() error {
		changedAt := time.Now()
		ok, toggledVersion, previousRuntimeState := s.runtimeCache.applyToggle(id, enabled, changedAt)
		if !ok {
			return fmt.Errorf("账号不存在: %d", id)
		}
		if err := s.store.ToggleAccount(ctx, id, enabled); err != nil {
			if restored := s.runtimeCache.restoreRuntimeStateIfVersion(id, toggledVersion, previousRuntimeState); !restored {
				_ = s.reloadAccountIntoCache(ctx, id)
			}
			return err
		}
		return nil
	})
}

func (s *AccountPoolService) Close() error {
	var firstErr error
	if s.quotaProbeScheduler != nil {
		if err := s.quotaProbeScheduler.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if s.quotaRefreshDispatcher != nil {
		if err := s.quotaRefreshDispatcher.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if s.runtimeWriter != nil {
		if err := s.runtimeWriter.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (s *AccountPoolService) TryEnqueueQuotaRefresh(id int64) bool {
	if s == nil || s.quotaRefreshDispatcher == nil {
		return false
	}
	return s.quotaRefreshDispatcher.TryEnqueue(id)
}

// ===== 调度/状态回写（供 proxy 使用） =====

func (s *AccountPoolService) ListSchedulableAccounts(ctx context.Context) ([]*store.UpstreamAccountRecord, error) {
	result, err := s.prepareSchedulableAccounts(ctx, "", "")
	if err != nil {
		return nil, err
	}
	return result.Accounts, nil
}

func (s *AccountPoolService) MarkAccountSuccess(ctx context.Context, id int64) error {
	if err := s.ensureRuntimeCache(ctx); err != nil {
		return err
	}
	return s.runAccountStateWrite(ctx, id, func() error {
		successAt := time.Now()
		if ok, _ := s.runtimeCache.markSuccess(id, successAt); !ok {
			return fmt.Errorf("账号不存在: %d", id)
		}
		if err := s.store.MarkAccountSuccess(ctx, id, successAt); err != nil {
			_ = s.reloadAccountIntoCache(ctx, id)
			return err
		}
		if s.softFailureTracker != nil {
			s.softFailureTracker.Clear(id)
		}
		return nil
	})
}

func (s *AccountPoolService) MarkAccountSuccessIfNoNewerFailure(ctx context.Context, id int64, attemptStartedAt time.Time) (bool, error) {
	if err := s.ensureRuntimeCache(ctx); err != nil {
		return false, err
	}
	s.accountStateMu.Lock()
	defer s.accountStateMu.Unlock()
	successAt := time.Now()
	updated, err := s.publishAccountRuntime(id, accountRuntimeMutationSuccess, func() (uint64, error) {
		_, version := s.runtimeCache.markSuccessIfNoNewerFailure(id, successAt, attemptStartedAt)
		return version, nil
	})
	if updated && s.softFailureTracker != nil {
		s.softFailureTracker.Clear(id)
	}
	return updated, err
}

func (s *AccountPoolService) MarkAccountAuthFailed(ctx context.Context, id int64, reason string) error {
	if err := s.ensureRuntimeCache(ctx); err != nil {
		return err
	}
	s.accountStateMu.Lock()
	defer s.accountStateMu.Unlock()
	failedAt := time.Now()
	_, err := s.publishAccountRuntime(id, accountRuntimeMutationAuthFailed, func() (uint64, error) {
		ok, version := s.runtimeCache.markAuthFailed(id, reason, failedAt)
		if !ok {
			return 0, fmt.Errorf("账号不存在: %d", id)
		}
		return version, nil
	})
	if err != nil {
		return err
	}
	if s.softFailureTracker != nil {
		s.softFailureTracker.Clear(id)
	}
	return nil
}

func (s *AccountPoolService) MarkAccountTransientFailure(ctx context.Context, id int64, reason string, cooldown time.Duration) error {
	if err := s.ensureRuntimeCache(ctx); err != nil {
		return err
	}
	s.accountStateMu.Lock()
	defer s.accountStateMu.Unlock()
	if cooldown <= 0 {
		cooldown = 30 * time.Second
	}
	failedAt := time.Now()
	cooldownUntil := failedAt.Add(cooldown)
	updated, err := s.publishAccountRuntime(id, accountRuntimeMutationTransientFailure, func() (uint64, error) {
		ok, version := s.runtimeCache.markTransientFailure(id, reason, cooldownUntil, failedAt)
		if !ok {
			return 0, fmt.Errorf("账号不存在: %d", id)
		}
		return version, nil
	})
	if err != nil || !updated {
		return err
	}
	if s.softFailureTracker != nil {
		s.softFailureTracker.Clear(id)
	}
	return nil
}

// ClearAccountCooldown 手动清除账号临时故障冷却；不改变额度耗尽或 disabled_auth 状态。
func (s *AccountPoolService) ClearAccountCooldown(ctx context.Context, id int64) error {
	if err := s.ensureRuntimeCache(ctx); err != nil {
		return err
	}
	if id <= 0 {
		return fmt.Errorf("账号不存在: %d", id)
	}
	return s.runAccountStateWrite(ctx, id, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		version, exists := s.runtimeCache.cooldownVersion(id)
		if !exists {
			return fmt.Errorf("账号不存在: %d", id)
		}
		if version == 0 {
			return nil
		}
		clearer, ok := s.store.(interface {
			ClearAccountCooldown(context.Context, int64, time.Time) error
		})
		if !ok {
			return fmt.Errorf("账号存储不支持清除冷却")
		}
		clearedAt := time.Now()
		if err := clearer.ClearAccountCooldown(ctx, id, clearedAt); err != nil {
			return err
		}
		if !s.runtimeCache.clearCooldownIfVersion(id, version, clearedAt) {
			return fmt.Errorf("账号 %d 冷却已落库，但运行态版本发生变化", id)
		}
		if s.softFailureTracker != nil {
			s.softFailureTracker.Clear(id)
		}
		return nil
	})
}

func (s *AccountPoolService) MarkAccountUsageLimitExceeded(ctx context.Context, id int64, reason, planType string, resetAt time.Time) error {
	if err := s.ensureRuntimeCache(ctx); err != nil {
		return err
	}
	if id <= 0 {
		return fmt.Errorf("invalid account id: %d", id)
	}
	s.accountStateMu.Lock()
	defer s.accountStateMu.Unlock()

	now := time.Now()
	if resetAt.IsZero() || !resetAt.After(now) {
		return fmt.Errorf("invalid usage limit reset time: %v", resetAt)
	}

	acc, err := s.GetAccount(ctx, id)
	if err != nil {
		return err
	}
	if acc == nil {
		return fmt.Errorf("账号不存在: %d", id)
	}

	resolvedPlanType := accountauth.NormalizeOpenAIPlanType(firstNonEmptyString(planType, acc.PlanType))
	if resolvedPlanType != "" {
		acc.PlanType = resolvedPlanType
	}

	quotaWindow := inferUsageLimitQuotaWindow(acc, resolvedPlanType, resetAt)
	shouldPersistProfile := false
	switch quotaWindow {
	case usageLimitQuotaWindowWeekly:
		if resolvedPlanType == "free" {
			acc.Quota5HUsedPercent = nil
			acc.Quota5HResetAt = nil
		}
		acc.QuotaWeeklyUsedPercent = float64Ptr(100)
		acc.QuotaWeeklyResetAt = cloneTimePtr(&resetAt)
		shouldPersistProfile = true
	case usageLimitQuotaWindow5H:
		acc.Quota5HUsedPercent = float64Ptr(100)
		acc.Quota5HResetAt = cloneTimePtr(&resetAt)
		shouldPersistProfile = true
	}

	profileStatus := quotaStatusUnavailable
	if shouldPersistProfile {
		profileStatus = quotaStatusExhausted
	}
	profileUpdateTime(acc, now, profileStatus)

	updated, err := s.publishAccountRuntime(id, accountRuntimeMutationTransientFailure, func() (uint64, error) {
		ok, version := s.runtimeCache.markTransientFailure(id, reason, resetAt, now)
		if !ok {
			return 0, fmt.Errorf("账号不存在: %d", id)
		}
		return version, nil
	})
	if err != nil || !updated {
		return err
	}
	if s.softFailureTracker != nil {
		s.softFailureTracker.Clear(id)
	}

	if err := s.store.UpdateAccountProfile(ctx, acc); err != nil {
		if shouldPersistProfile {
			return fmt.Errorf("写回额度耗尽画像失败: %w", err)
		}
		return fmt.Errorf("写回未知额度窗口画像失败: %w", err)
	}
	if !s.runtimeCache.mergeRecordPreservingRuntimeState(acc) {
		if err := s.reloadAccountIntoCache(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func inferUsageLimitQuotaWindow(acc *store.UpstreamAccountRecord, planType string, resetAt time.Time) usageLimitQuotaWindow {
	if accountauth.NormalizeOpenAIPlanType(planType) == "free" {
		return usageLimitQuotaWindowWeekly
	}
	if acc == nil || resetAt.IsZero() {
		return usageLimitQuotaWindowUnknown
	}

	fiveHDiff, hasFiveH := accountResetMatchDistance(acc.Quota5HResetAt, resetAt)
	weeklyDiff, hasWeekly := accountResetMatchDistance(acc.QuotaWeeklyResetAt, resetAt)

	switch {
	case hasFiveH && (!hasWeekly || fiveHDiff <= weeklyDiff) && fiveHDiff <= usageLimitResetMatchWindow:
		return usageLimitQuotaWindow5H
	case hasWeekly && weeklyDiff <= usageLimitResetMatchWindow:
		return usageLimitQuotaWindowWeekly
	default:
		return usageLimitQuotaWindowUnknown
	}
}

func accountResetMatchDistance(existing *time.Time, target time.Time) (time.Duration, bool) {
	if existing == nil || existing.IsZero() || target.IsZero() {
		return 0, false
	}
	diff := existing.Sub(target)
	if diff < 0 {
		diff = -diff
	}
	return diff, true
}

// TestUpstreamAccount 测试账号连通性
func (s *AccountPoolService) TestUpstreamAccount(ctx context.Context, id int64) error {
	if err := s.ensureRuntimeCache(ctx); err != nil {
		return err
	}
	acc, err := s.GetAccount(ctx, id)
	if err != nil {
		return fmt.Errorf("获取账号失败: %w", err)
	}
	if acc == nil {
		return fmt.Errorf("账号不存在: %d", id)
	}

	providerType := accountauth.NormalizeProviderType(acc.ProviderType)
	isOAuth := providerType == accountauth.ProviderChatGPTRefreshToken

	targetURL := defaultOpenAIResponsesURL
	if isOAuth {
		targetURL = chatGPTCodexTestURL
	} else {
		base := strings.TrimSuffix(strings.TrimSpace(acc.BaseURL), "/")
		if base == "" {
			base = "https://api.openai.com"
		}
		targetURL = base + "/v1/responses"
	}

	payload := strings.NewReader(`{"input":"ping"}`)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, payload)
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if err := accountauth.ApplyAccountAuth(ctx, req, acc.ProviderType, acc.CredentialRaw, s.refreshTokenManager); err != nil {
		return fmt.Errorf("应用账号鉴权失败: %w", err)
	}
	if isOAuth {
		accountID := accountauth.ExtractChatGPTAccountID(acc.CredentialRaw)
		if accountID == "" {
			return fmt.Errorf("OAuth 账号缺少 chatgpt_account_id，请重新授权后保存完整凭据")
		}
		req.Host = "chatgpt.com"
		req.Header.Set("Host", "chatgpt.com")
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("OpenAI-Beta", defaultOpenAIBetaHeader)
		req.Header.Set("originator", defaultOAuthOriginatorValue)
		req.Header.Set("chatgpt-account-id", accountID)
	}

	client, err := s.buildHTTPClient(15 * time.Second)
	if err != nil {
		return fmt.Errorf("创建 HTTP 客户端失败: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("连通性测试失败: %w", err)
	}
	defer resp.Body.Close()

	reachable := (resp.StatusCode >= 200 && resp.StatusCode < 400) ||
		resp.StatusCode == http.StatusBadRequest ||
		resp.StatusCode == http.StatusTooManyRequests
	if reachable {
		if err := s.MarkAccountSuccess(ctx, id); err != nil {
			return fmt.Errorf("写回账号连通成功时间失败: %w", err)
		}
		if isOAuth {
			_, _ = s.RefreshAccountProfile(ctx, id)
		}
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	bodyText := strings.TrimSpace(string(body))
	if isNoAvailableProviders503(resp.StatusCode, bodyText) {
		if err := s.MarkAccountSuccess(ctx, id); err != nil {
			return fmt.Errorf("写回账号连通成功时间失败: %w", err)
		}
		if isOAuth {
			_, _ = s.RefreshAccountProfile(ctx, id)
		}
		return nil
	}

	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		if !isOAuth && isMissingResponsesWriteScope(bodyText) {
			return fmt.Errorf("鉴权通过但权限不足 (%d): 缺少 api.responses.write，请重新走 OAuth 授权并更新 RT。原始响应: %s", resp.StatusCode, bodyText)
		}
		return fmt.Errorf("鉴权或权限不足 (%d): %s", resp.StatusCode, bodyText)
	default:
		return fmt.Errorf("上游返回状态码 %d: %s", resp.StatusCode, bodyText)
	}
}

func isMissingResponsesWriteScope(bodyText string) bool {
	lower := strings.ToLower(strings.TrimSpace(bodyText))
	if lower == "" {
		return false
	}
	return strings.Contains(lower, "api.responses.write") &&
		(strings.Contains(lower, "missing scopes") || strings.Contains(lower, "insufficient permissions"))
}

func isNoAvailableProviders503(statusCode int, bodyText string) bool {
	if statusCode != http.StatusServiceUnavailable {
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(bodyText))
	if lower == "" {
		return false
	}
	return strings.Contains(lower, "no_available_providers") ||
		strings.Contains(lower, "no available providers")
}

func (s *AccountPoolService) buildHTTPClient(timeout time.Duration) (*http.Client, error) {
	if s.config == nil {
		return &http.Client{Timeout: timeout}, nil
	}
	tp, err := transport.CreateTransport(s.config)
	if err != nil {
		return nil, err
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: tp,
	}, nil
}

func (s *AccountPoolService) ensureRuntimeCache(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("account pool service is nil")
	}
	if s.runtimeInitialized.Load() {
		return nil
	}

	s.runtimeInitMu.Lock()
	defer s.runtimeInitMu.Unlock()

	if s.runtimeInitialized.Load() {
		return nil
	}
	return s.reloadRuntimeCacheLocked(ctx)
}

func (s *AccountPoolService) reloadRuntimeCacheLocked(ctx context.Context) error {
	records, err := s.store.ListAccounts(ctx, true)
	if err != nil {
		return fmt.Errorf("加载账号运行时缓存失败: %w", err)
	}
	if s.runtimeCache == nil {
		s.runtimeCache = newAccountRuntimeCache()
	}
	s.runtimeCache.replaceAll(records)
	if err := s.restorePersistedPinnedAccountSelection(ctx); err != nil {
		return err
	}
	s.runtimeInitialized.Store(true)
	return nil
}

func (s *AccountPoolService) persistPinnedAccountSelection(ctx context.Context, id int64) error {
	if s == nil || s.settingsService == nil || id <= 0 {
		return nil
	}
	return s.settingsService.Set(ctx, accountPoolSettingsCategory, accountPoolPinnedAccountIDSettingKey, strconv.FormatInt(id, 10))
}

func (s *AccountPoolService) clearPersistedPinnedAccountSelection(ctx context.Context) error {
	if s == nil || s.settingsService == nil {
		return nil
	}
	err := s.settingsService.Delete(ctx, accountPoolSettingsCategory, accountPoolPinnedAccountIDSettingKey)
	if err != nil && !strings.Contains(err.Error(), "设置不存在") {
		return err
	}
	return nil
}

func (s *AccountPoolService) clearPersistedPinnedAccountSelectionIfMatches(ctx context.Context, id int64) error {
	if s == nil || s.settingsService == nil || id <= 0 {
		return nil
	}
	persistedID, ok, err := s.readPersistedPinnedAccountSelectionID(ctx)
	if err != nil || !ok || persistedID != id {
		return err
	}
	return s.clearPersistedPinnedAccountSelection(ctx)
}

func (s *AccountPoolService) readPersistedPinnedAccountSelectionID(ctx context.Context) (int64, bool, error) {
	if s == nil || s.settingsService == nil {
		return 0, false, nil
	}
	value, err := s.settingsService.GetValue(ctx, accountPoolSettingsCategory, accountPoolPinnedAccountIDSettingKey)
	if err != nil {
		return 0, false, err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false, nil
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, true, s.clearPersistedPinnedAccountSelection(ctx)
	}
	return id, true, nil
}

func (s *AccountPoolService) restorePersistedPinnedAccountSelection(ctx context.Context) error {
	persistedID, ok, err := s.readPersistedPinnedAccountSelectionID(ctx)
	if err != nil || !ok {
		return err
	}
	account, exists := s.runtimeCache.get(persistedID)
	now := time.Now()
	if !exists || !isPinnedAccountRestorable(account, now) {
		return s.clearPersistedPinnedAccountSelection(ctx)
	}
	s.runtimeCache.selectAccount(persistedID)
	if s.softFailureTracker != nil {
		s.softFailureTracker.Clear(persistedID)
	}
	return nil
}

func isPinnedAccountRestorable(account *store.UpstreamAccountRecord, now time.Time) bool {
	if account == nil || !account.Enabled {
		return false
	}

	state := strings.TrimSpace(strings.ToLower(account.State))
	if state == "disabled_auth" || state == "disabled" {
		return false
	}
	if state == "cooldown" {
		return false
	}
	if account.CooldownUntil != nil && account.CooldownUntil.After(now) {
		return false
	}
	if normalizeQuotaStatus(account.QuotaStatus) == quotaStatusExhausted && hasExhaustedQuotaReset(account, now) {
		return false
	}
	return true
}

func (s *AccountPoolService) reloadAccountIntoCache(ctx context.Context, id int64) error {
	if err := s.ensureRuntimeCache(ctx); err != nil {
		return err
	}
	record, err := s.store.GetAccount(ctx, id)
	if err != nil {
		return err
	}
	if record == nil {
		s.runtimeCache.remove(id)
		return nil
	}
	s.runtimeCache.upsert(record)
	return nil
}

func mergeEditableAccountRecord(current, incoming *store.UpstreamAccountRecord) *store.UpstreamAccountRecord {
	merged := cloneUpstreamAccountRecord(current)
	if merged == nil {
		return cloneUpstreamAccountRecord(incoming)
	}
	if incoming == nil {
		return merged
	}

	merged.ProviderType = incoming.ProviderType
	merged.AccountName = incoming.AccountName
	merged.CredentialRaw = incoming.CredentialRaw
	merged.BaseURL = incoming.BaseURL
	merged.ModelRewriteRules = incoming.ModelRewriteRules
	merged.SoftFailureWindowMinutes = config.CloneSoftFailureValue(incoming.SoftFailureWindowMinutes)
	merged.SoftFailureThreshold = config.CloneSoftFailureValue(incoming.SoftFailureThreshold)
	merged.EnableRequestCompression = incoming.EnableRequestCompression
	merged.StripCodexInternalMetadata = incoming.StripCodexInternalMetadata
	merged.CostMultiplier = incoming.CostMultiplier
	merged.InputCostMultiplier = incoming.InputCostMultiplier
	merged.OutputCostMultiplier = incoming.OutputCostMultiplier
	merged.CacheCreationCostMultiplier = incoming.CacheCreationCostMultiplier
	merged.CacheCreationCostMultiplier1h = incoming.CacheCreationCostMultiplier1h
	merged.CacheReadCostMultiplier = incoming.CacheReadCostMultiplier
	merged.GroupKey = incoming.GroupKey
	merged.Priority = incoming.Priority
	return merged
}

func applyEnabledTransitionForUpdate(record *store.UpstreamAccountRecord, enabled bool) {
	if record == nil {
		return
	}
	record.Enabled = enabled
	if enabled {
		record.State = "active"
		record.FailCount = 0
		record.CooldownUntil = nil
		record.LastError = ""
	}
}
