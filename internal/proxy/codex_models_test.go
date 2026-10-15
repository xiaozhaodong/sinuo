package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sinuo/config"
	"sinuo/internal/endpoint"
	"sinuo/internal/store"
	"sinuo/internal/tracking"
)

type mockCodexModelListProvider struct {
	response []byte
	handled  bool
	err      error
}

func (m mockCodexModelListProvider) GetCodexModelListResponse(context.Context) ([]byte, bool, error) {
	return m.response, m.handled, m.err
}

type mockClaudeModelListProvider struct {
	response []byte
	err      error
}

func (m mockClaudeModelListProvider) GetClaudeModelListResponse(context.Context) ([]byte, error) {
	return m.response, m.err
}

func TestHandler_ClaudeGatewayModelsUsesAnthropicHeaderBeforeCodexRouting(t *testing.T) {
	tracker := newCodexModelsTestTracker(t)
	defer tracker.Close()

	fallbackHits := 0
	fallbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackHits++
		http.Error(w, "unexpected fallback", http.StatusBadGateway)
	}))
	defer fallbackServer.Close()

	handler := newCodexModelsPassthroughTestHandler(t, config.EndpointConfig{
		Name:     "claude-upstream",
		URL:      fallbackServer.URL,
		Priority: 1,
		Timeout:  30 * time.Second,
		Token:    "fallback-token",
	})
	handler.SetUsageTracker(tracker)
	handler.SetCodexModelListProvider(mockCodexModelListProvider{
		handled:  true,
		response: []byte(`{"object":"list","data":[{"id":"gpt-5.3-codex"}]}`),
	})
	handler.SetClaudeModelListProvider(mockClaudeModelListProvider{
		response: []byte(`{"data":[{"id":"deepseek-v4-flash[1m]","display_name":"DeepSeek V4 Flash"},{"id":"kimi-k3","display_name":"Kimi K3"},{"id":"glm-5.2[1m]","display_name":"GLM 5.2"}]}`),
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models?limit=1000", nil)
	req.Header.Set("anthropic-version", "2023-06-01")
	req = req.WithContext(context.WithValue(req.Context(), "conn_id", "req-claude-models"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if fallbackHits != 0 {
		t.Fatalf("expected Claude catalog to avoid endpoint fallback, got %d hits", fallbackHits)
	}
	var payload struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	if len(payload.Data) != 3 || payload.Data[0].ID != "deepseek-v4-flash[1m]" || payload.Data[2].ID != "glm-5.2[1m]" {
		t.Fatalf("unexpected Claude discovery response: %+v", payload.Data)
	}
	assertNoTrackedRequest(t, tracker, "req-claude-models")
}

func TestHandler_ClaudeGatewayModelsWithoutProviderReturnsAnthropicError(t *testing.T) {
	tracker := newCodexModelsTestTracker(t)
	defer tracker.Close()

	handler := newCodexModelsPassthroughTestHandler(t)
	handler.SetUsageTracker(tracker)
	handler.SetCodexModelListProvider(mockCodexModelListProvider{
		handled:  true,
		response: []byte(`{"object":"list","data":[{"id":"gpt-5.3-codex"}]}`),
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("anthropic-version", "2023-06-01")
	req = req.WithContext(context.WithValue(req.Context(), "conn_id", "req-claude-models-unavailable"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Type  string `json:"type"`
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error response failed: %v", err)
	}
	if payload.Type != "error" || payload.Error.Type != "claude_models_unavailable" {
		t.Fatalf("unexpected Anthropic error response: %+v", payload)
	}
	assertNoTrackedRequest(t, tracker, "req-claude-models-unavailable")
}

func TestHandler_LocalCodexModelsInterceptsModelsRequest(t *testing.T) {
	tracker := newCodexModelsTestTracker(t)
	defer tracker.Close()

	fallbackHits := 0
	fallbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"upstream"}]}`))
	}))
	defer fallbackServer.Close()

	handler := newCodexModelsPassthroughTestHandler(t, config.EndpointConfig{
		Name:     "codex-upstream",
		URL:      fallbackServer.URL,
		Priority: 1,
		Timeout:  30 * time.Second,
		Token:    "fallback-token",
	})
	handler.SetUsageTracker(tracker)
	handler.SetCodexModelListProvider(mockCodexModelListProvider{
		handled:  true,
		response: []byte(`{"object":"list","data":[{"id":"gpt-5.3-codex","object":"model","owned_by":"openai"}]}`),
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models?client_version=0.131.0", nil)
	req = req.WithContext(context.WithValue(req.Context(), "conn_id", "req-local-models"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if fallbackHits != 0 {
		t.Fatalf("expected local catalog to avoid endpoint fallback, got %d hits", fallbackHits)
	}

	var payload struct {
		Object string `json:"object"`
		Data   []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	if payload.Object != "list" || len(payload.Data) != 1 || payload.Data[0].ID != "gpt-5.3-codex" {
		t.Fatalf("unexpected models response: %+v", payload)
	}
	assertNoTrackedRequest(t, tracker, "req-local-models")
}

func TestHandler_CodexModelsDoesNotFallbackToClaudeEndpoint(t *testing.T) {
	tracker := newCodexModelsTestTracker(t)
	defer tracker.Close()

	fallbackHits := 0
	fallbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"upstream"}]}`))
	}))
	defer fallbackServer.Close()

	handler := newCodexModelsPassthroughTestHandler(t, config.EndpointConfig{
		Name:     "codex-upstream",
		URL:      fallbackServer.URL,
		Priority: 1,
		Timeout:  30 * time.Second,
		Token:    "fallback-token",
	})
	handler.SetUsageTracker(tracker)
	handler.SetCodexModelListProvider(mockCodexModelListProvider{handled: false})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req = req.WithContext(context.WithValue(req.Context(), "conn_id", "req-models-passthrough"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d body=%s", rec.Code, rec.Body.String())
	}
	assertJSONErrorType(t, rec, "codex_models_upstream_unavailable")
	if fallbackHits != 0 {
		t.Fatalf("expected /v1/models not to hit Claude endpoint, got %d", fallbackHits)
	}
	assertNoTrackedRequest(t, tracker, "req-models-passthrough")
}

func TestHandler_CodexModelsPassthroughUsesAccountPoolSelection(t *testing.T) {
	tracker := newCodexModelsTestTracker(t)
	defer tracker.Close()

	accountHits := 0
	accountServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accountHits++
		if r.URL.Path != "/v1/models" {
			t.Errorf("expected account pool passthrough path /v1/models, got %s", r.URL.Path)
		}
		if r.URL.RawQuery != "client_version=0.131.0" {
			t.Errorf("expected client_version query to be preserved, got %s", r.URL.RawQuery)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-selected" {
			t.Errorf("expected selected account auth header, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"account-pool-model"}]}`))
	}))
	defer accountServer.Close()

	endpointHits := 0
	endpointServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		endpointHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"endpoint-model"}]}`))
	}))
	defer endpointServer.Close()

	accountService := &mockAccountPoolService{
		accounts: []*store.UpstreamAccountRecord{
			{
				ID:            42,
				AccountName:   "selected-account",
				ProviderType:  "api_key",
				CredentialRaw: "sk-selected",
				BaseURL:       accountServer.URL,
				Enabled:       true,
				State:         "active",
			},
		},
	}
	handler := newCodexModelsAccountPoolTestHandler(t, accountService, config.EndpointConfig{
		Name:     "codex-endpoint-fallback",
		URL:      endpointServer.URL,
		Priority: 1,
		Timeout:  30 * time.Second,
		Token:    "endpoint-token",
	})
	handler.SetUsageTracker(tracker)
	handler.SetCodexModelListProvider(mockCodexModelListProvider{handled: false})

	req := httptest.NewRequest(http.MethodGet, "/v1/models?client_version=0.131.0", nil)
	req = req.WithContext(context.WithValue(req.Context(), "conn_id", "req-models-account-pool"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if accountHits != 1 {
		t.Fatalf("expected /v1/models to hit selected account once, got %d", accountHits)
	}
	if endpointHits != 0 {
		t.Fatalf("expected account pool passthrough not to hit endpoint fallback, got %d", endpointHits)
	}
	if len(accountService.previewCalls) != 1 || accountService.previewCalls[0] != "/v1/models" {
		t.Fatalf("expected account pool preview for /v1/models, got %+v", accountService.previewCalls)
	}
	if len(accountService.prepareCalls) != 0 {
		t.Fatalf("expected /v1/models not to create account schedule draft via Prepare, got %+v", accountService.prepareCalls)
	}
	assertNoTrackedRequest(t, tracker, "req-models-account-pool")
}

func TestHandler_CodexModelsAccountPoolFailsOverAfterAuthFailure(t *testing.T) {
	tracker := newCodexModelsTestTracker(t)
	defer tracker.Close()

	firstHits := 0
	firstServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstHits++
		http.Error(w, "bad token", http.StatusUnauthorized)
	}))
	defer firstServer.Close()

	secondHits := 0
	secondServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"second-account-model"}]}`))
	}))
	defer secondServer.Close()

	accountService := &mockAccountPoolService{
		accounts: []*store.UpstreamAccountRecord{
			{
				ID:            41,
				AccountName:   "expired-account",
				ProviderType:  "api_key",
				CredentialRaw: "sk-expired",
				BaseURL:       firstServer.URL,
				Enabled:       true,
				State:         "active",
			},
			{
				ID:            42,
				AccountName:   "healthy-account",
				ProviderType:  "api_key",
				CredentialRaw: "sk-healthy",
				BaseURL:       secondServer.URL,
				Enabled:       true,
				State:         "active",
			},
		},
	}
	handler := newCodexModelsAccountPoolTestHandler(t, accountService)
	handler.SetUsageTracker(tracker)
	handler.SetCodexModelListProvider(mockCodexModelListProvider{handled: false})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req = req.WithContext(context.WithValue(req.Context(), "conn_id", "req-models-account-auth-failover"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if firstHits != 1 || secondHits != 1 {
		t.Fatalf("expected first and second accounts to be tried once, got first=%d second=%d", firstHits, secondHits)
	}
	if len(accountService.authFailCalls) != 1 || accountService.authFailCalls[0].id != 41 {
		t.Fatalf("expected auth failure to be recorded for first account, got %+v", accountService.authFailCalls)
	}
	if len(accountService.successCalls) != 1 || accountService.successCalls[0] != 42 {
		t.Fatalf("expected second account success to be recorded, got %+v", accountService.successCalls)
	}
	if len(accountService.prepareCalls) != 0 || len(accountService.completeCalls) != 0 {
		t.Fatalf("expected /v1/models not to create or complete schedule drafts, prepare=%+v complete=%+v", accountService.prepareCalls, accountService.completeCalls)
	}
	assertNoTrackedRequest(t, tracker, "req-models-account-auth-failover")
}

func TestHandler_CodexModelsAccountPoolFailsOverAfterUsageLimit(t *testing.T) {
	tracker := newCodexModelsTestTracker(t)
	defer tracker.Close()

	firstHits := 0
	firstServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstHits++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"type":"usage_limit_reached","message":"limit","plan_type":"plus","resets_in_seconds":3600}}`))
	}))
	defer firstServer.Close()

	secondHits := 0
	secondServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"second-account-model"}]}`))
	}))
	defer secondServer.Close()

	accountService := &mockAccountPoolService{
		accounts: []*store.UpstreamAccountRecord{
			{
				ID:            51,
				AccountName:   "limited-account",
				ProviderType:  "api_key",
				CredentialRaw: "sk-limited",
				BaseURL:       firstServer.URL,
				Enabled:       true,
				State:         "active",
			},
			{
				ID:            52,
				AccountName:   "healthy-account",
				ProviderType:  "api_key",
				CredentialRaw: "sk-healthy",
				BaseURL:       secondServer.URL,
				Enabled:       true,
				State:         "active",
			},
		},
	}
	handler := newCodexModelsAccountPoolTestHandler(t, accountService)
	handler.SetUsageTracker(tracker)
	handler.SetCodexModelListProvider(mockCodexModelListProvider{handled: false})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req = req.WithContext(context.WithValue(req.Context(), "conn_id", "req-models-account-usage-failover"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if firstHits != 1 || secondHits != 1 {
		t.Fatalf("expected first and second accounts to be tried once, got first=%d second=%d", firstHits, secondHits)
	}
	if len(accountService.usageLimitCalls) != 1 || accountService.usageLimitCalls[0].id != 51 || accountService.usageLimitCalls[0].planType != "plus" {
		t.Fatalf("expected usage limit to be recorded for first account, got %+v", accountService.usageLimitCalls)
	}
	if len(accountService.successCalls) != 1 || accountService.successCalls[0] != 52 {
		t.Fatalf("expected second account success to be recorded, got %+v", accountService.successCalls)
	}
	if len(accountService.prepareCalls) != 0 || len(accountService.completeCalls) != 0 {
		t.Fatalf("expected /v1/models not to create or complete schedule drafts, prepare=%+v complete=%+v", accountService.prepareCalls, accountService.completeCalls)
	}
	assertNoTrackedRequest(t, tracker, "req-models-account-usage-failover")
}

func newCodexModelsPassthroughTestHandler(t *testing.T, endpoints ...config.EndpointConfig) *Handler {
	t.Helper()
	cfg := &config.Config{
		Streaming: config.StreamingConfig{
			ResponseHeaderTimeout: 5 * time.Second,
		},
		GlobalTimeout: 5 * time.Second,
		Endpoints:     endpoints,
	}
	endpointManager := endpoint.NewManager(cfg)
	return NewHandler(endpointManager, cfg)
}

func newCodexModelsAccountPoolTestHandler(t *testing.T, accountService AccountPoolService, endpoints ...config.EndpointConfig) *Handler {
	t.Helper()
	cfg := &config.Config{
		AccountPool: config.AccountPoolConfig{
			Enabled: true,
		},
		Streaming: config.StreamingConfig{
			ResponseHeaderTimeout: 5 * time.Second,
		},
		GlobalTimeout: 5 * time.Second,
		Endpoints:     endpoints,
	}
	endpointManager := endpoint.NewManager(cfg)
	handler := NewHandler(endpointManager, cfg)
	handler.SetAccountPoolService(accountService)
	return handler
}

func newCodexModelsTestTracker(t *testing.T) *tracking.UsageTracker {
	t.Helper()
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
	return tracker
}

func assertNoTrackedRequest(t *testing.T, tracker *tracking.UsageTracker, requestID string) {
	t.Helper()
	start := time.Now().Add(-time.Minute)
	end := time.Now().Add(time.Minute)
	details, _, err := tracker.QueryRequestDetailsWithHotPool(context.Background(), &tracking.QueryOptions{
		StartDate: &start,
		EndDate:   &end,
		Limit:     50,
	})
	if err != nil {
		t.Fatalf("failed to query request details: %v", err)
	}
	for _, detail := range details {
		if detail.RequestID == requestID {
			t.Fatalf("expected %s to stay out of request tracking, got %+v", requestID, detail)
		}
	}
}

func assertJSONErrorType(t *testing.T, rec *httptest.ResponseRecorder, expectedType string) {
	t.Helper()
	if contentType := rec.Header().Get("Content-Type"); !strings.Contains(contentType, "application/json") {
		t.Fatalf("expected JSON error response, got content-type=%q body=%s", contentType, rec.Body.String())
	}
	var payload struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode JSON error failed: %v body=%s", err, rec.Body.String())
	}
	if payload.Error.Type != expectedType {
		t.Fatalf("expected error type %q, got %q body=%s", expectedType, payload.Error.Type, rec.Body.String())
	}
}
