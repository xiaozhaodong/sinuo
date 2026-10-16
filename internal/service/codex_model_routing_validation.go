package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"sinuo/internal/accountauth"
	"sinuo/internal/modelrouting"
	"sinuo/internal/store"
)

func codexBindingKind(model string) string {
	if modelrouting.IsSearchKey(model) {
		return modelrouting.BindingKindSearch
	}
	return modelrouting.BindingKindModel
}

// 调用方持有与账号删除共用的写锁；账号缓存首次加载可能查询同一个 SQLite 连接，
// 因此类型补齐与账号校验必须在事务开始前完成，避免持有事务时等待第二个连接。
func (s *CodexModelRoutingService) prepareCodexBindingWrite(ctx context.Context, id int64, in CodexModelRouteBindingInput) (CodexModelRouteBindingInput, *store.ModelRoutingAssignmentRecord, error) {
	a := &store.ModelRoutingAssignmentRecord{Family: modelrouting.FamilyCodex, MatchType: modelrouting.MatchExact}
	var err error
	if id != 0 {
		a, err = s.store.GetAssignment(ctx, id)
		if err != nil {
			return in, nil, err
		}
		if a == nil || a.Family != modelrouting.FamilyCodex {
			return in, nil, fmt.Errorf("Codex 绑定不存在")
		}
		if in.Kind == "" {
			in.Kind = codexBindingKind(a.ModelName)
		}
	}
	in, err = normalizeCodexBindingInput(in)
	if err != nil {
		return in, nil, err
	}
	if id != 0 && codexBindingKind(a.ModelName) != in.Kind {
		return in, nil, fmt.Errorf("映射类型创建后不能修改")
	}
	if err := s.validateAccountIDs(ctx, in.AccountIDs, in.Kind); err != nil {
		return in, nil, err
	}
	return in, a, nil
}

func normalizeCodexBindingKind(in CodexModelRouteBindingInput) (CodexModelRouteBindingInput, error) {
	switch in.Kind {
	case "", modelrouting.BindingKindModel:
		in.Kind = modelrouting.BindingKindModel
		in.Model = strings.TrimSpace(in.Model)
		if modelrouting.IsReservedKey(in.Model) {
			return CodexModelRouteBindingInput{}, fmt.Errorf("模型名不能以保留前缀 %q 开头，联网搜索映射请选择「联网搜索」类型", modelrouting.ReservedKeyPrefix)
		}
	case modelrouting.BindingKindSearch:
		in.Model = modelrouting.SearchKey
	default:
		return CodexModelRouteBindingInput{}, fmt.Errorf("不支持的映射类型 %q", in.Kind)
	}
	return in, nil
}

// 写入前复用引擎的重名校验，避免唯一索引先返回不友好的数据库错误。
// 账号组在同一事务内创建或读取，这里用占位组仅校验映射本身与同族重名。
func validateCodexBindingAssignment(in CodexModelRouteBindingInput, id int64, records []*store.ModelRoutingAssignmentRecord) error {
	siblings := make([]modelrouting.Assignment, 0, len(records))
	for _, record := range records {
		if record != nil && record.Family == modelrouting.FamilyCodex {
			siblings = append(siblings, modelrouting.Assignment{ID: record.ID, ModelName: record.ModelName})
		}
	}
	return modelrouting.ValidateAssignment(modelrouting.Assignment{ID: id, ModelName: in.Model, GroupID: 1, Note: in.Note}, modelrouting.MatchExact, siblings, []modelrouting.Group{{ID: 1}})
}

// normalizeCodexBindingInput 统一校验、账号存在性检查和存储使用的成员集合。
// 显式账号列表保留顺序；仅提供状态映射时按 ID 排序；未填写的参与状态默认启用。
func normalizeCodexBindingInput(in CodexModelRouteBindingInput) (CodexModelRouteBindingInput, error) {
	var err error
	in, err = normalizeCodexBindingKind(in)
	if err != nil {
		return CodexModelRouteBindingInput{}, err
	}
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
func (s *CodexModelRoutingService) validateAccountIDs(ctx context.Context, ids []int64, kind string) error {
	if s.listAccounts == nil {
		return fmt.Errorf("账号状态服务未初始化")
	}
	accounts, err := s.listAccounts(ctx, true)
	if err != nil {
		return err
	}
	known := map[int64]*store.UpstreamAccountRecord{}
	for _, a := range accounts {
		if a != nil {
			known[a.ID] = a
		}
	}
	for _, id := range ids {
		if known[id] == nil {
			return fmt.Errorf("Codex 账号 %d 不存在", id)
		}
		if kind == modelrouting.BindingKindSearch && !accountauth.IsChatGPTOAuthProvider(known[id].ProviderType) {
			return fmt.Errorf("联网搜索只能使用 ChatGPT OAuth 账号：账号 %d 不是 OAuth 账号", id)
		}
	}
	return nil
}
