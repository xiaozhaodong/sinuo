package migration

import (
	"context"
	"strings"
	"testing"
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
