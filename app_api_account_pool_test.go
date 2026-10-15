package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sinuo/internal/service"
	"sinuo/internal/store"

	_ "modernc.org/sqlite"
)

func TestGetUpstreamAccounts_ReturnsUTC(t *testing.T) {
	app, _ := newAccountPoolAPITestApp(t)

	resetAtUTC := time.Date(2026, 3, 11, 13, 6, 0, 0, time.UTC)
	_, err := app.accountPoolService.CreateAccount(context.Background(), &store.UpstreamAccountRecord{
		ProviderType:                  "chatgpt_refresh_token",
		AccountName:                   "lixunhuan",
		CredentialRaw:                 `{"refresh_token":"rt-test"}`,
		BaseURL:                       "https://api.openai.com",
		CostMultiplier:                1.0,
		InputCostMultiplier:           1.0,
		OutputCostMultiplier:          1.0,
		CacheCreationCostMultiplier:   1.0,
		CacheCreationCostMultiplier1h: 1.0,
		CacheReadCostMultiplier:       1.0,
		Priority:                      20,
		Enabled:                       true,
		State:                         "active",
		QuotaStatus:                   "ok",
		Quota5HUsedPercent:            float64Ptr(6),
		Quota5HResetAt:                &resetAtUTC,
	})
	if err != nil {
		t.Fatalf("CreateAccount failed: %v", err)
	}

	accounts, err := app.GetUpstreamAccounts()
	if err != nil {
		t.Fatalf("GetUpstreamAccounts failed: %v", err)
	}
	if len(accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(accounts))
	}
	if got := accounts[0].Quota5HResetAt; got != "2026-03-11T13:06:00.000000Z" {
		t.Fatalf("expected quota reset in UTC, got %q", got)
	}
}

func TestGetUpstreamAccounts_RejectsUnmigratedNaiveTimestamps(t *testing.T) {
	app, db := newAccountPoolAPITestApp(t)

	created, err := app.accountPoolStore.CreateAccount(context.Background(), &store.UpstreamAccountRecord{
		ProviderType:                  "chatgpt_refresh_token",
		AccountName:                   "legacy-naive",
		CredentialRaw:                 `{"refresh_token":"rt-legacy"}`,
		BaseURL:                       "https://api.openai.com",
		CostMultiplier:                1.0,
		InputCostMultiplier:           1.0,
		OutputCostMultiplier:          1.0,
		CacheCreationCostMultiplier:   1.0,
		CacheCreationCostMultiplier1h: 1.0,
		CacheReadCostMultiplier:       1.0,
		Priority:                      30,
		Enabled:                       true,
		State:                         "active",
		QuotaStatus:                   "ok",
	})
	if err != nil {
		t.Fatalf("CreateAccount failed: %v", err)
	}

	if _, err := db.Exec(
		`UPDATE upstream_accounts
		 SET quota_5h_reset_at = ?, updated_at = ?
		 WHERE id = ?`,
		"2026-03-11 21:06:00", "2026-03-11 21:06:00", created.ID,
	); err != nil {
		t.Fatalf("seed legacy naive timestamps failed: %v", err)
	}

	if _, err := app.GetUpstreamAccounts(); err == nil {
		t.Fatal("expected unmigrated naive account time to fail closed")
	}
}

func TestGetUpstreamAccounts_DoesNotReportActiveSelectionAfterSameTierGroupMove(t *testing.T) {
	app, _ := newAccountPoolAPITestApp(t)
	ctx := context.Background()

	first, err := app.accountPoolService.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "primary-a",
		CredentialRaw: "sk-primary-a",
		BaseURL:       "https://api.openai.com",
		Priority:      10,
		Enabled:       true,
		State:         "active",
		QuotaStatus:   "ok",
	})
	if err != nil {
		t.Fatalf("CreateAccount first failed: %v", err)
	}

	second, err := app.accountPoolService.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "primary-b",
		CredentialRaw: "sk-primary-b",
		BaseURL:       "https://api.openai.com",
		Priority:      10,
		Enabled:       true,
		State:         "active",
		QuotaStatus:   "ok",
	})
	if err != nil {
		t.Fatalf("CreateAccount second failed: %v", err)
	}

	result, err := app.MoveUpstreamAccountToTier(second.ID, "primary")
	if err != nil {
		t.Fatalf("MoveUpstreamAccountToTier failed: %v", err)
	}
	if result.Changed {
		t.Fatalf("expected same-tier group move to stay in auto mode without manual selection change, got %+v", result)
	}

	accounts, err := app.GetUpstreamAccounts()
	if err != nil {
		t.Fatalf("GetUpstreamAccounts failed: %v", err)
	}
	if len(accounts) != 2 {
		t.Fatalf("expected 2 accounts, got %d", len(accounts))
	}

	var firstInfo *UpstreamAccountInfo
	var secondInfo *UpstreamAccountInfo
	for idx := range accounts {
		account := accounts[idx]
		switch account.ID {
		case first.ID:
			firstInfo = &account
		case second.ID:
			secondInfo = &account
		}
	}
	if firstInfo == nil || secondInfo == nil {
		t.Fatalf("expected both accounts in response, got %+v", accounts)
	}
	if firstInfo.IsActiveSelection || secondInfo.IsActiveSelection {
		t.Fatalf("expected same-tier group move not to enter manual active selection, got first=%+v second=%+v", firstInfo, secondInfo)
	}
}

func TestGetLatestAccountScheduleSnapshot_UsesAccountDisplayTimeZoneForCandidateTimes(t *testing.T) {
	app, _ := newAccountPoolAPITestApp(t)

	lastSuccessUTC := time.Date(2026, 3, 11, 13, 6, 0, 0, time.UTC)
	_, err := app.accountPoolService.CreateAccount(context.Background(), &store.UpstreamAccountRecord{
		ProviderType:                  "chatgpt_refresh_token",
		AccountName:                   "snapshot-candidate",
		CredentialRaw:                 `{"refresh_token":"rt-snapshot"}`,
		BaseURL:                       "https://api.openai.com",
		CostMultiplier:                1.0,
		InputCostMultiplier:           1.0,
		OutputCostMultiplier:          1.0,
		CacheCreationCostMultiplier:   1.0,
		CacheCreationCostMultiplier1h: 1.0,
		CacheReadCostMultiplier:       1.0,
		Priority:                      20,
		Enabled:                       true,
		State:                         "active",
		LastSuccessAt:                 &lastSuccessUTC,
		QuotaStatus:                   "ok",
		Quota5HUsedPercent:            float64Ptr(12),
	})
	if err != nil {
		t.Fatalf("CreateAccount failed: %v", err)
	}

	if _, err := app.accountPoolService.PrepareSchedulableAccounts(context.Background(), "req-1", "/v1/responses"); err != nil {
		t.Fatalf("PrepareSchedulableAccounts failed: %v", err)
	}

	snapshot, err := app.GetLatestAccountScheduleSnapshot()
	if err != nil {
		t.Fatalf("GetLatestAccountScheduleSnapshot failed: %v", err)
	}
	if len(snapshot.Candidates) == 0 {
		t.Fatal("expected snapshot candidates")
	}
	if got := snapshot.Candidates[0].LastSuccessAt; got != "2026-03-11T13:06:00.000000Z" {
		t.Fatalf("expected candidate last_success_at in UTC, got %q", got)
	}
}

func TestSwapUpstreamAccountGroups_ExchangesWholeGroups(t *testing.T) {
	app, _ := newAccountPoolAPITestApp(t)
	ctx := context.Background()

	if _, err := app.accountPoolService.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "primary-a",
		CredentialRaw: "sk-primary-a",
		BaseURL:       "https://api.openai.com",
		GroupKey:      "primary",
		Priority:      10,
		Enabled:       true,
		State:         "active",
		QuotaStatus:   "ok",
	}); err != nil {
		t.Fatalf("CreateAccount primary-a failed: %v", err)
	}
	backup, err := app.accountPoolService.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "backup-a",
		CredentialRaw: "sk-backup-a",
		BaseURL:       "https://api.openai.com",
		GroupKey:      "backup",
		Priority:      10,
		Enabled:       true,
		State:         "active",
		QuotaStatus:   "ok",
	})
	if err != nil {
		t.Fatalf("CreateAccount backup-a failed: %v", err)
	}

	result, err := app.SwapUpstreamAccountGroups("backup", "primary")
	if err != nil {
		t.Fatalf("SwapUpstreamAccountGroups failed: %v", err)
	}
	if !result.Changed {
		t.Fatalf("expected whole-group swap to report changed, got %+v", result)
	}

	accounts, err := app.GetUpstreamAccounts()
	if err != nil {
		t.Fatalf("GetUpstreamAccounts failed: %v", err)
	}

	var backupInfo *UpstreamAccountInfo
	for idx := range accounts {
		account := accounts[idx]
		if account.ID == backup.ID {
			backupInfo = &account
			break
		}
	}
	if backupInfo == nil {
		t.Fatalf("expected swapped backup account in response, got %+v", accounts)
	}
	if backupInfo.GroupKey != "primary" {
		t.Fatalf("expected backup-a to become primary after swap, got %+v", backupInfo)
	}
}

func TestSetGroupActiveAccount_PrefersChosenAccountWithinRequestedGroup(t *testing.T) {
	app, _ := newAccountPoolAPITestApp(t)
	ctx := context.Background()

	_, err := app.accountPoolService.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "primary-a",
		CredentialRaw: "sk-primary-a",
		BaseURL:       "https://api.openai.com",
		GroupKey:      "primary",
		Priority:      10,
		Enabled:       true,
		State:         "active",
		QuotaStatus:   "ok",
	})
	if err != nil {
		t.Fatalf("CreateAccount primary-a failed: %v", err)
	}
	primaryB, err := app.accountPoolService.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "primary-b",
		CredentialRaw: "sk-primary-b",
		BaseURL:       "https://api.openai.com",
		GroupKey:      "primary",
		Priority:      20,
		Enabled:       true,
		State:         "active",
		QuotaStatus:   "ok",
	})
	if err != nil {
		t.Fatalf("CreateAccount primary-b failed: %v", err)
	}

	result, err := app.SetGroupActiveAccount("primary", primaryB.ID)
	if err != nil {
		t.Fatalf("SetGroupActiveAccount failed: %v", err)
	}
	if !result.Changed {
		t.Fatalf("expected setting group preferred account to report changed, got %+v", result)
	}

	orderedSchedule, err := app.accountPoolService.PrepareSchedulableAccounts(ctx, "req-app-group-preferred", "/v1/responses")
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts failed: %v", err)
	}
	ordered := orderedSchedule.Accounts
	if len(ordered) == 0 || ordered[0] == nil || ordered[0].ID != primaryB.ID {
		t.Fatalf("expected chosen group account to lead selected group, got %+v want first=%d", ordered, primaryB.ID)
	}

	accounts, err := app.GetUpstreamAccounts()
	if err != nil {
		t.Fatalf("GetUpstreamAccounts failed: %v", err)
	}
	var primaryAPreferred, primaryBPreferred *UpstreamAccountInfo
	for idx := range accounts {
		account := accounts[idx]
		switch account.AccountName {
		case "primary-a":
			primaryAPreferred = &account
		case "primary-b":
			primaryBPreferred = &account
		}
	}
	if primaryAPreferred == nil || primaryBPreferred == nil {
		t.Fatalf("expected both primary accounts in response, got %+v", accounts)
	}
	if primaryAPreferred.IsGroupPreferred {
		t.Fatalf("expected primary-a not to be the preferred account, got %+v", primaryAPreferred)
	}
	if !primaryBPreferred.IsGroupPreferred {
		t.Fatalf("expected primary-b to be marked as group preferred, got %+v", primaryBPreferred)
	}
}

func TestMoveUpstreamAccountToTier_ReturnsNoChangeWhenTargetAlreadyInRequestedGroupUnderAutoMode(t *testing.T) {
	app, _ := newAccountPoolAPITestApp(t)
	ctx := context.Background()

	primary, err := app.accountPoolService.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "chatgpt_refresh_token",
		AccountName:   "primary-auto",
		CredentialRaw: `{"refresh_token":"rt-primary-auto"}`,
		BaseURL:       "https://api.openai.com",
		Priority:      10,
		Enabled:       true,
		State:         "active",
		QuotaStatus:   "ok",
	})
	if err != nil {
		t.Fatalf("CreateAccount primary failed: %v", err)
	}

	result, err := app.MoveUpstreamAccountToTier(primary.ID, "primary")
	if err != nil {
		t.Fatalf("MoveUpstreamAccountToTier failed: %v", err)
	}
	if result.Changed {
		t.Fatalf("expected no change when target is already in requested group under auto mode, got %+v", result)
	}
}

func TestMoveUpstreamAccountToTier_MessageReflectsSchedulableStatusWithoutLeavingAutoMode(t *testing.T) {
	app, _ := newAccountPoolAPITestApp(t)
	ctx := context.Background()

	_, err := app.accountPoolService.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "primary-active",
		CredentialRaw: "sk-primary-active",
		BaseURL:       "https://api.openai.com",
		Priority:      10,
		Enabled:       true,
		State:         "active",
		QuotaStatus:   "ok",
	})
	if err != nil {
		t.Fatalf("CreateAccount primary failed: %v", err)
	}
	backup, err := app.accountPoolService.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "backup-promote",
		CredentialRaw: "sk-backup-promote",
		BaseURL:       "https://api.openai.com",
		Priority:      20,
		Enabled:       true,
		State:         "active",
		QuotaStatus:   "ok",
	})
	if err != nil {
		t.Fatalf("CreateAccount backup failed: %v", err)
	}

	result, err := app.MoveUpstreamAccountToTier(backup.ID, "primary")
	if err != nil {
		t.Fatalf("MoveUpstreamAccountToTier failed: %v", err)
	}
	if !result.Changed {
		t.Fatalf("expected group reassignment to report changed, got %+v", result)
	}
	if !strings.Contains(result.Message, "当前可调度状态优先使用") || strings.Contains(result.Message, "立即生效") {
		t.Fatalf("expected API message to describe auto-mode schedulable semantics, got %q", result.Message)
	}

	accounts, err := app.GetUpstreamAccounts()
	if err != nil {
		t.Fatalf("GetUpstreamAccounts failed: %v", err)
	}
	var promotedBackupInfo *UpstreamAccountInfo
	for idx := range accounts {
		account := accounts[idx]
		if account.ID == backup.ID {
			promotedBackupInfo = &account
		}
	}
	if promotedBackupInfo == nil {
		t.Fatalf("expected promoted backup account in response, got %+v", accounts)
	}
	if promotedBackupInfo.IsActiveSelection {
		t.Fatalf("expected group move not to enable manual active selection, got %+v", promotedBackupInfo)
	}
}

func TestPinUpstreamAccountSelection_ChangesActiveSelectionWithoutChangingTier(t *testing.T) {
	app, _ := newAccountPoolAPITestApp(t)
	ctx := context.Background()

	primary, err := app.accountPoolService.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "primary-a",
		CredentialRaw: "sk-primary-a",
		BaseURL:       "https://api.openai.com",
		Priority:      10,
		Enabled:       true,
		State:         "active",
		QuotaStatus:   "ok",
	})
	if err != nil {
		t.Fatalf("CreateAccount primary failed: %v", err)
	}
	backup, err := app.accountPoolService.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "backup-a",
		CredentialRaw: "sk-backup-a",
		BaseURL:       "https://api.openai.com",
		Priority:      20,
		Enabled:       true,
		State:         "active",
		QuotaStatus:   "ok",
	})
	if err != nil {
		t.Fatalf("CreateAccount backup failed: %v", err)
	}

	result, err := app.PinUpstreamAccountSelection(backup.ID)
	if err != nil {
		t.Fatalf("PinUpstreamAccountSelection failed: %v", err)
	}
	if !result.Changed {
		t.Fatalf("expected pin selection to report changed, got %+v", result)
	}

	accounts, err := app.GetUpstreamAccounts()
	if err != nil {
		t.Fatalf("GetUpstreamAccounts failed: %v", err)
	}

	var primaryInfo *UpstreamAccountInfo
	var backupInfo *UpstreamAccountInfo
	for idx := range accounts {
		account := accounts[idx]
		switch account.ID {
		case primary.ID:
			primaryInfo = &account
		case backup.ID:
			backupInfo = &account
		}
	}
	if primaryInfo == nil || backupInfo == nil {
		t.Fatalf("expected both accounts in response, got %+v", accounts)
	}
	if primaryInfo.Priority != 10 || backupInfo.Priority != 20 {
		t.Fatalf("expected tier priorities unchanged, got primary=%+v backup=%+v", primaryInfo, backupInfo)
	}
	if primaryInfo.IsActiveSelection {
		t.Fatalf("expected primary not to stay active after pin, got %+v", primaryInfo)
	}
	if !backupInfo.IsActiveSelection {
		t.Fatalf("expected backup to become active selection after pin, got %+v", backupInfo)
	}
}

func TestPinUpstreamAccountSelection_RestoresPersistedSelectionAfterServiceRestart(t *testing.T) {
	app, db := newAccountPoolAPITestApp(t)
	ctx := context.Background()

	_, err := app.accountPoolService.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "primary-a",
		CredentialRaw: "sk-primary-a",
		BaseURL:       "https://api.openai.com",
		GroupKey:      "primary",
		Priority:      10,
		Enabled:       true,
		State:         "active",
		QuotaStatus:   "ok",
	})
	if err != nil {
		t.Fatalf("CreateAccount primary failed: %v", err)
	}
	backup, err := app.accountPoolService.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "backup-a",
		CredentialRaw: "sk-backup-a",
		BaseURL:       "https://api.openai.com",
		GroupKey:      "backup",
		Priority:      10,
		Enabled:       true,
		State:         "active",
		QuotaStatus:   "ok",
	})
	if err != nil {
		t.Fatalf("CreateAccount backup failed: %v", err)
	}

	result, err := app.PinUpstreamAccountSelection(backup.ID)
	if err != nil {
		t.Fatalf("PinUpstreamAccountSelection failed: %v", err)
	}
	if !result.Changed {
		t.Fatalf("expected pin selection to report changed, got %+v", result)
	}

	restarted := NewApp()
	restarted.accountPoolStore = store.NewSQLiteAccountPoolStore(db)
	restarted.settingsStore = store.NewSQLiteSettingsStore(db)
	restarted.settingsService = service.NewSettingsService(restarted.settingsStore)
	restarted.accountPoolService = service.NewAccountPoolService(restarted.accountPoolStore, nil)
	restarted.accountPoolService.SetSettingsService(restarted.settingsService)
	t.Cleanup(func() { _ = restarted.accountPoolService.Close() })

	accounts, err := restarted.GetUpstreamAccounts()
	if err != nil {
		t.Fatalf("GetUpstreamAccounts after restart failed: %v", err)
	}

	var backupInfo *UpstreamAccountInfo
	for idx := range accounts {
		account := accounts[idx]
		if account.ID == backup.ID {
			backupInfo = &account
			break
		}
	}
	if backupInfo == nil {
		t.Fatalf("expected backup account in response, got %+v", accounts)
	}
	if !backupInfo.IsActiveSelection {
		t.Fatalf("expected backup to restore active selection after restart, got %+v", backupInfo)
	}
}

func TestEnableAutomaticAccountSelection_ClearsManualPinnedSelection(t *testing.T) {
	app, _ := newAccountPoolAPITestApp(t)
	ctx := context.Background()

	_, err := app.accountPoolService.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "primary-a",
		CredentialRaw: "sk-primary-a",
		BaseURL:       "https://api.openai.com",
		GroupKey:      "primary",
		Priority:      10,
		Enabled:       true,
		State:         "active",
		QuotaStatus:   "ok",
	})
	if err != nil {
		t.Fatalf("CreateAccount primary failed: %v", err)
	}
	backup, err := app.accountPoolService.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "backup-a",
		CredentialRaw: "sk-backup-a",
		BaseURL:       "https://api.openai.com",
		GroupKey:      "backup",
		Priority:      10,
		Enabled:       true,
		State:         "active",
		QuotaStatus:   "ok",
	})
	if err != nil {
		t.Fatalf("CreateAccount backup failed: %v", err)
	}

	if _, err := app.PinUpstreamAccountSelection(backup.ID); err != nil {
		t.Fatalf("PinUpstreamAccountSelection failed: %v", err)
	}

	result, err := app.EnableAutomaticAccountSelection()
	if err != nil {
		t.Fatalf("EnableAutomaticAccountSelection failed: %v", err)
	}
	if !result.Changed {
		t.Fatalf("expected enabling auto selection to report changed, got %+v", result)
	}

	accounts, err := app.GetUpstreamAccounts()
	if err != nil {
		t.Fatalf("GetUpstreamAccounts failed: %v", err)
	}
	for _, account := range accounts {
		if account.IsActiveSelection {
			t.Fatalf("expected all accounts to leave manual pinned state, got %+v", accounts)
		}
	}
}

func TestMoveUpstreamAccountToTier_SupportsColdTier(t *testing.T) {
	app, _ := newAccountPoolAPITestApp(t)
	ctx := context.Background()

	_, err := app.accountPoolService.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "primary-a",
		CredentialRaw: "sk-primary-a",
		BaseURL:       "https://api.openai.com",
		Priority:      10,
		Enabled:       true,
		State:         "active",
		QuotaStatus:   "ok",
	})
	if err != nil {
		t.Fatalf("CreateAccount primary failed: %v", err)
	}
	backup, err := app.accountPoolService.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "backup-a",
		CredentialRaw: "sk-backup-a",
		BaseURL:       "https://api.openai.com",
		Priority:      20,
		Enabled:       true,
		State:         "active",
		QuotaStatus:   "ok",
	})
	if err != nil {
		t.Fatalf("CreateAccount backup failed: %v", err)
	}
	_, err = app.accountPoolService.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "cold-a",
		CredentialRaw: "sk-cold-a",
		BaseURL:       "https://api.openai.com",
		Priority:      30,
		Enabled:       true,
		State:         "active",
		QuotaStatus:   "ok",
	})
	if err != nil {
		t.Fatalf("CreateAccount cold failed: %v", err)
	}

	result, err := app.MoveUpstreamAccountToTier(backup.ID, "cold")
	if err != nil {
		t.Fatalf("MoveUpstreamAccountToTier cold failed: %v", err)
	}
	if !result.Changed {
		t.Fatalf("expected moving backup account to cold tier to report changed, got %+v", result)
	}
}

func TestFormatTime_NormalizesUTC(t *testing.T) {
	loc := time.FixedZone("UTC-7", -7*60*60)
	timestamp := time.Date(2026, 3, 11, 6, 30, 0, 0, loc)

	if got := formatTime(timestamp); got != "2026-03-11T13:30:00.000000Z" {
		t.Fatalf("expected UTC API time, got %q", got)
	}
}

func newAccountPoolAPITestApp(t *testing.T) (*App, *sql.DB) {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	schemaPath := filepath.Join("internal", "tracking", "schema.sql")
	schemaSQL, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read schema failed: %v", err)
	}
	if _, err := db.Exec(string(schemaSQL)); err != nil {
		t.Fatalf("exec schema failed: %v", err)
	}

	app := NewApp()
	app.accountPoolStore = store.NewSQLiteAccountPoolStore(db)
	app.settingsStore = store.NewSQLiteSettingsStore(db)
	app.settingsService = service.NewSettingsService(app.settingsStore)
	app.accountPoolService = service.NewAccountPoolService(app.accountPoolStore, nil)
	app.accountPoolService.SetSettingsService(app.settingsService)
	t.Cleanup(func() { _ = app.accountPoolService.Close() })
	return app, db
}

func float64Ptr(value float64) *float64 {
	return &value
}

func timePtr(value time.Time) *time.Time {
	return &value
}
