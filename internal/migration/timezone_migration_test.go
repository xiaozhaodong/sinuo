package migration

import (
	"context"
	"database/sql"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	timezonepolicy "sinuo/internal/timezone"
)

func TestCoordinatorTimezoneMigrationNormalizesHistoryAndIsIdempotent(t *testing.T) {
	db := openLegacyFixtureDB(t)
	defer db.Close()
	var endpointCreatedRaw, requestCreatedRaw, privacyCreatedRaw string
	if err := db.QueryRow(`SELECT CAST(created_at AS TEXT) FROM endpoints WHERE id=7`).Scan(&endpointCreatedRaw); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT CAST(created_at AS TEXT) FROM request_logs WHERE request_id='req-fixture-claude'`).Scan(&requestCreatedRaw); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT CAST(created_at AS TEXT) FROM privacy_rules WHERE id=1`).Scan(&privacyCreatedRaw); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(`
		UPDATE endpoint_runtime_states SET cooldown_until='2099-08-03T04:00:00.000000Z';
		INSERT INTO request_logs (request_id, path, start_time, end_time, status, input_tokens, output_tokens, total_cost_usd)
		VALUES ('req-naive-shanghai', '/v1/messages', '2026-08-02 10:00:00', '2026-08-02 10:00:01', 'completed', 7, 3, 0.25);
		CREATE TABLE upstream_accounts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			account_name TEXT NOT NULL,
			cooldown_until DATETIME,
			last_success_at DATETIME,
			quota_5h_reset_at DATETIME,
			quota_weekly_reset_at DATETIME,
			quota_refreshed_at DATETIME,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		INSERT INTO upstream_accounts (
			account_name, cooldown_until, last_success_at, quota_5h_reset_at,
			quota_weekly_reset_at, quota_refreshed_at, created_at, updated_at
		) VALUES ('legacy-account', '2026-08-04T12:00:00Z', NULL, NULL, NULL, NULL,
			'2026-08-04T12:00:00Z', '2026-08-04T12:00:00Z');
	`); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	raw, err := os.ReadFile(filepath.Join("testdata", "legacy_config.yml"))
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), "type: yaml", "type: sqlite", 1))
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	coordinator := &Coordinator{
		DB: db, ConfigPath: configPath, DataDir: dir, DatabaseExisted: true,
		Now: func() time.Time { return time.Date(2026, 8, 4, 1, 2, 3, 0, time.UTC) },
	}
	status, err := coordinator.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.MigrationID != TimezoneUTCMigrationID || status.State != StartupReady || status.Phase != PhaseCompleted {
		t.Fatalf("timezone migration status = %+v", status)
	}

	assertStoredTime(t, db, `SELECT CAST(start_time AS TEXT) FROM request_logs WHERE request_id='req-naive-shanghai'`, "2026-08-02T02:00:00.000000Z")
	assertStoredTime(t, db, `SELECT CAST(start_time AS TEXT) FROM request_logs WHERE request_id='req-fixture-claude'`, "2026-08-01T02:00:00.000000Z")
	assertStoredTime(t, db, `SELECT CAST(cooldown_until AS TEXT) FROM endpoint_runtime_states WHERE endpoint_id=7 AND scope='messages'`, "2099-08-03T04:00:00.000000Z")
	assertStoredTime(t, db, `SELECT CAST(cooldown_until AS TEXT) FROM upstream_accounts WHERE account_name='legacy-account'`, "2026-08-04T04:00:00.000000Z")
	assertStoredTime(t, db, `SELECT CAST(created_at AS TEXT) FROM endpoints WHERE id=7`, canonicalNaiveUTC(t, endpointCreatedRaw))
	assertStoredTime(t, db, `SELECT CAST(created_at AS TEXT) FROM request_logs WHERE request_id='req-fixture-claude'`, canonicalNaiveUTC(t, requestCreatedRaw))
	assertStoredTime(t, db, `SELECT CAST(created_at AS TEXT) FROM privacy_rules WHERE id=1`, canonicalNaiveUTC(t, privacyCreatedRaw))

	var summaryRows int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM usage_summary`).Scan(&summaryRows); err != nil {
		t.Fatal(err)
	}
	if summaryRows != 0 {
		t.Fatalf("timezone migration must leave recent summary cache empty, rows=%d", summaryRows)
	}
	var foreignKeys int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1", foreignKeys)
	}

	backupEntries, err := os.ReadDir(filepath.Join(dir, "migration-backups"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := coordinator.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.State != StartupReady || second.Phase != PhaseCompleted {
		t.Fatalf("second timezone migration status = %+v", second)
	}
	secondBackupEntries, err := os.ReadDir(filepath.Join(dir, "migration-backups"))
	if err != nil {
		t.Fatal(err)
	}
	if len(secondBackupEntries) != len(backupEntries) {
		t.Fatalf("idempotent run created another backup: before=%d after=%d", len(backupEntries), len(secondBackupEntries))
	}
}

func TestTimezoneMigrationRejectsUnknownHistoricalTimeBeforeBackup(t *testing.T) {
	db := openLegacyFixtureDB(t)
	defer db.Close()
	if _, err := db.Exec(`UPDATE request_logs SET start_time='not-a-time' WHERE request_id='req-fixture-claude'`); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("timezone: Asia/Shanghai\nendpoints_storage:\n  type: sqlite\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	coordinator := &Coordinator{DB: db, ConfigPath: configPath, DataDir: dir, DatabaseExisted: true}
	status, err := coordinator.runTimezoneUTCMigration(context.Background(), &LegacyConfig{
		SourceMode: SourceModeSQLite, GlobalTimezone: "Asia/Shanghai",
	}, Status{})
	if err == nil {
		t.Fatal("expected unknown historical time to fail migration")
	}
	message := err.Error()
	for _, expected := range []string{"table=request_logs", "field=start_time", "row_id=1", "format=unknown"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("migration error %q does not contain %q", message, expected)
		}
	}
	if status.State != StartupMigrationFailed || !status.RetryAllowed {
		t.Fatalf("failed status = %+v", status)
	}
	if _, err := os.Stat(filepath.Join(dir, "migration-backups")); !os.IsNotExist(err) {
		t.Fatalf("invalid data must fail before backup creation, stat error=%v", err)
	}
}

func TestCompletedTimezoneMigrationUsesSchemaFastPath(t *testing.T) {
	db := openLegacyFixtureDB(t)
	defer db.Close()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("timezone: Asia/Shanghai\nendpoints_storage:\n  type: sqlite\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	coordinator := &Coordinator{DB: db, ConfigPath: configPath, DataDir: dir, DatabaseExisted: true}
	if _, err := coordinator.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE request_logs SET start_time='not-a-time' WHERE request_id='req-fixture-claude'`); err != nil {
		t.Fatal(err)
	}
	schemaNeeded, err := needsTimezoneUTCSchemaMigration(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if schemaNeeded {
		t.Fatal("completed UTC schema must stay on the fast path")
	}
	dataNeeded, err := needsTimezoneUTCDataMigration(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if !dataNeeded {
		t.Fatal("full diagnostic scan must still detect noncanonical data")
	}
	status, err := coordinator.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StartupReady || status.Phase != PhaseCompleted {
		t.Fatalf("completed migration fast-path status = %+v", status)
	}
}

func TestParseHistoricalTimeRules(t *testing.T) {
	policy, err := timezonepolicy.New("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		raw  string
		rule historicalTimeRule
		want string
	}{
		{name: "configured wall", raw: "2026-08-04 10:00:00", rule: historicalConfiguredWall, want: "2026-08-04T02:00:00.000000Z"},
		{name: "explicit offset", raw: "2026-08-04T10:00:00+08:00", rule: historicalConfiguredWall, want: "2026-08-04T02:00:00.000000Z"},
		{name: "runtime UTC wall", raw: "2026-08-04 10:00:00", rule: historicalUTCWall, want: "2026-08-04T10:00:00.000000Z"},
		{name: "account legacy Z", raw: "2026-08-04T10:00:00Z", rule: historicalAccountCompatibility, want: "2026-08-04T02:00:00.000000Z"},
		{name: "go time string", raw: "2026-04-30 00:49:14.502607 +0800 CST", rule: historicalConfiguredWall, want: "2026-04-29T16:49:14.502607Z"},
		{name: "go time string without fractional", raw: "2026-08-04 10:00:00 +0800 CST", rule: historicalUTCWall, want: "2026-08-04T02:00:00.000000Z"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parsed, _, err := parseHistoricalTime(tc.raw, policy, tc.rule)
			if err != nil {
				t.Fatal(err)
			}
			if got := timezonepolicy.FormatStorage(parsed); got != tc.want {
				t.Fatalf("parsed time = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestInspectTimezoneDataUsesCompletedYAMLFlattenLedgerForEndpointAuditTimes(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		CREATE TABLE endpoints (
			id INTEGER PRIMARY KEY,
			created_at DATETIME DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now', 'localtime') || '+08:00'),
			updated_at DATETIME DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now', 'localtime') || '+08:00')
		);
		INSERT INTO endpoints (id, created_at, updated_at)
		VALUES (1, '2026-08-03 09:10:11', '2026-08-03 09:10:11');
		CREATE TABLE app_schema_migrations (
			migration_id TEXT PRIMARY KEY,
			phase TEXT NOT NULL,
			source_mode TEXT NOT NULL,
			backup_dir TEXT NOT NULL,
			backup_manifest_sha256 TEXT NOT NULL
		);
		INSERT INTO app_schema_migrations (
			migration_id, phase, source_mode, backup_dir, backup_manifest_sha256
		) VALUES ('20260803_claude_endpoint_flatten_v1', 'completed', 'yaml', '/tmp/fixture', 'fixture');
	`); err != nil {
		t.Fatal(err)
	}

	preflight, err := inspectTimezoneData(context.Background(), db, "Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	var createdUpdate timezoneValueUpdate
	for _, update := range preflight.Updates {
		if update.table == "endpoints" && update.field == "created_at" && update.rowID == 1 {
			createdUpdate = update
			break
		}
	}
	if createdUpdate.canonical != "2026-08-03T09:10:11.000000Z" {
		t.Fatalf("flatten-created endpoint audit time = %+v", createdUpdate)
	}
	if preflight.Formats["endpoints.created_at"]["endpoint_flatten_current_timestamp"] != 1 {
		t.Fatalf("endpoint created_at formats = %+v", preflight.Formats["endpoints.created_at"])
	}
}

func TestValidateMigrationDiskSpace(t *testing.T) {
	if err := validateMigrationDiskSpace(100, 300); err != nil {
		t.Fatal(err)
	}
	if err := validateMigrationDiskSpace(100, 299); err == nil {
		t.Fatal("expected insufficient disk space error")
	}
	if err := validateMigrationDiskSpace(math.MaxUint64, math.MaxUint64); err == nil {
		t.Fatal("expected overflow protection error")
	}
}

func assertStoredTime(t *testing.T, queryer interface{ QueryRow(string, ...any) *sql.Row }, query, want string) {
	t.Helper()
	var got string
	if err := queryer.QueryRow(query).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("stored time = %q, want %q", got, want)
	}
}

func canonicalNaiveUTC(t *testing.T, raw string) string {
	t.Helper()
	parsed, ok := parseNaiveUTCWall(raw)
	if !ok {
		t.Fatalf("fixture time %q is not a naive UTC wall time", raw)
	}
	return timezonepolicy.FormatStorage(parsed)
}
