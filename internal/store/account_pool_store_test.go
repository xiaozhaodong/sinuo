package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestListAccounts_AutoAddsMissingGroupKeyColumnForLegacyDatabase(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	legacySchema := `
	CREATE TABLE upstream_accounts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		provider_type TEXT NOT NULL DEFAULT 'api_key',
		account_name TEXT NOT NULL,
		credential_raw TEXT NOT NULL,
		base_url TEXT NOT NULL DEFAULT 'https://api.openai.com',
		cost_multiplier REAL DEFAULT 1.0,
		input_cost_multiplier REAL DEFAULT 1.0,
		output_cost_multiplier REAL DEFAULT 1.0,
		cache_creation_cost_multiplier REAL DEFAULT 1.0,
		cache_creation_cost_multiplier_1h REAL DEFAULT 1.0,
		cache_read_cost_multiplier REAL DEFAULT 1.0,
		priority INTEGER DEFAULT 100,
		enabled INTEGER DEFAULT 1,
		state TEXT DEFAULT 'active',
		cooldown_until DATETIME,
		fail_count INTEGER DEFAULT 0,
		last_success_at DATETIME,
		last_error TEXT DEFAULT '',
		plan_type TEXT DEFAULT '',
		chatgpt_account_id TEXT DEFAULT '',
		chatgpt_user_id TEXT DEFAULT '',
		organization_id TEXT DEFAULT '',
		quota_5h_used_percent REAL,
		quota_5h_reset_at DATETIME,
		quota_weekly_used_percent REAL,
		quota_weekly_reset_at DATETIME,
		quota_status TEXT DEFAULT '',
		quota_refreshed_at DATETIME,
		fingerprint TEXT UNIQUE NOT NULL,
		created_at DATETIME DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now', 'localtime') || '+08:00'),
		updated_at DATETIME DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now', 'localtime') || '+08:00')
	);
	CREATE INDEX idx_upstream_accounts_priority ON upstream_accounts(priority);
	`
	if _, err := db.Exec(legacySchema); err != nil {
		t.Fatalf("exec legacy schema failed: %v", err)
	}

	if _, err := db.Exec(`
		INSERT INTO upstream_accounts (
			provider_type, account_name, credential_raw, base_url,
			cost_multiplier, input_cost_multiplier, output_cost_multiplier,
			cache_creation_cost_multiplier, cache_creation_cost_multiplier_1h, cache_read_cost_multiplier,
			priority, enabled, state, fail_count, last_error,
			plan_type, chatgpt_account_id, chatgpt_user_id, organization_id,
			quota_status, fingerprint, created_at, updated_at
		) VALUES (
			'api_key', 'legacy-account', 'sk-legacy', 'https://api.openai.com',
			1.0, 1.0, 1.0, 1.0, 1.0, 1.0,
			10, 1, 'active', 0, '',
			'', '', '', '',
			'ok', 'legacy-fingerprint', '2026-03-22 12:00:00.000000+08:00', '2026-03-22 12:00:00.000000+08:00'
		)
	`); err != nil {
		t.Fatalf("insert legacy account failed: %v", err)
	}

	st := NewSQLiteAccountPoolStore(db)
	ctx := context.Background()

	accounts, err := st.ListAccounts(ctx, true)
	if err != nil {
		t.Fatalf("ListAccounts failed: %v", err)
	}
	if len(accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(accounts))
	}
	if accounts[0].GroupKey != "primary" {
		t.Fatalf("expected legacy account to infer primary group, got %+v", accounts[0])
	}

	current, err := st.GetAccount(ctx, accounts[0].ID)
	if err != nil {
		t.Fatalf("GetAccount failed: %v", err)
	}
	if current == nil || current.GroupKey != "primary" {
		t.Fatalf("expected GetAccount to work after auto-heal, got %+v", current)
	}

	var groupKeyCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('upstream_accounts') WHERE name = 'group_key'`).Scan(&groupKeyCount); err != nil {
		t.Fatalf("query group_key existence failed: %v", err)
	}
	if groupKeyCount != 1 {
		t.Fatalf("expected group_key column to be auto-added, got count=%d", groupKeyCount)
	}

	var modelRewriteRulesCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('upstream_accounts') WHERE name = 'model_rewrite_rules'`).Scan(&modelRewriteRulesCount); err != nil {
		t.Fatalf("query model_rewrite_rules existence failed: %v", err)
	}
	if modelRewriteRulesCount != 1 {
		t.Fatalf("expected model_rewrite_rules column to be auto-added, got count=%d", modelRewriteRulesCount)
	}
	var requestCompressionCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('upstream_accounts') WHERE name = 'enable_request_compression'`).Scan(&requestCompressionCount); err != nil {
		t.Fatalf("query enable_request_compression existence failed: %v", err)
	}
	if requestCompressionCount != 1 {
		t.Fatalf("expected enable_request_compression column to be auto-added, got count=%d", requestCompressionCount)
	}
	if current.EnableRequestCompression {
		t.Fatalf("legacy account should default to request compression disabled")
	}
}

func TestFindAccountByFingerprint_ReturnsAccount(t *testing.T) {
	st := newTestSQLiteAccountPoolStore(t)
	ctx := context.Background()

	record, err := st.CreateAccount(ctx, &UpstreamAccountRecord{
		ProviderType:                  "api_key",
		AccountName:                   "alpha",
		CredentialRaw:                 "sk-alpha",
		BaseURL:                       "https://api.openai.com",
		CostMultiplier:                1.6,
		InputCostMultiplier:           1.2,
		OutputCostMultiplier:          1.3,
		CacheCreationCostMultiplier:   1.4,
		CacheCreationCostMultiplier1h: 1.5,
		CacheReadCostMultiplier:       1.1,
		Priority:                      10,
		Enabled:                       true,
		State:                         "active",
	})
	if err != nil {
		t.Fatalf("CreateAccount failed: %v", err)
	}

	got, err := st.FindAccountByFingerprint(ctx, record.Fingerprint)
	if err != nil {
		t.Fatalf("FindAccountByFingerprint failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected account, got nil")
	}
	if got.ID != record.ID {
		t.Fatalf("expected account id %d, got %d", record.ID, got.ID)
	}
	if got.CostMultiplier != 1.6 || got.InputCostMultiplier != 1.2 || got.OutputCostMultiplier != 1.3 {
		t.Fatalf("unexpected multipliers: %+v", got)
	}
}

func TestUpdateAccountPriorities_UpdatesAllRequestedAccounts(t *testing.T) {
	st := newTestSQLiteAccountPoolStore(t)
	ctx := context.Background()

	first := mustCreateTestAccount(t, st, "first", 10)
	second := mustCreateTestAccount(t, st, "second", 20)
	third := mustCreateTestAccount(t, st, "third", 30)

	if err := st.UpdateAccountPriorities(ctx, map[int64]int{
		first.ID:  30,
		second.ID: 10,
		third.ID:  20,
	}); err != nil {
		t.Fatalf("UpdateAccountPriorities failed: %v", err)
	}

	accounts, err := st.ListAccounts(ctx, true)
	if err != nil {
		t.Fatalf("ListAccounts failed: %v", err)
	}
	if len(accounts) != 3 {
		t.Fatalf("expected 3 accounts, got %d", len(accounts))
	}

	if accounts[0].ID != second.ID || accounts[0].Priority != 10 {
		t.Fatalf("expected second account to become priority 10, got %+v", accounts[0])
	}
	if accounts[1].ID != third.ID || accounts[1].Priority != 20 {
		t.Fatalf("expected third account to become priority 20, got %+v", accounts[1])
	}
	if accounts[2].ID != first.ID || accounts[2].Priority != 30 {
		t.Fatalf("expected first account to become priority 30, got %+v", accounts[2])
	}
}

func TestCreateAccount_PersistsExplicitGroupKey(t *testing.T) {
	st := newTestSQLiteAccountPoolStore(t)
	ctx := context.Background()

	record, err := st.CreateAccount(ctx, &UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "grouped-primary",
		CredentialRaw: "sk-grouped-primary",
		BaseURL:       "https://api.openai.com",
		GroupKey:      "primary",
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("CreateAccount failed: %v", err)
	}

	if record.GroupKey != "primary" {
		t.Fatalf("expected group_key primary, got %+v", record)
	}
}

func TestCreateAccount_PersistsModelRewriteRules(t *testing.T) {
	st := newTestSQLiteAccountPoolStore(t)
	ctx := context.Background()

	rules := `[{"paths":["/v1/responses"],"match":"exact","from":"gpt-5.4","to":"gpt-5.5"}]`
	record, err := st.CreateAccount(ctx, &UpstreamAccountRecord{
		ProviderType:      "api_key",
		AccountName:       "rewrite-rules",
		CredentialRaw:     "sk-rewrite-rules",
		BaseURL:           "https://api.openai.com",
		ModelRewriteRules: rules,
		GroupKey:          "primary",
		Priority:          10,
		Enabled:           true,
		State:             "active",
	})
	if err != nil {
		t.Fatalf("CreateAccount failed: %v", err)
	}

	got, err := st.GetAccount(ctx, record.ID)
	if err != nil {
		t.Fatalf("GetAccount failed: %v", err)
	}
	if got.ModelRewriteRules != rules {
		t.Fatalf("expected model rewrite rules to persist, got %q", got.ModelRewriteRules)
	}
}

func TestCreateAccount_PersistsRequestCompressionSetting(t *testing.T) {
	st := newTestSQLiteAccountPoolStore(t)
	record, err := st.CreateAccount(context.Background(), &UpstreamAccountRecord{
		ProviderType:             "api_key",
		AccountName:              "zstd-account",
		CredentialRaw:            "sk-zstd-account",
		BaseURL:                  "https://api.openai.com",
		EnableRequestCompression: true,
		Enabled:                  true,
	})
	if err != nil {
		t.Fatalf("CreateAccount failed: %v", err)
	}
	got, err := st.GetAccount(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("GetAccount failed: %v", err)
	}
	if !got.EnableRequestCompression {
		t.Fatalf("expected request compression setting to persist")
	}
}

func TestCreateAccount_DoesNotDefaultAnyRouterModelRewriteRules(t *testing.T) {
	st := newTestSQLiteAccountPoolStore(t)
	ctx := context.Background()

	record, err := st.CreateAccount(ctx, &UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "anyrouter",
		CredentialRaw: "sk-anyrouter",
		BaseURL:       "https://anyrouter.top",
		GroupKey:      "primary",
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("CreateAccount failed: %v", err)
	}

	got, err := st.GetAccount(ctx, record.ID)
	if err != nil {
		t.Fatalf("GetAccount failed: %v", err)
	}
	if got.ModelRewriteRules != "" {
		t.Fatalf("expected new AnyRouter account to require explicit model rewrite rules, got %q", got.ModelRewriteRules)
	}
}

func TestUpdateAccount_AllowsClearingExplicitModelRewriteRules(t *testing.T) {
	st := newTestSQLiteAccountPoolStore(t)
	ctx := context.Background()
	rules := `[{"paths":["/v1/responses"],"match":"exact","from":"gpt-5.4","to":"gpt-5.5"}]`

	record, err := st.CreateAccount(ctx, &UpstreamAccountRecord{
		ProviderType:      "api_key",
		AccountName:       "anyrouter-clear",
		CredentialRaw:     "sk-anyrouter-clear",
		BaseURL:           "https://anyrouter.top",
		ModelRewriteRules: rules,
		GroupKey:          "primary",
		Priority:          10,
		Enabled:           true,
		State:             "active",
	})
	if err != nil {
		t.Fatalf("CreateAccount failed: %v", err)
	}

	record.ModelRewriteRules = ""
	if err := st.UpdateAccount(ctx, record); err != nil {
		t.Fatalf("UpdateAccount failed: %v", err)
	}
	got, err := st.GetAccount(ctx, record.ID)
	if err != nil {
		t.Fatalf("GetAccount failed: %v", err)
	}
	if got.ModelRewriteRules != "" {
		t.Fatalf("expected cleared model rewrite rules to stay empty, got %q", got.ModelRewriteRules)
	}
}

func TestListAccounts_MigratesPrefixModelRewriteRulesToExact(t *testing.T) {
	st := newTestSQLiteAccountPoolStore(t)
	ctx := context.Background()
	prefixRules := `[{"paths":["/v1/responses"],"match":"prefix","from":"gpt-5.4","to":"gpt-5.5"}]`

	if _, err := st.db.ExecContext(ctx, `
		INSERT INTO upstream_accounts (
			provider_type, account_name, credential_raw, base_url, model_rewrite_rules,
			priority, enabled, state, fingerprint
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "api_key", "prefix-rule", "sk-prefix-rule", "https://api.anyrouter.example", prefixRules, 10, 1, "active", "prefix-rule-fingerprint"); err != nil {
		t.Fatalf("insert prefix rule account failed: %v", err)
	}

	if _, err := st.ListAccounts(ctx, true); err != nil {
		t.Fatalf("ListAccounts failed: %v", err)
	}

	var storedRules string
	if err := st.db.QueryRowContext(ctx, `SELECT model_rewrite_rules FROM upstream_accounts WHERE account_name = 'prefix-rule'`).Scan(&storedRules); err != nil {
		t.Fatalf("query model rewrite rules failed: %v", err)
	}
	if !strings.Contains(storedRules, `"match":"exact"`) {
		t.Fatalf("expected prefix rule to migrate to exact, got %s", storedRules)
	}
	if strings.Contains(storedRules, `"match":"prefix"`) {
		t.Fatalf("expected migrated rules not to contain prefix, got %s", storedRules)
	}
}

func TestListAccounts_RebuildsMissingGroupKeysFromLegacyPriorityTiers(t *testing.T) {
	st := newTestSQLiteAccountPoolStore(t)
	ctx := context.Background()

	first := mustCreateTestAccount(t, st, "legacy-primary", 10)
	second := mustCreateTestAccount(t, st, "legacy-backup", 20)
	third := mustCreateTestAccount(t, st, "legacy-cold-a", 30)
	fourth := mustCreateTestAccount(t, st, "legacy-cold-b", 40)

	if _, err := st.db.ExecContext(ctx, `UPDATE upstream_accounts SET group_key = '' WHERE id IN (?, ?, ?, ?)`, first.ID, second.ID, third.ID, fourth.ID); err != nil {
		t.Fatalf("clear group_key failed: %v", err)
	}

	accounts, err := st.ListAccounts(ctx, true)
	if err != nil {
		t.Fatalf("ListAccounts failed: %v", err)
	}

	gotByID := make(map[int64]*UpstreamAccountRecord, len(accounts))
	for _, account := range accounts {
		gotByID[account.ID] = account
	}

	if gotByID[first.ID].GroupKey != "primary" {
		t.Fatalf("expected first account group primary, got %+v", gotByID[first.ID])
	}
	if gotByID[second.ID].GroupKey != "backup" {
		t.Fatalf("expected second account group backup, got %+v", gotByID[second.ID])
	}
	if gotByID[third.ID].GroupKey != "cold" {
		t.Fatalf("expected third account group cold, got %+v", gotByID[third.ID])
	}
	if gotByID[fourth.ID].GroupKey != "cold" {
		t.Fatalf("expected fourth account group cold, got %+v", gotByID[fourth.ID])
	}
}

func TestListAccounts_ReindexesPriorityWithinEachExplicitGroup(t *testing.T) {
	st := newTestSQLiteAccountPoolStore(t)
	ctx := context.Background()

	primaryA := mustCreateTestAccount(t, st, "primary-a", 90)
	primaryB := mustCreateTestAccount(t, st, "primary-b", 10)
	backupA := mustCreateTestAccount(t, st, "backup-a", 70)

	if _, err := st.db.ExecContext(ctx, `
		UPDATE upstream_accounts
		SET group_key = CASE id
			WHEN ? THEN 'primary'
			WHEN ? THEN 'primary'
			WHEN ? THEN 'backup'
		END
		WHERE id IN (?, ?, ?)
	`, primaryA.ID, primaryB.ID, backupA.ID, primaryA.ID, primaryB.ID, backupA.ID); err != nil {
		t.Fatalf("seed group_key failed: %v", err)
	}

	accounts, err := st.ListAccounts(ctx, true)
	if err != nil {
		t.Fatalf("ListAccounts failed: %v", err)
	}

	gotByID := make(map[int64]*UpstreamAccountRecord, len(accounts))
	for _, account := range accounts {
		gotByID[account.ID] = account
	}

	if gotByID[primaryB.ID].Priority != 10 {
		t.Fatalf("expected primary-b to keep first group priority 10, got %+v", gotByID[primaryB.ID])
	}
	if gotByID[primaryA.ID].Priority != 20 {
		t.Fatalf("expected primary-a to become second primary priority 20, got %+v", gotByID[primaryA.ID])
	}
	if gotByID[backupA.ID].Priority != 10 {
		t.Fatalf("expected backup-a to become first backup priority 10, got %+v", gotByID[backupA.ID])
	}
}

func TestMarkAccountSuccessIfNoNewerFailure_PreservesNewerCooldownState(t *testing.T) {
	st := newTestSQLiteAccountPoolStore(t)
	ctx := context.Background()

	record := mustCreateTestAccount(t, st, "guarded", 10)
	failureTime := time.Now()
	if err := st.MarkAccountTransientFailure(ctx, record.ID, "stream failed", failureTime.Add(60*time.Second)); err != nil {
		t.Fatalf("MarkAccountTransientFailure failed: %v", err)
	}

	updated, err := st.MarkAccountSuccessIfNoNewerFailure(ctx, record.ID, failureTime.Add(2*time.Second), failureTime.Add(-2*time.Second))
	if err != nil {
		t.Fatalf("MarkAccountSuccessIfNoNewerFailure failed: %v", err)
	}
	if updated {
		t.Fatal("expected stale success not to clear newer cooldown state")
	}

	current, err := st.GetAccount(ctx, record.ID)
	if err != nil {
		t.Fatalf("GetAccount failed: %v", err)
	}
	if current.State != "cooldown" {
		t.Fatalf("expected account to remain in cooldown, got %s", current.State)
	}
	if current.CooldownUntil == nil {
		t.Fatal("expected cooldown_until to remain set")
	}
	if current.FailCount != 1 {
		t.Fatalf("expected fail_count to remain 1, got %d", current.FailCount)
	}

	updated, err = st.MarkAccountSuccessIfNoNewerFailure(ctx, record.ID, failureTime.Add(3*time.Second), failureTime.Add(3*time.Second))
	if err != nil {
		t.Fatalf("MarkAccountSuccessIfNoNewerFailure second call failed: %v", err)
	}
	if !updated {
		t.Fatal("expected newer success attempt to clear cooldown state")
	}

	current, err = st.GetAccount(ctx, record.ID)
	if err != nil {
		t.Fatalf("GetAccount after success failed: %v", err)
	}
	if current.State != "active" {
		t.Fatalf("expected account to become active, got %s", current.State)
	}
	if current.CooldownUntil != nil {
		t.Fatalf("expected cooldown_until cleared, got %v", current.CooldownUntil)
	}
	if current.FailCount != 0 {
		t.Fatalf("expected fail_count reset to 0, got %d", current.FailCount)
	}
}

func TestClearAccountCooldown_ClearsTransientStateOnly(t *testing.T) {
	st := newTestSQLiteAccountPoolStore(t)
	ctx := context.Background()
	record := mustCreateTestAccount(t, st, "clear-cooldown", 10)
	until := time.Now().Add(time.Minute)
	if err := st.MarkAccountTransientFailure(ctx, record.ID, "temporary", until); err != nil {
		t.Fatalf("MarkAccountTransientFailure failed: %v", err)
	}
	if err := st.ClearAccountCooldown(ctx, record.ID, time.Now()); err != nil {
		t.Fatalf("ClearAccountCooldown failed: %v", err)
	}
	current, err := st.GetAccount(ctx, record.ID)
	if err != nil {
		t.Fatalf("GetAccount failed: %v", err)
	}
	if current.State != "active" || current.FailCount != 0 || current.CooldownUntil != nil || current.LastError != "" {
		t.Fatalf("expected transient state cleared, got %+v", current)
	}
}

func TestMarkAccountSuccessIfNoNewerFailure_PreservesSameMillisecondNewerCooldownState(t *testing.T) {
	st := newTestSQLiteAccountPoolStore(t)
	ctx := context.Background()

	record := mustCreateTestAccount(t, st, "same-ms-guarded", 10)
	attemptStartedAt := time.Date(2026, 3, 9, 12, 34, 56, 789123000, time.UTC)
	newerFailureAt := time.Date(2026, 3, 9, 12, 34, 56, 789456000, time.UTC)
	cooldownUntil := newerFailureAt.Add(60 * time.Second)

	if _, err := st.db.ExecContext(ctx,
		`UPDATE upstream_accounts
		 SET fail_count = 1, state = 'cooldown', cooldown_until = ?, last_error = ?, updated_at = ?
		 WHERE id = ?`,
		formatDBTime(cooldownUntil), "same millisecond failure", formatDBTime(newerFailureAt), record.ID); err != nil {
		t.Fatalf("seed newer cooldown state failed: %v", err)
	}

	updated, err := st.MarkAccountSuccessIfNoNewerFailure(ctx, record.ID, newerFailureAt.Add(2*time.Second), attemptStartedAt)
	if err != nil {
		t.Fatalf("MarkAccountSuccessIfNoNewerFailure failed: %v", err)
	}
	if updated {
		t.Fatal("expected stale success not to clear newer cooldown state in the same millisecond window")
	}

	current, err := st.GetAccount(ctx, record.ID)
	if err != nil {
		t.Fatalf("GetAccount failed: %v", err)
	}
	if current.State != "cooldown" {
		t.Fatalf("expected account to remain in cooldown, got %s", current.State)
	}
	if current.FailCount != 1 {
		t.Fatalf("expected fail_count to remain 1, got %d", current.FailCount)
	}
}

func TestMarkAccountTransientFailure_StoresMicrosecondUpdatedAt(t *testing.T) {
	st := newTestSQLiteAccountPoolStore(t)
	ctx := context.Background()

	record := mustCreateTestAccount(t, st, "precision-check", 10)
	if err := st.MarkAccountTransientFailure(ctx, record.ID, "stream failed", time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("MarkAccountTransientFailure failed: %v", err)
	}

	var updatedAt string
	if err := st.db.QueryRowContext(ctx, `SELECT CAST(updated_at AS TEXT) FROM upstream_accounts WHERE id = ?`, record.ID).Scan(&updatedAt); err != nil {
		t.Fatalf("query updated_at failed: %v", err)
	}

	pattern := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{6}Z$`)
	if !pattern.MatchString(updatedAt) {
		t.Fatalf("expected microsecond precision updated_at, got %q", updatedAt)
	}
}

func TestMarkAccountAuthFailedWithProfile_ReturnsErrorWhenAccountMissing(t *testing.T) {
	st := newTestSQLiteAccountPoolStore(t)
	ctx := context.Background()

	err := st.MarkAccountAuthFailedWithProfile(ctx, &UpstreamAccountRecord{
		ID:            999,
		ProviderType:  "chatgpt_refresh_token",
		AccountName:   "missing",
		CredentialRaw: "rt-missing",
		BaseURL:       "https://chatgpt.com",
		PlanType:      "free",
		QuotaStatus:   "auth_invalid",
	}, "oauth invalid")
	if err == nil {
		t.Fatal("expected missing account error")
	}
	if !strings.Contains(err.Error(), "账号不存在") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func newTestSQLiteAccountPoolStore(t *testing.T) *SQLiteAccountPoolStore {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	schemaPath := filepath.Join("..", "tracking", "schema.sql")
	schemaSQL, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read schema failed: %v", err)
	}
	if _, err := db.Exec(string(schemaSQL)); err != nil {
		t.Fatalf("exec schema failed: %v", err)
	}

	return NewSQLiteAccountPoolStore(db)
}

func mustCreateTestAccount(t *testing.T, st *SQLiteAccountPoolStore, name string, priority int) *UpstreamAccountRecord {
	t.Helper()

	record, err := st.CreateAccount(context.Background(), &UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   name,
		CredentialRaw: "sk-" + name,
		BaseURL:       "https://api.openai.com",
		GroupKey:      "",
		Priority:      priority,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("CreateAccount(%s) failed: %v", name, err)
	}
	return record
}
