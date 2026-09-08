package service

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cc-forwarder/internal/endpoint"
	"cc-forwarder/internal/modelrouting"
	"cc-forwarder/internal/store"

	_ "modernc.org/sqlite"
)

func newModelRoutingServiceForTest(t *testing.T, check EndpointNameChecker) (*ModelRoutingService, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "model-routing-service.db")+"?_foreign_keys=1")
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return NewModelRoutingService(store.NewSQLiteModelRoutingStore(db), check), db
}

func boolPtr(value bool) *bool { return &value }

func TestModelRoutingService_InitializeCreateAndResolve(t *testing.T) {
	service, _ := newModelRoutingServiceForTest(t, func(name string) bool { return name == "endpoint-a" })
	ctx := context.Background()
	if err := service.Initialize(ctx); err != nil {
		t.Fatalf("initialize failed: %v", err)
	}
	if service.Status() != ModelRoutingStatusReady || service.SnapshotVersion() == 0 {
		t.Fatalf("unexpected initial service state: status=%s version=%d", service.Status(), service.SnapshotVersion())
	}
	group, err := service.CreateGroup(ctx, &store.ModelUpstreamGroupRecord{
		Name: "primary", MembersJSON: `["endpoint-a"]`, RouteMode: "auto",
	})
	if err != nil {
		t.Fatalf("create group failed: %v", err)
	}
	if _, err := service.CreateAssignment(ctx, &store.ModelRoutingAssignmentRecord{
		ModelName: "claude-sonnet", GroupID: group.ID, Enabled: true,
	}); err != nil {
		t.Fatalf("create assignment failed: %v", err)
	}
	mode, target := "manual_fixed", "endpoint-a"
	if err := service.UpdateGroup(ctx, group.ID, ModelUpstreamGroupPatch{RouteMode: &mode, RouteTarget: &target}); err != nil {
		t.Fatalf("update group failed: %v", err)
	}
	updatedGroup, err := service.GetGroup(ctx, group.ID)
	if err != nil || updatedGroup.RouteRevision != 1 {
		t.Fatalf("route revision should increment from persisted value: %#v, %v", updatedGroup, err)
	}
	if _, err := service.UpdateSettings(ctx, store.ModelRoutingSettingsPatch{ClaudeEnabled: boolPtr(true)}); err != nil {
		t.Fatalf("enable model routing failed: %v", err)
	}
	resolved, ok := service.CurrentSnapshot().Resolve(" CLAUDE-SONNET ")
	if !ok || resolved.Group.Name != "primary" {
		t.Fatalf("resolution failed: %#v, %v", resolved, ok)
	}
	scope, ok := service.ResolveScope("claude-sonnet")
	if !ok || scope.Kind != endpoint.RouteScopeUpstreamGroup || scope.Target != "endpoint-a" || scope.SnapshotVersion == 0 {
		t.Fatalf("unexpected route scope: %#v, %v", scope, ok)
	}
	if !service.ScopeStillValid(scope) {
		t.Fatal("fresh route scope should remain valid")
	}
	unassigned, ok := service.ResolveScope("unknown-model")
	if !ok || unassigned.Kind != endpoint.RouteScopeUnassigned || unassigned.UnassignedReason != "model_routing_unassigned" {
		t.Fatalf("unexpected unassigned scope: %#v, %v", unassigned, ok)
	}
}

func TestModelRoutingService_RejectsInvalidWritesWithoutPublishing(t *testing.T) {
	service, _ := newModelRoutingServiceForTest(t, nil)
	ctx := context.Background()
	if err := service.Initialize(ctx); err != nil {
		t.Fatalf("initialize failed: %v", err)
	}
	version := service.SnapshotVersion()
	if _, err := service.CreateGroup(ctx, &store.ModelUpstreamGroupRecord{
		Name: "invalid", MembersJSON: `[]`, RouteMode: "auto",
	}); err == nil {
		t.Fatal("empty group members should fail")
	}
	if service.SnapshotVersion() != version {
		t.Fatalf("rejected write changed snapshot version: before=%d after=%d", version, service.SnapshotVersion())
	}
	groups, err := service.ListGroups(ctx)
	if err != nil {
		t.Fatalf("list groups failed: %v", err)
	}
	if len(groups) != 0 {
		t.Fatalf("invalid group was persisted: %#v", groups)
	}
}

// TestModelRoutingService_LoadFailureWhileDisabledStaysOpen 保证从未启用模型路由的用户
// 不会因为一次数据库抖动就被 fail-closed 打死：开关关着，失败快照也保持关着。
func TestModelRoutingService_LoadFailureWhileDisabledStaysOpen(t *testing.T) {
	service, db := newModelRoutingServiceForTest(t, nil)
	ctx := context.Background()
	if err := service.Initialize(ctx); err != nil {
		t.Fatalf("initialize failed: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close db failed: %v", err)
	}
	if err := service.Initialize(ctx); err == nil {
		t.Fatal("initialize on closed db should fail")
	}
	if service.Status() != ModelRoutingStatusLoadFailed {
		t.Fatalf("unexpected failure status: %s", service.Status())
	}
	if service.CurrentSnapshot().Enabled() {
		t.Fatal("routing was never enabled: the failure snapshot must stay disabled")
	}
	if _, ok := service.ResolveScope("claude-sonnet"); ok {
		t.Fatal("model routing was never enabled: requests must keep using global routing")
	}
}

func TestModelRoutingService_EndpointAndGroupReferenceGuards(t *testing.T) {
	service, _ := newModelRoutingServiceForTest(t, func(name string) bool { return strings.HasPrefix(name, "endpoint-") })
	ctx := context.Background()
	if err := service.Initialize(ctx); err != nil {
		t.Fatalf("initialize failed: %v", err)
	}
	if _, err := service.CreateGroup(ctx, &store.ModelUpstreamGroupRecord{
		Name: "primary", MembersJSON: `["missing"]`, RouteMode: "auto",
	}); err == nil {
		t.Fatal("unknown endpoint should fail")
	}
	group, err := service.CreateGroup(ctx, &store.ModelUpstreamGroupRecord{
		Name: "primary", MembersJSON: `["endpoint-a"]`, RouteMode: "auto",
	})
	if err != nil {
		t.Fatalf("create valid group failed: %v", err)
	}
	if _, err := service.CreateAssignment(ctx, &store.ModelRoutingAssignmentRecord{
		ModelName: "claude-sonnet", GroupID: group.ID, MatchType: "prefix", Enabled: true,
	}); err == nil {
		t.Fatal("prefix assignment should fail")
	}
	if err := service.DeleteGroup(ctx, group.ID); err != nil {
		t.Fatalf("delete unused group failed: %v", err)
	}
}

// 已明确读到开启的开关后，绑定数据损坏仍按原有规则返回 422。
func TestModelRoutingService_BindingLoadFailureIsFailClosed(t *testing.T) {
	service, db := newModelRoutingServiceForTest(t, func(name string) bool { return name == "endpoint-a" })
	ctx := context.Background()
	if err := service.Initialize(ctx); err != nil {
		t.Fatalf("initialize failed: %v", err)
	}
	group, err := service.CreateGroup(ctx, &store.ModelUpstreamGroupRecord{
		Name: "primary", MembersJSON: `["endpoint-a"]`, RouteMode: "auto",
	})
	if err != nil {
		t.Fatalf("create group failed: %v", err)
	}
	if _, err := service.CreateAssignment(ctx, &store.ModelRoutingAssignmentRecord{
		ModelName: "claude-sonnet", GroupID: group.ID, Enabled: true,
	}); err != nil {
		t.Fatalf("create assignment failed: %v", err)
	}
	if _, err := service.UpdateSettings(ctx, store.ModelRoutingSettingsPatch{ClaudeEnabled: boolPtr(true)}); err != nil {
		t.Fatalf("enable model routing failed: %v", err)
	}
	if scope, ok := service.ResolveScope("claude-sonnet"); !ok || scope.Kind != endpoint.RouteScopeUpstreamGroup {
		t.Fatalf("expected group scope before failure, got %#v ok=%v", scope, ok)
	}

	// 开关仍能读取，仅破坏绑定数据。
	if _, err := db.ExecContext(ctx, `UPDATE model_upstream_groups SET members_json = ? WHERE id = ?`, `[]`, group.ID); err != nil {
		t.Fatalf("构造损坏的绑定失败: %v", err)
	}
	if err := service.Initialize(ctx); err == nil {
		t.Fatal("绑定数据损坏时应加载失败")
	}
	if service.Status() != ModelRoutingStatusLoadFailed {
		t.Fatalf("expected load_failed status, got %s", service.Status())
	}

	scope, ok := service.ResolveScope("claude-sonnet")
	if !ok {
		t.Fatal("fail-closed: model routing must stay authoritative after a load failure")
	}
	if scope.Kind != endpoint.RouteScopeUnassigned {
		t.Fatalf("fail-closed: expected unassigned scope, got %q", scope.Kind)
	}
	if scope.UnassignedReason != "model_routing_load_failed" {
		t.Fatalf("expected load-failure reason, got %q", scope.UnassignedReason)
	}
}

type modelRoutingSettingsReadFailureStore struct {
	store.ModelRoutingStore
	fail bool
}

func (s *modelRoutingSettingsReadFailureStore) GetSettings(ctx context.Context) (*store.ModelRoutingSettingsRecord, error) {
	if s.fail {
		return nil, errors.New("临时设置读取错误")
	}
	return s.ModelRoutingStore.GetSettings(ctx)
}

func TestModelRoutingServices_SettingsReadFailureFallsBackAndRecovers(t *testing.T) {
	for _, family := range []string{modelrouting.FamilyClaude, modelrouting.FamilyCodex} {
		t.Run(family, func(t *testing.T) {
			ctx := context.Background()
			base := newCodexModelRoutingServiceForTest(t)
			st := &modelRoutingSettingsReadFailureStore{ModelRoutingStore: base.store}
			var initialize func(context.Context) error
			var enabled func() bool
			var resolve func() (string, bool)
			var enable func(context.Context) error
			if family == modelrouting.FamilyClaude {
				svc := NewModelRoutingService(st)
				if _, err := svc.CreateBinding(ctx, ModelRouteBindingInput{Model: "test-model", Endpoints: []string{"endpoint-a"}, RouteMode: modelrouting.RouteModeAuto, Enabled: true}); err != nil {
					t.Fatal(err)
				}
				initialize = svc.Initialize
				enabled = func() bool { return svc.CurrentSnapshot().Enabled() }
				resolve = func() (string, bool) { scope, ok := svc.ResolveScope("test-model"); return scope.Kind, ok }
				enable = func(ctx context.Context) error {
					_, err := svc.UpdateSettings(ctx, store.ModelRoutingSettingsPatch{ClaudeEnabled: boolPtr(true), CodexEnabled: boolPtr(true)})
					return err
				}
			} else {
				svc := NewCodexModelRoutingService(st, base.listAccounts)
				if _, err := svc.CreateBinding(ctx, CodexModelRouteBindingInput{Model: "test-model", AccountIDs: []int64{1}, RouteMode: modelrouting.RouteModeAuto, Enabled: true}); err != nil {
					t.Fatal(err)
				}
				initialize, enabled = svc.Initialize, svc.Enabled
				resolve = func() (string, bool) { scope, ok := svc.ResolveScope("test-model"); return scope.Kind, ok }
				enable = func(ctx context.Context) error { return svc.UpdateEnabled(ctx, true) }
			}
			if err := base.store.UpdateSettings(ctx, store.ModelRoutingSettingsPatch{ClaudeEnabled: boolPtr(true), CodexEnabled: boolPtr(true)}); err != nil {
				t.Fatal(err)
			}
			if err := initialize(ctx); err != nil {
				t.Fatal(err)
			}
			if kind, ok := resolve(); !ok || kind != "upstream_group" {
				t.Fatalf("初始模型绑定未生效: kind=%s applies=%v", kind, ok)
			}
			st.fail = true
			if err := initialize(ctx); err == nil {
				t.Fatal("应返回临时设置读取错误")
			}
			if kind, ok := resolve(); enabled() || ok {
				t.Fatalf("开关读取失败后未恢复全局路由: enabled=%v kind=%s applies=%v", enabled(), kind, ok)
			}
			stored, err := base.store.GetSettings(ctx)
			if err != nil || !stored.ClaudeEnabled || !stored.CodexEnabled {
				t.Fatalf("运行时降级不应覆盖已保存开关: settings=%+v err=%v", stored, err)
			}
			st.fail = false
			if err := enable(ctx); err != nil {
				t.Fatalf("读取恢复后重新开启失败: %v", err)
			}
			if kind, ok := resolve(); !enabled() || !ok || kind != "upstream_group" {
				t.Fatalf("重新开启后绑定未恢复: enabled=%v kind=%s applies=%v", enabled(), kind, ok)
			}
		})
	}
}

// TestModelRoutingService_StaleScopeCannotWriteRuntime 锁定组运行态的 fencing 语义：
// 在途请求拿到的作用域，在模型被改派到别的组、组策略被修改或路由被关闭之后，
// 结束时不得再写入旧组的「最近命中端点 / 最近结局」。
func TestModelRoutingService_StaleScopeCannotWriteRuntime(t *testing.T) {
	service, _ := newModelRoutingServiceForTest(t, func(name string) bool { return name == "endpoint-a" || name == "endpoint-b" })
	ctx := context.Background()
	if err := service.Initialize(ctx); err != nil {
		t.Fatalf("initialize failed: %v", err)
	}
	groupA, err := service.CreateGroup(ctx, &store.ModelUpstreamGroupRecord{Name: "group-a", MembersJSON: `["endpoint-a"]`, RouteMode: "auto"})
	if err != nil {
		t.Fatalf("create group a failed: %v", err)
	}
	groupB, err := service.CreateGroup(ctx, &store.ModelUpstreamGroupRecord{Name: "group-b", MembersJSON: `["endpoint-b"]`, RouteMode: "auto"})
	if err != nil {
		t.Fatalf("create group b failed: %v", err)
	}
	assignment, err := service.CreateAssignment(ctx, &store.ModelRoutingAssignmentRecord{ModelName: "claude-sonnet", GroupID: groupA.ID, Enabled: true})
	if err != nil {
		t.Fatalf("create assignment failed: %v", err)
	}
	if _, err := service.UpdateSettings(ctx, store.ModelRoutingSettingsPatch{ClaudeEnabled: boolPtr(true)}); err != nil {
		t.Fatalf("enable model routing failed: %v", err)
	}

	// 请求 1：在改派前解析出的作用域，正常写入组 A 运行态。
	inFlight, ok := service.ResolveScope("claude-sonnet")
	if !ok || inFlight.GroupID != groupA.ID {
		t.Fatalf("expected scope on group a, got %#v", inFlight)
	}
	service.NoteDecision(inFlight, "endpoint-a")
	service.SettleDecision(inFlight, "endpoint-a", "success")
	if runtime := service.GroupRuntime(groupA.ID); runtime.LastEffectiveEndpoint != "endpoint-a" || runtime.LastOutcome != "success" {
		t.Fatalf("fresh scope should update runtime, got %#v", runtime)
	}

	// 用户把模型改派到组 B：快照版本变化，旧作用域失效。
	if err := service.UpdateAssignment(ctx, assignment.ID, ModelRoutingAssignmentPatch{GroupID: &groupB.ID}); err != nil {
		t.Fatalf("reassign failed: %v", err)
	}
	if service.ScopeStillValid(inFlight) {
		t.Fatal("scope resolved before reassignment must be invalid afterwards")
	}
	service.NoteDecision(inFlight, "endpoint-stale")
	service.SettleDecision(inFlight, "endpoint-stale", "failed_after_commit")
	if runtime := service.GroupRuntime(groupA.ID); runtime.LastEffectiveEndpoint != "endpoint-a" || runtime.LastOutcome != "success" {
		t.Fatalf("stale scope must not overwrite group a runtime, got %#v", runtime)
	}

	// 组内策略变更 bump RouteRevision：同组、同快照之前拿到的作用域同样失效。
	current, ok := service.ResolveScope("claude-sonnet")
	if !ok || current.GroupID != groupB.ID {
		t.Fatalf("expected scope on group b, got %#v", current)
	}
	mode, target := "manual_preferred", "endpoint-b"
	if err := service.UpdateGroup(ctx, groupB.ID, ModelUpstreamGroupPatch{RouteMode: &mode, RouteTarget: &target}); err != nil {
		t.Fatalf("update group b failed: %v", err)
	}
	service.SettleDecision(current, "endpoint-b", "success")
	if runtime := service.GroupRuntime(groupB.ID); runtime.LastOutcome != "" {
		t.Fatalf("scope from previous revision must not write runtime, got %#v", runtime)
	}

	// 关闭路由后，任何组作用域都不再有效。
	fresh, ok := service.ResolveScope("claude-sonnet")
	if !ok || fresh.Kind != endpoint.RouteScopeUpstreamGroup {
		t.Fatalf("expected group scope before disabling, got %#v", fresh)
	}
	if _, err := service.UpdateSettings(ctx, store.ModelRoutingSettingsPatch{ClaudeEnabled: boolPtr(false)}); err != nil {
		t.Fatalf("disable model routing failed: %v", err)
	}
	service.SettleDecision(fresh, "endpoint-b", "success")
	if runtime := service.GroupRuntime(groupB.ID); runtime.LastOutcome != "" {
		t.Fatalf("scope must be fenced after routing is disabled, got %#v", runtime)
	}
}

// TestModelRoutingService_RuntimeFencingIsAtomicWithPublish 锁定 fencing 的原子性：
// 「校验作用域 + 写入运行态」与「发布新快照」必须共用 runtimeMu，
// 否则校验通过后、写入前发布新快照，旧作用域仍能覆盖当前组运行态（TOCTOU）。
// 测试直接持有 runtimeMu 制造交错窗口，只有校验发生在锁内才能通过。
func TestModelRoutingService_RuntimeFencingIsAtomicWithPublish(t *testing.T) {
	service, _ := newModelRoutingServiceForTest(t, func(name string) bool { return name == "endpoint-a" })
	ctx := context.Background()
	if err := service.Initialize(ctx); err != nil {
		t.Fatalf("initialize failed: %v", err)
	}
	group, err := service.CreateGroup(ctx, &store.ModelUpstreamGroupRecord{Name: "group-a", MembersJSON: `["endpoint-a"]`, RouteMode: "auto"})
	if err != nil {
		t.Fatalf("create group failed: %v", err)
	}
	assignment, err := service.CreateAssignment(ctx, &store.ModelRoutingAssignmentRecord{ModelName: "claude-sonnet", GroupID: group.ID, Enabled: true})
	if err != nil {
		t.Fatalf("create assignment failed: %v", err)
	}
	if _, err := service.UpdateSettings(ctx, store.ModelRoutingSettingsPatch{ClaudeEnabled: boolPtr(true)}); err != nil {
		t.Fatalf("enable model routing failed: %v", err)
	}
	scope, ok := service.ResolveScope("claude-sonnet")
	if !ok || !service.ScopeStillValid(scope) {
		t.Fatalf("expected a valid in-flight scope, got %#v", scope)
	}

	// 交错 1：写入方在校验前就被 runtimeMu 挡住；此时发布新快照，再放行写入。
	// 校验若在锁外发生会看到旧快照并放行写入；在锁内发生则必须看到新快照并拒绝。
	service.runtimeMu.Lock()
	settled := make(chan struct{})
	go func() {
		defer close(settled)
		service.SettleDecision(scope, "endpoint-stale", "success")
	}()
	time.Sleep(20 * time.Millisecond) // 给写入方机会先跑到（锁外的）校验点
	stale := modelrouting.FailedSnapshot(true)
	stale.Version = service.version.Add(1)
	service.snapshot.Store(stale)
	service.runtimeMu.Unlock()
	<-settled
	if runtime := service.GroupRuntime(group.ID); runtime.LastOutcome != "" || runtime.LastEffectiveEndpoint != "" {
		t.Fatalf("scope invalidated before the write acquired the lock must not write runtime, got %#v", runtime)
	}

	// 交错 2：发布方必须与运行态写入共用同一把锁——持有 runtimeMu 时快照不得被替换。
	if err := service.Initialize(ctx); err != nil {
		t.Fatalf("re-initialize failed: %v", err)
	}
	before := service.SnapshotVersion()
	service.runtimeMu.Lock()
	published := make(chan error, 1)
	go func() {
		enabled := false
		published <- service.UpdateAssignment(ctx, assignment.ID, ModelRoutingAssignmentPatch{Enabled: &enabled})
	}()
	select {
	case err := <-published:
		service.runtimeMu.Unlock()
		t.Fatalf("publish must block while runtimeMu is held, but returned early: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if service.SnapshotVersion() != before {
		service.runtimeMu.Unlock()
		t.Fatal("snapshot was replaced while runtimeMu was held; publish is not linearized with runtime writes")
	}
	service.runtimeMu.Unlock()
	if err := <-published; err != nil {
		t.Fatalf("publish failed after lock release: %v", err)
	}
	if service.SnapshotVersion() == before {
		t.Fatal("snapshot version should advance once the lock is released")
	}
}
