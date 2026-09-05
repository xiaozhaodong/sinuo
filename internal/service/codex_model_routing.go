package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cc-forwarder/internal/modelrouting"
	"cc-forwarder/internal/store"
)

const (
	CodexModelRoutingMissingModel     = "model_routing_missing_model"
	CodexModelRoutingUnassigned       = "model_routing_unassigned"
	CodexModelRoutingLoadFailed       = "model_routing_load_failed"
	CodexModelRoutingNoAvailable      = "model_routing_no_available_accounts"
	CodexModelRoutingFixedUnavailable = "model_routing_fixed_unavailable"
)

// AccountLister 是账号池的只读视图。模型路由只消费账号状态，不改变账号池调度策略。
type AccountLister func(context.Context, bool) ([]*store.UpstreamAccountRecord, error)

type CodexModelRouteScope struct {
	Kind             string
	GroupID          int64
	GroupName        string
	Members          []int64
	Mode             string
	TargetAccountID  int64
	Revision         int64
	SnapshotVersion  int64
	Model            string
	UnassignedReason string
}

type CodexModelRouteResult struct {
	Accounts []*store.UpstreamAccountRecord
	Scope    CodexModelRouteScope
}

type CodexModelRouteBinding struct {
	AssignmentID   int64
	GroupID        int64
	Model          string
	AccountIDs     []int64
	RouteMode      string
	RouteTargetID  int64
	Enabled        bool
	Note           string
	LastAccountID  int64
	LastOutcome    string
	LastDecisionAt time.Time
	UpdatedAt      time.Time
}

type CodexModelRouteBindingInput struct {
	Model         string
	AccountIDs    []int64
	RouteMode     string
	RouteTargetID int64
	Enabled       bool
	Note          string
}

func (s *CodexModelRoutingService) ListBindings(ctx context.Context) ([]CodexModelRouteBinding, error) {
	groups, err := s.store.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	byGroup := make(map[int64]*store.ModelUpstreamGroupRecord)
	for _, g := range groups {
		if g != nil && g.Family == modelrouting.FamilyCodex {
			byGroup[g.ID] = g
		}
	}
	assignments, err := s.store.ListAssignments(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]CodexModelRouteBinding, 0)
	for _, a := range assignments {
		if a == nil || a.Family != modelrouting.FamilyCodex {
			continue
		}
		g := byGroup[a.GroupID]
		if g == nil {
			continue
		}
		members, parseErr := parseCodexMembers(g.MembersJSON)
		if parseErr != nil {
			return nil, parseErr
		}
		target := int64(0)
		if g.RouteTarget != "" {
			_, _ = fmt.Sscan(g.RouteTarget, &target)
		}
		rt := s.Runtime(g.ID)
		result = append(result, CodexModelRouteBinding{AssignmentID: a.ID, GroupID: g.ID, Model: a.ModelName, AccountIDs: members, RouteMode: g.RouteMode, RouteTargetID: target, Enabled: a.Enabled, Note: a.Note, LastAccountID: rt.LastAccountID, LastOutcome: rt.LastOutcome, LastDecisionAt: rt.LastDecisionAt, UpdatedAt: a.UpdatedAt})
	}
	return result, nil
}

func (s *CodexModelRoutingService) CreateBinding(ctx context.Context, input CodexModelRouteBindingInput) (int64, error) {
	if err := validateCodexBindingInput(input); err != nil {
		return 0, err
	}
	if err := s.validateAccountIDs(ctx, input.AccountIDs); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	groups, err := s.store.ListGroups(ctx)
	if err != nil {
		return 0, err
	}
	assignments, err := s.store.ListAssignments(ctx)
	if err != nil {
		return 0, err
	}
	for _, a := range assignments {
		if a != nil && a.Family == modelrouting.FamilyCodex && strings.EqualFold(strings.TrimSpace(a.ModelName), strings.TrimSpace(input.Model)) {
			return 0, fmt.Errorf("Codex 模型 %q 已经分配过账号组", input.Model)
		}
	}
	members, _ := json.Marshal(input.AccountIDs)
	group, err := s.store.CreateGroup(ctx, &store.ModelUpstreamGroupRecord{Family: modelrouting.FamilyCodex, Name: "codex-" + strings.TrimSpace(input.Model), MembersJSON: string(members), RouteMode: input.RouteMode, RouteTarget: formatAccountTarget(input.RouteTargetID), RouteRevision: 1})
	if err != nil {
		return 0, err
	}
	created, err := s.store.CreateAssignment(ctx, &store.ModelRoutingAssignmentRecord{Family: modelrouting.FamilyCodex, MatchType: modelrouting.MatchExact, ModelName: strings.TrimSpace(input.Model), GroupID: group.ID, Enabled: input.Enabled, Note: input.Note})
	if err != nil {
		_ = s.store.DeleteGroup(ctx, group.ID)
		return 0, err
	}
	if err := s.rebuildLocked(ctx); err != nil {
		return 0, err
	}
	_ = groups
	return created.ID, nil
}

func (s *CodexModelRoutingService) UpdateBinding(ctx context.Context, assignmentID int64, input CodexModelRouteBindingInput) error {
	if assignmentID <= 0 {
		return fmt.Errorf("invalid Codex model binding")
	}
	if err := validateCodexBindingInput(input); err != nil {
		return err
	}
	if err := s.validateAccountIDs(ctx, input.AccountIDs); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	assignment, err := s.store.GetAssignment(ctx, assignmentID)
	if err != nil {
		return err
	}
	if assignment == nil || assignment.Family != modelrouting.FamilyCodex {
		return fmt.Errorf("Codex 模型绑定不存在: %d", assignmentID)
	}
	assignments, err := s.store.ListAssignments(ctx)
	if err != nil {
		return err
	}
	for _, sibling := range assignments {
		if sibling != nil && sibling.ID != assignmentID && sibling.Family == modelrouting.FamilyCodex && strings.EqualFold(strings.TrimSpace(sibling.ModelName), strings.TrimSpace(input.Model)) {
			return fmt.Errorf("Codex 模型 %q 已经分配过账号组", input.Model)
		}
	}
	group, err := s.store.GetGroup(ctx, assignment.GroupID)
	if err != nil || group == nil {
		if err != nil {
			return err
		}
		return fmt.Errorf("Codex 模型绑定账号组不存在: %d", assignment.GroupID)
	}
	members, _ := json.Marshal(input.AccountIDs)
	group.MembersJSON = string(members)
	group.RouteMode = input.RouteMode
	group.RouteTarget = formatAccountTarget(input.RouteTargetID)
	group.RouteRevision++
	if err := s.store.UpdateGroup(ctx, group); err != nil {
		return err
	}
	assignment.ModelName = strings.TrimSpace(input.Model)
	assignment.Enabled = input.Enabled
	assignment.Note = input.Note
	if err := s.store.UpdateAssignment(ctx, assignment); err != nil {
		return err
	}
	return s.rebuildLocked(ctx)
}

func (s *CodexModelRoutingService) DeleteBinding(ctx context.Context, assignmentID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.store.GetAssignment(ctx, assignmentID)
	if err != nil {
		return err
	}
	if a == nil || a.Family != modelrouting.FamilyCodex {
		return fmt.Errorf("Codex 模型绑定不存在: %d", assignmentID)
	}
	if err := s.store.DeleteAssignment(ctx, assignmentID); err != nil {
		return err
	}
	if err := s.store.DeleteGroup(ctx, a.GroupID); err != nil {
		return err
	}
	return s.rebuildLocked(ctx)
}

func validateCodexBindingInput(input CodexModelRouteBindingInput) error {
	if strings.TrimSpace(input.Model) == "" {
		return fmt.Errorf("Codex 模型名不能为空")
	}
	if len(input.AccountIDs) == 0 {
		return fmt.Errorf("Codex 账号组至少需要一个账号")
	}
	seen := map[int64]struct{}{}
	for _, id := range input.AccountIDs {
		if id <= 0 {
			return fmt.Errorf("Codex 账号 ID 必须为正整数")
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("Codex 账号 ID %d 重复", id)
		}
		seen[id] = struct{}{}
	}
	if input.RouteMode != modelrouting.RouteModeAuto && input.RouteMode != modelrouting.RouteModeManualPreferred && input.RouteMode != modelrouting.RouteModeManualFixed {
		return fmt.Errorf("不支持的 Codex 路由模式 %q", input.RouteMode)
	}
	if input.RouteMode == modelrouting.RouteModeAuto && input.RouteTargetID != 0 {
		return fmt.Errorf("Codex 自动模式不能指定目标账号")
	}
	if input.RouteMode != modelrouting.RouteModeAuto {
		if _, ok := seen[input.RouteTargetID]; !ok {
			return fmt.Errorf("Codex 目标账号不在组成员中")
		}
	}
	return nil
}

func (s *CodexModelRoutingService) validateAccountIDs(ctx context.Context, ids []int64) error {
	if s.listAccounts == nil {
		return fmt.Errorf("account lister is not initialized")
	}
	accounts, err := s.listAccounts(ctx, true)
	if err != nil {
		return err
	}
	known := make(map[int64]struct{}, len(accounts))
	for _, account := range accounts {
		if account != nil {
			known[account.ID] = struct{}{}
		}
	}
	for _, id := range ids {
		if _, ok := known[id]; !ok {
			return fmt.Errorf("Codex 账号 %d 不存在", id)
		}
	}
	return nil
}

func formatAccountTarget(id int64) string {
	if id <= 0 {
		return ""
	}
	return fmt.Sprintf("%d", id)
}

type codexModelRoutingSnapshot struct {
	Version    int64
	Enabled    bool
	LoadFailed bool
	Groups     map[int64]*codexModelRoutingGroup
	ByModel    map[string]*codexModelRoutingAssignment
}

type codexModelRoutingGroup struct {
	ID          int64
	Name        string
	Members     []int64
	RouteMode   string
	RouteTarget int64
	Revision    int64
}

type codexModelRoutingAssignment struct {
	ID      int64
	Model   string
	GroupID int64
	Enabled bool
}

// CodexModelRoutingService 提供简单的模型到账号组绑定，不参与现有账号池排序。
type CodexModelRoutingService struct {
	store        store.ModelRoutingStore
	listAccounts AccountLister
	mu           *sync.Mutex
	snapshot     atomic.Pointer[codexModelRoutingSnapshot]
	version      atomic.Int64
	status       atomic.Value
	runtimeMu    sync.RWMutex
	runtime      map[int64]codexModelRouteRuntime
	negativeMu   sync.Mutex
	negativeHits map[string]time.Time
}

type codexModelRouteRuntime struct {
	LastAccountID  int64
	LastOutcome    string
	LastDecisionAt time.Time
}

func NewCodexModelRoutingService(st store.ModelRoutingStore, lister AccountLister) *CodexModelRoutingService {
	s := &CodexModelRoutingService{store: st, listAccounts: lister, mu: &sync.Mutex{}, runtime: make(map[int64]codexModelRouteRuntime), negativeHits: make(map[string]time.Time)}
	s.status.Store(ModelRoutingStatusReady)
	s.snapshot.Store(&codexModelRoutingSnapshot{Groups: map[int64]*codexModelRoutingGroup{}, ByModel: map[string]*codexModelRoutingAssignment{}})
	return s
}

func (s *CodexModelRoutingService) SetWriteMutex(mu *sync.Mutex) {
	if s != nil && mu != nil {
		s.mu = mu
	}
}
func (s *CodexModelRoutingService) Status() string {
	if s == nil {
		return ModelRoutingStatusLoadFailed
	}
	v, _ := s.status.Load().(string)
	if v == "" {
		return ModelRoutingStatusReady
	}
	return v
}
func (s *CodexModelRoutingService) SnapshotVersion() int64 {
	if s == nil || s.snapshot.Load() == nil {
		return 0
	}
	return s.snapshot.Load().Version
}
func (s *CodexModelRoutingService) CurrentSnapshot() *codexModelRoutingSnapshot {
	if s == nil {
		return nil
	}
	return s.snapshot.Load()
}

func (s *CodexModelRoutingService) Initialize(ctx context.Context) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("codex model routing service is not initialized")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rebuildLocked(ctx)
}

func (s *CodexModelRoutingService) Enabled() bool {
	snap := s.CurrentSnapshot()
	return snap != nil && snap.Enabled
}

func (s *CodexModelRoutingService) ResolveScope(model string) (CodexModelRouteScope, bool) {
	snap := s.CurrentSnapshot()
	model = strings.TrimSpace(model)
	scope := CodexModelRouteScope{Kind: "unassigned", Model: model, SnapshotVersion: snapVersion(snap), UnassignedReason: CodexModelRoutingUnassigned}
	if snap == nil || !snap.Enabled {
		return scope, false
	}
	if snap.LoadFailed {
		scope.UnassignedReason = CodexModelRoutingLoadFailed
		return scope, true
	}
	if model == "" {
		scope.UnassignedReason = CodexModelRoutingMissingModel
		return scope, true
	}
	a := snap.ByModel[modelrouting.NormalizeModelName(model)]
	if a == nil || !a.Enabled {
		return scope, true
	}
	g := snap.Groups[a.GroupID]
	if g == nil {
		return scope, true
	}
	scope.Kind = "upstream_group"
	scope.GroupID = g.ID
	scope.GroupName = g.Name
	scope.Members = append([]int64(nil), g.Members...)
	scope.Mode = g.RouteMode
	scope.TargetAccountID = g.RouteTarget
	scope.Revision = g.Revision
	scope.UnassignedReason = ""
	return scope, true
}

func snapVersion(s *codexModelRoutingSnapshot) int64 {
	if s == nil {
		return 0
	}
	return s.Version
}

func (s *CodexModelRoutingService) Prepare(ctx context.Context, model string) (CodexModelRouteResult, error) {
	scope, applies := s.ResolveScope(model)
	if !applies {
		return CodexModelRouteResult{Scope: scope}, nil
	}
	if scope.Kind != "upstream_group" {
		return CodexModelRouteResult{Scope: scope}, nil
	}
	if s.listAccounts == nil {
		return CodexModelRouteResult{Scope: scope}, fmt.Errorf("account lister is not initialized")
	}
	accounts, err := s.listAccounts(ctx, true)
	if err != nil {
		return CodexModelRouteResult{Scope: scope}, err
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
	result := make([]*store.UpstreamAccountRecord, 0, len(ids))
	for _, id := range ids {
		a := byID[id]
		if a == nil || !a.Enabled || strings.TrimSpace(a.CredentialRaw) == "" || a.State == "disabled_auth" {
			continue
		}
		if a.CooldownUntil != nil && time.Now().Before(*a.CooldownUntil) {
			continue
		}
		if s.HasNegativeHit(a.ID, scope.Model) {
			continue
		}
		result = append(result, a)
	}
	if len(result) == 0 {
		return CodexModelRouteResult{Accounts: result, Scope: scope}, nil
	}
	if scope.Mode == modelrouting.RouteModeManualFixed && result[0].ID != scope.TargetAccountID {
		result = nil
	}
	if scope.Mode == modelrouting.RouteModeManualFixed && len(result) > 1 {
		result = result[:1]
	}
	return CodexModelRouteResult{Accounts: result, Scope: scope}, nil
}

func (s *CodexModelRoutingService) RecordNegativeHit(accountID int64, model string) {
	s.negativeMu.Lock()
	defer s.negativeMu.Unlock()
	s.negativeHits[fmt.Sprintf("%d|%s", accountID, modelrouting.NormalizeModelName(model))] = time.Now().Add(30 * time.Minute)
}

func (s *CodexModelRoutingService) HasNegativeHit(accountID int64, model string) bool {
	s.negativeMu.Lock()
	defer s.negativeMu.Unlock()
	key := fmt.Sprintf("%d|%s", accountID, modelrouting.NormalizeModelName(model))
	expiresAt, ok := s.negativeHits[key]
	if ok && time.Now().After(expiresAt) {
		delete(s.negativeHits, key)
		return false
	}
	return ok
}

func (s *CodexModelRoutingService) ClearNegativeHits(accountID int64) {
	s.negativeMu.Lock()
	defer s.negativeMu.Unlock()
	prefix := fmt.Sprintf("%d|", accountID)
	for key := range s.negativeHits {
		if strings.HasPrefix(key, prefix) {
			delete(s.negativeHits, key)
		}
	}
}

func filterID(ids []int64, target int64) []int64 {
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id != target {
			out = append(out, id)
		}
	}
	return out
}

func (s *CodexModelRoutingService) ScopeStillValid(scope CodexModelRouteScope) bool {
	snap := s.CurrentSnapshot()
	if snap == nil || !snap.Enabled || snap.Version != scope.SnapshotVersion {
		return false
	}
	g := snap.Groups[scope.GroupID]
	return g != nil && g.Revision == scope.Revision
}

func (s *CodexModelRoutingService) Settle(scope CodexModelRouteScope, accountID int64, outcome string) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if !s.ScopeStillValid(scope) {
		return
	}
	s.runtime[scope.GroupID] = codexModelRouteRuntime{LastAccountID: accountID, LastOutcome: outcome, LastDecisionAt: time.Now()}
}

func (s *CodexModelRoutingService) Runtime(groupID int64) codexModelRouteRuntime {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	return s.runtime[groupID]
}

// ModelsUsingAccount 返回仍引用账号的 Codex 模型，供删除守卫使用。
func (s *CodexModelRoutingService) ModelsUsingAccount(ctx context.Context, accountID int64) ([]string, error) {
	if s == nil || s.store == nil || accountID <= 0 {
		return nil, nil
	}
	groups, err := s.store.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	groupHasAccount := make(map[int64]bool)
	for _, group := range groups {
		if group == nil || group.Family != modelrouting.FamilyCodex {
			continue
		}
		members, parseErr := parseCodexMembers(group.MembersJSON)
		if parseErr != nil {
			return nil, parseErr
		}
		for _, member := range members {
			if member == accountID {
				groupHasAccount[group.ID] = true
				break
			}
		}
	}
	assignments, err := s.store.ListAssignments(ctx)
	if err != nil {
		return nil, err
	}
	models := make([]string, 0)
	for _, assignment := range assignments {
		if assignment != nil && assignment.Family == modelrouting.FamilyCodex && assignment.Enabled && groupHasAccount[assignment.GroupID] {
			models = append(models, assignment.ModelName)
		}
	}
	return models, nil
}

func (s *CodexModelRoutingService) GetSettings(ctx context.Context) (*store.ModelRoutingSettingsRecord, error) {
	return s.store.GetSettings(ctx)
}
func (s *CodexModelRoutingService) UpdateEnabled(ctx context.Context, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.store.GetSettings(ctx)
	if err != nil {
		return err
	}
	if current == nil {
		current = &store.ModelRoutingSettingsRecord{ID: 1}
	}
	current.CodexEnabled = enabled
	if err := s.store.UpdateSettings(ctx, current); err != nil {
		return err
	}
	return s.rebuildLocked(ctx)
}

func (s *CodexModelRoutingService) rebuildLocked(ctx context.Context) error {
	settings, err := s.store.GetSettings(ctx)
	if err != nil {
		return s.publishFailure(false, err)
	}
	if settings == nil {
		return s.publishFailure(false, fmt.Errorf("model routing settings row is missing"))
	}
	groups, err := s.store.ListGroups(ctx)
	if err != nil {
		return s.publishFailure(settings.CodexEnabled, err)
	}
	assignments, err := s.store.ListAssignments(ctx)
	if err != nil {
		return s.publishFailure(settings.CodexEnabled, err)
	}
	snap := &codexModelRoutingSnapshot{Enabled: settings.CodexEnabled, Groups: map[int64]*codexModelRoutingGroup{}, ByModel: map[string]*codexModelRoutingAssignment{}}
	for _, r := range groups {
		if r == nil || r.Family != modelrouting.FamilyCodex {
			continue
		}
		members, e := parseCodexMembers(r.MembersJSON)
		if e != nil {
			return s.publishFailure(settings.CodexEnabled, e)
		}
		g := &codexModelRoutingGroup{ID: r.ID, Name: r.Name, Members: members, RouteMode: r.RouteMode, RouteTarget: 0, Revision: r.RouteRevision}
		if r.RouteTarget != "" {
			if _, e = fmt.Sscan(r.RouteTarget, &g.RouteTarget); e != nil {
				return s.publishFailure(settings.CodexEnabled, fmt.Errorf("codex route target invalid: %w", e))
			}
		}
		snap.Groups[g.ID] = g
	}
	for _, r := range assignments {
		if r == nil || r.Family != modelrouting.FamilyCodex || !r.Enabled {
			continue
		}
		if _, ok := snap.Groups[r.GroupID]; !ok {
			return s.publishFailure(settings.CodexEnabled, fmt.Errorf("codex assignment %q references missing group", r.ModelName))
		}
		key := modelrouting.NormalizeModelName(r.ModelName)
		if _, exists := snap.ByModel[key]; exists {
			return s.publishFailure(settings.CodexEnabled, fmt.Errorf("duplicate codex model assignment %q", r.ModelName))
		}
		snap.ByModel[key] = &codexModelRoutingAssignment{ID: r.ID, Model: r.ModelName, GroupID: r.GroupID, Enabled: r.Enabled}
	}
	snap.Version = s.version.Add(1)
	s.snapshot.Store(snap)
	s.status.Store(ModelRoutingStatusReady)
	return nil
}

func (s *CodexModelRoutingService) publishFailure(enabled bool, err error) error {
	snap := &codexModelRoutingSnapshot{Version: s.version.Add(1), Enabled: enabled, LoadFailed: true, Groups: map[int64]*codexModelRoutingGroup{}, ByModel: map[string]*codexModelRoutingAssignment{}}
	s.snapshot.Store(snap)
	s.status.Store(ModelRoutingStatusLoadFailed)
	return err
}

func parseCodexMembers(raw string) ([]int64, error) {
	var values []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, fmt.Errorf("解析 Codex 账号组成员失败: %w", err)
	}
	out := make([]int64, 0, len(values))
	seen := map[int64]struct{}{}
	for _, value := range values {
		var id int64
		if err := json.Unmarshal(value, &id); err != nil || id <= 0 {
			var text string
			if json.Unmarshal(value, &text) != nil {
				return nil, fmt.Errorf("Codex 账号组成员必须是正整数账号 ID")
			}
			_, scanErr := fmt.Sscan(strings.TrimSpace(text), &id)
			if scanErr != nil || id <= 0 {
				return nil, fmt.Errorf("Codex 账号组成员必须是正整数账号 ID")
			}
		}
		if _, ok := seen[id]; ok {
			return nil, fmt.Errorf("Codex 账号 ID %d 重复", id)
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("Codex 账号组至少需要一个成员")
	}
	return out, nil
}
