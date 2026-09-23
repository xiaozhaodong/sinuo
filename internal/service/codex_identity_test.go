package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"cc-forwarder/internal/codexidentity"
	"cc-forwarder/internal/store"
)

func TestCodexIdentitySettingsLifecycle(t *testing.T) {
	_, s, st := newCodexSubscriptionProxySettingsTestEnv(t)
	ctx := context.Background()
	if err := s.InitDefaults(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := s.CodexIdentityPolicy(ctx)
	if err != nil || !p.Enabled || !p.Automatic || p.Mode() != codexidentity.IdentityModeAuto || p.Version != codexidentity.DefaultCompatVersion {
		t.Fatalf("默认值错误 %+v %v", p, err)
	}
	if row, err := st.Get(ctx, categoryCodexIdentityInternal, "namespace"); err != nil || row == nil {
		t.Fatal("默认自动模式应初始化 namespace")
	}
	// 变更回调可读取相同服务，也允许已有应用回调中的迁移写入。
	callback := make(chan error, 1)
	s.SetOnChangeCallback(func() { _, err := s.CodexIdentityPolicy(ctx); callback <- err })
	done := make(chan error, 1)
	go func() {
		done <- s.UpdateAndApply(ctx, []*store.SettingRecord{{Category: CategoryCodexIdentity, Key: "mode", Value: "enabled"}, {Category: CategoryCodexIdentity, Key: "compat_version", Value: "0.156.0"}})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("设置回调死锁")
	}
	if err := <-callback; err != nil {
		t.Fatal(err)
	}
	s.SetOnChangeCallback(nil)
	enabled, err := s.CodexIdentityPolicy(ctx)
	if err != nil || !enabled.Enabled || enabled.Automatic || enabled.Mode() != codexidentity.IdentityModeEnabled || enabled.Namespace.IsZero() || enabled.Version != "0.156.0" {
		t.Fatalf("启用失败 %+v %v", enabled, err)
	}
	for _, records := range [][]*store.SettingRecord{
		{{Category: CategoryCodexIdentity, Key: "mode", Value: "enabled"}, {Category: CategoryCodexIdentity, Key: "compat_version", Value: "invalid"}},
		{{Category: CategoryCodexIdentity, Key: "mode", Value: "yes"}},
		{{Category: CategoryCodexIdentity, Key: "mode", Value: "disabled"}, {Category: CategoryCodexIdentity, Key: "mode", Value: "enabled"}},
		{{Category: CategoryCodexIdentity, Key: "unknown", Value: "x"}},
		{{Category: CategoryCodexIdentity, Key: "enabled", Value: "true"}},
		{{Category: CategoryCodexIdentity, Key: "mode", Value: "disabled"}, {Category: categoryCodexIdentityInternal, Key: "namespace", Value: "bad"}},
	} {
		if err := s.UpdateAndApply(ctx, records); err == nil {
			t.Fatal("非法写入被接受")
		}
		got, _ := s.CodexIdentityPolicy(ctx)
		if got != enabled {
			t.Fatal("非法批次部分生效")
		}
	}
	if err := s.ResetCategory(ctx, CategoryCodexIdentity); err != nil {
		t.Fatal(err)
	}
	restarted := NewSettingsService(st)
	after, err := restarted.CodexIdentityPolicy(ctx)
	if err != nil || after.Mode() != codexidentity.IdentityModeAuto || after.Namespace != enabled.Namespace {
		t.Fatal("重启或重置轮换了 namespace")
	}
	if err := st.BatchUpdateValues(ctx, []*store.SettingRecord{{Category: CategoryCodexIdentity, Key: "mode", Value: "disabled"}, {Category: CategoryCodexIdentity, Key: "compat_version", Value: "0.157.0"}}); err != nil {
		t.Fatal(err)
	}
	after, err = s.CodexIdentityPolicy(ctx)
	if err != nil || after.Enabled || after.Version != "0.157.0" || !enabled.Enabled || enabled.Version != "0.156.0" {
		t.Fatal("直接写入未生效或旧值快照被修改")
	}
	for _, fn := range []func() error{
		func() error { return s.Set(ctx, categoryCodexIdentityInternal, "namespace", "bad") },
		func() error { return s.Delete(ctx, categoryCodexIdentityInternal, "namespace") },
		func() error { return s.ResetCategory(ctx, categoryCodexIdentityInternal) },
		func() error { return s.Delete(ctx, CategoryCodexIdentity, "mode") },
	} {
		if fn() == nil {
			t.Fatal("通用接口可删除/覆盖身份设置")
		}
	}
}

type countingCodexIdentityStore struct {
	store.SettingsStore
	mu    sync.Mutex
	reads int
}

func (s *countingCodexIdentityStore) GetByCategory(ctx context.Context, category string) ([]*store.SettingRecord, error) {
	s.mu.Lock()
	s.reads++
	s.mu.Unlock()
	return s.SettingsStore.GetByCategory(ctx, category)
}

func (s *countingCodexIdentityStore) readCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

func TestCodexIdentityPolicyUsesSnapshotCache(t *testing.T) {
	_, _, base := newCodexSubscriptionProxySettingsTestEnv(t)
	ctx := context.Background()
	counting := &countingCodexIdentityStore{SettingsStore: base}
	svc := NewSettingsService(counting)
	if err := svc.InitDefaults(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CodexIdentityPolicySnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CodexIdentityPolicySnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	if got := counting.readCount(); got != 1 {
		t.Fatalf("策略快照未复用，SQLite 读取次数=%d", got)
	}
	if err := svc.Set(ctx, CategoryCodexIdentity, "mode", "enabled"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CodexIdentityPolicySnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	if got := counting.readCount(); got != 3 {
		t.Fatalf("设置写入后未失效快照，SQLite 读取次数=%d", got)
	}
}

func TestCodexIdentityNamespaceConcurrentInitializationAndFailure(t *testing.T) {
	_, s, st := newCodexSubscriptionProxySettingsTestEnv(t)
	ctx := context.Background()
	if err := st.Set(ctx, CategoryCodexIdentity, "mode", "enabled"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan codexidentity.Policy, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := NewSettingsService(st).CodexIdentityPolicy(ctx)
			if err != nil {
				t.Error(err)
			}
			results <- p
		}()
	}
	wg.Wait()
	close(results)
	var namespace codexidentity.Namespace
	for p := range results {
		if p.NamespaceError != nil || p.Namespace.IsZero() {
			t.Fatalf("namespace 初始化失败: %v", p.NamespaceError)
		}
		if namespace.IsZero() {
			namespace = p.Namespace
		}
		if namespace != p.Namespace {
			t.Fatal("并发创建多个 namespace")
		}
	}
	if err := st.Set(ctx, categoryCodexIdentityInternal, "namespace", "corrupt"); err != nil {
		t.Fatal(err)
	}
	p, err := s.CodexIdentityPolicy(ctx)
	if err != nil || p.NamespaceError == nil {
		t.Fatal("坏 namespace 未显式暴露")
	}
	if err := s.Set(ctx, CategoryCodexIdentity, "mode", "disabled"); err != nil {
		t.Fatal("namespace 坏了仍应允许关闭", err)
	}
	if err := s.Set(ctx, CategoryCodexIdentity, "mode", "enabled"); err == nil {
		t.Fatal("不应临时替换坏 namespace")
	}
	p, err = s.CodexIdentityPolicy(ctx)
	if err != nil || p.Enabled || p.NamespaceError != nil {
		t.Fatal("关闭回退失败")
	}
	row, _ := st.Get(ctx, categoryCodexIdentityInternal, "namespace")
	if row.Value != "corrupt" {
		t.Fatal("坏 namespace 被覆盖")
	}
}

// flakyNamespaceStore 让 namespace 读取先失败指定次数，模拟超时或数据库暂时不可用。
type flakyNamespaceStore struct {
	store.SettingsStore
	mu       sync.Mutex
	failures int
}

func (s *flakyNamespaceStore) Get(ctx context.Context, category, key string) (*store.SettingRecord, error) {
	if category == categoryCodexIdentityInternal {
		s.mu.Lock()
		fail := s.failures > 0
		if fail {
			s.failures--
		}
		s.mu.Unlock()
		if fail {
			return nil, context.DeadlineExceeded
		}
	}
	return s.SettingsStore.Get(ctx, category, key)
}

func TestCodexIdentitySnapshotDoesNotCacheTransientNamespaceFailure(t *testing.T) {
	_, _, base := newCodexSubscriptionProxySettingsTestEnv(t)
	ctx := context.Background()
	flaky := &flakyNamespaceStore{SettingsStore: base}
	svc := NewSettingsService(flaky)
	if err := svc.InitDefaults(ctx); err != nil {
		t.Fatal(err)
	}
	flaky.failures = 1
	p, err := svc.CodexIdentityPolicySnapshot(ctx)
	if err != nil || !errors.Is(p.NamespaceError, context.DeadlineExceeded) {
		t.Fatalf("瞬时读取失败未暴露: policy=%+v err=%v", p, err)
	}
	p, err = svc.CodexIdentityPolicySnapshot(ctx)
	if err != nil || p.NamespaceError != nil || p.Namespace.IsZero() {
		t.Fatalf("数据库恢复后快照仍返回旧失败: %v", p.NamespaceError)
	}

	// 坏值是确定性错误：继续缓存，绕过服务修好也要等设置写入失效后才重新读取。
	if err := base.Set(ctx, categoryCodexIdentityInternal, "namespace", "corrupt"); err != nil {
		t.Fatal(err)
	}
	svc = NewSettingsService(flaky)
	if p, _ = svc.CodexIdentityPolicySnapshot(ctx); p.NamespaceError == nil {
		t.Fatal("坏 namespace 未暴露")
	}
	if err := base.Set(ctx, categoryCodexIdentityInternal, "namespace", "067cbe02-1334-45d1-85c0-df7f459055ef"); err != nil {
		t.Fatal(err)
	}
	if p, _ = svc.CodexIdentityPolicySnapshot(ctx); p.NamespaceError == nil {
		t.Fatal("确定性 namespace 错误未随快照缓存")
	}
}

type failedIdentityStore struct {
	store.SettingsStore
	failWrite bool
}

func (s failedIdentityStore) GetByCategory(ctx context.Context, category string) ([]*store.SettingRecord, error) {
	if !s.failWrite {
		return nil, errors.New("read failed")
	}
	return s.SettingsStore.GetByCategory(ctx, category)
}
func (s failedIdentityStore) BatchUpdateValues(context.Context, []*store.SettingRecord) error {
	return errors.New("write failed")
}

func TestCodexIdentitySettingsFailuresAreVisible(t *testing.T) {
	_, _, st := newCodexSubscriptionProxySettingsTestEnv(t)
	ctx := context.Background()
	if err := st.Set(ctx, CategoryCodexIdentity, "mode", "disabled"); err != nil {
		t.Fatal(err)
	}
	s := NewSettingsService(failedIdentityStore{SettingsStore: st})
	if _, err := s.CodexIdentityPolicy(ctx); err == nil {
		t.Fatal("读取失败被当作默认值")
	}
	s = NewSettingsService(failedIdentityStore{SettingsStore: st, failWrite: true})
	called := false
	s.SetOnChangeCallback(func() { called = true })
	if err := s.UpdateAndApply(ctx, []*store.SettingRecord{{Category: CategoryCodexIdentity, Key: "mode", Value: "enabled"}}); err == nil || called {
		t.Fatal("写入失败触发生效通知")
	}
	p, err := s.CodexIdentityPolicy(ctx)
	if err != nil || p.Enabled {
		t.Fatal("写入失败改变已存策略")
	}
}

func TestCodexIdentityModeMigration(t *testing.T) {
	for _, tc := range []struct {
		name, legacy, current string
		want                  codexidentity.IdentityMode
	}{
		{"new", "", "", codexidentity.IdentityModeAuto},
		{"previously_enabled", "true", "", codexidentity.IdentityModeAuto},
		{"previously_disabled", "false", "", codexidentity.IdentityModeDisabled},
		{"legacy_bool_uppercase", "TRUE", "", codexidentity.IdentityModeAuto},
		{"legacy_bool_zero", "0", "", codexidentity.IdentityModeDisabled},
		{"existing_override", "true", "disabled", codexidentity.IdentityModeDisabled},
		{"existing_auto", "false", "auto", codexidentity.IdentityModeAuto},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, s, st := newCodexSubscriptionProxySettingsTestEnv(t)
			ctx := context.Background()
			for key, value := range map[string]string{"enabled": tc.legacy, "mode": tc.current} {
				if value != "" {
					if err := st.Set(ctx, CategoryCodexIdentity, key, value); err != nil {
						t.Fatal(err)
					}
				}
			}
			namespace := "067cbe02-1334-45d1-85c0-df7f459055ef"
			if err := st.Set(ctx, categoryCodexIdentityInternal, "namespace", namespace); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if err := s.InitDefaults(ctx); err != nil {
					t.Fatal(err)
				}
				p, err := s.CodexIdentityPolicySnapshot(ctx)
				if err != nil || p.Mode() != tc.want || p.NamespaceError != nil {
					t.Fatalf("迁移错误: mode=%s namespace_error=%v err=%v", p.Mode(), p.NamespaceError, err)
				}
				s = NewSettingsService(st)
			}
			legacy, err := st.Get(ctx, CategoryCodexIdentity, "enabled")
			if err != nil || legacy != nil {
				t.Fatal("旧开关未删除", err)
			}
			row, err := st.Get(ctx, categoryCodexIdentityInternal, "namespace")
			if err != nil || row == nil || row.Value != namespace {
				t.Fatal("迁移改变 namespace", err)
			}
		})
	}
}

func TestCodexIdentityModeMigrationRejectsInvalidLegacy(t *testing.T) {
	_, s, st := newCodexSubscriptionProxySettingsTestEnv(t)
	ctx := context.Background()
	if err := st.Set(ctx, CategoryCodexIdentity, "enabled", "broken"); err != nil {
		t.Fatal(err)
	}
	if err := s.InitDefaults(ctx); err != nil {
		t.Fatal("身份迁移失败不应中止其他设置初始化", err)
	}
	if row, err := st.Get(ctx, CategoryServer, "preferred_port"); err != nil || row == nil {
		t.Fatal("身份迁移失败时其他分类默认值未初始化", err)
	}
	if _, err := s.CodexIdentityPolicySnapshot(ctx); err == nil {
		t.Fatal("迁移未完成却发布了默认自动策略")
	}
	row, err := st.Get(ctx, CategoryCodexIdentity, "enabled")
	if err != nil || row == nil || row.Value != "broken" {
		t.Fatal("迁移失败修改旧数据", err)
	}
	row, err = st.Get(ctx, CategoryCodexIdentity, "mode")
	if err != nil || row != nil {
		t.Fatal("失败迁移留下新模式", err)
	}
}

type interruptedIdentityMigrationStore struct{ store.SettingsStore }

func (s interruptedIdentityMigrationStore) Delete(context.Context, string, string) error {
	return errors.New("migration interrupted")
}

func TestCodexIdentityModeMigrationResumesAfterInsert(t *testing.T) {
	_, _, st := newCodexSubscriptionProxySettingsTestEnv(t)
	ctx := context.Background()
	if err := st.Set(ctx, CategoryCodexIdentity, "enabled", "false"); err != nil {
		t.Fatal(err)
	}
	s := NewSettingsService(interruptedIdentityMigrationStore{st})
	if err := s.InitDefaults(ctx); err != nil {
		t.Fatal("迁移中断不应中止其他设置初始化", err)
	}
	legacy, err := st.Get(ctx, CategoryCodexIdentity, "enabled")
	if err != nil || legacy == nil {
		t.Fatal("迁移中断后旧开关应保留待重试", err)
	}
	p, err := s.CodexIdentityPolicySnapshot(ctx)
	if err != nil || p.Mode() != codexidentity.IdentityModeDisabled {
		t.Fatal("中断后未使用已持久化的新模式", err)
	}
	s = NewSettingsService(st)
	if err := s.InitDefaults(ctx); err != nil {
		t.Fatal(err)
	}
	p, err = s.CodexIdentityPolicySnapshot(ctx)
	legacy, readErr := st.Get(ctx, CategoryCodexIdentity, "enabled")
	if err != nil || readErr != nil || legacy != nil || p.Mode() != codexidentity.IdentityModeDisabled {
		t.Fatal("重启没有完成迁移或覆盖了新模式", err, readErr)
	}
}
