package tracking

import (
	"context"
	"database/sql"
	"strings"
)

// 旧库的正数约束不允许保存分项模式。仅替换该列，保留端点 ID、子表和自增序列。
func migrateEndpointMultiplier(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var definition string
	if err := tx.QueryRowContext(ctx, "SELECT sql FROM sqlite_schema WHERE name='endpoints' AND type='table'").Scan(&definition); err != nil {
		return err
	}
	if !strings.Contains(strings.Join(strings.Fields(strings.ToLower(definition)), ""), "check(cost_multiplier>0)") {
		return tx.Commit()
	}
	var timestampTrigger string
	err = tx.QueryRowContext(ctx, "SELECT sql FROM sqlite_schema WHERE type='trigger' AND name='update_endpoints_timestamp'").Scan(&timestampTrigger)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if timestampTrigger != "" {
		if _, err := tx.ExecContext(ctx, "DROP TRIGGER update_endpoints_timestamp"); err != nil {
			return err
		}
	}
	for _, q := range []string{
		"ALTER TABLE endpoints ADD COLUMN new_cost_multiplier REAL NOT NULL DEFAULT 1 CHECK(new_cost_multiplier >= 0)",
		"UPDATE endpoints SET new_cost_multiplier=cost_multiplier",
		"ALTER TABLE endpoints DROP COLUMN cost_multiplier",
		"ALTER TABLE endpoints RENAME COLUMN new_cost_multiplier TO cost_multiplier",
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	if timestampTrigger != "" {
		if _, err := tx.ExecContext(ctx, timestampTrigger); err != nil {
			return err
		}
	}
	return tx.Commit()
}
