package service

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"sinuo/internal/privacy"
	"sinuo/internal/store"

	_ "modernc.org/sqlite"
)

func newTestPrivacyService(t *testing.T) *PrivacyService {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	svc := NewPrivacyService(store.NewSQLitePrivacyStore(db))
	if err := svc.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize failed: %v", err)
	}
	return svc
}

func enableRedactMode(t *testing.T, svc *PrivacyService) {
	t.Helper()
	ctx := context.Background()
	settings, err := svc.GetSettings(ctx)
	if err != nil {
		t.Fatalf("get settings failed: %v", err)
	}
	settings.Mode = privacy.ModeRedact
	if _, err := svc.UpdateSettings(ctx, settings); err != nil {
		t.Fatalf("update settings failed: %v", err)
	}
}

func newOpenAIKeyRuleRecord() *store.PrivacyRuleRecord {
	return &store.PrivacyRuleRecord{
		Enabled:     true,
		Name:        "OpenAI Key",
		Priority:    100,
		MatchType:   "regex",
		Pattern:     `sk-(?:proj-)?[A-Za-z0-9_-]{20,}`,
		Placeholder: "[OpenAI密钥]",
		Action:      "redact",
		ScopeJSON:   "{}",
	}
}

func TestPrivacyServiceDefaultDisabledPassthrough(t *testing.T) {
	svc := newTestPrivacyService(t)
	body := []byte(`{"messages":[{"role":"user","content":"sk-proj-abcdefghijklmnopqrstuvwxyz123456"}]}`)
	result, err := svc.Apply(context.Background(), privacy.Request{Path: "/v1/messages"}, body)
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if !bytes.Equal(result.Body, body) || result.Action != privacy.ModeDisabled {
		t.Errorf("default disabled must passthrough: %+v", result)
	}
}

func TestPrivacyServiceRuleSaveHotReload(t *testing.T) {
	svc := newTestPrivacyService(t)
	ctx := context.Background()
	enableRedactMode(t, svc)

	versionBefore := svc.SnapshotVersion()
	created, err := svc.CreateRule(ctx, newOpenAIKeyRuleRecord())
	if err != nil {
		t.Fatalf("create rule failed: %v", err)
	}
	if svc.SnapshotVersion() <= versionBefore {
		t.Error("snapshot version must increase after rule save")
	}

	body := []byte(`{"messages":[{"role":"user","content":"key sk-proj-abcdefghijklmnopqrstuvwxyz123456"}]}`)
	result, err := svc.Apply(ctx, privacy.Request{Path: "/v1/messages", UpstreamType: "endpoint"}, body)
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if !result.Changed || !bytes.Contains(result.Body, []byte("[OpenAI密钥]")) {
		t.Errorf("rule not effective after save: %s", result.Body)
	}

	// 修改 pattern 后无需重启立即生效
	created.Pattern = `nomatch-[0-9]{10}`
	if _, err := svc.UpdateRule(ctx, created); err != nil {
		t.Fatalf("update rule failed: %v", err)
	}
	result, err = svc.Apply(ctx, privacy.Request{Path: "/v1/messages", UpstreamType: "endpoint"}, body)
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if result.Changed {
		t.Error("updated pattern must take effect immediately")
	}

	// 禁用规则后恢复透传
	created.Pattern = `sk-(?:proj-)?[A-Za-z0-9_-]{20,}`
	created.Enabled = false
	if _, err := svc.UpdateRule(ctx, created); err != nil {
		t.Fatalf("disable rule failed: %v", err)
	}
	result, err = svc.Apply(ctx, privacy.Request{Path: "/v1/messages", UpstreamType: "endpoint"}, body)
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if result.Changed || !bytes.Equal(result.Body, body) {
		t.Error("disabled rule must restore passthrough")
	}
}

func TestPrivacyServiceInvalidRegexRejectedWithoutSnapshotSwap(t *testing.T) {
	svc := newTestPrivacyService(t)
	ctx := context.Background()
	enableRedactMode(t, svc)
	if _, err := svc.CreateRule(ctx, newOpenAIKeyRuleRecord()); err != nil {
		t.Fatalf("create rule failed: %v", err)
	}
	versionBefore := svc.SnapshotVersion()

	bad := newOpenAIKeyRuleRecord()
	bad.Name = "Broken"
	bad.Pattern = `sk-(unclosed`
	if _, err := svc.CreateRule(ctx, bad); err == nil {
		t.Fatal("invalid regex must be rejected")
	}
	if svc.SnapshotVersion() != versionBefore {
		t.Error("failed save must not swap snapshot")
	}

	rules, err := svc.ListRules(ctx)
	if err != nil {
		t.Fatalf("list rules failed: %v", err)
	}
	for _, rule := range rules {
		if rule.Name == "Broken" {
			t.Fatal("invalid rule must not be persisted")
		}
	}

	// 现有规则仍然生效
	body := []byte(`{"messages":[{"role":"user","content":"sk-proj-abcdefghijklmnopqrstuvwxyz123456"}]}`)
	result, err := svc.Apply(ctx, privacy.Request{Path: "/v1/messages"}, body)
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if !result.Changed {
		t.Error("existing snapshot must keep working after rejected save")
	}
}

func TestPrivacyServiceDisabledRuleAllowsInvalidPatternButEnableRejects(t *testing.T) {
	svc := newTestPrivacyService(t)
	ctx := context.Background()

	disabled := newOpenAIKeyRuleRecord()
	disabled.Enabled = false
	disabled.Pattern = `sk-(unclosed`
	created, err := svc.CreateRule(ctx, disabled)
	if err != nil {
		t.Fatalf("disabled rule with invalid pattern must be savable: %v", err)
	}

	created.Enabled = true
	if _, err := svc.UpdateRule(ctx, created); err == nil {
		t.Fatal("enabling invalid pattern must be rejected")
	}
}

func TestPrivacyServiceStartupDegradedOnInvalidEnabledRule(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	st := store.NewSQLitePrivacyStore(db)
	ctx := context.Background()

	// 直接落一条非法启用规则模拟历史脏数据
	if _, err := st.CreateRule(ctx, &store.PrivacyRuleRecord{
		Enabled: true, Name: "Legacy Broken", Priority: 100,
		MatchType: "regex", Pattern: `sk-(unclosed`, Placeholder: "[x]",
		Action: "redact", ScopeJSON: "{}", Source: "custom",
	}); err != nil {
		t.Fatalf("seed broken rule failed: %v", err)
	}
	if _, err := st.CreateRule(ctx, &store.PrivacyRuleRecord{
		Enabled: true, Name: "Valid", Priority: 110,
		MatchType: "literal", Pattern: "secret", Placeholder: "[x]",
		Action: "redact", ScopeJSON: "{}", Source: "custom",
	}); err != nil {
		t.Fatalf("seed valid rule failed: %v", err)
	}

	svc := NewPrivacyService(st)
	if err := svc.Initialize(ctx); err != nil {
		t.Fatalf("initialize must not fail on degraded rules: %v", err)
	}
	if svc.Status() != PrivacyStatusDegraded {
		t.Errorf("status = %q, want degraded", svc.Status())
	}
	if !strings.Contains(svc.CurrentSnapshot().CompileError, "Legacy Broken") {
		t.Errorf("compile error summary missing: %q", svc.CurrentSnapshot().CompileError)
	}

	// 失败信息写回 compile_error，有效规则仍激活
	rules, err := st.ListRules(ctx)
	if err != nil {
		t.Fatalf("list rules failed: %v", err)
	}
	for _, rule := range rules {
		if rule.Name == "Legacy Broken" && rule.CompileError == "" {
			t.Error("compile_error must be written back for broken enabled rule")
		}
	}
	if len(svc.CurrentSnapshot().Rules) != 4 {
		t.Errorf("active rules = %d, want builtin rules plus valid custom rule", len(svc.CurrentSnapshot().Rules))
	}
}

func TestPrivacyServiceInitializeRetiresLegacyBuiltinEmailRule(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	st := store.NewSQLitePrivacyStore(db)
	ctx := context.Background()

	if _, err := st.CreateRule(ctx, &store.PrivacyRuleRecord{
		Enabled: true, Name: "邮箱地址", Description: "基础邮箱格式",
		Priority: 50, MatchType: privacy.MatchTypeBuiltin, Pattern: privacy.BuiltinEmail,
		Placeholder: "[邮箱]", Action: privacy.ActionRedact, ScopeJSON: "{}", Source: privacy.SourceBuiltin,
	}); err != nil {
		t.Fatalf("seed legacy email rule failed: %v", err)
	}

	svc := NewPrivacyService(st)
	if err := svc.Initialize(ctx); err != nil {
		t.Fatalf("initialize failed: %v", err)
	}

	rules, err := st.ListRules(ctx)
	if err != nil {
		t.Fatalf("list rules failed: %v", err)
	}
	var emailRule *store.PrivacyRuleRecord
	for _, rule := range rules {
		if rule.Pattern == privacy.BuiltinEmail {
			emailRule = rule
			break
		}
	}
	if emailRule == nil {
		t.Fatal("legacy email rule should remain visible but disabled")
	}
	if emailRule.Enabled {
		t.Fatalf("legacy email rule must be disabled after initialize: %+v", emailRule)
	}
	if !strings.Contains(emailRule.Description, "误报风险高") {
		t.Fatalf("legacy email rule should explain retirement, got %q", emailRule.Description)
	}

	result := svc.TestText(privacy.Request{Path: "/v1/messages"}, "contact alice@example.com")
	if result.HitCount != 0 || strings.Contains(string(result.Body), "[邮箱]") {
		t.Fatalf("retired email rule must not be active: %+v body=%s", result, result.Body)
	}
}

func TestPrivacyServiceImportPresetSkipsDuplicates(t *testing.T) {
	svc := newTestPrivacyService(t)
	ctx := context.Background()

	created, err := svc.ImportPreset(ctx, privacy.PresetAIAPIKeys)
	if err != nil {
		t.Fatalf("import preset failed: %v", err)
	}
	if len(created) == 0 {
		t.Fatal("preset must create rules")
	}

	again, err := svc.ImportPreset(ctx, privacy.PresetAIAPIKeys)
	if err != nil {
		t.Fatalf("re-import preset failed: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("duplicate names must be skipped, got %d", len(again))
	}

	if _, err := svc.ImportPreset(ctx, "no-such-preset"); err == nil {
		t.Fatal("unknown preset must fail")
	}
}

func TestPrivacyServiceImportPresetSyncsExistingPresetRule(t *testing.T) {
	svc := newTestPrivacyService(t)
	ctx := context.Background()
	enableRedactMode(t, svc)

	if _, err := svc.ImportPreset(ctx, privacy.PresetAIAPIKeys); err != nil {
		t.Fatalf("import preset failed: %v", err)
	}
	rules, err := svc.ListRules(ctx)
	if err != nil {
		t.Fatalf("list rules failed: %v", err)
	}
	var passwordRule *store.PrivacyRuleRecord
	for _, rule := range rules {
		if rule.Name == "密码字段" {
			passwordRule = rule
			break
		}
	}
	if passwordRule == nil {
		t.Fatal("password preset rule not found")
	}

	passwordRule.Pattern = `(?i)(?:\b(?:password|passwd|pwd|passphrase)\b|密码|口令)\s*[:=：]\s*['"]?[^'"\s,;]{6,}['"]?`
	if _, err := svc.UpdateRule(ctx, passwordRule); err != nil {
		t.Fatalf("seed legacy password pattern failed: %v", err)
	}

	changed, err := svc.ImportPreset(ctx, privacy.PresetAIAPIKeys)
	if err != nil {
		t.Fatalf("re-import preset failed: %v", err)
	}
	if len(changed) != 1 || changed[0].Name != "密码字段" {
		t.Fatalf("changed rules = %+v, want password rule only", changed)
	}
	if !strings.Contains(changed[0].Pattern, "?P<redact>") {
		t.Fatalf("password rule pattern was not synced: %s", changed[0].Pattern)
	}

	result := svc.TestText(privacy.Request{Path: "/v1/messages"}, `password = "SuperSecret123"`)
	if string(result.Body) != `password = "[密码]"` {
		t.Fatalf("body = %q", result.Body)
	}
}

func TestPrivacyServiceTestTextIgnoresDisabledMode(t *testing.T) {
	svc := newTestPrivacyService(t)
	ctx := context.Background()
	if _, err := svc.CreateRule(ctx, newOpenAIKeyRuleRecord()); err != nil {
		t.Fatalf("create rule failed: %v", err)
	}

	result := svc.TestText(privacy.Request{Path: "/v1/messages"}, "key sk-proj-abcdefghijklmnopqrstuvwxyz123456")
	if result.HitCount != 1 || !strings.Contains(string(result.Body), "[OpenAI密钥]") {
		t.Errorf("test panel must work with mode=disabled: %+v", result)
	}
}

func TestPrivacyServiceRuntimeStats(t *testing.T) {
	svc := newTestPrivacyService(t)
	ctx := context.Background()
	enableRedactMode(t, svc)
	if _, err := svc.CreateRule(ctx, newOpenAIKeyRuleRecord()); err != nil {
		t.Fatalf("create rule failed: %v", err)
	}

	body := []byte(`{"messages":[{"role":"user","content":"sk-proj-abcdefghijklmnopqrstuvwxyz123456"}]}`)
	if _, err := svc.Apply(ctx, privacy.Request{Path: "/v1/messages"}, body); err != nil {
		t.Fatalf("apply failed: %v", err)
	}

	stats := svc.RuntimeStats()
	if stats.ScanCount != 1 || stats.HitCount != 1 || len(stats.RuleStats) != 1 {
		t.Errorf("unexpected stats: %+v", stats)
	}
}

func TestPrivacyServiceRuntimeStatsDeduplicatesByRequestID(t *testing.T) {
	svc := newTestPrivacyService(t)
	ctx := context.Background()
	enableRedactMode(t, svc)
	if _, err := svc.CreateRule(ctx, newOpenAIKeyRuleRecord()); err != nil {
		t.Fatalf("create rule failed: %v", err)
	}

	body := []byte(`{"messages":[{"role":"user","content":"sk-proj-abcdefghijklmnopqrstuvwxyz123456"}]}`)
	req := privacy.Request{RequestID: "req-privacy-dedupe", Path: "/v1/messages"}
	for i := 0; i < 2; i++ {
		if _, err := svc.Apply(ctx, req, body); err != nil {
			t.Fatalf("apply %d failed: %v", i, err)
		}
	}

	stats := svc.RuntimeStats()
	if stats.ScanCount != 1 || stats.HitCount != 1 {
		t.Fatalf("duplicate request stats not deduped: %+v", stats)
	}
	if len(stats.RuleStats) != 1 || stats.RuleStats[0].HitCount != 1 {
		t.Fatalf("duplicate rule stats not deduped: %+v", stats.RuleStats)
	}
}

// 去重窗口滑出的请求连同其规则命中记录一起清掉，内存不随请求数增长
func TestPrivacyServiceStatsDedupeWindowEvictsRuleHits(t *testing.T) {
	svc := NewPrivacyService(nil)
	result := privacy.ApplyResult{Action: privacy.ModeRedact, RuleHits: []privacy.RuleHit{{RuleID: 1, Count: 1}, {RuleID: 2, Count: 1}}}
	for i := range privacyStatsDedupeWindow + 10 {
		svc.recordStats(privacy.Request{RequestID: fmt.Sprintf("req-%d", i)}, result, nil)
	}
	if len(svc.seenRuleHits) != privacyStatsDedupeWindow || len(svc.seenRequests) != privacyStatsDedupeWindow {
		t.Fatalf("dedupe maps should be bounded: rules=%d requests=%d", len(svc.seenRuleHits), len(svc.seenRequests))
	}
	if _, kept := svc.seenRuleHits["req-0"]; kept {
		t.Fatal("evicted request should drop its rule-hit record")
	}
	if stats := svc.RuntimeStats(); stats.HitCount != int64(2*(privacyStatsDedupeWindow+10)) {
		t.Fatalf("hit count = %d", stats.HitCount)
	}
}

func TestPrivacyServiceExportRules(t *testing.T) {
	svc := newTestPrivacyService(t)
	ctx := context.Background()
	if _, err := svc.CreateRule(ctx, newOpenAIKeyRuleRecord()); err != nil {
		t.Fatalf("create rule failed: %v", err)
	}
	export, err := svc.ExportRules(ctx)
	if err != nil {
		t.Fatalf("export failed: %v", err)
	}
	if export.Settings == nil || len(export.Rules) != 4 {
		t.Errorf("unexpected export: %+v", export)
	}
}

func TestPrivacyServiceExactSecretHotReload(t *testing.T) {
	svc := newTestPrivacyService(t)
	ctx := context.Background()
	enableRedactMode(t, svc)

	body := []byte(`{"messages":[{"role":"user","content":"key sk-proj-abcdefghijklmnopqrstuvwxyz123456"}]}`)
	result, err := svc.Apply(ctx, privacy.Request{Path: "/v1/messages"}, body)
	if err != nil {
		t.Fatalf("apply before exact failed: %v", err)
	}
	if result.Changed {
		t.Fatal("unregistered fake key must not be redacted by default")
	}

	created, err := svc.CreateExactSecret(ctx, &store.PrivacyExactSecretRecord{
		Enabled:     true,
		Name:        "项目 OpenAI Key",
		SecretValue: "sk-proj-abcdefghijklmnopqrstuvwxyz123456",
		Category:    "api_key",
		Placeholder: "[OpenAI密钥]",
		SourceType:  "manual",
	})
	if err != nil {
		t.Fatalf("create exact secret failed: %v", err)
	}
	result, err = svc.Apply(ctx, privacy.Request{Path: "/v1/messages"}, body)
	if err != nil {
		t.Fatalf("apply after exact failed: %v", err)
	}
	if !result.Changed || !bytes.Contains(result.Body, []byte("[OpenAI密钥]")) {
		t.Fatalf("exact secret must redact after hot reload: %s", result.Body)
	}
	if len(result.RuleHits) != 1 || result.RuleHits[0].Source != privacy.SourceExact {
		t.Fatalf("hit source must be exact: %+v", result.RuleHits)
	}

	created.Enabled = false
	if _, err := svc.UpdateExactSecret(ctx, created.ID, created); err != nil {
		t.Fatalf("disable exact secret failed: %v", err)
	}
	result, err = svc.Apply(ctx, privacy.Request{Path: "/v1/messages"}, body)
	if err != nil {
		t.Fatalf("apply after disable failed: %v", err)
	}
	if result.Changed {
		t.Fatal("disabled exact secret must stop redacting")
	}
}

func TestPrivacyServiceExactSecretDetectModeCountsOnly(t *testing.T) {
	svc := newTestPrivacyService(t)
	ctx := context.Background()
	settings, err := svc.GetSettings(ctx)
	if err != nil {
		t.Fatalf("get settings failed: %v", err)
	}
	settings.Mode = privacy.ModeDetect
	if _, err := svc.UpdateSettings(ctx, settings); err != nil {
		t.Fatalf("enable detect mode failed: %v", err)
	}
	if _, err := svc.CreateExactSecret(ctx, &store.PrivacyExactSecretRecord{
		Enabled: true, Name: "Token", SecretValue: "tok_123456789012",
		Category: "token", Placeholder: "[Token]", SourceType: "manual",
	}); err != nil {
		t.Fatalf("create exact secret failed: %v", err)
	}
	body := []byte(`{"messages":[{"role":"user","content":"tok_123456789012"}]}`)
	result, err := svc.Apply(ctx, privacy.Request{Path: "/v1/messages"}, body)
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if result.Changed || !bytes.Equal(result.Body, body) || result.HitCount != 1 {
		t.Fatalf("detect mode must count without replacing: %+v", result)
	}
}

func TestPrivacyServiceExactSecretDuplicateAndMask(t *testing.T) {
	svc := newTestPrivacyService(t)
	ctx := context.Background()
	secret := "short-token12"
	created, err := svc.CreateExactSecret(ctx, &store.PrivacyExactSecretRecord{
		Enabled: true, Name: "短 Token", SecretValue: " " + secret + " ",
		Category: "token", Placeholder: "[Token]", SourceType: "manual",
	})
	if err != nil {
		t.Fatalf("create exact secret failed: %v", err)
	}
	if created.SecretValue != secret {
		t.Fatalf("secret value must be trimmed, got %q", created.SecretValue)
	}
	if _, err := svc.CreateExactSecret(ctx, &store.PrivacyExactSecretRecord{
		Enabled: true, Name: "重复 Token", SecretValue: secret,
		Category: "token", Placeholder: "[Token]", SourceType: "manual",
	}); err != ErrPrivacyExactSecretExists {
		t.Fatalf("duplicate must return ErrPrivacyExactSecretExists, got %v", err)
	}
	masked := MaskPrivacySecretValue(created.SecretValue, created.ValueHash)
	if masked != "••••••••" || strings.Contains(masked, secret[:4]) || strings.Contains(masked, "hash=") {
		t.Fatalf("short secret mask must be opaque and user-facing, got %q", masked)
	}
}

func TestPrivacyServiceExactSecretRejectsTooShortValue(t *testing.T) {
	svc := newTestPrivacyService(t)
	_, err := svc.CreateExactSecret(context.Background(), &store.PrivacyExactSecretRecord{
		Enabled: true, Name: "短密码", SecretValue: "1234567",
		Category: "password", Placeholder: "[密码]", SourceType: "manual",
	})
	if err == nil {
		t.Fatal("short password exact secret must be rejected")
	}
}

func TestPrivacyServiceUpdateRulePreservesPresetSourceWhenInputOmitsSource(t *testing.T) {
	svc := newTestPrivacyService(t)
	ctx := context.Background()

	created, err := svc.ImportPreset(ctx, privacy.PresetAIAPIKeys)
	if err != nil {
		t.Fatalf("import preset failed: %v", err)
	}
	if len(created) == 0 {
		t.Fatal("preset must create rules")
	}
	rule := created[0]
	if rule.Source != privacy.SourcePreset {
		t.Fatalf("imported source = %q, want preset", rule.Source)
	}

	rule.Enabled = false
	rule.Source = "" // 模拟 Wails API 更新输入不携带 source
	updated, err := svc.UpdateRule(ctx, rule)
	if err != nil {
		t.Fatalf("update rule failed: %v", err)
	}
	if updated.Source != privacy.SourcePreset {
		t.Fatalf("source after update = %q, want preset", updated.Source)
	}
}

func TestPrivacyServiceUpdateSettingsValidation(t *testing.T) {
	svc := newTestPrivacyService(t)
	ctx := context.Background()
	settings, err := svc.GetSettings(ctx)
	if err != nil {
		t.Fatalf("get settings failed: %v", err)
	}
	settings.Mode = "invalid-mode"
	if _, err := svc.UpdateSettings(ctx, settings); err == nil {
		t.Fatal("invalid mode must be rejected")
	}
}
