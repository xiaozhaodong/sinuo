package proxy

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"sinuo/internal/privacy"
	"sinuo/internal/store"
)

// 旧路由的历史 ID 从未在本进程登记。账号池应扫描可见的用户消息和工具结果后发送。
func TestRestorePoolAcceptsUntrackedToolHistory(t *testing.T) {
	const secret = "syntheticHistoryValue73"
	filter := newRestoreTestFilter(t)
	rules, err := privacy.CompileRules([]privacy.Rule{{
		ID: 2, Name: "测试字段", Enabled: true, MatchType: privacy.MatchTypeRegex,
		Pattern: "password=(?P<redact>[A-Za-z0-9]+)", Placeholder: "[MASK]", Action: privacy.ActionRedact,
	}})
	if err != nil {
		t.Fatal(err)
	}
	filter.Rules = rules
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		if gjson.GetBytes(body, "previous_response_id").Str != "external-response" {
			t.Error("出站丢失原有历史引用")
		}
		out := gjson.GetBytes(body, "input.1.output").Str
		if gjson.GetBytes(body, "input.0.content").Str == "continue" {
			if out != "exit code 0" {
				t.Error("普通工具结果应原样送到上游")
			}
		} else {
			token := wireToken.FindString(out)
			if token == "" || out != token || gjson.GetBytes(body, "input.0.content").Str != "password="+token {
				t.Error("未用本轮发现的值再次遮蔽工具结果")
			}
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"accepted","output":[],"usage":{"input_tokens":2,"output_tokens":1}}`)
	}))
	t.Cleanup(up.Close)
	h, _ := phaseAccountHandler(t, up.URL, 1)
	h.SetPrivacyFilter(filter)
	for _, tc := range []struct{ session, prompt, output string }{
		{"first", "continue", "exit code 0"},
		{"second", "password=" + secret, secret},
	} {
		body := fmt.Sprintf(`{"model":"gpt-test","previous_response_id":"external-response","input":[{"role":"user","content":%q},{"type":"function_call_output","call_id":"external-tool","output":%q}]}`, tc.prompt, tc.output)
		req := privacySummaryRequest("/v1/responses", body, tc.session)
		req.Header.Set("Session-Id", tc.session)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 200 || gjson.Get(w.Body.String(), "id").Str != "accepted" {
			t.Errorf("未登记的历史引用导致请求失败: %d %s", w.Code, w.Body.String())
		}
	}
}

// 连续调用真实账号池入口。先把工具裸值段预热入基础缓存，再验证有、无会话头时的补遮。
func TestRestoreToolRoundTripAndCompact(t *testing.T) {
	const secret = "syntheticAb3Cd4Ef"
	const cipher = "sample-compact-cipher"
	output := strings.Repeat("tool output ", 32) + secret
	filter := newRestoreTestFilter(t)
	field := privacy.Rule{ID: 2, Placeholder: "[MASK]", Name: "测试字段", Enabled: true, MatchType: privacy.MatchTypeRegex, Pattern: "password=(?P<redact>[A-Za-z0-9]+)", Action: privacy.ActionRedact}
	rules, err := privacy.CompileRules([]privacy.Rule{field})
	if err != nil {
		t.Fatal(err)
	}
	filter.Rules = rules
	filter.Cache = privacy.NewSegmentCache(16)
	var token, window string
	var calls int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch calls {
		case 1: // 无规则上下文的相同文本先进入基础缓存。
			if !strings.Contains(string(raw), secret) {
				t.Error("预热基线被意外改写")
			}
			io.WriteString(w, `{"id":"warm","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)
		case 2:
			token = string(wireToken.Find(raw))
			if token == "" || strings.Contains(string(raw), secret) {
				t.Error("首轮未遮蔽")
			}
			fmt.Fprintf(w, `{"id":"first","output":[{"type":"function_call","id":"fc1","name":"local","arguments":"{\"value\":\"%s\"}"}],"usage":{"input_tokens":3,"output_tokens":5}}`, token)
		case 3, 5:
			v := gjson.GetBytes(raw, "input.0.output").Str
			if calls == 5 {
				v = gjson.GetBytes(raw, "input.3.output").Str
			}
			if strings.Contains(string(raw), secret) || !strings.HasSuffix(v, token) {
				t.Errorf("续接 %d 含原值或丢失标识", calls)
			}
			fmt.Fprintf(w, `{"id":"r%d","output":[{"type":"message","id":"m%d","content":[{"type":"output_text","text":"%s"}]}],"usage":{"input_tokens":3,"output_tokens":5}}`, calls, calls, token)
		case 4:
			if !strings.HasSuffix(r.URL.Path, "/compact") || !strings.Contains(string(raw), token) || strings.Contains(string(raw), secret) {
				t.Error("compact 请求未补遮")
			}
			window = fmt.Sprintf(`[ { "type":"message", "role":"assistant", "content":[{"type":"output_text","text":"%s"}]}, {"type":"function_call","name":"keep","arguments":"{}"}, {"type":"compaction","encrypted_content":"%s"} ]`, token, cipher)
			fmt.Fprintf(w, `{"id":"window","output":%s}`, window)
		case 6:
			if gjson.GetBytes(raw, "input.1.output").Str != output {
				t.Error("重启后应按当前内容放行未识别的裸值")
			}
			io.WriteString(w, `{"id":"after-restart","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)
		default:
			t.Error("意外出站")
		}
	}))
	t.Cleanup(up.Close)
	acc := &store.UpstreamAccountRecord{ID: 1, AccountName: "test", ProviderType: "api_key", CredentialRaw: "test", BaseURL: up.URL, Enabled: true}
	h := newAccountPipelineTestHandler(t, up.URL, &mockAccountPoolService{accounts: []*store.UpstreamAccountRecord{acc}})
	h.SetPrivacyFilter(filter)
	post := func(path, fields string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"gpt-5",`+fields+`}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	post("/v1/responses", fmt.Sprintf(`"input":%q`, output))
	first := post("/v1/responses", fmt.Sprintf(`"input":"password=%s"`, secret))
	if gjson.Get(gjson.Get(first.Body.String(), "output.0.arguments").Str, "value").Str != secret {
		t.Fatal("工具未收到还原值", first.Code)
	}
	tool := fmt.Sprintf(`{"type":"function_call_output","call_id":"c","output":%q}`, output)
	next := post("/v1/responses", `"previous_response_id":"first","input":[`+tool+`]`)
	if gjson.Get(next.Body.String(), "output.0.content.0.text").Str != secret {
		t.Fatalf("续接失败 %d %s", next.Code, next.Body)
	}
	compact := post("/v1/responses/compact", `"previous_response_id":"r3","input":[`+tool+`]`)
	if window == "" || gjson.Get(compact.Body.String(), "output").Raw != window {
		t.Fatalf("混合窗口未原样保留 %d", compact.Code)
	}
	fields := `"input":` + strings.TrimSuffix(window, "]") + "," + tool + "]"
	if r := post("/v1/responses", fields); gjson.Get(r.Body.String(), "output.0.content.0.text").Str != secret {
		t.Fatalf("压缩后续接失败 %d %s", r.Code, r.Body)
	}
	filter.Dict = privacy.NewRestoreDict()
	if r := post("/v1/responses", `"previous_response_id":"first","input":[{"role":"user","content":"new question"},`+tool+`]`); r.Code != 200 || calls != 6 {
		t.Fatalf("断档后工具结果未正常出站，状态 %d，出站 %d", r.Code, calls)
	}
}

// 无会话头的 B 首轮请求和后续工具结果均按 A 发布的原值补遮，响应按同一字典还原。
func TestRestoreAcrossEndpointAndPool(t *testing.T) {
	const secret = "fixtureCrossSessionAb123"
	filter := newRestoreTestFilter(t)
	field := privacy.Rule{ID: 2, Name: "密码字段", Enabled: true, MatchType: privacy.MatchTypeRegex, Pattern: "password=(?P<redact>[A-Za-z0-9]+)", Action: privacy.ActionRedact, Placeholder: "[MASK]"}
	rules, err := privacy.CompileRules([]privacy.Rule{field})
	if err != nil {
		t.Fatal(err)
	}
	filter.Rules = rules
	var token string
	claude := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		token = string(wireToken.Find(raw))
		if token == "" || strings.Contains(string(raw), secret) {
			t.Error("Claude 请求未遮蔽")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	t.Cleanup(claude.Close)
	endpoint, _ := newEndpointPipelineTestHandler(t, endpointPipelineConfig("A", claude.URL, 1))
	endpoint.SetPrivacyFilter(filter)
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"claude","messages":[{"role":"user","content":"password=`+secret+`"}]}`))
	req.Header.Set("X-Claude-Code-Session-Id", "session-A")
	endpoint.ServeHTTP(httptest.NewRecorder(), req)
	var calls int
	codex := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		calls++
		if calls == 1 {
			if gjson.GetBytes(raw, "input").Str != token {
				t.Error("B 首轮没有历史关联，仍应遮蔽 A 记录过的裸值")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, routeSSE(token, true))
			return
		}
		if strings.Contains(string(raw), secret) || calls == 2 && gjson.GetBytes(raw, "input.0.output").Str != token || calls == 3 && gjson.GetBytes(raw, "input.1.output").Str != token {
			t.Error("B 未再次遮蔽工具结果")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"b","output":[{"type":"custom_tool_call","input":"%s"},{"type":"function_call","id":"fc-B","call_id":"call-B","arguments":"{\"v\":\"%s\"}"}],"usage":{"input_tokens":1,"output_tokens":1}}`, token, token)
	}))
	t.Cleanup(codex.Close)
	acc := &store.UpstreamAccountRecord{ID: 2, AccountName: "B", ProviderType: "api_key", CredentialRaw: "test", BaseURL: codex.URL, Enabled: true}
	pool := newAccountPipelineTestHandler(t, codex.URL, &mockAccountPoolService{accounts: []*store.UpstreamAccountRecord{acc}})
	pool.SetPrivacyFilter(filter)
	post := func(raw string) string {
		r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(raw))
		w := httptest.NewRecorder()
		pool.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal("跨会话请求失败", w.Code, w.Body)
		}
		return w.Body.String()
	}
	if s := post(`{"model":"gpt-5","input":"` + secret + `","stream":true}`); !strings.Contains(s, "before "+secret+" after") || strings.Contains(s, token) {
		t.Fatal("跨协议 SSE 未使用进程字典")
	}
	next := post(`{"model":"gpt-5","previous_response_id":"r","input":[{"type":"function_call_output","output":"` + secret + `"}]}`)
	if calls != 2 || gjson.Get(next, "output.0.input").Str != secret || gjson.Get(gjson.Get(next, "output.1.arguments").Str, "v").Str != secret {
		t.Fatal("func/custom 工具未使用相同字典")
	}
	item := gjson.Get(next, "output.1").Raw
	replayed := post(`{"model":"gpt-5","input":[` + item + `,{"type":"function_call_output","call_id":"call-B","output":"` + secret + `"}]}`)
	if calls != 3 || gjson.Get(replayed, "output.0.input").Str != secret {
		t.Fatal("无会话头和 previous_response_id 的工具重放未使用进程字典")
	}

}

func TestRestoreShortValuesReachUpstream(t *testing.T) {
	const weak = "123456"
	var received []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received = append(received, gjson.GetBytes(body, "messages.0.content").Str)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"content":[],"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	t.Cleanup(up.Close)
	h, _ := newEndpointPipelineTestHandler(t, endpointPipelineConfig("weak", up.URL, 1))
	filter := newRestoreTestFilter(t)
	field := privacy.Rule{ID: 1, Name: "密码字段", Enabled: true, MatchType: privacy.MatchTypeRegex, Pattern: "password=(?P<redact>[0-9]+)", Action: privacy.ActionRedact, Placeholder: "[MASK]"}
	explicit := privacy.Rule{ID: 2, Name: "精确值", Enabled: true, MatchType: privacy.MatchTypeLiteral, Pattern: weak, Action: privacy.ActionRedact, Placeholder: "[MASK]"}
	for _, rules := range [][]privacy.Rule{{field}, {field, explicit}} {
		var err error
		filter.Rules, err = privacy.CompileRules(rules)
		if err != nil {
			t.Fatal(err)
		}
		h.SetPrivacyFilter(filter)
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"claude","messages":[{"role":"user","content":"password=123456 123456 build_123456"}]}`))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body)
		}
	}
	if len(received) != 2 {
		t.Fatal("短值请求未到达上游")
	}
	token := wireToken.FindString(received[0])
	if token == "" || received[0] != "password="+token+" "+token+" build_"+weak || received[1] != "password="+token+" "+token+" build_"+token {
		t.Fatal("字段发现值与显式精确值没有使用各自的边界规则")
	}
}
