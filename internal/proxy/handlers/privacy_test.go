package handlers

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"sinuo/config"
	"sinuo/internal/endpoint"
	"sinuo/internal/privacy"
)

// fakePrivacyFilter 可编程的 PrivacyFilter 测试替身
type fakePrivacyFilter struct {
	mu         sync.Mutex
	applyCalls int
	requests   []privacy.Request
	redactTo   []byte
	err        error
	version    int64
}

type fakeScopePrivacyFilter struct {
	*fakePrivacyFilter
	fingerprint string
}

func (f *fakeScopePrivacyFilter) PrivacyScopeFingerprint(_ privacy.Request, snapshotVersion int64) string {
	return fmt.Sprintf("%s|v%d", f.fingerprint, snapshotVersion)
}

func (f *fakePrivacyFilter) Apply(_ context.Context, req privacy.Request, body []byte) (privacy.ApplyResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applyCalls++
	f.requests = append(f.requests, req)
	if f.err != nil {
		return privacy.ApplyResult{Action: privacy.ModeRedact}, f.err
	}
	out := body
	changed := false
	if f.redactTo != nil {
		out = f.redactTo
		changed = true
	}
	return privacy.ApplyResult{Body: out, Changed: changed, Action: privacy.ModeRedact}, nil
}

func (f *fakePrivacyFilter) SnapshotVersion() int64 {
	return f.version
}

func (f *fakePrivacyFilter) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.applyCalls
}

func newPrivacyTestEndpoint(name string) *endpoint.Endpoint {
	cfg := &config.Config{
		Endpoints: []config.EndpointConfig{
			{Name: name, URL: "https://upstream.example.com", Priority: 1, Timeout: 5 * time.Second},
		},
	}
	manager := endpoint.NewManager(cfg)
	return manager.GetAllEndpoints()[0]
}

func newPrivacyTestRequest(t *testing.T, state *PrivacyRequestState) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	if state != nil {
		req = req.WithContext(WithPrivacyRequestState(req.Context(), state))
	}
	return req
}

func TestApplyPrivacyFilterForEndpointNilFilterPassthrough(t *testing.T) {
	body := []byte(`{"messages":[]}`)
	out, err := ApplyPrivacyFilterForEndpoint(nil, newPrivacyTestRequest(t, nil), body, newPrivacyTestEndpoint("ep-a"))
	if err != nil {
		t.Fatalf("nil filter must not error: %v", err)
	}
	if !bytes.Equal(out, body) {
		t.Error("nil filter must passthrough body")
	}
}

func TestApplyPrivacyFilterAttemptCacheReusesResult(t *testing.T) {
	filter := &fakePrivacyFilter{redactTo: []byte(`{"redacted":true}`), version: 7}
	state := NewPrivacyRequestState("req-cache-01")
	req := newPrivacyTestRequest(t, state)
	ep := newPrivacyTestEndpoint("ep-a")

	first, err := ApplyPrivacyFilterForEndpoint(filter, req, []byte(`{"original":true}`), ep)
	if err != nil {
		t.Fatalf("first apply failed: %v", err)
	}
	// 模拟同端点重试：同一 requestID + scopeFingerprint 不得重复扫描
	second, err := ApplyPrivacyFilterForEndpoint(filter, req, []byte(`{"original":true}`), ep)
	if err != nil {
		t.Fatalf("second apply failed: %v", err)
	}
	if filter.calls() != 1 {
		t.Errorf("apply calls = %d, want 1 (retry must reuse cached result)", filter.calls())
	}
	if !bytes.Equal(first, second) {
		t.Error("cached result must match original result")
	}
	if filter.requests[0].RequestID != "req-cache-01" {
		t.Errorf("request id not propagated: %+v", filter.requests[0])
	}
}

func TestApplyPrivacyFilterDifferentEndpointsScannedSeparately(t *testing.T) {
	filter := &fakePrivacyFilter{version: 1}
	state := NewPrivacyRequestState("req-scope-01")
	req := newPrivacyTestRequest(t, state)

	if _, err := ApplyPrivacyFilterForEndpoint(filter, req, []byte(`{}`), newPrivacyTestEndpoint("ep-a")); err != nil {
		t.Fatalf("apply ep-a failed: %v", err)
	}
	if _, err := ApplyPrivacyFilterForEndpoint(filter, req, []byte(`{}`), newPrivacyTestEndpoint("ep-b")); err != nil {
		t.Fatalf("apply ep-b failed: %v", err)
	}
	if filter.calls() != 2 {
		t.Errorf("apply calls = %d, want 2 (different endpoints = different scope)", filter.calls())
	}
}

func TestApplyPrivacyFilterScopeFingerprintReusesAcrossEndpoints(t *testing.T) {
	filter := &fakeScopePrivacyFilter{
		fakePrivacyFilter: &fakePrivacyFilter{version: 3},
		fingerprint:       "global:/v1/messages",
	}
	state := NewPrivacyRequestState("req-global-scope-01")
	req := newPrivacyTestRequest(t, state)

	if _, err := ApplyPrivacyFilterForEndpoint(filter, req, []byte(`{"same":true}`), newPrivacyTestEndpoint("ep-a")); err != nil {
		t.Fatalf("apply ep-a failed: %v", err)
	}
	if _, err := ApplyPrivacyFilterForEndpoint(filter, req, []byte(`{"same":true}`), newPrivacyTestEndpoint("ep-b")); err != nil {
		t.Fatalf("apply ep-b failed: %v", err)
	}
	if filter.calls() != 1 {
		t.Errorf("apply calls = %d, want 1 (scope fingerprint should reuse across endpoints)", filter.calls())
	}
}

func TestApplyPrivacyFilterBodyHashSeparatesCacheEntries(t *testing.T) {
	filter := &fakeScopePrivacyFilter{
		fakePrivacyFilter: &fakePrivacyFilter{version: 3},
		fingerprint:       "global:/v1/messages",
	}
	state := NewPrivacyRequestState("req-body-hash-01")
	req := newPrivacyTestRequest(t, state)

	if _, err := ApplyPrivacyFilterForEndpoint(filter, req, []byte(`{"body":"a"}`), newPrivacyTestEndpoint("ep-a")); err != nil {
		t.Fatalf("apply body a failed: %v", err)
	}
	if _, err := ApplyPrivacyFilterForEndpoint(filter, req, []byte(`{"body":"b"}`), newPrivacyTestEndpoint("ep-b")); err != nil {
		t.Fatalf("apply body b failed: %v", err)
	}
	if filter.calls() != 2 {
		t.Errorf("apply calls = %d, want 2 (different body hash must not reuse cache)", filter.calls())
	}
}

func TestApplyPrivacyFilterPolicyErrorCachedAndReturned(t *testing.T) {
	policyErr := &privacy.PolicyError{StatusCode: 413, Code: privacy.CodeScanBodyTooLarge, Message: "too large"}
	filter := &fakePrivacyFilter{err: policyErr, version: 1}
	state := NewPrivacyRequestState("req-policy-01")
	req := newPrivacyTestRequest(t, state)
	ep := newPrivacyTestEndpoint("ep-a")

	_, err := ApplyPrivacyFilterForEndpoint(filter, req, []byte(`{}`), ep)
	if AsPrivacyPolicyError(err) == nil {
		t.Fatalf("expected policy error, got %v", err)
	}
	_, err = ApplyPrivacyFilterForEndpoint(filter, req, []byte(`{}`), ep)
	if AsPrivacyPolicyError(err) == nil {
		t.Fatalf("cached policy error must be returned, got %v", err)
	}
	if filter.calls() != 1 {
		t.Errorf("apply calls = %d, want 1 (policy error cached for retries)", filter.calls())
	}
}

func TestAsPrivacyPolicyErrorUnwrapsWrappedError(t *testing.T) {
	policyErr := &privacy.PolicyError{StatusCode: 422, Code: privacy.CodeScanFailed, Message: "scan failed"}
	wrapped := fmt.Errorf("request failed: %w", policyErr)
	if got := AsPrivacyPolicyError(wrapped); got == nil || got.Code != privacy.CodeScanFailed {
		t.Fatalf("wrapped policy error not recognized: %v", got)
	}
	if AsPrivacyPolicyError(fmt.Errorf("plain error")) != nil {
		t.Error("plain error must not match")
	}
	if AsPrivacyPolicyError(nil) != nil {
		t.Error("nil error must not match")
	}
}

func TestPrivacyFailureReasonMapping(t *testing.T) {
	if got := PrivacyFailureReason(&privacy.PolicyError{Code: privacy.CodeScanFailed}); got != PrivacyFailureReasonScanFailed {
		t.Errorf("scan failed reason = %q", got)
	}
	if got := PrivacyFailureReason(&privacy.PolicyError{Code: privacy.CodeScanBodyTooLarge}); got != PrivacyFailureReasonBlocked {
		t.Errorf("blocked reason = %q", got)
	}
}

func TestApplyPrivacyFilterWithoutStateStillFilters(t *testing.T) {
	filter := &fakePrivacyFilter{redactTo: []byte(`{"redacted":true}`), version: 1}
	req := newPrivacyTestRequest(t, nil) // 无请求级状态（如 count_tokens 拦截路径）
	out, err := ApplyPrivacyFilterForEndpoint(filter, req, []byte(`{"x":1}`), newPrivacyTestEndpoint("ep-a"))
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if !bytes.Equal(out, []byte(`{"redacted":true}`)) {
		t.Error("filter must work without state (no caching)")
	}
}

func TestWritePrivacyPolicyErrorResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	WritePrivacyPolicyErrorResponse(rec, &privacy.PolicyError{
		StatusCode: 413, Code: privacy.CodeScanBodyTooLarge, Message: "too large",
	})
	if rec.Code != 413 {
		t.Errorf("status = %d, want 413", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(privacy.CodeScanBodyTooLarge)) {
		t.Errorf("body missing code: %s", rec.Body.String())
	}
}
