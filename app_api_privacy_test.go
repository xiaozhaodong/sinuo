package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
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

func TestPrivacyRulesAPI_ReversiblePreviewMatchesOutboundWithoutPublishing(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	svc := service.NewPrivacyService(store.NewSQLitePrivacyStore(db))
	if err := svc.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	settings, err := svc.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.Mode, settings.ResponseRestoreEnabled = privacy.ModeRedact, true
	if _, err := svc.UpdateSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	value := "synthetic-api-preview-value"
	record, err := svc.CreateExactSecret(ctx, &store.PrivacyExactSecretRecord{
		Enabled: true, Name: "预览测试", SecretValue: value, Category: "password", Placeholder: "[敏感值]",
	})
	if err != nil {
		t.Fatal(err)
	}
	app := &App{privacyService: svc}
	input := PrivacyRuleTestInput{Text: value, Path: "/v1/responses", UpstreamType: privacy.UpstreamTypeAccount}
	stats := svc.RuntimeStats()
	preview, err := app.TestPrivacyRules(input)
	if err != nil || preview.HitCount != 1 || !preview.Changed || !strings.HasPrefix(preview.ReplacedText, privacy.RestorePrefix) {
		t.Fatalf("API 应返回可逆占位符: %v", err)
	}
	page, err := svc.ListRestoreDictionary(0, 10)
	if err != nil || page.Total != 0 || !reflect.DeepEqual(svc.RuntimeStats(), stats) {
		t.Fatal("测试不能发布词条或改变请求统计")
	}
	body, err := json.Marshal(map[string]string{"input": value})
	if err != nil {
		t.Fatal(err)
	}
	actual, err := svc.CurrentSnapshot().Apply(privacy.Request{Path: input.Path, UpstreamType: input.UpstreamType}, body)
	if err != nil || actual.Restore == nil {
		t.Fatalf("实际出站应生成可逆映射: %v", err)
	}
	defer actual.Restore.Release()
	var outbound map[string]string
	if err := json.Unmarshal(actual.Body, &outbound); err != nil || outbound["input"] != preview.ReplacedText {
		t.Fatal("测试与实际出站必须使用同一个占位符")
	}
	if err := actual.Restore.Publish(); err != nil {
		t.Fatal(err)
	}
	record.Enabled = false
	if _, err := svc.UpdateExactSecret(ctx, record.ID, record); err != nil {
		t.Fatal(err)
	}
	before, err := svc.ListRestoreDictionary(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	preview, err = app.TestPrivacyRules(input)
	if err != nil || preview.HitCount != 0 || len(preview.RuleHits) != 0 || preview.RemaskMatches != 1 ||
		!preview.Changed || preview.ReplacedText != outbound["input"] {
		t.Fatalf("停用规则后仍应展示内存词典再次遮蔽: %v", err)
	}
	after, err := svc.ListRestoreDictionary(0, 10)
	if err != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(svc.RuntimeStats(), stats) {
		t.Fatal("字典预览不能改变词条、回填计数或请求统计")
	}
	settings.ScanMaxBytes = 4
	if _, err := svc.UpdateSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	preview, err = app.TestPrivacyRules(input)
	if err != nil || preview.Changed || preview.ReplacedText != value || preview.RemaskMatches != 0 ||
		!strings.Contains(preview.PreviewWarning, "原文放行") {
		t.Fatalf("超限放行应展示完整原文和明确提示: %v", err)
	}
	settings.OverLimitAction = privacy.OverLimitFailClosed
	if _, err := svc.UpdateSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	preview, err = app.TestPrivacyRules(input)
	if err == nil || !strings.Contains(err.Error(), "超过上限") || preview.ReplacedText != "" {
		t.Fatal("超限拒绝必须返回错误，不能显示成功预览")
	}
}
