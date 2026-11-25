package tracking

import (
	"context"
	"fmt"
)

// 升级旧库时保留原来的耗时推导口径，以及归档和未归档请求的所有占号记录。
// SQLite 写事务串行执行；任何回填失败都会连同标记回滚，后续启动可以重试。
func (s *SQLiteAdapter) migrateRequestHistory(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, migration := range []struct{ name, query string }{
		{"backfill_completion_ms_v1", `
			UPDATE request_logs SET completion_ms = MAX(0, duration_ms - first_token_ms)
			WHERE completion_ms IS NULL AND duration_ms IS NOT NULL AND first_token_ms IS NOT NULL`},
		{"reserve_historical_request_ids_v1", `
			INSERT INTO request_id_reservations (request_id)
			SELECT request_id FROM request_logs WHERE true
			ON CONFLICT(request_id) DO NOTHING`},
	} {
		marker, err := tx.ExecContext(ctx, `INSERT INTO tracking_data_migrations (name)
			VALUES (?) ON CONFLICT(name) DO NOTHING`, migration.name)
		if err != nil {
			return fmt.Errorf("登记迁移 %s: %w", migration.name, err)
		}
		inserted, err := marker.RowsAffected()
		if err != nil {
			return fmt.Errorf("检查迁移 %s: %w", migration.name, err)
		}
		if inserted == 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, migration.query); err != nil {
			return fmt.Errorf("执行迁移 %s: %w", migration.name, err)
		}
	}
	return tx.Commit()
}
