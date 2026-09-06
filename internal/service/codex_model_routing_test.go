package service

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"cc-forwarder/internal/modelrouting"
	"cc-forwarder/internal/store"

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
