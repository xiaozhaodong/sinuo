package tracking

import (
	"database/sql"
	"strings"
	"testing"
)

func TestSoftFailurePolicy_旧库迁移与来源表重建(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		custom, legacySource bool
	}{
		{"缺列继承默认", false, false},
		{"保留自定义", true, false},
		{"旧来源重建继承默认", false, true},
		{"旧来源重建保留自定义", true, true},
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
			var lines []string
			for _, line := range strings.Split(string(raw), "\n") {
				if !tc.custom && strings.Contains(line, "soft_failure_") {
					continue
				}
				lines = append(lines, line)
			}
			db := adapter.GetDB()
			if _, err := db.Exec(strings.Join(lines, "\n")); err != nil {
				t.Fatal(err)
			}
			if tc.legacySource {
				if _, err := db.Exec(`ALTER TABLE upstream_accounts ADD COLUMN source_id INTEGER`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec(`INSERT INTO upstream_accounts (account_name, credential_raw, fingerprint) VALUES (?, ?, ?)`, "policy", "fixture", "policy"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO endpoints (name, url) VALUES (?, ?)`, "policy", "https://example.test"); err != nil {
				t.Fatal(err)
			}
			if tc.custom {
				for _, table := range []string{"upstream_accounts", "endpoints"} {
					if _, err := db.Exec(`UPDATE `+table+` SET soft_failure_window_minutes = ?, soft_failure_threshold = ?`, 10, 5); err != nil {
						t.Fatal(err)
					}
				}
			}
			for pass := 0; pass < 2; pass++ {
				if err := adapter.InitSchema(); err != nil {
					t.Fatal(err)
				}
				for _, table := range []string{"upstream_accounts", "endpoints"} {
					var window, threshold sql.NullInt64
					if err := db.QueryRow(`SELECT soft_failure_window_minutes, soft_failure_threshold FROM `+table).Scan(&window, &threshold); err != nil {
						t.Fatal(err)
					}
					if window.Valid != tc.custom || threshold.Valid != tc.custom || tc.custom && (window.Int64 != 10 || threshold.Int64 != 5) {
						t.Fatalf("%s 第 %d 轮策略不符: %v / %v", table, pass, window, threshold)
					}
				}
				var integrity string
				if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
					t.Fatalf("完整性错误: %s %v", integrity, err)
				}
			}
		})
	}
}
