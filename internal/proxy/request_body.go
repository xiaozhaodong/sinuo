package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sinuo/config"
	"strings"
	"sync"

	"github.com/klauspost/compress/zstd"
)

const (
	requestBodyReadInitCap             = 512
	requestBodyReadMaxInitCap          = 1 << 20
	defaultEncodedRequestBodyMaxBytes  = 64 << 20
	requestContentEncodingIdentity     = "identity"
	requestContentEncodingZstd         = "zstd"
	requestBodyCodeReadFailed          = "request_body_read_failed"
	requestBodyCodeTooLarge            = "request_body_too_large"
	requestBodyCodeDecodedTooLarge     = "decompressed_request_body_too_large"
	requestBodyCodeZstdWindowTooLarge  = "zstd_window_too_large"
	requestBodyCodeInvalidZstd         = "invalid_zstd_request_body"
	requestBodyCodeUnsupportedEncoding = "unsupported_content_encoding"
)

type requestBodyError struct {
	StatusCode int
	Code       string
	Message    string
	Cause      error
}

// normalizedRequestBodyMetadata 的内容比较一律用 SHA-256 摘要：正文切片可能被过滤器原地修改，
// 比较切片会连同被比较的一方一起改变，导致复用旧压缩体把修改前的内容发出去。
type normalizedRequestBodyMetadata struct {
	normalizedBody         []byte
	originalCompressedBody []byte
	inboundContentEncoding string
	normalizedDigest       [sha256.Size]byte // 规范化完成时的明文摘要，仅 zstd 入站记录
	cachedDigest           [sha256.Size]byte
	cachedCompressed       []byte
}

// canReuseOriginalCompressed 判断出站明文是否仍与客户端原始 zstd 帧内容一致；nil 安全。
func (m *normalizedRequestBodyMetadata) canReuseOriginalCompressed(body []byte) bool {
	return m != nil && m.inboundContentEncoding == requestContentEncodingZstd && m.originalCompressedBody != nil &&
		sha256.Sum256(body) == m.normalizedDigest
}

type normalizedRequestBodyContextKey struct{}

var zstdEncoderPool = sync.Pool{
	New: func() any {
		encoder, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
		if err != nil {
			return nil
		}
		return encoder
	},
}

var zstdDecoderPools sync.Map // map[int64]*sync.Pool; one pool per decode limit

func (e *requestBodyError) Error() string {
	if e == nil {
		return ""
	}
	if e.Cause == nil {
		return e.Message
	}
	return fmt.Sprintf("%s: %v", e.Message, e.Cause)
}

func (e *requestBodyError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func readRequestBodyWithPrealloc(req *http.Request) ([]byte, error) {
	if req == nil || req.Body == nil {
		return nil, nil
	}

	capHint := requestBodyReadInitCap
	if req.ContentLength > 0 {
		switch {
		case req.ContentLength < int64(requestBodyReadInitCap):
			capHint = requestBodyReadInitCap
		case req.ContentLength > int64(requestBodyReadMaxInitCap):
			capHint = requestBodyReadMaxInitCap
		default:
			capHint = int(req.ContentLength)
		}
	}

	buf := bytes.NewBuffer(make([]byte, 0, capHint))
	if _, err := io.Copy(buf, req.Body); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// readAndNormalizeRequestBody 是请求方向唯一的 body 规范化入口。
// 所有下游消费者只接收这里返回的明文 body；zstd 解压不会进入 retry/failover 循环。
func readAndNormalizeRequestBody(w http.ResponseWriter, req *http.Request, cfg *config.Config) ([]byte, *requestBodyError) {
	metadata, bodyErr := normalizeRequestBodyWithMetadata(w, req, cfg)
	if bodyErr != nil || metadata == nil {
		return nil, bodyErr
	}
	return metadata.normalizedBody, nil
}

func normalizeRequestBodyWithMetadata(w http.ResponseWriter, req *http.Request, cfg *config.Config) (*normalizedRequestBodyMetadata, *requestBodyError) {
	if req == nil {
		return nil, nil
	}

	originalBody := req.Body
	if originalBody != nil {
		defer originalBody.Close()
	}

	contentEncoding, encodingErr := normalizeRequestContentEncoding(req.Header.Values("Content-Encoding"))
	if encodingErr != nil {
		return nil, encodingErr
	}

	limit := requestBodyMaxBytes(cfg)
	if contentEncoding == requestContentEncodingZstd && limit <= 0 {
		limit = defaultEncodedRequestBodyMaxBytes
	}
	if originalBody != nil && limit > 0 {
		req.Body = http.MaxBytesReader(w, originalBody, limit)
	}

	if originalBody == nil {
		if contentEncoding == requestContentEncodingZstd {
			return nil, invalidZstdRequestBodyError(io.ErrUnexpectedEOF)
		}
		setNormalizedRequestBody(req, nil)
		return &normalizedRequestBodyMetadata{normalizedBody: nil, inboundContentEncoding: contentEncoding}, nil
	}

	rawBody, err := readRequestBodyWithPrealloc(req)
	if err != nil {
		if maxErr, ok := extractMaxBytesError(err); ok {
			return nil, &requestBodyError{
				StatusCode: http.StatusRequestEntityTooLarge,
				Code:       requestBodyCodeTooLarge,
				Message:    buildRequestBodyTooLargeMessage(maxErr.Limit),
				Cause:      err,
			}
		}
		return nil, &requestBodyError{
			StatusCode: http.StatusInternalServerError,
			Code:       requestBodyCodeReadFailed,
			Message:    "Failed to read request body",
			Cause:      err,
		}
	}

	normalizedBody := rawBody
	if contentEncoding == requestContentEncodingZstd {
		if len(rawBody) == 0 {
			return nil, invalidZstdRequestBodyError(io.ErrUnexpectedEOF)
		}
		normalizedBody, err = decodeZstdRequestBody(rawBody, limit)
		if err != nil {
			return nil, classifyZstdDecodeError(err, limit)
		}
		if int64(len(normalizedBody)) > limit {
			return nil, &requestBodyError{
				StatusCode: http.StatusRequestEntityTooLarge,
				Code:       requestBodyCodeDecodedTooLarge,
				Message:    buildDecodedRequestBodyTooLargeMessage(limit),
			}
		}
		logZstdRequestDecompression(req, len(rawBody), len(normalizedBody))
	}

	setNormalizedRequestBody(req, normalizedBody)
	metadata := &normalizedRequestBodyMetadata{
		normalizedBody:         normalizedBody,
		inboundContentEncoding: contentEncoding,
	}
	if contentEncoding == requestContentEncodingZstd {
		metadata.originalCompressedBody = append([]byte(nil), rawBody...)
		metadata.normalizedDigest = sha256.Sum256(normalizedBody)
	}
	return metadata, nil
}

func classifyZstdDecodeError(err error, limit int64) *requestBodyError {
	if errors.Is(err, zstd.ErrWindowSizeExceeded) {
		return &requestBodyError{
			StatusCode: http.StatusRequestEntityTooLarge,
			Code:       requestBodyCodeZstdWindowTooLarge,
			Message:    buildZstdWindowTooLargeMessage(limit),
			Cause:      err,
		}
	}
	if errors.Is(err, zstd.ErrDecoderSizeExceeded) {
		return &requestBodyError{
			StatusCode: http.StatusRequestEntityTooLarge,
			Code:       requestBodyCodeDecodedTooLarge,
			Message:    buildDecodedRequestBodyTooLargeMessage(limit),
			Cause:      err,
		}
	}
	return invalidZstdRequestBodyError(err)
}

func attachNormalizedRequestBodyMetadata(req *http.Request, metadata *normalizedRequestBodyMetadata) {
	if req == nil {
		return
	}
	ctx := context.WithValue(req.Context(), normalizedRequestBodyContextKey{}, metadata)
	*req = *req.WithContext(ctx)
}

func normalizedRequestBodyMetadataFromRequest(req *http.Request) *normalizedRequestBodyMetadata {
	if req == nil {
		return nil
	}
	metadata, _ := req.Context().Value(normalizedRequestBodyContextKey{}).(*normalizedRequestBodyMetadata)
	return metadata
}

func logZstdRequestDecompression(req *http.Request, compressedBytes, decompressedBytes int) {
	requestID := ""
	path := ""
	if req != nil {
		requestID, _ = req.Context().Value("conn_id").(string)
		if req.URL != nil {
			path = req.URL.Path
		}
	}
	slog.Info(fmt.Sprintf("🗜️ [请求解压] [%s] zstd 解压完成，路径: %s, 压缩体: %d字节, 明文体: %d字节",
		requestID, path, compressedBytes, decompressedBytes))
}

func normalizeRequestContentEncoding(headerValues []string) (string, *requestBodyError) {
	var encodings []string
	for _, headerValue := range headerValues {
		for _, part := range strings.Split(headerValue, ",") {
			encoding := strings.ToLower(strings.TrimSpace(part))
			if encoding != "" {
				encodings = append(encodings, encoding)
			}
		}
	}

	if len(encodings) == 0 {
		return "", nil
	}
	if len(encodings) == 1 && (encodings[0] == requestContentEncodingIdentity || encodings[0] == requestContentEncodingZstd) {
		return encodings[0], nil
	}
	return "", &requestBodyError{
		StatusCode: http.StatusUnsupportedMediaType,
		Code:       requestBodyCodeUnsupportedEncoding,
		Message:    "Unsupported Content-Encoding; supported values are identity and zstd",
	}
}

func decodeZstdRequestBody(rawBody []byte, limit int64) ([]byte, error) {
	decoderPool := zstdDecoderPoolForLimit(limit)
	decoderValue := decoderPool.Get()
	if decoderValue == nil {
		return nil, fmt.Errorf("failed to create zstd decoder")
	}
	decoder := decoderValue.(*zstd.Decoder)
	defer func() {
		_ = decoder.Reset(nil)
		decoderPool.Put(decoder)
	}()
	return decoder.DecodeAll(rawBody, nil)
}

func encodeZstdRequestBody(body []byte) ([]byte, error) {
	encoderValue := zstdEncoderPool.Get()
	if encoderValue == nil {
		return nil, fmt.Errorf("failed to create zstd encoder")
	}
	encoder := encoderValue.(*zstd.Encoder)
	defer func() {
		encoder.Reset(nil)
		zstdEncoderPool.Put(encoder)
	}()
	return encoder.EncodeAll(body, nil), nil
}

func zstdDecoderPoolForLimit(limit int64) *sync.Pool {
	if existing, ok := zstdDecoderPools.Load(limit); ok {
		return existing.(*sync.Pool)
	}
	maxWindow := uint64(limit)
	if maxWindow < 1024 {
		maxWindow = 1024
	}
	decoderPool := &sync.Pool{New: func() any {
		decoder, err := zstd.NewReader(nil,
			zstd.WithDecoderConcurrency(1),
			zstd.WithDecoderLowmem(true),
			zstd.WithDecoderMaxMemory(uint64(limit)),
			zstd.WithDecoderMaxWindow(maxWindow),
		)
		if err != nil {
			return nil
		}
		return decoder
	}}
	actual, _ := zstdDecoderPools.LoadOrStore(limit, decoderPool)
	return actual.(*sync.Pool)
}

func compressNormalizedRequestBody(metadata *normalizedRequestBodyMetadata, body []byte) ([]byte, error) {
	digest := sha256.Sum256(body)
	if metadata != nil && metadata.cachedCompressed != nil && metadata.cachedDigest == digest {
		return metadata.cachedCompressed, nil
	}
	compressed, err := encodeZstdRequestBody(body)
	if err != nil {
		return nil, err
	}
	if metadata != nil {
		metadata.cachedDigest = digest
		metadata.cachedCompressed = compressed
	}
	return compressed, nil
}

func setNormalizedRequestBody(req *http.Request, body []byte) {
	if req == nil {
		return
	}
	req.Header.Del("Content-Encoding")
	req.Header.Del("Content-Length")
	req.ContentLength = int64(len(body))
	req.TransferEncoding = nil
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
}

func invalidZstdRequestBodyError(cause error) *requestBodyError {
	return &requestBodyError{
		StatusCode: http.StatusBadRequest,
		Code:       requestBodyCodeInvalidZstd,
		Message:    "Invalid zstd request body",
		Cause:      cause,
	}
}

func extractMaxBytesError(err error) (*http.MaxBytesError, bool) {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		return maxErr, true
	}
	return nil, false
}

func formatRequestBodyLimit(limit int64) string {
	const mb = 1024 * 1024
	if limit >= mb {
		return fmt.Sprintf("%dMB", limit/mb)
	}
	return fmt.Sprintf("%dB", limit)
}

func buildRequestBodyTooLargeMessage(limit int64) string {
	return fmt.Sprintf("Request body too large, limit is %s", formatRequestBodyLimit(limit))
}

func buildDecodedRequestBodyTooLargeMessage(limit int64) string {
	return fmt.Sprintf("Decompressed request body too large, limit is %s", formatRequestBodyLimit(limit))
}

func buildZstdWindowTooLargeMessage(limit int64) string {
	return fmt.Sprintf("Zstd frame window exceeds request body limit of %s", formatRequestBodyLimit(limit))
}

func requestBodyMaxBytes(cfg *config.Config) int64 {
	if cfg == nil {
		return 0
	}
	if cfg.RequestBodyMaxBytes <= 0 {
		return 0
	}
	return cfg.RequestBodyMaxBytes
}
