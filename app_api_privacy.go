// app_api_privacy.go - 隐私保护规则管理 API (Wails Bindings)
package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"sinuo/internal/privacy"
	"sinuo/internal/service"
	"sinuo/internal/store"
)

// PrivacySettingsInfo 隐私保护全局设置
type PrivacySettingsInfo struct {
	Mode            string `json:"mode"`
	ScanMaxBytes    int64  `json:"scan_max_bytes"`
	OverLimitAction string `json:"over_limit_action"`
	OnError         string `json:"on_error"`
	Version         int64  `json:"version"`
	Status          string `json:"status"`
	CompileError    string `json:"compile_error"`
	EnabledRules    int    `json:"enabled_rules"`
	UpdatedAt       string `json:"updated_at"`
}

// PrivacySettingsInput 更新全局设置输入
type PrivacySettingsInput struct {
	Mode            string `json:"mode"`
	ScanMaxBytes    int64  `json:"scan_max_bytes"`
	OverLimitAction string `json:"over_limit_action"`
	OnError         string `json:"on_error"`
}

// PrivacyRuleInfo 隐私规则
type PrivacyRuleInfo struct {
	ID           int64  `json:"id"`
	Enabled      bool   `json:"enabled"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Priority     int    `json:"priority"`
	MatchType    string `json:"match_type"`
	Pattern      string `json:"pattern"`
	Placeholder  string `json:"placeholder"`
	Action       string `json:"action"`
	ScopeJSON    string `json:"scope_json"`
	Source       string `json:"source"`
	CompileError string `json:"compile_error"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

// PrivacyRuleInput 创建/更新规则输入
type PrivacyRuleInput struct {
	Enabled     bool   `json:"enabled"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Priority    int    `json:"priority"`
	MatchType   string `json:"match_type"`
	Pattern     string `json:"pattern"`
	Placeholder string `json:"placeholder"`
	Action      string `json:"action"`
	ScopeJSON   string `json:"scope_json"`
}

// PrivacyRuleOrderInput 规则排序输入
type PrivacyRuleOrderInput struct {
	ID       int64 `json:"id"`
	Priority int   `json:"priority"`
}

// PrivacyRuleTestInput 测试面板输入（文本只在内存中处理，不落日志/数据库）
type PrivacyRuleTestInput struct {
	Text         string `json:"text"`
	Path         string `json:"path"`
	UpstreamType string `json:"upstream_type"`
	EndpointName string `json:"endpoint_name"`
	AccountID    int64  `json:"account_id"`
	ProviderType string `json:"provider_type"`
}

// PrivacyRuleHitInfo 测试命中明细（不含命中原文）
type PrivacyRuleHitInfo struct {
	RuleID   int64  `json:"rule_id"`
	RuleName string `json:"rule_name"`
	Source   string `json:"source"`
	Action   string `json:"action"`
	Count    int    `json:"count"`
}

// PrivacyRuleTestResult 测试面板结果
type PrivacyRuleTestResult struct {
	OriginalLength int                  `json:"original_length"`
	HitCount       int                  `json:"hit_count"`
	Changed        bool                 `json:"changed"`
	ReplacedText   string               `json:"replaced_text"`
	RuleHits       []PrivacyRuleHitInfo `json:"rule_hits"`
	ScanDurationMs float64              `json:"scan_duration_ms"`
}

// PrivacyPresetInfo 预设规则集摘要
type PrivacyPresetInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	RuleCount   int    `json:"rule_count"`
}

// PrivacyRuleExportInfo 规则导出载荷
type PrivacyRuleExportInfo struct {
	ExportedAt string              `json:"exported_at"`
	Settings   PrivacySettingsInfo `json:"settings"`
	Rules      []PrivacyRuleInfo   `json:"rules"`
}

// PrivacyRuntimeStatsInfo 运行期聚合统计
type PrivacyRuntimeStatsInfo struct {
	ScanCount      int64                `json:"scan_count"`
	HitCount       int64                `json:"hit_count"`
	BlockedCount   int64                `json:"blocked_count"`
	TruncatedCount int64                `json:"truncated_count"`
	RuleStats      []PrivacyRuleHitInfo `json:"rule_stats"`
}

func (a *App) privacyServiceOrError() (*service.PrivacyService, error) {
	if a.privacyService == nil {
		return nil, fmt.Errorf("隐私保护服务未启用（需要开启 usage_tracking）")
	}
	return a.privacyService, nil
}

func privacyAPIContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

// GetPrivacySettings 读取全局设置与服务状态
func (a *App) GetPrivacySettings() (PrivacySettingsInfo, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	svc, err := a.privacyServiceOrError()
	if err != nil {
		return PrivacySettingsInfo{}, err
	}
	ctx, cancel := privacyAPIContext()
	defer cancel()
	settings, err := svc.GetSettings(ctx)
	if err != nil {
		return PrivacySettingsInfo{}, err
	}
	return a.buildPrivacySettingsInfo(svc, settings), nil
}

// UpdatePrivacySettings 更新全局设置并热生效
func (a *App) UpdatePrivacySettings(input PrivacySettingsInput) (PrivacySettingsInfo, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	svc, err := a.privacyServiceOrError()
	if err != nil {
		return PrivacySettingsInfo{}, err
	}
	ctx, cancel := privacyAPIContext()
	defer cancel()
	updated, err := svc.UpdateSettings(ctx, &store.PrivacySettingsRecord{
		Mode:            strings.TrimSpace(strings.ToLower(input.Mode)),
		ScanMaxBytes:    input.ScanMaxBytes,
		OverLimitAction: strings.TrimSpace(strings.ToLower(input.OverLimitAction)),
		OnError:         strings.TrimSpace(strings.ToLower(input.OnError)),
	})
	if err != nil {
		return PrivacySettingsInfo{}, err
	}
	return a.buildPrivacySettingsInfo(svc, updated), nil
}

// ListPrivacyRules 列出全部规则
func (a *App) ListPrivacyRules() ([]PrivacyRuleInfo, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	svc, err := a.privacyServiceOrError()
	if err != nil {
		return nil, err
	}
	ctx, cancel := privacyAPIContext()
	defer cancel()
	records, err := svc.ListRules(ctx)
	if err != nil {
		return nil, err
	}
	rules := make([]PrivacyRuleInfo, 0, len(records))
	for _, record := range records {
		rules = append(rules, buildPrivacyRuleInfo(record))
	}
	return rules, nil
}

// CreatePrivacyRule 新增规则（编译失败不落库）
func (a *App) CreatePrivacyRule(input PrivacyRuleInput) (PrivacyRuleInfo, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	svc, err := a.privacyServiceOrError()
	if err != nil {
		return PrivacyRuleInfo{}, err
	}
	ctx, cancel := privacyAPIContext()
	defer cancel()
	created, err := svc.CreateRule(ctx, privacyRuleRecordFromInput(0, input))
	if err != nil {
		return PrivacyRuleInfo{}, err
	}
	return buildPrivacyRuleInfo(created), nil
}

// UpdatePrivacyRule 更新规则（编译失败不落库）
func (a *App) UpdatePrivacyRule(id int64, input PrivacyRuleInput) (PrivacyRuleInfo, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	svc, err := a.privacyServiceOrError()
	if err != nil {
		return PrivacyRuleInfo{}, err
	}
	ctx, cancel := privacyAPIContext()
	defer cancel()
	updated, err := svc.UpdateRule(ctx, privacyRuleRecordFromInput(id, input))
	if err != nil {
		return PrivacyRuleInfo{}, err
	}
	return buildPrivacyRuleInfo(updated), nil
}

// DeletePrivacyRule 删除规则
func (a *App) DeletePrivacyRule(id int64) error {
	a.mu.RLock()
	defer a.mu.RUnlock()

	svc, err := a.privacyServiceOrError()
	if err != nil {
		return err
	}
	ctx, cancel := privacyAPIContext()
	defer cancel()
	return svc.DeleteRule(ctx, id)
}

// ReorderPrivacyRules 批量更新规则优先级
func (a *App) ReorderPrivacyRules(input []PrivacyRuleOrderInput) error {
	a.mu.RLock()
	defer a.mu.RUnlock()

	svc, err := a.privacyServiceOrError()
	if err != nil {
		return err
	}
	priorities := make(map[int64]int, len(input))
	for _, item := range input {
		priorities[item.ID] = item.Priority
	}
	ctx, cancel := privacyAPIContext()
	defer cancel()
	return svc.ReorderRules(ctx, priorities)
}

// TestPrivacyRules 测试面板：对一段文本执行当前规则（忽略全局模式）。
// 测试文本不记录日志、不写数据库。
func (a *App) TestPrivacyRules(input PrivacyRuleTestInput) (PrivacyRuleTestResult, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	svc, err := a.privacyServiceOrError()
	if err != nil {
		return PrivacyRuleTestResult{}, err
	}
	req := privacy.Request{
		Path:         strings.TrimSpace(input.Path),
		UpstreamType: strings.TrimSpace(input.UpstreamType),
		EndpointName: strings.TrimSpace(input.EndpointName),
		AccountID:    input.AccountID,
		ProviderType: strings.TrimSpace(input.ProviderType),
	}
	result := svc.TestText(req, input.Text)

	hits := make([]PrivacyRuleHitInfo, 0, len(result.RuleHits))
	for _, hit := range result.RuleHits {
		hits = append(hits, PrivacyRuleHitInfo{
			RuleID: hit.RuleID, RuleName: hit.RuleName, Source: hit.Source, Action: hit.Action, Count: hit.Count,
		})
	}
	return PrivacyRuleTestResult{
		OriginalLength: len(input.Text),
		HitCount:       result.HitCount,
		Changed:        result.Changed,
		ReplacedText:   string(result.Body),
		RuleHits:       hits,
		ScanDurationMs: float64(result.ScanDuration.Microseconds()) / 1000.0,
	}, nil
}

// ListPrivacyPresets 列出内置预设
func (a *App) ListPrivacyPresets() ([]PrivacyPresetInfo, error) {
	presets := privacy.Presets()
	out := make([]PrivacyPresetInfo, 0, len(presets))
	for _, preset := range presets {
		if preset.ID == privacy.PresetBasicPrivacy {
			continue
		}
		out = append(out, PrivacyPresetInfo{
			ID:          preset.ID,
			Name:        preset.Name,
			Description: preset.Description,
			RuleCount:   len(preset.Rules),
		})
	}
	return out, nil
}

// ImportPrivacyPreset 导入预设规则集，返回实际新增或同步更新的规则
func (a *App) ImportPrivacyPreset(presetID string) ([]PrivacyRuleInfo, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	svc, err := a.privacyServiceOrError()
	if err != nil {
		return nil, err
	}
	ctx, cancel := privacyAPIContext()
	defer cancel()
	created, err := svc.ImportPreset(ctx, strings.TrimSpace(presetID))
	if err != nil {
		return nil, err
	}
	rules := make([]PrivacyRuleInfo, 0, len(created))
	for _, record := range created {
		rules = append(rules, buildPrivacyRuleInfo(record))
	}
	return rules, nil
}

// ExportPrivacyRules 导出设置与全部规则
func (a *App) ExportPrivacyRules() (PrivacyRuleExportInfo, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	svc, err := a.privacyServiceOrError()
	if err != nil {
		return PrivacyRuleExportInfo{}, err
	}
	ctx, cancel := privacyAPIContext()
	defer cancel()
	export, err := svc.ExportRules(ctx)
	if err != nil {
		return PrivacyRuleExportInfo{}, err
	}
	rules := make([]PrivacyRuleInfo, 0, len(export.Rules))
	for _, record := range export.Rules {
		rules = append(rules, buildPrivacyRuleInfo(record))
	}
	return PrivacyRuleExportInfo{
		ExportedAt: export.ExportedAt,
		Settings:   a.buildPrivacySettingsInfo(svc, export.Settings),
		Rules:      rules,
	}, nil
}

// GetPrivacyRuntimeStats 运行期命中聚合统计（不含命中原文）
func (a *App) GetPrivacyRuntimeStats() (PrivacyRuntimeStatsInfo, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	svc, err := a.privacyServiceOrError()
	if err != nil {
		return PrivacyRuntimeStatsInfo{}, err
	}
	stats := svc.RuntimeStats()
	ruleStats := make([]PrivacyRuleHitInfo, 0, len(stats.RuleStats))
	for _, item := range stats.RuleStats {
		ruleStats = append(ruleStats, PrivacyRuleHitInfo{
			RuleID: item.RuleID, RuleName: item.RuleName, Source: item.Source, Count: int(item.HitCount),
		})
	}
	return PrivacyRuntimeStatsInfo{
		ScanCount:      stats.ScanCount,
		HitCount:       stats.HitCount,
		BlockedCount:   stats.BlockedCount,
		TruncatedCount: stats.TruncatedCount,
		RuleStats:      ruleStats,
	}, nil
}

func (a *App) buildPrivacySettingsInfo(svc *service.PrivacyService, settings *store.PrivacySettingsRecord) PrivacySettingsInfo {
	info := PrivacySettingsInfo{
		Status: svc.Status(),
	}
	if settings != nil {
		info.Mode = settings.Mode
		info.ScanMaxBytes = settings.ScanMaxBytes
		info.OverLimitAction = settings.OverLimitAction
		info.OnError = settings.OnError
		info.UpdatedAt = formatTime(settings.UpdatedAt)
	}
	if snapshot := svc.CurrentSnapshot(); snapshot != nil {
		info.Version = snapshot.Version
		info.CompileError = snapshot.CompileError
		info.EnabledRules = len(snapshot.Rules)
	}
	return info
}

func buildPrivacyRuleInfo(record *store.PrivacyRuleRecord) PrivacyRuleInfo {
	if record == nil {
		return PrivacyRuleInfo{}
	}
	return PrivacyRuleInfo{
		ID:           record.ID,
		Enabled:      record.Enabled,
		Name:         record.Name,
		Description:  record.Description,
		Priority:     record.Priority,
		MatchType:    record.MatchType,
		Pattern:      record.Pattern,
		Placeholder:  record.Placeholder,
		Action:       record.Action,
		ScopeJSON:    record.ScopeJSON,
		Source:       record.Source,
		CompileError: record.CompileError,
		CreatedAt:    formatTime(record.CreatedAt),
		UpdatedAt:    formatTime(record.UpdatedAt),
	}
}

func privacyRuleRecordFromInput(id int64, input PrivacyRuleInput) *store.PrivacyRuleRecord {
	return &store.PrivacyRuleRecord{
		ID:          id,
		Enabled:     input.Enabled,
		Name:        strings.TrimSpace(input.Name),
		Description: strings.TrimSpace(input.Description),
		Priority:    input.Priority,
		MatchType:   strings.TrimSpace(strings.ToLower(input.MatchType)),
		Pattern:     input.Pattern,
		Placeholder: input.Placeholder,
		Action:      strings.TrimSpace(strings.ToLower(input.Action)),
		ScopeJSON:   strings.TrimSpace(input.ScopeJSON),
	}
}
