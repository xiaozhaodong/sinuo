package migration

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigrateDatabaseFromYAMLFixture(t *testing.T) {
	db := openLegacyFixtureDB(t)
	defer db.Close()
	legacy, err := LoadLegacyConfig(filepath.Join("testdata", "legacy_config.yml"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := migrateDatabase(context.Background(), db, legacy, &BackupResult{Directory: "/fixture/backup", ManifestSHA256: "fixture-sha"})
	if err != nil {
		t.Fatal(err)
	}
	if result.EndpointCountBefore != 2 || result.EndpointCountAfter != 10 || result.DerivedRecordCount != 5 || result.RequestLogCount != 4 {
		t.Fatalf("migration result = %+v", result)
	}
	if err := validateTargetSchema(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	assertColumnAbsent(t, db, "endpoints", "channel")
	assertColumnAbsent(t, db, "endpoints", "enabled")
	assertColumnAbsent(t, db, "request_logs", "channel")
	assertColumnAbsent(t, db, "request_logs", "group_name")

	var availability, failover int
	if err := db.QueryRow(`SELECT availability_enabled, failover_enabled FROM endpoints WHERE name = 'multi-auth · token-a · api-b'`).Scan(&availability, &failover); err != nil {
		t.Fatal(err)
	}
	if availability != 0 || failover != 0 {
		t.Fatalf("derived endpoint state = availability:%d failover:%d", availability, failover)
	}
	var runtimeStates int
	if err := db.QueryRow(`SELECT COUNT(*) FROM endpoint_runtime_states`).Scan(&runtimeStates); err != nil {
		t.Fatal(err)
	}
	if runtimeStates != 0 {
		t.Fatalf("YAML authoritative migration must discard SQLite-only runtime states, got %d", runtimeStates)
	}
	var scopeJSON string
	if err := db.QueryRow(`SELECT scope_json FROM privacy_rules WHERE name = 'fixture-endpoint-scope'`).Scan(&scopeJSON); err != nil {
		t.Fatal(err)
	}
	if scopeJSON != `{"endpoint_names":["legacy-primary"]}` {
		t.Fatalf("unrelated privacy scope changed = %s", scopeJSON)
	}
	assertRequestFamily(t, db, "req-fixture-claude", "claude", "endpoint", "legacy-primary")
	assertRequestFamily(t, db, "req-fixture-codex", "codex", "account", "codex-fixture")
	assertRequestFamily(t, db, "req-fixture-image", "image", "endpoint", "image-fixture")
	assertRequestFamily(t, db, "req-fixture-other", "other", "", "")
	var summaryCount int64
	if err := db.QueryRow(`SELECT SUM(request_count) FROM usage_summary`).Scan(&summaryCount); err != nil {
		t.Fatal(err)
	}
	if summaryCount != 4 {
		t.Fatalf("summary request count = %d", summaryCount)
	}
}

func TestMigrateDatabaseSQLiteAuthorityPreservesIDsAndRuntimeState(t *testing.T) {
	db := openLegacyFixtureDB(t)
	defer db.Close()
	legacy := &LegacyConfig{SourceMode: SourceModeSQLite}
	result, err := migrateDatabase(context.Background(), db, legacy, &BackupResult{Directory: "/fixture/backup", ManifestSHA256: "fixture-sha"})
	if err != nil {
		t.Fatal(err)
	}
	if result.EndpointCountBefore != 2 || result.EndpointCountAfter != 2 || result.DerivedRecordCount != 0 {
		t.Fatalf("migration result = %+v", result)
	}
	var endpointID int64
	if err := db.QueryRow(`SELECT id FROM endpoints WHERE name = 'legacy-primary'`).Scan(&endpointID); err != nil {
		t.Fatal(err)
	}
	if endpointID != 7 {
		t.Fatalf("legacy-primary ID = %d, want 7", endpointID)
	}
	var revision int
	if err := db.QueryRow(`SELECT revision FROM endpoint_runtime_states WHERE endpoint_id = 7 AND scope = 'messages'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != 42 {
		t.Fatalf("runtime revision = %d", revision)
	}
	var mode, target string
	if err := db.QueryRow(`SELECT
		MAX(CASE WHEN key='mode' THEN value ELSE '' END),
		MAX(CASE WHEN key='endpoint_name' THEN value ELSE '' END)
		FROM app_settings WHERE category='claude_routing'`).Scan(&mode, &target); err != nil {
		t.Fatal(err)
	}
	if mode != "manual_fixed" || target != "legacy-primary" {
		t.Fatalf("routing state = %q/%q", mode, target)
	}
}

func openLegacyFixtureDB(t *testing.T) *sql.DB {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "legacy_schema.sql"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy.db")+"?_foreign_keys=1")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(string(raw)); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db
}

func assertColumnAbsent(t *testing.T, db *sql.DB, table, column string) {
	t.Helper()
	has, err := columnExists(context.Background(), db, table, column)
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Fatalf("%s.%s still exists", table, column)
	}
}

func assertRequestFamily(t *testing.T, db *sql.DB, requestID, family, upstreamType, upstreamName string) {
	t.Helper()
	var gotFamily, gotType, gotName string
	if err := db.QueryRow(`SELECT request_family, upstream_type, upstream_name FROM request_logs WHERE request_id = ?`, requestID).Scan(&gotFamily, &gotType, &gotName); err != nil {
		t.Fatal(err)
	}
	if gotFamily != family || gotType != upstreamType || gotName != upstreamName {
		t.Fatalf("request %s = %q/%q/%q, want %q/%q/%q", requestID, gotFamily, gotType, gotName, family, upstreamType, upstreamName)
	}
}

func TestSoftFailurePolicy_YAML校验及端点重建保留(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		valid       bool
	}{
		{"整数", "10", true},
		{"小数", "1.5", false},
		{"零值", "0", false},
		{"缺少窗口", "null", false},
		{"字符串", "'10'", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			raw := "endpoints:\n  - name: policy\n    url: https://example.test\n    soft_failure_window_minutes: " + tc.value + "\n    soft_failure_threshold: 5\n"
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			legacy, err := LoadLegacyConfig(path)
			if (err == nil) != tc.valid {
				t.Fatalf("YAML 校验错误: %v", err)
			}
			if !tc.valid {
				return
			}
			flattened, err := FlattenLegacyEndpoints(legacy)
			if err != nil {
				t.Fatal(err)
			}
			db := openLegacyFixtureDB(t)
			defer db.Close()
			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if err := rebuildEndpoints(context.Background(), tx, flattened); err != nil {
				t.Fatal(err)
			}
			var window, threshold int
			if err := tx.QueryRow("SELECT soft_failure_window_minutes, soft_failure_threshold FROM endpoints WHERE name = ?", "policy").Scan(&window, &threshold); err != nil {
				t.Fatal(err)
			}
			if window != 10 || threshold != 5 {
				t.Fatalf("迁移丢失策略: %d / %d", window, threshold)
			}
		})
	}
}
