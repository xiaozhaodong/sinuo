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

// pruneRuntimeLocked 让组级运行态与 retained 跟随最新快照收敛。
// scope key 含 RouteRevision，所以这里同时回收了「组已删除」和「组 revision 已 bump」
// 两种陈旧 retained——后者在 revision 变化的瞬间就已失效，此处只是回收内存。
func (s *ModelRoutingService) pruneRuntimeLocked(snapshot *modelrouting.Snapshot) {
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
	for groupID := range s.runtime {
		if _, ok := validGroupIDs[groupID]; !ok {
			delete(s.runtime, groupID)
		}
	}
	s.runtimeMu.Unlock()

	if s.scopePruner != nil {
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

func (s *ModelRoutingService) NoteDecision(groupID int64, effectiveEndpoint string) {
	if s == nil || groupID == 0 {
		return
	}
	s.runtimeMu.Lock()
	runtime := s.runtime[groupID]
	runtime.LastEffectiveEndpoint = effectiveEndpoint
	runtime.LastDecisionAt = time.Now()
	s.runtime[groupID] = runtime
	s.runtimeMu.Unlock()
}

func (s *ModelRoutingService) SettleDecision(groupID int64, effectiveEndpoint, outcome string) {
	if s == nil || groupID == 0 {
		return
	}
	s.runtimeMu.Lock()
	runtime := s.runtime[groupID]
	if effectiveEndpoint != "" {
		runtime.LastEffectiveEndpoint = effectiveEndpoint
	}
	runtime.LastOutcome = outcome
	runtime.LastDecisionAt = time.Now()
	s.runtime[groupID] = runtime
	s.runtimeMu.Unlock()
}

func (s *ModelRoutingService) ScopeStillValid(scope endpoint.RouteScope) bool {
	if s == nil || scope.Kind != endpoint.RouteScopeUpstreamGroup {
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

func (s *ModelRoutingService) UpdateSettings(ctx context.Context, record *store.ModelRoutingSettingsRecord) (*store.ModelRoutingSettingsRecord, error) {
	if record == nil {
		return nil, fmt.Errorf("model routing settings is nil")
	}
	if err := s.checkStore(); err != nil {
		return nil, err
	}
	candidate := *record
	candidate.ID = 1
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.store.UpdateSettings(ctx, &candidate); err != nil {
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
	// desiredEnabled 是「用户希望模型路由开着吗」的当前最佳认知：
	// 优先用本次读到的 settings 行，读不到才退回上一次已发布快照。
	// 加载失败时它决定 fail-closed 还是保持关闭。
	desiredEnabled := s.CurrentSnapshot().Enabled()
	fail := func(err error) error { return s.publishLoadFailure(desiredEnabled, err) }

	if err := s.checkStore(); err != nil {
		return fail(err)
	}
	settingsRecord, err := s.store.GetSettings(ctx)
	if err != nil {
		return fail(err)
	}
	if settingsRecord == nil {
		return fail(fmt.Errorf("model routing settings row is missing"))
	}
	desiredEnabled = settingsRecord.ClaudeEnabled
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
	s.snapshot.Store(snapshot)
	s.status.Store(ModelRoutingStatusReady)
	s.pruneRuntimeLocked(snapshot)
	return nil
}

// publishLoadFailure 发布 fail-closed 失败快照：开关曾经打开就让所有模型判为未分配（422），
// 不静默回落到全局端点路由；从未打开过则保持关闭，避免一次数据库抖动打死 Claude 转发。
func (s *ModelRoutingService) publishLoadFailure(desiredEnabled bool, err error) error {
	snapshot := modelrouting.FailedSnapshot(desiredEnabled)
	snapshot.Version = s.version.Add(1)
	s.snapshot.Store(snapshot)
	s.status.Store(ModelRoutingStatusLoadFailed)
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
