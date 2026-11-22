package inputcomposition

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestAnalyzeProtocolInputs(t *testing.T) {
	for _, tc := range []struct{ route, body string }{
		{"/v1/responses", `{"instructions":"base","tools":[{"type":"function","name":"Read","description":"secret-description"}],"input":[{"role":"user","content":"# AGENTS.md instructions for /private/home"},{"role":"user","content":"<skill>secret-skill"},{"role":"user","content":"hello"},{"type":"function_call","call_id":"secret-id","name":"Read","arguments":"{\"path\":\"/private/home/main.go\"}"},{"type":"function_call_output","call_id":"secret-id","output":"secret-code"},{"type":"function_call_output","call_id":"unknown","output":"secret-result"}]}`},
		{"/v1/messages", `{"system":[{"type":"text","text":"base"}],"tools":[{"name":"Read"}],"messages":[{"role":"user","content":[{"type":"text","text":"# AGENTS.md instructions for /private/home"},{"type":"text","text":"<skill>secret-skill"},{"type":"text","text":"hello"}]},{"role":"assistant","content":[{"type":"tool_use","id":"secret-id","name":"Read","input":{"file_path":"/private/home/main.go"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"secret-id","content":[{"type":"text","text":"secret-code"}]},{"type":"tool_result","tool_use_id":"unknown","content":"secret-result"}]}]}`},
	} {
		s := Analyze(tc.route, []byte(tc.body))
		if s == nil || s.Partial {
			t.Fatal(tc.route, s)
		}
		groups := map[string]bool{}
		for _, item := range s.Items {
			groups[item.Group] = true
			if item.Group == "files" && item.Name != "main.go" {
				t.Fatal(item)
			}
		}
		if len(groups) != 7 {
			t.Fatal(groups)
		}
		raw, err := json.Marshal(s)
		if err != nil || strings.Contains(string(raw), "secret-") || strings.Contains(string(raw), "/private/") {
			t.Fatalf("摘要含原文: %s", raw)
		}
	}
}

func TestAnalyzeBoundsAndOpaqueInputs(t *testing.T) {
	input := make([]string, 0, 300)
	for i := range 300 {
		input = append(input, fmt.Sprintf(`{"name":"tool_%d"}`, i))
	}
	body := `{"type":"response.create","previous_response_id":"upstream-only","response":{"tools":[` + strings.Join(input, ",") + `],"input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,hidden"}]}]}}`
	s := Analyze("/v1/responses", []byte(body))
	if !s.Partial || len(s.Items) != maxNamedItems+1 {
		t.Fatal(s)
	}
	s = Analyze("/v1/responses/compact", make([]byte, maxBodyBytes+1))
	if s == nil || !s.Partial || len(s.Items) != 0 {
		t.Fatal(s)
	}
	if Analyze("/v1/messages", []byte("{")) != nil || Analyze("/v1/images/edits", []byte("{}")) != nil {
		t.Fatal("错误输入不应采集")
	}
}

func TestReadLabels(t *testing.T) {
	for _, tc := range []struct{ name, args, group, label string }{
		{"Read", `{"path":"C:\\project\\CLAUDE.md"}`, "memory", "CLAUDE.md"},
		{"Read", `{"path":"a/SKILL.md"}`, "skills", "SKILL.md"},
		{"functions.exec_command", `{"cmd":"cat src/example.go"}`, "files", "example.go"},
		{"exec_command", `{"cmd":"sed -n '16,32p' src/sample.py"}`, "files", "sample.py"},
		{"bash", `{"command":"head -n 50 /private/project/AGENTS.md"}`, "memory", "AGENTS.md"},
		{"exec_command", `{"cmd":"cat foo.go | head"}`, "results", "exec_command"},
		{"Read", `{"path":"/root/sk-sensitive.json"}`, "files", "文件内容"},
	} {
		body := fmt.Sprintf(`{"input":[{"type":"function_call","name":%q,"arguments":%s,"call_id":"x"},{"type":"function_call_output","call_id":"x","output":"value"}]}`, tc.name, tc.args)
		s := Analyze("/v1/responses", []byte(body))
		found := false
		for _, i := range s.Items {
			found = found || i.Group == tc.group && i.Name == tc.label
		}
		if !found {
			t.Fatal(tc, s)
		}
	}
}

func BenchmarkAnalyze(b *testing.B) {
	for _, size := range []int{64 << 10, 512 << 10, 2 << 20} {
		message := `{"role":"user","content":[{"type":"input_text","text":"` + strings.Repeat("text ", 200) + `"}]}`
		body := []byte(`{"model":"test","input":[` + strings.TrimSuffix(strings.Repeat(message+",", size/len(message)), ",") + `]}`)
		b.Run(fmt.Sprintf("%dKiB", size>>10), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				Analyze("/v1/responses", body)
			}
		})
	}
}

func TestExecAttribution(t *testing.T) {
	for _, tc := range []struct{ input, group, name string }{
		{`{"cmd":"cat /private/workspace/app.go"}`, "files", "app.go"},
		{`text((await tools.exec_command({cmd:"go test ./...",max_output_tokens:300})).output)`, "results", "测试输出"},
		{`const r = await tools.exec_command({cmd:'git diff -- README.md'}); text(r.output)`, "results", "Git 差异与提交内容"},
		{`text(await tools.exec_command({workdir:"/private/workspace",cmd:'rg needle src'}))`, "results", "文本搜索结果"},
		{`text(await Promise.allSettled([tools.exec_command({cmd:"cat a.go"}), tools.exec_command({cmd:"cat b.go"})]))`, "results", "批量命令结果"},
		{`text(await tools.exec_command({cmd:dynamic}))`, "results", "exec"},
		{`const r = await tools.exec_command({cmd:'git diff'}); text(other.output)`, "results", "exec"},
		{`text(await tools.exec_command({cmd:"cat main.go"})); text(other)`, "results", "exec"},
		{`text(await tools.exec_command({cmd:"cat main.go | head"}))`, "results", "exec"},
	} {
		for _, kind := range []string{"custom_tool_call", "function_call"} {
			call := map[string]any{"type": kind, "call_id": "c", "name": "exec"}
			if kind == "custom_tool_call" {
				call["input"] = tc.input
			} else {
				args := tc.input
				if !strings.HasPrefix(args, "{") {
					encoded, _ := json.Marshal(map[string]string{"code": args})
					args = string(encoded)
				}
				call["arguments"] = args
			}
			body, err := json.Marshal(map[string]any{"input": []any{call, map[string]string{"type": kind + "_output", "call_id": "c", "output": "abcd"}}})
			if err != nil {
				t.Fatal(err)
			}
			s := Analyze("/v1/responses", body)
			found := false
			for _, item := range s.Items {
				if item.Group == tc.group && item.Name == tc.name && item.Estimate == 1 {
					found = true
				}
			}
			if !found {
				t.Errorf("%s / %s: %+v", kind, tc.input, s.Items)
			}
		}
	}
}

func TestPartialAttributionReasons(t *testing.T) {
	body := []byte(`{"previous_response_id":"sensitive-id","input":[{"type":"reasoning","encrypted_content":"private-cipher","summary":[{"type":"summary_text","text":"abcdefgh"}]},{"type":"compaction","encrypted_content":"opaque"},{"type":"item_reference","id":"secret"},{"type":"message","role":"user","content":[{"type":"input_image","image_url":"private-source"},{"type":"input_file","file_id":"secret"},{"type":"input_image"}]}]}`)
	s := Analyze("/v1/responses", body)
	s.NoteWebSocketCreate()
	if !s.Partial || strings.Join(s.PartialReasons, ",") != "history_reference,encrypted_reasoning,compaction,image,attachment,ws_create" {
		t.Fatal(s)
	}
	if len(s.Items) != 1 || s.Items[0] != (Item{"chat", "思考记录", 2}) {
		t.Fatal(s.Items)
	}
	data, _ := json.Marshal(s)
	for _, value := range []string{"private", "sensitive", "secret", "abcdefgh"} {
		if strings.Contains(string(data), value) {
			t.Fatal("摘要泄漏内容", string(data))
		}
	}
}

var shortCreateBody = []byte(`{"type":"response.create","previous_response_id":"r","input":[{"type":"function_call_output","call_id":"c","output":"done"}]}`)

func BenchmarkShortCreate(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		Analyze("/v1/responses", shortCreateBody)
	}
}

func TestHistoryKeepsOnlyConversationAndCompletedOutput(t *testing.T) {
	root := []byte(`{"instructions":"private instruction","tools":[{"name":"OldTool"}],"input":[{"role":"developer","content":"persistent rule"},{"role":"user","content":"first question"}]}`)
	_, base := AnalyzeCreate(root, nil)
	frame := []byte(`{"response":{"output":[{"type":"function_call","call_id":"secret-call","name":"Read","arguments":"{\"path\":\"/private/dir/main.go\"}"}]}}`)
	parent := base.Completed(frame, 1000, nil)
	body := []byte(`{"type":"response.create","previous_response_id":"confirmed","tools":[{"name":"NewTool"}],"input":[{"type":"function_call_output","call_id":"secret-call","output":"abcd"},{"role":"user","content":"second"}]}`)
	child, next := AnalyzeCreate(body, parent)
	check := func(items []Item, group, name string, want int) {
		t.Helper()
		got := 0
		for _, i := range items {
			if i.Group == group && i.Name == name {
				got += i.Estimate
			}
		}
		if got != want {
			t.Fatalf("%s %s got %d want %d: %+v", group, name, got, want, items)
		}
	}
	check(child.Items, "files", "main.go", 1)
	check(child.Items, "chat", "第 2 轮对话", 2)
	check(child.HistoryItems, "chat", "第 1 轮对话", 4)
	check(child.HistoryItems, "system", "系统指令", 4)
	check(child.HistoryItems, "tools", "OldTool", 0)
	check(next.items, "tools", "NewTool", 0)
	// 从同一父响应分叉，两次都可独立匹配同一个调用。
	sibling, _ := AnalyzeCreate(body, parent)
	check(sibling.Items, "files", "main.go", 1)
	grandchild, _ := AnalyzeCreate(shortCreateBody, next.Completed([]byte(`{"response":{"output":[]}}`), 1000, nil))
	check(grandchild.HistoryItems, "files", "main.go", 1)
	// 显式 conversation: null 与省略字段等价，不应阻断父响应历史继承。
	withNullConversation, _ := AnalyzeCreate([]byte(`{"previous_response_id":"confirmed","conversation":null,"input":[{"role":"user","content":"third"}]}`), parent)
	check(withNullConversation.HistoryItems, "chat", "第 1 轮对话", 4)
	for _, field := range []string{`"input":[{"type":"compaction"}]`, `"input":[{"type":"item_reference","id":"z"}]`, `"truncation":"auto"`, `"conversation":"other"`} {
		s, _ := AnalyzeCreate([]byte(`{"previous_response_id":"r",`+field+`}`), parent)
		if len(s.HistoryItems) != 0 || !s.Partial {
			t.Fatal(field, s)
		}
	}
	if got := base.Completed([]byte(`{"response":{"output":[{"type":"compaction"}]}}`), 1000, nil); got != nil {
		t.Fatal("压缩终态保留了旧上下文")
	}
	for _, row := range []*Summary{child, grandchild} {
		raw, _ := json.Marshal(row)
		for _, secret := range []string{"private", "persistent rule", "secret-call", "confirmed", "first question", "abcd"} {
			if strings.Contains(string(raw), secret) {
				t.Fatal("摘要带有原文", string(raw))
			}
		}
	}
}

func TestSectionsNamespacesAndReadWrappers(t *testing.T) {
	body := []byte(`{"tools":[{"type":"namespace","name":"functions","tools":[{"name":"exec"},{"name":"write_stdin"}]},{"name":"mcp__github__get_issue"},{"name":"mcp__github__list"},{"type":"mcp","server_label":"sk-unsafe"}],"messages":[{"role":"user","content":"<system-reminder>Contents of /private/CLAUDE.md (project instructions):\n## rule\nThe following skills are available for use with the Skill tool:\n- a\n\nend</system-reminder>\nhello"},{"role":"assistant","content":"reply"},{"role":"user","content":[{"type":"tool_result","tool_use_id":"missing","content":"result"}]},{"role":"user","content":"next"}]}`)
	s := Analyze("/v1/messages", body)
	items := map[string]int{}
	for _, i := range s.Items {
		items[i.Name] += i.Estimate
	}
	for _, name := range []string{"第 1 轮对话", "第 2 轮对话", "工具组说明", "exec", "write_stdin", "MCP · github", "MCP 工具", "项目指令", "可用技能清单", "工具输出"} {
		if items[name] == 0 {
			t.Fatal(name, items)
		}
	}
	if items["第 3 轮对话"] != 0 || items["sk-unsafe"] != 0 {
		t.Fatal(items)
	}
	for _, tc := range []struct{ cmd, name string }{
		{"cd /private && nl -ba 'src/main.go'", "main.go"}, {"bash -lc 'head -n 5 src/main.go'", "main.go"},
		{"cat one.go two.go", "多文件读取"}, {"cat file.go | head", "exec"}, {"cd a && echo ok && cat wrong.go", "exec"},
	} {
		if got := commandLabel("exec", tc.cmd); got.name != tc.name {
			t.Fatal(tc, got)
		}
	}
}

func TestHistoryBoundsAndRecentTurns(t *testing.T) {
	msgs := make([]string, 50)
	for i := range msgs {
		msgs[i] = `{"role":"user","content":"abcd"}`
	}
	s, h := AnalyzeCreate([]byte(`{"input":[`+strings.Join(msgs, ",")+`]}`), nil)
	if len(s.Items) != namedTurns+1 {
		t.Fatal(s.Items)
	}
	var total int
	for _, i := range s.Items {
		total += i.Estimate
	}
	if total != 50 {
		t.Fatal(s.Items)
	}
	h = h.Completed([]byte(`{}`), 10, nil)
	total = 0
	for _, i := range h.items {
		total += i.Estimate
	}
	if total != 10 {
		t.Fatal(h.items)
	}
}

func BenchmarkShortCreateWithHistory(b *testing.B) {
	_, h := AnalyzeCreate([]byte(`{"input":[{"role":"developer","content":"instructions"},{"role":"user","content":"hello"},{"type":"function_call","call_id":"x","name":"Read","arguments":{"path":"main.go"}},{"type":"function_call_output","call_id":"x","output":"abcdefgh"}]}`), nil)
	h = h.Completed([]byte(`{"response":{"output":[{"type":"function_call","call_id":"c","name":"Bash","arguments":{"command":"go test ./..."}}]}}`), 1000, nil)
	b.ReportAllocs()
	for b.Loop() {
		AnalyzeCreate(shortCreateBody, h)
	}
}

func TestDeferredToolsAndServerResults(t *testing.T) {
	s, h := AnalyzeCreate([]byte(`{"input":[{"type":"additional_tools","tools":[{"name":"Read"}]},{"type":"tool_search_output","tools":[{"name":"Edit"}]}]}`), nil)
	if len(s.Items) != 2 || s.Partial || len(h.items) != 0 {
		t.Fatal(s, h)
	}
	s = Analyze("/v1/messages", []byte(`{"messages":[{"role":"assistant","content":[{"type":"server_tool_use","id":"x","name":"web_search","input":{}}]},{"role":"user","content":[{"type":"web_search_tool_result","tool_use_id":"x","content":[{"type":"web_search_result","title":"title","url":"url","encrypted_content":"private"}]}]}]}`))
	if !s.Partial || len(s.PartialReasons) != 1 || s.PartialReasons[0] != "sealed_tool_result" {
		t.Fatal(s)
	}
	for _, i := range s.Items {
		if i == (Item{"results", "web_search", 2}) {
			return
		}
	}
	t.Fatal(s.Items)
}

func TestToolCallNamesAndEchoScope(t *testing.T) {
	const calls = `[{"type":"custom_tool_call","name":"exec","input":"abcd"},{"type":"function_call","name":"Read","arguments":"xy"},{"type":"function_call","name":"sk-private","arguments":"x"}]`
	s, _ := AnalyzeCreate([]byte(`{"previous_response_id":"r","input":`+calls+`}`), nil)
	for _, want := range []Item{{"chat", "exec 工具调用", 3}, {"chat", "Read 工具调用", 2}, {"chat", "工具调用", 3}} {
		found := false
		for _, i := range s.Items {
			if i == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("缺少 %+v: %+v", want, s.Items)
		}
	}
	if s.ToolEcho != "unreported" {
		t.Fatal(s)
	}
	frame := []byte(`{"response":{"tools":[{"type":"function","name":"exec","description":"private documentation","parameters":{"type":"object"}}]}}`)
	if !s.ObserveToolEcho(frame) || s.ToolEcho != "reported" || len(s.EchoItems) != 1 || s.EchoItems[0].Name != "exec" {
		t.Fatal(s)
	}
	if s.ObserveToolEcho(frame) || s.ObserveToolEcho([]byte(`{"response":{"output":[]}}`)) {
		t.Fatal("重复或省略的回显改变了摘要")
	}
	if s.ObserveToolEcho(make([]byte, maxBodyBytes+1)) || s.ToolEcho != "reported" || len(s.EchoItems) != 1 {
		t.Fatal("超限终态帧清空了此前已采集的工具回显")
	}
	unreported, _ := AnalyzeCreate([]byte(`{"previous_response_id":"r","tools":null,"input":[]}`), nil)
	if unreported.ToolEcho != "unreported" {
		t.Fatalf("tools: null 应等待工具回显: %+v", unreported)
	}
	if !unreported.ObserveToolEcho(make([]byte, maxBodyBytes+1)) || unreported.ToolEcho != "unavailable" || len(unreported.EchoItems) != 0 {
		t.Fatalf("未采集回显时遇到超限帧应标记为 unavailable: %+v", unreported)
	}
	raw, _ := json.Marshal(s)
	if strings.Contains(string(raw), "private") {
		t.Fatal(string(raw))
	}
	if !s.ObserveToolEcho([]byte(`{"response":{"tools":[]}}`)) || s.ToolEcho != "empty" || len(s.EchoItems) != 0 {
		t.Fatal(s)
	}
	if !s.ObserveToolEcho([]byte(`{"response":{"tools":[{"type":"function","name":"Read"},null]}}`)) || s.ToolEcho != "partial" || len(s.EchoItems) != 1 {
		t.Fatal(s)
	}
	if !s.ObserveToolEcho([]byte(`{"response":{"tools":null}}`)) || s.ToolEcho != "unavailable" || len(s.EchoItems) != 0 {
		t.Fatal(s)
	}
	for _, body := range []string{`{"input":[]}`, `{"previous_response_id":"r","tools":[]}`, `{"previous_response_id":"r","tools":[{"name":"exec"}]}`, `{"previous_response_id":"r","input":[{"type":"additional_tools","tools":[{"name":"exec"}]}]}`} {
		s, _ := AnalyzeCreate([]byte(body), nil)
		if s.ObserveToolEcho(frame) || s.ToolEcho != "" {
			t.Fatalf("当前请求已有工具或并非续接: %+v", s)
		}
	}
}
