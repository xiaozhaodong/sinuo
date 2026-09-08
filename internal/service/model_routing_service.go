package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cc-forwarder/internal/endpoint"
	"cc-forwarder/internal/modelrouting"
	"cc-forwarder/internal/store"
)

const (
	ModelRoutingStatusReady      = "ready"
	ModelRoutingStatusLoadFailed = "load_failed"
)

// EndpointNameChecker 用于校验组成员仍然是当前存在的端点。
// P0 可以不注入；注入后会启用端点存在性校验，供后续 endpoint manager 接线复用。
type EndpointNameChecker func(name string) bool

// ModelRoutingService 管理模型路由配置和原子只读快照。
// 请求链路接入前，快照只提供给上层查询和后续运行态编排使用。
type ModelRoutingService struct {
	store         store.ModelRoutingStore
	endpointCheck EndpointNameChecker
	endpointView  endpoint.EndpointMemberViewer
	scopePruner   func(validScopeKeys map[string]struct{})
	mu            *sync.Mutex
	snapshot      atomic.Pointer[modelrouting.Snapshot]
	version       atomic.Int64
	status        atomic.Value // string
	runtimeMu     sync.RWMutex
	runtime       map[int64]ModelRoutingGroupRuntime
}

// ModelRoutingGroupRuntime 是组级运行态，不写入配置表。
type ModelRoutingGroupRuntime struct {
	LastEffectiveEndpoint string
	LastDecisionAt        time.Time
	LastOutcome           string
}

// NewModelRoutingService 创建模型路由服务。
func NewModelRoutingService(st store.ModelRoutingStore, checks ...EndpointNameChecker) *ModelRoutingService {
	svc := &ModelRoutingService{store: st, mu: &sync.Mutex{}, runtime: make(map[int64]ModelRoutingGroupRuntime)}
	if len(checks) > 0 {
		svc.endpointCheck = checks[0]
	}
	svc.status.Store(ModelRoutingStatusReady)
	svc.snapshot.Store(modelrouting.EmptySnapshot())
	return svc
}

// SetWriteMutex 让 App 的 routingMu 同时保护全局 override、模型路由配置和端点删除。
func (s *ModelRoutingService) SetWriteMutex(mu *sync.Mutex) {
	if s != nil && mu != nil {
		s.mu = mu
	}
}

// SetEndpointMemberViewer 注入端点运行态视图提供器。
func (s *ModelRoutingService) SetEndpointMemberViewer(viewer endpoint.EndpointMemberViewer) {
	if s != nil {
		s.endpointView = viewer
		if viewer != nil {
			s.endpointCheck = func(name string) bool { return viewer(name).Exists }
		}
	}
}

// SetScopePruner 注入作用域剪枝器（通常是 endpoint.Manager.PruneAutoRetentionScopes）。
// 每次快照重建后调用，清掉已删除组遗留的 retained 与运行态。
func (s *ModelRoutingService) SetScopePruner(pruner func(validScopeKeys map[string]struct{})) {
	if s != nil {
		s.scopePruner = pruner
	}
}

// publishSnapshotLocked 在 runtimeMu 内原子完成「发布快照 + 收敛组运行态」。
// 快照发布与运行态写入共用同一把锁，NoteDecision / SettleDecision 的「校验作用域 + 写入」
// 才能与发布线性化，否则校验通过后、写入前发布新快照，旧作用域仍会覆盖当前组运行态。
// scope key 含 RouteRevision，retained 剪枝只是回收内存，放在锁外执行以缩短临界区。
func (s *ModelRoutingService) publishSnapshotLocked(snapshot *modelrouting.Snapshot, status string) {
	groups := snapshot.Groups()
	validScopeKeys := make(map[string]struct{}, len(groups))
	validGroupIDs := make(map[int64]struct{}, len(groups))
	for _, group := range groups {
		validGroupIDs[group.ID] = struct{}{}
		scope := endpoint.RouteScope{
			Kind:     endpoint.RouteScopeUpstreamGroup,
			GroupID:  group.ID,
			Revision: group.RouteRevision,
		}
		validScopeKeys[scope.Key()] = struct{}{}
	}

	s.runtimeMu.Lock()
	s.snapshot.Store(snapshot)
	s.status.Store(status)
	// 加载失败快照没有任何组，此时保留运行态：配置恢复后组仍能显示最近命中。
	if !snapshot.LoadFailed() {
		for groupID := range s.runtime {
			if _, ok := validGroupIDs[groupID]; !ok {
				delete(s.runtime, groupID)
			}
		}
	}
	s.runtimeMu.Unlock()

	if s.scopePruner != nil && !snapshot.LoadFailed() {
		s.scopePruner(validScopeKeys)
	}
}

// ResolveScope 实现 endpoint.RouteScopePolicy。
func (s *ModelRoutingService) ResolveScope(model string) (endpoint.RouteScope, bool) {
	snapshot := s.CurrentSnapshot()
	if snapshot == nil || !snapshot.Enabled() {
		return endpoint.RouteScope{}, false
	}
	model = strings.TrimSpace(model)
	scope := endpoint.RouteScope{
		Kind:             endpoint.RouteScopeUnassigned,
		Mode:             "",
		FallbackEnabled:  false,
		SnapshotVersion:  snapshot.Version,
		UnassignedReason: "model_routing_unassigned",
		Model:            model,
	}
	// fail-closed：配置加载失败但开关仍处于打开状态时，快照里没有任何分配，
	// 所有模型都会落到未分配。此处给出真实原因，避免把「配置坏了」误报成「忘记分配」。
	if snapshot.LoadFailed() {
		scope.UnassignedReason = "model_routing_load_failed"
		return scope, true
	}
	if model == "" {
		scope.UnassignedReason = "model_routing_missing_model"
		return scope, true
	}
	resolution, ok := snapshot.Resolve(model)
	if !ok || resolution.Group == nil {
		return scope, true
	}
	group := resolution.Group
	scope.Kind = endpoint.RouteScopeUpstreamGroup
	scope.GroupID = group.ID
	scope.GroupName = group.Name
	scope.Members = append([]string(nil), group.Members...)
	scope.Mode = endpoint.NormalizeRouteMode(group.RouteMode)
	scope.Target = strings.TrimSpace(group.RouteTarget)
	scope.Revision = group.RouteRevision
	scope.FallbackEnabled = scope.Mode != endpoint.RouteModeManualFixed
	scope.UnassignedReason = ""
	return scope, true
}

// NoteDecision 记录组内本次选中的端点。带 fencing：在途请求期间模型被改派、组策略变更
// 或路由被关闭后，旧作用域不再允许写入组运行态，否则面板会显示已失效组的「最近命中」。
func (s *ModelRoutingService) NoteDecision(scope endpoint.RouteScope, effectiveEndpoint string) {
	s.updateRuntimeFenced(scope, func(runtime *ModelRoutingGroupRuntime) {
		runtime.LastEffectiveEndpoint = effectiveEndpoint
	})
}

// SettleDecision 记录组内本次请求的终态。fencing 语义同 NoteDecision。
func (s *ModelRoutingService) SettleDecision(scope endpoint.RouteScope, effectiveEndpoint, outcome string) {
	s.updateRuntimeFenced(scope, func(runtime *ModelRoutingGroupRuntime) {
		if effectiveEndpoint != "" {
			runtime.LastEffectiveEndpoint = effectiveEndpoint
		}
		runtime.LastOutcome = outcome
	})
}

// updateRuntimeFenced 在 runtimeMu 内先校验作用域再写入。
// 校验与写入必须在同一临界区：快照发布同样持有 runtimeMu（见 publishSnapshotLocked），
// 因此「校验通过 → 新快照发布 → 旧作用域写入」的交错不可能发生。
func (s *ModelRoutingService) updateRuntimeFenced(scope endpoint.RouteScope, apply func(*ModelRoutingGroupRuntime)) {
	if s == nil {
		return
	}
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if !s.ScopeStillValid(scope) {
		return
	}
	runtime := s.runtime[scope.GroupID]
	apply(&runtime)
	runtime.LastDecisionAt = time.Now()
	s.runtime[scope.GroupID] = runtime
}

// ScopeStillValid 判断作用域是否仍是当前快照中的有效组作用域：
// 快照版本与组 RouteRevision 都必须一致，任一变化都视为作用域失效。
// 本方法只做无锁快照读取；需要与写入原子的调用方（updateRuntimeFenced）自行持有 runtimeMu。
func (s *ModelRoutingService) ScopeStillValid(scope endpoint.RouteScope) bool {
	if s == nil || scope.Kind != endpoint.RouteScopeUpstreamGroup || scope.GroupID == 0 {
		return false
	}
	snapshot := s.CurrentSnapshot()
	if snapshot == nil || !snapshot.Enabled() || snapshot.Version != scope.SnapshotVersion {
		return false
	}
	group, ok := snapshot.Group(scope.GroupID)
	return ok && group.RouteRevision == scope.Revision
}

func (s *ModelRoutingService) GroupRuntime(groupID int64) ModelRoutingGroupRuntime {
	if s == nil {
		return ModelRoutingGroupRuntime{}
	}
	s.runtimeMu.RLock()
	runtime := s.runtime[groupID]
	s.runtimeMu.RUnlock()
	return runtime
}

func (s *ModelRoutingService) EndpointMember(name string) endpoint.EndpointMemberView {
	if s == nil || s.endpointView == nil {
		return endpoint.EndpointMemberView{Name: strings.TrimSpace(name)}
	}
	return s.endpointView(name)
}

// ModelsUsingEndpoint 返回仍把该端点列为上游的模型名，供端点删除守卫给出可操作的提示。
// 调用方应在 routingMu 内调用，以和端点删除、映射编辑串行化。
func (s *ModelRoutingService) ModelsUsingEndpoint(ctx context.Context, name string) ([]string, error) {
	if err := s.checkStore(); err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	bindings, err := s.ListBindings(ctx)
	if err != nil {
		return nil, err
	}
	models := make([]string, 0)
	for _, binding := range bindings {
		for _, endpointName := range binding.Endpoints {
			if strings.TrimSpace(endpointName) == name {
				models = append(models, binding.Model)
				break
			}
		}
	}
	return models, nil
}

// Initialize 启动时加载数据库配置并发布快照。
func (s *ModelRoutingService) Initialize(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("model routing service is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rebuildSnapshotLocked(ctx)
}

// CurrentSnapshot 返回当前只读快照。
func (s *ModelRoutingService) CurrentSnapshot() *modelrouting.Snapshot {
	if s == nil {
		return nil
	}
	return s.snapshot.Load()
}

// SnapshotVersion 返回当前快照版本。
func (s *ModelRoutingService) SnapshotVersion() int64 {
	if s == nil {
		return 0
	}
	return s.CurrentSnapshot().Version
}

// Status 返回 ready 或 load_failed。
func (s *ModelRoutingService) Status() string {
	if s == nil {
		return ModelRoutingStatusLoadFailed
	}
	if status, ok := s.status.Load().(string); ok {
		return status
	}
	return ModelRoutingStatusReady
}

func (s *ModelRoutingService) GetSettings(ctx context.Context) (*store.ModelRoutingSettingsRecord, error) {
	if err := s.checkStore(); err != nil {
		return nil, err
	}
	return s.store.GetSettings(ctx)
}

// UpdateSettings 按列级 patch 写入全局开关：写入只落 patch 指定的列，调用方无需先读整行，
// 因此并发开启 Claude 与 Codex 不会互相覆盖。空 patch 不重建快照。
func (s *ModelRoutingService) UpdateSettings(ctx context.Context, patch store.ModelRoutingSettingsPatch) (*store.ModelRoutingSettingsRecord, error) {
	if err := s.checkStore(); err != nil {
		return nil, err
	}
	if patch.ClaudeEnabled == nil && patch.CodexEnabled == nil {
		return s.store.GetSettings(ctx)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.store.UpdateSettings(ctx, patch); err != nil {
		return nil, err
	}
	if err := s.rebuildSnapshotLocked(ctx); err != nil {
		return nil, err
	}
	return s.store.GetSettings(ctx)
}

func (s *ModelRoutingService) ListGroups(ctx context.Context) ([]*store.ModelUpstreamGroupRecord, error) {
	if err := s.checkStore(); err != nil {
		return nil, err
	}
	records, err := s.store.ListGroups(ctx)
	return filterClaudeGroups(records), err
}

func (s *ModelRoutingService) GetGroup(ctx context.Context, id int64) (*store.ModelUpstreamGroupRecord, error) {
	if err := s.checkStore(); err != nil {
		return nil, err
	}
	record, err := s.store.GetGroup(ctx, id)
	if err != nil || record == nil || record.Family == modelrouting.FamilyClaude || record.Family == "" {
		return record, err
	}
	return nil, nil
}

func (s *ModelRoutingService) CreateGroup(ctx context.Context, record *store.ModelUpstreamGroupRecord) (*store.ModelUpstreamGroupRecord, error) {
	if record == nil {
		return nil, fmt.Errorf("model upstream group is nil")
	}
	if err := s.checkStore(); err != nil {
		return nil, err
	}
	candidate := *record
	normalizeGroupRecord(&candidate)
	candidate.ID = 0

	s.mu.Lock()
	defer s.mu.Unlock()
	groups, _, err := s.loadRecords(ctx)
	if err != nil {
		return nil, err
	}
	if len(groups) >= modelrouting.MaxGroups {
		return nil, fmt.Errorf("上游组不能超过 %d 个", modelrouting.MaxGroups)
	}
	if err := s.validateGroupCandidate(candidate, groups); err != nil {
		return nil, err
	}
	created, err := s.store.CreateGroup(ctx, &candidate)
	if err != nil {
		return nil, err
	}
	if err := s.rebuildSnapshotLocked(ctx); err != nil {
		return nil, err
	}
	return created, nil
}

// ModelUpstreamGroupPatch 描述一次组更新的意图。nil 字段表示「保持原值」。
// 用 patch 而不是整条记录，是为了让「只改组内策略」不会把并发写入的成员变更覆盖掉。
type ModelUpstreamGroupPatch struct {
	Name        *string
	Members     *[]string
	RouteMode   *string
	RouteTarget *string
}

// UpdateGroup 在写锁内完成读—改—写，调用方不需要（也不应该）先自行读取记录。
func (s *ModelRoutingService) UpdateGroup(ctx context.Context, id int64, patch ModelUpstreamGroupPatch) error {
	if id <= 0 {
		return fmt.Errorf("invalid model upstream group")
	}
	if err := s.checkStore(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	existing, err := s.store.GetGroup(ctx, id)
	if err != nil {
		return err
	}
	if existing == nil {
		return fmt.Errorf("model upstream group %d not found", id)
	}
	if existing.Family != "" && existing.Family != modelrouting.FamilyClaude {
		return fmt.Errorf("model upstream group %d 不属于 Claude 路由", id)
	}

	candidate := *existing
	if patch.Name != nil {
		candidate.Name = *patch.Name
	}
	if patch.Members != nil {
		membersJSON, encodeErr := modelrouting.EncodeMembers(*patch.Members)
		if encodeErr != nil {
			return encodeErr
		}
		candidate.MembersJSON = membersJSON
	}
	if patch.RouteMode != nil {
		candidate.RouteMode = *patch.RouteMode
	}
	if patch.RouteTarget != nil {
		candidate.RouteTarget = *patch.RouteTarget
	}
	normalizeGroupRecord(&candidate)

	groups, _, err := s.loadRecords(ctx)
	if err != nil {
		return err
	}
	if err := s.validateGroupCandidate(candidate, groups); err != nil {
		return err
	}
	candidate.RouteRevision = existing.RouteRevision + 1
	if err := s.store.UpdateGroup(ctx, &candidate); err != nil {
		return err
	}
	return s.rebuildSnapshotLocked(ctx)
}

func (s *ModelRoutingService) DeleteGroup(ctx context.Context, id int64) error {
	if err := s.checkStore(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	groups, assignments, err := s.loadRecords(ctx)
	if err != nil {
		return err
	}
	found := false
	for _, group := range groups {
		if group.ID == id {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("model upstream group %d not found", id)
	}
	for _, assignment := range assignments {
		if assignment.GroupID == id {
			return fmt.Errorf("上游组 %d 仍被模型分配引用", id)
		}
	}
	if err := s.store.DeleteGroup(ctx, id); err != nil {
		return err
	}
	return s.rebuildSnapshotLocked(ctx)
}

func (s *ModelRoutingService) ListAssignments(ctx context.Context) ([]*store.ModelRoutingAssignmentRecord, error) {
	if err := s.checkStore(); err != nil {
		return nil, err
	}
	records, err := s.store.ListAssignments(ctx)
	return filterClaudeAssignments(records), err
}

func (s *ModelRoutingService) GetAssignment(ctx context.Context, id int64) (*store.ModelRoutingAssignmentRecord, error) {
	if err := s.checkStore(); err != nil {
		return nil, err
	}
	record, err := s.store.GetAssignment(ctx, id)
	if err != nil || record == nil || record.Family == modelrouting.FamilyClaude || record.Family == "" {
		return record, err
	}
	return nil, nil
}

func (s *ModelRoutingService) CreateAssignment(ctx context.Context, record *store.ModelRoutingAssignmentRecord) (*store.ModelRoutingAssignmentRecord, error) {
	if record == nil {
		return nil, fmt.Errorf("model routing assignment is nil")
	}
	if err := s.checkStore(); err != nil {
		return nil, err
	}
	candidate := *record
	normalizeAssignmentRecord(&candidate)
	candidate.ID = 0
	s.mu.Lock()
	defer s.mu.Unlock()
	groups, assignments, err := s.loadRecords(ctx)
	if err != nil {
		return nil, err
	}
	if len(assignments) >= modelrouting.MaxAssignments {
		return nil, fmt.Errorf("模型分配不能超过 %d 条", modelrouting.MaxAssignments)
	}
	if err := s.validateAssignmentCandidate(candidate, assignments, groups); err != nil {
		return nil, err
	}
	created, err := s.store.CreateAssignment(ctx, &candidate)
	if err != nil {
		return nil, err
	}
	if err := s.rebuildSnapshotLocked(ctx); err != nil {
		return nil, err
	}
	return created, nil
}

// ModelRoutingAssignmentPatch 描述一次分配更新的意图。nil 字段表示「保持原值」。
type ModelRoutingAssignmentPatch struct {
	ModelName *string
	GroupID   *int64
	Enabled   *bool
	Note      *string
	MatchType *string
}

// UpdateAssignment 在写锁内完成读—改—写，语义与 UpdateGroup 一致。
func (s *ModelRoutingService) UpdateAssignment(ctx context.Context, id int64, patch ModelRoutingAssignmentPatch) error {
	if id <= 0 {
		return fmt.Errorf("invalid model routing assignment")
	}
	if err := s.checkStore(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	existing, err := s.store.GetAssignment(ctx, id)
	if err != nil {
		return err
	}
	if existing == nil {
		return fmt.Errorf("model routing assignment %d not found", id)
	}
	if existing.Family != "" && existing.Family != modelrouting.FamilyClaude {
		return fmt.Errorf("model routing assignment %d 不属于 Claude 路由", id)
	}

	candidate := *existing
	if patch.ModelName != nil {
		candidate.ModelName = *patch.ModelName
	}
	if patch.GroupID != nil {
		candidate.GroupID = *patch.GroupID
	}
	if patch.Enabled != nil {
		candidate.Enabled = *patch.Enabled
	}
	if patch.Note != nil {
		candidate.Note = *patch.Note
	}
	if patch.MatchType != nil {
		candidate.MatchType = *patch.MatchType
	}
	normalizeAssignmentRecord(&candidate)

	groups, assignments, err := s.loadRecords(ctx)
	if err != nil {
		return err
	}
	if err := s.validateAssignmentCandidate(candidate, assignments, groups); err != nil {
		return err
	}
	if err := s.store.UpdateAssignment(ctx, &candidate); err != nil {
		return err
	}
	return s.rebuildSnapshotLocked(ctx)
}

func (s *ModelRoutingService) DeleteAssignment(ctx context.Context, id int64) error {
	if err := s.checkStore(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, err := s.store.GetAssignment(ctx, id); err != nil {
		return err
	} else if existing == nil {
		return fmt.Errorf("model routing assignment %d not found", id)
	}
	if err := s.store.DeleteAssignment(ctx, id); err != nil {
		return err
	}
	return s.rebuildSnapshotLocked(ctx)
}

func (s *ModelRoutingService) checkStore() error {
	if s == nil || s.store == nil {
		return fmt.Errorf("model routing service is not initialized")
	}
	return nil
}

func (s *ModelRoutingService) rebuildSnapshotLocked(ctx context.Context) error {
	// 开关读取失败时本次运行使用全局路由，保留数据库中的配置供用户重新开启。
	if err := s.checkStore(); err != nil {
		return s.publishLoadFailure(false, err)
	}
	settingsRecord, err := s.store.GetSettings(ctx)
	if err != nil {
		return s.publishLoadFailure(false, err)
	}
	if settingsRecord == nil {
		return s.publishLoadFailure(false, fmt.Errorf("model routing settings row is missing"))
	}
	fail := func(err error) error { return s.publishLoadFailure(settingsRecord.ClaudeEnabled, err) }
	groupRecords, err := s.store.ListGroups(ctx)
	if err != nil {
		return fail(err)
	}
	assignmentRecords, err := s.store.ListAssignments(ctx)
	if err != nil {
		return fail(err)
	}
	groups, err := groupsFromRecords(filterClaudeGroups(groupRecords))
	if err != nil {
		return fail(err)
	}
	assignments, err := assignmentsFromRecords(filterClaudeAssignments(assignmentRecords))
	if err != nil {
		return fail(err)
	}
	snapshot, err := modelrouting.Compile(modelrouting.Settings{Enabled: settingsRecord.ClaudeEnabled, UpdatedAt: settingsRecord.UpdatedAt}, groups, assignments)
	if err != nil {
		return fail(err)
	}
	snapshot.Version = s.version.Add(1)
	s.publishSnapshotLocked(snapshot, ModelRoutingStatusReady)
	return nil
}

// publishLoadFailure 只更新运行态；开关读取失败传 false，重新开启时再加载已保存的绑定。
// 已明确开启但绑定数据加载失败时，仍按既有契约返回 422。
func (s *ModelRoutingService) publishLoadFailure(desiredEnabled bool, err error) error {
	snapshot := modelrouting.FailedSnapshot(desiredEnabled)
	snapshot.Version = s.version.Add(1)
	s.publishSnapshotLocked(snapshot, ModelRoutingStatusLoadFailed)
	return err
}

func (s *ModelRoutingService) loadRecords(ctx context.Context) ([]modelrouting.Group, []modelrouting.Assignment, error) {
	groupRecords, err := s.store.ListGroups(ctx)
	if err != nil {
		return nil, nil, err
	}
	assignmentRecords, err := s.store.ListAssignments(ctx)
	if err != nil {
		return nil, nil, err
	}
	groups, err := groupsFromRecords(filterClaudeGroups(groupRecords))
	if err != nil {
		return nil, nil, err
	}
	assignments, err := assignmentsFromRecords(filterClaudeAssignments(assignmentRecords))
	if err != nil {
		return nil, nil, err
	}
	return groups, assignments, nil
}
