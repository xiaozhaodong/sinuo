package endpoint

import (
	"context"
	"testing"

	"cc-forwarder/config"
)

// oauthSchedulerConfig 构造一组端点，authModes 与 names 一一对应，priority 依次递增
func oauthSchedulerConfig(names []string, authModes []string) *config.Config {
	cfg := schedulerTestConfig(true, names...)
	for i := range cfg.Endpoints {
		cfg.Endpoints[i].AuthMode = authModes[i]
	}
	return cfg
}

func TestClaudeCodeEntryContextMarker(t *testing.T) {
	if IsClaudeCodeEntry(context.Background()) {
		t.Fatal("未标记的上下文不应视为 Claude Code 入口")
	}
	if !IsClaudeCodeEntry(WithClaudeCodeEntry(context.Background(), ClaudeCodeEntryRequest{})) {
		t.Fatal("标记后应识别为 Claude Code 入口")
	}
	var nilCtx context.Context
	if IsClaudeCodeEntry(nilCtx) {
		t.Fatal("nil 上下文不应视为 Claude Code 入口")
	}
}

func TestPrepareRouteCandidates_ClientOAuthOnlyForClaudeCodeEntry(t *testing.T) {
	manager := newSchedulerTestManager(t, oauthSchedulerConfig(
		[]string{"subscription", "vendor"},
		[]string{config.EndpointAuthModeClientOAuth, config.EndpointAuthModeConfigured},
	))

	mainPort := manager.PrepareRouteCandidates(context.Background(), RouteRequestProfile{Path: "/v1/messages"})
	if got := candidateNames(mainPort); len(got) != 1 || got[0] != "vendor" {
		t.Fatalf("主端口请求只能用端点凭据端点，got %v", got)
	}
	if reason, _ := skippedReason(t, mainPort, "subscription"); reason != routeSkipClientOAuthOutsideEntry {
		t.Fatalf("订阅直通端点跳过原因 = %q", reason)
	}

	entry := manager.PrepareRouteCandidates(context.Background(), RouteRequestProfile{Path: "/v1/messages", ClaudeCodeEntry: true})
	if got := candidateNames(entry); len(got) != 1 || got[0] != "subscription" {
		t.Fatalf("入口请求以首个候选（订阅直通）为准，不得混入 API 端点，got %v", got)
	}
	if reason, _ := skippedReason(t, entry, "vendor"); reason != routeSkipCrossAuthBoundary {
		t.Fatalf("跨认证方式候选跳过原因 = %q", reason)
	}
}

func TestPrepareRouteCandidates_AuthBoundaryKeepsSameModeFallbacks(t *testing.T) {
	manager := newSchedulerTestManager(t, oauthSchedulerConfig(
		[]string{"vendor-a", "subscription", "vendor-b"},
		[]string{config.EndpointAuthModeConfigured, config.EndpointAuthModeClientOAuth, ""},
	))

	result := manager.PrepareRouteCandidates(context.Background(), RouteRequestProfile{Path: "/v1/messages", ClaudeCodeEntry: true})
	got := candidateNames(result)
	if len(got) != 2 || got[0] != "vendor-a" || got[1] != "vendor-b" {
		t.Fatalf("首个候选是端点凭据时只保留同类候选（空认证方式按 configured），got %v", got)
	}
	if len(result.Plans) != len(result.Candidates) {
		t.Fatalf("Plans 与 Candidates 必须同步裁剪: plans=%d candidates=%d", len(result.Plans), len(result.Candidates))
	}
	if reason, _ := skippedReason(t, result, "subscription"); reason != routeSkipCrossAuthBoundary {
		t.Fatalf("订阅直通端点跳过原因 = %q", reason)
	}
}

func TestGetRouteBlock_ManualFixedClientOAuthOutsideEntry(t *testing.T) {
	manager := newSchedulerTestManager(t, oauthSchedulerConfig(
		[]string{"subscription"},
		[]string{config.EndpointAuthModeClientOAuth},
	))
	manager.SetClaudeRoutingOverride(RouteOverrideState{Mode: RouteModeManualFixed, EndpointName: "subscription"})

	block := manager.GetRouteBlock(RouteRequestProfile{Path: "/v1/messages"})
	if block == nil || block.Code != routeSkipClientOAuthOutsideEntry {
		t.Fatalf("主端口请求固定到订阅直通端点应得到明确阻断，got %+v", block)
	}
	if block := manager.GetRouteBlock(RouteRequestProfile{Path: "/v1/messages", ClaudeCodeEntry: true}); block != nil {
		t.Fatalf("入口请求不应被阻断，got %+v", block)
	}
}

func TestFirstLogicalCandidateSkipsClientOAuthOutsideEntry(t *testing.T) {
	manager := newSchedulerTestManager(t, oauthSchedulerConfig(
		[]string{"subscription", "vendor"},
		[]string{config.EndpointAuthModeClientOAuth, config.EndpointAuthModeConfigured},
	))
	if name := manager.firstLogicalCandidateName(RouteRequestProfile{Path: "/v1/messages"}); name != "vendor" {
		t.Fatalf("主端口请求的第一逻辑候选应跳过订阅直通端点，got %q", name)
	}
	if name := manager.firstLogicalCandidateName(RouteRequestProfile{Path: "/v1/messages", ClaudeCodeEntry: true}); name != "subscription" {
		t.Fatalf("入口请求的第一逻辑候选应为订阅直通端点，got %q", name)
	}
}

func entryProfile(mode string, classOverride bool) RouteRequestProfile {
	return RouteRequestProfile{Path: "/v1/messages", ClaudeCodeEntry: true, ClaudeCodeEntryMode: mode, ClaudeCodeEntryClassOverride: classOverride}
}

func TestEntryRouteDecision(t *testing.T) {
	cases := []struct {
		name        string
		clientOAuth bool
		profile     RouteRequestProfile
		wantOK      bool
		wantReason  string
	}{
		{"主端口 + 端点凭据", false, RouteRequestProfile{}, true, ""},
		{"主端口 + 订阅直通", true, RouteRequestProfile{}, false, routeSkipClientOAuthOutsideEntry},
		{"自动 + 订阅直通", true, entryProfile(ClaudeCodeEntryModeAuto, false), true, ""},
		{"自动 + 端点凭据", false, entryProfile(ClaudeCodeEntryModeAuto, false), true, ""},
		{"仅订阅 + 端点凭据", false, entryProfile(ClaudeCodeEntryModeSubscription, false), false, routeSkipEntryModeSubscriptionOnly},
		{"仅订阅 + 订阅直通", true, entryProfile(ClaudeCodeEntryModeSubscription, false), true, ""},
		{"仅 API + 订阅直通", true, entryProfile(ClaudeCodeEntryModeAPI, false), false, routeSkipEntryModeAPIOnly},
		{"类别覆盖 API + 订阅直通", true, entryProfile(ClaudeCodeEntryModeAPI, true), false, routeSkipEntryRequestClassAPI},
		{"仅 API + 端点凭据", false, entryProfile(ClaudeCodeEntryModeAPI, false), true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason := entryRouteDecision(tc.clientOAuth, tc.profile)
			if ok != tc.wantOK || reason != tc.wantReason {
				t.Fatalf("got ok=%v reason=%q, want ok=%v reason=%q", ok, reason, tc.wantOK, tc.wantReason)
			}
		})
	}
}

func TestPrepareRouteCandidates_ClaudeCodeEntryModes(t *testing.T) {
	manager := newSchedulerTestManager(t, oauthSchedulerConfig(
		[]string{"subscription", "vendor"},
		[]string{config.EndpointAuthModeClientOAuth, config.EndpointAuthModeConfigured},
	))

	apiOnly := manager.PrepareRouteCandidates(context.Background(), entryProfile(ClaudeCodeEntryModeAPI, false))
	if got := candidateNames(apiOnly); len(got) != 1 || got[0] != "vendor" {
		t.Fatalf("仅 API 模式只能用第三方端点，got %v", got)
	}
	if reason, _ := skippedReason(t, apiOnly, "subscription"); reason != routeSkipEntryModeAPIOnly {
		t.Fatalf("订阅直通端点跳过原因 = %q", reason)
	}

	subscriptionOnly := manager.PrepareRouteCandidates(context.Background(), entryProfile(ClaudeCodeEntryModeSubscription, false))
	if got := candidateNames(subscriptionOnly); len(got) != 1 || got[0] != "subscription" {
		t.Fatalf("仅订阅模式只能用订阅直通端点，got %v", got)
	}
	if reason, _ := skippedReason(t, subscriptionOnly, "vendor"); reason != routeSkipEntryModeSubscriptionOnly {
		t.Fatalf("第三方端点跳过原因 = %q", reason)
	}
}

func TestGetRouteBlock_ClaudeCodeModeWithoutCompatibleEndpoint(t *testing.T) {
	manager := newSchedulerTestManager(t, oauthSchedulerConfig(
		[]string{"vendor"},
		[]string{config.EndpointAuthModeConfigured},
	))

	block := manager.GetRouteBlock(entryProfile(ClaudeCodeEntryModeSubscription, false))
	if block == nil || block.Code != RouteBlockCodeClaudeCodeModeNoEndpoint || block.StatusCode != 422 {
		t.Fatalf("仅订阅模式下没有订阅直通端点应返回明确的 422，got %+v", block)
	}
	if block := manager.GetRouteBlock(entryProfile(ClaudeCodeEntryModeAPI, false)); block != nil {
		t.Fatalf("存在符合模式的端点时不应阻断，got %+v", block)
	}
	if block := manager.GetRouteBlock(entryProfile(ClaudeCodeEntryModeAuto, false)); block != nil {
		t.Fatalf("自动模式不做模式阻断，got %+v", block)
	}
}

func TestGetRouteBlock_ManualFixedConflictsWithEntryMode(t *testing.T) {
	manager := newSchedulerTestManager(t, oauthSchedulerConfig(
		[]string{"subscription", "vendor"},
		[]string{config.EndpointAuthModeClientOAuth, config.EndpointAuthModeConfigured},
	))
	manager.SetClaudeRoutingOverride(RouteOverrideState{Mode: RouteModeManualFixed, EndpointName: "subscription"})

	block := manager.GetRouteBlock(entryProfile(ClaudeCodeEntryModeAPI, false))
	if block == nil || block.Code != routeSkipEntryModeAPIOnly || block.Reason != "manual_fixed_"+routeSkipEntryModeAPIOnly {
		t.Fatalf("仅 API 模式下手动固定到订阅直通端点应得到明确阻断，got %+v", block)
	}
}

func TestNormalizeClaudeCodeAPIRequestClasses(t *testing.T) {
	got, err := NormalizeClaudeCodeAPIRequestClasses([]string{"workflow", " SUBAGENT ", "workflow", ""})
	if err != nil || len(got) != 2 || got[0] != "subagent" || got[1] != "workflow" {
		t.Fatalf("got %v err=%v", got, err)
	}
	if got, err := NormalizeClaudeCodeAPIRequestClasses(nil); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("空输入应得到非 nil 空切片，got %#v err=%v", got, err)
	}
	if _, err := NormalizeClaudeCodeAPIRequestClasses([]string{"main"}); err == nil {
		t.Fatal("main 不在可配置类别中")
	}
}
