// Package store 提供数据存储层实现
// 模型定价存储 - v5.0.0 新增 (2025-12-06)
package store

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"
)

// ModelPricingRecord 表示数据库中的模型定价记录
type ModelPricingRecord struct {
	ID int64 `json:"id"`
	// RateSource 仅在发布时指定；旧库已有的报价保留原来源。
	RateSource string `json:"-"`
	// MetadataOnly 只用于 Update，保留当前单价及其版本。
	MetadataOnly bool `json:"-"`

	// 模型信息
	ModelName string `json:"model_name"` // 模型名称（如 claude-sonnet-4-20250514）

	// 定价信息 (USD per 1M tokens)
	InputPrice           float64 `json:"input_price"`             // 输入价格
	OutputPrice          float64 `json:"output_price"`            // 输出价格
	CacheCreationPrice5m float64 `json:"cache_creation_price_5m"` // 5分钟缓存创建价格 (input * 1.25)
	CacheCreationPrice1h float64 `json:"cache_creation_price_1h"` // 1小时缓存创建价格 (input * 2.0)
	CacheReadPrice       float64 `json:"cache_read_price"`        // 缓存读取价格 (input * 0.1)

	// 模型元信息
	DisplayName string `json:"display_name,omitempty"` // 显示名称
	Description string `json:"description,omitempty"`  // 模型描述
	IsDefault   bool   `json:"is_default"`             // 是否为默认定价

	// 审计字段
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ModelPricingStore 定义模型定价存储接口
type ModelPricingStore interface {
	CurrentBaseRate(context.Context, string) (BaseRateVersion, error)
	CurrentRateSheet(context.Context, string) (*RateSheet, error)
	CurrentChannelRule(context.Context, BillingChannel, string) (ChannelModelRule, error)
	SeedRateSheet(context.Context, string, RateSheet) error
	SeedResponsesRateSheet(context.Context, *ModelPricingRecord, RateSheet) (bool, error)
	ReadPricingCatalog(context.Context) ([]BaseRateVersion, []ChannelRuleVersion, error)
	ListChannelRules(context.Context) ([]ChannelRuleVersion, error)
	PublishRateSheet(context.Context, string, string, RateSheet) error
	SaveChannelModelRule(context.Context, BillingChannel, string, ChannelModelRule) error
	ListBaseRates(context.Context) ([]BaseRateVersion, error)
	// CRUD 操作
	Create(ctx context.Context, record *ModelPricingRecord) (*ModelPricingRecord, error)
	Get(ctx context.Context, modelName string) (*ModelPricingRecord, error)
	GetByID(ctx context.Context, id int64) (*ModelPricingRecord, error)
	List(ctx context.Context) ([]*ModelPricingRecord, error)
	Update(ctx context.Context, record *ModelPricingRecord) error
	Delete(ctx context.Context, modelName string) error

	// 批量操作
	BatchCreate(ctx context.Context, records []*ModelPricingRecord) error
	BatchUpsert(ctx context.Context, records []*ModelPricingRecord) error

	// 查询
	GetDefault(ctx context.Context) (*ModelPricingRecord, error)
	SetDefault(ctx context.Context, modelName string) error

	// 统计
	Count(ctx context.Context) (int, error)

	// 事务支持
	WithTx(tx *sql.Tx) ModelPricingStore
}

// SQLiteModelPricingStore 实现 ModelPricingStore 接口
type SQLiteModelPricingStore struct {
	db *sql.DB
	tx *sql.Tx // 事务上下文（可选）
	mu sync.RWMutex
}

// NewSQLiteModelPricingStore 创建新的 SQLite 模型定价存储
func NewSQLiteModelPricingStore(db *sql.DB) *SQLiteModelPricingStore {
	return &SQLiteModelPricingStore{db: db}
}

// getQuerier 返回用于执行查询的对象（事务或数据库连接）
func (s *SQLiteModelPricingStore) getQuerier() interface {
	QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
} {
	if s.tx != nil {
		return s.tx
	}
	return s.db
}

// Create 创建新的模型定价
func (s *SQLiteModelPricingStore) Create(ctx context.Context, record *ModelPricingRecord) (*ModelPricingRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	created := *record
	err := s.writeTransaction(ctx, func(tx *sql.Tx) error {
		result, err := writePricing(ctx, tx, insertPricingSQL, &created)
		if err != nil {
			return err
		}
		created.ID, err = result.LastInsertId()
		if err != nil {
			return err
		}
		return clearOtherPricingDefaults(ctx, tx, &created)
	})
	if err != nil {
		return nil, fmt.Errorf("创建模型定价失败: %w", err)
	}
	created.CreatedAt = time.Now()
	created.UpdatedAt = created.CreatedAt
	*record = created
	return record, nil
}

// 旧表的可空缓存单价只在数值型展示接口中显示为 0。
const selectModelPricing = `
	SELECT id, model_name, input_price, output_price,
		COALESCE(cache_creation_price_5m, 0), COALESCE(cache_creation_price_1h, 0), COALESCE(cache_read_price, 0),
		display_name, description, is_default,
		CAST(created_at AS TEXT), CAST(updated_at AS TEXT)
	FROM model_pricing`

// Get 根据模型名称获取定价
func (s *SQLiteModelPricingStore) Get(ctx context.Context, modelName string) (*ModelPricingRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := selectModelPricing + " WHERE model_name = ?"
	return s.scanModelPricing(s.getQuerier().QueryRowContext(ctx, query, modelName))
}

// GetByID 根据 ID 获取定价
func (s *SQLiteModelPricingStore) GetByID(ctx context.Context, id int64) (*ModelPricingRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := selectModelPricing + " WHERE id = ?"
	return s.scanModelPricing(s.getQuerier().QueryRowContext(ctx, query, id))
}

// List 获取所有模型定价
func (s *SQLiteModelPricingStore) List(ctx context.Context) ([]*ModelPricingRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := selectModelPricing + " ORDER BY is_default DESC, model_name ASC"
	return s.scanModelPricings(ctx, query)
}

// Update 更新模型定价
func (s *SQLiteModelPricingStore) Update(ctx context.Context, record *ModelPricingRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.writeTransaction(ctx, func(tx *sql.Tx) error {
		if _, err := writePricing(ctx, tx, updatePricingSQL, record); err != nil {
			return err
		}
		return clearOtherPricingDefaults(ctx, tx, record)
	})
}

// clearOtherPricingDefaults 与设置新默认的单条写入共用事务。
func clearOtherPricingDefaults(ctx context.Context, tx *sql.Tx, record *ModelPricingRecord) error {
	if !record.IsDefault {
		return nil
	}
	_, err := tx.ExecContext(ctx, "UPDATE model_pricing SET is_default = 0 WHERE is_default = 1 AND model_name <> ?", record.ModelName)
	if err != nil {
		return fmt.Errorf("清除旧默认定价失败: %w", err)
	}
	return nil
}

// Delete 移除当前配置，并关闭该模型当前费率行的生效区间。
func (s *SQLiteModelPricingStore) Delete(ctx context.Context, modelName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeTransaction(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "DELETE FROM model_pricing WHERE model_name = ?", modelName)
		if err != nil {
			return err
		}
		if err := requirePricingRow(result, modelName); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, closeBaseRateSQL, time.Now().UnixNano(), modelName)
		return err
	})
}

func (s *SQLiteModelPricingStore) BatchCreate(ctx context.Context, records []*ModelPricingRecord) error {
	return s.writePricingBatch(ctx, insertPricingSQL, records)
}

func (s *SQLiteModelPricingStore) BatchUpsert(ctx context.Context, records []*ModelPricingRecord) error {
	return s.writePricingBatch(ctx, upsertPricingSQL, records)
}

// GetDefault 获取默认定价
func (s *SQLiteModelPricingStore) GetDefault(ctx context.Context) (*ModelPricingRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := selectModelPricing + " WHERE is_default = 1 LIMIT 1"
	return s.scanModelPricing(s.getQuerier().QueryRowContext(ctx, query))
}

// SetDefault 设置默认定价（取消其他默认）
func (s *SQLiteModelPricingStore) SetDefault(ctx context.Context, modelName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 使用事务
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始事务失败: %w", err)
	}
	defer tx.Rollback()

	// 先清除所有默认标记
	_, err = tx.ExecContext(ctx, "UPDATE model_pricing SET is_default = 0")
	if err != nil {
		return fmt.Errorf("清除默认标记失败: %w", err)
	}

	// 设置新的默认
	result, err := tx.ExecContext(ctx, "UPDATE model_pricing SET is_default = 1 WHERE model_name = ?", modelName)
	if err != nil {
		return fmt.Errorf("设置默认定价失败: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("获取影响行数失败: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("模型定价不存在: %s", modelName)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交事务失败: %w", err)
	}

	return nil
}

// Count 获取模型定价总数
func (s *SQLiteModelPricingStore) Count(ctx context.Context) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var count int
	err := s.getQuerier().QueryRowContext(ctx, "SELECT COUNT(*) FROM model_pricing").Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("获取模型定价数量失败: %w", err)
	}

	return count, nil
}

// WithTx 返回使用事务的存储实例
func (s *SQLiteModelPricingStore) WithTx(tx *sql.Tx) ModelPricingStore {
	return &SQLiteModelPricingStore{
		db: s.db,
		tx: tx,
	}
}

// scanModelPricing 从单行扫描模型定价记录
func (s *SQLiteModelPricingStore) scanModelPricing(row *sql.Row) (*ModelPricingRecord, error) {
	var record ModelPricingRecord
	var displayName, description sql.NullString
	var isDefault int
	var createdAt, updatedAt string

	err := row.Scan(
		&record.ID, &record.ModelName,
		&record.InputPrice, &record.OutputPrice,
		&record.CacheCreationPrice5m, &record.CacheCreationPrice1h, &record.CacheReadPrice,
		&displayName, &description, &isDefault,
		&createdAt, &updatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("扫描模型定价记录失败: %w", err)
	}

	// 解析可空字段
	if displayName.Valid {
		record.DisplayName = displayName.String
	}
	if description.Valid {
		record.Description = description.String
	}

	// 转换布尔值
	record.IsDefault = isDefault == 1

	if record.CreatedAt, err = parseDBTime(createdAt); err != nil {
		return nil, fmt.Errorf("解析模型定价 created_at 失败: %w", err)
	}
	if record.UpdatedAt, err = parseDBTime(updatedAt); err != nil {
		return nil, fmt.Errorf("解析模型定价 updated_at 失败: %w", err)
	}

	return &record, nil
}

// scanModelPricings 扫描多个模型定价记录
func (s *SQLiteModelPricingStore) scanModelPricings(ctx context.Context, query string, args ...interface{}) ([]*ModelPricingRecord, error) {
	rows, err := s.getQuerier().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询模型定价失败: %w", err)
	}
	defer rows.Close()

	var records []*ModelPricingRecord
	for rows.Next() {
		var record ModelPricingRecord
		var displayName, description sql.NullString
		var isDefault int
		var createdAt, updatedAt string

		err := rows.Scan(
			&record.ID, &record.ModelName,
			&record.InputPrice, &record.OutputPrice,
			&record.CacheCreationPrice5m, &record.CacheCreationPrice1h, &record.CacheReadPrice,
			&displayName, &description, &isDefault,
			&createdAt, &updatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("扫描模型定价记录失败: %w", err)
		}

		// 解析可空字段
		if displayName.Valid {
			record.DisplayName = displayName.String
		}
		if description.Valid {
			record.Description = description.String
		}

		// 转换布尔值
		record.IsDefault = isDefault == 1

		if record.CreatedAt, err = parseDBTime(createdAt); err != nil {
			return nil, fmt.Errorf("解析模型定价 created_at 失败: %w", err)
		}
		if record.UpdatedAt, err = parseDBTime(updatedAt); err != nil {
			return nil, fmt.Errorf("解析模型定价 updated_at 失败: %w", err)
		}

		records = append(records, &record)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历模型定价记录失败: %w", err)
	}

	return records, nil
}
