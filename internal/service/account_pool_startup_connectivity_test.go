package service

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"sync"
	"testing"

	"sinuo/internal/store"
)

func TestRunStartupConnectivityChecks_FiltersDisabledOrCredentiallessAccounts(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	first := mustCreateStartupConnectivityAccount(t, st, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "eligible-a",
		CredentialRaw: "sk-eligible-a",
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	second := mustCreateStartupConnectivityAccount(t, st, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "eligible-b",
		CredentialRaw: "sk-eligible-b",
		Priority:      20,
		Enabled:       true,
		State:         "active",
	})
	_ = mustCreateStartupConnectivityAccount(t, st, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "disabled",
		CredentialRaw: "sk-disabled",
		Priority:      30,
		Enabled:       false,
		State:         "inactive",
	})
	_ = mustCreateStartupConnectivityAccount(t, st, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "missing-credential",
		CredentialRaw: "   ",
		Priority:      40,
		Enabled:       true,
		State:         "active",
	})

	var (
		mu        sync.Mutex
		checkedID []int64
	)
	svc.startupConnectivityTestRunner = func(ctx context.Context, id int64) error {
		mu.Lock()
		checkedID = append(checkedID, id)
		mu.Unlock()
		return nil
	}

	result := svc.RunStartupConnectivityChecks(ctx, 4)

	sort.Slice(checkedID, func(i, j int) bool { return checkedID[i] < checkedID[j] })
	wantChecked := []int64{first.ID, second.ID}
	if !reflect.DeepEqual(checkedID, wantChecked) {
		t.Fatalf("unexpected checked ids: got %v want %v", checkedID, wantChecked)
	}
	if result.Total != 2 {
		t.Fatalf("expected total eligible accounts to be 2, got %d", result.Total)
	}
	if result.SuccessCount != 2 {
		t.Fatalf("expected 2 successful checks, got %d", result.SuccessCount)
	}
	if result.FailureCount != 0 {
		t.Fatalf("expected 0 failed checks, got %d", result.FailureCount)
	}
	if result.SkippedCount != 2 {
		t.Fatalf("expected 2 skipped accounts, got %d", result.SkippedCount)
	}
}

func TestRunStartupConnectivityChecks_ContinuesAfterSingleAccountFailure(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	failing := mustCreateStartupConnectivityAccount(t, st, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "failing",
		CredentialRaw: "sk-failing",
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	successful := mustCreateStartupConnectivityAccount(t, st, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "successful",
		CredentialRaw: "sk-successful",
		Priority:      20,
		Enabled:       true,
		State:         "active",
	})

	expectedErr := errors.New("synthetic startup connectivity failure")
	var (
		mu        sync.Mutex
		checkedID []int64
	)
	svc.startupConnectivityTestRunner = func(ctx context.Context, id int64) error {
		mu.Lock()
		checkedID = append(checkedID, id)
		mu.Unlock()
		if id == failing.ID {
			return expectedErr
		}
		return nil
	}

	result := svc.RunStartupConnectivityChecks(ctx, 2)

	sort.Slice(checkedID, func(i, j int) bool { return checkedID[i] < checkedID[j] })
	wantChecked := []int64{failing.ID, successful.ID}
	if !reflect.DeepEqual(checkedID, wantChecked) {
		t.Fatalf("unexpected checked ids: got %v want %v", checkedID, wantChecked)
	}
	if result.SuccessCount != 1 {
		t.Fatalf("expected 1 successful check, got %d", result.SuccessCount)
	}
	if result.FailureCount != 1 {
		t.Fatalf("expected 1 failed check, got %d", result.FailureCount)
	}
	if len(result.Failures) != 1 {
		t.Fatalf("expected 1 failure detail, got %d", len(result.Failures))
	}
	if result.Failures[0].AccountID != failing.ID {
		t.Fatalf("expected failure to belong to account %d, got %d", failing.ID, result.Failures[0].AccountID)
	}
	if result.Failures[0].Message != expectedErr.Error() {
		t.Fatalf("expected failure message %q, got %q", expectedErr.Error(), result.Failures[0].Message)
	}
}

func TestRunStartupConnectivityCheck_NilServiceReturnsNil(t *testing.T) {
	var svc *AccountPoolService

	if err := svc.runStartupConnectivityCheck(context.Background(), 42); err != nil {
		t.Fatalf("expected nil error for nil service helper, got %v", err)
	}
}

func mustCreateStartupConnectivityAccount(t *testing.T, st *store.SQLiteAccountPoolStore, rec *store.UpstreamAccountRecord) *store.UpstreamAccountRecord {
	t.Helper()

	created, err := st.CreateAccount(context.Background(), rec)
	if err != nil {
		t.Fatalf("CreateAccount failed: %v", err)
	}
	return created
}
