package main

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"sinuo/internal/privacy"
	"sinuo/internal/service"
	"sinuo/internal/store"

	_ "modernc.org/sqlite"
)

func TestPrivacyRulesAPI_CompileBlocked_ReturnsErrorAndRecovers(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	st := store.NewSQLitePrivacyStore(db)
	value := strings.Repeat("x", (64<<10)+1)
	// 模拟旧版本留下的超限值，新版服务入口会拒绝创建此记录。
	legacy, err := st.CreateExactSecret(ctx, &store.PrivacyExactSecretRecord{
		Enabled: true, Name: "历史敏感值", SecretValue: value,
		ValueHash: service.HashPrivacySecretValue(value), Category: "password", Placeholder: "[敏感值]",
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := service.NewPrivacyService(st)
	if err := svc.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if !svc.CurrentSnapshot().CompileBlocked {
		t.Fatal("旧词典超限应阻止编译")
	}
	app := &App{privacyService: svc}
	input := PrivacyRuleTestInput{Text: value, Path: "/v1/messages", UpstreamType: privacy.UpstreamTypeEndpoint}
	result, err := app.TestPrivacyRules(input)
	if err == nil || !strings.Contains(err.Error(), privacy.ErrLiteralBudget.Error()) {
		t.Fatalf("测试接口应返回明确的容量错误: %v", err)
	}
	if result.ReplacedText != "" || strings.Contains(err.Error(), value) {
		t.Fatal("测试失败不应返回未扫描的原文或在错误中暴露敏感值")
	}

	if err := svc.DeleteExactSecret(ctx, legacy.ID); err != nil {
		t.Fatal(err)
	}
	result, err = app.TestPrivacyRules(input)
	if err != nil || result.HitCount != 0 || result.Changed || result.ReplacedText != value {
		t.Fatalf("修复词典后正常零命中测试应恢复成功: %v", err)
	}
	const secret = "synthetic-passphrase"
	if _, err := svc.CreateExactSecret(ctx, &store.PrivacyExactSecretRecord{
		Enabled: true, Name: "有效敏感值", SecretValue: secret, Category: "password", Placeholder: "[敏感值]",
	}); err != nil {
		t.Fatal(err)
	}
	input.Text = secret
	result, err = app.TestPrivacyRules(input)
	if err != nil || result.HitCount != 1 || !result.Changed || result.ReplacedText != "[敏感值]" {
		t.Fatalf("修复词典后正常命中测试应恢复成功: %v", err)
	}
}
