package endpoint

import (
	"context"
	"testing"
	"time"

	"cc-forwarder/config"
)

func TestNegativeHitCacheModelSchemaAndPayloadAreRouteScoped(t *testing.T) {
	cache := NewNegativeHitCache(time.Hour, 16)

	modelProfile := RouteRequestProfile{Path: "/v1/messages", Model: "claude-opus-4"}
	cache.Record("primary", FailureClassModelUnsupported, modelProfile, "model_not_found")

	if hit, reason := cache.Has("primary", modelProfile); !hit || reason != FailureClassModelUnsupported {
		t.Fatalf("expected model negative hit, hit=%v reason=%q", hit, reason)
	}
	if hit, _ := cache.Has("backup", modelProfile); hit {
		t.Fatal("model negative hit leaked to another endpoint")
	}
	if hit, _ := cache.Has("primary", RouteRequestProfile{Path: "/v1/messages", Model: "claude-sonnet-4"}); hit {
		t.Fatal("model negative hit leaked to another model")
	}

	schemaProfile := RouteRequestProfile{Path: "/v1/messages", SchemaFields: []string{"context_management"}}
	cache.Record("primary", FailureClassSchemaIncompatible, schemaProfile, "extra inputs are not permitted: context_management")
	if hit, reason := cache.Has("primary", schemaProfile); !hit || reason != FailureClassSchemaIncompatible {
		t.Fatalf("expected schema negative hit, hit=%v reason=%q", hit, reason)
	}

	payloadProfile := RouteRequestProfile{Path: "/v1/messages", BodySize: 300 * 1024}
	cache.Record("primary", FailureClassPayloadTooLarge, payloadProfile, "payload too large")
	if hit, reason := cache.Has("primary", RouteRequestProfile{Path: "/v1/messages", BodySize: 280 * 1024}); !hit || reason != FailureClassPayloadTooLarge {
		t.Fatalf("expected payload bucket hit, hit=%v reason=%q", hit, reason)
	}

	cache.Clear("primary")
	if hit, reason := cache.Has("primary", modelProfile); hit {
		t.Fatalf("expected endpoint negative hits to be cleared, hit=%v reason=%q", hit, reason)
	}
}

func TestNegativeHitCacheClearAll(t *testing.T) {
	cache := NewNegativeHitCache(time.Hour, 16)
	cache.Record("primary", FailureClassCountTokensUnsupported, RouteRequestProfile{Path: "/v1/messages/count_tokens", IsCountTokens: true}, "not supported")
	cache.Record("backup", FailureClassModelUnsupported, RouteRequestProfile{Path: "/v1/messages", Model: "claude-opus-4"}, "model_not_found")

	cache.Clear("")

	if hit, reason := cache.Has("primary", RouteRequestProfile{Path: "/v1/messages/count_tokens", IsCountTokens: true}); hit {
		t.Fatalf("expected count_tokens hit to be cleared, hit=%v reason=%q", hit, reason)
	}
	if hit, reason := cache.Has("backup", RouteRequestProfile{Path: "/v1/messages", Model: "claude-opus-4"}); hit {
		t.Fatalf("expected model hit to be cleared, hit=%v reason=%q", hit, reason)
	}
}

func TestNegativeHitCacheRefreshKeepsLRUOrderUnique(t *testing.T) {
	cache := NewNegativeHitCache(time.Hour, 2)
	modelA := RouteRequestProfile{Path: "/v1/messages", Model: "claude-a"}
	modelB := RouteRequestProfile{Path: "/v1/messages", Model: "claude-b"}
	modelC := RouteRequestProfile{Path: "/v1/messages", Model: "claude-c"}

	cache.Record("primary", FailureClassModelUnsupported, modelA, "model unsupported")
	cache.Record("primary", FailureClassModelUnsupported, modelB, "model unsupported")
	cache.Record("primary", FailureClassModelUnsupported, modelA, "model unsupported")
	cache.Record("primary", FailureClassModelUnsupported, modelC, "model unsupported")

	if hit, reason := cache.Has("primary", modelA); !hit || reason != FailureClassModelUnsupported {
		t.Fatalf("expected refreshed model A to remain cached, hit=%v reason=%q", hit, reason)
	}
	if hit, reason := cache.Has("primary", modelB); hit {
		t.Fatalf("expected older model B to be evicted, hit=%v reason=%q", hit, reason)
	}
	if hit, reason := cache.Has("primary", modelC); !hit || reason != FailureClassModelUnsupported {
		t.Fatalf("expected newest model C to remain cached, hit=%v reason=%q", hit, reason)
	}
}

func TestNegativeHitCacheExpiredEntriesDoNotHit(t *testing.T) {
	cache := NewNegativeHitCache(time.Hour, 16)
	profile := RouteRequestProfile{Path: "/v1/messages/count_tokens", IsCountTokens: true}
	cache.Record("primary", FailureClassCountTokensUnsupported, profile, "count_tokens not supported")
	cache.unsupportedCountToken["primary"] = time.Now().Add(-time.Second)

	if hit, reason := cache.Has("primary", profile); hit {
		t.Fatalf("expected expired count_tokens hit to be ignored, hit=%v reason=%q", hit, reason)
	}
}

func TestGetManualFixedRouteBlockUsesHardAvailability(t *testing.T) {
	hardEnabled := false
	manager := newSchedulerTestManager(t, &config.Config{Endpoints: []config.EndpointConfig{{
		Name: "fixed", URL: "https://example.com", Priority: 1,
		AvailabilityEnabled: &hardEnabled,
	}}})
	manager.SetClaudeRoutingOverride(RouteOverrideState{Mode: RouteModeManualFixed, EndpointName: "fixed"})

	block := manager.GetManualFixedRouteBlock(RouteRequestProfile{Path: "/v1/messages"})
	if block == nil || block.Reason != "manual_fixed_endpoint_disabled" {
		t.Fatalf("expected hard availability block, got %+v", block)
	}
}

func TestGetManualFixedRouteBlockUsesPathScopedCooldown(t *testing.T) {
	manager := newSchedulerTestManager(t, schedulerTestConfig(true, "fixed"))
	manager.SetClaudeRoutingOverride(RouteOverrideState{Mode: RouteModeManualFixed, EndpointName: "fixed"})
	profile := RouteRequestProfile{Path: "/v1/messages/count_tokens", IsCountTokens: true}

	manager.SetEndpointCooldown("fixed", time.Minute, SoftFailureCooldownReason(SoftFailureCategoryServerError))
	if block := manager.GetManualFixedRouteBlock(profile); block != nil {
		t.Fatalf("messages cooldown must not block count_tokens, got %+v", block)
	}

	manager.SetScopedCooldown("fixed", SoftFailureScopeCountTokens, time.Minute, SoftFailureCooldownReason(SoftFailureCategoryRateLimit))
	block := manager.GetManualFixedRouteBlock(profile)
	if block == nil || block.Reason != "count_tokens_scoped_cooldown" {
		t.Fatalf("expected count_tokens scoped block, got %+v", block)
	}
}

func TestLegacyRouteHelpersReadConfigRaceFree(t *testing.T) {
	manager := newSchedulerTestManager(t, schedulerTestConfig(true, "fixed", "backup"))
	manager.SetClaudeRoutingOverride(RouteOverrideState{Mode: RouteModeManualFixed, EndpointName: "fixed"})

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			cfg := schedulerTestConfig(true, "fixed", "backup").Endpoints[0]
			cfg.Priority = 1 + i%2
			_ = manager.UpdateEndpointConfig("fixed", cfg)
		}
	}()
	for i := 0; i < 200; i++ {
		_ = manager.GetEndpointByNameAny("fixed")
		_ = manager.GetManualFixedRouteBlock(RouteRequestProfile{Path: "/v1/messages"})
		_ = manager.PrepareRouteCandidates(context.Background(), RouteRequestProfile{Path: "/v1/messages"})
	}
	<-done
}
