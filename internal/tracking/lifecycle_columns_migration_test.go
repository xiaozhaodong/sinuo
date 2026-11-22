package tracking

import (
	"testing"
)

const legacyRequestLogsWithoutLifecycleColumns = `
CREATE TABLE IF NOT EXISTS request_logs (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	request_id TEXT UNIQUE NOT NULL,
	client_ip TEXT,
	user_agent TEXT,
	method TEXT DEFAULT 'POST',
	path TEXT DEFAULT '/v1/messages',
	start_time DATETIME NOT NULL,
	end_time DATETIME,
	duration_ms INTEGER,
	first_token_ms INTEGER,
	completion_ms INTEGER,
	request_family TEXT NOT NULL DEFAULT 'other',
	endpoint_name TEXT,
	model_name TEXT,
	upstream_type TEXT NOT NULL DEFAULT '',
	upstream_source_name TEXT NOT NULL DEFAULT '',
	upstream_name TEXT NOT NULL DEFAULT '',
	upstream_id INTEGER NOT NULL DEFAULT 0,
	is_streaming BOOLEAN DEFAULT FALSE,
	route_mode TEXT DEFAULT 'auto',
	requested_endpoint TEXT DEFAULT '',
	effective_endpoint TEXT DEFAULT '',
	fallback_reason TEXT DEFAULT '',
	route_decision_at DATETIME,
	status TEXT NOT NULL DEFAULT 'pending',
	http_status_code INTEGER,
	retry_count INTEGER DEFAULT 0,
	failure_reason TEXT,
	last_failure_reason TEXT,
	cancel_reason TEXT,
	input_tokens INTEGER DEFAULT 0,
	output_tokens INTEGER DEFAULT 0,
	cache_creation_tokens INTEGER DEFAULT 0,
	cache_creation_5m_tokens INTEGER DEFAULT 0,
	cache_creation_1h_tokens INTEGER DEFAULT 0,
	cache_read_tokens INTEGER DEFAULT 0,
	input_cost_usd REAL DEFAULT 0,
	output_cost_usd REAL DEFAULT 0,
	cache_creation_cost_usd REAL DEFAULT 0,
	cache_creation_5m_cost_usd REAL DEFAULT 0,
	cache_creation_1h_cost_usd REAL DEFAULT 0,
	cache_read_cost_usd REAL DEFAULT 0,
	total_cost_usd REAL DEFAULT 0,
	created_at DATETIME,
	updated_at DATETIME
)`

// TestInitSchemaAddsLifecycleColumnsToLegacyDB 旧库经 InitSchema 后补齐详情列，且重复执行幂等。
func TestInitSchemaAddsLifecycleColumnsToLegacyDB(t *testing.T) {
	adapter, err := NewSQLiteAdapter(DatabaseConfig{DatabasePath: ":memory:"})
	if err != nil {
		t.Fatalf("create adapter failed: %v", err)
	}
	if err := adapter.Open(); err != nil {
		t.Fatalf("open adapter failed: %v", err)
	}
	t.Cleanup(func() { _ = adapter.Close() })

	db := adapter.GetDB()
	if _, err := db.Exec(legacyRequestLogsWithoutLifecycleColumns); err != nil {
		t.Fatalf("create legacy request_logs failed: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO request_logs (request_id, start_time) VALUES ('legacy-row', '2026-08-13T00:00:00.000000Z')`); err != nil {
		t.Fatalf("insert legacy row failed: %v", err)
	}

	if err := adapter.InitSchema(); err != nil {
		t.Fatalf("InitSchema on legacy db failed: %v", err)
	}

	for _, column := range []string{"upstream_write_ms", "schedule_snapshot_json", "privacy_scan_json", "input_composition_json"} {
		var count int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info('request_logs') WHERE name = ?`, column,
		).Scan(&count); err != nil {
			t.Fatalf("query column %s failed: %v", column, err)
		}
		if count != 1 {
			t.Fatalf("expected column %s added by migration, got count=%d", column, count)
		}
	}

	// 旧行保留且新列默认 NULL。
	var upstreamWriteMs interface{}
	var scheduleJSON, privacyJSON, compositionJSON interface{}
	if err := db.QueryRow(
		`SELECT upstream_write_ms, schedule_snapshot_json, privacy_scan_json, input_composition_json FROM request_logs WHERE request_id = 'legacy-row'`,
	).Scan(&upstreamWriteMs, &scheduleJSON, &privacyJSON, &compositionJSON); err != nil {
		t.Fatalf("query legacy row failed: %v", err)
	}
	if upstreamWriteMs != nil || scheduleJSON != nil || privacyJSON != nil || compositionJSON != nil {
		t.Fatalf("expected NULL defaults for new columns, got %v / %v / %v / %v", upstreamWriteMs, scheduleJSON, privacyJSON, compositionJSON)
	}

	// 重复执行必须幂等。
	if err := adapter.InitSchema(); err != nil {
		t.Fatalf("second InitSchema failed: %v", err)
	}
}
