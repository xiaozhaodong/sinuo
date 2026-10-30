package migration

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"sinuo/internal/tracking"
)

var requestLogTargetColumns = []string{
	"id", "request_id", "client_ip", "user_agent", "method", "path",
	"start_time", "end_time", "duration_ms", "first_token_ms", "completion_ms",
	"first_content_ms", "first_text_ms", "first_reasoning_ms", "reasoning_hidden", "stream_flow_ms", "stop_reason", "upstream_request_id",
	"upstream_write_ms", "upstream_first_dispatch_ms", "upstream_dispatch_ms", "upstream_conn_ms", "schedule_snapshot_json", "privacy_scan_json",
	"request_family", "request_source", "request_kind", "endpoint_name", "model_name",
	"request_model", "upstream_model", "response_model", "request_reasoning", "response_reasoning", "session_id", "upstream_type",
	"upstream_source_name", "upstream_name", "upstream_id", "is_streaming",
	"route_mode", "requested_endpoint", "effective_endpoint", "fallback_reason",
	"route_decision_at", "status", "http_status_code", "retry_count",
	"failure_reason", "last_failure_reason", "cancel_reason", "input_tokens",
	"output_tokens", "cache_creation_tokens", "cache_creation_5m_tokens",
	"cache_creation_1h_tokens", "cache_read_tokens", "input_cost_usd",
	"output_cost_usd", "cache_creation_cost_usd", "cache_creation_5m_cost_usd",
	"cache_creation_1h_cost_usd", "cache_read_cost_usd", "total_cost_usd",
	"created_at", "updated_at",
}

func migrateRequestHistory(ctx context.Context, tx *sql.Tx) (int64, error) {
	exists, err := tableExistsTx(ctx, tx, "request_logs")
	if err != nil {
		return 0, err
	}
	if !exists {
		if _, err := tx.ExecContext(ctx, requestLogsTargetSchema("request_logs")); err != nil {
			return 0, fmt.Errorf("create request_logs target table: %w", err)
		}
		if err := createRequestLogAuxiliarySchema(ctx, tx); err != nil {
			return 0, err
		}
		return 0, nil
	}

	columns, err := tableColumnsTx(ctx, tx, "request_logs")
	if err != nil {
		return 0, err
	}
	var beforeCount int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM request_logs`).Scan(&beforeCount); err != nil {
		return 0, fmt.Errorf("count legacy request logs: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DROP TRIGGER IF EXISTS update_request_logs_timestamp`); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, requestLogsTargetSchema("request_logs_new")); err != nil {
		return 0, fmt.Errorf("create request_logs_new: %w", err)
	}

	expressions := make([]string, 0, len(requestLogTargetColumns))
	for _, column := range requestLogTargetColumns {
		expressions = append(expressions, requestHistoryExpression(column, columns))
	}
	insert := `INSERT INTO request_logs_new (` + joinIdentifiers(requestLogTargetColumns) + `) SELECT ` +
		strings.Join(expressions, ", ") + ` FROM request_logs`
	if _, err := tx.ExecContext(ctx, insert); err != nil {
		return 0, fmt.Errorf("copy request history into target schema: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE request_logs`); err != nil {
		return 0, fmt.Errorf("drop legacy request_logs: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE request_logs_new RENAME TO request_logs`); err != nil {
		return 0, fmt.Errorf("rename request_logs target table: %w", err)
	}
	if err := createRequestLogAuxiliarySchema(ctx, tx); err != nil {
		return 0, err
	}
	var afterCount int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM request_logs`).Scan(&afterCount); err != nil {
		return 0, err
	}
	if beforeCount != afterCount {
		return 0, fmt.Errorf("request log count changed during migration: before=%d after=%d", beforeCount, afterCount)
	}
	return afterCount, nil
}

func requestHistoryExpression(column string, columns map[string]bool) string {
	has := func(name string) bool { return columns[name] }
	value := func(name, fallback string) string {
		if has(name) {
			return quoteIdentifier(name)
		}
		return fallback
	}
	switch column {
	case "request_source":
		if has(column) {
			return quoteIdentifier(column)
		}
		return `'legacy_proxy'`
	case "request_kind":
		if has(column) {
			return quoteIdentifier(column)
		}
		return `'legacy'`
	case "request_family":
		path := value("path", "''")
		upstreamType := value("upstream_type", "''")
		return `CASE
			WHEN ` + path + ` IN ('/v1/messages', '/v1/messages/count_tokens') THEN 'claude'
			WHEN ` + path + ` IN ('/v1/responses', '/v1/responses/compact', '/v1/alpha/search') THEN 'codex'
			WHEN ` + path + ` LIKE '/v1/images/%' THEN 'image'
			WHEN ` + upstreamType + ` = 'account' THEN 'codex'
			ELSE 'other' END`
	case "upstream_name":
		return `COALESCE(NULLIF(` + value("upstream_name", "''") + `, ''), NULLIF(` +
			value("effective_endpoint", "''") + `, ''), NULLIF(` + value("endpoint_name", "''") + `, ''), '')`
	case "upstream_source_name", "request_model", "upstream_model", "response_model", "request_reasoning", "response_reasoning", "session_id":
		return `COALESCE(` + value(column, "''") + `, '')`
	case "upstream_type":
		path := value("path", "''")
		existing := value("upstream_type", "''")
		return `CASE
			WHEN ` + existing + ` IN ('endpoint', 'account', 'subscription') THEN ` + existing + `
			WHEN ` + path + ` IN ('/v1/responses', '/v1/responses/compact', '/v1/alpha/search') THEN 'account'
			WHEN ` + path + ` IN ('/v1/messages', '/v1/messages/count_tokens') OR ` + path + ` LIKE '/v1/images/%' THEN 'endpoint'
			ELSE '' END`
	case "upstream_id":
		return `COALESCE(` + value("upstream_id", "0") + `, 0)`
	case "id":
		return value(column, "NULL")
	case "request_id":
		return value(column, "''")
	case "start_time":
		return value(column, "strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')")
	case "status":
		return `COALESCE(NULLIF(` + value(column, "'pending'") + `, ''), 'pending')`
	case "method":
		return `COALESCE(NULLIF(` + value(column, "'POST'") + `, ''), 'POST')`
	case "path":
		return `COALESCE(NULLIF(` + value(column, "'/v1/messages'") + `, ''), '/v1/messages')`
	case "is_streaming":
		return `COALESCE(` + value(column, "0") + `, 0)`
	case "retry_count":
		return `COALESCE(` + value(column, "0") + `, 0)`
	case "input_tokens", "output_tokens", "cache_creation_tokens",
		"cache_creation_5m_tokens", "cache_creation_1h_tokens", "cache_read_tokens":
		return value(column, "0")
	case "input_cost_usd", "output_cost_usd", "cache_creation_cost_usd",
		"cache_creation_5m_cost_usd", "cache_creation_1h_cost_usd", "cache_read_cost_usd", "total_cost_usd":
		return value(column, "0")
	case "created_at", "updated_at":
		return `COALESCE(` + value(column, "strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')") + `, strftime('%Y-%m-%dT%H:%M:%f000Z', 'now'))`
	default:
		return value(column, "NULL")
	}
}

func requestLogsTargetSchema(table string) string {
	return `CREATE TABLE ` + quoteIdentifier(table) + ` (
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
		first_content_ms INTEGER,
		first_text_ms INTEGER,
		first_reasoning_ms INTEGER,
		reasoning_hidden INTEGER,
		stream_flow_ms INTEGER,
		stop_reason TEXT,
		upstream_request_id TEXT,
		upstream_write_ms INTEGER,
		upstream_first_dispatch_ms INTEGER,
		upstream_dispatch_ms INTEGER,
		upstream_conn_ms INTEGER,
		schedule_snapshot_json TEXT,
		privacy_scan_json TEXT,
		request_family TEXT NOT NULL DEFAULT 'other' CHECK (request_family IN ('claude', 'codex', 'image', 'other')),
		request_source TEXT NOT NULL DEFAULT 'legacy_proxy' CHECK (request_source IN ('legacy_proxy', 'codex_subscription_proxy')),
		request_kind TEXT NOT NULL DEFAULT 'legacy' CHECK (request_kind IN ('legacy', 'generation', 'compaction')),
		endpoint_name TEXT,
		model_name TEXT,
		request_model TEXT DEFAULT '',
		upstream_model TEXT DEFAULT '',
		response_model TEXT DEFAULT '',
		request_reasoning TEXT DEFAULT '',
		response_reasoning TEXT DEFAULT '',
		session_id TEXT DEFAULT '',
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
		created_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')),
		updated_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now'))
	)`
}

func createRequestLogAuxiliarySchema(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		`CREATE INDEX IF NOT EXISTS idx_request_logs_request_id ON request_logs(request_id)`,
		`CREATE INDEX IF NOT EXISTS idx_request_logs_start_time ON request_logs(start_time)`,
		`CREATE INDEX IF NOT EXISTS idx_request_logs_session_time ON request_logs(session_id, start_time)`,
		`CREATE INDEX IF NOT EXISTS idx_request_logs_status ON request_logs(status)`,
		`CREATE INDEX IF NOT EXISTS idx_request_logs_model ON request_logs(model_name)`,
		`CREATE INDEX IF NOT EXISTS idx_request_logs_endpoint ON request_logs(endpoint_name)`,
		`CREATE INDEX IF NOT EXISTS idx_request_logs_failure_reason ON request_logs(failure_reason)`,
		`CREATE INDEX IF NOT EXISTS idx_request_logs_family_time ON request_logs(request_family, start_time)`,
		`CREATE INDEX IF NOT EXISTS idx_request_logs_family_upstream_time ON request_logs(request_family, upstream_name, start_time)`,
		`CREATE INDEX IF NOT EXISTS idx_request_logs_upstream_type_time ON request_logs(upstream_type, start_time)`,
		`CREATE TRIGGER IF NOT EXISTS update_request_logs_timestamp
			AFTER UPDATE ON request_logs FOR EACH ROW WHEN NEW.updated_at = OLD.updated_at
		BEGIN
			UPDATE request_logs SET updated_at = strftime('%Y-%m-%dT%H:%M:%f000Z', 'now') WHERE id = NEW.id;
		END`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("create request_logs index or trigger: %w", err)
		}
	}
	return nil
}

func rebuildUsageSummary(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DROP TRIGGER IF EXISTS update_usage_summary_timestamp`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS usage_summary`); err != nil {
		return fmt.Errorf("drop legacy usage_summary: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE usage_summary (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		date TEXT NOT NULL,
		model_name TEXT NOT NULL DEFAULT '',
		request_family TEXT NOT NULL CHECK (request_family IN ('claude', 'codex', 'image', 'other')),
		request_kind TEXT NOT NULL DEFAULT 'legacy' CHECK (request_kind IN ('legacy', 'generation', 'compaction')),
		upstream_type TEXT NOT NULL DEFAULT '',
		upstream_name TEXT NOT NULL DEFAULT '',
		upstream_id INTEGER NOT NULL DEFAULT 0,
		request_count INTEGER DEFAULT 0,
		success_count INTEGER DEFAULT 0,
		error_count INTEGER DEFAULT 0,
		total_input_tokens INTEGER DEFAULT 0,
		total_output_tokens INTEGER DEFAULT 0,
		total_cache_creation_tokens INTEGER DEFAULT 0,
		total_cache_read_tokens INTEGER DEFAULT 0,
		total_cost_usd REAL DEFAULT 0,
		avg_duration_ms REAL DEFAULT 0,
		created_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')),
		updated_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')),
		UNIQUE(date, model_name, request_family, request_kind, upstream_type, upstream_name, upstream_id)
	)`); err != nil {
		return fmt.Errorf("create usage_summary target table: %w", err)
	}
	summaryQuery := fmt.Sprintf(`INSERT INTO usage_summary (
		date, model_name, request_family, request_kind, upstream_type, upstream_name, upstream_id,
		request_count, success_count, error_count, total_input_tokens, total_output_tokens,
		total_cache_creation_tokens, total_cache_read_tokens, total_cost_usd, avg_duration_ms
	) SELECT
		COALESCE(substr(start_time, 1, 10), ''), COALESCE(model_name, ''), request_family, request_kind,
		COALESCE(upstream_type, ''), COALESCE(upstream_name, ''), COALESCE(upstream_id, 0),
		COUNT(*), SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END),
		SUM(CASE WHEN status IN (%s) THEN 1 ELSE 0 END),
		CASE WHEN COUNT(input_tokens) + COUNT(output_tokens) + COUNT(cache_creation_tokens) + COUNT(cache_read_tokens) > 0
			THEN SUM(COALESCE(input_tokens, 0)) ELSE NULL END,
		CASE WHEN COUNT(input_tokens) + COUNT(output_tokens) + COUNT(cache_creation_tokens) + COUNT(cache_read_tokens) > 0
			THEN SUM(COALESCE(output_tokens, 0)) ELSE NULL END,
		CASE WHEN COUNT(input_tokens) + COUNT(output_tokens) + COUNT(cache_creation_tokens) + COUNT(cache_read_tokens) > 0
			THEN SUM(COALESCE(cache_creation_tokens, 0)) ELSE NULL END,
		CASE WHEN COUNT(input_tokens) + COUNT(output_tokens) + COUNT(cache_creation_tokens) + COUNT(cache_read_tokens) > 0
			THEN SUM(COALESCE(cache_read_tokens, 0)) ELSE NULL END,
		CASE WHEN COUNT(total_cost_usd) > 0 THEN SUM(COALESCE(total_cost_usd, 0)) ELSE NULL END,
		AVG(CASE WHEN duration_ms IS NOT NULL AND duration_ms > 0 THEN duration_ms ELSE NULL END)
	FROM request_logs
	GROUP BY 1, 2, 3, 4, 5, 6, 7`, tracking.FailedRequestStatusesSQLList)
	if _, err := tx.ExecContext(ctx, summaryQuery); err != nil {
		return fmt.Errorf("rebuild usage_summary: %w", err)
	}
	statements := []string{
		`CREATE INDEX idx_usage_summary_date ON usage_summary(date)`,
		`CREATE INDEX idx_usage_summary_model ON usage_summary(model_name)`,
		`CREATE INDEX idx_usage_summary_family ON usage_summary(request_family)`,
		`CREATE INDEX idx_usage_summary_kind ON usage_summary(request_kind)`,
		`CREATE INDEX idx_usage_summary_upstream ON usage_summary(upstream_type, upstream_name, upstream_id)`,
		`CREATE TRIGGER update_usage_summary_timestamp
			AFTER UPDATE ON usage_summary FOR EACH ROW WHEN NEW.updated_at = OLD.updated_at
		BEGIN
			UPDATE usage_summary SET updated_at = strftime('%Y-%m-%dT%H:%M:%f000Z', 'now') WHERE id = NEW.id;
		END`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return validateSummaryConservation(ctx, tx)
}

func validateSummaryConservation(ctx context.Context, tx *sql.Tx) error {
	var requestCount, summaryCount int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM request_logs`).Scan(&requestCount); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(request_count), 0) FROM usage_summary`).Scan(&summaryCount); err != nil {
		return err
	}
	if requestCount != summaryCount {
		return fmt.Errorf("usage summary request count mismatch: request_logs=%d summary=%d", requestCount, summaryCount)
	}
	var requestInput, requestOutput, requestCreation, requestRead int64
	var requestCost float64
	if err := tx.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		COALESCE(SUM(cache_creation_tokens), 0), COALESCE(SUM(cache_read_tokens), 0),
		COALESCE(SUM(total_cost_usd), 0) FROM request_logs`).Scan(
		&requestInput, &requestOutput, &requestCreation, &requestRead, &requestCost,
	); err != nil {
		return err
	}
	var summaryInput, summaryOutput, summaryCreation, summaryRead int64
	var summaryCost float64
	if err := tx.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(total_input_tokens), 0), COALESCE(SUM(total_output_tokens), 0),
		COALESCE(SUM(total_cache_creation_tokens), 0), COALESCE(SUM(total_cache_read_tokens), 0),
		COALESCE(SUM(total_cost_usd), 0) FROM usage_summary`).Scan(
		&summaryInput, &summaryOutput, &summaryCreation, &summaryRead, &summaryCost,
	); err != nil {
		return err
	}
	if requestInput != summaryInput || requestOutput != summaryOutput || requestCreation != summaryCreation || requestRead != summaryRead {
		return fmt.Errorf("usage summary token totals changed during rebuild")
	}
	delta := requestCost - summaryCost
	if delta < 0 {
		delta = -delta
	}
	if delta > 0.000000001 {
		return fmt.Errorf("usage summary cost total changed during rebuild: request_logs=%f summary=%f", requestCost, summaryCost)
	}
	return nil
}

func joinIdentifiers(columns []string) string {
	quoted := make([]string, len(columns))
	for i, column := range columns {
		quoted[i] = quoteIdentifier(column)
	}
	return strings.Join(quoted, ", ")
}
