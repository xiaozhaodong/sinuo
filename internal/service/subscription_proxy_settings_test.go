package service

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"cc-forwarder/internal/store"

	_ "modernc.org/sqlite"
)

func newCodexSubscriptionProxySettingsTestEnv(t *testing.T) (*CodexSubscriptionProxySettingsService, *SettingsService, *store.SQLiteSettingsStore) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	schemaSQL, err := os.ReadFile(filepath.Join("..", "tracking", "schema.sql"))
	if err != nil {
		t.Fatalf("read schema failed: %v", err)
	}
	if _, err := db.Exec(string(schemaSQL)); err != nil {
		t.Fatalf("exec schema failed: %v", err)
	}
	settingsStore := store.NewSQLiteSettingsStore(db)
	return NewCodexSubscriptionProxySettingsService(settingsStore), NewSettingsService(settingsStore), settingsStore
}

func readCodexSubscriptionProxyRow(t *testing.T, st *store.SQLiteSettingsStore) *store.SettingRecord {
	t.Helper()
	record, err := st.Get(context.Background(), CategoryCodexSubscriptionProxy, CodexSubscriptionProxyConfigKey)
	if err != nil {
		t.Fatalf("get row failed: %v", err)
	}
	return record
}

func defaultCodexSubscriptionProxyInput() CodexSubscriptionProxyConfigInput {
	return CodexSubscriptionProxyConfigInput{
		Enabled:       false,
		Mode:          CodexSubscriptionProxyModeSubscription,
		ListenHost:    "127.0.0.1",
		ListenPort:    28088,
		WSCompression: CodexSubscriptionProxyCompressionAuto,
	}
}

func TestCodexSubscriptionProxySettings_LoadMissingRowReturnsDefaults(t *testing.T) {
	svc, _, _ := newCodexSubscriptionProxySettingsTestEnv(t)
	state, err := svc.Load(context.Background())
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if state.Initialized {
		t.Fatal("expected uninitialized state")
	}
	if state.Config.Enabled || state.Config.Mode != CodexSubscriptionProxyModeSubscription ||
		state.Config.ListenHost != "127.0.0.1" || state.Config.ListenPort != 28088 ||
		state.Config.WSCompression != CodexSubscriptionProxyCompressionAuto || state.Config.AccessConfigured() {
		t.Fatalf("unexpected defaults: %+v", state.Config)
	}
}

func TestCodexSubscriptionProxySettings_FirstSaveInsertsRowAndReturnsSecretOnce(t *testing.T) {
	svc, _, st := newCodexSubscriptionProxySettingsTestEnv(t)
	ctx := context.Background()

	result, err := svc.Save(ctx, defaultCodexSubscriptionProxyInput(), 0)
	if err != nil {
		t.Fatalf("first save failed: %v", err)
	}
	if len(result.AccessSecret) != 43 {
		t.Fatalf("expected 43-char base64url secret, got %q", result.AccessSecret)
	}
	if result.Config.Revision != 1 || !result.Config.AccessConfigured() {
		t.Fatalf("unexpected config after init: %+v", result.Config)
	}

	row := readCodexSubscriptionProxyRow(t, st)
	if row == nil {
		t.Fatal("expected config row")
	}
	if strings.Contains(row.Value, result.AccessSecret) {
		t.Fatal("plaintext secret must not be persisted")
	}
	var stored CodexSubscriptionProxyConfig
	if err := json.Unmarshal([]byte(row.Value), &stored); err != nil {
		t.Fatalf("stored value is not JSON: %v", err)
	}
	if stored.LocalAccessHash == nil || *stored.LocalAccessHash != hex.EncodeToString(HashCodexSubscriptionProxyAccessSecret(result.AccessSecret)) {
		t.Fatal("stored hash does not match sha256(secret)")
	}
	if row.ValueType != ValueTypeJSON {
		t.Fatalf("expected json value type, got %q", row.ValueType)
	}

	if !VerifyCodexSubscriptionProxyAccessSecret(stored.AccessHashBytes(), result.AccessSecret) {
		t.Fatal("secret should verify")
	}
	if VerifyCodexSubscriptionProxyAccessSecret(stored.AccessHashBytes(), result.AccessSecret+"x") {
		t.Fatal("wrong secret must not verify")
	}
	if VerifyCodexSubscriptionProxyAccessSecret(nil, result.AccessSecret) {
		t.Fatal("missing hash must not verify")
	}

	// 之后的 Load 不再返回密钥，只返回哈希已配置
	state, err := svc.Load(ctx)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if !state.Initialized || !state.Config.AccessConfigured() || state.Config.Revision != 1 {
		t.Fatalf("unexpected loaded state: %+v", state)
	}
}

func TestCodexSubscriptionProxySettings_SaveRequiresMatchingRevision(t *testing.T) {
	svc, _, _ := newCodexSubscriptionProxySettingsTestEnv(t)
	ctx := context.Background()
	if _, err := svc.Save(ctx, defaultCodexSubscriptionProxyInput(), 0); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	// 再次以 0 初始化必须冲突，不得覆盖
	if _, err := svc.Save(ctx, defaultCodexSubscriptionProxyInput(), 0); !errors.Is(err, ErrCodexSubscriptionProxyRevisionConflict) {
		t.Fatalf("expected revision conflict, got %v", err)
	}

	input := defaultCodexSubscriptionProxyInput()
	input.Mode = CodexSubscriptionProxyModePool
	input.Enabled = true
	result, err := svc.Save(ctx, input, 1)
	if err != nil {
		t.Fatalf("save with revision 1 failed: %v", err)
	}
	if result.Config.Revision != 2 || result.Config.Mode != CodexSubscriptionProxyModePool || !result.Config.Enabled {
		t.Fatalf("unexpected config: %+v", result.Config)
	}
	if result.AccessSecret != "" {
		t.Fatal("secret must only be returned on initialization or rotation")
	}
	if !result.Config.AccessConfigured() {
		t.Fatal("hash must survive subsequent saves")
	}

	// 过期 revision 再次保存冲突
	if _, err := svc.Save(ctx, input, 1); !errors.Is(err, ErrCodexSubscriptionProxyRevisionConflict) {
		t.Fatalf("expected stale revision conflict, got %v", err)
	}
}

func TestCodexSubscriptionProxySettings_ConcurrentSaveOnlyOneWins(t *testing.T) {
	svc, _, _ := newCodexSubscriptionProxySettingsTestEnv(t)
	ctx := context.Background()
	if _, err := svc.Save(ctx, defaultCodexSubscriptionProxyInput(), 0); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	const writers = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes, conflicts := 0, 0
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(port int) {
			defer wg.Done()
			input := defaultCodexSubscriptionProxyInput()
			input.ListenPort = port
			_, err := svc.Save(ctx, input, 1)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				successes++
			case errors.Is(err, ErrCodexSubscriptionProxyRevisionConflict):
				conflicts++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}(20000 + i)
	}
	wg.Wait()
	if successes != 1 || conflicts != writers-1 {
		t.Fatalf("expected exactly one success, got successes=%d conflicts=%d", successes, conflicts)
	}
	state, err := svc.Load(ctx)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if state.Config.Revision != 2 {
		t.Fatalf("expected revision 2, got %d", state.Config.Revision)
	}
}

func TestCodexSubscriptionProxySettings_ConcurrentInitializeOnlyOneWins(t *testing.T) {
	svc, _, _ := newCodexSubscriptionProxySettingsTestEnv(t)
	ctx := context.Background()

	const writers = 6
	var wg sync.WaitGroup
	var mu sync.Mutex
	secrets := 0
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := svc.Save(ctx, defaultCodexSubscriptionProxyInput(), 0)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				if result.AccessSecret == "" {
					t.Error("winner must receive the secret")
				}
				secrets++
			} else if !errors.Is(err, ErrCodexSubscriptionProxyRevisionConflict) {
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	if secrets != 1 {
		t.Fatalf("expected exactly one initialization, got %d", secrets)
	}
}

func TestCodexSubscriptionProxySettings_CorruptedRowIsReportedAndNeverOverwritten(t *testing.T) {
	svc, _, st := newCodexSubscriptionProxySettingsTestEnv(t)
	ctx := context.Background()

	cases := map[string]string{
		"invalid json":      `{"schema_version":1,"revision":1,`,
		"wrong schema":      `{"schema_version":2,"revision":1,"mode":"subscription","listen_host":"127.0.0.1","listen_port":18089,"ws_compression":"auto"}`,
		"bad port":          `{"schema_version":1,"revision":1,"mode":"subscription","listen_host":"127.0.0.1","listen_port":0,"ws_compression":"auto"}`,
		"non loopback host": `{"schema_version":1,"revision":1,"mode":"subscription","listen_host":"0.0.0.0","listen_port":18089,"ws_compression":"auto"}`,
		"bad hash":          `{"schema_version":1,"revision":1,"mode":"subscription","listen_host":"127.0.0.1","listen_port":18089,"ws_compression":"auto","local_access_hash":"zz"}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if err := st.Set(ctx, CategoryCodexSubscriptionProxy, CodexSubscriptionProxyConfigKey, raw); err != nil {
				t.Fatalf("seed row failed: %v", err)
			}
			if _, err := svc.Load(ctx); !errors.Is(err, ErrCodexSubscriptionProxyConfigCorrupted) {
				t.Fatalf("expected corrupted error, got %v", err)
			}
			if _, err := svc.Save(ctx, defaultCodexSubscriptionProxyInput(), 0); !errors.Is(err, ErrCodexSubscriptionProxyConfigCorrupted) {
				t.Fatalf("save must refuse corrupted config, got %v", err)
			}
			if _, err := svc.RotateAccess(ctx, 1); !errors.Is(err, ErrCodexSubscriptionProxyConfigCorrupted) {
				t.Fatalf("rotate must refuse corrupted config, got %v", err)
			}
			if row := readCodexSubscriptionProxyRow(t, st); row == nil || row.Value != raw {
				t.Fatal("corrupted row must remain untouched")
			}
		})
	}
}

func TestCodexSubscriptionProxySettings_InputValidation(t *testing.T) {
	svc, _, _ := newCodexSubscriptionProxySettingsTestEnv(t)
	ctx := context.Background()

	invalid := []CodexSubscriptionProxyConfigInput{
		{Mode: "hybrid", ListenHost: "127.0.0.1", ListenPort: 18089},
		{Mode: "pool", ListenHost: "0.0.0.0", ListenPort: 18089},
		{Mode: "pool", ListenHost: "192.168.1.10", ListenPort: 18089},
		{Mode: "pool", ListenHost: "localhost", ListenPort: 18089},
		{Mode: "pool", ListenHost: "127.0.0.1", ListenPort: 70000},
		{Mode: "pool", ListenHost: "127.0.0.1", ListenPort: -1},
		{Mode: "pool", ListenHost: "127.0.0.1", ListenPort: 18089, WSCompression: "gzip"},
	}
	for i, input := range invalid {
		if _, err := svc.Save(ctx, input, 0); !errors.Is(err, ErrCodexSubscriptionProxyInvalidConfig) {
			t.Fatalf("case %d: expected invalid config error, got %v", i, err)
		}
	}
	if state, err := svc.Load(ctx); err != nil || state.Initialized {
		t.Fatalf("invalid inputs must not initialize the row: state=%+v err=%v", state, err)
	}

	// 空值与大小写归一
	normalized, err := NormalizeCodexSubscriptionProxyInput(CodexSubscriptionProxyConfigInput{Mode: " POOL ", ListenHost: "[::1]", WSCompression: "Disabled"})
	if err != nil {
		t.Fatalf("normalize failed: %v", err)
	}
	if normalized.Mode != CodexSubscriptionProxyModePool || normalized.ListenHost != "::1" || normalized.ListenPort != 28088 || normalized.WSCompression != CodexSubscriptionProxyCompressionDisabled {
		t.Fatalf("unexpected normalization: %+v", normalized)
	}
}

func TestCodexSubscriptionProxySettings_RotateReplacesHashAndBumpsRevision(t *testing.T) {
	svc, _, st := newCodexSubscriptionProxySettingsTestEnv(t)
	ctx := context.Background()
	first, err := svc.Save(ctx, defaultCodexSubscriptionProxyInput(), 0)
	if err != nil {
		t.Fatalf("init failed: %v", err)
	}

	if _, err := svc.RotateAccess(ctx, 0); !errors.Is(err, ErrCodexSubscriptionProxyRevisionConflict) {
		t.Fatalf("rotate with stale revision must conflict, got %v", err)
	}
	rotated, err := svc.RotateAccess(ctx, 1)
	if err != nil {
		t.Fatalf("rotate failed: %v", err)
	}
	if rotated.AccessSecret == "" || rotated.AccessSecret == first.AccessSecret {
		t.Fatal("rotation must return a new secret")
	}
	if rotated.Config.Revision != 2 {
		t.Fatalf("expected revision 2, got %d", rotated.Config.Revision)
	}
	row := readCodexSubscriptionProxyRow(t, st)
	var stored CodexSubscriptionProxyConfig
	if err := json.Unmarshal([]byte(row.Value), &stored); err != nil {
		t.Fatalf("stored value is not JSON: %v", err)
	}
	if VerifyCodexSubscriptionProxyAccessSecret(stored.AccessHashBytes(), first.AccessSecret) {
		t.Fatal("old secret must stop verifying after rotation")
	}
	if !VerifyCodexSubscriptionProxyAccessSecret(stored.AccessHashBytes(), rotated.AccessSecret) {
		t.Fatal("new secret must verify")
	}
}

func TestCodexSubscriptionProxySettings_SetEnabled(t *testing.T) {
	svc, _, _ := newCodexSubscriptionProxySettingsTestEnv(t)
	ctx := context.Background()

	if _, err := svc.SetEnabled(ctx, true, 0); !errors.Is(err, ErrCodexSubscriptionProxyNotInitialized) {
		t.Fatalf("expected not initialized error, got %v", err)
	}
	if _, err := svc.Save(ctx, defaultCodexSubscriptionProxyInput(), 0); err != nil {
		t.Fatalf("init failed: %v", err)
	}
	enabled, err := svc.SetEnabled(ctx, true, 1)
	if err != nil {
		t.Fatalf("enable failed: %v", err)
	}
	if !enabled.Config.Enabled || enabled.Config.Revision != 2 || enabled.Config.Mode != CodexSubscriptionProxyModeSubscription {
		t.Fatalf("unexpected config: %+v", enabled.Config)
	}
	if _, err := svc.SetEnabled(ctx, false, 1); !errors.Is(err, ErrCodexSubscriptionProxyRevisionConflict) {
		t.Fatalf("expected conflict on stale revision, got %v", err)
	}
}

func TestCodexSubscriptionProxySettings_GenericSettingsWritesAreRejected(t *testing.T) {
	svc, generic, st := newCodexSubscriptionProxySettingsTestEnv(t)
	ctx := context.Background()
	if _, err := svc.Save(ctx, defaultCodexSubscriptionProxyInput(), 0); err != nil {
		t.Fatalf("init failed: %v", err)
	}
	before := readCodexSubscriptionProxyRow(t, st)

	record := &store.SettingRecord{Category: CategoryCodexSubscriptionProxy, Key: CodexSubscriptionProxyConfigKey, Value: "{}"}
	attempts := map[string]func() error{
		"Set": func() error {
			return generic.Set(ctx, CategoryCodexSubscriptionProxy, CodexSubscriptionProxyConfigKey, "{}")
		},
		"Delete": func() error {
			return generic.Delete(ctx, CategoryCodexSubscriptionProxy, CodexSubscriptionProxyConfigKey)
		},
		"BatchSet": func() error { return generic.BatchSet(ctx, []*store.SettingRecord{record}) },
		"UpdateAndApply": func() error {
			return generic.UpdateAndApply(ctx, []*store.SettingRecord{{Category: CategoryStrategy, Key: "type", Value: "priority"}, record})
		},
		"ResetCategory": func() error { return generic.ResetCategory(ctx, CategoryCodexSubscriptionProxy) },
	}
	for name, attempt := range attempts {
		if err := attempt(); !errors.Is(err, ErrProtectedSettingsCategory) {
			t.Fatalf("%s: expected protected category error, got %v", name, err)
		}
	}
	after := readCodexSubscriptionProxyRow(t, st)
	if after == nil || after.Value != before.Value {
		t.Fatal("protected row must remain unchanged after rejected writes")
	}

	// 其他分类不受影响
	if err := generic.Set(ctx, CategoryStrategy, "type", "fastest"); err != nil {
		t.Fatalf("generic set on strategy failed: %v", err)
	}
	if err := generic.ResetCategory(ctx, CategoryRetry); err != nil {
		t.Fatalf("generic reset on retry failed: %v", err)
	}
	if err := generic.UpdateAndApply(ctx, []*store.SettingRecord{{Category: CategoryStrategy, Key: "type", Value: "priority"}}); err != nil {
		t.Fatalf("generic batch update failed: %v", err)
	}
}

func TestCodexSubscriptionProxySettings_InitDefaultsNeverTouchesProtectedRow(t *testing.T) {
	svc, generic, st := newCodexSubscriptionProxySettingsTestEnv(t)
	ctx := context.Background()

	if err := generic.InitDefaults(ctx); err != nil {
		t.Fatalf("init defaults failed: %v", err)
	}
	if row := readCodexSubscriptionProxyRow(t, st); row != nil {
		t.Fatal("InitDefaults must not create the protected row")
	}
	for _, category := range generic.GetCategories() {
		if category.Name == CategoryCodexSubscriptionProxy {
			t.Fatal("protected category must not be listed as a generic category")
		}
	}

	if _, err := svc.Save(ctx, defaultCodexSubscriptionProxyInput(), 0); err != nil {
		t.Fatalf("init failed: %v", err)
	}
	before := readCodexSubscriptionProxyRow(t, st)
	if err := generic.InitDefaults(ctx); err != nil {
		t.Fatalf("second init defaults failed: %v", err)
	}
	after := readCodexSubscriptionProxyRow(t, st)
	if after.Value != before.Value || after.ValueType != before.ValueType {
		t.Fatal("InitDefaults must not overwrite the protected row")
	}
}

func TestSettingsStore_InsertIfAbsentAndCompareAndSwap(t *testing.T) {
	_, _, st := newCodexSubscriptionProxySettingsTestEnv(t)
	ctx := context.Background()
	record := &store.SettingRecord{Category: "cas_test", Key: "k", Value: "v1", ValueType: ValueTypeString}

	inserted, err := st.InsertIfAbsent(ctx, record)
	if err != nil || !inserted {
		t.Fatalf("first insert: inserted=%v err=%v", inserted, err)
	}
	record.Value = "v-other"
	inserted, err = st.InsertIfAbsent(ctx, record)
	if err != nil || inserted {
		t.Fatalf("second insert must be a no-op: inserted=%v err=%v", inserted, err)
	}
	if got, _ := st.Get(ctx, "cas_test", "k"); got == nil || got.Value != "v1" {
		t.Fatalf("value must stay v1, got %+v", got)
	}

	swapped, err := st.CompareAndSwapValue(ctx, "cas_test", "k", "wrong", "v2")
	if err != nil || swapped {
		t.Fatalf("CAS with wrong expected value must miss: swapped=%v err=%v", swapped, err)
	}
	swapped, err = st.CompareAndSwapValue(ctx, "cas_test", "k", "v1", "v2")
	if err != nil || !swapped {
		t.Fatalf("CAS with correct expected value must hit: swapped=%v err=%v", swapped, err)
	}
	swapped, err = st.CompareAndSwapValue(ctx, "cas_test", "missing", "v1", "v2")
	if err != nil || swapped {
		t.Fatalf("CAS on missing row must miss: swapped=%v err=%v", swapped, err)
	}
	if got, _ := st.Get(ctx, "cas_test", "k"); got == nil || got.Value != "v2" {
		t.Fatalf("value must be v2, got %+v", got)
	}
}
