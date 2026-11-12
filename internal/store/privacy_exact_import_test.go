package store

import (
	"context"
	"errors"
	"testing"
)

func TestPrivacyExactImportTransaction(t *testing.T) {
	s := newTestPrivacyStore(t)
	s.db.SetMaxOpenConns(1)
	ctx := context.Background()
	record := func(value string) *PrivacyExactSecretRecord {
		return &PrivacyExactSecretRecord{Enabled: true, Name: "合成候选", SecretValue: value, ValueHash: value, Category: "password", Placeholder: "[密码]", SourceType: "imported"}
	}
	a := record("first-value")
	blocked := errors.New("synthetic compile failure")
	_, err := s.ImportExactSecrets(ctx, []*PrivacyExactSecretRecord{a, record("second-value")}, func(all []*PrivacyExactSecretRecord) error {
		if len(all) != 2 || all[0].ID == 0 {
			t.Fatal("提交前没有完整持久化 ID", all)
		}
		return blocked
	})
	if !errors.Is(err, blocked) {
		t.Fatal(err)
	}
	all, err := s.ListExactSecrets(ctx)
	if err != nil || len(all) != 0 {
		t.Fatal("回滚失败", all, err)
	}
	a.Enabled = false
	a.Name = "保留名字"
	if _, err = s.CreateExactSecret(ctx, a); err != nil {
		t.Fatal(err)
	}
	a.Enabled = true
	a.Name = "不能覆盖"
	prepared := 0
	n, err := s.ImportExactSecrets(ctx, []*PrivacyExactSecretRecord{a, record("second-value"), record("second-value")}, func(all []*PrivacyExactSecretRecord) error { prepared++; return nil })
	if err != nil || n != 1 || prepared != 1 {
		t.Fatal(n, prepared, err)
	}
	saved, err := s.FindExactSecretByHash(ctx, a.ValueHash)
	if err != nil || saved.Enabled || saved.Name != "保留名字" {
		t.Fatal(saved, err)
	}
	n, err = s.ImportExactSecrets(ctx, []*PrivacyExactSecretRecord{a}, func(all []*PrivacyExactSecretRecord) error { t.Fatal("全重复时不应重建"); return nil })
	if err != nil || n != 0 {
		t.Fatal(n, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = s.ImportExactSecrets(cancelled, []*PrivacyExactSecretRecord{record("third-value")}, func([]*PrivacyExactSecretRecord) error { return nil }); err == nil {
		t.Fatal("取消未阻止写入")
	}
}
