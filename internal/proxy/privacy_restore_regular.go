package proxy

import (
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"

	"sinuo/internal/codexidentity"
	"sinuo/internal/proxy/response"
	"sinuo/internal/proxy/restorestream"
	"sinuo/internal/tracking"
)

// readPrivacyRegularResponse 限制解压后的实际字节，避免按 Content-Length 判断压缩回包。
func readPrivacyRegularResponse(resp *http.Response) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding"))) {
	case "", "identity", "gzip", "deflate", "br", "compress", "zstd":
	default:
		return nil, restorestream.ErrDelivery
	}
	reader, err := response.NewProcessor().DecompressStreamReader(resp)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	raw, err := io.ReadAll(io.LimitReader(reader, restorestream.MaxJSONBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > restorestream.MaxJSONBytes {
		return nil, restorestream.ErrDelivery
	}
	return raw, nil
}

// unexpectedRegularUsage 仅消费明确声明为 JSON 的错格式成功回包。沿用两种协议已有的
// 用量解释器，把原始 usage 交给失败结算；错误正文无需进入下游或调试日志。
func (sp *StreamProcessor) unexpectedRegularUsage(resp *http.Response, path string) *tracking.TokenUsage {
	reader := *resp
	reader.Body = io.NopCloser(resp.Body)
	body, err := readPrivacyRegularResponse(&reader)
	if err != nil || !utf8.Valid(body) || codexidentity.ValidateJSON(body) != nil {
		return nil
	}
	usage := gjson.GetBytes(body, "usage")
	if !usage.IsObject() {
		return nil
	}
	event := "message_delta"
	if path == "/v1/responses" {
		event = "response.completed"
	}
	sp.tokenParser.ParseSSELine("event: " + event)
	sp.tokenParser.ParseSSELine(`data: {"usage":` + usage.Raw + `}`)
	if sp.tokenParser.ParseSSELine("") == nil {
		return nil
	}
	return sp.tokenParser.GetFinalUsage()
}
