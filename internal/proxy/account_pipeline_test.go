package proxy

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sinuo/config"
	"sinuo/internal/accountauth"
	"sinuo/internal/endpoint"
	servicepkg "sinuo/internal/service"
	"sinuo/internal/store"

	_ "modernc.org/sqlite"
)

const testGPT54ModelRewriteRules = `[{"paths":["/v1/responses","/v1/responses/compact"],"match":"exact","from":"gpt-5.4","to":"gpt-5.5"}]`
const testGPT54AndMiniModelRewriteRules = `[{"paths":["/v1/responses","/v1/responses/compact"],"match":"exact","from":"gpt-5.4","to":"gpt-5.5"},{"paths":["/v1/responses","/v1/responses/compact"],"match":"exact","from":"gpt-5.4-mini","to":"gpt-5.5"}]`

type accountTransientCall struct {
	id       int64
	reason   string
	cooldown time.Duration
}

type accountAuthCall struct {
	id     int64
	reason string
}

type accountSoftFailureCall struct {
	id         int64
	reason     string
	category   string
	retryAfter time.Duration
}

type accountUsageLimitCall struct {
	id       int64
	reason   string
	planType string
	resetAt  time.Time
}

type accountSchedulePrepareCall struct {
	requestID   string
	requestPath string
}

type accountScheduleCompleteCall struct {
	requestID   string
	accountID   int64
	accountName string
	outcome     string
	finalError  string
}

type codexRouteSettleCall struct {
	scope     servicepkg.CodexModelRouteScope
	accountID int64
	outcome   string
}

type mockCodexModelRouter struct {
	enabled bool
	result  servicepkg.CodexModelRouteResult
	err     error

	mu           sync.Mutex
	prepareCalls []string
	settleCalls  []codexRouteSettleCall
	negativeHits []int64
}

func (m *mockCodexModelRouter) Enabled() bool {
	return m != nil && m.enabled
}

func (m *mockCodexModelRouter) Prepare(_ context.Context, model, path string) (servicepkg.CodexModelRouteResult, error) {
	m.mu.Lock()
	m.prepareCalls = append(m.prepareCalls, model+" "+path)
	m.mu.Unlock()
	return m.result, m.err
}

func (m *mockCodexModelRouter) Settle(scope servicepkg.CodexModelRouteScope, accountID int64, outcome string) {
	m.mu.Lock()
	m.settleCalls = append(m.settleCalls, codexRouteSettleCall{scope: scope, accountID: accountID, outcome: outcome})
	m.mu.Unlock()
}

func (m *mockCodexModelRouter) RecordNegativeHit(accountID int64, _, _ string, _ uint64) {
	m.mu.Lock()
	m.negativeHits = append(m.negativeHits, accountID)
	m.mu.Unlock()
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

type mockAccountPoolService struct {
	accounts []*store.UpstreamAccountRecord
	listErr  error
	pinned   bool

	mu                sync.Mutex
	previewCalls      []string
	prepareCalls      []accountSchedulePrepareCall
	completeCalls     []accountScheduleCompleteCall
	completeSnapshots []*servicepkg.LatestAccountScheduleSnapshot
	savedDrafts       []*servicepkg.LatestAccountScheduleSnapshot
	completeErrors    []error
	completeCtxErrs   []error
	quotaRefreshCalls []int64
	successCalls      []int64
	successGuardTimes []time.Time
	successCtxErrs    []error
	authFailCalls     []accountAuthCall
	transientCalls    []accountTransientCall
	softFailureCalls  []accountSoftFailureCall
	usageLimitCalls   []accountUsageLimitCall

	softFailureShouldFailover *bool
}

func (m *mockAccountPoolService) SaveLatestScheduleSnapshot(snapshot *servicepkg.LatestAccountScheduleSnapshot) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.savedDrafts = append(m.savedDrafts, snapshot)
}

func (m *mockAccountPoolService) PrepareSchedulableAccounts(ctx context.Context, requestID, requestPath string) (servicepkg.AccountScheduleResult, error) {
	m.mu.Lock()
	m.prepareCalls = append(m.prepareCalls, accountSchedulePrepareCall{requestID: requestID, requestPath: requestPath})
	m.mu.Unlock()
	accounts, err := m.ListSchedulableAccounts(ctx)
	if err != nil {
		return servicepkg.AccountScheduleResult{}, err
	}
	return servicepkg.AccountScheduleResult{Accounts: accounts, Pinned: m.pinned}, nil
}

func (m *mockAccountPoolService) PreviewSchedulableAccounts(ctx context.Context, requestPath string) (servicepkg.AccountScheduleResult, error) {
	m.mu.Lock()
	m.previewCalls = append(m.previewCalls, requestPath)
	m.mu.Unlock()
	accounts, err := m.ListSchedulableAccounts(ctx)
	if err != nil {
		return servicepkg.AccountScheduleResult{}, err
	}
	return servicepkg.AccountScheduleResult{Accounts: accounts, Pinned: m.pinned}, nil
}

func (m *mockAccountPoolService) ListSchedulableAccounts(ctx context.Context) ([]*store.UpstreamAccountRecord, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	out := make([]*store.UpstreamAccountRecord, 0, len(m.accounts))
	for _, item := range m.accounts {
		cp := *item
		out = append(out, &cp)
	}
	return out, nil
}

func (m *mockAccountPoolService) CompleteLatestScheduleSnapshot(ctx context.Context, requestID string, accountID int64, accountName, outcome, finalError string) (*servicepkg.LatestAccountScheduleSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	callIndex := len(m.completeCalls)
	m.completeCtxErrs = append(m.completeCtxErrs, ctx.Err())
	m.completeCalls = append(m.completeCalls, accountScheduleCompleteCall{
		requestID:   requestID,
		accountID:   accountID,
		accountName: accountName,
		outcome:     outcome,
		finalError:  finalError,
	})
	var snapshot *servicepkg.LatestAccountScheduleSnapshot
	if callIndex < len(m.completeSnapshots) {
		snapshot = m.completeSnapshots[callIndex]
	}
	var err error
	if callIndex < len(m.completeErrors) {
		err = m.completeErrors[callIndex]
	}
	return snapshot, err
}

func (m *mockAccountPoolService) TryEnqueueQuotaRefresh(id int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.quotaRefreshCalls = append(m.quotaRefreshCalls, id)
	return true
}

func (m *mockAccountPoolService) MarkAccountSuccess(ctx context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.successCtxErrs = append(m.successCtxErrs, ctx.Err())
	m.successCalls = append(m.successCalls, id)
	return nil
}

func (m *mockAccountPoolService) MarkAccountSuccessIfNoNewerFailure(ctx context.Context, id int64, attemptStartedAt time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.successCtxErrs = append(m.successCtxErrs, ctx.Err())
	m.successCalls = append(m.successCalls, id)
	m.successGuardTimes = append(m.successGuardTimes, attemptStartedAt)
	return true, nil
}

func (m *mockAccountPoolService) MarkAccountAuthFailed(ctx context.Context, id int64, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.authFailCalls = append(m.authFailCalls, accountAuthCall{id: id, reason: reason})
	return nil
}

func (m *mockAccountPoolService) MarkAccountTransientFailure(ctx context.Context, id int64, reason string, cooldown time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.transientCalls = append(m.transientCalls, accountTransientCall{id: id, reason: reason, cooldown: cooldown})
	return nil
}

func (m *mockAccountPoolService) MarkAccountUsageLimitExceeded(ctx context.Context, id int64, reason, planType string, resetAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.usageLimitCalls = append(m.usageLimitCalls, accountUsageLimitCall{id: id, reason: reason, planType: planType, resetAt: resetAt})
	return nil
}

func (m *mockAccountPoolService) RecordAccountSoftFailure(ctx context.Context, id int64, reason, category string, retryAfter time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.softFailureCalls = append(m.softFailureCalls, accountSoftFailureCall{id: id, reason: reason, category: category, retryAfter: retryAfter})
	if m.softFailureShouldFailover != nil {
		return *m.softFailureShouldFailover, nil
	}
	return true, nil
}

func newAccountPipelineTestHandlerWithEnabled(t *testing.T, fallbackURL string, accountService AccountPoolService, enabled bool) *Handler {
	t.Helper()

	cfg := &config.Config{
		AccountPool: config.AccountPoolConfig{
			Enabled: enabled,
		},
		Failover: config.FailoverConfig{
			Enabled: true,
		},
		Streaming: config.StreamingConfig{
			ResponseHeaderTimeout: 5 * time.Second,
		},
		GlobalTimeout: 5 * time.Second,
		Endpoints: []config.EndpointConfig{
			{
				Name:     "fallback-endpoint",
				URL:      fallbackURL,
				Priority: 1,
				Timeout:  30 * time.Second,
				Token:    "fallback-token",
			},
		},
	}

	endpointManager := endpoint.NewManager(cfg)
	handler := NewHandler(endpointManager, cfg)
	handler.SetAccountPoolService(accountService)
	return handler
}

func newAccountPipelineTestHandler(t *testing.T, fallbackURL string, accountService AccountPoolService) *Handler {
	t.Helper()
	return newAccountPipelineTestHandlerWithEnabled(t, fallbackURL, accountService, true)
}

func performResponsesRequest(t *testing.T, handler *Handler) *httptest.ResponseRecorder {
	t.Helper()
	body := bytes.NewBufferString(`{"model":"gpt-4.1","input":"hello"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func performResponsesCompactRequest(t *testing.T, handler *Handler) *httptest.ResponseRecorder {
	t.Helper()
	body := bytes.NewBufferString(`{"model":"gpt-4.1","input":"hello"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func performStandaloneSearchRequest(t *testing.T, handler *Handler) *httptest.ResponseRecorder {
	t.Helper()
	body := bytes.NewBufferString(`{"id":"search-session","model":"gpt-5.6","commands":{"search_query":[{"q":"OpenAI Codex"}]},"settings":{"external_web_access":true}}`)
	req := httptest.NewRequest(http.MethodPost, codexStandaloneSearchPath, body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func performResponsesStreamingRequest(t *testing.T, handler *Handler) *httptest.ResponseRecorder {
	t.Helper()
	body := bytes.NewBufferString(`{"model":"gpt-4.1","input":"hello","stream":true}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func performZstdAccountRequest(t *testing.T, handler *Handler, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	compressed := mustEncodeZstd(t, body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(compressed))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "zstd")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func newRealAccountPipelineTestService(t *testing.T, accounts []*store.UpstreamAccountRecord) *servicepkg.AccountPoolService {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	schemaPath := filepath.Join("..", "tracking", "schema.sql")
	schemaSQL, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read schema failed: %v", err)
	}
	if _, err := db.Exec(string(schemaSQL)); err != nil {
		t.Fatalf("exec schema failed: %v", err)
	}

	st := store.NewSQLiteAccountPoolStore(db)
	svc := servicepkg.NewAccountPoolService(st, nil)
	t.Cleanup(func() { _ = svc.Close() })

	for _, account := range accounts {
		if account == nil {
			continue
		}
		if _, err := svc.CreateAccount(context.Background(), account); err != nil {
			t.Fatalf("create account failed: %v", err)
		}
	}

	return svc
}

func TestAccountPipeline_NoEndpointFallbackWhenAccountPoolEmpty(t *testing.T) {
	fallbackHits := 0
	fallbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer fallbackServer.Close()

	service := &mockAccountPoolService{
		accounts: []*store.UpstreamAccountRecord{},
	}
	handler := newAccountPipelineTestHandler(t, fallbackServer.URL, service)

	rec := performResponsesRequest(t, handler)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d", rec.Code)
	}
	if fallbackHits != 0 {
		t.Fatalf("expected endpoint fallback not to be called, got %d calls", fallbackHits)
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	errObj, _ := payload["error"].(map[string]any)
	if errObj["type"] != "account_pool_unavailable" {
		t.Fatalf("unexpected error type: %#v", errObj["type"])
	}
}

func TestAccountPipeline_StandaloneSearchReturnsExplicitOAuthUnavailableError(t *testing.T) {
	fallbackHits := 0
	fallbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"unexpected":true}`))
	}))
	defer fallbackServer.Close()

	service := &mockAccountPoolService{accounts: []*store.UpstreamAccountRecord{}}
	handler := newAccountPipelineTestHandler(t, fallbackServer.URL, service)

	rec := performStandaloneSearchRequest(t, handler)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d body=%s", rec.Code, rec.Body.String())
	}
	if fallbackHits != 0 {
		t.Fatalf("expected standalone search not to fall back to endpoint, got %d calls", fallbackHits)
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	errObj, _ := payload["error"].(map[string]any)
	if errObj["type"] != "codex_search_oauth_unavailable" {
		t.Fatalf("unexpected error type: %#v", errObj["type"])
	}
	if message, _ := errObj["message"].(string); !strings.Contains(message, "ChatGPT OAuth account") {
		t.Fatalf("expected OAuth-specific error message, got %#v", errObj["message"])
	}
	if len(service.completeCalls) != 1 || service.completeCalls[0].finalError != errObj["message"] {
		t.Fatalf("expected OAuth-specific schedule completion, got %+v", service.completeCalls)
	}
}

func TestFailAndFinalizeAccountScheduleSnapshot_PersistsOnlyFinalSnapshot(t *testing.T) {
	tracker := newLifecyclePanelTestTracker(t)
	const requestID = "req-account-snapshot-finalize"

	service := &mockAccountPoolService{
		completeSnapshots: []*servicepkg.LatestAccountScheduleSnapshot{
			{
				RequestID:    requestID,
				CapturedAt:   time.Now(),
				UpdatedAt:    time.Now(),
				FinalOutcome: servicepkg.AccountScheduleOutcomeTransientFailure,
				Summary:      "attempt snapshot",
			},
			{
				RequestID:    requestID,
				CapturedAt:   time.Now(),
				UpdatedAt:    time.Now(),
				FinalOutcome: servicepkg.AccountScheduleOutcomeTransientFailure,
				Summary:      "final snapshot",
			},
		},
	}
	handler := newAccountPipelineTestHandler(t, "https://example.com", service)
	handler.SetUsageTracker(tracker)

	rlm := NewRequestLifecycleManager(tracker, nil, requestID, nil)
	rlm.StartRequest("127.0.0.1", "test-agent", "POST", "/v1/responses", true)
	if err := handler.failAndFinalizeAccountScheduleSnapshot(context.Background(), requestID, 7, "acct-a", "write failed"); err != nil {
		t.Fatalf("failAndFinalizeAccountScheduleSnapshot failed: %v", err)
	}

	if len(service.completeCalls) != 2 {
		t.Fatalf("expected attempt plus terminal completion, got %+v", service.completeCalls)
	}
	if service.completeCalls[0].accountID != 7 || service.completeCalls[1].accountID != 0 {
		t.Fatalf("unexpected completion order: %+v", service.completeCalls)
	}

	data, err := tracker.GetRequestLifecycleData(context.Background(), requestID)
	if err != nil {
		t.Fatalf("GetRequestLifecycleData failed: %v", err)
	}
	if data == nil || data.ScheduleSnapshotJSON == "" {
		t.Fatalf("expected finalized schedule snapshot, got %+v", data)
	}
	var persisted map[string]interface{}
	if err := json.Unmarshal([]byte(data.ScheduleSnapshotJSON), &persisted); err != nil {
		t.Fatalf("unmarshal persisted schedule snapshot failed: %v", err)
	}
	if persisted["summary"] != "final snapshot" {
		t.Fatalf("expected only final snapshot persisted, got %v", persisted["summary"])
	}
}

func TestAccountPipeline_StandaloneSearchUsesAccountPoolInsteadOfEndpoint(t *testing.T) {
	fallbackHits := 0
	fallbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"unexpected":true}`))
	}))
	defer fallbackServer.Close()

	service := &mockAccountPoolService{accounts: []*store.UpstreamAccountRecord{
		{ID: 91, AccountName: "search-account", ProviderType: "chatgpt_refresh_token", CredentialRaw: `{"refresh_token":"rt-search","access_token":"at-search","expires_at":"2099-01-01T00:00:00Z","chatgpt_account_id":"acc-search"}`, EnableRequestCompression: true, Enabled: true},
	}}
	handler := newAccountPipelineTestHandler(t, fallbackServer.URL, service)
	accountHits := 0
	handler.accountHTTPInitOnce.Do(func() {})
	handler.accountHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		accountHits++
		if r.URL.String() != chatGPTCodexSearchURL {
			t.Errorf("expected standalone search target %s, got %s", chatGPTCodexSearchURL, r.URL.String())
		}
		if got := r.Header.Get("Authorization"); got != "Bearer at-search" {
			t.Errorf("expected OAuth access token, got %q", got)
		}
		if got := r.Header.Get("chatgpt-account-id"); got != "acc-search" {
			t.Errorf("expected ChatGPT account id, got %q", got)
		}
		if got := r.Header.Get("Content-Encoding"); got != "" {
			t.Errorf("standalone search must remain uncompressed, got Content-Encoding=%q", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"encrypted_output":null,"output":"search result","results":[]}`)),
			Request:    r,
		}, nil
	})}
	handler.accountSSEHTTPClient = handler.accountHTTPClient

	rec := performStandaloneSearchRequest(t, handler)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if accountHits != 1 {
		t.Fatalf("expected account pool upstream to be called once, got %d", accountHits)
	}
	if fallbackHits != 0 {
		t.Fatalf("expected Claude endpoint fallback not to be called, got %d calls", fallbackHits)
	}
	if len(service.prepareCalls) != 1 || service.prepareCalls[0].requestPath != codexStandaloneSearchPath {
		t.Fatalf("expected search path in account scheduling snapshot, got %+v", service.prepareCalls)
	}
}

func TestAccountPipeline_StandaloneSearchDoesNotUseEndpointWhenAccountPoolDisabled(t *testing.T) {
	fallbackHits := 0
	fallbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"unexpected":true}`))
	}))
	defer fallbackServer.Close()

	handler := newAccountPipelineTestHandlerWithEnabled(t, fallbackServer.URL, nil, false)
	rec := performStandaloneSearchRequest(t, handler)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d body=%s", rec.Code, rec.Body.String())
	}
	if fallbackHits != 0 {
		t.Fatalf("expected disabled account pool not to fall back to Claude endpoint, got %d calls", fallbackHits)
	}
}

func TestAccountPipeline_DoesNotUseEndpointWhenAccountPoolDisabled(t *testing.T) {
	fallbackHits := 0
	fallbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer fallbackServer.Close()

	handler := newAccountPipelineTestHandlerWithEnabled(t, fallbackServer.URL, nil, false)

	rec := performResponsesRequest(t, handler)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d", rec.Code)
	}
	if fallbackHits != 0 {
		t.Fatalf("expected endpoint fallback not to be called, got %d calls", fallbackHits)
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	errObj, _ := payload["error"].(map[string]any)
	if errObj["type"] != "account_pool_disabled" {
		t.Fatalf("unexpected error type: %#v", errObj["type"])
	}
	if errObj["message"] != "account pool is disabled for Codex /v1/responses, /v1/responses/compact, and /v1/alpha/search" {
		t.Fatalf("unexpected error message: %#v", errObj["message"])
	}
}

func TestAccountPipeline_DoesNotUseEndpointWhenServiceNotReady(t *testing.T) {
	fallbackHits := 0
	fallbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer fallbackServer.Close()

	handler := newAccountPipelineTestHandlerWithEnabled(t, fallbackServer.URL, nil, true)

	rec := performResponsesRequest(t, handler)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d", rec.Code)
	}
	if fallbackHits != 0 {
		t.Fatalf("expected endpoint fallback not to be called, got %d calls", fallbackHits)
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	errObj, _ := payload["error"].(map[string]any)
	if errObj["type"] != "account_pool_unavailable" {
		t.Fatalf("unexpected error type: %#v", errObj["type"])
	}
	if errObj["message"] != "account pool service is not initialized for Codex /v1/responses, /v1/responses/compact, and /v1/alpha/search" {
		t.Fatalf("unexpected error message: %#v", errObj["message"])
	}
}

func TestShouldUseAccountPipeline_RequiresEnabledAndInitializedService(t *testing.T) {
	handler := &Handler{
		config: &config.Config{
			AccountPool: config.AccountPoolConfig{Enabled: true},
		},
	}
	if handler.shouldUseAccountPipeline("/v1/responses") {
		t.Fatal("expected disabled routing when service is nil")
	}

	handler.accountPoolService = &mockAccountPoolService{}
	if !handler.shouldUseAccountPipeline("/v1/responses") {
		t.Fatal("expected account pipeline to be enabled when service is ready")
	}

	handler.config.AccountPool.Enabled = false
	if handler.shouldUseAccountPipeline("/v1/responses") {
		t.Fatal("expected disabled routing when account_pool.enabled=false")
	}
}

func TestShouldUseAccountPipeline_SupportsResponsesCompactPath(t *testing.T) {
	handler := &Handler{
		config: &config.Config{
			AccountPool: config.AccountPoolConfig{Enabled: true},
		},
		accountPoolService: &mockAccountPoolService{},
	}

	if !handler.shouldUseAccountPipeline("/v1/responses/compact") {
		t.Fatal("expected /v1/responses/compact to use account pipeline")
	}

	if !handler.isAccountPipelinePath("/v1/responses/compact") {
		t.Fatal("expected /v1/responses/compact to be treated as account pipeline path")
	}

	if !handler.shouldUseAccountPipeline(codexStandaloneSearchPath) {
		t.Fatalf("expected %s to use account pipeline", codexStandaloneSearchPath)
	}

	if !handler.isAccountPipelinePath(codexStandaloneSearchPath) {
		t.Fatalf("expected %s to be treated as account pipeline path", codexStandaloneSearchPath)
	}
}

func TestHandler_GetAccountHTTPClient_ReusesSharedTransport(t *testing.T) {
	handler := newAccountPipelineTestHandlerWithEnabled(t, "https://example.com", nil, true)

	regularClient1, err := handler.getAccountHTTPClient(false)
	if err != nil {
		t.Fatalf("getAccountHTTPClient(false) failed: %v", err)
	}
	regularClient2, err := handler.getAccountHTTPClient(false)
	if err != nil {
		t.Fatalf("second getAccountHTTPClient(false) failed: %v", err)
	}
	sseClient, err := handler.getAccountHTTPClient(true)
	if err != nil {
		t.Fatalf("getAccountHTTPClient(true) failed: %v", err)
	}

	if regularClient1 != regularClient2 {
		t.Fatal("expected regular account client to be reused")
	}
	if regularClient1.Transport == nil || sseClient.Transport == nil {
		t.Fatal("expected shared transport to be initialized")
	}
	if regularClient1.Transport != sseClient.Transport {
		t.Fatal("expected regular and SSE clients to share the same transport")
	}
	httpTransport, ok := regularClient1.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected shared transport to be *http.Transport, got %T", regularClient1.Transport)
	}
	if httpTransport.MaxIdleConnsPerHost != 16 {
		t.Fatalf("expected MaxIdleConnsPerHost=16, got %d", httpTransport.MaxIdleConnsPerHost)
	}
	if httpTransport.MaxConnsPerHost != 32 {
		t.Fatalf("expected MaxConnsPerHost=32, got %d", httpTransport.MaxConnsPerHost)
	}
	if regularClient1.Timeout <= 0 {
		t.Fatalf("expected regular client timeout to be set, got %v", regularClient1.Timeout)
	}
	if sseClient.Timeout != 0 {
		t.Fatalf("expected SSE client timeout to be disabled, got %v", sseClient.Timeout)
	}
}

func TestAccountPipeline_StreamForwardContextDoesNotUseGlobalTimeout(t *testing.T) {
	handler := newAccountPipelineTestHandlerWithEnabled(t, "https://example.com", nil, true)
	handler.config.GlobalTimeout = 10 * time.Millisecond

	parent, parentCancel := context.WithCancel(context.Background())
	defer parentCancel()

	streamCtx, release := handler.newAccountStreamForwardContext(parent)
	defer release()

	if _, ok := streamCtx.Deadline(); ok {
		t.Fatal("expected account SSE forward context to have no global timeout deadline")
	}

	select {
	case <-streamCtx.Done():
		t.Fatalf("expected stream context to outlive global_timeout, got %v", streamCtx.Err())
	case <-time.After(30 * time.Millisecond):
	}

	parentCancel()

	select {
	case <-streamCtx.Done():
	case <-time.After(handler.accountStreamTailDrainTimeout() + 500*time.Millisecond):
		t.Fatal("expected stream context to be cancelled after parent cancel tail drain")
	}
}

func TestAccountPipeline_PreparesAndCompletesLatestScheduleSnapshot(t *testing.T) {
	upstreamHits := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_snapshot","status":"completed","output":[]}`))
	}))
	defer upstream.Close()

	service := &mockAccountPoolService{
		accounts: []*store.UpstreamAccountRecord{
			{ID: 7, AccountName: "snapshot-main", ProviderType: "api_key", CredentialRaw: "sk-snapshot", BaseURL: upstream.URL, Enabled: true},
		},
	}
	handler := newAccountPipelineTestHandler(t, upstream.URL, service)
	handler.accountHTTPInitOnce.Do(func() {})
	handler.accountHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		upstreamHits++
		body := io.NopCloser(strings.NewReader(`{"id":"resp_oauth","status":"completed","output":[]}`))
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       body,
			Request:    req,
		}, nil
	})}
	handler.accountSSEHTTPClient = handler.accountHTTPClient

	rec := performResponsesRequest(t, handler)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if upstreamHits != 1 {
		t.Fatalf("expected upstream to be called once, got %d", upstreamHits)
	}
	if len(service.prepareCalls) != 1 {
		t.Fatalf("expected one prepare call, got %+v", service.prepareCalls)
	}
	if service.prepareCalls[0].requestPath != "/v1/responses" {
		t.Fatalf("expected request path /v1/responses, got %+v", service.prepareCalls[0])
	}
	if service.prepareCalls[0].requestID == "" {
		t.Fatalf("expected non-empty request id in prepare call, got %+v", service.prepareCalls[0])
	}
	if len(service.completeCalls) == 0 {
		t.Fatalf("expected complete call, got %+v", service.completeCalls)
	}
	last := service.completeCalls[len(service.completeCalls)-1]
	if last.outcome != servicepkg.AccountScheduleOutcomeSuccess {
		t.Fatalf("expected success outcome, got %+v", last)
	}
	if last.accountID != 7 || last.accountName != "snapshot-main" {
		t.Fatalf("expected selected account to be recorded, got %+v", last)
	}
	if last.requestID == "" {
		t.Fatalf("expected request id in complete call, got %+v", last)
	}
	if len(service.quotaRefreshCalls) != 0 {
		t.Fatalf("expected api_key success not to enqueue quota refresh, got %+v", service.quotaRefreshCalls)
	}
}

func TestAccountPipeline_CodexUnassignedCompletesRoutingSnapshot(t *testing.T) {
	service := &mockAccountPoolService{}
	router := &mockCodexModelRouter{
		enabled: true,
		result: servicepkg.CodexModelRouteResult{
			Scope: servicepkg.CodexModelRouteScope{
				Kind:             "unassigned",
				Model:            "gpt-5-codex",
				SnapshotVersion:  7,
				UnassignedReason: servicepkg.CodexModelRoutingUnassigned,
			},
		},
	}
	handler := newAccountPipelineTestHandler(t, "https://example.com", service)
	handler.SetCodexModelRouter(router)

	rec := performResponsesRequest(t, handler)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected status 422, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(service.savedDrafts) != 1 {
		t.Fatalf("expected one routing snapshot draft, got %d", len(service.savedDrafts))
	}
	draft := service.savedDrafts[0]
	if draft.RequestModel != "gpt-4.1" || draft.ModelRoutingFamily != "codex" || draft.ModelRoutingVersion != 7 || draft.RouteScopeKind != "unassigned" {
		t.Fatalf("unexpected routing snapshot draft: %#v", draft)
	}
	if len(service.completeCalls) != 1 || service.completeCalls[0].outcome != servicepkg.CodexModelRoutingUnassigned {
		t.Fatalf("expected unassigned terminal snapshot, got %+v", service.completeCalls)
	}
}

func TestAccountPipeline_CodexNoAvailableUsesRoutingOutcome(t *testing.T) {
	service := &mockAccountPoolService{}
	router := &mockCodexModelRouter{
		enabled: true,
		result: servicepkg.CodexModelRouteResult{
			Scope: servicepkg.CodexModelRouteScope{
				Kind:            "upstream_group",
				GroupID:         11,
				Revision:        2,
				SnapshotVersion: 8,
				Mode:            "auto",
				Model:           "gpt-4.1",
			},
			Candidates: []servicepkg.AccountScheduleCandidateDecision{{AccountID: 9, Decision: "skipped", Reason: "cooldown"}},
		},
	}
	handler := newAccountPipelineTestHandler(t, "https://example.com", service)
	handler.SetCodexModelRouter(router)

	rec := performResponsesRequest(t, handler)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(service.completeCalls) != 1 || service.completeCalls[0].outcome != servicepkg.CodexModelRoutingNoAvailable {
		t.Fatalf("expected model-routing no-available outcome, got %+v", service.completeCalls)
	}
	if len(router.settleCalls) != 1 || router.settleCalls[0].outcome != servicepkg.CodexModelRoutingNoAvailable {
		t.Fatalf("expected model-routing settle outcome, got %+v", router.settleCalls)
	}
}

func TestAccountPipeline_CodexFailureSettlesLastOutcome(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"temporary"}`))
	}))
	defer upstream.Close()

	account := &store.UpstreamAccountRecord{ID: 12, AccountName: "codex-failing", ProviderType: "api_key", CredentialRaw: "sk-test", BaseURL: upstream.URL, Enabled: true}
	service := &mockAccountPoolService{accounts: []*store.UpstreamAccountRecord{account}}
	router := &mockCodexModelRouter{
		enabled: true,
		result: servicepkg.CodexModelRouteResult{
			Accounts: []*store.UpstreamAccountRecord{account},
			Scope:    servicepkg.CodexModelRouteScope{Kind: "upstream_group", GroupID: 13, Revision: 1, SnapshotVersion: 9, Mode: "auto", Model: "gpt-4.1"},
		},
	}
	handler := newAccountPipelineTestHandler(t, upstream.URL, service)
	handler.SetCodexModelRouter(router)

	rec := performResponsesRequest(t, handler)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(router.settleCalls) != 1 || router.settleCalls[0].outcome != servicepkg.AccountScheduleOutcomeTransientFailure || router.settleCalls[0].accountID != account.ID {
		t.Fatalf("expected last Codex failure to settle runtime, got %+v", router.settleCalls)
	}
}

func TestAccountPipeline_PreparesCompactRequestPath(t *testing.T) {
	service := &mockAccountPoolService{
		accounts: []*store.UpstreamAccountRecord{
			{ID: 8, AccountName: "compact-main", ProviderType: "api_key", CredentialRaw: "sk-compact", BaseURL: "https://example.com", Enabled: true},
		},
	}
	handler := newAccountPipelineTestHandler(t, "https://example.com", service)
	handler.accountHTTPInitOnce.Do(func() {})
	handler.accountHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/v1/responses/compact" {
			t.Fatalf("expected upstream path /v1/responses/compact, got %s", req.URL.Path)
		}
		body := io.NopCloser(strings.NewReader(`{"output":[]}`))
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       body,
			Request:    req,
		}, nil
	})}
	handler.accountSSEHTTPClient = handler.accountHTTPClient

	rec := performResponsesCompactRequest(t, handler)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(service.prepareCalls) != 1 {
		t.Fatalf("expected one prepare call, got %+v", service.prepareCalls)
	}
	if service.prepareCalls[0].requestPath != "/v1/responses/compact" {
		t.Fatalf("expected request path /v1/responses/compact, got %+v", service.prepareCalls[0])
	}
}

func TestAccountPipeline_ZstdRequestsReachAPIKeyUpstreamAsPlainJSON(t *testing.T) {
	body := []byte(`{"model":"gpt-4.1","input":"desktop compressed"}`)
	for _, path := range []string{"/v1/responses", "/v1/responses/compact"} {
		t.Run(path, func(t *testing.T) {
			var receivedBody []byte
			var receivedContentLength int64
			var receivedContentEncoding string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				receivedBody, _ = io.ReadAll(r.Body)
				receivedContentLength = r.ContentLength
				receivedContentEncoding = r.Header.Get("Content-Encoding")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"resp_zstd","status":"completed","output":[]}`))
			}))
			defer upstream.Close()

			service := &mockAccountPoolService{accounts: []*store.UpstreamAccountRecord{
				{ID: 81, AccountName: "api-key-zstd", ProviderType: "api_key", CredentialRaw: "sk-zstd", BaseURL: upstream.URL, Enabled: true},
			}}
			handler := newAccountPipelineTestHandler(t, upstream.URL, service)

			rec := performZstdAccountRequest(t, handler, path, body)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
			}
			if !bytes.Equal(receivedBody, body) {
				t.Fatalf("upstream body mismatch\nwant: %s\n got: %s", body, receivedBody)
			}
			if receivedContentEncoding != "" {
				t.Fatalf("upstream Content-Encoding must be empty, got %q", receivedContentEncoding)
			}
			if receivedContentLength != int64(len(body)) {
				t.Fatalf("upstream ContentLength = %d, want %d", receivedContentLength, len(body))
			}
		})
	}
}

func TestAccountPipeline_ZstdRequestSupportsConfiguredModelRewrite(t *testing.T) {
	var receivedBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer upstream.Close()

	service := &mockAccountPoolService{accounts: []*store.UpstreamAccountRecord{
		{ID: 82, AccountName: "rewrite-zstd", ProviderType: "api_key", CredentialRaw: "sk-rewrite", BaseURL: upstream.URL, ModelRewriteRules: testGPT54ModelRewriteRules, Enabled: true},
	}}
	handler := newAccountPipelineTestHandler(t, upstream.URL, service)

	rec := performZstdAccountRequest(t, handler, "/v1/responses", []byte(`{"model":"gpt-5.4","input":"hello"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(receivedBody, &payload); err != nil {
		t.Fatalf("decode upstream body: %v body=%s", err, receivedBody)
	}
	if payload["model"] != "gpt-5.5" {
		t.Fatalf("expected rewritten model gpt-5.5, got %v", payload["model"])
	}
}

func TestAccountPipeline_ZstdRequestFailoverReusesNormalizedBody(t *testing.T) {
	body := []byte(`{"model":"gpt-4.1","input":"retry same plaintext"}`)
	var receivedBodies [][]byte
	var receivedEncodings []string
	firstServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ := io.ReadAll(r.Body)
		receivedBodies = append(receivedBodies, received)
		receivedEncodings = append(receivedEncodings, r.Header.Get("Content-Encoding"))
		http.Error(w, "invalid session", http.StatusUnauthorized)
	}))
	defer firstServer.Close()
	secondServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ := io.ReadAll(r.Body)
		receivedBodies = append(receivedBodies, received)
		receivedEncodings = append(receivedEncodings, r.Header.Get("Content-Encoding"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_ok","status":"completed","output":[]}`))
	}))
	defer secondServer.Close()

	service := &mockAccountPoolService{accounts: []*store.UpstreamAccountRecord{
		{ID: 83, AccountName: "zstd-first", ProviderType: "api_key", CredentialRaw: "sk-first", BaseURL: firstServer.URL, Enabled: true},
		{ID: 84, AccountName: "zstd-second", ProviderType: "api_key", CredentialRaw: "sk-second", BaseURL: secondServer.URL, Enabled: true},
	}}
	handler := newAccountPipelineTestHandler(t, firstServer.URL, service)

	rec := performZstdAccountRequest(t, handler, "/v1/responses", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(receivedBodies) != 2 {
		t.Fatalf("expected two upstream attempts, got %d", len(receivedBodies))
	}
	for idx := range receivedBodies {
		if !bytes.Equal(receivedBodies[idx], body) {
			t.Fatalf("attempt %d body mismatch\nwant: %s\n got: %s", idx+1, body, receivedBodies[idx])
		}
		if receivedEncodings[idx] != "" {
			t.Fatalf("attempt %d Content-Encoding must be empty, got %q", idx+1, receivedEncodings[idx])
		}
	}
}

func TestAccountPipeline_ZstdRequestCompressionReusesOriginalFrame(t *testing.T) {
	body := []byte(`{"model":"gpt-4.1","input":"reuse compressed frame"}`)
	originalCompressed := mustEncodeZstd(t, body)
	var received []byte
	var receivedEncoding string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		receivedEncoding = r.Header.Get("Content-Encoding")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_reuse","status":"completed","output":[]}`))
	}))
	defer upstream.Close()

	service := &mockAccountPoolService{accounts: []*store.UpstreamAccountRecord{
		{ID: 85, AccountName: "reuse-zstd", ProviderType: "api_key", CredentialRaw: "sk-reuse", BaseURL: upstream.URL, EnableRequestCompression: true, Enabled: true},
	}}
	handler := newAccountPipelineTestHandler(t, upstream.URL, service)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(originalCompressed))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "zstd")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if receivedEncoding != "zstd" {
		t.Fatalf("expected zstd upstream encoding, got %q", receivedEncoding)
	}
	if !bytes.Equal(received, originalCompressed) {
		t.Fatalf("expected original compressed frame to be reused")
	}
}

// 隐私过滤器原地修改正文（切片不变）时，不能因切片相等而复用含原文的客户端压缩帧。
func TestAccountPipeline_RequestCompressionRecompressesAfterInPlacePrivacyEdit(t *testing.T) {
	body := []byte(`{"model":"gpt-4.1","input":"SECRET"}`)
	originalCompressed := mustEncodeZstd(t, body)
	var received []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_inplace","status":"completed","output":[]}`))
	}))
	defer upstream.Close()

	service := &mockAccountPoolService{accounts: []*store.UpstreamAccountRecord{
		{ID: 87, AccountName: "inplace-zstd", ProviderType: "api_key", CredentialRaw: "sk-inplace", BaseURL: upstream.URL, EnableRequestCompression: true, Enabled: true},
	}}
	handler := newAccountPipelineTestHandler(t, upstream.URL, service)
	handler.SetPrivacyFilter(identityPrivacyMutator(func(raw []byte) []byte {
		if at := bytes.Index(raw, []byte("SECRET")); at >= 0 {
			copy(raw[at:], "XXXXXX")
		}
		return raw
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(originalCompressed))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "zstd")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if bytes.Equal(received, originalCompressed) || bytes.Contains(zstdDecompress(t, received), []byte("SECRET")) {
		t.Fatalf("在原地脱敏后仍发送了原始压缩帧")
	}
}

func TestAccountPipeline_RequestCompressionRecompressesAfterModelRewrite(t *testing.T) {
	var received []byte
	var receivedEncoding string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		receivedEncoding = r.Header.Get("Content-Encoding")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_recompress","status":"completed","output":[]}`))
	}))
	defer upstream.Close()

	service := &mockAccountPoolService{accounts: []*store.UpstreamAccountRecord{
		{ID: 86, AccountName: "recompress-zstd", ProviderType: "api_key", CredentialRaw: "sk-recompress", BaseURL: upstream.URL, EnableRequestCompression: true, ModelRewriteRules: testGPT54ModelRewriteRules, Enabled: true},
	}}
	handler := newAccountPipelineTestHandler(t, upstream.URL, service)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"model":"gpt-5.4","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if receivedEncoding != "zstd" {
		t.Fatalf("expected zstd upstream encoding, got %q", receivedEncoding)
	}
	decoded, err := decodeZstdRequestBody(received, defaultEncodedRequestBodyMaxBytes)
	if err != nil {
		t.Fatalf("decode upstream zstd body: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(decoded, &payload); err != nil {
		t.Fatalf("decode upstream JSON body: %v", err)
	}
	if payload["model"] != "gpt-5.5" {
		t.Fatalf("expected rewritten model gpt-5.5, got %v", payload["model"])
	}
}

func TestAccountPipeline_RequestCompression415DoesNotFallback(t *testing.T) {
	var calls int
	var receivedEncoding string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		receivedEncoding = r.Header.Get("Content-Encoding")
		http.Error(w, "zstd unsupported", http.StatusUnsupportedMediaType)
	}))
	defer upstream.Close()

	service := &mockAccountPoolService{accounts: []*store.UpstreamAccountRecord{
		{ID: 87, AccountName: "reject-zstd", ProviderType: "api_key", CredentialRaw: "sk-reject", BaseURL: upstream.URL, EnableRequestCompression: true, Enabled: true},
	}}
	handler := newAccountPipelineTestHandler(t, upstream.URL, service)
	rec := performResponsesRequest(t, handler)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected status 415, got %d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 1 {
		t.Fatalf("expected no plaintext fallback attempt, got %d calls", calls)
	}
	if receivedEncoding != "zstd" {
		t.Fatalf("expected rejected request to use zstd, got %q", receivedEncoding)
	}
}

func TestAccountPipeline_OAuthCompressionSwitchSendsZstd(t *testing.T) {
	var receivedBody []byte
	var receivedEncoding string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		receivedEncoding = r.Header.Get("Content-Encoding")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"oauth-explicit-plain","status":"completed","output":[]}`))
	}))
	defer upstream.Close()

	service := &mockAccountPoolService{accounts: []*store.UpstreamAccountRecord{
		{ID: 88, AccountName: "oauth-explicit-compression", ProviderType: "chatgpt_refresh_token", CredentialRaw: `{"refresh_token":"rt-explicit","access_token":"at-explicit","expires_at":"2099-01-01T00:00:00Z","chatgpt_account_id":"acc-explicit"}`, BaseURL: upstream.URL, EnableRequestCompression: true, Enabled: true},
	}}
	handler := newAccountPipelineTestHandler(t, upstream.URL, service)
	handler.accountHTTPInitOnce.Do(func() {})
	handler.accountHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		receivedBody, _ = io.ReadAll(req.Body)
		receivedEncoding = req.Header.Get("Content-Encoding")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"oauth-explicit-plain","status":"completed","output":[]}`)),
			Request:    req,
		}, nil
	})}
	handler.accountSSEHTTPClient = handler.accountHTTPClient
	body := []byte(`{"model":"gpt-4.1","input":"oauth explicit switch"}`)
	rec := performZstdAccountRequest(t, handler, "/v1/responses", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if receivedEncoding != requestContentEncodingZstd {
		t.Fatalf("OAuth upstream Content-Encoding = %q, want zstd", receivedEncoding)
	}
	decoded, err := decodeZstdRequestBody(receivedBody, defaultEncodedRequestBodyMaxBytes)
	if err != nil {
		t.Fatalf("decode OAuth upstream zstd body: %v", err)
	}
	if !bytes.Equal(decoded, body) {
		t.Fatalf("OAuth upstream body mismatch\nwant: %s\n got: %s", body, decoded)
	}
}

func TestPrepareAccountOutboundBody_APIKeyAndOAuthCanCompress(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-4.1"}`))
	metadata := &normalizedRequestBodyMetadata{normalizedBody: []byte(`{"model":"gpt-4.1"}`)}
	attachNormalizedRequestBodyMetadata(req, metadata)
	body := metadata.normalizedBody

	for _, providerType := range []string{"api_key", "chatgpt_refresh_token", "oauth"} {
		wire, sendZstd, mode, err := prepareAccountOutboundBody(req, body, &store.UpstreamAccountRecord{
			ProviderType:             providerType,
			EnableRequestCompression: true,
		})
		if err != nil {
			t.Fatalf("provider %s returned error: %v", providerType, err)
		}
		if !sendZstd || mode != "recompress" {
			t.Fatalf("provider %s must use zstd: sendZstd=%v mode=%s", providerType, sendZstd, mode)
		}
		decoded, err := decodeZstdRequestBody(wire, defaultEncodedRequestBodyMaxBytes)
		if err != nil {
			t.Fatalf("decode provider %s zstd body: %v", providerType, err)
		}
		if !bytes.Equal(decoded, body) {
			t.Fatalf("provider %s body mismatch: %q", providerType, decoded)
		}
	}

	wire, sendZstd, mode, err := prepareAccountOutboundBody(req, body, &store.UpstreamAccountRecord{
		ProviderType:             "session_cookie",
		EnableRequestCompression: true,
	})
	if err != nil {
		t.Fatalf("session cookie provider returned error: %v", err)
	}
	if sendZstd || mode != "disabled" || !bytes.Equal(wire, body) {
		t.Fatalf("session cookie provider must remain plaintext: sendZstd=%v mode=%s body=%q", sendZstd, mode, wire)
	}
}

func TestPrepareAccountOutboundBody_StandaloneSearchRemainsPlainJSON(t *testing.T) {
	body := []byte(`{"id":"search-session","model":"gpt-5.6"}`)
	req := httptest.NewRequest(http.MethodPost, codexStandaloneSearchPath, bytes.NewReader(body))
	attachNormalizedRequestBodyMetadata(req, &normalizedRequestBodyMetadata{normalizedBody: body})

	wire, sendZstd, mode, err := prepareAccountOutboundBody(req, body, &store.UpstreamAccountRecord{
		ProviderType:             "api_key",
		EnableRequestCompression: true,
	})
	if err != nil {
		t.Fatalf("prepareAccountOutboundBody returned error: %v", err)
	}
	if sendZstd || mode != "disabled" || !bytes.Equal(wire, body) {
		t.Fatalf("standalone search must remain plaintext: sendZstd=%v mode=%s body=%q", sendZstd, mode, wire)
	}
}

func TestAccountPipeline_ConfiguredRuleRewritesUnsupportedCodexModelBeforeForward(t *testing.T) {
	var receivedModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode upstream body failed: %v", err)
		}
		receivedModel, _ = payload["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer upstream.Close()

	service := &mockAccountPoolService{
		accounts: []*store.UpstreamAccountRecord{
			{ID: 9, AccountName: "anyrouter", ProviderType: "api_key", CredentialRaw: "sk-anyrouter", BaseURL: upstream.URL, ModelRewriteRules: testGPT54ModelRewriteRules, Enabled: true},
		},
	}
	handler := newAccountPipelineTestHandler(t, upstream.URL, service)

	body := bytes.NewBufferString(`{"model":"gpt-5.4","input":"hello"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if receivedModel != "gpt-5.5" {
		t.Fatalf("expected upstream model gpt-5.5, got %q", receivedModel)
	}
}

func TestAccountPipeline_OAuthSuccessEnqueuesQuotaRefresh(t *testing.T) {
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at-oauth","refresh_token":"rt-oauth","expires_in":3600}`))
	}))
	defer authServer.Close()

	oldRefreshURL := accountauth.CurrentOpenAIRefreshTokenURLForTest()
	t.Cleanup(func() {
		accountauth.SetOpenAIRefreshTokenURLForTest(oldRefreshURL)
	})
	accountauth.SetOpenAIRefreshTokenURLForTest(authServer.URL)

	upstreamHits := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_oauth","status":"completed","output":[]}`))
	}))
	defer upstream.Close()

	service := &mockAccountPoolService{
		accounts: []*store.UpstreamAccountRecord{
			{ID: 17, AccountName: "oauth-main", ProviderType: "chatgpt_refresh_token", CredentialRaw: `{"refresh_token":"rt-1","chatgpt_account_id":"acc-1"}`, BaseURL: upstream.URL, Enabled: true},
		},
	}
	handler := newAccountPipelineTestHandler(t, upstream.URL, service)
	handler.accountHTTPInitOnce.Do(func() {})
	handler.accountHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		upstreamHits++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"resp_oauth","status":"completed","output":[]}`)),
			Request:    req,
		}, nil
	})}
	handler.accountSSEHTTPClient = handler.accountHTTPClient

	rec := performResponsesRequest(t, handler)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if upstreamHits != 1 {
		t.Fatalf("expected upstream to be called once, got %d", upstreamHits)
	}
	if len(service.quotaRefreshCalls) != 1 || service.quotaRefreshCalls[0] != 17 {
		t.Fatalf("expected oauth success to enqueue quota refresh, got %+v", service.quotaRefreshCalls)
	}
}

func TestAccountPipeline_ZstdRequestReachesOAuthAsPlainJSONWhenCompressionDisabled(t *testing.T) {
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at-zstd","refresh_token":"rt-zstd","expires_in":3600}`))
	}))
	defer authServer.Close()

	oldRefreshURL := accountauth.CurrentOpenAIRefreshTokenURLForTest()
	t.Cleanup(func() {
		accountauth.SetOpenAIRefreshTokenURLForTest(oldRefreshURL)
	})
	accountauth.SetOpenAIRefreshTokenURLForTest(authServer.URL)

	body := []byte(`{"model":"gpt-4.1","input":"oauth compressed"}`)
	var receivedBody []byte
	var receivedContentEncoding string
	var receivedContentLength int64
	var receivedPath string
	var receivedBeta string
	var receivedOriginator string
	var receivedAccountID string

	service := &mockAccountPoolService{accounts: []*store.UpstreamAccountRecord{
		{ID: 85, AccountName: "oauth-zstd", ProviderType: "chatgpt_refresh_token", CredentialRaw: `{"refresh_token":"rt-1","chatgpt_account_id":"acc-zstd"}`, Enabled: true},
	}}
	handler := newAccountPipelineTestHandler(t, "https://example.com", service)
	handler.accountHTTPInitOnce.Do(func() {})
	handler.accountHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		receivedBody, _ = io.ReadAll(req.Body)
		receivedContentEncoding = req.Header.Get("Content-Encoding")
		receivedContentLength = req.ContentLength
		receivedPath = req.URL.Path
		receivedBeta = req.Header.Get("OpenAI-Beta")
		receivedOriginator = req.Header.Get("originator")
		receivedAccountID = req.Header.Get("chatgpt-account-id")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"resp_oauth_zstd","status":"completed","output":[]}`)),
			Request:    req,
		}, nil
	})}
	handler.accountSSEHTTPClient = handler.accountHTTPClient

	rec := performZstdAccountRequest(t, handler, "/v1/responses", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(receivedBody, body) {
		t.Fatalf("OAuth upstream body mismatch\nwant: %s\n got: %s", body, receivedBody)
	}
	if receivedContentEncoding != "" {
		t.Fatalf("OAuth upstream Content-Encoding must be empty, got %q", receivedContentEncoding)
	}
	if receivedContentLength != int64(len(body)) {
		t.Fatalf("OAuth upstream ContentLength = %d, want %d", receivedContentLength, len(body))
	}
	if receivedPath != "/backend-api/codex/responses" {
		t.Fatalf("unexpected OAuth upstream path: %s", receivedPath)
	}
	if receivedBeta != openAIBetaResponsesValue || receivedOriginator != defaultOAuthOriginator || receivedAccountID != "acc-zstd" {
		t.Fatalf("unexpected OAuth headers: beta=%q originator=%q account=%q", receivedBeta, receivedOriginator, receivedAccountID)
	}
}

func TestAccountPipeline_429SoftFailure_RespectsServiceNoFailoverDecision(t *testing.T) {
	firstHits := 0
	firstServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstHits++
		http.Error(w, `{"error":"rate limited"}`, http.StatusTooManyRequests)
	}))
	defer firstServer.Close()

	secondHits := 0
	secondServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_2","status":"completed","output":[]}`))
	}))
	defer secondServer.Close()

	shouldFailover := false
	service := &mockAccountPoolService{
		accounts: []*store.UpstreamAccountRecord{
			{ID: 11, AccountName: "acc-a", ProviderType: "api_key", CredentialRaw: "sk-a", BaseURL: firstServer.URL, Enabled: true},
			{ID: 12, AccountName: "acc-b", ProviderType: "api_key", CredentialRaw: "sk-b", BaseURL: secondServer.URL, Enabled: true},
		},
		softFailureShouldFailover: &shouldFailover,
	}
	handler := newAccountPipelineTestHandler(t, firstServer.URL, service)

	rec := performResponsesRequest(t, handler)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected first 429 soft failure to stop on active account, got %d body=%s", rec.Code, rec.Body.String())
	}
	if firstHits != 1 || secondHits != 0 {
		t.Fatalf("expected backup account not to be attempted when service keeps active account, got first=%d second=%d", firstHits, secondHits)
	}

	if len(service.softFailureCalls) != 1 || service.softFailureCalls[0].id != 11 {
		t.Fatalf("expected soft failure on account 11, got %+v", service.softFailureCalls)
	}
	if service.softFailureCalls[0].category != accountSoftFailureCategoryRateLimit {
		t.Fatalf("expected rate_limit soft failure, got %+v", service.softFailureCalls[0])
	}
	if len(service.transientCalls) != 0 {
		t.Fatalf("expected no direct transient cooldown for first 429, got %+v", service.transientCalls)
	}
	if len(service.successCalls) != 0 {
		t.Fatalf("expected no backup success while service keeps active account, got %+v", service.successCalls)
	}
	if len(service.authFailCalls) != 0 {
		t.Fatalf("expected no auth-failed call, got %+v", service.authFailCalls)
	}
}

func TestAccountPipeline_UsesServiceReturnedOrderWithoutPriorityReordering(t *testing.T) {
	firstHits := 0
	firstServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstHits++
		http.Error(w, `{"error":"rate limited"}`, http.StatusTooManyRequests)
	}))
	defer firstServer.Close()

	secondHits := 0
	secondServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_service_order","status":"completed","output":[]}`))
	}))
	defer secondServer.Close()

	shouldFailover := false
	service := &mockAccountPoolService{
		accounts: []*store.UpstreamAccountRecord{
			{ID: 41, AccountName: "manual-backup", ProviderType: "api_key", CredentialRaw: "sk-backup", BaseURL: firstServer.URL, Priority: 20, Enabled: true},
			{ID: 42, AccountName: "manual-main", ProviderType: "api_key", CredentialRaw: "sk-main", BaseURL: secondServer.URL, Priority: 10, Enabled: true},
		},
		softFailureShouldFailover: &shouldFailover,
	}
	handler := newAccountPipelineTestHandler(t, firstServer.URL, service)

	rec := performResponsesRequest(t, handler)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected first returned account to remain active when service rejects failover, got %d body=%s", rec.Code, rec.Body.String())
	}
	if firstHits != 1 || secondHits != 0 {
		t.Fatalf("expected proxy to honor returned order without trying second account, got first=%d second=%d", firstHits, secondHits)
	}
	if len(service.softFailureCalls) != 1 || service.softFailureCalls[0].id != 41 {
		t.Fatalf("expected first returned account to record soft failure, got %+v", service.softFailureCalls)
	}
	if len(service.transientCalls) != 0 {
		t.Fatalf("expected no immediate cooldown for first returned account, got %+v", service.transientCalls)
	}
	if len(service.successCalls) != 0 {
		t.Fatalf("expected second returned account not to be attempted, got %+v", service.successCalls)
	}
}

func TestAccountPipeline_UsageLimitReachedUsesResetTimeCooldown(t *testing.T) {
	resetAt := time.Now().Add(90 * time.Minute).Round(time.Second)

	firstHits := 0
	firstServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstHits++
		w.Header().Set("Content-Type", "application/json")
		http.Error(w, fmt.Sprintf(`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"free","resets_at":%d,"resets_in_seconds":5400}}`, resetAt.Unix()), http.StatusTooManyRequests)
	}))
	defer firstServer.Close()

	secondHits := 0
	secondServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_after_usage_limit","status":"completed","output":[]}`))
	}))
	defer secondServer.Close()

	service := &mockAccountPoolService{
		accounts: []*store.UpstreamAccountRecord{
			{ID: 61, AccountName: "acc-usage-limit", ProviderType: "api_key", CredentialRaw: "sk-usage-limit", BaseURL: firstServer.URL, Enabled: true},
			{ID: 62, AccountName: "acc-fallback", ProviderType: "api_key", CredentialRaw: "sk-fallback", BaseURL: secondServer.URL, Enabled: true},
		},
	}
	handler := newAccountPipelineTestHandler(t, firstServer.URL, service)

	rec := performResponsesRequest(t, handler)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if firstHits != 1 || secondHits != 1 {
		t.Fatalf("expected both accounts attempted once, got first=%d second=%d", firstHits, secondHits)
	}
	if len(service.usageLimitCalls) != 1 || service.usageLimitCalls[0].id != 61 {
		t.Fatalf("expected usage limit call on account 61, got %+v", service.usageLimitCalls)
	}
	if service.usageLimitCalls[0].planType != "free" {
		t.Fatalf("expected free plan type, got %+v", service.usageLimitCalls[0])
	}
	if got := service.usageLimitCalls[0].resetAt.Unix(); got != resetAt.Unix() {
		t.Fatalf("expected resetAt=%d, got %d", resetAt.Unix(), got)
	}
	if len(service.softFailureCalls) != 0 {
		t.Fatalf("expected no soft failure calls, got %+v", service.softFailureCalls)
	}
	if len(service.transientCalls) != 0 {
		t.Fatalf("expected no generic transient failure calls, got %+v", service.transientCalls)
	}
	if len(service.successCalls) != 1 || service.successCalls[0] != 62 {
		t.Fatalf("expected fallback account success, got %+v", service.successCalls)
	}
}

func TestAccountPipeline_Local503NoAvailableProviders_PassthroughWithoutCooldown(t *testing.T) {
	firstHits := 0
	firstServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstHits++
		w.Header().Set("Content-Type", "application/json")
		http.Error(w, `{"error":{"type":"no_available_providers","message":"no_available_providers::ccf_local"}}`, http.StatusServiceUnavailable)
	}))
	defer firstServer.Close()

	secondHits := 0
	secondServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"should_not_run"}`))
	}))
	defer secondServer.Close()

	service := &mockAccountPoolService{
		accounts: []*store.UpstreamAccountRecord{
			{ID: 13, AccountName: "acc-a", ProviderType: "api_key", CredentialRaw: "sk-a", BaseURL: firstServer.URL, Enabled: true},
			{ID: 14, AccountName: "acc-b", ProviderType: "api_key", CredentialRaw: "sk-b", BaseURL: secondServer.URL, Enabled: true},
		},
	}
	handler := newAccountPipelineTestHandler(t, firstServer.URL, service)

	rec := performResponsesRequest(t, handler)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d body=%s", rec.Code, rec.Body.String())
	}
	if firstHits != 1 || secondHits != 0 {
		t.Fatalf("expected only first account attempted, got first=%d second=%d", firstHits, secondHits)
	}
	if !strings.Contains(rec.Body.String(), "no_available_providers") {
		t.Fatalf("expected passthrough body to contain no_available_providers, got %s", rec.Body.String())
	}
	if len(service.softFailureCalls) != 0 {
		t.Fatalf("expected no soft failure calls, got %+v", service.softFailureCalls)
	}
	if len(service.transientCalls) != 0 {
		t.Fatalf("expected no transient failure calls, got %+v", service.transientCalls)
	}
	if len(service.authFailCalls) != 0 {
		t.Fatalf("expected no auth-failed calls, got %+v", service.authFailCalls)
	}
	if len(service.successCalls) != 0 {
		t.Fatalf("expected no success calls, got %+v", service.successCalls)
	}
}

func TestAccountPipeline_Generic503RecordedAsServerSoftFailure(t *testing.T) {
	firstHits := 0
	firstServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstHits++
		w.Header().Set("Content-Type", "application/json")
		http.Error(w, `{"error":{"type":"service_unavailable","message":"upstream overloaded"}}`, http.StatusServiceUnavailable)
	}))
	defer firstServer.Close()

	secondHits := 0
	secondServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_after_503","status":"completed","output":[]}`))
	}))
	defer secondServer.Close()

	shouldFailover := false
	service := &mockAccountPoolService{
		accounts: []*store.UpstreamAccountRecord{
			{ID: 51, AccountName: "acc-a", ProviderType: "api_key", CredentialRaw: "sk-a", BaseURL: firstServer.URL, Enabled: true},
			{ID: 52, AccountName: "acc-b", ProviderType: "api_key", CredentialRaw: "sk-b", BaseURL: secondServer.URL, Enabled: true},
		},
		softFailureShouldFailover: &shouldFailover,
	}
	handler := newAccountPipelineTestHandler(t, firstServer.URL, service)

	rec := performResponsesRequest(t, handler)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected generic 503 soft failure to stay on active account when service rejects failover, got %d body=%s", rec.Code, rec.Body.String())
	}
	if firstHits != 1 || secondHits != 0 {
		t.Fatalf("expected no backup attempt on first generic 503 soft failure, got first=%d second=%d", firstHits, secondHits)
	}
	if len(service.softFailureCalls) != 1 || service.softFailureCalls[0].id != 51 {
		t.Fatalf("expected soft failure for first account, got %+v", service.softFailureCalls)
	}
	if service.softFailureCalls[0].category != accountSoftFailureCategoryServerError {
		t.Fatalf("expected server_error soft failure, got %+v", service.softFailureCalls[0])
	}
	if len(service.transientCalls) != 0 {
		t.Fatalf("expected no immediate transient cooldown, got %+v", service.transientCalls)
	}
	if len(service.successCalls) != 0 {
		t.Fatalf("expected second account not to be attempted on first generic 503, got %+v", service.successCalls)
	}
}

// newRefusedConnectionURL 返回一个已释放端口的 URL，对其发起连接会得到
// connection refused（WroteHeaders 前失败，重放安全类）。
func newRefusedConnectionURL(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for refused url failed: %v", err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	return "http://" + addr
}

func TestAccountPipeline_ConnectionFailureBeforeWroteHeaders_FailsOverImmediatelyWhenNotPinned(t *testing.T) {
	secondHits := 0
	secondServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_replay_safe_failover","status":"completed","output":[]}`))
	}))
	defer secondServer.Close()

	noFailover := false
	service := &mockAccountPoolService{
		accounts: []*store.UpstreamAccountRecord{
			{ID: 1, ProviderType: "api_key", AccountName: "refused-main", CredentialRaw: "sk-main", BaseURL: newRefusedConnectionURL(t), Enabled: true, State: "active"},
			{ID: 2, ProviderType: "api_key", AccountName: "same-tier-backup", CredentialRaw: "sk-backup", BaseURL: secondServer.URL, Enabled: true, State: "active"},
		},
		softFailureShouldFailover: &noFailover,
	}
	handler := newAccountPipelineTestHandler(t, secondServer.URL, service)

	rec := performResponsesRequest(t, handler)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected replay-safe connection failure to fail over immediately despite below-threshold decision, got %d body=%s", rec.Code, rec.Body.String())
	}
	if secondHits != 1 {
		t.Fatalf("expected backup account to serve the request, got hits=%d", secondHits)
	}

	service.mu.Lock()
	softFailureCalls := append([]accountSoftFailureCall(nil), service.softFailureCalls...)
	service.mu.Unlock()
	if len(softFailureCalls) != 1 || softFailureCalls[0].id != 1 {
		t.Fatalf("expected exactly one soft failure recorded for refused account, got %+v", softFailureCalls)
	}
}

func TestAccountPipeline_ConnectionFailureBeforeWroteHeaders_PinnedAccountDoesNotFailOver(t *testing.T) {
	secondHits := 0
	secondServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_should_not_serve","status":"completed","output":[]}`))
	}))
	defer secondServer.Close()

	noFailover := false
	service := &mockAccountPoolService{
		accounts: []*store.UpstreamAccountRecord{
			{ID: 1, ProviderType: "api_key", AccountName: "pinned-refused", CredentialRaw: "sk-main", BaseURL: newRefusedConnectionURL(t), Enabled: true, State: "active"},
			{ID: 2, ProviderType: "api_key", AccountName: "same-tier-backup", CredentialRaw: "sk-backup", BaseURL: secondServer.URL, Enabled: true, State: "active"},
		},
		pinned:                    true,
		softFailureShouldFailover: &noFailover,
	}
	handler := newAccountPipelineTestHandler(t, secondServer.URL, service)

	rec := performResponsesRequest(t, handler)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected pinned account to pass through below-threshold connection failure without failover, got %d body=%s", rec.Code, rec.Body.String())
	}
	if secondHits != 0 {
		t.Fatalf("expected backup account not attempted while pinned, got hits=%d", secondHits)
	}

	service.mu.Lock()
	softFailureCalls := append([]accountSoftFailureCall(nil), service.softFailureCalls...)
	service.mu.Unlock()
	if len(softFailureCalls) != 1 || softFailureCalls[0].id != 1 {
		t.Fatalf("expected soft failure still recorded for pinned account, got %+v", softFailureCalls)
	}
}

func TestAccountPipeline_ExhaustedPreservesLastAttemptedUpstream(t *testing.T) {
	noFailover := false
	service := &mockAccountPoolService{
		accounts: []*store.UpstreamAccountRecord{
			{ID: 55, ProviderType: "api_key", AccountName: "fastaitoken", CredentialRaw: "sk-fast", BaseURL: newRefusedConnectionURL(t), Enabled: true, State: "active"},
		},
		pinned:                    true,
		softFailureShouldFailover: &noFailover,
	}
	handler := newAccountPipelineTestHandler(t, "https://example.com", service)
	body := []byte(`{"model":"gpt-5.6-sol","input":"hello"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	lifecycleManager := NewRequestLifecycleManager(nil, nil, "req-upstream-attribution", nil)

	handler.handleAccountPipeline(context.Background(), rec, req, body, lifecycleManager)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected exhausted account pool status 503, got %d body=%s", rec.Code, rec.Body.String())
	}
	state := lifecycleManager.snapshotState()
	if state.upstreamType != "account" || state.upstreamSourceName != "account-pool" {
		t.Fatalf("unexpected upstream classification: type=%q source=%q", state.upstreamType, state.upstreamSourceName)
	}
	if state.upstreamName != "fastaitoken" || state.upstreamID != 55 {
		t.Fatalf("expected real attempted upstream to be preserved, got name=%q id=%d", state.upstreamName, state.upstreamID)
	}
	if state.endpointName != "fastaitoken" {
		t.Fatalf("expected endpoint compatibility field to preserve account name, got %q", state.endpointName)
	}
}

func TestAccountPipeline_FailureAfterWroteHeaders_KeepsThresholdSemantics(t *testing.T) {
	var firstHits int32
	firstServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&firstHits, 1)
		_, _ = io.ReadAll(r.Body)
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Errorf("response writer does not support hijack")
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack failed: %v", err)
			return
		}
		_ = conn.Close()
	}))
	defer firstServer.Close()

	var secondHits int32
	secondServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&secondHits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_should_not_serve","status":"completed","output":[]}`))
	}))
	defer secondServer.Close()

	noFailover := false
	service := &mockAccountPoolService{
		accounts: []*store.UpstreamAccountRecord{
			{ID: 1, ProviderType: "api_key", AccountName: "drops-after-request", CredentialRaw: "sk-main", BaseURL: firstServer.URL, Enabled: true, State: "active"},
			{ID: 2, ProviderType: "api_key", AccountName: "same-tier-backup", CredentialRaw: "sk-backup", BaseURL: secondServer.URL, Enabled: true, State: "active"},
		},
		softFailureShouldFailover: &noFailover,
	}
	handler := newAccountPipelineTestHandler(t, secondServer.URL, service)

	rec := performResponsesRequest(t, handler)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected ambiguous failure after WroteHeaders to keep below-threshold passthrough, got %d body=%s", rec.Code, rec.Body.String())
	}
	firstHitCount := atomic.LoadInt32(&firstHits)
	secondHitCount := atomic.LoadInt32(&secondHits)
	if firstHitCount != 1 || secondHitCount != 0 {
		t.Fatalf("expected no failover for post-WroteHeaders failure below threshold, got first=%d second=%d", firstHitCount, secondHitCount)
	}

	service.mu.Lock()
	softFailureCalls := append([]accountSoftFailureCall(nil), service.softFailureCalls...)
	service.mu.Unlock()
	if len(softFailureCalls) != 1 || softFailureCalls[0].id != 1 {
		t.Fatalf("expected soft failure recorded for dropped connection, got %+v", softFailureCalls)
	}
}

func TestAccountPipeline_ServerSoftFailure_DoesNotFailOverBeforeThirdConsecutiveFailure(t *testing.T) {
	firstHits := 0
	firstServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstHits++
		w.Header().Set("Content-Type", "application/json")
		http.Error(w, `{"error":{"type":"service_unavailable","message":"upstream overloaded"}}`, http.StatusServiceUnavailable)
	}))
	defer firstServer.Close()

	secondHits := 0
	secondServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_after_threshold","status":"completed","output":[]}`))
	}))
	defer secondServer.Close()

	service := newRealAccountPipelineTestService(t, []*store.UpstreamAccountRecord{
		{ProviderType: "api_key", AccountName: "active-main", CredentialRaw: "sk-main", BaseURL: firstServer.URL, Priority: 10, Enabled: true, State: "active"},
		{ProviderType: "api_key", AccountName: "same-tier-backup", CredentialRaw: "sk-backup", BaseURL: secondServer.URL, Priority: 10, Enabled: true, State: "active"},
	})
	handler := newAccountPipelineTestHandler(t, firstServer.URL, service)

	firstAttempt := performResponsesRequest(t, handler)
	if firstAttempt.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected first soft failure to stop without failover, got %d body=%s", firstAttempt.Code, firstAttempt.Body.String())
	}
	if firstHits != 1 || secondHits != 0 {
		t.Fatalf("expected only active account attempted on first soft failure, got first=%d second=%d", firstHits, secondHits)
	}

	secondAttempt := performResponsesRequest(t, handler)
	if secondAttempt.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected second consecutive soft failure to stop without failover, got %d body=%s", secondAttempt.Code, secondAttempt.Body.String())
	}
	if firstHits != 2 || secondHits != 0 {
		t.Fatalf("expected backup account not attempted before threshold, got first=%d second=%d", firstHits, secondHits)
	}

	thirdAttempt := performResponsesRequest(t, handler)
	if thirdAttempt.Code != http.StatusOK {
		t.Fatalf("expected third consecutive soft failure to fail over, got %d body=%s", thirdAttempt.Code, thirdAttempt.Body.String())
	}
	if firstHits != 3 || secondHits != 1 {
		t.Fatalf("expected backup to be attempted only after threshold, got first=%d second=%d", firstHits, secondHits)
	}
}

func TestAccountPipeline_SuccessResetsTransientFailureThreshold(t *testing.T) {
	var firstHits int
	firstServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstHits++
		w.Header().Set("Content-Type", "application/json")
		switch firstHits {
		case 2:
			_, _ = w.Write([]byte(`{"id":"resp_recovered","status":"completed","output":[]}`))
		default:
			http.Error(w, `{"error":{"type":"service_unavailable","message":"upstream overloaded"}}`, http.StatusServiceUnavailable)
		}
	}))
	defer firstServer.Close()

	secondHits := 0
	secondServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_after_reset","status":"completed","output":[]}`))
	}))
	defer secondServer.Close()

	service := newRealAccountPipelineTestService(t, []*store.UpstreamAccountRecord{
		{ProviderType: "api_key", AccountName: "active-main", CredentialRaw: "sk-main", BaseURL: firstServer.URL, Priority: 10, Enabled: true, State: "active"},
		{ProviderType: "api_key", AccountName: "same-tier-backup", CredentialRaw: "sk-backup", BaseURL: secondServer.URL, Priority: 10, Enabled: true, State: "active"},
	})
	handler := newAccountPipelineTestHandler(t, firstServer.URL, service)

	firstAttempt := performResponsesRequest(t, handler)
	if firstAttempt.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected initial transient failure to stay on active account, got %d body=%s", firstAttempt.Code, firstAttempt.Body.String())
	}
	if firstHits != 1 || secondHits != 0 {
		t.Fatalf("expected no failover on first transient failure, got first=%d second=%d", firstHits, secondHits)
	}

	secondAttempt := performResponsesRequest(t, handler)
	if secondAttempt.Code != http.StatusOK {
		t.Fatalf("expected active account recovery success, got %d body=%s", secondAttempt.Code, secondAttempt.Body.String())
	}
	if firstHits != 2 || secondHits != 0 {
		t.Fatalf("expected recovered active account to serve success itself, got first=%d second=%d", firstHits, secondHits)
	}

	thirdAttempt := performResponsesRequest(t, handler)
	if thirdAttempt.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected post-success first failure to stay on active account, got %d body=%s", thirdAttempt.Code, thirdAttempt.Body.String())
	}
	if firstHits != 3 || secondHits != 0 {
		t.Fatalf("expected failover counter reset after success, got first=%d second=%d", firstHits, secondHits)
	}

	fourthAttempt := performResponsesRequest(t, handler)
	if fourthAttempt.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected post-success second failure to stay on active account, got %d body=%s", fourthAttempt.Code, fourthAttempt.Body.String())
	}
	if firstHits != 4 || secondHits != 0 {
		t.Fatalf("expected no failover before the new third consecutive failure, got first=%d second=%d", firstHits, secondHits)
	}
}

func TestAccountPipeline_AuthFailedSwitchToNextAccount(t *testing.T) {
	firstHits := 0
	firstServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstHits++
		http.Error(w, "invalid session", http.StatusUnauthorized)
	}))
	defer firstServer.Close()

	secondHits := 0
	secondServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_ok","status":"completed","output":[]}`))
	}))
	defer secondServer.Close()

	service := &mockAccountPoolService{
		accounts: []*store.UpstreamAccountRecord{
			{ID: 21, AccountName: "acc-a", ProviderType: "api_key", CredentialRaw: "sk-a", BaseURL: firstServer.URL, Enabled: true},
			{ID: 22, AccountName: "acc-b", ProviderType: "api_key", CredentialRaw: "sk-b", BaseURL: secondServer.URL, Enabled: true},
		},
	}
	handler := newAccountPipelineTestHandler(t, firstServer.URL, service)

	rec := performResponsesRequest(t, handler)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if firstHits != 1 || secondHits != 1 {
		t.Fatalf("expected both accounts to be attempted once, got first=%d second=%d", firstHits, secondHits)
	}

	if len(service.authFailCalls) != 1 || service.authFailCalls[0].id != 21 {
		t.Fatalf("expected auth-failed for account 21, got %+v", service.authFailCalls)
	}
	if !strings.Contains(service.authFailCalls[0].reason, "invalid session") {
		t.Fatalf("expected auth-failed reason to contain upstream detail, got %q", service.authFailCalls[0].reason)
	}
	if len(service.successCalls) != 1 || service.successCalls[0] != 22 {
		t.Fatalf("expected success on account 22, got %+v", service.successCalls)
	}
	if len(service.transientCalls) != 0 {
		t.Fatalf("expected no transient failure calls, got %+v", service.transientCalls)
	}
}

func TestAccountPipeline_Upstream4xxPassthroughWithoutSwitch(t *testing.T) {
	firstHits := 0
	firstServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstHits++
		http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
	}))
	defer firstServer.Close()

	secondHits := 0
	secondServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"should_not_run"}`))
	}))
	defer secondServer.Close()

	service := &mockAccountPoolService{
		accounts: []*store.UpstreamAccountRecord{
			{ID: 31, AccountName: "acc-a", ProviderType: "api_key", CredentialRaw: "sk-a", BaseURL: firstServer.URL, Enabled: true},
			{ID: 32, AccountName: "acc-b", ProviderType: "api_key", CredentialRaw: "sk-b", BaseURL: secondServer.URL, Enabled: true},
		},
	}
	handler := newAccountPipelineTestHandler(t, firstServer.URL, service)

	rec := performResponsesRequest(t, handler)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if firstHits != 1 {
		t.Fatalf("expected first account called once, got %d", firstHits)
	}
	if secondHits != 0 {
		t.Fatalf("expected second account not to be called, got %d", secondHits)
	}
	if !strings.Contains(rec.Body.String(), "bad request") {
		t.Fatalf("expected passthrough body to contain upstream error, got %s", rec.Body.String())
	}
	if len(service.successCalls) != 0 || len(service.authFailCalls) != 0 || len(service.transientCalls) != 0 {
		t.Fatalf("expected no account state callbacks for passthrough 4xx, got success=%+v auth=%+v transient=%+v",
			service.successCalls, service.authFailCalls, service.transientCalls)
	}
}

func TestAccountPipeline_ResponsesStreamingCompleted_TreatedAsSuccess(t *testing.T) {
	upstreamHits := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("event: response.in_progress\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.in_progress","response":{"id":"resp_1","model":"gpt-5-codex"}}` + "\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-5-codex"},"usage":{"input_tokens":12,"output_tokens":3,"input_tokens_details":{"cached_tokens":0}}}` + "\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	service := &mockAccountPoolService{
		accounts: []*store.UpstreamAccountRecord{
			{ID: 41, AccountName: "acc-stream", ProviderType: "api_key", CredentialRaw: "sk-a", BaseURL: upstream.URL, Enabled: true},
		},
	}
	handler := newAccountPipelineTestHandler(t, upstream.URL, service)

	rec := performResponsesStreamingRequest(t, handler)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if upstreamHits != 1 {
		t.Fatalf("expected upstream called once, got %d", upstreamHits)
	}
	if len(service.successCalls) != 1 || service.successCalls[0] != 41 {
		t.Fatalf("expected success on account 41, got %+v", service.successCalls)
	}
	if len(service.transientCalls) != 0 {
		t.Fatalf("expected no transient failures, got %+v", service.transientCalls)
	}
	if len(service.authFailCalls) != 0 {
		t.Fatalf("expected no auth failures, got %+v", service.authFailCalls)
	}
}

func TestAccountPipeline_ResponsesStreamingFailed_RecordedAsServerSoftFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("event: response.created\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.4"}}` + "\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = w.Write([]byte("event: response.failed\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.failed","response":{"id":"resp_1","model":"gpt-5.4","error":{"type":"unknown_error","message":"Unknown error"}}}` + "\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	service := &mockAccountPoolService{
		accounts: []*store.UpstreamAccountRecord{
			{ID: 71, AccountName: "acc-stream-failed", ProviderType: "api_key", CredentialRaw: "sk-a", BaseURL: upstream.URL, Enabled: true},
		},
	}
	handler := newAccountPipelineTestHandler(t, upstream.URL, service)

	rec := performResponsesStreamingRequest(t, handler)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected streaming response to keep status 200 once headers are sent, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(service.softFailureCalls) != 1 {
		t.Fatalf("expected one soft failure call, got %+v", service.softFailureCalls)
	}
	if service.softFailureCalls[0].id != 71 || service.softFailureCalls[0].category != accountSoftFailureCategoryServerError {
		t.Fatalf("expected server_error soft failure for account 71, got %+v", service.softFailureCalls[0])
	}
	if len(service.transientCalls) != 0 {
		t.Fatalf("expected no immediate transient cooldown for stream error, got %+v", service.transientCalls)
	}
	if len(service.successCalls) != 0 {
		t.Fatalf("expected no success callbacks, got %+v", service.successCalls)
	}
}

func TestAccountPipeline_ResponsesStreamingUsageLimitReached_UsesResetTimeCooldown(t *testing.T) {
	resetAt := time.Now().Add(90 * time.Minute).Round(time.Second)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("event: response.created\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.created","response":{"id":"resp_usage_limit","model":"gpt-5.4"}}` + "\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = w.Write([]byte("event: response.failed\n"))
		_, _ = w.Write([]byte(fmt.Sprintf(`data: {"type":"response.failed","response":{"id":"resp_usage_limit","model":"gpt-5.4","error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"free","resets_at":%d,"resets_in_seconds":5400}}}`+"\n\n", resetAt.Unix())))
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	service := &mockAccountPoolService{
		accounts: []*store.UpstreamAccountRecord{
			{ID: 81, AccountName: "acc-stream-usage-limit", ProviderType: "api_key", CredentialRaw: "sk-a", BaseURL: upstream.URL, Enabled: true},
		},
	}
	handler := newAccountPipelineTestHandler(t, upstream.URL, service)

	rec := performResponsesStreamingRequest(t, handler)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected streaming response to keep status 200 once headers are sent, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(service.usageLimitCalls) != 1 {
		t.Fatalf("expected one usage-limit cooldown call, got %+v", service.usageLimitCalls)
	}
	if service.usageLimitCalls[0].id != 81 || service.usageLimitCalls[0].planType != "free" {
		t.Fatalf("unexpected usage-limit call: %+v", service.usageLimitCalls[0])
	}
	if service.usageLimitCalls[0].resetAt.Unix() != resetAt.Unix() {
		t.Fatalf("expected resetAt=%d, got %d", resetAt.Unix(), service.usageLimitCalls[0].resetAt.Unix())
	}
	if len(service.softFailureCalls) != 0 {
		t.Fatalf("expected no soft failure fallback for usage-limit stream, got %+v", service.softFailureCalls)
	}
	if len(service.transientCalls) != 0 {
		t.Fatalf("expected no generic transient cooldown, got %+v", service.transientCalls)
	}
}

func TestResolveAccountTargetURL_OAuthUsesChatGPTCodexEndpoint(t *testing.T) {
	acc := &store.UpstreamAccountRecord{
		ProviderType: "chatgpt_refresh_token",
		BaseURL:      "https://api.openai.com",
	}

	targetURL, err := resolveAccountTargetURL(acc, "/v1/responses", "stream=true")
	if err != nil {
		t.Fatalf("resolveAccountTargetURL returned error: %v", err)
	}

	parsed, err := url.Parse(targetURL)
	if err != nil {
		t.Fatalf("parse target url failed: %v", err)
	}
	if parsed.Scheme != "https" || parsed.Host != "chatgpt.com" {
		t.Fatalf("expected chatgpt host, got %s://%s", parsed.Scheme, parsed.Host)
	}
	if parsed.Path != "/backend-api/codex/responses" {
		t.Fatalf("unexpected path: %s", parsed.Path)
	}
	if parsed.RawQuery != "stream=true" {
		t.Fatalf("unexpected query: %s", parsed.RawQuery)
	}
}

func TestResolveAccountTargetURL_OAuthUsesChatGPTCodexCompactEndpoint(t *testing.T) {
	acc := &store.UpstreamAccountRecord{
		ProviderType: "chatgpt_refresh_token",
		BaseURL:      "https://api.openai.com",
	}

	targetURL, err := resolveAccountTargetURL(acc, "/v1/responses/compact", "stream=true")
	if err != nil {
		t.Fatalf("resolveAccountTargetURL returned error: %v", err)
	}

	parsed, err := url.Parse(targetURL)
	if err != nil {
		t.Fatalf("parse target url failed: %v", err)
	}
	if parsed.Scheme != "https" || parsed.Host != "chatgpt.com" {
		t.Fatalf("expected chatgpt host, got %s://%s", parsed.Scheme, parsed.Host)
	}
	if parsed.Path != "/backend-api/codex/responses/compact" {
		t.Fatalf("unexpected path: %s", parsed.Path)
	}
	if parsed.RawQuery != "stream=true" {
		t.Fatalf("unexpected query: %s", parsed.RawQuery)
	}
}

func TestResolveAccountTargetURL_OAuthUsesChatGPTCodexModelsEndpoint(t *testing.T) {
	acc := &store.UpstreamAccountRecord{
		ProviderType: "chatgpt_refresh_token",
		BaseURL:      "https://api.openai.com",
	}

	targetURL, err := resolveAccountTargetURL(acc, "/v1/models", "client_version=0.131.0")
	if err != nil {
		t.Fatalf("resolveAccountTargetURL returned error: %v", err)
	}

	parsed, err := url.Parse(targetURL)
	if err != nil {
		t.Fatalf("parse target url failed: %v", err)
	}
	if parsed.Scheme != "https" || parsed.Host != "chatgpt.com" {
		t.Fatalf("expected chatgpt host, got %s://%s", parsed.Scheme, parsed.Host)
	}
	if parsed.Path != "/backend-api/codex/models" {
		t.Fatalf("unexpected path: %s", parsed.Path)
	}
	if parsed.RawQuery != "client_version=0.131.0" {
		t.Fatalf("unexpected query: %s", parsed.RawQuery)
	}
}

func TestResolveAccountTargetURL_OAuthUsesChatGPTCodexSearchEndpoint(t *testing.T) {
	acc := &store.UpstreamAccountRecord{
		ProviderType: "chatgpt_refresh_token",
		BaseURL:      "https://api.openai.com",
	}

	targetURL, err := resolveAccountTargetURL(acc, codexStandaloneSearchPath, "client_version=0.146.0")
	if err != nil {
		t.Fatalf("resolveAccountTargetURL returned error: %v", err)
	}

	parsed, err := url.Parse(targetURL)
	if err != nil {
		t.Fatalf("parse target url failed: %v", err)
	}
	if parsed.Scheme != "https" || parsed.Host != "chatgpt.com" {
		t.Fatalf("expected chatgpt host, got %s://%s", parsed.Scheme, parsed.Host)
	}
	if parsed.Path != "/backend-api/codex/alpha/search" {
		t.Fatalf("unexpected path: %s", parsed.Path)
	}
	if parsed.RawQuery != "client_version=0.146.0" {
		t.Fatalf("unexpected query: %s", parsed.RawQuery)
	}
}

func TestApplyOpenAIChatGPTOAuthHeaders_SetsRequiredHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", strings.NewReader(`{}`))
	req.Header.Set("chatgpt-account-id", "resolved-account")

	applyOpenAIChatGPTOAuthHeaders(req)

	if req.Host != "chatgpt.com" {
		t.Fatalf("expected host=chatgpt.com, got %s", req.Host)
	}
	if req.Header.Get("Accept") != "text/event-stream" {
		t.Fatalf("unexpected Accept header: %s", req.Header.Get("Accept"))
	}
	if req.Header.Get("OpenAI-Beta") != "responses=experimental" {
		t.Fatalf("unexpected OpenAI-Beta header: %s", req.Header.Get("OpenAI-Beta"))
	}
	if req.Header.Get("originator") != "codex_cli_rs" {
		t.Fatalf("unexpected originator header: %s", req.Header.Get("originator"))
	}
	if req.Header.Get("chatgpt-account-id") != "resolved-account" {
		t.Fatalf("unexpected chatgpt-account-id header: %s", req.Header.Get("chatgpt-account-id"))
	}
}

func TestApplyOpenAIChatGPTModelsHeaders_SkipsResponsesOnlyHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "https://chatgpt.com/backend-api/codex/models", nil)
	req.Header.Set("chatgpt-account-id", "resolved-account")

	applyOpenAIChatGPTModelsHeaders(req)

	if req.Host != "chatgpt.com" {
		t.Fatalf("expected host=chatgpt.com, got %s", req.Host)
	}
	if req.Header.Get("chatgpt-account-id") != "resolved-account" {
		t.Fatalf("unexpected chatgpt-account-id header: %s", req.Header.Get("chatgpt-account-id"))
	}
	if req.Header.Get("OpenAI-Beta") != "" {
		t.Fatalf("expected models request not to set OpenAI-Beta, got %s", req.Header.Get("OpenAI-Beta"))
	}
	if req.Header.Get("Accept") == "text/event-stream" {
		t.Fatal("expected models request not to force text/event-stream")
	}
}

func TestApplyOpenAIChatGPTSearchHeaders_SkipsResponsesOnlyHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, chatGPTCodexSearchURL, strings.NewReader(`{}`))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("chatgpt-account-id", "resolved-account")

	applyOpenAIChatGPTSearchHeaders(req)

	if req.Host != "chatgpt.com" {
		t.Fatalf("expected host=chatgpt.com, got %s", req.Host)
	}
	if req.Header.Get("chatgpt-account-id") != "resolved-account" {
		t.Fatalf("unexpected chatgpt-account-id header: %s", req.Header.Get("chatgpt-account-id"))
	}
	if req.Header.Get("OpenAI-Beta") != "" {
		t.Fatalf("expected search request not to set OpenAI-Beta, got %s", req.Header.Get("OpenAI-Beta"))
	}
	if req.Header.Get("originator") != defaultOAuthOriginator {
		t.Fatalf("expected default search originator, got %s", req.Header.Get("originator"))
	}
	if req.Header.Get("Accept") != "application/json" {
		t.Fatalf("expected search request to preserve JSON Accept header, got %s", req.Header.Get("Accept"))
	}
}

func TestPrepareCodexAccountBodyForUpstream_ConfiguredModelRewriteRules(t *testing.T) {
	anyRouterAccount := &store.UpstreamAccountRecord{
		AccountName:       "anyrouter-codex",
		ProviderType:      "api_key",
		CredentialRaw:     "sk-anyrouter",
		BaseURL:           "https://api.anyrouter.example",
		ModelRewriteRules: testGPT54ModelRewriteRules,
	}
	anyRouterMultiRuleAccount := &store.UpstreamAccountRecord{
		AccountName:       "anyrouter-codex-multi",
		ProviderType:      "api_key",
		CredentialRaw:     "sk-anyrouter",
		BaseURL:           "https://api.anyrouter.example",
		ModelRewriteRules: testGPT54AndMiniModelRewriteRules,
	}
	regularAccount := &store.UpstreamAccountRecord{
		AccountName:   "openai",
		ProviderType:  "api_key",
		CredentialRaw: "sk-openai",
		BaseURL:       "https://api.openai.com",
	}
	anyRouterWithoutRules := &store.UpstreamAccountRecord{
		AccountName:   "anyrouter-no-rules",
		ProviderType:  "api_key",
		CredentialRaw: "sk-anyrouter",
		BaseURL:       "https://api.anyrouter.example",
	}

	tests := []struct {
		name      string
		account   *store.UpstreamAccountRecord
		path      string
		body      string
		wantModel string
		wantRaw   string
	}{
		{
			name:      "keeps gpt-5.4-mini when only exact gpt-5.4 rule is configured",
			account:   anyRouterAccount,
			path:      "/v1/responses",
			body:      `{"model":"gpt-5.4-mini","input":"hello"}`,
			wantModel: "gpt-5.4-mini",
		},
		{
			name:      "rewrites exact gpt-5.4-mini when explicitly configured",
			account:   anyRouterMultiRuleAccount,
			path:      "/v1/responses",
			body:      `{"model":"gpt-5.4-mini","input":"hello"}`,
			wantModel: "gpt-5.5",
		},
		{
			name:      "rewrites gpt-5.4 for anyrouter compact",
			account:   anyRouterAccount,
			path:      "/v1/responses/compact",
			body:      `{"model":"gpt-5.4","input":"hello"}`,
			wantModel: "gpt-5.5",
		},
		{
			name:      "keeps supported model for anyrouter",
			account:   anyRouterAccount,
			path:      "/v1/responses",
			body:      `{"model":"gpt-5.5","input":"hello"}`,
			wantModel: "gpt-5.5",
		},
		{
			name:      "keeps gpt-5.4 for non-anyrouter account",
			account:   regularAccount,
			path:      "/v1/responses",
			body:      `{"model":"gpt-5.4-mini","input":"hello"}`,
			wantModel: "gpt-5.4-mini",
		},
		{
			name:      "keeps gpt-5.4 for anyrouter account without configured rule",
			account:   anyRouterWithoutRules,
			path:      "/v1/responses",
			body:      `{"model":"gpt-5.4-mini","input":"hello"}`,
			wantModel: "gpt-5.4-mini",
		},
		{
			name:    "keeps models path untouched",
			account: anyRouterAccount,
			path:    "/v1/models",
			body:    `{"model":"gpt-5.4-mini","input":"hello"}`,
			wantRaw: `{"model":"gpt-5.4-mini","input":"hello"}`,
		},
		{
			name:    "keeps invalid json untouched",
			account: anyRouterAccount,
			path:    "/v1/responses",
			body:    `{"model":`,
			wantRaw: `{"model":`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, upstreamModel := prepareCodexAccountBodyForUpstream([]byte(tt.body), tt.account, tt.path)
			if tt.wantRaw != "" {
				if string(got) != tt.wantRaw || upstreamModel != "" {
					t.Fatalf("expected raw body %q, got %q (upstream=%q)", tt.wantRaw, string(got), upstreamModel)
				}
				return
			}
			var original map[string]any
			_ = json.Unmarshal([]byte(tt.body), &original)
			if original["model"] == tt.wantModel && upstreamModel != "" {
				t.Fatalf("unrewritten body must report empty upstream model, got %q", upstreamModel)
			}
			if original["model"] != tt.wantModel && upstreamModel != tt.wantModel {
				t.Fatalf("rewritten body must report upstream model %q, got %q", tt.wantModel, upstreamModel)
			}

			var payload map[string]any
			if err := json.Unmarshal(got, &payload); err != nil {
				t.Fatalf("unmarshal rewritten body failed: %v body=%s", err, string(got))
			}
			if payload["model"] != tt.wantModel {
				t.Fatalf("expected model %q, got %q", tt.wantModel, payload["model"])
			}
		})
	}
}

func TestParseAccountStreamStatusError_Cancelled(t *testing.T) {
	status, modelName := parseAccountStreamStatusError(context.Canceled)
	if status != "cancelled" {
		t.Fatalf("expected cancelled status for context.Canceled, got %s", status)
	}
	if modelName != "unknown" {
		t.Fatalf("expected unknown model for context.Canceled, got %s", modelName)
	}

	status, modelName = parseAccountStreamStatusError(errors.New("stream_status:cancelled:model:gpt-5.4: context canceled"))
	if status != "cancelled" {
		t.Fatalf("expected cancelled status for structured stream error, got %s", status)
	}
	if modelName != "gpt-5.4" {
		t.Fatalf("expected gpt-5.4 model for structured stream error, got %s", modelName)
	}
	if !isCancelledAccountStreamError(errors.New("stream_status:cancelled:model:gpt-5.4: context canceled")) {
		t.Fatal("expected structured cancelled stream error to be recognized")
	}
}

func TestAccountScheduleOutcomeForCodexStreamError(t *testing.T) {
	if got := accountScheduleOutcomeForCodexStreamError(errors.New("stream_status:auth_error:model:gpt-5.4: unauthorized")); got != servicepkg.AccountScheduleOutcomeAuthFailed {
		t.Fatalf("expected auth stream failure outcome, got %q", got)
	}
	if got := accountScheduleOutcomeForCodexStreamError(errors.New("stream_status:rate_limited:model:gpt-5.4: retry later")); got != servicepkg.AccountScheduleOutcomeTransientFailure {
		t.Fatalf("expected retryable stream failure outcome, got %q", got)
	}
}

func TestReadAndRestoreResponseBody_PreservesFullBody(t *testing.T) {
	fullBody := strings.Repeat("abcdefghij", 300)
	resp := &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Body:       io.NopCloser(strings.NewReader(fullBody)),
	}

	detail := readAndRestoreResponseBody(resp, 32)
	if len(detail) != 32 {
		t.Fatalf("expected truncated detail length 32, got %d", len(detail))
	}

	restoredBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll restored body failed: %v", err)
	}
	if string(restoredBody) != fullBody {
		t.Fatal("expected restored body to keep full upstream payload")
	}
}

func TestAccountPipeline_ResponsesStreamingCompletedAfterClientCancel_TreatedAsSuccess(t *testing.T) {
	completedWritten := make(chan struct{})
	allowClose := make(chan struct{})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("event: response.in_progress\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.in_progress","response":{"id":"resp_1","model":"gpt-5.4"}}` + "\n\n"))
		if flusher != nil {
			flusher.Flush()
		}

		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.4"},"usage":{"input_tokens":21,"output_tokens":4,"input_tokens_details":{"cached_tokens":11}}}` + "\n\n"))
		if flusher != nil {
			flusher.Flush()
		}

		close(completedWritten)
		<-allowClose
	}))
	defer upstream.Close()

	service := &mockAccountPoolService{
		accounts: []*store.UpstreamAccountRecord{
			{ID: 61, AccountName: "acc-stream-cancel", ProviderType: "api_key", CredentialRaw: "sk-a", BaseURL: upstream.URL, Enabled: true},
		},
	}
	handler := newAccountPipelineTestHandler(t, upstream.URL, service)

	body := bytes.NewBufferString(`{"model":"gpt-5.4","stream":true,"input":"hello"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", body)
	req.Header.Set("Content-Type", "application/json")
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(rec, req)
	}()

	<-completedWritten
	cancel()
	close(allowClose)
	<-done

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(service.successCalls) != 1 || service.successCalls[0] != 61 {
		t.Fatalf("expected success on account 61, got %+v", service.successCalls)
	}
	if len(service.successCtxErrs) != 1 || service.successCtxErrs[0] != nil {
		t.Fatalf("expected success finalization with live context, got %+v", service.successCtxErrs)
	}
	if len(service.completeCalls) == 0 || service.completeCalls[len(service.completeCalls)-1].outcome != servicepkg.AccountScheduleOutcomeSuccess {
		t.Fatalf("expected schedule snapshot success, got %+v", service.completeCalls)
	}
	if len(service.completeCtxErrs) == 0 || service.completeCtxErrs[len(service.completeCtxErrs)-1] != nil {
		t.Fatalf("expected schedule snapshot finalization with live context, got %+v", service.completeCtxErrs)
	}
	if len(service.transientCalls) != 0 {
		t.Fatalf("expected no transient failures, got %+v", service.transientCalls)
	}
}

func TestAccountPipeline_ClientCancelAfterFailover_RecordsCancelledOnCurrentAccount(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "busy", http.StatusInternalServerError)
	}))
	defer failing.Close()

	streamStarted := make(chan struct{})
	streaming := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.in_progress\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.in_progress","response":{"id":"resp_1","model":"gpt-5.4"}}` + "\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(streamStarted)
		<-r.Context().Done()
	}))
	defer streaming.Close()

	service := &mockAccountPoolService{
		accounts: []*store.UpstreamAccountRecord{
			{ID: 71, AccountName: "acc-failing", ProviderType: "api_key", CredentialRaw: "sk-a", BaseURL: failing.URL, Enabled: true},
			{ID: 72, AccountName: "acc-streaming", ProviderType: "api_key", CredentialRaw: "sk-b", BaseURL: streaming.URL, Enabled: true},
		},
	}
	handler := newAccountPipelineTestHandler(t, failing.URL, service)

	body := bytes.NewBufferString(`{"model":"gpt-5.4","stream":true,"input":"hello"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", body)
	req.Header.Set("Content-Type", "application/json")
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(rec, req)
	}()

	<-streamStarted
	cancel()
	<-done

	service.mu.Lock()
	defer service.mu.Unlock()
	if len(service.completeCalls) != 2 {
		t.Fatalf("expected failed attempt and cancelled attempt in snapshot, got %+v", service.completeCalls)
	}
	first, last := service.completeCalls[0], service.completeCalls[1]
	if first.accountID != 71 || first.outcome != servicepkg.AccountScheduleOutcomeTransientFailure {
		t.Fatalf("expected first account recorded as transient failure, got %+v", first)
	}
	if last.accountID != 72 || last.outcome != servicepkg.AccountScheduleOutcomeCancelled || last.finalError != accountScheduleCancelledByClient {
		t.Fatalf("expected current account recorded as cancelled, got %+v", last)
	}
	for _, call := range service.softFailureCalls {
		if call.id == 72 {
			t.Fatalf("client cancel must not count as soft failure, got %+v", service.softFailureCalls)
		}
	}
	if len(service.transientCalls) != 0 {
		t.Fatalf("client cancel must not cool down account, got %+v", service.transientCalls)
	}
}

func TestAccountPipeline_SoftFailurePolicy_独立阈值触发换号(t *testing.T) {
	var primaryHits, backupHits int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&primaryHits, 1)
		http.Error(w, "busy", http.StatusServiceUnavailable)
	}))
	t.Cleanup(primary.Close)
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&backupHits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"policy-response","status":"completed","output":[]}`))
	}))
	t.Cleanup(backup.Close)
	window, threshold := 10, 1
	svc := newRealAccountPipelineTestService(t, []*store.UpstreamAccountRecord{
		{ProviderType: "api_key", AccountName: "policy-primary", CredentialRaw: "fixture-primary", BaseURL: primary.URL, Priority: 10, Enabled: true, State: "active", SoftFailureWindowMinutes: &window, SoftFailureThreshold: &threshold},
		{ProviderType: "api_key", AccountName: "policy-backup", CredentialRaw: "fixture-backup", BaseURL: backup.URL, Priority: 10, Enabled: true, State: "active"},
	})
	handler := newAccountPipelineTestHandler(t, primary.URL, svc)
	result := performResponsesRequest(t, handler)
	if result.Code != http.StatusOK {
		t.Fatalf("自定义阈值未触发换号: %d %s", result.Code, result.Body.String())
	}
	if atomic.LoadInt32(&primaryHits) != 1 || atomic.LoadInt32(&backupHits) != 1 {
		t.Fatal("同账号重试次数发生变化")
	}
}
