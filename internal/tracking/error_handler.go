package tracking

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrorHandler handles errors and provides recovery mechanisms
type ErrorHandler struct {
	tracker *UsageTracker
	logger  *slog.Logger
}

// NewErrorHandler creates a new error handler
func NewErrorHandler(tracker *UsageTracker, logger *slog.Logger) *ErrorHandler {
	return &ErrorHandler{
		tracker: tracker,
		logger:  logger,
	}
}

// HandleDatabaseError handles database-related errors with recovery attempts
func (eh *ErrorHandler) HandleDatabaseError(err error, operation string) bool {
	if err == nil {
		return true
	}

	eh.logger.Error("Database operation failed",
		"operation", operation,
		"error", err.Error())

	// 尝试诊断错误类型
	switch {
	case isDiskSpaceError(err):
		return eh.handleDiskSpaceError()
	case isDatabaseCorruptionError(err):
		return eh.handleCorruptionError()
	case isDatabaseLockedError(err):
		return eh.handleLockError()
	case isConnectionError(err):
		return eh.handleConnectionError()
	default:
		eh.logger.Warn("Unknown database error type", "error", err)
		return false
	}
}

// handleDiskSpaceError handles disk space issues
func (eh *ErrorHandler) handleDiskSpaceError() bool {
	eh.logger.Warn("Disk space error detected, attempting cleanup...")

	// 尝试清理旧数据
	if eh.tracker != nil {
		if err := eh.tracker.cleanupOldRecords(); err != nil {
			eh.logger.Error("Emergency cleanup failed", "error", err)
			return false
		}
		eh.logger.Info("Emergency cleanup completed")
		return true
	}

	return false
}

// handleCorruptionError handles database corruption
func (eh *ErrorHandler) handleCorruptionError() bool {
	eh.logger.Error("Database corruption detected, attempting recovery...")

	if eh.tracker == nil || eh.tracker.db == nil {
		return false
	}

	// 尝试数据库完整性检查
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var integrityResult string
	err := eh.tracker.db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrityResult)
	if err != nil {
		eh.logger.Error("Integrity check failed", "error", err)
		return false
	}

	if integrityResult != "ok" {
		// 不自动用备份覆盖：两个连接池都开着库时替换文件不安全，WAL 下残留的 -wal 还会套到新文件上。
		// 需要恢复时先退出 App，再手动用 usage.db.backup 替换（同时删除 -wal / -shm）
		eh.logger.Error("Database integrity compromised, manual recovery required", "result", integrityResult)
		return false
	}

	eh.logger.Info("Database integrity check passed")
	return true
}

// handleLockError handles database lock issues
func (eh *ErrorHandler) handleLockError() bool {
	eh.logger.Warn("Database lock detected, waiting for release...")

	// 等待锁释放
	for i := 0; i < 10; i++ {
		time.Sleep(time.Duration(i+1) * 100 * time.Millisecond)

		// 尝试简单查询测试连接
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		var count int
		err := eh.tracker.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master").Scan(&count)
		cancel()

		if err == nil {
			eh.logger.Info("Database lock released", "attempts", i+1)
			return true
		}
	}

	eh.logger.Error("Database lock timeout exceeded")
	return false
}

// handleConnectionError handles connection issues
func (eh *ErrorHandler) handleConnectionError() bool {
	eh.logger.Warn("Database connection error, attempting reconnection...")

	if eh.tracker == nil || eh.tracker.config == nil {
		return false
	}

	// 尝试重新连接
	return eh.attemptReconnection()
}

// attemptReconnection attempts to reconnect to the database
func (eh *ErrorHandler) attemptReconnection() bool {
	for attempt := 1; attempt <= 3; attempt++ {
		eh.logger.Info("Attempting database reconnection", "attempt", attempt)

		if err := eh.tracker.reconnectDatabases(); err != nil {
			eh.logger.Error("Reconnection failed", "attempt", attempt, "error", err)
			time.Sleep(time.Duration(attempt) * time.Second)
			continue
		}

		eh.logger.Info("Database reconnection successful")
		return true
	}

	eh.logger.Error("All reconnection attempts failed")
	return false
}

// CreateBackup creates a backup of the current database
func (eh *ErrorHandler) CreateBackup() error {
	if eh.tracker == nil || eh.tracker.db == nil {
		return fmt.Errorf("tracker or database not initialized")
	}

	dbPath := eh.tracker.config.DatabasePath
	backupPath := dbPath + ".backup"
	tempBackupPath := backupPath + ".tmp"

	// 创建备份目录
	if err := os.MkdirAll(filepath.Dir(tempBackupPath), 0755); err != nil {
		return fmt.Errorf("failed to create backup directory: %w", err)
	}

	// 使用SQLite的备份API
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// 创建临时备份
	backupDB, err := sql.Open("sqlite", tempBackupPath)
	if err != nil {
		return fmt.Errorf("failed to create backup database: %w", err)
	}
	defer backupDB.Close()

	// 简化的备份方案：导出和导入数据
	if err := eh.performSimpleBackup(ctx, backupDB); err != nil {
		os.Remove(tempBackupPath)
		return fmt.Errorf("backup operation failed: %w", err)
	}

	// 原子性地移动备份文件
	if err := os.Rename(tempBackupPath, backupPath); err != nil {
		os.Remove(tempBackupPath)
		return fmt.Errorf("failed to finalize backup: %w", err)
	}

	eh.logger.Info("Database backup created successfully", "backup_path", backupPath)
	return nil
}

// performSimpleBackup performs a simple backup by recreating schema and copying data
func (eh *ErrorHandler) performSimpleBackup(ctx context.Context, backupDB *sql.DB) error {
	schemaSQL, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		return fmt.Errorf("failed to read backup schema: %w", err)
	}
	if _, err := backupDB.ExecContext(ctx, string(schemaSQL)); err != nil {
		return fmt.Errorf("failed to create backup schema: %w", err)
	}

	tables := []string{
		"request_logs",
		"usage_summary",
		"upstream_accounts",
	}

	totalRows := 0
	for _, table := range tables {
		rowsCopied, err := eh.copyTableData(ctx, eh.tracker.db, backupDB, table)
		if err != nil {
			return fmt.Errorf("failed to backup %s: %w", table, err)
		}
		totalRows += rowsCopied
	}

	eh.logger.Info("Backup completed", "tables", len(tables), "rows_copied", totalRows)
	return nil
}

func (eh *ErrorHandler) copyTableData(ctx context.Context, srcDB, dstDB *sql.DB, table string) (int, error) {
	exists, err := eh.tableExists(ctx, srcDB, table)
	if err != nil {
		return 0, err
	}
	if !exists {
		return 0, nil
	}

	srcColumns, hasID, err := eh.tableColumns(ctx, srcDB, table)
	if err != nil {
		return 0, err
	}
	dstColumns, _, err := eh.tableColumns(ctx, dstDB, table)
	if err != nil {
		return 0, err
	}

	allowed := make(map[string]struct{}, len(dstColumns))
	for _, column := range dstColumns {
		allowed[column] = struct{}{}
	}

	columns := make([]string, 0, len(srcColumns))
	for _, column := range srcColumns {
		if _, ok := allowed[column]; ok {
			columns = append(columns, column)
		}
	}
	if len(columns) == 0 {
		return 0, nil
	}

	selectSQL := fmt.Sprintf("SELECT %s FROM %s", joinQuotedColumns(columns), quoteIdentifier(table))
	if hasID {
		selectSQL += " ORDER BY \"id\""
	}

	rows, err := srcDB.QueryContext(ctx, selectSQL)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	insertSQL := fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s)",
		quoteIdentifier(table),
		joinQuotedColumns(columns),
		placeholders(len(columns)),
	)
	stmt, err := dstDB.PrepareContext(ctx, insertSQL)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	count := 0
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return count, err
		}
		for i, value := range values {
			if raw, ok := value.([]byte); ok {
				values[i] = string(raw)
			}
		}
		if _, err := stmt.ExecContext(ctx, values...); err != nil {
			return count, err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return count, err
	}

	return count, nil
}

func (eh *ErrorHandler) tableExists(ctx context.Context, db *sql.DB, table string) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (eh *ErrorHandler) tableColumns(ctx context.Context, db *sql.DB, table string) ([]string, bool, error) {
	query := fmt.Sprintf("PRAGMA table_info(%s)", quoteIdentifier(table))
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	columns := make([]string, 0, 16)
	hasID := false
	for rows.Next() {
		var (
			cid        int
			name       string
			columnType string
			notNull    int
			defaultVal any
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultVal, &primaryKey); err != nil {
			return nil, false, err
		}
		columns = append(columns, name)
		if name == "id" {
			hasID = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return columns, hasID, nil
}

func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func joinQuotedColumns(columns []string) string {
	quoted := make([]string, 0, len(columns))
	for _, column := range columns {
		quoted = append(quoted, quoteIdentifier(column))
	}
	return strings.Join(quoted, ", ")
}

func placeholders(count int) string {
	if count <= 0 {
		return ""
	}
	parts := make([]string, count)
	for i := range parts {
		parts[i] = "?"
	}
	return strings.Join(parts, ", ")
}

// Error type detection helper functions
func isDiskSpaceError(err error) bool {
	errStr := err.Error()
	return contains(errStr, "no space left") ||
		contains(errStr, "disk full") ||
		contains(errStr, "SQLITE_FULL")
}

func isDatabaseCorruptionError(err error) bool {
	errStr := err.Error()
	return contains(errStr, "SQLITE_CORRUPT") ||
		contains(errStr, "database disk image is malformed") ||
		contains(errStr, "file is not a database")
}

func isDatabaseLockedError(err error) bool {
	errStr := err.Error()
	return contains(errStr, "SQLITE_BUSY") ||
		contains(errStr, "SQLITE_LOCKED") ||
		contains(errStr, "database is locked")
}

func isConnectionError(err error) bool {
	errStr := err.Error()
	return contains(errStr, "connection") ||
		contains(errStr, "no such file") ||
		contains(errStr, "unable to open database")
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr ||
		(len(s) > len(substr) &&
			(s[:len(substr)] == substr ||
				s[len(s)-len(substr):] == substr ||
				containsSubstring(s, substr))))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
