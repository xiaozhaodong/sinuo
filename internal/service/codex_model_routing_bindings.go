package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"cc-forwarder/internal/modelrouting"
	"cc-forwarder/internal/store"
)

// parseCodexMembers 严格解析正整数 ID，不接受尾随文本、浮点数或超过上限的成员。
func parseCodexMembers(raw string) ([]int64, error) {
	states, err := parseCodexMemberStates(raw)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(states))
	for _, member := range states {
		ids = append(ids, member.ID)
	}
	return ids, nil
}

type codexMemberState struct {
	ID      int64 `json:"id"`
	Enabled bool  `json:"enabled"`
}

func parseCodexMemberStates(raw string) ([]codexMemberState, error) {
	var values []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, fmt.Errorf("解析 Codex 账号组成员失败: %w", err)
	}
	if len(values) == 0 || len(values) > modelrouting.MaxGroupMembers {
		return nil, fmt.Errorf("账号组成员数量必须为 1–%d", modelrouting.MaxGroupMembers)
	}
	members := make([]codexMemberState, 0, len(values))
	seen := map[int64]bool{}
	for _, v := range values {
		text := string(v)
		enabled := true
		if len(v) > 0 && v[0] == '{' {
			var obj struct {
				ID      json.RawMessage `json:"id"`
				Enabled *bool           `json:"enabled"`
			}
			if err := json.Unmarshal(v, &obj); err != nil || len(obj.ID) == 0 {
				return nil, fmt.Errorf("账号组成员格式无效")
			}
			text = string(obj.ID)
			if obj.Enabled != nil {
				enabled = *obj.Enabled
			}
		}
		if len(v) > 0 && v[0] == '"' {
			if err := json.Unmarshal(v, &text); err != nil {
				return nil, err
			}
		}
		id, err := strconv.ParseInt(text, 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != text {
			return nil, fmt.Errorf("账号 ID 必须是规范十进制正整数")
		}
		if seen[id] {
			return nil, fmt.Errorf("账号 ID %d 重复", id)
		}
		seen[id] = true
		members = append(members, codexMemberState{ID: id, Enabled: enabled})
	}
	return members, nil
}
func formatAccountTarget(id int64) string {
	if id == 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}
func codexGroupFromRecord(g *store.ModelUpstreamGroupRecord) (modelrouting.Group, error) {
	ids, err := parseCodexMembers(g.MembersJSON)
	if err != nil {
		return modelrouting.Group{}, err
	}
	members := make([]string, 0, len(ids))
	for _, id := range ids {
		members = append(members, strconv.FormatInt(id, 10))
	}
	return modelrouting.Group{ID: g.ID, Name: g.Name, Members: members, RouteMode: g.RouteMode, RouteTarget: g.RouteTarget, RouteRevision: g.RouteRevision}, nil
}

// 共用 modelrouting 的校验和编译规则，Codex 只适配账号 ID 的存储表示。
func loadCodexSnapshot(ctx context.Context, st store.ModelRoutingStore) (*codexModelRoutingSnapshot, error) {
	settings, err := st.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	return loadCodexSnapshotWithSettings(ctx, st, settings)
}

// loadCodexSnapshotWithSettings 使用调用方已经读取的设置，避免重建快照时重复读取开关。
func loadCodexSnapshotWithSettings(ctx context.Context, st store.ModelRoutingStore, settings *store.ModelRoutingSettingsRecord) (*codexModelRoutingSnapshot, error) {
	if settings == nil {
		return nil, fmt.Errorf("模型路由设置行不存在")
	}
	records, err := st.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	groups := []modelrouting.Group{}
	memberStates := map[int64]map[int64]bool{}
	for _, r := range records {
		if r != nil && r.Family == modelrouting.FamilyCodex {
			g, e := codexGroupFromRecord(r)
			if e != nil {
				return nil, e
			}
			groups = append(groups, g)
			states, e := parseCodexMemberStates(r.MembersJSON)
			if e != nil {
				return nil, e
			}
			memberStates[r.ID] = map[int64]bool{}
			for _, member := range states {
				memberStates[r.ID][member.ID] = member.Enabled
			}
		}
	}
	recordsA, err := st.ListAssignments(ctx)
	if err != nil {
		return nil, err
	}
	assignments := []modelrouting.Assignment{}
	for _, r := range recordsA {
		if r == nil || r.Family != modelrouting.FamilyCodex {
			continue
		}
		a := modelrouting.Assignment{ID: r.ID, ModelName: r.ModelName, GroupID: r.GroupID, Enabled: r.Enabled, Note: r.Note}
		if err := modelrouting.ValidateAssignment(a, r.MatchType, assignments, groups); err != nil {
			return nil, err
		}
		assignments = append(assignments, a)
	}
	if len(groups) > modelrouting.MaxGroups || len(assignments) > modelrouting.MaxAssignments {
		return nil, fmt.Errorf("Codex 模型路由配置数量超过上限")
	}
	compiled, err := modelrouting.Compile(modelrouting.Settings{Enabled: settings.CodexEnabled, UpdatedAt: settings.UpdatedAt}, groups, assignments)
	if err != nil {
		return nil, err
	}
	snap := &codexModelRoutingSnapshot{Enabled: settings.CodexEnabled, Groups: map[int64]*codexModelRoutingGroup{}, ByModel: map[string]*codexModelRoutingAssignment{}}
	for _, g := range compiled.Groups() {
		cg := &codexModelRoutingGroup{ID: g.ID, Name: g.Name, RouteMode: g.RouteMode, Revision: g.RouteRevision, MemberEnabled: memberStates[g.ID]}
		for _, m := range g.Members {
			id, _ := strconv.ParseInt(m, 10, 64)
			cg.Members = append(cg.Members, id)
		}
		if g.RouteTarget != "" {
			cg.RouteTarget, _ = strconv.ParseInt(g.RouteTarget, 10, 64)
		}
		snap.Groups[g.ID] = cg
	}
	for _, a := range compiled.Assignments() {
		snap.ByModel[modelrouting.NormalizeModelName(a.ModelName)] = &codexModelRoutingAssignment{ID: a.ID, Model: a.ModelName, GroupID: a.GroupID, Enabled: a.Enabled}
	}
	return snap, nil
}

// normalizeCodexBindingInput 统一校验、账号存在性检查和存储使用的成员集合。
// 显式账号列表保留顺序；仅提供状态映射时按 ID 排序；未填写的参与状态默认启用。
func normalizeCodexBindingInput(in CodexModelRouteBindingInput) (CodexModelRouteBindingInput, error) {
	ids := append([]int64(nil), in.AccountIDs...)
	if len(ids) == 0 && len(in.MemberEnabled) > 0 {
		ids = make([]int64, 0, len(in.MemberEnabled))
		for id := range in.MemberEnabled {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return CodexModelRouteBindingInput{}, err
	}
	g, err := codexGroupFromRecord(&store.ModelUpstreamGroupRecord{ID: 1, Name: "binding", MembersJSON: string(raw), RouteMode: in.RouteMode, RouteTarget: formatAccountTarget(in.RouteTargetID)})
	if err != nil {
		return CodexModelRouteBindingInput{}, err
	}
	if err = modelrouting.ValidateGroup(g, nil, nil); err != nil {
		return CodexModelRouteBindingInput{}, err
	}
	if err = modelrouting.ValidateAssignment(modelrouting.Assignment{ModelName: in.Model, GroupID: 1, Note: in.Note}, modelrouting.MatchExact, nil, []modelrouting.Group{g}); err != nil {
		return CodexModelRouteBindingInput{}, err
	}
	members := make(map[int64]bool, len(ids))
	for _, id := range ids {
		members[id] = true
	}
	for id := range in.MemberEnabled {
		if !members[id] {
			return CodexModelRouteBindingInput{}, fmt.Errorf("账号 %d 的参与状态不属于本模型组成员", id)
		}
	}
	in.AccountIDs = ids
	in.MemberEnabled = cloneMemberEnabled(in.MemberEnabled, ids)
	return in, nil
}
func (s *CodexModelRoutingService) validateAccountIDs(ctx context.Context, ids []int64) error {
	if s.listAccounts == nil {
		return fmt.Errorf("账号状态服务未初始化")
	}
	accounts, err := s.listAccounts(ctx, true)
	if err != nil {
		return err
	}
	known := map[int64]bool{}
	for _, a := range accounts {
		if a != nil {
			known[a.ID] = true
		}
	}
	for _, id := range ids {
		if !known[id] {
			return fmt.Errorf("Codex 账号 %d 不存在", id)
		}
	}
	return nil
}
func (s *CodexModelRoutingService) ListBindings(ctx context.Context) ([]CodexModelRouteBinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	groups, err := s.store.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	byID := map[int64]*store.ModelUpstreamGroupRecord{}
	for _, g := range groups {
		if g != nil && g.Family == modelrouting.FamilyCodex {
			byID[g.ID] = g
		}
	}
	assignments, err := s.store.ListAssignments(ctx)
	if err != nil {
		return nil, err
	}
	out := []CodexModelRouteBinding{}
	for _, a := range assignments {
		if a == nil || a.Family != modelrouting.FamilyCodex {
			continue
		}
		g := byID[a.GroupID]
		if g == nil {
			return nil, fmt.Errorf("模型 %q 的账号组不存在", a.ModelName)
		}
		members, e := parseCodexMembers(g.MembersJSON)
		if e != nil {
			return nil, e
		}
		target := int64(0)
		if g.RouteTarget != "" {
			target, e = strconv.ParseInt(g.RouteTarget, 10, 64)
			if e != nil {
				return nil, e
			}
		}
		rt := s.Runtime(g.ID)
		states, e := parseCodexMemberStates(g.MembersJSON)
		if e != nil {
			return nil, e
		}
		memberEnabled := map[int64]bool{}
		for _, member := range states {
			memberEnabled[member.ID] = member.Enabled
		}
		out = append(out, CodexModelRouteBinding{AssignmentID: a.ID, GroupID: g.ID, Model: a.ModelName, AccountIDs: members, MemberEnabled: memberEnabled, RouteMode: g.RouteMode, RouteTargetID: target, Enabled: a.Enabled, Note: a.Note, LastAccountID: rt.LastAccountID, LastOutcome: rt.LastOutcome, LastDecisionAt: rt.LastDecisionAt, UpdatedAt: a.UpdatedAt})
	}
	sort.Slice(out, func(i, j int) bool {
		return modelrouting.NormalizeModelName(out[i].Model) < modelrouting.NormalizeModelName(out[j].Model)
	})
	return out, nil
}
func (s *CodexModelRoutingService) CreateBinding(ctx context.Context, in CodexModelRouteBindingInput) (int64, error) {
	return s.saveBinding(ctx, 0, in)
}
func (s *CodexModelRoutingService) UpdateBinding(ctx context.Context, id int64, in CodexModelRouteBindingInput) error {
	if id <= 0 {
		return fmt.Errorf("无效绑定 ID")
	}
	_, err := s.saveBinding(ctx, id, in)
	return err
}
func (s *CodexModelRoutingService) saveBinding(ctx context.Context, id int64, in CodexModelRouteBindingInput) (int64, error) {
	var err error
	in, err = normalizeCodexBindingInput(in)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// 必须在绑定写入/账号删除共享锁内校验存在性。
	if err := s.validateAccountIDs(ctx, in.AccountIDs); err != nil {
		return 0, err
	}
	var snap *codexModelRoutingSnapshot
	err = s.store.RunInTx(ctx, func(st store.ModelRoutingStore) error {
		a := &store.ModelRoutingAssignmentRecord{Family: modelrouting.FamilyCodex, MatchType: modelrouting.MatchExact}
		var g *store.ModelUpstreamGroupRecord
		var err error
		if id != 0 {
			a, err = st.GetAssignment(ctx, id)
			if err != nil {
				return err
			}
			if a == nil || a.Family != modelrouting.FamilyCodex {
				return fmt.Errorf("Codex 绑定不存在")
			}
			g, err = st.GetGroup(ctx, a.GroupID)
			if err != nil {
				return err
			}
			if g == nil || g.Family != modelrouting.FamilyCodex {
				return fmt.Errorf("Codex 账号组不存在")
			}
		}
		groups, err := st.ListGroups(ctx)
		if err != nil {
			return err
		}
		assignments, err := st.ListAssignments(ctx)
		if err != nil {
			return err
		}
		shared := 0
		for _, other := range assignments {
			if g != nil && other.GroupID == g.ID {
				shared++
			}
		}
		if shared > 1 {
			g = nil
		} // 编辑共享组时为该模型创建专属组，不改变其他模型。
		if g == nil {
			g = &store.ModelUpstreamGroupRecord{Family: modelrouting.FamilyCodex}
		}
		g.Name = dedicatedGroupName(in.Model, groups, g.ID)
		memberObjects := make([]codexMemberState, 0, len(in.AccountIDs))
		for _, memberID := range in.AccountIDs {
			memberObjects = append(memberObjects, codexMemberState{ID: memberID, Enabled: in.MemberEnabled[memberID]})
		}
		members, err := json.Marshal(memberObjects)
		if err != nil {
			return err
		}
		g.MembersJSON = string(members)
		g.RouteMode = in.RouteMode
		g.RouteTarget = formatAccountTarget(in.RouteTargetID)
		g.RouteRevision++
		if g.ID == 0 {
			g, err = st.CreateGroup(ctx, g)
		} else {
			err = st.UpdateGroup(ctx, g)
		}
		if err != nil {
			return err
		}
		a.ModelName = strings.TrimSpace(in.Model)
		a.GroupID = g.ID
		a.Enabled = in.Enabled
		a.Note = in.Note
		if id == 0 {
			a, err = st.CreateAssignment(ctx, a)
		} else {
			err = st.UpdateAssignment(ctx, a)
		}
		if err != nil {
			return err
		}
		id = a.ID
		snap, err = loadCodexSnapshot(ctx, st) // 提交前编译；失败回滚两张表。
		return err
	})
	if err != nil {
		return 0, err
	}
	s.publishSnapshot(snap, ModelRoutingStatusReady)
	return id, nil
}
func (s *CodexModelRoutingService) DeleteBinding(ctx context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var snap *codexModelRoutingSnapshot
	err := s.store.RunInTx(ctx, func(st store.ModelRoutingStore) error {
		a, err := st.GetAssignment(ctx, id)
		if err != nil {
			return err
		}
		if a == nil || a.Family != modelrouting.FamilyCodex {
			return fmt.Errorf("Codex 绑定不存在")
		}
		g, err := st.GetGroup(ctx, a.GroupID)
		if err != nil {
			return err
		}
		if g != nil && g.Family != modelrouting.FamilyCodex {
			return fmt.Errorf("账号组协议族不一致")
		}
		if err = st.DeleteAssignment(ctx, id); err != nil {
			return err
		}
		rest, err := st.ListAssignments(ctx)
		if err != nil {
			return err
		}
		referenced := false
		for _, other := range rest {
			if other.GroupID == a.GroupID {
				referenced = true
			}
		}
		if g != nil && !referenced {
			if err = st.DeleteGroup(ctx, g.ID); err != nil {
				return err
			}
		}
		snap, err = loadCodexSnapshot(ctx, st)
		return err
	})
	if err != nil {
		return err
	}
	s.publishSnapshot(snap, ModelRoutingStatusReady)
	return nil
}
