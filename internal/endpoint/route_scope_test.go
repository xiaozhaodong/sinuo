package endpoint

import (
	"context"
	"strings"
	"testing"
)

type testRouteScopePolicy struct {
	scope RouteScope
}

func (p testRouteScopePolicy) ResolveScope(string, bool) (RouteScope, bool) { return p.scope, true }
func (testRouteScopePolicy) NoteDecision(RouteScope, string)                {}
func (testRouteScopePolicy) SettleDecision(RouteScope, string, string)      {}
func (p testRouteScopePolicy) ScopeStillValid(scope RouteScope) bool {
	return scope.Key() == p.scope.Key()
}

func TestPrepareRouteCandidates_ModelGroupScopesMembers(t *testing.T) {
	manager := newSchedulerTestManager(t, schedulerTestConfig(true, "a", "b", "c"))
	manager.SetRouteScopePolicy(testRouteScopePolicy{scope: RouteScope{
		Kind: RouteScopeUpstreamGroup, GroupID: 7, GroupName: "primary",
		Members: []string{"b", "missing"}, Mode: RouteModeAuto, FallbackEnabled: true,
		Revision: 3, SnapshotVersion: 1, Model: "claude-sonnet",
	}})
	result := manager.PrepareRouteCandidates(context.Background(), RouteRequestProfile{Path: "/v1/messages", Model: "claude-sonnet"})
	if got := candidateNames(result); len(got) != 1 || got[0] != "b" {
		t.Fatalf("expected only group member b, got %v", got)
	}
	if result.Snapshot.RouteScopeKind != RouteScopeUpstreamGroup || result.Snapshot.RouteScopeGroupID != 7 || result.Snapshot.RequestModel != "claude-sonnet" {
		t.Fatalf("scope fields missing from snapshot: %+v", result.Snapshot)
	}
	if reason, _ := skippedReason(t, result, "missing"); reason != "member_missing" {
		t.Fatalf("expected missing member diagnostic, got %q", reason)
	}
}

func TestGetRouteBlock_UnassignedModelIsExplicit(t *testing.T) {
	manager := newSchedulerTestManager(t, schedulerTestConfig(true, "a"))
	manager.SetRouteScopePolicy(testRouteScopePolicy{scope: RouteScope{
		Kind: RouteScopeUnassigned, Mode: RouteModeManualFixed, UnassignedReason: "model_routing_unassigned",
	}})
	block := manager.GetRouteBlock(RouteRequestProfile{Path: "/v1/messages", Model: "unknown"})
	if block == nil || block.StatusCode != 422 || block.Code != "model_routing_unassigned" {
		t.Fatalf("expected explicit unassigned block, got %+v", block)
	}
}

func TestPrepareRouteCandidates_GroupFixedTargetMustBeMember(t *testing.T) {
	manager := newSchedulerTestManager(t, schedulerTestConfig(true, "a", "b"))
	manager.SetRouteScopePolicy(testRouteScopePolicy{scope: RouteScope{
		Kind: RouteScopeUpstreamGroup, GroupID: 9, Members: []string{"a"},
		Mode: RouteModeManualFixed, Target: "b", FallbackEnabled: false,
		Revision: 1, SnapshotVersion: 1,
	}})
	result := manager.PrepareRouteCandidates(context.Background(), RouteRequestProfile{Path: "/v1/messages", Model: "m"})
	if len(result.Candidates) != 0 {
		t.Fatalf("fixed target outside group must not be selected: %v", candidateNames(result))
	}
	if reason, _ := skippedReason(t, result, "b"); reason != "manual_fixed_target_not_member" {
		t.Fatalf("expected target membership diagnostic, got %q", reason)
	}
}

// TestAutoRetention_RevisionBumpInvalidatesRetained 锁定 retained 的 revision 失效语义：
// 组成员或组内策略变更会 bump RouteRevision，此时上一版 revision 建立的 retained
// 必须立刻停止影响候选排序，否则用户改完配置后仍会被旧的粘滞端点接住。
func TestAutoRetention_RevisionBumpInvalidatesRetained(t *testing.T) {
	manager := newSchedulerTestManager(t, schedulerTestConfig(true, "a", "b"))
	scope := RouteScope{
		Kind: RouteScopeUpstreamGroup, GroupID: 7, GroupName: "primary",
		Members: []string{"a", "b"}, Mode: RouteModeAuto, FallbackEnabled: true,
		Revision: 3, SnapshotVersion: 1,
	}
	manager.SetRouteScopePolicy(testRouteScopePolicy{scope: scope})

	if !manager.UpdateAutoRetentionForScope(scope, "b", 1, "auto_priority") {
		t.Fatal("expected retained to be established for the current revision")
	}
	if got := manager.RetainedInTierForScope(scope.Key(), 1); got != "b" {
		t.Fatalf("retained should be readable at its own revision, got %q", got)
	}

	bumped := scope
	bumped.Revision = 4
	if got := manager.RetainedInTierForScope(bumped.Key(), 1); got != "" {
		t.Fatalf("retained from revision 3 must not leak into revision 4, got %q", got)
	}

	// 快照重建后只保留当前 revision 的作用域，陈旧条目被回收。
	manager.PruneAutoRetentionScopes(map[string]struct{}{bumped.Key(): {}})
	if got := manager.RetainedInTierForScope(scope.Key(), 1); got != "" {
		t.Fatalf("stale revision scope should have been pruned, got %q", got)
	}
}

// TestResolveRouteScope_OnlyAppliesToClaudeMessagesPaths 锁定模型路由的路径边界：
// 只有 /v1/messages 与 /v1/messages/count_tokens 参与模型分组，其他进入端点管线的
// 路径即便 body 带 model 也保持全局路由，不会被误判为未分配。
func TestResolveRouteScope_OnlyAppliesToClaudeMessagesPaths(t *testing.T) {
	manager := newSchedulerTestManager(t, schedulerTestConfig(true, "a"))
	manager.SetRouteScopePolicy(testRouteScopePolicy{scope: RouteScope{
		Kind: RouteScopeUnassigned, UnassignedReason: "model_routing_unassigned",
	}})

	for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens"} {
		scope := manager.ResolveRouteScope(RouteRequestProfile{Path: path, Model: "claude-x"})
		if scope.Kind != RouteScopeUnassigned {
			t.Fatalf("%s should participate in model routing, got %+v", path, scope)
		}
	}
	for _, path := range []string{"/v1/chat/completions", "/v1/messages/foo", "/v1/messages/", ""} {
		scope := manager.ResolveRouteScope(RouteRequestProfile{Path: path, Model: "claude-x"})
		if scope.Kind != RouteScopeGlobal {
			t.Fatalf("%q must stay on global routing, got %+v", path, scope)
		}
		if block := manager.GetRouteBlock(RouteRequestProfile{Path: path, Model: "claude-x"}); block != nil {
			t.Fatalf("%q must not be blocked by model routing, got %+v", path, block)
		}
	}
}

// TestGetRouteBlock_CountTokensUnassignedHintsTokenCounting 锁定 Token 计数关闭时
// count_tokens 透传路径的 fail-closed 契约：仍返回 422，但提示开启 Token 计数可降级估算。
func TestGetRouteBlock_CountTokensUnassignedHintsTokenCounting(t *testing.T) {
	manager := newSchedulerTestManager(t, schedulerTestConfig(true, "a"))
	manager.SetRouteScopePolicy(testRouteScopePolicy{scope: RouteScope{
		Kind: RouteScopeUnassigned, UnassignedReason: "model_routing_unassigned", Model: "claude-x",
	}})
	block := manager.GetRouteBlock(RouteRequestProfile{Path: "/v1/messages/count_tokens", IsCountTokens: true, Model: "claude-x"})
	if block == nil || block.StatusCode != 422 || block.Code != "model_routing_unassigned" {
		t.Fatalf("count_tokens passthrough must stay fail-closed, got %+v", block)
	}
	if !strings.Contains(block.Message, "Token 计数") {
		t.Fatalf("count_tokens block should hint enabling token counting, got %q", block.Message)
	}
	messages := manager.GetRouteBlock(RouteRequestProfile{Path: "/v1/messages", Model: "claude-x"})
	if messages == nil || strings.Contains(messages.Message, "Token 计数") {
		t.Fatalf("/v1/messages block must not carry the count_tokens hint, got %+v", messages)
	}
}
