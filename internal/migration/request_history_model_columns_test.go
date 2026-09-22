package migration

import (
	"context"
	"testing"
)

// TestRequestHistoryMigrationModelConsistencyColumns legacy 源缺列 → 重建后 5 列存在且回退空串；已有值保留。
func TestRequestHistoryMigrationModelConsistencyColumns(t *testing.T) {
	db := openLegacyFixtureDB(t)
	defer db.Close()

	if _, err := db.Exec(`ALTER TABLE request_logs ADD COLUMN request_model TEXT DEFAULT ''`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE request_logs SET request_model = 'gpt-5.4' WHERE request_id = 'req-fixture-codex'`); err != nil {
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

	var requestModel, upstreamModel, responseModel, requestReasoning, responseReasoning string
	if err := db.QueryRow(`SELECT request_model, upstream_model, response_model, request_reasoning, response_reasoning
		FROM request_logs WHERE request_id = 'req-fixture-codex'`).Scan(
		&requestModel, &upstreamModel, &responseModel, &requestReasoning, &responseReasoning); err != nil {
		t.Fatal(err)
	}
	if requestModel != "gpt-5.4" {
		t.Fatalf("request_model = %q, want preserved gpt-5.4", requestModel)
	}
	if upstreamModel != "" || responseModel != "" || requestReasoning != "" || responseReasoning != "" {
		t.Fatalf("missing source columns must fall back to '', got %q %q %q %q", upstreamModel, responseModel, requestReasoning, responseReasoning)
	}
}
