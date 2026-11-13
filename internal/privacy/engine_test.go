package privacy

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func newTestSnapshot(t *testing.T, settings Settings, rules ...Rule) *Snapshot {
	t.Helper()
	compiled, err := CompileRules(rules)
	if err != nil {
		t.Fatalf("compile rules failed: %v", err)
	}
	return &Snapshot{Version: 1, Settings: settings, Rules: compiled, Dict: NewRestoreDict()}
}

func redactSettings() Settings {
	s := DefaultSettings()
	s.Mode = ModeRedact
	return s
}

func detectSettings() Settings {
	s := DefaultSettings()
	s.Mode = ModeDetect
	return s
}

func openAIKeyRule() Rule {
	return Rule{
		ID:          1,
		Enabled:     true,
		Name:        "OpenAI Key",
		Priority:    100,
		MatchType:   MatchTypeRegex,
		Pattern:     `sk-(?:proj-)?[A-Za-z0-9_-]{20,}`,
		Placeholder: "[OpenAI密钥]",
		Action:      ActionRedact,
	}
}

func builtinEmailRule() Rule {
	return Rule{
		ID:          1,
		Enabled:     true,
		Name:        "邮箱地址",
		Priority:    50,
		MatchType:   MatchTypeBuiltin,
		Pattern:     BuiltinEmail,
		Placeholder: "[邮箱]",
		Action:      ActionRedact,
		Source:      SourceBuiltin,
	}
}

func claudeRequest() Request {
	return Request{
		RequestID:    "req-test0001",
		Path:         "/v1/messages",
		Method:       "POST",
		UpstreamType: UpstreamTypeEndpoint,
		EndpointName: "ep-a",
		ContentType:  "application/json",
	}
}

const claudeBodyWithKey = `{"model":"claude-sonnet-4","messages":[{"role":"user","content":"my key is sk-proj-abcdefghijklmnopqrstuvwxyz123456"}],"stream":true}`

func TestApplyDisabledModeReturnsByteIdenticalBody(t *testing.T) {
	snapshot := newTestSnapshot(t, DefaultSettings(), openAIKeyRule())
	body := []byte(claudeBodyWithKey)
	result, err := snapshot.Apply(claudeRequest(), body)
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if !bytes.Equal(result.Body, body) {
		t.Error("disabled mode must return byte-identical body")
	}
	if result.Action != ModeDisabled || result.Changed || result.HitCount != 0 {
		t.Errorf("unexpected result: %+v", result)
	}
}

func TestApplyNilSnapshotReturnsOriginalBody(t *testing.T) {
	var snapshot *Snapshot
	body := []byte(claudeBodyWithKey)
	result, err := snapshot.Apply(claudeRequest(), body)
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if !bytes.Equal(result.Body, body) {
		t.Error("nil snapshot must return original body")
	}
}

func TestApplyDetectModeCountsButDoesNotModify(t *testing.T) {
	snapshot := newTestSnapshot(t, detectSettings(), openAIKeyRule())
	body := []byte(claudeBodyWithKey)
	result, err := snapshot.Apply(claudeRequest(), body)
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if !bytes.Equal(result.Body, body) {
		t.Error("detect mode must return byte-identical body even on hits")
	}
	if result.HitCount != 1 || result.Changed {
		t.Errorf("expected 1 hit without change, got %+v", result)
	}
	if len(result.RuleHits) != 1 || result.RuleHits[0].RuleName != "OpenAI Key" {
		t.Errorf("unexpected rule hits: %+v", result.RuleHits)
	}
	if len(result.RuleHits[0].Matches) != 1 || result.RuleHits[0].Matches[0] != "sk-proj-abcdefghijklmnopqrstuvwxyz123456" {
		t.Errorf("unexpected debug matches: %+v", result.RuleHits[0].Matches)
	}
}

func TestApplyRedactModeZeroHitByteIdentical(t *testing.T) {
	snapshot := newTestSnapshot(t, redactSettings(), openAIKeyRule())
	body := []byte(`{"model":"claude-sonnet-4","messages":[{"role":"user","content":"no secrets here"}]}`)
	result, err := snapshot.Apply(claudeRequest(), body)
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if !bytes.Equal(result.Body, body) {
		t.Error("zero-hit redact must return byte-identical body")
	}
	if result.Changed {
		t.Error("Changed must be false on zero hits")
	}
}

func TestApplyRedactModeReplacesHitOnly(t *testing.T) {
	snapshot := newTestSnapshot(t, redactSettings(), openAIKeyRule())
	body := []byte(claudeBodyWithKey)
	result, err := snapshot.Apply(claudeRequest(), body)
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if !result.Changed || result.HitCount != 1 {
		t.Fatalf("expected redact change, got %+v", result)
	}
	var decoded map[string]any
	if err := json.Unmarshal(result.Body, &decoded); err != nil {
		t.Fatalf("redacted body invalid json: %v", err)
	}
	content := decoded["messages"].([]any)[0].(map[string]any)["content"].(string)
	if content != "my key is [OpenAI密钥]" {
		t.Errorf("content = %q", content)
	}
	// 未扫描字段不被改写
	if decoded["model"].(string) != "claude-sonnet-4" {
		t.Error("model field was modified")
	}
	if decoded["stream"].(bool) != true {
		t.Error("stream field was modified")
	}
}

func TestApplyRedactCoversClaudeToolResult(t *testing.T) {
	snapshot := newTestSnapshot(t, redactSettings(), openAIKeyRule())
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"env has sk-proj-abcdefghijklmnopqrstuvwxyz123456"}]}]}`)
	result, err := snapshot.Apply(claudeRequest(), body)
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if !result.Changed {
		t.Fatal("tool_result hit must be redacted")
	}
	if !bytes.Contains(result.Body, []byte("[OpenAI密钥]")) {
		t.Errorf("placeholder missing: %s", result.Body)
	}
}

func TestApplyRedactCoversCodexFunctionCallOutput(t *testing.T) {
	snapshot := newTestSnapshot(t, redactSettings(), openAIKeyRule())
	req := claudeRequest()
	req.Path = "/v1/responses"
	body := []byte(`{"model":"gpt-5.4","input":[{"type":"function_call_output","call_id":"c1","output":"token sk-proj-abcdefghijklmnopqrstuvwxyz123456 leaked"}]}`)
	result, err := snapshot.Apply(req, body)
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if !result.Changed {
		t.Fatal("function_call_output hit must be redacted")
	}
	var decoded map[string]any
	if err := json.Unmarshal(result.Body, &decoded); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	output := decoded["input"].([]any)[0].(map[string]any)["output"].(string)
	if output != "token [OpenAI密钥] leaked" {
		t.Errorf("output = %q", output)
	}
	if decoded["model"].(string) != "gpt-5.4" {
		t.Error("model was modified")
	}
}

func TestApplyLiteralRule(t *testing.T) {
	rule := Rule{
		ID: 2, Enabled: true, Name: "项目代号", Priority: 50,
		MatchType: MatchTypeLiteral, Pattern: "ProjectPhoenix",
		Placeholder: "[项目]", Action: ActionRedact,
	}
	snapshot := newTestSnapshot(t, redactSettings(), rule)
	body := []byte(`{"messages":[{"role":"user","content":"ProjectPhoenix and ProjectPhoenix again"}]}`)
	result, err := snapshot.Apply(claudeRequest(), body)
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if result.HitCount != 2 {
		t.Errorf("hit count = %d, want 2", result.HitCount)
	}
	if !bytes.Contains(result.Body, []byte("[项目] and [项目] again")) {
		t.Errorf("body = %s", result.Body)
	}
}

func TestApplyScopeEndpointOnlyAffectsTargetEndpoint(t *testing.T) {
	rule := openAIKeyRule()
	rule.Scope = Scope{EndpointNames: []string{"ep-a"}}
	snapshot := newTestSnapshot(t, redactSettings(), rule)

	reqA := claudeRequest()
	resultA, err := snapshot.Apply(reqA, []byte(claudeBodyWithKey))
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if !resultA.Changed {
		t.Error("scoped endpoint ep-a must be redacted")
	}

	reqB := claudeRequest()
	reqB.EndpointName = "ep-b"
	body := []byte(claudeBodyWithKey)
	resultB, err := snapshot.Apply(reqB, body)
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if resultB.Changed || !bytes.Equal(resultB.Body, body) {
		t.Error("non-target endpoint must be untouched")
	}
}

func TestApplyScopeAccountOnlyAffectsTargetAccount(t *testing.T) {
	rule := openAIKeyRule()
	rule.Scope = Scope{UpstreamTypes: []string{UpstreamTypeAccount}, AccountIDs: []int64{7}}
	snapshot := newTestSnapshot(t, redactSettings(), rule)

	req := Request{Path: "/v1/responses", UpstreamType: UpstreamTypeAccount, AccountID: 7, ProviderType: "api_key"}
	body := []byte(`{"input":"key sk-proj-abcdefghijklmnopqrstuvwxyz123456"}`)
	result, err := snapshot.Apply(req, body)
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if !result.Changed {
		t.Error("account 7 must be redacted")
	}

	reqOther := req
	reqOther.AccountID = 8
	resultOther, err := snapshot.Apply(reqOther, body)
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if resultOther.Changed {
		t.Error("account 8 must be untouched")
	}
}

func TestApplyPriorityOverlapHigherPriorityWins(t *testing.T) {
	anthropic := Rule{
		ID: 1, Enabled: true, Name: "Anthropic", Priority: 100,
		MatchType: MatchTypeRegex, Pattern: `sk-ant-[A-Za-z0-9_-]{20,}`,
		Placeholder: "[Anthropic密钥]", Action: ActionRedact,
	}
	openai := Rule{
		ID: 2, Enabled: true, Name: "OpenAI", Priority: 110,
		MatchType: MatchTypeRegex, Pattern: `sk-(?:proj-)?[A-Za-z0-9_-]{20,}`,
		Placeholder: "[OpenAI密钥]", Action: ActionRedact,
	}
	snapshot := newTestSnapshot(t, redactSettings(), openai, anthropic)
	body := []byte(`{"messages":[{"role":"user","content":"sk-ant-abcdefghijklmnopqrstuvwxyz"}]}`)
	result, err := snapshot.Apply(claudeRequest(), body)
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if !bytes.Contains(result.Body, []byte("[Anthropic密钥]")) {
		t.Errorf("higher priority rule must win: %s", result.Body)
	}
	if bytes.Contains(result.Body, []byte("[OpenAI密钥]")) {
		t.Errorf("overlapping lower priority hit must be dropped: %s", result.Body)
	}
	if result.HitCount != 1 {
		t.Errorf("hit count = %d, want 1", result.HitCount)
	}
}

func TestApplyDetectActionRuleNeverModifiesInRedactMode(t *testing.T) {
	rule := openAIKeyRule()
	rule.Action = ActionDetect
	rule.Placeholder = ""
	snapshot := newTestSnapshot(t, redactSettings(), rule)
	body := []byte(claudeBodyWithKey)
	result, err := snapshot.Apply(claudeRequest(), body)
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if result.Changed || !bytes.Equal(result.Body, body) {
		t.Error("detect-action rule must not modify body")
	}
	if result.HitCount != 1 {
		t.Errorf("hit count = %d, want 1", result.HitCount)
	}
}

func TestApplyScanPrefixTruncation(t *testing.T) {
	settings := redactSettings()
	settings.ScanMaxBytes = 64
	snapshot := newTestSnapshot(t, settings, openAIKeyRule())

	padding := strings.Repeat("a", 200)
	body := []byte(`{"messages":[{"role":"user","content":"` + padding + ` sk-proj-abcdefghijklmnopqrstuvwxyz123456"}]}`)
	result, err := snapshot.Apply(claudeRequest(), body)
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if !result.Truncated || result.SkippedReason != SkippedScanTruncated {
		t.Errorf("expected truncation flags, got %+v", result)
	}
	// 命中在截断点之后，不应被替换
	if result.Changed {
		t.Error("hit beyond scan budget must not be redacted")
	}
	if !bytes.Equal(result.Body, body) {
		t.Error("truncated scan must passthrough original body")
	}
}

func TestApplyOverLimitFailClosedReturnsPolicyError(t *testing.T) {
	settings := redactSettings()
	settings.ScanMaxBytes = 16
	settings.OverLimitAction = OverLimitFailClosed
	snapshot := newTestSnapshot(t, settings, openAIKeyRule())

	body := []byte(`{"messages":[{"role":"user","content":"` + strings.Repeat("a", 64) + `"}]}`)
	_, err := snapshot.Apply(claudeRequest(), body)
	var policyErr *PolicyError
	if !errors.As(err, &policyErr) {
		t.Fatalf("expected PolicyError, got %v", err)
	}
	if policyErr.StatusCode != 413 || policyErr.Code != CodeScanBodyTooLarge {
		t.Errorf("unexpected policy error: %+v", policyErr)
	}
}

func TestApplyInvalidJSONFailOpenPassthrough(t *testing.T) {
	snapshot := newTestSnapshot(t, redactSettings(), openAIKeyRule())
	body := []byte(`{"messages": [ broken`)
	result, err := snapshot.Apply(claudeRequest(), body)
	if err != nil {
		t.Fatalf("fail_open must not return error, got %v", err)
	}
	if !bytes.Equal(result.Body, body) {
		t.Error("fail_open must passthrough original body")
	}
	if result.SkippedReason != SkippedFailOpen || !result.FailOpen {
		t.Errorf("skipped reason = %q", result.SkippedReason)
	}
}

func TestApplyInvalidJSONFailClosedReturnsPolicyError(t *testing.T) {
	settings := redactSettings()
	settings.OnError = OnErrorFailClosed
	snapshot := newTestSnapshot(t, settings, openAIKeyRule())
	_, err := snapshot.Apply(claudeRequest(), []byte(`{"messages": [ broken`))
	var policyErr *PolicyError
	if !errors.As(err, &policyErr) {
		t.Fatalf("expected PolicyError, got %v", err)
	}
	if policyErr.StatusCode != 422 || policyErr.Code != CodeScanFailed {
		t.Errorf("unexpected policy error: %+v", policyErr)
	}
}

func TestApplyUnsupportedPathSkipped(t *testing.T) {
	snapshot := newTestSnapshot(t, redactSettings(), openAIKeyRule())
	req := claudeRequest()
	req.Path = "/v1/models"
	body := []byte(`{"data": "sk-proj-abcdefghijklmnopqrstuvwxyz123456"}`)
	result, err := snapshot.Apply(req, body)
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if result.SkippedReason != SkippedUnsupportedPath {
		t.Errorf("skipped reason = %q", result.SkippedReason)
	}
	if !bytes.Equal(result.Body, body) {
		t.Error("unsupported path must passthrough")
	}
}

func TestApplyPlainTextBodyScanned(t *testing.T) {
	snapshot := newTestSnapshot(t, redactSettings(), openAIKeyRule())
	req := claudeRequest()
	req.Path = "/custom/echo"
	req.ContentType = "text/plain; charset=utf-8"
	body := []byte("raw sk-proj-abcdefghijklmnopqrstuvwxyz123456 text")
	result, err := snapshot.Apply(req, body)
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if string(result.Body) != "raw [OpenAI密钥] text" {
		t.Errorf("body = %s", result.Body)
	}
}

func TestApplyNonTextBodySkipped(t *testing.T) {
	snapshot := newTestSnapshot(t, redactSettings(), openAIKeyRule())
	req := claudeRequest()
	req.Path = "/upload"
	req.ContentType = "application/octet-stream"
	body := []byte{0x01, 0x02, 0x03}
	result, err := snapshot.Apply(req, body)
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if result.SkippedReason != SkippedNonTextBody {
		t.Errorf("skipped reason = %q", result.SkippedReason)
	}
}

func TestCompileRulesInvalidRegexFails(t *testing.T) {
	rule := openAIKeyRule()
	rule.Pattern = `sk-(unclosed`
	if _, err := CompileRules([]Rule{rule}); err == nil {
		t.Fatal("expected compile error for invalid regex")
	}
}

func TestValidateRuleRejectsMissingFields(t *testing.T) {
	cases := []Rule{
		{Name: "", Pattern: "x", MatchType: MatchTypeLiteral, Action: ActionDetect},
		{Name: "n", Pattern: "", MatchType: MatchTypeLiteral, Action: ActionDetect},
		{Name: "n", Pattern: "x", MatchType: "glob", Action: ActionDetect},
		{Name: "n", Pattern: "x", MatchType: MatchTypeLiteral, Action: "block"},
		{Name: "n", Pattern: "x", MatchType: MatchTypeLiteral, Action: ActionRedact, Placeholder: ""},
	}
	for i, rule := range cases {
		if err := ValidateRule(rule); err == nil {
			t.Errorf("case %d: expected validation error", i)
		}
	}
}

func TestPresetsCompile(t *testing.T) {
	for _, preset := range Presets() {
		if _, err := CompileRules(preset.Rules); err != nil {
			t.Errorf("preset %s failed to compile: %v", preset.ID, err)
		}
	}
}

func TestRegexNamedRedactGroupOnlyReplacesCapturedValue(t *testing.T) {
	rule := Rule{
		ID:          1,
		Enabled:     true,
		Name:        "Password",
		Priority:    100,
		MatchType:   MatchTypeRegex,
		Pattern:     `(?i)\bpassword\b\s*=\s*"(?P<redact>[^"]+)"`,
		Placeholder: "[密码]",
		Action:      ActionRedact,
	}
	snapshot := newTestSnapshot(t, redactSettings(), rule)

	result := snapshot.ApplyToText(claudeRequest(), `password = "SuperSecret123"`)
	if string(result.Body) != `password = "[密码]"` {
		t.Fatalf("body = %q", result.Body)
	}
}

func TestBasicPrivacyPresetCoversCorePII(t *testing.T) {
	preset, ok := FindPreset(PresetBasicPrivacy)
	if !ok {
		t.Fatal("preset not found")
	}
	rules := make([]Rule, len(preset.Rules))
	copy(rules, preset.Rules)
	for i := range rules {
		rules[i].ID = int64(i + 1)
	}
	snapshot := newTestSnapshot(t, redactSettings(), rules...)

	text := "手机号 13812345678 身份证 11010519491231002X 银行卡 4111111111111111"
	result := snapshot.ApplyToText(claudeRequest(), text)
	for _, want := range []string{"[手机号]", "[身份证]", "[银行卡]"} {
		if !bytes.Contains(result.Body, []byte(want)) {
			t.Errorf("expected %s in result: %s", want, result.Body)
		}
	}
}

func TestBuiltinPIIAvoidsCommonFalsePositives(t *testing.T) {
	rules := BuiltinPIIRules()
	for i := range rules {
		rules[i].ID = int64(i + 1)
	}
	snapshot := newTestSnapshot(t, redactSettings(), rules...)

	text := strings.Join([]string{
		"git@github.com:org/repo.git",
		"ssh user@example.com",
		"scp user@example.com:path",
		"身份证 110105194912310021",
		"身份证 " + "991105" + "19491231" + "0024",
		"手机号 14112345678",
		"手机号 16012345678",
		"手机号 17406123456",
		"银行卡 4111111111111112",
		"go pseudo version v0.0.0-" + "20240101" + "000030" + "-715c2860da7e",
		"epoch millis " + "170000" + "0000004",
		"IPv4 192.168.1.100",
		`AccessToken: cached.accessToken`,
		`RefreshToken: firstNonEmptyString(...)`,
		`password = "SuperSecret123"`,
		`sk-proj-abcdefghijklmnopqrstuvwxyz123456`,
	}, "\n")
	result := snapshot.ApplyToText(claudeRequest(), text)
	if result.HitCount != 0 || result.Changed {
		t.Fatalf("default builtin rules must avoid false positives, got %+v body=%s", result, result.Body)
	}
}

func TestBuiltinCNMobileUsesMainlandMobileSegments(t *testing.T) {
	rules := BuiltinPIIRules()
	for i := range rules {
		rules[i].ID = int64(i + 1)
	}
	snapshot := newTestSnapshot(t, redactSettings(), rules...)

	specialSegment := "1740" + "0123456"
	normalSegment := "166" + "12345678"
	invalidSegment := "141" + "12345678"
	result := snapshot.ApplyToText(claudeRequest(), "mobile "+specialSegment+" "+normalSegment+" "+invalidSegment)

	if result.HitCount != 2 {
		t.Fatalf("expected two valid mobile segments to hit, got %+v body=%s", result, result.Body)
	}
	if bytes.Count(result.Body, []byte("[手机号]")) != 2 {
		t.Fatalf("expected two mobile placeholders in result: %s", result.Body)
	}
	if !bytes.Contains(result.Body, []byte(invalidSegment)) {
		t.Fatalf("invalid mainland mobile segment must stay visible: %s", result.Body)
	}
}

func TestBuiltinPIIValidatesIDCardAndBankCard(t *testing.T) {
	rules := BuiltinPIIRules()
	for i := range rules {
		rules[i].ID = int64(i + 1)
	}
	snapshot := newTestSnapshot(t, redactSettings(), rules...)

	result := snapshot.ApplyToText(claudeRequest(), "身份证 11010519491231002X 银行卡 4111111111111111 邮箱 a@example.com")
	for _, want := range []string{"[身份证]", "[银行卡]"} {
		if !bytes.Contains(result.Body, []byte(want)) {
			t.Fatalf("expected %s in result: %s", want, result.Body)
		}
	}
	if bytes.Contains(result.Body, []byte("[邮箱]")) || !bytes.Contains(result.Body, []byte("a@example.com")) {
		t.Fatalf("builtin PII rules must not include email by default: %s", result.Body)
	}
}

func TestBuiltinBankCardRequiresKnownIssuerProfile(t *testing.T) {
	rules := BuiltinPIIRules()
	for i := range rules {
		rules[i].ID = int64(i + 1)
	}
	snapshot := newTestSnapshot(t, redactSettings(), rules...)

	visaTestNumber := "4111" + "1111" + "1111" + "1111"
	luhnTimestamp := "20240101" + "000030"
	result := snapshot.ApplyToText(claudeRequest(), "card "+visaTestNumber+"\nversion "+luhnTimestamp)

	if result.HitCount != 1 {
		t.Fatalf("expected only known issuer profile to hit, got %+v body=%s", result, result.Body)
	}
	if !bytes.Contains(result.Body, []byte("[银行卡]")) {
		t.Fatalf("expected card placeholder in result: %s", result.Body)
	}
	if !bytes.Contains(result.Body, []byte(luhnTimestamp)) {
		t.Fatalf("unknown-prefix Luhn timestamp must stay visible: %s", result.Body)
	}
}

func TestBuiltinEmailKeepsOrdinaryAddressBeforeColon(t *testing.T) {
	snapshot := newTestSnapshot(t, redactSettings(), builtinEmailRule())

	result := snapshot.ApplyToText(claudeRequest(), "contact alice@example.com: ready")
	if !bytes.Contains(result.Body, []byte("[邮箱]: ready")) {
		t.Fatalf("ordinary email before colon should be redacted: %s", result.Body)
	}
}

func TestBuiltinEmailIgnoresImageScaleAssetNames(t *testing.T) {
	snapshot := newTestSnapshot(t, redactSettings(), builtinEmailRule())

	text := "screenshots 20.58.51@2x.png avatar@3x.webp contact alice@example.com"
	result := snapshot.ApplyToText(claudeRequest(), text)

	if result.HitCount != 1 {
		t.Fatalf("expected only the real email to hit, got %+v body=%s", result, result.Body)
	}
	for _, want := range []string{"20.58.51@2x.png", "avatar@3x.webp", "contact [邮箱]"} {
		if !bytes.Contains(result.Body, []byte(want)) {
			t.Fatalf("expected %q to remain in result: %s", want, result.Body)
		}
	}
}

func TestAIAPIKeysPresetCoversPasswordAndTokens(t *testing.T) {
	preset, ok := FindPreset(PresetAIAPIKeys)
	if !ok {
		t.Fatal("preset not found")
	}
	rules := make([]Rule, len(preset.Rules))
	copy(rules, preset.Rules)
	for i := range rules {
		rules[i].ID = int64(i + 1)
	}
	snapshot := newTestSnapshot(t, redactSettings(), rules...)

	text := `password = "SuperSecret123"
密码：中文Secret123456
api_key=abcdefghijklmnopqrstuvwxyz123456
amap.web-service-key=0123456789abcdef0123456789abcdef
Authorization: Bearer abcdefghijklmnopqrstuvwxyz1234567890
DATABASE_URL=postgres://admin:secret@localhost:5432/app`
	result := snapshot.ApplyToText(claudeRequest(), text)
	for _, want := range []string{"[密码]", "[密钥]", "[高德地图密钥]", "[Bearer令牌]", "[含凭据连接串]"} {
		if !bytes.Contains(result.Body, []byte(want)) {
			t.Errorf("expected %s in result: %s", want, result.Body)
		}
	}
	for _, want := range []string{
		`password = "[密码]"`,
		`密码：[密码]`,
		`api_key=[密钥]`,
		`amap.web-service-key=[高德地图密钥]`,
		`Authorization: Bearer [Bearer令牌]`,
	} {
		if !bytes.Contains(result.Body, []byte(want)) {
			t.Errorf("expected value-only replacement %q in result: %s", want, result.Body)
		}
	}
}

func TestPresetAnthropicBeatsOpenAIKey(t *testing.T) {
	preset, ok := FindPreset(PresetAIAPIKeys)
	if !ok {
		t.Fatal("preset not found")
	}
	rules := make([]Rule, len(preset.Rules))
	copy(rules, preset.Rules)
	for i := range rules {
		rules[i].ID = int64(i + 1)
	}
	snapshot := newTestSnapshot(t, redactSettings(), rules...)
	body := []byte(`{"messages":[{"role":"user","content":"sk-ant-api03-abcdefghijklmnopqrstuvwxyz"}]}`)
	result, err := snapshot.Apply(claudeRequest(), body)
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if !bytes.Contains(result.Body, []byte("[Anthropic密钥]")) {
		t.Errorf("expected anthropic placeholder: %s", result.Body)
	}
}

func TestApplyToTextForTestPanel(t *testing.T) {
	snapshot := newTestSnapshot(t, DefaultSettings(), openAIKeyRule())
	req := claudeRequest()
	result := snapshot.ApplyToText(req, "key sk-proj-abcdefghijklmnopqrstuvwxyz123456 end")
	if result.HitCount != 1 || !result.Changed {
		t.Errorf("unexpected result: %+v", result)
	}
	if string(result.Body) != "key [OpenAI密钥] end" {
		t.Errorf("body = %s", result.Body)
	}
}

func TestScopeMatchSemantics(t *testing.T) {
	scope := Scope{
		Paths:         []string{"/v1/messages"},
		UpstreamTypes: []string{UpstreamTypeEndpoint},
	}
	req := claudeRequest()
	if !scope.Matches(req) {
		t.Error("expected match")
	}
	req.Path = "/v1/responses"
	if scope.Matches(req) {
		t.Error("path mismatch must fail (AND semantics)")
	}
}
