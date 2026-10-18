package response

import (
	"compress/flate"
	"compress/gzip"
	"compress/lzw"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

// zstdResponseMaxWindow 是 zstd 响应解码的窗口上限，取 RFC 9659 对 HTTP zstd 内容编码规定的 8MB。
// 解码器按帧头声明的窗口预分配历史缓冲，不设上限时几个字节的帧就能触发约 512MB 分配。
const zstdResponseMaxWindow = 8 << 20

// Processor handles response processing including decompression
type Processor struct{}

// NewProcessor creates a new response processor
func NewProcessor() *Processor {
	return &Processor{}
}

// DecompressStreamReader 创建解压缩的流式读取器，保持流式特性
// 根据Content-Encoding头部返回适当的解压缩读取器，如果无压缩则返回原始读取器
func (p *Processor) DecompressStreamReader(resp *http.Response) (io.ReadCloser, error) {
	// 检查Content-Encoding头部
	contentEncoding := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	if contentEncoding == "" || contentEncoding == "identity" {
		// 无编码或identity编码，直接返回原始读取器
		return resp.Body, nil
	}

	// 根据压缩格式创建相应的解压缩读取器
	switch contentEncoding {
	case "gzip":
		gzipReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to create gzip stream reader: %w", err)
		}
		return gzipReader, nil

	case "deflate":
		// deflate使用flate.NewReader，它返回io.ReadCloser
		return flate.NewReader(resp.Body), nil

	case "br":
		// brotli读取器需要包装一个closer
		brotliReader := brotli.NewReader(resp.Body)
		return &brotliReadCloser{reader: brotliReader, closer: resp.Body}, nil

	case "compress":
		// LZW读取器需要包装一个closer
		lzwReader := lzw.NewReader(resp.Body, lzw.MSB, 8)
		return lzwReader, nil

	case "zstd":
		zstdReader, err := zstd.NewReader(resp.Body,
			zstd.WithDecoderConcurrency(1),
			zstd.WithDecoderLowmem(true),
			zstd.WithDecoderMaxWindow(zstdResponseMaxWindow),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to create zstd stream reader: %w", err)
		}
		return zstdReader.IOReadCloser(), nil

	default:
		// 未知编码，记录警告但返回原始读取器以保持兼容性
		slog.Warn(fmt.Sprintf("⚠️ [流式解压] 未知的内容编码: %s, 使用原始流", contentEncoding))
		return resp.Body, nil
	}
}

// brotliReadCloser 为brotli读取器添加Close方法
type brotliReadCloser struct {
	reader io.Reader
	closer io.Closer
}

func (brc *brotliReadCloser) Read(p []byte) (int, error) {
	return brc.reader.Read(p)
}

func (brc *brotliReadCloser) Close() error {
	return brc.closer.Close()
}

// CopyResponseHeaders 复制响应头到客户端
func (p *Processor) CopyResponseHeaders(resp *http.Response, w http.ResponseWriter) {
	for key, values := range resp.Header {
		// 跳过一些不应该复制的头部
		switch key {
		case "Content-Length", "Transfer-Encoding", "Connection", "Content-Encoding":
			continue
		}
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
}

// ProcessResponseBody 处理响应体（包括解压缩）
func (p *Processor) ProcessResponseBody(resp *http.Response) ([]byte, error) {
	// 🔧 [统一修复] 复用DecompressStreamReader逻辑，确保一致性
	decompressedReader, err := p.DecompressStreamReader(resp)
	if err != nil {
		return nil, fmt.Errorf("failed to create decompressed reader: %w", err)
	}
	defer decompressedReader.Close()

	// 读取响应体
	responseBytes, err := io.ReadAll(decompressedReader)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	return responseBytes, nil
}
