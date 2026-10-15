package service

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"sinuo/internal/modelrouting"
	"sinuo/internal/store"

	_ "modernc.org/sqlite"
)

func newCodexModelRoutingServiceForTest(t *testing.T) *CodexModelRoutingService {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "codex-model-routing.db")+"?_foreign_keys=1")
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = db.Close() })

	accounts := []*store.UpstreamAccountRecord{
		{ID: 1, AccountName: "account-one", Enabled: true, CredentialRaw: "credential-one"},
		{ID: 2, AccountName: "account-two", Enabled: true, CredentialRaw: "credential-two"},
	}
	svc := NewCodexModelRoutingService(store.NewSQLiteModelRoutingStore(db), func(context.Context, bool) ([]*store.UpstreamAccountRecord, error) {
		return accounts, nil
	})
	if err := svc.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize failed: %v", err)
	}
	return svc
}

type failOnCodexSettingsReadStore struct {
	store.ModelRoutingStore
	calls  int
	failAt int
}

func (s *failOnCodexSettingsReadStore) GetSettings(ctx context.Context) (*store.ModelRoutingSettingsRecord, error) {
	s.calls++
	if s.calls == s.failAt {
		return nil, errors.New("second settings read should not occur")
	}
	return s.ModelRoutingStore.GetSettings(ctx)
}

func TestCodexModelRouting_InitializeReadsSettingsOnce(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "codex-model-routing-settings-read.db")+"?_foreign_keys=1")
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	base := store.NewSQLiteModelRoutingStore(db)
	if err := base.UpdateSettings(ctx, store.ModelRoutingSettingsPatch{CodexEnabled: boolPtr(true)}); err != nil {
		t.Fatalf("seed settings failed: %v", err)
	}
	st := &failOnCodexSettingsReadStore{ModelRoutingStore: base, failAt: 2}
	svc := NewCodexModelRoutingService(st, nil)
	if err := svc.Initialize(ctx); err != nil {
		t.Fatalf("initialize should not reread settings: %v", err)
	}
	if st.calls != 1 {
		t.Fatalf("expected one settings read, got %d", st.calls)
	}
	if !svc.Enabled() || svc.Status() != ModelRoutingStatusReady {
		t.Fatalf("successful initialization lost enabled state: enabled=%v status=%s", svc.Enabled(), svc.Status())
	}
}

func TestCodexModelRouting_PrepareNegativeCacheAndFencing(t *testing.T) {
	svc := newCodexModelRoutingServiceForTest(t)
	ctx := context.Background()
	id, err := svc.CreateBinding(ctx, CodexModelRouteBindingInput{
		Model: "gpt-5", AccountIDs: []int64{1, 2}, RouteMode: modelrouting.RouteModeManualPreferred,
		RouteTargetID: 2, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create binding failed: %v", err)
	}
	if err := svc.UpdateEnabled(ctx, true); err != nil {
		t.Fatalf("enable routing failed: %v", err)
	}

	prepared, err := svc.Prepare(ctx, " GPT-5 ", "/v1/responses")
	if err != nil {
		t.Fatalf("prepare failed: %v", err)
	}
	if prepared.Scope.Kind != "upstream_group" || len(prepared.Accounts) != 2 || prepared.Accounts[0].ID != 2 || prepared.Accounts[1].ID != 1 {
		t.Fatalf("preferred route should lead with target account: %#v", prepared)
	}
	if prepared.Candidates[0].Decision != "selected" || prepared.Candidates[0].AccountID != 2 {
		t.Fatalf("first candidate should record the selected target: %#v", prepared.Candidates)
	}

	generation := prepared.CacheGenerations[2]
	svc.RecordNegativeHit(2, "/v1/responses", "gpt-5", generation)
	cached, err := svc.Prepare(ctx, "gpt-5", "/v1/responses")
	if err != nil {
		t.Fatalf("prepare with cache failed: %v", err)
	}
	if len(cached.Accounts) != 1 || cached.Accounts[0].ID != 1 {
		t.Fatalf("unsupported account should be skipped by model/path cache: %#v", cached.Accounts)
	}

	// 清除缓存同时推进 generation；在途的旧请求不能把过期失败重新写回。
	svc.ClearNegativeHits(2)
	svc.RecordNegativeHit(2, "/v1/responses", "gpt-5", generation)
	cleared, err := svc.Prepare(ctx, "gpt-5", "/v1/responses")
	if err != nil {
		t.Fatalf("prepare after clear failed: %v", err)
	}
	if len(cleared.Accounts) != 2 || cleared.Accounts[0].ID != 2 {
		t.Fatalf("stale negative-cache write must be fenced: %#v", cleared.Accounts)
	}

	// 更新绑定会发布新版本，旧请求的终态不得覆盖新组运行态。
	if err := svc.UpdateBinding(ctx, id, CodexModelRouteBindingInput{
		Model: "gpt-5", AccountIDs: []int64{1, 2}, RouteMode: modelrouting.RouteModeManualFixed,
		RouteTargetID: 1, Enabled: true,
	}); err != nil {
		t.Fatalf("update binding failed: %v", err)
	}
	if svc.ScopeStillValid(prepared.Scope) {
		t.Fatal("scope resolved before binding update must be invalid")
	}
	svc.Settle(prepared.Scope, 2, "stale")
	if runtime := svc.Runtime(prepared.Scope.GroupID); runtime.LastOutcome != "" {
		t.Fatalf("stale scope wrote runtime after config publish: %#v", runtime)
	}

	fixed, err := svc.Prepare(ctx, "gpt-5", "/v1/responses")
	if err != nil {
		t.Fatalf("prepare fixed route failed: %v", err)
	}
	if len(fixed.Accounts) != 1 || fixed.Accounts[0].ID != 1 {
		t.Fatalf("fixed route must expose exactly its target account: %#v", fixed.Accounts)
	}
	svc.Settle(fixed.Scope, fixed.Accounts[0].ID, AccountScheduleOutcomeAuthFailed)
	if runtime := svc.Runtime(fixed.Scope.GroupID); runtime.LastAccountID != fixed.Accounts[0].ID || runtime.LastOutcome != AccountScheduleOutcomeAuthFailed {
		t.Fatalf("failed terminal outcome should update Codex runtime: %#v", runtime)
	}
}

func TestCodexModelRouting_AccountReferenceGuardIncludesDisabledBindings(t *testing.T) {
	svc := newCodexModelRoutingServiceForTest(t)
	ctx := context.Background()
	if _, err := svc.CreateBinding(ctx, CodexModelRouteBindingInput{
		Model: "gpt-5", AccountIDs: []int64{1}, RouteMode: modelrouting.RouteModeAuto, Enabled: false,
	}); err != nil {
		t.Fatalf("create disabled binding failed: %v", err)
	}
	models, err := svc.ModelsUsingAccount(ctx, 1)
	if err != nil {
		t.Fatalf("find account references failed: %v", err)
	}
	if len(models) != 1 || models[0] != "gpt-5" {
		t.Fatalf("disabled binding must still protect a referenced account from deletion: %v", models)
	}
}

func TestCodexModelRouting_MemberParticipationCanBePaused(t *testing.T) {
	svc := newCodexModelRoutingServiceForTest(t)
	ctx := context.Background()
	if _, err := svc.CreateBinding(ctx, CodexModelRouteBindingInput{
		Model: "gpt-5", AccountIDs: []int64{1, 2}, MemberEnabled: map[int64]bool{1: false, 2: true}, RouteMode: modelrouting.RouteModeAuto, Enabled: true,
	}); err != nil {
		t.Fatalf("create binding failed: %v", err)
	}
	if err := svc.UpdateEnabled(ctx, true); err != nil {
		t.Fatalf("enable routing failed: %v", err)
	}
	prepared, err := svc.Prepare(ctx, "gpt-5", "/v1/responses")
	if err != nil {
		t.Fatalf("prepare failed: %v", err)
	}
	if len(prepared.Accounts) != 1 || prepared.Accounts[0].ID != 2 {
		t.Fatalf("paused member should be excluded: %#v", prepared.Accounts)
	}
	if prepared.Candidates[0].Reason != "member_disabled" {
		t.Fatalf("paused member reason mismatch: %#v", prepared.Candidates)
	}
}

func TestCodexModelRouting_PausedMemberCanBeFixed(t *testing.T) {
	svc := newCodexModelRoutingServiceForTest(t)
	ctx := context.Background()
	if _, err := svc.CreateBinding(ctx, CodexModelRouteBindingInput{
		Model: "gpt-5-fixed", AccountIDs: []int64{1, 2}, MemberEnabled: map[int64]bool{1: false, 2: true}, RouteMode: modelrouting.RouteModeManualFixed, RouteTargetID: 1, Enabled: true,
	}); err != nil {
		t.Fatalf("create binding failed: %v", err)
	}
	if err := svc.UpdateEnabled(ctx, true); err != nil {
		t.Fatalf("enable routing failed: %v", err)
	}
	prepared, err := svc.Prepare(ctx, "gpt-5-fixed", "/v1/responses")
	if err != nil {
		t.Fatalf("prepare failed: %v", err)
	}
	if len(prepared.Accounts) != 1 || prepared.Accounts[0].ID != 1 {
		t.Fatalf("fixed route should allow paused target account: %#v", prepared.Accounts)
	}
	if prepared.Candidates[0].Decision != "selected" || prepared.Candidates[0].Reason == "member_disabled" {
		t.Fatalf("paused fixed target should be selected: %#v", prepared.Candidates)
	}
}

func TestCodexModelRouting_QuotaExhaustedAccountsRespectRouteModes(t *testing.T) {
	for _, mode := range []string{modelrouting.RouteModeAuto, modelrouting.RouteModeManualPreferred, modelrouting.RouteModeManualFixed} {
		for _, path := range []string{"/v1/responses", "/v1/responses/compact"} {
			t.Run(mode+path, func(t *testing.T) {
				svc := newCodexModelRoutingServiceForTest(t)
				ctx := context.Background()
				used := 100.0
				reset := time.Now().Add(time.Hour)
				accounts := []*store.UpstreamAccountRecord{
					{ID: 1, ProviderType: "chatgpt_refresh_token", CredentialRaw: "fixture-quota", Enabled: true, State: "active", PlanType: "plus", QuotaStatus: "exhausted", Quota5HUsedPercent: &used, Quota5HResetAt: &reset},
					{ID: 2, ProviderType: "api_key", CredentialRaw: "fixture-two", Enabled: true, Priority: 10},
					{ID: 3, ProviderType: "api_key", CredentialRaw: "fixture-three", Enabled: true, Priority: 20},
				}
				svc.listAccounts = func(context.Context, bool) ([]*store.UpstreamAccountRecord, error) { return accounts, nil }
				target := int64(1)
				if mode == modelrouting.RouteModeAuto {
					target = 0
				}
				if _, err := svc.CreateBinding(ctx, CodexModelRouteBindingInput{Model: "quota-model", AccountIDs: []int64{1, 3, 2}, RouteMode: mode, RouteTargetID: target, Enabled: true}); err != nil {
					t.Fatal(err)
				}
				if err := svc.UpdateEnabled(ctx, true); err != nil {
					t.Fatal(err)
				}
				prepared, err := svc.Prepare(ctx, "quota-model", path)
				if err != nil {
					t.Fatal(err)
				}
				var selected []int64
				for _, account := range prepared.Accounts {
					selected = append(selected, account.ID)
				}
				want := []int64{3, 2}
				if mode == modelrouting.RouteModeManualFixed {
					want = nil
				}
				if !slices.Equal(selected, want) {
					t.Fatalf("额度耗尽后候选应为 %v，实际为 %v", want, selected)
				}
				if len(prepared.Candidates) == 0 || prepared.Candidates[0].AccountID != 1 || prepared.Candidates[0].Decision != "skipped" || prepared.Candidates[0].Reason != "quota_exhausted_until_reset" {
					t.Fatalf("调度快照未记录额度耗尽原因: %+v", prepared.Candidates)
				}
				if accounts[0].State != "active" || accounts[0].CooldownUntil != nil || accounts[0].FailCount != 0 {
					t.Fatal("资格检查不应修改账号冷却或失败次数")
				}

				// 重置时间已过后，下一次请求应重新允许该账号，无需修改绑定或清除负缓存。
				past := time.Now().Add(-time.Hour)
				accounts[0].Quota5HResetAt = &past
				resumed, err := svc.Prepare(ctx, "quota-model", path)
				if err != nil {
					t.Fatal(err)
				}
				if len(resumed.Accounts) == 0 || resumed.Accounts[0].ID != 1 {
					t.Fatal("额度重置后账号未恢复参与调度")
				}
				if mode == modelrouting.RouteModeManualFixed && len(resumed.Accounts) != 1 {
					t.Fatal("固定模式恢复后只能使用目标账号")
				}
			})
		}
	}
}

func TestCodexModelRouting_QuotaEligibilityBoundaries(t *testing.T) {
	used := 100.0
	reset := time.Now().Add(time.Hour)
	for _, test := range []struct {
		name    string
		mutate  func(*store.UpstreamAccountRecord)
		skipped bool
	}{
		{name: "周额度耗尽", skipped: true, mutate: func(a *store.UpstreamAccountRecord) {
			a.Quota5HUsedPercent, a.Quota5HResetAt = nil, nil
			a.QuotaWeeklyUsedPercent, a.QuotaWeeklyResetAt = &used, &reset
		}},
		{name: "free周额度耗尽", skipped: true, mutate: func(a *store.UpstreamAccountRecord) {
			a.PlanType = "free"
			a.QuotaWeeklyUsedPercent, a.QuotaWeeklyResetAt = &used, &reset
		}},
		{name: "free忽略5h窗口", mutate: func(a *store.UpstreamAccountRecord) { a.PlanType = "free" }},
		{name: "未知额度状态", mutate: func(a *store.UpstreamAccountRecord) { a.QuotaStatus = "unavailable" }},
		{name: "重置时间未知", mutate: func(a *store.UpstreamAccountRecord) { a.Quota5HResetAt = nil }},
		{name: "APIKey沿用无限额", mutate: func(a *store.UpstreamAccountRecord) { a.ProviderType = "api_key" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc := newCodexModelRoutingServiceForTest(t)
			ctx := context.Background()
			account := &store.UpstreamAccountRecord{ID: 1, ProviderType: "chatgpt_refresh_token", CredentialRaw: "fixture-quota", Enabled: true, State: "active", PlanType: "plus", QuotaStatus: "exhausted", Quota5HUsedPercent: &used, Quota5HResetAt: &reset}
			test.mutate(account)
			svc.listAccounts = func(context.Context, bool) ([]*store.UpstreamAccountRecord, error) {
				return []*store.UpstreamAccountRecord{account}, nil
			}
			if _, err := svc.CreateBinding(ctx, CodexModelRouteBindingInput{Model: "quota-model", AccountIDs: []int64{1}, RouteMode: modelrouting.RouteModeAuto, Enabled: true}); err != nil {
				t.Fatal(err)
			}
			if err := svc.UpdateEnabled(ctx, true); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"/v1/responses", "/v1/responses/compact"} {
				prepared, err := svc.Prepare(ctx, "quota-model", path)
				if err != nil {
					t.Fatal(err)
				}
				if (len(prepared.Accounts) == 0) != test.skipped {
					t.Fatalf("%s: 额度资格检查结果不符，期望跳过=%v，候选数=%d", path, test.skipped, len(prepared.Accounts))
				}
			}
		})
	}
}
