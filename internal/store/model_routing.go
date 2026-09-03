package store

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"
)

// ModelRoutingSettingsRecord 是模型路由全局设置的持久化记录（单行 id=1）。
type ModelRoutingSettingsRecord struct {
	ID            int64     `json:"id"`
	ClaudeEnabled bool      `json:"claude_enabled"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// ModelUpstreamGroupRecord 是模型路由上游组的持久化记录。
type ModelUpstreamGroupRecord struct {
	ID            int64     `json:"id"`
	Family        string    `json:"family"`
	Name          string    `json:"name"`
	MembersJSON   string    `json:"members_json"`
	RouteMode     string    `json:"route_mode"`
	RouteTarget   string    `json:"route_target"`
	RouteRevision int64     `json:"route_revision"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// ModelRoutingAssignmentRecord 是模型到上游组分配的持久化记录。
type ModelRoutingAssignmentRecord struct {
	ID        int64     `json:"id"`
	Family    string    `json:"family"`
	MatchType string    `json:"match_type"`
	ModelName string    `json:"model_name"`
	GroupID   int64     `json:"group_id"`
	Enabled   bool      `json:"enabled"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ModelRoutingStore 定义模型路由设置、组和分配的存储接口。
type ModelRoutingStore interface {
	GetSettings(ctx context.Context) (*ModelRoutingSettingsRecord, error)
	UpdateSettings(ctx context.Context, record *ModelRoutingSettingsRecord) error

	ListGroups(ctx context.Context) ([]*ModelUpstreamGroupRecord, error)
	GetGroup(ctx context.Context, id int64) (*ModelUpstreamGroupRecord, error)
	CreateGroup(ctx context.Context, record *ModelUpstreamGroupRecord) (*ModelUpstreamGroupRecord, error)
	UpdateGroup(ctx context.Context, record *ModelUpstreamGroupRecord) error
	DeleteGroup(ctx context.Context, id int64) error

	ListAssignments(ctx context.Context) ([]*ModelRoutingAssignmentRecord, error)
	GetAssignment(ctx context.Context, id int64) (*ModelRoutingAssignmentRecord, error)
	CreateAssignment(ctx context.Context, record *ModelRoutingAssignmentRecord) (*ModelRoutingAssignmentRecord, error)
	UpdateAssignment(ctx context.Context, record *ModelRoutingAssignmentRecord) error
	DeleteAssignment(ctx context.Context, id int64) error

	WithTx(tx *sql.Tx) ModelRoutingStore
	// RunInTx 在单个事务内执行 fn。一条模型映射横跨 groups 与 assignments 两张表，
	// 必须原子写入，否则「建组成功 + 建分配失败」会留下永久占用配额的孤儿组。
	RunInTx(ctx context.Context, fn func(ModelRoutingStore) error) error
}

// SQLiteModelRoutingStore 是 SQLite 模型路由存储实现。
type SQLiteModelRoutingStore struct {
	db *sql.DB
	tx *sql.Tx
	mu sync.RWMutex

	schemaOnce  *sync.Once
	schemaError *error
}

// NewSQLiteModelRoutingStore 创建模型路由存储。
func NewSQLiteModelRoutingStore(db *sql.DB) *SQLiteModelRoutingStore {
	err := error(nil)
	return &SQLiteModelRoutingStore{
		db:          db,
		schemaOnce:  &sync.Once{},
		schemaError: &err,
	}
}

func (s *SQLiteModelRoutingStore) WithTx(tx *sql.Tx) ModelRoutingStore {
	if s == nil {
		return &SQLiteModelRoutingStore{tx: tx}
	}
	return &SQLiteModelRoutingStore{
		db:          s.db,
		tx:          tx,
		schemaOnce:  s.schemaOnce,
		schemaError: s.schemaError,
	}
}

// RunInTx 在单个事务内执行 fn，fn 收到的 store 已绑定该事务。
// fn 返回错误即整体回滚；已处于事务内时直接复用，避免嵌套 BeginTx 在 SQLite 上自锁。
func (s *SQLiteModelRoutingStore) RunInTx(ctx context.Context, fn func(ModelRoutingStore) error) error {
	if s == nil {
		return fmt.Errorf("model routing store is nil")
	}
	if s.tx != nil {
		return fn(s)
	}
	if s.db == nil {
		return fmt.Errorf("model routing store db is nil")
	}
	// schema 建表不能放进事务：ensureSchema 走的是 s.db，与下面的 tx 并存会互相阻塞。
	if err := s.ensureSchema(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin model routing tx failed: %w", err)
	}
	if err := fn(s.WithTx(tx)); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit model routing tx failed: %w", err)
	}
	return nil
}

type modelRoutingQuerier interface {	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
}

func (s *SQLiteModelRoutingStore) getQuerier() modelRoutingQuerier {
	if s.tx != nil {
		return s.tx
	}
	return s.db
}

func (s *SQLiteModelRoutingStore) ensureSchema(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("model routing store is nil")
	}
	if s.tx != nil {
		return nil
	}
	if s.db == nil {
		return fmt.Errorf("model routing store db is nil")
	}
	if s.schemaOnce == nil {
		s.schemaOnce = &sync.Once{}
	}
	if s.schemaError == nil {
		err := error(nil)
		s.schemaError = &err
	}
	s.schemaOnce.Do(func() {
		for _, statement := range modelRoutingSchemaStatements() {
			if _, err := s.db.ExecContext(ctx, statement); err != nil {
				*s.schemaError = fmt.Errorf("ensure model routing schema failed: %w", err)
				return
			}
		}
	})
	return *s.schemaError
}

func modelRoutingSchemaStatements() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS model_routing_settings (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			claude_enabled INTEGER NOT NULL DEFAULT 0 CHECK (claude_enabled IN (0, 1)),
			updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now'))
		)`,
		`INSERT OR IGNORE INTO model_routing_settings (id) VALUES (1)`,
		`CREATE TABLE IF NOT EXISTS model_upstream_groups (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			family TEXT NOT NULL DEFAULT 'claude' CHECK (family IN ('claude', 'codex')),
			name TEXT NOT NULL CHECK (length(trim(name)) > 0),
			members_json TEXT NOT NULL CHECK (json_valid(members_json) AND json_type(members_json) = 'array'),
			route_mode TEXT NOT NULL DEFAULT 'auto' CHECK (route_mode IN ('auto', 'manual_preferred', 'manual_fixed')),
			route_target TEXT NOT NULL DEFAULT '',
			route_revision INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')),
			updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now'))
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_model_upstream_groups_family_name
			ON model_upstream_groups(family, lower(name))`,
		`CREATE TABLE IF NOT EXISTS model_routing_assignments (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			family TEXT NOT NULL DEFAULT 'claude' CHECK (family IN ('claude', 'codex')),
			match_type TEXT NOT NULL DEFAULT 'exact' CHECK (match_type IN ('exact', 'prefix')),
			model_name TEXT NOT NULL CHECK (length(trim(model_name)) > 0),
			group_id INTEGER NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
			note TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')),
			updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now'))
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_model_routing_assignments_family_model
			ON model_routing_assignments(family, match_type, lower(model_name))`,
		`CREATE INDEX IF NOT EXISTS idx_model_routing_assignments_group
			ON model_routing_assignments(group_id)`,
		`CREATE TRIGGER IF NOT EXISTS update_model_routing_settings_timestamp
			AFTER UPDATE ON model_routing_settings
			FOR EACH ROW WHEN NEW.updated_at = OLD.updated_at
		BEGIN
			UPDATE model_routing_settings SET updated_at = strftime('%Y-%m-%dT%H:%M:%f000Z', 'now') WHERE id = NEW.id;
		END`,
		`CREATE TRIGGER IF NOT EXISTS update_model_upstream_groups_timestamp
			AFTER UPDATE ON model_upstream_groups
			FOR EACH ROW WHEN NEW.updated_at = OLD.updated_at
		BEGIN
			UPDATE model_upstream_groups SET updated_at = strftime('%Y-%m-%dT%H:%M:%f000Z', 'now') WHERE id = NEW.id;
		END`,
		`CREATE TRIGGER IF NOT EXISTS update_model_routing_assignments_timestamp
			AFTER UPDATE ON model_routing_assignments
			FOR EACH ROW WHEN NEW.updated_at = OLD.updated_at
		BEGIN
			UPDATE model_routing_assignments SET updated_at = strftime('%Y-%m-%dT%H:%M:%f000Z', 'now') WHERE id = NEW.id;
		END`,
	}
}

func (s *SQLiteModelRoutingStore) GetSettings(ctx context.Context) (*ModelRoutingSettingsRecord, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return nil, err
	}
	return scanModelRoutingSettings(s.getQuerier().QueryRowContext(ctx, `
		SELECT id, claude_enabled, CAST(updated_at AS TEXT)
		FROM model_routing_settings WHERE id = 1`))
}

func (s *SQLiteModelRoutingStore) UpdateSettings(ctx context.Context, record *ModelRoutingSettingsRecord) error {
	if record == nil {
		return fmt.Errorf("model routing settings is nil")
	}
	if err := s.ensureSchema(ctx); err != nil {
		return err
	}
	_, err := s.getQuerier().ExecContext(ctx, `
		UPDATE model_routing_settings
		SET claude_enabled = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')
		WHERE id = 1`, boolToInt(record.ClaudeEnabled))
	if err != nil {
		return fmt.Errorf("update model routing settings failed: %w", err)
	}
	return nil
}

func (s *SQLiteModelRoutingStore) ListGroups(ctx context.Context) ([]*ModelUpstreamGroupRecord, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := s.getQuerier().QueryContext(ctx, `
		SELECT id, family, name, members_json, route_mode, route_target, route_revision,
			CAST(created_at AS TEXT), CAST(updated_at AS TEXT)
		FROM model_upstream_groups ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list model upstream groups failed: %w", err)
	}
	defer rows.Close()
	var records []*ModelUpstreamGroupRecord
	for rows.Next() {
		record, err := scanModelUpstreamGroup(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate model upstream groups failed: %w", err)
	}
	return records, nil
}

func (s *SQLiteModelRoutingStore) GetGroup(ctx context.Context, id int64) (*ModelUpstreamGroupRecord, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return nil, err
	}
	return scanModelUpstreamGroup(s.getQuerier().QueryRowContext(ctx, `
		SELECT id, family, name, members_json, route_mode, route_target, route_revision,
			CAST(created_at AS TEXT), CAST(updated_at AS TEXT)
		FROM model_upstream_groups WHERE id = ?`, id))
}

func (s *SQLiteModelRoutingStore) CreateGroup(ctx context.Context, record *ModelUpstreamGroupRecord) (*ModelUpstreamGroupRecord, error) {
	if record == nil {
		return nil, fmt.Errorf("model upstream group is nil")
	}
	if err := s.ensureSchema(ctx); err != nil {
		return nil, err
	}
	if record.Family == "" {
		record.Family = "claude"
	}
	if record.RouteMode == "" {
		record.RouteMode = "auto"
	}
	result, err := s.getQuerier().ExecContext(ctx, `
		INSERT INTO model_upstream_groups (family, name, members_json, route_mode, route_target, route_revision)
		VALUES (?, ?, ?, ?, ?, ?)`, record.Family, record.Name, record.MembersJSON, record.RouteMode, record.RouteTarget, record.RouteRevision)
	if err != nil {
		return nil, fmt.Errorf("create model upstream group failed: %w", err)
	}
	record.ID, err = result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("read model upstream group id failed: %w", err)
	}
	return s.GetGroup(ctx, record.ID)
}

func (s *SQLiteModelRoutingStore) UpdateGroup(ctx context.Context, record *ModelUpstreamGroupRecord) error {
	if record == nil || record.ID <= 0 {
		return fmt.Errorf("invalid model upstream group")
	}
	if err := s.ensureSchema(ctx); err != nil {
		return err
	}
	result, err := s.getQuerier().ExecContext(ctx, `
		UPDATE model_upstream_groups
		SET family = ?, name = ?, members_json = ?, route_mode = ?, route_target = ?, route_revision = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')
		WHERE id = ?`, record.Family, record.Name, record.MembersJSON, record.RouteMode, record.RouteTarget, record.RouteRevision, record.ID)
	if err != nil {
		return fmt.Errorf("update model upstream group failed: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return fmt.Errorf("model upstream group %d not found", record.ID)
	}
	return nil
}

func (s *SQLiteModelRoutingStore) DeleteGroup(ctx context.Context, id int64) error {
	if err := s.ensureSchema(ctx); err != nil {
		return err
	}
	result, err := s.getQuerier().ExecContext(ctx, `DELETE FROM model_upstream_groups WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete model upstream group failed: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return fmt.Errorf("model upstream group %d not found", id)
	}
	return nil
}

func (s *SQLiteModelRoutingStore) ListAssignments(ctx context.Context) ([]*ModelRoutingAssignmentRecord, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := s.getQuerier().QueryContext(ctx, `
		SELECT id, family, match_type, model_name, group_id, enabled, note,
			CAST(created_at AS TEXT), CAST(updated_at AS TEXT)
		FROM model_routing_assignments ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list model routing assignments failed: %w", err)
	}
	defer rows.Close()
	var records []*ModelRoutingAssignmentRecord
	for rows.Next() {
		record, err := scanModelRoutingAssignment(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate model routing assignments failed: %w", err)
	}
	return records, nil
}

func (s *SQLiteModelRoutingStore) GetAssignment(ctx context.Context, id int64) (*ModelRoutingAssignmentRecord, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return nil, err
	}
	return scanModelRoutingAssignment(s.getQuerier().QueryRowContext(ctx, `
		SELECT id, family, match_type, model_name, group_id, enabled, note,
			CAST(created_at AS TEXT), CAST(updated_at AS TEXT)
		FROM model_routing_assignments WHERE id = ?`, id))
}

func (s *SQLiteModelRoutingStore) CreateAssignment(ctx context.Context, record *ModelRoutingAssignmentRecord) (*ModelRoutingAssignmentRecord, error) {
	if record == nil {
		return nil, fmt.Errorf("model routing assignment is nil")
	}
	if err := s.ensureSchema(ctx); err != nil {
		return nil, err
	}
	if record.Family == "" {
		record.Family = "claude"
	}
	if record.MatchType == "" {
		record.MatchType = "exact"
	}
	result, err := s.getQuerier().ExecContext(ctx, `
		INSERT INTO model_routing_assignments (family, match_type, model_name, group_id, enabled, note)
		VALUES (?, ?, ?, ?, ?, ?)`, record.Family, record.MatchType, record.ModelName, record.GroupID, boolToInt(record.Enabled), record.Note)
	if err != nil {
		return nil, fmt.Errorf("create model routing assignment failed: %w", err)
	}
	record.ID, err = result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("read model routing assignment id failed: %w", err)
	}
	return s.GetAssignment(ctx, record.ID)
}

func (s *SQLiteModelRoutingStore) UpdateAssignment(ctx context.Context, record *ModelRoutingAssignmentRecord) error {
	if record == nil || record.ID <= 0 {
		return fmt.Errorf("invalid model routing assignment")
	}
	if err := s.ensureSchema(ctx); err != nil {
		return err
	}
	result, err := s.getQuerier().ExecContext(ctx, `
		UPDATE model_routing_assignments
		SET family = ?, match_type = ?, model_name = ?, group_id = ?, enabled = ?, note = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')
		WHERE id = ?`, record.Family, record.MatchType, record.ModelName, record.GroupID, boolToInt(record.Enabled), record.Note, record.ID)
	if err != nil {
		return fmt.Errorf("update model routing assignment failed: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return fmt.Errorf("model routing assignment %d not found", record.ID)
	}
	return nil
}

func (s *SQLiteModelRoutingStore) DeleteAssignment(ctx context.Context, id int64) error {
	if err := s.ensureSchema(ctx); err != nil {
		return err
	}
	result, err := s.getQuerier().ExecContext(ctx, `DELETE FROM model_routing_assignments WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete model routing assignment failed: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return fmt.Errorf("model routing assignment %d not found", id)
	}
	return nil
}

type modelRoutingRowScanner interface {
	Scan(dest ...any) error
}

func scanModelRoutingSettings(row modelRoutingRowScanner) (*ModelRoutingSettingsRecord, error) {
	record := &ModelRoutingSettingsRecord{}
	var enabled int
	var updatedAt string
	if err := row.Scan(&record.ID, &enabled, &updatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("scan model routing settings failed: %w", err)
	}
	record.ClaudeEnabled = enabled != 0
	var err error
	record.UpdatedAt, err = parseDBTime(updatedAt)
	if err != nil {
		return nil, fmt.Errorf("parse model routing settings updated_at failed: %w", err)
	}
	return record, nil
}

func scanModelUpstreamGroup(row modelRoutingRowScanner) (*ModelUpstreamGroupRecord, error) {
	record := &ModelUpstreamGroupRecord{}
	var createdAt, updatedAt string
	if err := row.Scan(&record.ID, &record.Family, &record.Name, &record.MembersJSON, &record.RouteMode, &record.RouteTarget,
		&record.RouteRevision, &createdAt, &updatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("scan model upstream group failed: %w", err)
	}
	var err error
	if record.CreatedAt, err = parseDBTime(createdAt); err != nil {
		return nil, fmt.Errorf("parse model upstream group created_at failed: %w", err)
	}
	if record.UpdatedAt, err = parseDBTime(updatedAt); err != nil {
		return nil, fmt.Errorf("parse model upstream group updated_at failed: %w", err)
	}
	return record, nil
}

func scanModelRoutingAssignment(row modelRoutingRowScanner) (*ModelRoutingAssignmentRecord, error) {
	record := &ModelRoutingAssignmentRecord{}
	var enabled int
	var createdAt, updatedAt string
	if err := row.Scan(&record.ID, &record.Family, &record.MatchType, &record.ModelName, &record.GroupID, &enabled, &record.Note, &createdAt, &updatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("scan model routing assignment failed: %w", err)
	}
	record.Enabled = enabled != 0
	var err error
	if record.CreatedAt, err = parseDBTime(createdAt); err != nil {
		return nil, fmt.Errorf("parse model routing assignment created_at failed: %w", err)
	}
	if record.UpdatedAt, err = parseDBTime(updatedAt); err != nil {
		return nil, fmt.Errorf("parse model routing assignment updated_at failed: %w", err)
	}
	return record, nil
}
