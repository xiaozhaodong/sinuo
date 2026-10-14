package endpoint

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"cc-forwarder/config"
)

func TestAcquireEndpointAttemptBuildsImmutableFlatTarget(t *testing.T) {
	availability := true
	autoSchedule := true
	cfg := &config.Config{
		Failover: config.FailoverConfig{Enabled: true, MaxCandidateAttempts: 3},
		Endpoints: []config.EndpointConfig{{
			Name:                "primary",
			URL:                 "https://old.example.com",
			Priority:            3,
			Timeout:             15 * time.Second,
			Headers:             map[string]string{"X-Snapshot": "old"},
			Token:               "token-1",
			ApiKey:              "key-1",
			SupportsCountTokens: true,
			ModelRewriteRules:   `[{"paths":["/v1/messages","/v1/messages/count_tokens"],"match":"exact","from":"old","to":"new"}]`,
			AvailabilityEnabled: &availability,
			FailoverEnabled:     &autoSchedule,
		}},
	}
	manager := NewManager(cfg)
	t.Cleanup(manager.Stop)

	plan := manager.PrepareRouteCandidates(context.Background(), RouteRequestProfile{}).Plans[0]
	revision := plan.ConfigRevision
	admission, err := manager.AcquireEndpointAttempt(plan)
	if err != nil {
		t.Fatalf("acquire attempt: %v", err)
	}
	t.Cleanup(admission.Release)

	updated := cfg.Endpoints[0]
	updated.URL = "https://new.example.com"
	updated.Priority = 9
	updated.Timeout = time.Minute
	updated.Headers = map[string]string{"X-Snapshot": "new"}
	updated.Token = "new-token"
	updated.ApiKey = "new-key"
	updated.SupportsCountTokens = false
	updated.ModelRewriteRules = ""
	if err := manager.UpdateEndpointConfig("primary", updated); err != nil {
		t.Fatalf("update live endpoint: %v", err)
	}

	targetConfig := admission.Target.Config()
	if targetConfig.URL != "https://old.example.com" || targetConfig.Priority != 3 || targetConfig.Timeout != 15*time.Second {
		t.Fatalf("target changed with live config: %+v", targetConfig)
	}
	if targetConfig.Headers["X-Snapshot"] != "old" || targetConfig.Token != "token-1" || targetConfig.ApiKey != "key-1" {
		t.Fatalf("flat credentials or headers changed: %+v", targetConfig)
	}
	if !targetConfig.SupportsCountTokens || targetConfig.ModelRewriteRules == "" || admission.Target.Revision() != revision {
		t.Fatalf("target capability/revision changed: %+v revision=%d", targetConfig, admission.Target.Revision())
	}
	targetConfig.Headers["X-Snapshot"] = "mutated-copy"
	if got := admission.Target.Config().Headers["X-Snapshot"]; got != "old" {
		t.Fatalf("target headers are externally mutable, got %q", got)
	}
}

func TestUpdateEndpointPriorityInvalidatesExistingPlan(t *testing.T) {
	cfg := &config.Config{
		Failover:  config.FailoverConfig{Enabled: true, MaxCandidateAttempts: 3},
		Endpoints: []config.EndpointConfig{{Name: "priority-target", URL: "https://example.com", Priority: 1, Timeout: time.Second}},
	}
	manager := NewManager(cfg)
	t.Cleanup(manager.Stop)

	oldPlan := manager.PrepareRouteCandidates(context.Background(), RouteRequestProfile{}).Plans[0]
	if err := manager.UpdateEndpointPriority("priority-target", 5); err != nil {
		t.Fatalf("update priority: %v", err)
	}
	if _, err := manager.AcquireEndpointAttempt(oldPlan); err == nil || !strings.Contains(err.Error(), "config_changed_since_snapshot") {
		t.Fatalf("old plan must be rejected after priority update, got %v", err)
	}
	updated := manager.PrepareRouteCandidates(context.Background(), RouteRequestProfile{})
	if len(updated.Plans) != 1 || updated.Plans[0].Priority != 5 || updated.Plans[0].ConfigRevision == oldPlan.ConfigRevision {
		t.Fatalf("expected new priority/revision plan, old=%+v new=%+v", oldPlan, updated.Plans)
	}
}

func TestAcquireEndpointAttemptUsesCredentialsCapturedDuringPlanning(t *testing.T) {
	cfg := &config.Config{
		Failover: config.FailoverConfig{Enabled: true, MaxCandidateAttempts: 3},
		Endpoints: []config.EndpointConfig{{
			Name: "credential-target", URL: "https://example.com", Priority: 1,
			Token: "token-1", ApiKey: "key-1",
		}},
	}
	manager := NewManager(cfg)
	t.Cleanup(manager.Stop)

	plan := manager.PrepareRouteCandidates(context.Background(), RouteRequestProfile{}).Plans[0]
	ep := manager.GetEndpointByNameAny("credential-target")
	ep.mutex.Lock()
	ep.Config.Token = "mutated-without-publication"
	ep.Config.ApiKey = "mutated-key-without-publication"
	ep.mutex.Unlock()

	admission, err := manager.AcquireEndpointAttempt(plan)
	if err != nil {
		t.Fatalf("acquire attempt: %v", err)
	}
	t.Cleanup(admission.Release)
	got := admission.Target.Config()
	if got.Token != "token-1" || got.ApiKey != "key-1" {
		t.Fatalf("attempt credentials changed after planning: token=%q apiKey=%q", got.Token, got.ApiKey)
	}
}

func TestAttemptSettlementDoesNotMutateNewRevision(t *testing.T) {
	cfg := &config.Config{
		Failover:       config.FailoverConfig{Enabled: true, MaxCandidateAttempts: 3},
		FailureTracker: config.FailureTrackerConfig{Enabled: true, TimeWindow: time.Minute, Threshold: 3},
		Endpoints:      []config.EndpointConfig{{Name: "settlement-target", URL: "https://old.example.com", Priority: 1}},
	}
	manager := NewManager(cfg)
	t.Cleanup(manager.Stop)
	profile := BuildRouteRequestProfile("/v1/messages", []byte(`{"model":"old-model"}`))
	oldPlan := manager.PrepareRouteCandidates(context.Background(), profile).Plans[0]
	oldAdmission, err := manager.AcquireEndpointAttempt(oldPlan)
	if err != nil {
		t.Fatalf("acquire old attempt: %v", err)
	}
	oldRevision := oldAdmission.Target.Revision()
	oldAdmission.Release()

	updated := cfg.Endpoints[0]
	updated.URL = "https://new.example.com"
	if err := manager.UpdateEndpointConfig("settlement-target", updated); err != nil {
		t.Fatalf("update endpoint: %v", err)
	}
	applied := manager.ApplyEndpointAttemptSettlement("settlement-target", oldRevision, func() {
		manager.RecordSoftFailure("settlement-target", SoftFailureScopeMessages, SoftFailureCategoryServerError)
		manager.SetEndpointCooldown("settlement-target", time.Minute, "auth_rejected")
		manager.RecordNegativeRouteHit("settlement-target", FailureClassModelUnsupported, profile, "old response")
	})
	if applied {
		t.Fatal("old attempt settlement must be fenced after config edit")
	}
	if counts := manager.GetSoftFailureCounts("settlement-target", SoftFailureScopeMessages); len(counts) != 0 {
		t.Fatalf("old attempt polluted soft failures: %+v", counts)
	}
	if inCooldown, _, _ := manager.GetEndpointCooldownInfo("settlement-target"); inCooldown {
		t.Fatal("old attempt polluted cooldown state")
	}
	if hit, _ := manager.routeState.HasNegativeHit("settlement-target", profile); hit {
		t.Fatal("old attempt polluted negative route cache")
	}
}

// TestFailureEpochPropagationChain epoch 透传链:
// snapshot → plan.FailureEpoch → admission → target.FailureEpoch() 全程一致;
// Reset 后重新规划,新 plan 携带推进后的 epoch
func TestFailureEpochPropagationChain(t *testing.T) {
	cfg := &config.Config{
		Failover:       config.FailoverConfig{Enabled: true, MaxCandidateAttempts: 3},
		FailureTracker: config.FailureTrackerConfig{Enabled: true, TimeWindow: time.Minute, Threshold: 3},
		Endpoints:      []config.EndpointConfig{{Name: "epoch-chain", URL: "https://old.example.com", Priority: 1}},
	}
	manager := NewManager(cfg)
	t.Cleanup(manager.Stop)

	profile := BuildRouteRequestProfile("/v1/messages", []byte(`{"model":"old-model"}`))
	snapshots := manager.snapshotEndpointCandidates()
	if len(snapshots) != 1 {
		t.Fatalf("快照数量应为 1: %d", len(snapshots))
	}

	result := manager.PrepareRouteCandidates(context.Background(), profile)
	if len(result.Plans) != 1 {
		t.Fatalf("plans 数量应为 1: %d", len(result.Plans))
	}
	if result.Plans[0].FailureEpoch != snapshots[0].failureEpoch {
		t.Fatalf("plan.FailureEpoch 与快照不一致: plan=%d snapshot=%d",
			result.Plans[0].FailureEpoch, snapshots[0].failureEpoch)
	}

	admission, err := manager.AcquireEndpointAttempt(result.Plans[0])
	if err != nil {
		t.Fatalf("acquire attempt: %v", err)
	}
	defer admission.Release()
	if got := admission.Target.FailureEpoch(); got != result.Plans[0].FailureEpoch {
		t.Fatalf("target.FailureEpoch() 与 plan 不一致: got=%d plan=%d", got, result.Plans[0].FailureEpoch)
	}

	// Reset 推进 epoch 后重新规划,新 plan 携带推进后的 epoch
	if _, found := manager.ResetEndpointFailureState("epoch-chain"); !found {
		t.Fatal("Reset 应成功")
	}
	result2 := manager.PrepareRouteCandidates(context.Background(), profile)
	if result2.Plans[0].FailureEpoch != snapshots[0].failureEpoch+1 {
		t.Fatalf("Reset 后 plan.FailureEpoch 应 +1: got=%d want=%d",
			result2.Plans[0].FailureEpoch, snapshots[0].failureEpoch+1)
	}
}

// TestApplyEndpointFailureSettlementEntryCheck 入口检查:
// epoch+revision 均匹配 → apply 执行返回 true;
// epoch 不匹配 / revision 不匹配 → 不执行返回 false
func TestApplyEndpointFailureSettlementEntryCheck(t *testing.T) {
	cfg := &config.Config{
		Failover:       config.FailoverConfig{Enabled: true, MaxCandidateAttempts: 3},
		FailureTracker: config.FailureTrackerConfig{Enabled: true, TimeWindow: time.Minute, Threshold: 3},
		Endpoints:      []config.EndpointConfig{{Name: "settlement-epoch", URL: "https://old.example.com", Priority: 1}},
	}
	manager := NewManager(cfg)
	t.Cleanup(manager.Stop)

	ep := manager.GetEndpointByNameAny("settlement-epoch")
	ep.mutex.RLock()
	revision := ep.configRevision
	epoch := ep.failureEpoch
	ep.mutex.RUnlock()

	// 均匹配 → 执行
	executed := false
	ok := manager.ApplyEndpointFailureSettlement("settlement-epoch", revision, epoch, func() { executed = true })
	if !ok || !executed {
		t.Fatalf("均匹配应执行 apply: ok=%v executed=%v", ok, executed)
	}

	// epoch 不匹配 → 不执行
	executed = false
	ok = manager.ApplyEndpointFailureSettlement("settlement-epoch", revision, epoch+1, func() { executed = true })
	if ok || executed {
		t.Fatalf("epoch 不匹配应跳过: ok=%v executed=%v", ok, executed)
	}

	// revision 不匹配 → 不执行
	executed = false
	ok = manager.ApplyEndpointFailureSettlement("settlement-epoch", revision+1, epoch, func() { executed = true })
	if ok || executed {
		t.Fatalf("revision 不匹配应跳过: ok=%v executed=%v", ok, executed)
	}
}

func TestPrepareRouteCandidatesUsesConsistentFlatSnapshotsDuringUpdates(t *testing.T) {
	auto := true
	cfgA := config.EndpointConfig{Name: "moving", URL: "https://a.example.com", Priority: 1, Timeout: time.Second, SupportsCountTokens: true, FailoverEnabled: &auto, Token: "a"}
	cfgB := config.EndpointConfig{Name: "moving", URL: "https://b.example.com", Priority: 3, Timeout: 3 * time.Second, SupportsCountTokens: false, FailoverEnabled: &auto, Token: "b"}
	cfg := &config.Config{
		Failover:  config.FailoverConfig{Enabled: true, MaxCandidateAttempts: 3},
		Endpoints: []config.EndpointConfig{cfgA, {Name: "stable", URL: "https://stable.example.com", Priority: 2, Timeout: 2 * time.Second}},
	}
	manager := NewManager(cfg)
	t.Cleanup(manager.Stop)

	const iterations = 300
	var wg sync.WaitGroup
	errCh := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			next := cfgA
			if i%2 == 1 {
				next = cfgB
			}
			if err := manager.UpdateEndpointConfig("moving", next); err != nil {
				errCh <- err
				return
			}
		}
	}()

	for i := 0; i < iterations; i++ {
		result := manager.PrepareRouteCandidates(context.Background(), RouteRequestProfile{})
		if len(result.Plans) != 2 {
			t.Fatalf("expected two plans, got %+v", result.Plans)
		}
		var moving EndpointAttemptPlan
		movingIndex := -1
		for idx, plan := range result.Plans {
			if plan.EndpointName == "moving" {
				moving, movingIndex = plan, idx
				break
			}
		}
		switch moving.URL {
		case cfgA.URL:
			if moving.Priority != cfgA.Priority || moving.Timeout != cfgA.Timeout || !moving.SupportsCountTokens || movingIndex != 0 || moving.resolvedToken != "a" {
				t.Fatalf("mixed A snapshot: plan=%+v index=%d", moving, movingIndex)
			}
		case cfgB.URL:
			if moving.Priority != cfgB.Priority || moving.Timeout != cfgB.Timeout || moving.SupportsCountTokens || movingIndex != 1 || moving.resolvedToken != "b" {
				t.Fatalf("mixed B snapshot: plan=%+v index=%d", moving, movingIndex)
			}
		default:
			t.Fatalf("unexpected moving snapshot: %+v", moving)
		}
	}
	wg.Wait()
	select {
	case err := <-errCh:
		t.Fatalf("concurrent update failed: %v", err)
	default:
	}
}

func TestRemoveEndpointClearsNameKeyedRuntimeStateBeforeRecreate(t *testing.T) {
	cfg := &config.Config{
		Failover:       config.FailoverConfig{Enabled: true, MaxCandidateAttempts: 3},
		FailureTracker: config.FailureTrackerConfig{Enabled: true, TimeWindow: time.Minute, Threshold: 3},
		Endpoints:      []config.EndpointConfig{{Name: "recreated", URL: "https://old.example.com", Priority: 1}},
	}
	manager := NewManager(cfg)
	t.Cleanup(manager.Stop)
	profile := BuildRouteRequestProfile("/v1/messages/count_tokens", []byte(`{"model":"old-model"}`))

	manager.RecordNegativeRouteHit("recreated", FailureClassCountTokensUnsupported, profile, "unsupported")
	manager.SetScopedCooldown("recreated", SoftFailureScopeCountTokens, time.Minute, "soft_failure_rate_limit")
	manager.RecordSoftFailure("recreated", SoftFailureScopeMessages, SoftFailureCategoryConnection)
	if !manager.UpdateAutoRetention("recreated", 1, "auto_priority", 0) {
		t.Fatal("expected retained setup")
	}
	if err := manager.RemoveEndpoint("recreated"); err != nil {
		t.Fatalf("remove endpoint: %v", err)
	}
	if err := manager.AddEndpoint(config.EndpointConfig{Name: "recreated", URL: "https://new.example.com", Priority: 1}); err != nil {
		t.Fatalf("recreate endpoint: %v", err)
	}
	if hit, _ := manager.routeState.HasNegativeHit("recreated", profile); hit {
		t.Fatal("negative cache survived endpoint recreation")
	}
	if active, _, _ := manager.ScopedCooldownActive("recreated", SoftFailureScopeCountTokens); active {
		t.Fatal("scoped cooldown survived endpoint recreation")
	}
	if counts := manager.GetSoftFailureCounts("recreated", SoftFailureScopeMessages); len(counts) != 0 {
		t.Fatalf("soft failures survived endpoint recreation: %+v", counts)
	}
	if got := manager.RetainedInTier(1); got != "" {
		t.Fatalf("retained state survived endpoint recreation: %q", got)
	}
}
