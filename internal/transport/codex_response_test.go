package transport

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
)

func gzipBytes(t *testing.T, payload string) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	if _, err := writer.Write([]byte(payload)); err != nil {
		t.Fatalf("gzip write failed: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("gzip close failed: %v", err)
	}
	return buf.Bytes()
}

func brotliBytes(t *testing.T, payload string) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := brotli.NewWriter(&buf)
	if _, err := writer.Write([]byte(payload)); err != nil {
		t.Fatalf("brotli write failed: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("brotli close failed: %v", err)
	}
	return buf.Bytes()
}

func newResponse(encoding string, body []byte) *http.Response {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
	if encoding != "" {
		resp.Header.Set("Content-Encoding", encoding)
	}
	return resp
}

func TestReadCodexResponseBodyDecodesByContentEncoding(t *testing.T) {
	const payload = `{"access_token":"at","expires_in":3600}`
	cases := []struct {
		name     string
		encoding string
		body     []byte
	}{
		{name: "plain", encoding: "", body: []byte(payload)},
		{name: "identity", encoding: "identity", body: []byte(payload)},
		{name: "gzip", encoding: "gzip", body: gzipBytes(t, payload)},
		{name: "gzip-upper", encoding: "GZIP", body: gzipBytes(t, payload)},
		{name: "br", encoding: "br", body: brotliBytes(t, payload)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ReadCodexResponseBody(newResponse(tc.encoding, tc.body), 1024)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got) != payload {
				t.Fatalf("decoded body mismatch: %q", got)
			}
		})
	}
}

func TestReadCodexResponseBodyRejectsUnknownEncoding(t *testing.T) {
	_, err := ReadCodexResponseBody(newResponse("zstd", []byte("x")), 1024)
	if err == nil || !strings.Contains(err.Error(), "zstd") {
		t.Fatalf("expected unsupported encoding error, got %v", err)
	}
}

func TestReadCodexResponseBodyRejectsCorruptGzip(t *testing.T) {
	_, err := ReadCodexResponseBody(newResponse("gzip", []byte("not gzip at all")), 1024)
	if err == nil {
		t.Fatal("expected gzip decode error")
	}
}

func TestReadCodexResponseBodyLimitAppliesToDecodedBytes(t *testing.T) {
	payload := strings.Repeat("a", 2048)
	compressed := gzipBytes(t, payload)
	if int64(len(compressed)) >= 1024 {
		t.Fatalf("test precondition: compressed size %d should be below limit", len(compressed))
	}
	_, err := ReadCodexResponseBody(newResponse("gzip", compressed), 1024)
	if err == nil || !strings.Contains(err.Error(), "超过读取上限") {
		t.Fatalf("expected limit error on decoded size, got %v", err)
	}
	got, err := ReadCodexResponseBody(newResponse("gzip", gzipBytes(t, payload)), 2048)
	if err != nil || len(got) != 2048 {
		t.Fatalf("expected exact-limit body to pass, got len=%d err=%v", len(got), err)
	}
}

func TestReadCodexResponseBodyRejectsNilAndBadLimit(t *testing.T) {
	if _, err := ReadCodexResponseBody(nil, 1024); err == nil {
		t.Fatal("expected error for nil response")
	}
	if _, err := ReadCodexResponseBody(newResponse("", []byte("x")), 0); err == nil {
		t.Fatal("expected error for non-positive limit")
	}
}
