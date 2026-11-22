// Package inputcomposition 从客户端输入生成有界分类摘要，供单请求详情使用。
package inputcomposition

import (
	"encoding/json"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
)

const (
	maxBodyBytes  = 8 << 20
	maxNamedItems = 16 // 每类额外保留一个合并条目
	maxCalls      = 256
)

// Summary 仅包含显示标签与估算量。收到 usage 后由详情使用同一条记录的完整输入校准。
type Summary struct {
	Version        int      `json:"version"`
	Items          []Item   `json:"items"`
	HistoryItems   []Item   `json:"history_items,omitempty"`
	ToolEcho       string   `json:"tool_echo,omitempty"`
	EchoItems      []Item   `json:"echo_items,omitempty"`
	Partial        bool     `json:"partial,omitempty"` // 历史续接、附件或其他上游持有的内容不可估算
	PartialReasons []string `json:"partial_reasons,omitempty"`
	Window         int64    `json:"window,omitempty"`
	WindowSource   string   `json:"window_source,omitempty"`
	WindowModel    string   `json:"window_model,omitempty"`
}

type Item struct {
	Group    string `json:"group"`
	Name     string `json:"name"`
	Estimate int    `json:"estimate"`
}

type label struct{ group, name string }
type collector struct {
	weights   map[string]map[string]int
	calls     map[string]label
	summary   *Summary
	turn      int
	turnBytes [namedTurns]int
	newUser   bool
}

// Analyze 仅处理生成与压缩输入。请求正文、调用 ID、工具参数和绝对路径均不随摘要保存。
func Analyze(route string, body []byte) *Summary {
	summary, _ := analyze(route, body, nil, false)
	return summary
}

func analyze(route string, body []byte, parent *History, keep bool) (*Summary, *History) {
	if route != "/v1/responses" && route != "/v1/responses/compact" && route != "/v1/messages" {
		return nil, nil
	}
	if len(body) > maxBodyBytes {
		return &Summary{Version: 1, Items: []Item{}, Partial: true, PartialReasons: []string{"body_limit"}}, nil
	}
	if !json.Valid(body) {
		return nil, nil
	}
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return nil, nil
	}
	fields := inputFields(root)
	if fields["type"].Str == "response.create" && fields["response"].IsObject() {
		for k, v := range inputFields(fields["response"]) {
			fields[k] = v
		}
	}
	result := &Summary{Version: 1, Items: []Item{}}
	c := collector{weights: make(map[string]map[string]int), calls: make(map[string]label), summary: result}
	if fields["previous_response_id"].Str != "" || fields["conversation"].Type != gjson.Null {
		result.notePartial("history_reference")
	}
	input := fields["input"]
	if route == "/v1/messages" {
		input = fields["messages"]
	}
	if parent != nil && fields["previous_response_id"].Str != "" && fields["conversation"].Type == gjson.Null &&
		fields["truncation"].Str != "auto" && !containsHistoryBoundary(input) {
		result.HistoryItems = parent.items
		for _, reason := range parent.reasons {
			result.notePartial(reason)
		}
		c.turn = parent.turn
		for id, l := range parent.calls {
			c.calls[id] = l
		}
	}
	if fields["truncation"].Str == "auto" {
		result.notePartial("auto_truncation")
	}
	if input.Type == gjson.String {
		c.newUser = true
		c.text(label{"chat", "用户消息"}, input.Str)
	} else if input.IsArray() {
		input.ForEach(func(_, entry gjson.Result) bool { c.message(entry); return true })
	} else if input.Exists() {
		c.summary.notePartial("unknown_block")
	}

	var next *History
	if keep && fields["truncation"].Str != "auto" {
		next = c.history(result.HistoryItems)
	}
	// 顶层工具和 instructions 只属于这次请求。只有 input 消息能进入后继响应的历史。
	c.content(fields["instructions"], label{"system", "系统指令"})
	c.content(fields["system"], label{"system", "系统指令"})
	fields["tools"].ForEach(func(_, tool gjson.Result) bool { c.toolDefinition(tool, true); return true })
	if keep && fields["previous_response_id"].Str != "" && fields["tools"].Type == gjson.Null && len(c.weights["tools"]) == 0 {
		result.ToolEcho = "unreported"
	}
	result.Items = c.items()
	return result, next
}

// ObserveToolEcho 只补本次响应明确回显的工具，按回显对象本身的大小估算。
// 完成帧可以替换或清空创建帧提供的清单。省略字段或超限时沿用已观测到的回显。
func (s *Summary) ObserveToolEcho(frame []byte) bool {
	if s.ToolEcho == "" {
		return false
	}
	if len(frame) > maxBodyBytes {
		if s.ToolEcho != "unreported" {
			return false
		}
		s.ToolEcho, s.EchoItems = "unavailable", nil
		return true
	}
	tools := gjson.GetBytes(frame, "response.tools")
	if !tools.Exists() {
		return false
	}
	state := "unavailable"
	var items []Item
	if tools.IsArray() {
		state = "empty"
		c := collector{weights: make(map[string]map[string]int)}
		tools.ForEach(func(_, tool gjson.Result) bool {
			if !tool.IsObject() || (tool.Get("type").Str == "" && tool.Get("name").Str == "" && tool.Get("function.name").Str == "") {
				state = "partial"
				return true
			}
			c.toolDefinition(tool, true)
			return true
		})
		items = c.items()
		if len(items) > 0 && state != "partial" {
			state = "reported"
		}
	}
	if s.ToolEcho == state && slices.Equal(s.EchoItems, items) {
		return false
	}
	s.ToolEcho, s.EchoItems = state, items
	return true
}

func (c *collector) items() []Item {
	items := make([]Item, 0, len(c.weights)+min(c.turn, namedTurns))
	for t := max(1, c.turn-namedTurns+1); t <= c.turn; t++ {
		if n := c.turnBytes[t%namedTurns]; n > 0 {
			items = append(items, Item{"chat", turnLabel(t), (n + 3) / 4})
		}
	}

	for group, entries := range c.weights {
		for name, weight := range entries {
			items = append(items, Item{group, strings.Clone(name), (weight + 3) / 4})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.Estimate != b.Estimate {
			return a.Estimate > b.Estimate
		}
		if a.Group != b.Group {
			return a.Group < b.Group
		}
		return a.Name < b.Name
	})
	return items
}

// 单遍选取字段，避免每查一个可选字段都重新扫描整段历史。
func inputFields(root gjson.Result) map[string]gjson.Result {
	fields := make(map[string]gjson.Result, 8)
	root.ForEach(func(k, v gjson.Result) bool {
		switch k.Str {
		case "type", "response", "previous_response_id", "conversation", "truncation", "instructions", "system", "input", "messages", "tools":
			fields[k.Str] = v
		}
		return true
	})
	return fields
}

// 按可见 UTF-8 文本字节估算。超出条目预算仍计入分类总量。
func (c *collector) add(l label, size int) {
	if size == 0 {
		return
	}
	m := c.weights[l.group]
	if m == nil {
		m = make(map[string]int)
		c.weights[l.group] = m
	}
	if _, exists := m[l.name]; !exists && len(m) >= maxNamedItems {
		l.name = "其他条目"
	}
	m[l.name] += size
}

func promptLabel(l label, s string) label {
	prefix := strings.TrimSpace(s)
	switch {
	case strings.HasPrefix(prefix, "# AGENTS.md instructions"), strings.HasPrefix(prefix, "<INSTRUCTIONS>"):
		l = label{"memory", "AGENTS.md"}
	case strings.HasPrefix(prefix, "<system-reminder>"):
		header, _, _ := strings.Cut(strings.TrimSpace(strings.TrimPrefix(prefix, "<system-reminder>")), "\n")
		if strings.HasPrefix(header, "Contents of ") && len(header) < 2048 {
			switch {
			case strings.HasSuffix(headerFile(header), "/CLAUDE.md"), strings.HasSuffix(headerFile(header), "/AGENTS.md"):
				l = label{"memory", "项目指令"}
			case strings.HasSuffix(headerFile(header), "/SKILL.md"):
				l = label{"skills", "SKILL.md"}
			default:
				l = label{"system", "系统提醒"}
			}
		} else {
			l = label{"system", "系统提醒"}
		}
	case strings.HasPrefix(prefix, "<skill>"), strings.HasPrefix(prefix, "<skills_instructions>"):
		l = label{"skills", "技能说明"}
	case strings.HasPrefix(prefix, "<environment_context>"):
		l = label{"system", "运行环境"}
	}
	return l
}

func (c *collector) message(m gjson.Result) {
	l := label{"chat", "用户消息"}
	role := m.Get("role").Str
	c.newUser = role == "user"
	switch role {
	case "system", "developer":
		l = label{"system", "系统指令"}
	case "assistant":
		l.name = "助手消息"
	}
	if content := m.Get("content"); content.Exists() && (role != "" || m.Get("type").Str == "message") {
		c.content(content, l)
		return
	}
	c.block(m, l)
}

func (c *collector) content(v gjson.Result, l label) {
	if v.Type == gjson.String {
		c.text(l, v.Str)
		return
	}
	if v.IsArray() {
		v.ForEach(func(_, part gjson.Result) bool { c.block(part, l); return true })
	} else if v.Exists() {
		c.block(v, l)
	}
}

func (s *Summary) notePartial(reason string) {
	s.Partial = true
	if !slices.Contains(s.PartialReasons, reason) {
		s.PartialReasons = append(s.PartialReasons, reason)
	}
}

// NoteWebSocketCreate 标记采集范围，连接上的附加帧可能独立于当前 create。
func (s *Summary) NoteWebSocketCreate() { s.notePartial("ws_create") }

func (c *collector) noteOpaque(kind string) {
	switch kind {
	case "image", "input_image":
		c.summary.notePartial("image")
	case "document", "input_file":
		c.summary.notePartial("attachment")
	case "audio", "input_audio":
		c.summary.notePartial("audio")
	case "compaction", "compaction_summary":
		c.summary.notePartial("compaction")
	case "redacted_thinking":
		c.summary.notePartial("redacted_thinking")
	case "item_reference":
		c.summary.notePartial("history_reference")
	default:
		c.summary.notePartial("unknown_block")
	}
}

func (c *collector) block(v gjson.Result, l label) {
	kind := v.Get("type").Str
	switch kind {
	case "additional_tools":
		v.Get("tools").ForEach(func(_, t gjson.Result) bool { c.toolDefinition(t, true); return true })
	case "text", "input_text", "output_text":
		c.text(l, v.Get("text").Str)
	case "function_call", "tool_use", "server_tool_use", "custom_tool_call", "local_shell_call", "tool_search_call":
		id, args := v.Get("call_id").Str, v.Get("arguments")
		if id == "" {
			id = v.Get("id").Str
		}
		if !args.Exists() {
			args = v.Get("input")
		}
		name := safeTool(v.Get("name").Str)
		if kind == "tool_search_call" && v.Get("name").Str == "" {
			name = "工具检索"
		}
		if kind == "local_shell_call" {
			name, args = "local_shell", v.Get("action")
		}
		callName := "工具调用"
		if name != "工具" {
			callName = name + " 工具调用"
		}
		c.add(label{"chat", callName}, len(args.Raw)+len(name))
		if id != "" && len(id) <= 256 && len(c.calls) < maxCalls {
			c.calls[id] = resultLabel(name, args)
		}
	case "function_call_output", "custom_tool_call_output", "local_shell_call_output", "tool_result", "tool_search_output", "web_search_tool_result", "web_fetch_tool_result":
		if kind == "tool_search_output" {
			v.Get("tools").ForEach(func(_, t gjson.Result) bool { c.toolDefinition(t, true); return true })
		}
		id, out := v.Get("call_id").Str, v.Get("output")
		if id == "" {
			id = v.Get("tool_use_id").Str
			if id == "" {
				id = v.Get("id").Str
			}
		}
		if !out.Exists() {
			out = v.Get("content")
		}
		result, ok := c.calls[id]
		if !ok {
			result = label{"results", "工具输出"}
			switch kind {
			case "web_search_tool_result":
				result.name = "web_search"
			case "web_fetch_tool_result":
				result.name = "web_fetch"
			case "tool_search_output":
				result.name = "工具检索"
			}
		}
		delete(c.calls, id)
		if out.IsArray() {
			out.ForEach(func(_, part gjson.Result) bool { c.toolResultPart(result, part); return true })
		} else if out.Exists() {
			c.toolResultPart(result, out)
		}
	case "reasoning":
		// 可读推理和摘要计入对话；密文及历史引用没有可用的明文长度。
		visible := false
		for _, field := range []string{"summary", "content"} {
			v.Get(field).ForEach(func(_, part gjson.Result) bool {
				if t := part.Get("text"); t.Type == gjson.String && len(t.Str) > 0 {
					c.add(label{"chat", "思考记录"}, len(t.Str))
					visible = true
				}
				return true
			})
		}
		if v.Get("encrypted_content").Str != "" {
			c.summary.notePartial("encrypted_reasoning")
		} else if !visible {
			c.summary.notePartial("reasoning_history")
		}
	case "thinking":
		c.add(label{"chat", "思考记录"}, len(v.Get("thinking").Str))
	default:
		c.noteOpaque(v.Get("type").Str)
	}
}

func safeTool(name string) string {
	if !safeIdentifier(name, 64) {
		return "工具"
	}
	return name
}

func safeIdentifier(s string, limit int) bool {
	if s == "" || len(s) > limit || strings.Contains(s, "..") {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
			return false
		}
	}
	// 拒绝看似密钥或散列值的名称，保留常用的短工具名与源码文件名。
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == '_' || r == '-' || r == '.' }) {
		if len(part) >= 24 {
			return false
		}
	}
	lower := strings.ToLower(s)
	for _, prefix := range []string{"sk-", "sk_", "ghp_", "github_pat_", "akia", "eyj", "xox"} {
		if strings.Contains(lower, prefix) {
			return false
		}
	}
	return true
}

func resultLabel(name string, args gjson.Result) label {
	tool := strings.TrimPrefix(strings.ToLower(name), "functions.")
	if len(args.Raw) > 8192 {
		return label{"results", name}
	}
	if tool == "exec" {
		return execOutputLabel(name, args)
	}
	if args.Type == gjson.String {
		args = gjson.Parse(args.Str)
	}
	switch tool {
	case "read", "read_file", "readfile", "read_text_file":
		file := args.Get("file_path").Str
		if file == "" {
			file = args.Get("path").Str
		}
		return fileLabel(file)
	case "local_shell":
		cmd := args.Get("command").Array()
		if len(cmd) == 3 && shellWrapper(cmd[0].Str, cmd[1].Str) {
			return commandLabel(name, cmd[2].Str)
		}
		return label{"results", name}
	case "exec_command", "bash":
		return commandLabel(name, shellCommand(args))
	default:
		return label{"results", name}
	}
}

func fileLabel(file string) label {
	base := path.Base(strings.ReplaceAll(file, "\\", "/"))
	switch strings.ToLower(base) {
	case "agents.md", "claude.md", "memory.md":
		return label{"memory", base}
	case "skill.md":
		return label{"skills", "SKILL.md"}
	}
	if safeIdentifier(base, 64) && !strings.HasPrefix(base, ".") {
		switch strings.ToLower(path.Ext(base)) {
		case ".go", ".js", ".jsx", ".ts", ".tsx", ".md", ".py", ".rs", ".java", ".css", ".html", ".sql", ".json", ".yaml", ".yml", ".toml", ".c", ".cpp", ".h", ".sh", ".vue", ".swift", ".kt", ".txt":
			return label{"files", base}
		}
	}
	return label{"files", "文件内容"}
}

func digits(s string) bool {
	return s != "" && strings.Trim(s, "0123456789") == ""
}
