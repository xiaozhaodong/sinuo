package migration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sinuo/internal/tracking"
)

func TestUsageSummaryUTCSchemaIncludesRequestKindUniqueKey(t *testing.T) {
	db := openLegacyFixtureDB(t)
	defer db.Close()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := rebuildUsageSummaryUTC(context.Background(), tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	var schema string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='usage_summary'`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	compact := strings.Join(strings.Fields(strings.ToLower(schema)), " ")
	if !strings.Contains(compact, "request_kind text not null") ||
		!strings.Contains(compact, "request_family, request_kind, upstream_type") {
		t.Fatalf("usage_summary schema missing kind key: %s", schema)
	}
	insert := `INSERT INTO usage_summary (
		timezone_name, date, model_name, request_family, request_kind, upstream_type, upstream_name, upstream_id
	) VALUES ('UTC', '2026-09-10', 'synthetic-model', 'codex', ?, 'subscription', 'chatgpt-subscription', 0)`
	if _, err := db.Exec(insert, "generation"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(insert, "compaction"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(insert, "generation"); err == nil {
		t.Fatal("duplicate summary kind key unexpectedly accepted")
	}
	var indexCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_usage_summary_kind'`).Scan(&indexCount); err != nil {
		t.Fatal(err)
	}
	if indexCount != 1 {
		t.Fatalf("kind index count = %d", indexCount)
	}
}

// 覆盖真实启动顺序：旧格式迁移先完成，tracking 再执行 schema 和增量列迁移。
// 已存在日缓存时，后续时区表重建也必须保留失效触发器。
func TestUsageDailyLegacyStartupAndTimezoneRebuild(t *testing.T) {
	db := openLegacyFixtureDB(t)
	defer db.Close()
	var sequence int
	var databaseName, databasePath string
	if err := db.QueryRow("PRAGMA database_list").Scan(&sequence, &databaseName, &databasePath); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	raw, err := os.ReadFile(filepath.Join("testdata", "legacy_config.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(strings.Replace(string(raw), "type: yaml", "type: sqlite", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	coordinator := &Coordinator{DB: db, ConfigPath: configPath, DataDir: dir, DatabaseExisted: true,
		Now: func() time.Time { return time.Date(2026, 8, 4, 1, 2, 3, 0, time.UTC) }}
	if _, err := coordinator.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	adapter, err := tracking.NewSQLiteAdapter(tracking.DatabaseConfig{DatabasePath: databasePath})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Open(); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	for i := 0; i < 2; i++ {
		if err := adapter.InitSchema(); err != nil {
			t.Fatal(err)
		}
	}
	if needed, err := needsTimezoneUTCMigration(context.Background(), db); err != nil || needed {
		t.Fatalf("日缓存 schema 不应触发额外时区迁移: %v / %v", needed, err)
	}
	if err := validateTargetSchema(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO usage_daily(timezone_name,date,start_utc,end_utc)
		VALUES('UTC','2026-08-01','2026-08-01T00:00:00.000000Z','2026-08-02T00:00:00.000000Z')`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := rebuildTablePreservingSchema(context.Background(), tx, "request_logs"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var triggers int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name LIKE 'usage_daily_invalidate_%'`).Scan(&triggers); err != nil || triggers != 3 {
		t.Fatalf("重建后触发器数: %d / %v", triggers, err)
	}
	if _, err := db.Exec(`UPDATE request_logs SET input_tokens=input_tokens+1 WHERE request_id='req-fixture-claude'`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM usage_daily").Scan(&count); err != nil || count != 0 {
		t.Fatalf("重建后失效触发器未生效: %d / %v", count, err)
	}
}
