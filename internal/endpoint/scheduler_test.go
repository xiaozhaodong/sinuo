package endpoint

import (
	"context"
	"testing"
	"time"

	"sinuo/config"
)

func newSchedulerTestManager(t *testing.T, cfg *config.Config) *Manager {
	t.Helper()
	manager := NewManager(cfg)
	t.Cleanup(manager.Stop)
	return manager
}

func schedulerTestConfig(failoverEnabled bool, names ...string) *config.Config {
	cfg := &config.Config{
		Failover: config.FailoverConfig{Enabled: failoverEnabled},
		FailureTracker: config.FailureTrackerConfig{
			Enabled:    true,
			TimeWindow: 5 * time.Minute,
			Threshold:  3,
		},
	}
	for i, name := range names {
		cfg.Endpoints = append(cfg.Endpoints, config.EndpointConfig{
			Name: name, URL: "http://" + name + ".test", Priority: i + 1,
		})
	}
	return cfg
}

func candidateNames(result EndpointScheduleResult) []string {
	names := make([]string, 0, len(result.Candidates))
	for _, ep := range result.Candidates {
		names = append(names, ep.Config.Name)
	}
	return names
}

func skippedReason(t *testing.T, result EndpointScheduleResult, name string) (string, time.Time) {
	t.Helper()
	for _, decision := range result.Snapshot.Decisions {
		if decision.Name == name && decision.Decision == endpointDecisionSkipped {
			return decision.Reason, decision.AvailableAt
		}
	}
	t.Fatalf("expected skipped decision for %s, got %+v", name, result.Snapshot.Decisions)
	return "", time.Time{}
}

// [Phase3 §8.2/§8.3] priority 层级序；retained 只在同层内提供粘性。
func TestPrepareRouteCandidates_PriorityTierOrder(t *testing.T) {
	manager := newSchedulerTestManager(t, schedulerTestConfig(true, "a", "b", "c"))

	result := manager.PrepareRouteCandidates(context.Background(), RouteRequestProfile{})
	if got := candidateNames(result); len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("expected [a b c] priority order, got %v", got)
	}
	if len(result.Plans) != 3 || result.Plans[0].SelectionSource != "auto_priority" ||
		result.Plans[1].SelectionSource != "fallback" {
		t.Fatalf("unexpected plan sources: %+v", result.Plans)
	}
}

// [Phase3 §8.4] 同层 retained 粘性与高优先级恢复回归
func TestPrepareRouteCandidates_RetainedStickyWithinTierOnly(t *testing.T) {
	cfg := schedulerTestConfig(true, "a1", "a2", "b1")
	cfg.Endpoints[0].Priority = 1
	cfg.Endpoints[1].Priority = 1
	cfg.Endpoints[2].Priority = 2
	manager := newSchedulerTestManager(t, cfg)

	// 同层（priority 1）retained：a2 优先于 a1
	if !manager.UpdateAutoRetention("a2", 1, "auto_retained", 0) {
		t.Fatal("expected retained update for a2")
	}
	result := manager.PrepareRouteCandidates(context.Background(), RouteRequestProfile{})
	if got := candidateNames(result); got[0] != "a2" || result.Plans[0].SelectionSource != "auto_retained" {
		t.Fatalf("expected retained a2 first in tier, got %v (%+v)", got, result.Plans)
	}

	// 低优先级 retained 不阻挡高优先级层（§8.4 规则 6）
	manager.UpdateAutoRetention("b1", 2, "auto_retained", 0)
	result = manager.PrepareRouteCandidates(context.Background(), RouteRequestProfile{})
	if got := candidateNames(result); got[0] != "a2" {
		t.Fatalf("low-tier retained must not override best tier, got %v", got)
	}
}

func TestUpdateAutoRetentionIgnoresManualTargetButKeepsPreferredFallback(t *testing.T) {
	manager := newSchedulerTestManager(t, schedulerTestConfig(true, "a", "b"))
	fixed := manager.SetClaudeRoutingOverride(RouteOverrideState{Mode: RouteModeManualFixed, EndpointName: "a"})
	if manager.UpdateAutoRetention("a", 1, "manual_fixed", fixed.Revision) {
		t.Fatal("manual fixed success must not update auto retained")
	}

	preferred := manager.SetClaudeRoutingOverride(RouteOverrideState{Mode: RouteModeManualPreferred, EndpointName: "a"})
	if manager.UpdateAutoRetention("a", 1, "manual_preferred", preferred.Revision) {
		t.Fatal("manual preferred target success must not update auto retained")
	}
	if !manager.UpdateAutoRetention("b", 1, "fallback", preferred.Revision) {
		t.Fatal("manual preferred automatic fallback may update auto retained")
	}
	if got := manager.RetainedInTier(1); got != "b" {
		t.Fatalf("expected fallback endpoint retained, got %q", got)
	}

	manager.ClearClaudeRoutingOverride("test")
	if manager.UpdateAutoRetention("a", 1, "auto_priority", preferred.Revision) {
		t.Fatal("stale route revision must not update retained")
	}
}

func TestUpdateAutoRetentionDoesNotRestoreEndpointAfterConcurrentCooldown(t *testing.T) {
	manager := newSchedulerTestManager(t, schedulerTestConfig(true, "cooling"))

	if !manager.UpdateAutoRetention("cooling", 1, "auto_priority", 0) {
		t.Fatal("expected initial retained update")
	}
	manager.SetEndpointCooldown("cooling", time.Minute, "soft_failure_rate_limit")
	if got := manager.RetainedInTier(1); got != "" {
		t.Fatalf("cooldown must clear retained state, got %q", got)
	}

	// 模拟同一端点较早发起的慢请求随后成功：更新路径必须观察当前 cooldown，
	// 不能把并发失败刚清除的 retained 状态重新写回。
	if manager.UpdateAutoRetention("cooling", 1, "auto_priority", 0) {
		t.Fatal("slow success must not restore retained state after a newer cooldown")
	}
	if got := manager.RetainedInTier(1); got != "" {
		t.Fatalf("retained state was restored during cooldown: %q", got)
	}
}

func TestPrepareRouteCandidates_FiltersWithAvailableAtSources(t *testing.T) {
	manager := newSchedulerTestManager(t, schedulerTestConfig(true, "active-ep", "cooling", "tripped", "negcached", "ok"))

	now := time.Now()
	cooldownUntil := now.Add(10 * time.Minute)

	cooling := manager.GetEndpointByNameAny("cooling")
	cooling.mutex.Lock()
	cooling.Status.CooldownUntil = cooldownUntil
	cooling.mutex.Unlock()

	// v8：软失败阈值触发即写入 cooldown，tripped 状态以 cooldown 表达（§9.3）
	trippedUntil := now.Add(3 * time.Minute)
	tripped := manager.GetEndpointByNameAny("tripped")
	tripped.mutex.Lock()
	tripped.Status.CooldownUntil = trippedUntil
	tripped.Status.CooldownReason = SoftFailureCooldownReason(SoftFailureCategoryRateLimit)
	tripped.mutex.Unlock()

	profile := RouteRequestProfile{Model: "gpt-test"}
	manager.RecordNegativeRouteHit("negcached", "model_unsupported", profile, "model not found")

	result := manager.PrepareRouteCandidates(context.Background(), profile)
	if got := candidateNames(result); len(got) != 2 || got[0] != "active-ep" || got[1] != "ok" {
		t.Fatalf("expected [active-ep ok], got %v", got)
	}

	if reason, availableAt := skippedReason(t, result, "cooling"); reason != "cooldown" || !availableAt.Equal(cooldownUntil) {
		t.Fatalf("cooling: got reason=%q availableAt=%v", reason, availableAt)
	}
	if reason, availableAt := skippedReason(t, result, "tripped"); reason != "cooldown" || !availableAt.Equal(trippedUntil) {
		t.Fatalf("tripped: got reason=%q availableAt=%v", reason, availableAt)
	}
	if reason, availableAt := skippedReason(t, result, "negcached"); reason != "negative_cache_model_unsupported" || availableAt.IsZero() {
		t.Fatalf("negcached: got reason=%q availableAt=%v", reason, availableAt)
	}

	// 各来源中最早的是 tripped 的 3m 软失败冷却，早于 10m 冷却。
	if earliest := result.Snapshot.EarliestAvailableAt(); !earliest.Equal(trippedUntil) {
		t.Fatalf("expected earliest availableAt from tripped cooldown (3m), got %v", earliest)
	}
}

func TestPrepareRouteCandidates_FailoverDisabledSingleCandidate(t *testing.T) {
	manager := newSchedulerTestManager(t, schedulerTestConfig(false, "a", "b", "c"))

	result := manager.PrepareRouteCandidates(context.Background(), RouteRequestProfile{})
	if got := candidateNames(result); len(got) != 1 || got[0] != "a" {
		t.Fatalf("expected single candidate [a], got %v", got)
	}

	// v8：第一逻辑候选冷却时，下一请求重新选择首候选（不冻结未来请求，§8.2）
	active := manager.GetEndpointByNameAny("a")
	active.mutex.Lock()
	active.Status.CooldownUntil = time.Now().Add(time.Minute)
	active.mutex.Unlock()

	result = manager.PrepareRouteCandidates(context.Background(), RouteRequestProfile{})
	if got := candidateNames(result); len(got) != 1 || got[0] != "b" {
		t.Fatalf("expected next request to pick [b] when a cooling, got %v", candidateNames(result))
	}
}

func TestPrepareRouteCandidates_ManualFixedOnlyTarget(t *testing.T) {
	manager := newSchedulerTestManager(t, schedulerTestConfig(true, "a", "b", "c"))
	manager.SetClaudeRoutingOverride(RouteOverrideState{Mode: RouteModeManualFixed, EndpointName: "c"})

	result := manager.PrepareRouteCandidates(context.Background(), RouteRequestProfile{})
	if got := candidateNames(result); len(got) != 1 || got[0] != "c" {
		t.Fatalf("expected manual_fixed single candidate [c], got %v", got)
	}
	if result.RouteOverrideRevision == 0 {
		t.Fatal("expected route override revision captured")
	}
	if result.Snapshot.RouteMode != RouteModeManualFixed || result.Snapshot.RouteEndpointName != "c" {
		t.Fatalf("expected manual fixed metadata in snapshot, got %+v", result.Snapshot)
	}
	if !result.Snapshot.FailoverEnabled {
		t.Fatal("expected global failover flag in snapshot")
	}

	// 目标冷却：空候选而非静默转移
	target := manager.GetEndpointByNameAny("c")
	target.mutex.Lock()
	target.Status.CooldownUntil = time.Now().Add(time.Minute)
	target.mutex.Unlock()

	result = manager.PrepareRouteCandidates(context.Background(), RouteRequestProfile{})
	if len(result.Candidates) != 0 {
		t.Fatalf("expected empty candidates for cooling manual_fixed target, got %v", candidateNames(result))
	}
	if reason, _ := skippedReason(t, result, "c"); reason != "manual_fixed_cooldown" {
		t.Fatalf("expected manual_fixed_cooldown reason, got %q", reason)
	}
}

func TestEndpointScheduleSnapshot_RecordsAttemptsAndFinalOutcome(t *testing.T) {
	manager := newSchedulerTestManager(t, schedulerTestConfig(true, "primary", "backup"))

	result := manager.PrepareRouteCandidates(context.Background(), RouteRequestProfile{})
	manager.BeginEndpointScheduleSnapshot("req-snapshot", "/v1/messages", result.Snapshot)

	// 验证 store 持有深拷贝，而不是调度器返回值本身。
	result.Snapshot.Decisions[0].Reason = "mutated-after-save"
	manager.RecordEndpointScheduleAttempt("req-snapshot", "primary", EndpointScheduleRuntimeTryNext, "dial failed")
	manager.RecordEndpointScheduleAttempt("req-snapshot", "backup", EndpointScheduleRuntimeAttempting, "")
	manager.CompleteEndpointScheduleSnapshot("req-snapshot", "backup", EndpointScheduleOutcomeSuccess, "")

	snapshot := manager.GetLatestEndpointScheduleSnapshot()
	if snapshot == nil {
		t.Fatal("expected latest endpoint schedule snapshot")
	}
	if snapshot.RequestID != "req-snapshot" || snapshot.RequestPath != "/v1/messages" {
		t.Fatalf("unexpected request metadata: %+v", snapshot)
	}
	if snapshot.SelectedEndpoint != "backup" {
		t.Fatalf("unexpected endpoint selection metadata: %+v", snapshot)
	}
	if snapshot.FinalOutcome != EndpointScheduleOutcomeSuccess || snapshot.FinalError != "" {
		t.Fatalf("unexpected final outcome: %+v", snapshot)
	}
	if snapshot.UpdatedAt.Before(snapshot.CapturedAt) {
		t.Fatalf("updated_at must not precede captured_at: %+v", snapshot)
	}
	if got := snapshot.Decisions[0]; got.Reason == "mutated-after-save" || got.RuntimeOutcome != EndpointScheduleRuntimeTryNext || got.RuntimeError != "dial failed" {
		t.Fatalf("unexpected primary decision: %+v", got)
	}
	if got := snapshot.Decisions[1]; got.RuntimeOutcome != EndpointScheduleOutcomeSuccess || got.RuntimeError != "" {
		t.Fatalf("unexpected backup decision: %+v", got)
	}

	// GetLatest 同样必须返回副本。
	snapshot.Decisions[0].RuntimeError = "mutated-read"
	if got := manager.GetLatestEndpointScheduleSnapshot().Decisions[0].RuntimeError; got != "dial failed" {
		t.Fatalf("latest snapshot leaked mutable decision slice: %q", got)
	}
}

func TestEndpointScheduleSnapshot_OlderCompletionDoesNotReplaceNewerRequest(t *testing.T) {
	manager := newSchedulerTestManager(t, schedulerTestConfig(true, "a", "b"))

	oldResult := manager.PrepareRouteCandidates(context.Background(), RouteRequestProfile{})
	manager.BeginEndpointScheduleSnapshot("req-old", "/v1/messages", oldResult.Snapshot)

	newResult := manager.PrepareRouteCandidates(context.Background(), RouteRequestProfile{})
	manager.BeginEndpointScheduleSnapshot("req-new", "/v1/messages", newResult.Snapshot)

	manager.CompleteEndpointScheduleSnapshot("req-old", "a", EndpointScheduleOutcomeSuccess, "")
	latest := manager.GetLatestEndpointScheduleSnapshot()
	if latest == nil || latest.RequestID != "req-new" || latest.FinalOutcome != EndpointScheduleOutcomePending {
		t.Fatalf("older completion replaced newer latest snapshot: %+v", latest)
	}

	manager.CompleteEndpointScheduleSnapshot("req-new", "b", EndpointScheduleOutcomePassthroughError, "upstream 503")
	latest = manager.GetLatestEndpointScheduleSnapshot()
	if latest.RequestID != "req-new" || latest.SelectedEndpoint != "b" || latest.FinalOutcome != EndpointScheduleOutcomePassthroughError {
		t.Fatalf("newer completion not reflected: %+v", latest)
	}
}

func TestBeginEndpointScheduleSnapshotNilCreatesSnapshot(t *testing.T) {
	manager := newSchedulerTestManager(t, schedulerTestConfig(true, "endpoint"))
	manager.BeginEndpointScheduleSnapshot("req-rejected", "/v1/messages", nil)

	snapshot := manager.GetLatestEndpointScheduleSnapshot()
	if snapshot == nil {
		t.Fatal("expected snapshot")
	}
	if snapshot.RequestID != "req-rejected" || snapshot.RequestPath != "/v1/messages" {
		t.Fatalf("snapshot identity = %q/%q", snapshot.RequestID, snapshot.RequestPath)
	}
}

func TestPrepareRouteCandidates_ManualPreferredMovesTargetFirst(t *testing.T) {
	manager := newSchedulerTestManager(t, schedulerTestConfig(true, "a", "b", "c"))
	manager.SetClaudeRoutingOverride(RouteOverrideState{Mode: RouteModeManualPreferred, EndpointName: "c"})

	result := manager.PrepareRouteCandidates(context.Background(), RouteRequestProfile{})
	if got := candidateNames(result); len(got) != 3 || got[0] != "c" {
		t.Fatalf("expected preferred target first, got %v", got)
	}
	if len(result.Snapshot.Decisions) < 3 || result.Snapshot.Decisions[0].Name != "c" {
		t.Fatalf("expected snapshot candidate order to match actual attempts, got %+v", result.Snapshot.Decisions)
	}
}

func TestPrepareRouteCandidates_FailoverDisabledEndpointExcluded(t *testing.T) {
	cfg := schedulerTestConfig(true, "a", "b", "c")
	disabled := false
	cfg.Endpoints[2].FailoverEnabled = &disabled

	manager := newSchedulerTestManager(t, cfg)

	result := manager.PrepareRouteCandidates(context.Background(), RouteRequestProfile{})
	if got := candidateNames(result); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("expected [a b] with c excluded, got %v", got)
	}
	if reason, _ := skippedReason(t, result, "c"); reason != "auto_schedule_disabled" {
		t.Fatalf("expected auto_schedule_disabled reason, got %q", reason)
	}
}
