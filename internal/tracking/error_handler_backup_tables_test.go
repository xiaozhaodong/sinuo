package tracking

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

const backupTestTimestamp = "2026-06-23T00:51:00.000000Z"

func TestCreateBackup_PreservesEntireDatabase(t *testing.T) {
	tracker := newBackupTestTracker(t)
	seedBackupTestDatabase(t, tracker.db)
	backupPath := tracker.config.DatabasePath + ".backup"
	writeBackupTestFile(t, backupPath+".tmp", "stale temporary backup")
	writeBackupTestFile(t, backupPath, "previous backup")

	if err := tracker.errorHandler.CreateBackup(); err != nil {
		t.Fatalf("failed to create backup: %v", err)
	}
	assertBackupTestFileMissing(t, backupPath+".tmp")
	info, err := os.Stat(backupPath)
	if err != nil {
		t.Fatalf("backup file missing: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("expected backup permissions 0600, got %04o", info.Mode().Perm())
	}

	backupDB := openBackupTestDatabase(t, backupPath)
	sourceSchema := backupTestSchemaObjects(t, tracker.db)
	backupSchema := backupTestSchemaObjects(t, backupDB)
	if !reflect.DeepEqual(sourceSchema, backupSchema) {
		t.Fatalf("backup schema differs from source: source=%v, backup=%v", sourceSchema, backupSchema)
	}
	assertBackupTestData(t, backupDB)
	assertBackupTestTimestamps(t, backupDB)
}

func newBackupTestTracker(t *testing.T) *UsageTracker {
	t.Helper()
	// 同时覆盖真实目录中的空格和 VACUUM INTO 路径中的单引号。
	dbPath := filepath.Join(t.TempDir(), "Application Support", "Sinuo's test", "usage.db")
	tracker, err := NewUsageTracker(&Config{
		Enabled:         true,
		DatabasePath:    dbPath,
		BufferSize:      100,
		BatchSize:       10,
		FlushInterval:   50 * time.Millisecond,
		MaxRetry:        3,
		CleanupInterval: 24 * time.Hour,
		RetentionDays:   30,
	})
	if err != nil {
		t.Fatalf("failed to create tracker: %v", err)
	}
	t.Cleanup(func() {
		if err := tracker.Close(); err != nil {
			t.Errorf("failed to close tracker: %v", err)
		}
	})
	return tracker
}

func seedBackupTestDatabase(t *testing.T, db *sql.DB) {
	t.Helper()
	statements := []string{
		`INSERT INTO request_logs (request_id, start_time, end_time, route_decision_at, created_at, updated_at, status)
		 VALUES ('req-backup-1', '2026-06-23T00:51:00.000000Z', '2026-06-23T00:51:01.123456Z',
		 '2026-06-23T00:51:00.654321Z', '2026-06-23T00:51:00.000001Z', '2026-06-23T00:51:01.999999Z', 'completed')`,
		`INSERT INTO usage_summary (timezone_name, date, model_name, request_family, request_count, total_cost_usd)
		 VALUES ('Asia/Shanghai', '2026-06-23', 'backup-model', 'codex', 1, 1.25)`,
		`INSERT INTO upstream_accounts (id, account_name, credential_raw, fingerprint)
		 VALUES (10, 'account-1', 'test-only-credential', 'fp-1')`,
		`INSERT INTO endpoints (name, url) VALUES ('backup-endpoint', 'https://example.invalid')`,
		`INSERT INTO settings (category, key, value) VALUES ('backup', 'test-key', 'test-value')`,
		`INSERT INTO model_pricing (model_name, input_price) VALUES ('backup-model', 2.5)`,
		// 模拟不在内置 schema.sql 中的迁移表与扩展对象。
		`CREATE TABLE app_schema_migrations (migration_id TEXT PRIMARY KEY, status TEXT NOT NULL)`,
		`INSERT INTO app_schema_migrations VALUES ('backup-test-migration', 'completed')`,
		`CREATE TABLE backup_extra (id INTEGER PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE INDEX idx_backup_extra_value ON backup_extra(value)`,
		`CREATE TABLE backup_extra_audit (value TEXT NOT NULL)`,
		`CREATE TRIGGER backup_extra_insert AFTER INSERT ON backup_extra
		 BEGIN INSERT INTO backup_extra_audit VALUES (NEW.value); END`,
		`CREATE VIEW backup_extra_view AS SELECT value FROM backup_extra`,
		`INSERT INTO backup_extra VALUES (1, 'extra-value')`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(context.Background(), statement); err != nil {
			t.Fatalf("failed to seed backup database: %v", err)
		}
	}
}

func assertBackupTestData(t *testing.T, db *sql.DB) {
	t.Helper()
	queries := map[string]string{
		`SELECT request_id FROM request_logs`:                    "req-backup-1",
		`SELECT CAST(total_cost_usd AS TEXT) FROM usage_summary`: "1.25",
		`SELECT account_name FROM upstream_accounts`:             "account-1",
		`SELECT name FROM endpoints`:                             "backup-endpoint",
		`SELECT value FROM settings WHERE category = 'backup'`:   "test-value",
		`SELECT CAST(input_price AS TEXT) FROM model_pricing`:    "2.5",
		`SELECT status FROM app_schema_migrations`:               "completed",
		`SELECT value FROM backup_extra`:                         "extra-value",
		`SELECT value FROM backup_extra_audit`:                   "extra-value",
		`SELECT value FROM backup_extra_view`:                    "extra-value",
	}
	for query, expected := range queries {
		var actual string
		if err := db.QueryRowContext(context.Background(), query).Scan(&actual); err != nil {
			t.Fatalf("failed to query backup data: %v", err)
		}
		if actual != expected {
			t.Fatalf("backup data mismatch for %s: expected %q, got %q", query, expected, actual)
		}
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM backup_extra").Scan(&count); err != nil || count != 1 {
		t.Fatalf("expected exactly one extra row, got %d: %v", count, err)
	}
}

func assertBackupTestTimestamps(t *testing.T, db *sql.DB) {
	t.Helper()
	columns := map[string]string{
		"start_time":        backupTestTimestamp,
		"end_time":          "2026-06-23T00:51:01.123456Z",
		"route_decision_at": "2026-06-23T00:51:00.654321Z",
		"created_at":        "2026-06-23T00:51:00.000001Z",
		"updated_at":        "2026-06-23T00:51:01.999999Z",
	}
	for column, expected := range columns {
		var actual string
		query := "SELECT CAST(" + column + " AS TEXT) FROM request_logs WHERE request_id = ?"
		if err := db.QueryRow(query, "req-backup-1").Scan(&actual); err != nil {
			t.Fatalf("failed to query %s: %v", column, err)
		}
		if actual != expected {
			t.Fatalf("%s changed in backup: expected %q, got %q", column, expected, actual)
		}
	}
}

func backupTestSchemaObjects(t *testing.T, db *sql.DB) [][2]string {
	t.Helper()
	rows, err := db.Query("SELECT type, name FROM sqlite_master ORDER BY type, name")
	if err != nil {
		t.Fatalf("failed to query schema objects: %v", err)
	}
	defer rows.Close()
	var objects [][2]string
	for rows.Next() {
		var object [2]string
		if err := rows.Scan(&object[0], &object[1]); err != nil {
			t.Fatalf("failed to scan schema object: %v", err)
		}
		objects = append(objects, object)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("failed to read schema objects: %v", err)
	}
	return objects
}

func TestCreateBackup_DoesNotWaitForUncommittedWrite(t *testing.T) {
	tracker := newBackupTestTracker(t)
	if tracker.db != tracker.GetWriteDB() || tracker.db.Stats().MaxOpenConnections != 1 {
		t.Fatal("regression test requires the tracker's shared single write connection")
	}
	tx, err := tracker.GetWriteDB().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("failed to begin write transaction: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO request_logs (request_id, start_time)
		VALUES ('req-uncommitted', '2026-06-23T00:51:00.000000Z')`); err != nil {
		t.Fatalf("failed to insert uncommitted row: %v", err)
	}
	backupDone := make(chan error, 1)
	go func() { backupDone <- tracker.errorHandler.CreateBackup() }()
	select {
	case err := <-backupDone:
		if err != nil {
			t.Fatalf("failed to back up with an open write transaction: %v", err)
		}
	case <-time.After(3 * time.Second):
		// 释放写连接并等待备份退出，避免失败后的清理也被卡住。
		_ = tx.Rollback()
		select {
		case <-backupDone:
		case <-time.After(3 * time.Second):
			t.Error("backup did not exit after releasing the write transaction")
		}
		t.Fatal("backup waited for the tracker's uncommitted write transaction")
	}
	backupDB := openBackupTestDatabase(t, tracker.config.DatabasePath+".backup")
	assertBackupTestRequestCount(t, backupDB, "req-uncommitted", 0)
	if err := tx.Commit(); err != nil {
		t.Fatalf("failed to commit write transaction after backup: %v", err)
	}
	assertBackupTestRequestCount(t, tracker.db, "req-uncommitted", 1)
}

func TestCreateBackup_RejectsNonFileDatabasePath(t *testing.T) {
	tracker := newBackupTestTracker(t)
	for _, dbPath := range []string{"", ":memory:"} {
		t.Run(dbPath, func(t *testing.T) {
			tracker.config.DatabasePath = dbPath
			if err := tracker.errorHandler.CreateBackup(); err == nil {
				t.Fatal("expected backup to reject a non-file database path")
			}
		})
	}
}

func TestCreateBackup_SnapshotFailurePreservesPreviousBackup(t *testing.T) {
	tracker := newBackupTestTracker(t)
	dbPath := filepath.Join(filepath.Dir(tracker.config.DatabasePath), "missing.db")
	tracker.config.DatabasePath = dbPath
	backupPath := dbPath + ".backup"
	writeBackupTestFile(t, backupPath, "previous backup")
	writeBackupTestFile(t, backupPath+".tmp", "stale temporary backup")
	if err := tracker.errorHandler.CreateBackup(); err == nil || !strings.Contains(err.Error(), "snapshot") {
		t.Fatalf("expected a snapshot failure with context, got %v", err)
	}
	assertBackupTestFileContent(t, backupPath, "previous backup")
	assertBackupTestFileMissing(t, backupPath+".tmp")
	assertBackupTestFileMissing(t, dbPath)
}

func TestCreateBackup_FinalizeFailureRemovesTemporaryBackup(t *testing.T) {
	tracker := newBackupTestTracker(t)
	backupPath := tracker.config.DatabasePath + ".backup"
	if err := os.Mkdir(backupPath, 0o700); err != nil {
		t.Fatalf("failed to create conflicting backup directory: %v", err)
	}
	previousPath := filepath.Join(backupPath, "previous")
	writeBackupTestFile(t, previousPath, "previous backup")
	if err := tracker.errorHandler.CreateBackup(); err == nil || !strings.Contains(err.Error(), "finalize") {
		t.Fatalf("expected a finalization failure with context, got %v", err)
	}
	assertBackupTestFileContent(t, previousPath, "previous backup")
	assertBackupTestFileMissing(t, backupPath+".tmp")
}

func TestValidateSQLiteBackup_RejectsInvalidFile(t *testing.T) {
	backupPath := filepath.Join(t.TempDir(), "invalid.backup.tmp")
	writeBackupTestFile(t, backupPath, "not a SQLite database")
	if err := validateSQLiteBackup(context.Background(), backupPath); err == nil {
		t.Fatal("expected backup integrity validation to fail")
	}
}

func openBackupTestDatabase(t *testing.T, backupPath string) *sql.DB {
	t.Helper()
	// 自动文件级恢复已停用：直接只读打开备份核对内容。
	db, err := sql.Open("sqlite", "file:"+backupPath+"?mode=ro")
	if err != nil {
		t.Fatalf("failed to open backup read-only: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("failed to close backup database: %v", err)
		}
	})
	return db
}

func assertBackupTestRequestCount(t *testing.T, db *sql.DB, requestID string, expected int) {
	t.Helper()
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM request_logs WHERE request_id = ?", requestID).Scan(&count); err != nil {
		t.Fatalf("failed to query request count: %v", err)
	}
	if count != expected {
		t.Fatalf("expected %d rows for %s, got %d", expected, requestID, count)
	}
}

func writeBackupTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write backup fixture: %v", err)
	}
}

func assertBackupTestFileContent(t *testing.T, path, expected string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read backup fixture: %v", err)
	}
	if string(content) != expected {
		t.Fatalf("previous backup changed: expected %q, got %q", expected, content)
	}
}

func assertBackupTestFileMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected file %s to be absent, got %v", path, err)
	}
}
