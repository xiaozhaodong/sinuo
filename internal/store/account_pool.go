// Package store 提供账号池相关数据存储实现
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"sinuo/config"
	"sinuo/internal/accountauth"
	timezonepolicy "sinuo/internal/timezone"
)

const (
	defaultAccountBaseURL = "https://api.openai.com"
	defaultAccountState   = "active"
	defaultAccountPrio    = 100
	accountGroupPrimary   = "primary"
	accountGroupBackup    = "backup"
	accountGroupCold      = "cold"
)

// UpstreamAccountRecord 上游账号记录
type UpstreamAccountRecord struct {
	ID                            int64      `json:"id"`
	ProviderType                  string     `json:"provider_type"`
	AccountName                   string     `json:"account_name"`
	CredentialRaw                 string     `json:"credential_raw"`
	BaseURL                       string     `json:"base_url"`
	ModelRewriteRules             string     `json:"model_rewrite_rules"`
	SoftFailureWindowMinutes      *int       `json:"soft_failure_window_minutes"`
	SoftFailureThreshold          *int       `json:"soft_failure_threshold"`
	EnableRequestCompression      bool       `json:"enable_request_compression"`
	StripCodexInternalMetadata    bool       `json:"strip_codex_internal_metadata"` // 「标准 Responses 兼容」开关，为兼容保留列名
	CostMultiplier                float64    `json:"cost_multiplier"`
	InputCostMultiplier           float64    `json:"input_cost_multiplier"`
	OutputCostMultiplier          float64    `json:"output_cost_multiplier"`
	CacheCreationCostMultiplier   float64    `json:"cache_creation_cost_multiplier"`
	CacheCreationCostMultiplier1h float64    `json:"cache_creation_cost_multiplier_1h"`
	CacheReadCostMultiplier       float64    `json:"cache_read_cost_multiplier"`
	GroupKey                      string     `json:"group_key"`
	Priority                      int        `json:"priority"`
	Enabled                       bool       `json:"enabled"`
	State                         string     `json:"state"`
	CooldownUntil                 *time.Time `json:"cooldown_until,omitempty"`
	FailCount                     int        `json:"fail_count"`
	LastSuccessAt                 *time.Time `json:"last_success_at,omitempty"`
	LastError                     string     `json:"last_error"`
	PlanType                      string     `json:"plan_type"`
	ChatGPTAccountID              string     `json:"chatgpt_account_id"`
	ChatGPTUserID                 string     `json:"chatgpt_user_id"`
	OrganizationID                string     `json:"organization_id"`
	Quota5HUsedPercent            *float64   `json:"quota_5h_used_percent,omitempty"`
	Quota5HResetAt                *time.Time `json:"quota_5h_reset_at,omitempty"`
	QuotaWeeklyUsedPercent        *float64   `json:"quota_weekly_used_percent,omitempty"`
	QuotaWeeklyResetAt            *time.Time `json:"quota_weekly_reset_at,omitempty"`
	QuotaStatus                   string     `json:"quota_status"`
	QuotaRefreshedAt              *time.Time `json:"quota_refreshed_at,omitempty"`
	Fingerprint                   string     `json:"fingerprint"`
	CreatedAt                     time.Time  `json:"created_at"`
	UpdatedAt                     time.Time  `json:"updated_at"`
}

type AccountSchedulingUpdate struct {
	GroupKey string `json:"group_key"`
	Priority int    `json:"priority"`
}

// AccountPoolStore 账号池存储接口
type AccountPoolStore interface {
	CreateAccount(ctx context.Context, record *UpstreamAccountRecord) (*UpstreamAccountRecord, error)
	UpdateAccount(ctx context.Context, record *UpstreamAccountRecord) error
	UpdateAccountPriorities(ctx context.Context, updates map[int64]int) error
	UpdateAccountScheduling(ctx context.Context, updates map[int64]AccountSchedulingUpdate) error
	DeleteAccount(ctx context.Context, id int64) error
	GetAccount(ctx context.Context, id int64) (*UpstreamAccountRecord, error)
	ListAccounts(ctx context.Context, includeDisabled bool) ([]*UpstreamAccountRecord, error)
	ListSchedulableAccounts(ctx context.Context, now time.Time) ([]*UpstreamAccountRecord, error)
	FindAccountByFingerprint(ctx context.Context, fingerprint string) (*UpstreamAccountRecord, error)
	ToggleAccount(ctx context.Context, id int64, enabled bool) error
	MarkAccountSuccess(ctx context.Context, id int64, successAt time.Time) error
	MarkAccountSuccessIfNoNewerFailure(ctx context.Context, id int64, successAt, attemptStartedAt time.Time) (bool, error)
	MarkAccountAuthFailed(ctx context.Context, id int64, reason string) error
	MarkAccountAuthFailedWithProfile(ctx context.Context, record *UpstreamAccountRecord, reason string) error
	MarkAccountTransientFailure(ctx context.Context, id int64, reason string, cooldownUntil time.Time) error
	SaveAccountRuntimeState(ctx context.Context, id int64, state AccountRuntimeState) error
	UpdateAccountProfile(ctx context.Context, record *UpstreamAccountRecord) error
}

// SQLiteAccountPoolStore SQLite 账号池存储实现
type SQLiteAccountPoolStore struct {
	db                *sql.DB
	tx                *sql.Tx
	mu                sync.RWMutex
	schemaCompatOnce  sync.Once
	schemaCompatError error
}

// NewSQLiteAccountPoolStore 创建账号池存储
func NewSQLiteAccountPoolStore(db *sql.DB) *SQLiteAccountPoolStore {
	return &SQLiteAccountPoolStore{db: db}
}

func (s *SQLiteAccountPoolStore) ensureSchemaCompatibility(ctx context.Context) error {
	if s == nil || s.db == nil {
		return nil
	}

	s.schemaCompatOnce.Do(func() {
		migrations := []struct {
			column string
			sql    string
			errMsg string
		}{
			{
				column: "soft_failure_window_minutes",
				sql:    `ALTER TABLE upstream_accounts ADD COLUMN soft_failure_window_minutes INTEGER`,
				errMsg: "补齐 soft_failure_window_minutes 字段失败",
			},
			{
				column: "soft_failure_threshold",
				sql:    `ALTER TABLE upstream_accounts ADD COLUMN soft_failure_threshold INTEGER`,
				errMsg: "补齐 soft_failure_threshold 字段失败",
			},
			{
				column: "group_key",
				sql:    `ALTER TABLE upstream_accounts ADD COLUMN group_key TEXT DEFAULT ''`,
				errMsg: "补齐 group_key 字段失败",
			},
			{
				column: "model_rewrite_rules",
				sql:    `ALTER TABLE upstream_accounts ADD COLUMN model_rewrite_rules TEXT DEFAULT ''`,
				errMsg: "补齐 model_rewrite_rules 字段失败",
			},
			{
				column: "strip_codex_internal_metadata",
				sql:    `ALTER TABLE upstream_accounts ADD COLUMN strip_codex_internal_metadata INTEGER NOT NULL DEFAULT 0`,
				errMsg: "补齐 strip_codex_internal_metadata 字段失败",
			},
			{
				column: "enable_request_compression",
				sql:    `ALTER TABLE upstream_accounts ADD COLUMN enable_request_compression INTEGER DEFAULT 0`,
				errMsg: "补齐 enable_request_compression 字段失败",
			},
		}

		for _, migration := range migrations {
			exists, err := s.columnExists(ctx, "upstream_accounts", migration.column)
			if err != nil {
				s.schemaCompatError = err
				return
			}
			if exists {
				continue
			}
			if _, err := s.db.ExecContext(ctx, migration.sql); err != nil {
				s.schemaCompatError = fmt.Errorf("%s: %w", migration.errMsg, err)
				return
			}
		}
		if _, err := s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_upstream_accounts_group_key ON upstream_accounts(group_key)`); err != nil {
			s.schemaCompatError = fmt.Errorf("补齐 group_key 索引失败: %w", err)
			return
		}
		if err := s.migrateModelRewriteRulesToExact(ctx); err != nil {
			s.schemaCompatError = err
			return
		}
	})

	return s.schemaCompatError
}

func (s *SQLiteAccountPoolStore) migrateModelRewriteRulesToExact(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, model_rewrite_rules
		FROM upstream_accounts
		WHERE TRIM(COALESCE(model_rewrite_rules, '')) != ''
	`)
	if err != nil {
		return fmt.Errorf("读取模型改写规则失败: %w", err)
	}
	defer rows.Close()

	updates := make(map[int64]string)
	for rows.Next() {
		var (
			id    int64
			rules string
		)
		if err := rows.Scan(&id, &rules); err != nil {
			return fmt.Errorf("扫描模型改写规则失败: %w", err)
		}
		if normalized, changed := normalizeModelRewriteRulesToExact(rules); changed {
			updates[id] = normalized
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历模型改写规则失败: %w", err)
	}
	for id, rules := range updates {
		if _, err := s.db.ExecContext(ctx, `UPDATE upstream_accounts SET model_rewrite_rules = ? WHERE id = ?`, rules, id); err != nil {
			return fmt.Errorf("迁移模型改写规则为精确匹配失败: %w", err)
		}
	}
	return nil
}

func normalizeModelRewriteRulesToExact(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return raw, false
	}

	var rules []map[string]any
	if err := json.Unmarshal([]byte(raw), &rules); err == nil {
		if !rewriteRuleMatchesToExact(rules) {
			return raw, false
		}
		encoded, err := json.Marshal(rules)
		if err != nil {
			return raw, false
		}
		return string(encoded), true
	}

	var single map[string]any
	if err := json.Unmarshal([]byte(raw), &single); err != nil {
		return raw, false
	}
	if !rewriteRuleMatchesToExact([]map[string]any{single}) {
		return raw, false
	}
	encoded, err := json.Marshal(single)
	if err != nil {
		return raw, false
	}
	return string(encoded), true
}

func rewriteRuleMatchesToExact(rules []map[string]any) bool {
	changed := false
	for _, rule := range rules {
		match, ok := rule["match"].(string)
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(match), "prefix") {
			rule["match"] = "exact"
			changed = true
		}
	}
	return changed
}

func (s *SQLiteAccountPoolStore) columnExists(ctx context.Context, table, column string) (bool, error) {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid       int
			name      string
			dataType  string
			notNull   int
			dfltValue interface{}
			pk        int
		)
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &dfltValue, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (s *SQLiteAccountPoolStore) getQuerier() interface {
	QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
} {
	if s.tx != nil {
		return s.tx
	}
	return s.db
}

// CreateAccount 创建账号
func (s *SQLiteAccountPoolStore) CreateAccount(ctx context.Context, record *UpstreamAccountRecord) (*UpstreamAccountRecord, error) {
	if err := config.ValidateSoftFailureOverrides(record.SoftFailureWindowMinutes, record.SoftFailureThreshold); err != nil {
		return nil, err
	}
	if err := s.ensureSchemaCompatibility(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	normalizeAccountRecord(record)
	query := `
		INSERT INTO upstream_accounts (
			provider_type, account_name, credential_raw, base_url, model_rewrite_rules, soft_failure_window_minutes, soft_failure_threshold, enable_request_compression, strip_codex_internal_metadata,
			cost_multiplier, input_cost_multiplier, output_cost_multiplier,
			cache_creation_cost_multiplier, cache_creation_cost_multiplier_1h, cache_read_cost_multiplier,
			group_key, priority, enabled, state, cooldown_until, fail_count, last_success_at, last_error,
			plan_type, chatgpt_account_id, chatgpt_user_id, organization_id,
			quota_5h_used_percent, quota_5h_reset_at, quota_weekly_used_percent, quota_weekly_reset_at,
			quota_status, quota_refreshed_at, fingerprint
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	res, err := s.getQuerier().ExecContext(ctx, query,
		record.ProviderType, record.AccountName, record.CredentialRaw, record.BaseURL, record.ModelRewriteRules, record.SoftFailureWindowMinutes, record.SoftFailureThreshold, boolToInt(record.EnableRequestCompression), boolToInt(record.StripCodexInternalMetadata),
		record.CostMultiplier, record.InputCostMultiplier, record.OutputCostMultiplier,
		record.CacheCreationCostMultiplier, record.CacheCreationCostMultiplier1h, record.CacheReadCostMultiplier,
		record.GroupKey, record.Priority, boolToInt(record.Enabled), record.State, nullableTime(record.CooldownUntil), record.FailCount,
		nullableTime(record.LastSuccessAt), record.LastError,
		record.PlanType, record.ChatGPTAccountID, record.ChatGPTUserID, record.OrganizationID,
		nullableFloat(record.Quota5HUsedPercent), nullableTime(record.Quota5HResetAt),
		nullableFloat(record.QuotaWeeklyUsedPercent), nullableTime(record.QuotaWeeklyResetAt),
		record.QuotaStatus, nullableTime(record.QuotaRefreshedAt), record.Fingerprint)
	if err != nil {
		return nil, fmt.Errorf("创建账号失败: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("获取账号 ID 失败: %w", err)
	}
	return s.getAccountByID(ctx, id)
}

// UpdateAccount 更新账号
func (s *SQLiteAccountPoolStore) UpdateAccount(ctx context.Context, record *UpstreamAccountRecord) error {
	if err := config.ValidateSoftFailureOverrides(record.SoftFailureWindowMinutes, record.SoftFailureThreshold); err != nil {
		return err
	}
	if err := s.ensureSchemaCompatibility(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if record.ID <= 0 {
		return fmt.Errorf("无效的账号 ID")
	}
	normalizeAccountRecord(record)
	query := `
		UPDATE upstream_accounts
		SET provider_type = ?, account_name = ?, credential_raw = ?, base_url = ?, model_rewrite_rules = ?, soft_failure_window_minutes = ?, soft_failure_threshold = ?, enable_request_compression = ?, strip_codex_internal_metadata = ?,
			cost_multiplier = ?, input_cost_multiplier = ?, output_cost_multiplier = ?,
			cache_creation_cost_multiplier = ?, cache_creation_cost_multiplier_1h = ?, cache_read_cost_multiplier = ?,
			group_key = ?, priority = ?, enabled = ?, state = ?, cooldown_until = ?, fail_count = ?, last_success_at = ?, last_error = ?,
			plan_type = ?, chatgpt_account_id = ?, chatgpt_user_id = ?, organization_id = ?,
			quota_5h_used_percent = ?, quota_5h_reset_at = ?, quota_weekly_used_percent = ?, quota_weekly_reset_at = ?,
			quota_status = ?, quota_refreshed_at = ?, fingerprint = ?
		WHERE id = ?
	`
	res, err := s.getQuerier().ExecContext(ctx, query,
		record.ProviderType, record.AccountName, record.CredentialRaw, record.BaseURL, record.ModelRewriteRules, record.SoftFailureWindowMinutes, record.SoftFailureThreshold, boolToInt(record.EnableRequestCompression), boolToInt(record.StripCodexInternalMetadata),
		record.CostMultiplier, record.InputCostMultiplier, record.OutputCostMultiplier,
		record.CacheCreationCostMultiplier, record.CacheCreationCostMultiplier1h, record.CacheReadCostMultiplier,
		record.GroupKey, record.Priority, boolToInt(record.Enabled), record.State, nullableTime(record.CooldownUntil), record.FailCount,
		nullableTime(record.LastSuccessAt), record.LastError,
		record.PlanType, record.ChatGPTAccountID, record.ChatGPTUserID, record.OrganizationID,
		nullableFloat(record.Quota5HUsedPercent), nullableTime(record.Quota5HResetAt),
		nullableFloat(record.QuotaWeeklyUsedPercent), nullableTime(record.QuotaWeeklyResetAt),
		record.QuotaStatus, nullableTime(record.QuotaRefreshedAt), record.Fingerprint, record.ID)
	if err != nil {
		return fmt.Errorf("更新账号失败: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("获取影响行数失败: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("账号不存在: %d", record.ID)
	}
	return nil
}

// UpdateAccountPriorities 批量更新账号优先级。
func (s *SQLiteAccountPoolStore) UpdateAccountPriorities(ctx context.Context, updates map[int64]int) error {
	if err := s.ensureSchemaCompatibility(ctx); err != nil {
		return err
	}
	if len(updates) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始账号优先级事务失败: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `UPDATE upstream_accounts SET priority = ? WHERE id = ?`)
	if err != nil {
		return fmt.Errorf("准备账号优先级更新语句失败: %w", err)
	}
	defer stmt.Close()

	for id, priority := range updates {
		res, execErr := stmt.ExecContext(ctx, priority, id)
		if execErr != nil {
			return fmt.Errorf("更新账号优先级失败: %w", execErr)
		}
		affected, rowsErr := res.RowsAffected()
		if rowsErr != nil {
			return fmt.Errorf("获取账号优先级更新影响行数失败: %w", rowsErr)
		}
		if affected == 0 {
			return fmt.Errorf("账号不存在: %d", id)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交账号优先级事务失败: %w", err)
	}
	return nil
}

func (s *SQLiteAccountPoolStore) UpdateAccountScheduling(ctx context.Context, updates map[int64]AccountSchedulingUpdate) error {
	if err := s.ensureSchemaCompatibility(ctx); err != nil {
		return err
	}
	if len(updates) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始账号调度事务失败: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `UPDATE upstream_accounts SET group_key = ?, priority = ? WHERE id = ?`)
	if err != nil {
		return fmt.Errorf("准备账号调度更新语句失败: %w", err)
	}
	defer stmt.Close()

	for id, update := range updates {
		res, execErr := stmt.ExecContext(ctx, normalizeAccountGroupKey(update.GroupKey), update.Priority, id)
		if execErr != nil {
			return fmt.Errorf("更新账号调度字段失败: %w", execErr)
		}
		affected, rowsErr := res.RowsAffected()
		if rowsErr != nil {
			return fmt.Errorf("获取账号调度更新影响行数失败: %w", rowsErr)
		}
		if affected == 0 {
			return fmt.Errorf("账号不存在: %d", id)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交账号调度事务失败: %w", err)
	}
	return nil
}

// DeleteAccount 删除账号
func (s *SQLiteAccountPoolStore) DeleteAccount(ctx context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.getQuerier().ExecContext(ctx, `DELETE FROM upstream_accounts WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("删除账号失败: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("获取影响行数失败: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("账号不存在: %d", id)
	}
	return nil
}

// GetAccount 获取单个账号
func (s *SQLiteAccountPoolStore) GetAccount(ctx context.Context, id int64) (*UpstreamAccountRecord, error) {
	if err := s.ensureSchemaCompatibility(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.getAccountByID(ctx, id)
}

func (s *SQLiteAccountPoolStore) getAccountByID(ctx context.Context, id int64) (*UpstreamAccountRecord, error) {
	query := `
		SELECT id, provider_type, account_name, credential_raw, base_url, model_rewrite_rules, soft_failure_window_minutes, soft_failure_threshold, enable_request_compression, strip_codex_internal_metadata,
			cost_multiplier, input_cost_multiplier, output_cost_multiplier,
			cache_creation_cost_multiplier, cache_creation_cost_multiplier_1h, cache_read_cost_multiplier,
			group_key, priority, enabled, state, CAST(cooldown_until AS TEXT), fail_count, CAST(last_success_at AS TEXT), last_error,
			plan_type, chatgpt_account_id, chatgpt_user_id, organization_id,
			quota_5h_used_percent, CAST(quota_5h_reset_at AS TEXT), quota_weekly_used_percent, CAST(quota_weekly_reset_at AS TEXT),
			quota_status, CAST(quota_refreshed_at AS TEXT), fingerprint, CAST(created_at AS TEXT), CAST(updated_at AS TEXT)
		FROM upstream_accounts
		WHERE id = ?
	`
	rec, err := scanAccountRow(s.getQuerier().QueryRowContext(ctx, query, id))
	if err != nil {
		return nil, err
	}
	if normalizeAccountGroupKey(rec.GroupKey) != rec.GroupKey {
		rec.GroupKey = normalizeAccountGroupKey(rec.GroupKey)
	}
	if rec.GroupKey == "" {
		records, listErr := s.listAccountsRaw(ctx, true)
		if listErr != nil {
			return nil, listErr
		}
		normalized := normalizeAccountGroupRecords(records)
		for _, item := range normalized {
			if item != nil && item.ID == id {
				return item, nil
			}
		}
	}
	return normalizeSingleAccountRecord(rec), nil
}

// ListAccounts 列出账号
func (s *SQLiteAccountPoolStore) ListAccounts(ctx context.Context, includeDisabled bool) ([]*UpstreamAccountRecord, error) {
	if err := s.ensureSchemaCompatibility(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	records, err := s.listAccountsRaw(ctx, includeDisabled)
	if err != nil {
		return nil, err
	}
	return normalizeAccountGroupRecords(records), nil
}

func (s *SQLiteAccountPoolStore) listAccountsRaw(ctx context.Context, includeDisabled bool) ([]*UpstreamAccountRecord, error) {

	query := `
		SELECT id, provider_type, account_name, credential_raw, base_url, model_rewrite_rules, soft_failure_window_minutes, soft_failure_threshold, enable_request_compression, strip_codex_internal_metadata,
			cost_multiplier, input_cost_multiplier, output_cost_multiplier,
			cache_creation_cost_multiplier, cache_creation_cost_multiplier_1h, cache_read_cost_multiplier,
			group_key, priority, enabled, state, CAST(cooldown_until AS TEXT), fail_count, CAST(last_success_at AS TEXT), last_error,
			plan_type, chatgpt_account_id, chatgpt_user_id, organization_id,
			quota_5h_used_percent, CAST(quota_5h_reset_at AS TEXT), quota_weekly_used_percent, CAST(quota_weekly_reset_at AS TEXT),
			quota_status, CAST(quota_refreshed_at AS TEXT), fingerprint, CAST(created_at AS TEXT), CAST(updated_at AS TEXT)
		FROM upstream_accounts
	`
	if !includeDisabled {
		query += ` WHERE enabled = 1`
	}
	query += ` ORDER BY priority ASC, id ASC`

	rows, err := s.getQuerier().QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("查询账号失败: %w", err)
	}
	defer rows.Close()

	var out []*UpstreamAccountRecord
	for rows.Next() {
		rec, scanErr := scanAccountRow(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历账号失败: %w", err)
	}
	return out, nil
}

// ListSchedulableAccounts 查询可调度账号
func (s *SQLiteAccountPoolStore) ListSchedulableAccounts(ctx context.Context, now time.Time) ([]*UpstreamAccountRecord, error) {
	if err := s.ensureSchemaCompatibility(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
		SELECT id, provider_type, account_name, credential_raw, base_url, model_rewrite_rules, soft_failure_window_minutes, soft_failure_threshold, enable_request_compression, strip_codex_internal_metadata,
			cost_multiplier, input_cost_multiplier, output_cost_multiplier,
			cache_creation_cost_multiplier, cache_creation_cost_multiplier_1h, cache_read_cost_multiplier,
			group_key, priority, enabled, state, CAST(cooldown_until AS TEXT), fail_count, CAST(last_success_at AS TEXT), last_error,
			plan_type, chatgpt_account_id, chatgpt_user_id, organization_id,
			quota_5h_used_percent, CAST(quota_5h_reset_at AS TEXT), quota_weekly_used_percent, CAST(quota_weekly_reset_at AS TEXT),
			quota_status, CAST(quota_refreshed_at AS TEXT), fingerprint, CAST(created_at AS TEXT), CAST(updated_at AS TEXT)
		FROM upstream_accounts
		WHERE enabled = 1
			AND state != 'disabled_auth'
			AND (cooldown_until IS NULL OR cooldown_until <= ?)
		ORDER BY priority ASC, id ASC
	`
	rows, err := s.getQuerier().QueryContext(ctx, query, formatDBTime(now))
	if err != nil {
		return nil, fmt.Errorf("查询可调度账号失败: %w", err)
	}
	defer rows.Close()

	var out []*UpstreamAccountRecord
	for rows.Next() {
		rec, scanErr := scanAccountRow(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历可调度账号失败: %w", err)
	}
	return normalizeAccountGroupRecords(out), nil
}

// FindAccountByFingerprint 按指纹查询账号
func (s *SQLiteAccountPoolStore) FindAccountByFingerprint(ctx context.Context, fingerprint string) (*UpstreamAccountRecord, error) {
	if err := s.ensureSchemaCompatibility(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
		SELECT id, provider_type, account_name, credential_raw, base_url, model_rewrite_rules, soft_failure_window_minutes, soft_failure_threshold, enable_request_compression, strip_codex_internal_metadata,
			cost_multiplier, input_cost_multiplier, output_cost_multiplier,
			cache_creation_cost_multiplier, cache_creation_cost_multiplier_1h, cache_read_cost_multiplier,
			group_key, priority, enabled, state, CAST(cooldown_until AS TEXT), fail_count, CAST(last_success_at AS TEXT), last_error,
			plan_type, chatgpt_account_id, chatgpt_user_id, organization_id,
			quota_5h_used_percent, CAST(quota_5h_reset_at AS TEXT), quota_weekly_used_percent, CAST(quota_weekly_reset_at AS TEXT),
			quota_status, CAST(quota_refreshed_at AS TEXT), fingerprint, CAST(created_at AS TEXT), CAST(updated_at AS TEXT)
		FROM upstream_accounts
		WHERE fingerprint = ?
	`
	rec, err := scanAccountRow(s.getQuerier().QueryRowContext(ctx, query, fingerprint))
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return normalizeSingleAccountRecord(rec), nil
}

// ToggleAccount 启停账号
func (s *SQLiteAccountPoolStore) ToggleAccount(ctx context.Context, id int64, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var (
		res sql.Result
		err error
	)
	if enabled {
		res, err = s.getQuerier().ExecContext(ctx,
			`UPDATE upstream_accounts SET enabled = 1, state = 'active', fail_count = 0, cooldown_until = NULL WHERE id = ?`,
			id)
	} else {
		res, err = s.getQuerier().ExecContext(ctx,
			`UPDATE upstream_accounts SET enabled = 0 WHERE id = ?`,
			id)
	}
	if err != nil {
		return fmt.Errorf("切换账号状态失败: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("获取影响行数失败: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("账号不存在: %d", id)
	}
	return nil
}

// MarkAccountSuccess 标记账号成功
func (s *SQLiteAccountPoolStore) MarkAccountSuccess(ctx context.Context, id int64, successAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.getQuerier().ExecContext(ctx,
		`UPDATE upstream_accounts
		 SET fail_count = 0, state = 'active', cooldown_until = NULL, last_success_at = ?, last_error = '', updated_at = ?
		 WHERE id = ?`,
		formatDBTime(successAt), currentDBTime(), id)
	if err != nil {
		return fmt.Errorf("更新账号成功状态失败: %w", err)
	}
	return nil
}

func (s *SQLiteAccountPoolStore) MarkAccountSuccessIfNoNewerFailure(ctx context.Context, id int64, successAt, attemptStartedAt time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.getQuerier().ExecContext(ctx,
		`UPDATE upstream_accounts
		 SET fail_count = 0, state = 'active', cooldown_until = NULL, last_success_at = ?, last_error = '', updated_at = ?
		 WHERE id = ?
		   AND NOT (updated_at > ? AND state IN ('cooldown', 'disabled_auth'))`,
		formatDBTime(successAt), currentDBTime(), id, formatDBTime(attemptStartedAt))
	if err != nil {
		return false, fmt.Errorf("条件更新账号成功状态失败: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("获取影响行数失败: %w", err)
	}
	return affected > 0, nil
}

// MarkAccountAuthFailed 标记账号鉴权失败
func (s *SQLiteAccountPoolStore) MarkAccountAuthFailed(ctx context.Context, id int64, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.getQuerier().ExecContext(ctx,
		`UPDATE upstream_accounts
		 SET enabled = 0, state = 'disabled_auth', cooldown_until = NULL, last_error = ?, updated_at = ?
		 WHERE id = ?`,
		reason, currentDBTime(), id)
	if err != nil {
		return fmt.Errorf("更新账号鉴权失败状态失败: %w", err)
	}
	return nil
}

func (s *SQLiteAccountPoolStore) MarkAccountAuthFailedWithProfile(ctx context.Context, record *UpstreamAccountRecord, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if record == nil || record.ID <= 0 {
		return fmt.Errorf("无效的账号记录")
	}
	record.PlanType = strings.TrimSpace(record.PlanType)
	record.ChatGPTAccountID = strings.TrimSpace(record.ChatGPTAccountID)
	record.ChatGPTUserID = strings.TrimSpace(record.ChatGPTUserID)
	record.OrganizationID = strings.TrimSpace(record.OrganizationID)
	record.QuotaStatus = strings.TrimSpace(record.QuotaStatus)
	record.Fingerprint = GenerateAccountFingerprint(record.ProviderType, record.CredentialRaw, record.BaseURL)

	res, err := s.getQuerier().ExecContext(ctx, `
		UPDATE upstream_accounts
		SET enabled = 0, state = 'disabled_auth', cooldown_until = NULL, last_error = ?, updated_at = ?,
			credential_raw = ?, plan_type = ?, chatgpt_account_id = ?, chatgpt_user_id = ?, organization_id = ?,
			quota_5h_used_percent = ?, quota_5h_reset_at = ?, quota_weekly_used_percent = ?, quota_weekly_reset_at = ?,
			quota_status = ?, quota_refreshed_at = ?, fingerprint = ?
		WHERE id = ?
	`,
		reason, currentDBTime(),
		record.CredentialRaw, record.PlanType, record.ChatGPTAccountID, record.ChatGPTUserID, record.OrganizationID,
		nullableFloat(record.Quota5HUsedPercent), nullableTime(record.Quota5HResetAt),
		nullableFloat(record.QuotaWeeklyUsedPercent), nullableTime(record.QuotaWeeklyResetAt),
		record.QuotaStatus, nullableTime(record.QuotaRefreshedAt), record.Fingerprint, record.ID,
	)
	if err != nil {
		return fmt.Errorf("更新账号鉴权失败与画像状态失败: %w", err)
	}
	affected, rowsErr := res.RowsAffected()
	if rowsErr != nil {
		return fmt.Errorf("获取账号鉴权失败画像影响行数失败: %w", rowsErr)
	}
	if affected == 0 {
		return fmt.Errorf("账号不存在: %d", record.ID)
	}
	return nil
}

// MarkAccountTransientFailure 标记账号瞬时失败（进入冷却）
func (s *SQLiteAccountPoolStore) MarkAccountTransientFailure(ctx context.Context, id int64, reason string, cooldownUntil time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.getQuerier().ExecContext(ctx,
		`UPDATE upstream_accounts
		 SET fail_count = fail_count + 1, state = 'cooldown', cooldown_until = ?, last_error = ?, updated_at = ?
		 WHERE id = ?`,
		formatDBTime(cooldownUntil), reason, currentDBTime(), id)
	if err != nil {
		return fmt.Errorf("更新账号瞬时失败状态失败: %w", err)
	}
	return nil
}

// ClearAccountCooldown 清除账号临时故障冷却，但保留额度与鉴权资格状态。
func (s *SQLiteAccountPoolStore) ClearAccountCooldown(ctx context.Context, id int64, clearedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.getQuerier().ExecContext(ctx,
		`UPDATE upstream_accounts
		 SET state = 'active', fail_count = 0, cooldown_until = NULL, last_error = '', updated_at = ?
		 WHERE id = ? AND state = 'cooldown'`,
		formatDBTime(clearedAt), id)
	if err != nil {
		return fmt.Errorf("清除账号冷却失败: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("获取清除账号冷却影响行数失败: %w", err)
	}
	if affected == 0 {
		var exists int
		if err := s.getQuerier().QueryRowContext(ctx, `SELECT 1 FROM upstream_accounts WHERE id = ?`, id).Scan(&exists); err != nil {
			if err == sql.ErrNoRows {
				return fmt.Errorf("账号不存在: %d", id)
			}
			return fmt.Errorf("确认账号状态失败: %w", err)
		}
	}
	return nil
}

// UpdateAccountProfile 更新账号画像与 quota 字段。
func (s *SQLiteAccountPoolStore) UpdateAccountProfile(ctx context.Context, record *UpstreamAccountRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if record == nil || record.ID <= 0 {
		return fmt.Errorf("无效的账号记录")
	}
	record.PlanType = strings.TrimSpace(record.PlanType)
	record.ChatGPTAccountID = strings.TrimSpace(record.ChatGPTAccountID)
	record.ChatGPTUserID = strings.TrimSpace(record.ChatGPTUserID)
	record.OrganizationID = strings.TrimSpace(record.OrganizationID)
	record.QuotaStatus = strings.TrimSpace(record.QuotaStatus)
	record.Fingerprint = GenerateAccountFingerprint(record.ProviderType, record.CredentialRaw, record.BaseURL)

	res, err := s.getQuerier().ExecContext(ctx, `
		UPDATE upstream_accounts
		SET credential_raw = ?, plan_type = ?, chatgpt_account_id = ?, chatgpt_user_id = ?, organization_id = ?,
			quota_5h_used_percent = ?, quota_5h_reset_at = ?, quota_weekly_used_percent = ?, quota_weekly_reset_at = ?,
			quota_status = ?, quota_refreshed_at = ?, fingerprint = ?
		WHERE id = ?
	`,
		record.CredentialRaw, record.PlanType, record.ChatGPTAccountID, record.ChatGPTUserID, record.OrganizationID,
		nullableFloat(record.Quota5HUsedPercent), nullableTime(record.Quota5HResetAt),
		nullableFloat(record.QuotaWeeklyUsedPercent), nullableTime(record.QuotaWeeklyResetAt),
		record.QuotaStatus, nullableTime(record.QuotaRefreshedAt), record.Fingerprint, record.ID,
	)
	if err != nil {
		return fmt.Errorf("更新账号画像失败: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("获取影响行数失败: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("账号不存在: %d", record.ID)
	}
	return nil
}

// GenerateAccountFingerprint 生成账号指纹（用于去重）
func GenerateAccountFingerprint(providerType, credentialRaw, baseURL string) string {
	text := strings.ToLower(strings.TrimSpace(providerType)) + "\n" +
		strings.TrimSpace(credentialRaw) + "\n" +
		normalizeBaseURL(baseURL)
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// SupportsAccountRequestCompression 判断账号类型是否允许向上游发送 zstd 请求体。
func SupportsAccountRequestCompression(providerType string) bool {
	normalized := accountauth.NormalizeProviderType(providerType)
	return normalized == accountauth.ProviderAPIKey || normalized == accountauth.ProviderChatGPTRefreshToken
}

func normalizeAccountRecord(record *UpstreamAccountRecord) {
	if record.ProviderType == "" {
		record.ProviderType = "api_key"
	}
	if strings.TrimSpace(record.AccountName) == "" {
		record.AccountName = "unnamed-account"
	}
	if strings.TrimSpace(record.BaseURL) == "" {
		record.BaseURL = defaultAccountBaseURL
	}
	record.BaseURL = normalizeBaseURL(record.BaseURL)
	record.ModelRewriteRules = strings.TrimSpace(record.ModelRewriteRules)
	if accountauth.NormalizeProviderType(record.ProviderType) != accountauth.ProviderAPIKey {
		record.ModelRewriteRules = ""
	}
	if !SupportsAccountRequestCompression(record.ProviderType) {
		record.EnableRequestCompression = false
	}
	applyAccountCostMultiplierPolicy(record)
	if record.Priority == 0 {
		record.Priority = defaultAccountPrio
	}
	record.GroupKey = normalizeAccountGroupKey(record.GroupKey)
	if record.State == "" {
		record.State = defaultAccountState
	}
	if record.Fingerprint == "" {
		record.Fingerprint = GenerateAccountFingerprint(record.ProviderType, record.CredentialRaw, record.BaseURL)
	}
}

func normalizeMultiplierValue(value float64) float64 {
	if value <= 0 {
		return 1.0
	}
	return value
}

func applyAccountCostMultiplierPolicy(record *UpstreamAccountRecord) {
	if record == nil {
		return
	}

	if strings.TrimSpace(strings.ToLower(record.ProviderType)) != "api_key" {
		record.CostMultiplier = 1.0
		record.InputCostMultiplier = 1.0
		record.OutputCostMultiplier = 1.0
		record.CacheCreationCostMultiplier = 1.0
		record.CacheCreationCostMultiplier1h = 1.0
		record.CacheReadCostMultiplier = 1.0
		return
	}

	record.CostMultiplier = normalizeMultiplierValue(record.CostMultiplier)
	record.InputCostMultiplier = normalizeMultiplierValue(record.InputCostMultiplier)
	record.OutputCostMultiplier = normalizeMultiplierValue(record.OutputCostMultiplier)
	record.CacheCreationCostMultiplier = normalizeMultiplierValue(record.CacheCreationCostMultiplier)
	record.CacheCreationCostMultiplier1h = normalizeMultiplierValue(record.CacheCreationCostMultiplier1h)
	record.CacheReadCostMultiplier = normalizeMultiplierValue(record.CacheReadCostMultiplier)
}

func normalizeBaseURL(baseURL string) string {
	trimmed := strings.TrimSpace(baseURL)
	trimmed = strings.TrimSuffix(trimmed, "/")
	if trimmed == "" {
		return defaultAccountBaseURL
	}
	return trimmed
}

func nullableTime(v *time.Time) any {
	if v == nil || v.IsZero() {
		return nil
	}
	return formatDBTime(*v)
}

func nullableFloat(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func formatDBTime(t time.Time) string {
	return timezonepolicy.FormatStorage(t)
}

// FormatAccountDisplayTime 保留旧调用名，API 契约统一输出 UTC；展示转换由前端 Policy 完成。
func FormatAccountDisplayTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return timezonepolicy.FormatStorage(t)
}

func currentDBTime() string {
	return formatDBTime(time.Now())
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanAccountRow(scanner rowScanner) (*UpstreamAccountRecord, error) {
	var (
		rec                        UpstreamAccountRecord
		enabled                    int
		cooldownUntilStr           sql.NullString
		enabledRequestCompression  int
		stripCodexInternalMetadata int
		lastSuccessAtStr           sql.NullString
		quota5HUsedPercent         sql.NullFloat64
		quota5HResetAtStr          sql.NullString
		quotaWeeklyUsedPercent     sql.NullFloat64
		quotaWeeklyResetAtStr      sql.NullString
		quotaRefreshedAtStr        sql.NullString
		costMultiplier             sql.NullFloat64
		inputCostMultiplier        sql.NullFloat64
		outputCostMultiplier       sql.NullFloat64
		cacheCreationMultiplier    sql.NullFloat64
		cacheCreationMultiplier1h  sql.NullFloat64
		cacheReadMultiplier        sql.NullFloat64
		createdAtStr               string
		updatedAtStr               string
	)
	if err := scanner.Scan(
		&rec.ID, &rec.ProviderType, &rec.AccountName, &rec.CredentialRaw, &rec.BaseURL,
		&rec.ModelRewriteRules, &rec.SoftFailureWindowMinutes, &rec.SoftFailureThreshold, &enabledRequestCompression, &stripCodexInternalMetadata,
		&costMultiplier, &inputCostMultiplier, &outputCostMultiplier,
		&cacheCreationMultiplier, &cacheCreationMultiplier1h, &cacheReadMultiplier,
		&rec.GroupKey, &rec.Priority, &enabled, &rec.State, &cooldownUntilStr, &rec.FailCount, &lastSuccessAtStr, &rec.LastError,
		&rec.PlanType, &rec.ChatGPTAccountID, &rec.ChatGPTUserID, &rec.OrganizationID,
		&quota5HUsedPercent, &quota5HResetAtStr, &quotaWeeklyUsedPercent, &quotaWeeklyResetAtStr,
		&rec.QuotaStatus, &quotaRefreshedAtStr, &rec.Fingerprint, &createdAtStr, &updatedAtStr,
	); err != nil {
		return nil, err
	}
	if err := config.ValidateSoftFailureOverrides(rec.SoftFailureWindowMinutes, rec.SoftFailureThreshold); err != nil {
		return nil, fmt.Errorf("账号 %d 失败策略无效: %w", rec.ID, err)
	}
	rec.Enabled = enabled == 1
	rec.EnableRequestCompression = enabledRequestCompression == 1
	rec.StripCodexInternalMetadata = stripCodexInternalMetadata == 1
	var err error
	if rec.CooldownUntil, err = parseNullableTime(cooldownUntilStr); err != nil {
		return nil, fmt.Errorf("parse account cooldown_until: %w", err)
	}
	rec.CostMultiplier = parseMultiplierFloat(costMultiplier)
	rec.InputCostMultiplier = parseMultiplierFloat(inputCostMultiplier)
	rec.OutputCostMultiplier = parseMultiplierFloat(outputCostMultiplier)
	rec.CacheCreationCostMultiplier = parseMultiplierFloat(cacheCreationMultiplier)
	rec.CacheCreationCostMultiplier1h = parseMultiplierFloat(cacheCreationMultiplier1h)
	rec.CacheReadCostMultiplier = parseMultiplierFloat(cacheReadMultiplier)
	if rec.LastSuccessAt, err = parseNullableTime(lastSuccessAtStr); err != nil {
		return nil, fmt.Errorf("parse account last_success_at: %w", err)
	}
	rec.Quota5HUsedPercent = parseNullableFloat(quota5HUsedPercent)
	if rec.Quota5HResetAt, err = parseNullableTime(quota5HResetAtStr); err != nil {
		return nil, fmt.Errorf("parse account quota_5h_reset_at: %w", err)
	}
	rec.QuotaWeeklyUsedPercent = parseNullableFloat(quotaWeeklyUsedPercent)
	if rec.QuotaWeeklyResetAt, err = parseNullableTime(quotaWeeklyResetAtStr); err != nil {
		return nil, fmt.Errorf("parse account quota_weekly_reset_at: %w", err)
	}
	if rec.QuotaRefreshedAt, err = parseNullableTime(quotaRefreshedAtStr); err != nil {
		return nil, fmt.Errorf("parse account quota_refreshed_at: %w", err)
	}
	if rec.CreatedAt, err = parseDBTime(createdAtStr); err != nil {
		return nil, fmt.Errorf("parse account created_at: %w", err)
	}
	if rec.UpdatedAt, err = parseDBTime(updatedAtStr); err != nil {
		return nil, fmt.Errorf("parse account updated_at: %w", err)
	}
	return &rec, nil
}

func normalizeAccountGroupKey(value string) string {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case accountGroupPrimary:
		return accountGroupPrimary
	case accountGroupBackup:
		return accountGroupBackup
	case accountGroupCold:
		return accountGroupCold
	default:
		return ""
	}
}

func inferLegacyGroupKeyFromPriority(priority int) string {
	if priority <= 10 {
		return accountGroupPrimary
	}
	if priority <= 20 {
		return accountGroupBackup
	}
	return accountGroupCold
}

func accountGroupRank(groupKey string) int {
	switch normalizeAccountGroupKey(groupKey) {
	case accountGroupPrimary:
		return 0
	case accountGroupBackup:
		return 1
	case accountGroupCold:
		return 2
	default:
		return 9
	}
}

func normalizeSingleAccountRecord(record *UpstreamAccountRecord) *UpstreamAccountRecord {
	if record == nil {
		return nil
	}
	cloned := cloneAccountRecord(record)
	cloned.GroupKey = normalizeAccountGroupKey(cloned.GroupKey)
	if cloned.GroupKey == "" {
		cloned.GroupKey = inferLegacyGroupKeyFromPriority(cloned.Priority)
	}
	return cloned
}

func normalizeAccountGroupRecords(records []*UpstreamAccountRecord) []*UpstreamAccountRecord {
	if len(records) == 0 {
		return nil
	}

	cloned := make([]*UpstreamAccountRecord, 0, len(records))
	hasExplicitGroup := false
	for _, record := range records {
		if record == nil {
			continue
		}
		item := cloneAccountRecord(record)
		item.GroupKey = normalizeAccountGroupKey(item.GroupKey)
		if item.GroupKey != "" {
			hasExplicitGroup = true
		}
		cloned = append(cloned, item)
	}

	sort.SliceStable(cloned, func(i, j int) bool {
		left := cloned[i]
		right := cloned[j]
		if left.Priority != right.Priority {
			return left.Priority < right.Priority
		}
		return left.ID < right.ID
	})

	uniqueTiers := make([]int, 0)
	seenTier := make(map[int]struct{})
	for _, record := range cloned {
		if record.GroupKey != "" {
			continue
		}
		if _, ok := seenTier[record.Priority]; ok {
			continue
		}
		seenTier[record.Priority] = struct{}{}
		uniqueTiers = append(uniqueTiers, record.Priority)
	}
	sort.Ints(uniqueTiers)

	legacyGroupByTier := make(map[int]string, len(uniqueTiers))
	for index, tier := range uniqueTiers {
		groupKey := accountGroupCold
		if index == 0 {
			groupKey = accountGroupPrimary
		} else if index == 1 {
			groupKey = accountGroupBackup
		}
		legacyGroupByTier[tier] = groupKey
	}

	for _, record := range cloned {
		if record.GroupKey == "" {
			if inferred := legacyGroupByTier[record.Priority]; inferred != "" {
				record.GroupKey = inferred
			} else {
				record.GroupKey = inferLegacyGroupKeyFromPriority(record.Priority)
			}
		}
	}

	sort.SliceStable(cloned, func(i, j int) bool {
		left := cloned[i]
		right := cloned[j]
		if accountGroupRank(left.GroupKey) != accountGroupRank(right.GroupKey) {
			return accountGroupRank(left.GroupKey) < accountGroupRank(right.GroupKey)
		}
		if left.Priority != right.Priority {
			return left.Priority < right.Priority
		}
		return left.ID < right.ID
	})

	if hasExplicitGroup {
		groupCounters := map[string]int{}
		for _, record := range cloned {
			groupCounters[record.GroupKey]++
			record.Priority = groupCounters[record.GroupKey] * 10
		}
	}

	return cloned
}

func cloneAccountRecord(record *UpstreamAccountRecord) *UpstreamAccountRecord {
	if record == nil {
		return nil
	}
	cloned := *record
	cloned.CooldownUntil = cloneTimePtr(record.CooldownUntil)
	cloned.LastSuccessAt = cloneTimePtr(record.LastSuccessAt)
	cloned.Quota5HUsedPercent = cloneFloatPtr(record.Quota5HUsedPercent)
	cloned.Quota5HResetAt = cloneTimePtr(record.Quota5HResetAt)
	cloned.QuotaWeeklyUsedPercent = cloneFloatPtr(record.QuotaWeeklyUsedPercent)
	cloned.QuotaWeeklyResetAt = cloneTimePtr(record.QuotaWeeklyResetAt)
	cloned.QuotaRefreshedAt = cloneTimePtr(record.QuotaRefreshedAt)
	return &cloned
}

func cloneTimePtr(t *time.Time) *time.Time {
	if t == nil || t.IsZero() {
		return nil
	}
	value := *t
	return &value
}

func cloneFloatPtr(v *float64) *float64 {
	if v == nil {
		return nil
	}
	value := *v
	return &value
}

func parseMultiplierFloat(v sql.NullFloat64) float64 {
	if !v.Valid || v.Float64 <= 0 {
		return 1.0
	}
	return v.Float64
}

func parseNullableTime(v sql.NullString) (*time.Time, error) {
	if !v.Valid || strings.TrimSpace(v.String) == "" {
		return nil, nil
	}
	t, err := parseDBTime(v.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func parseNullableFloat(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	value := v.Float64
	return &value
}

func parseDBTime(text string) (time.Time, error) {
	return timezonepolicy.ParseStorage(text)
}
