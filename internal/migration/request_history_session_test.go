package migration

import (
	"context"
	"testing"
)

func TestRequestHistoryMigrationSessionID(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "旧表缺列", true: "保留现有会话"}[existing], func(t *testing.T) {
			db := openLegacyFixtureDB(t)
			defer db.Close()
			want := ""
			if existing {
				want = "synthetic-session"
				if _, err := db.Exec(`ALTER TABLE request_logs ADD COLUMN session_id TEXT DEFAULT ''`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`UPDATE request_logs SET session_id = ? WHERE request_id = 'req-fixture-codex'`, want); err != nil {
					t.Fatal(err)
				}
			}
			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := migrateRequestHistory(context.Background(), tx); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			var session, columns string
			if err := db.QueryRow(`SELECT session_id FROM request_logs WHERE request_id = 'req-fixture-codex'`).Scan(&session); err != nil || session != want {
				t.Fatalf("迁移会话值不一致: got=%q err=%v", session, err)
			}
			if err := db.QueryRow(`SELECT group_concat(name, ',') FROM pragma_index_info('idx_request_logs_session_time')`).Scan(&columns); err != nil || columns != "session_id,start_time" {
				t.Fatalf("迁移后缺少会话索引: %q err=%v", columns, err)
			}
		})
	}
}
