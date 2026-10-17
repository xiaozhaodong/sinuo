package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"sinuo/internal/modelrouting"
	"sinuo/internal/store"
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
	BindingKind      string
	Kind             string
	GroupID          int64
	GroupName        string
	Members          []int64
	MemberEnabled    map[int64]bool
	Mode             string
	TargetAccountID  int64
	Revision         int64
	SnapshotVersion  int64
	Model            string
	UnassignedReason string
}

type CodexModelRouteResult struct {
	CacheGenerations map[int64]uint64
	Accounts         []*store.UpstreamAccountRecord
	Scope            CodexModelRouteScope
	Candidates       []AccountScheduleCandidateDecision
	// EarliestCooldownUntil 仅供内部本地拒绝提示使用，不进入调度快照 DTO 或 Wails 绑定。
	EarliestCooldownUntil time.Time
}

type CodexModelRouteBinding struct {
	Kind           string
	AssignmentID   int64
	GroupID        int64
	Model          string
	AccountIDs     []int64
	RouteMode      string
	RouteTargetID  int64
	MemberEnabled  map[int64]bool
	Enabled        bool
	Note           string
	LastAccountID  int64
	LastOutcome    string
	LastDecisionAt time.Time
	UpdatedAt      time.Time
}

type CodexModelRouteBindingInput struct {
	Kind          string
	Model         string
	AccountIDs    []int64
	MemberEnabled map[int64]bool
	RouteMode     string
	RouteTargetID int64
	Enabled       bool
	Note          string
}

type codexModelRoutingSnapshot struct {
	Search     *codexModelRoutingAssignment
	Version    int64
	Enabled    bool
	LoadFailed bool
	Groups     map[int64]*codexModelRoutingGroup
	ByModel    map[string]*codexModelRoutingAssignment
}

type codexModelRoutingGroup struct {
	ID            int64
	Name          string
	Members       []int64
	RouteMode     string
	RouteTarget   int64
	Revision      int64
	MemberEnabled map[int64]bool
}

type codexModelRoutingAssignment struct {
	ID      int64
	Model   string
	GroupID int64
	Enabled bool
}

// CodexModelRoutingService 提供简单的模型到账号组绑定，不参与现有账号池排序。
type CodexModelRoutingService struct {
	store           store.ModelRoutingStore
	listAccounts    AccountLister
	mu              *sync.Mutex
	snapshot        atomic.Pointer[codexModelRoutingSnapshot]
	version         atomic.Int64
	status          atomic.Value
	runtimeMu       sync.RWMutex
	runtime         map[int64]codexModelRouteRuntime
	negativeMu      sync.Mutex
	negativeHits    map[codexNegativeKey]time.Time
	cacheGeneration map[int64]uint64
}

type codexModelRouteRuntime struct {
	LastAccountID  int64
	LastOutcome    string
	LastDecisionAt time.Time
}

func NewCodexModelRoutingService(st store.ModelRoutingStore, lister AccountLister) *CodexModelRoutingService {
	s := &CodexModelRoutingService{store: st, listAccounts: lister, mu: &sync.Mutex{}, runtime: make(map[int64]codexModelRouteRuntime), negativeHits: make(map[codexNegativeKey]time.Time), cacheGeneration: make(map[int64]uint64)}
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
	if s == nil {
		return 0
	}
	snap := s.snapshot.Load()
	if snap == nil {
		return 0
	}
	return snap.Version
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
	snap := s.snapshot.Load()
	return snap != nil && snap.Enabled
}

func (s *CodexModelRoutingService) ResolveScope(model string) (CodexModelRouteScope, bool) {
	snap := s.snapshot.Load()
	model = strings.TrimSpace(model)
	scope := CodexModelRouteScope{Kind: "unassigned", Model: model, SnapshotVersion: snapVersion(snap), UnassignedReason: CodexModelRoutingUnassigned}
	if snap == nil || !snap.Enabled {
		scope.Kind = "global"
		scope.UnassignedReason = ""
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
	return codexScopeForGroup(snap, g, model, modelrouting.BindingKindModel), true
}

func snapVersion(s *codexModelRoutingSnapshot) int64 {
	if s == nil {
		return 0
	}
	return s.Version
}

func (s *CodexModelRoutingService) Prepare(ctx context.Context, model, path string) (CodexModelRouteResult, error) {
	scope, applies := s.ResolveScope(model)
	if !applies {
		return CodexModelRouteResult{Scope: scope}, nil
	}
	if scope.Kind != "upstream_group" {
		return CodexModelRouteResult{Scope: scope}, nil
	}
	return s.prepareCodexScope(ctx, scope, path)
}

func cloneMemberEnabled(src map[int64]bool, members []int64) map[int64]bool {
	out := make(map[int64]bool, len(members))
	for _, id := range members {
		if enabled, ok := src[id]; ok {
			out[id] = enabled
		} else {
			out[id] = true
		}
	}
	return out
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
	snap := s.snapshot.Load()
	if snap == nil || (!snap.Enabled && scope.BindingKind != modelrouting.BindingKindSearch) || snap.Version != scope.SnapshotVersion {
		return false
	}
	g := snap.Groups[scope.GroupID]
	return g != nil && g.Revision == scope.Revision
}

func (s *CodexModelRoutingService) Settle(scope CodexModelRouteScope, accountID int64, outcome string) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	snap := s.snapshot.Load()
	if snap == nil || (!snap.Enabled && scope.BindingKind != modelrouting.BindingKindSearch) || snap.Version != scope.SnapshotVersion {
		return
	}
	g := snap.Groups[scope.GroupID]
	if g == nil || g.Revision != scope.Revision {
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
		if assignment != nil && assignment.Family == modelrouting.FamilyCodex && groupHasAccount[assignment.GroupID] {
			name := assignment.ModelName
			if modelrouting.IsSearchKey(name) {
				name = "联网搜索"
			}
			models = append(models, name)
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
	if err := s.store.UpdateSettings(ctx, store.ModelRoutingSettingsPatch{CodexEnabled: &enabled}); err != nil {
		return err
	}
	return s.rebuildLocked(ctx)
}

func (s *CodexModelRoutingService) rebuildLocked(ctx context.Context) error {
	// 读不到开关时使用全局账号池，不把一次读取失败写成持久化关闭。
	desired := false
	settings, err := s.store.GetSettings(ctx)
	if err == nil && settings != nil {
		desired = settings.CodexEnabled
	} else if err == nil {
		err = fmt.Errorf("模型路由设置行不存在")
	}
	var snap *codexModelRoutingSnapshot
	if err == nil {
		snap, err = loadCodexSnapshotWithSettings(ctx, s.store, settings)
	}
	if err != nil {
		s.publishSnapshot(&codexModelRoutingSnapshot{Enabled: desired, LoadFailed: true, Groups: map[int64]*codexModelRoutingGroup{}, ByModel: map[string]*codexModelRoutingAssignment{}}, ModelRoutingStatusLoadFailed)
		return err
	}
	s.publishSnapshot(snap, ModelRoutingStatusReady)
	return nil
}

// 发布与 Settle 的校验及写入共用锁，杜绝校验通过后发布新配置的交错。
func (s *CodexModelRoutingService) publishSnapshot(snapshot *codexModelRoutingSnapshot, status string) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	snapshot.Version = s.version.Add(1)
	s.snapshot.Store(snapshot)
	s.status.Store(status)
	clear(s.runtime)
}
