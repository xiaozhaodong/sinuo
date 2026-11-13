package privacy

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func previewTestSnapshot(t *testing.T, rules ...Rule) *Snapshot {
	t.Helper()
	s := newTestSnapshot(t, redactSettings(), rules...)
	s.Settings.ResponseRestoreEnabled = true
	s.RestoreKey = bytes.Repeat([]byte{42}, 32)
	return s
}

func previewTestBody(t *testing.T, req Request, text string) ([]byte, string) {
	t.Helper()
	var input any
	path := "input"
	if isClaudeMessagesPath(req.Path) {
		input = map[string]any{"messages": []any{map[string]string{"role": "user", "content": text}}}
		path = "messages.0.content"
	} else {
		input = map[string]string{"input": text}
	}
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return body, path
}

func TestApplyToTextMatchesReversibleOutboundWithoutPublishing(t *testing.T) {
	value := "synthetic-preview-value"
	rule := literalRule(1, 1, value, "[敏感值]", ActionRedact)
	rule.Source = SourceExact
	for _, req := range []Request{
		{Path: "/v1/messages", UpstreamType: UpstreamTypeEndpoint},
		{Path: "/v1/messages/count_tokens", UpstreamType: UpstreamTypeEndpoint},
		{Path: "/v1/responses", UpstreamType: UpstreamTypeAccount},
		{Path: "/v1/responses/compact", UpstreamType: UpstreamTypeAccount},
		{Path: "/v1/messages", UpstreamType: UpstreamTypeSubscription},
		{Path: "/v1/responses", UpstreamType: UpstreamTypeSubscription},
	} {
		t.Run(req.Path+"/"+req.UpstreamType, func(t *testing.T) {
			s := previewTestSnapshot(t, rule)
			text := "中文\n\"" + value + "\" \\ {{ user.name }}"
			body, path := previewTestBody(t, req, text)
			actual, err := s.Apply(req, body)
			if err != nil {
				t.Fatal(err)
			}
			defer actual.Restore.Release()
			for range 2 {
				preview := s.ApplyToText(req, text)
				if preview.SkippedReason != "" || preview.HitCount != 1 || !preview.Changed ||
					string(preview.Body) != gjson.GetBytes(actual.Body, path).Str || !containsRestoreToken(string(preview.Body)) {
					t.Fatal("回填预览必须与实际出站文本一致，且重复测试标识稳定")
				}
				if preview.Restore != nil || len(s.Dict.entries) != 0 || s.Dict.activeViews != 0 || s.Dict.capacity.slots != 0 {
					t.Fatal("测试不能返回可发布视图、发布新词条或占用在途引用")
				}
			}
		})
	}
}

func TestApplyToTextRemasksDictionaryWithoutChangingIt(t *testing.T) {
	s := previewTestSnapshot(t)
	seed := prepareDictionaryTest(t, s.Dict)
	longValue, weakValue := "synthetic-dictionary-value", "123456"
	longToken, err := seed.add(s.RestoreKey, longValue)
	if err != nil {
		t.Fatal(err)
	}
	weakToken, err := seed.add(s.RestoreKey, weakValue)
	if err != nil {
		t.Fatal(err)
	}
	publishDictionaryTest(t, seed)
	seed.Release()
	revision, size := s.Dict.revision, s.Dict.bytes
	first := s.Dict.writes.Front().Value
	req := Request{Path: "/v1/responses", UpstreamType: UpstreamTypeAccount}
	text := longValue + " " + weakValue + " build_" + weakValue
	preview := s.ApplyToText(req, text)
	if preview.HitCount != 0 || len(preview.RuleHits) != 0 || preview.RemaskMatches != 2 || !preview.Changed ||
		string(preview.Body) != longToken+" "+weakToken+" build_"+weakValue {
		t.Fatal("无直接规则时也应预览字典再次遮蔽，并保留弱值边界")
	}
	if s.Dict.revision != revision || s.Dict.bytes != size || len(s.Dict.entries) != 2 || s.Dict.writes.Front().Value != first || s.Dict.activeViews != 0 {
		t.Fatal("预览不得改动词条、容量、淘汰顺序或在途状态")
	}
	for _, entry := range s.Dict.entries {
		if entry.pins != 0 || entry.text != 0 || entry.tool != 0 || !entry.last.IsZero() {
			t.Fatal("预览不得修改回填计数或引用")
		}
	}
}

func TestApplyToTextExpandsNewMatchesOnlyInsidePreview(t *testing.T) {
	rule := Rule{ID: 1, Enabled: true, Name: "字段", MatchType: MatchTypeRegex,
		Pattern: `credential=(?P<redact>[a-z0-9-]+)`, Placeholder: "[敏感值]", Action: ActionRedact}
	s := previewTestSnapshot(t, rule)
	req := Request{Path: "/v1/responses", UpstreamType: UpstreamTypeAccount}
	value := "synthetic-local-value"
	preview := s.ApplyToText(req, "credential="+value+" "+value)
	if preview.HitCount != 1 || preview.RemaskMatches != 2 || bytes.Contains(preview.Body, []byte(value)) {
		t.Fatal("新发现值应在本次测试内按真实逻辑扩展遮蔽")
	}
	if next := s.ApplyToText(req, value); next.Changed || next.RemaskMatches != 0 {
		t.Fatal("测试发现值不得污染后续请求的内存词典")
	}
}

func TestApplyToTextKeepsPlainModeScopesAndVueTemplates(t *testing.T) {
	value := "synthetic-template-value"
	rule := literalRule(1, 1, value, "[敏感值]", ActionRedact)
	s := previewTestSnapshot(t, rule)
	req := Request{Path: "/v1/responses", UpstreamType: UpstreamTypeAccount}
	for _, text := range []string{
		"<p>{{ user.name }}</p>", "{{SNU_example}}", "{{ SNU_" + strings.Repeat("a", 32) + " }}",
		"{{SNU_" + strings.Repeat("A", 32) + "}}", "{{SNU_" + strings.Repeat("a", 31) + "}}",
	} {
		if containsRestoreToken(text) {
			t.Fatal("普通 Vue 模板或非完整标识不应被识别为回填标识")
		}
		if got := s.ApplyToText(req, text); got.Changed || string(got.Body) != text || got.SkippedReason != "" {
			t.Fatal("普通模板预览应逐字保持不变")
		}
	}
	s.Settings.ResponseRestoreEnabled = false
	if got := s.ApplyToText(req, value); string(got.Body) != "[敏感值]" {
		t.Fatal("关闭回填仍使用规则的固定占位符")
	}
	s.Settings.ResponseRestoreEnabled = true
	if got := s.ApplyToText(Request{Path: "/v1/alpha/search", UpstreamType: UpstreamTypeAccount}, value); string(got.Body) != "[敏感值]" {
		t.Fatal("不支持回填的路径应保持实际出站的固定替换行为")
	}
	s.Rules[0].Scope.AccountIDs = []int64{42}
	if got := s.ApplyToText(req, value); got.Changed {
		t.Fatal("预览不能绕过规则作用域")
	}
	req.AccountID = 42
	if got := s.ApplyToText(req, value); !containsRestoreToken(string(got.Body)) {
		t.Fatal("匹配作用域后应恢复可逆预览")
	}
	s.Settings.Mode = ModeDisabled
	s.Dict = nil
	if got := s.ApplyToText(req, value); !containsRestoreToken(string(got.Body)) || s.Dict != nil || s.Settings.Mode != ModeDisabled {
		t.Fatal("关闭全局保护也可独立预览，但不能改动全局配置或词典")
	}
}

func TestApplyToTextPreservesFailurePoliciesWithoutWarmingCache(t *testing.T) {
	value := "synthetic-preview-budget"
	s := previewTestSnapshot(t, literalRule(1, 1, value, "[敏感值]", ActionRedact))
	req := Request{Path: "/v1/responses", UpstreamType: UpstreamTypeAccount}
	s.Settings.ScanMaxBytes = 4
	preview := s.ApplyToText(req, value)
	if !preview.FailOpen || preview.Changed || string(preview.Body) != value || preview.Restore != nil {
		t.Fatal("回填扫描超限放行必须完整撤销预览替换")
	}
	s.Settings.OverLimitAction = OverLimitFailClosed
	if got := s.ApplyToText(req, value); got.SkippedReason == "" || got.FailOpen || got.Changed {
		t.Fatal("超限拒绝必须显示测试失败，不能伪装成零命中")
	}
	s.Settings.ScanMaxBytes = DefaultScanMaxBytes
	s.CompileError = "synthetic compile failure"
	if got := s.ApplyToText(req, value); !got.FailOpen || got.Changed {
		t.Fatal("回填规则编译失败应与实际请求一样按出错策略处理")
	}
	s.Settings.OnError = OnErrorFailClosed
	if got := s.ApplyToText(req, value); got.SkippedReason == "" || got.Changed {
		t.Fatal("出错拒绝必须明确失败")
	}
	s.CompileError = ""
	s.Settings.OnError = OnErrorFailOpen
	s.Settings.OverLimitAction = OverLimitScanPrefix
	s.Cache = NewSegmentCache(DefaultSegmentCacheCapacity)
	longText := value + strings.Repeat("x", minCachedSegmentBytes)
	if got := s.ApplyToText(req, longText); !got.Changed {
		t.Fatal("初次预览应命中")
	}
	s.Settings.ScanMaxBytes = 4
	body, _ := previewTestBody(t, req, longText)
	actual, err := s.Apply(req, body)
	if err != nil || !actual.FailOpen || actual.Changed {
		t.Fatal("测试不能预热真实扫描缓存而绕过后续扫描预算")
	}
}
