package service

import (
	"fmt"
	"strings"

	"sinuo/internal/modelrouting"
	"sinuo/internal/store"
)

// validateGroupCandidate 校验组候选。自身排除由 ValidateGroup 按 ID 处理，
// 因此 siblings 可以直接传全量组列表。
func (s *ModelRoutingService) validateGroupCandidate(record store.ModelUpstreamGroupRecord, siblings []modelrouting.Group) error {
	group, err := groupFromRecord(&record)
	if err != nil {
		return err
	}
	knownEndpoints := map[string]struct{}(nil)
	if s.endpointCheck != nil {
		knownEndpoints = make(map[string]struct{}, len(group.Members))
		for _, member := range group.Members {
			if s.endpointCheck(member) {
				knownEndpoints[member] = struct{}{}
			}
		}
	}
	return modelrouting.ValidateGroup(*group, siblings, knownEndpoints)
}

// validateAssignmentCandidate 校验分配候选。自身排除同样由 ValidateAssignment 按 ID 处理。
func (s *ModelRoutingService) validateAssignmentCandidate(record store.ModelRoutingAssignmentRecord, siblings []modelrouting.Assignment, groups []modelrouting.Group) error {
	assignment, err := assignmentFromRecord(&record)
	if err != nil {
		return err
	}
	return modelrouting.ValidateAssignment(*assignment, record.MatchType, siblings, groups)
}

func normalizeGroupRecord(record *store.ModelUpstreamGroupRecord) {
	record.Family = modelrouting.FamilyClaude
	record.Name = strings.TrimSpace(record.Name)
	if record.RouteMode == "" {
		record.RouteMode = modelrouting.RouteModeAuto
	}
	record.RouteTarget = strings.TrimSpace(record.RouteTarget)
	if members, err := modelrouting.ParseMembers(record.MembersJSON); err == nil {
		for i := range members {
			members[i] = strings.TrimSpace(members[i])
		}
		if encoded, encodeErr := modelrouting.EncodeMembers(members); encodeErr == nil {
			record.MembersJSON = encoded
		}
	}
}

func normalizeAssignmentRecord(record *store.ModelRoutingAssignmentRecord) {
	record.Family = modelrouting.FamilyClaude
	if record.MatchType == "" {
		record.MatchType = modelrouting.MatchExact
	}
	record.ModelName = strings.TrimSpace(record.ModelName)
}

func groupFromRecord(record *store.ModelUpstreamGroupRecord) (*modelrouting.Group, error) {
	members, err := modelrouting.ParseMembers(record.MembersJSON)
	if err != nil {
		return nil, err
	}
	return &modelrouting.Group{
		ID: record.ID, Name: record.Name, Members: members, RouteMode: record.RouteMode,
		RouteTarget: record.RouteTarget, RouteRevision: record.RouteRevision,
		CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}, nil
}

func assignmentFromRecord(record *store.ModelRoutingAssignmentRecord) (*modelrouting.Assignment, error) {
	return &modelrouting.Assignment{
		ID: record.ID, ModelName: record.ModelName, GroupID: record.GroupID, Enabled: record.Enabled,
		Note: record.Note, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}, nil
}

func groupsFromRecords(records []*store.ModelUpstreamGroupRecord) ([]modelrouting.Group, error) {
	groups := make([]modelrouting.Group, 0, len(records))
	for _, record := range records {
		if record == nil {
			return nil, fmt.Errorf("model upstream group record is nil")
		}
		group, err := groupFromRecord(record)
		if err != nil {
			return nil, err
		}
		groups = append(groups, *group)
	}
	return groups, nil
}

func assignmentsFromRecords(records []*store.ModelRoutingAssignmentRecord) ([]modelrouting.Assignment, error) {
	assignments := make([]modelrouting.Assignment, 0, len(records))
	for _, record := range records {
		if record == nil {
			return nil, fmt.Errorf("model routing assignment record is nil")
		}
		if record.MatchType != "" && record.MatchType != modelrouting.MatchExact {
			// schema 的 CHECK 仍放行 prefix（预留扩展）。本期引擎只认 exact，
			// 跳过单条即可——让一条越界数据把整个快照打成 load_failed 不成比例。
			continue
		}
		assignment, err := assignmentFromRecord(record)
		if err != nil {
			return nil, err
		}
		assignments = append(assignments, *assignment)
	}
	return assignments, nil
}

func filterClaudeGroups(records []*store.ModelUpstreamGroupRecord) []*store.ModelUpstreamGroupRecord {
	filtered := make([]*store.ModelUpstreamGroupRecord, 0, len(records))
	for _, record := range records {
		if record == nil {
			continue
		}
		if record.Family == "" || record.Family == modelrouting.FamilyClaude {
			filtered = append(filtered, record)
		}
	}
	return filtered
}

func filterClaudeAssignments(records []*store.ModelRoutingAssignmentRecord) []*store.ModelRoutingAssignmentRecord {
	filtered := make([]*store.ModelRoutingAssignmentRecord, 0, len(records))
	for _, record := range records {
		if record == nil {
			continue
		}
		if record.Family == "" || record.Family == modelrouting.FamilyClaude {
			filtered = append(filtered, record)
		}
	}
	return filtered
}
