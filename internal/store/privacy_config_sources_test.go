package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"sinuo/internal/store/schema"
)

func TestPrivacySourceTransactionAndSharedOwnership(t *testing.T) {
	s := newTestPrivacyStore(t)
	s.db.SetMaxOpenConns(1)
	ctx := context.Background()
	r, err := s.SaveConfigSource(ctx, &PrivacyConfigSourceRecord{Name: "one", Path: "/synthetic/one/.env", Kind: "file", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	r.ContentHash = "one"
	r.LastScanAt = "2026-11-13T01:00:00Z"
	r.ValueCount = 1
	r.ReviewHashes = []string{"pending-review-hash"}
	value := PrivacyConfigSourceValue{FilePath: r.Path, FieldPath: "password", Secret: &PrivacyExactSecretRecord{Enabled: true, Name: "synthetic", SecretValue: "synthetic-password", ValueHash: "synthetic-hash", Category: "password", Placeholder: "[密码]"}}
	blocked := errors.New("synthetic compile failure")
	if _, err := s.SyncConfigSource(ctx, r, []PrivacyConfigSourceValue{value}, func([]*PrivacyExactSecretRecord) error { return blocked }); !errors.Is(err, blocked) {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM privacy_config_source_values`).Scan(&count); err != nil || count != 0 {
		t.Fatal("关联未回滚", err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM privacy_config_source_reviews`).Scan(&count); err != nil || count != 0 {
		t.Fatal("待确认候选关联未回滚", err)
	}
	all, _ := s.ListExactSecrets(ctx)
	sources, _ := s.ListConfigSources(ctx)
	if len(all) != 0 || sources[0].ContentHash != "" || sources[0].LastSuccessAt != "" {
		t.Fatal("失败事务留下部分状态")
	}
	prepared := 0
	prepare := func(all []*PrivacyExactSecretRecord) error {
		prepared++
		if len(all) != 1 || all[0].ID <= 0 {
			t.Fatal("缺少完整候选词典")
		}
		return nil
	}
	if added, err := s.SyncConfigSource(ctx, r, []PrivacyConfigSourceValue{value}, prepare); err != nil || added != 1 {
		t.Fatal(added, err)
	}
	other, err := s.SaveConfigSource(ctx, &PrivacyConfigSourceRecord{Name: "two", Path: "/synthetic/two/.env", Kind: "file", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if added, err := s.SyncConfigSource(ctx, other, []PrivacyConfigSourceValue{value}, prepare); err != nil || added != 0 || prepared != 1 {
		t.Fatal("共享值不应重复写入或编译", err)
	}
	if err := s.DeleteConfigSource(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM privacy_config_source_reviews WHERE source_id=?`, r.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("移除来源必须清理待确认关联", err)
	}
	all, _ = s.ListExactSecrets(ctx)
	if len(all) != 1 {
		t.Fatal("删除来源影响共享值")
	}
	if err := s.DeleteExactSecret(ctx, all[0].ID); err != nil {
		t.Fatal(err)
	}
	if added, err := s.SyncConfigSource(ctx, other, []PrivacyConfigSourceValue{value}, prepare); err != nil || added != 0 {
		t.Fatal("人工删除值被重新导入", err)
	}
}

func TestPrivacySourceReviewMigrationRefreshesLegacySummaryOnce(t *testing.T) {
	s := newTestPrivacyStore(t)
	s.db.SetMaxOpenConns(1)
	ctx := context.Background()
	start := strings.Index(schema.PrivacySources, "-- 仅保存")
	end := strings.Index(schema.PrivacySources, "CREATE TABLE IF NOT EXISTS privacy_config_source_values")
	if start < 0 || end <= start {
		t.Fatal("无法构造旧版来源表")
	}
	legacy := schema.PrivacySources[:start] + schema.PrivacySources[end:]
	legacy = strings.Replace(legacy, "    excluded_count", "    review_count INTEGER NOT NULL DEFAULT 0,\n    excluded_count", 1)
	if _, err := s.db.ExecContext(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO privacy_config_sources(name,path,resolved_path,kind,content_hash,review_count,last_success_at)
VALUES('legacy','/synthetic/.env','/synthetic/.env','file','old-digest',29,'previous-success')`); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListConfigSources(ctx)
	if err != nil || len(rows) != 1 || rows[0].ContentHash != "" || rows[0].LastSuccessAt != "previous-success" || rows[0].ReviewCount != 0 {
		t.Fatal("旧计数必须失效并在下一次来源扫描时重建，保留成功状态", err)
	}
	r := rows[0]
	r.ContentHash, r.LastScanAt, r.ReviewHashes = "new-digest", "new-scan", []string{"one", "one", "two"}
	if _, err := s.SyncConfigSource(ctx, r, nil, func([]*PrivacyExactSecretRecord) error { return nil }); err != nil {
		t.Fatal(err)
	}
	reopened := NewSQLitePrivacyStore(s.db)
	rows, err = reopened.ListConfigSources(ctx)
	if err != nil || rows[0].ContentHash != "new-digest" || rows[0].ReviewCount != 2 {
		t.Fatal("重复初始化不能再次清除摘要或丢失去重候选", err)
	}
	if _, err := reopened.SaveConfigSource(ctx, r); err != nil {
		t.Fatal(err)
	}
	rows, err = reopened.ListConfigSources(ctx)
	if err != nil || rows[0].ReviewCount != 2 {
		t.Fatal("读取范围未变时应保留候选关联", err)
	}
	r.Path, r.ResolvedPath = "/synthetic/changed.env", "/synthetic/changed.env"
	if _, err := reopened.SaveConfigSource(ctx, r); err != nil {
		t.Fatal(err)
	}
	rows, err = reopened.ListConfigSources(ctx)
	if err != nil || rows[0].ReviewCount != 0 {
		t.Fatal("修改读取范围后应清除旧的候选关联", err)
	}
}

func TestPrivacySourceManualImportHistoryTransaction(t *testing.T) {
	s := newTestPrivacyStore(t)
	s.db.SetMaxOpenConns(1)
	ctx := context.Background()
	r, err := s.SaveConfigSource(ctx, &PrivacyConfigSourceRecord{Name: "paused", Path: "/synthetic/project", ResolvedPath: "/synthetic/project", Kind: "directory", Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	secret := &PrivacyExactSecretRecord{Enabled: true, Name: "reviewed", SecretValue: "source-reviewed-synthetic-value", ValueHash: "source-reviewed-hash", Category: "custom", Placeholder: "[敏感值]"}
	values := []PrivacyConfigSourceValue{
		{FilePath: "/synthetic/project/.env", FieldPath: "USERNAME", Secret: secret},
		{FilePath: "/synthetic/project/.env.local", FieldPath: "USERNAME", Secret: secret},
	}
	blocked := errors.New("synthetic snapshot failure")
	if _, err := s.ImportConfigSourceSecrets(ctx, r.ID, values, func([]*PrivacyExactSecretRecord) error { return blocked }); !errors.Is(err, blocked) {
		t.Fatal("快照准备失败应向上传递", err)
	}
	var history int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM privacy_config_source_values WHERE source_id=?`, r.ID).Scan(&history); err != nil || history != 0 {
		t.Fatal("失败确认不能留下来源历史", err)
	}
	all, err := s.ListExactSecrets(ctx)
	if err != nil || len(all) != 0 {
		t.Fatal("失败确认不能留下词条", err)
	}
	prepare := func(all []*PrivacyExactSecretRecord) error {
		if len(all) != 1 {
			t.Fatal("同值多文件关联应只导入一个词条")
		}
		return nil
	}
	if added, err := s.ImportConfigSourceSecrets(ctx, r.ID, values, prepare); err != nil || added != 1 {
		t.Fatal("暂停来源仍允许显式确认", added, err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM privacy_config_source_values WHERE source_id=?`, r.ID).Scan(&history); err != nil || history != 2 {
		t.Fatal("确认成功必须保留所有文件字段关联", err)
	}
	if added, err := s.ImportConfigSourceSecrets(ctx, r.ID, values, prepare); err != nil || added != 0 {
		t.Fatal("重复确认应保持幂等", added, err)
	}
	other, err := s.SaveConfigSource(ctx, &PrivacyConfigSourceRecord{Name: "other", Path: "/synthetic/other", Kind: "directory", Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	if added, err := s.ImportConfigSourceSecrets(ctx, other.ID, values, prepare); err != nil || added != 0 {
		t.Fatal("预览后由其他入口加入的同值不能重复写入", added, err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM privacy_config_source_values WHERE source_id=?`, other.ID).Scan(&history); err != nil || history != 2 {
		t.Fatal("同值已经存在时，确认仍必须建立当前来源历史", err)
	}
	if _, err := s.ImportConfigSourceSecrets(ctx, other.ID+100, values, prepare); err == nil {
		t.Fatal("不能向已移除来源写入孤立关联")
	}
}

func TestPrivacySourceSaveOnlyInvalidatesChangedScope(t *testing.T) {
	for _, field := range []string{"name", "enabled", "path", "resolved_path", "kind", "patterns"} {
		t.Run(field, func(t *testing.T) {
			s := newTestPrivacyStore(t)
			s.db.SetMaxOpenConns(1)
			ctx := context.Background()
			r, err := s.SaveConfigSource(ctx, &PrivacyConfigSourceRecord{Name: "original", Path: "/synthetic/project", ResolvedPath: "/synthetic/project", Kind: "directory", Patterns: ".env", Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			r.ContentHash, r.LastScanAt, r.FileCount = "digest", "last-scan", 2
			r.ExcludedCount, r.ReviewHashes = 1, []string{"pending-hash"}
			if _, err := s.SyncConfigSource(ctx, r, nil, func([]*PrivacyExactSecretRecord) error { return nil }); err != nil {
				t.Fatal(err)
			}
			if err := s.SetConfigSourceError(ctx, r.ID, "failed-scan", "synthetic read failure"); err != nil {
				t.Fatal(err)
			}
			switch field {
			case "name":
				r.Name = "renamed"
			case "enabled":
				r.Enabled = false
			case "path":
				r.Path = "/synthetic/changed"
			case "resolved_path":
				r.ResolvedPath = "/synthetic/changed"
			case "kind":
				r.Kind = "file"
			case "patterns":
				r.Patterns = "*.yaml"
			}
			// 统计由存储状态决定，不能被保存请求中的旧摘要覆盖。
			r.ReviewCount, r.LastError = 999, "forged"
			saved, err := s.SaveConfigSource(ctx, r)
			if err != nil {
				t.Fatal(err)
			}
			if field == "name" || field == "enabled" {
				if saved.ReviewCount != 1 || saved.ContentHash != "digest" || saved.LastScanAt != "failed-scan" || saved.LastSuccessAt != "last-scan" || saved.LastError != "synthetic read failure" || saved.FileCount != 2 || saved.ExcludedCount != 1 {
					t.Fatal("名称与启用开关应保留真实扫描结果", saved)
				}
			} else if saved.ReviewCount != 0 || saved.ContentHash != "" || saved.LastScanAt != "" || saved.LastSuccessAt != "" || saved.LastError != "" || saved.FileCount != 0 || saved.ExcludedCount != 0 {
				t.Fatal("读取范围变化必须清除旧结果，等待重新扫描", saved)
			}
		})
	}
}

func TestPrivacySourceResolvedPathMigrationAndPersistence(t *testing.T) {
	s := newTestPrivacyStore(t)
	s.db.SetMaxOpenConns(1)
	ctx := context.Background()
	// 重建尚无实际路径列的旧版表，确认迁移保留登记、状态与历史读取范围。
	legacy := strings.Replace(schema.PrivacySources, "    resolved_path TEXT NOT NULL,\n", "", 1)
	if legacy == schema.PrivacySources {
		t.Fatal("旧版 schema 样例未去掉新列")
	}
	if _, err := s.db.ExecContext(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO privacy_config_sources(name,path,kind,enabled,content_hash,last_success_at)
VALUES('legacy','/synthetic/.env','file',0,'digest','2026-11-13T01:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListConfigSources(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	old := rows[0]
	if old.ResolvedPath != old.Path || old.Enabled || old.ContentHash != "digest" || old.LastSuccessAt == "" {
		t.Fatal("旧来源迁移丢失数据", old)
	}
	old.Path, old.ResolvedPath = "/synthetic/.netrc", "/synthetic/dotfiles/netrc"
	if _, err := s.SaveConfigSource(ctx, old); err != nil {
		t.Fatal(err)
	}
	// 重复初始化不得用逻辑路径覆盖已固定的实际路径。
	reopened := NewSQLitePrivacyStore(s.db)
	rows, err = reopened.ListConfigSources(ctx)
	if err != nil || len(rows) != 1 || rows[0].Path != old.Path || rows[0].ResolvedPath != old.ResolvedPath {
		t.Fatal("逻辑文件名或实际路径未持久化", rows, err)
	}
}
