package service

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"sinuo/internal/store"
)

func TestPrepareSchedulableAccounts_SelectsHighestPriorityTierOnly(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	mainAPI, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "main-api",
		CredentialRaw: "sk-main-api",
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create main api account failed: %v", err)
	}

	mainOAuth, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:           "chatgpt_refresh_token",
		AccountName:            "main-oauth",
		CredentialRaw:          "rt-main-oauth",
		Priority:               10,
		Enabled:                true,
		State:                  "active",
		QuotaStatus:            "ok",
		QuotaWeeklyUsedPercent: testFloat64Ptr(40),
	})
	if err != nil {
		t.Fatalf("create main oauth account failed: %v", err)
	}

	backup, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "backup-api",
		CredentialRaw: "sk-backup-api",
		Priority:      20,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create backup account failed: %v", err)
	}

	accountsSchedule, err := svc.PrepareSchedulableAccounts(ctx, "req-select-main", "/v1/responses")
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts failed: %v", err)
	}
	accounts := accountsSchedule.Accounts
	if got, want := collectAccountIDs(accounts), []int64{mainOAuth.ID, mainAPI.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected selected tier accounts: got %v want %v", got, want)
	}

	snapshot, err := svc.GetLatestAccountScheduleSnapshot(ctx)
	if err != nil {
		t.Fatalf("GetLatestAccountScheduleSnapshot failed: %v", err)
	}
	if snapshot == nil {
		t.Fatal("expected latest snapshot")
	}
	if snapshot.SelectedPriority != 10 || snapshot.SelectedTierIndex != 1 || snapshot.SelectedTierLabel != "主组" {
		t.Fatalf("unexpected selected tier info: %+v", snapshot)
	}
	if snapshot.DegradedToLowerPriority {
		t.Fatalf("expected no degrade, got %+v", snapshot)
	}
	candidate := mustFindCandidateDecision(t, snapshot, backup.ID)
	if candidate.Decision != accountScheduleDecisionSkipped || candidate.Reason != "higher_priority_tier_selected" {
		t.Fatalf("expected lower tier skipped by higher priority, got %+v", candidate)
	}
}

func TestPrepareSchedulableAccounts_StandaloneSearchUsesOAuthAndPreservesPinnedAPIKey(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	oauth, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:           "chatgpt_refresh_token",
		AccountName:            "search-oauth",
		CredentialRaw:          `{"refresh_token":"rt-search","chatgpt_account_id":"acc-search"}`,
		Priority:               10,
		Enabled:                true,
		State:                  "active",
		QuotaStatus:            "ok",
		QuotaWeeklyUsedPercent: testFloat64Ptr(20),
	})
	if err != nil {
		t.Fatalf("create oauth account failed: %v", err)
	}

	apiKey, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "pinned-api-key",
		CredentialRaw: "sk-third-party",
		Priority:      20,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create api key account failed: %v", err)
	}
	sameTierOAuth, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:           "chatgpt_refresh_token",
		AccountName:            "backup-search-oauth",
		CredentialRaw:          `{"refresh_token":"rt-backup-search","chatgpt_account_id":"acc-backup-search"}`,
		Priority:               20,
		Enabled:                true,
		State:                  "active",
		QuotaStatus:            "ok",
		QuotaWeeklyUsedPercent: testFloat64Ptr(10),
	})
	if err != nil {
		t.Fatalf("create same-tier oauth account failed: %v", err)
	}

	changed, err := svc.PinAccountSelection(ctx, apiKey.ID)
	if err != nil {
		t.Fatalf("PinAccountSelection failed: %v", err)
	}
	if !changed {
		t.Fatal("expected api key pin to change runtime selection")
	}

	searchSchedule, err := svc.PrepareSchedulableAccounts(ctx, "req-search-oauth-only", CodexStandaloneSearchPath)
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts search failed: %v", err)
	}
	if searchSchedule.Pinned {
		t.Fatal("expected incompatible api key pin not to apply to standalone search")
	}
	if got, want := collectAccountIDs(searchSchedule.Accounts), []int64{oauth.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected standalone search to use OAuth only, got %v want %v", got, want)
	}

	snapshot, err := svc.GetLatestAccountScheduleSnapshot(ctx)
	if err != nil {
		t.Fatalf("GetLatestAccountScheduleSnapshot failed: %v", err)
	}
	apiKeyDecision := mustFindCandidateDecision(t, snapshot, apiKey.ID)
	if apiKeyDecision.Decision != accountScheduleDecisionSkipped || apiKeyDecision.Reason != accountScheduleSkipRouteRequiresChatGPTOAuth {
		t.Fatalf("expected api key skipped by search route constraint, got %+v", apiKeyDecision)
	}

	activeAccountID, pinned, err := svc.GetActiveSelectionAccountID(ctx)
	if err != nil {
		t.Fatalf("GetActiveSelectionAccountID failed: %v", err)
	}
	if !pinned || activeAccountID != apiKey.ID {
		t.Fatalf("expected global api key pin to remain unchanged, got pinned=%v accountID=%d", pinned, activeAccountID)
	}

	responsesSchedule, err := svc.PrepareSchedulableAccounts(ctx, "req-responses-after-search", "/v1/responses")
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts responses failed: %v", err)
	}
	if !responsesSchedule.Pinned {
		t.Fatal("expected ordinary responses request to keep using the global pin")
	}
	if got, want := collectAccountIDs(responsesSchedule.Accounts), []int64{apiKey.ID, sameTierOAuth.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected ordinary responses request to return to pinned api key, got %v want %v", got, want)
	}

	changed, err = svc.PinAccountSelection(ctx, sameTierOAuth.ID)
	if err != nil {
		t.Fatalf("PinAccountSelection OAuth failed: %v", err)
	}
	if !changed {
		t.Fatal("expected OAuth pin to change runtime selection")
	}
	oauthPinnedSearchSchedule, err := svc.PrepareSchedulableAccounts(ctx, "req-search-compatible-oauth-pin", CodexStandaloneSearchPath)
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts OAuth-pinned search failed: %v", err)
	}
	if !oauthPinnedSearchSchedule.Pinned {
		t.Fatal("expected compatible OAuth pin to remain effective for standalone search")
	}
	if got, want := collectAccountIDs(oauthPinnedSearchSchedule.Accounts), []int64{sameTierOAuth.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected standalone search to honor compatible OAuth pin, got %v want %v", got, want)
	}
}

func TestPrepareSchedulableAccounts_StandaloneSearchRejectsAPIKeyOnlyPool(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	apiKey, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "third-party-only",
		CredentialRaw: "sk-third-party",
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create api key account failed: %v", err)
	}

	schedule, err := svc.PrepareSchedulableAccounts(ctx, "req-search-api-key-only", CodexStandaloneSearchPath)
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts failed: %v", err)
	}
	if len(schedule.Accounts) != 0 {
		t.Fatalf("expected no standalone search candidates, got %v", collectAccountIDs(schedule.Accounts))
	}

	snapshot, err := svc.GetLatestAccountScheduleSnapshot(ctx)
	if err != nil {
		t.Fatalf("GetLatestAccountScheduleSnapshot failed: %v", err)
	}
	decision := mustFindCandidateDecision(t, snapshot, apiKey.ID)
	if decision.Reason != accountScheduleSkipRouteRequiresChatGPTOAuth {
		t.Fatalf("expected route constraint reason, got %+v", decision)
	}
	if !strings.Contains(snapshot.Summary, "ChatGPT OAuth") {
		t.Fatalf("expected OAuth-specific empty summary, got %q", snapshot.Summary)
	}
}

func TestPrepareSchedulableAccounts_DegradesWhenHigherTierTemporarilyExhausted(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()
	now := time.Now()

	exhausted, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:           "chatgpt_refresh_token",
		AccountName:            "main-exhausted",
		CredentialRaw:          "rt-main-exhausted",
		Priority:               10,
		Enabled:                true,
		State:                  "active",
		QuotaStatus:            "exhausted",
		Quota5HUsedPercent:     testFloat64Ptr(100),
		Quota5HResetAt:         testTimePtr(now.Add(2 * time.Hour)),
		QuotaWeeklyUsedPercent: testFloat64Ptr(100),
	})
	if err != nil {
		t.Fatalf("create exhausted account failed: %v", err)
	}

	backup, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:           "chatgpt_refresh_token",
		AccountName:            "backup-ok",
		CredentialRaw:          "rt-backup-ok",
		Priority:               20,
		Enabled:                true,
		State:                  "active",
		QuotaStatus:            "ok",
		QuotaWeeklyUsedPercent: testFloat64Ptr(20),
	})
	if err != nil {
		t.Fatalf("create backup account failed: %v", err)
	}

	accountsSchedule, err := svc.PrepareSchedulableAccounts(ctx, "req-degrade", "/v1/responses")
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts failed: %v", err)
	}
	accounts := accountsSchedule.Accounts
	if got, want := collectAccountIDs(accounts), []int64{backup.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected degraded selection: got %v want %v", got, want)
	}

	snapshot, err := svc.GetLatestAccountScheduleSnapshot(ctx)
	if err != nil {
		t.Fatalf("GetLatestAccountScheduleSnapshot failed: %v", err)
	}
	if snapshot == nil {
		t.Fatal("expected latest snapshot")
	}
	if !snapshot.DegradedToLowerPriority || snapshot.SelectedTierIndex != 2 || snapshot.SelectedTierLabel != "备组" {
		t.Fatalf("expected degrade to backup tier, got %+v", snapshot)
	}
	candidate := mustFindCandidateDecision(t, snapshot, exhausted.ID)
	if candidate.Reason != "quota_exhausted_until_reset" {
		t.Fatalf("expected exhausted account skipped by reset reason, got %+v", candidate)
	}
}

func TestPrepareSchedulableAccounts_RetainsSuccessfulAccountWithinTierUntilSignificantImprovement(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()
	now := time.Now()

	sticky, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:           "chatgpt_refresh_token",
		AccountName:            "sticky-main",
		CredentialRaw:          "rt-sticky-main",
		Priority:               10,
		Enabled:                true,
		State:                  "active",
		QuotaStatus:            "ok",
		Quota5HUsedPercent:     testFloat64Ptr(20),
		Quota5HResetAt:         testTimePtr(now.Add(4 * time.Hour)),
		QuotaWeeklyUsedPercent: testFloat64Ptr(20),
		QuotaWeeklyResetAt:     testTimePtr(now.Add(5 * 24 * time.Hour)),
	})
	if err != nil {
		t.Fatalf("create sticky account failed: %v", err)
	}

	challenger, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:           "chatgpt_refresh_token",
		AccountName:            "challenger",
		CredentialRaw:          "rt-challenger",
		Priority:               10,
		Enabled:                true,
		State:                  "active",
		QuotaStatus:            "ok",
		Quota5HUsedPercent:     testFloat64Ptr(30),
		Quota5HResetAt:         testTimePtr(now.Add(4 * time.Hour)),
		QuotaWeeklyUsedPercent: testFloat64Ptr(20),
		QuotaWeeklyResetAt:     testTimePtr(now.Add(5 * 24 * time.Hour)),
	})
	if err != nil {
		t.Fatalf("create challenger account failed: %v", err)
	}

	initialSchedule, err := svc.PrepareSchedulableAccounts(ctx, "req-sticky-initial", "/v1/responses")
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts initial failed: %v", err)
	}
	initial := initialSchedule.Accounts
	if got, want := collectAccountIDs(initial), []int64{sticky.ID, challenger.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected initial same-tier ranking: got %v want %v", got, want)
	}
	updated, err := svc.MarkAccountSuccessIfNoNewerFailure(ctx, sticky.ID, now)
	if err != nil {
		t.Fatalf("MarkAccountSuccessIfNoNewerFailure sticky failed: %v", err)
	}
	if !updated {
		t.Fatal("expected sticky success to update automatic retain selection")
	}

	updatedChallenger, err := st.GetAccount(ctx, challenger.ID)
	if err != nil {
		t.Fatalf("store GetAccount challenger failed: %v", err)
	}
	if updatedChallenger == nil {
		t.Fatal("expected challenger account")
	}
	updatedChallenger.Quota5HUsedPercent = testFloat64Ptr(24)
	updatedChallenger.Quota5HResetAt = testTimePtr(now.Add(4 * time.Hour))
	updatedChallenger.QuotaWeeklyUsedPercent = testFloat64Ptr(20)
	updatedChallenger.QuotaWeeklyResetAt = testTimePtr(now.Add(5 * 24 * time.Hour))
	if err := st.UpdateAccount(ctx, updatedChallenger); err != nil {
		t.Fatalf("store UpdateAccount challenger failed: %v", err)
	}
	if err := svc.reloadAccountIntoCache(ctx, challenger.ID); err != nil {
		t.Fatalf("reloadAccountIntoCache challenger failed: %v", err)
	}

	nextSchedule, err := svc.PrepareSchedulableAccounts(ctx, "req-sticky-next", "/v1/responses")
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts next failed: %v", err)
	}
	next := nextSchedule.Accounts
	if got, want := collectAccountIDs(next), []int64{sticky.ID, challenger.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected recent successful account to remain selected when challenger improvement is not significant, got %v want %v", got, want)
	}

	snapshot, err := svc.GetLatestAccountScheduleSnapshot(ctx)
	if err != nil {
		t.Fatalf("GetLatestAccountScheduleSnapshot failed: %v", err)
	}
	selected := mustFindCandidateDecision(t, snapshot, sticky.ID)
	if selected.Reason != "retained_active_account_in_selected_tier" {
		t.Fatalf("expected sticky account to be retained within tier, got %+v", selected)
	}
}

func TestPrepareSchedulableAccounts_SwitchesWithinTierAfterSignificantImprovement(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()
	now := time.Now()

	sticky, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:       "chatgpt_refresh_token",
		AccountName:        "sticky-main",
		CredentialRaw:      "rt-sticky-main",
		Priority:           10,
		Enabled:            true,
		State:              "active",
		QuotaStatus:        "ok",
		Quota5HUsedPercent: testFloat64Ptr(20),
		Quota5HResetAt:     testTimePtr(now.Add(4 * time.Hour)),
	})
	if err != nil {
		t.Fatalf("create sticky account failed: %v", err)
	}

	challenger, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:       "chatgpt_refresh_token",
		AccountName:        "challenger",
		CredentialRaw:      "rt-challenger",
		Priority:           10,
		Enabled:            true,
		State:              "active",
		QuotaStatus:        "ok",
		Quota5HUsedPercent: testFloat64Ptr(30),
		Quota5HResetAt:     testTimePtr(now.Add(4 * time.Hour)),
	})
	if err != nil {
		t.Fatalf("create challenger account failed: %v", err)
	}

	initialSchedule, err := svc.PrepareSchedulableAccounts(ctx, "req-significant-initial", "/v1/responses")
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts initial failed: %v", err)
	}
	initial := initialSchedule.Accounts
	if got, want := collectAccountIDs(initial), []int64{sticky.ID, challenger.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected initial same-tier ranking: got %v want %v", got, want)
	}
	updated, err := svc.MarkAccountSuccessIfNoNewerFailure(ctx, sticky.ID, now)
	if err != nil {
		t.Fatalf("MarkAccountSuccessIfNoNewerFailure sticky failed: %v", err)
	}
	if !updated {
		t.Fatal("expected sticky success to update automatic retain selection")
	}

	updatedChallenger, err := st.GetAccount(ctx, challenger.ID)
	if err != nil {
		t.Fatalf("store GetAccount challenger failed: %v", err)
	}
	if updatedChallenger == nil {
		t.Fatal("expected challenger account")
	}
	updatedChallenger.Quota5HUsedPercent = testFloat64Ptr(10)
	updatedChallenger.Quota5HResetAt = testTimePtr(now.Add(2 * time.Hour))
	if err := st.UpdateAccount(ctx, updatedChallenger); err != nil {
		t.Fatalf("store UpdateAccount challenger failed: %v", err)
	}
	if err := svc.reloadAccountIntoCache(ctx, challenger.ID); err != nil {
		t.Fatalf("reloadAccountIntoCache challenger failed: %v", err)
	}

	nextSchedule, err := svc.PrepareSchedulableAccounts(ctx, "req-significant-next", "/v1/responses")
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts next failed: %v", err)
	}
	next := nextSchedule.Accounts
	if got, want := collectAccountIDs(next), []int64{challenger.ID, sticky.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected challenger to replace sticky account after significant improvement, got %v want %v", got, want)
	}

	snapshot, err := svc.GetLatestAccountScheduleSnapshot(ctx)
	if err != nil {
		t.Fatalf("GetLatestAccountScheduleSnapshot failed: %v", err)
	}
	selected := mustFindCandidateDecision(t, snapshot, challenger.ID)
	if selected.Reason != "highest_ranked_in_selected_tier" {
		t.Fatalf("expected challenger to win after significant improvement, got %+v", selected)
	}
}

func TestPrepareSchedulableAccounts_SwitchesWithinTierWhenRetainedAccountFails(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()
	now := time.Now()

	sticky, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:       "chatgpt_refresh_token",
		AccountName:        "sticky-main",
		CredentialRaw:      "rt-sticky-main",
		Priority:           10,
		Enabled:            true,
		State:              "active",
		QuotaStatus:        "ok",
		Quota5HUsedPercent: testFloat64Ptr(20),
		Quota5HResetAt:     testTimePtr(now.Add(4 * time.Hour)),
	})
	if err != nil {
		t.Fatalf("create sticky account failed: %v", err)
	}

	challenger, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:       "chatgpt_refresh_token",
		AccountName:        "challenger",
		CredentialRaw:      "rt-challenger",
		Priority:           10,
		Enabled:            true,
		State:              "active",
		QuotaStatus:        "ok",
		Quota5HUsedPercent: testFloat64Ptr(30),
		Quota5HResetAt:     testTimePtr(now.Add(4 * time.Hour)),
	})
	if err != nil {
		t.Fatalf("create challenger account failed: %v", err)
	}

	initialSchedule, err := svc.PrepareSchedulableAccounts(ctx, "req-failure-initial", "/v1/responses")
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts initial failed: %v", err)
	}
	initial := initialSchedule.Accounts
	if got, want := collectAccountIDs(initial), []int64{sticky.ID, challenger.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected initial same-tier ranking: got %v want %v", got, want)
	}
	updated, err := svc.MarkAccountSuccessIfNoNewerFailure(ctx, sticky.ID, now)
	if err != nil {
		t.Fatalf("MarkAccountSuccessIfNoNewerFailure sticky failed: %v", err)
	}
	if !updated {
		t.Fatal("expected sticky success to update automatic retain selection")
	}
	if err := svc.MarkAccountTransientFailure(ctx, sticky.ID, "boom", time.Minute); err != nil {
		t.Fatalf("MarkAccountTransientFailure sticky failed: %v", err)
	}

	nextSchedule, err := svc.PrepareSchedulableAccounts(ctx, "req-failure-next", "/v1/responses")
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts next failed: %v", err)
	}
	next := nextSchedule.Accounts
	if got, want := collectAccountIDs(next), []int64{challenger.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected challenger to replace failed sticky account, got %v want %v", got, want)
	}
}

func TestPrepareSchedulableAccounts_DoesNotRetainOnConnectivityStyleSuccessWrites(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()
	now := time.Now()

	sticky, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:       "chatgpt_refresh_token",
		AccountName:        "sticky-main",
		CredentialRaw:      "rt-sticky-main",
		Priority:           10,
		Enabled:            true,
		State:              "active",
		QuotaStatus:        "ok",
		Quota5HUsedPercent: testFloat64Ptr(20),
		Quota5HResetAt:     testTimePtr(now.Add(4 * time.Hour)),
	})
	if err != nil {
		t.Fatalf("create sticky account failed: %v", err)
	}

	challenger, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:       "chatgpt_refresh_token",
		AccountName:        "challenger",
		CredentialRaw:      "rt-challenger",
		Priority:           10,
		Enabled:            true,
		State:              "active",
		QuotaStatus:        "ok",
		Quota5HUsedPercent: testFloat64Ptr(24),
		Quota5HResetAt:     testTimePtr(now.Add(4 * time.Hour)),
	})
	if err != nil {
		t.Fatalf("create challenger account failed: %v", err)
	}

	initialSchedule, err := svc.PrepareSchedulableAccounts(ctx, "req-connectivity-initial", "/v1/responses")
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts initial failed: %v", err)
	}
	initial := initialSchedule.Accounts
	if got, want := collectAccountIDs(initial), []int64{sticky.ID, challenger.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected initial same-tier ranking: got %v want %v", got, want)
	}

	if err := svc.MarkAccountSuccess(ctx, challenger.ID); err != nil {
		t.Fatalf("MarkAccountSuccess challenger failed: %v", err)
	}

	nextSchedule, err := svc.PrepareSchedulableAccounts(ctx, "req-connectivity-next", "/v1/responses")
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts next failed: %v", err)
	}
	next := nextSchedule.Accounts
	if got, want := collectAccountIDs(next), []int64{sticky.ID, challenger.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected connectivity-style success writes not to alter automatic retain selection, got %v want %v", got, want)
	}
}

func TestPrepareSchedulableAccounts_AutoReturnsToHigherTierAfterRecovery(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()
	now := time.Now()

	primary, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:           "chatgpt_refresh_token",
		AccountName:            "primary-exhausted",
		CredentialRaw:          "rt-primary-exhausted",
		Priority:               10,
		Enabled:                true,
		State:                  "active",
		QuotaStatus:            "exhausted",
		Quota5HUsedPercent:     testFloat64Ptr(100),
		Quota5HResetAt:         testTimePtr(now.Add(2 * time.Hour)),
		QuotaWeeklyUsedPercent: testFloat64Ptr(100),
	})
	if err != nil {
		t.Fatalf("create exhausted primary failed: %v", err)
	}

	backup, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:           "chatgpt_refresh_token",
		AccountName:            "backup-sticky",
		CredentialRaw:          "rt-backup-sticky",
		Priority:               20,
		Enabled:                true,
		State:                  "active",
		QuotaStatus:            "ok",
		Quota5HUsedPercent:     testFloat64Ptr(20),
		Quota5HResetAt:         testTimePtr(now.Add(90 * time.Minute)),
		QuotaWeeklyUsedPercent: testFloat64Ptr(25),
		QuotaWeeklyResetAt:     testTimePtr(now.Add(6 * 24 * time.Hour)),
	})
	if err != nil {
		t.Fatalf("create backup account failed: %v", err)
	}

	degradedSchedule, err := svc.PrepareSchedulableAccounts(ctx, "req-degrade-sticky", "/v1/responses")
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts degraded failed: %v", err)
	}
	degraded := degradedSchedule.Accounts
	if got, want := collectAccountIDs(degraded), []int64{backup.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected initial degrade to backup, got %v want %v", got, want)
	}

	recoveredPrimary, err := st.GetAccount(ctx, primary.ID)
	if err != nil {
		t.Fatalf("store GetAccount primary failed: %v", err)
	}
	if recoveredPrimary == nil {
		t.Fatal("expected recovered primary account")
	}
	recoveredPrimary.QuotaStatus = "ok"
	recoveredPrimary.Quota5HUsedPercent = testFloat64Ptr(10)
	recoveredPrimary.Quota5HResetAt = testTimePtr(now.Add(30 * time.Minute))
	recoveredPrimary.QuotaWeeklyUsedPercent = testFloat64Ptr(10)
	recoveredPrimary.QuotaWeeklyResetAt = testTimePtr(now.Add(5 * 24 * time.Hour))
	if err := st.UpdateAccount(ctx, recoveredPrimary); err != nil {
		t.Fatalf("store UpdateAccount primary failed: %v", err)
	}
	if err := svc.reloadAccountIntoCache(ctx, primary.ID); err != nil {
		t.Fatalf("reloadAccountIntoCache primary failed: %v", err)
	}

	stillBackupSchedule, err := svc.PrepareSchedulableAccounts(ctx, "req-still-backup", "/v1/responses")
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts after recovery failed: %v", err)
	}
	stillBackup := stillBackupSchedule.Accounts
	if got, want := collectAccountIDs(stillBackup), []int64{primary.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected recovered primary tier to be selected again, got %v want %v", got, want)
	}

	snapshot, err := svc.GetLatestAccountScheduleSnapshot(ctx)
	if err != nil {
		t.Fatalf("GetLatestAccountScheduleSnapshot failed: %v", err)
	}
	if snapshot.DegradedToLowerPriority {
		t.Fatalf("expected snapshot to return to primary tier after recovery, got %+v", snapshot)
	}
	recoveredPrimaryCandidate := mustFindCandidateDecision(t, snapshot, primary.ID)
	if recoveredPrimaryCandidate.Reason != "highest_ranked_in_selected_tier" {
		t.Fatalf("expected recovered primary to win by current ranking, got %+v", recoveredPrimaryCandidate)
	}
	backupCandidate := mustFindCandidateDecision(t, snapshot, backup.ID)
	if backupCandidate.Reason != "higher_priority_tier_selected" {
		t.Fatalf("expected backup tier to be skipped after primary recovers, got %+v", backupCandidate)
	}
}

func TestMoveAccountToTier_OverridesDegradedSelectionEvenWhenPriorityUnchanged(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()
	now := time.Now()

	primary, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:           "chatgpt_refresh_token",
		AccountName:            "manual-primary",
		CredentialRaw:          "rt-manual-primary",
		Priority:               10,
		Enabled:                true,
		State:                  "active",
		QuotaStatus:            "exhausted",
		Quota5HUsedPercent:     testFloat64Ptr(100),
		Quota5HResetAt:         testTimePtr(now.Add(90 * time.Minute)),
		QuotaWeeklyUsedPercent: testFloat64Ptr(100),
	})
	if err != nil {
		t.Fatalf("create primary account failed: %v", err)
	}

	backup, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:           "chatgpt_refresh_token",
		AccountName:            "sticky-backup",
		CredentialRaw:          "rt-sticky-backup",
		Priority:               20,
		Enabled:                true,
		State:                  "active",
		QuotaStatus:            "ok",
		Quota5HUsedPercent:     testFloat64Ptr(20),
		Quota5HResetAt:         testTimePtr(now.Add(45 * time.Minute)),
		QuotaWeeklyUsedPercent: testFloat64Ptr(30),
		QuotaWeeklyResetAt:     testTimePtr(now.Add(6 * 24 * time.Hour)),
	})
	if err != nil {
		t.Fatalf("create backup account failed: %v", err)
	}

	degradedSchedule, err := svc.PrepareSchedulableAccounts(ctx, "req-before-manual-switch", "/v1/responses")
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts degraded failed: %v", err)
	}
	degraded := degradedSchedule.Accounts
	if got, want := collectAccountIDs(degraded), []int64{backup.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected initial degrade to sticky backup, got %v want %v", got, want)
	}

	recoveredPrimary, err := st.GetAccount(ctx, primary.ID)
	if err != nil {
		t.Fatalf("store GetAccount primary failed: %v", err)
	}
	if recoveredPrimary == nil {
		t.Fatal("expected recovered primary account")
	}
	recoveredPrimary.QuotaStatus = "ok"
	recoveredPrimary.Quota5HUsedPercent = nil
	recoveredPrimary.Quota5HResetAt = nil
	recoveredPrimary.QuotaWeeklyUsedPercent = nil
	recoveredPrimary.QuotaWeeklyResetAt = nil
	if err := st.UpdateAccount(ctx, recoveredPrimary); err != nil {
		t.Fatalf("store UpdateAccount primary failed: %v", err)
	}
	if err := svc.reloadAccountIntoCache(ctx, primary.ID); err != nil {
		t.Fatalf("reloadAccountIntoCache primary failed: %v", err)
	}

	changed, err := svc.PinAccountSelection(ctx, primary.ID)
	if err != nil {
		t.Fatalf("PinAccountSelection failed: %v", err)
	}
	if !changed {
		t.Fatal("expected pinning primary account to count as effective change")
	}

	snapshot, err := svc.GetLatestAccountScheduleSnapshot(ctx)
	if err != nil {
		t.Fatalf("GetLatestAccountScheduleSnapshot failed: %v", err)
	}
	if snapshot != nil {
		t.Fatalf("expected stale schedule snapshot to be cleared after manual switch, got %+v", snapshot)
	}

	orderedSchedule, err := svc.PrepareSchedulableAccounts(ctx, "req-after-manual-switch", "/v1/responses")
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts after manual switch failed: %v", err)
	}
	ordered := orderedSchedule.Accounts
	if got, want := collectAccountIDs(ordered), []int64{primary.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected manual pin to force primary account selection, got %v want %v", got, want)
	}
}

func TestPrepareSchedulableAccounts_FallsBackToRecoveredHigherPriorityTierWhenPinnedBackupBecomesUnavailable(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()
	now := time.Now()

	primary, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:           "chatgpt_refresh_token",
		AccountName:            "primary-ok",
		CredentialRaw:          "rt-primary-ok",
		Priority:               10,
		Enabled:                true,
		State:                  "active",
		QuotaStatus:            "ok",
		Quota5HUsedPercent:     testFloat64Ptr(15),
		Quota5HResetAt:         testTimePtr(now.Add(30 * time.Minute)),
		QuotaWeeklyUsedPercent: testFloat64Ptr(10),
		QuotaWeeklyResetAt:     testTimePtr(now.Add(5 * 24 * time.Hour)),
	})
	if err != nil {
		t.Fatalf("create primary account failed: %v", err)
	}

	backup, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:           "chatgpt_refresh_token",
		AccountName:            "backup-pinned",
		CredentialRaw:          "rt-backup-pinned",
		Priority:               20,
		Enabled:                true,
		State:                  "active",
		QuotaStatus:            "ok",
		Quota5HUsedPercent:     testFloat64Ptr(10),
		Quota5HResetAt:         testTimePtr(now.Add(20 * time.Minute)),
		QuotaWeeklyUsedPercent: testFloat64Ptr(10),
		QuotaWeeklyResetAt:     testTimePtr(now.Add(5 * 24 * time.Hour)),
	})
	if err != nil {
		t.Fatalf("create backup account failed: %v", err)
	}

	changed, err := svc.PinAccountSelection(ctx, backup.ID)
	if err != nil {
		t.Fatalf("PinAccountSelection backup failed: %v", err)
	}
	if !changed {
		t.Fatal("expected pinning backup account to change runtime selection")
	}

	updatedBackup, err := st.GetAccount(ctx, backup.ID)
	if err != nil {
		t.Fatalf("GetAccount backup failed: %v", err)
	}
	if updatedBackup == nil {
		t.Fatal("expected backup account")
	}
	updatedBackup.QuotaStatus = "exhausted"
	updatedBackup.Quota5HUsedPercent = testFloat64Ptr(100)
	updatedBackup.Quota5HResetAt = testTimePtr(now.Add(2 * time.Hour))
	updatedBackup.QuotaWeeklyUsedPercent = testFloat64Ptr(100)
	if err := st.UpdateAccount(ctx, updatedBackup); err != nil {
		t.Fatalf("UpdateAccount backup failed: %v", err)
	}
	if err := svc.reloadAccountIntoCache(ctx, backup.ID); err != nil {
		t.Fatalf("reloadAccountIntoCache backup failed: %v", err)
	}

	orderedSchedule, err := svc.PrepareSchedulableAccounts(ctx, "req-pinned-backup-unavailable", "/v1/responses")
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts failed: %v", err)
	}
	ordered := orderedSchedule.Accounts
	if got, want := collectAccountIDs(ordered), []int64{primary.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected scheduler to fall back to recovered higher-priority tier, got %v want %v", got, want)
	}
}

func TestMoveAccountToTier_MovingAccountToPrimaryGroupPinsClickedAccountWithoutDemotingOtherGroups(t *testing.T) {
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
		Priority:      10,
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
		GroupKey:      accountGroupBackup,
		Priority:      20,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create backup-b failed: %v", err)
	}

	changed, err := svc.MoveAccountToTier(ctx, backupB.ID, 0)
	if err != nil {
		t.Fatalf("MoveAccountToTier failed: %v", err)
	}
	if !changed {
		t.Fatal("expected moving backup account to primary group to count as a change")
	}

	updatedBackupB, err := st.GetAccount(ctx, backupB.ID)
	if err != nil {
		t.Fatalf("GetAccount backup-b failed: %v", err)
	}
	if updatedBackupB == nil || updatedBackupB.GroupKey != accountGroupPrimary || updatedBackupB.Priority != 10 {
		t.Fatalf("expected clicked backup account to become first primary account, got %+v", updatedBackupB)
	}

	updatedPrimaryA, err := st.GetAccount(ctx, primaryA.ID)
	if err != nil {
		t.Fatalf("GetAccount primary-a failed: %v", err)
	}
	if updatedPrimaryA == nil || updatedPrimaryA.GroupKey != accountGroupPrimary || updatedPrimaryA.Priority != 20 {
		t.Fatalf("expected existing primary account to remain in primary behind clicked account, got %+v", updatedPrimaryA)
	}

	updatedBackupA, err := st.GetAccount(ctx, backupA.ID)
	if err != nil {
		t.Fatalf("GetAccount backup-a failed: %v", err)
	}
	if updatedBackupA == nil || updatedBackupA.GroupKey != accountGroupBackup || updatedBackupA.Priority != 10 {
		t.Fatalf("expected untouched backup account to remain backup, got %+v", updatedBackupA)
	}

	orderedSchedule, err := svc.PrepareSchedulableAccounts(ctx, "req-after-tier-promotion", "/v1/responses")
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts after primary group move failed: %v", err)
	}
	ordered := orderedSchedule.Accounts
	if got, want := collectAccountIDs(ordered), []int64{primaryA.ID, primaryB.ID, backupB.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected automatic scheduling to use primary-group auto ranking after group move, got %v want %v", got, want)
	}
}

func TestPrepareSchedulableAccounts_RanksByUtilizationThenHealthWithinTier(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()
	now := time.Now()

	expiringSoon, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:           "chatgpt_refresh_token",
		AccountName:            "expiring-soon",
		CredentialRaw:          "rt-expiring-soon",
		Priority:               10,
		Enabled:                true,
		State:                  "active",
		QuotaStatus:            "ok",
		Quota5HUsedPercent:     testFloat64Ptr(40),
		Quota5HResetAt:         testTimePtr(now.Add(20 * time.Minute)),
		QuotaWeeklyUsedPercent: testFloat64Ptr(20),
		QuotaWeeklyResetAt:     testTimePtr(now.Add(5 * 24 * time.Hour)),
		FailCount:              0,
		LastSuccessAt:          testTimePtr(now.Add(-2 * time.Minute)),
	})
	if err != nil {
		t.Fatalf("create expiring soon account failed: %v", err)
	}

	stableHigh, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:           "chatgpt_refresh_token",
		AccountName:            "stable-high",
		CredentialRaw:          "rt-stable-high",
		Priority:               10,
		Enabled:                true,
		State:                  "active",
		QuotaStatus:            "ok",
		Quota5HUsedPercent:     testFloat64Ptr(20),
		Quota5HResetAt:         testTimePtr(now.Add(4 * time.Hour)),
		QuotaWeeklyUsedPercent: testFloat64Ptr(20),
		QuotaWeeklyResetAt:     testTimePtr(now.Add(5 * 24 * time.Hour)),
		FailCount:              0,
		LastSuccessAt:          testTimePtr(now.Add(-1 * time.Minute)),
	})
	if err != nil {
		t.Fatalf("create stable high account failed: %v", err)
	}

	weeklyGuarded, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:           "chatgpt_refresh_token",
		AccountName:            "weekly-guarded",
		CredentialRaw:          "rt-weekly-guarded",
		Priority:               10,
		Enabled:                true,
		State:                  "active",
		QuotaStatus:            "ok",
		Quota5HUsedPercent:     testFloat64Ptr(10),
		Quota5HResetAt:         testTimePtr(now.Add(1 * time.Hour)),
		QuotaWeeklyUsedPercent: testFloat64Ptr(92),
		QuotaWeeklyResetAt:     testTimePtr(now.Add(5 * 24 * time.Hour)),
		FailCount:              0,
		LastSuccessAt:          testTimePtr(now.Add(-30 * time.Second)),
	})
	if err != nil {
		t.Fatalf("create weekly guarded account failed: %v", err)
	}

	smallUrgent, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:           "chatgpt_refresh_token",
		AccountName:            "small-urgent",
		CredentialRaw:          "rt-small-urgent",
		Priority:               10,
		Enabled:                true,
		State:                  "active",
		QuotaStatus:            "ok",
		Quota5HUsedPercent:     testFloat64Ptr(93),
		Quota5HResetAt:         testTimePtr(now.Add(5 * time.Minute)),
		QuotaWeeklyUsedPercent: testFloat64Ptr(0),
		QuotaWeeklyResetAt:     testTimePtr(now.Add(5 * 24 * time.Hour)),
		FailCount:              0,
		LastSuccessAt:          testTimePtr(now.Add(-45 * time.Second)),
	})
	if err != nil {
		t.Fatalf("create small urgent account failed: %v", err)
	}

	unknown, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:           "chatgpt_refresh_token",
		AccountName:            "unknown-no-reset",
		CredentialRaw:          "rt-unknown-no-reset",
		Priority:               10,
		Enabled:                true,
		State:                  "active",
		QuotaStatus:            "ok",
		QuotaWeeklyUsedPercent: testFloat64Ptr(10),
	})
	if err != nil {
		t.Fatalf("create unknown account failed: %v", err)
	}

	apiFallback, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "api-fallback",
		CredentialRaw: "sk-api-fallback",
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create api fallback account failed: %v", err)
	}

	accountsSchedule, err := svc.PrepareSchedulableAccounts(ctx, "req-rank", "/v1/responses")
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts failed: %v", err)
	}
	accounts := accountsSchedule.Accounts
	if got, want := collectAccountIDs(accounts), []int64{expiringSoon.ID, smallUrgent.ID, weeklyGuarded.ID, stableHigh.ID, unknown.ID, apiFallback.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected ranked order: got %v want %v", got, want)
	}
}

func TestPrepareSchedulableAccounts_UsesSingleCompleteQuotaWindowWhenScoring(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()
	now := time.Now()

	single5H, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:       "chatgpt_refresh_token",
		AccountName:        "single-5h",
		CredentialRaw:      "rt-single-5h",
		Priority:           10,
		Enabled:            true,
		State:              "active",
		QuotaStatus:        "ok",
		Quota5HUsedPercent: testFloat64Ptr(45),
		Quota5HResetAt:     testTimePtr(now.Add(1 * time.Hour)),
	})
	if err != nil {
		t.Fatalf("create single 5h account failed: %v", err)
	}

	singleWeekly, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:           "chatgpt_refresh_token",
		AccountName:            "single-weekly",
		CredentialRaw:          "rt-single-weekly",
		Priority:               10,
		Enabled:                true,
		State:                  "active",
		QuotaStatus:            "ok",
		QuotaWeeklyUsedPercent: testFloat64Ptr(30),
		QuotaWeeklyResetAt:     testTimePtr(now.Add(12 * time.Hour)),
	})
	if err != nil {
		t.Fatalf("create single weekly account failed: %v", err)
	}

	noReset, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:           "chatgpt_refresh_token",
		AccountName:            "known-percent-no-reset",
		CredentialRaw:          "rt-known-percent-no-reset",
		Priority:               10,
		Enabled:                true,
		State:                  "active",
		QuotaStatus:            "ok",
		QuotaWeeklyUsedPercent: testFloat64Ptr(10),
	})
	if err != nil {
		t.Fatalf("create no reset account failed: %v", err)
	}

	apiFallback, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "api-fallback",
		CredentialRaw: "sk-api-fallback",
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create api fallback account failed: %v", err)
	}

	accountsSchedule, err := svc.PrepareSchedulableAccounts(ctx, "req-single-window", "/v1/responses")
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts failed: %v", err)
	}
	accounts := accountsSchedule.Accounts
	if got, want := collectAccountIDs(accounts), []int64{single5H.ID, singleWeekly.ID, noReset.ID, apiFallback.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected single-window ranking: got %v want %v", got, want)
	}
}

func TestPrepareSchedulableAccounts_SnapshotReasonDetailsExplainUtilizationPriority(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()
	now := time.Now()

	expiringSoon, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:           "chatgpt_refresh_token",
		AccountName:            "expiring-soon",
		CredentialRaw:          "rt-expiring-soon",
		Priority:               10,
		Enabled:                true,
		State:                  "active",
		QuotaStatus:            "ok",
		Quota5HUsedPercent:     testFloat64Ptr(40),
		Quota5HResetAt:         testTimePtr(now.Add(20 * time.Minute)),
		QuotaWeeklyUsedPercent: testFloat64Ptr(20),
		QuotaWeeklyResetAt:     testTimePtr(now.Add(5 * 24 * time.Hour)),
	})
	if err != nil {
		t.Fatalf("create expiring soon account failed: %v", err)
	}

	weeklyGuarded, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:           "chatgpt_refresh_token",
		AccountName:            "weekly-guarded",
		CredentialRaw:          "rt-weekly-guarded",
		Priority:               10,
		Enabled:                true,
		State:                  "active",
		QuotaStatus:            "ok",
		Quota5HUsedPercent:     testFloat64Ptr(10),
		Quota5HResetAt:         testTimePtr(now.Add(1 * time.Hour)),
		QuotaWeeklyUsedPercent: testFloat64Ptr(92),
		QuotaWeeklyResetAt:     testTimePtr(now.Add(5 * 24 * time.Hour)),
	})
	if err != nil {
		t.Fatalf("create weekly guarded account failed: %v", err)
	}

	unknown, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:           "chatgpt_refresh_token",
		AccountName:            "unknown-no-reset",
		CredentialRaw:          "rt-unknown-no-reset",
		Priority:               10,
		Enabled:                true,
		State:                  "active",
		QuotaStatus:            "ok",
		QuotaWeeklyUsedPercent: testFloat64Ptr(10),
	})
	if err != nil {
		t.Fatalf("create unknown account failed: %v", err)
	}

	apiFallback, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "api-fallback",
		CredentialRaw: "sk-api-fallback",
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create api fallback account failed: %v", err)
	}

	if _, err := svc.PrepareSchedulableAccounts(ctx, "req-snapshot-reason", "/v1/responses"); err != nil {
		t.Fatalf("PrepareSchedulableAccounts failed: %v", err)
	}

	snapshot, err := svc.GetLatestAccountScheduleSnapshot(ctx)
	if err != nil {
		t.Fatalf("GetLatestAccountScheduleSnapshot failed: %v", err)
	}
	if snapshot == nil {
		t.Fatal("expected latest snapshot")
	}

	selected := mustFindCandidateDecision(t, snapshot, expiringSoon.ID)
	assertStringContainsAll(t, selected.ReasonDetail, []string{"5h", "重置"})
	if selected.EffectiveQuotaRemaining == nil {
		t.Fatal("expected selected candidate effective quota remaining to stay populated")
	}

	guarded := mustFindCandidateDecision(t, snapshot, weeklyGuarded.ID)
	assertStringContainsAll(t, guarded.ReasonDetail, []string{"周额度", "护栏"})
	if guarded.EffectiveQuotaRemaining == nil {
		t.Fatal("expected guarded candidate effective quota remaining to stay populated")
	}

	unknownCandidate := mustFindCandidateDecision(t, snapshot, unknown.ID)
	assertStringContainsAll(t, unknownCandidate.ReasonDetail, []string{"quota", "未知", "劣后"})
	if unknownCandidate.EffectiveQuotaRemaining == nil {
		t.Fatal("expected unknown candidate effective quota remaining to stay populated when usage percent exists")
	}

	apiCandidate := mustFindCandidateDecision(t, snapshot, apiFallback.ID)
	assertStringContainsAll(t, apiCandidate.ReasonDetail, []string{"api_key", "兜底", "OAuth"})
	if apiCandidate.EffectiveQuotaRemaining != nil {
		t.Fatal("expected api_key candidate effective quota remaining to remain nil")
	}
}

func TestCompleteLatestScheduleSnapshot_UpdatesOutcomeAndRuntimeCandidate(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	selected, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "selected",
		CredentialRaw: "sk-selected",
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create selected account failed: %v", err)
	}

	if _, err := svc.PrepareSchedulableAccounts(ctx, "req-complete", "/v1/responses"); err != nil {
		t.Fatalf("PrepareSchedulableAccounts failed: %v", err)
	}
	if _, err := svc.CompleteLatestScheduleSnapshot(ctx, "req-complete", selected.ID, selected.AccountName, accountScheduleOutcomeSuccess, ""); err != nil {
		t.Fatalf("CompleteLatestScheduleSnapshot failed: %v", err)
	}

	snapshot, err := svc.GetLatestAccountScheduleSnapshot(ctx)
	if err != nil {
		t.Fatalf("GetLatestAccountScheduleSnapshot failed: %v", err)
	}
	if snapshot == nil {
		t.Fatal("expected latest snapshot")
	}
	if snapshot.FinalOutcome != accountScheduleOutcomeSuccess {
		t.Fatalf("expected success outcome, got %+v", snapshot)
	}
	candidate := mustFindCandidateDecision(t, snapshot, selected.ID)
	if candidate.RuntimeOutcome != accountScheduleOutcomeSuccess {
		t.Fatalf("expected runtime outcome to be recorded, got %+v", candidate)
	}
}

func TestLatestAccountScheduleSnapshotStore_SaveDraftCleansExpiredPending(t *testing.T) {
	store := newLatestAccountScheduleSnapshotStore()
	now := time.Now()
	store.pending["expired"] = &LatestAccountScheduleSnapshot{
		RequestID:   "expired",
		CapturedAt:  now.Add(-latestAccountScheduleSnapshotPendingTTL - time.Minute),
		UpdatedAt:   now.Add(-latestAccountScheduleSnapshotPendingTTL - time.Minute),
		RequestPath: "/v1/responses",
	}
	store.pending["fresh"] = &LatestAccountScheduleSnapshot{
		RequestID:   "fresh",
		CapturedAt:  now,
		UpdatedAt:   now,
		RequestPath: "/v1/responses",
	}

	store.saveDraft(&LatestAccountScheduleSnapshot{
		RequestID:   "new-request",
		CapturedAt:  now,
		UpdatedAt:   now,
		RequestPath: "/v1/responses",
	})

	store.mu.RLock()
	defer store.mu.RUnlock()

	if _, ok := store.pending["expired"]; ok {
		t.Fatal("expected expired pending snapshot to be cleaned up")
	}
	if _, ok := store.pending["fresh"]; !ok {
		t.Fatal("expected fresh pending snapshot to be retained")
	}
	if _, ok := store.pending["new-request"]; !ok {
		t.Fatal("expected new draft snapshot to be stored")
	}
}

func TestLatestAccountScheduleSnapshotStore_CancelledIsTerminal(t *testing.T) {
	store := newLatestAccountScheduleSnapshotStore()
	store.saveDraft(&LatestAccountScheduleSnapshot{
		RequestID:   "req-cancel",
		RequestPath: "/v1/responses",
		Candidates:  []AccountScheduleCandidateDecision{{AccountID: 1}, {AccountID: 2}},
	})
	store.complete("req-cancel", 1, "acc-1", AccountScheduleOutcomeTransientFailure, "upstream returned 500")

	snapshot, ok := store.complete("req-cancel", 2, "acc-2", AccountScheduleOutcomeCancelled, "client cancelled")
	if !ok || snapshot.FinalOutcome != AccountScheduleOutcomeCancelled {
		t.Fatalf("expected cancelled final outcome, got ok=%v snapshot=%+v", ok, snapshot)
	}
	if got := mustFindCandidateDecision(t, snapshot, 2).RuntimeOutcome; got != AccountScheduleOutcomeCancelled {
		t.Fatalf("expected cancelled runtime outcome on current account, got %q", got)
	}
	if got := mustFindCandidateDecision(t, snapshot, 1).RuntimeOutcome; got != AccountScheduleOutcomeTransientFailure {
		t.Fatalf("expected earlier failure to be kept, got %q", got)
	}

	store.mu.RLock()
	defer store.mu.RUnlock()
	if _, ok := store.pending["req-cancel"]; ok {
		t.Fatal("expected cancelled snapshot to leave pending")
	}
}

func TestLatestAccountScheduleSnapshotStore_LongRequestCompletesAfterOtherDrafts(t *testing.T) {
	store := newLatestAccountScheduleSnapshotStore()
	tenMinutesAgo := time.Now().Add(-10 * time.Minute)
	store.pending["long-request"] = &LatestAccountScheduleSnapshot{
		RequestID:   "long-request",
		CapturedAt:  tenMinutesAgo,
		UpdatedAt:   tenMinutesAgo,
		RequestPath: "/v1/responses",
		Candidates:  []AccountScheduleCandidateDecision{{AccountID: 1}},
	}
	// 长请求跑着的时候别的请求进来，触发过期清理
	store.saveDraft(&LatestAccountScheduleSnapshot{RequestID: "other-request", RequestPath: "/v1/responses"})

	snapshot, ok := store.complete("long-request", 1, "acc-1", AccountScheduleOutcomeSuccess, "")
	if !ok || snapshot.FinalOutcome != AccountScheduleOutcomeSuccess {
		t.Fatalf("expected long request to finalize its own snapshot, got ok=%v snapshot=%+v", ok, snapshot)
	}
}

func mustFindCandidateDecision(t *testing.T, snapshot *LatestAccountScheduleSnapshot, accountID int64) AccountScheduleCandidateDecision {
	t.Helper()
	for _, candidate := range snapshot.Candidates {
		if candidate.AccountID == accountID {
			return candidate
		}
	}
	t.Fatalf("candidate %d not found in snapshot %+v", accountID, snapshot)
	return AccountScheduleCandidateDecision{}
}

func assertStringContainsAll(t *testing.T, got string, wants []string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q to contain %q", got, want)
		}
	}
}
