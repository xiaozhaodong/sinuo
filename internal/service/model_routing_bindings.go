package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"sinuo/internal/modelrouting"
	"sinuo/internal/store"
)

// ModelRouteBinding 是「一个模型 → 一组端点」的合一视图。
// 底层仍是 group + assignment 两行，但组是该模型的专属实现细节，不在 UI 露出。
type ModelRouteBinding struct {
	AssignmentID int64
	GroupID      int64
	// Kind 区分模型映射与联网工具映射；后者 Model 为保留键，前端不直接展示。
	Kind        string
	Model       string
	Endpoints   []string
	RouteMode   string
	RouteTarget string
	Enabled     bool
	Note        string
	// SharedModels 是与本条共用同一个组的其它模型。1 模型 1 组语义下恒为空，
	// 非空只可能来自更早版本手工编排的共享组，编辑本条时会自动分裂掉。
	SharedModels []string
	Runtime      ModelRoutingGroupRuntime
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// ModelRouteBindingInput 是一次映射写入的完整意图，组与分配的字段合成一份。
type ModelRouteBindingInput struct {
	// Kind 为空或 model 时按 Model 字段匹配；为 web_tools 时忽略 Model，写入联网工具保留键。
	Kind        string
	Model       string
	Endpoints   []string
	RouteMode   string
	RouteTarget string
	Enabled     bool
	Note        string
}

// bindingState 是一次写事务开始时的库内现状，供上限检查与重名校验共用。
type bindingState struct {
	groupRecords      []*store.ModelUpstreamGroupRecord
	assignmentRecords []*store.ModelRoutingAssignmentRecord
	groups            []modelrouting.Group
	assignments       []modelrouting.Assignment
}

func loadBindingState(ctx context.Context, st store.ModelRoutingStore) (*bindingState, error) {
	groupRecords, err := st.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	assignmentRecords, err := st.ListAssignments(ctx)
	if err != nil {
		return nil, err
	}
	state := &bindingState{
		groupRecords:      filterClaudeGroups(groupRecords),
		assignmentRecords: filterClaudeAssignments(assignmentRecords),
	}
	if state.groups, err = groupsFromRecords(state.groupRecords); err != nil {
		return nil, err
	}
	if state.assignments, err = assignmentsFromRecords(state.assignmentRecords); err != nil {
		return nil, err
	}
	return state, nil
}

func (b *bindingState) countAssignmentsForGroup(groupID int64) int {
	count := 0
	for _, record := range b.assignmentRecords {
		if record != nil && record.GroupID == groupID {
			count++
		}
	}
	return count
}

// ListBindings 返回按模型名排序的映射列表。
func (s *ModelRoutingService) ListBindings(ctx context.Context) ([]ModelRouteBinding, error) {
	if err := s.checkStore(); err != nil {
		return nil, err
	}
	groupRecords, err := s.store.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	assignmentRecords, err := s.store.ListAssignments(ctx)
	if err != nil {
		return nil, err
	}

	groups := make(map[int64]*store.ModelUpstreamGroupRecord)
	for _, record := range filterClaudeGroups(groupRecords) {
		groups[record.ID] = record
	}
	assignments := filterClaudeAssignments(assignmentRecords)
	modelsByGroup := make(map[int64][]string, len(groups))
	for _, record := range assignments {
		modelsByGroup[record.GroupID] = append(modelsByGroup[record.GroupID], record.ModelName)
	}

	bindings := make([]ModelRouteBinding, 0, len(assignments))
	for _, record := range assignments {
		group := groups[record.GroupID]
		if group == nil {
			// 引用完整性由写路径与快照编译保证；真出现说明数据已损坏，
			// 跳过单条即可——让一行脏数据把整个配置页打空不成比例。
			continue
		}
		binding, buildErr := buildBinding(record, group, modelsByGroup[group.ID])
		if buildErr != nil {
			return nil, buildErr
		}
		binding.Runtime = s.GroupRuntime(group.ID)
		bindings = append(bindings, binding)
	}
	sort.Slice(bindings, func(i, j int) bool {
		return modelrouting.NormalizeModelName(bindings[i].Model) < modelrouting.NormalizeModelName(bindings[j].Model)
	})
	return bindings, nil
}

func buildBinding(record *store.ModelRoutingAssignmentRecord, group *store.ModelUpstreamGroupRecord, groupModels []string) (ModelRouteBinding, error) {
	members, err := modelrouting.ParseMembers(group.MembersJSON)
	if err != nil {
		return ModelRouteBinding{}, err
	}
	shared := make([]string, 0)
	for _, model := range groupModels {
		if !strings.EqualFold(strings.TrimSpace(model), strings.TrimSpace(record.ModelName)) {
			shared = append(shared, model)
		}
	}
	return ModelRouteBinding{
		AssignmentID: record.ID, GroupID: group.ID, Model: record.ModelName,
		Kind:      modelrouting.BindingKind(record.ModelName),
		Endpoints: members, RouteMode: group.RouteMode, RouteTarget: group.RouteTarget,
		Enabled: record.Enabled, Note: record.Note, SharedModels: shared,
		CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}, nil
}

// bindingDisplayName 给端点删除守卫等提示用：联网工具映射不暴露保留键。
func bindingDisplayName(binding ModelRouteBinding) string {
	if binding.Kind == modelrouting.BindingKindWebTools {
		return "联网工具"
	}
	return binding.Model
}

// normalizeBindingInput 把映射种类折算成分配键：联网工具映射统一写保留键，
// 模型映射保持用户输入；未知种类直接拒绝，避免前端拼错字段静默落成模型映射。
func normalizeBindingInput(input ModelRouteBindingInput) (ModelRouteBindingInput, error) {
	switch strings.TrimSpace(input.Kind) {
	case "", modelrouting.BindingKindModel:
		input.Kind = modelrouting.BindingKindModel
		input.Model = strings.TrimSpace(input.Model)
		// 下层校验放行 server_tool:web 本身以便联网映射入库，所以普通模型入口必须
		// 自己拒掉整个保留前缀，否则 kind=model 能绕过前端造出联网映射。
		if modelrouting.IsReservedKey(input.Model) {
			return input, fmt.Errorf("模型名不能以保留前缀 %q 开头，联网工具映射请选择「联网工具」类型", modelrouting.ReservedKeyPrefix)
		}
	case modelrouting.BindingKindWebTools:
		input.Kind = modelrouting.BindingKindWebTools
		input.Model = modelrouting.WebToolsKey
	default:
		return input, fmt.Errorf("不支持的映射类型 %q", input.Kind)
	}
	return input, nil
}

// CreateBinding 在一个事务内建立专属组与分配，返回新分配 ID。
func (s *ModelRoutingService) CreateBinding(ctx context.Context, input ModelRouteBindingInput) (int64, error) {
	if err := s.checkStore(); err != nil {
		return 0, err
	}
	input, err := normalizeBindingInput(input)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	assignmentID := int64(0)
	err = s.store.RunInTx(ctx, func(st store.ModelRoutingStore) error {
		state, stateErr := loadBindingState(ctx, st)
		if stateErr != nil {
			return stateErr
		}
		if err := state.checkCapacityForNewBinding(); err != nil {
			return err
		}
		group, groupErr := s.persistDedicatedGroup(ctx, st, input, state, nil)
		if groupErr != nil {
			return groupErr
		}
		saved, saveErr := s.persistAssignment(ctx, st, input, state, group, nil)
		if saveErr != nil {
			return saveErr
		}
		assignmentID = saved.ID
		return nil
	})
	if err != nil {
		return 0, err
	}
	return assignmentID, s.rebuildSnapshotLocked(ctx)
}

// UpdateBinding 覆盖写一条映射的模型名、端点集合、组内策略与启用状态。
func (s *ModelRoutingService) UpdateBinding(ctx context.Context, assignmentID int64, input ModelRouteBindingInput) error {
	if assignmentID <= 0 {
		return fmt.Errorf("invalid model routing assignment")
	}
	if err := s.checkStore(); err != nil {
		return err
	}
	input, err := normalizeBindingInput(input)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	err = s.store.RunInTx(ctx, func(st store.ModelRoutingStore) error {
		existing, loadErr := loadClaudeAssignment(ctx, st, assignmentID)
		if loadErr != nil {
			return loadErr
		}
		state, stateErr := loadBindingState(ctx, st)
		if stateErr != nil {
			return stateErr
		}
		group, groupErr := s.resolveGroupForUpdate(ctx, st, input, state, existing)
		if groupErr != nil {
			return groupErr
		}
		_, saveErr := s.persistAssignment(ctx, st, input, state, group, existing)
		return saveErr
	})
	if err != nil {
		return err
	}
	return s.rebuildSnapshotLocked(ctx)
}

// DeleteBinding 删除分配，并回收只服务于它的专属组。
func (s *ModelRoutingService) DeleteBinding(ctx context.Context, assignmentID int64) error {
	if assignmentID <= 0 {
		return fmt.Errorf("invalid model routing assignment")
	}
	if err := s.checkStore(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	err := s.store.RunInTx(ctx, func(st store.ModelRoutingStore) error {
		existing, loadErr := loadClaudeAssignment(ctx, st, assignmentID)
		if loadErr != nil {
			return loadErr
		}
		if delErr := st.DeleteAssignment(ctx, assignmentID); delErr != nil {
			return delErr
		}
		state, stateErr := loadBindingState(ctx, st)
		if stateErr != nil {
			return stateErr
		}
		// 组是本模型的专属实现细节：没有其它分配引用时一并回收，避免孤儿组白占配额。
		// 历史共享组仍被别的模型引用时保留，删掉会连带打死那些模型。
		if state.countAssignmentsForGroup(existing.GroupID) > 0 {
			return nil
		}
		return st.DeleteGroup(ctx, existing.GroupID)
	})
	if err != nil {
		return err
	}
	return s.rebuildSnapshotLocked(ctx)
}

func (b *bindingState) checkCapacityForNewBinding() error {
	if len(b.groupRecords) >= modelrouting.MaxGroups {
		return fmt.Errorf("模型映射不能超过 %d 条", modelrouting.MaxGroups)
	}
	if len(b.assignmentRecords) >= modelrouting.MaxAssignments {
		return fmt.Errorf("模型映射不能超过 %d 条", modelrouting.MaxAssignments)
	}
	return nil
}

func loadClaudeAssignment(ctx context.Context, st store.ModelRoutingStore, id int64) (*store.ModelRoutingAssignmentRecord, error) {
	existing, err := st.GetAssignment(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, fmt.Errorf("model routing assignment %d not found", id)
	}
	if existing.Family != "" && existing.Family != modelrouting.FamilyClaude {
		return nil, fmt.Errorf("model routing assignment %d 不属于 Claude 路由", id)
	}
	return existing, nil
}

// resolveGroupForUpdate 决定这次更新写到哪个组：独占组就地改，历史共享组分裂出新的。
func (s *ModelRoutingService) resolveGroupForUpdate(ctx context.Context, st store.ModelRoutingStore,
	input ModelRouteBindingInput, state *bindingState, existing *store.ModelRoutingAssignmentRecord) (*store.ModelUpstreamGroupRecord, error) {
	if state.countAssignmentsForGroup(existing.GroupID) > 1 {
		// 历史共享组：改成员或策略会波及别的模型，为本条分裂出专属组。
		if len(state.groupRecords) >= modelrouting.MaxGroups {
			return nil, fmt.Errorf("上游组已达 %d 个上限，无法为该模型拆出独立端点集合", modelrouting.MaxGroups)
		}
		return s.persistDedicatedGroup(ctx, st, input, state, nil)
	}
	current, err := st.GetGroup(ctx, existing.GroupID)
	if err != nil {
		return nil, err
	}
	if current == nil {
		// 组被外部手工删掉时补建一个，比让这条映射永久卡死更符合预期。
		return s.persistDedicatedGroup(ctx, st, input, state, nil)
	}
	if current.Family != "" && current.Family != modelrouting.FamilyClaude {
		return nil, fmt.Errorf("model upstream group %d 不属于 Claude 路由", current.ID)
	}
	return s.persistDedicatedGroup(ctx, st, input, state, current)
}

// persistDedicatedGroup 写入模型的专属组。current 为 nil 表示新建，否则就地覆盖并 bump revision。
func (s *ModelRoutingService) persistDedicatedGroup(ctx context.Context, st store.ModelRoutingStore,
	input ModelRouteBindingInput, state *bindingState, current *store.ModelUpstreamGroupRecord) (*store.ModelUpstreamGroupRecord, error) {
	membersJSON, err := modelrouting.EncodeMembers(input.Endpoints)
	if err != nil {
		return nil, err
	}
	candidate := store.ModelUpstreamGroupRecord{
		MembersJSON: membersJSON,
		RouteMode:   input.RouteMode,
		RouteTarget: input.RouteTarget,
	}
	if current != nil {
		candidate = *current
		candidate.MembersJSON = membersJSON
		candidate.RouteMode = input.RouteMode
		candidate.RouteTarget = input.RouteTarget
	}
	candidate.Name = dedicatedGroupName(input.Model, state.groupRecords, candidate.ID)
	normalizeGroupRecord(&candidate)
	if err := s.validateGroupCandidate(candidate, state.groups); err != nil {
		return nil, err
	}
	if current == nil {
		return st.CreateGroup(ctx, &candidate)
	}
	// 成员或组内策略一变就 bump revision，旧 retained 的 scope key 随之失效。
	candidate.RouteRevision = current.RouteRevision + 1
	if err := st.UpdateGroup(ctx, &candidate); err != nil {
		return nil, err
	}
	return &candidate, nil
}

// persistAssignment 写入分配行。existing 为 nil 表示新建。
func (s *ModelRoutingService) persistAssignment(ctx context.Context, st store.ModelRoutingStore,
	input ModelRouteBindingInput, state *bindingState, group *store.ModelUpstreamGroupRecord,
	existing *store.ModelRoutingAssignmentRecord) (*store.ModelRoutingAssignmentRecord, error) {
	candidate := store.ModelRoutingAssignmentRecord{}
	if existing != nil {
		candidate = *existing
	}
	candidate.ModelName = input.Model
	candidate.GroupID = group.ID
	candidate.Enabled = input.Enabled
	candidate.Note = input.Note
	candidate.MatchType = modelrouting.MatchExact
	normalizeAssignmentRecord(&candidate)

	// group 可能是本事务内刚建的，state 里还没有；补进候选组列表才能通过引用校验。
	groups := state.groups
	if parsed, err := groupFromRecord(group); err == nil {
		groups = append(append([]modelrouting.Group(nil), groups...), *parsed)
	}
	if err := s.validateAssignmentCandidate(candidate, state.assignments, groups); err != nil {
		return nil, err
	}
	if existing == nil {
		return st.CreateAssignment(ctx, &candidate)
	}
	if err := st.UpdateAssignment(ctx, &candidate); err != nil {
		return nil, err
	}
	return &candidate, nil
}

// dedicatedGroupName 为模型生成专属组名。组名不在 UI 露出，只需与其它组不冲突
// （唯一索引是 lower(name)）；撞名时追加 #n，组数上限保证循环必然终止。
func dedicatedGroupName(model string, records []*store.ModelUpstreamGroupRecord, selfGroupID int64) string {
	base := strings.TrimSpace(model)
	if base == "" {
		base = "model"
	}
	// 给 " #99" 之类的后缀预留位置，避免拼完超过组名长度上限被校验打回。
	if runes := []rune(base); len(runes) > modelrouting.MaxGroupNameLen-8 {
		base = strings.TrimSpace(string(runes[:modelrouting.MaxGroupNameLen-8]))
	}
	taken := make(map[string]struct{}, len(records))
	for _, record := range records {
		if record == nil || (selfGroupID != 0 && record.ID == selfGroupID) {
			continue
		}
		taken[strings.ToLower(strings.TrimSpace(record.Name))] = struct{}{}
	}
	candidate := base
	for suffix := 2; ; suffix++ {
		if _, exists := taken[strings.ToLower(candidate)]; !exists {
			return candidate
		}
		candidate = fmt.Sprintf("%s #%d", base, suffix)
	}
}
