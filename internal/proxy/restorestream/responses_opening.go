package restorestream

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"

	"sinuo/internal/codexidentity"
)

var ErrResponseFormat = fmt.Errorf("%w: 上游响应格式与预期 SSE 不符", ErrDelivery)

// OpenUntypedResponses 在已协商流式的 Codex 请求缺失响应类型时，验证首个数据事件。
// 心跳和预读字节只在确认协议后交付。首事件可以包含较大的响应快照，沿用回填器的事件上限。
func OpenUntypedResponses(ctx context.Context, r *bufio.Reader) (io.Reader, error) {
	if b, _ := r.Peek(1); len(b) > 0 && b[0] == 0xef {
		if bom, _ := r.Peek(3); bytes.Equal(bom, []byte("\xef\xbb\xbf")) {
			_, _ = r.Discard(3)
		}
	}
	var prefix []byte
	var start, pos int
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		_, err := r.Peek(1)
		if err != nil && err != io.EOF {
			return nil, err
		}
		n := min(r.Buffered(), maxBytes-len(prefix))
		buf, _ := r.Peek(n)
		window := buf
		if len(prefix) != 0 {
			window = append(prefix, buf...)
		}
		for start < len(window) {
			frame := window[start:]
			if !strings.ContainsRune(":deir\r\n", rune(frame[0])) {
				return nil, ErrResponseFormat
			}
			end, next := eventEnd(frame, pos, err == io.EOF)
			if end == 0 {
				pos = next
				break
			}
			hasData, frameErr := checkResponsesOpening(frame[:end])
			if frameErr != nil {
				return nil, frameErr
			}
			if hasData {
				if len(prefix) == 0 {
					return r, nil // 首事件已在读缓冲区，无需复制、回放。
				}
				_, _ = r.Discard(n)
				return io.MultiReader(bytes.NewReader(window), r), nil
			}
			start += end
			pos = 0
		}
		if err == io.EOF || len(window) == maxBytes {
			return nil, ErrResponseFormat
		}
		if len(prefix) == 0 {
			window = bytes.Clone(window)
		}
		prefix = window
		_, _ = r.Discard(n)
	}
}

// checkResponsesOpening 允许标准 SSE 元数据和多行 data，首个数据事件必须能识别为 Responses。
func checkResponsesOpening(frame []byte) (bool, error) {
	fields, err := parseSSEFields(frame, true)
	if err != nil {
		return false, ErrResponseFormat
	}
	if len(fields.lines) == 0 {
		return false, nil
	}
	raw := fields.payload()
	if !utf8.Valid(raw) || codexidentity.ValidateJSON(raw) != nil {
		return false, ErrResponseFormat
	}
	typ := gjson.GetBytes(raw, "type").Str
	if fields.name != "" && fields.name != "message" && fields.name != typ {
		return false, ErrResponseFormat
	}
	if !strings.HasPrefix(typ, "response.") && typ != "error" && typ != "codex.rate_limits" && typ != "codex.response.metadata" {
		return false, ErrResponseFormat
	}
	return true, nil
}
