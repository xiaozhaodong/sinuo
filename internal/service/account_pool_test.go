package service

import (
	"compress/gzip"
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"sinuo/internal/accountauth"
	"sinuo/internal/store"

	_ "modernc.org/sqlite"
)

func TestTestUpstreamAccount_UsesResponsesEndpointAndTreats400AsReachable(t *testing.T) {
	var receivedMethod string
	var receivedPath string
	var receivedAuth string
	var receivedBody string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		receivedPath = r.URL.Path
		receivedAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		receivedBody = string(raw)

		http.Error(w, `{"error":"missing required parameter: model"}`, http.StatusBadRequest)
	}))
	defer server.Close()

	svc, accountID := newTestAccountPoolServiceForConnectivity(t, server.URL, "api_key", "sk-test")

	err := svc.TestUpstreamAccount(context.Background(), accountID)
	if err != nil {
		t.Fatalf("expected nil error for 400 reachability, got %v", err)
	}

	if receivedMethod != http.MethodPost {
		t.Fatalf("expected POST, got %s", receivedMethod)
	}
	if receivedPath != "/v1/responses" {
		t.Fatalf("expected /v1/responses, got %s", receivedPath)
	}
	if receivedAuth != "Bearer sk-test" {
		t.Fatalf("expected bearer auth, got %s", receivedAuth)
	}
	if receivedBody == "" {
		t.Fatal("expected non-empty request body")
	}

	account, err := svc.GetAccount(context.Background(), accountID)
	if err != nil {
		t.Fatalf("GetAccount failed: %v", err)
	}
	if account == nil || account.LastSuccessAt == nil || account.LastSuccessAt.IsZero() {
		t.Fatalf("expected last_success_at to be updated after connectivity test, got %+v", account)
	}
}

func TestTestUpstreamAccount_ReturnsPermissionErrorOn403(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"insufficient scope"}`, http.StatusForbidden)
	}))
	defer server.Close()

	svc, accountID := newTestAccountPoolServiceForConnectivity(t, server.URL, "api_key", "sk-test")

	err := svc.TestUpstreamAccount(context.Background(), accountID)
	if err == nil {
		t.Fatal("expected error for 403")
	}
	if got := err.Error(); got == "" || !strings.Contains(got, "鉴权或权限不足") || !strings.Contains(got, "403") {
		t.Fatalf("unexpected error: %s", got)
	}
}

func TestClearAccountCooldown_ClearsRuntimeAndPersistence(t *testing.T) {
	svc, accountID := newTestAccountPoolServiceForConnectivity(t, "https://example.invalid", "api_key", "sk-clear")
	ctx := context.Background()
	if err := svc.MarkAccountTransientFailure(ctx, accountID, "temporary", time.Minute); err != nil {
		t.Fatalf("MarkAccountTransientFailure failed: %v", err)
	}
	if err := svc.ClearAccountCooldown(ctx, accountID); err != nil {
		t.Fatalf("ClearAccountCooldown failed: %v", err)
	}
	account, err := svc.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatalf("GetAccount failed: %v", err)
	}
	if account.State != "active" || account.FailCount != 0 || account.CooldownUntil != nil || account.LastError != "" {
		t.Fatalf("expected runtime cooldown cleared, got %+v", account)
	}
}

func TestTestUpstreamAccount_MissingResponsesWriteScopeHint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"Missing scopes: api.responses.write"}}`, http.StatusUnauthorized)
	}))
	defer server.Close()

	svc, accountID := newTestAccountPoolServiceForConnectivity(t, server.URL, "api_key", "sk-test")

	err := svc.TestUpstreamAccount(context.Background(), accountID)
	if err == nil {
		t.Fatal("expected error for missing responses scope")
	}
	msg := err.Error()
	if !strings.Contains(msg, "缺少 api.responses.write") || !strings.Contains(msg, "重新走 OAuth 授权") {
		t.Fatalf("unexpected error message: %s", msg)
	}
}

func TestTestUpstreamAccount_Treats429AsReachable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"rate limited"}`, http.StatusTooManyRequests)
	}))
	defer server.Close()

	svc, accountID := newTestAccountPoolServiceForConnectivity(t, server.URL, "api_key", "sk-test")

	err := svc.TestUpstreamAccount(context.Background(), accountID)
	if err != nil {
		t.Fatalf("expected nil error for 429 reachability, got %v", err)
	}
}

func TestTestUpstreamAccount_Treats503NoAvailableProvidersAsReachable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"type":"no_available_providers","message":"no_available_providers"}}`, http.StatusServiceUnavailable)
	}))
	defer server.Close()

	svc, accountID := newTestAccountPoolServiceForConnectivity(t, server.URL, "api_key", "sk-test")

	err := svc.TestUpstreamAccount(context.Background(), accountID)
	if err != nil {
		t.Fatalf("expected nil error for 503 no_available_providers, got %v", err)
	}
}

func TestListSchedulableAccounts_V1ReturnsHighestAvailablePriorityTier(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	_, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:           "chatgpt_refresh_token",
		AccountName:            "tier-20-a",
		CredentialRaw:          "rt-a",
		Priority:               20,
		Enabled:                true,
		State:                  "active",
		FailCount:              9,
		QuotaStatus:            "exhausted",
		QuotaWeeklyUsedPercent: testFloat64Ptr(95),
	})
	if err != nil {
		t.Fatalf("create first account failed: %v", err)
	}

	second, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:           "api_key",
		AccountName:            "tier-10-main",
		CredentialRaw:          "sk-main",
		Priority:               10,
		Enabled:                true,
		State:                  "active",
		QuotaStatus:            "ok",
		QuotaWeeklyUsedPercent: testFloat64Ptr(5),
	})
	if err != nil {
		t.Fatalf("create second account failed: %v", err)
	}

	_, err = st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:           "chatgpt_refresh_token",
		AccountName:            "tier-20-b",
		CredentialRaw:          "rt-b",
		Priority:               20,
		Enabled:                true,
		State:                  "active",
		FailCount:              0,
		QuotaStatus:            "ok",
		QuotaWeeklyUsedPercent: testFloat64Ptr(10),
	})
	if err != nil {
		t.Fatalf("create third account failed: %v", err)
	}

	accounts, err := svc.ListSchedulableAccounts(ctx)
	if err != nil {
		t.Fatalf("ListSchedulableAccounts failed: %v", err)
	}

	gotIDs := collectAccountIDs(accounts)
	wantIDs := []int64{second.ID}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("unexpected manual failover order: got %v want %v", gotIDs, wantIDs)
	}
}

func TestPrepareSchedulableAccounts_DoesNotExposeManualSelectionForAutomaticChoice(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	first, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "first",
		CredentialRaw: "sk-first",
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create first account failed: %v", err)
	}
	second, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "second",
		CredentialRaw: "sk-second",
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create second account failed: %v", err)
	}

	orderedSchedule, err := svc.PrepareSchedulableAccounts(ctx, "req-auto-selection", "/v1/responses")
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts failed: %v", err)
	}
	if orderedSchedule.Pinned {
		t.Fatal("expected automatic scheduling result not to report pinned selection")
	}
	ordered := orderedSchedule.Accounts
	if got, want := collectAccountIDs(ordered), []int64{first.ID, second.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected automatic ranking: got %v want %v", got, want)
	}

	accountID, ok, err := svc.GetActiveSelectionAccountID(ctx)
	if err != nil {
		t.Fatalf("GetActiveSelectionAccountID failed: %v", err)
	}
	if ok {
		t.Fatalf("expected automatic scheduling not to set manual active selection, got account %d", accountID)
	}
}

func TestListSchedulableAccounts_V1FiltersDisabledAuthAndFutureCooldown(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()
	now := time.Now()

	main, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "main",
		CredentialRaw: "sk-main",
		GroupKey:      accountGroupPrimary,
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create main account failed: %v", err)
	}

	_, err = st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "disabled-auth",
		CredentialRaw: "sk-disabled",
		GroupKey:      accountGroupPrimary,
		Priority:      5,
		Enabled:       true,
		State:         "disabled_auth",
	})
	if err != nil {
		t.Fatalf("create disabled-auth account failed: %v", err)
	}

	_, err = st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "cooldown-future",
		CredentialRaw: "sk-future",
		GroupKey:      accountGroupPrimary,
		Priority:      1,
		Enabled:       true,
		State:         "cooldown",
		CooldownUntil: testTimePtr(now.Add(5 * time.Minute)),
	})
	if err != nil {
		t.Fatalf("create future cooldown account failed: %v", err)
	}

	_, err = st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "cooldown-past",
		CredentialRaw: "sk-past",
		GroupKey:      accountGroupBackup,
		Priority:      20,
		Enabled:       true,
		State:         "cooldown",
		CooldownUntil: testTimePtr(now.Add(-5 * time.Minute)),
	})
	if err != nil {
		t.Fatalf("create past cooldown account failed: %v", err)
	}

	_, err = st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "disabled",
		CredentialRaw: "sk-disabled-manual",
		GroupKey:      accountGroupBackup,
		Priority:      2,
		Enabled:       false,
		State:         "disabled",
	})
	if err != nil {
		t.Fatalf("create disabled account failed: %v", err)
	}

	accounts, err := svc.ListSchedulableAccounts(ctx)
	if err != nil {
		t.Fatalf("ListSchedulableAccounts failed: %v", err)
	}

	gotIDs := collectAccountIDs(accounts)
	wantIDs := []int64{main.ID}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("unexpected schedulable accounts: got %v want %v", gotIDs, wantIDs)
	}
}

func TestMoveAccountToTier_MovesAccountToPrimaryGroupWithoutShufflingOtherGroups(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	first, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "first",
		CredentialRaw: "sk-first",
		GroupKey:      accountGroupPrimary,
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create first account failed: %v", err)
	}
	second, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "second",
		CredentialRaw: "sk-second",
		GroupKey:      accountGroupBackup,
		Priority:      20,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create second account failed: %v", err)
	}
	third, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "third",
		CredentialRaw: "sk-third",
		GroupKey:      accountGroupCold,
		Priority:      30,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create third account failed: %v", err)
	}

	changed, err := svc.MoveAccountToTier(ctx, third.ID, 0)
	if err != nil {
		t.Fatalf("MoveAccountToTier failed: %v", err)
	}
	if !changed {
		t.Fatal("expected group move to change scheduling state")
	}

	accounts, err := st.ListAccounts(ctx, true)
	if err != nil {
		t.Fatalf("ListAccounts failed: %v", err)
	}

	got := collectAccountIDs(accounts)
	want := []int64{third.ID, first.ID, second.ID}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected reordered accounts: got %v want %v", got, want)
	}
	if accounts[0].GroupKey != accountGroupPrimary || accounts[0].Priority != 10 {
		t.Fatalf("expected moved account to become first primary candidate, got %+v", accounts[0])
	}
	if accounts[1].GroupKey != accountGroupPrimary || accounts[1].Priority != 20 {
		t.Fatalf("expected existing primary account to remain in primary group behind moved account, got %+v", accounts[1])
	}
	if accounts[2].GroupKey != accountGroupBackup || accounts[2].Priority != 10 {
		t.Fatalf("expected backup account to stay in backup group, got %+v", accounts[2])
	}
}

func TestSwapAccountGroups_ExchangesPrimaryAndBackupGroups(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	primaryA, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "primary-a",
		CredentialRaw: "sk-primary-a",
		GroupKey:      accountGroupPrimary,
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create primary-a failed: %v", err)
	}
	primaryB, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "primary-b",
		CredentialRaw: "sk-primary-b",
		GroupKey:      accountGroupPrimary,
		Priority:      20,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create primary-b failed: %v", err)
	}
	backupA, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "backup-a",
		CredentialRaw: "sk-backup-a",
		GroupKey:      accountGroupBackup,
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create backup-a failed: %v", err)
	}

	changed, err := svc.SwapAccountGroups(ctx, accountGroupBackup, accountGroupPrimary)
	if err != nil {
		t.Fatalf("SwapAccountGroups failed: %v", err)
	}
	if !changed {
		t.Fatal("expected swapping primary and backup groups to report changed")
	}

	accounts, err := st.ListAccounts(ctx, true)
	if err != nil {
		t.Fatalf("ListAccounts failed: %v", err)
	}
	if got, want := collectAccountIDs(accounts), []int64{backupA.ID, primaryA.ID, primaryB.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected group order after swap: got %v want %v", got, want)
	}
	if accounts[0].GroupKey != accountGroupPrimary || accounts[0].Priority != 10 {
		t.Fatalf("expected backup-a to become first primary account, got %+v", accounts[0])
	}
	if accounts[1].GroupKey != accountGroupBackup || accounts[1].Priority != 10 {
		t.Fatalf("expected primary-a to become first backup account, got %+v", accounts[1])
	}
	if accounts[2].GroupKey != accountGroupBackup || accounts[2].Priority != 20 {
		t.Fatalf("expected primary-b to remain second backup account, got %+v", accounts[2])
	}

	orderedSchedule, err := svc.PrepareSchedulableAccounts(ctx, "req-after-group-swap", "/v1/responses")
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts failed: %v", err)
	}
	ordered := orderedSchedule.Accounts
	if got, want := collectAccountIDs(ordered), []int64{backupA.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected promoted backup group to become primary selection, got %v want %v", got, want)
	}
}

func TestSwapAccountGroups_BackupAndColdPreservesCurrentPrimarySelection(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	primaryA, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "primary-a",
		CredentialRaw: "sk-primary-a",
		GroupKey:      accountGroupPrimary,
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create primary-a failed: %v", err)
	}
	backupA, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "backup-a",
		CredentialRaw: "sk-backup-a",
		GroupKey:      accountGroupBackup,
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create backup-a failed: %v", err)
	}
	coldA, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "cold-a",
		CredentialRaw: "sk-cold-a",
		GroupKey:      accountGroupCold,
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create cold-a failed: %v", err)
	}

	if _, err := svc.PrepareSchedulableAccounts(ctx, "req-before-backup-cold-swap", "/v1/responses"); err != nil {
		t.Fatalf("PrepareSchedulableAccounts before swap failed: %v", err)
	}

	changed, err := svc.SwapAccountGroups(ctx, accountGroupBackup, accountGroupCold)
	if err != nil {
		t.Fatalf("SwapAccountGroups backup/cold failed: %v", err)
	}
	if !changed {
		t.Fatal("expected swapping backup and cold groups to report changed")
	}

	accountID, ok, err := svc.GetActiveSelectionAccountID(ctx)
	if err != nil {
		t.Fatalf("GetActiveSelectionAccountID failed: %v", err)
	}
	if !ok || accountID != primaryA.ID {
		t.Fatalf("expected primary selection to be preserved, got ok=%v accountID=%d", ok, accountID)
	}

	accounts, err := st.ListAccounts(ctx, true)
	if err != nil {
		t.Fatalf("ListAccounts failed: %v", err)
	}
	if got, want := collectAccountIDs(accounts), []int64{primaryA.ID, coldA.ID, backupA.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected account order after backup/cold swap: got %v want %v", got, want)
	}
	if accounts[1].GroupKey != accountGroupBackup || accounts[2].GroupKey != accountGroupCold {
		t.Fatalf("expected backup/cold groups to exchange in place, got %+v", accounts)
	}
}

func TestSwapAccountGroups_BackupAndColdPreservesPreferredPrimaryAccount(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	primaryA, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "primary-a",
		CredentialRaw: "sk-primary-a",
		GroupKey:      accountGroupPrimary,
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create primary-a failed: %v", err)
	}
	primaryB, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "primary-b",
		CredentialRaw: "sk-primary-b",
		GroupKey:      accountGroupPrimary,
		Priority:      20,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create primary-b failed: %v", err)
	}
	backupA, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "backup-a",
		CredentialRaw: "sk-backup-a",
		GroupKey:      accountGroupBackup,
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create backup-a failed: %v", err)
	}
	coldA, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "cold-a",
		CredentialRaw: "sk-cold-a",
		GroupKey:      accountGroupCold,
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create cold-a failed: %v", err)
	}

	changed, err := svc.SetGroupActiveAccount(ctx, accountGroupPrimary, primaryB.ID)
	if err != nil {
		t.Fatalf("SetGroupActiveAccount primary failed: %v", err)
	}
	if !changed {
		t.Fatal("expected setting primary preferred account to report changed")
	}

	changed, err = svc.SwapAccountGroups(ctx, accountGroupBackup, accountGroupCold)
	if err != nil {
		t.Fatalf("SwapAccountGroups backup/cold failed: %v", err)
	}
	if !changed {
		t.Fatal("expected swapping backup and cold groups to report changed")
	}

	accountID, ok, err := svc.GetActiveSelectionAccountID(ctx)
	if err != nil {
		t.Fatalf("GetActiveSelectionAccountID failed: %v", err)
	}
	if !ok || accountID != primaryB.ID {
		t.Fatalf("expected preferred primary account to remain selected, got ok=%v accountID=%d", ok, accountID)
	}

	orderedSchedule, err := svc.PrepareSchedulableAccounts(ctx, "req-after-preserving-primary-preferred", "/v1/responses")
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts failed: %v", err)
	}
	ordered := orderedSchedule.Accounts
	if got, want := collectAccountIDs(ordered), []int64{primaryB.ID, primaryA.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected preferred primary account to stay ahead after swap, got %v want %v", got, want)
	}

	accounts, err := st.ListAccounts(ctx, true)
	if err != nil {
		t.Fatalf("ListAccounts failed: %v", err)
	}
	if got, want := collectAccountIDs(accounts), []int64{primaryA.ID, primaryB.ID, coldA.ID, backupA.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected account order after backup/cold swap with primary preference: got %v want %v", got, want)
	}
	if accounts[2].GroupKey != accountGroupBackup || accounts[3].GroupKey != accountGroupCold {
		t.Fatalf("expected backup/cold groups to exchange in place, got %+v", accounts)
	}
}

func TestSetGroupActiveAccount_PrefersChosenBackupAccountWithoutPinningRecoveredPrimary(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()
	now := time.Now()

	primaryA, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "primary-a",
		CredentialRaw: "sk-primary-a",
		GroupKey:      accountGroupPrimary,
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create primary-a failed: %v", err)
	}
	backupA, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "backup-a",
		CredentialRaw: "sk-backup-a",
		GroupKey:      accountGroupBackup,
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create backup-a failed: %v", err)
	}
	backupB, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "backup-b",
		CredentialRaw: "sk-backup-b",
		GroupKey:      accountGroupBackup,
		Priority:      20,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create backup-b failed: %v", err)
	}

	if err := svc.ensureRuntimeCache(ctx); err != nil {
		t.Fatalf("ensureRuntimeCache failed: %v", err)
	}
	if changed, _ := svc.runtimeCache.markTransientFailure(primaryA.ID, "primary cooldown", now.Add(30*time.Minute), now); !changed {
		t.Fatal("expected primary account to enter cooldown for test setup")
	}

	changed, err := svc.SetGroupActiveAccount(ctx, accountGroupBackup, backupB.ID)
	if err != nil {
		t.Fatalf("SetGroupActiveAccount failed: %v", err)
	}
	if !changed {
		t.Fatal("expected setting backup preferred account to report changed")
	}

	orderedSchedule, err := svc.PrepareSchedulableAccounts(ctx, "req-backup-preferred", "/v1/responses")
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts during primary cooldown failed: %v", err)
	}
	ordered := orderedSchedule.Accounts
	if got, want := collectAccountIDs(ordered), []int64{backupB.ID, backupA.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected chosen backup account to lead backup group, got %v want %v", got, want)
	}

	if changed, _ := svc.runtimeCache.markSuccess(primaryA.ID, now.Add(time.Minute)); !changed {
		t.Fatal("expected primary account recovery to update runtime state")
	}

	recoveredSchedule, err := svc.PrepareSchedulableAccounts(ctx, "req-primary-recovered", "/v1/responses")
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts after primary recovery failed: %v", err)
	}
	ordered = recoveredSchedule.Accounts
	if got, want := collectAccountIDs(ordered), []int64{primaryA.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected scheduler to return to primary group after recovery, got %v want %v", got, want)
	}
}

func TestSetGroupActiveAccount_PrefersChosenPrimaryAccountWithinPrimaryGroup(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	primaryA, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "primary-a",
		CredentialRaw: "sk-primary-a",
		GroupKey:      accountGroupPrimary,
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create primary-a failed: %v", err)
	}
	primaryB, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "primary-b",
		CredentialRaw: "sk-primary-b",
		GroupKey:      accountGroupPrimary,
		Priority:      20,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create primary-b failed: %v", err)
	}

	changed, err := svc.SetGroupActiveAccount(ctx, accountGroupPrimary, primaryB.ID)
	if err != nil {
		t.Fatalf("SetGroupActiveAccount primary failed: %v", err)
	}
	if !changed {
		t.Fatal("expected setting primary preferred account to report changed")
	}

	orderedSchedule, err := svc.PrepareSchedulableAccounts(ctx, "req-primary-preferred", "/v1/responses")
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts failed: %v", err)
	}
	ordered := orderedSchedule.Accounts
	if got, want := collectAccountIDs(ordered), []int64{primaryB.ID, primaryA.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected chosen primary account to lead primary group, got %v want %v", got, want)
	}
}

func TestMoveAccountToTier_NoChangeWhenTargetAlreadyInSameGroupUnderAutoMode(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	first, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "first",
		CredentialRaw: "sk-first",
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create first account failed: %v", err)
	}
	if _, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "second",
		CredentialRaw: "sk-second",
		Priority:      20,
		Enabled:       true,
		State:         "active",
	}); err != nil {
		t.Fatalf("create second account failed: %v", err)
	}

	changed, err := svc.MoveAccountToTier(ctx, first.ID, 0)
	if err != nil {
		t.Fatalf("MoveAccountToTier failed: %v", err)
	}
	if changed {
		t.Fatal("expected moving an already-primary account not to change auto-mode state")
	}

	changed, err = svc.MoveAccountToTier(ctx, first.ID, 0)
	if err != nil {
		t.Fatalf("second MoveAccountToTier failed: %v", err)
	}
	if changed {
		t.Fatal("expected no change after repeating the same group move")
	}
}

func TestMoveAccountToTier_ChangingPrimaryAccountWithinSameTierDoesNotCreateManualSelection(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	first, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "first",
		CredentialRaw: "sk-first",
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create first account failed: %v", err)
	}
	second, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "second",
		CredentialRaw: "sk-second",
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create second account failed: %v", err)
	}

	changed, err := svc.MoveAccountToTier(ctx, first.ID, 0)
	if err != nil {
		t.Fatalf("MoveAccountToTier first failed: %v", err)
	}
	if changed {
		t.Fatal("expected same-group move on first account not to create manual selection")
	}

	changed, err = svc.MoveAccountToTier(ctx, second.ID, 0)
	if err != nil {
		t.Fatalf("MoveAccountToTier second failed: %v", err)
	}
	if changed {
		t.Fatal("expected switching within the same primary group not to create manual selection")
	}

	orderedSchedule, err := svc.PrepareSchedulableAccounts(ctx, "req-same-tier-manual-switch", "/v1/responses")
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts failed: %v", err)
	}
	ordered := orderedSchedule.Accounts
	if got, want := collectAccountIDs(ordered), []int64{first.ID, second.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected automatic scheduling order to remain unchanged within the same primary group, got %v want %v", got, want)
	}

	accountID, ok, err := svc.GetActiveSelectionAccountID(ctx)
	if err != nil {
		t.Fatalf("GetActiveSelectionAccountID failed: %v", err)
	}
	if ok || accountID != 0 {
		t.Fatalf("expected no manual selection after same-group move, got ok=%v accountID=%d", ok, accountID)
	}
}

func TestUpdateAccount_PreservesPinnedSelectionWhenPinnedAccountPriorityChanges(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	first, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "first",
		CredentialRaw: "sk-first",
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create first account failed: %v", err)
	}
	if _, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "second",
		CredentialRaw: "sk-second",
		Priority:      20,
		Enabled:       true,
		State:         "active",
	}); err != nil {
		t.Fatalf("create second account failed: %v", err)
	}

	changed, err := svc.PinAccountSelection(ctx, first.ID)
	if err != nil {
		t.Fatalf("PinAccountSelection failed: %v", err)
	}
	if !changed {
		t.Fatal("expected pinning first account to change runtime state")
	}

	updatedFirst, err := svc.GetAccount(ctx, first.ID)
	if err != nil {
		t.Fatalf("GetAccount first failed: %v", err)
	}
	if updatedFirst == nil {
		t.Fatal("expected first account")
	}
	updatedFirst.Priority = 30
	if err := svc.UpdateAccount(ctx, updatedFirst); err != nil {
		t.Fatalf("UpdateAccount first failed: %v", err)
	}

	orderedSchedule, err := svc.PrepareSchedulableAccounts(ctx, "req-pinned-priority-edit", "/v1/responses")
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts failed: %v", err)
	}
	ordered := orderedSchedule.Accounts
	if got, want := collectAccountIDs(ordered), []int64{first.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected pinned account to remain selected after priority edit, got %v want %v", got, want)
	}

	accountID, ok, err := svc.GetActiveSelectionAccountID(ctx)
	if err != nil {
		t.Fatalf("GetActiveSelectionAccountID failed: %v", err)
	}
	if !ok || accountID != first.ID {
		t.Fatalf("expected pinned account to remain active selection, got ok=%v accountID=%d", ok, accountID)
	}
}

func TestUpdateAccount_PersistsModelRewriteRules(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	account, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:      "api_key",
		AccountName:       "anyrouter",
		CredentialRaw:     "sk-anyrouter",
		BaseURL:           "https://api.anyrouter.example",
		ModelRewriteRules: `[{"paths":["/v1/responses"],"match":"exact","from":"gpt-5.4","to":"gpt-5.5"}]`,
		Priority:          10,
		Enabled:           true,
		State:             "active",
	})
	if err != nil {
		t.Fatalf("create account failed: %v", err)
	}

	updated, err := svc.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetAccount failed: %v", err)
	}
	updated.ModelRewriteRules = `[{"paths":["/v1/responses","/v1/responses/compact"],"match":"exact","from":"gpt-5.4","to":"gpt-5.5"},{"paths":["/v1/responses","/v1/responses/compact"],"match":"exact","from":"gpt-5.4-mini","to":"gpt-5.5"}]`
	if err := svc.UpdateAccount(ctx, updated); err != nil {
		t.Fatalf("UpdateAccount failed: %v", err)
	}

	persisted, err := st.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetAccount from store failed: %v", err)
	}
	if persisted.ModelRewriteRules != updated.ModelRewriteRules {
		t.Fatalf("expected model rewrite rules to persist, got %s want %s", persisted.ModelRewriteRules, updated.ModelRewriteRules)
	}
}

func TestUpdateAccount_PersistsRequestCompressionSetting(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	account, err := st.CreateAccount(context.Background(), &store.UpstreamAccountRecord{
		ProviderType: "api_key", AccountName: "zstd-service", CredentialRaw: "sk-zstd-service", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create account failed: %v", err)
	}
	updated, err := svc.GetAccount(context.Background(), account.ID)
	if err != nil {
		t.Fatalf("get account failed: %v", err)
	}
	updated.EnableRequestCompression = true
	if err := svc.UpdateAccount(context.Background(), updated); err != nil {
		t.Fatalf("update account failed: %v", err)
	}
	persisted, err := st.GetAccount(context.Background(), account.ID)
	if err != nil {
		t.Fatalf("get persisted account failed: %v", err)
	}
	if !persisted.EnableRequestCompression {
		t.Fatalf("expected request compression setting to persist")
	}
}

func TestPinAccountSelection_PreservesPinnedTargetAcrossTransientCooldown(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	primary, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "primary",
		CredentialRaw: "sk-primary",
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create primary account failed: %v", err)
	}
	backup, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "backup",
		CredentialRaw: "sk-backup",
		Priority:      20,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create backup account failed: %v", err)
	}

	changed, err := svc.PinAccountSelection(ctx, primary.ID)
	if err != nil {
		t.Fatalf("PinAccountSelection primary failed: %v", err)
	}
	if !changed {
		t.Fatal("expected pinning primary account to change runtime state")
	}

	if err := svc.MarkAccountTransientFailure(ctx, primary.ID, "temporary upstream failure", 2*time.Minute); err != nil {
		t.Fatalf("MarkAccountTransientFailure primary failed: %v", err)
	}

	accountID, ok, err := svc.GetActiveSelectionAccountID(ctx)
	if err != nil {
		t.Fatalf("GetActiveSelectionAccountID failed: %v", err)
	}
	if !ok || accountID != primary.ID {
		t.Fatalf("expected manual selection to stay pinned on primary during cooldown, got ok=%v accountID=%d", ok, accountID)
	}

	orderedSchedule, err := svc.PrepareSchedulableAccounts(ctx, "req-manual-cooldown-degrade", "/v1/responses")
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts failed: %v", err)
	}
	ordered := orderedSchedule.Accounts
	if got, want := collectAccountIDs(ordered), []int64{backup.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected cooled-down primary to be skipped until cooldown ends, got %v want %v", got, want)
	}

	currentPrimary, err := svc.GetAccount(ctx, primary.ID)
	if err != nil {
		t.Fatalf("GetAccount primary failed: %v", err)
	}
	if currentPrimary == nil {
		t.Fatal("expected primary account")
	}
	if currentPrimary.State != "cooldown" || currentPrimary.CooldownUntil == nil || currentPrimary.FailCount != 1 {
		t.Fatalf("expected manual selection not to clear transient cooldown, got %+v", currentPrimary)
	}

	persistedPrimary, err := st.GetAccount(ctx, primary.ID)
	if err != nil {
		t.Fatalf("store GetAccount primary failed: %v", err)
	}
	if persistedPrimary == nil {
		t.Fatal("expected persisted primary account")
	}
	persistedPrimary.State = "cooldown"
	expiredAt := time.Now().Add(-time.Minute)
	persistedPrimary.CooldownUntil = &expiredAt
	persistedPrimary.FailCount = 1
	if err := st.UpdateAccount(ctx, persistedPrimary); err != nil {
		t.Fatalf("UpdateAccount primary cooldown expiry failed: %v", err)
	}
	if err := svc.reloadAccountIntoCache(ctx, primary.ID); err != nil {
		t.Fatalf("reloadAccountIntoCache primary failed: %v", err)
	}

	cooldownRecoveredSchedule, err := svc.PrepareSchedulableAccounts(ctx, "req-manual-cooldown-recovered", "/v1/responses")
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts after cooldown failed: %v", err)
	}
	ordered = cooldownRecoveredSchedule.Accounts
	if got, want := collectAccountIDs(ordered), []int64{primary.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected manual pinned primary to take effect after cooldown ends, got %v want %v", got, want)
	}

	currentBackup, err := svc.GetAccount(ctx, backup.ID)
	if err != nil {
		t.Fatalf("GetAccount backup failed: %v", err)
	}
	if currentBackup == nil {
		t.Fatal("expected backup account")
	}
}

func TestManualPinnedSelection_IsNotOverwrittenByLaterSuccessFromAnotherAccount(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	primary, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "primary",
		CredentialRaw: "sk-primary",
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create primary account failed: %v", err)
	}
	backup, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "backup",
		CredentialRaw: "sk-backup",
		Priority:      20,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create backup account failed: %v", err)
	}

	changed, err := svc.PinAccountSelection(ctx, primary.ID)
	if err != nil {
		t.Fatalf("PinAccountSelection primary failed: %v", err)
	}
	if !changed {
		t.Fatal("expected pinning primary account to change runtime state")
	}

	if err := svc.MarkAccountSuccess(ctx, backup.ID); err != nil {
		t.Fatalf("MarkAccountSuccess backup failed: %v", err)
	}

	accountID, ok, err := svc.GetActiveSelectionAccountID(ctx)
	if err != nil {
		t.Fatalf("GetActiveSelectionAccountID failed: %v", err)
	}
	if !ok || accountID != primary.ID {
		t.Fatalf("expected manual pinned selection to remain on primary, got ok=%v accountID=%d", ok, accountID)
	}

	orderedSchedule, err := svc.PrepareSchedulableAccounts(ctx, "req-manual-pin-not-overwritten", "/v1/responses")
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts failed: %v", err)
	}
	ordered := orderedSchedule.Accounts
	if got, want := collectAccountIDs(ordered), []int64{primary.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected manual pinned selection to remain effective after backup success, got %v want %v", got, want)
	}
}

func TestMoveAccountToTier_MovingSinglePrimaryAccountToBackupKeepsAutoMode(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	first, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "first",
		CredentialRaw: "sk-first",
		GroupKey:      accountGroupPrimary,
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create first account failed: %v", err)
	}
	second, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "second",
		CredentialRaw: "sk-second",
		GroupKey:      accountGroupPrimary,
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create second account failed: %v", err)
	}

	orderedSchedule, err := svc.PrepareSchedulableAccounts(ctx, "req-before-single-tier-backup", "/v1/responses")
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts before backup no-op failed: %v", err)
	}
	ordered := orderedSchedule.Accounts
	if got, want := collectAccountIDs(ordered), []int64{first.ID, second.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected baseline selected accounts: got %v want %v", got, want)
	}

	snapshotBefore, err := svc.GetLatestAccountScheduleSnapshot(ctx)
	if err != nil {
		t.Fatalf("GetLatestAccountScheduleSnapshot before backup no-op failed: %v", err)
	}
	if snapshotBefore == nil {
		t.Fatal("expected existing snapshot before backup no-op")
	}

	changed, err := svc.MoveAccountToTier(ctx, second.ID, 1)
	if err != nil {
		t.Fatalf("MoveAccountToTier backup move failed: %v", err)
	}
	if !changed {
		t.Fatal("expected moving one primary account into backup to change group assignment")
	}

	snapshotAfter, err := svc.GetLatestAccountScheduleSnapshot(ctx)
	if err != nil {
		t.Fatalf("GetLatestAccountScheduleSnapshot after backup no-op failed: %v", err)
	}
	if snapshotAfter != nil {
		t.Fatalf("expected latest snapshot to be cleared after explicit backup move, got before=%+v after=%+v", snapshotBefore, snapshotAfter)
	}

	afterBackupSchedule, err := svc.PrepareSchedulableAccounts(ctx, "req-after-single-tier-backup", "/v1/responses")
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts after backup move failed: %v", err)
	}
	ordered = afterBackupSchedule.Accounts
	if got, want := collectAccountIDs(ordered), []int64{first.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected auto scheduling to remain on primary group after moving another account to backup, got %v want %v", got, want)
	}
	accountID, ok, err := svc.GetActiveSelectionAccountID(ctx)
	if err != nil {
		t.Fatalf("GetActiveSelectionAccountID failed: %v", err)
	}
	if ok || accountID != 0 {
		t.Fatalf("expected backup move not to enter manual mode, got ok=%v accountID=%d", ok, accountID)
	}
}

func TestMoveAccountToTier_MovingToBackupKeepsOtherColdAccountsInPlaceWithoutLeavingAutoMode(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	first, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "first",
		CredentialRaw: "sk-first",
		GroupKey:      accountGroupPrimary,
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create first account failed: %v", err)
	}
	second, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "second",
		CredentialRaw: "sk-second",
		GroupKey:      accountGroupBackup,
		Priority:      20,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create second account failed: %v", err)
	}
	third, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "third",
		CredentialRaw: "sk-third",
		GroupKey:      accountGroupCold,
		Priority:      30,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create third account failed: %v", err)
	}
	thirdPeer, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "third-peer",
		CredentialRaw: "sk-third-peer",
		GroupKey:      accountGroupCold,
		Priority:      30,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create third peer account failed: %v", err)
	}

	changed, err := svc.MoveAccountToTier(ctx, third.ID, 1)
	if err != nil {
		t.Fatalf("MoveAccountToTier failed: %v", err)
	}
	if !changed {
		t.Fatal("expected moving third account into backup group to change scheduling state")
	}

	orderedSchedule, err := svc.PrepareSchedulableAccounts(ctx, "req-after-backup-move", "/v1/responses")
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts failed: %v", err)
	}
	ordered := orderedSchedule.Accounts
	if got, want := collectAccountIDs(ordered), []int64{first.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected auto scheduling to remain on primary group after moving a cold account to backup, got %v want %v", got, want)
	}
	accountID, ok, err := svc.GetActiveSelectionAccountID(ctx)
	if err != nil {
		t.Fatalf("GetActiveSelectionAccountID failed: %v", err)
	}
	if ok || accountID != 0 {
		t.Fatalf("expected backup move not to create manual selection, got ok=%v accountID=%d", ok, accountID)
	}

	recordThird, err := st.GetAccount(ctx, third.ID)
	if err != nil {
		t.Fatalf("GetAccount third failed: %v", err)
	}
	if recordThird == nil || recordThird.GroupKey != accountGroupBackup || recordThird.Priority != 10 {
		t.Fatalf("expected third account to become first backup account, got %+v", recordThird)
	}
	recordSecond, err := st.GetAccount(ctx, second.ID)
	if err != nil {
		t.Fatalf("GetAccount second failed: %v", err)
	}
	if recordSecond == nil || recordSecond.GroupKey != accountGroupBackup || recordSecond.Priority != 20 {
		t.Fatalf("expected second account to remain backup behind moved account, got %+v", recordSecond)
	}
	recordThirdPeer, err := st.GetAccount(ctx, thirdPeer.ID)
	if err != nil {
		t.Fatalf("GetAccount third peer failed: %v", err)
	}
	if recordThirdPeer == nil || recordThirdPeer.GroupKey != accountGroupCold {
		t.Fatalf("expected untouched cold account to stay cold, got %+v", recordThirdPeer)
	}

	accounts, err := st.ListAccounts(ctx, true)
	if err != nil {
		t.Fatalf("ListAccounts failed: %v", err)
	}
	if got, want := collectAccountIDs(accounts), []int64{first.ID, third.ID, second.ID, thirdPeer.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected reordered accounts after moving to backup: got %v want %v", got, want)
	}
}

func TestMoveAccountToTier_ChangingBackupAccountWithinSameTierDoesNotCreateManualSelection(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	primary, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "primary",
		CredentialRaw: "sk-primary",
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create primary account failed: %v", err)
	}
	backupA, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "backup-a",
		CredentialRaw: "sk-backup-a",
		Priority:      20,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create backup-a failed: %v", err)
	}
	backupB, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "backup-b",
		CredentialRaw: "sk-backup-b",
		Priority:      20,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create backup-b failed: %v", err)
	}

	changed, err := svc.MoveAccountToTier(ctx, backupA.ID, 1)
	if err != nil {
		t.Fatalf("MoveAccountToTier backup-a failed: %v", err)
	}
	if changed {
		t.Fatal("expected moving an already-backup account within the same group not to create manual selection")
	}

	changed, err = svc.MoveAccountToTier(ctx, backupB.ID, 1)
	if err != nil {
		t.Fatalf("MoveAccountToTier backup-b failed: %v", err)
	}
	if changed {
		t.Fatal("expected switching within the same backup group not to create manual selection")
	}

	orderedSchedule, err := svc.PrepareSchedulableAccounts(ctx, "req-same-tier-backup-switch", "/v1/responses")
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts failed: %v", err)
	}
	ordered := orderedSchedule.Accounts
	if got, want := collectAccountIDs(ordered), []int64{primary.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected auto mode to keep selecting the primary group, got %v want %v", got, want)
	}
	accountID, ok, err := svc.GetActiveSelectionAccountID(ctx)
	if err != nil {
		t.Fatalf("GetActiveSelectionAccountID failed: %v", err)
	}
	if ok || accountID != 0 {
		t.Fatalf("expected no manual selection after backup group move, got ok=%v accountID=%d", ok, accountID)
	}
}

func TestMoveAccountToTier_PreservesExistingManualPinnedSelectionWhenReorderingGroups(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	primary, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "primary-a",
		CredentialRaw: "sk-primary-a",
		GroupKey:      accountGroupPrimary,
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create primary failed: %v", err)
	}
	backup, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "backup-a",
		CredentialRaw: "sk-backup-a",
		GroupKey:      accountGroupBackup,
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create backup failed: %v", err)
	}
	cold, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "cold-a",
		CredentialRaw: "sk-cold-a",
		GroupKey:      accountGroupCold,
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create cold failed: %v", err)
	}

	if _, err := svc.PinAccountSelection(ctx, backup.ID); err != nil {
		t.Fatalf("PinAccountSelection backup failed: %v", err)
	}
	changed, err := svc.MoveAccountToTier(ctx, cold.ID, 1)
	if err != nil {
		t.Fatalf("MoveAccountToTier cold->backup failed: %v", err)
	}
	if !changed {
		t.Fatal("expected moving cold account into backup group to change scheduling state")
	}

	accountID, ok, err := svc.GetActiveSelectionAccountID(ctx)
	if err != nil {
		t.Fatalf("GetActiveSelectionAccountID failed: %v", err)
	}
	if !ok || accountID != backup.ID {
		t.Fatalf("expected existing manual pin to be preserved, got ok=%v accountID=%d", ok, accountID)
	}

	orderedSchedule, err := svc.PrepareSchedulableAccounts(ctx, "req-preserve-existing-manual-pin", "/v1/responses")
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts failed: %v", err)
	}
	ordered := orderedSchedule.Accounts
	if got, want := collectAccountIDs(ordered), []int64{backup.ID, cold.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected existing manual pin to remain effective after group move while still exposing same-group fallbacks, got %v want %v", got, want)
	}

	_ = primary
}

func TestPinAccountSelection_PinsSpecificAccountWithoutChangingTierPriorities(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	primary, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "primary",
		CredentialRaw: "sk-primary",
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create primary account failed: %v", err)
	}
	backup, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "backup",
		CredentialRaw: "sk-backup",
		Priority:      20,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create backup account failed: %v", err)
	}

	changed, err := svc.PinAccountSelection(ctx, backup.ID)
	if err != nil {
		t.Fatalf("PinAccountSelection failed: %v", err)
	}
	if !changed {
		t.Fatal("expected pinning backup account to change runtime selection")
	}

	orderedSchedule, err := svc.PrepareSchedulableAccounts(ctx, "req-pinned-backup", "/v1/responses")
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts failed: %v", err)
	}
	if !orderedSchedule.Pinned {
		t.Fatal("expected schedule result to report pinned selection after PinAccountSelection")
	}
	ordered := orderedSchedule.Accounts
	if got, want := collectAccountIDs(ordered), []int64{backup.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected pinned backup account to be selected before higher priority tier, got %v want %v", got, want)
	}

	for accountID, wantPriority := range map[int64]int{
		primary.ID: 10,
		backup.ID:  20,
	} {
		record, getErr := st.GetAccount(ctx, accountID)
		if getErr != nil {
			t.Fatalf("GetAccount %d failed: %v", accountID, getErr)
		}
		if record == nil || record.Priority != wantPriority {
			t.Fatalf("expected priority %d for account %d, got %+v", wantPriority, accountID, record)
		}
	}
}

func TestPinAccountSelection_RestoresPinnedAccountAfterServiceRestart(t *testing.T) {
	svc, st, settingsSvc := newTestAccountPoolServiceWithSettings(t)
	ctx := context.Background()

	if _, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "primary-a",
		CredentialRaw: "sk-primary-a",
		GroupKey:      accountGroupPrimary,
		Priority:      10,
		Enabled:       true,
		State:         "active",
	}); err != nil {
		t.Fatalf("create primary failed: %v", err)
	}
	backup, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "backup-a",
		CredentialRaw: "sk-backup-a",
		GroupKey:      accountGroupBackup,
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create backup failed: %v", err)
	}

	if _, err := svc.PinAccountSelection(ctx, backup.ID); err != nil {
		t.Fatalf("PinAccountSelection failed: %v", err)
	}
	value, err := settingsSvc.GetValue(ctx, accountPoolSettingsCategory, accountPoolPinnedAccountIDSettingKey)
	if err != nil {
		t.Fatalf("GetValue failed: %v", err)
	}
	if value != "2" {
		t.Fatalf("expected pinned account setting to be persisted as 2, got %q", value)
	}

	restarted := NewAccountPoolService(st, nil)
	restarted.SetSettingsService(settingsSvc)
	t.Cleanup(func() { _ = restarted.Close() })

	accountID, ok, err := restarted.GetActiveSelectionAccountID(ctx)
	if err != nil {
		t.Fatalf("GetActiveSelectionAccountID after restart failed: %v", err)
	}
	if !ok || accountID != backup.ID {
		t.Fatalf("expected restarted service to restore pinned backup, got ok=%v accountID=%d", ok, accountID)
	}
}

func TestPinAccountSelection_DoesNotRestoreUnavailablePinnedAccountAfterServiceRestart(t *testing.T) {
	svc, st, settingsSvc := newTestAccountPoolServiceWithSettings(t)
	ctx := context.Background()
	now := time.Now()

	if _, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "primary-a",
		CredentialRaw: "sk-primary-a",
		GroupKey:      accountGroupPrimary,
		Priority:      10,
		Enabled:       true,
		State:         "active",
	}); err != nil {
		t.Fatalf("create primary failed: %v", err)
	}
	backup, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "backup-a",
		CredentialRaw: "sk-backup-a",
		GroupKey:      accountGroupBackup,
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create backup failed: %v", err)
	}

	if _, err := svc.PinAccountSelection(ctx, backup.ID); err != nil {
		t.Fatalf("PinAccountSelection failed: %v", err)
	}
	if err := st.MarkAccountTransientFailure(ctx, backup.ID, "temporary cooldown", now.Add(5*time.Minute)); err != nil {
		t.Fatalf("MarkAccountTransientFailure failed: %v", err)
	}

	restarted := NewAccountPoolService(st, nil)
	restarted.SetSettingsService(settingsSvc)
	t.Cleanup(func() { _ = restarted.Close() })

	accountID, ok, err := restarted.GetActiveSelectionAccountID(ctx)
	if err != nil {
		t.Fatalf("GetActiveSelectionAccountID after restart failed: %v", err)
	}
	if ok || accountID != 0 {
		t.Fatalf("expected unavailable pinned account not to be restored, got ok=%v accountID=%d", ok, accountID)
	}
	value, err := settingsSvc.GetValue(ctx, accountPoolSettingsCategory, accountPoolPinnedAccountIDSettingKey)
	if err != nil {
		t.Fatalf("GetValue failed: %v", err)
	}
	if value != "" {
		t.Fatalf("expected unavailable pinned account setting to be cleared, got %q", value)
	}
}

func TestPinAccountSelection_ClearsPersistedPinnedAccountWhenTargetDeletedAfterServiceRestart(t *testing.T) {
	svc, st, settingsSvc := newTestAccountPoolServiceWithSettings(t)
	ctx := context.Background()

	if _, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "primary-a",
		CredentialRaw: "sk-primary-a",
		GroupKey:      accountGroupPrimary,
		Priority:      10,
		Enabled:       true,
		State:         "active",
	}); err != nil {
		t.Fatalf("create primary failed: %v", err)
	}
	backup, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "backup-a",
		CredentialRaw: "sk-backup-a",
		GroupKey:      accountGroupBackup,
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create backup failed: %v", err)
	}

	if _, err := svc.PinAccountSelection(ctx, backup.ID); err != nil {
		t.Fatalf("PinAccountSelection failed: %v", err)
	}
	if err := st.DeleteAccount(ctx, backup.ID); err != nil {
		t.Fatalf("DeleteAccount failed: %v", err)
	}

	restarted := NewAccountPoolService(st, nil)
	restarted.SetSettingsService(settingsSvc)
	t.Cleanup(func() { _ = restarted.Close() })

	accountID, ok, err := restarted.GetActiveSelectionAccountID(ctx)
	if err != nil {
		t.Fatalf("GetActiveSelectionAccountID after restart failed: %v", err)
	}
	if ok || accountID != 0 {
		t.Fatalf("expected deleted pinned account not to be restored, got ok=%v accountID=%d", ok, accountID)
	}
	value, err := settingsSvc.GetValue(ctx, accountPoolSettingsCategory, accountPoolPinnedAccountIDSettingKey)
	if err != nil {
		t.Fatalf("GetValue failed: %v", err)
	}
	if value != "" {
		t.Fatalf("expected deleted pinned account setting to be cleared, got %q", value)
	}
}

func TestEnableAutomaticAccountSelection_ClearsPinnedSelectionAndPersistedSetting(t *testing.T) {
	svc, st, settingsSvc := newTestAccountPoolServiceWithSettings(t)
	ctx := context.Background()

	_, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "primary-a",
		CredentialRaw: "sk-primary-a",
		GroupKey:      accountGroupPrimary,
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create primary failed: %v", err)
	}
	backup, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "backup-a",
		CredentialRaw: "sk-backup-a",
		GroupKey:      accountGroupBackup,
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create backup failed: %v", err)
	}

	if _, err := svc.PinAccountSelection(ctx, backup.ID); err != nil {
		t.Fatalf("PinAccountSelection failed: %v", err)
	}

	changed, err := svc.EnableAutomaticAccountSelection(ctx)
	if err != nil {
		t.Fatalf("EnableAutomaticAccountSelection failed: %v", err)
	}
	if !changed {
		t.Fatal("expected enabling automatic account selection to report changed")
	}

	accountID, ok, err := svc.GetActiveSelectionAccountID(ctx)
	if err != nil {
		t.Fatalf("GetActiveSelectionAccountID failed: %v", err)
	}
	if ok || accountID != 0 {
		t.Fatalf("expected manual pin to be cleared, got ok=%v accountID=%d", ok, accountID)
	}

	value, err := settingsSvc.GetValue(ctx, accountPoolSettingsCategory, accountPoolPinnedAccountIDSettingKey)
	if err != nil {
		t.Fatalf("GetValue failed: %v", err)
	}
	if value != "" {
		t.Fatalf("expected pinned account setting to be cleared, got %q", value)
	}
}

func collectAccountIDs(accounts []*store.UpstreamAccountRecord) []int64 {
	ids := make([]int64, 0, len(accounts))
	for _, account := range accounts {
		if account == nil {
			continue
		}
		ids = append(ids, account.ID)
	}
	return ids
}

func testFloat64Ptr(value float64) *float64 {
	return &value
}

func testTimePtr(value time.Time) *time.Time {
	return &value
}

func newTestAccountPoolServiceForConnectivity(t *testing.T, baseURL, providerType, credential string) (*AccountPoolService, int64) {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	schemaPath := filepath.Join("..", "tracking", "schema.sql")
	schemaSQL, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read schema failed: %v", err)
	}
	if _, err := db.Exec(string(schemaSQL)); err != nil {
		t.Fatalf("exec schema failed: %v", err)
	}

	st := store.NewSQLiteAccountPoolStore(db)
	rec, err := st.CreateAccount(context.Background(), &store.UpstreamAccountRecord{
		ProviderType:  providerType,
		AccountName:   "acc-test",
		CredentialRaw: credential,
		BaseURL:       baseURL,
		Priority:      1,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create account failed: %v", err)
	}

	svc := NewAccountPoolService(st, nil)
	t.Cleanup(func() { _ = svc.Close() })
	return svc, rec.ID
}

func newTestAccountPoolServiceWithStore(t *testing.T) (*AccountPoolService, *store.SQLiteAccountPoolStore) {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	schemaPath := filepath.Join("..", "tracking", "schema.sql")
	schemaSQL, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read schema failed: %v", err)
	}
	if _, err := db.Exec(string(schemaSQL)); err != nil {
		t.Fatalf("exec schema failed: %v", err)
	}

	st := store.NewSQLiteAccountPoolStore(db)
	svc := NewAccountPoolService(st, nil)
	t.Cleanup(func() { _ = svc.Close() })
	return svc, st
}

func TestDeleteAccountWithModelGuard_ReportsAccountNameAndRoutingHint(t *testing.T) {
	ctx := context.Background()
	svc, st := newTestAccountPoolServiceWithStore(t)

	rec, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "venlacy",
		CredentialRaw: "sk-test",
		Priority:      1,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create account failed: %v", err)
	}

	modelsUsing := func(context.Context, int64) ([]string, error) { return []string{"gpt-6-astra"}, nil }

	guardErr := svc.DeleteAccountWithModelGuard(ctx, rec.ID, modelsUsing)
	if guardErr == nil {
		t.Fatal("expected delete to be blocked while the account is still bound to a Codex model")
	}
	for _, want := range []string{"账号「venlacy」", "gpt-6-astra", "「模型路由」"} {
		if !strings.Contains(guardErr.Error(), want) {
			t.Fatalf("guard message %q missing %q", guardErr.Error(), want)
		}
	}
	if acc, err := svc.GetAccount(ctx, rec.ID); err != nil || acc == nil {
		t.Fatalf("blocked delete must keep the account: acc=%+v err=%v", acc, err)
	}

	noRefs := func(context.Context, int64) ([]string, error) { return nil, nil }
	if err := svc.DeleteAccountWithModelGuard(ctx, rec.ID, noRefs); err != nil {
		t.Fatalf("expected delete to succeed after unbinding, got %v", err)
	}

	// 账号已不存在时退回 ID 文案，保证守卫提示不会变空
	ghostErr := svc.DeleteAccountWithModelGuard(ctx, rec.ID, modelsUsing)
	if ghostErr == nil || !strings.Contains(ghostErr.Error(), "账号 ") {
		t.Fatalf("expected id fallback in guard message, got %v", ghostErr)
	}
}

func newTestAccountPoolServiceWithSettings(t *testing.T) (*AccountPoolService, *store.SQLiteAccountPoolStore, *SettingsService) {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	schemaPath := filepath.Join("..", "tracking", "schema.sql")
	schemaSQL, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read schema failed: %v", err)
	}
	if _, err := db.Exec(string(schemaSQL)); err != nil {
		t.Fatalf("exec schema failed: %v", err)
	}

	accountStore := store.NewSQLiteAccountPoolStore(db)
	settingsStore := store.NewSQLiteSettingsStore(db)
	settingsSvc := NewSettingsService(settingsStore)
	svc := NewAccountPoolService(accountStore, nil)
	svc.SetSettingsService(settingsSvc)
	t.Cleanup(func() { _ = svc.Close() })
	return svc, accountStore, settingsSvc
}

// API Key 账号走标准 Responses 契约，连通性测试不得套用 Codex 首方 UA。
func TestTestUpstreamAccount_APIKeyDoesNotUseCodexUserAgent(t *testing.T) {
	var receivedUserAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedUserAgent = r.Header.Get("User-Agent")
		http.Error(w, `{"error":"missing required parameter: model"}`, http.StatusBadRequest)
	}))
	defer server.Close()

	svc, accountID := newTestAccountPoolServiceForConnectivity(t, server.URL, "api_key", "sk-test")
	if err := svc.TestUpstreamAccount(context.Background(), accountID); err != nil {
		t.Fatalf("TestUpstreamAccount failed: %v", err)
	}
	if strings.HasPrefix(receivedUserAgent, accountauth.CodexCompatOriginator+"/") {
		t.Fatalf("API Key connectivity test must not impersonate Codex first-party client, got %q", receivedUserAgent)
	}
}

// 关闭自动 gzip 后，上游主动压缩的 503 no_available_providers 正文必须先解码再判定。
func TestTestUpstreamAccount_DecodesGzipNoAvailableProvidersBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusServiceUnavailable)
		writer := gzip.NewWriter(w)
		_, _ = writer.Write([]byte(`{"error":{"type":"no_available_providers","message":"no_available_providers"}}`))
		_ = writer.Close()
	}))
	defer server.Close()

	svc, accountID := newTestAccountPoolServiceForConnectivity(t, server.URL, "api_key", "sk-test")
	if err := svc.TestUpstreamAccount(context.Background(), accountID); err != nil {
		t.Fatalf("expected nil error for gzip 503 no_available_providers, got %v", err)
	}
}
