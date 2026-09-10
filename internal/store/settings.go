// Package store 提供数据存储层实现
// 设置存储 - v5.1.0 新增 (2025-12-08)
package store

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"
)

// SettingRecord 表示数据库中的设置记录
type SettingRecord struct {
	ID int64 `json:"id"`

	// 设置标识
	Category string `json:"category"` // 分类: strategy, retry, health, etc.
	Key      string `json:"key"`      // 配置键

	// 设置值
	Value     string `json:"value"`      // 值 (支持 JSON)
	ValueType string `json:"value_type"` // 类型: string, int, float, bool, duration, json

	// 显示信息
	Label        string `json:"label"`         // 显示名称
	Description  string `json:"description"`   // 配置说明
	DisplayOrder int    `json:"display_order"` // 显示顺序

	// 元信息
	RequiresRestart bool `json:"requires_restart"` // 是否需要重启生效

	// 审计字段
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SettingsStore 定义设置存储接口
type SettingsStore interface {
	// CRUD 操作
	Get(ctx context.Context, category, key string) (*SettingRecord, error)
	Set(ctx context.Context, category, key, value string) error
	Delete(ctx context.Context, category, key string) error

	// 批量操作
	GetByCategory(ctx context.Context, category string) ([]*SettingRecord, error)
	GetAll(ctx context.Context) ([]*SettingRecord, error)
	BatchSet(ctx context.Context, records []*SettingRecord) error
	BatchUpdateValues(ctx context.Context, records []*SettingRecord) error // 只更新value
	DeleteByCategory(ctx context.Context, category string) error

	// 单行原子写入（供专用配置服务使用，不经过默认值初始化/元数据同步/分类重置）
	InsertIfAbsent(ctx context.Context, record *SettingRecord) (bool, error)
	CompareAndSwapValue(ctx context.Context, category, key, expectedValue, newValue string) (bool, error)

	// 初始化
	InitDefaults(ctx context.Context, defaults []*SettingRecord) error
	IsInitialized(ctx context.Context) (bool, error)
	SyncMetadata(ctx context.Context, defaults []*SettingRecord) error // 同步元数据

	// 统计
	Count(ctx context.Context) (int, error)
	CountByCategory(ctx context.Context, category string) (int, error)

	// 分类列表
	ListCategories(ctx context.Context) ([]string, error)
}

// SQLiteSettingsStore 实现 SettingsStore 接口
type SQLiteSettingsStore struct {
	db *sql.DB
	mu sync.RWMutex
}

// NewSQLiteSettingsStore 创建新的 SQLite 设置存储
func NewSQLiteSettingsStore(db *sql.DB) *SQLiteSettingsStore {
	return &SQLiteSettingsStore{db: db}
}

// Get 获取单个设置
func (s *SQLiteSettingsStore) Get(ctx context.Context, category, key string) (*SettingRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
		SELECT id, category, key, value, value_type,
			COALESCE(label, '') as label,
			COALESCE(description, '') as description,
			display_order, requires_restart, CAST(created_at AS TEXT), CAST(updated_at AS TEXT)
		FROM settings
		WHERE category = ? AND key = ?
	`

	var record SettingRecord
	var requiresRestart int
	var createdAt, updatedAt string

	err := s.db.QueryRowContext(ctx, query, category, key).Scan(
		&record.ID, &record.Category, &record.Key, &record.Value, &record.ValueType,
		&record.Label, &record.Description, &record.DisplayOrder,
		&requiresRestart, &createdAt, &updatedAt,
	)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("获取设置失败: %w", err)
	}

	record.RequiresRestart = requiresRestart == 1
	if record.CreatedAt, err = parseDBTime(createdAt); err != nil {
		return nil, fmt.Errorf("解析设置 created_at 失败: %w", err)
	}
	if record.UpdatedAt, err = parseDBTime(updatedAt); err != nil {
		return nil, fmt.Errorf("解析设置 updated_at 失败: %w", err)
	}

	return &record, nil
}

// Set 设置单个值（存在则更新，不存在则插入）
func (s *SQLiteSettingsStore) Set(ctx context.Context, category, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := `
		INSERT INTO settings (category, key, value)
		VALUES (?, ?, ?)
		ON CONFLICT(category, key) DO UPDATE SET
			value = excluded.value
	`

	_, err := s.db.ExecContext(ctx, query, category, key, value)
	if err != nil {
		return fmt.Errorf("设置值失败: %w", err)
	}

	return nil
}

// BatchUpdateValues 批量更新值（只更新 value，保留其他元数据）
func (s *SQLiteSettingsStore) BatchUpdateValues(ctx context.Context, records []*SettingRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(records) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始事务失败: %w", err)
	}
	defer tx.Rollback()

	// 只更新 value，不影响 label、description 等元数据
	query := `UPDATE settings SET value = ? WHERE category = ? AND key = ?`

	stmt, err := tx.PrepareContext(ctx, query)
	if err != nil {
		return fmt.Errorf("准备语句失败: %w", err)
	}
	defer stmt.Close()

	for _, record := range records {
		_, err = stmt.ExecContext(ctx, record.Value, record.Category, record.Key)
		if err != nil {
			return fmt.Errorf("更新 %s.%s 失败: %w", record.Category, record.Key, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交事务失败: %w", err)
	}

	return nil
}

// Delete 删除单个设置
func (s *SQLiteSettingsStore) Delete(ctx context.Context, category, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := `DELETE FROM settings WHERE category = ? AND key = ?`

	result, err := s.db.ExecContext(ctx, query, category, key)
	if err != nil {
		return fmt.Errorf("删除设置失败: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return fmt.Errorf("设置不存在: %s.%s", category, key)
	}

	return nil
}

// GetByCategory 获取分类下的所有设置
func (s *SQLiteSettingsStore) GetByCategory(ctx context.Context, category string) ([]*SettingRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
		SELECT id, category, key, value, value_type,
			COALESCE(label, '') as label,
			COALESCE(description, '') as description,
			display_order, requires_restart, CAST(created_at AS TEXT), CAST(updated_at AS TEXT)
		FROM settings
		WHERE category = ?
		ORDER BY display_order ASC, key ASC
	`

	return s.scanSettings(ctx, query, category)
}

// GetAll 获取所有设置
func (s *SQLiteSettingsStore) GetAll(ctx context.Context) ([]*SettingRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
		SELECT id, category, key, value, value_type,
			COALESCE(label, '') as label,
			COALESCE(description, '') as description,
			display_order, requires_restart, CAST(created_at AS TEXT), CAST(updated_at AS TEXT)
		FROM settings
		ORDER BY category ASC, display_order ASC, key ASC
	`

	return s.scanSettings(ctx, query)
}

// BatchSet 批量设置（事务）
func (s *SQLiteSettingsStore) BatchSet(ctx context.Context, records []*SettingRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(records) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始事务失败: %w", err)
	}
	defer tx.Rollback()

	query := `
		INSERT INTO settings (category, key, value, value_type, label, description, display_order, requires_restart)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(category, key) DO UPDATE SET
			value = excluded.value,
			value_type = excluded.value_type,
			label = excluded.label,
			description = excluded.description,
			display_order = excluded.display_order,
			requires_restart = excluded.requires_restart
	`

	stmt, err := tx.PrepareContext(ctx, query)
	if err != nil {
		return fmt.Errorf("准备语句失败: %w", err)
	}
	defer stmt.Close()

	for _, record := range records {
		_, err = stmt.ExecContext(ctx,
			record.Category, record.Key, record.Value, record.ValueType,
			record.Label, record.Description, record.DisplayOrder,
			boolToInt(record.RequiresRestart),
		)
		if err != nil {
			return fmt.Errorf("设置 %s.%s 失败: %w", record.Category, record.Key, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交事务失败: %w", err)
	}

	return nil
}

// InsertIfAbsent 仅在 (category, key) 不存在时插入完整记录，返回是否真正插入。
// 已存在时不改动任何列；用于“首次初始化只允许插入缺行”的专用配置，
// 与 BatchSet/SyncMetadata 的 upsert 语义严格区分。
func (s *SQLiteSettingsStore) InsertIfAbsent(ctx context.Context, record *SettingRecord) (bool, error) {
	if record == nil {
		return false, fmt.Errorf("设置记录为空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	query := `
		INSERT INTO settings (category, key, value, value_type, label, description, display_order, requires_restart)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(category, key) DO NOTHING
	`
	result, err := s.db.ExecContext(ctx, query,
		record.Category, record.Key, record.Value, record.ValueType,
		record.Label, record.Description, record.DisplayOrder,
		boolToInt(record.RequiresRestart),
	)
	if err != nil {
		return false, fmt.Errorf("插入设置 %s.%s 失败: %w", record.Category, record.Key, err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("读取插入结果失败: %w", err)
	}
	return inserted == 1, nil
}

// CompareAndSwapValue 仅当当前 value 与 expectedValue 逐字节相等时更新为 newValue，返回是否命中。
// 记录不存在或值已被其他写入者改变时返回 false 且不做任何修改；
// 调用方据此返回明确的并发冲突错误，不得退化为“先 Get 再 Set”。
func (s *SQLiteSettingsStore) CompareAndSwapValue(ctx context.Context, category, key, expectedValue, newValue string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := `UPDATE settings SET value = ? WHERE category = ? AND key = ? AND value = ?`
	result, err := s.db.ExecContext(ctx, query, newValue, category, key, expectedValue)
	if err != nil {
		return false, fmt.Errorf("原子更新设置 %s.%s 失败: %w", category, key, err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("读取更新结果失败: %w", err)
	}
	return updated == 1, nil
}

// DeleteByCategory 删除分类下的所有设置
func (s *SQLiteSettingsStore) DeleteByCategory(ctx context.Context, category string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := `DELETE FROM settings WHERE category = ?`

	_, err := s.db.ExecContext(ctx, query, category)
	if err != nil {
		return fmt.Errorf("删除分类设置失败: %w", err)
	}

	return nil
}

// InitDefaults 初始化默认设置（仅当设置为空时）
func (s *SQLiteSettingsStore) InitDefaults(ctx context.Context, defaults []*SettingRecord) error {
	initialized, err := s.IsInitialized(ctx)
	if err != nil {
		return err
	}

	if initialized {
		return nil // 已初始化，跳过
	}

	return s.BatchSet(ctx, defaults)
}

// IsInitialized 检查是否已初始化（设置表是否有数据）
func (s *SQLiteSettingsStore) IsInitialized(ctx context.Context) (bool, error) {
	count, err := s.Count(ctx)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// SyncMetadata 同步设置元数据（label、description等），保留用户设置的value
func (s *SQLiteSettingsStore) SyncMetadata(ctx context.Context, defaults []*SettingRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(defaults) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始事务失败: %w", err)
	}
	defer tx.Rollback()

	// 只更新元数据，不更新 value（保留用户设置的值）
	// 如果记录不存在，则插入默认值
	query := `
		INSERT INTO settings (category, key, value, value_type, label, description, display_order, requires_restart)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(category, key) DO UPDATE SET
			value_type = excluded.value_type,
			label = excluded.label,
			description = excluded.description,
			display_order = excluded.display_order,
			requires_restart = excluded.requires_restart
	`

	stmt, err := tx.PrepareContext(ctx, query)
	if err != nil {
		return fmt.Errorf("准备语句失败: %w", err)
	}
	defer stmt.Close()

	for _, record := range defaults {
		_, err = stmt.ExecContext(ctx,
			record.Category, record.Key, record.Value, record.ValueType,
			record.Label, record.Description, record.DisplayOrder,
			boolToInt(record.RequiresRestart),
		)
		if err != nil {
			return fmt.Errorf("同步元数据 %s.%s 失败: %w", record.Category, record.Key, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交事务失败: %w", err)
	}

	return nil
}

// Count 获取设置总数
func (s *SQLiteSettingsStore) Count(ctx context.Context) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var count int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM settings").Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("获取设置数量失败: %w", err)
	}

	return count, nil
}

// CountByCategory 获取分类下的设置数量
func (s *SQLiteSettingsStore) CountByCategory(ctx context.Context, category string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var count int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM settings WHERE category = ?", category).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("获取分类设置数量失败: %w", err)
	}

	return count, nil
}

// ListCategories 获取所有分类
func (s *SQLiteSettingsStore) ListCategories(ctx context.Context) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `SELECT DISTINCT category FROM settings ORDER BY category ASC`

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("获取分类列表失败: %w", err)
	}
	defer rows.Close()

	var categories []string
	for rows.Next() {
		var category string
		if err := rows.Scan(&category); err != nil {
			return nil, fmt.Errorf("扫描分类失败: %w", err)
		}
		categories = append(categories, category)
	}

	return categories, nil
}

// scanSettings 扫描多个设置记录
func (s *SQLiteSettingsStore) scanSettings(ctx context.Context, query string, args ...interface{}) ([]*SettingRecord, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询设置失败: %w", err)
	}
	defer rows.Close()

	var records []*SettingRecord
	for rows.Next() {
		var record SettingRecord
		var requiresRestart int
		var createdAt, updatedAt string

		err := rows.Scan(
			&record.ID, &record.Category, &record.Key, &record.Value, &record.ValueType,
			&record.Label, &record.Description, &record.DisplayOrder,
			&requiresRestart, &createdAt, &updatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("扫描设置记录失败: %w", err)
		}

		record.RequiresRestart = requiresRestart == 1
		if record.CreatedAt, err = parseDBTime(createdAt); err != nil {
			return nil, fmt.Errorf("解析设置 created_at 失败: %w", err)
		}
		if record.UpdatedAt, err = parseDBTime(updatedAt); err != nil {
			return nil, fmt.Errorf("解析设置 updated_at 失败: %w", err)
		}

		records = append(records, &record)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历设置记录失败: %w", err)
	}

	return records, nil
}
