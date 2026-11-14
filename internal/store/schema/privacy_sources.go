// Package schema 保存需要由数据库初始化和独立存储入口共用的表定义。
package schema

import (
	"context"
	"database/sql"
	_ "embed"
)

// PrivacySources 是配置来源及来源关联的唯一建表定义。
//
//go:embed privacy_sources.sql
var PrivacySources string

// EnsurePrivacySources 统一新表创建与旧表升级，供应用初始化及独立存储入口共用。
func EnsurePrivacySources(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var hadReviews bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='privacy_config_source_reviews')").Scan(&hadReviews); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, PrivacySources); err != nil {
		return err
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pragma_table_info('privacy_config_sources') WHERE name='resolved_path')").Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err := tx.ExecContext(ctx, "ALTER TABLE privacy_config_sources ADD COLUMN resolved_path TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
		// 旧版本保存的 path 即为实际读取路径；保留其原有读取边界。
		if _, err := tx.ExecContext(ctx, "UPDATE privacy_config_sources SET resolved_path=path"); err != nil {
			return err
		}
	}
	if !hadReviews {
		// 旧版仅保存计数，首次启动需重新解析以建立候选摘要；不改动已导入值。
		if _, err := tx.ExecContext(ctx, "UPDATE privacy_config_sources SET content_hash=''"); err != nil {
			return err
		}
	}
	return tx.Commit()
}
