package privacy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"unicode/utf8"
)

// textSegment 表示 body 中一个可扫描的文本字段。
// Start/End 是原始 JSON 字符串 token（含双引号）的字节区间，Value 是解码后的字符串。
// 对非 JSON 文本，Start/End 覆盖整个 body，Raw 置 false。
type textSegment struct {
	Start  int // 含
	End    int // 不含
	Value  string
	IsJSON bool // true 表示区间是 JSON 字符串 token，替换时需要重新 escape
}

// pathElem JSON 路径元素：Key 为对象键；Index >= 0 时为数组下标。
type pathElem struct {
	Key   string
	Index int
}

// segmentMatcher 判断给定 JSON 路径上的字符串值是否属于可扫描文本字段
type segmentMatcher func(path []pathElem) bool

// walkerForPath 返回请求路径对应的字段匹配器；不支持的路径返回 nil。
func walkerForPath(requestPath string) segmentMatcher {
	switch requestPath {
	case "/v1/messages", "/v1/messages/count_tokens":
		return matchClaudeMessagesTextPath
	case "/v1/responses", "/v1/responses/compact":
		return matchCodexResponsesTextPath
	case "/v1/alpha/search":
		return matchCodexSearchTextPath
	case "/v1/images/generations", "/v1/images/edits":
		return matchOpenAIImagesGenerationsTextPath
	default:
		return nil
	}
}

// matchCodexSearchTextPath 匹配 Codex /v1/alpha/search 可扫描文本字段（rust-v0.153.4 SearchRequest）：
//  1. input（字符串）
//  2. input[].content（字符串）
//  3. input[].content[].text（最近对话消息的 input_text/output_text）
//  4. commands.search_query[].q / commands.image_query[].q
//  5. commands.find[].pattern
//
// id/model/settings、open/click/find 的 ref_id、recency/domains 等引用与控制字段不匹配；
// 看似 base64 或高熵的字符串不因此跳过。
func matchCodexSearchTextPath(path []pathElem) bool {
	switch len(path) {
	case 1:
		return path[0].Key == "input" && path[0].Index < 0
	case 3:
		return path[0].Key == "input" && path[1].Index >= 0 && path[2].Key == "content"
	case 4:
		if path[0].Key != "commands" || path[0].Index >= 0 || path[2].Index < 0 {
			return false
		}
		switch path[1].Key {
		case "search_query", "image_query":
			return path[3].Key == "q"
		case "find":
			return path[3].Key == "pattern"
		default:
			return false
		}
	case 5:
		return path[0].Key == "input" && path[1].Index >= 0 &&
			path[2].Key == "content" && path[3].Index >= 0 &&
			path[4].Key == "text"
	default:
		return false
	}
}

// matchOpenAIImagesGenerationsTextPath 匹配 Image API 的 prompt 字段。
func matchOpenAIImagesGenerationsTextPath(path []pathElem) bool {
	return len(path) == 1 && path[0].Key == "prompt" && path[0].Index < 0
}

// matchClaudeMessagesTextPath 匹配 Claude /v1/messages 可扫描文本字段：
//  1. system（字符串）
//  2. system[].text
//  3. messages[].content（字符串）
//  4. messages[].content[].text
//  5. messages[].content[] 中 tool_result 的 content（字符串）
//  6. messages[].content[] 中 tool_result 的 content[].text
//
// 按字段结构匹配而非读取兄弟字段 type：合法请求里 text/content 字段
// 只出现在对应类型的块上，结构匹配等价且无需前瞻。
// tools/tool_choice/metadata/model/stream 等路径天然不匹配。
func matchClaudeMessagesTextPath(path []pathElem) bool {
	switch len(path) {
	case 1:
		// system: "..."
		return path[0].Key == "system" && path[0].Index < 0
	case 3:
		// system[].text
		if path[0].Key == "system" && path[1].Index >= 0 && path[2].Key == "text" {
			return true
		}
		// messages[].content: "..."
		return path[0].Key == "messages" && path[1].Index >= 0 && path[2].Key == "content"
	case 5:
		if path[0].Key != "messages" || path[1].Index < 0 || path[2].Key != "content" || path[3].Index < 0 {
			return false
		}
		// messages[].content[].text 或 tool_result 的 content 字符串
		return path[4].Key == "text" || path[4].Key == "content"
	case 7:
		// messages[].content[].content[].text（tool_result 嵌套文本块）
		return path[0].Key == "messages" && path[1].Index >= 0 &&
			path[2].Key == "content" && path[3].Index >= 0 &&
			path[4].Key == "content" && path[5].Index >= 0 &&
			path[6].Key == "text"
	default:
		return false
	}
}

// matchCodexResponsesTextPath 匹配 Codex /v1/responses 可扫描文本字段。
// WS response.create 可能把请求放在 response 对象中；两种包形共用同一规则。
//  1. instructions（字符串）
//  2. input（字符串）
//  3. input[].content（字符串）
//  4. input[].content[].text（input_text/text）
//  5. input[].output（function_call_output 的输出字符串）
//  6. input[].output[].text（工具结果数组中的明文块）
//  7. input[].arguments / input[].input（回放的 function/custom 工具输入）
//  8. tools/additional_tools 中的 description、JSON schema description/default/examples
//
// encrypted_content/encrypted_output/encrypted_function_args、图片/文件、名称、属性键、
// enum、模型、引用与控制字段天然不匹配；不能因普通字符串看似密文就跳过。
func matchCodexResponsesTextPath(path []pathElem) bool {
	if len(path) > 1 && path[0].Key == "response" && path[0].Index < 0 {
		path = path[1:]
	}
	switch len(path) {
	case 1:
		return (path[0].Key == "instructions" || path[0].Key == "input") && path[0].Index < 0
	case 3:
		if path[0].Key == "input" && path[1].Index >= 0 {
			switch path[2].Key {
			case "content", "output", "arguments", "input":
				return true
			}
		}
		return isCodexToolArray(path[0]) && path[1].Index >= 0 && path[2].Key == "description"
	case 5:
		if path[0].Key == "input" && path[1].Index >= 0 && path[3].Index >= 0 && path[4].Key == "text" {
			return path[2].Key == "content" || path[2].Key == "output"
		}
	default:
	}
	return matchCodexToolSchemaTextPath(path)
}

func isCodexToolArray(elem pathElem) bool {
	return elem.Index < 0 && (elem.Key == "tools" || elem.Key == "additional_tools")
}

// matchCodexToolSchemaTextPath 只匹配工具 JSON schema 内明确可读的说明/示例值。
// properties 的键和 enum 值属于调用契约，不得改写。
func matchCodexToolSchemaTextPath(path []pathElem) bool {
	if len(path) < 4 || !isCodexToolArray(path[0]) || path[1].Index < 0 || path[2].Key != "parameters" {
		return false
	}
	last := path[len(path)-1]
	if last.Index < 0 && (last.Key == "description" || last.Key == "default") {
		return true
	}
	return len(path) >= 2 && path[len(path)-2].Key == "examples" && last.Index >= 0
}

// collectJSONTextSegments 用 offset-aware 方式收集 body 中可扫描文本字段。
// 只记录字符串 token 的原始字节区间，不重编码整个 JSON，
// 保证未命中字段 byte-identical。
func collectJSONTextSegments(body []byte, match segmentMatcher) ([]textSegment, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()

	type frame struct {
		isObject  bool
		expectKey bool
		key       string
		index     int
	}

	var (
		stack    []*frame
		segments []textSegment
	)
	prevOffset := int64(0)

	currentPath := func() []pathElem {
		path := make([]pathElem, 0, len(stack))
		for _, f := range stack {
			if f.isObject {
				path = append(path, pathElem{Key: f.key, Index: -1})
			} else {
				path = append(path, pathElem{Index: f.index})
			}
		}
		return path
	}

	// 值结束后推进所在容器的游标
	advanceContainer := func() {
		if len(stack) == 0 {
			return
		}
		top := stack[len(stack)-1]
		if top.isObject {
			top.expectKey = true
		} else {
			top.index++
		}
	}

	for {
		token, err := decoder.Token()
		if err == io.EOF {
			// 截断的 JSON（容器未闭合）Token() 也可能返回干净 EOF，这里显式校验
			if len(stack) != 0 {
				return nil, fmt.Errorf("invalid json body: unexpected end of input")
			}
			break
		}
		if err != nil {
			return nil, fmt.Errorf("invalid json body: %w", err)
		}
		offset := decoder.InputOffset()

		switch tok := token.(type) {
		case json.Delim:
			switch tok {
			case '{':
				stack = append(stack, &frame{isObject: true, expectKey: true})
			case '[':
				stack = append(stack, &frame{isObject: false, index: 0})
			case '}', ']':
				stack = stack[:len(stack)-1]
				advanceContainer()
			}
		case string:
			top := (*frame)(nil)
			if len(stack) > 0 {
				top = stack[len(stack)-1]
			}
			if top != nil && top.isObject && top.expectKey {
				top.key = tok
				top.expectKey = false
				prevOffset = offset
				continue
			}
			// 字符串值：定位 token 原始字节区间（前一 token 末尾到当前末尾之间，
			// 仅有空白/逗号/冒号，首个双引号即 token 起点）
			if match != nil && match(currentPath()) {
				gap := body[prevOffset:offset]
				quote := bytes.IndexByte(gap, '"')
				if quote < 0 {
					return nil, fmt.Errorf("failed to locate string token at offset %d", prevOffset)
				}
				segments = append(segments, textSegment{
					Start:  int(prevOffset) + quote,
					End:    int(offset),
					Value:  tok,
					IsJSON: true,
				})
			}
			advanceContainer()
		default:
			// number/bool/null 值
			advanceContainer()
		}
		prevOffset = offset
	}

	return segments, nil
}

// appendJSONStringBody 按 JSON string 规则 escape 并追加（不含首尾引号）。
// 不对 <、>、& 做 HTML escape，避免改变上游收到的语义。
func appendJSONStringBody(dst []byte, s string) []byte {
	for i := 0; i < len(s); {
		b := s[i]
		if b < 0x20 {
			switch b {
			case '\b':
				dst = append(dst, '\\', 'b')
			case '\f':
				dst = append(dst, '\\', 'f')
			case '\n':
				dst = append(dst, '\\', 'n')
			case '\r':
				dst = append(dst, '\\', 'r')
			case '\t':
				dst = append(dst, '\\', 't')
			default:
				dst = fmt.Appendf(dst, "\\u%04x", b)
			}
			i++
			continue
		}
		if b == '"' || b == '\\' {
			dst = append(dst, '\\', b)
			i++
			continue
		}
		if b < utf8.RuneSelf {
			dst = append(dst, b)
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			// 无效 UTF-8 字节按 � 输出，避免产生非法 JSON
			dst = append(dst, []byte(`�`)...)
			i++
			continue
		}
		dst = append(dst, s[i:i+size]...)
		i += size
	}
	return dst
}

// encodeJSONString 返回带引号的 JSON 字符串 token
func encodeJSONString(s string) []byte {
	out := make([]byte, 0, len(s)+2)
	out = append(out, '"')
	out = appendJSONStringBody(out, s)
	out = append(out, '"')
	return out
}

// replaceSegments 按段替换重建 body。replacements 的 key 是段下标，
// value 是该段替换后的解码字符串；未替换的段保持原始字节。
func replaceSegments(body []byte, segments []textSegment, replacements map[int]string) []byte {
	if len(replacements) == 0 {
		return body
	}
	out := make([]byte, 0, len(body)+64)
	cursor := 0
	for i, seg := range segments {
		replaced, ok := replacements[i]
		if !ok {
			continue
		}
		out = append(out, body[cursor:seg.Start]...)
		if seg.IsJSON {
			out = append(out, encodeJSONString(replaced)...)
		} else {
			out = append(out, replaced...)
		}
		cursor = seg.End
	}
	out = append(out, body[cursor:]...)
	return out
}

func (p pathElem) String() string {
	if p.Index >= 0 {
		return "[" + strconv.Itoa(p.Index) + "]"
	}
	return p.Key
}
