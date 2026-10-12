package migration

import (
	"context"
	"database/sql"
	"testing"
)

func TestRequestHistoryMigration_TimingColumnsPreservedOrNull(t *testing.T) {
	for _, existing := range []bool{true, false} {
		t.Run(map[bool]string{true: "preserve", false: "legacy_null"}[existing], func(t *testing.T) {
			db := openLegacyFixtureDB(t)
			defer db.Close()
			if existing {
				for _, statement := range []string{
					`ALTER TABLE request_logs ADD COLUMN upstream_dispatch_ms INTEGER`,
					`ALTER TABLE request_logs ADD COLUMN upstream_conn_ms INTEGER`,
				} {
					if _, err := db.Exec(statement); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := db.Exec(`UPDATE request_logs SET upstream_dispatch_ms = ?, upstream_conn_ms = ? WHERE request_id = ?`, 100, 200, "req-fixture-codex"); err != nil {
					t.Fatal(err)
				}
			}
			// 时区重建再次执行也必须保留偏移，不能回填旧行。
			for i := 0; i < 2; i++ {
				tx, err := db.BeginTx(context.Background(), nil)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := migrateRequestHistory(context.Background(), tx); err != nil {
					_ = tx.Rollback()
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
				var dispatch, conn sql.NullInt64
				if err := db.QueryRow(`SELECT upstream_dispatch_ms, upstream_conn_ms FROM request_logs WHERE request_id = ?`, "req-fixture-codex").Scan(&dispatch, &conn); err != nil {
					t.Fatal(err)
				}
				if existing {
					if !dispatch.Valid || !conn.Valid || dispatch.Int64 != 100 || conn.Int64 != 200 {
						t.Fatalf("重建丢失计时偏移：%v/%v", dispatch, conn)
					}
				} else if dispatch.Valid || conn.Valid {
					t.Fatalf("旧行应保持 NULL：%v/%v", dispatch, conn)
				}
			}
		})
	}
}
