package store

import (
	"context"
	"database/sql"
	"errors"
)

// ImportExactSecrets 在一个事务中追加、去重，并在提交前准备完整运行快照。
// prepare 只能使用传入的记录，不能再次查询同一数据库或提前发布快照。
func (s *SQLitePrivacyStore) ImportExactSecrets(ctx context.Context, records []*PrivacyExactSecretRecord, prepare func([]*PrivacyExactSecretRecord) error) (int, error) {
	return s.importExactSecrets(ctx, records, 0, nil, prepare)
}

// ImportConfigSourceSecrets 显式确认允许处理已暂停来源，历史关联与词条原子提交。
func (s *SQLitePrivacyStore) ImportConfigSourceSecrets(ctx context.Context, sourceID int64, values []PrivacyConfigSourceValue, prepare func([]*PrivacyExactSecretRecord) error) (int, error) {
	if sourceID <= 0 {
		return 0, errors.New("配置来源 ID 无效")
	}
	records := make([]*PrivacyExactSecretRecord, 0, len(values))
	for _, value := range values {
		records = append(records, value.Secret)
	}
	return s.importExactSecrets(ctx, records, sourceID, values, prepare)
}

func (s *SQLitePrivacyStore) importExactSecrets(ctx context.Context, records []*PrivacyExactSecretRecord, sourceID int64, values []PrivacyConfigSourceValue, prepare func([]*PrivacyExactSecretRecord) error) (int, error) {
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
	if sourceID > 0 {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM privacy_config_sources WHERE id=?`, sourceID).Scan(&exists); err != nil {
			return 0, err
		}
		if err := recordConfigSourceValues(ctx, tx, sourceID, values); err != nil {
			return 0, err
		}
	}
	inserted, err := insertExactSecrets(ctx, tx, records)
	if err != nil {
		return 0, err
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

// 手动导入和来源同步共用追加语义：同值保留原名称、分类与启用状态。
func insertExactSecrets(ctx context.Context, tx *sql.Tx, records []*PrivacyExactSecretRecord) (int, error) {
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
	return inserted, nil
}
