package migration

import (
	"context"
	"database/sql"
	"testing"
)

// TestRequestHistoryMigrationPreservesLifecycleColumns 源表已有详情列且带值 → 重建后值保留（防重跑丢数据）。
func TestRequestHistoryMigrationPreservesLifecycleColumns(t *testing.T) {
	db := openLegacyFixtureDB(t)
	defer db.Close()

	// 模拟已升级、已产生生命周期数据的库。
	for _, statement := range []string{
		`ALTER TABLE request_logs ADD COLUMN upstream_write_ms INTEGER`,
		`ALTER TABLE request_logs ADD COLUMN schedule_snapshot_json TEXT`,
		`ALTER TABLE request_logs ADD COLUMN privacy_scan_json TEXT`,
		`ALTER TABLE request_logs ADD COLUMN input_composition_json TEXT`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE request_logs SET
		upstream_write_ms = 321,
		schedule_snapshot_json = '{"request_id":"req-fixture-codex","final_outcome":"success"}',
		privacy_scan_json = '{"action":"redact","hit_count":1}',
		input_composition_json = '{"version":1,"items":[{"group":"chat","name":"用户消息","estimate":2}]}'
		WHERE request_id = 'req-fixture-codex'`); err != nil {
		t.Fatal(err)
	}

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

	var upstreamWriteMs sql.NullInt64
	var scheduleJSON, privacyJSON, compositionJSON sql.NullString
	if err := db.QueryRow(`SELECT upstream_write_ms, schedule_snapshot_json, privacy_scan_json, input_composition_json
		FROM request_logs WHERE request_id = 'req-fixture-codex'`).Scan(
		&upstreamWriteMs, &scheduleJSON, &privacyJSON, &compositionJSON,
	); err != nil {
		t.Fatal(err)
	}
	if !upstreamWriteMs.Valid || upstreamWriteMs.Int64 != 321 {
		t.Fatalf("upstream_write_ms = %v, want 321", upstreamWriteMs)
	}
	if !scheduleJSON.Valid || scheduleJSON.String == "" {
		t.Fatalf("schedule_snapshot_json = %v, want preserved value", scheduleJSON)
	}
	if !privacyJSON.Valid || privacyJSON.String == "" {
		t.Fatalf("privacy_scan_json = %v, want preserved value", privacyJSON)
	}
	if !compositionJSON.Valid || compositionJSON.String == "" {
		t.Fatalf("input_composition_json = %v, want preserved value", compositionJSON)
	}
}

// TestRequestHistoryMigrationLifecycleColumnsFallbackNull legacy 源缺列 → 重建后新列回退 NULL。
func TestRequestHistoryMigrationLifecycleColumnsFallbackNull(t *testing.T) {
	db := openLegacyFixtureDB(t)
	defer db.Close()

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

	var upstreamWriteMs sql.NullInt64
	var scheduleJSON, privacyJSON, compositionJSON sql.NullString
	if err := db.QueryRow(`SELECT upstream_write_ms, schedule_snapshot_json, privacy_scan_json, input_composition_json
		FROM request_logs WHERE request_id = 'req-fixture-claude'`).Scan(
		&upstreamWriteMs, &scheduleJSON, &privacyJSON, &compositionJSON,
	); err != nil {
		t.Fatal(err)
	}
	if upstreamWriteMs.Valid || scheduleJSON.Valid || privacyJSON.Valid || compositionJSON.Valid {
		t.Fatalf("expected NULL fallbacks, got %v / %v / %v / %v", upstreamWriteMs, scheduleJSON, privacyJSON, compositionJSON)
	}
}
