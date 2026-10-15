package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sinuo/config"
	"sinuo/internal/store"
)

func TestNormalizeFailoverEvent_RedactsAndBoundsDisplayFields(t *testing.T) {
	event, ok := normalizeFailoverEvent(FailoverEvent{
		Lane:         FailoverLaneCodex,
		From:         "main\naccount",
		To:           "backup",
		ReasonCode:   "auth_failed",
		ReasonDetail: `HTTP 401 Bearer very-secret-token refresh_token=rt-super-secret-value {"access_token":"eyJ-secret-value"}`,
		RequestID:    "request-1",
		Attempt:      -1,
	})
	if !ok {
		t.Fatal("expected valid failover event")
	}
	if event.Kind != FailoverEventKind {
		t.Fatalf("expected kind %q, got %q", FailoverEventKind, event.Kind)
	}
	if event.ReasonLabel != "鉴权失败" {
		t.Fatalf("expected localized reason label, got %q", event.ReasonLabel)
	}
	if strings.ContainsAny(event.From, "\r\n") {
		t.Fatalf("expected control characters removed from endpoint name, got %q", event.From)
	}
	if strings.Contains(event.ReasonDetail, "very-secret-token") ||
		strings.Contains(event.ReasonDetail, "rt-super-secret-value") ||
		strings.Contains(event.ReasonDetail, "eyJ-secret-value") {
		t.Fatalf("expected sensitive detail redacted, got %q", event.ReasonDetail)
	}
	if event.Attempt != 0 {
		t.Fatalf("expected negative attempt clamped to zero, got %d", event.Attempt)
	}
}

func TestFailoverReasonLabel_CoversKnownReasonCodes(t *testing.T) {
	knownCodes := []string{
		FailoverReasonUnknown,
		FailoverReasonConnectionFailedBeforeHeaders,
		FailoverReasonForwardError,
		FailoverReasonEmptyResponse,
		FailoverReasonAuthFailed,
		FailoverReasonAuthRejected,
		FailoverReasonRateLimited,
		FailoverReasonUsageLimit,
		FailoverReasonServerError,
		FailoverReasonProcessingError,
		FailoverReasonPayloadTooLarge,
		FailoverReasonModelUnsupported,
		FailoverReasonSchemaIncompatible,
	}
	for _, code := range knownCodes {
		label := FailoverReasonLabel(code)
		if strings.TrimSpace(label) == "" || label == code {
			t.Fatalf("expected localized label for %q, got %q", code, label)
		}
	}
	if got := FailoverReasonLabel("future_reason_code"); got != "未知原因（future_reason_code）" {
		t.Fatalf("expected unknown reason to retain a Chinese fallback label, got %q", got)
	}
}

func TestNotifyAccountFailover_DoesNotEmitWithoutNextCandidateOrAfterCancellation(t *testing.T) {
	handler := &Handler{}
	events := make(chan FailoverEvent, 1)
	handler.SetOnFailoverTriggered(func(event FailoverEvent) { events <- event })

	accounts := []*store.UpstreamAccountRecord{
		{ID: 1, AccountName: "main"},
		{ID: 2, AccountName: "backup"},
	}
	handler.notifyAccountFailover(context.Background(), accounts[:1], 0, "main", "auth_failed", http.StatusUnauthorized, "expired", "request-1", "/v1/responses")

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	handler.notifyAccountFailover(cancelledCtx, accounts, 0, "main", "auth_failed", http.StatusUnauthorized, "expired", "request-2", "/v1/responses")

	select {
	case event := <-events:
		t.Fatalf("expected no failover event, got %+v", event)
	default:
	}
}

func TestNotifyAccountFailover_SkipsNilCandidatesAndUsesActualAttempt(t *testing.T) {
	handler := &Handler{}
	events := make(chan FailoverEvent, 1)
	handler.SetOnFailoverTriggered(func(event FailoverEvent) { events <- event })

	accounts := []*store.UpstreamAccountRecord{
		{ID: 1, AccountName: "main"},
		nil,
		{ID: 2, AccountName: "backup"},
	}
	handler.notifyAccountFailover(context.Background(), accounts, 0, "main", FailoverReasonAuthFailed, http.StatusUnauthorized, "expired", "request-1", "/v1/models")

	select {
	case event := <-events:
		if event.To != "backup" || event.Attempt != 1 {
			t.Fatalf("expected next non-nil candidate with actual attempt 1, got %+v", event)
		}
	default:
		t.Fatal("expected failover event")
	}
}

func TestEndpointPipeline_EmitsFailoverEventForActualCandidateSwitch(t *testing.T) {
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(backup.Close)

	handler, _ := newEndpointPipelineTestHandler(t,
		endpointPipelineConfig("primary", endpointPipelineClosedURL(t), 1),
		endpointPipelineConfig("backup", backup.URL, 2),
	)
	events := make(chan FailoverEvent, 1)
	handler.SetOnFailoverTriggered(func(event FailoverEvent) { events <- event })

	recorder := performEndpointPipelineRequest(t, handler, false)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected backup success, got status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	select {
	case event := <-events:
		if event.Lane != FailoverLaneCC || event.From != "primary" || event.To != "backup" {
			t.Fatalf("unexpected endpoint failover event: %+v", event)
		}
		if event.ReasonCode != "connection_failed_before_wrote_headers" {
			t.Fatalf("unexpected endpoint failover reason: %+v", event)
		}
		if event.RequestPath != "/v1/messages" || event.Attempt != 1 {
			t.Fatalf("unexpected endpoint request metadata: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for endpoint failover event")
	}
}

func TestAccountPipeline_EmitsFailoverEventForAuthFailure(t *testing.T) {
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "invalid session", http.StatusUnauthorized)
	}))
	t.Cleanup(first.Close)
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok","status":"completed","output":[]}`))
	}))
	t.Cleanup(backup.Close)

	service := &mockAccountPoolService{accounts: []*store.UpstreamAccountRecord{
		{ID: 101, AccountName: "account-main", ProviderType: "api_key", CredentialRaw: "sk-main", BaseURL: first.URL, Enabled: true},
		{ID: 102, AccountName: "account-backup", ProviderType: "api_key", CredentialRaw: "sk-backup", BaseURL: backup.URL, Enabled: true},
	}}
	handler := newAccountPipelineTestHandler(t, first.URL, service)
	events := make(chan FailoverEvent, 1)
	handler.SetOnFailoverTriggered(func(event FailoverEvent) { events <- event })

	recorder := performResponsesRequest(t, handler)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected backup account success, got status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	select {
	case event := <-events:
		if event.Lane != FailoverLaneCodex || event.From != "account-main" || event.To != "account-backup" {
			t.Fatalf("unexpected account failover event: %+v", event)
		}
		if event.ReasonCode != "auth_failed" || !strings.HasPrefix(event.ReasonDetail, "HTTP 401") {
			t.Fatalf("unexpected account failover reason: %+v", event)
		}
		if event.RequestPath != "/v1/responses" || event.RequestID == "" || event.Attempt != 1 {
			t.Fatalf("unexpected account request metadata: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for account failover event")
	}
}

func TestCodexModelsAccountPassthrough_EmitsFailoverEvent(t *testing.T) {
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "expired", http.StatusUnauthorized)
	}))
	t.Cleanup(first.Close)
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	t.Cleanup(backup.Close)

	service := &mockAccountPoolService{accounts: []*store.UpstreamAccountRecord{
		{ID: 201, AccountName: "models-main", ProviderType: "api_key", CredentialRaw: "sk-models-main", BaseURL: first.URL, Enabled: true},
		{ID: 202, AccountName: "models-backup", ProviderType: "api_key", CredentialRaw: "sk-models-backup", BaseURL: backup.URL, Enabled: true},
	}}
	handler := newCodexModelsAccountPoolTestHandler(t, service, config.EndpointConfig{
		Name: "unused-codex-endpoint",
		URL:  backup.URL,
	})
	events := make(chan FailoverEvent, 1)
	handler.SetOnFailoverTriggered(func(event FailoverEvent) { events <- event })

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil).WithContext(context.WithValue(context.Background(), "conn_id", "models-request"))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected backup model list success, got status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	select {
	case event := <-events:
		if event.Lane != FailoverLaneCodex || event.From != "models-main" || event.To != "models-backup" {
			t.Fatalf("unexpected models failover event: %+v", event)
		}
		if event.RequestID != "models-request" || event.RequestPath != "/v1/models" || event.ReasonCode != "auth_failed" {
			t.Fatalf("unexpected models event metadata: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for models failover event")
	}
}
