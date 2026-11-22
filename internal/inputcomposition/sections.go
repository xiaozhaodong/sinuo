package inputcomposition

import (
	"strings"

	"github.com/tidwall/gjson"
)

func (c *collector) toolDefinition(tool gjson.Result, expand bool) {
	if !tool.IsObject() {
		return
	}
	name := tool.Get("name").Str
	if name == "" {
		name = tool.Get("function.name").Str
	}
	if name == "" {
		name = tool.Get("type").Str
	}
	if expand && name == "functions" {
		if inner := tool.Get("tools"); inner.IsArray() {
			overhead := len(tool.Raw)
			inner.ForEach(func(_, child gjson.Result) bool {
				if child.IsObject() {
					overhead -= len(child.Raw)
					c.toolDefinition(child, false)
				}
				return true
			})
			c.add(label{"tools", "工具组说明"}, overhead)
			return
		}
	}
	server := tool.Get("server_label").Str
	if name == "mcp" && server != "" {
		name = mcpName(server)
	} else if rest, ok := strings.CutPrefix(name, "mcp__"); ok {
		server, _, _ = strings.Cut(rest, "__")
		name = mcpName(server)
	} else {
		name = safeTool(name)
	}
	c.add(label{"tools", name}, len(tool.Raw))
}

func mcpName(server string) string {
	if safeIdentifier(server, 64) {
		return "MCP · " + server
	}
	return "MCP 工具"
}

func headerFile(header string) string {
	file, _, _ := strings.Cut(header, " (")
	return strings.TrimSuffix(file, ":")
}

const claudeSkills = "The following skills are available for use with the Skill tool:"

var promptTags = []struct {
	open, close string
	group, name string
}{
	{"<skills_instructions>", "</skills_instructions>", "skills", "技能说明"},
	{"<available_skills>", "</available_skills>", "skills", "可用技能清单"},
	{"<skill>", "</skill>", "skills", "技能说明"},
	{"<INSTRUCTIONS>", "</INSTRUCTIONS>", "memory", "AGENTS.md"},
	{"<environment_context>", "</environment_context>", "system", "运行环境"},
	{"<system-reminder>", "</system-reminder>", "system", "系统提醒"},
}

func (c *collector) text(l label, text string) {
	if l.name == "助手消息" {
		c.addText(l, text)
		return
	}
	if !strings.Contains(text, "<") && !strings.Contains(text, claudeSkills) {
		c.addText(promptLabel(l, text), text)
		return
	}
	c.promptSections(l, text, true)
}

// 只拆行首的协议区段。代码围栏里的示例保留在原消息；每个字节只归入一个分类。
func (c *collector) promptSections(base label, text string, nested bool) {
	start, fence := 0, ""
	// 下一个候选区段必须从新行开始，避免重复扫描长行中的相邻标签。
	nextLine := func(end int) int {
		if end == len(text) || end > 0 && text[end-1] == '\n' {
			return end
		}
		if n := strings.IndexByte(text[end:], '\n'); n >= 0 {
			return end + n + 1
		}
		return len(text)
	}
	add := func(l label, part string) {
		if strings.TrimSpace(part) != "" {
			c.addText(promptLabel(l, part), part)
		}
	}
	for pos := 0; pos < len(text); {
		line, _, _ := strings.Cut(text[pos:], "\n")
		endLine := min(pos+len(line)+1, len(text))
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "```") || strings.HasPrefix(trim, "~~~") {
			if fence == trim[:3] {
				fence = ""
			} else if fence == "" {
				fence = trim[:3]
			}
			pos = endLine
			continue
		}
		if fence != "" {
			pos = endLine
			continue
		}
		if trim == claudeSkills {
			end := len(text)
			if n := strings.Index(text[endLine:], "\n\n"); n >= 0 {
				end = endLine + n
			}
			add(base, text[start:pos])
			c.add(label{"skills", "可用技能清单"}, end-pos)
			start, pos = end, end
			continue
		}
		if !strings.HasPrefix(trim, "<") {
			pos = endLine
			continue
		}
		matched := false
		for _, tag := range promptTags {
			if !strings.HasPrefix(trim, tag.open) {
				continue
			}
			content := pos + strings.Index(line, tag.open) + len(tag.open)
			stop := strings.Index(text[content:], tag.close)
			if stop < 0 {
				// 未闭合区段只保留原协议标签，不反复搜索余下的大段文本。
				add(base, text[start:])
				return
			}
			end := content + stop + len(tag.close)
			add(base, text[start:pos])
			l := promptLabel(label{tag.group, tag.name}, text[pos:end])
			if nested && (tag.open == "<system-reminder>" || tag.open == "<INSTRUCTIONS>") {
				c.add(l, content-pos+len(tag.close))
				c.promptSections(l, text[content:content+stop], false)
			} else {
				c.add(l, end-pos)
			}
			start, pos, matched = end, nextLine(end), true
			break
		}
		if !matched {
			pos = endLine
		}
	}
	add(base, text[start:])
}

// 工具结果可能混有明文、工具引用及加密来源。仅可见字段参与大小估算。
func (c *collector) toolResultPart(l label, part gjson.Result) {
	if part.Type == gjson.String {
		c.add(l, len(part.Str))
		return
	}
	kind := part.Get("type").Str
	switch kind {
	case "web_search_result":
		c.add(l, len(part.Get("title").Str)+len(part.Get("url").Str))
		if part.Get("encrypted_content").Exists() {
			c.summary.notePartial("sealed_tool_result")
		}
	case "web_fetch_result":
		c.add(l, len(part.Get("url").Str))
		source := part.Get("content.source")
		if source.Get("type").Str == "text" {
			c.add(l, len(source.Get("data").Str))
		} else {
			c.noteOpaque("document")
		}
	case "tool_reference":
		c.add(l, len(part.Get("tool_name").Str))
	default:
		if t := part.Get("text"); t.Type == gjson.String {
			c.add(l, len(t.Str))
		} else {
			c.noteOpaque(kind)
		}
	}
}
