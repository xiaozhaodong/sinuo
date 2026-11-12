package store

import (
	"context"
	"errors"
)

// ImportExactSecrets 在一个事务中追加、去重，并在提交前准备完整运行快照。
// prepare 只能使用传入的记录，不能再次查询同一数据库或提前发布快照。
func (s *SQLitePrivacyStore) ImportExactSecrets(ctx context.Context, records []*PrivacyExactSecretRecord, prepare func([]*PrivacyExactSecretRecord) error) (int, error) {
	if prepare == nil {
		return 0, errors.New("缺少敏感值快照准备步骤")
	}
	if err := s.ensureSchema(ctx); err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, insertPrivacyExactSecret+" ON CONFLICT(value_hash) DO NOTHING")
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	inserted := 0
	for _, r := range records {
		if r == nil {
			return 0, errors.New("敏感值记录为空")
		}
		res, err := stmt.ExecContext(ctx, r.Enabled, r.Name, r.SecretValue, r.ValueHash, r.Placeholder, r.Category, r.SourceType, r.SourceRef, r.Description)
		if err != nil {
			return 0, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		inserted += int(n)
	}
	if inserted > 0 {
		all, err := listPrivacyExactSecrets(ctx, tx)
		if err != nil {
			return 0, err
		}
		if err := prepare(all); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return inserted, nil
}
