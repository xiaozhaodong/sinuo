package middleware

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"sinuo/config"
)

func TestLANAccessGate_RemoteAddressAndTokenForms(t *testing.T) {
	gate := NewLANAccessGate(nil)
	gate.Update(true, "secret")
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := gate.Wrap(next)

	tests := []struct {
		name       string
		remoteAddr string
		headers    map[string]string
		status     int
	}{
		{name: "ipv4 loopback", remoteAddr: "127.0.0.1:1234", status: http.StatusNoContent},
		{name: "ipv6 loopback", remoteAddr: "[::1]:1234", status: http.StatusNoContent},
		{name: "mapped loopback", remoteAddr: "[::ffff:127.0.0.1]:1234", status: http.StatusNoContent},
		{name: "remote missing", remoteAddr: "192.168.1.20:1234", status: http.StatusUnauthorized},
		{name: "remote bearer", remoteAddr: "192.168.1.20:1234", headers: map[string]string{"Authorization": "Bearer secret"}, status: http.StatusNoContent},
		{name: "remote api key", remoteAddr: "192.168.1.20:1234", headers: map[string]string{"x-api-key": "secret"}, status: http.StatusNoContent},
		{name: "remote bearer wins over wrong api key", remoteAddr: "192.168.1.20:1234", headers: map[string]string{"Authorization": "Bearer secret", "x-api-key": "wrong"}, status: http.StatusNoContent},
		{name: "forwarded loopback cannot bypass", remoteAddr: "192.168.1.20:1234", headers: map[string]string{"X-Forwarded-For": "127.0.0.1"}, status: http.StatusUnauthorized},
		{name: "unparseable remote", remoteAddr: "not-an-address", headers: map[string]string{"Authorization": "Bearer secret"}, status: http.StatusNoContent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://localhost/health", nil)
			req.RemoteAddr = tt.remoteAddr
			for key, value := range tt.headers {
				req.Header.Set(key, value)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)
			if recorder.Code != tt.status {
				t.Fatalf("status = %d, want %d; body=%q", recorder.Code, tt.status, recorder.Body.String())
			}
		})
	}
}

func TestLANAccessGate_FailsClosedAndAuthBypass(t *testing.T) {
	gate := NewLANAccessGate(nil)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := gate.Wrap(next)
	gate.Update(false, "secret")
	req := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)
	req.RemoteAddr = "192.168.1.20:1234"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("disabled LAN access status = %d, want 403", recorder.Code)
	}

	gate.Update(true, "")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("unconfigured LAN token status = %d, want 403", recorder.Code)
	}

	gate.Update(true, "secret")
	auth := NewAuthMiddleware(config.AuthConfig{Enabled: true, Token: "different"})
	wrapped := gate.Wrap(auth.Wrap(next))
	req.Header.Set("Authorization", "Bearer secret")
	recorder = httptest.NewRecorder()
	wrapped.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("LAN-authenticated request was rejected by auth middleware: %d", recorder.Code)
	}
}

func TestAuthMiddleware_AcceptsAPIKeyAndUpdatesConcurrently(t *testing.T) {
	auth := NewAuthMiddleware(config.AuthConfig{Enabled: true, Token: "secret"})
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := auth.Wrap(next)
	req := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)
	req.Header.Set("x-api-key", "secret")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("x-api-key status = %d, want 204", recorder.Code)
	}

	requestWithBothHeaders := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)
	requestWithBothHeaders.Header.Set("Authorization", "Bearer secret")
	requestWithBothHeaders.Header.Set("x-api-key", "wrong")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, requestWithBothHeaders)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("correct Bearer with wrong x-api-key status = %d, want 204", recorder.Code)
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			auth.UpdateConfig(config.AuthConfig{Enabled: true, Token: "secret"})
		}()
		go func() {
			defer wg.Done()
			request := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)
			request.Header.Set("Authorization", "Bearer secret")
			handler.ServeHTTP(httptest.NewRecorder(), request)
		}()
	}
	wg.Wait()
}
