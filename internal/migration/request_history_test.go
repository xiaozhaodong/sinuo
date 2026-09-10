package migration

import (
	"context"
	"database/sql"
	"testing"

	"cc-forwarder/internal/tracking"
)

func TestRequestHistoryMigrationUsesPathBeforeUpstreamType(t *testing.T) {
	db := openLegacyFixtureDB(t)
	defer db.Close()
	if _, err := db.Exec(`UPDATE request_logs SET upstream_type = 'endpoint' WHERE request_id = 'req-fixture-codex'`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrateRequestHistory(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	assertRequestFamily(t, db, "req-fixture-codex", "codex", "endpoint", "codex-fixture")
}

func TestRequestHistoryMigrationBackfillsSourceKindWithoutTouchingUpdatedAt(t *testing.T) {
	db := openLegacyFixtureDB(t)
	defer db.Close()
	var before string
	if err := db.QueryRow(`SELECT CAST(updated_at AS TEXT) FROM request_logs WHERE request_id='req-fixture-claude'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for run := 0; run < 2; run++ {
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
	}
	var source, kind, after string
	if err := db.QueryRow(`SELECT request_source, request_kind, CAST(updated_at AS TEXT)
		FROM request_logs WHERE request_id='req-fixture-claude'`).Scan(&source, &kind, &after); err != nil {
		t.Fatal(err)
	}
	if source != tracking.RequestSourceLegacyProxy || kind != tracking.RequestKindLegacy {
		t.Fatalf("source/kind = %q/%q", source, kind)
	}
	if after != before {
		t.Fatalf("updated_at changed: before=%q after=%q", before, after)
	}
}

func TestRequestHistoryMigrationRecognizesStandaloneCodexSearch(t *testing.T) {
	db := openLegacyFixtureDB(t)
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO request_logs (request_id, path, start_time, status, upstream_type)
		VALUES ('req-search', '/v1/alpha/search', CURRENT_TIMESTAMP, 'completed', '')`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrateRequestHistory(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	assertRequestFamily(t, db, "req-search", "codex", "account", "")
}

func TestRequestHistoryMigrationDefaultsUpstreamSourceName(t *testing.T) {
	tests := []struct {
		name      string
		requestID string
		prepare   func(*testing.T, *sql.DB)
	}{
		{
			name:      "missing column",
			requestID: "req-without-source-column",
			prepare: func(t *testing.T, db *sql.DB) {
				t.Helper()
				if _, err := db.Exec(`DROP TABLE request_logs`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`CREATE TABLE request_logs (
					request_id TEXT UNIQUE NOT NULL,
					start_time DATETIME NOT NULL
				)`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`INSERT INTO request_logs (request_id, start_time) VALUES (?, CURRENT_TIMESTAMP)`, "req-without-source-column"); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:      "explicit null",
			requestID: "req-fixture-claude",
			prepare: func(t *testing.T, db *sql.DB) {
				t.Helper()
				if _, err := db.Exec(`UPDATE request_logs SET upstream_source_name = NULL WHERE request_id = ?`, "req-fixture-claude"); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openLegacyFixtureDB(t)
			defer db.Close()
			tt.prepare(t, db)

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

			var sourceName string
			if err := db.QueryRow(`SELECT upstream_source_name FROM request_logs WHERE request_id = ?`, tt.requestID).Scan(&sourceName); err != nil {
				t.Fatal(err)
			}
			if sourceName != "" {
				t.Fatalf("upstream_source_name = %q, want empty string", sourceName)
			}
		})
	}
}

func TestRebuildUsageSummaryMatchesRuntimeAggregation(t *testing.T) {
	db := openLegacyFixtureDB(t)
	defer db.Close()
	if _, err := db.Exec(`DELETE FROM request_logs`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO request_logs (
		request_id, path, start_time, model_name, status, duration_ms
	) VALUES
		('req-completed', '/v1/messages', CURRENT_TIMESTAMP, 'summary-model', 'completed', 100),
		('req-failed', '/v1/messages', CURRENT_TIMESTAMP, 'summary-model', 'failed', 300),
		('req-error', '/v1/messages', CURRENT_TIMESTAMP, 'summary-model', 'error', 0),
		('req-auth-error', '/v1/messages', CURRENT_TIMESTAMP, 'summary-model', 'auth_error', 500),
		('req-cancelled', '/v1/messages', CURRENT_TIMESTAMP, 'summary-model', 'cancelled', NULL)`); err != nil {
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
	if err := rebuildUsageSummary(context.Background(), tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	var requestCount, successCount, errorCount int
	var avgDuration float64
	if err := db.QueryRow(`SELECT request_count, success_count, error_count, avg_duration_ms
		FROM usage_summary WHERE model_name = 'summary-model'`).Scan(
		&requestCount, &successCount, &errorCount, &avgDuration,
	); err != nil {
		t.Fatal(err)
	}
	if requestCount != 5 || successCount != 1 || errorCount != 3 || avgDuration != 300 {
		t.Fatalf("summary = requests:%d success:%d errors:%d avg:%v", requestCount, successCount, errorCount, avgDuration)
	}
}

func TestRebuildUsageSummaryPreservesUnknownUsage(t *testing.T) {
	db := openLegacyFixtureDB(t)
	defer db.Close()
	if _, err := db.Exec(`DELETE FROM request_logs`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO request_logs (
		request_id, path, start_time, model_name, status,
		input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens, total_cost_usd
	) VALUES ('unknown-summary', '/v1/responses', CURRENT_TIMESTAMP, 'unknown-model', 'failed',
		NULL, NULL, NULL, NULL, NULL)`); err != nil {
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
	if err := rebuildUsageSummary(context.Background(), tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var input, output, creation, read sql.NullInt64
	var cost sql.NullFloat64
	if err := db.QueryRow(`SELECT total_input_tokens, total_output_tokens,
		total_cache_creation_tokens, total_cache_read_tokens, total_cost_usd
		FROM usage_summary WHERE model_name='unknown-model'`).Scan(&input, &output, &creation, &read, &cost); err != nil {
		t.Fatal(err)
	}
	if input.Valid || output.Valid || creation.Valid || read.Valid || cost.Valid {
		t.Fatalf("unknown summary became known: input=%v output=%v creation=%v read=%v cost=%v", input, output, creation, read, cost)
	}
}
