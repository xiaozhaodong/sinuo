package migration

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	timezonepolicy "sinuo/internal/timezone"
)

type historicalTimeRule int

const (
	historicalConfiguredWall historicalTimeRule = iota
	historicalUTCWall
	historicalAccountCompatibility
)

type timezoneTableSpec struct {
	name   string
	fields map[string]historicalTimeRule
}

var timezoneTableSpecs = []timezoneTableSpec{
	{name: "request_logs", fields: map[string]historicalTimeRule{
		"start_time": historicalConfiguredWall, "end_time": historicalConfiguredWall,
		"route_decision_at": historicalConfiguredWall, "created_at": historicalConfiguredWall,
		"updated_at": historicalConfiguredWall,
	}},
	{name: "endpoints", fields: map[string]historicalTimeRule{
		"created_at": historicalConfiguredWall, "updated_at": historicalConfiguredWall,
	}},
	{name: "endpoint_runtime_states", fields: map[string]historicalTimeRule{
		"cooldown_until": historicalUTCWall, "updated_at": historicalUTCWall,
	}},
	{name: "model_pricing", fields: map[string]historicalTimeRule{
		"created_at": historicalConfiguredWall, "updated_at": historicalConfiguredWall,
	}},
	{name: "settings", fields: map[string]historicalTimeRule{
		"created_at": historicalConfiguredWall, "updated_at": historicalConfiguredWall,
	}},
	{name: "upstream_accounts", fields: map[string]historicalTimeRule{
		"cooldown_until": historicalAccountCompatibility, "last_success_at": historicalAccountCompatibility,
		"quota_5h_reset_at": historicalAccountCompatibility, "quota_weekly_reset_at": historicalAccountCompatibility,
		"quota_refreshed_at": historicalAccountCompatibility, "created_at": historicalAccountCompatibility,
		"updated_at": historicalAccountCompatibility,
	}},
	{name: "privacy_settings", fields: map[string]historicalTimeRule{"updated_at": historicalConfiguredWall}},
	{name: "privacy_rules", fields: map[string]historicalTimeRule{
		"created_at": historicalConfiguredWall, "updated_at": historicalConfiguredWall,
	}},
	{name: "privacy_exact_secrets", fields: map[string]historicalTimeRule{
		"created_at": historicalConfiguredWall, "updated_at": historicalConfiguredWall,
	}},
	{name: "app_schema_migrations", fields: map[string]historicalTimeRule{
		"started_at": historicalUTCWall, "db_committed_at": historicalUTCWall,
		"config_committed_at": historicalUTCWall, "completed_at": historicalUTCWall,
	}},
}

type timezoneValueUpdate struct {
	table     string
	field     string
	rowID     int64
	canonical string
}

type requestAggregate struct {
	Count        int64
	InputTokens  int64
	OutputTokens int64
	SuccessCount int64
	FailureCount int64
	TotalCost    float64
}

type timezonePreflight struct {
	LegacyTimezone string
	Updates        []timezoneValueUpdate
	TableCounts    map[string]int64
	Formats        map[string]map[string]int64
	Requests       requestAggregate
}

func (c *Coordinator) runTimezoneUTCMigration(ctx context.Context, legacy *LegacyConfig, status Status) (Status, error) {
	status.MigrationID = TimezoneUTCMigrationID
	status.Error = ""
	status.RetryAllowed = false
	c.setStatus(status)

	ledger, err := loadMigrationLedger(ctx, c.DB, TimezoneUTCMigrationID)
	if err != nil {
		return c.failForMigration(status, TimezoneUTCMigrationID, err)
	}
	if ledger != nil && ledger.Phase == PhaseCompleted {
		needed, err := needsTimezoneUTCSchemaMigration(ctx, c.DB)
		if err != nil {
			return c.failForMigration(status, TimezoneUTCMigrationID, err)
		}
		if needed {
			return c.failForMigration(status, TimezoneUTCMigrationID,
				fmt.Errorf("timezone migration ledger is completed but legacy schema remains"))
		}
		status.State = StartupReady
		status.Phase = PhaseCompleted
		status.BackupDir = ledger.BackupDir
		status.ManifestSHA256 = ledger.ManifestSHA256
		status.DatabaseIntegrity = "ok"
		c.setStatus(status)
		return status, nil
	}
	needed, err := needsTimezoneUTCMigration(ctx, c.DB)
	if err != nil {
		return c.failForMigration(status, TimezoneUTCMigrationID, err)
	}
	if !needed {
		status.State = StartupReady
		status.Phase = PhaseCompleted
		c.setStatus(status)
		return status, nil
	}

	// 失败重试时优先使用 manifest 已固定的时区口径，避免依赖当前系统探测。
	var backup *BackupResult
	var legacyTimezone string
	if ledger != nil && strings.TrimSpace(ledger.BackupDir) != "" {
		manifest, err := readVerifiedBackupManifest(ledger.BackupDir, ledger.ManifestSHA256, TimezoneUTCMigrationID)
		if err != nil {
			return c.failForMigration(status, TimezoneUTCMigrationID, fmt.Errorf("verify timezone migration backup: %w", err))
		}
		if strings.TrimSpace(manifest.LegacyTimezone) == "" {
			return c.failForMigration(status, TimezoneUTCMigrationID, fmt.Errorf("timezone migration backup does not pin legacy timezone"))
		}
		legacyTimezone = manifest.LegacyTimezone
		backup = &BackupResult{Directory: ledger.BackupDir, ManifestSHA256: ledger.ManifestSHA256, IntegrityCheck: manifest.IntegrityCheck}
	} else {
		legacyTimezone, err = legacy.LegacyTrackingTimezone()
		if err != nil {
			return c.failForMigration(status, TimezoneUTCMigrationID, fmt.Errorf("resolve legacy tracking timezone: %w", err))
		}
		preflight, err := inspectTimezoneData(ctx, c.DB, legacyTimezone)
		if err != nil {
			return c.failForMigration(status, TimezoneUTCMigrationID, err)
		}
		c.logTimezonePreflight(preflight)
		if c.DatabaseExisted {
			if err := ensureMigrationDiskSpace(ctx, c.DB); err != nil {
				return c.failForMigration(status, TimezoneUTCMigrationID, err)
			}
		}
		backup, err = CreateMigrationBackup(ctx, BackupOptions{
			DB: c.DB, MigrationID: TimezoneUTCMigrationID, DirectorySlug: "20260804-timezone-utc",
			LegacyTimezone: legacyTimezone, DatabasePath: c.DatabasePath, ConfigPath: c.ConfigPath,
			DataDir: c.DataDir, SourceMode: legacy.SourceMode, DatabaseExisted: c.DatabaseExisted, Now: c.Now,
		})
		if err != nil {
			return c.failForMigration(status, TimezoneUTCMigrationID, err)
		}
		if err := prepareTimezoneLedger(ctx, c.DB, legacy.SourceMode, legacyTimezone, backup); err != nil {
			return c.failForMigration(status, TimezoneUTCMigrationID, err)
		}
	}

	status.State = StartupMigrating
	status.Phase = PhasePrepared
	status.BackupDir = backup.Directory
	status.BackupIntegrity = backup.IntegrityCheck
	status.ManifestSHA256 = backup.ManifestSHA256
	c.setStatus(status)

	preflight, err := inspectTimezoneData(ctx, c.DB, legacyTimezone)
	if err != nil {
		_ = updateMigrationLedger(ctx, c.DB, TimezoneUTCMigrationID, PhaseFailed, sanitizeError(err))
		return c.failForMigration(status, TimezoneUTCMigrationID, err)
	}
	activePolicy, err := timezonepolicy.New(legacy.EffectiveGlobalTimezone())
	if err != nil {
		return c.failForMigration(status, TimezoneUTCMigrationID, err)
	}
	err = withForeignKeysDisabled(ctx, c.DB, func(tx *sql.Tx) error {
		return executeTimezoneMigration(ctx, tx, preflight)
	})
	if err != nil {
		_ = updateMigrationLedger(ctx, c.DB, TimezoneUTCMigrationID, PhaseFailed, sanitizeError(err))
		return c.failForMigration(status, TimezoneUTCMigrationID, err)
	}
	if needed, err := needsTimezoneUTCMigration(ctx, c.DB); err != nil {
		return c.failForMigration(status, TimezoneUTCMigrationID, err)
	} else if needed {
		reason, _ := timezoneMigrationNeedReason(ctx, c.DB)
		return c.failForMigration(status, TimezoneUTCMigrationID, fmt.Errorf("timezone migration post-check still finds noncanonical schema or data: %s", reason))
	}

	status.State = StartupReady
	status.Phase = PhaseCompleted
	status.DatabaseIntegrity = "ok"
	status.RetryAllowed = false
	c.setStatus(status)
	if c.Logger != nil {
		c.Logger.Info("UTC 时间规范化迁移完成", "migration_id", TimezoneUTCMigrationID,
			"legacy_timezone", legacyTimezone, "active_timezone", activePolicy.Name(), "backup_dir", backup.Directory)
	}
	return status, nil
}

func inspectTimezoneData(ctx context.Context, db *sql.DB, legacyTimezone string) (*timezonePreflight, error) {
	policy, err := timezonepolicy.New(legacyTimezone)
	if err != nil {
		return nil, fmt.Errorf("load legacy tracking timezone: %w", err)
	}
	endpointFlattenNaiveUTC, err := endpointFlattenNaiveAuditIsUTC(ctx, db)
	if err != nil {
		return nil, err
	}
	result := &timezonePreflight{
		LegacyTimezone: legacyTimezone,
		TableCounts:    make(map[string]int64),
		Formats:        make(map[string]map[string]int64),
	}
	for _, spec := range timezoneTableSpecs {
		exists, err := tableExists(ctx, db, spec.name)
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		var tableCount int64
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+quoteIdentifier(spec.name)).Scan(&tableCount); err != nil {
			return nil, fmt.Errorf("count %s rows: %w", spec.name, err)
		}
		result.TableCounts[spec.name] = tableCount
		fields := make([]string, 0, len(spec.fields))
		for field := range spec.fields {
			fields = append(fields, field)
		}
		sort.Strings(fields)
		for _, field := range fields {
			has, err := columnExists(ctx, db, spec.name, field)
			if err != nil {
				return nil, err
			}
			if !has {
				continue
			}
			usesCurrentTimestamp, err := columnDefaultUsesCurrentTimestamp(ctx, db, spec.name, field)
			if err != nil {
				return nil, err
			}
			query := `SELECT rowid, CAST(` + quoteIdentifier(field) + ` AS TEXT) FROM ` + quoteIdentifier(spec.name) +
				` WHERE ` + quoteIdentifier(field) + ` IS NOT NULL AND TRIM(CAST(` + quoteIdentifier(field) + ` AS TEXT)) <> ''`
			rows, err := db.QueryContext(ctx, query)
			if err != nil {
				return nil, fmt.Errorf("scan %s.%s: %w", spec.name, field, err)
			}
			for rows.Next() {
				var rowID int64
				var raw string
				if err := rows.Scan(&rowID, &raw); err != nil {
					rows.Close()
					return nil, err
				}
				naiveUTCClassification := ""
				if usesCurrentTimestamp {
					naiveUTCClassification = "naive_current_timestamp"
				} else if endpointFlattenNaiveUTC && spec.name == "endpoints" && (field == "created_at" || field == "updated_at") {
					naiveUTCClassification = "endpoint_flatten_current_timestamp"
				}
				parsed, classification, err := parseHistoricalColumnTime(raw, policy, spec.fields[field], naiveUTCClassification)
				if result.Formats[spec.name+"."+field] == nil {
					result.Formats[spec.name+"."+field] = make(map[string]int64)
				}
				result.Formats[spec.name+"."+field][classification]++
				if err != nil {
					rows.Close()
					return nil, fmt.Errorf("unrecognized historical time: table=%s field=%s row_id=%d format=%s: %w",
						spec.name, field, rowID, classification, err)
				}
				canonical := timezonepolicy.FormatStorage(parsed)
				if strings.TrimSpace(raw) != canonical {
					result.Updates = append(result.Updates, timezoneValueUpdate{table: spec.name, field: field, rowID: rowID, canonical: canonical})
				}
			}
			if err := rows.Close(); err != nil {
				return nil, err
			}
			if err := rows.Err(); err != nil {
				return nil, err
			}
		}
	}
	result.Requests, err = readRequestAggregate(ctx, db)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func endpointFlattenNaiveAuditIsUTC(ctx context.Context, db *sql.DB) (bool, error) {
	ledger, err := loadMigrationLedger(ctx, db, EndpointFlattenMigrationID)
	if err != nil {
		return false, err
	}
	return ledger != nil && ledger.Phase == PhaseCompleted && ledger.SourceMode == SourceModeYAML, nil
}

func normalizeCurrentTimestampAuditColumns(ctx context.Context, tx *sql.Tx, table string) error {
	exists, err := tableExistsTx(ctx, tx, table)
	if err != nil || !exists {
		return err
	}
	for _, field := range []string{"created_at", "updated_at"} {
		usesCurrentTimestamp, err := columnDefaultUsesCurrentTimestamp(ctx, tx, table, field)
		if err != nil {
			return err
		}
		if !usesCurrentTimestamp {
			continue
		}
		rows, err := tx.QueryContext(ctx, `SELECT rowid, CAST(`+quoteIdentifier(field)+` AS TEXT) FROM `+quoteIdentifier(table)+
			` WHERE `+quoteIdentifier(field)+` IS NOT NULL AND TRIM(CAST(`+quoteIdentifier(field)+` AS TEXT)) <> ''`)
		if err != nil {
			return fmt.Errorf("scan CURRENT_TIMESTAMP values from %s.%s: %w", table, field, err)
		}
		var updates []timezoneValueUpdate
		for rows.Next() {
			var rowID int64
			var raw string
			if err := rows.Scan(&rowID, &raw); err != nil {
				rows.Close()
				return err
			}
			if _, err := timezonepolicy.ParseStorage(strings.TrimSpace(raw)); err == nil {
				continue
			}
			parsed, ok := parseNaiveUTCWall(raw)
			if !ok {
				continue
			}
			updates = append(updates, timezoneValueUpdate{
				table: table, field: field, rowID: rowID, canonical: timezonepolicy.FormatStorage(parsed),
			})
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, update := range updates {
			if _, err := tx.ExecContext(ctx, `UPDATE `+quoteIdentifier(update.table)+` SET `+quoteIdentifier(update.field)+`=? WHERE rowid=?`,
				update.canonical, update.rowID); err != nil {
				return fmt.Errorf("normalize CURRENT_TIMESTAMP value %s.%s rowid=%d: %w", update.table, update.field, update.rowID, err)
			}
		}
	}
	return nil
}

func columnDefaultUsesCurrentTimestamp(ctx context.Context, db queryContexter, table, field string) (bool, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+quoteIdentifier(table)+`)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, err
		}
		if name != field || defaultValue == nil {
			continue
		}
		return strings.Contains(strings.ToUpper(fmt.Sprint(defaultValue)), "CURRENT_TIMESTAMP"), nil
	}
	return false, rows.Err()
}

func parseHistoricalColumnTime(raw string, policy *timezonepolicy.Policy, rule historicalTimeRule, naiveUTCClassification string) (time.Time, string, error) {
	if naiveUTCClassification != "" {
		if parsed, ok := parseNaiveUTCWall(raw); ok {
			return parsed, naiveUTCClassification, nil
		}
	}
	return parseHistoricalTime(raw, policy, rule)
}

func parseHistoricalTime(raw string, policy *timezonepolicy.Policy, rule historicalTimeRule) (time.Time, string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return time.Time{}, "empty", fmt.Errorf("empty time")
	}
	if rule == historicalAccountCompatibility && strings.Contains(value, "T") && strings.HasSuffix(value, "Z") {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return time.Time{}, "account_legacy_z", err
		}
		fixed := time.FixedZone("UTC+8", 8*60*60)
		return time.Date(parsed.Year(), parsed.Month(), parsed.Day(), parsed.Hour(), parsed.Minute(), parsed.Second(), parsed.Nanosecond(), fixed).UTC(), "account_legacy_z", nil
	}
	if parsed, err := timezonepolicy.ParseStorage(value); err == nil {
		return parsed.UTC(), "explicit_offset", nil
	}
	if parsed, ok := parseGoStringTime(value); ok {
		return parsed.UTC(), "go_string_time", nil
	}
	if rule == historicalUTCWall {
		if parsed, ok := parseNaiveUTCWall(value); ok {
			return parsed, "naive_utc", nil
		}
		return time.Time{}, "unknown", fmt.Errorf("unsupported UTC wall time %q", value)
	}
	parsed, err := policy.ParseInput(value)
	if err != nil {
		return time.Time{}, "unknown", err
	}
	if rule == historicalAccountCompatibility {
		return parsed.UTC(), "account_wall", nil
	}
	return parsed.UTC(), "configured_wall", nil
}

// parseGoStringTime 兼容历史版本直接以 time.Time 作为 SQL 参数写库产生的
// Go String() 格式（如 "2026-04-30 00:49:14.502607 +0800 CST"）。
// 该格式自带显式偏移，转 UTC 语义确定，不依赖配置时区。
func parseGoStringTime(raw string) (time.Time, bool) {
	value := strings.TrimSpace(raw)
	for _, layout := range []string{"2006-01-02 15:04:05.999999999 -0700 MST", "2006-01-02 15:04:05 -0700 MST"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

func parseNaiveUTCWall(raw string) (time.Time, bool) {
	value := strings.TrimSpace(raw)
	for _, layout := range []string{"2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05", "2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05"} {
		if parsed, err := time.ParseInLocation(layout, value, time.UTC); err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

func readRequestAggregate(ctx context.Context, db queryRower) (requestAggregate, error) {
	var result requestAggregate
	exists, err := tableExists(ctx, db, "request_logs")
	if err != nil || !exists {
		return result, err
	}
	err = db.QueryRowContext(ctx, `SELECT COUNT(*),
		COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		COALESCE(SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status IN ('failed','error','auth_error','rate_limited','server_error','network_error','stream_error','timeout') THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(total_cost_usd), 0) FROM request_logs`).Scan(
		&result.Count, &result.InputTokens, &result.OutputTokens, &result.SuccessCount, &result.FailureCount, &result.TotalCost)
	if err != nil {
		return result, fmt.Errorf("read request aggregate: %w", err)
	}
	return result, nil
}

func (c *Coordinator) logTimezonePreflight(preflight *timezonePreflight) {
	if c.Logger == nil || preflight == nil {
		return
	}
	c.Logger.Info("UTC 时间迁移预检完成", "migration_id", TimezoneUTCMigrationID,
		"legacy_timezone", preflight.LegacyTimezone, "converted_values", len(preflight.Updates),
		"request_count", preflight.Requests.Count, "format_counts", preflight.Formats)
	legacyAccountZ := int64(0)
	for field, formats := range preflight.Formats {
		if strings.HasPrefix(field, "upstream_accounts.") {
			legacyAccountZ += formats["account_legacy_z"]
		}
	}
	if legacyAccountZ > 0 {
		c.Logger.Warn("账号池存在按旧 UTC+8 墙上时间解释的 T...Z 值，正式迁移前需在隔离副本核对可见时刻",
			"migration_id", TimezoneUTCMigrationID, "value_count", legacyAccountZ)
	}
}
