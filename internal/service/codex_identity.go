package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"cc-forwarder/internal/codexidentity"
	"cc-forwarder/internal/store"
)

const (
	CategoryCodexIdentity         = "codex_identity"
	categoryCodexIdentityInternal = "codex_identity_internal"
)

func codexIdentityDefaults() []*store.SettingRecord {
	return []*store.SettingRecord{
		{
			Category: CategoryCodexIdentity, Key: "mode", Value: string(codexidentity.IdentityModeAuto),
			ValueType: ValueTypeString, Label: "身份处理", DisplayOrder: 1,
			Description: "自动识别实际目标：官方 OAuth 启用会话隔离，第三方/API Key 保留原有身份信息；订阅直通不受影响。切换接入或路由模式无需调整。",
		},
		{
			Category: CategoryCodexIdentity, Key: "compat_version", Value: codexidentity.DefaultCompatVersion,
			ValueType: ValueTypeString, Label: "兼容版本（高级）", DisplayOrder: 2,
			Description: "仅缺少有效客户端描述时使用，日常无需修改。",
		},
	}
}

// migrateCodexIdentityMode 只在初始化时替换旧开关；先落新键再删旧键，可安全重入。
// 已开启升级为自动；明确关闭保留回退意图；已有新模式不被覆盖。
func (s *SettingsService) migrateCodexIdentityMode(ctx context.Context) error {
	s.identityMu.Lock()
	defer s.identityMu.Unlock()
	legacy, err := s.store.Get(ctx, CategoryCodexIdentity, "enabled")
	if err != nil || legacy == nil {
		return err
	}
	defer s.invalidateCodexIdentityPolicyCache()
	current, err := s.store.Get(ctx, CategoryCodexIdentity, "mode")
	if err != nil {
		return err
	}
	if current == nil {
		next := codexIdentityDefaults()[0]
		enabled, err := strconv.ParseBool(legacy.Value)
		if err != nil {
			return fmt.Errorf("旧 codex_identity.enabled 值无效，无法迁移")
		}
		if !enabled {
			next.Value = string(codexidentity.IdentityModeDisabled)
		}
		if _, err := s.store.InsertIfAbsent(ctx, next); err != nil {
			return err
		}
	}
	if err := s.store.Delete(ctx, CategoryCodexIdentity, "enabled"); err != nil {
		return err
	}
	return nil
}

// 回调可能再次读取或写入设置，必须在释放写入锁之后触发。
func (s *SettingsService) withCodexIdentityUpdate(ctx context.Context, rows []*store.SettingRecord, write func() error) error {
	s.identityMu.Lock()
	defer s.identityMu.Unlock()
	if err := s.validateCodexIdentityUpdates(ctx, rows); err != nil {
		return err
	}
	if err := write(); err != nil {
		return err
	}
	s.invalidateCodexIdentityPolicyCache()
	return nil
}

// CodexIdentityPolicy 读取当前 settings。保留该方法的即时读取语义，供管理/诊断调用使用。
func (s *SettingsService) CodexIdentityPolicy(ctx context.Context) (codexidentity.Policy, error) {
	s.identityMu.Lock()
	defer s.identityMu.Unlock()
	return s.loadCodexIdentityPolicy(ctx)
}

// CodexIdentityPolicySnapshot 返回进程内策略快照；设置写入成功后主动失效，
// 避免每个账号池 attempt 都持全局锁查询 SQLite。
func (s *SettingsService) CodexIdentityPolicySnapshot(ctx context.Context) (codexidentity.Policy, error) {
	s.identityCacheMu.RLock()
	if s.identityCached {
		policy := s.identityPolicy
		s.identityCacheMu.RUnlock()
		return policy, nil
	}
	s.identityCacheMu.RUnlock()

	s.identityMu.Lock()
	defer s.identityMu.Unlock()
	s.identityCacheMu.RLock()
	if s.identityCached {
		policy := s.identityPolicy
		s.identityCacheMu.RUnlock()
		return policy, nil
	}
	s.identityCacheMu.RUnlock()
	policy, err := s.loadCodexIdentityPolicy(ctx)
	if err != nil {
		return policy, err
	}
	if !cacheableCodexIdentityPolicy(policy) {
		return policy, nil
	}
	s.identityCacheMu.Lock()
	s.identityPolicy = policy
	s.identityCached = true
	s.identityCacheMu.Unlock()
	return policy, nil
}

func (s *SettingsService) loadCodexIdentityPolicy(ctx context.Context) (codexidentity.Policy, error) {
	policy, err := s.readCodexIdentityPolicy(ctx, nil)
	if err != nil {
		return policy, err
	}
	if policy.Enabled {
		policy.Namespace, policy.NamespaceError = s.loadCodexIdentityNamespace(ctx)
	}
	return policy, nil
}

func (s *SettingsService) invalidateCodexIdentityPolicyCache() {
	s.identityCacheMu.Lock()
	s.identityCached = false
	s.identityCacheMu.Unlock()
}

// invalidCodexIdentityNamespaceError 表示已持久化的 namespace 值本身非法，属于确定性错误。
type invalidCodexIdentityNamespaceError struct{ err error }

func (e invalidCodexIdentityNamespaceError) Error() string { return e.err.Error() }
func (e invalidCodexIdentityNamespaceError) Unwrap() error { return e.err }

// cacheableCodexIdentityPolicy 读取、超时等可能是瞬时失败，不能写入快照，否则数据库恢复后
// 首方请求仍持续 503；坏 namespace 值是确定性错误，可随快照缓存避免每个请求重复读库。
func cacheableCodexIdentityPolicy(policy codexidentity.Policy) bool {
	var invalid invalidCodexIdentityNamespaceError
	return policy.NamespaceError == nil || errors.As(policy.NamespaceError, &invalid)
}

func (s *SettingsService) readCodexIdentityPolicy(ctx context.Context, updates []*store.SettingRecord) (codexidentity.Policy, error) {
	p := codexidentity.Policy{Version: codexidentity.DefaultCompatVersion}
	rows, err := s.store.GetByCategory(ctx, CategoryCodexIdentity)
	if err != nil {
		return p, err
	}
	values := map[string]string{"compat_version": p.Version}
	for _, row := range rows {
		values[row.Key] = row.Value
	}
	for _, row := range updates {
		if row != nil && row.Category == CategoryCodexIdentity {
			values[row.Key] = row.Value
		}
	}
	if _, exists := values["mode"]; !exists {
		if _, legacy := values["enabled"]; legacy {
			return p, fmt.Errorf("Codex 身份旧开关尚未迁移，请先完成设置初始化")
		}
		values["mode"] = string(codexidentity.IdentityModeAuto)
	}
	switch codexidentity.IdentityMode(values["mode"]) {
	case codexidentity.IdentityModeAuto:
		p.Enabled, p.Automatic = true, true
	case codexidentity.IdentityModeEnabled:
		p.Enabled = true
	case codexidentity.IdentityModeDisabled:
	default:
		return p, fmt.Errorf("codex_identity.mode 必须为 auto、enabled 或 disabled")
	}
	p.Version, err = codexidentity.ValidateCompatVersion(values["compat_version"])
	return p, err
}

func (s *SettingsService) loadCodexIdentityNamespace(ctx context.Context) (codexidentity.Namespace, error) {
	row, err := s.store.Get(ctx, categoryCodexIdentityInternal, "namespace")
	if err != nil {
		return codexidentity.Namespace{}, err
	}
	if row == nil {
		namespace, err := codexidentity.NewRandomNamespace()
		if err != nil {
			return namespace, err
		}
		_, err = s.store.InsertIfAbsent(ctx, &store.SettingRecord{Category: categoryCodexIdentityInternal, Key: "namespace", Value: namespace.String(), ValueType: ValueTypeString})
		if err != nil {
			return codexidentity.Namespace{}, err
		}
		row, err = s.store.Get(ctx, categoryCodexIdentityInternal, "namespace")
		if err != nil {
			return codexidentity.Namespace{}, err
		}
		if row == nil {
			return codexidentity.Namespace{}, fmt.Errorf("Codex namespace 写入后不存在")
		}
	}
	namespace, err := codexidentity.ParseNamespace(row.Value)
	if err != nil {
		return namespace, invalidCodexIdentityNamespaceError{err: err}
	}
	return namespace, nil
}

// validateCodexIdentityUpdates 在整批写入之前验证，失败不修改已有设置；必须持有 identityMu。
func (s *SettingsService) validateCodexIdentityUpdates(ctx context.Context, rows []*store.SettingRecord) error {
	found := false
	seen := map[string]bool{}
	for _, row := range rows {
		if row == nil || row.Category != CategoryCodexIdentity {
			continue
		}
		found = true
		if row.Key != "mode" && row.Key != "compat_version" {
			return fmt.Errorf("未知 Codex 身份设置: %s", row.Key)
		}
		if seen[row.Key] {
			return fmt.Errorf("重复 Codex 身份设置: %s", row.Key)
		}
		seen[row.Key] = true
	}
	if !found {
		return nil
	}
	p, err := s.readCodexIdentityPolicy(ctx, rows)
	if err != nil {
		return err
	}
	if p.Enabled {
		_, err = s.loadCodexIdentityNamespace(ctx)
	}
	return err
}
