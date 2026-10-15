// Package store 提供数据存储层实现
// 端点存储 - v5.0.0 新增 (2025-12-05)
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"sinuo/config"
)

// EndpointRecord 表示数据库中的端点记录
type EndpointRecord struct {
	ID int64 `json:"id"`

	// 基本信息
	Name string `json:"name"` // 端点唯一名称
	URL  string `json:"url"`  // 端点 URL

	// 认证配置
	Token   string            `json:"token,omitempty"`   // Bearer Token
	ApiKey  string            `json:"api_key,omitempty"` // API Key
	Headers map[string]string `json:"headers,omitempty"` // 自定义请求头

	// 路由配置
	Priority        int  `json:"priority"`         // 优先级（数字越小越高）
	FailoverEnabled bool `json:"failover_enabled"` // 是否参与故障转移
	CooldownSeconds *int `json:"cooldown_seconds"` // 冷却时间（秒，nil=使用全局配置）
	TimeoutSeconds  int  `json:"timeout_seconds"`  // 请求超时（秒）

	// 功能支持
	SupportsCountTokens      bool   `json:"supports_count_tokens"` // 是否支持 count_tokens
	ModelRewriteRules        string `json:"model_rewrite_rules"`   // CC 端点模型兼容改写规则 JSON
	SoftFailureWindowMinutes *int   `json:"soft_failure_window_minutes"`
	SoftFailureThreshold     *int   `json:"soft_failure_threshold"`

	// 成本倍率
	CostMultiplier                float64 `json:"cost_multiplier"`
	InputCostMultiplier           float64 `json:"input_cost_multiplier"`
	OutputCostMultiplier          float64 `json:"output_cost_multiplier"`
	CacheCreationCostMultiplier   float64 `json:"cache_creation_cost_multiplier"`    // 5分钟缓存创建倍率（默认）
	CacheCreationCostMultiplier1h float64 `json:"cache_creation_cost_multiplier_1h"` // 1小时缓存创建倍率
	CacheReadCostMultiplier       float64 `json:"cache_read_cost_multiplier"`

	// 硬启用（nil 视为 true；false=任何模式都不可使用）
	AvailabilityEnabled *bool `json:"availability_enabled,omitempty"`

	// 审计字段
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// IsAvailabilityEnabled 硬启用状态（nil 默认 true）
func (r *EndpointRecord) IsAvailabilityEnabled() bool {
	if r == nil || r.AvailabilityEnabled == nil {
		return true
	}
	return *r.AvailabilityEnabled
}

// EndpointStore 定义端点存储接口
type EndpointStore interface {
	// CRUD 操作
	Create(ctx context.Context, record *EndpointRecord) (*EndpointRecord, error)
	Get(ctx context.Context, name string) (*EndpointRecord, error)
	GetByID(ctx context.Context, id int64) (*EndpointRecord, error)
	List(ctx context.Context) ([]*EndpointRecord, error)
	Update(ctx context.Context, record *EndpointRecord) error
	Delete(ctx context.Context, name string) error

	// 批量操作
	BatchCreate(ctx context.Context, records []*EndpointRecord) error
	BatchDelete(ctx context.Context, names []string) error

	// 统计
	Count(ctx context.Context) (int, error)

	// SetAvailabilityEnabled 更新硬启用状态（v8）
	SetAvailabilityEnabled(ctx context.Context, name string, enabled bool) error

	// SetFailoverEnabled 更新“参与自动调度”状态（物理列仍为 failover_enabled）
	SetFailoverEnabled(ctx context.Context, name string, enabled bool) error

	// 事务支持
	WithTx(tx *sql.Tx) EndpointStore
}

// SQLiteEndpointStore 实现 EndpointStore 接口
type SQLiteEndpointStore struct {
	db *sql.DB
	tx *sql.Tx // 事务上下文（可选）
	mu sync.RWMutex
}

// NewSQLiteEndpointStore 创建新的 SQLite 端点存储
func NewSQLiteEndpointStore(db *sql.DB) *SQLiteEndpointStore {
	return &SQLiteEndpointStore{db: db}
}

func marshalEndpointHeaders(headers map[string]string) ([]byte, error) {
	if headers == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(headers)
}

// getQuerier 返回用于执行查询的对象（事务或数据库连接）
func (s *SQLiteEndpointStore) getQuerier() interface {
	QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
} {
	if s.tx != nil {
		return s.tx
	}
	return s.db
}

// Create 创建新端点
func (s *SQLiteEndpointStore) Create(ctx context.Context, record *EndpointRecord) (*EndpointRecord, error) {
	if err := config.ValidateSoftFailureOverrides(record.SoftFailureWindowMinutes, record.SoftFailureThreshold); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// 序列化 headers
	headersJSON, err := marshalEndpointHeaders(record.Headers)
	if err != nil {
		return nil, fmt.Errorf("序列化 headers 失败: %w", err)
	}

	// 设置默认值
	if record.CostMultiplier == 0 {
		record.CostMultiplier = 1.0
	}
	if record.InputCostMultiplier == 0 {
		record.InputCostMultiplier = 1.0
	}
	if record.OutputCostMultiplier == 0 {
		record.OutputCostMultiplier = 1.0
	}
	if record.CacheCreationCostMultiplier == 0 {
		record.CacheCreationCostMultiplier = 1.0
	}
	if record.CacheCreationCostMultiplier1h == 0 {
		record.CacheCreationCostMultiplier1h = 1.0
	}
	if record.CacheReadCostMultiplier == 0 {
		record.CacheReadCostMultiplier = 1.0
	}
	if record.TimeoutSeconds == 0 {
		record.TimeoutSeconds = 300
	}

	query := `
		INSERT INTO endpoints (
			name, url, token, api_key, headers,
			priority, failover_enabled, cooldown_seconds, timeout_seconds,
			supports_count_tokens, model_rewrite_rules, soft_failure_window_minutes, soft_failure_threshold,
			cost_multiplier, input_cost_multiplier, output_cost_multiplier,
			cache_creation_cost_multiplier, cache_creation_cost_multiplier_1h, cache_read_cost_multiplier,
			availability_enabled
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	result, err := s.getQuerier().ExecContext(ctx, query,
		record.Name, record.URL, record.Token, record.ApiKey, string(headersJSON),
		record.Priority, boolToInt(record.FailoverEnabled), record.CooldownSeconds, record.TimeoutSeconds,
		boolToInt(record.SupportsCountTokens), record.ModelRewriteRules, record.SoftFailureWindowMinutes, record.SoftFailureThreshold,
		record.CostMultiplier, record.InputCostMultiplier, record.OutputCostMultiplier,
		record.CacheCreationCostMultiplier, record.CacheCreationCostMultiplier1h, record.CacheReadCostMultiplier,
		boolToInt(record.IsAvailabilityEnabled()),
	)
	if err != nil {
		return nil, fmt.Errorf("创建端点失败: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("获取插入 ID 失败: %w", err)
	}

	record.ID = id
	record.CreatedAt = time.Now()
	record.UpdatedAt = time.Now()

	return record, nil
}

// Get 根据名称获取端点
func (s *SQLiteEndpointStore) Get(ctx context.Context, name string) (*EndpointRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
		SELECT id, name, url, token, api_key, headers,
			priority, failover_enabled, cooldown_seconds, timeout_seconds,
			supports_count_tokens, model_rewrite_rules, soft_failure_window_minutes, soft_failure_threshold,
			cost_multiplier, input_cost_multiplier, output_cost_multiplier,
			cache_creation_cost_multiplier, cache_creation_cost_multiplier_1h, cache_read_cost_multiplier,
			availability_enabled, CAST(created_at AS TEXT), CAST(updated_at AS TEXT)
		FROM endpoints WHERE name = ?
	`

	return s.scanEndpoint(s.getQuerier().QueryRowContext(ctx, query, name))
}

// GetByID 根据 ID 获取端点
func (s *SQLiteEndpointStore) GetByID(ctx context.Context, id int64) (*EndpointRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
		SELECT id, name, url, token, api_key, headers,
			priority, failover_enabled, cooldown_seconds, timeout_seconds,
			supports_count_tokens, model_rewrite_rules, soft_failure_window_minutes, soft_failure_threshold,
			cost_multiplier, input_cost_multiplier, output_cost_multiplier,
			cache_creation_cost_multiplier, cache_creation_cost_multiplier_1h, cache_read_cost_multiplier,
			availability_enabled, CAST(created_at AS TEXT), CAST(updated_at AS TEXT)
		FROM endpoints WHERE id = ?
	`

	return s.scanEndpoint(s.getQuerier().QueryRowContext(ctx, query, id))
}

// List 获取所有端点
func (s *SQLiteEndpointStore) List(ctx context.Context) ([]*EndpointRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
		SELECT id, name, url, token, api_key, headers,
			priority, failover_enabled, cooldown_seconds, timeout_seconds,
			supports_count_tokens, model_rewrite_rules, soft_failure_window_minutes, soft_failure_threshold,
			cost_multiplier, input_cost_multiplier, output_cost_multiplier,
			cache_creation_cost_multiplier, cache_creation_cost_multiplier_1h, cache_read_cost_multiplier,
			availability_enabled, CAST(created_at AS TEXT), CAST(updated_at AS TEXT)
		FROM endpoints
		ORDER BY priority ASC, name ASC
	`

	return s.scanEndpoints(ctx, query)
}

// Update 更新端点
func (s *SQLiteEndpointStore) Update(ctx context.Context, record *EndpointRecord) error {
	if err := config.ValidateSoftFailureOverrides(record.SoftFailureWindowMinutes, record.SoftFailureThreshold); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// 序列化 headers
	headersJSON, err := marshalEndpointHeaders(record.Headers)
	if err != nil {
		return fmt.Errorf("序列化 headers 失败: %w", err)
	}

	query := `
		UPDATE endpoints SET
			url = ?, token = ?, api_key = ?, headers = ?,
			priority = ?, failover_enabled = ?, cooldown_seconds = ?, timeout_seconds = ?,
			supports_count_tokens = ?, model_rewrite_rules = ?, soft_failure_window_minutes = ?, soft_failure_threshold = ?,
			cost_multiplier = ?, input_cost_multiplier = ?, output_cost_multiplier = ?,
			cache_creation_cost_multiplier = ?, cache_creation_cost_multiplier_1h = ?, cache_read_cost_multiplier = ?,
			availability_enabled = ?
		WHERE name = ?
	`

	result, err := s.getQuerier().ExecContext(ctx, query,
		record.URL, record.Token, record.ApiKey, string(headersJSON),
		record.Priority, boolToInt(record.FailoverEnabled), record.CooldownSeconds, record.TimeoutSeconds,
		boolToInt(record.SupportsCountTokens), record.ModelRewriteRules, record.SoftFailureWindowMinutes, record.SoftFailureThreshold,
		record.CostMultiplier, record.InputCostMultiplier, record.OutputCostMultiplier,
		record.CacheCreationCostMultiplier, record.CacheCreationCostMultiplier1h, record.CacheReadCostMultiplier,
		boolToInt(record.IsAvailabilityEnabled()),
		record.Name,
	)
	if err != nil {
		return fmt.Errorf("更新端点失败: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("获取影响行数失败: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("端点不存在: %s", record.Name)
	}

	return nil
}

// Delete 删除端点，并在同一事务内清理它的运行态。
// 连接未启用外键，schema 里 endpoint_runtime_states 的 ON DELETE CASCADE 不会触发，必须显式删除。
// 已处于 WithTx 事务中时沿用调用方事务，由调用方负责提交。
func (s *SQLiteEndpointStore) Delete(ctx context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.tx != nil {
		return deleteEndpointWithRuntimeStates(ctx, s.tx, name)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开启删除端点事务失败: %w", err)
	}
	defer tx.Rollback()
	if err := deleteEndpointWithRuntimeStates(ctx, tx, name); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交删除端点事务失败: %w", err)
	}
	return nil
}

func deleteEndpointWithRuntimeStates(ctx context.Context, tx *sql.Tx, name string) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM endpoint_runtime_states WHERE endpoint_id IN (SELECT id FROM endpoints WHERE name = ?)`, name); err != nil {
		return fmt.Errorf("删除端点运行态失败: %w", err)
	}

	result, err := tx.ExecContext(ctx, `DELETE FROM endpoints WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("删除端点失败: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("获取影响行数失败: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("端点不存在: %s", name)
	}

	return nil
}

// SetAvailabilityEnabled 更新硬启用状态（v8）
func (s *SQLiteEndpointStore) SetAvailabilityEnabled(ctx context.Context, name string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	result, err := s.getQuerier().ExecContext(ctx,
		`UPDATE endpoints SET availability_enabled = ? WHERE name = ?`, boolToInt(enabled), name)
	if err != nil {
		return fmt.Errorf("更新端点硬启用状态失败: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("获取影响行数失败: %w", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("端点不存在: %s", name)
	}
	return nil
}

// SetFailoverEnabled 更新“参与自动调度”状态（物理列 failover_enabled）
func (s *SQLiteEndpointStore) SetFailoverEnabled(ctx context.Context, name string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	result, err := s.getQuerier().ExecContext(ctx,
		`UPDATE endpoints SET failover_enabled = ? WHERE name = ?`, boolToInt(enabled), name)
	if err != nil {
		return fmt.Errorf("更新端点自动调度状态失败: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("获取影响行数失败: %w", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("端点不存在: %s", name)
	}
	return nil
}

// BatchCreate 批量创建端点
func (s *SQLiteEndpointStore) BatchCreate(ctx context.Context, records []*EndpointRecord) error {
	for _, record := range records {
		if err := config.ValidateSoftFailureOverrides(record.SoftFailureWindowMinutes, record.SoftFailureThreshold); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(records) == 0 {
		return nil
	}

	// 使用事务
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始事务失败: %w", err)
	}
	defer tx.Rollback()

	query := `
		INSERT INTO endpoints (
			name, url, token, api_key, headers,
			priority, failover_enabled, cooldown_seconds, timeout_seconds,
			supports_count_tokens, model_rewrite_rules, soft_failure_window_minutes, soft_failure_threshold,
			cost_multiplier, input_cost_multiplier, output_cost_multiplier,
			cache_creation_cost_multiplier, cache_creation_cost_multiplier_1h, cache_read_cost_multiplier,
			availability_enabled
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	stmt, err := tx.PrepareContext(ctx, query)
	if err != nil {
		return fmt.Errorf("准备语句失败: %w", err)
	}
	defer stmt.Close()

	for _, record := range records {
		// 设置默认值
		if record.CostMultiplier == 0 {
			record.CostMultiplier = 1.0
		}
		if record.InputCostMultiplier == 0 {
			record.InputCostMultiplier = 1.0
		}
		if record.OutputCostMultiplier == 0 {
			record.OutputCostMultiplier = 1.0
		}
		if record.CacheCreationCostMultiplier == 0 {
			record.CacheCreationCostMultiplier = 1.0
		}
		if record.CacheCreationCostMultiplier1h == 0 {
			record.CacheCreationCostMultiplier1h = 1.0
		}
		if record.CacheReadCostMultiplier == 0 {
			record.CacheReadCostMultiplier = 1.0
		}
		if record.TimeoutSeconds == 0 {
			record.TimeoutSeconds = 300
		}

		headersJSON, err := marshalEndpointHeaders(record.Headers)
		if err != nil {
			return fmt.Errorf("序列化 headers 失败: %w", err)
		}

		_, err = stmt.ExecContext(ctx,
			record.Name, record.URL, record.Token, record.ApiKey, string(headersJSON),
			record.Priority, boolToInt(record.FailoverEnabled), record.CooldownSeconds, record.TimeoutSeconds,
			boolToInt(record.SupportsCountTokens), record.ModelRewriteRules, record.SoftFailureWindowMinutes, record.SoftFailureThreshold,
			record.CostMultiplier, record.InputCostMultiplier, record.OutputCostMultiplier,
			record.CacheCreationCostMultiplier, record.CacheCreationCostMultiplier1h, record.CacheReadCostMultiplier,
			boolToInt(record.IsAvailabilityEnabled()),
		)
		if err != nil {
			return fmt.Errorf("插入端点 %s 失败: %w", record.Name, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交事务失败: %w", err)
	}

	return nil
}

// BatchDelete 批量删除端点
func (s *SQLiteEndpointStore) BatchDelete(ctx context.Context, names []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(names) == 0 {
		return nil
	}

	placeholders := make([]string, len(names))
	args := make([]interface{}, len(names))
	for i, name := range names {
		placeholders[i] = "?"
		args[i] = name
	}

	query := fmt.Sprintf("DELETE FROM endpoints WHERE name IN (%s)", strings.Join(placeholders, ","))

	_, err := s.getQuerier().ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("批量删除端点失败: %w", err)
	}

	return nil
}

// Count 获取端点总数
func (s *SQLiteEndpointStore) Count(ctx context.Context) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var count int
	err := s.getQuerier().QueryRowContext(ctx, "SELECT COUNT(*) FROM endpoints").Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("获取端点数量失败: %w", err)
	}

	return count, nil
}

// WithTx 返回使用事务的存储实例
func (s *SQLiteEndpointStore) WithTx(tx *sql.Tx) EndpointStore {
	return &SQLiteEndpointStore{
		db: s.db,
		tx: tx,
	}
}

// scanEndpoint 从单行扫描端点记录
func (s *SQLiteEndpointStore) scanEndpoint(row *sql.Row) (*EndpointRecord, error) {
	var record EndpointRecord
	var headersJSON string
	var cooldownSeconds sql.NullInt64
	var failoverEnabled, supportsCountTokens, availabilityEnabled int
	var createdAt, updatedAt string

	err := row.Scan(
		&record.ID, &record.Name, &record.URL,
		&record.Token, &record.ApiKey, &headersJSON,
		&record.Priority, &failoverEnabled, &cooldownSeconds, &record.TimeoutSeconds,
		&supportsCountTokens, &record.ModelRewriteRules, &record.SoftFailureWindowMinutes, &record.SoftFailureThreshold,
		&record.CostMultiplier, &record.InputCostMultiplier, &record.OutputCostMultiplier,
		&record.CacheCreationCostMultiplier, &record.CacheCreationCostMultiplier1h, &record.CacheReadCostMultiplier,
		&availabilityEnabled, &createdAt, &updatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("扫描端点记录失败: %w", err)
	}

	// 解析 headers
	if headersJSON != "" && headersJSON != "null" {
		if err := json.Unmarshal([]byte(headersJSON), &record.Headers); err != nil {
			// 忽略解析错误，保持 Headers 为 nil
		}
	}

	// 解析可空字段
	if cooldownSeconds.Valid {
		cd := int(cooldownSeconds.Int64)
		record.CooldownSeconds = &cd
	}

	// 转换布尔值
	if err := config.ValidateSoftFailureOverrides(record.SoftFailureWindowMinutes, record.SoftFailureThreshold); err != nil {
		return nil, fmt.Errorf("端点 %s 失败策略无效: %w", record.Name, err)
	}
	record.FailoverEnabled = failoverEnabled == 1
	record.SupportsCountTokens = supportsCountTokens == 1
	availValue := availabilityEnabled == 1
	record.AvailabilityEnabled = &availValue

	if record.CreatedAt, err = parseDBTime(createdAt); err != nil {
		return nil, fmt.Errorf("解析端点 created_at 失败: %w", err)
	}
	if record.UpdatedAt, err = parseDBTime(updatedAt); err != nil {
		return nil, fmt.Errorf("解析端点 updated_at 失败: %w", err)
	}

	return &record, nil
}

// scanEndpoints 扫描多个端点记录
func (s *SQLiteEndpointStore) scanEndpoints(ctx context.Context, query string) ([]*EndpointRecord, error) {
	return s.scanEndpointsWithArgs(ctx, query)
}

// scanEndpointsWithArgs 使用参数扫描多个端点记录
func (s *SQLiteEndpointStore) scanEndpointsWithArgs(ctx context.Context, query string, args ...interface{}) ([]*EndpointRecord, error) {
	rows, err := s.getQuerier().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询端点失败: %w", err)
	}
	defer rows.Close()

	var records []*EndpointRecord
	for rows.Next() {
		var record EndpointRecord
		var headersJSON string
		var cooldownSeconds sql.NullInt64
		var failoverEnabled, supportsCountTokens, availabilityEnabled int
		var createdAt, updatedAt string

		err := rows.Scan(
			&record.ID, &record.Name, &record.URL,
			&record.Token, &record.ApiKey, &headersJSON,
			&record.Priority, &failoverEnabled, &cooldownSeconds, &record.TimeoutSeconds,
			&supportsCountTokens, &record.ModelRewriteRules, &record.SoftFailureWindowMinutes, &record.SoftFailureThreshold,
			&record.CostMultiplier, &record.InputCostMultiplier, &record.OutputCostMultiplier,
			&record.CacheCreationCostMultiplier, &record.CacheCreationCostMultiplier1h, &record.CacheReadCostMultiplier,
			&availabilityEnabled, &createdAt, &updatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("扫描端点记录失败: %w", err)
		}

		// 解析 headers
		if headersJSON != "" && headersJSON != "null" {
			if err := json.Unmarshal([]byte(headersJSON), &record.Headers); err != nil {
				// 忽略解析错误
			}
		}

		// 解析可空字段
		if cooldownSeconds.Valid {
			cd := int(cooldownSeconds.Int64)
			record.CooldownSeconds = &cd
		}

		// 转换布尔值
		if err := config.ValidateSoftFailureOverrides(record.SoftFailureWindowMinutes, record.SoftFailureThreshold); err != nil {
			return nil, fmt.Errorf("端点 %s 失败策略无效: %w", record.Name, err)
		}
		record.FailoverEnabled = failoverEnabled == 1
		record.SupportsCountTokens = supportsCountTokens == 1
		availValue := availabilityEnabled == 1
		record.AvailabilityEnabled = &availValue

		if record.CreatedAt, err = parseDBTime(createdAt); err != nil {
			return nil, fmt.Errorf("解析端点 created_at 失败: %w", err)
		}
		if record.UpdatedAt, err = parseDBTime(updatedAt); err != nil {
			return nil, fmt.Errorf("解析端点 updated_at 失败: %w", err)
		}

		records = append(records, &record)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历端点记录失败: %w", err)
	}

	return records, nil
}

// boolToInt 将布尔值转换为整数
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
