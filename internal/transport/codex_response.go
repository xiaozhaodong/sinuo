package transport

import (
	"compress/flate"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/andybalholm/brotli"
)

// ReadCodexResponseBody 读取辅助请求（RT 刷新、授权码兑换、quota、连通性测试）的响应正文。
//
// NewCodexHTTPClient 关闭了 Go 的自动 gzip 协商，Transport 不再替调用方透明解压；
// 上游仍可能主动返回压缩正文，这里按实际 Content-Encoding 做且只做一次解码。
// limit 作用于解码后的字节数，解码结果超过 limit 视为错误，避免解压后无限增长。
// 未知编码明确报错，不把压缩字节当明文交给调用方解析。
func ReadCodexResponseBody(resp *http.Response, limit int64) ([]byte, error) {
	if resp == nil || resp.Body == nil {
		return nil, errors.New("响应正文为空")
	}
	if limit <= 0 {
		return nil, fmt.Errorf("非法的响应读取上限: %d", limit)
	}

	reader, err := newCodexResponseDecoder(resp)
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, fmt.Errorf("读取响应正文失败 (content_encoding=%q): %w", resp.Header.Get("Content-Encoding"), err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("响应正文超过读取上限 %d 字节", limit)
	}
	return body, nil
}

func newCodexResponseDecoder(resp *http.Response) (io.ReadCloser, error) {
	encoding := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	switch encoding {
	case "", "identity":
		return io.NopCloser(resp.Body), nil
	case "gzip":
		gzipReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("初始化 gzip 解码失败: %w", err)
		}
		return gzipReader, nil
	case "deflate":
		return flate.NewReader(resp.Body), nil
	case "br":
		return io.NopCloser(brotli.NewReader(resp.Body)), nil
	default:
		return nil, fmt.Errorf("不支持的响应 Content-Encoding: %q", encoding)
	}
}
