package store

import (
	"context"
	"database/sql"
	"fmt"
)

const privacyExactSecretColumns = `id, enabled, name, secret_value, value_hash, placeholder,
	category, source_type, source_ref, description,
	COALESCE(CAST(created_at AS TEXT), ''), COALESCE(CAST(updated_at AS TEXT), '')`

const insertPrivacyExactSecret = `INSERT INTO privacy_exact_secrets (
	enabled, name, secret_value, value_hash, placeholder, category, source_type, source_ref, description
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`

type privacyExactQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// ListExactSecrets 列出本地精确敏感值。调用方负责遮蔽 SecretValue。
func (s *SQLitePrivacyStore) ListExactSecrets(ctx context.Context) ([]*PrivacyExactSecretRecord, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return nil, err
	}
	return listPrivacyExactSecrets(ctx, s.db)
}

func listPrivacyExactSecrets(ctx context.Context, db privacyExactQueryer) ([]*PrivacyExactSecretRecord, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT `+privacyExactSecretColumns+`
		FROM privacy_exact_secrets
		ORDER BY created_at DESC, id DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("list privacy exact secrets failed: %w", err)
	}
	defer rows.Close()

	var records []*PrivacyExactSecretRecord
	for rows.Next() {
		record, err := scanPrivacyExactSecret(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate privacy exact secrets failed: %w", err)
	}
	return records, nil
}

// GetExactSecret 按 ID 读取精确敏感值；不存在时返回 (nil, nil)。
func (s *SQLitePrivacyStore) GetExactSecret(ctx context.Context, id int64) (*PrivacyExactSecretRecord, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+privacyExactSecretColumns+`
		FROM privacy_exact_secrets WHERE id = ?
	`, id)
	if err != nil {
		return nil, fmt.Errorf("get privacy exact secret failed: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	return scanPrivacyExactSecret(rows)
}

// FindExactSecretByHash 按 sha256(secret_value) 查重；不存在时返回 (nil, nil)。
func (s *SQLitePrivacyStore) FindExactSecretByHash(ctx context.Context, valueHash string) (*PrivacyExactSecretRecord, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+privacyExactSecretColumns+`
		FROM privacy_exact_secrets WHERE value_hash = ?
	`, valueHash)
	if err != nil {
		return nil, fmt.Errorf("find privacy exact secret failed: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	return scanPrivacyExactSecret(rows)
}

// CreateExactSecret 新增精确敏感值并返回完整记录。
func (s *SQLitePrivacyStore) CreateExactSecret(ctx context.Context, record *PrivacyExactSecretRecord) (*PrivacyExactSecretRecord, error) {
	if record == nil {
		return nil, fmt.Errorf("privacy exact secret record is nil")
	}
	if err := s.ensureSchema(ctx); err != nil {
		return nil, err
	}
	result, err := s.db.ExecContext(ctx, insertPrivacyExactSecret, record.Enabled, record.Name, record.SecretValue, record.ValueHash, record.Placeholder,
		record.Category, record.SourceType, record.SourceRef, record.Description)
	if err != nil {
		return nil, fmt.Errorf("create privacy exact secret failed: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("read privacy exact secret id failed: %w", err)
	}
	return s.GetExactSecret(ctx, id)
}

// UpdateExactSecret 更新精确敏感值。
func (s *SQLitePrivacyStore) UpdateExactSecret(ctx context.Context, record *PrivacyExactSecretRecord) error {
	if record == nil || record.ID <= 0 {
		return fmt.Errorf("invalid privacy exact secret record")
	}
	if err := s.ensureSchema(ctx); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE privacy_exact_secrets
		SET enabled = ?, name = ?, secret_value = ?, value_hash = ?, placeholder = ?,
		    category = ?, source_type = ?, source_ref = ?, description = ?,
		    updated_at = strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')
		WHERE id = ?
	`, record.Enabled, record.Name, record.SecretValue, record.ValueHash, record.Placeholder,
		record.Category, record.SourceType, record.SourceRef, record.Description, record.ID)
	if err != nil {
		return fmt.Errorf("update privacy exact secret failed: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read affected rows failed: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("privacy exact secret %d not found", record.ID)
	}
	return nil
}

// DeleteExactSecret 删除单条精确敏感值。
func (s *SQLitePrivacyStore) DeleteExactSecret(ctx context.Context, id int64) error {
	if err := s.ensureSchema(ctx); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM privacy_exact_secrets WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete privacy exact secret failed: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read affected rows failed: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("privacy exact secret %d not found", id)
	}
	return nil
}

// ClearExactSecrets 清空本地精确敏感值库。
func (s *SQLitePrivacyStore) ClearExactSecrets(ctx context.Context) error {
	if err := s.ensureSchema(ctx); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM privacy_exact_secrets`); err != nil {
		return fmt.Errorf("clear privacy exact secrets failed: %w", err)
	}
	return nil
}

type privacyExactSecretScanner interface {
	Scan(dest ...any) error
}

func scanPrivacyExactSecret(row privacyExactSecretScanner) (*PrivacyExactSecretRecord, error) {
	record := &PrivacyExactSecretRecord{}
	var createdAt, updatedAt string
	if err := row.Scan(
		&record.ID, &record.Enabled, &record.Name, &record.SecretValue,
		&record.ValueHash, &record.Placeholder, &record.Category, &record.SourceType,
		&record.SourceRef, &record.Description, &createdAt, &updatedAt,
	); err != nil {
		return nil, fmt.Errorf("scan privacy exact secret failed: %w", err)
	}
	var err error
	if record.CreatedAt, err = parseDBTime(createdAt); err != nil {
		return nil, fmt.Errorf("parse privacy exact secret created_at: %w", err)
	}
	if record.UpdatedAt, err = parseDBTime(updatedAt); err != nil {
		return nil, fmt.Errorf("parse privacy exact secret updated_at: %w", err)
	}
	return record, nil
}
