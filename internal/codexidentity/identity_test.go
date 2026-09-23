package codexidentity

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestIdentityBodyValidationCacheOwnsSnapshot(t *testing.T) {
	r := NewRequest(testPolicy(t), cliSource())
	body := []byte(`{"model":"gpt-5","input":"hello"}`)
	first, err := r.validatedBody(body)
	if err != nil {
		t.Fatal(err)
	}
	copyHit, err := r.validatedBody(bytes.Clone(body))
	if err != nil || copyHit != first {
		t.Fatal("同内容的新切片未复用校验")
	}
	out := prepare(t, r, testScope(1), OperationResponses, string(body), nil)
	out.Body[0] = '['
	if string(first.raw) != string(body) || r.body.object != first {
		t.Fatal("Prepared 暴露了缓存内部的可变切片")
	}
	for _, id := range []int64{2, 1} {
		prepare(t, r, testScope(id), OperationResponses, string(body), nil)
		if r.body.object != first {
			t.Fatal("A → B → A 对同一原文重复校验")
		}
	}
	body[0] = '[' // 地址、长度都没变，也不能复用旧校验结果。
	if _, err := r.validatedBody(body); err == nil {
		t.Fatal("原地修改误命中校验缓存")
	}
	if first.raw[0] != '{' {
		t.Fatal("调用方修改污染了已发布快照")
	}
}

func TestIdentityBodyValidationCacheConcurrentVersions(t *testing.T) {
	r := NewRequest(testPolicy(t), cliSource())
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := []byte(fmt.Sprintf(`{"model":"gpt-%d","input":"same"}`, i%2))
			for attempt := 0; attempt < 10; attempt++ {
				object, err := r.validatedBody(body)
				if err != nil || !bytes.Equal(object.raw, body) {
					t.Error("缓存返回了其他正文版本", err)
				}
			}
		}(i)
	}
	wg.Wait()
}

func TestIdentityPrivacyValidationDoesNotTrustSliceIdentity(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func([]byte) []byte
		wantErr bool
	}{
		{"same_slice", func(body []byte) []byte { return body }, false},
		{"equal_copy", bytes.Clone, false},
		{"user_text", func(body []byte) []byte { return bytes.ReplaceAll(body, []byte(`hello`), []byte(`clean`)) }, false},
		{"inplace_user_text", func(body []byte) []byte {
			copy(body[bytes.Index(body, []byte(`hello`)):], "clean")
			return body
		}, false},
		{"inplace_model", func(body []byte) []byte {
			body[bytes.Index(body, []byte(`gpt-5`))+4] = '6'
			return body
		}, true},
		{"duplicate_nonidentity_key", func(body []byte) []byte {
			return append(body[:len(body)-1], []byte(`,"input":"injected"}`)...)
		}, true},
		{"inplace_invalid_json", func(body []byte) []byte { body[0] = '['; return body }, true},
		{"extra_document", func(body []byte) []byte { return append(body, []byte(` {}`)...) }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRequest(testPolicy(t), cliSource())
			out := prepare(t, r, testScope(1), OperationResponses,
				`{"model":"gpt-5","prompt_cache_key":"source","client_metadata":{"session_id":"source"},"input":"hello"}`, nil)
			err := out.ValidatePrivacyBody(tc.mutate(out.Body))
			if (err != nil) != tc.wantErr {
				t.Fatalf("隐私校验错误: %v", err)
			}
		})
	}
}

func testPolicy(t *testing.T) Policy {
	t.Helper()
	n, err := ParseNamespace("067cbe02-1334-45d1-85c0-df7f459055ef")
	if err != nil {
		t.Fatal(err)
	}
	return Policy{Enabled: true, Version: DefaultCompatVersion, Namespace: n}
}
func testScope(id int64) AccountScope {
	s, _ := NewAccountScope(id, "oauth", "https://chatgpt.com/backend-api/codex", fmt.Sprintf("acct-%d", id), true)
	return s
}
func cliSource() SourceHeaders {
	return SourceHeaders{UserAgent: "codex_cli_rs/0.155.1 (Mac OS 27.0.0; arm64) unknown", Version: "0.155.1", Originator: Originator, SessionID: "source", ThreadID: "source", ClientRequestID: "source", WindowID: "source:4", InstallationID: "install", ParentThreadID: "parent"}
}
func prepare(t *testing.T, request *Request, scope AccountScope, op Operation, body string, owners *OwnerIndex) *Prepared {
	t.Helper()
	out, err := request.Prepare(scope, op, []byte(body), owners)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestIdentityAssociationsAndRawPreservation(t *testing.T) {
	src := cliSource()
	src.TurnMetadata = `{"session_id":"source","thread_id":"source","window_id":"source:4","parent_thread_id":"parent","turn_id":"turn","tool_namespaces_info":{"local":true}}`
	body := ` { "model" : "gpt-5", "service_tier":"priority", "prompt_cache_key":"source", "metadata":{"business":9007199254740993}, "input":[{"id":"server-id","encrypted_content":"AA\\BB", "text":"source", "number":1.2300e+19}], "client_metadata":{"session_id":"source","thread_id":"source","x-codex-window-id":"source:4","x-codex-turn-metadata":` + string(JSONString(src.TurnMetadata)) + `,"x-oai-attestation":"proof","normal":"keep"} } `
	r := NewRequest(testPolicy(t), src)
	a := prepare(t, r, testScope(1), OperationResponses, body, nil)
	aRetry := prepare(t, r, testScope(1), OperationResponses, body, nil)
	b := prepare(t, r, testScope(2), OperationResponses, body, nil)
	aAgain := prepare(t, NewRequest(testPolicy(t), src), testScope(1), OperationResponses, body, nil)
	if !bytes.Equal(a.Body, aRetry.Body) || !bytes.Equal(a.Body, aAgain.Body) || bytes.Equal(a.Body, b.Body) {
		t.Fatal("映射不稳定或跨账号复用")
	}
	id := a.Headers.Get(HeaderSessionID)
	if id == "source" || id != a.Headers.Get(HeaderThreadID) || id != a.Headers.Get(HeaderClientRequestID) || a.Headers.Get(HeaderWindowID) != id+":4" {
		t.Fatalf("CLI 关系错误: %v", a.Headers)
	}
	if a.Headers.Get(HeaderInstallationID) != "" {
		t.Fatal("installation id 不应作为独立 HTTP 请求头发送")
	}
	fields, _ := JSONFields(a.Body)
	original, _ := JSONFields([]byte(body))
	for _, k := range []string{"input", "metadata"} {
		if !bytes.Equal(fields[k], original[k]) {
			t.Fatalf("修改了 %s 原始表示", k)
		}
	}
	var cache string
	_ = json.Unmarshal(fields["prompt_cache_key"], &cache)
	if cache != id {
		t.Fatal("cache 与 session 不一致")
	}
	metadata, _ := JSONFields(fields["client_metadata"])
	installation := JSONString(testPolicy(t).Namespace.Mapper(testScope(1)).InstallationID())
	if string(metadata["session_id"]) != string(JSONString(id)) || metadata[HeaderAttestation] != nil || string(metadata["normal"]) != `"keep"` || !bytes.Equal(metadata[HeaderInstallationID], installation) || metadata["installation_id"] != nil {
		t.Fatalf("metadata 处理错误: %s", fields["client_metadata"])
	}
	var nested string
	_ = json.Unmarshal(metadata[HeaderTurnMetadata], &nested)
	if nested != a.Headers.Get(HeaderTurnMetadata) || strings.Contains(nested, `"source"`) {
		t.Fatal("头与序列化 metadata 不一致")
	}
	if a.Headers.Get(HeaderRoutingHint) != "model=gpt-5;tier=priority" {
		t.Fatal("routing hint 错误")
	}
	app := src
	app.SessionID = "app-session"
	app.TurnMetadata = ""
	appOut := prepare(t, NewRequest(testPolicy(t), app), testScope(1), OperationResponses, `{"input":"hello"}`, nil)
	if appOut.Headers.Get(HeaderSessionID) == appOut.Headers.Get(HeaderThreadID) {
		t.Fatal("App 不等值关系丢失")
	}
	mapper := testPolicy(t).Namespace.Mapper(testScope(1))
	if mapper.PromptCacheKey("guardian:parent", "parent") != "guardian:"+mapper.ID("parent") || mapper.InstallationID() == testPolicy(t).Namespace.Mapper(testScope(2)).InstallationID() {
		t.Fatal("派生父线程或安装 ID 错误")
	}
}

// 原生 rust-v0.155.1 client_metadata() 顶层键集合，映射后不能多出非原生键。
func TestIdentityNativeClientMetadataKeys(t *testing.T) {
	body := `{"model":"gpt-5","prompt_cache_key":"source","client_metadata":{"x-codex-installation-id":"install","session_id":"source","thread_id":"source","x-codex-window-id":"source:4"},"input":"hello"}`
	out := prepare(t, NewRequest(testPolicy(t), cliSource()), testScope(1), OperationResponses, body, nil)
	fields, _ := JSONFields(out.Body)
	metadata, _ := JSONFields(fields["client_metadata"])
	want := []string{HeaderInstallationID, "session_id", "thread_id", HeaderWindowID}
	if len(metadata) != len(want) {
		t.Fatalf("client_metadata 键集合偏离原生: %s", fields["client_metadata"])
	}
	for _, key := range want {
		if metadata[key] == nil {
			t.Fatalf("缺少原生键 %s: %s", key, fields["client_metadata"])
		}
	}
	installation := JSONString(testPolicy(t).Namespace.Mapper(testScope(1)).InstallationID())
	if !bytes.Equal(metadata[HeaderInstallationID], installation) {
		t.Fatalf("installation 未按 scope 映射: %s", metadata[HeaderInstallationID])
	}
}

func TestIdentityMissingSourcesAndTargetMatrix(t *testing.T) {
	p := testPolicy(t)
	r := NewRequest(p, SourceHeaders{})
	a := prepare(t, r, testScope(1), OperationResponses, `{}`, nil)
	retry := prepare(t, r, testScope(1), OperationResponses, `{}`, nil)
	other := prepare(t, NewRequest(p, SourceHeaders{}), testScope(1), OperationResponses, `{}`, nil)
	if a.Headers.Get(HeaderSessionID) != retry.Headers.Get(HeaderSessionID) || a.Headers.Get(HeaderSessionID) == other.Headers.Get(HeaderSessionID) {
		t.Fatal("缺省会话未按请求隔离")
	}
	for _, tc := range []struct {
		provider, url string
		oauth         bool
	}{{"api_key", "https://api.openai.com/v1", false}, {"api_key", "https://relay.test/v1", false}, {"oauth", "https://relay.test/v1", true}, {"oauth", "http://chatgpt.com", true}} {
		t.Run(tc.provider+tc.url, func(t *testing.T) {
			scope, _ := NewAccountScope(1, tc.provider, tc.url, "acct", tc.oauth)
			policy := p
			policy.Namespace = Namespace{}
			policy.NamespaceError = errors.New("db down")
			out := prepare(t, NewRequest(policy, cliSource()), scope, OperationResponses, `{"client_metadata":{"workspace":"secret"},"prompt_cache_key":"source","metadata":{"business":1},"input":"source"}`, nil)
			fields, _ := JSONFields(out.Body)
			if out.FirstParty || fields["client_metadata"] != nil || string(fields["prompt_cache_key"]) != `"source"` || string(fields["metadata"]) != `{"business":1}` {
				t.Fatalf("目标契约错误: %s", out.Body)
			}
			h := http.Header{}
			h.Set(HeaderSessionID, "source")
			h.Set(HeaderAttestation, "proof")
			h.Set(HeaderTurnMetadata, "private")
			out.ApplyHeaders(h, nil)
			if h.Get(HeaderSessionID) != "" || h.Get(HeaderAttestation) != "" || h.Get(HeaderTurnMetadata) != "" || h.Get(HeaderClientRequestID) == "" {
				t.Fatalf("目标头错误: %v", h)
			}
		})
	}
}

func TestIdentityErrorsAndProfiles(t *testing.T) {
	for _, body := range []string{`{"client_metadata":{"session_id":"other"}}`, `{"model":"a","model":"b"}`, `{"input":{"a":1,"a":2}}`, `{"client_metadata":{"thread_id":12}}`, `{"client_metadata":{"x-codex-turn-metadata":"not-json"}}`, `{} {}`} {
		_, err := NewRequest(testPolicy(t), cliSource()).Prepare(testScope(1), OperationResponses, []byte(body), nil)
		if err == nil {
			t.Fatalf("应拒绝 %s", body)
		}
	}
	p := testPolicy(t)
	p.Namespace = Namespace{}
	_, err := NewRequest(p, SourceHeaders{}).Prepare(testScope(1), OperationResponses, []byte(`{}`), nil)
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != CodeNamespaceUnavailable {
		t.Fatalf("namespace 错误: %v", err)
	}
	for _, op := range []Operation{OperationModels, OperationAuxiliary} {
		out := prepare(t, NewRequest(p, SourceHeaders{}), testScope(1), op, "", nil)
		if out.Headers.Get("User-Agent") == "" || out.Headers.Get(HeaderSessionID) != "" {
			t.Fatal("辅助接口被 namespace 影响")
		}
	}
	src := cliSource()
	src.Version = "0.156.0"
	src.UserAgent = "codex_cli_rs/0.156.0 (Linux; x86_64) unknown"
	src.BetaFeatures = "remote_compaction_v2"
	src.Subagent = "my-task"
	out := prepare(t, NewRequest(testPolicy(t), src), testScope(1), OperationResponses, `{}`, nil)
	if out.Version != src.Version || out.DescriptionSource != "request" || out.Headers.Get(HeaderSubagent) != "my-task" {
		t.Fatal("有效来源未保留")
	}
	src.BetaFeatures = "remote_compaction_v2,unknown_feature"
	out = prepare(t, NewRequest(testPolicy(t), src), testScope(1), OperationResponses, `{}`, nil)
	if out.Headers.Get(HeaderBetaFeatures) != src.BetaFeatures || len(out.UnknownBetaFeatures) != 1 || out.UnknownBetaFeatures[0] != "unknown_feature" {
		t.Fatalf("未知 beta 值未原样透传并标记告警: %q %v", out.Headers.Get(HeaderBetaFeatures), out.UnknownBetaFeatures)
	}
	src.ResponsesLite = "maybe"
	_, err = NewRequest(testPolicy(t), src).Prepare(testScope(1), OperationResponses, []byte(`{}`), nil)
	if !errors.As(err, &typed) || typed.Code != CodeUnsupportedFeature {
		t.Fatalf("无法按协议解释的布尔声明未拒绝: %v", err)
	}
	src.ResponsesLite = ""
	src.BetaFeatures = ""
	src.Subagent = ""
	src.UserAgent = "unknown/0.156.0"
	out = prepare(t, NewRequest(testPolicy(t), src), testScope(1), OperationResponses, `{}`, nil)
	if out.DescriptionSource != "builtin" || out.Version != DefaultCompatVersion {
		t.Fatal("未知描述被信任")
	}
	profile, _ := BuiltinProfile(DefaultCompatVersion)
	if profile.IsStale(profile.ValidatedAt) || !profile.IsStale(profile.ValidUntil.Add(time.Second)) || !strings.Contains(profile.UserAgent(OperationResponses), "(codex_cli_rs; 0.155.1)") || strings.Contains(profile.UserAgent(OperationModels), "(codex_cli_rs;") {
		t.Fatal("档案操作或时效错误")
	}
	unverified, _ := BuiltinProfile("0.156.0")
	if !unverified.IsStale(time.Now()) || !unverified.ValidatedAt.IsZero() || unverified.Evidence != "" {
		t.Fatal("覆盖版本错误继承基线验收")
	}
	for _, bad := range []string{"", "x", "0.155.1\r\nx:x", strings.Repeat("1", 33)} {
		if _, err := ValidateCompatVersion(bad); err == nil {
			t.Fatal("无效版本被接受")
		}
	}
}

func TestKnownDescriptorExecAndVSCode(t *testing.T) {
	for _, tc := range []struct {
		originator string
		userAgent  string
	}{
		{"codex_exec", "codex_exec/0.155.1 (Mac OS 27.0.0; arm64) otty/1.5.1"},
		{"codex_vscode", "codex_vscode/0.155.1 (Mac OS 27.0.0; arm64)"},
	} {
		src := cliSource()
		src.Originator = tc.originator
		src.UserAgent = tc.userAgent
		src.BetaFeatures = "remote_compaction_v2"
		out := prepare(t, NewRequest(testPolicy(t), src), testScope(1), OperationResponses, `{}`, nil)
		if out.DescriptionSource != "request" || out.Headers.Get(HeaderBetaFeatures) != "remote_compaction_v2" {
			t.Fatalf("%s 未识别为已知客户端: %+v", tc.originator, out.Headers)
		}
	}
}

func TestPromptCacheKeyFallbackForMissingSession(t *testing.T) {
	src := cliSource()
	src.SessionID, src.ThreadID, src.ClientRequestID = "", "", ""
	out := prepare(t, NewRequest(testPolicy(t), src), testScope(1), OperationResponses, `{"prompt_cache_key":"cache-source"}`, nil)
	if out.Headers.Get(HeaderSessionID) == "" || out.Headers.Get(HeaderSessionID) != out.Headers.Get(HeaderThreadID) {
		t.Fatalf("未从 prompt_cache_key 建立稳定会话: %+v", out.Headers)
	}
	mapper := testPolicy(t).Namespace.Mapper(testScope(1))
	if out.Headers.Get(HeaderSessionID) != mapper.ID("cache-source") {
		t.Fatalf("prompt_cache_key 未作为 session 兜底: got=%q", out.Headers.Get(HeaderSessionID))
	}
}

// 客户端描述识别只决定出站 UA，不承担功能权限判断：未识别描述的合法声明同样原样保留。
func TestIdentityFeatureDeclarationsIgnoreDescriptor(t *testing.T) {
	src := cliSource()
	src.UserAgent, src.Originator = "my-tool/1.0", "my-tool"
	src.BetaFeatures = "remote_compaction_v2,worktrees,future_flag"
	src.ResponsesLite, src.MemgenRequest, src.IncludeTimingMetrics, src.Subagent = "true", "true", "true", "review"
	body := `{"model":"gpt-5","client_metadata":{"session_id":"source","x-openai-subagent":"review"},"input":"hello"}`
	out := prepare(t, NewRequest(testPolicy(t), src), testScope(1), OperationResponses, body, nil)
	if out.DescriptionSource != "builtin" {
		t.Fatal("未知描述被当作出站描述")
	}
	for header, want := range map[string]string{HeaderBetaFeatures: src.BetaFeatures, HeaderResponsesLite: "true", HeaderMemgenRequest: "true", HeaderIncludeTimingMetrics: "true", HeaderSubagent: "review"} {
		if got := out.Headers.Get(header); got != want {
			t.Fatalf("%s=%q want=%q", header, got, want)
		}
	}
	if len(out.UnknownBetaFeatures) != 1 || out.UnknownBetaFeatures[0] != "future_flag" {
		t.Fatalf("已知 beta 值被误报或未知值漏报: %v", out.UnknownBetaFeatures)
	}
	fields, _ := JSONFields(out.Body)
	metadata, _ := JSONFields(fields["client_metadata"])
	if string(metadata[HeaderSubagent]) != `"review"` {
		t.Fatalf("metadata 中的 subagent 声明被删除: %s", fields["client_metadata"])
	}
	src.ResponsesLite = "false"
	out = prepare(t, NewRequest(testPolicy(t), src), testScope(1), OperationResponses, body, nil)
	if out.Headers.Get(HeaderResponsesLite) != "" {
		t.Fatal("false 声明被补发成原生客户端不会发送的头")
	}
}

// 未识别的首方扩展头和 metadata 键删除（避免新身份字段未映射出站），只记录名称；无前缀的业务键保留。
func TestIdentityDropsUnknownFirstPartyExtensions(t *testing.T) {
	header := http.Header{}
	header.Set("User-Agent", cliSource().UserAgent)
	header.Set(HeaderOriginator, Originator)
	header.Set(HeaderVersion, DefaultCompatVersion)
	header.Set(HeaderSessionID, "source")
	header.Set("X-Codex-Future-Id", "raw-header-id")
	header.Set(HeaderSubagent, "review")
	header.Set("X-Custom", "keep")
	src := CaptureSourceHeaders(header)
	if len(src.Unrecognized) != 1 || src.Unrecognized[0] != "header.x-codex-future-id" {
		t.Fatalf("未识别首方头采集错误: %v", src.Unrecognized)
	}
	turn := `{"session_id":"source","x-openai-future":"raw-turn-id","workspaces":{"a":1}}`
	body := `{"model":"gpt-5","client_metadata":{"session_id":"source","x-codex-future-id":"raw-body-id","plain_key":"keep","x-codex-turn-metadata":` + string(JSONString(turn)) + `},"input":"hello"}`
	out := prepare(t, NewRequest(testPolicy(t), src), testScope(1), OperationResponses, body, nil)
	if strings.Contains(string(out.Body), "raw-body-id") || strings.Contains(string(out.Body), "raw-turn-id") || !strings.Contains(string(out.Body), `"plain_key":"keep"`) || !strings.Contains(string(out.Body), `workspaces`) {
		t.Fatalf("metadata 扩展清理错误: %s", out.Body)
	}
	want := []string{"header.x-codex-future-id", "client_metadata.x-codex-future-id", "turn_metadata.x-openai-future"}
	if len(out.DroppedFields) != len(want) {
		t.Fatalf("删除字段记录错误: %v", out.DroppedFields)
	}
	for _, name := range want {
		if !slices.Contains(out.DroppedFields, name) {
			t.Fatalf("缺少删除记录 %s: %v", name, out.DroppedFields)
		}
	}
	outbound := header.Clone()
	out.ApplyHeaders(outbound, nil)
	if outbound.Get("X-Codex-Future-Id") != "" || outbound.Get("X-Custom") != "keep" || outbound.Get(HeaderSubagent) != "review" {
		t.Fatalf("出站头处理错误: %v", outbound)
	}
	// 记录上限与去重，防止来源用大量字段放大日志。
	for i := range maxReportedFieldNames + 4 {
		header.Set(fmt.Sprintf("X-Codex-Extra-%d", i), "v")
	}
	if got := len(CaptureSourceHeaders(header).Unrecognized); got != maxReportedFieldNames {
		t.Fatalf("告警名称未限量: %d", got)
	}
}

// session 只在 turn metadata 头里时，独立 prompt_cache_key 不能先占位造成误判冲突。
func TestPromptCacheKeyFallbackAfterTurnMetadataHeader(t *testing.T) {
	src := cliSource()
	src.SessionID, src.ThreadID, src.ClientRequestID = "", "", ""
	src.TurnMetadata = `{"session_id":"real-session","thread_id":"real-session"}`
	out := prepare(t, NewRequest(testPolicy(t), src), testScope(1), OperationResponses, `{"model":"gpt-5","prompt_cache_key":"custom-key","input":"hello"}`, nil)
	mapper := testPolicy(t).Namespace.Mapper(testScope(1))
	if out.Headers.Get(HeaderSessionID) != mapper.ID("real-session") {
		t.Fatalf("未使用 turn metadata 头中的真实 session: %q", out.Headers.Get(HeaderSessionID))
	}
	fields, _ := JSONFields(out.Body)
	if string(fields["prompt_cache_key"]) != string(JSONString(mapper.ID("custom-key"))) {
		t.Fatalf("独立缓存键未按自身映射: %s", fields["prompt_cache_key"])
	}
}

func TestIdentityTurnStateIsolationAndToggle(t *testing.T) {
	index := NewOwnerIndex(2, time.Minute)
	src := cliSource()
	p := testPolicy(t)
	first := prepare(t, NewRequest(p, src), testScope(1), OperationResponses, `{}`, index)
	index.Register("token", first.Owner, time.Now())
	src.TurnState = "token"
	body := `{"client_metadata":{"x-codex-turn-state":"token"}}`
	same := prepare(t, NewRequest(p, src), testScope(1), OperationResponses, body, index)
	if same.Headers.Get(HeaderTurnState) != "token" || !strings.Contains(string(same.Body), `"token"`) {
		t.Fatal("同 owner 的状态未复用")
	}
	for _, change := range []string{"account", "session", "version", "disabled", "restart"} {
		t.Run(change, func(t *testing.T) {
			policy := p
			source := src
			scope := testScope(1)
			owners := index
			switch change {
			case "account":
				scope = testScope(2)
			case "session":
				source.SessionID = "another"
			case "version":
				policy.Version = "0.156.0"
			case "disabled":
				policy.Enabled = false
			case "restart":
				owners = NewOwnerIndex(2, time.Minute)
			}
			out := prepare(t, NewRequest(policy, source), scope, OperationResponses, body, owners)
			if change == "disabled" {
				if out.Headers.Get(HeaderTurnState) != "" || !strings.Contains(string(out.Body), `"token"`) {
					t.Fatal("关闭时 Prepare 不应改写状态，但单元结果仍应保留正文")
				}
				return
			}
			if out.Headers.Get(HeaderTurnState) != "" || strings.Contains(string(out.Body), `"token"`) {
				t.Fatal("跨 owner 状态泄漏")
			}
		})
	}
	p.Enabled = false
	raw := ` {"client_metadata":{"normal":"keep","session_id":"source"},"input":9007199254740993} `
	legacy := prepare(t, NewRequest(p, src), testScope(1), OperationResponses, raw, index)
	if string(legacy.Body) != raw {
		t.Fatal("关闭后修改了非状态正文")
	}
	for _, metadata := range []string{`null`, `[]`, `"opaque"`} {
		raw := `{"client_metadata":` + metadata + `,"input":"hello"}`
		out := prepare(t, NewRequest(p, src), testScope(1), OperationResponses, raw, index)
		if string(out.Body) != raw {
			t.Fatal("关闭时改变 client_metadata 的原始类型")
		}
	}
	src.TurnState = "legacy"
	legacy = prepare(t, NewRequest(p, src), testScope(1), OperationResponses, `{}`, index)
	index.Register("legacy", first.Owner, time.Now())
	index.Register("third", first.Owner, time.Now())
	if index.Allows("token", first.Owner, time.Now()) || len(index.entries) > 2 || index.Allows("third", first.Owner, time.Now().Add(time.Hour)) {
		t.Fatal("容量或 TTL 失效")
	}
	index.Register("conflict", first.Owner, time.Now())
	index.Register("conflict", legacy.Owner, time.Now())
	if index.Allows("conflict", first.Owner, time.Now()) || index.Allows("conflict", legacy.Owner, time.Now()) {
		t.Fatal("不确定 owner 被接受")
	}
}
