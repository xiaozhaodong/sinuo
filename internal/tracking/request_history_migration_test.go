package tracking

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRequestHistoryMigration_UpgradeRetryAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.db")
	core, err := OpenCoreDatabase(DatabaseConfig{Type: "sqlite", DatabasePath: path})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, core.Close()) })
	db := core.DB()
	legacy, err := os.ReadFile("../../tests/testdata/pre_p2_tracking_schema.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(legacy))
	require.NoError(t, err)
	_, err = db.Exec(`
		INSERT INTO request_logs (request_id, start_time, duration_ms, first_token_ms, completion_ms) VALUES
			('legacy', '2026-01-01T00:00:00.000000Z', 100, 40, NULL),
			('clamp', '2026-01-01T00:00:00.000000Z', 10, 20, NULL),
			('known', '2026-01-01T00:00:00.000000Z', 100, 40, 7),
			('unknown', '2026-01-01T00:00:00.000000Z', 100, NULL, NULL);
		CREATE TABLE request_id_reservations (request_id TEXT PRIMARY KEY NOT NULL) WITHOUT ROWID;
		INSERT INTO request_id_reservations VALUES ('issued-without-archive'), ('known');
		CREATE TRIGGER reject_seed BEFORE INSERT ON request_id_reservations
		WHEN NEW.request_id = 'legacy' BEGIN SELECT RAISE(ABORT, 'seed failed'); END;`)
	require.NoError(t, err)

	// 第二项迁移失败时，先前的耗时回填和两个标记一起回滚。
	require.ErrorContains(t, core.InitSchema(), "seed failed")
	var count int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM tracking_data_migrations").Scan(&count))
	require.Zero(t, count)
	require.NoError(t, db.QueryRow("SELECT COUNT(completion_ms) FROM request_logs").Scan(&count))
	require.Equal(t, 1, count)
	_, err = db.Exec("DROP TRIGGER reject_seed")
	require.NoError(t, err)
	require.NoError(t, core.InitSchema())
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM request_logs WHERE
		(request_id = 'legacy' AND completion_ms = 60) OR (request_id = 'clamp' AND completion_ms = 0)
		OR (request_id = 'known' AND completion_ms = 7) OR (request_id = 'unknown' AND completion_ms IS NULL)`).Scan(&count))
	require.Equal(t, 4, count)
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM request_id_reservations").Scan(&count))
	require.Equal(t, 5, count)

	// 模拟迁移完成后导入的归档。给重复占号设陷阱，重启时若重扫历史会直接报错。
	_, err = db.Exec(`
		INSERT INTO request_logs (request_id, start_time, duration_ms, first_token_ms)
		VALUES ('imported', '2026-01-02T00:00:00.000000Z', 80, 20);
		CREATE TRIGGER reject_reseed BEFORE INSERT ON request_id_reservations
		WHEN NEW.request_id = 'legacy' BEGIN SELECT RAISE(ABORT, '重复登记历史 ID'); END;`)
	require.NoError(t, err)
	require.NoError(t, core.Close())
	reopened := openIDTestCore(t, path)
	require.NoError(t, reopened.DB().QueryRow("SELECT COUNT(completion_ms) FROM request_logs WHERE request_id = 'imported'").Scan(&count))
	require.Zero(t, count, "重启不能改写迁移完成后缺失的实测耗时")
	_, err = reopened.DB().Exec("DELETE FROM request_logs")
	require.NoError(t, err)
	reopened.requestIDs.candidate = requestIDCandidates("imported", "issued-without-archive", "available")
	id, err := reopened.NewRequestID(context.Background())
	require.NoError(t, err)
	require.Equal(t, "available", id)
}
