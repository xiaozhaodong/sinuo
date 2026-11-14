// Package store 提供隐私保护规则与设置的数据存储实现
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"sinuo/internal/store/schema"
)

// PrivacySettingsRecord 隐私保护全局设置（单行，id=1）
type PrivacySettingsRecord struct {
	ResponseRestoreEnabled bool      `json:"response_restore_enabled"`
	RestoreKey             []byte    `json:"-"`
	Mode                   string    `json:"mode"`
	ScanMaxBytes           int64     `json:"scan_max_bytes"`
	OverLimitAction        string    `json:"over_limit_action"`
	OnError                string    `json:"on_error"`
	UpdatedAt              time.Time `json:"updated_at"`
}

// PrivacyRuleRecord 隐私规则记录
type PrivacyRuleRecord struct {
	ID           int64     `json:"id"`
	Enabled      bool      `json:"enabled"`
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	Priority     int       `json:"priority"`
	MatchType    string    `json:"match_type"`
	Pattern      string    `json:"pattern"`
	Placeholder  string    `json:"placeholder"`
	Action       string    `json:"action"`
	ScopeJSON    string    `json:"scope_json"`
	Source       string    `json:"source"`
	CompileError string    `json:"compile_error"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// PrivacyExactSecretRecord 本地精确敏感值记录。
// SecretValue 仅供本机扫描使用，不允许出现在列表响应或普通日志中。
type PrivacyExactSecretRecord struct {
	ID          int64     `json:"id"`
	Enabled     bool      `json:"enabled"`
	Name        string    `json:"name"`
	SecretValue string    `json:"secret_value"`
	ValueHash   string    `json:"value_hash"`
	Placeholder string    `json:"placeholder"`
	Category    string    `json:"category"`
	SourceType  string    `json:"source_type"`
	SourceRef   string    `json:"source_ref"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// PrivacyStore 隐私规则存储接口
type PrivacyStore interface {
	PrivacyConfigSourceStore
	GetSettings(ctx context.Context) (*PrivacySettingsRecord, error)
	UpdateSettings(ctx context.Context, record *PrivacySettingsRecord) error
	ListRules(ctx context.Context) ([]*PrivacyRuleRecord, error)
	GetRule(ctx context.Context, id int64) (*PrivacyRuleRecord, error)
	CreateRule(ctx context.Context, record *PrivacyRuleRecord) (*PrivacyRuleRecord, error)
	UpdateRule(ctx context.Context, record *PrivacyRuleRecord) error
	DeleteRule(ctx context.Context, id int64) error
	UpdateRulePriorities(ctx context.Context, priorities map[int64]int) error
	SetRuleCompileError(ctx context.Context, id int64, compileError string) error
	CreateRules(ctx context.Context, records []*PrivacyRuleRecord) ([]*PrivacyRuleRecord, error)
	ListExactSecrets(ctx context.Context) ([]*PrivacyExactSecretRecord, error)
	GetExactSecret(ctx context.Context, id int64) (*PrivacyExactSecretRecord, error)
	FindExactSecretByHash(ctx context.Context, valueHash string) (*PrivacyExactSecretRecord, error)
	CreateExactSecret(ctx context.Context, record *PrivacyExactSecretRecord) (*PrivacyExactSecretRecord, error)
	ImportExactSecrets(ctx context.Context, records []*PrivacyExactSecretRecord, prepare func([]*PrivacyExactSecretRecord) error) (int, error)
	UpdateExactSecret(ctx context.Context, record *PrivacyExactSecretRecord) error
	DeleteExactSecret(ctx context.Context, id int64) error
	ClearExactSecrets(ctx context.Context) error
}

// SQLitePrivacyStore SQLite 隐私规则存储实现
type SQLitePrivacyStore struct {
	db          *sql.DB
	schemaOnce  sync.Once
	schemaError error
}

// NewSQLitePrivacyStore 创建隐私规则存储
func NewSQLitePrivacyStore(db *sql.DB) *SQLitePrivacyStore {
	return &SQLitePrivacyStore{db: db}
}

// ensureSchema 幂等建表与默认设置行。canonical schema 在 tracking/schema.sql，
// 这里兜底保证 store 单测与旧数据库可用。
func (s *SQLitePrivacyStore) ensureSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("privacy store db is nil")
	}
	s.schemaOnce.Do(func() {
		statements := []string{
			`CREATE TABLE IF NOT EXISTS privacy_settings (
				id INTEGER PRIMARY KEY CHECK (id = 1),
				mode TEXT NOT NULL DEFAULT 'disabled',
				response_restore_enabled INTEGER NOT NULL DEFAULT 0,
				restore_key BLOB,
				scan_max_bytes INTEGER NOT NULL DEFAULT 4194304,
				over_limit_action TEXT NOT NULL DEFAULT 'scan_prefix',
				on_error TEXT NOT NULL DEFAULT 'fail_open',
				updated_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now'))
			)`,
			`INSERT OR IGNORE INTO privacy_settings (id) VALUES (1)`,
			`CREATE TABLE IF NOT EXISTS privacy_rules (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				enabled BOOLEAN NOT NULL DEFAULT TRUE,
				name TEXT NOT NULL,
				description TEXT DEFAULT '',
				priority INTEGER NOT NULL DEFAULT 100,
				match_type TEXT NOT NULL,
				pattern TEXT NOT NULL,
				placeholder TEXT NOT NULL DEFAULT '[已脱敏]',
				action TEXT NOT NULL DEFAULT 'redact',
				scope_json TEXT NOT NULL DEFAULT '{}',
				source TEXT NOT NULL DEFAULT 'custom',
				compile_error TEXT DEFAULT '',
				created_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')),
				updated_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now'))
			)`,
			`CREATE INDEX IF NOT EXISTS idx_privacy_rules_enabled_priority ON privacy_rules(enabled, priority)`,
			`CREATE TABLE IF NOT EXISTS privacy_exact_secrets (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				enabled BOOLEAN NOT NULL DEFAULT TRUE,
				name TEXT NOT NULL,
				secret_value TEXT NOT NULL,
				value_hash TEXT NOT NULL,
				placeholder TEXT NOT NULL DEFAULT '[敏感值]',
				category TEXT NOT NULL DEFAULT 'custom',
				source_type TEXT NOT NULL DEFAULT 'manual',
				source_ref TEXT NOT NULL DEFAULT '',
				description TEXT NOT NULL DEFAULT '',
				created_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')),
				updated_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now'))
			)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_privacy_exact_secrets_value_hash
				ON privacy_exact_secrets(value_hash)`,
			`CREATE TRIGGER IF NOT EXISTS update_privacy_exact_secrets_timestamp
				AFTER UPDATE ON privacy_exact_secrets
				FOR EACH ROW
				WHEN NEW.updated_at = OLD.updated_at
			BEGIN
				UPDATE privacy_exact_secrets
				SET updated_at = strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')
				WHERE id = NEW.id;
			END`,
		}
		for _, stmt := range statements {
			if _, err := s.db.ExecContext(ctx, stmt); err != nil {
				s.schemaError = fmt.Errorf("ensure privacy schema failed: %w", err)
				return
			}
		}
		if err := schema.EnsurePrivacySources(ctx, s.db); err != nil {
			s.schemaError = err
			return
		}
		s.schemaError = s.ensureRestoreColumns(ctx)
	})
	return s.schemaError
}

// ensureRestoreColumns 为已有单行设置表补列；新旧列同时生效。
func (s *SQLitePrivacyStore) ensureRestoreColumns(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, col := range []struct{ name, ddl string }{
		{"response_restore_enabled", "ALTER TABLE privacy_settings ADD COLUMN response_restore_enabled INTEGER NOT NULL DEFAULT 0"},
		{"restore_key", "ALTER TABLE privacy_settings ADD COLUMN restore_key BLOB"},
	} {
		var exists bool
		if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pragma_table_info('privacy_settings') WHERE name=?)", col.name).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			if _, err = tx.ExecContext(ctx, col.ddl); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// GetSettings 读取全局设置（保证默认行存在）
func (s *SQLitePrivacyStore) GetSettings(ctx context.Context) (*PrivacySettingsRecord, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return nil, err
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT mode, scan_max_bytes, over_limit_action, on_error, response_restore_enabled, restore_key, COALESCE(CAST(updated_at AS TEXT), '')
		FROM privacy_settings WHERE id = 1
	`)
	record := &PrivacySettingsRecord{}
	var updatedAt string
	if err := row.Scan(&record.Mode, &record.ScanMaxBytes, &record.OverLimitAction, &record.OnError, &record.ResponseRestoreEnabled, &record.RestoreKey, &updatedAt); err != nil {
		return nil, fmt.Errorf("read privacy settings failed: %w", err)
	}
	var err error
	if record.UpdatedAt, err = parseDBTime(updatedAt); err != nil {
		return nil, fmt.Errorf("parse privacy settings updated_at: %w", err)
	}
	return record, nil
}

// UpdateSettings 更新全局设置
func (s *SQLitePrivacyStore) UpdateSettings(ctx context.Context, record *PrivacySettingsRecord) error {
	if record == nil {
		return fmt.Errorf("privacy settings record is nil")
	}
	if err := s.ensureSchema(ctx); err != nil {
		return err
	}
	var key []byte
	if record.ResponseRestoreEnabled {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return fmt.Errorf("generate privacy restore key: %w", err)
		}
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE privacy_settings
		SET response_restore_enabled = ?, restore_key = COALESCE(restore_key, ?),
            mode = ?, scan_max_bytes = ?, over_limit_action = ?, on_error = ?,
		    updated_at = strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')
		WHERE id = 1
	`, record.ResponseRestoreEnabled, key, record.Mode, record.ScanMaxBytes, record.OverLimitAction, record.OnError)
	if err != nil {
		return fmt.Errorf("update privacy settings failed: %w", err)
	}
	return nil
}

const privacyRuleColumns = `id, enabled, name, COALESCE(description, ''), priority, match_type, pattern,
	placeholder, action, scope_json, source, COALESCE(compile_error, ''),
	COALESCE(CAST(created_at AS TEXT), ''), COALESCE(CAST(updated_at AS TEXT), '')`

// ListRules 列出全部规则（priority 升序，同优先级按 ID）
func (s *SQLitePrivacyStore) ListRules(ctx context.Context) ([]*PrivacyRuleRecord, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+privacyRuleColumns+`
		FROM privacy_rules
		ORDER BY priority ASC, id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list privacy rules failed: %w", err)
	}
	defer rows.Close()

	var records []*PrivacyRuleRecord
	for rows.Next() {
		record, err := scanPrivacyRule(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate privacy rules failed: %w", err)
	}
	return records, nil
}

// GetRule 按 ID 读取规则；不存在时返回 (nil, nil)
func (s *SQLitePrivacyStore) GetRule(ctx context.Context, id int64) (*PrivacyRuleRecord, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+privacyRuleColumns+`
		FROM privacy_rules WHERE id = ?
	`, id)
	if err != nil {
		return nil, fmt.Errorf("get privacy rule failed: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	return scanPrivacyRule(rows)
}

// CreateRule 新增规则并返回完整记录
func (s *SQLitePrivacyStore) CreateRule(ctx context.Context, record *PrivacyRuleRecord) (*PrivacyRuleRecord, error) {
	if record == nil {
		return nil, fmt.Errorf("privacy rule record is nil")
	}
	if err := s.ensureSchema(ctx); err != nil {
		return nil, err
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO privacy_rules (enabled, name, description, priority, match_type, pattern, placeholder, action, scope_json, source, compile_error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, record.Enabled, record.Name, record.Description, record.Priority, record.MatchType,
		record.Pattern, record.Placeholder, record.Action, record.ScopeJSON, record.Source, record.CompileError)
	if err != nil {
		return nil, fmt.Errorf("create privacy rule failed: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("read privacy rule id failed: %w", err)
	}
	return s.GetRule(ctx, id)
}

// CreateRules 在单事务中批量新增规则（预设导入用）
func (s *SQLitePrivacyStore) CreateRules(ctx context.Context, records []*PrivacyRuleRecord) ([]*PrivacyRuleRecord, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin privacy rules tx failed: %w", err)
	}
	defer tx.Rollback()

	ids := make([]int64, 0, len(records))
	for _, record := range records {
		if record == nil {
			continue
		}
		result, err := tx.ExecContext(ctx, `
			INSERT INTO privacy_rules (enabled, name, description, priority, match_type, pattern, placeholder, action, scope_json, source, compile_error)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, record.Enabled, record.Name, record.Description, record.Priority, record.MatchType,
			record.Pattern, record.Placeholder, record.Action, record.ScopeJSON, record.Source, record.CompileError)
		if err != nil {
			return nil, fmt.Errorf("batch create privacy rule %q failed: %w", record.Name, err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			return nil, fmt.Errorf("read privacy rule id failed: %w", err)
		}
		ids = append(ids, id)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit privacy rules tx failed: %w", err)
	}

	created := make([]*PrivacyRuleRecord, 0, len(ids))
	for _, id := range ids {
		record, err := s.GetRule(ctx, id)
		if err != nil {
			return nil, err
		}
		if record != nil {
			created = append(created, record)
		}
	}
	return created, nil
}

// UpdateRule 更新规则
func (s *SQLitePrivacyStore) UpdateRule(ctx context.Context, record *PrivacyRuleRecord) error {
	if record == nil || record.ID <= 0 {
		return fmt.Errorf("invalid privacy rule record")
	}
	if err := s.ensureSchema(ctx); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE privacy_rules
		SET enabled = ?, name = ?, description = ?, priority = ?, match_type = ?,
		    pattern = ?, placeholder = ?, action = ?, scope_json = ?,
		    source = COALESCE(NULLIF(?, ''), source), compile_error = ?
		WHERE id = ?
	`, record.Enabled, record.Name, record.Description, record.Priority, record.MatchType,
		record.Pattern, record.Placeholder, record.Action, record.ScopeJSON, record.Source, record.CompileError, record.ID)
	if err != nil {
		return fmt.Errorf("update privacy rule failed: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read affected rows failed: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("privacy rule %d not found", record.ID)
	}
	return nil
}

// DeleteRule 删除规则
func (s *SQLitePrivacyStore) DeleteRule(ctx context.Context, id int64) error {
	if err := s.ensureSchema(ctx); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM privacy_rules WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete privacy rule failed: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read affected rows failed: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("privacy rule %d not found", id)
	}
	return nil
}

// UpdateRulePriorities 在单事务中批量更新优先级（拖拽排序用）
func (s *SQLitePrivacyStore) UpdateRulePriorities(ctx context.Context, priorities map[int64]int) error {
	if len(priorities) == 0 {
		return nil
	}
	if err := s.ensureSchema(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin privacy priorities tx failed: %w", err)
	}
	defer tx.Rollback()
	for id, priority := range priorities {
		if _, err := tx.ExecContext(ctx, `UPDATE privacy_rules SET priority = ? WHERE id = ?`, priority, id); err != nil {
			return fmt.Errorf("update privacy rule %d priority failed: %w", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit privacy priorities tx failed: %w", err)
	}
	return nil
}

// SetRuleCompileError 写回历史规则的编译失败信息
func (s *SQLitePrivacyStore) SetRuleCompileError(ctx context.Context, id int64, compileError string) error {
	if err := s.ensureSchema(ctx); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE privacy_rules SET compile_error = ? WHERE id = ?`, compileError, id); err != nil {
		return fmt.Errorf("set privacy rule compile_error failed: %w", err)
	}
	return nil
}

type privacyRuleScanner interface {
	Scan(dest ...any) error
}

func scanPrivacyRule(row privacyRuleScanner) (*PrivacyRuleRecord, error) {
	record := &PrivacyRuleRecord{}
	var createdAt, updatedAt string
	if err := row.Scan(
		&record.ID, &record.Enabled, &record.Name, &record.Description, &record.Priority,
		&record.MatchType, &record.Pattern, &record.Placeholder, &record.Action,
		&record.ScopeJSON, &record.Source, &record.CompileError, &createdAt, &updatedAt,
	); err != nil {
		return nil, fmt.Errorf("scan privacy rule failed: %w", err)
	}
	var err error
	if record.CreatedAt, err = parseDBTime(createdAt); err != nil {
		return nil, fmt.Errorf("parse privacy rule created_at: %w", err)
	}
	if record.UpdatedAt, err = parseDBTime(updatedAt); err != nil {
		return nil, fmt.Errorf("parse privacy rule updated_at: %w", err)
	}
	return record, nil
}
