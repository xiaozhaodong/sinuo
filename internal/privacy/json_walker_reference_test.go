package privacy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// referenceWalkJSONText 固定优化前的 Token 实现，只供对拍和性能比较使用。
// 不把生产逻辑同步到此处，避免两份实现同时引入相同错误。
func referenceWalkJSONText(body []byte, match segmentMatcher, replay *restoreInput) ([]textSegment, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()

	type frame struct {
		keys      map[string]struct{}
		isObject  bool
		expectKey bool
		key       string
		index     int
	}

	var (
		stack    []frame
		segments []textSegment
		pathBuf  []pathElem
	)
	prevOffset := int64(0)

	// currentPath 复用缓冲区构造当前路径；只有命中的段才复制保存
	currentPath := func() []pathElem {
		pathBuf = pathBuf[:0]
		for _, f := range stack {
			if f.isObject {
				pathBuf = append(pathBuf, pathElem{Key: f.key, Index: -1})
			} else {
				pathBuf = append(pathBuf, pathElem{Index: f.index})
			}
		}
		return pathBuf
	}

	// 值结束后推进所在容器的游标
	advanceContainer := func() {
		if len(stack) == 0 {
			return
		}
		top := &stack[len(stack)-1]
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
			if len(stack) != 0 || replay != nil && prevOffset == 0 {
				return nil, fmt.Errorf("invalid json body: unexpected end of input")
			}
			break
		}
		if err != nil {
			return nil, fmt.Errorf("invalid json body: %w", err)
		}
		offset := decoder.InputOffset()
		if replay != nil {
			if len(stack) == 0 && (prevOffset != 0 || token != json.Delim('{')) || len(stack) > 257 {
				return nil, fmt.Errorf("invalid request object")
			}
			key := len(stack) > 0 && stack[len(stack)-1].isObject && stack[len(stack)-1].expectKey
			if delim, ok := token.(json.Delim); (!ok || delim != '}' && delim != ']') && !key {
				if len(stack) > 256 {
					return nil, fmt.Errorf("request exceeds depth limit")
				}
				if err := replay.observe(currentPath(), token); err != nil {
					return nil, err
				}
			}
		}

		switch tok := token.(type) {
		case json.Delim:
			switch tok {
			case '{':
				stack = append(stack, frame{isObject: true, expectKey: true})
			case '[':
				stack = append(stack, frame{isObject: false, index: 0})
			case '}', ']':
				stack = stack[:len(stack)-1]
				advanceContainer()
			}
		case string:
			var top *frame
			if len(stack) > 0 {
				top = &stack[len(stack)-1]
			}
			if top != nil && top.isObject && top.expectKey {
				if replay != nil {
					if top.keys == nil {
						top.keys = make(map[string]struct{})
					}
					if _, exists := top.keys[tok]; exists {
						return nil, fmt.Errorf("duplicate object key")
					}
					top.keys[tok] = struct{}{}
				}
				top.key = tok
				top.expectKey = false
				prevOffset = offset
				continue
			}
			// 字符串值：定位 token 原始字节区间（前一 token 末尾到当前末尾之间，
			// 仅有空白/逗号/冒号，首个双引号即 token 起点）
			if match != nil {
				if path := currentPath(); match(path) {
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
						Path:   append([]pathElem(nil), path...),
					})
				}
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
