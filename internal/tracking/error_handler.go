package tracking

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
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

// CreateBackup 创建当前数据库的完整快照。
func (eh *ErrorHandler) CreateBackup() (err error) {
	startedAt := time.Now()
	if eh.tracker == nil || eh.tracker.db == nil || eh.tracker.config == nil {
		return fmt.Errorf("tracker, database or config not initialized")
	}
	dbPath := eh.tracker.config.DatabasePath
	if dbPath == "" || dbPath == ":memory:" {
		return fmt.Errorf("backup requires a file database path")
	}

	backupPath := dbPath + ".backup"
	tempBackupPath := backupPath + ".tmp"
	defer func() {
		if err != nil {
			if cleanupErr := os.Remove(tempBackupPath); cleanupErr != nil && !os.IsNotExist(cleanupErr) {
				err = errors.Join(err, fmt.Errorf("failed to remove temporary backup: %w", cleanupErr))
			}
		}
	}()
	// VACUUM INTO 要求目标文件不存在。
	if err := os.Remove(tempBackupPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove stale temporary backup: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if err := createSQLiteBackupSnapshot(ctx, dbPath, tempBackupPath); err != nil {
		return err
	}
	if err := validateSQLiteBackup(ctx, tempBackupPath); err != nil {
		return err
	}
	if err := os.Chmod(tempBackupPath, 0o600); err != nil {
		return fmt.Errorf("failed to secure backup permissions: %w", err)
	}
	info, err := os.Stat(tempBackupPath)
	if err != nil {
		return fmt.Errorf("failed to stat temporary backup: %w", err)
	}
	if err := os.Rename(tempBackupPath, backupPath); err != nil {
		return fmt.Errorf("failed to finalize backup: %w", err)
	}
	eh.logger.Info("Database backup created successfully", "backup_path", backupPath,
		"duration", time.Since(startedAt), "size_bytes", info.Size())
	return nil
}

// createSQLiteBackupSnapshot 使用独立只读连接，避免占用 tracker 的写连接。
func createSQLiteBackupSnapshot(ctx context.Context, dbPath, backupPath string) (err error) {
	sourceDB, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return fmt.Errorf("failed to open backup source read-only: %w", err)
	}
	defer func() {
		if closeErr := sourceDB.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("failed to close backup source: %w", closeErr))
		}
	}()
	query := "VACUUM INTO '" + strings.ReplaceAll(backupPath, "'", "''") + "'"
	if _, err := sourceDB.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("failed to create SQLite backup snapshot: %w", err)
	}
	return nil
}

// validateSQLiteBackup 只读检查临时快照，校验通过后才能替换旧备份。
func validateSQLiteBackup(ctx context.Context, backupPath string) (err error) {
	backupDB, err := sql.Open("sqlite", "file:"+backupPath+"?mode=ro")
	if err != nil {
		return fmt.Errorf("failed to open backup for validation: %w", err)
	}
	defer func() {
		if closeErr := backupDB.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("failed to close backup validation database: %w", closeErr))
		}
	}()
	var result string
	if err := backupDB.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&result); err != nil {
		return fmt.Errorf("failed to check backup integrity: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("backup quick_check returned %q", result)
	}
	return nil
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
