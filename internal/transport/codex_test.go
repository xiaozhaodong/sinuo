package transport

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"sinuo/config"
)

func TestNewCodexHTTPClientDisablesAutomaticGzip(t *testing.T) {
	var gotAcceptEncoding string
	var seen bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = true
		gotAcceptEncoding = r.Header.Get("Accept-Encoding")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	for name, cfg := range map[string]*config.Config{"nil-config": nil, "proxy-disabled": {}} {
		t.Run(name, func(t *testing.T) {
			seen = false
			client, err := NewCodexHTTPClient(cfg, 5*time.Second)
			if err != nil {
				t.Fatalf("NewCodexHTTPClient: %v", err)
			}
			if client.Timeout != 5*time.Second {
				t.Fatalf("timeout = %v, want 5s", client.Timeout)
			}
			resp, err := client.Get(server.URL)
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			resp.Body.Close()
			if !seen {
				t.Fatalf("server did not receive request")
			}
			if gotAcceptEncoding != "" {
				t.Fatalf("Accept-Encoding = %q, want absent", gotAcceptEncoding)
			}
		})
	}
}

func TestNewCodexHTTPClientKeepsProxyConfig(t *testing.T) {
	cfg := &config.Config{}
	cfg.Proxy.Enabled = true
	cfg.Proxy.Type = "http"
	cfg.Proxy.URL = "http://127.0.0.1:1"
	client, err := NewCodexHTTPClient(cfg, time.Second)
	if err != nil {
		t.Fatalf("NewCodexHTTPClient: %v", err)
	}
	httpTransport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T", client.Transport)
	}
	if httpTransport.Proxy == nil {
		t.Fatalf("proxy config was dropped")
	}
	if !httpTransport.DisableCompression {
		t.Fatalf("DisableCompression must be true")
	}
}

func TestNewCodexHTTPClientRejectsInvalidProxy(t *testing.T) {
	cfg := &config.Config{}
	cfg.Proxy.Enabled = true
	cfg.Proxy.Type = "ftp"
	if _, err := NewCodexHTTPClient(cfg, time.Second); err == nil {
		t.Fatalf("expected error for unsupported proxy type")
	}
}
