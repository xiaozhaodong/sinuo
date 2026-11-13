package privacy

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestRestoreStagingAndStableReplay(t *testing.T) {
	rule := openAIKeyRule()
	rule.Pattern = `password=(?P<redact>[a-zA-Z0-9]+)`
	s := newTestSnapshot(t, redactSettings(), rule)
	s.Settings.ResponseRestoreEnabled = true
	s.Settings.OnError = OnErrorFailClosed
	s.RestoreKey = bytes.Repeat([]byte{7}, 32)
	req := claudeRequest()
	req.StreamingHTTP = true
	const secret = "abCD34efGH89JK"
	body := []byte(`{"stream":true,"messages":[{"role":"user","content":"password=` + secret + `"},{"role":"assistant","content":"again ` + secret + `"}]}`)
	result, err := s.Apply(req, body)
	if err != nil {
		t.Fatal(err)
	}
	token := regexp.MustCompile(`\{\{SNU_[0-9a-f]{32}\}\}`).FindString(string(result.Body))
	if token == "" || strings.Count(string(result.Body), token) != 2 || bytes.Contains(result.Body, []byte(secret)) {
		t.Fatalf("重放正文不完整: %s", result.Body)
	}
	if v, ok := result.Restore.Lookup(token); !ok || v != secret {
		t.Fatal("映射与出站正文不匹配")
	}
	req.Path = "/v1/messages/count_tokens"
	req.StreamingHTTP = false
	count, err := s.Apply(req, body)
	if err != nil || !bytes.Equal(count.Body, result.Body) {
		t.Fatal("count_tokens 与实际出站前缀不一致", err)
	}
	other, err := s.Apply(req, bytes.ReplaceAll(body, []byte(secret), []byte("somethingElse987")))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := other.Restore.Lookup(token); ok {
		t.Fatal("从其他请求借用了映射")
	}
	if short, err := s.Apply(req, bytes.ReplaceAll(body, []byte(secret), []byte("123456"))); err != nil || bytes.Contains(short.Body, []byte("123456")) {
		t.Fatal("应遮蔽独立短裸值并允许出站", err)
	}
	s.Settings.ScanMaxBytes = 8
	s.Settings.OverLimitAction = OverLimitFailClosed
	if _, err = s.Apply(req, body); err == nil {
		t.Fatal("接受了不完整扫描")
	}
}

func TestRestoreResponsesReplayAndAdmission(t *testing.T) {
	const secret = "value-with-quote\""
	s := newTestSnapshot(t, redactSettings(), literalRule(1, 1, secret, "[MASK]", ActionRedact))
	s.Settings.ResponseRestoreEnabled = true
	s.Settings.OnError = OnErrorFailClosed
	s.RestoreKey = bytes.Repeat([]byte{2}, 32)
	req := Request{Path: "/v1/responses", UpstreamType: UpstreamTypeAccount, StreamingHTTP: true}
	args, _ := json.Marshal(map[string]any{secret: []string{secret}})
	body, _ := json.Marshal(map[string]any{"stream": true, "input": []any{map[string]any{"type": "function_call", "name": secret, "arguments": string(args)}}})
	res, err := s.Apply(req, body)
	if err != nil || res.Restore == nil {
		t.Fatal("没有内嵌参数映射", err)
	}
	var arg map[string][]string
	if err = json.Unmarshal([]byte(gjson.GetBytes(res.Body, "input.0.arguments").Str), &arg); err != nil {
		t.Fatal(err)
	}
	if len(arg[secret]) != 1 || arg[secret][0] == secret || gjson.GetBytes(res.Body, "input.0.name").Str != secret {
		t.Fatal("嵌套键或工具名被修改", arg)
	}
	if v, ok := res.Restore.Lookup(arg[secret][0]); !ok || v != secret {
		t.Fatal("映射未按解码后的值生成")
	}
	for _, raw := range []string{`{"stream":false}`, `{"stream":true,"previous_response_id":"r"}`, `{"stream":true,"conversation":"c"}`, `{"stream":true,"input":[{"type":"item_reference","id":"i"}]}`, `{"stream":true,"input":[{"type":"reasoning","encrypted_content":"x"}]}`} {
		if _, err := s.Apply(req, []byte(raw)); err != nil {
			t.Fatalf("准入应放行 %s: %v", raw, err)
		}
	}
	req.StreamingHTTP = false
	if _, err := s.Apply(req, []byte(`{"stream":true,"stream":false}`)); err == nil {
		t.Fatal("允许歧义流式标志")
	}
}

func containsRestoreToken(s string) bool { return len(restoreMarkers(s)) != 0 }

var exampleRestoreToken = RestorePrefix + strings.Repeat("0123456789abcdef", 2) + "}}"

func TestReservedRestoreSyntax(t *testing.T) {
	if containsRestoreToken("explain {{SNU_ in a template") || containsRestoreToken("{{SNU_example}}") {
		t.Fatal("普通模板文本不应与机器标识冲突")
	}
	if !containsRestoreToken("fragment {{SNU_ then " + exampleRestoreToken) {
		t.Fatal("遗漏完整机器标识")
	}
}

func TestRestoreWeakValuesAndOpaqueFields(t *testing.T) {
	rule := openAIKeyRule()
	rule.Pattern = `password=(?P<redact>[a-zA-Z0-9]+)`
	short := literalRule(2, 2, "0123", "[x]", ActionRedact)
	s := newTestSnapshot(t, redactSettings(), rule, short)
	s.Settings.ResponseRestoreEnabled = true
	s.Settings.OnError = OnErrorFailClosed
	s.RestoreKey = bytes.Repeat([]byte{1}, 32)
	req := claudeRequest()
	apply := func(text string) (ApplyResult, error) {
		raw, _ := json.Marshal(map[string]any{"messages": []any{map[string]string{"role": "user", "content": text}}})
		return s.Apply(req, raw)
	}
	for _, v := range []string{"123456", "aaaaaaaaaaaaaaaa"} {
		res, err := apply("password=" + v + " " + v + " build_" + v + " " + v + "x " + "x" + v + " " + exampleRestoreToken)
		if err != nil {
			t.Fatal(err)
		}
		got := gjson.GetBytes(res.Body, "messages.0.content").Str
		token := strings.TrimPrefix(strings.Fields(got)[0], "password=")
		if !containsRestoreToken(token) || got != "password="+token+" "+token+" build_"+v+" "+v+"x x"+v+" "+exampleRestoreToken || res.HitCount != 1 {
			t.Fatal("弱值边界或标识内命中错误", got)
		}
	}
	for _, r := range []struct {
		value  string
		strong bool
	}{
		{"abcd0123456", false}, {"abcdef012345", true}, {"abcdefghabcdefgh", true}, {"aaaaaaaabbbbbbbb", false},
	} {
		if expandable(r.value) != r.strong {
			t.Errorf("强值阈值判定错误 %s", r.value)
		}
	}
	adj := literalRule(3, 3, "XYZ", "[x]", ActionRedact)
	s.Rules, _ = CompileRules([]Rule{adj, short})
	res, err := apply(exampleRestoreToken + "XYZ")
	if err != nil || !strings.HasPrefix(gjson.GetBytes(res.Body, "messages.0.content").Str, exampleRestoreToken+RestorePrefix) {
		t.Fatal("标识相邻的秘密未遮蔽", err)
	}
	edge := literalRule(4, 4, "}}XYZ", "[x]", ActionRedact)
	s.Rules, _ = CompileRules([]Rule{edge})
	if _, err := apply(exampleRestoreToken + "XYZ"); err == nil {
		t.Fatal("错误地发送跨占位符边缘的匹配")
	}
	s.Rules, _ = CompileRules([]Rule{rule, short})
	thinking := func(value string) (ApplyResult, error) {
		return s.Apply(req, []byte(`{"messages":[{"role":"assistant","content":[{"signature":"\\u0061","thinking":"`+value+`","type":"thinking"}]}]}`))
	}
	if r, err := thinking(exampleRestoreToken); err != nil || r.Changed {
		t.Fatal("含历史标识的签名块被拒绝或改写")
	}
	if _, err := thinking("password=123456"); err == nil {
		t.Fatal("thinking 中的可读秘密被放行")
	}
}

func TestRestoreCacheBudgetAndStrictJSON(t *testing.T) {
	s := newTestSnapshot(t, redactSettings(), literalRule(1, 1, "fixture-secret", "[x]", ActionRedact))
	s.Settings.ResponseRestoreEnabled = true
	s.Settings.OnError = OnErrorFailClosed
	s.RestoreKey = bytes.Repeat([]byte{1}, 32)
	s.Cache = NewSegmentCache(16)
	req := claudeRequest()
	if _, err := s.Apply(req, []byte(`{"system":"`+strings.Repeat("a", 2<<20)+`"}`)); err != nil {
		t.Fatal(err)
	}
	large := strings.Repeat("a", 2<<20)
	body := []byte(`{"messages":[{"content":"` + large + `"},{"content":"` + large + `"},{"content":"` + large + `"},{"content":"` + strings.Repeat("b", 100<<10) + `"}]}`)
	if _, err := s.Apply(req, body); err != nil {
		t.Fatal("热缓存预算应只包含新增段", err)
	}
	s.Cache = nil
	s.Settings.OverLimitAction = OverLimitFailClosed
	if _, err := s.Apply(req, body); err == nil {
		t.Fatal("冷缓存大正文被放行")
	}
	for _, b := range [][]byte{[]byte(`{"stream":true,"str\u0065am":false}`), []byte(`{"messages":[{"type":"message","type":"x"}]}`), []byte(`{}` + "\n" + `{}`), []byte(`{"x":`), []byte(`[]`), []byte("{\"system\":\"\xff\"}"), []byte(`{"stream":"false"}`)} {
		if _, err := s.Apply(req, b); err == nil {
			t.Fatal("歧义或非法正文被放行")
		}
	}
}

func TestToolResultScannedWithOrWithoutPlainHistory(t *testing.T) {
	rule := openAIKeyRule()
	rule.Pattern = `password=(?P<redact>[a-zA-Z0-9]+)`
	s := newTestSnapshot(t, redactSettings(), rule)
	s.Dict = NewRestoreDict()
	s.Settings.ResponseRestoreEnabled = true
	s.Settings.OnError = OnErrorFailClosed
	s.RestoreKey = bytes.Repeat([]byte{2}, 32)
	req := claudeRequest()
	req.SessionID = "old-session"
	result := `{"role":"user","content":[{"type":"text","text":"继续"},{"type":"tool_result","tool_use_id":"t","content":"syntheticAb12Cd34"}]}`
	standalone := []byte(`{"messages":[` + result + `]}`)
	if r, err := s.Apply(req, standalone); err != nil || r.Changed || !bytes.Equal(standalone, r.Body) {
		t.Fatal("缺少历史时应照常交付规则未识别的工具结果", err)
	}
	raw := `{"messages":[{"role":"user","content":"password=syntheticAb12Cd34"},{"role":"assistant","content":[{"type":"tool_use","id":"t","input":{}}]},` + result + `]}`
	if r, err := s.Apply(req, []byte(raw)); err != nil || bytes.Contains(r.Body, []byte("syntheticAb12Cd34")) {
		t.Fatal("完整明文和工具调用应能重建", err)
	}
}

func TestExistingMarkerInsideEscapedToolJSON(t *testing.T) {
	s := newTestSnapshot(t, redactSettings(), literalRule(1, 1, "0123", "[x]", ActionRedact), literalRule(2, 2, "secret", "[x]", ActionRedact))
	s.Dict = NewRestoreDict()
	s.Settings.ResponseRestoreEnabled = true
	s.Settings.OnError = OnErrorFailClosed
	s.RestoreKey = bytes.Repeat([]byte{2}, 32)
	req := Request{Path: "/v1/responses", UpstreamType: UpstreamTypeAccount}
	inner := `{"path":"\u007b` + exampleRestoreToken[1:] + `secret"}`
	raw, _ := json.Marshal(map[string]any{"input": []any{map[string]string{"type": "function_call", "arguments": inner}}})
	res, err := s.Apply(req, raw)
	if err != nil {
		t.Fatal(err)
	}
	path := gjson.Get(gjson.GetBytes(res.Body, "input.0.arguments").Str, "path").Str
	tail := strings.TrimPrefix(path, exampleRestoreToken)
	if path == tail || !containsRestoreToken(tail) || res.HitCount != 1 {
		t.Fatal("内层 JSON 转义破坏占位符边界", path)
	}
	if e := res.Restore.Publish(); e != nil {
		t.Fatal(e)
	}
	defer res.Restore.Release()
	if _, ok := s.Dict.Lookup(exampleRestoreToken); ok {
		t.Fatal("旧字面量不应建表")
	}
}

func TestToolOutputAfterPartialDictionaryEviction(t *testing.T) {
	rule := openAIKeyRule()
	rule.Pattern = `password=(?P<redact>[a-zA-Z0-9]+)`
	s := newTestSnapshot(t, redactSettings(), rule)
	s.Settings.ResponseRestoreEnabled = true
	s.Settings.OnError = OnErrorFailClosed
	s.RestoreKey = bytes.Repeat([]byte{3}, 32)
	s.Dict = newRestoreDict(2, 4096)
	var tokens []string
	values := []string{"oldSyntheticR52", "knownSynthetic8Z", "thirdSynthetic69"}
	for _, value := range values {
		in := []byte(`{"messages":[{"role":"user","content":"password=` + value + `"}]}`)
		r, err := s.Apply(claudeRequest(), in)
		if err != nil {
			t.Fatal(err)
		}
		token := strings.TrimPrefix(gjson.GetBytes(r.Body, "messages.0.content").Str, "password=")
		if !containsRestoreToken(token) {
			t.Fatal("规则未生成占位符")
		}
		tokens = append(tokens, token)
		publishDictionaryTest(t, r.Restore)
		r.Restore.Release()
	}
	if _, ok := s.Dict.Lookup(tokens[0]); ok {
		t.Fatal("最早的条目应已淘汰")
	}
	req := Request{Path: "/v1/responses", UpstreamType: UpstreamTypeAccount, SessionID: "another-session"}
	in := []byte(`{"previous_response_id":"external","input":[{"type":"function_call_output","call_id":"unknown","output":"` + strings.Join(values, " ") + `"}]}`)
	r, err := s.Apply(req, in)
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(r.Body, "input.0.output").Str; got != values[0]+" "+tokens[1]+" "+tokens[2] || r.HitCount != 0 {
		t.Fatalf("淘汰后应原样发送失效值并遮蔽两个驻留值: %q hits=%d", got, r.HitCount)
	}
	publishDictionaryTest(t, r.Restore)
	r.Restore.Release()
}
