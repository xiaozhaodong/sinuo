package migration

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"regexp"
	"strings"

	timezonepolicy "sinuo/internal/timezone"
)

var (
	createTablePrefix       = regexp.MustCompile(`(?is)^\s*CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(?:"[^"]+"|` + "`[^`]+`" + `|\[[^]]+\]|[^\s(]+)`)
	legacyLocaltimeDefault  = regexp.MustCompile(`(?i)strftime\s*\(\s*'%Y-%m-%d %H:%M:%f'\s*,\s*'now'\s*,\s*'localtime'\s*\)\s*\|\|\s*'\+08:00'`)
	currentTimestampDefault = regexp.MustCompile(`(?i)\bDEFAULT\s+CURRENT_TIMESTAMP\b`)
	currentTimestampAssign  = regexp.MustCompile(`(?i)=\s*CURRENT_TIMESTAMP\b`)
)

func loadMigrationLedger(ctx context.Context, db *sql.DB, migrationID string) (*migrationLedger, error) {
	exists, err := tableExists(ctx, db, "app_schema_migrations")
	if err != nil || !exists {
		return nil, err
	}
	var phase, sourceMode, backupDir, manifest string
	err = db.QueryRowContext(ctx, `SELECT phase, source_mode, backup_dir, backup_manifest_sha256
		FROM app_schema_migrations WHERE migration_id = ?`, migrationID).
		Scan(&phase, &sourceMode, &backupDir, &manifest)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read migration ledger %s: %w", migrationID, err)
	}
	return &migrationLedger{Phase: Phase(phase), SourceMode: SourceMode(sourceMode), BackupDir: backupDir, ManifestSHA256: manifest}, nil
}

func prepareTimezoneLedger(ctx context.Context, db *sql.DB, sourceMode SourceMode, legacyTimezone string, backup *BackupResult) error {
	if backup == nil || backup.Directory == "" || backup.ManifestSHA256 == "" {
		return fmt.Errorf("verified timezone migration backup is required")
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS app_schema_migrations (
		migration_id TEXT PRIMARY KEY,
		phase TEXT NOT NULL CHECK (phase IN ('prepared', 'db_committed', 'config_committed', 'completed', 'failed')),
		source_mode TEXT NOT NULL,
		backup_dir TEXT NOT NULL,
		backup_manifest_sha256 TEXT NOT NULL,
		legacy_timezone TEXT NOT NULL DEFAULT '',
		started_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')),
		db_committed_at DATETIME,
		config_committed_at DATETIME,
		completed_at DATETIME,
		error_message TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		return fmt.Errorf("create timezone migration ledger: %w", err)
	}
	hasLegacyTimezone, err := columnExists(ctx, db, "app_schema_migrations", "legacy_timezone")
	if err != nil {
		return err
	}
	if !hasLegacyTimezone {
		if _, err := db.ExecContext(ctx, `ALTER TABLE app_schema_migrations ADD COLUMN legacy_timezone TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add legacy timezone to migration ledger: %w", err)
		}
	}
	_, err = db.ExecContext(ctx, `INSERT INTO app_schema_migrations (
		migration_id, phase, source_mode, backup_dir, backup_manifest_sha256, legacy_timezone
	) VALUES (?, ?, ?, ?, ?, ?)
	ON CONFLICT(migration_id) DO UPDATE SET
		phase=excluded.phase, source_mode=excluded.source_mode, backup_dir=excluded.backup_dir,
		backup_manifest_sha256=excluded.backup_manifest_sha256, legacy_timezone=excluded.legacy_timezone,
		error_message=''`, TimezoneUTCMigrationID, string(PhasePrepared), string(sourceMode),
		backup.Directory, backup.ManifestSHA256, legacyTimezone)
	if err != nil {
		return fmt.Errorf("record prepared timezone migration: %w", err)
	}
	return nil
}

func updateMigrationLedger(ctx context.Context, db *sql.DB, migrationID string, phase Phase, errorMessage string) error {
	column := ""
	switch phase {
	case PhaseDBCommitted:
		column = ", db_committed_at = strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')"
	case PhaseCompleted:
		column = ", db_committed_at = COALESCE(db_committed_at, strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')), completed_at = strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')"
	}
	_, err := db.ExecContext(ctx, `UPDATE app_schema_migrations SET phase=?, error_message=?`+column+` WHERE migration_id=?`,
		string(phase), errorMessage, migrationID)
	return err
}

func needsTimezoneUTCMigration(ctx context.Context, db *sql.DB) (bool, error) {
	needed, err := needsTimezoneUTCSchemaMigration(ctx, db)
	if err != nil || needed {
		return needed, err
	}
	return needsTimezoneUTCDataMigration(ctx, db)
}

func needsTimezoneUTCSchemaMigration(ctx context.Context, db *sql.DB) (bool, error) {
	for _, spec := range timezoneTableSpecs {
		exists, err := tableExists(ctx, db, spec.name)
		if err != nil {
			return false, err
		}
		if !exists {
			continue
		}
		var schema sql.NullString
		if err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, spec.name).Scan(&schema); err != nil {
			return false, err
		}
		lower := strings.ToLower(schema.String)
		if strings.Contains(lower, "localtime") || strings.Contains(lower, "current_timestamp") {
			return true, nil
		}
	}
	usageExists, err := tableExists(ctx, db, "usage_summary")
	if err != nil {
		return false, err
	}
	if usageExists {
		hasTimezone, err := columnExists(ctx, db, "usage_summary", "timezone_name")
		if err != nil {
			return false, err
		}
		if !hasTimezone {
			return true, nil
		}
	}
	rows, err := db.QueryContext(ctx, `SELECT type, name FROM sqlite_master
		WHERE sql IS NOT NULL AND (lower(sql) LIKE '%localtime%' OR lower(sql) LIKE '%current_timestamp%')`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var objectType, name string
		if err := rows.Scan(&objectType, &name); err != nil {
			return false, err
		}
		if isTimezoneSchemaObject(name) {
			return true, nil
		}
	}
	return false, rows.Err()
}

func needsTimezoneUTCDataMigration(ctx context.Context, db *sql.DB) (bool, error) {
	for _, spec := range timezoneTableSpecs {
		exists, err := tableExists(ctx, db, spec.name)
		if err != nil {
			return false, err
		}
		if !exists {
			continue
		}
		for field := range spec.fields {
			has, err := columnExists(ctx, db, spec.name, field)
			if err != nil {
				return false, err
			}
			if !has {
				continue
			}
			rows, err := db.QueryContext(ctx, `SELECT CAST(`+quoteIdentifier(field)+` AS TEXT) FROM `+quoteIdentifier(spec.name)+
				` WHERE `+quoteIdentifier(field)+` IS NOT NULL AND TRIM(CAST(`+quoteIdentifier(field)+` AS TEXT)) <> ''`)
			if err != nil {
				return false, err
			}
			for rows.Next() {
				var raw string
				if err := rows.Scan(&raw); err != nil {
					rows.Close()
					return false, err
				}
				parsed, err := timezonepolicy.ParseStorage(raw)
				if err != nil || raw != timezonepolicy.FormatStorage(parsed) {
					rows.Close()
					return true, nil
				}
			}
			if err := rows.Close(); err != nil {
				return false, err
			}
		}
	}
	return false, nil
}

func timezoneMigrationNeedReason(ctx context.Context, db *sql.DB) (string, error) {
	for _, spec := range timezoneTableSpecs {
		exists, err := tableExists(ctx, db, spec.name)
		if err != nil || !exists {
			continue
		}
		var schema sql.NullString
		if err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, spec.name).Scan(&schema); err == nil {
			lower := strings.ToLower(schema.String)
			if strings.Contains(lower, "localtime") || strings.Contains(lower, "current_timestamp") {
				return spec.name + " schema contains legacy current-time expression", nil
			}
		}
		for field := range spec.fields {
			has, err := columnExists(ctx, db, spec.name, field)
			if err != nil || !has {
				continue
			}
			rows, err := db.QueryContext(ctx, `SELECT rowid, CAST(`+quoteIdentifier(field)+` AS TEXT) FROM `+quoteIdentifier(spec.name)+
				` WHERE `+quoteIdentifier(field)+` IS NOT NULL AND TRIM(CAST(`+quoteIdentifier(field)+` AS TEXT)) <> ''`)
			if err != nil {
				return "", err
			}
			for rows.Next() {
				var rowID int64
				var raw string
				if err := rows.Scan(&rowID, &raw); err != nil {
					rows.Close()
					return "", err
				}
				parsed, err := timezonepolicy.ParseStorage(raw)
				if err != nil || raw != timezonepolicy.FormatStorage(parsed) {
					rows.Close()
					return fmt.Sprintf("%s.%s row %d is %q", spec.name, field, rowID, raw), nil
				}
			}
			rows.Close()
		}
	}
	rows, err := db.QueryContext(ctx, `SELECT type, name, sql FROM sqlite_master
		WHERE sql IS NOT NULL AND (lower(sql) LIKE '%localtime%' OR lower(sql) LIKE '%current_timestamp%')`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var objectType, name, statement string
		if err := rows.Scan(&objectType, &name, &statement); err != nil {
			return "", err
		}
		if isTimezoneSchemaObject(name) {
			return fmt.Sprintf("%s %s contains legacy current-time expression", objectType, name), nil
		}
	}
	usageExists, _ := tableExists(ctx, db, "usage_summary")
	if usageExists {
		hasTimezone, _ := columnExists(ctx, db, "usage_summary", "timezone_name")
		if !hasTimezone {
			return "usage_summary.timezone_name is missing", nil
		}
	}
	return "unknown timezone migration condition", nil
}

func isTimezoneSchemaObject(name string) bool {
	for _, spec := range timezoneTableSpecs {
		if name == spec.name || strings.HasPrefix(name, "idx_"+spec.name) ||
			strings.HasPrefix(name, "update_"+spec.name) || strings.HasPrefix(name, spec.name+"_") {
			return true
		}
	}
	return name == "usage_summary" || strings.HasPrefix(name, "idx_usage_summary") || strings.HasPrefix(name, "update_usage_summary")
}

func executeTimezoneMigration(ctx context.Context, tx *sql.Tx, preflight *timezonePreflight) error {
	if preflight == nil {
		return fmt.Errorf("timezone migration preflight is required")
	}
	for _, update := range preflight.Updates {
		if _, err := tx.ExecContext(ctx, `UPDATE `+quoteIdentifier(update.table)+` SET `+quoteIdentifier(update.field)+`=? WHERE rowid=?`, update.canonical, update.rowID); err != nil {
			return fmt.Errorf("normalize %s.%s row %d: %w", update.table, update.field, update.rowID, err)
		}
	}

	sequences, err := snapshotSQLiteSequences(ctx, tx)
	if err != nil {
		return err
	}
	for _, table := range []string{"request_logs"} {
		if err := rebuildTablePreservingSchema(ctx, tx, table); err != nil {
			return err
		}
	}
	if err := rebuildUsageSummaryUTC(ctx, tx); err != nil {
		return err
	}
	if err := rebuildEndpointsAndRuntimeUTC(ctx, tx); err != nil {
		return err
	}
	for _, table := range []string{
		"model_pricing", "settings", "upstream_accounts", "privacy_settings",
		"privacy_rules", "privacy_exact_secrets", "app_schema_migrations",
	} {
		if err := rebuildTablePreservingSchema(ctx, tx, table); err != nil {
			return err
		}
	}
	if err := restoreSQLiteSequences(ctx, tx, sequences); err != nil {
		return err
	}
	if err := validateTimezoneMigrationTx(ctx, tx, preflight); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE app_schema_migrations SET phase=?, error_message='',
		db_committed_at=COALESCE(db_committed_at, strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')),
		completed_at=strftime('%Y-%m-%dT%H:%M:%f000Z', 'now') WHERE migration_id=?`,
		string(PhaseCompleted), TimezoneUTCMigrationID); err != nil {
		return fmt.Errorf("mark timezone migration completed: %w", err)
	}
	return nil
}

type schemaObject struct {
	typ string
	sql string
}

func rebuildTablePreservingSchema(ctx context.Context, tx *sql.Tx, table string) error {
	exists, err := tableExistsTx(ctx, tx, table)
	if err != nil || !exists {
		return err
	}
	var createSQL string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&createSQL); err != nil {
		return fmt.Errorf("read %s schema: %w", table, err)
	}
	objects, err := readAuxiliarySchema(ctx, tx, table)
	if err != nil {
		return err
	}
	for _, object := range objects {
		if _, err := tx.ExecContext(ctx, `DROP `+strings.ToUpper(object.typ)+` IF EXISTS `+quoteIdentifier(auxiliaryName(object.sql))); err != nil {
			return fmt.Errorf("drop %s auxiliary schema: %w", table, err)
		}
	}
	temporary := "__timezone_old_" + table
	if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS `+quoteIdentifier(temporary)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE `+quoteIdentifier(table)+` RENAME TO `+quoteIdentifier(temporary)); err != nil {
		return fmt.Errorf("rename %s for timezone migration: %w", table, err)
	}
	createSQL = createTablePrefix.ReplaceAllString(createSQL, `CREATE TABLE `+quoteIdentifier(table))
	createSQL = normalizeTimestampSQL(createSQL)
	if _, err := tx.ExecContext(ctx, createSQL); err != nil {
		return fmt.Errorf("recreate %s with UTC defaults: %w", table, err)
	}
	columns, err := orderedTableColumns(ctx, tx, temporary)
	if err != nil {
		return err
	}
	joined := joinIdentifiers(columns)
	if _, err := tx.ExecContext(ctx, `INSERT INTO `+quoteIdentifier(table)+` (`+joined+`) SELECT `+joined+` FROM `+quoteIdentifier(temporary)); err != nil {
		return fmt.Errorf("copy %s during timezone migration: %w", table, err)
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE `+quoteIdentifier(temporary)); err != nil {
		return fmt.Errorf("drop old %s table: %w", table, err)
	}
	for _, object := range objects {
		if _, err := tx.ExecContext(ctx, normalizeTimestampSQL(object.sql)); err != nil {
			return fmt.Errorf("restore %s auxiliary schema: %w", table, err)
		}
	}
	return nil
}

func readAuxiliarySchema(ctx context.Context, tx *sql.Tx, table string) ([]schemaObject, error) {
	rows, err := tx.QueryContext(ctx, `SELECT type, sql FROM sqlite_master
		WHERE tbl_name=? AND type IN ('index','trigger') AND sql IS NOT NULL ORDER BY type, name`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []schemaObject
	for rows.Next() {
		var item schemaObject
		if err := rows.Scan(&item.typ, &item.sql); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func auxiliaryName(statement string) string {
	fields := strings.Fields(statement)
	for i, field := range fields {
		if strings.EqualFold(field, "INDEX") || strings.EqualFold(field, "TRIGGER") {
			for j := i + 1; j < len(fields); j++ {
				candidate := strings.Trim(fields[j], "`\"[]")
				if strings.EqualFold(candidate, "IF") || strings.EqualFold(candidate, "NOT") || strings.EqualFold(candidate, "EXISTS") {
					continue
				}
				return candidate
			}
		}
	}
	return ""
}

func normalizeTimestampSQL(statement string) string {
	statement = legacyLocaltimeDefault.ReplaceAllString(statement, "strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')")
	statement = currentTimestampDefault.ReplaceAllString(statement, "DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now'))")
	return currentTimestampAssign.ReplaceAllString(statement, "= strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')")
}

func orderedTableColumns(ctx context.Context, tx *sql.Tx, table string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(`+quoteIdentifier(table)+`)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns = append(columns, name)
	}
	return columns, rows.Err()
}

func rebuildEndpointsAndRuntimeUTC(ctx context.Context, tx *sql.Tx) error {
	runtimeExists, err := tableExistsTx(ctx, tx, "endpoint_runtime_states")
	if err != nil {
		return err
	}
	var runtimeCreate string
	var runtimeObjects []schemaObject
	var runtimeColumns []string
	if runtimeExists {
		if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='endpoint_runtime_states'`).Scan(&runtimeCreate); err != nil {
			return err
		}
		runtimeObjects, err = readAuxiliarySchema(ctx, tx, "endpoint_runtime_states")
		if err != nil {
			return err
		}
		runtimeColumns, err = orderedTableColumns(ctx, tx, "endpoint_runtime_states")
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `CREATE TEMP TABLE __timezone_runtime_snapshot AS SELECT * FROM endpoint_runtime_states`); err != nil {
			return fmt.Errorf("snapshot endpoint runtime states: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DROP TABLE endpoint_runtime_states`); err != nil {
			return fmt.Errorf("drop endpoint runtime states: %w", err)
		}
	}
	if err := rebuildTablePreservingSchema(ctx, tx, "endpoints"); err != nil {
		return err
	}
	if !runtimeExists {
		return nil
	}
	runtimeCreate = createTablePrefix.ReplaceAllString(runtimeCreate, `CREATE TABLE "endpoint_runtime_states"`)
	if _, err := tx.ExecContext(ctx, normalizeTimestampSQL(runtimeCreate)); err != nil {
		return fmt.Errorf("recreate endpoint runtime states: %w", err)
	}
	joined := joinIdentifiers(runtimeColumns)
	if _, err := tx.ExecContext(ctx, `INSERT INTO endpoint_runtime_states (`+joined+`) SELECT `+joined+` FROM __timezone_runtime_snapshot`); err != nil {
		return fmt.Errorf("restore endpoint runtime states: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE __timezone_runtime_snapshot`); err != nil {
		return err
	}
	for _, object := range runtimeObjects {
		if _, err := tx.ExecContext(ctx, normalizeTimestampSQL(object.sql)); err != nil {
			return fmt.Errorf("restore endpoint runtime index: %w", err)
		}
	}
	return nil
}

func snapshotSQLiteSequences(ctx context.Context, tx *sql.Tx) (map[string]int64, error) {
	result := make(map[string]int64)
	exists, err := tableExistsTx(ctx, tx, "sqlite_sequence")
	if err != nil || !exists {
		return result, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT name, seq FROM sqlite_sequence`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var seq int64
		if err := rows.Scan(&name, &seq); err != nil {
			return nil, err
		}
		result[name] = seq
	}
	return result, rows.Err()
}

func restoreSQLiteSequences(ctx context.Context, tx *sql.Tx, sequences map[string]int64) error {
	for table, sequence := range sequences {
		result, err := tx.ExecContext(ctx, `UPDATE sqlite_sequence SET seq=MAX(seq, ?) WHERE name=?`, sequence, table)
		if err != nil {
			return err
		}
		updated, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if updated == 0 {
			if _, err := tx.ExecContext(ctx, `INSERT INTO sqlite_sequence(name, seq) VALUES (?, ?)`, table, sequence); err != nil {
				return err
			}
		}
	}
	return nil
}

func rebuildUsageSummaryUTC(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DROP TRIGGER IF EXISTS update_usage_summary_timestamp`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS usage_summary`); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, usageSummaryUTCSchema)
	return err
}

const usageSummaryUTCSchema = `CREATE TABLE usage_summary (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	timezone_name TEXT NOT NULL,
	date TEXT NOT NULL,
	model_name TEXT NOT NULL DEFAULT '',
	request_family TEXT NOT NULL CHECK (request_family IN ('claude','codex','image','other')),
	request_kind TEXT NOT NULL DEFAULT 'legacy' CHECK (request_kind IN ('legacy','generation','compaction')),
	upstream_type TEXT NOT NULL DEFAULT '', upstream_name TEXT NOT NULL DEFAULT '', upstream_id INTEGER NOT NULL DEFAULT 0,
	request_count INTEGER DEFAULT 0, success_count INTEGER DEFAULT 0, error_count INTEGER DEFAULT 0,
	total_input_tokens INTEGER DEFAULT 0, total_output_tokens INTEGER DEFAULT 0,
	total_cache_creation_tokens INTEGER DEFAULT 0, total_cache_read_tokens INTEGER DEFAULT 0,
	total_cost_usd REAL DEFAULT 0, avg_duration_ms REAL DEFAULT 0,
	created_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')),
	updated_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')),
	UNIQUE(timezone_name, date, model_name, request_family, request_kind, upstream_type, upstream_name, upstream_id)
);
CREATE INDEX idx_usage_summary_timezone_date ON usage_summary(timezone_name, date);
CREATE INDEX idx_usage_summary_model ON usage_summary(model_name);
CREATE INDEX idx_usage_summary_family_date ON usage_summary(request_family, date);
CREATE INDEX idx_usage_summary_kind ON usage_summary(request_kind);
CREATE INDEX idx_usage_summary_upstream_date ON usage_summary(upstream_name, date);
CREATE TRIGGER update_usage_summary_timestamp AFTER UPDATE ON usage_summary
	FOR EACH ROW WHEN NEW.updated_at = OLD.updated_at BEGIN
	UPDATE usage_summary SET updated_at=strftime('%Y-%m-%dT%H:%M:%f000Z', 'now') WHERE id=NEW.id;
END;`

func validateTimezoneMigrationTx(ctx context.Context, tx *sql.Tx, before *timezonePreflight) error {
	for table, expected := range before.TableCounts {
		exists, err := tableExistsTx(ctx, tx, table)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("timezone migration dropped table %s", table)
		}
		var actual int64
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+quoteIdentifier(table)).Scan(&actual); err != nil {
			return err
		}
		if actual != expected {
			return fmt.Errorf("%s row count changed: before=%d after=%d", table, expected, actual)
		}
	}
	after, err := readRequestAggregate(ctx, tx)
	if err != nil {
		return err
	}
	if after.Count != before.Requests.Count || after.InputTokens != before.Requests.InputTokens ||
		after.OutputTokens != before.Requests.OutputTokens || after.SuccessCount != before.Requests.SuccessCount ||
		after.FailureCount != before.Requests.FailureCount || math.Abs(after.TotalCost-before.Requests.TotalCost) > 1e-9 {
		return fmt.Errorf("request aggregate changed during timezone migration: before=%+v after=%+v", before.Requests, after)
	}
	for _, spec := range timezoneTableSpecs {
		exists, err := tableExistsTx(ctx, tx, spec.name)
		if err != nil || !exists {
			continue
		}
		for field := range spec.fields {
			has, err := columnExists(ctx, tx, spec.name, field)
			if err != nil || !has {
				continue
			}
			rows, err := tx.QueryContext(ctx, `SELECT rowid, CAST(`+quoteIdentifier(field)+` AS TEXT) FROM `+quoteIdentifier(spec.name)+
				` WHERE `+quoteIdentifier(field)+` IS NOT NULL AND TRIM(CAST(`+quoteIdentifier(field)+` AS TEXT)) <> ''`)
			if err != nil {
				return err
			}
			for rows.Next() {
				var rowID int64
				var raw string
				if err := rows.Scan(&rowID, &raw); err != nil {
					rows.Close()
					return err
				}
				parsed, err := timezonepolicy.ParseStorage(raw)
				if err != nil || raw != timezonepolicy.FormatStorage(parsed) {
					rows.Close()
					return fmt.Errorf("noncanonical UTC after migration: table=%s field=%s row_id=%d", spec.name, field, rowID)
				}
			}
			if err := rows.Close(); err != nil {
				return err
			}
		}
	}
	var summaryCount int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_summary`).Scan(&summaryCount); err != nil {
		return err
	}
	if summaryCount != 0 {
		return fmt.Errorf("usage summary cache must be empty after timezone migration: rows=%d", summaryCount)
	}
	return nil
}
