package privacy

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

// 切段的同时收集续接和签名字段。先暂存候选路径，等对象结束后再判定类型，字段顺序不影响决策。
type restoreInput struct {
	claude                             bool
	conversationObject, conversationID bool
	items                              map[int]*restoreInputItem
}
type restoreInputItem struct {
	kind, id string
	opaque   bool
}

func (r *restoreInput) observe(path []pathElem, token any) error {
	text, isString := token.(string)
	if len(path) == 1 {
		if path[0].Key == "stream" {
			if _, ok := token.(bool); !ok {
				return fmt.Errorf("invalid stream")
			}
		}
		if !r.claude {
			switch path[0].Key {
			case "response":
				return fmt.Errorf("unexpected response wrapper")
			case "previous_response_id", "conversation":
				if token == json.Delim('{') && path[0].Key == "conversation" {
					r.conversationObject = true
				}
				if token != nil && !isString && !(path[0].Key == "conversation" && token == json.Delim('{')) {
					return fmt.Errorf("invalid reference")
				}
			}
		}
	}
	if !r.claude && len(path) == 2 && path[0].Key == "conversation" && path[1].Key == "id" {
		if !isString || text == "" {
			return fmt.Errorf("invalid conversation id")
		}
		r.conversationID = true
	}
	if len(path) < 3 || path[1].Index < 0 {
		return nil
	}
	if r.claude && path[0].Key != "messages" || !r.claude && path[0].Key != "input" {
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
		case "encrypted_content":
			if !isString && token != nil {
				return fmt.Errorf("invalid encrypted content")
			}
			item.opaque = text != ""
		case "encrypted_function_args", "encrypted_output":
			if token != nil && (!isString || text != "") {
				return fmt.Errorf("unsupported encrypted tool input")
			}
		}
	}

	return nil
}

func (r *restoreInput) finish() *PolicyError {
	if r.conversationObject && !r.conversationID {
		return restoreError("conversation 缺少会话标识")
	}
	for _, item := range r.items {
		if item.kind == "item_reference" && item.id == "" {
			return restoreError("item_reference 缺少历史标识")
		}
		if item.opaque && item.kind != "reasoning" && item.kind != "compaction" {
			return restoreError("无法识别加密历史项")
		}
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
	if !utf8.Valid(body) {
		return restoreError("可逆脱敏需要有效 UTF-8")
	}
	replay := restoreInput{claude: isClaudeMessagesPath(req.Path), items: make(map[int]*restoreInputItem)}
	match := walkerForPath(req.Path)
	segments, err := walkJSONText(body, func(p []pathElem) bool {
		return match(p) || replay.claude && claudeThinking(p) || !replay.claude && reasoningText(p)
	}, &replay)
	if err != nil {
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
		seg.sealed = claudeThinking(seg.Path) || !replay.claude && reasoningText(seg.Path) && replay.items[seg.Path[1].Index].kind == "reasoning"
		if seg.sealed || match(seg.Path) {
			kept = append(kept, seg)
		}
	}
	view, e := s.Dict.Prepare()
	if e != nil {
		return e
	}
	e = s.applyReversible(req, body, result, candidates, kept, view)
	view.index = nil
	if e != nil {
		return e
	}
	result.Restore = view
	result.RestoreStatus = "pending"
	if req.Path == "/v1/messages/count_tokens" || req.Path == "/v1/responses/compact" {
		result.RestoreStatus = "not_applicable"
	}
	return nil
}
