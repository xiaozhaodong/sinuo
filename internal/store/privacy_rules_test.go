package store

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func newTestPrivacyStore(t *testing.T) *SQLitePrivacyStore {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewSQLitePrivacyStore(db)
}

func TestPrivacyStoreSettingsDefaultsAndUpdate(t *testing.T) {
	store := newTestPrivacyStore(t)
	ctx := context.Background()

	settings, err := store.GetSettings(ctx)
	if err != nil {
		t.Fatalf("get settings failed: %v", err)
	}
	if settings.Mode != "disabled" {
		t.Errorf("default mode = %q, want disabled", settings.Mode)
	}
	if settings.ScanMaxBytes != 4194304 {
		t.Errorf("default scan_max_bytes = %d", settings.ScanMaxBytes)
	}
	if settings.OverLimitAction != "scan_prefix" || settings.OnError != "fail_open" {
		t.Errorf("unexpected defaults: %+v", settings)
	}

	settings.Mode = "redact"
	settings.ScanMaxBytes = 1024
	settings.OverLimitAction = "fail_closed"
	settings.OnError = "fail_closed"
	if err := store.UpdateSettings(ctx, settings); err != nil {
		t.Fatalf("update settings failed: %v", err)
	}

	reloaded, err := store.GetSettings(ctx)
	if err != nil {
		t.Fatalf("reload settings failed: %v", err)
	}
	if reloaded.Mode != "redact" || reloaded.ScanMaxBytes != 1024 ||
		reloaded.OverLimitAction != "fail_closed" || reloaded.OnError != "fail_closed" {
		t.Errorf("settings not persisted: %+v", reloaded)
	}
}

func TestPrivacyStoreRuleCRUD(t *testing.T) {
	store := newTestPrivacyStore(t)
	ctx := context.Background()

	created, err := store.CreateRule(ctx, &PrivacyRuleRecord{
		Enabled:     true,
		Name:        "OpenAI Key",
		Description: "desc",
		Priority:    100,
		MatchType:   "regex",
		Pattern:     `sk-[A-Za-z0-9]{20,}`,
		Placeholder: "[密钥]",
		Action:      "redact",
		ScopeJSON:   `{"paths":["/v1/messages"]}`,
		Source:      "custom",
	})
	if err != nil {
		t.Fatalf("create rule failed: %v", err)
	}
	if created.ID <= 0 || created.Name != "OpenAI Key" || created.ScopeJSON != `{"paths":["/v1/messages"]}` {
		t.Fatalf("unexpected created record: %+v", created)
	}

	created.Name = "OpenAI Key v2"
	created.Enabled = false
	if err := store.UpdateRule(ctx, created); err != nil {
		t.Fatalf("update rule failed: %v", err)
	}

	fetched, err := store.GetRule(ctx, created.ID)
	if err != nil {
		t.Fatalf("get rule failed: %v", err)
	}
	if fetched == nil || fetched.Name != "OpenAI Key v2" || fetched.Enabled {
		t.Fatalf("update not persisted: %+v", fetched)
	}

	rules, err := store.ListRules(ctx)
	if err != nil {
		t.Fatalf("list rules failed: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("rule count = %d, want 1", len(rules))
	}

	if err := store.DeleteRule(ctx, created.ID); err != nil {
		t.Fatalf("delete rule failed: %v", err)
	}
	missing, err := store.GetRule(ctx, created.ID)
	if err != nil {
		t.Fatalf("get deleted rule failed: %v", err)
	}
	if missing != nil {
		t.Fatal("rule still exists after delete")
	}
	if err := store.DeleteRule(ctx, created.ID); err == nil {
		t.Fatal("deleting missing rule must fail")
	}
}

func TestPrivacyStoreListOrderAndPriorities(t *testing.T) {
	store := newTestPrivacyStore(t)
	ctx := context.Background()

	ruleA, err := store.CreateRule(ctx, &PrivacyRuleRecord{
		Enabled: true, Name: "A", Priority: 200, MatchType: "literal",
		Pattern: "a", Placeholder: "[a]", Action: "redact", ScopeJSON: "{}", Source: "custom",
	})
	if err != nil {
		t.Fatalf("create rule A failed: %v", err)
	}
	ruleB, err := store.CreateRule(ctx, &PrivacyRuleRecord{
		Enabled: true, Name: "B", Priority: 100, MatchType: "literal",
		Pattern: "b", Placeholder: "[b]", Action: "redact", ScopeJSON: "{}", Source: "custom",
	})
	if err != nil {
		t.Fatalf("create rule B failed: %v", err)
	}

	rules, err := store.ListRules(ctx)
	if err != nil {
		t.Fatalf("list rules failed: %v", err)
	}
	if len(rules) != 2 || rules[0].Name != "B" || rules[1].Name != "A" {
		t.Fatalf("unexpected order: %v, %v", rules[0].Name, rules[1].Name)
	}

	if err := store.UpdateRulePriorities(ctx, map[int64]int{ruleA.ID: 10, ruleB.ID: 20}); err != nil {
		t.Fatalf("update priorities failed: %v", err)
	}
	rules, err = store.ListRules(ctx)
	if err != nil {
		t.Fatalf("list rules failed: %v", err)
	}
	if rules[0].Name != "A" || rules[1].Name != "B" {
		t.Fatalf("priorities not applied: %v, %v", rules[0].Name, rules[1].Name)
	}
}

func TestPrivacyStoreBatchCreateAndCompileError(t *testing.T) {
	store := newTestPrivacyStore(t)
	ctx := context.Background()

	created, err := store.CreateRules(ctx, []*PrivacyRuleRecord{
		{Enabled: true, Name: "P1", Priority: 100, MatchType: "regex", Pattern: "x+", Placeholder: "[x]", Action: "redact", ScopeJSON: "{}", Source: "preset"},
		{Enabled: true, Name: "P2", Priority: 110, MatchType: "regex", Pattern: "y+", Placeholder: "[y]", Action: "redact", ScopeJSON: "{}", Source: "preset"},
	})
	if err != nil {
		t.Fatalf("batch create failed: %v", err)
	}
	if len(created) != 2 {
		t.Fatalf("created count = %d, want 2", len(created))
	}

	if err := store.SetRuleCompileError(ctx, created[0].ID, "bad pattern"); err != nil {
		t.Fatalf("set compile error failed: %v", err)
	}
	rule, err := store.GetRule(ctx, created[0].ID)
	if err != nil {
		t.Fatalf("get rule failed: %v", err)
	}
	if rule.CompileError != "bad pattern" {
		t.Errorf("compile_error = %q", rule.CompileError)
	}
}

func TestPrivacyStoreExactSecretCRUDAndUniqueness(t *testing.T) {
	store := newTestPrivacyStore(t)
	ctx := context.Background()

	created, err := store.CreateExactSecret(ctx, &PrivacyExactSecretRecord{
		Enabled:     true,
		Name:        "项目 Token",
		SecretValue: "tok_123456789012",
		ValueHash:   "hash-a",
		Placeholder: "[Token]",
		Category:    "token",
		SourceType:  "manual",
		SourceRef:   "",
		Description: "desc",
	})
	if err != nil {
		t.Fatalf("create exact secret failed: %v", err)
	}
	if created.ID <= 0 || created.SecretValue != "tok_123456789012" || created.ValueHash != "hash-a" {
		t.Fatalf("unexpected created exact secret: %+v", created)
	}

	if _, err := store.CreateExactSecret(ctx, &PrivacyExactSecretRecord{
		Enabled: true, Name: "重复", SecretValue: "other",
		ValueHash: "hash-a", Placeholder: "[Token]", Category: "token", SourceType: "manual",
	}); err == nil {
		t.Fatal("duplicate value_hash must fail")
	}

	found, err := store.FindExactSecretByHash(ctx, "hash-a")
	if err != nil {
		t.Fatalf("find exact secret failed: %v", err)
	}
	if found == nil || found.ID != created.ID {
		t.Fatalf("unexpected found record: %+v", found)
	}

	created.Enabled = false
	created.Name = "项目 Token v2"
	created.SecretValue = "tok_abcdefghijkl"
	created.ValueHash = "hash-b"
	if err := store.UpdateExactSecret(ctx, created); err != nil {
		t.Fatalf("update exact secret failed: %v", err)
	}
	reloaded, err := store.GetExactSecret(ctx, created.ID)
	if err != nil {
		t.Fatalf("get exact secret failed: %v", err)
	}
	if reloaded.Enabled || reloaded.Name != "项目 Token v2" || reloaded.ValueHash != "hash-b" {
		t.Fatalf("update not persisted: %+v", reloaded)
	}

	records, err := store.ListExactSecrets(ctx)
	if err != nil {
		t.Fatalf("list exact secrets failed: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("exact secret count = %d, want 1", len(records))
	}

	if err := store.DeleteExactSecret(ctx, created.ID); err != nil {
		t.Fatalf("delete exact secret failed: %v", err)
	}
	missing, err := store.GetExactSecret(ctx, created.ID)
	if err != nil {
		t.Fatalf("get deleted exact secret failed: %v", err)
	}
	if missing != nil {
		t.Fatal("exact secret still exists after delete")
	}
}

func TestPrivacyRestoreUpgradeAndKeyPersistence(t *testing.T) {
	s := newTestPrivacyStore(t)
	ctx := context.Background()
	_, err := s.db.Exec(`CREATE TABLE privacy_settings (id INTEGER PRIMARY KEY, mode TEXT DEFAULT 'disabled',scan_max_bytes INTEGER DEFAULT 4194304,over_limit_action TEXT DEFAULT 'scan_prefix',on_error TEXT DEFAULT 'fail_open',updated_at TEXT DEFAULT '2026-11-07T00:00:00.000000Z')`)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.GetSettings(ctx)
	if err != nil || r.ResponseRestoreEnabled || len(r.RestoreKey) != 0 {
		t.Fatal("旧库迁移失败", err)
	}
	r.ResponseRestoreEnabled = true
	if err = s.UpdateSettings(ctx, r); err != nil {
		t.Fatal(err)
	}
	r, err = s.GetSettings(ctx)
	if err != nil || len(r.RestoreKey) != 32 {
		t.Fatal("本地密钥未生成", err)
	}
	first := string(r.RestoreKey)
	if _, err = s.db.Exec(`UPDATE privacy_settings SET mode='redact' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	s = NewSQLitePrivacyStore(s.db)
	r, err = s.GetSettings(ctx)
	if err != nil || !r.ResponseRestoreEnabled || string(r.RestoreKey) != first || r.Mode != "redact" {
		t.Fatal("旧版写入或重启丢失配置", err)
	}
	for _, enabled := range []bool{false, true} {
		r.ResponseRestoreEnabled = enabled
		if err = s.UpdateSettings(ctx, r); err != nil {
			t.Fatal(err)
		}
		r, err = s.GetSettings(ctx)
		if err != nil || r.ResponseRestoreEnabled != enabled || string(r.RestoreKey) != first {
			t.Fatal("切换开关重置了映射密钥", err)
		}
	}
}
