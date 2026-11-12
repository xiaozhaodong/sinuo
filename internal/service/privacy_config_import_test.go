package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"sinuo/internal/privacy"
	"sinuo/internal/privacy/configimport"
	"sinuo/internal/proxy/restorestream"
	"sinuo/internal/store"

	"github.com/tidwall/gjson"
)

func configPreview(t *testing.T, s *PrivacyService, content string) PrivacyConfigPreview {
	t.Helper()
	p, err := s.PreviewConfigImport(context.Background(), []configimport.File{{Name: "synthetic.json", Content: content}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.CancelConfigImport(p.ID) })
	return p
}

func TestPrivacyConfigImportYAMLAndVendorCredentialsRoundTrip(t *testing.T) {
	s := newTestPrivacyService(t)
	ctx := context.Background()
	settings, err := s.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.Mode, settings.ResponseRestoreEnabled = privacy.ModeRedact, true
	if _, err = s.UpdateSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	secrets := []string{
		"  Synthetic+/: secret42  ",
		"SG." + strings.Repeat("a", 22) + "." + strings.Repeat("b", 43),
		"github_pat_" + strings.Repeat("A_", 41),
		"sk-proj-" + strings.Repeat("a", 74) + "T3BlbkFJ" + strings.Repeat("b", 74),
		"sk-ant-api03-" + strings.Repeat("a", 93) + "AA",
		"glpat-" + strings.Repeat("a", 27) + ".ab1234567",
	}
	content := fmt.Sprintf("plain: &value %q\ndefaults: &defaults {password: *value}\ncopy: {<<: *defaults}\nordinary: [%q, %q, %q, %q, %q]\n", secrets[0], secrets[1], secrets[2], secrets[3], secrets[4], secrets[5])
	files := []configimport.File{{Name: "synthetic.yml", Content: content}, {Name: "duplicate.json", Content: fmt.Sprintf(`{"ordinary":%q}`, secrets[1])}}
	preview, err := s.PreviewConfigImport(ctx, files)
	if err != nil || len(suggestedIDs(preview)) != len(secrets) || len(preview.Candidates[0].Sources) != 2 || len(preview.Candidates[1].Sources) != 2 {
		t.Fatal("别名和跨文件凭据应按实际值去重并保留来源", err)
	}
	wire, _ := json.Marshal(preview)
	for _, secret := range secrets {
		if strings.Contains(string(wire), secret) {
			t.Fatal("预览暴露凭据原文")
		}
	}
	before := s.SnapshotVersion()
	if result, err := s.CommitConfigImport(ctx, preview.ID, suggestedIDs(preview)); err != nil || result.Added != len(secrets) || s.SnapshotVersion() != before+1 {
		t.Fatal("导入应整批入库并发布一次快照", err)
	}
	repeat, err := s.PreviewConfigImport(ctx, files)
	if err != nil || len(suggestedIDs(repeat)) != 0 || len(repeat.Candidates) != len(secrets) {
		t.Fatal("再次导入不应重复登记", err)
	}
	s.CancelConfigImport(repeat.ID)
	if err := s.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	records, err := s.ListExactSecrets(ctx)
	if err != nil || len(records) != len(secrets) {
		t.Fatal("重新加载后词典不完整", err)
	}
	for _, secret := range secrets {
		if ok, err := s.ExactSecretExists(ctx, secret); err != nil || !ok {
			t.Fatal("实际值入库不保真", err)
		}
	}
	// 两种协议都验证原值、URL 和 Base64；使用已有响应回填断言。
	var forms []string
	for _, secret := range secrets {
		forms = append(forms, secret, url.QueryEscape(secret), base64.StdEncoding.EncodeToString([]byte(secret)))
	}
	text := strings.Join(forms, " | ")
	for _, path := range []string{"/v1/messages", "/v1/responses"} {
		req := privacy.Request{Path: path, StreamingHTTP: true, UpstreamType: privacy.UpstreamTypeEndpoint}
		var input any = map[string]any{"stream": true, "messages": []map[string]string{{"role": "user", "content": text}}}
		if path == "/v1/responses" {
			req.UpstreamType = privacy.UpstreamTypeAccount
			args, _ := json.Marshal(map[string]string{"password": text})
			input = map[string]any{"stream": true, "input": []map[string]string{{"type": "function_call", "call_id": "synthetic-call", "name": "tool", "arguments": string(args)}}}
		}
		body, _ := json.Marshal(input)
		r, err := s.Apply(ctx, req, body)
		if err != nil || !r.Changed || r.Restore == nil || r.HitCount != len(forms) || len(r.RuleHits) != len(secrets) {
			t.Fatal("导入后未完整保护原值和编码形式", path, err, r.HitCount)
		}
		tokens := regexp.MustCompile(`\{\{SNU_[0-9a-f]{32}\}\}`).FindAllString(string(r.Body), -1)
		if len(tokens) != len(forms) {
			t.Fatal("未逐个遮蔽导入值")
		}
		for i, token := range tokens {
			if value, ok := r.Restore.Lookup(token); !ok || value != forms[i] {
				t.Fatal("回填映射改变实际拼写")
			}
		}
		if err := r.Restore.Publish(); err != nil {
			t.Fatal(err)
		}
		assertEncodedJSONRestore(t, path, strings.Join(tokens, " | "), text, r.Restore)
		r.Restore.Release()
	}
}

func TestPrivacyConfigImportCompileBudgetRollsBackAndCanRetry(t *testing.T) {
	s := newTestPrivacyService(t)
	ctx := context.Background()
	if _, err := s.CreateExactSecret(ctx, &store.PrivacyExactSecretRecord{Enabled: true, Name: "保留原值", SecretValue: "existing-passphrase", Category: "password"}); err != nil {
		t.Fatal(err)
	}
	before := s.CurrentSnapshot()
	values := make([]map[string]string, 25)
	for i := range values {
		values[i] = map[string]string{"password": strings.Repeat("A+/:?&= ࠾࠿z", 1000) + fmt.Sprintf("%04d", i)}
	}
	raw, _ := json.Marshal(values)
	p := configPreview(t, s, string(raw))
	ids := suggestedIDs(p)
	if len(ids) != len(values) {
		t.Fatal("合成配置应生成全部候选", len(ids))
	}
	if _, err := s.CommitConfigImport(ctx, p.ID, ids); !errors.Is(err, privacy.ErrLiteralBudget) {
		t.Fatal("派生词典超限未明确拒绝", err)
	}
	all, err := s.ListExactSecrets(ctx)
	if err != nil || len(all) != 1 || all[0].SecretValue != "existing-passphrase" || s.CurrentSnapshot() != before {
		t.Fatal("编译容量失败留下半写或替换了旧快照", err)
	}
	if s.configImport == nil {
		t.Fatal("失败应保留预览，允许减少选择后重试")
	}
	if r, err := s.CommitConfigImport(ctx, p.ID, ids[:1]); err != nil || r.Added != 1 || s.SnapshotVersion() != before.Version+1 {
		t.Fatal("减少选择后重试失败", r, err)
	}
	if err := s.Initialize(ctx); err != nil {
		t.Fatal("预算内词典重新加载失败", err)
	}
}

func TestPrivacyManualCompileBudgetDoesNotPersistChanges(t *testing.T) {
	s := newTestPrivacyService(t)
	ctx := context.Background()
	r, err := s.CreateExactSecret(ctx, &store.PrivacyExactSecretRecord{Enabled: true, Name: "原值", SecretValue: "existing-passphrase", Category: "password"})
	if err != nil {
		t.Fatal(err)
	}
	before := s.CurrentSnapshot()
	large := &store.PrivacyExactSecretRecord{Enabled: true, Name: "过长值", SecretValue: strings.Repeat("x", 65537), Category: "password"}
	if _, err := s.CreateExactSecret(ctx, large); !errors.Is(err, privacy.ErrLiteralBudget) {
		t.Fatal("手动新增未在写库前检查容量", err)
	}
	if _, err := s.UpdateExactSecret(ctx, r.ID, large); !errors.Is(err, privacy.ErrLiteralBudget) {
		t.Fatal("手动编辑未在写库前检查容量", err)
	}
	all, err := s.ListExactSecrets(ctx)
	if err != nil || len(all) != 1 || all[0].SecretValue != r.SecretValue || all[0].Name != r.Name || s.CurrentSnapshot() != before {
		t.Fatal("失败的手动保存改变了数据库或快照", err)
	}
	// 自定义字面量的容量校验也必须计入已有精确值，不能先落库再编译失败。
	rulesBefore, err := s.ListRules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.CreateRule(ctx, &store.PrivacyRuleRecord{Enabled: true, Name: "超预算组合", MatchType: privacy.MatchTypeLiteral, Pattern: strings.Repeat("x", 8<<20), Placeholder: "[MASK]", Action: privacy.ActionRedact})
	if !errors.Is(err, privacy.ErrLiteralBudget) {
		t.Fatal("普通规则未计入现有精确词典的容量", err)
	}
	rulesAfter, err := s.ListRules(ctx)
	if err != nil || len(rulesAfter) != len(rulesBefore) || s.CurrentSnapshot() != before {
		t.Fatal("容量失败的普通规则被写入", err)
	}
	// 未启用的大值可以保留，但启用失败不能悄悄改写 enabled。
	large.Enabled = false
	disabled, err := s.CreateExactSecret(ctx, large)
	if err != nil {
		t.Fatal(err)
	}
	before = s.CurrentSnapshot()
	large.Enabled = true
	if _, err = s.UpdateExactSecret(ctx, disabled.ID, large); !errors.Is(err, privacy.ErrLiteralBudget) {
		t.Fatal("启用时未检查容量", err)
	}
	stored, err := s.GetExactSecret(ctx, disabled.ID)
	if err != nil || stored.Enabled || s.CurrentSnapshot() != before {
		t.Fatal("失败的启用操作已生效", err)
	}
}

func TestPrivacyOversizedLegacyDictionaryBlocksUntilRepaired(t *testing.T) {
	s := newTestPrivacyService(t)
	ctx := context.Background()
	enableRedactMode(t, s)
	// 模拟旧版本或离线脚本写入的过长值；新保存入口已在写库前阻止。
	for i := range 2 {
		value := strings.Repeat("x", 65537) + fmt.Sprint(i)
		if _, err := s.store.CreateExactSecret(ctx, &store.PrivacyExactSecretRecord{Enabled: true, Name: "历史值", SecretValue: value, ValueHash: HashPrivacySecretValue(value), Category: "password", Placeholder: "[MASK]"}); err != nil {
			t.Fatal(err)
		}
	}
	restarted := NewPrivacyService(s.store)
	if err := restarted.Initialize(ctx); err != nil || !restarted.CurrentSnapshot().CompileBlocked || restarted.Status() != PrivacyStatusDegraded {
		t.Fatal("旧库超预算应保留服务管理入口并显式降级", err)
	}
	assertBlocked := func() {
		t.Helper()
		_, err := restarted.Apply(ctx, privacy.Request{Path: "/v1/messages"}, []byte(`{"messages":[{"role":"user","content":"sample"}]}`))
		var policy *privacy.PolicyError
		if !errors.As(err, &policy) || policy.StatusCode != 503 || policy.Code != privacy.CodeScanFailed {
			t.Fatal("超限的旧词典不能在开启隐私时透传请求", err)
		}
	}
	assertBlocked()
	settings, err := restarted.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.Mode = privacy.ModeDisabled
	if _, err := restarted.UpdateSettings(ctx, settings); err != nil {
		t.Fatal("不能显式关闭隐私模式", err)
	}
	if _, err := restarted.Apply(ctx, privacy.Request{}, []byte("sample")); err != nil {
		t.Fatal("用户显式关闭隐私后仍被阻止", err)
	}
	settings.Mode = privacy.ModeRedact
	if _, err := restarted.UpdateSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	assertBlocked()
	all, err := restarted.ListExactSecrets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i, record := range all {
		record.Enabled = false
		if _, err := restarted.UpdateExactSecret(ctx, record.ID, record); err != nil {
			t.Fatal("无法逐条停用超限值", err)
		}
		if i == 0 {
			assertBlocked()
		}
	}
	if restarted.CurrentSnapshot().CompileBlocked || restarted.Status() != PrivacyStatusOK {
		t.Fatal("删减到预算内后未恢复完整编译")
	}
	if _, err := restarted.Apply(ctx, privacy.Request{Path: "/v1/messages"}, []byte(`{"messages":[]}`)); err != nil {
		t.Fatal("修复词典后仍无法使用", err)
	}
}

func TestPrivacyOversizedLegacyCustomRuleRemainsInCandidateBudget(t *testing.T) {
	s := newTestPrivacyService(t)
	ctx := context.Background()
	enableRedactMode(t, s)
	legacy, err := s.store.CreateRule(ctx, &store.PrivacyRuleRecord{Enabled: true, Name: "历史大字面量", MatchType: privacy.MatchTypeLiteral, Pattern: strings.Repeat("x", (8<<20)+1), Placeholder: "[MASK]", Action: privacy.ActionRedact})
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewPrivacyService(s.store)
	if err := restarted.Initialize(ctx); err != nil || !restarted.CurrentSnapshot().CompileBlocked {
		t.Fatal("旧普通规则超限不应被悄悄跳过", err)
	}
	before := restarted.CurrentSnapshot()
	rulesBefore, err := restarted.ListRules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	candidate := &store.PrivacyRuleRecord{Enabled: true, Name: "新字面量", MatchType: privacy.MatchTypeLiteral, Pattern: "new-literal-value", Placeholder: "[MASK]", Action: privacy.ActionRedact}
	if _, err := restarted.CreateRule(ctx, candidate); !errors.Is(err, privacy.ErrLiteralBudget) {
		t.Fatal("候选预校验跳过了旧的超预算普通规则", err)
	}
	if _, err := restarted.CreateExactSecret(ctx, &store.PrivacyExactSecretRecord{Enabled: true, Name: "新敏感值", SecretValue: "new-secret-value", Category: "password"}); !errors.Is(err, privacy.ErrLiteralBudget) {
		t.Fatal("精确值预校验跳过了旧的超预算普通规则", err)
	}
	rulesAfter, err := restarted.ListRules(ctx)
	if err != nil || len(rulesAfter) != len(rulesBefore) || restarted.CurrentSnapshot() != before {
		t.Fatal("超限时仍写入了新规则", err)
	}
	legacy.Enabled = false
	if _, err := restarted.UpdateRule(ctx, legacy); err != nil || restarted.CurrentSnapshot().CompileBlocked {
		t.Fatal("无法停用旧普通规则恢复服务", err)
	}
	if _, err := restarted.CreateRule(ctx, candidate); err != nil {
		t.Fatal("修复旧词典后新增仍失败", err)
	}
}

func TestPrivacyConfigPreviewURLClassificationAndAcronyms(t *testing.T) {
	s := newTestPrivacyService(t)
	for _, tc := range []struct {
		input, status string
		selected      bool
	}{
		{`{"password":"password123"}`, "review", false},
		{`{"url":"postgres://u:password123@h/db"}`, "review", false},
		{`{"url":"postgres://h/db?password=password123"}`, "review", false},
		{`{"password":"${DB_PASSWORD}"}`, "excluded", false},
		{`{"url":"postgres://u:%24%7BDB_PASSWORD%7D@h/db"}`, "excluded", false},
		{`{"url":"postgres://h/db?password=%24%7BDB_PASSWORD%7D"}`, "excluded", false},
		{`{"APIKey":"synthetic-credential-123456"}`, "new", true},
	} {
		p := configPreview(t, s, tc.input)
		if len(p.Candidates) == 0 || p.Candidates[0].Status != tc.status || p.Candidates[0].Selected != tc.selected {
			t.Fatal("服务预览候选判断错误", tc.input)
		}
	}
}

func suggestedIDs(p PrivacyConfigPreview) []string {
	var ids []string
	for _, c := range p.Candidates {
		if c.Selected {
			ids = append(ids, c.ID)
		}
	}
	return ids
}

func TestPrivacyConfigImportPreviewCommitAndRepeat(t *testing.T) {
	s := newTestPrivacyService(t)
	ctx := context.Background()
	existing, err := s.CreateExactSecret(ctx, &store.PrivacyExactSecretRecord{Enabled: false, Name: "手动停用", SecretValue: "existing-passphrase", Category: "password"})
	if err != nil {
		t.Fatal(err)
	}
	before := s.SnapshotVersion()
	const content = `{"password":"new-passphrase","nested":{"password":"new-passphrase"},"old_password":"existing-passphrase","short_password":"short","username":"admin","host":"10.0.0.8","reference_token":"${ACCESS_TOKEN}"}`
	p := configPreview(t, s, content)
	if len(p.Candidates) != 6 || len(suggestedIDs(p)) != 1 || len(p.Candidates[0].Sources) != 2 {
		t.Fatal(p)
	}
	wire, _ := json.Marshal(p)
	for _, secret := range []string{"new-passphrase", "existing-passphrase", "10.0.0.8", "${ACCESS_TOKEN}"} {
		if strings.Contains(string(wire), secret) {
			t.Fatal("预览泄露原文", secret)
		}
	}
	all, err := s.ListExactSecrets(ctx)
	if err != nil || len(all) != 1 || s.SnapshotVersion() != before {
		t.Fatal("预览不应写入或发布")
	}
	if _, err = s.CommitConfigImport(ctx, p.ID, []string{"not-a-candidate"}); err == nil {
		t.Fatal("接受伪造候选")
	}
	r, err := s.CommitConfigImport(ctx, p.ID, suggestedIDs(p))
	if err != nil || r.Added != 1 || r.Skipped != 0 || s.SnapshotVersion() != before+1 {
		t.Fatal(r, err)
	}
	if s.configImport != nil {
		t.Fatal("确认后未释放")
	}
	saved, err := s.GetExactSecret(ctx, existing.ID)
	if err != nil || saved.Enabled || saved.Name != "手动停用" {
		t.Fatal(saved, err)
	}
	if _, err = s.CommitConfigImport(ctx, p.ID, suggestedIDs(p)); err == nil {
		t.Fatal("接受重复提交")
	}
	p = configPreview(t, s, content)
	if len(suggestedIDs(p)) != 0 {
		t.Fatal("重复导入应不预选已有值", p)
	}
	// 新旧密码并存，不因同字段轮换删除历史值。
	p = configPreview(t, s, `{"password":"rotated-passphrase"}`)
	if _, err = s.CommitConfigImport(ctx, p.ID, suggestedIDs(p)); err != nil {
		t.Fatal(err)
	}
	all, err = s.ListExactSecrets(ctx)
	if err != nil || len(all) != 3 {
		t.Fatal(all, err)
	}
	if err = s.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if !s.TestText(privacy.Request{Path: "/v1/messages"}, "new-passphrase rotated-passphrase").Changed {
		t.Fatal("重启加载后未生效")
	}
}

func TestPrivacyConfigImportCancellationExpiryAndConcurrentChanges(t *testing.T) {
	s := newTestPrivacyService(t)
	ctx := context.Background()
	p := configPreview(t, s, `{"password":"first-passphrase"}`)
	newer := configPreview(t, s, `{"password":"second-passphrase"}`)
	s.CancelConfigImport(p.ID)
	if s.configImport == nil || s.configImport.id != newer.ID {
		t.Fatal("旧窗口清掉了新预览")
	}
	s.CancelConfigImport(newer.ID)
	if _, err := s.CommitConfigImport(ctx, newer.ID, suggestedIDs(newer)); err == nil {
		t.Fatal("取消后仍可提交")
	}
	p = configPreview(t, s, `{"password":"first-passphrase"}`)
	s.mu.Lock()
	s.configImport.expires = time.Now().Add(-time.Second)
	s.mu.Unlock()
	if _, err := s.CommitConfigImport(ctx, p.ID, suggestedIDs(p)); err == nil || s.configImport != nil {
		t.Fatal("过期未释放")
	}
	p = configPreview(t, s, `{"password":"first-passphrase"}`)
	_, err := s.CreateExactSecret(ctx, &store.PrivacyExactSecretRecord{Name: "导入前已添加", SecretValue: "first-passphrase", Enabled: false, Category: "password"})
	if err != nil {
		t.Fatal(err)
	}
	before := s.SnapshotVersion()
	r, err := s.CommitConfigImport(ctx, p.ID, append(suggestedIDs(p), suggestedIDs(p)...))
	if err != nil || r.Added != 0 || r.Skipped != 1 || s.SnapshotVersion() != before {
		t.Fatal(r, err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.CancelConfigImport(p.ID)
			s.TestText(privacy.Request{Path: "/v1/messages"}, "first-passphrase")
		}()
	}
	wg.Wait()
}

func TestPrivacyConfigImportPreservesDecodedValuesAndRestore(t *testing.T) {
	s := newTestPrivacyService(t)
	ctx := context.Background()
	settings, err := s.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.Mode = privacy.ModeRedact
	settings.ResponseRestoreEnabled = true
	if _, err = s.UpdateSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	secrets := []string{"  alpha9 beta:42  ", "abc\\def\"xyz42", "-----BEGIN PRIVATE KEY-----\nYWJjZGVmZ2hpamts\n-----END PRIVATE KEY-----\n"}
	input, _ := json.Marshal(map[string]any{"passwords": []map[string]string{{"password": secrets[0]}, {"password": secrets[1]}, {"private_key": secrets[2]}}})
	p := configPreview(t, s, string(input))
	if len(suggestedIDs(p)) != len(secrets) {
		t.Fatal(p)
	}
	dict := s.CurrentSnapshot().Dict
	if _, err = s.CommitConfigImport(ctx, p.ID, suggestedIDs(p)); err != nil {
		t.Fatal(err)
	}
	if s.CurrentSnapshot().Dict != dict {
		t.Fatal("导入不应清空已有回填字典")
	}
	all, err := s.ListExactSecrets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range secrets {
		found := false
		for _, r := range all {
			if r.SecretValue == secret {
				found = true
			}
		}
		if !found {
			t.Fatal("入库原文被改变")
		}
		if ok, err := s.ExactSecretExists(ctx, secret); err != nil || !ok {
			t.Fatal("按真实值查重失败", err)
		}
		for _, path := range []string{"/v1/messages", "/v1/responses"} {
			var body []byte
			req := privacy.Request{Path: path, StreamingHTTP: true, UpstreamType: "endpoint"}
			if path == "/v1/messages" {
				body, _ = json.Marshal(map[string]any{"stream": true, "messages": []map[string]string{{"role": "user", "content": secret}}})
			} else {
				req.UpstreamType = privacy.UpstreamTypeAccount
				args, _ := json.Marshal(map[string]string{"password": secret})
				body, _ = json.Marshal(map[string]any{"stream": true, "input": []map[string]string{{"type": "function_call", "call_id": "synthetic-call", "name": "tool", "arguments": string(args)}}})
			}
			result, err := s.Apply(ctx, req, body)
			if err != nil || !result.Changed || result.Restore == nil {
				t.Fatal("导入后未保护请求", path, err)
			}
			token := regexp.MustCompile(`\{\{SNU_[0-9a-f]{32}\}\}`).FindString(string(result.Body))
			if restored, ok := result.Restore.Lookup(token); !ok || restored != secret {
				t.Fatal("正文或工具参数回填映射未保真", path)
			}
			var response []byte
			if path == "/v1/messages" {
				response, _ = json.Marshal(map[string]any{"content": []any{map[string]string{"type": "text", "text": token}, map[string]any{"type": "tool_use", "input": map[string]string{"password": token}}}})
			} else {
				args, _ := json.Marshal(map[string]string{"password": token})
				response, _ = json.Marshal(map[string]any{"output": []any{map[string]any{"type": "message", "content": []map[string]string{{"type": "output_text", "text": token}}}, map[string]string{"type": "function_call", "arguments": string(args)}}})
			}
			delivery, err := restorestream.RestoreJSON(path, response, result.Restore)
			if err != nil {
				t.Fatal("响应回填失败", err)
			}
			var textValue, toolValue string
			if path == "/v1/messages" {
				textValue = gjson.GetBytes(delivery.Body, "content.0.text").Str
				toolValue = gjson.GetBytes(delivery.Body, "content.1.input.password").Str
			} else {
				textValue = gjson.GetBytes(delivery.Body, "output.0.content.0.text").Str
				toolValue = gjson.Get(gjson.GetBytes(delivery.Body, "output.1.arguments").Str, "password").Str
			}
			if textValue != secret || toolValue != secret {
				t.Fatal("响应正文或工具参数未按实际值还原", path)
			}
			result.Restore.Release()
		}
	}
}

func TestPrivacyConfigImportStrictestCategoryAndAtomicFailure(t *testing.T) {
	s := newTestPrivacyService(t)
	ctx := context.Background()
	p := configPreview(t, s, `{"username":"short","password":"short"}`)
	if len(p.Candidates) != 1 || p.Candidates[0].Selectable || p.Candidates[0].Category != "password" {
		t.Fatal("短凭据被自定义类别绕过", p)
	}
	p = configPreview(t, s, `{"password":"valid-passphrase"}`)
	before := s.SnapshotVersion()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.CommitConfigImport(cancelled, p.ID, suggestedIDs(p)); err == nil {
		t.Fatal("取消事务仍成功")
	}
	all, err := s.ListExactSecrets(ctx)
	if err != nil || len(all) != 0 || s.SnapshotVersion() != before {
		t.Fatal("失败留下部分状态")
	}
	if _, err = s.CommitConfigImport(ctx, p.ID, suggestedIDs(p)); err != nil {
		t.Fatal("失败后不能重试", err)
	}
}

func BenchmarkPrivacyConfigDictionary(b *testing.B) {
	for _, count := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("entries=%d", count), func(b *testing.B) {
			s := NewPrivacyService(nil)
			s.snapshot.Store(&privacy.Snapshot{Version: 1, Settings: privacy.DefaultSettings()})
			records := make([]*store.PrivacyExactSecretRecord, count)
			for i := range records {
				records[i] = &store.PrivacyExactSecretRecord{ID: int64(i + 1), Enabled: true, Name: "合成值", SecretValue: fmt.Sprintf("synthetic-secret-%08d-abcdefghijklmnop", i)}
			}
			records[0].SecretValue = "-----BEGIN PRIVATE KEY-----\n" + strings.Repeat("YWJjZGVmZ2hpamts\n", 100) + "-----END PRIVATE KEY-----\n"
			b.Run("compile", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if _, err := s.snapshotWithExactSecretsLocked(records); err != nil {
						b.Fatal(err)
					}
				}
			})
			snap, err := s.snapshotWithExactSecretsLocked(records)
			if err != nil {
				b.Fatal(err)
			}
			text := strings.Repeat("ordinary code and prose; ", 700) + records[len(records)-1].SecretValue
			snap.Settings.Mode = privacy.ModeRedact
			body, _ := json.Marshal(map[string]any{"messages": []map[string]string{{"role": "user", "content": text}}})
			for _, cached := range []bool{false, true} {
				b.Run(fmt.Sprintf("scan-cached=%t", cached), func(b *testing.B) {
					if cached {
						snap.Cache = privacy.NewSegmentCache(privacy.DefaultSegmentCacheCapacity)
					} else {
						snap.Cache = nil
					}
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						if _, err := snap.Apply(privacy.Request{Path: "/v1/messages"}, body); err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		})
	}
}
