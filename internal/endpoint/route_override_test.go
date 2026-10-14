package endpoint

import (
	"context"
	"strings"
	"testing"
	"time"

	"cc-forwarder/config"
)

func TestRouteOverrideManualFixedBlocksSystemSwitch(t *testing.T) {
	override := NewRouteOverride()
	override.Set(RouteOverrideState{
		Mode:            RouteModeManualFixed,
		EndpointName:    "primary",
		SetBy:           RouteCallerUser,
		FallbackEnabled: false,
	})

	if allowed, reason := override.AllowSystemSwitch("primary", "backup", RouteCallerSystemFailoverRequest); allowed || reason == "" {
		t.Fatalf("expected system failover to be blocked, allowed=%v reason=%q", allowed, reason)
	}
	if allowed, reason := override.AllowSystemSwitch("primary", "backup", RouteCallerUser); !allowed || reason != "" {
		t.Fatalf("expected user switch to be allowed, allowed=%v reason=%q", allowed, reason)
	}
}

func TestManualFixedFailoverBlockReasonPreservesPercentLiterals(t *testing.T) {
	manager := NewManager(&config.Config{})
	defer manager.Stop()
	manager.SetClaudeRoutingOverride(RouteOverrideState{
		Mode:            RouteModeManualFixed,
		EndpointName:    "primary%s",
		SetBy:           RouteCallerUser,
		FallbackEnabled: false,
	})

	allowed, reason := manager.AllowSystemRouteSwitch("primary%s", "backup%q", RouteCallerSystemFailoverRequest)
	if allowed || reason == "" {
		t.Fatal("expected manual fixed failover to be blocked")
	}
	if strings.Contains(reason, "%!") {
		t.Fatalf("block reason was treated as a format string: %q", reason)
	}
	if !strings.Contains(reason, "primary%s") || !strings.Contains(reason, "backup%q") {
		t.Fatalf("block reason lost endpoint names: %q", reason)
	}
}

func TestManualPreferredAllowsFallbackWithoutClearingPreference(t *testing.T) {
	cfg := &config.Config{
		Failover: config.FailoverConfig{
			Enabled: true,
		},
		Endpoints: []config.EndpointConfig{
			{Name: "primary", URL: "http://primary.example", Priority: 1},
			{Name: "backup", URL: "http://backup.example", Priority: 2},
		},
	}
	manager := NewManager(cfg)
	defer manager.Stop()
	manager.SetClaudeRoutingOverride(RouteOverrideState{
		Mode:            RouteModeManualPreferred,
		EndpointName:    "primary",
		SetBy:           RouteCallerUser,
		FallbackEnabled: true,
	})
	manager.SetEndpointCooldown("primary", time.Minute, "unit test")

	result := manager.PrepareRouteCandidates(context.Background(), RouteRequestProfile{})
	if len(result.Candidates) != 1 || result.Candidates[0].Config.Name != "backup" {
		t.Fatalf("expected scheduler fallback candidate [backup], got %v", candidateNames(result))
	}

	override := manager.GetClaudeRoutingOverride()
	if override.Mode != RouteModeManualPreferred || override.EndpointName != "primary" {
		t.Fatalf("manual preference should remain recorded, got %+v", override)
	}
}

func TestManualFixedSelectionDoesNotFailoverPastPinnedEndpoint(t *testing.T) {
	cfg := &config.Config{
		Failover: config.FailoverConfig{
			Enabled: true,
		},
		FailureTracker: config.FailureTrackerConfig{
			Enabled:    true,
			TimeWindow: time.Hour,
			Threshold:  1,
		},
		Endpoints: []config.EndpointConfig{
			{Name: "primary", URL: "http://primary.example", Priority: 1},
			{Name: "backup", URL: "http://backup.example", Priority: 2},
		},
	}
	manager := NewManager(cfg)
	manager.SetClaudeRoutingOverride(RouteOverrideState{
		Mode:            RouteModeManualFixed,
		EndpointName:    "primary",
		SetBy:           RouteCallerUser,
		FallbackEnabled: false,
	})
	manager.SetEndpointCooldown("primary", time.Minute, SoftFailureCooldownReason(SoftFailureCategoryServerError))

	healthy := manager.GetHealthyEndpoints()
	if len(healthy) != 0 {
		t.Fatalf("expected manual fixed route to return no fallback endpoint, got %d", len(healthy))
	}

	override := manager.GetClaudeRoutingOverride()
	if override.Mode != RouteModeManualFixed || override.EndpointName != "primary" {
		t.Fatalf("manual fixed override changed unexpectedly: %+v", override)
	}
}
