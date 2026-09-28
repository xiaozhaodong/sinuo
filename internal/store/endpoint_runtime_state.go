// Package store - v8 端点运行态存储（收敛方案 §7.2）
// 只保存达到阈值后的持久化 cooldown（global auth/quota 与 messages），
// 软失败事件列表不落库；count_tokens scope 预留，第一版不写入。
package store

import (
	timezonepolicy "cc-forwarder/internal/timezone"
	"context"
	"database/sql"
	"fmt"
	"time"
)

// 端点运行态 scope 常量
const (
	EndpointRuntimeScopeGlobal      = "global"
	EndpointRuntimeScopeMessages    = "messages"
	EndpointRuntimeScopeCountTokens = "count_tokens"
)

// EndpointRuntimeStateRecord 端点运行态记录
type EndpointRuntimeStateRecord struct {
	EndpointID     int64      `json:"endpoint_id"`
	Scope          string     `json:"scope"`
	State          string     `json:"state"`
	CooldownUntil  *time.Time `json:"cooldown_until,omitempty"`
	CooldownReason string     `json:"cooldown_reason"`
	Revision       int64      `json:"revision"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// EndpointRuntimeStateStore 端点运行态存储接口
type EndpointRuntimeStateStore interface {
	// Upsert 写入运行态；仅当 revision 更高时覆盖（§14.4：旧任务丢弃）
	Upsert(ctx context.Context, record *EndpointRuntimeStateRecord) error
	// ClearCooldownTombstones 以更高 revision 的 tombstone 覆盖 global + messages 两行,
	// 单事务原子提交;手动解除冷却的持久化出口,失败必须整体回滚
	ClearCooldownTombstones(ctx context.Context, endpointID int64, revision int64) error
	// ListActiveCooldowns 列出仍在生效的 cooldown（cooldown_until > now）
	ListActiveCooldowns(ctx context.Context, now time.Time) ([]*EndpointRuntimeStateRecord, error)
	// Clear 清除指定端点 scope 的运行态
	Clear(ctx context.Context, endpointID int64, scope string) error
	// PruneOrphans 删除所属端点已不存在的运行态，返回删除行数（启动时清理历史残留）。
	// 连接未启用外键，schema 的 ON DELETE CASCADE 不会触发；删除端点时由 SQLiteEndpointStore.Delete
	// 在同一事务内清理，本方法只兜底旧版本留下的残留
	PruneOrphans(ctx context.Context) (int64, error)
	// MaxRevision 返回全表最大 revision（空表为 0），用于启动播种 revision 发号器
	MaxRevision(ctx context.Context) (int64, error)
}

// SQLiteEndpointRuntimeStateStore SQLite 实现
type SQLiteEndpointRuntimeStateStore struct {
	db *sql.DB
}

// NewSQLiteEndpointRuntimeStateStore 创建运行态存储
func NewSQLiteEndpointRuntimeStateStore(db *sql.DB) *SQLiteEndpointRuntimeStateStore {
	return &SQLiteEndpointRuntimeStateStore{db: db}
}

// Upsert 写入运行态；已有更高或相同 revision 时静默丢弃旧任务
func (s *SQLiteEndpointRuntimeStateStore) Upsert(ctx context.Context, record *EndpointRuntimeStateRecord) error {
	if record == nil {
		return fmt.Errorf("endpoint runtime state record is nil")
	}
	var cooldownUntil interface{}
	if record.CooldownUntil != nil {
		cooldownUntil = timezonepolicy.FormatStorage(*record.CooldownUntil)
	}
	nowUTC := timezonepolicy.FormatStorage(time.Now())
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO endpoint_runtime_states (endpoint_id, scope, state, cooldown_until, cooldown_reason, revision, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(endpoint_id, scope) DO UPDATE SET
			state = excluded.state,
			cooldown_until = excluded.cooldown_until,
			cooldown_reason = excluded.cooldown_reason,
			revision = excluded.revision,
			updated_at = excluded.updated_at
		WHERE excluded.revision > endpoint_runtime_states.revision
	`, record.EndpointID, record.Scope, record.State, cooldownUntil, record.CooldownReason, record.Revision, nowUTC)
	if err != nil {
		return fmt.Errorf("写入端点运行态失败: %w", err)
	}
	return nil
}

// tombstoneUpsertSQL 与 Upsert 同形态的覆盖写;tombstone 即 state='active'、
// cooldown_until=NULL、cooldown_reason=” 的记录。沿用 revision 单调保护:
// 晚执行的旧 persist 任务(更低 revision)不会复活冷却。
const tombstoneUpsertSQL = `
	INSERT INTO endpoint_runtime_states (endpoint_id, scope, state, cooldown_until, cooldown_reason, revision, updated_at)
	VALUES (?, ?, 'active', NULL, '', ?, ?)
	ON CONFLICT(endpoint_id, scope) DO UPDATE SET
		state = excluded.state,
		cooldown_until = excluded.cooldown_until,
		cooldown_reason = excluded.cooldown_reason,
		revision = excluded.revision,
		updated_at = excluded.updated_at
	WHERE excluded.revision > endpoint_runtime_states.revision
`

// ClearCooldownTombstones 以更高 revision 的 tombstone(state='active'、until=NULL、
// reason=”)覆盖端点运行态,单事务原子提交,按 global → messages 固定顺序写入
// (测试的失败注入依赖此顺序)。每行沿用 revision 单调保护(WHERE excluded.revision >
// endpoint_runtime_states.revision),晚执行的旧 persist 任务不会复活冷却。
// 不写 count_tokens scope(进程内态不落库,D17)。
func (s *SQLiteEndpointRuntimeStateStore) ClearCooldownTombstones(ctx context.Context, endpointID int64, revision int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开启冷却 tombstone 事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	nowUTC := timezonepolicy.FormatStorage(time.Now())
	for _, scope := range []string{EndpointRuntimeScopeGlobal, EndpointRuntimeScopeMessages} {
		if _, err := tx.ExecContext(ctx, tombstoneUpsertSQL, endpointID, scope, revision, nowUTC); err != nil {
			return fmt.Errorf("写入冷却 tombstone(scope=%s)失败: %w", scope, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交冷却 tombstone 事务失败: %w", err)
	}
	return nil
}

// ListActiveCooldowns 列出仍在生效的 cooldown
func (s *SQLiteEndpointRuntimeStateStore) ListActiveCooldowns(ctx context.Context, now time.Time) ([]*EndpointRuntimeStateRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT endpoint_id, scope, state, CAST(cooldown_until AS TEXT), cooldown_reason, revision, CAST(updated_at AS TEXT)
		FROM endpoint_runtime_states
		WHERE cooldown_until IS NOT NULL AND cooldown_until > ?
	`, timezonepolicy.FormatStorage(now))
	if err != nil {
		return nil, fmt.Errorf("查询端点运行态失败: %w", err)
	}
	defer rows.Close()

	var records []*EndpointRuntimeStateRecord
	for rows.Next() {
		var record EndpointRuntimeStateRecord
		var cooldownUntil timezonepolicy.NullDBTime
		var updatedAt timezonepolicy.DBTime
		if err := rows.Scan(&record.EndpointID, &record.Scope, &record.State,
			&cooldownUntil, &record.CooldownReason, &record.Revision, &updatedAt); err != nil {
			return nil, fmt.Errorf("扫描端点运行态失败: %w", err)
		}
		if cooldownUntil.Valid {
			value := cooldownUntil.Time.UTC()
			record.CooldownUntil = &value
		}
		record.UpdatedAt = updatedAt.Time.UTC()
		records = append(records, &record)
	}
	return records, rows.Err()
}

// Clear 清除指定端点 scope 的运行态
func (s *SQLiteEndpointRuntimeStateStore) Clear(ctx context.Context, endpointID int64, scope string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM endpoint_runtime_states WHERE endpoint_id = ? AND scope = ?`, endpointID, scope)
	if err != nil {
		return fmt.Errorf("清除端点运行态失败: %w", err)
	}
	return nil
}

// PruneOrphans 删除所属端点已不存在的运行态。
// 旧版本删除端点时依赖从未生效的外键级联，留下了这类残留行
func (s *SQLiteEndpointRuntimeStateStore) PruneOrphans(ctx context.Context) (int64, error) {
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM endpoint_runtime_states WHERE endpoint_id NOT IN (SELECT id FROM endpoints)`)
	if err != nil {
		return 0, fmt.Errorf("清理残留端点运行态失败: %w", err)
	}
	pruned, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("获取残留端点运行态清理行数失败: %w", err)
	}
	return pruned, nil
}

// MaxRevision 返回全表最大 revision（空表为 0）
func (s *SQLiteEndpointRuntimeStateStore) MaxRevision(ctx context.Context) (int64, error) {
	var max sql.NullInt64
	if err := s.db.QueryRowContext(ctx,
		`SELECT MAX(revision) FROM endpoint_runtime_states`).Scan(&max); err != nil {
		return 0, fmt.Errorf("查询端点运行态最大 revision 失败: %w", err)
	}
	return max.Int64, nil
}
