package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"sinuo/internal/accountauth"
	"sinuo/internal/modelrouting"
	"sinuo/internal/store"
)

// ResolveSearchScope 的映射独立于模型路由开关；未配置时继续使用全局 OAuth 编排。
func (s *CodexModelRoutingService) ResolveSearchScope(model string) (CodexModelRouteScope, bool) {
	snap := s.snapshot.Load()
	scope := CodexModelRouteScope{Kind: "global", Model: strings.TrimSpace(model), SnapshotVersion: snapVersion(snap)}
	if snap == nil {
		return scope, false
	}
	if snap.LoadFailed {
		if !snap.Enabled {
			return scope, false
		}
		scope.Kind = "unassigned"
		scope.BindingKind = modelrouting.BindingKindSearch
		scope.UnassignedReason = CodexModelRoutingLoadFailed
		return scope, true
	}
	if snap.Search == nil || !snap.Search.Enabled {
		return scope, false
	}
	g := snap.Groups[snap.Search.GroupID]
	if g == nil {
		return scope, false
	}
	return codexScopeForGroup(snap, g, scope.Model, modelrouting.BindingKindSearch), true
}

func codexScopeForGroup(snap *codexModelRoutingSnapshot, g *codexModelRoutingGroup, model, kind string) CodexModelRouteScope {
	return CodexModelRouteScope{
		Kind: "upstream_group", BindingKind: kind, Model: model, SnapshotVersion: snap.Version,
		GroupID: g.ID, GroupName: g.Name, Members: append([]int64(nil), g.Members...),
		MemberEnabled: cloneMemberEnabled(g.MemberEnabled, g.Members),
		Mode:          g.RouteMode, TargetAccountID: g.RouteTarget, Revision: g.Revision,
	}
}

func (s *CodexModelRoutingService) PrepareSearch(ctx context.Context, model string) (CodexModelRouteResult, bool, error) {
	scope, applies := s.ResolveSearchScope(model)
	if !applies || scope.Kind != "upstream_group" {
		return CodexModelRouteResult{Scope: scope}, applies, nil
	}
	result, err := s.prepareCodexScope(ctx, scope, CodexStandaloneSearchPath)
	return result, true, err
}

// prepareCodexScope 共用模型与搜索映射的候选筛选和负缓存规则。
func (s *CodexModelRoutingService) prepareCodexScope(ctx context.Context, scope CodexModelRouteScope, path string) (CodexModelRouteResult, error) {
	result := CodexModelRouteResult{Scope: scope}
	if s.listAccounts == nil {
		return result, fmt.Errorf("account lister is not initialized")
	}
	accounts, err := s.listAccounts(ctx, true)
	if err != nil {
		return result, err
	}
	byID := make(map[int64]*store.UpstreamAccountRecord, len(accounts))
	for _, a := range accounts {
		if a != nil {
			byID[a.ID] = a
		}
	}
	ids := append([]int64(nil), scope.Members...)
	if scope.Mode != modelrouting.RouteModeAuto && scope.TargetAccountID != 0 {
		ids = append([]int64{scope.TargetAccountID}, filterID(ids, scope.TargetAccountID)...)
	}
	result.Accounts = make([]*store.UpstreamAccountRecord, 0, len(ids))
	result.Candidates = make([]AccountScheduleCandidateDecision, 0, len(ids))
	result.CacheGenerations = make(map[int64]uint64)
	now := time.Now()
	for _, id := range ids {
		a := byID[id]
		candidate := codexRouteCandidate(scope, id, a, now)
		// 只统计确实因冷却被排除的候选，避免把停用、授权失效或固定模式的其他账号算作恢复目标。
		if candidate.Reason == "cooldown" && (result.EarliestCooldownUntil.IsZero() || a.CooldownUntil.Before(result.EarliestCooldownUntil)) {
			result.EarliestCooldownUntil = *a.CooldownUntil
		}
		if candidate.Reason == "" {
			generation, hit := s.negativeState(id, path, scope.Model)
			result.CacheGenerations[id] = generation
			if hit {
				candidate.Reason, candidate.ReasonDetail = "negative_cache_model_unsupported", "该账号暂不支持此模型"
			} else {
				candidate.Decision, candidate.Reason, candidate.ReasonDetail = "eligible", "configured_order", "按模型绑定中的账号顺序"
				if len(result.Accounts) == 0 {
					candidate.Decision = "selected"
				}
				result.Accounts = append(result.Accounts, cloneUpstreamAccountRecord(a))
			}
		}
		result.Candidates = append(result.Candidates, candidate)
	}
	return result, nil
}

func codexRouteCandidate(scope CodexModelRouteScope, id int64, a *store.UpstreamAccountRecord, now time.Time) AccountScheduleCandidateDecision {
	c := AccountScheduleCandidateDecision{AccountID: id, Decision: "skipped"}
	if a != nil {
		c.AccountName, c.ProviderType = displayAccountName(a), a.ProviderType
	}
	switch {
	case scope.Mode == modelrouting.RouteModeManualFixed && id != scope.TargetAccountID:
		c.Reason, c.ReasonDetail = "manual_fixed_other_account", "固定模式只允许目标账号"
	// 暂停只影响自动/优选，固定仍须满足账号自身的所有硬约束。
	case !scope.MemberEnabled[id] && scope.Mode != modelrouting.RouteModeManualFixed:
		c.Reason, c.ReasonDetail = "member_disabled", "该账号已暂停参与此模型组调度"
	case a == nil || !a.Enabled || strings.TrimSpace(a.CredentialRaw) == "" || a.State == "disabled_auth":
		c.Reason, c.ReasonDetail = "account_unavailable", "账号不存在、停用、缺少凭据或授权失效"
	case scope.BindingKind == modelrouting.BindingKindSearch && !accountauth.IsChatGPTOAuthProvider(a.ProviderType):
		c.Reason, c.ReasonDetail = accountScheduleSkipRouteRequiresChatGPTOAuth, "Codex /v1/alpha/search 仅支持 ChatGPT OAuth 账号"
	case a.CooldownUntil != nil && now.Before(*a.CooldownUntil):
		c.Reason, c.ReasonDetail = "cooldown", "账号处于冷却期"
	default:
		eligibility := classifySchedulableAccount(a, now)
		c.Reason, c.ReasonDetail = eligibility.skipReason, eligibility.skipReasonDetail
	}
	return c
}
