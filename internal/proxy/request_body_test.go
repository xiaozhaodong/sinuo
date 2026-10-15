package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sinuo/config"
	"sinuo/internal/endpoint"
	"sinuo/internal/tracking"

	"github.com/klauspost/compress/zstd"
)

func mustEncodeZstd(t testing.TB, body []byte) []byte {
	t.Helper()
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	if err != nil {
		t.Fatalf("create zstd encoder: %v", err)
	}
	defer encoder.Close()
	return encoder.EncodeAll(body, nil)
}

func TestReadRequestBodyWithPrealloc_ReadsBody(t *testing.T) {
	body := `{"model":"gpt-5.4","input":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(body))

	got, err := readRequestBodyWithPrealloc(req)
	if err != nil {
		t.Fatalf("readRequestBodyWithPrealloc returned error: %v", err)
	}
	if string(got) != body {
		t.Fatalf("expected body %q, got %q", body, string(got))
	}
}

func TestReadAndNormalizeRequestBody_PlainJSONPreservesBody(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"hello"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Length", "stale")
	rec := httptest.NewRecorder()

	got, bodyErr := readAndNormalizeRequestBody(rec, req, &config.Config{})
	if bodyErr != nil {
		t.Fatalf("readAndNormalizeRequestBody returned error: %v", bodyErr)
	}
	assertNormalizedRequestBody(t, req, got, body)
}

func TestReadAndNormalizeRequestBody_IdentityPreservesBody(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"identity"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req.Header.Set("Content-Encoding", "identity")
	rec := httptest.NewRecorder()

	got, bodyErr := readAndNormalizeRequestBody(rec, req, &config.Config{})
	if bodyErr != nil {
		t.Fatalf("readAndNormalizeRequestBody returned error: %v", bodyErr)
	}
	assertNormalizedRequestBody(t, req, got, body)
}

func TestReadAndNormalizeRequestBody_ZstdDecodesAndCleansHeaders(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","stream":true,"input":"compressed"}`)
	compressed := mustEncodeZstd(t, body)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(compressed))
	req.Header.Set("Content-Encoding", "ZSTD")
	req.Header.Set("Content-Length", "1")
	rec := httptest.NewRecorder()

	got, bodyErr := readAndNormalizeRequestBody(rec, req, &config.Config{})
	if bodyErr != nil {
		t.Fatalf("readAndNormalizeRequestBody returned error: %v", bodyErr)
	}
	assertNormalizedRequestBody(t, req, got, body)
}

func TestReadAndNormalizeRequestBody_LogsOnlySuccessfulZstd(t *testing.T) {
	originalLogger := slog.Default()
	var logBuffer bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuffer, nil)))
	t.Cleanup(func() {
		slog.SetDefault(originalLogger)
	})

	plainReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.4"}`))
	if _, bodyErr := readAndNormalizeRequestBody(httptest.NewRecorder(), plainReq, &config.Config{}); bodyErr != nil {
		t.Fatalf("normalize plain body: %v", bodyErr)
	}

	identityReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.4"}`))
	identityReq.Header.Set("Content-Encoding", "identity")
	if _, bodyErr := readAndNormalizeRequestBody(httptest.NewRecorder(), identityReq, &config.Config{}); bodyErr != nil {
		t.Fatalf("normalize identity body: %v", bodyErr)
	}

	invalidReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader("not-zstd"))
	invalidReq.Header.Set("Content-Encoding", "zstd")
	if _, bodyErr := readAndNormalizeRequestBody(httptest.NewRecorder(), invalidReq, &config.Config{}); bodyErr == nil {
		t.Fatal("expected invalid zstd error")
	}

	body := []byte(`{"model":"gpt-5.4","input":"log marker must stay private"}`)
	compressed := mustEncodeZstd(t, body)
	zstdReq := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(compressed))
	zstdReq.Header.Set("Content-Encoding", "zstd")
	zstdReq = zstdReq.WithContext(context.WithValue(zstdReq.Context(), "conn_id", "req-zstd-log"))
	if _, bodyErr := readAndNormalizeRequestBody(httptest.NewRecorder(), zstdReq, &config.Config{}); bodyErr != nil {
		t.Fatalf("normalize zstd body: %v", bodyErr)
	}

	logs := logBuffer.String()
	if count := strings.Count(logs, "[请求解压]"); count != 1 {
		t.Fatalf("request decompression log count = %d, want 1; logs=%s", count, logs)
	}
	for _, want := range []string{
		"[req-zstd-log]",
		"zstd 解压完成",
		"路径: /v1/responses",
		fmt.Sprintf("压缩体: %d字节", len(compressed)),
		fmt.Sprintf("明文体: %d字节", len(body)),
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("decompression log missing %q: %s", want, logs)
		}
	}
	if strings.Contains(logs, "log marker must stay private") {
		t.Fatalf("decompression log must not include request body: %s", logs)
	}
}

func TestReadAndNormalizeRequestBody_InvalidZstd(t *testing.T) {
	valid := mustEncodeZstd(t, []byte(`{"model":"gpt-5.4"}`))
	tests := []struct {
		name string
		body []byte
	}{
		{name: "empty body", body: nil},
		{name: "not a zstd frame", body: []byte("not-zstd")},
		{name: "truncated frame", body: valid[:len(valid)/2]},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(tt.body))
			req.Header.Set("Content-Encoding", "zstd")
			_, bodyErr := readAndNormalizeRequestBody(httptest.NewRecorder(), req, &config.Config{})
			if bodyErr == nil {
				t.Fatal("expected invalid zstd error")
			}
			if bodyErr.StatusCode != http.StatusBadRequest || bodyErr.Code != requestBodyCodeInvalidZstd {
				t.Fatalf("unexpected error: %+v", bodyErr)
			}
		})
	}
}

func TestReadAndNormalizeRequestBody_RejectsUnsupportedEncoding(t *testing.T) {
	for _, encoding := range []string{"gzip", "zstd, identity"} {
		t.Run(encoding, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
			req.Header.Set("Content-Encoding", encoding)
			_, bodyErr := readAndNormalizeRequestBody(httptest.NewRecorder(), req, &config.Config{})
			if bodyErr == nil {
				t.Fatal("expected unsupported encoding error")
			}
			if bodyErr.StatusCode != http.StatusUnsupportedMediaType || bodyErr.Code != requestBodyCodeUnsupportedEncoding {
				t.Fatalf("unexpected error: %+v", bodyErr)
			}
		})
	}
}

func TestReadAndNormalizeRequestBody_RejectsCompressedBodyOverLimit(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"compressed limit"}`)
	compressed := mustEncodeZstd(t, body)
	limit := int64(len(compressed) - 1)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(compressed))
	req.Header.Set("Content-Encoding", "zstd")

	_, bodyErr := readAndNormalizeRequestBody(httptest.NewRecorder(), req, &config.Config{RequestBodyMaxBytes: limit})
	if bodyErr == nil {
		t.Fatal("expected compressed body limit error")
	}
	if bodyErr.StatusCode != http.StatusRequestEntityTooLarge || bodyErr.Code != requestBodyCodeTooLarge {
		t.Fatalf("unexpected error: %+v", bodyErr)
	}
}

func TestReadAndNormalizeRequestBody_RejectsDecodedBodyOverLimit(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"` + strings.Repeat("a", 4096) + `"}`)
	compressed := mustEncodeZstd(t, body)
	limit := int64(len(compressed) + 16)
	if int64(len(body)) <= limit {
		t.Fatalf("test setup requires decoded body over limit: decoded=%d limit=%d", len(body), limit)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(compressed))
	req.Header.Set("Content-Encoding", "zstd")

	_, bodyErr := readAndNormalizeRequestBody(httptest.NewRecorder(), req, &config.Config{RequestBodyMaxBytes: limit})
	if bodyErr == nil {
		t.Fatal("expected decompressed body limit error")
	}
	if bodyErr.StatusCode != http.StatusRequestEntityTooLarge || bodyErr.Code != requestBodyCodeDecodedTooLarge {
		t.Fatalf("unexpected error: %+v", bodyErr)
	}
}

func TestReadAndNormalizeRequestBody_ReportsZstdWindowLimitSeparately(t *testing.T) {
	limit := int64(1024)
	bodyErr := classifyZstdDecodeError(zstd.ErrWindowSizeExceeded, limit)
	if bodyErr.Code != requestBodyCodeZstdWindowTooLarge || bodyErr.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("unexpected error: %+v", bodyErr)
	}
	if strings.Contains(bodyErr.Message, "Decompressed request body too large") {
		t.Fatalf("window error should not use decoded-size message: %q", bodyErr.Message)
	}
}

func TestCompressNormalizedRequestBody_ReusesCompressedResultForSamePlaintext(t *testing.T) {
	metadata := &normalizedRequestBodyMetadata{}
	body := []byte(`{"model":"gpt-5.4","input":"cache me"}`)
	first, err := compressNormalizedRequestBody(metadata, body)
	if err != nil {
		t.Fatalf("first compression failed: %v", err)
	}
	second, err := compressNormalizedRequestBody(metadata, append([]byte(nil), body...))
	if err != nil {
		t.Fatalf("second compression failed: %v", err)
	}
	if metadata.cachedDigest != sha256.Sum256(body) || len(metadata.cachedCompressed) == 0 {
		t.Fatalf("expected one cached compressed body")
	}
	if len(first) == 0 || &first[0] != &second[0] {
		t.Fatalf("expected second compression to reuse cached bytes")
	}
}

func TestCompressNormalizedRequestBody_ReplacesSingleSlotForChangedPlaintext(t *testing.T) {
	metadata := &normalizedRequestBodyMetadata{}
	firstBody := []byte(`{"model":"gpt-5.4","input":"first"}`)
	secondBody := []byte(`{"model":"gpt-5.4","input":"second"}`)

	if _, err := compressNormalizedRequestBody(metadata, firstBody); err != nil {
		t.Fatalf("first compression failed: %v", err)
	}
	secondCompressed, err := compressNormalizedRequestBody(metadata, secondBody)
	if err != nil {
		t.Fatalf("second compression failed: %v", err)
	}
	if metadata.cachedDigest != sha256.Sum256(secondBody) {
		t.Fatalf("cached plaintext was not replaced")
	}
	if !bytes.Equal(metadata.cachedCompressed, secondCompressed) {
		t.Fatalf("cached compressed body was not replaced")
	}
}

// 过滤器原地修改正文时切片地址和长度都不变：原始帧复用与压缩缓存都必须按内容发现变化，
// 否则会把修改前（未脱敏）的内容以压缩体发出去。
func TestNormalizedRequestBodyReuseDetectsInPlaceMutation(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"SECRET"}`)
	compressed := mustEncodeZstd(t, body)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(compressed))
	req.Header.Set("Content-Encoding", "zstd")
	metadata, bodyErr := normalizeRequestBodyWithMetadata(httptest.NewRecorder(), req, nil)
	if bodyErr != nil {
		t.Fatal(bodyErr)
	}
	plain := metadata.normalizedBody
	if !metadata.canReuseOriginalCompressed(plain) || !metadata.canReuseOriginalCompressed(bytes.Clone(plain)) {
		t.Fatal("内容未变时应复用原始 zstd 帧")
	}
	cached, err := compressNormalizedRequestBody(metadata, plain)
	if err != nil {
		t.Fatal(err)
	}
	copy(plain[bytes.Index(plain, []byte("SECRET")):], "XXXXXX")
	if metadata.canReuseOriginalCompressed(plain) {
		t.Fatal("原地修改后仍复用原始 zstd 帧")
	}
	recompressed, err := compressNormalizedRequestBody(metadata, plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(recompressed, cached) || bytes.Contains(zstdDecompress(t, recompressed), []byte("SECRET")) {
		t.Fatal("原地修改后命中了旧压缩缓存")
	}
	if (*normalizedRequestBodyMetadata)(nil).canReuseOriginalCompressed(plain) {
		t.Fatal("nil metadata 不应复用")
	}
}

func TestServeHTTP_ZstdNormalizationErrorsUseCodexEnvelope(t *testing.T) {
	decodedBody := []byte(`{"model":"gpt-5.4","input":"` + strings.Repeat("a", 4096) + `"}`)
	decodedCompressed := mustEncodeZstd(t, decodedBody)
	decodedLimit := int64(len(decodedCompressed) + 16)

	tests := []struct {
		name       string
		body       []byte
		encoding   string
		limit      int64
		wantStatus int
		wantCode   string
	}{
		{
			name:       "invalid zstd",
			body:       []byte("not-zstd"),
			encoding:   "zstd",
			wantStatus: http.StatusBadRequest,
			wantCode:   requestBodyCodeInvalidZstd,
		},
		{
			name:       "unsupported encoding",
			body:       []byte(`{}`),
			encoding:   "gzip",
			wantStatus: http.StatusUnsupportedMediaType,
			wantCode:   requestBodyCodeUnsupportedEncoding,
		},
		{
			name:       "decoded body too large",
			body:       decodedCompressed,
			encoding:   "zstd",
			limit:      decodedLimit,
			wantStatus: http.StatusRequestEntityTooLarge,
			wantCode:   requestBodyCodeDecodedTooLarge,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{RequestBodyMaxBytes: tt.limit}
			handler := NewHandler(endpoint.NewManager(cfg), cfg)
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Content-Encoding", tt.encoding)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d body=%s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			var payload struct {
				Error struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode error response: %v body=%s", err, rec.Body.String())
			}
			if payload.Error.Type != tt.wantCode || payload.Error.Message == "" {
				t.Fatalf("unexpected error envelope: %+v", payload.Error)
			}
			if bytes.Contains(rec.Body.Bytes(), tt.body) {
				t.Fatal("error response must not include request body")
			}
		})
	}
}

func assertNormalizedRequestBody(t *testing.T, req *http.Request, got, want []byte) {
	t.Helper()
	if !bytes.Equal(got, want) {
		t.Fatalf("normalized body mismatch\nwant: %q\n got: %q", want, got)
	}
	if req.Header.Get("Content-Encoding") != "" {
		t.Fatalf("Content-Encoding must be removed, got %q", req.Header.Get("Content-Encoding"))
	}
	if req.Header.Get("Content-Length") != "" {
		t.Fatalf("stale Content-Length header must be removed, got %q", req.Header.Get("Content-Length"))
	}
	if req.ContentLength != int64(len(want)) {
		t.Fatalf("ContentLength = %d, want %d", req.ContentLength, len(want))
	}
	if len(req.TransferEncoding) != 0 {
		t.Fatalf("TransferEncoding must be cleared, got %v", req.TransferEncoding)
	}
	restored, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read normalized request body: %v", err)
	}
	if !bytes.Equal(restored, want) {
		t.Fatalf("request body mismatch\nwant: %q\n got: %q", want, restored)
	}
	clonedBody, err := req.GetBody()
	if err != nil {
		t.Fatalf("GetBody returned error: %v", err)
	}
	defer clonedBody.Close()
	cloned, err := io.ReadAll(clonedBody)
	if err != nil {
		t.Fatalf("read cloned body: %v", err)
	}
	if !bytes.Equal(cloned, want) {
		t.Fatalf("cloned body mismatch\nwant: %q\n got: %q", want, cloned)
	}
}

func BenchmarkReadAndNormalizeRequestBody_Zstd(b *testing.B) {
	originalLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.Cleanup(func() {
		slog.SetDefault(originalLogger)
	})

	for _, size := range []int{1 << 20, 10 << 20} {
		b.Run(fmt.Sprintf("%dMiB", size>>20), func(b *testing.B) {
			body := []byte(`{"model":"gpt-5.4","input":"` + strings.Repeat("a", size) + `"}`)
			compressed := mustEncodeZstd(b, body)
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(compressed))
				req.Header.Set("Content-Encoding", "zstd")
				if _, bodyErr := readAndNormalizeRequestBody(httptest.NewRecorder(), req, &config.Config{}); bodyErr != nil {
					b.Fatalf("normalize zstd body: %v", bodyErr)
				}
			}
		})
	}
}

func TestServeHTTP_RequestBodyTooLarge(t *testing.T) {
	cfg := &config.Config{
		RequestBodyMaxBytes: 8,
	}
	endpointManager := endpoint.NewManager(cfg)
	handler := NewHandler(endpointManager, cfg)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"model":"gpt-5.4"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected status %d, got %d", http.StatusRequestEntityTooLarge, rec.Code)
	}
	if !strings.Contains(strings.ToLower(rec.Body.String()), "too large") {
		t.Fatalf("expected body too large message, got %q", rec.Body.String())
	}
}

func TestRequestBodyMaxBytes_DefaultDisabled(t *testing.T) {
	cfg := &config.Config{}
	if got := requestBodyMaxBytes(cfg); got != 0 {
		t.Fatalf("expected default request body max bytes to be disabled, got %d", got)
	}
}

func TestServeHTTP_RequestBodyTooLarge_TracksRequest(t *testing.T) {
	tracker, err := tracking.NewUsageTracker(&tracking.Config{
		Enabled:         true,
		DatabasePath:    ":memory:",
		BufferSize:      10,
		BatchSize:       5,
		FlushInterval:   50 * time.Millisecond,
		MaxRetry:        3,
		CleanupInterval: 24 * time.Hour,
		RetentionDays:   30,
	})
	if err != nil {
		t.Fatalf("failed to create usage tracker: %v", err)
	}
	defer tracker.Close()

	cfg := &config.Config{
		RequestBodyMaxBytes: 8,
	}
	endpointManager := endpoint.NewManager(cfg)
	handler := NewHandler(endpointManager, cfg)
	handler.SetUsageTracker(tracker)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"model":"gpt-5.4"}`))
	req = req.WithContext(context.WithValue(req.Context(), "conn_id", "req-body-too-large"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected status %d, got %d", http.StatusRequestEntityTooLarge, rec.Code)
	}

	start := time.Now().Add(-time.Minute)
	end := time.Now().Add(time.Minute)
	details, _, err := tracker.QueryRequestDetailsWithHotPool(context.Background(), &tracking.QueryOptions{
		StartDate: &start,
		EndDate:   &end,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("failed to query request details: %v", err)
	}

	for _, detail := range details {
		if detail.RequestID == "req-body-too-large" {
			if detail.FailureReason == "" {
				t.Fatalf("expected failure reason to be recorded for oversized body")
			}
			return
		}
	}

	t.Fatalf("expected oversized request to be tracked")
}
