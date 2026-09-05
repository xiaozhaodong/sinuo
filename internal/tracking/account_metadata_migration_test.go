package tracking

import (
	"strings"
	"testing"
)

func TestAccountMetadataCompatibility_SchemaMigration(t *testing.T) {
	for _, tc := range []struct {
		name          string
		hasSetting    bool
		legacySource  bool
		settingBefore bool
	}{
		{name: "旧账号默认关闭"},
		{name: "旧来源表先补列再重建", legacySource: true},
		{name: "重建保留显式开启", hasSetting: true, legacySource: true, settingBefore: true},
		{name: "重复初始化保留显式开启", hasSetting: true, settingBefore: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adapter, err := NewSQLiteAdapter(DatabaseConfig{DatabasePath: ":memory:"})
			if err != nil {
				t.Fatal(err)
			}
			if err := adapter.Open(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = adapter.Close() })
			raw, err := sqliteSchemaFS.ReadFile("schema.sql")
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(string(raw), "\n")
			var schema []string
			for _, line := range lines {
				if !tc.hasSetting && strings.Contains(line, "strip_codex_internal_metadata") {
					continue
				}
				schema = append(schema, line)
			}
			db := adapter.GetDB()
			if _, err := db.Exec(strings.Join(schema, "\n")); err != nil {
				t.Fatal(err)
			}
			if tc.legacySource {
				if _, err := db.Exec(`ALTER TABLE upstream_accounts ADD COLUMN source_id INTEGER`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec(`INSERT INTO upstream_accounts (account_name, credential_raw, fingerprint) VALUES (?, ?, ?)`, "metadata-fixture", "fixture-credential", "metadata-fixture"); err != nil {
				t.Fatal(err)
			}
			if tc.hasSetting {
				if _, err := db.Exec(`UPDATE upstream_accounts SET strip_codex_internal_metadata = ?`, tc.settingBefore); err != nil {
					t.Fatal(err)
				}
			}
			for pass := 0; pass < 2; pass++ {
				if err := adapter.InitSchema(); err != nil {
					t.Fatalf("migration %d: %v", pass, err)
				}
				var got bool
				if err := db.QueryRow(`SELECT strip_codex_internal_metadata FROM upstream_accounts WHERE fingerprint = ?`, "metadata-fixture").Scan(&got); err != nil {
					t.Fatal(err)
				}
				if got != tc.settingBefore {
					t.Fatalf("migration %d: setting = %v, want %v", pass, got, tc.settingBefore)
				}
				var oldColumns int
				if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('upstream_accounts') WHERE name = 'source_id'`).Scan(&oldColumns); err != nil || oldColumns != 0 {
					t.Fatalf("legacy source_id still present: count=%d err=%v", oldColumns, err)
				}
			}
		})
	}
}
