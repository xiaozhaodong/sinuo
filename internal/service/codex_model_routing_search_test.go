package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"sinuo/internal/accountauth"
	"sinuo/internal/modelrouting"
	"sinuo/internal/store"
)

func TestCodexSearchScope_BehaviorMatrix(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, state := range []string{"absent", "disabled", "enabled"} {
			for _, failed := range []bool{false, true} {
				t.Run(fmt.Sprintf("enabled=%v/%s/failed=%v", enabled, state, failed), func(t *testing.T) {
					s, _ := newCodexSearchService(t)
					ctx := context.Background()
					if state != "absent" {
						in := codexSearchInput()
						in.Enabled = state == "enabled"
						if _, err := s.CreateBinding(ctx, in); err != nil {
							t.Fatal(err)
						}
					}
					if err := s.UpdateEnabled(ctx, enabled); err != nil {
						t.Fatal(err)
					}
					snap := *s.CurrentSnapshot()
					snap.LoadFailed = failed
					s.snapshot.Store(&snap)
					scope, applies := s.ResolveSearchScope(" request-model ")
					wantApplies := enabled && failed || !failed && state == "enabled"
					wantKind := "global"
					if enabled && failed {
						wantKind = "unassigned"
					} else if wantApplies {
						wantKind = "upstream_group"
					}
					if applies != wantApplies || scope.Kind != wantKind || scope.Model != "request-model" {
						t.Fatalf("矩阵不符: %+v applies=%v", scope, applies)
					}
					result, prepared, err := s.PrepareSearch(ctx, "request-model")
					if err != nil || prepared != wantApplies || result.Scope.Kind != wantKind {
						t.Fatalf("PrepareSearch 矩阵不符: %+v %v %v", result, prepared, err)
					}
					if wantKind == "unassigned" && scope.UnassignedReason != CodexModelRoutingLoadFailed {
						t.Fatalf("加载失败必须保留本地拒绝原因: %+v", scope)
					}
					if wantKind == "upstream_group" && (scope.BindingKind != "search" || len(result.Accounts) != 2) {
						t.Fatalf("必须命中搜索成员: %+v", result)
					}
				})
			}
		}
	}
}

func TestCodexSearchScope_EmptyMissingGroupAndReservedModel(t *testing.T) {
	s, _ := newCodexSearchService(t)
	s.snapshot.Store(nil)
	if scope, applies := s.ResolveSearchScope(""); applies || scope.Kind != "global" {
		t.Fatalf("%+v %v", scope, applies)
	}
	if err := s.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateBinding(context.Background(), codexSearchInput()); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateEnabled(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if scope, applies := s.ResolveScope(modelrouting.SearchKey); !applies || scope.Kind != "unassigned" {
		t.Fatalf("保留键不能被 model 请求命中: %+v %v", scope, applies)
	}
	snap := *s.CurrentSnapshot()
	snap.Groups = nil
	s.snapshot.Store(&snap)
	if scope, applies := s.ResolveSearchScope("gpt"); applies || scope.Kind != "global" {
		t.Fatalf("%+v %v", scope, applies)
	}
}

func TestCodexSearchPrepare_OAuthModesAndNegativeCache(t *testing.T) {
	for _, mode := range []string{"auto", "manual_preferred", "manual_fixed"} {
		t.Run(mode, func(t *testing.T) {
			s, accounts := newCodexSearchService(t)
			in := codexSearchInput()
			in.RouteMode = mode
			if mode != "auto" {
				in.RouteTargetID = 1
			}
			if _, err := s.CreateBinding(context.Background(), in); err != nil {
				t.Fatal(err)
			}
			// 模拟创建后账号 provider 变化或历史成员，不允许进入搜索热路径。
			accounts[0].ProviderType = accountauth.ProviderAPIKey
			result, applies, err := s.PrepareSearch(context.Background(), "gpt")
			if err != nil || !applies || result.Candidates[0].Reason != accountScheduleSkipRouteRequiresChatGPTOAuth || result.Candidates[0].ReasonDetail != "Codex /v1/alpha/search 仅支持 ChatGPT OAuth 账号" {
				t.Fatalf("非 OAuth 成员必须跳过: %+v %v %v", result, applies, err)
			}
			if mode == "manual_fixed" {
				if len(result.Accounts) != 0 {
					t.Fatalf("固定目标不可用不得回落: %+v", result)
				}
				return
			}
			if len(result.Accounts) != 1 || result.Accounts[0].ID != 3 {
				t.Fatalf("必须只剩 OAuth: %+v", result)
			}
			s.RecordNegativeHit(3, CodexStandaloneSearchPath, "gpt", result.CacheGenerations[3])
			cached, _, err := s.PrepareSearch(context.Background(), "gpt")
			if err != nil || len(cached.Accounts) != 0 || cached.Candidates[1].Reason != "negative_cache_model_unsupported" {
				t.Fatalf("%+v %v", cached, err)
			}
			other, _, err := s.PrepareSearch(context.Background(), "other")
			if err != nil || len(other.Accounts) != 1 {
				t.Fatalf("负缓存不能串模型: %+v %v", other, err)
			}
		})
	}
}

func TestCodexSearchSettle_OffSwitchAndFencing(t *testing.T) {
	s, _ := newCodexSearchService(t)
	ctx := context.Background()
	id, err := s.CreateBinding(ctx, codexSearchInput())
	if err != nil {
		t.Fatal(err)
	}
	result, _, err := s.PrepareSearch(ctx, "")
	if err != nil || !s.ScopeStillValid(result.Scope) {
		t.Fatalf("%+v %v", result, err)
	}
	s.Settle(result.Scope, 1, "success")
	if got := s.Runtime(result.Scope.GroupID); got.LastAccountID != 1 || got.LastDecisionAt.IsZero() {
		t.Fatalf("关闭开关仍需记录搜索命中: %+v", got)
	}
	modelScope := result.Scope
	modelScope.BindingKind = "model"
	if s.ScopeStillValid(modelScope) {
		t.Fatal("关闭开关时普通模型 scope 不应有效")
	}
	s.Settle(modelScope, 3, "should-not-record")
	if s.Runtime(result.Scope.GroupID).LastAccountID != 1 {
		t.Fatal("关闭开关不能记录普通模型")
	}
	in := codexSearchInput()
	in.Note = "新修订"
	if err := s.UpdateBinding(ctx, id, in); err != nil {
		t.Fatal(err)
	}
	if s.ScopeStillValid(result.Scope) {
		t.Fatal("旧版本 scope 必须失效")
	}
	s.Settle(result.Scope, 3, "stale")
	if s.Runtime(result.Scope.GroupID).LastAccountID != 0 {
		t.Fatal("旧 scope 不能写入新运行态")
	}
	current, _ := s.ResolveSearchScope("")
	current.Revision--
	if s.ScopeStillValid(current) {
		t.Fatal("组修订号不符必须失效")
	}
}

func TestCodexPrepare_EarliestCooldownUntil(t *testing.T) {
	late := time.Now().Add(90 * time.Second)
	early := late.Add(-45 * time.Second)
	for _, kind := range []string{modelrouting.BindingKindModel, modelrouting.BindingKindSearch} {
		for _, tc := range []struct {
			name   string
			change func(*CodexModelRouteScope, []*store.UpstreamAccountRecord)
			want   time.Time
		}{
			{name: "earliest_in_group", want: early},
			{name: "fixed_target_only", change: func(scope *CodexModelRouteScope, _ []*store.UpstreamAccountRecord) {
				scope.Mode, scope.TargetAccountID = modelrouting.RouteModeManualFixed, 1
			}, want: late},
			{name: "preferred_can_recover_via_other_member", change: func(scope *CodexModelRouteScope, _ []*store.UpstreamAccountRecord) {
				scope.Mode, scope.TargetAccountID = modelrouting.RouteModeManualPreferred, 1
			}, want: early},
			{name: "paused_member_excluded", change: func(scope *CodexModelRouteScope, _ []*store.UpstreamAccountRecord) {
				scope.MemberEnabled[2] = false
			}, want: late},
			{name: "disabled_account_excluded", change: func(_ *CodexModelRouteScope, accounts []*store.UpstreamAccountRecord) {
				accounts[1].Enabled = false
			}, want: late},
			{name: "invalid_auth_excluded", change: func(_ *CodexModelRouteScope, accounts []*store.UpstreamAccountRecord) {
				accounts[1].State = "disabled_auth"
			}, want: late},
			{name: "missing_credential_excluded", change: func(_ *CodexModelRouteScope, accounts []*store.UpstreamAccountRecord) {
				accounts[1].CredentialRaw = ""
			}, want: late},
			{name: "all_disabled_has_no_recovery_time", change: func(_ *CodexModelRouteScope, accounts []*store.UpstreamAccountRecord) {
				accounts[0].Enabled, accounts[1].Enabled = false, false
			}},
			{name: "fixed_disabled_does_not_use_other_cooldown", change: func(scope *CodexModelRouteScope, accounts []*store.UpstreamAccountRecord) {
				scope.Mode, scope.TargetAccountID = modelrouting.RouteModeManualFixed, 1
				accounts[0].Enabled = false
			}},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				accounts := []*store.UpstreamAccountRecord{
					{ID: 1, AccountName: "late", ProviderType: "oauth", Enabled: true, CredentialRaw: "test-one", CooldownUntil: &late},
					{ID: 2, AccountName: "early", ProviderType: "oauth", Enabled: true, CredentialRaw: "test-two", CooldownUntil: &early},
				}
				scope := CodexModelRouteScope{
					Kind: "upstream_group", BindingKind: kind, Mode: modelrouting.RouteModeAuto,
					Members: []int64{1, 2}, MemberEnabled: map[int64]bool{1: true, 2: true},
				}
				if tc.change != nil {
					tc.change(&scope, accounts)
				}
				s := NewCodexModelRoutingService(nil, func(context.Context, bool) ([]*store.UpstreamAccountRecord, error) {
					return accounts, nil
				})
				path := "/v1/responses"
				if kind == modelrouting.BindingKindSearch {
					path = CodexStandaloneSearchPath
				}
				result, err := s.prepareCodexScope(context.Background(), scope, path)
				if err != nil || !result.EarliestCooldownUntil.Equal(tc.want) || len(result.Accounts) != 0 {
					t.Fatalf("错误的恢复时间或可用账号: result=%+v want=%v err=%v", result, tc.want, err)
				}
			})
		}
	}
}
