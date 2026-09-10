package tracking

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	timezonepolicy "cc-forwarder/internal/timezone"
)

func TestRequestClassificationUpgradePreservesEveryHistoricalColumn(t *testing.T) {
	adapter, err := NewSQLiteAdapter(DatabaseConfig{DatabasePath: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Open(); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	if err := adapter.InitSchema(); err != nil {
		t.Fatal(err)
	}
	// 在隔离库移除本阶段两列，恢复升级前的 request_logs 形状。
	for _, statement := range []string{
		`ALTER TABLE request_logs DROP COLUMN request_source`,
		`ALTER TABLE request_logs DROP COLUMN request_kind`,
		`INSERT INTO request_logs (request_id, start_time, end_time, duration_ms, first_token_ms,
		completion_ms, request_family, path, upstream_type, upstream_name, upstream_id, model_name,
		status, http_status_code, input_tokens, output_tokens, cache_creation_tokens,
		cache_creation_5m_tokens, cache_creation_1h_tokens, cache_read_tokens, total_cost_usd,
		created_at, updated_at) VALUES
		('legacy-known', '2026-09-09T00:00:00.123456Z', '2026-09-09T00:00:01.123456Z', 1000, 123,
		877, 'codex', '/v1/responses', 'account', 'synthetic-account', 7, 'synthetic-model',
		'completed', 200, 101, 17, 13, 5, 8, 29, 1.23456789,
		'2026-09-09T00:00:00.123456Z', '2026-09-09T00:00:01.123456Z'),
		('legacy-unknown', '2026-09-09T01:00:00.123456Z', NULL, NULL, NULL,
		NULL, 'claude', '/v1/messages', 'endpoint', 'synthetic-endpoint', 0, '',
		'cancelled', NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL,
		'2026-09-09T01:00:00.123456Z', '2026-09-09T01:00:00.123456Z')`,
	} {
		if _, err := adapter.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := adapter.db.Query(`SELECT * FROM request_logs LIMIT 0`)
	if err != nil {
		t.Fatal(err)
	}
	columns, err := rows.Columns()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	snapshot := func() [][]any {
		t.Helper()
		rows, err := adapter.db.Query(`SELECT ` + strings.Join(columns, ", ") + ` FROM request_logs ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var records [][]any
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for index := range values {
				pointers[index] = &values[index]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			records = append(records, values)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return records
	}
	before := snapshot()
	for range 2 {
		if err := adapter.InitSchema(); err != nil {
			t.Fatal(err)
		}
		if after := snapshot(); !reflect.DeepEqual(before, after) {
			t.Fatalf("historical columns changed\nbefore=%#v\nafter=%#v", before, after)
		}
		var correctlyClassified int
		if err := adapter.db.QueryRow(`SELECT COUNT(*) FROM request_logs
			WHERE request_source='legacy_proxy' AND request_kind='legacy'`).Scan(&correctlyClassified); err != nil {
			t.Fatal(err)
		}
		if correctlyClassified != len(before) {
			t.Fatalf("classified rows=%d", correctlyClassified)
		}
	}
}

func TestEnsureUsageSummaryRequestKindRecalculatesFromRequestLogs(t *testing.T) {
	adapter, err := NewSQLiteAdapter(DatabaseConfig{DatabasePath: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Open(); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	if err := adapter.InitSchema(); err != nil {
		t.Fatal(err)
	}

	started := timezonepolicy.FormatStorage(time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC))
	if _, err := adapter.db.Exec(`INSERT INTO request_logs (
		request_id, start_time, duration_ms, model_name, request_family, request_source, request_kind,
		upstream_type, upstream_name, upstream_id, status, input_tokens, output_tokens,
		cache_creation_tokens, cache_read_tokens, total_cost_usd
	) VALUES
		('generation-row', ?, 100, 'synthetic-model', 'codex', 'codex_subscription_proxy', 'generation',
		 'subscription', 'chatgpt-subscription', 0, 'completed', 10, 4, 0, 2, 0.25),
		('compaction-row', ?, 300, 'synthetic-model', 'codex', 'codex_subscription_proxy', 'compaction',
		 'subscription', 'chatgpt-subscription', 0, 'completed', 6, 1, 0, 1, 0.10)`, started, started); err != nil {
		t.Fatal(err)
	}

	if _, err := adapter.db.Exec(`DROP TRIGGER update_usage_summary_timestamp; DROP TABLE usage_summary;
		CREATE TABLE usage_summary (
			id INTEGER PRIMARY KEY AUTOINCREMENT, timezone_name TEXT NOT NULL, date TEXT NOT NULL,
			model_name TEXT NOT NULL, request_family TEXT NOT NULL, upstream_type TEXT NOT NULL DEFAULT '',
			upstream_name TEXT NOT NULL DEFAULT '', upstream_id INTEGER NOT NULL DEFAULT 0,
			request_count INTEGER DEFAULT 0, success_count INTEGER DEFAULT 0, error_count INTEGER DEFAULT 0,
			total_input_tokens INTEGER DEFAULT 0, total_output_tokens INTEGER DEFAULT 0,
			total_cache_creation_tokens INTEGER DEFAULT 0, total_cache_read_tokens INTEGER DEFAULT 0,
			total_cost_usd REAL DEFAULT 0, avg_duration_ms REAL DEFAULT 0,
			created_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')),
			updated_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')),
			UNIQUE(timezone_name, date, model_name, request_family, upstream_type, upstream_name, upstream_id)
		);
		INSERT INTO usage_summary (
			timezone_name, date, model_name, request_family, upstream_type, upstream_name, upstream_id,
			request_count, success_count, total_input_tokens, total_cost_usd
		) VALUES ('UTC', '2026-09-10', 'synthetic-model', 'codex', 'subscription', 'chatgpt-subscription', 0,
			999, 0, 99999, 999.0)`); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if err := adapter.ensureUsageSummaryRequestKind(ctx); err != nil {
		t.Fatal(err)
	}
	assertRecalculatedSummaryRows(t, adapter)
	if err := adapter.ensureUsageSummaryRequestKind(ctx); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	assertRecalculatedSummaryRows(t, adapter)
}

func assertRecalculatedSummaryRows(t *testing.T, adapter *SQLiteAdapter) {
	t.Helper()
	rows, err := adapter.db.Query(`SELECT request_kind, request_count, success_count,
		total_input_tokens, total_output_tokens, total_cache_read_tokens, total_cost_usd, avg_duration_ms
		FROM usage_summary ORDER BY request_kind`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type summaryRow struct {
		kind                        string
		requests, successes         int
		input, output, cacheRead    int64
		cost, averageDurationMillis float64
	}
	var got []summaryRow
	for rows.Next() {
		var row summaryRow
		if err := rows.Scan(&row.kind, &row.requests, &row.successes, &row.input, &row.output, &row.cacheRead, &row.cost, &row.averageDurationMillis); err != nil {
			t.Fatal(err)
		}
		got = append(got, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("summary rows = %+v, want generation and compaction", got)
	}
	if got[0] != (summaryRow{kind: RequestKindCompaction, requests: 1, successes: 1, input: 6, output: 1, cacheRead: 1, cost: 0.10, averageDurationMillis: 300}) {
		t.Fatalf("compaction summary = %+v", got[0])
	}
	if got[1] != (summaryRow{kind: RequestKindGeneration, requests: 1, successes: 1, input: 10, output: 4, cacheRead: 2, cost: 0.25, averageDurationMillis: 100}) {
		t.Fatalf("generation summary = %+v", got[1])
	}
}
