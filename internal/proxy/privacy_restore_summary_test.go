package proxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"sinuo/internal/privacy"
)

func privacySummaryRequest(path, body, requestID string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req.WithContext(context.WithValue(req.Context(), "conn_id", requestID))
}

func TestClaudeRestoreSummaryAfterMessageStop(t *testing.T) {
	const trailer = ": keepalive"
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		token := wireToken.Find(body)
		if err != nil || token == nil {
			t.Error("出站请求缺少占位符", err)
			http.Error(w, "fixture", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, routeSSE(string(token), false)+trailer)
	}))
	t.Cleanup(up.Close)
	h, _ := newEndpointPipelineTestHandler(t, endpointPipelineConfig("synthetic", up.URL, 1))
	tracker := attachRetryTracker(t, h)
	h.SetPrivacyFilter(newRestoreTestFilter(t))
	const id = "claude-restore-terminal"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, privacySummaryRequest("/v1/messages", routeBody(false), id))

	result := timingLifecycleData(t, tracker, id)
	restore := gjson.Get(result.PrivacyScanJSON, "restore")
	if result.Detail.Status != "completed" || restore.Get("status").Str != "completed" || restore.Get("text").Int() != 1 {
		t.Fatalf("终态后误报回填失败: status=%s restore=%s", result.Detail.Status, restore.Raw)
	}
	if result.StopReason == nil || *result.StopReason != "end_turn" || result.Detail.OutputTokens != 11 {
		t.Fatal("没有保留上游的结束原因或输出用量")
	}
	var visible strings.Builder
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if raw, ok := strings.CutPrefix(line, "data: "); ok && gjson.Get(raw, "type").Str == "content_block_delta" {
			visible.WriteString(gjson.Get(raw, "delta.text").Str)
		}
	}
	if visible.String() != "before "+routeTestValue+" after" || !strings.HasSuffix(rec.Body.String(), "data: "+`{"type":"message_stop"}`+"\n\n") {
		t.Fatal("客户端未收到完整回填文本和正常终态")
	}
}

// 已有映射来自一次真实扫描和发布。第二个会话的干净正文从全局字典取值，续接只带工具结果。
func TestPrivacyRestoreSummaryHistoryAndRescan(t *testing.T) {
	const secret = "syntheticRestoreA27v"
	filter := newRestoreTestFilter(t)
	rules, err := privacy.CompileRules([]privacy.Rule{{
		ID: 1, Name: "合成字段", Enabled: true, MatchType: privacy.MatchTypeRegex,
		Pattern: `password=(?P<redact>[A-Za-z0-9]+)`, Placeholder: "[MASK]", Action: privacy.ActionRedact,
	}})
	if err != nil {
		t.Fatal(err)
	}
	filter.Rules = rules
	seed, err := filter.Apply(context.Background(), privacy.Request{Path: "/v1/responses", UpstreamType: "account", SessionID: "origin"}, []byte(`{"model":"gpt-test","input":"password=`+secret+`"}`))
	if err != nil || seed.Restore == nil {
		t.Fatalf("初始化测试映射失败 %v", err)
	}
	defer seed.Restore.Release()
	if err := seed.Restore.Publish(); err != nil {
		t.Fatal(err)
	}
	token := string(wireToken.Find(seed.Body))
	if token == "" {
		t.Fatal("首轮没有产生映射")
	}

	started := make(chan struct{})
	finish := make(chan struct{})
	unresolved := privacy.RestorePrefix + "ae"
	args := fmt.Sprintf(`{"fixture_argument":%q}`, unresolved)
	text := token + " " + token
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		if gjson.GetBytes(body, "previous_response_id").Exists() {
			if gjson.GetBytes(body, "input.0.output").Str != token || strings.Contains(string(body), secret) {
				t.Error("续接的工具结果没有再次遮蔽")
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"after-history","output":[],"usage":{"input_tokens":2,"output_tokens":1}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		close(started)
		select {
		case <-finish:
		case <-r.Context().Done():
			return
		}
		fmt.Fprintf(w, "data: "+`{"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"m","sequence_number":0,"delta":%q}`+"\n\n", text)
		fmt.Fprintf(w, "data: "+`{"type":"response.output_text.done","output_index":0,"content_index":0,"item_id":"m","sequence_number":1,"text":%q}`+"\n\n", text)
		fmt.Fprintf(w, "data: "+`{"type":"response.function_call_arguments.delta","output_index":1,"item_id":"fc","sequence_number":2,"delta":%q}`+"\n\n", args)
		fmt.Fprintf(w, "data: "+`{"type":"response.function_call_arguments.done","output_index":1,"item_id":"fc","sequence_number":3,"arguments":%q}`+"\n\n", args)
		fmt.Fprintf(w, "data: "+`{"type":"response.completed","sequence_number":4,"response":{"id":"history","status":"completed","output":[{"type":"message","id":"m","content":[{"type":"output_text","text":%q}]},{"type":"function_call","id":"fc","arguments":%q}],"usage":{"input_tokens":2,"output_tokens":4}}}`+"\n\n", text, args)
	}))
	t.Cleanup(up.Close)
	h, tracker := phaseAccountHandler(t, up.URL, 1)
	h.SetPrivacyFilter(filter)
	const reqID = "summary-history"
	req := privacySummaryRequest("/v1/responses", `{"model":"gpt-test","input":"clean","stream":true}`, reqID)
	req.Header.Set("Session-Id", "receiver")
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(rec, req)
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		close(finish)
		t.Fatal("未到达上游")
	}
	pending, pendingErr := tracker.GetRequestLifecycleData(context.Background(), reqID)
	close(finish)
	<-done
	if pendingErr != nil || pending == nil || pending.Source != "hot_pool" || gjson.Get(pending.PrivacyScanJSON, "restore.status").Str != "pending" {
		t.Fatalf("在途详情未记录 pending：%v %+v", pendingErr, pending)
	}
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), secret+" "+secret) || !strings.Contains(rec.Body.String(), unresolved) || !strings.Contains(rec.Body.String(), "response.completed") {
		t.Fatal("成功终态或工具参数未正常交付")
	}

	summary := timingLifecycleData(t, tracker, reqID)
	if summary.Detail.Status != "completed" || summary.Detail.OutputTokens != 4 {
		t.Fatalf("回填影响了正常结束或用量 %s %d", summary.Detail.Status, summary.Detail.OutputTokens)
	}
	scan := gjson.Parse(summary.PrivacyScanJSON)
	if !scan.Get("changed").Exists() || scan.Get("changed").Bool() || scan.Get("hit_count").Int() != 0 ||
		scan.Get("restore.status").Str != "completed" || scan.Get("restore.text").Int() != 2 || scan.Get("restore.unknown_tool").Int() != 1 {
		t.Fatalf("回填详情重复计算快照或丢失未还原状态 %s", scan.Raw)
	}
	for _, raw := range []string{secret, token, unresolved, "fixture_argument"} {
		if strings.Contains(scan.Raw, raw) {
			t.Fatal("已交付内容进入持久化摘要")
		}
	}
	const followID = "summary-follow"
	follow := privacySummaryRequest("/v1/responses", `{"model":"gpt-test","previous_response_id":"history","input":[{"type":"function_call_output","call_id":"fc","output":"`+secret+`"}]}`, followID)
	next := httptest.NewRecorder()
	h.ServeHTTP(next, follow)
	followSummary := gjson.Parse(timingLifecycleData(t, tracker, followID).PrivacyScanJSON)
	if next.Code != 200 || !followSummary.Get("changed").Bool() || followSummary.Get("hit_count").Int() != 0 ||
		followSummary.Get("restore.status").Str != "completed" || followSummary.Get("restore.text").Int() != 0 {
		t.Fatalf("补遮后的实际改写与零回填统计不符 %d %s", next.Code, followSummary.Raw)
	}
	// 等真实转发请求的归档事务完成，再从详情查询检查持久化的最终交付结果。
	deadline := time.Now().Add(3 * time.Second)
	for {
		archived := timingLifecycleData(t, tracker, reqID)
		if archived.Source == "database" {
			if archived.PrivacyScanJSON != summary.PrivacyScanJSON {
				t.Fatalf("持久化后最终摘要变化 %s", archived.PrivacyScanJSON)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("回填请求未完成归档")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPrivacyRestoreSummaryDeliveryStates(t *testing.T) {
	const okBody = `{"id":"fixture","output":[],"usage":{"input_tokens":2,"output_tokens":1}}`
	for _, tc := range []struct {
		name, path, body, want string
		code                   int
		unknownTool            int64
		disabled, disconnect   bool
	}{
		{name: "零回填", body: okBody, want: "completed"},
		{name: "整包工具未知标识原样交付", body: `{"output":[{"id":"fc","type":"function_call","arguments":"{\"value\":\"` + privacy.RestorePrefix + strings.Repeat("f", 32) + `}}\"}"}],"usage":{"input_tokens":2,"output_tokens":1}}`, want: "completed", unknownTool: 1},
		{name: "仅响应的关闭状态", body: okBody, disabled: true, want: "disabled"},
		{name: "compact无响应回填", path: "/v1/responses/compact", body: okBody, want: "not_applicable"},
		{name: "客户端中断", body: okBody, disconnect: true, want: "interrupted"},
		{name: "工具协议错误", body: `{"output":[{"id":"fc","type":"function_call","arguments":"{broken"}],"usage":{"input_tokens":2,"output_tokens":1}}`, want: "failed"},
		{name: "上游拒绝", code: 400, body: `{"error":{"message":"synthetic rejection"}}`, want: "upstream_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if tc.code != 0 {
					w.WriteHeader(tc.code)
				}
				io.WriteString(w, tc.body)
			}))
			t.Cleanup(up.Close)
			h, tracker := phaseAccountHandler(t, up.URL, 1)
			filter := newRestoreTestFilter(t)
			if tc.disabled {
				filter.Settings.Mode = privacy.ModeDisabled
			}
			h.SetPrivacyFilter(filter)
			path := tc.path
			if path == "" {
				path = "/v1/responses"
			}
			const reqID = "summary-state"
			req := privacySummaryRequest(path, `{"model":"gpt-test","input":"clean"}`, reqID)
			var w http.ResponseWriter = httptest.NewRecorder()
			if tc.disconnect {
				w = &failingBodyWriter{httptest.NewRecorder()}
			}
			h.ServeHTTP(w, req)
			data := timingLifecycleData(t, tracker, reqID)
			scan := gjson.Parse(data.PrivacyScanJSON)
			if scan.Get("restore.status").Str != tc.want || scan.Get("restore.text").Int() != 0 || scan.Get("restore.tool").Int() != 0 {
				t.Fatalf("终态 %s 的摘要异常 %s", data.Detail.Status, scan.Raw)
			}
			if scan.Get("restore.unknown_tool").Int() != tc.unknownTool || strings.Contains(scan.Raw, privacy.RestorePrefix) {
				t.Fatalf("未知工具标识的交付计数或摘要异常 %s", scan.Raw)
			}
			if tc.disconnect && (data.Detail.Status != "cancelled" || data.Detail.InputTokens != 2 || data.Detail.OutputTokens != 1) {
				t.Fatalf("客户端短写后应按原始 usage 结算一次: %+v", data.Detail)
			}
			if tc.disabled && len(scan.Map()) != 1 {
				t.Fatalf("未扫描时写入了请求侧字段 %s", scan.Raw)
			}
		})
	}
}
