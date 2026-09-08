package service

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"cc-forwarder/internal/store"
)

type countingAccountPoolStore struct {
	inner store.AccountPoolStore

	mu                   sync.Mutex
	listAccountsCalls    int
	listSchedulableCalls int
}

type flakyTransientFailureStore struct {
	inner store.AccountPoolStore

	mu                sync.Mutex
	transientCalls    int
	failOnTransientAt int
	failAfterWrite    bool
}

type failingUpdateStore struct {
	inner store.AccountPoolStore

	failUpdate        bool
	failToggle        bool
	failProfileUpdate bool
}

func (s *countingAccountPoolStore) CreateAccount(ctx context.Context, record *store.UpstreamAccountRecord) (*store.UpstreamAccountRecord, error) {
	return s.inner.CreateAccount(ctx, record)
}

func (s *countingAccountPoolStore) UpdateAccount(ctx context.Context, record *store.UpstreamAccountRecord) error {
	return s.inner.UpdateAccount(ctx, record)
}

func (s *countingAccountPoolStore) UpdateAccountPriorities(ctx context.Context, updates map[int64]int) error {
	return s.inner.UpdateAccountPriorities(ctx, updates)
}

func (s *countingAccountPoolStore) UpdateAccountScheduling(ctx context.Context, updates map[int64]store.AccountSchedulingUpdate) error {
	return s.inner.UpdateAccountScheduling(ctx, updates)
}

func (s *countingAccountPoolStore) DeleteAccount(ctx context.Context, id int64) error {
	return s.inner.DeleteAccount(ctx, id)
}

func (s *countingAccountPoolStore) GetAccount(ctx context.Context, id int64) (*store.UpstreamAccountRecord, error) {
	return s.inner.GetAccount(ctx, id)
}

func (s *countingAccountPoolStore) ListAccounts(ctx context.Context, includeDisabled bool) ([]*store.UpstreamAccountRecord, error) {
	s.mu.Lock()
	s.listAccountsCalls++
	s.mu.Unlock()
	return s.inner.ListAccounts(ctx, includeDisabled)
}

func (s *countingAccountPoolStore) ListSchedulableAccounts(ctx context.Context, now time.Time) ([]*store.UpstreamAccountRecord, error) {
	s.mu.Lock()
	s.listSchedulableCalls++
	s.mu.Unlock()
	return s.inner.ListSchedulableAccounts(ctx, now)
}

func (s *countingAccountPoolStore) FindAccountByFingerprint(ctx context.Context, fingerprint string) (*store.UpstreamAccountRecord, error) {
	return s.inner.FindAccountByFingerprint(ctx, fingerprint)
}

func (s *countingAccountPoolStore) ToggleAccount(ctx context.Context, id int64, enabled bool) error {
	return s.inner.ToggleAccount(ctx, id, enabled)
}

func (s *countingAccountPoolStore) MarkAccountSuccess(ctx context.Context, id int64, successAt time.Time) error {
	return s.inner.MarkAccountSuccess(ctx, id, successAt)
}

func (s *countingAccountPoolStore) MarkAccountSuccessIfNoNewerFailure(ctx context.Context, id int64, successAt, attemptStartedAt time.Time) (bool, error) {
	return s.inner.MarkAccountSuccessIfNoNewerFailure(ctx, id, successAt, attemptStartedAt)
}

func (s *countingAccountPoolStore) MarkAccountAuthFailed(ctx context.Context, id int64, reason string) error {
	return s.inner.MarkAccountAuthFailed(ctx, id, reason)
}

func (s *countingAccountPoolStore) MarkAccountAuthFailedWithProfile(ctx context.Context, record *store.UpstreamAccountRecord, reason string) error {
	return s.inner.MarkAccountAuthFailedWithProfile(ctx, record, reason)
}

func (s *countingAccountPoolStore) MarkAccountTransientFailure(ctx context.Context, id int64, reason string, cooldownUntil time.Time) error {
	return s.inner.MarkAccountTransientFailure(ctx, id, reason, cooldownUntil)
}

func (s *countingAccountPoolStore) UpdateAccountProfile(ctx context.Context, record *store.UpstreamAccountRecord) error {
	return s.inner.UpdateAccountProfile(ctx, record)
}

func (s *countingAccountPoolStore) SaveAccountRuntimeState(ctx context.Context, id int64, state store.AccountRuntimeState) error {
	return s.inner.SaveAccountRuntimeState(ctx, id, state)
}

func (s *flakyTransientFailureStore) CreateAccount(ctx context.Context, record *store.UpstreamAccountRecord) (*store.UpstreamAccountRecord, error) {
	return s.inner.CreateAccount(ctx, record)
}

func (s *flakyTransientFailureStore) UpdateAccount(ctx context.Context, record *store.UpstreamAccountRecord) error {
	return s.inner.UpdateAccount(ctx, record)
}

func (s *flakyTransientFailureStore) UpdateAccountPriorities(ctx context.Context, updates map[int64]int) error {
	return s.inner.UpdateAccountPriorities(ctx, updates)
}

func (s *flakyTransientFailureStore) UpdateAccountScheduling(ctx context.Context, updates map[int64]store.AccountSchedulingUpdate) error {
	return s.inner.UpdateAccountScheduling(ctx, updates)
}

func (s *flakyTransientFailureStore) DeleteAccount(ctx context.Context, id int64) error {
	return s.inner.DeleteAccount(ctx, id)
}

func (s *flakyTransientFailureStore) GetAccount(ctx context.Context, id int64) (*store.UpstreamAccountRecord, error) {
	return s.inner.GetAccount(ctx, id)
}

func (s *flakyTransientFailureStore) ListAccounts(ctx context.Context, includeDisabled bool) ([]*store.UpstreamAccountRecord, error) {
	return s.inner.ListAccounts(ctx, includeDisabled)
}

func (s *flakyTransientFailureStore) ListSchedulableAccounts(ctx context.Context, now time.Time) ([]*store.UpstreamAccountRecord, error) {
	return s.inner.ListSchedulableAccounts(ctx, now)
}

func (s *flakyTransientFailureStore) FindAccountByFingerprint(ctx context.Context, fingerprint string) (*store.UpstreamAccountRecord, error) {
	return s.inner.FindAccountByFingerprint(ctx, fingerprint)
}

func (s *flakyTransientFailureStore) ToggleAccount(ctx context.Context, id int64, enabled bool) error {
	return s.inner.ToggleAccount(ctx, id, enabled)
}

func (s *flakyTransientFailureStore) MarkAccountSuccess(ctx context.Context, id int64, successAt time.Time) error {
	return s.inner.MarkAccountSuccess(ctx, id, successAt)
}

func (s *flakyTransientFailureStore) MarkAccountSuccessIfNoNewerFailure(ctx context.Context, id int64, successAt, attemptStartedAt time.Time) (bool, error) {
	return s.inner.MarkAccountSuccessIfNoNewerFailure(ctx, id, successAt, attemptStartedAt)
}

func (s *flakyTransientFailureStore) MarkAccountAuthFailed(ctx context.Context, id int64, reason string) error {
	return s.inner.MarkAccountAuthFailed(ctx, id, reason)
}

func (s *flakyTransientFailureStore) MarkAccountAuthFailedWithProfile(ctx context.Context, record *store.UpstreamAccountRecord, reason string) error {
	return s.inner.MarkAccountAuthFailedWithProfile(ctx, record, reason)
}

func (s *flakyTransientFailureStore) MarkAccountTransientFailure(ctx context.Context, id int64, reason string, cooldownUntil time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transientCalls++
	if s.failOnTransientAt > 0 && s.transientCalls == s.failOnTransientAt {
		return context.DeadlineExceeded
	}
	return s.inner.MarkAccountTransientFailure(ctx, id, reason, cooldownUntil)
}

func (s *flakyTransientFailureStore) UpdateAccountProfile(ctx context.Context, record *store.UpstreamAccountRecord) error {
	return s.inner.UpdateAccountProfile(ctx, record)
}

func (s *flakyTransientFailureStore) SaveAccountRuntimeState(ctx context.Context, id int64, state store.AccountRuntimeState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transientCalls++
	fail := s.failOnTransientAt > 0 && s.transientCalls == s.failOnTransientAt
	if fail && !s.failAfterWrite {
		return context.DeadlineExceeded
	}
	if err := s.inner.SaveAccountRuntimeState(ctx, id, state); err != nil {
		return err
	}
	if fail {
		return context.DeadlineExceeded
	}
	return nil
}

func (s *failingUpdateStore) CreateAccount(ctx context.Context, record *store.UpstreamAccountRecord) (*store.UpstreamAccountRecord, error) {
	return s.inner.CreateAccount(ctx, record)
}

func (s *failingUpdateStore) UpdateAccount(ctx context.Context, record *store.UpstreamAccountRecord) error {
	if s.failUpdate {
		return context.DeadlineExceeded
	}
	return s.inner.UpdateAccount(ctx, record)
}

func (s *failingUpdateStore) UpdateAccountPriorities(ctx context.Context, updates map[int64]int) error {
	return s.inner.UpdateAccountPriorities(ctx, updates)
}

func (s *failingUpdateStore) UpdateAccountScheduling(ctx context.Context, updates map[int64]store.AccountSchedulingUpdate) error {
	if s.failUpdate {
		return context.DeadlineExceeded
	}
	return s.inner.UpdateAccountScheduling(ctx, updates)
}

func (s *failingUpdateStore) DeleteAccount(ctx context.Context, id int64) error {
	return s.inner.DeleteAccount(ctx, id)
}

func (s *failingUpdateStore) GetAccount(ctx context.Context, id int64) (*store.UpstreamAccountRecord, error) {
	return s.inner.GetAccount(ctx, id)
}

func (s *failingUpdateStore) ListAccounts(ctx context.Context, includeDisabled bool) ([]*store.UpstreamAccountRecord, error) {
	return s.inner.ListAccounts(ctx, includeDisabled)
}

func (s *failingUpdateStore) ListSchedulableAccounts(ctx context.Context, now time.Time) ([]*store.UpstreamAccountRecord, error) {
	return s.inner.ListSchedulableAccounts(ctx, now)
}

func (s *failingUpdateStore) FindAccountByFingerprint(ctx context.Context, fingerprint string) (*store.UpstreamAccountRecord, error) {
	return s.inner.FindAccountByFingerprint(ctx, fingerprint)
}

func (s *failingUpdateStore) ToggleAccount(ctx context.Context, id int64, enabled bool) error {
	if s.failToggle {
		return context.DeadlineExceeded
	}
	return s.inner.ToggleAccount(ctx, id, enabled)
}

func (s *failingUpdateStore) MarkAccountSuccess(ctx context.Context, id int64, successAt time.Time) error {
	return s.inner.MarkAccountSuccess(ctx, id, successAt)
}

func (s *failingUpdateStore) MarkAccountSuccessIfNoNewerFailure(ctx context.Context, id int64, successAt, attemptStartedAt time.Time) (bool, error) {
	return s.inner.MarkAccountSuccessIfNoNewerFailure(ctx, id, successAt, attemptStartedAt)
}

func (s *failingUpdateStore) MarkAccountAuthFailed(ctx context.Context, id int64, reason string) error {
	return s.inner.MarkAccountAuthFailed(ctx, id, reason)
}

func (s *failingUpdateStore) MarkAccountAuthFailedWithProfile(ctx context.Context, record *store.UpstreamAccountRecord, reason string) error {
	return s.inner.MarkAccountAuthFailedWithProfile(ctx, record, reason)
}

func (s *failingUpdateStore) MarkAccountTransientFailure(ctx context.Context, id int64, reason string, cooldownUntil time.Time) error {
	return s.inner.MarkAccountTransientFailure(ctx, id, reason, cooldownUntil)
}

func (s *failingUpdateStore) UpdateAccountProfile(ctx context.Context, record *store.UpstreamAccountRecord) error {
	if s.failProfileUpdate {
		return context.DeadlineExceeded
	}
	return s.inner.UpdateAccountProfile(ctx, record)
}

func (s *failingUpdateStore) SaveAccountRuntimeState(ctx context.Context, id int64, state store.AccountRuntimeState) error {
	return s.inner.SaveAccountRuntimeState(ctx, id, state)
}

func TestPrepareSchedulableAccounts_UsesRuntimeCacheAfterInitialLoad(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	if _, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "cached-main",
		CredentialRaw: "sk-main",
		Priority:      10,
		Enabled:       true,
		State:         "active",
	}); err != nil {
		t.Fatalf("create account failed: %v", err)
	}

	countingStore := &countingAccountPoolStore{inner: st}
	svc = NewAccountPoolService(countingStore, nil)
	t.Cleanup(func() { _ = svc.Close() })

	if _, err := svc.PrepareSchedulableAccounts(ctx, "req-1", "/v1/responses"); err != nil {
		t.Fatalf("PrepareSchedulableAccounts first call failed: %v", err)
	}
	if _, err := svc.PrepareSchedulableAccounts(ctx, "req-2", "/v1/responses"); err != nil {
		t.Fatalf("PrepareSchedulableAccounts second call failed: %v", err)
	}

	countingStore.mu.Lock()
	listAccountsCalls := countingStore.listAccountsCalls
	listSchedulableCalls := countingStore.listSchedulableCalls
	countingStore.mu.Unlock()

	if listAccountsCalls != 1 {
		t.Fatalf("expected one initial ListAccounts call, got %d", listAccountsCalls)
	}
	if listSchedulableCalls != 0 {
		t.Fatalf("expected runtime cache to avoid ListSchedulableAccounts, got %d", listSchedulableCalls)
	}
}

func TestMarkAccountTransientFailure_UpdatesRuntimeImmediatelyAndFlushesAsync(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	rec, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "async-failure",
		CredentialRaw: "sk-async",
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create account failed: %v", err)
	}

	if err := svc.MarkAccountTransientFailure(ctx, rec.ID, "upstream failed", 2*time.Minute); err != nil {
		t.Fatalf("MarkAccountTransientFailure failed: %v", err)
	}

	runtimeRecord, err := svc.GetAccount(ctx, rec.ID)
	if err != nil {
		t.Fatalf("GetAccount failed: %v", err)
	}
	if runtimeRecord == nil {
		t.Fatal("expected runtime record")
	}
	if runtimeRecord.State != "cooldown" {
		t.Fatalf("expected runtime state cooldown, got %s", runtimeRecord.State)
	}
	if runtimeRecord.FailCount != 1 {
		t.Fatalf("expected runtime fail_count 1, got %d", runtimeRecord.FailCount)
	}
	if runtimeRecord.CooldownUntil == nil {
		t.Fatal("expected runtime cooldown_until to be set")
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		dbRecord, getErr := st.GetAccount(ctx, rec.ID)
		if getErr == nil && dbRecord != nil && dbRecord.State == "cooldown" && dbRecord.FailCount == 1 && dbRecord.CooldownUntil != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected async writer to flush cooldown state, last record=%+v err=%v", dbRecord, getErr)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestAccountRuntimeCache_MergeProfilePreservesRuntimeState(t *testing.T) {
	cache := newAccountRuntimeCache()
	startedAt := time.Now().Add(2 * time.Minute).Round(time.Second)
	cooldownUntil := startedAt.Add(5 * time.Minute)
	lastSuccessAt := startedAt.Add(-time.Minute)

	cache.replaceAll([]*store.UpstreamAccountRecord{
		{
			ID:                     1,
			ProviderType:           "chatgpt_refresh_token",
			AccountName:            "oauth",
			CredentialRaw:          "old-credential",
			Priority:               10,
			Enabled:                true,
			State:                  "cooldown",
			CooldownUntil:          &cooldownUntil,
			FailCount:              3,
			LastSuccessAt:          &lastSuccessAt,
			LastError:              "latest stream error",
			PlanType:               "free",
			ChatGPTAccountID:       "old-account",
			QuotaStatus:            quotaStatusUnavailable,
			QuotaWeeklyUsedPercent: float64Ptr(20),
			UpdatedAt:              startedAt,
		},
	})

	cache.mergeRecordPreservingRuntimeState(&store.UpstreamAccountRecord{
		ID:                     1,
		ProviderType:           "chatgpt_refresh_token",
		AccountName:            "oauth",
		CredentialRaw:          "new-credential",
		Priority:               10,
		Enabled:                true,
		State:                  "active",
		PlanType:               "plus",
		ChatGPTAccountID:       "new-account",
		QuotaStatus:            quotaStatusOK,
		QuotaWeeklyUsedPercent: float64Ptr(80),
	})

	record, ok := cache.get(1)
	if !ok || record == nil {
		t.Fatal("expected merged record in cache")
	}
	if record.State != "cooldown" {
		t.Fatalf("expected runtime state preserved, got %s", record.State)
	}
	if record.FailCount != 3 {
		t.Fatalf("expected fail_count preserved, got %d", record.FailCount)
	}
	if record.LastError != "latest stream error" {
		t.Fatalf("expected last_error preserved, got %s", record.LastError)
	}
	if record.CredentialRaw != "new-credential" {
		t.Fatalf("expected credential updated, got %s", record.CredentialRaw)
	}
	if record.PlanType != "plus" {
		t.Fatalf("expected plan_type updated, got %s", record.PlanType)
	}
	if record.ChatGPTAccountID != "new-account" {
		t.Fatalf("expected account id updated, got %s", record.ChatGPTAccountID)
	}
	if record.QuotaStatus != quotaStatusOK {
		t.Fatalf("expected quota status updated, got %s", record.QuotaStatus)
	}
}

func TestAccountPoolRuntimeWriter_SkipsStaleDetachedMutation(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	record, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "stale-detached",
		CredentialRaw: "sk-stale-detached",
		BaseURL:       "https://api.openai.com",
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create account failed: %v", err)
	}
	cache := svc.runtimeCache
	cache.replaceAll([]*store.UpstreamAccountRecord{record})

	writer := &accountPoolRuntimeWriter{
		store:            st,
		pending:          make(map[int64]accountRuntimeMutation),
		isCurrentVersion: cache.matchesStateVersion,
	}

	failedAt := time.Now()
	_, failureVersion := cache.markTransientFailure(record.ID, "stream failed", failedAt.Add(time.Minute), failedAt)
	state, ok := cache.snapshotState(record.ID, failureVersion)
	if !ok {
		t.Fatal("无法捕获失败快照")
	}
	if err := writer.enqueue(accountRuntimeMutation{accountID: record.ID, kind: accountRuntimeMutationTransientFailure, stateVersion: failureVersion, state: state}); err != nil {
		t.Fatal(err)
	}
	detached := writer.detachPending()
	if len(detached) != 1 {
		t.Fatalf("expected one detached mutation, got %d", len(detached))
	}

	successAt := failedAt.Add(2 * time.Second)
	if ok, _ := cache.markSuccess(record.ID, successAt); !ok {
		t.Fatal("expected markSuccess to update runtime cache")
	}

	mutation := detached[0]
	if err := writer.applyMutation(ctx, &mutation); err != nil {
		t.Fatalf("applyMutation failed: %v", err)
	}

	current, err := st.GetAccount(ctx, record.ID)
	if err != nil {
		t.Fatalf("GetAccount failed: %v", err)
	}
	if current.State != "active" {
		t.Fatalf("expected stale detached mutation to be skipped, got state=%s", current.State)
	}
	if current.FailCount != 0 {
		t.Fatalf("expected fail_count to remain 0, got %d", current.FailCount)
	}
}

func TestUpdateAccount_PreservesRuntimeStateInCache(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	record, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "preserve-runtime",
		CredentialRaw: "sk-old",
		BaseURL:       "https://api.openai.com",
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create account failed: %v", err)
	}

	if err := svc.MarkAccountTransientFailure(ctx, record.ID, "stream failed", time.Minute); err != nil {
		t.Fatalf("MarkAccountTransientFailure failed: %v", err)
	}

	updated := cloneUpstreamAccountRecord(record)
	updated.AccountName = "preserve-runtime-updated"
	updated.CredentialRaw = "sk-new"
	updated.BaseURL = "https://example.com"
	if err := svc.UpdateAccount(ctx, updated); err != nil {
		t.Fatalf("UpdateAccount failed: %v", err)
	}

	current, err := svc.GetAccount(ctx, record.ID)
	if err != nil {
		t.Fatalf("GetAccount failed: %v", err)
	}
	if current == nil {
		t.Fatal("expected updated runtime record")
	}
	if current.AccountName != "preserve-runtime-updated" || current.CredentialRaw != "sk-new" || current.BaseURL != "https://example.com" {
		t.Fatalf("expected editable fields updated, got %+v", current)
	}
	if current.State != "cooldown" {
		t.Fatalf("expected runtime state preserved, got %s", current.State)
	}
	if current.FailCount != 1 {
		t.Fatalf("expected fail_count preserved, got %d", current.FailCount)
	}
	if current.CooldownUntil == nil {
		t.Fatal("expected cooldown_until preserved")
	}
}

func TestUpdateAccount_AppliesEnabledToggle(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	record, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "toggle-via-update",
		CredentialRaw: "sk-toggle",
		BaseURL:       "https://api.openai.com",
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create account failed: %v", err)
	}

	updated := cloneUpstreamAccountRecord(record)
	updated.Enabled = false
	if err := svc.UpdateAccount(ctx, updated); err != nil {
		t.Fatalf("UpdateAccount disable failed: %v", err)
	}

	current, err := svc.GetAccount(ctx, record.ID)
	if err != nil {
		t.Fatalf("GetAccount after disable failed: %v", err)
	}
	if current == nil || current.Enabled {
		t.Fatalf("expected account disabled via update, got %+v", current)
	}

	updated.Enabled = true
	if err := svc.UpdateAccount(ctx, updated); err != nil {
		t.Fatalf("UpdateAccount enable failed: %v", err)
	}

	current, err = svc.GetAccount(ctx, record.ID)
	if err != nil {
		t.Fatalf("GetAccount after enable failed: %v", err)
	}
	if current == nil || !current.Enabled {
		t.Fatalf("expected account re-enabled via update, got %+v", current)
	}
	if current.State != "active" {
		t.Fatalf("expected enabled account to become active, got state=%s", current.State)
	}
}

func TestUpdateAccount_DoesNotDoubleCountPendingTransientFailure(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	record, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "update-no-double-fail",
		CredentialRaw: "sk-update-no-double-fail",
		BaseURL:       "https://api.openai.com",
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create account failed: %v", err)
	}

	manualWriter := &accountPoolRuntimeWriter{
		ctx:              context.Background(),
		cancel:           func() {},
		store:            st,
		pending:          make(map[int64]accountRuntimeMutation),
		isCurrentVersion: svc.runtimeCache.matchesStateVersion,
	}
	svc.runtimeWriter = manualWriter

	if err := svc.MarkAccountTransientFailure(ctx, record.ID, "stream failed", time.Minute); err != nil {
		t.Fatalf("MarkAccountTransientFailure failed: %v", err)
	}

	updated := cloneUpstreamAccountRecord(record)
	updated.AccountName = "update-no-double-fail-updated"
	if err := svc.UpdateAccount(ctx, updated); err != nil {
		t.Fatalf("UpdateAccount failed: %v", err)
	}

	dbRecord, err := st.GetAccount(ctx, record.ID)
	if err != nil {
		t.Fatalf("GetAccount before flush failed: %v", err)
	}
	if dbRecord.FailCount != 0 {
		t.Fatalf("expected DB fail_count to remain 0 before pending flush, got %d", dbRecord.FailCount)
	}

	detached := manualWriter.detachPending()
	if len(detached) != 1 {
		t.Fatalf("expected one pending mutation, got %d", len(detached))
	}
	mutation := detached[0]
	if err := manualWriter.applyMutation(ctx, &mutation); err != nil {
		t.Fatalf("applyMutation failed: %v", err)
	}

	dbRecord, err = st.GetAccount(ctx, record.ID)
	if err != nil {
		t.Fatalf("GetAccount after flush failed: %v", err)
	}
	if dbRecord.FailCount != 1 {
		t.Fatalf("expected fail_count to be 1 after single pending flush, got %d", dbRecord.FailCount)
	}
	if dbRecord.State != "cooldown" {
		t.Fatalf("expected state cooldown after single pending flush, got %s", dbRecord.State)
	}

	runtimeRecord, err := svc.GetAccount(ctx, record.ID)
	if err != nil {
		t.Fatalf("GetAccount runtime failed: %v", err)
	}
	if runtimeRecord == nil {
		t.Fatal("expected runtime record")
	}
	if runtimeRecord.FailCount != 1 {
		t.Fatalf("expected runtime fail_count to remain 1, got %d", runtimeRecord.FailCount)
	}
	if runtimeRecord.AccountName != "update-no-double-fail-updated" {
		t.Fatalf("expected editable update to remain applied, got %s", runtimeRecord.AccountName)
	}
}

func TestUpdateAccount_ReenableSkipsDetachedAuthFailedMutation(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	record, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "chatgpt_refresh_token",
		AccountName:   "reenable-after-auth-failed",
		CredentialRaw: "rt-reenable",
		BaseURL:       "https://chatgpt.com",
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create account failed: %v", err)
	}

	manualWriter := &accountPoolRuntimeWriter{
		ctx:              context.Background(),
		cancel:           func() {},
		store:            st,
		pending:          make(map[int64]accountRuntimeMutation),
		isCurrentVersion: svc.runtimeCache.matchesStateVersion,
	}
	svc.runtimeWriter = manualWriter

	if err := svc.MarkAccountAuthFailed(ctx, record.ID, "oauth invalid"); err != nil {
		t.Fatalf("MarkAccountAuthFailed failed: %v", err)
	}

	detached := manualWriter.detachPending()
	if len(detached) != 1 {
		t.Fatalf("expected one pending auth_failed mutation, got %d", len(detached))
	}

	updated := cloneUpstreamAccountRecord(record)
	updated.Enabled = true
	updated.AccountName = "reenabled-account"
	if err := svc.UpdateAccount(ctx, updated); err != nil {
		t.Fatalf("UpdateAccount re-enable failed: %v", err)
	}

	mutation := detached[0]
	if err := manualWriter.applyMutation(ctx, &mutation); err != nil {
		t.Fatalf("applyMutation failed: %v", err)
	}

	dbRecord, err := st.GetAccount(ctx, record.ID)
	if err != nil {
		t.Fatalf("GetAccount after flush failed: %v", err)
	}
	if dbRecord == nil {
		t.Fatal("expected persisted account")
	}
	if !dbRecord.Enabled {
		t.Fatalf("expected DB record to remain enabled after stale auth_failed flush, got %+v", dbRecord)
	}
	if dbRecord.State != "active" {
		t.Fatalf("expected DB state to remain active, got %s", dbRecord.State)
	}
	if dbRecord.AccountName != "reenabled-account" {
		t.Fatalf("expected editable update to persist, got %s", dbRecord.AccountName)
	}

	runtimeRecord, err := svc.GetAccount(ctx, record.ID)
	if err != nil {
		t.Fatalf("GetAccount runtime failed: %v", err)
	}
	if runtimeRecord == nil {
		t.Fatal("expected runtime account")
	}
	if !runtimeRecord.Enabled || runtimeRecord.State != "active" {
		t.Fatalf("expected runtime account enabled/active, got %+v", runtimeRecord)
	}
}

func TestUpdateAccount_ReenableRollbackPreservesPendingAuthFailedMutation(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	record, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "chatgpt_refresh_token",
		AccountName:   "reenable-auth-failed-rollback",
		CredentialRaw: "rt-reenable-rollback",
		BaseURL:       "https://chatgpt.com",
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create account failed: %v", err)
	}

	failingStore := &failingUpdateStore{inner: st, failUpdate: true}
	manualWriter := &accountPoolRuntimeWriter{
		ctx:              context.Background(),
		cancel:           func() {},
		store:            failingStore,
		pending:          make(map[int64]accountRuntimeMutation),
		isCurrentVersion: svc.runtimeCache.matchesStateVersion,
	}
	svc.runtimeWriter = manualWriter
	svc.store = failingStore

	if err := svc.MarkAccountAuthFailed(ctx, record.ID, "oauth invalid"); err != nil {
		t.Fatalf("MarkAccountAuthFailed failed: %v", err)
	}

	detached := manualWriter.detachPending()
	if len(detached) != 1 {
		t.Fatalf("expected one pending auth_failed mutation, got %d", len(detached))
	}

	updated := cloneUpstreamAccountRecord(record)
	updated.Enabled = true
	updated.AccountName = "should-not-persist"
	if err := svc.UpdateAccount(ctx, updated); err == nil {
		t.Fatal("expected UpdateAccount to fail")
	}

	runtimeRecord, err := svc.GetAccount(ctx, record.ID)
	if err != nil {
		t.Fatalf("GetAccount after failed update failed: %v", err)
	}
	if runtimeRecord == nil || runtimeRecord.Enabled || runtimeRecord.State != "disabled_auth" {
		t.Fatalf("expected runtime to roll back to auth_failed state, got %+v", runtimeRecord)
	}

	mutation := detached[0]
	if err := manualWriter.applyMutation(ctx, &mutation); err != nil {
		t.Fatalf("applyMutation failed: %v", err)
	}

	dbRecord, err := st.GetAccount(ctx, record.ID)
	if err != nil {
		t.Fatalf("GetAccount after auth_failed flush failed: %v", err)
	}
	if dbRecord == nil || dbRecord.Enabled || dbRecord.State != "disabled_auth" {
		t.Fatalf("expected DB auth_failed to persist after failed update rollback, got %+v", dbRecord)
	}
	if dbRecord.AccountName != "reenable-auth-failed-rollback" {
		t.Fatalf("expected editable update not to persist on failed update, got %s", dbRecord.AccountName)
	}
}

func TestAccountPoolRuntimeWriter_RetriesRuntimeSnapshotIdempotently(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	record, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "partial-retry",
		CredentialRaw: "sk-partial-retry",
		BaseURL:       "https://api.openai.com",
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create account failed: %v", err)
	}

	cache := svc.runtimeCache
	cache.replaceAll([]*store.UpstreamAccountRecord{record})
	failedAt := time.Now()
	var stateVersion uint64
	for i := 0; i < 3; i++ {
		_, stateVersion = cache.markTransientFailure(record.ID, "stream failed", failedAt.Add(time.Minute), failedAt)
	}

	flakyStore := &flakyTransientFailureStore{
		inner:             st,
		failOnTransientAt: 1,
		failAfterWrite:    true,
	}
	writer := &accountPoolRuntimeWriter{
		store:            flakyStore,
		pending:          make(map[int64]accountRuntimeMutation),
		isCurrentVersion: cache.matchesStateVersion,
	}

	state, ok := cache.snapshotState(record.ID, stateVersion)
	if !ok {
		t.Fatal("无法捕获失败快照")
	}
	mutation := accountRuntimeMutation{
		accountID: record.ID, kind: accountRuntimeMutationTransientFailure,
		stateVersion: stateVersion, state: state,
	}

	if err := writer.applyMutation(ctx, &mutation); err == nil {
		t.Fatal("expected error after the snapshot was committed")
	}
	committed, err := st.GetAccount(ctx, record.ID)
	if err != nil || committed.FailCount != 3 {
		t.Fatalf("expected the first attempt to commit count=3, err=%v", err)
	}

	flakyStore.failOnTransientAt = 0
	for i := 0; i < 2; i++ {
		if err := writer.applyMutation(ctx, &mutation); err != nil {
			t.Fatalf("retry applyMutation failed: %v", err)
		}
	}

	current, err := st.GetAccount(ctx, record.ID)
	if err != nil {
		t.Fatalf("GetAccount failed: %v", err)
	}
	if current.FailCount != 3 {
		t.Fatalf("expected repeated snapshots to keep fail_count=3, got %d", current.FailCount)
	}
}

func TestToggleAccount_FailedPersistRestoresRuntimeStateVersion(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	record, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "toggle-auth-failed-rollback",
		CredentialRaw: "rt-toggle-rollback",
		BaseURL:       "https://chatgpt.com",
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create account failed: %v", err)
	}

	failingStore := &failingUpdateStore{inner: st, failToggle: true}
	manualWriter := &accountPoolRuntimeWriter{
		ctx:              context.Background(),
		cancel:           func() {},
		store:            failingStore,
		pending:          make(map[int64]accountRuntimeMutation),
		isCurrentVersion: svc.runtimeCache.matchesStateVersion,
	}
	svc.runtimeWriter = manualWriter
	svc.store = failingStore

	if err := svc.MarkAccountAuthFailed(ctx, record.ID, "oauth invalid"); err != nil {
		t.Fatalf("MarkAccountAuthFailed failed: %v", err)
	}

	detached := manualWriter.detachPending()
	if len(detached) != 1 {
		t.Fatalf("expected one pending auth_failed mutation, got %d", len(detached))
	}

	if err := svc.ToggleAccount(ctx, record.ID, true); err == nil {
		t.Fatal("expected ToggleAccount to fail")
	}

	runtimeRecord, err := svc.GetAccount(ctx, record.ID)
	if err != nil {
		t.Fatalf("GetAccount after failed toggle failed: %v", err)
	}
	if runtimeRecord == nil || runtimeRecord.Enabled || runtimeRecord.State != "disabled_auth" {
		t.Fatalf("expected runtime to roll back to auth_failed state after failed toggle, got %+v", runtimeRecord)
	}

	mutation := detached[0]
	if err := manualWriter.applyMutation(ctx, &mutation); err != nil {
		t.Fatalf("applyMutation failed: %v", err)
	}

	dbRecord, err := st.GetAccount(ctx, record.ID)
	if err != nil {
		t.Fatalf("GetAccount after auth_failed flush failed: %v", err)
	}
	if dbRecord == nil || dbRecord.Enabled || dbRecord.State != "disabled_auth" {
		t.Fatalf("expected DB auth_failed to persist after failed toggle rollback, got %+v", dbRecord)
	}
}

func TestToggleAccount_FailedPersistRestoresPinnedSelectionState(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()

	first, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "primary-a",
		CredentialRaw: "sk-primary-a",
		BaseURL:       "https://api.openai.com",
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create first account failed: %v", err)
	}
	second, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "primary-b",
		CredentialRaw: "sk-primary-b",
		BaseURL:       "https://api.openai.com",
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create second account failed: %v", err)
	}

	changed, err := svc.PinAccountSelection(ctx, second.ID)
	if err != nil {
		t.Fatalf("PinAccountSelection failed: %v", err)
	}
	if !changed {
		t.Fatal("expected pinning second account to change runtime selection")
	}

	failingStore := &failingUpdateStore{inner: st, failToggle: true}
	svc.store = failingStore

	if err := svc.ToggleAccount(ctx, second.ID, false); err == nil {
		t.Fatal("expected ToggleAccount to fail")
	}

	activeAccountID, ok, err := svc.GetActiveSelectionAccountID(ctx)
	if err != nil {
		t.Fatalf("GetActiveSelectionAccountID failed: %v", err)
	}
	if !ok || activeAccountID != second.ID {
		t.Fatalf("expected pinned selection to be restored, got ok=%v activeAccountID=%d", ok, activeAccountID)
	}

	orderedSchedule, err := svc.PrepareSchedulableAccounts(ctx, "req-toggle-rollback-selection", "/v1/responses")
	if err != nil {
		t.Fatalf("PrepareSchedulableAccounts failed: %v", err)
	}
	ordered := orderedSchedule.Accounts
	if got, want := collectAccountIDs(ordered), []int64{second.ID, first.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected pinned account order restored after failed toggle persist, got %v want %v", got, want)
	}
}

func TestAccountRuntimeCache_SoftFailurePolicyCopiesOverrideValues(t *testing.T) {
	cache := newAccountRuntimeCache()
	window, threshold := 10, 5
	record := &store.UpstreamAccountRecord{ID: 1, AccountName: "policy", ProviderType: "api_key", Enabled: true, State: "active", SoftFailureWindowMinutes: &window, SoftFailureThreshold: &threshold}
	cache.upsert(record)
	window = 20
	first, ok := cache.get(1)
	if !ok || *first.SoftFailureWindowMinutes != 10 {
		t.Fatal("发布账号配置时共享了外部指针")
	}
	if !cache.mergeRecordPreservingRuntimeState(record) {
		t.Fatal("更新缓存失败")
	}
	window = 30
	second, ok := cache.get(1)
	if !ok || *second.SoftFailureWindowMinutes != 20 {
		t.Fatal("更新账号配置时共享了外部指针")
	}
	*second.SoftFailureThreshold = 99
	third, ok := cache.get(1)
	if !ok || *third.SoftFailureThreshold != 5 {
		t.Fatal("读取配置暴露了运行态可变指针")
	}
	cache.replaceAll([]*store.UpstreamAccountRecord{record})
	window = 40
	last, ok := cache.get(1)
	if !ok || *last.SoftFailureWindowMinutes != 30 {
		t.Fatal("全量加载配置时共享了外部指针")
	}
}
