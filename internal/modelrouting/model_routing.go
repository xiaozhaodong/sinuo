// Package modelrouting 提供「按模型选择上游组」的纯引擎：编译、归组解析与校验。
// 不依赖数据库与 HTTP，热路径只做一次 map 查找。
package modelrouting

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

// MatchExact 本期唯一放行的匹配方式。match_type 列保留是为了后续扩展不重建表。
const MatchExact = "exact"

// WebToolsKey 是「联网工具」映射在 model_routing_assignments.model_name 里的保留键。
// 请求 tools[] 里出现 web_search / web_fetch 类 server tool 时优先命中它，再回落到模型映射。
// 用保留键而不是新列，是为了不重建带 CHECK 约束的表；用户手输的模型名禁止使用该前缀。
const (
	WebToolsKey         = "server_tool:web"
	ReservedKeyPrefix   = "server_tool:"
	BindingKindModel    = "model"
	BindingKindWebTools = "web_tools"
)

// IsWebToolsKey 判断分配键是否为联网工具保留键。
func IsWebToolsKey(model string) bool {
	return NormalizeModelName(model) == WebToolsKey
}

// IsReservedKey 判断模型名是否落在保留前缀内（含联网工具保留键本身）。
// 用户手输的模型名一律不允许使用保留前缀，联网映射只能通过 kind=web_tools 建立。
func IsReservedKey(model string) bool {
	return strings.HasPrefix(NormalizeModelName(model), ReservedKeyPrefix)
}

// BindingKind 把分配键映射为对外的映射种类。
func BindingKind(model string) string {
	if IsWebToolsKey(model) {
		return BindingKindWebTools
	}
	return BindingKindModel
}

// FamilyClaude 与 FamilyCodex 是模型路由支持的协议族。
const FamilyClaude = "claude"
const FamilyCodex = "codex"

// 组内路由模式，与全局 claude_routing 三模式一一对应，只是作用域缩到组成员。
const (
	RouteModeAuto            = "auto"
	RouteModeManualPreferred = "manual_preferred"
	RouteModeManualFixed     = "manual_fixed"
)

// 数量与长度上限（方案 §4.2）
const (
	MaxGroups          = 50
	MaxAssignments     = 200
	MaxGroupMembers    = 32
	MaxGroupNameLen    = 64
	MaxModelNameLen    = 128
	MaxAssignmentNote  = 256
	MinGroupMemberSize = 1
)

// Group 上游组：一组端点 + 组内路由模式。
type Group struct {
	ID            int64     `json:"id"`
	Name          string    `json:"name"`
	Members       []string  `json:"members"`
	RouteMode     string    `json:"route_mode"`
	RouteTarget   string    `json:"route_target"`
	RouteRevision int64     `json:"route_revision"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Assignment 分配：把一个模型显式指向一个上游组。
type Assignment struct {
	ID        int64     `json:"id"`
	ModelName string    `json:"model_name"`
	GroupID   int64     `json:"group_id"`
	Enabled   bool      `json:"enabled"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Settings 模型路由单一开关。
type Settings struct {
	Enabled   bool      `json:"enabled"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Snapshot 编译产物，热路径只读；由服务层原子替换。
type Snapshot struct {
	Version  int64
	LoadedAt time.Time

	settings   Settings
	loadFailed bool
	groups     map[int64]*Group
	byModel    map[string]*Assignment
	byWebTools *Assignment
}

// Resolution 一次归组结果。MatchedKey 说明命中的是模型键还是联网工具保留键。
type Resolution struct {
	Assignment *Assignment
	Group      *Group
	MatchedKey string
}

// Compile 校验并编译出只读快照。任一分配指向不存在的组即报错——
// 引用完整性由服务层保证，编译期发现说明数据已损坏，此时由调用方发布
// FailedSnapshot 进入 fail-closed，不静默丢规则也不放行到全局路由。
func Compile(settings Settings, groups []Group, assignments []Assignment) (*Snapshot, error) {
	snapshot := &Snapshot{
		LoadedAt: time.Now(),
		settings: settings,
		groups:   make(map[int64]*Group, len(groups)),
		byModel:  make(map[string]*Assignment, len(assignments)),
	}

	for i := range groups {
		group := cloneGroup(groups[i])
		if _, exists := snapshot.groups[group.ID]; exists {
			return nil, fmt.Errorf("上游组 id %d 重复", group.ID)
		}
		snapshot.groups[group.ID] = group
	}
	groupList := make([]Group, 0, len(snapshot.groups))
	for _, group := range snapshot.groups {
		groupList = append(groupList, *group)
	}
	for i := range groupList {
		if err := ValidateGroup(groupList[i], groupList, nil); err != nil {
			return nil, fmt.Errorf("上游组 %q 无效: %w", groupList[i].Name, err)
		}
	}

	seenModels := make(map[string]struct{}, len(assignments))
	for i := range assignments {
		assignment := cloneAssignment(assignments[i])
		if _, ok := snapshot.groups[assignment.GroupID]; !ok {
			return nil, fmt.Errorf("模型 %q 指向的上游组 id %d 不存在", assignment.ModelName, assignment.GroupID)
		}
		key := NormalizeModelName(assignment.ModelName)
		if key == "" {
			return nil, fmt.Errorf("分配 id %d 的模型名为空", assignment.ID)
		}
		if _, duplicated := seenModels[key]; duplicated {
			return nil, fmt.Errorf("模型 %q 存在重复分配", assignment.ModelName)
		}
		seenModels[key] = struct{}{}
		if !assignment.Enabled {
			continue
		}
		if key == WebToolsKey {
			snapshot.byWebTools = assignment
			continue
		}
		snapshot.byModel[key] = assignment
	}

	return snapshot, nil
}

func cloneGroup(group Group) *Group {
	group.Members = append([]string(nil), group.Members...)
	return &group
}

func cloneAssignment(assignment Assignment) *Assignment {
	return &assignment
}

// EmptySnapshot 返回关闭状态的空快照（服务尚未加载时的初始值）。
func EmptySnapshot() *Snapshot {
	return &Snapshot{
		LoadedAt: time.Now(),
		groups:   map[int64]*Group{},
		byModel:  map[string]*Assignment{},
	}
}

// FailedSnapshot 返回加载失败快照，enabled 由本次加载结果决定。
// 开关读取失败传 false，使用全局路由；已明确开启但绑定加载失败传 true，返回 422。
func FailedSnapshot(enabled bool) *Snapshot {
	snapshot := EmptySnapshot()
	snapshot.settings = Settings{Enabled: enabled}
	snapshot.loadFailed = true
	return snapshot
}

// LoadFailed 报告快照是否来自一次失败的加载。
func (s *Snapshot) LoadFailed() bool {
	return s != nil && s.loadFailed
}

// Enabled 报告模型路由开关是否打开。nil 快照等价于关闭。
func (s *Snapshot) Enabled() bool {
	return s != nil && s.settings.Enabled
}

// Settings 返回快照内的开关状态。
func (s *Snapshot) Settings() Settings {
	if s == nil {
		return Settings{}
	}
	return s.settings
}

// Counts 返回组数与生效分配数，供 API 摘要使用。
func (s *Snapshot) Counts() (groups int, assignments int) {
	if s == nil {
		return 0, 0
	}
	assignments = len(s.byModel)
	if s.byWebTools != nil {
		assignments++
	}
	return len(s.groups), assignments
}

// Resolve 按请求原始模型名归组。model 为空或未命中时 ok=false。
func (s *Snapshot) Resolve(model string) (Resolution, bool) {
	if s == nil {
		return Resolution{}, false
	}
	key := NormalizeModelName(model)
	if key == "" {
		return Resolution{}, false
	}
	return s.resolveAssignment(key, s.byModel[key])
}

// ResolveRequest 是带请求特征的归组：携带联网工具且存在联网映射时优先命中它，
// 否则退回按模型名归组。联网映射是覆盖而不是门禁——没配就当普通请求处理。
func (s *Snapshot) ResolveRequest(model string, webTools bool) (Resolution, bool) {
	if s == nil {
		return Resolution{}, false
	}
	if webTools && s.byWebTools != nil {
		if resolution, ok := s.resolveAssignment(WebToolsKey, s.byWebTools); ok {
			return resolution, true
		}
	}
	return s.Resolve(model)
}

func (s *Snapshot) resolveAssignment(key string, assignment *Assignment) (Resolution, bool) {
	if assignment == nil {
		return Resolution{}, false
	}
	group, ok := s.groups[assignment.GroupID]
	if !ok {
		return Resolution{}, false
	}
	return Resolution{Assignment: cloneAssignment(*assignment), Group: cloneGroup(*group), MatchedKey: key}, true
}

// Group 按 ID 取组（供 retained revision 校验：组已删除时返回 false）。
func (s *Snapshot) Group(id int64) (*Group, bool) {
	if s == nil {
		return nil, false
	}
	group, ok := s.groups[id]
	if !ok {
		return nil, false
	}
	return cloneGroup(*group), true
}

// Groups 返回快照中的组副本，避免调用方修改热路径状态。
func (s *Snapshot) Groups() []Group {
	if s == nil {
		return nil
	}
	groups := make([]Group, 0, len(s.groups))
	for _, group := range s.groups {
		groups = append(groups, *cloneGroup(*group))
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].ID < groups[j].ID })
	return groups
}

// Assignments 返回已编译生效分配的副本，避免调用方修改热路径状态。
func (s *Snapshot) Assignments() []Assignment {
	if s == nil {
		return nil
	}
	assignments := make([]Assignment, 0, len(s.byModel)+1)
	for _, assignment := range s.byModel {
		assignments = append(assignments, *cloneAssignment(*assignment))
	}
	if s.byWebTools != nil {
		assignments = append(assignments, *cloneAssignment(*s.byWebTools))
	}
	sort.Slice(assignments, func(i, j int) bool {
		return NormalizeModelName(assignments[i].ModelName) < NormalizeModelName(assignments[j].ModelName)
	})
	return assignments
}

// NormalizeModelName 归一化模型名：TrimSpace + 小写，与 modelrewrite 的 exact 语义一致。
func NormalizeModelName(model string) string {
	return strings.ToLower(strings.TrimSpace(model))
}

// ParseMembers 解析 members_json。
func ParseMembers(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var members []string
	if err := json.Unmarshal([]byte(raw), &members); err != nil {
		return nil, fmt.Errorf("解析上游组成员失败: %w", err)
	}
	return members, nil
}

// EncodeMembers 序列化成员列表（保证空列表编码为 []，不是 null）。
func EncodeMembers(members []string) (string, error) {
	if members == nil {
		members = []string{}
	}
	encoded, err := json.Marshal(members)
	if err != nil {
		return "", fmt.Errorf("序列化上游组成员失败: %w", err)
	}
	return string(encoded), nil
}

// ValidateGroup 校验单个上游组。siblings 为除自身外的其它组（用于重名判定），
// knownEndpoints 为当前存在的端点名集合（只校验存在，不校验健康）。
func ValidateGroup(group Group, siblings []Group, knownEndpoints map[string]struct{}) error {
	name := strings.TrimSpace(group.Name)
	if name == "" {
		return fmt.Errorf("上游组名称不能为空")
	}
	if len([]rune(name)) > MaxGroupNameLen {
		return fmt.Errorf("上游组名称不能超过 %d 个字符", MaxGroupNameLen)
	}
	if strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return fmt.Errorf("上游组名称不能包含控制字符")
	}
	for _, sibling := range siblings {
		if sibling.ID != group.ID && strings.EqualFold(strings.TrimSpace(sibling.Name), name) {
			return fmt.Errorf("上游组名称 %q 已存在", name)
		}
	}

	if len(group.Members) < MinGroupMemberSize {
		return fmt.Errorf("上游组至少需要 1 个成员端点")
	}
	if len(group.Members) > MaxGroupMembers {
		return fmt.Errorf("上游组成员不能超过 %d 个", MaxGroupMembers)
	}
	seen := make(map[string]struct{}, len(group.Members))
	for _, member := range group.Members {
		member = strings.TrimSpace(member)
		if member == "" {
			return fmt.Errorf("上游组成员端点名不能为空")
		}
		if _, duplicated := seen[member]; duplicated {
			return fmt.Errorf("上游组成员 %q 重复", member)
		}
		seen[member] = struct{}{}
		if knownEndpoints != nil {
			if _, ok := knownEndpoints[member]; !ok {
				return fmt.Errorf("端点 %q 不存在", member)
			}
		}
	}

	target := strings.TrimSpace(group.RouteTarget)
	switch group.RouteMode {
	case RouteModeAuto:
		if target != "" {
			return fmt.Errorf("自动模式下不能指定目标端点")
		}
	case RouteModeManualPreferred, RouteModeManualFixed:
		if target == "" {
			return fmt.Errorf("优选/固定模式必须指定目标端点")
		}
		if _, ok := seen[target]; !ok {
			// 编辑成员导致目标掉出成员列表时同样落到这里：提示先恢复自动或改选目标。
			return fmt.Errorf("目标端点 %q 不在上游组成员中，请先恢复自动或改选目标", target)
		}
	default:
		return fmt.Errorf("不支持的组内路由模式 %q", group.RouteMode)
	}
	return nil
}

// ValidateAssignment 校验单条分配。siblings 为除自身外的其它分配，groups 为当前全部组。
func ValidateAssignment(assignment Assignment, matchType string, siblings []Assignment, groups []Group) error {
	if matchType != "" && matchType != MatchExact {
		return fmt.Errorf("本期只支持 exact 精确匹配")
	}

	model := strings.TrimSpace(assignment.ModelName)
	if model == "" {
		return fmt.Errorf("模型名不能为空")
	}
	if len([]rune(model)) > MaxModelNameLen {
		return fmt.Errorf("模型名不能超过 %d 个字符", MaxModelNameLen)
	}
	if strings.IndexFunc(model, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return fmt.Errorf("模型名 %q 不能包含空白或控制字符", model)
	}

	normalized := NormalizeModelName(model)
	if strings.HasPrefix(normalized, ReservedKeyPrefix) && normalized != WebToolsKey {
		return fmt.Errorf("模型名不能以保留前缀 %q 开头", ReservedKeyPrefix)
	}
	for _, sibling := range siblings {
		if sibling.ID != assignment.ID && NormalizeModelName(sibling.ModelName) == normalized {
			if normalized == WebToolsKey {
				return fmt.Errorf("联网工具映射已经存在，只能配置一条")
			}
			return fmt.Errorf("模型 %q 已经分配过上游组", model)
		}
	}

	if len([]rune(assignment.Note)) > MaxAssignmentNote {
		return fmt.Errorf("备注不能超过 %d 个字符", MaxAssignmentNote)
	}
	if strings.IndexFunc(assignment.Note, unicode.IsControl) >= 0 {
		return fmt.Errorf("备注不能包含控制字符")
	}

	for _, group := range groups {
		if group.ID == assignment.GroupID {
			return nil
		}
	}
	return fmt.Errorf("上游组不存在")
}
