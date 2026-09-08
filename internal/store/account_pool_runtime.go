package store

import (
	"context"
	"fmt"
	"time"
)

// AccountRuntimeState 是单个账号已发布的完整运行态，不包含凭据、画像或编排配置。
type AccountRuntimeState struct {
	Enabled       bool
	State         string
	CooldownUntil *time.Time
	FailCount     int
	LastSuccessAt *time.Time
	LastError     string
	UpdatedAt     time.Time
}

// SaveAccountRuntimeState 原子保存快照；重复写入同一快照不会重复累计失败次数。
func (s *SQLiteAccountPoolStore) SaveAccountRuntimeState(ctx context.Context, id int64, state AccountRuntimeState) error {
	if id <= 0 || state.FailCount < 0 || state.UpdatedAt.IsZero() {
		return fmt.Errorf("账号运行态快照无效: id=%d", id)
	}
	if state.State != "active" && state.State != "cooldown" && state.State != "disabled_auth" {
		return fmt.Errorf("账号运行态无效: %q", state.State)
	}
	if state.State == "disabled_auth" && state.Enabled || state.State == "cooldown" && state.CooldownUntil == nil {
		return fmt.Errorf("账号运行态字段不一致: id=%d state=%s", id, state.State)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.getQuerier().ExecContext(ctx, `
		UPDATE upstream_accounts
		SET enabled = ?, state = ?, cooldown_until = ?, fail_count = ?,
			last_success_at = ?, last_error = ?, updated_at = ?
		WHERE id = ?`,
		boolToInt(state.Enabled), state.State, nullableTime(state.CooldownUntil), state.FailCount,
		nullableTime(state.LastSuccessAt), state.LastError, formatDBTime(state.UpdatedAt), id)
	if err != nil {
		return fmt.Errorf("保存账号运行态失败: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("读取账号运行态写入结果失败: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("账号不存在: %d", id)
	}
	return nil
}
