package privacy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

// 切段的同时收集续接和签名字段。先暂存候选路径，等对象结束后再判定类型，字段顺序不影响决策。
type restoreInput struct {
	wrapped                            *restoreInput
	claude                             bool
	conversationObject, conversationID bool
	items                              map[int]*restoreInputItem
	hasEncryptedFunction               bool
	sealedThinking                     map[[2]int]struct{}
}
type restoreInputItem struct {
	kind, id, role                           string
	call, name, namespace, cipher, arguments string
	invalidNamespace                         bool
	hasTextInput, hasContent                 bool
}

func (r *restoreInput) observe(path []pathElem, token any) error {
	if r.wrapped != nil && len(path) > 0 && path[0].Key == "response" {
		if len(path) == 1 && token != json.Delim('{') {
			return requestFormatError("response 必须是对象")
		}
		return r.wrapped.observe(path[1:], token)
	}
	text, isString := token.(string)
	// 兼容上游可能返回缺失或空签名的思考块；其他签名形式保守地按不可改写处理。
	// 在同次遍历中按消息与内容块收集，字段前后顺序不影响最终判断。
	if r.claude && len(path) == 5 && path[0].Key == "messages" && path[1].Index >= 0 &&
		path[2].Key == "content" && path[3].Index >= 0 && path[4].Key == "signature" && (!isString || text != "") {
		if r.sealedThinking == nil {
			r.sealedThinking = make(map[[2]int]struct{})
		}
		r.sealedThinking[[2]int{path[1].Index, path[3].Index}] = struct{}{}
	}
	if len(path) == 1 {
		if path[0].Key == "stream" {
			if _, ok := token.(bool); !ok {
				return requestFormatError("stream 必须是布尔值")
			}
		}
		if !r.claude {
			switch path[0].Key {
			case "response":
				return requestFormatError("HTTP 请求不支持 response 包装")
			case "previous_response_id", "conversation":
				if token == json.Delim('{') && path[0].Key == "conversation" {
					r.conversationObject = true
				}
				if token != nil && !isString && !(path[0].Key == "conversation" && token == json.Delim('{')) {
					return requestFormatError("历史引用字段类型错误")
				}
			}
		}
	}
	if !r.claude && len(path) == 2 && path[0].Key == "conversation" && path[1].Key == "id" {
		if !isString || text == "" {
			return requestFormatError("conversation.id 必须是非空字符串")
		}
		r.conversationID = true
	}
	if len(path) < 3 || path[1].Index < 0 {
		return nil
	}
	if r.claude && path[0].Key != "messages" || !r.claude && path[0].Key != "input" {
		return nil
	}
	switch path[2].Key {
	case "encrypted_function_args", "encrypted_output":
		// 空数组只出现起始符；非空数组还会报告元素路径，包括值为 null 的元素。
		if len(path) > 3 || token != nil && token != "" && token != json.Delim('[') {
			return fmt.Errorf("unsupported encrypted tool input")
		}
		return nil
	}
	index := path[1].Index
	if r.items[index] == nil {
		r.items[index] = &restoreInputItem{}
	}
	item := r.items[index]
	if len(path) == 3 {
		switch path[2].Key {
		case "type":
			item.kind = text
		case "id":
			item.id = text
		case "role":
			item.role = text
		case "content":
			item.hasContent = isString || token == json.Delim('[')
		case "input":
			item.hasTextInput = isString
		case "call_id":
			item.call = text
		case "name":
			item.name = text
		case "namespace":
			item.namespace, item.invalidNamespace = text, !isString && token != nil
		case "arguments":
			item.arguments = text
		case "encrypted_content":
			if !isString && token != nil {
				return requestFormatError("encrypted_content 必须是字符串")
			}
			item.cipher = text
		}
	}

	return nil
}

func (r *restoreInput) finish() *PolicyError {
	if r.wrapped != nil {
		if e := r.wrapped.finish(); e != nil {
			return e
		}
	}
	if r.conversationObject && !r.conversationID {
		return invalidRequest("conversation 缺少会话标识")
	}
	for _, item := range r.items {
		if item.kind == "item_reference" && item.id == "" {
			return invalidRequest("item_reference 缺少历史标识")
		}
		if item.cipher == "" || item.kind == "reasoning" || item.kind == "compaction" {
			continue
		}
		if r.claude {
			return restoreError("无法识别加密历史项")
		}
		// 输出重放密文不参与文本改写；可读部分沿用各自的 Responses 扫描路径。
		switch item.kind {
		case "message":
			if item.role != "assistant" || !item.hasContent {
				return restoreError("无法识别加密历史项")
			}
		case "custom_tool_call":
			if item.call == "" || item.name == "" || !item.hasTextInput || item.invalidNamespace {
				return restoreError("加密自定义工具历史缺少有效的调用标识或文本输入")
			}
		case "function_call":
			if item.call == "" || item.name == "" || item.arguments == "" || item.invalidNamespace {
				return restoreError("加密工具历史缺少有效的调用标识或完整参数")
			}
			r.hasEncryptedFunction = true
		default:
			return restoreError("无法识别加密历史项")
		}
	}

	return nil
}

// 参数已按普通工具规则逐个扫描内层 JSON 字符串。已知记录若与改写结果冲突，拒绝本次提交。
func (r *restoreInput) verifyFunctionReplays(view *RestoreView, body []byte, path string) *PolicyError {
	if r.hasEncryptedFunction {
		var failure *PolicyError
		gjson.GetBytes(body, path).ForEach(func(index, value gjson.Result) bool {
			item := r.items[int(index.Int())]
			if item == nil || item.cipher == "" || item.kind != "function_call" {
				return true
			}
			args := value.Get("arguments").Str
			if args != item.arguments && view.conflictsWithFunctionReplay(item.call, item.name, item.namespace, item.cipher, args) {
				failure = restoreError("重新遮蔽的加密工具历史与已记录的调用不一致")
				return false
			}
			return true
		})
		if failure != nil {
			return failure
		}
	}
	if r.wrapped != nil {
		return r.wrapped.verifyFunctionReplays(view, body, "response.input")
	}
	return nil
}

func claudeThinking(path []pathElem) bool {
	return len(path) == 5 && path[0].Key == "messages" && path[1].Index >= 0 && path[2].Key == "content" && path[3].Index >= 0 && path[4].Key == "thinking"
}

func reasoningText(path []pathElem) bool {
	return len(path) >= 3 && path[0].Key == "input" && path[1].Index >= 0 &&
		(path[2].Key == "summary" || path[2].Key == "content") && path[len(path)-1].Key != "type"
}

func (s *Snapshot) processRestoreRequest(req Request, body []byte, result *ApplyResult, candidates []int) *PolicyError {
	if len(s.RestoreKey) != 32 || s.CompileError != "" {
		return restoreError("可逆脱敏密钥或规则快照不可用")
	}
	if s.Dict == nil {
		return restoreError("进程回填字典不可用")
	}
	if !utf8.Valid(body) {
		return invalidRequest("请求需要有效 UTF-8")
	}
	replay := restoreInput{claude: isClaudeMessagesPath(req.Path), items: make(map[int]*restoreInputItem)}
	if req.WebSocket {
		replay.wrapped = &restoreInput{items: make(map[int]*restoreInputItem)}
	}
	match := walkerForPath(req.Path)
	segments, err := walkJSONText(body, func(p []pathElem) bool {
		if replay.wrapped != nil && len(p) > 0 && p[0].Key == "response" {
			p = p[1:]
		}
		return match(p) || replay.claude && claudeThinking(p) || !replay.claude && reasoningText(p)
	}, &replay)
	if err != nil {
		if reason, ok := err.(requestFormatError); ok {
			return invalidRequest(restoreRejection(reason))
		}
		// 普通遍历无回填深度限制，只在失败路径复验语法；不把本地深度上限当成请求非法。
		if _, syntaxErr := walkJSONText(body, nil, nil); syntaxErr != nil || len(bytes.TrimSpace(body)) == 0 || bytes.TrimSpace(body)[0] != '{' {
			return invalidRequest("请求包含无效 JSON")
		}
		return restoreError("可逆脱敏需要无歧义的 JSON 请求")
	}
	if e := replay.finish(); e != nil {
		return e
	}
	if replay.claude {
		segments = dropClaudeAttributionSegments(segments)
	}
	kept := segments[:0]
	for _, seg := range segments {
		scope := &replay
		if replay.wrapped != nil && len(seg.Path) > 0 && seg.Path[0].Key == "response" {
			scope = replay.wrapped
			seg.Path = seg.Path[1:]
		}
		thinking := replay.claude && claudeThinking(seg.Path)
		if thinking {
			_, seg.sealed = scope.sealedThinking[[2]int{seg.Path[1].Index, seg.Path[3].Index}]
		} else {
			seg.sealed = !replay.claude && reasoningText(seg.Path) && scope.items[seg.Path[1].Index].kind == "reasoning"
		}
		if thinking || seg.sealed || match(seg.Path) {
			kept = append(kept, seg)
		}
	}
	view, e := s.Dict.Prepare()
	if e != nil {
		return e
	}
	if !replay.claude {
		view.BindToolReplayScope(req)
	}
	e = s.applyReversible(req, body, result, candidates, kept, view)
	view.index = nil
	if e != nil {
		view.Release()
		return e
	}
	if result.Changed {
		if e := replay.verifyFunctionReplays(view, result.Body, "input"); e != nil {
			view.Release()
			return e
		}
	}
	result.Restore = view
	result.RestoreStatus = "pending"
	if req.Path == "/v1/messages/count_tokens" || req.Path == "/v1/responses/compact" {
		result.RestoreStatus = "not_applicable"
	}
	return nil
}
