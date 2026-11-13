package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"sinuo/internal/privacy"
	"sinuo/internal/store"
)

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
		ResponseRestoreEnabled: settingsRecord.ResponseRestoreEnabled,
		Mode:                   settingsRecord.Mode,
		ScanMaxBytes:           settingsRecord.ScanMaxBytes,
		OverLimitAction:        settingsRecord.OverLimitAction,
		OnError:                settingsRecord.OnError,
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
		activeRules   []privacy.Rule
		compileErrors []string
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
				if errors.Is(compileErr, privacy.ErrLiteralBudget) {
					// 容量失败不按普通无效规则跳过，否则会缺失部分保护。
					activeRules = append(activeRules, rule)
					continue
				}
				parseErr = compileErr
			}
		}
		if parseErr != nil {
			if record.Enabled {
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
		current := s.CurrentSnapshot()
		if errors.Is(err, privacy.ErrLiteralBudget) && (current.Version == 0 || current.CompileBlocked) {
			// 旧库超限仍允许管理与删减，开启隐私时由引擎按 on_error 原文放行或拒绝。
			// 仅保留规则描述供候选校验使用，不伪装成部分编译成功的词典。
			descriptions := make([]privacy.CompiledRule, len(activeRules))
			for i, rule := range activeRules {
				descriptions[i].Rule = rule
			}
			s.publishSnapshotLocked(&privacy.Snapshot{
				Settings: settings, RestoreKey: settingsRecord.RestoreKey, Rules: descriptions,
				CompileBlocked: true, CompileError: privacy.ErrLiteralBudget.Error(),
			})
			return nil
		}
		// 单条合法不代表组合后的容量合法；失败时保留当前完整快照。
		return fmt.Errorf("compile privacy snapshot failed: %w", err)
	}

	snapshot := &privacy.Snapshot{
		RestoreKey:   settingsRecord.RestoreKey,
		Settings:     settings,
		Rules:        compiled,
		CompileError: strings.Join(compileErrors, "; "),
		// 缓存随快照替换整体失效：规则、设置变化后旧的扫描结果不再适用
		Cache: privacy.NewSegmentCache(privacy.DefaultSegmentCacheCapacity),
	}
	s.publishSnapshotLocked(snapshot)
	return nil
}

// rulesWithExactSecrets 只替换精确值集合，普通规则仍以当前有效快照为准。
func rulesWithExactSecrets(current *privacy.Snapshot, records []*store.PrivacyExactSecretRecord) []privacy.Rule {
	rules := make([]privacy.Rule, 0, len(current.Rules)+len(records))
	for _, r := range current.Rules {
		if r.Source != privacy.SourceExact {
			rules = append(rules, r.Rule)
		}
	}
	return append(rules, exactSecretRules(records)...)
}

// 手动新增、替换或启用前编译完整候选集合，容量失败不得先落库。
// 与导入事务及重启加载共用 privacy.CompileRules 的预算规则。
func (s *PrivacyService) validateExactSecretCandidateLocked(ctx context.Context, candidate *store.PrivacyExactSecretRecord, replaceID int64) error {
	if s.CurrentSnapshot().CompileBlocked && !candidate.Enabled {
		// 允许逐条停用旧的超预算词典，直至能够完整重新编译。
		return nil
	}
	records, err := s.store.ListExactSecrets(ctx)
	if err != nil {
		return err
	}
	kept := records[:0]
	for _, record := range records {
		if record.ID != replaceID {
			kept = append(kept, record)
		}
	}
	kept = append(kept, candidate)
	_, err = privacy.CompileRules(rulesWithExactSecrets(s.CurrentSnapshot(), kept))
	return err
}

// publishSnapshotLocked 在持久化成功后发布一次；不再访问数据库或执行可能失败的编译。
func (s *PrivacyService) publishSnapshotLocked(snapshot *privacy.Snapshot) {
	snapshot.Version = s.version.Add(1)
	snapshot.LoadedAt = time.Now()
	snapshot.Dict = s.restoreDictionaryForSnapshot(s.CurrentSnapshot(), snapshot)
	s.snapshot.Store(snapshot)
	if snapshot.CompileError != "" {
		s.status.Store(PrivacyStatusDegraded)
		slog.Warn(fmt.Sprintf("⚠️ [隐私保护] 部分规则未激活: %s", snapshot.CompileError))
	} else {
		s.status.Store(PrivacyStatusOK)
	}
	slog.Info(fmt.Sprintf("🛡️ [隐私保护] 快照已更新: version=%d mode=%s rules=%d",
		snapshot.Version, snapshot.Settings.Mode, len(snapshot.Rules)))
}
