package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCopyRequestHeadersForAccountIsolatesSourceHeaders(t *testing.T) {
	src := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8088/v1/responses", nil)
	for key, value := range map[string]string{
		"Content-Type":        "application/json",
		"Accept":              "text/event-stream",
		"originator":          "codex_exec",
		"session-id":          "sess-1",
		"x-codex-turn-state":  "opaque",
		"Authorization":       "Bearer client-token",
		"Cookie":              "a=b",
		"x-api-key":           "sk-client",
		"Proxy-Authorization": "Basic zzz",
		"Forwarded":           "for=203.0.113.7",
		"X-Forwarded-For":     "203.0.113.7",
		"X-Forwarded-Proto":   "https",
		"Via":                 "1.1 edge",
		"Keep-Alive":          "timeout=5",
		"TE":                  "trailers",
		"Upgrade":             "websocket",
		"Transfer-Encoding":   "chunked",
		"Content-Encoding":    "zstd",
		"Content-Length":      "12",
		"X-Client-Dynamic":    "dyn",
	} {
		src.Header.Set(key, value)
	}
	src.Header.Add("Connection", "keep-alive, X-Client-Dynamic")
	src.Header.Add("Connection", "authorization")

	dst := httptest.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
	copyRequestHeadersForAccount(src, dst)

	for _, key := range []string{"Content-Type", "Accept", "originator", "session-id", "x-codex-turn-state"} {
		if got := dst.Header.Get(key); got != src.Header.Get(key) {
			t.Fatalf("%s should be copied, got %q", key, got)
		}
	}
	for _, key := range []string{
		"Authorization", "Cookie", "x-api-key", "Proxy-Authorization",
		"Forwarded", "X-Forwarded-For", "X-Forwarded-Proto", "Via",
		"Connection", "Keep-Alive", "TE", "Upgrade", "Transfer-Encoding",
		"Content-Encoding", "Content-Length", "X-Client-Dynamic",
	} {
		if got := dst.Header.Get(key); got != "" {
			t.Fatalf("%s must not be copied, got %q", key, got)
		}
	}
	if dst.Host != "chatgpt.com" || dst.Header.Get("Host") != "chatgpt.com" {
		t.Fatalf("Host must follow target URL, got host=%q header=%q", dst.Host, dst.Header.Get("Host"))
	}

	// Connection 点名只影响来源复制：之后由账号模块设置的目标授权不受影响。
	dst.Header.Set("Authorization", "Bearer account-token")
	if got := dst.Header.Get("Authorization"); got != "Bearer account-token" {
		t.Fatalf("target credential must be settable after isolation, got %q", got)
	}
}

func TestShouldSkipAccountResponseHeaderDropsEncodingAfterDecompression(t *testing.T) {
	header := http.Header{}
	header.Set("Connection", "X-Upstream-Hop")
	named := connectionNamedHeaders(header)
	for _, key := range []string{"Content-Encoding", "Content-Length", "Transfer-Encoding", "Connection", "X-Upstream-Hop"} {
		if !shouldSkipAccountResponseHeader(key, named) {
			t.Fatalf("%s should be skipped", key)
		}
	}
	for _, key := range []string{"Content-Type", "x-codex-turn-state", "x-codex-primary-used-percent", "x-models-etag"} {
		if shouldSkipAccountResponseHeader(key, named) {
			t.Fatalf("%s should be kept", key)
		}
	}
}
