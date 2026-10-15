package proxy

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"sinuo/config"
	"sinuo/internal/accountauth"
	"sinuo/internal/endpoint"
	"sinuo/internal/privacy"
	servicepkg "sinuo/internal/service"
	"sinuo/internal/store"

	_ "modernc.org/sqlite"
)

// stubPrivacyFilter proxy 集成测试用的可编程过滤器
type stubPrivacyFilter struct {
	mu         sync.Mutex
	applyCalls int
	requests   []privacy.Request
	bodies     [][]byte
	redactTo   []byte
	err        error
}

func (s *stubPrivacyFilter) Apply(_ context.Context, req privacy.Request, body []byte) (privacy.ApplyResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applyCalls++
	s.requests = append(s.requests, req)
	s.bodies = append(s.bodies, append([]byte(nil), body...))
	if s.err != nil {
		return privacy.ApplyResult{Action: privacy.ModeRedact}, s.err
	}
	out := body
	changed := false
	if s.redactTo != nil {
		out = s.redactTo
		changed = true
	}
	return privacy.ApplyResult{Body: out, Changed: changed, Action: privacy.ModeRedact}, nil
}

func (s *stubPrivacyFilter) SnapshotVersion() int64 { return 1 }

func (s *stubPrivacyFilter) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applyCalls
}

func TestAccountPipeline_PrivacyPolicyErrorShortCircuitsWithoutFailover(t *testing.T) {
	upstreamHits := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","status":"completed","output":[]}`))
	}))
	defer upstream.Close()

	service := &mockAccountPoolService{
		accounts: []*store.UpstreamAccountRecord{
			{ID: 21, AccountName: "acc-a", ProviderType: "api_key", CredentialRaw: "sk-a", BaseURL: upstream.URL, Enabled: true},
			{ID: 22, AccountName: "acc-b", ProviderType: "api_key", CredentialRaw: "sk-b", BaseURL: upstream.URL, Enabled: true},
		},
	}
	handler := newAccountPipelineTestHandler(t, upstream.URL, service)
	handler.SetPrivacyFilter(&stubPrivacyFilter{
		err: &privacy.PolicyError{StatusCode: 413, Code: privacy.CodeScanBodyTooLarge, Message: "scannable text too large"},
	})

	rec := performResponsesRequest(t, handler)

	if rec.Code != 413 {
		t.Fatalf("expected policy status 413, got %d body=%s", rec.Code, rec.Body.String())
	}
	if upstreamHits != 0 {
		t.Fatalf("blocked request must not reach upstream, hits=%d", upstreamHits)
	}
	// 不冷却、不软失败、不换号、不鉴权失效
	if len(service.transientCalls) != 0 {
		t.Errorf("policy error must not cool down account: %+v", service.transientCalls)
	}
	if len(service.softFailureCalls) != 0 {
		t.Errorf("policy error must not record soft failure: %+v", service.softFailureCalls)
	}
	if len(service.authFailCalls) != 0 {
		t.Errorf("policy error must not mark auth failure: %+v", service.authFailCalls)
	}
	// 只尝试第一个账号即短路
	if len(service.completeCalls) != 1 {
		t.Fatalf("expected single schedule completion, got %+v", service.completeCalls)
	}
	if service.completeCalls[0].outcome != servicepkg.AccountScheduleOutcomePrivacyBlocked {
		t.Errorf("schedule outcome = %q, want privacy_blocked", service.completeCalls[0].outcome)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(privacy.CodeScanBodyTooLarge)) {
		t.Errorf("response body missing policy code: %s", rec.Body.String())
	}
}

// 被隐私策略拦下的请求不能先触发 RT 刷新：凭据里已有账号 ID 时，鉴权解析在隐私过滤之后。
func TestAccountPipeline_PrivacyPolicyErrorSkipsOAuthRefresh(t *testing.T) {
	refreshHits := 0
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at-oauth","refresh_token":"rt-oauth","expires_in":3600}`))
	}))
	defer authServer.Close()
	oldRefreshURL := accountauth.CurrentOpenAIRefreshTokenURLForTest()
	t.Cleanup(func() { accountauth.SetOpenAIRefreshTokenURLForTest(oldRefreshURL) })
	accountauth.SetOpenAIRefreshTokenURLForTest(authServer.URL)

	service := &mockAccountPoolService{
		accounts: []*store.UpstreamAccountRecord{
			{ID: 23, AccountName: "oauth-a", ProviderType: accountauth.ProviderChatGPTRefreshToken, CredentialRaw: `{"refresh_token":"rt-privacy","chatgpt_account_id":"acc-privacy"}`, Enabled: true},
		},
	}
	handler := newAccountPipelineTestHandler(t, "https://example.com", service)
	handler.SetPrivacyFilter(&stubPrivacyFilter{
		err: &privacy.PolicyError{StatusCode: 413, Code: privacy.CodeScanBodyTooLarge, Message: "scannable text too large"},
	})

	rec := performResponsesRequest(t, handler)

	if rec.Code != 413 {
		t.Fatalf("expected policy status 413, got %d body=%s", rec.Code, rec.Body.String())
	}
	if refreshHits != 0 {
		t.Fatalf("blocked request must not refresh token, hits=%d", refreshHits)
	}
	if len(service.softFailureCalls) != 0 || len(service.completeCalls) != 1 {
		t.Fatalf("blocked request must short-circuit: soft=%+v complete=%+v", service.softFailureCalls, service.completeCalls)
	}
}

// 按 RT 缓存的账号 ID 落后于已编辑的凭据时，出站头与身份 scope 同用凭据里的新 ID。
func TestAccountPipeline_OAuthAccountIDFollowsEditedCredential(t *testing.T) {
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at-edit","refresh_token":"rt-edit","expires_in":3600}`))
	}))
	defer authServer.Close()
	oldRefreshURL := accountauth.CurrentOpenAIRefreshTokenURLForTest()
	t.Cleanup(func() { accountauth.SetOpenAIRefreshTokenURLForTest(oldRefreshURL) })
	accountauth.SetOpenAIRefreshTokenURLForTest(authServer.URL)

	service := &mockAccountPoolService{
		accounts: []*store.UpstreamAccountRecord{
			{ID: 24, AccountName: "oauth-edit", ProviderType: accountauth.ProviderChatGPTRefreshToken, CredentialRaw: `{"refresh_token":"rt-edit","chatgpt_account_id":"acc-new"}`, Enabled: true},
		},
	}
	handler := newAccountPipelineTestHandler(t, "https://example.com", service)
	if _, err := handler.refreshTokenManager.ResolveAccessTokenDetails(context.Background(), `{"refresh_token":"rt-edit","chatgpt_account_id":"acc-old"}`); err != nil {
		t.Fatal(err)
	}
	var sentAccountID string
	handler.accountHTTPInitOnce.Do(func() {})
	handler.accountHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		sentAccountID = req.Header.Get("chatgpt-account-id")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader([]byte(`{"id":"resp_edit","status":"completed","output":[]}`))),
			Request:    req,
		}, nil
	})}
	handler.accountSSEHTTPClient = handler.accountHTTPClient

	rec := performResponsesRequest(t, handler)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if sentAccountID != "acc-new" {
		t.Fatalf("chatgpt-account-id = %q, want acc-new", sentAccountID)
	}
}

func TestAccountPipeline_PrivacyRedactsBodyBeforeUpstream(t *testing.T) {
	var receivedBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","status":"completed","output":[]}`))
	}))
	defer upstream.Close()

	service := &mockAccountPoolService{
		accounts: []*store.UpstreamAccountRecord{
			{ID: 31, AccountName: "acc-a", ProviderType: "api_key", CredentialRaw: "sk-a", BaseURL: upstream.URL, Enabled: true},
		},
	}
	handler := newAccountPipelineTestHandler(t, upstream.URL, service)
	redacted := []byte(`{"model":"gpt-4.1","input":"[已脱敏]"}`)
	handler.SetPrivacyFilter(&stubPrivacyFilter{redactTo: redacted})

	rec := performResponsesRequest(t, handler)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected success, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(receivedBody, redacted) {
		t.Errorf("upstream received %s, want redacted body", receivedBody)
	}
}

func TestAccountPipeline_ZstdPrivacyScansPlaintextBeforeUpstream(t *testing.T) {
	var receivedBody []byte
	var receivedContentEncoding string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		receivedContentEncoding = r.Header.Get("Content-Encoding")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","status":"completed","output":[]}`))
	}))
	defer upstream.Close()

	service := &mockAccountPoolService{accounts: []*store.UpstreamAccountRecord{
		{ID: 32, AccountName: "privacy-zstd", ProviderType: "api_key", CredentialRaw: "sk-zstd", BaseURL: upstream.URL, Enabled: true},
	}}
	handler := newAccountPipelineTestHandler(t, upstream.URL, service)
	original := []byte(`{"model":"gpt-4.1","input":"secret before redaction"}`)
	redacted := []byte(`{"model":"gpt-4.1","input":"[已脱敏]"}`)
	filter := &stubPrivacyFilter{redactTo: redacted}
	handler.SetPrivacyFilter(filter)

	rec := performZstdAccountRequest(t, handler, "/v1/responses", original)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected success, got %d body=%s", rec.Code, rec.Body.String())
	}
	filter.mu.Lock()
	scannedBodies := append([][]byte(nil), filter.bodies...)
	filter.mu.Unlock()
	if len(scannedBodies) != 1 || !bytes.Equal(scannedBodies[0], original) {
		t.Fatalf("privacy filter must scan normalized plaintext, got %q", scannedBodies)
	}
	if !bytes.Equal(receivedBody, redacted) {
		t.Fatalf("upstream received %s, want redacted body", receivedBody)
	}
	if receivedContentEncoding != "" {
		t.Fatalf("upstream Content-Encoding must be empty, got %q", receivedContentEncoding)
	}
}

func TestAccountPipeline_ZstdRealPrivacyServiceRedactsBeforeUpstream(t *testing.T) {
	var receivedBody []byte
	var receivedContentEncoding string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		receivedContentEncoding = r.Header.Get("Content-Encoding")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","status":"completed","output":[]}`))
	}))
	defer upstream.Close()

	privacyService := newProxyTestPrivacyService(t)
	ctx := context.Background()
	settings, err := privacyService.GetSettings(ctx)
	if err != nil {
		t.Fatalf("get privacy settings: %v", err)
	}
	settings.Mode = privacy.ModeRedact
	if _, err := privacyService.UpdateSettings(ctx, settings); err != nil {
		t.Fatalf("enable privacy redact mode: %v", err)
	}
	if _, err := privacyService.CreateRule(ctx, &store.PrivacyRuleRecord{
		Enabled: true, Name: "OpenAI Key", Priority: 100, MatchType: "regex",
		Pattern: `sk-(?:proj-)?[A-Za-z0-9_-]{20,}`, Placeholder: "[OpenAI密钥]",
		Action: "redact", ScopeJSON: `{"paths":["/v1/responses"]}`,
	}); err != nil {
		t.Fatalf("create privacy rule: %v", err)
	}

	service := &mockAccountPoolService{accounts: []*store.UpstreamAccountRecord{
		{ID: 33, AccountName: "privacy-real-zstd", ProviderType: "api_key", CredentialRaw: "sk-zstd", BaseURL: upstream.URL, EnableRequestCompression: true, Enabled: true},
	}}
	handler := newAccountPipelineTestHandler(t, upstream.URL, service)
	handler.SetPrivacyFilter(privacyService)
	originalSecret := "sk-proj-abcdefghijklmnopqrstuvwxyz123456"
	body := []byte(`{"model":"gpt-4.1","input":"key ` + originalSecret + `"}`)

	rec := performZstdAccountRequest(t, handler, "/v1/responses", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected success, got %d body=%s", rec.Code, rec.Body.String())
	}
	if receivedContentEncoding != "zstd" {
		t.Fatalf("expected zstd upstream encoding, got %q", receivedContentEncoding)
	}
	decoded, err := decodeZstdRequestBody(receivedBody, defaultEncodedRequestBodyMaxBytes)
	if err != nil {
		t.Fatalf("decode upstream zstd body: %v", err)
	}
	if !bytes.Contains(decoded, []byte("[OpenAI密钥]")) {
		t.Fatalf("upstream body must contain redaction placeholder: %s", decoded)
	}
	if bytes.Contains(decoded, []byte(originalSecret)) {
		t.Fatalf("original secret leaked to upstream: %s", decoded)
	}
}

// newPrivacyEndpointTestHandler 构造指向本地上游的 endpoint 链路 Handler
func newPrivacyEndpointTestHandler(t *testing.T, upstreamURL string) *Handler {
	t.Helper()
	cfg := &config.Config{
		Endpoints: []config.EndpointConfig{
			{Name: "privacy-ep", URL: upstreamURL, Priority: 1, Timeout: 5 * time.Second, Token: "tok"},
		},
		Failover: config.FailoverConfig{Enabled: true},
	}
	endpointManager := endpoint.NewManager(cfg)
	return NewHandler(endpointManager, cfg)
}

func TestEndpointRegular_PrivacyRedactsBodyBeforeUpstream(t *testing.T) {
	var receivedBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer upstream.Close()

	handler := newPrivacyEndpointTestHandler(t, upstream.URL)
	redacted := []byte(`{"model":"claude-sonnet-4","messages":[{"role":"user","content":"[已脱敏]"}]}`)
	handler.SetPrivacyFilter(&stubPrivacyFilter{redactTo: redacted})

	body := bytes.NewBufferString(`{"model":"claude-sonnet-4","messages":[{"role":"user","content":"secret"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected success, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(receivedBody, redacted) {
		t.Errorf("upstream received %s, want redacted body", receivedBody)
	}
}

func TestEndpointRegular_PrivacyPolicyErrorShortCircuitsWithoutRetry(t *testing.T) {
	upstreamHits := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	handler := newPrivacyEndpointTestHandler(t, upstream.URL)
	filter := &stubPrivacyFilter{
		err: &privacy.PolicyError{StatusCode: 422, Code: privacy.CodeScanFailed, Message: "scan failed"},
	}
	handler.SetPrivacyFilter(filter)

	body := bytes.NewBufferString(`{"model":"claude-sonnet-4","messages":[{"role":"user","content":"x"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != 422 {
		t.Fatalf("expected policy status 422, got %d body=%s", rec.Code, rec.Body.String())
	}
	if upstreamHits != 0 {
		t.Errorf("blocked request must not reach upstream, hits=%d", upstreamHits)
	}
	// MaxAttempts=2，但策略错误已缓存且短路，只应扫描一次
	if filter.calls() != 1 {
		t.Errorf("apply calls = %d, want 1 (no rescan on retry)", filter.calls())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(privacy.CodeScanFailed)) {
		t.Errorf("response body missing policy code: %s", rec.Body.String())
	}
}

func TestCountTokens_PrivacyPolicyErrorReturnsPolicyStatus(t *testing.T) {
	upstreamHits := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		_, _ = w.Write([]byte(`{"input_tokens":42}`))
	}))
	defer upstream.Close()

	cfg := &config.Config{
		TokenCounting: config.TokenCountingConfig{Enabled: true},
		Failover:      config.FailoverConfig{Enabled: true},
		Endpoints: []config.EndpointConfig{
			{Name: "count-ep", URL: upstream.URL, Priority: 1, Timeout: 5 * time.Second, SupportsCountTokens: true},
		},
	}
	handler := NewHandler(endpoint.NewManager(cfg), cfg)
	filter := &stubPrivacyFilter{
		err: &privacy.PolicyError{StatusCode: http.StatusRequestEntityTooLarge, Code: privacy.CodeScanBodyTooLarge, Message: "too large"},
	}
	handler.SetPrivacyFilter(filter)

	body := bytes.NewBufferString(`{"model":"claude-sonnet-4","messages":[{"role":"user","content":"secret"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", body)
	req = req.WithContext(context.WithValue(req.Context(), "conn_id", "req-count-tokens-privacy"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected policy status 413, got %d body=%s", rec.Code, rec.Body.String())
	}
	if upstreamHits != 0 {
		t.Fatalf("policy-blocked count_tokens must not reach upstream, hits=%d", upstreamHits)
	}
	if filter.calls() != 1 {
		t.Fatalf("privacy filter calls = %d, want 1", filter.calls())
	}
	filter.mu.Lock()
	gotRequestID := ""
	if len(filter.requests) > 0 {
		gotRequestID = filter.requests[0].RequestID
	}
	filter.mu.Unlock()
	if gotRequestID != "req-count-tokens-privacy" {
		t.Fatalf("privacy request id = %q, want req-count-tokens-privacy", gotRequestID)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(privacy.CodeScanBodyTooLarge)) {
		t.Errorf("response body missing policy code: %s", rec.Body.String())
	}
}

// TestEndpointRegular_RealPrivacyServiceEndToEnd 用真实 PrivacyService 走通
// “规则保存 -> 热生效 -> redact 上游 -> 关闭恢复透传” 全链路
func TestEndpointRegular_RealPrivacyServiceEndToEnd(t *testing.T) {
	var receivedBodies [][]byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedBodies = append(receivedBodies, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer upstream.Close()

	svc := newProxyTestPrivacyService(t)
	ctx := context.Background()

	settings, err := svc.GetSettings(ctx)
	if err != nil {
		t.Fatalf("get settings failed: %v", err)
	}
	settings.Mode = privacy.ModeRedact
	if _, err := svc.UpdateSettings(ctx, settings); err != nil {
		t.Fatalf("update settings failed: %v", err)
	}
	if _, err := svc.CreateRule(ctx, &store.PrivacyRuleRecord{
		Enabled: true, Name: "OpenAI Key", Priority: 100, MatchType: "regex",
		Pattern: `sk-(?:proj-)?[A-Za-z0-9_-]{20,}`, Placeholder: "[OpenAI密钥]",
		Action: "redact", ScopeJSON: "{}",
	}); err != nil {
		t.Fatalf("create rule failed: %v", err)
	}

	handler := newPrivacyEndpointTestHandler(t, upstream.URL)
	handler.SetPrivacyFilter(svc)

	send := func() *httptest.ResponseRecorder {
		body := bytes.NewBufferString(`{"model":"claude-sonnet-4","messages":[{"role":"user","content":"key sk-proj-abcdefghijklmnopqrstuvwxyz123456"}]}`)
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", body)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	if rec := send(); rec.Code != http.StatusOK {
		t.Fatalf("redact request failed: %d %s", rec.Code, rec.Body.String())
	}
	if len(receivedBodies) != 1 || !bytes.Contains(receivedBodies[0], []byte("[OpenAI密钥]")) {
		t.Fatalf("upstream must receive redacted body: %s", receivedBodies[0])
	}
	if bytes.Contains(receivedBodies[0], []byte("sk-proj-")) {
		t.Errorf("original secret leaked to upstream: %s", receivedBodies[0])
	}

	// 切回 disabled 后恢复透传
	settings, _ = svc.GetSettings(ctx)
	settings.Mode = privacy.ModeDisabled
	if _, err := svc.UpdateSettings(ctx, settings); err != nil {
		t.Fatalf("disable failed: %v", err)
	}
	if rec := send(); rec.Code != http.StatusOK {
		t.Fatalf("disabled request failed: %d %s", rec.Code, rec.Body.String())
	}
	if len(receivedBodies) != 2 || !bytes.Contains(receivedBodies[1], []byte("sk-proj-abcdefghijklmnopqrstuvwxyz123456")) {
		t.Errorf("disabled mode must passthrough original body: %s", receivedBodies[1])
	}
}

func newProxyTestPrivacyService(t *testing.T) *servicepkg.PrivacyService {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	svc := servicepkg.NewPrivacyService(store.NewSQLitePrivacyStore(db))
	if err := svc.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize privacy service failed: %v", err)
	}
	return svc
}
