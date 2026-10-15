package service

import (
	"context"
	"testing"
	"time"

	"sinuo/internal/store"
)

func TestAccountMetadataCompatibility_UpdateRefreshesRuntimeWithoutResettingState(t *testing.T) {
	svc, st, _ := newTestAccountPoolServiceWithSettings(t)
	ctx := context.Background()
	account, err := svc.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType: "api_key", AccountName: "metadata-runtime", CredentialRaw: "fixture-credential", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PinAccountSelection(ctx, account.ID); err != nil {
		t.Fatal(err)
	}
	before, err := svc.PrepareSchedulableAccounts(ctx, "before-edit", "/v1/responses")
	if err != nil || len(before.Accounts) != 1 {
		t.Fatalf("prepare before edit: %v", err)
	}
	account.StripCodexInternalMetadata = true
	if err := svc.UpdateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	after, err := svc.PrepareSchedulableAccounts(ctx, "after-edit", "/v1/responses")
	if err != nil || len(after.Accounts) != 1 {
		t.Fatalf("prepare after edit: %v", err)
	}
	if !after.Accounts[0].StripCodexInternalMetadata || !after.Pinned || after.Accounts[0].ID != account.ID {
		t.Fatal("next request must use the new setting and preserve the pinned account")
	}
	if before.Accounts[0].StripCodexInternalMetadata {
		t.Fatal("editing the account mutated an existing request snapshot")
	}
	if err := svc.MarkAccountTransientFailure(ctx, account.ID, "fixture failure", time.Minute); err != nil {
		t.Fatal(err)
	}
	cooled, err := svc.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	cooled.StripCodexInternalMetadata = false
	if err := svc.UpdateAccount(ctx, cooled); err != nil {
		t.Fatal(err)
	}
	preserved := func(got *store.UpstreamAccountRecord) bool {
		return got != nil && !got.StripCodexInternalMetadata && got.State == cooled.State && got.FailCount == cooled.FailCount && got.LastError == cooled.LastError && got.CooldownUntil != nil && got.CooldownUntil.Truncate(time.Microsecond).Equal(cooled.CooldownUntil.Truncate(time.Microsecond))
	}
	cached, err := svc.GetAccount(ctx, account.ID)
	if err != nil || !preserved(cached) {
		t.Fatalf("metadata edit changed cached cooldown or failure state: %v", err)
	}
	// 运行态按既有异步 writer 落库；编辑不能清除运行态，也不能被晚到写入覆盖新开关。
	deadline := time.Now().Add(2 * time.Second)
	for {
		got, err := st.GetAccount(ctx, account.ID)
		if err != nil {
			t.Fatal(err)
		}
		if preserved(got) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("metadata setting and runtime state were not persisted together")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAccountMetadataCompatibility_LocalInvalidRequestFinalizesSnapshot(t *testing.T) {
	svc, _ := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()
	svc.SaveLatestScheduleSnapshot(&LatestAccountScheduleSnapshot{RequestID: "invalid-local-request", CapturedAt: time.Now()})
	final, err := svc.CompleteLatestScheduleSnapshot(ctx, "invalid-local-request", 1, "fixture-account", "invalid_request", "codex_metadata_invalid_request")
	if err != nil || final == nil || final.FinalOutcome != "invalid_request" {
		t.Fatalf("invalid request snapshot was not completed: %v", err)
	}
	svc.SaveLatestScheduleSnapshot(&LatestAccountScheduleSnapshot{RequestID: "next-request", CapturedAt: time.Now()})
	late, err := svc.CompleteLatestScheduleSnapshot(ctx, "invalid-local-request", 1, "fixture-account", AccountScheduleOutcomeSuccess, "")
	if err != nil || late != nil {
		t.Fatalf("finalized local error still accepted a late attempt: snapshot=%v err=%v", late, err)
	}
}
