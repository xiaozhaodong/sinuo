package store

import (
	"context"
	"database/sql"
	"errors"
)

var ErrConfigSourceHistoryLimit = errors.New("该来源的历史关联超过 100000 条，请暂停并整理来源")

// PrivacyConfigSourceRecord 仅保存来源位置和同步摘要，不含文件内容或敏感值。
type PrivacyConfigSourceRecord struct {
	ID            int64    `json:"id"`
	Name          string   `json:"name"`
	Path          string   `json:"path"`
	ResolvedPath  string   `json:"-"` // 仅由服务端根据登记路径生成，固定实际读取位置。
	Kind          string   `json:"kind"`
	Patterns      string   `json:"patterns"`
	Enabled       bool     `json:"enabled"`
	ContentHash   string   `json:"-"`
	LastScanAt    string   `json:"last_scan_at"`
	LastSuccessAt string   `json:"last_success_at"`
	LastError     string   `json:"last_error"`
	FileCount     int      `json:"file_count"`
	ValueCount    int      `json:"value_count"`
	ReviewCount   int      `json:"review_count"`
	ReviewHashes  []string `json:"-"` // 最新可选候选的摘要，不暴露给前端。
	ExcludedCount int      `json:"excluded_count"`
}

type PrivacyConfigSourceValue struct {
	FilePath, FieldPath string
	Secret              *PrivacyExactSecretRecord
}

type PrivacyConfigSourceStore interface {
	ListConfigSources(context.Context) ([]*PrivacyConfigSourceRecord, error)
	SaveConfigSource(context.Context, *PrivacyConfigSourceRecord) (*PrivacyConfigSourceRecord, error)
	DeleteConfigSource(context.Context, int64) error
	SetConfigSourceError(context.Context, int64, string, string) error
	RefreshConfigSourceReviews(context.Context, int64, []string) error
	ImportConfigSourceSecrets(context.Context, int64, []PrivacyConfigSourceValue, func([]*PrivacyExactSecretRecord) error) (int, error)
	SyncConfigSource(context.Context, *PrivacyConfigSourceRecord, []PrivacyConfigSourceValue, func([]*PrivacyExactSecretRecord) error) (int, error)
}

const sourceColumns = `id, name, path, resolved_path, kind, patterns, enabled, content_hash, last_scan_at,
last_success_at, last_error, file_count, value_count,
(SELECT COUNT(*) FROM privacy_config_source_reviews AS review
 WHERE review.source_id=privacy_config_sources.id
 AND NOT EXISTS(SELECT 1 FROM privacy_exact_secrets AS secret WHERE secret.value_hash=review.value_hash)), excluded_count`

func (s *SQLitePrivacyStore) ListConfigSources(ctx context.Context) ([]*PrivacyConfigSourceRecord, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+sourceColumns+` FROM privacy_config_sources ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*PrivacyConfigSourceRecord, 0)
	for rows.Next() {
		r, err := scanPrivacyConfigSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanPrivacyConfigSource(row interface{ Scan(...any) error }) (*PrivacyConfigSourceRecord, error) {
	r := new(PrivacyConfigSourceRecord)
	err := row.Scan(&r.ID, &r.Name, &r.Path, &r.ResolvedPath, &r.Kind, &r.Patterns, &r.Enabled, &r.ContentHash,
		&r.LastScanAt, &r.LastSuccessAt, &r.LastError, &r.FileCount, &r.ValueCount, &r.ReviewCount, &r.ExcludedCount)
	return r, err
}

func (s *SQLitePrivacyStore) SaveConfigSource(ctx context.Context, r *PrivacyConfigSourceRecord) (*PrivacyConfigSourceRecord, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return nil, err
	}
	next := *r
	if next.ID == 0 {
		res, err := s.db.ExecContext(ctx, `INSERT INTO privacy_config_sources(name,path,resolved_path,kind,patterns,enabled) VALUES(?,?,?,?,?,?)`, r.Name, r.Path, r.ResolvedPath, r.Kind, r.Patterns, r.Enabled)
		if err != nil {
			return nil, err
		}
		next.ID, err = res.LastInsertId()
		return &next, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	previous, err := scanPrivacyConfigSource(tx.QueryRowContext(ctx, `SELECT `+sourceColumns+` FROM privacy_config_sources WHERE id=?`, r.ID))
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE privacy_config_sources SET name=?,path=?,resolved_path=?,kind=?,patterns=?,enabled=? WHERE id=?`,
		r.Name, r.Path, r.ResolvedPath, r.Kind, r.Patterns, r.Enabled, r.ID); err != nil {
		return nil, err
	}
	// 名称和启用开关不改变读取范围，必须保留最近检查与手动确认入口。
	if previous.Path != r.Path || previous.ResolvedPath != r.ResolvedPath || previous.Kind != r.Kind || previous.Patterns != r.Patterns {
		if _, err := tx.ExecContext(ctx, `UPDATE privacy_config_sources SET content_hash='',last_scan_at='',last_success_at='',last_error='',file_count=0,value_count=0,excluded_count=0 WHERE id=?`, r.ID); err != nil {
			return nil, err
		}
		if err := replaceConfigSourceReviews(ctx, tx, r.ID, nil); err != nil {
			return nil, err
		}
	}
	saved, err := scanPrivacyConfigSource(tx.QueryRowContext(ctx, `SELECT `+sourceColumns+` FROM privacy_config_sources WHERE id=?`, r.ID))
	if err != nil {
		return nil, err
	}
	return saved, tx.Commit()
}

// 删除登记仅移除关联，已导入的精确值继续保护历史请求。
func (s *SQLitePrivacyStore) DeleteConfigSource(ctx context.Context, id int64) error {
	if err := s.ensureSchema(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := replaceConfigSourceReviews(ctx, tx, id, nil); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM privacy_config_source_values WHERE source_id=?`, id); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM privacy_config_sources WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

func (s *SQLitePrivacyStore) SetConfigSourceError(ctx context.Context, id int64, at, message string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE privacy_config_sources SET last_scan_at=?,last_error=? WHERE id=?`, at, message, id)
	return err
}

// RefreshConfigSourceReviews 刷新预览发现的候选摘要，不导入原值或更改启用状态。
// 清除同步摘要，确保下次文件事件仍完整同步，避免预览后的内容恢复被缓存跳过。
func (s *SQLitePrivacyStore) RefreshConfigSourceReviews(ctx context.Context, id int64, hashes []string) error {
	if err := s.ensureSchema(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE privacy_config_sources SET content_hash='' WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		if err != nil {
			return err
		}
		return sql.ErrNoRows
	}
	if err := replaceConfigSourceReviews(ctx, tx, id, hashes); err != nil {
		return err
	}
	return tx.Commit()
}

func replaceConfigSourceReviews(ctx context.Context, tx *sql.Tx, id int64, hashes []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM privacy_config_source_reviews WHERE source_id=?`, id); err != nil {
		return err
	}
	if len(hashes) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO privacy_config_source_reviews(source_id,value_hash) VALUES(?,?) ON CONFLICT DO NOTHING`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, hash := range hashes {
		if _, err := stmt.ExecContext(ctx, id, hash); err != nil {
			return err
		}
	}
	return nil
}

// SyncConfigSource 将原值追加、来源关联与状态作为同一事务提交。prepare 失败整体回滚。
func (s *SQLitePrivacyStore) SyncConfigSource(ctx context.Context, r *PrivacyConfigSourceRecord, values []PrivacyConfigSourceValue, prepare func([]*PrivacyExactSecretRecord) error) (int, error) {
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
	var enabled bool
	if err = tx.QueryRowContext(ctx, `SELECT enabled FROM privacy_config_sources WHERE id=?`, r.ID).Scan(&enabled); err != nil {
		return 0, err
	}
	if !enabled {
		return 0, errors.New("配置来源已停用")
	}
	seenHashes := make(map[string]bool)
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT value_hash FROM privacy_config_source_values WHERE source_id=?`, r.ID)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			rows.Close()
			return 0, err
		}
		seenHashes[hash] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE privacy_config_source_values SET active=0 WHERE source_id=?`, r.ID); err != nil {
		return 0, err
	}
	if err := recordConfigSourceValues(ctx, tx, r.ID, values); err != nil {
		return 0, err
	}
	records := make([]*PrivacyExactSecretRecord, 0, len(values))
	for _, v := range values {
		// 已被用户删除的历史值不会因重启或重扫同一来源而自动复活。
		if !seenHashes[v.Secret.ValueHash] {
			records = append(records, v.Secret)
			seenHashes[v.Secret.ValueHash] = true
		}
	}
	added, err := insertExactSecrets(ctx, tx, records)
	if err != nil {
		return 0, err
	}
	if added > 0 {
		all, err := listPrivacyExactSecrets(ctx, tx)
		if err != nil {
			return 0, err
		}
		if err := prepare(all); err != nil {
			return 0, err
		}
	}
	if err := replaceConfigSourceReviews(ctx, tx, r.ID, r.ReviewHashes); err != nil {
		return 0, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE privacy_config_sources SET content_hash=?,last_scan_at=?,last_success_at=?,last_error='',file_count=?,value_count=?,excluded_count=? WHERE id=?`,
		r.ContentHash, r.LastScanAt, r.LastScanAt, r.FileCount, r.ValueCount, r.ExcludedCount, r.ID)
	if err != nil {
		return 0, err
	}
	return added, tx.Commit()
}

// 自动同步与来源手动确认共用历史记录，事务失败时随词条一起回滚。
func recordConfigSourceValues(ctx context.Context, tx *sql.Tx, id int64, values []PrivacyConfigSourceValue) error {
	for _, value := range values {
		if value.Secret == nil {
			return errors.New("来源敏感值为空")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO privacy_config_source_values(source_id,file_path,field_path,value_hash,active) VALUES(?,?,?,?,1)
ON CONFLICT(source_id,file_path,field_path,value_hash) DO UPDATE SET active=1`, id, value.FilePath, value.FieldPath, value.Secret.ValueHash); err != nil {
			return err
		}
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM privacy_config_source_values WHERE source_id=?`, id).Scan(&count); err != nil {
		return err
	}
	if count > 100000 {
		return ErrConfigSourceHistoryLimit
	}
	return nil
}
