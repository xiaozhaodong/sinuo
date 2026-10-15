package service

import (
	"context"
	"testing"
	"time"

	"sinuo/internal/store"
)

func TestAccountPoolService_MarkAccountUsageLimitExceeded_SetsExhaustedQuotaAndCooldownUntilReset(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	acc, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "chatgpt_refresh_token",
		AccountName:   "acc-usage-limit",
		CredentialRaw: `{"refresh_token":"rt-usage","chatgpt_account_id":"acct-usage"}`,
		Priority:      10,
		Enabled:       true,
		State:         "active",
		PlanType:      "free",
	})
	if err != nil {
		t.Fatalf("create account failed: %v", err)
	}

	resetAt := time.Now().Add(2 * time.Hour).Round(time.Second)
	if err := svc.MarkAccountUsageLimitExceeded(ctx, acc.ID, "usage limited", "free", resetAt); err != nil {
		t.Fatalf("MarkAccountUsageLimitExceeded failed: %v", err)
	}

	runtimeRecord, err := svc.GetAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("GetAccount failed: %v", err)
	}
	if runtimeRecord == nil {
		t.Fatal("expected runtime record")
	}
	if runtimeRecord.State != "cooldown" {
		t.Fatalf("expected cooldown state, got %s", runtimeRecord.State)
	}
	if runtimeRecord.QuotaStatus != quotaStatusExhausted {
		t.Fatalf("expected exhausted quota status, got %s", runtimeRecord.QuotaStatus)
	}
	if runtimeRecord.QuotaWeeklyResetAt == nil || runtimeRecord.QuotaWeeklyResetAt.Unix() != resetAt.Unix() {
		t.Fatalf("expected weekly reset at %v, got %+v", resetAt, runtimeRecord.QuotaWeeklyResetAt)
	}
	if runtimeRecord.QuotaWeeklyUsedPercent == nil || *runtimeRecord.QuotaWeeklyUsedPercent != 100 {
		t.Fatalf("expected weekly used percent 100, got %+v", runtimeRecord.QuotaWeeklyUsedPercent)
	}
	if runtimeRecord.CooldownUntil == nil || runtimeRecord.CooldownUntil.Unix() != resetAt.Unix() {
		t.Fatalf("expected cooldown_until %v, got %+v", resetAt, runtimeRecord.CooldownUntil)
	}

	waitForPersistedAccountState(t, st, acc.ID, func(record *store.UpstreamAccountRecord) bool {
		return record != nil &&
			record.State == "cooldown" &&
			record.QuotaStatus == quotaStatusExhausted &&
			record.CooldownUntil != nil &&
			record.CooldownUntil.Unix() == resetAt.Unix() &&
			record.QuotaWeeklyResetAt != nil &&
			record.QuotaWeeklyResetAt.Unix() == resetAt.Unix()
	})
}

func TestAccountPoolService_MarkAccountUsageLimitExceeded_NonFree5HWindowDoesNotPoisonWeeklyWindow(t *testing.T) {
	svc, _ := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	fiveHResetAt := time.Now().Add(2 * time.Hour).Round(time.Second)
	weeklyResetAt := time.Now().Add(4 * 24 * time.Hour).Round(time.Second)
	weeklyUsed := float64(35)

	acc, err := svc.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:           "chatgpt_refresh_token",
		AccountName:            "acc-plus-5h",
		CredentialRaw:          `{"refresh_token":"rt-plus-5h","chatgpt_account_id":"acct-plus-5h"}`,
		Priority:               10,
		Enabled:                true,
		State:                  "active",
		PlanType:               "plus",
		Quota5HResetAt:         &fiveHResetAt,
		QuotaWeeklyUsedPercent: &weeklyUsed,
		QuotaWeeklyResetAt:     &weeklyResetAt,
		QuotaStatus:            quotaStatusOK,
	})
	if err != nil {
		t.Fatalf("create account failed: %v", err)
	}

	if err := svc.MarkAccountUsageLimitExceeded(ctx, acc.ID, "usage limited", "plus", fiveHResetAt); err != nil {
		t.Fatalf("MarkAccountUsageLimitExceeded failed: %v", err)
	}

	runtimeRecord, err := svc.GetAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("GetAccount failed: %v", err)
	}
	if runtimeRecord == nil {
		t.Fatal("expected runtime record")
	}
	if runtimeRecord.Quota5HUsedPercent == nil || *runtimeRecord.Quota5HUsedPercent != 100 {
		t.Fatalf("expected 5h window marked exhausted, got %+v", runtimeRecord.Quota5HUsedPercent)
	}
	if runtimeRecord.QuotaWeeklyUsedPercent == nil || *runtimeRecord.QuotaWeeklyUsedPercent != weeklyUsed {
		t.Fatalf("expected weekly window preserved, got %+v", runtimeRecord.QuotaWeeklyUsedPercent)
	}

	after5HReset := cloneUpstreamAccountRecord(runtimeRecord)
	after5HReset.State = "active"
	after5HReset.CooldownUntil = nil
	candidate := classifySchedulableAccount(after5HReset, fiveHResetAt.Add(time.Minute))
	if candidate.skipReason != "" {
		t.Fatalf("expected account schedulable after 5h reset, got skip reason %s", candidate.skipReason)
	}
}

func TestAccountPoolService_MarkAccountUsageLimitExceeded_NonFreeWeeklyWindowBlocksUntilWeeklyReset(t *testing.T) {
	svc, _ := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	fiveHResetAt := time.Now().Add(2 * time.Hour).Round(time.Second)
	weeklyResetAt := time.Now().Add(5 * 24 * time.Hour).Round(time.Second)
	fiveHUsed := float64(20)

	acc, err := svc.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:       "chatgpt_refresh_token",
		AccountName:        "acc-team-weekly",
		CredentialRaw:      `{"refresh_token":"rt-team-weekly","chatgpt_account_id":"acct-team-weekly"}`,
		Priority:           10,
		Enabled:            true,
		State:              "active",
		PlanType:           "team",
		Quota5HUsedPercent: &fiveHUsed,
		Quota5HResetAt:     &fiveHResetAt,
		QuotaWeeklyResetAt: &weeklyResetAt,
		QuotaStatus:        quotaStatusOK,
	})
	if err != nil {
		t.Fatalf("create account failed: %v", err)
	}

	if err := svc.MarkAccountUsageLimitExceeded(ctx, acc.ID, "usage limited", "team", weeklyResetAt); err != nil {
		t.Fatalf("MarkAccountUsageLimitExceeded failed: %v", err)
	}

	runtimeRecord, err := svc.GetAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("GetAccount failed: %v", err)
	}
	if runtimeRecord == nil {
		t.Fatal("expected runtime record")
	}
	if runtimeRecord.QuotaWeeklyUsedPercent == nil || *runtimeRecord.QuotaWeeklyUsedPercent != 100 {
		t.Fatalf("expected weekly window marked exhausted, got %+v", runtimeRecord.QuotaWeeklyUsedPercent)
	}
	if runtimeRecord.Quota5HUsedPercent == nil || *runtimeRecord.Quota5HUsedPercent != fiveHUsed {
		t.Fatalf("expected 5h window preserved, got %+v", runtimeRecord.Quota5HUsedPercent)
	}

	beforeWeeklyReset := cloneUpstreamAccountRecord(runtimeRecord)
	beforeWeeklyReset.State = "active"
	beforeWeeklyReset.CooldownUntil = nil
	candidate := classifySchedulableAccount(beforeWeeklyReset, weeklyResetAt.Add(-time.Minute))
	if candidate.skipReason != "quota_exhausted_until_reset" {
		t.Fatalf("expected weekly exhausted skip, got %+v", candidate)
	}

	afterWeeklyReset := cloneUpstreamAccountRecord(runtimeRecord)
	afterWeeklyReset.State = "active"
	afterWeeklyReset.CooldownUntil = nil
	candidate = classifySchedulableAccount(afterWeeklyReset, weeklyResetAt.Add(time.Minute))
	if candidate.skipReason != "" {
		t.Fatalf("expected account schedulable after weekly reset, got %+v", candidate)
	}
}

func TestAccountPoolService_MarkAccountUsageLimitExceeded_NonFreeUnknownWindowFallsBackToRuntimeCooldownOnly(t *testing.T) {
	svc, _ := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	resetAt := time.Now().Add(3 * time.Hour).Round(time.Second)
	acc, err := svc.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "chatgpt_refresh_token",
		AccountName:   "acc-team-unknown",
		CredentialRaw: `{"refresh_token":"rt-team-unknown","chatgpt_account_id":"acct-team-unknown"}`,
		Priority:      10,
		Enabled:       true,
		State:         "active",
		PlanType:      "team",
		QuotaStatus:   quotaStatusOK,
	})
	if err != nil {
		t.Fatalf("create account failed: %v", err)
	}

	if err := svc.MarkAccountUsageLimitExceeded(ctx, acc.ID, "usage limited", "team", resetAt); err != nil {
		t.Fatalf("MarkAccountUsageLimitExceeded failed: %v", err)
	}

	runtimeRecord, err := svc.GetAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("GetAccount failed: %v", err)
	}
	if runtimeRecord == nil {
		t.Fatal("expected runtime record")
	}
	if runtimeRecord.QuotaStatus != quotaStatusUnavailable {
		t.Fatalf("expected quota status downgraded to unavailable on unknown window, got %s", runtimeRecord.QuotaStatus)
	}
	if runtimeRecord.Quota5HUsedPercent != nil || runtimeRecord.QuotaWeeklyUsedPercent != nil {
		t.Fatalf("expected no quota window mutation on unknown window, got %+v", runtimeRecord)
	}
	if runtimeRecord.CooldownUntil == nil || runtimeRecord.CooldownUntil.Unix() != resetAt.Unix() {
		t.Fatalf("expected runtime cooldown until resetAt, got %+v", runtimeRecord.CooldownUntil)
	}
}

func TestAccountPoolService_MarkAccountUsageLimitExceeded_UnknownWindowClearsDirtyExhaustedSkipState(t *testing.T) {
	svc, _ := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	oldWeeklyResetAt := time.Now().Add(5 * 24 * time.Hour).Round(time.Second)
	resetAt := time.Now().Add(3 * time.Hour).Round(time.Second)
	weeklyUsed := float64(100)

	acc, err := svc.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:           "chatgpt_refresh_token",
		AccountName:            "acc-team-dirty-unknown",
		CredentialRaw:          `{"refresh_token":"rt-team-dirty","chatgpt_account_id":"acct-team-dirty"}`,
		Priority:               10,
		Enabled:                true,
		State:                  "active",
		PlanType:               "team",
		QuotaStatus:            quotaStatusExhausted,
		QuotaWeeklyUsedPercent: &weeklyUsed,
		QuotaWeeklyResetAt:     &oldWeeklyResetAt,
	})
	if err != nil {
		t.Fatalf("create account failed: %v", err)
	}

	if err := svc.MarkAccountUsageLimitExceeded(ctx, acc.ID, "usage limited", "team", resetAt); err != nil {
		t.Fatalf("MarkAccountUsageLimitExceeded failed: %v", err)
	}

	runtimeRecord, err := svc.GetAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("GetAccount failed: %v", err)
	}
	if runtimeRecord == nil {
		t.Fatal("expected runtime record")
	}
	if runtimeRecord.QuotaStatus != quotaStatusUnavailable {
		t.Fatalf("expected dirty exhausted status downgraded to unavailable, got %s", runtimeRecord.QuotaStatus)
	}
	if runtimeRecord.CooldownUntil == nil || runtimeRecord.CooldownUntil.Unix() != resetAt.Unix() {
		t.Fatalf("expected runtime cooldown until resetAt, got %+v", runtimeRecord.CooldownUntil)
	}

	afterCooldown := cloneUpstreamAccountRecord(runtimeRecord)
	afterCooldown.State = "active"
	afterCooldown.CooldownUntil = nil
	candidate := classifySchedulableAccount(afterCooldown, resetAt.Add(time.Minute))
	if candidate.skipReason != "" {
		t.Fatalf("expected account schedulable after unknown-window cooldown, got %+v", candidate)
	}
}

func TestAccountPoolService_MarkAccountUsageLimitExceeded_ProfilePersistFailureStillLeavesRuntimeCooldown(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	acc, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "chatgpt_refresh_token",
		AccountName:   "acc-profile-fail",
		CredentialRaw: `{"refresh_token":"rt-profile-fail","chatgpt_account_id":"acct-profile-fail"}`,
		Priority:      10,
		Enabled:       true,
		State:         "active",
		PlanType:      "free",
	})
	if err != nil {
		t.Fatalf("create account failed: %v", err)
	}

	failingStore := &failingUpdateStore{inner: st, failProfileUpdate: true}
	svc.store = failingStore

	resetAt := time.Now().Add(2 * time.Hour).Round(time.Second)
	err = svc.MarkAccountUsageLimitExceeded(ctx, acc.ID, "usage limited", "free", resetAt)
	if err == nil {
		t.Fatal("expected MarkAccountUsageLimitExceeded to return profile persist error")
	}

	runtimeRecord, getErr := svc.GetAccount(ctx, acc.ID)
	if getErr != nil {
		t.Fatalf("GetAccount failed: %v", getErr)
	}
	if runtimeRecord == nil {
		t.Fatal("expected runtime record")
	}
	if runtimeRecord.State != "cooldown" {
		t.Fatalf("expected runtime cooldown despite profile persist failure, got %s", runtimeRecord.State)
	}
	if runtimeRecord.CooldownUntil == nil || runtimeRecord.CooldownUntil.Unix() != resetAt.Unix() {
		t.Fatalf("expected runtime cooldown until resetAt, got %+v", runtimeRecord.CooldownUntil)
	}

	waitForPersistedAccountState(t, st, acc.ID, func(record *store.UpstreamAccountRecord) bool {
		return record != nil &&
			record.State == "cooldown" &&
			record.CooldownUntil != nil &&
			record.CooldownUntil.Unix() == resetAt.Unix()
	})
}

func TestAccountPoolService_GetAccount_NormalizesExpiredCooldownState(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()
	pastCooldown := time.Now().Add(-5 * time.Minute).Round(time.Second)

	acc, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "chatgpt_refresh_token",
		AccountName:   "acc-expired-cooldown",
		CredentialRaw: `{"refresh_token":"rt-expired","chatgpt_account_id":"acct-expired"}`,
		Priority:      10,
		Enabled:       true,
		State:         "cooldown",
		CooldownUntil: &pastCooldown,
		FailCount:     2,
	})
	if err != nil {
		t.Fatalf("create account failed: %v", err)
	}

	runtimeRecord, err := svc.GetAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("GetAccount failed: %v", err)
	}
	if runtimeRecord == nil {
		t.Fatal("expected runtime record")
	}
	if runtimeRecord.State != "active" {
		t.Fatalf("expected active state after cooldown expiry, got %s", runtimeRecord.State)
	}
	if runtimeRecord.CooldownUntil != nil {
		t.Fatalf("expected cooldown_until cleared in runtime view, got %+v", runtimeRecord.CooldownUntil)
	}
	if runtimeRecord.FailCount != 2 {
		t.Fatalf("expected fail_count preserved, got %d", runtimeRecord.FailCount)
	}

	records, err := svc.ListAccounts(ctx, true)
	if err != nil {
		t.Fatalf("ListAccounts failed: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected one account, got %d", len(records))
	}
	if records[0].State != "active" {
		t.Fatalf("expected active state in list view, got %s", records[0].State)
	}
	if records[0].CooldownUntil != nil {
		t.Fatalf("expected list view cooldown_until cleared, got %+v", records[0].CooldownUntil)
	}
}
