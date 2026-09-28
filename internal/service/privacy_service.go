// Package service 提供隐私保护规则的业务服务：
// 负责 store 读写、规则编译、原子快照热替换与运行统计。
package service

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cc-forwarder/internal/privacy"
	"cc-forwarder/internal/store"
	timezonepolicy "cc-forwarder/internal/timezone"
)

// 隐私服务状态
const (
	PrivacyStatusOK       = "ok"
	PrivacyStatusDegraded = "degraded"
)

const privacyStatsDedupeWindow = 4096

// PrivacyRuleRuntimeStats 单规则运行期命中统计（不含命中原文）
type PrivacyRuleRuntimeStats struct {
	RuleID   int64  `json:"rule_id"`
	RuleName string `json:"rule_name"`
	Source   string `json:"source"`
	HitCount int64  `json:"hit_count"`
}

// PrivacyRuntimeStats 运行期聚合统计
type PrivacyRuntimeStats struct {
	ScanCount      int64                     `json:"scan_count"`
	HitCount       int64                     `json:"hit_count"`
	BlockedCount   int64                     `json:"blocked_count"`
	TruncatedCount int64                     `json:"truncated_count"`
	RuleStats      []PrivacyRuleRuntimeStats `json:"rule_stats"`
}

// PrivacyService 隐私保护服务
type PrivacyService struct {
	store    store.PrivacyStore
	mu       sync.Mutex // 串行化所有写操作（保存->编译->热替换）
	snapshot atomic.Pointer[privacy.Snapshot]
	version  atomic.Int64
	status   atomic.Value // string

	statsMu       sync.Mutex
	scanCount     int64
	hitCount      int64
	blockedCount  int64
	truncateCount int64
	ruleHits      map[int64]*PrivacyRuleRuntimeStats
	seenRequests  map[string]struct{}
	seenBlocked   map[string]struct{}
	seenTruncated map[string]struct{}
	seenRuleHits  map[string]map[int64]struct{} // requestID -> 已计数的规则
	seenOrder     []string
}

// NewPrivacyService 创建隐私保护服务
func NewPrivacyService(st store.PrivacyStore) *PrivacyService {
	svc := &PrivacyService{
		store:         st,
		ruleHits:      make(map[int64]*PrivacyRuleRuntimeStats),
		seenRequests:  make(map[string]struct{}),
		seenBlocked:   make(map[string]struct{}),
		seenTruncated: make(map[string]struct{}),
		seenRuleHits:  make(map[string]map[int64]struct{}),
	}
	svc.status.Store(PrivacyStatusOK)
	// 默认空快照（disabled），保证未初始化时热路径安全
	svc.snapshot.Store(&privacy.Snapshot{Settings: privacy.DefaultSettings(), LoadedAt: time.Now()})
	return svc
}

// Initialize 启动时从 SQLite 加载设置与规则并编译快照。
// enabled 规则编译失败时写回 compile_error、跳过激活并标记 degraded，不阻塞启动。
func (s *PrivacyService) Initialize(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rebuildSnapshotLocked(ctx, true)
}

// CurrentSnapshot 返回当前只读快照
func (s *PrivacyService) CurrentSnapshot() *privacy.Snapshot {
	if s == nil {
		return nil
	}
	return s.snapshot.Load()
}

// Status 返回服务状态（ok/degraded）
func (s *PrivacyService) Status() string {
	if s == nil {
		return PrivacyStatusOK
	}
	if status, ok := s.status.Load().(string); ok {
		return status
	}
	return PrivacyStatusOK
}

// Apply 实现 proxy 的 PrivacyFilter 接口：对出站请求 body 执行隐私过滤。
// 返回的 error 只会是 *privacy.PolicyError。
func (s *PrivacyService) Apply(_ context.Context, req privacy.Request, body []byte) (privacy.ApplyResult, error) {
	snapshot := s.CurrentSnapshot()
	result, err := snapshot.Apply(req, body)
	s.recordStats(req, result, err)
	return result, err
}

// TestText 测试面板：对一段裸文本执行当前规则（忽略全局模式，不记录原文与统计）
func (s *PrivacyService) TestText(req privacy.Request, text string) privacy.ApplyResult {
	return s.CurrentSnapshot().ApplyToText(req, text)
}

// GetSettings 读取全局设置
func (s *PrivacyService) GetSettings(ctx context.Context) (*store.PrivacySettingsRecord, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("privacy service is not initialized")
	}
	return s.store.GetSettings(ctx)
}

// UpdateSettings 更新全局设置并热生效
func (s *PrivacyService) UpdateSettings(ctx context.Context, record *store.PrivacySettingsRecord) (*store.PrivacySettingsRecord, error) {
	if record == nil {
		return nil, fmt.Errorf("privacy settings is nil")
	}
	settings := privacy.Settings{
		Mode:            record.Mode,
		ScanMaxBytes:    record.ScanMaxBytes,
		OverLimitAction: record.OverLimitAction,
		OnError:         record.OnError,
	}
	if err := privacy.ValidateSettings(settings); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.store.UpdateSettings(ctx, record); err != nil {
		return nil, err
	}
	if err := s.rebuildSnapshotLocked(ctx, false); err != nil {
		return nil, err
	}
	return s.store.GetSettings(ctx)
}

// ListRules 列出全部规则
func (s *PrivacyService) ListRules(ctx context.Context) ([]*store.PrivacyRuleRecord, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("privacy service is not initialized")
	}
	return s.store.ListRules(ctx)
}

// CreateRule 新增规则：先编译候选规则集，编译失败不写库、不替换快照
func (s *PrivacyService) CreateRule(ctx context.Context, record *store.PrivacyRuleRecord) (*store.PrivacyRuleRecord, error) {
	if record == nil {
		return nil, fmt.Errorf("privacy rule is nil")
	}
	normalizePrivacyRuleRecord(record)

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.validateCandidateLocked(ctx, record, 0); err != nil {
		return nil, err
	}
	created, err := s.store.CreateRule(ctx, record)
	if err != nil {
		return nil, err
	}
	if err := s.rebuildSnapshotLocked(ctx, false); err != nil {
		return nil, err
	}
	return created, nil
}

// UpdateRule 更新规则：先编译候选规则集，编译失败不写库、不替换快照
func (s *PrivacyService) UpdateRule(ctx context.Context, record *store.PrivacyRuleRecord) (*store.PrivacyRuleRecord, error) {
	if record == nil || record.ID <= 0 {
		return nil, fmt.Errorf("invalid privacy rule")
	}
	normalizePrivacyRuleRecord(record)

	s.mu.Lock()
	defer s.mu.Unlock()

	existing, err := s.store.GetRule(ctx, record.ID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, fmt.Errorf("privacy rule %d not found", record.ID)
	}
	if err := s.validateCandidateLocked(ctx, record, record.ID); err != nil {
		return nil, err
	}
	if err := s.store.UpdateRule(ctx, record); err != nil {
		return nil, err
	}
	if err := s.rebuildSnapshotLocked(ctx, false); err != nil {
		return nil, err
	}
	return s.store.GetRule(ctx, record.ID)
}

// DeleteRule 删除规则并热生效
func (s *PrivacyService) DeleteRule(ctx context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.store.DeleteRule(ctx, id); err != nil {
		return err
	}
	return s.rebuildSnapshotLocked(ctx, false)
}

// ReorderRules 批量更新优先级并热生效
func (s *PrivacyService) ReorderRules(ctx context.Context, priorities map[int64]int) error {
	if len(priorities) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.store.UpdateRulePriorities(ctx, priorities); err != nil {
		return err
	}
	return s.rebuildSnapshotLocked(ctx, false)
}

// ImportPreset 导入内置预设。
// 同名自定义规则跳过；同名预设规则同步到当前内置版本。
// 返回实际新增或同步更新的规则。
func (s *PrivacyService) ImportPreset(ctx context.Context, presetID string) ([]*store.PrivacyRuleRecord, error) {
	preset, ok := privacy.FindPreset(presetID)
	if !ok {
		return nil, fmt.Errorf("unknown privacy preset: %s", presetID)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	existing, err := s.store.ListRules(ctx)
	if err != nil {
		return nil, err
	}
	existingByName := make(map[string]*store.PrivacyRuleRecord, len(existing))
	for _, rule := range existing {
		existingByName[rule.Name] = rule
	}

	var candidates []*store.PrivacyRuleRecord
	var updates []*store.PrivacyRuleRecord
	for _, rule := range preset.Rules {
		scopeJSON, err := privacy.EncodeScope(rule.Scope)
		if err != nil {
			return nil, err
		}
		candidate := &store.PrivacyRuleRecord{
			Enabled:     rule.Enabled,
			Name:        rule.Name,
			Description: rule.Description,
			Priority:    rule.Priority,
			MatchType:   rule.MatchType,
			Pattern:     rule.Pattern,
			Placeholder: rule.Placeholder,
			Action:      rule.Action,
			ScopeJSON:   scopeJSON,
			Source:      rule.Source,
		}
		if candidate.Source == "" {
			candidate.Source = privacy.SourcePreset
		}
		existingRule := existingByName[rule.Name]
		if existingRule == nil {
			candidates = append(candidates, candidate)
			continue
		}
		if existingRule.Source != privacy.SourcePreset {
			continue
		}
		candidate.ID = existingRule.ID
		candidate.Enabled = existingRule.Enabled
		candidate.Priority = existingRule.Priority
		if !presetRuleNeedsSync(existingRule, candidate) {
			continue
		}
		updates = append(updates, candidate)
	}
	if len(candidates) == 0 && len(updates) == 0 {
		return nil, nil
	}

	for _, candidate := range candidates {
		if err := s.validateCandidateLocked(ctx, candidate, 0); err != nil {
			return nil, fmt.Errorf("preset rule %q invalid: %w", candidate.Name, err)
		}
	}
	for _, candidate := range updates {
		if err := s.validateCandidateLocked(ctx, candidate, candidate.ID); err != nil {
			return nil, fmt.Errorf("preset rule %q invalid: %w", candidate.Name, err)
		}
	}

	changed := make([]*store.PrivacyRuleRecord, 0, len(candidates)+len(updates))
	for _, candidate := range updates {
		if err := s.store.UpdateRule(ctx, candidate); err != nil {
			return nil, err
		}
		updated, err := s.store.GetRule(ctx, candidate.ID)
		if err != nil {
			return nil, err
		}
		if updated != nil {
			changed = append(changed, updated)
		}
	}
	if len(candidates) > 0 {
		created, err := s.store.CreateRules(ctx, candidates)
		if err != nil {
			return nil, err
		}
		changed = append(changed, created...)
	}
	if err := s.rebuildSnapshotLocked(ctx, false); err != nil {
		return nil, err
	}
	return changed, nil
}

func presetRuleNeedsSync(existing, candidate *store.PrivacyRuleRecord) bool {
	return existing.Description != candidate.Description ||
		existing.MatchType != candidate.MatchType ||
		existing.Pattern != candidate.Pattern ||
		existing.Placeholder != candidate.Placeholder ||
		existing.Action != candidate.Action ||
		existing.ScopeJSON != candidate.ScopeJSON ||
		existing.Source != candidate.Source
}

// PrivacyRuleExport 规则导出载荷
type PrivacyRuleExport struct {
	ExportedAt string                       `json:"exported_at"`
	Settings   *store.PrivacySettingsRecord `json:"settings"`
	Rules      []*store.PrivacyRuleRecord   `json:"rules"`
}

// ExportRules 导出设置与全部规则
func (s *PrivacyService) ExportRules(ctx context.Context) (*PrivacyRuleExport, error) {
	settings, err := s.store.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	rules, err := s.store.ListRules(ctx)
	if err != nil {
		return nil, err
	}
	return &PrivacyRuleExport{
		ExportedAt: timezonepolicy.FormatStorage(time.Now()),
		Settings:   settings,
		Rules:      rules,
	}, nil
}

// SnapshotVersion 当前快照版本
func (s *PrivacyService) SnapshotVersion() int64 {
	if snapshot := s.CurrentSnapshot(); snapshot != nil {
		return snapshot.Version
	}
	return 0
}

// PrivacyScopeFingerprint 返回当前快照下的 scope-aware attempt cache key。
// 只有存在对应 scope 维度的规则时才纳入端点/账号信息，减少全局规则在 failover 中的重复扫描。
func (s *PrivacyService) PrivacyScopeFingerprint(req privacy.Request, snapshotVersion int64) string {
	fields := []string{req.Path, strconv.FormatInt(snapshotVersion, 10)}
	snapshot := s.CurrentSnapshot()
	if snapshot == nil {
		return strings.Join(fields, "|")
	}

	includeUpstreamType := false
	includeEndpoint := false
	includeAccount := false
	includeProvider := false
	for _, rule := range snapshot.Rules {
		if len(rule.Scope.UpstreamTypes) > 0 {
			includeUpstreamType = true
		}
		if len(rule.Scope.EndpointNames) > 0 {
			includeEndpoint = true
		}
		if len(rule.Scope.AccountIDs) > 0 {
			includeAccount = true
		}
		if len(rule.Scope.ProviderTypes) > 0 {
			includeProvider = true
		}
	}
	if includeUpstreamType {
		fields = append(fields, "upstream="+req.UpstreamType)
	}
	if includeEndpoint {
		fields = append(fields, "endpoint="+req.EndpointName)
	}
	if includeAccount {
		fields = append(fields, "account="+strconv.FormatInt(req.AccountID, 10))
	}
	if includeProvider {
		fields = append(fields, "provider="+req.ProviderType)
	}
	return strings.Join(fields, "|")
}

// RuntimeStats 返回运行期聚合统计副本
func (s *PrivacyService) RuntimeStats() PrivacyRuntimeStats {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	stats := PrivacyRuntimeStats{
		ScanCount:      s.scanCount,
		HitCount:       s.hitCount,
		BlockedCount:   s.blockedCount,
		TruncatedCount: s.truncateCount,
	}
	for _, hit := range s.ruleHits {
		stats.RuleStats = append(stats.RuleStats, *hit)
	}
	sort.Slice(stats.RuleStats, func(i, j int) bool {
		return stats.RuleStats[i].HitCount > stats.RuleStats[j].HitCount
	})
	return stats
}

// validateCandidateLocked 用“当前规则集 + 本次变更”编译候选规则集。
// replaceID > 0 表示替换对应规则。要求调用方已持有 s.mu。
func (s *PrivacyService) validateCandidateLocked(ctx context.Context, candidate *store.PrivacyRuleRecord, replaceID int64) error {
	candidateRule, err := privacyRuleFromRecord(candidate)
	if err != nil {
		return err
	}
	// disabled 规则允许 pattern 暂时无效，但基本字段仍需合法；
	// 启用规则必须整体编译通过。
	if candidate.Enabled {
		if _, err := privacy.CompileRule(candidateRule); err != nil {
			return err
		}
	} else if err := privacy.ValidateRule(candidateRule); err != nil {
		return err
	}

	existing, err := s.store.ListRules(ctx)
	if err != nil {
		return err
	}
	var enabledRules []privacy.Rule
	for _, record := range existing {
		if record.ID == replaceID {
			continue
		}
		if !record.Enabled {
			continue
		}
		rule, err := privacyRuleFromRecord(record)
		if err != nil {
			// 历史规则 scope 损坏不阻塞新规则保存，启动时已降级处理
			continue
		}
		if _, err := privacy.CompileRule(rule); err != nil {
			continue
		}
		enabledRules = append(enabledRules, rule)
	}
	if candidate.Enabled {
		enabledRules = append(enabledRules, candidateRule)
	}
	if _, err := privacy.CompileRules(enabledRules); err != nil {
		return err
	}
	return nil
}

// rebuildSnapshotLocked 从 store 加载并编译新快照后原子替换。
// markCompileErrors 为 true 时（启动路径）将编译失败写回规则记录。
func (s *PrivacyService) rebuildSnapshotLocked(ctx context.Context, markCompileErrors bool) error {
	if s.store == nil {
		return fmt.Errorf("privacy store is nil")
	}
	settingsRecord, err := s.store.GetSettings(ctx)
	if err != nil {
		return err
	}
	settings := privacy.Settings{
		Mode:            settingsRecord.Mode,
		ScanMaxBytes:    settingsRecord.ScanMaxBytes,
		OverLimitAction: settingsRecord.OverLimitAction,
		OnError:         settingsRecord.OnError,
	}
	if err := privacy.ValidateSettings(settings); err != nil {
		// 历史脏数据回退默认（disabled），不阻塞启动
		slog.Warn(fmt.Sprintf("⚠️ [隐私保护] 设置非法，回退默认: %v", err))
		settings = privacy.DefaultSettings()
	}

	records, err := s.store.ListRules(ctx)
	if err != nil {
		return err
	}
	if markCompileErrors {
		if err := s.retireLegacyBuiltinRulesLocked(ctx, records); err != nil {
			return err
		}
	}
	if err := s.ensureBuiltinRulesLocked(ctx, records); err != nil {
		return err
	}
	records, err = s.store.ListRules(ctx)
	if err != nil {
		return err
	}
	exactSecrets, err := s.store.ListExactSecrets(ctx)
	if err != nil {
		return err
	}

	var (
		activeRules    []privacy.Rule
		compileErrors  []string
		degradedActive bool
	)
	for _, record := range records {
		rule, parseErr := privacyRuleFromRecord(record)
		if parseErr == nil && record.Enabled {
			if _, compileErr := privacy.CompileRule(rule); compileErr == nil {
				activeRules = append(activeRules, rule)
				if record.CompileError != "" {
					_ = s.store.SetRuleCompileError(ctx, record.ID, "")
				}
				continue
			} else {
				parseErr = compileErr
			}
		}
		if parseErr != nil {
			if record.Enabled {
				degradedActive = true
				compileErrors = append(compileErrors, fmt.Sprintf("%s: %v", record.Name, parseErr))
				if markCompileErrors {
					if err := s.store.SetRuleCompileError(ctx, record.ID, parseErr.Error()); err != nil {
						slog.Warn(fmt.Sprintf("⚠️ [隐私保护] 写回规则 %d compile_error 失败: %v", record.ID, err))
					}
				}
			}
		}
	}
	activeRules = append(activeRules, exactSecretRules(exactSecrets)...)

	compiled, err := privacy.CompileRules(activeRules)
	if err != nil {
		// activeRules 已逐条编译过，这里理论上不会失败；防御性保留当前快照
		return fmt.Errorf("compile privacy snapshot failed: %w", err)
	}

	snapshot := &privacy.Snapshot{
		Version:      s.version.Add(1),
		Settings:     settings,
		Rules:        compiled,
		LoadedAt:     time.Now(),
		CompileError: strings.Join(compileErrors, "; "),
		// 缓存随快照替换整体失效：规则、设置变化后旧的扫描结果不再适用
		Cache: privacy.NewSegmentCache(privacy.DefaultSegmentCacheCapacity),
	}
	s.snapshot.Store(snapshot)
	if degradedActive {
		s.status.Store(PrivacyStatusDegraded)
		slog.Warn(fmt.Sprintf("⚠️ [隐私保护] 部分规则未激活: %s", snapshot.CompileError))
	} else {
		s.status.Store(PrivacyStatusOK)
	}
	slog.Info(fmt.Sprintf("🛡️ [隐私保护] 快照已更新: version=%d mode=%s rules=%d",
		snapshot.Version, settings.Mode, len(compiled)))
	return nil
}

func (s *PrivacyService) recordStats(req privacy.Request, result privacy.ApplyResult, err error) {
	if s == nil {
		return
	}
	if result.Action == privacy.ModeDisabled && err == nil {
		return
	}
	requestID := strings.TrimSpace(req.RequestID)

	s.statsMu.Lock()
	defer s.statsMu.Unlock()

	firstForRequest := true
	if requestID != "" {
		if _, seen := s.seenRequests[requestID]; seen {
			firstForRequest = false
		} else {
			s.rememberStatsRequestLocked(requestID)
		}
	}
	if requestID == "" || firstForRequest {
		s.scanCount++
	}
	if err != nil && (requestID == "" || s.markStatsOnceLocked(s.seenBlocked, requestID)) {
		s.blockedCount++
	}
	if result.Truncated && (requestID == "" || s.markStatsOnceLocked(s.seenTruncated, requestID)) {
		s.truncateCount++
	}
	for _, hit := range result.RuleHits {
		if requestID != "" && !s.markRuleHitOnceLocked(requestID, hit.RuleID) {
			continue
		}
		s.hitCount += int64(hit.Count)
		entry := s.ruleHits[hit.RuleID]
		if entry == nil {
			entry = &PrivacyRuleRuntimeStats{RuleID: hit.RuleID, RuleName: hit.RuleName, Source: hit.Source}
			s.ruleHits[hit.RuleID] = entry
		}
		entry.HitCount += int64(hit.Count)
	}
}

func (s *PrivacyService) rememberStatsRequestLocked(requestID string) {
	if s.seenRequests == nil {
		s.seenRequests = make(map[string]struct{})
	}
	if s.seenBlocked == nil {
		s.seenBlocked = make(map[string]struct{})
	}
	if s.seenTruncated == nil {
		s.seenTruncated = make(map[string]struct{})
	}
	if s.seenRuleHits == nil {
		s.seenRuleHits = make(map[string]map[int64]struct{})
	}
	s.seenRequests[requestID] = struct{}{}
	s.seenOrder = append(s.seenOrder, requestID)
	if len(s.seenOrder) <= privacyStatsDedupeWindow {
		return
	}
	evicted := s.seenOrder[0]
	s.seenOrder = s.seenOrder[1:]
	delete(s.seenRequests, evicted)
	delete(s.seenBlocked, evicted)
	delete(s.seenTruncated, evicted)
	delete(s.seenRuleHits, evicted)
}

// markRuleHitOnceLocked 同一请求的同一规则只计一次（重试 / 换端点时不重复累计）
func (s *PrivacyService) markRuleHitOnceLocked(requestID string, ruleID int64) bool {
	rules := s.seenRuleHits[requestID]
	if rules == nil {
		rules = make(map[int64]struct{})
		s.seenRuleHits[requestID] = rules
	}
	if _, seen := rules[ruleID]; seen {
		return false
	}
	rules[ruleID] = struct{}{}
	return true
}

func (s *PrivacyService) markStatsOnceLocked(seen map[string]struct{}, requestID string) bool {
	if _, ok := seen[requestID]; ok {
		return false
	}
	seen[requestID] = struct{}{}
	return true
}

func normalizePrivacyRuleRecord(record *store.PrivacyRuleRecord) {
	record.Name = strings.TrimSpace(record.Name)
	record.MatchType = strings.TrimSpace(strings.ToLower(record.MatchType))
	record.Action = strings.TrimSpace(strings.ToLower(record.Action))
	if strings.TrimSpace(record.ScopeJSON) == "" {
		record.ScopeJSON = "{}"
	}
	if record.ID == 0 && record.Source == "" {
		record.Source = privacy.SourceCustom
	}
	if record.Priority == 0 {
		record.Priority = 100
	}
	if record.Action == privacy.ActionRedact && record.Placeholder == "" {
		record.Placeholder = privacy.DefaultPlaceholder
	}
}

func privacyRuleFromRecord(record *store.PrivacyRuleRecord) (privacy.Rule, error) {
	scope, err := privacy.ParseScope(record.ScopeJSON)
	if err != nil {
		return privacy.Rule{}, fmt.Errorf("rule %q scope invalid: %w", record.Name, err)
	}
	return privacy.Rule{
		ID:          record.ID,
		Enabled:     record.Enabled,
		Name:        record.Name,
		Description: record.Description,
		Priority:    record.Priority,
		MatchType:   record.MatchType,
		Pattern:     record.Pattern,
		Placeholder: record.Placeholder,
		Action:      record.Action,
		Scope:       scope,
		Source:      record.Source,
	}, nil
}
