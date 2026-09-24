package migration

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"cc-forwarder/config"
	"cc-forwarder/internal/privacy"
)

type databaseMigrationResult struct {
	EndpointCountBefore int
	EndpointCountAfter  int
	SplitEndpointCount  int
	DerivedRecordCount  int
	RequestLogCount     int64
}

func migrateDatabase(ctx context.Context, db *sql.DB, legacy *LegacyConfig, backup *BackupResult) (databaseMigrationResult, error) {
	if backup == nil || strings.TrimSpace(backup.Directory) == "" || strings.TrimSpace(backup.ManifestSHA256) == "" {
		return databaseMigrationResult{}, fmt.Errorf("verified migration backup is required")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return databaseMigrationResult{}, fmt.Errorf("begin migration transaction: %w", err)
	}
	defer tx.Rollback()

	if err := createMigrationLedger(ctx, tx, legacy.SourceMode, backup); err != nil {
		return databaseMigrationResult{}, err
	}
	for _, table := range []string{"endpoints", "request_logs"} {
		if err := normalizeCurrentTimestampAuditColumns(ctx, tx, table); err != nil {
			return databaseMigrationResult{}, err
		}
	}
	var endpointCountBefore int
	if exists, err := tableExistsTx(ctx, tx, "endpoints"); err != nil {
		return databaseMigrationResult{}, err
	} else if exists {
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM endpoints`).Scan(&endpointCountBefore); err != nil {
			return databaseMigrationResult{}, err
		}
	}

	var flattened EndpointFlattenResult
	if legacy.SourceMode == SourceModeSQLite {
		flattened, err = loadSQLiteEndpointSnapshots(ctx, tx)
	} else {
		flattened, err = FlattenLegacyEndpoints(legacy)
	}
	if err != nil {
		return databaseMigrationResult{}, err
	}
	if err := rebuildEndpoints(ctx, tx, flattened); err != nil {
		return databaseMigrationResult{}, err
	}
	if err := expandPrivacyEndpointScopes(ctx, tx, flattened.DerivedNamesBySource); err != nil {
		return databaseMigrationResult{}, err
	}
	if err := repairClaudeRoutingTarget(ctx, tx); err != nil {
		return databaseMigrationResult{}, err
	}
	requestCount, err := migrateRequestHistory(ctx, tx)
	if err != nil {
		return databaseMigrationResult{}, err
	}
	if err := rebuildUsageSummary(ctx, tx); err != nil {
		return databaseMigrationResult{}, err
	}
	if err := validateTargetSchemaTx(ctx, tx); err != nil {
		return databaseMigrationResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE app_schema_migrations
		SET phase = ?, db_committed_at = strftime('%Y-%m-%dT%H:%M:%f000Z', 'now'), error_message = ''
		WHERE migration_id = ?`, string(PhaseDBCommitted), MigrationID); err != nil {
		return databaseMigrationResult{}, fmt.Errorf("mark migration database committed: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return databaseMigrationResult{}, fmt.Errorf("commit migration transaction: %w", err)
	}
	return databaseMigrationResult{
		EndpointCountBefore: endpointCountBefore,
		EndpointCountAfter:  len(flattened.Endpoints),
		SplitEndpointCount:  flattened.SplitEndpointCount,
		DerivedRecordCount:  flattened.DerivedRecordCount,
		RequestLogCount:     requestCount,
	}, nil
}

func createMigrationLedger(ctx context.Context, tx *sql.Tx, sourceMode SourceMode, backup *BackupResult) error {
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS app_schema_migrations (
		migration_id TEXT PRIMARY KEY,
		phase TEXT NOT NULL CHECK (phase IN ('prepared', 'db_committed', 'config_committed', 'completed', 'failed')),
		source_mode TEXT NOT NULL,
		backup_dir TEXT NOT NULL,
		backup_manifest_sha256 TEXT NOT NULL,
		started_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')),
		db_committed_at DATETIME,
		config_committed_at DATETIME,
		completed_at DATETIME,
		error_message TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO app_schema_migrations (
		migration_id, phase, source_mode, backup_dir, backup_manifest_sha256
	) VALUES (?, ?, ?, ?, ?)
	ON CONFLICT(migration_id) DO UPDATE SET
		phase = excluded.phase,
		source_mode = excluded.source_mode,
		backup_dir = excluded.backup_dir,
		backup_manifest_sha256 = excluded.backup_manifest_sha256,
		error_message = ''`, MigrationID, string(PhasePrepared), string(sourceMode), backup.Directory, backup.ManifestSHA256); err != nil {
		return fmt.Errorf("record prepared migration: %w", err)
	}
	return nil
}

func rebuildEndpoints(ctx context.Context, tx *sql.Tx, flattened EndpointFlattenResult) error {
	for _, endpoint := range flattened.Endpoints {
		if err := config.ValidateSoftFailureOverrides(endpoint.SoftFailureWindowMinutes, endpoint.SoftFailureThreshold); err != nil {
			return fmt.Errorf("端点 %s 失败策略无效: %w", endpoint.Name, err)
		}
	}
	hasRuntimeStates, err := tableExistsTx(ctx, tx, "endpoint_runtime_states")
	if err != nil {
		return err
	}
	if hasRuntimeStates {
		if _, err := tx.ExecContext(ctx, `CREATE TEMP TABLE migration_endpoint_runtime_states AS
			SELECT e.name AS endpoint_name, s.scope, s.state, s.cooldown_until,
				s.cooldown_reason, s.revision, s.updated_at
			FROM endpoint_runtime_states s JOIN endpoints e ON e.id = s.endpoint_id`); err != nil {
			return fmt.Errorf("snapshot endpoint runtime states: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DROP TABLE endpoint_runtime_states`); err != nil {
			return fmt.Errorf("drop legacy endpoint runtime states: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DROP TRIGGER IF EXISTS update_endpoints_timestamp`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, endpointTargetSchema("endpoints_new")); err != nil {
		return fmt.Errorf("create endpoints_new: %w", err)
	}
	insert := `INSERT INTO endpoints_new (
		id, name, url, token, api_key, headers, priority, failover_enabled,
		availability_enabled, cooldown_seconds, timeout_seconds, supports_count_tokens,
		model_rewrite_rules, soft_failure_window_minutes, soft_failure_threshold, cost_multiplier, input_cost_multiplier, output_cost_multiplier,
		cache_creation_cost_multiplier, cache_creation_cost_multiplier_1h,
		cache_read_cost_multiplier, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, COALESCE(NULLIF(?, ''), strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')), COALESCE(NULLIF(?, ''), strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')))`
	for _, endpoint := range flattened.Endpoints {
		headers, err := json.Marshal(endpoint.Headers)
		if err != nil {
			return fmt.Errorf("encode endpoint %q headers: %w", endpoint.Name, err)
		}
		var id any
		if endpoint.ID > 0 {
			id = endpoint.ID
		}
		if _, err := tx.ExecContext(ctx, insert,
			id, endpoint.Name, endpoint.URL, endpoint.Token, endpoint.APIKey, string(headers),
			endpoint.Priority, boolInt(endpoint.FailoverEnabled), boolInt(endpoint.AvailabilityEnabled),
			endpoint.CooldownSeconds, endpoint.TimeoutSeconds, boolInt(endpoint.SupportsCountTokens),
			endpoint.ModelRewriteRules, endpoint.SoftFailureWindowMinutes, endpoint.SoftFailureThreshold, endpoint.CostMultiplier, endpoint.InputCostMultiplier,
			endpoint.OutputCostMultiplier, endpoint.CacheCreationCostMultiplier,
			endpoint.CacheCreationCostMultiplier1h, endpoint.CacheReadCostMultiplier,
			endpoint.CreatedAt, endpoint.UpdatedAt,
		); err != nil {
			return fmt.Errorf("insert migrated endpoint %q: %w", endpoint.Name, err)
		}
	}
	if exists, err := tableExistsTx(ctx, tx, "endpoints"); err != nil {
		return err
	} else if exists {
		if _, err := tx.ExecContext(ctx, `DROP TABLE endpoints`); err != nil {
			return fmt.Errorf("drop legacy endpoints: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE endpoints_new RENAME TO endpoints`); err != nil {
		return fmt.Errorf("rename migrated endpoints table: %w", err)
	}
	for _, statement := range endpointAuxiliarySchema() {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("create endpoint target index or trigger: %w", err)
		}
	}
	if hasRuntimeStates {
		if _, err := tx.ExecContext(ctx, `INSERT INTO endpoint_runtime_states (
			endpoint_id, scope, state, cooldown_until, cooldown_reason, revision, updated_at
		) SELECT e.id, s.scope, s.state, s.cooldown_until, s.cooldown_reason, s.revision, s.updated_at
		FROM migration_endpoint_runtime_states s JOIN endpoints e ON e.name = s.endpoint_name`); err != nil {
			return fmt.Errorf("restore endpoint runtime states: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DROP TABLE migration_endpoint_runtime_states`); err != nil {
			return err
		}
	}
	return nil
}

func endpointTargetSchema(table string) string {
	return `CREATE TABLE ` + quoteIdentifier(table) + ` (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT UNIQUE NOT NULL CHECK (length(trim(name)) > 0),
		url TEXT NOT NULL CHECK (length(trim(url)) > 0),
		token TEXT,
		api_key TEXT,
		headers TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(headers) AND json_type(headers) = 'object'),
		auth_mode TEXT NOT NULL DEFAULT 'configured',
		priority INTEGER NOT NULL DEFAULT 1 CHECK (priority >= 0),
		failover_enabled INTEGER NOT NULL DEFAULT 1 CHECK (failover_enabled IN (0, 1)),
		availability_enabled INTEGER NOT NULL DEFAULT 1 CHECK (availability_enabled IN (0, 1)),
		cooldown_seconds INTEGER,
		timeout_seconds INTEGER NOT NULL DEFAULT 300 CHECK (timeout_seconds > 0),
		supports_count_tokens INTEGER NOT NULL DEFAULT 0 CHECK (supports_count_tokens IN (0, 1)),
		model_rewrite_rules TEXT NOT NULL DEFAULT '',
		soft_failure_window_minutes INTEGER,
		soft_failure_threshold INTEGER,
		cost_multiplier REAL NOT NULL DEFAULT 1.0 CHECK (cost_multiplier > 0),
		input_cost_multiplier REAL NOT NULL DEFAULT 1.0 CHECK (input_cost_multiplier > 0),
		output_cost_multiplier REAL NOT NULL DEFAULT 1.0 CHECK (output_cost_multiplier > 0),
		cache_creation_cost_multiplier REAL NOT NULL DEFAULT 1.0 CHECK (cache_creation_cost_multiplier > 0),
		cache_creation_cost_multiplier_1h REAL NOT NULL DEFAULT 1.0 CHECK (cache_creation_cost_multiplier_1h > 0),
		cache_read_cost_multiplier REAL NOT NULL DEFAULT 1.0 CHECK (cache_read_cost_multiplier > 0),
		created_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')),
		updated_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now'))
	)`
}

func endpointAuxiliarySchema() []string {
	return []string{
		`CREATE INDEX idx_endpoints_priority ON endpoints(priority)`,
		`CREATE INDEX idx_endpoints_failover ON endpoints(failover_enabled)`,
		`CREATE INDEX idx_endpoints_availability ON endpoints(availability_enabled)`,
		`CREATE TABLE endpoint_runtime_states (
			endpoint_id INTEGER NOT NULL,
			scope TEXT NOT NULL CHECK (scope IN ('global', 'messages', 'count_tokens')),
			state TEXT NOT NULL DEFAULT 'active',
			cooldown_until DATETIME,
			cooldown_reason TEXT NOT NULL DEFAULT '',
			revision INTEGER NOT NULL DEFAULT 0,
			updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')),
			PRIMARY KEY (endpoint_id, scope),
			FOREIGN KEY (endpoint_id) REFERENCES endpoints(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX idx_endpoint_runtime_states_cooldown ON endpoint_runtime_states(scope, cooldown_until)`,
		`CREATE TRIGGER update_endpoints_timestamp
			AFTER UPDATE ON endpoints FOR EACH ROW WHEN NEW.updated_at = OLD.updated_at
		BEGIN
			UPDATE endpoints SET updated_at = strftime('%Y-%m-%dT%H:%M:%f000Z', 'now') WHERE id = NEW.id;
		END`,
	}
}

func expandPrivacyEndpointScopes(ctx context.Context, tx *sql.Tx, derived map[string][]string) error {
	if len(derived) == 0 {
		return validatePrivacyRules(ctx, tx)
	}
	exists, err := tableExistsTx(ctx, tx, "privacy_rules")
	if err != nil || !exists {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, scope_json FROM privacy_rules ORDER BY id`)
	if err != nil {
		return fmt.Errorf("read privacy scopes: %w", err)
	}
	type update struct {
		id    int64
		scope string
	}
	var updates []update
	for rows.Next() {
		var id int64
		var scopeJSON string
		if err := rows.Scan(&id, &scopeJSON); err != nil {
			rows.Close()
			return err
		}
		scope, err := privacy.ParseScope(scopeJSON)
		if err != nil {
			rows.Close()
			return fmt.Errorf("parse privacy rule %d scope: %w", id, err)
		}
		changed := false
		seen := make(map[string]struct{}, len(scope.EndpointNames))
		for _, name := range scope.EndpointNames {
			seen[name] = struct{}{}
		}
		for _, sourceName := range append([]string(nil), scope.EndpointNames...) {
			for _, name := range derived[sourceName] {
				if _, exists := seen[name]; exists {
					continue
				}
				scope.EndpointNames = append(scope.EndpointNames, name)
				seen[name] = struct{}{}
				changed = true
			}
		}
		if changed {
			encoded, err := privacy.EncodeScope(scope)
			if err != nil {
				rows.Close()
				return err
			}
			updates = append(updates, update{id: id, scope: encoded})
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range updates {
		if _, err := tx.ExecContext(ctx, `UPDATE privacy_rules SET scope_json = ? WHERE id = ?`, item.scope, item.id); err != nil {
			return err
		}
	}
	return validatePrivacyRules(ctx, tx)
}

func validatePrivacyRules(ctx context.Context, tx *sql.Tx) error {
	exists, err := tableExistsTx(ctx, tx, "privacy_rules")
	if err != nil || !exists {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, enabled, name, description, priority, match_type,
		pattern, placeholder, action, scope_json, source FROM privacy_rules ORDER BY priority, id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var rules []privacy.Rule
	for rows.Next() {
		var rule privacy.Rule
		var scopeJSON string
		if err := rows.Scan(&rule.ID, &rule.Enabled, &rule.Name, &rule.Description, &rule.Priority,
			&rule.MatchType, &rule.Pattern, &rule.Placeholder, &rule.Action, &scopeJSON, &rule.Source); err != nil {
			return err
		}
		scope, err := privacy.ParseScope(scopeJSON)
		if err != nil {
			return fmt.Errorf("parse privacy rule %d scope: %w", rule.ID, err)
		}
		rule.Scope = scope
		rules = append(rules, rule)
	}
	if _, err := privacy.CompileRules(rules); err != nil {
		return fmt.Errorf("compile privacy rules after endpoint migration: %w", err)
	}
	return rows.Err()
}

func repairClaudeRoutingTarget(ctx context.Context, tx *sql.Tx) error {
	exists, err := tableExistsTx(ctx, tx, "app_settings")
	if err != nil || !exists {
		return err
	}
	var target string
	err = tx.QueryRowContext(ctx, `SELECT value FROM app_settings
		WHERE category = 'claude_routing' AND key = 'endpoint_name'`).Scan(&target)
	if err == sql.ErrNoRows || strings.TrimSpace(target) == "" {
		return nil
	}
	if err != nil {
		return err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM endpoints WHERE name = ?`, target).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	updates := map[string]string{"mode": "auto", "endpoint_name": "", "fallback_enabled": "true"}
	for key, value := range updates {
		if _, err := tx.ExecContext(ctx, `UPDATE app_settings SET value = ?
			WHERE category = 'claude_routing' AND key = ?`, value, key); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE app_settings SET value = CAST(COALESCE(NULLIF(value, ''), '0') AS INTEGER) + 1
		WHERE category = 'claude_routing' AND key = 'revision'`); err != nil {
		return err
	}
	return nil
}

func validateTargetSchema(ctx context.Context, db *sql.DB) error {
	for table, forbidden := range map[string][]string{
		"endpoints":    {"channel", "enabled"},
		"request_logs": {"channel", "group_name"},
	} {
		for _, column := range forbidden {
			has, err := columnExists(ctx, db, table, column)
			if err != nil {
				return err
			}
			if has {
				return fmt.Errorf("target schema still contains %s.%s", table, column)
			}
		}
	}
	for table, required := range map[string][]string{
		"endpoints":     {"availability_enabled", "failover_enabled"},
		"request_logs":  {"request_family", "upstream_name"},
		"usage_summary": {"request_family", "upstream_type", "upstream_name", "upstream_id"},
	} {
		for _, column := range required {
			has, err := columnExists(ctx, db, table, column)
			if err != nil {
				return err
			}
			if !has {
				return fmt.Errorf("target schema is missing %s.%s", table, column)
			}
		}
	}
	var invalidFamily int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM request_logs
		WHERE request_family NOT IN ('claude', 'codex', 'image', 'other')`).Scan(&invalidFamily); err != nil {
		return err
	}
	if invalidFamily != 0 {
		return fmt.Errorf("target request_logs contains %d invalid request_family values", invalidFamily)
	}
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("target database contains foreign key violations")
	}
	return rows.Err()
}

func validateTargetSchemaTx(ctx context.Context, tx *sql.Tx) error {
	for table, forbidden := range map[string][]string{
		"endpoints":    {"channel", "enabled"},
		"request_logs": {"channel", "group_name"},
	} {
		columns, err := tableColumnsTx(ctx, tx, table)
		if err != nil {
			return err
		}
		for _, column := range forbidden {
			if columns[column] {
				return fmt.Errorf("target schema still contains %s.%s", table, column)
			}
		}
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("target database contains foreign key violations")
	}
	return rows.Err()
}

func tableExistsTx(ctx context.Context, tx *sql.Tx, table string) (bool, error) {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func tableColumnsTx(ctx context.Context, tx *sql.Tx, table string) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(`+quoteIdentifier(table)+`)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, dataType string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
