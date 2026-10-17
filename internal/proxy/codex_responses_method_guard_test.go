package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sinuo/internal/service"
)

func TestHandler_RejectsNonPostCodexResponsesBeforePipelines(t *testing.T) {
	tracker := newCodexModelsTestTracker(t)
	defer tracker.Close()

	accountService := &mockAccountPoolService{}
	router := &mockCodexModelRouter{enabled: true}
	handler := newAccountPipelineTestHandler(t, "https://example.com", accountService)
	handler.SetUsageTracker(tracker)
	handler.SetCodexModelRouter(router)

	tests := []struct {
		name             string
		method           string
		path             string
		websocketUpgrade bool
		status           int
		errorType        string
		allow            string
	}{
		{
			name:             "websocket upgrade",
			method:           http.MethodGet,
			path:             "/v1/responses",
			websocketUpgrade: true,
			status:           http.StatusUpgradeRequired,
			errorType:        "responses_websocket_unsupported",
		},
		{
			name:      "responses get",
			method:    http.MethodGet,
			path:      "/v1/responses",
			status:    http.StatusMethodNotAllowed,
			errorType: "method_not_allowed",
			allow:     http.MethodPost,
		},
		{
			name:      "responses compact get",
			method:    http.MethodGet,
			path:      "/v1/responses/compact",
			status:    http.StatusMethodNotAllowed,
			errorType: "method_not_allowed",
			allow:     http.MethodPost,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			requestID := "req-non-post-" + strings.ReplaceAll(tc.name, " ", "-")
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.RemoteAddr = "192.0.2.10:1234"
			req = req.WithContext(context.WithValue(req.Context(), "conn_id", requestID))
			if tc.websocketUpgrade {
				req.Header.Set("Upgrade", "websocket")
				req.Header.Set("Connection", "keep-alive, Upgrade")
			}

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tc.status {
				t.Fatalf("expected status %d, got %d body=%s", tc.status, rec.Code, rec.Body.String())
			}
			if allow := rec.Header().Get("Allow"); allow != tc.allow {
				t.Fatalf("expected Allow=%q, got %q", tc.allow, allow)
			}
			var payload struct {
				Error struct {
					Type string `json:"type"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if payload.Error.Type != tc.errorType {
				t.Fatalf("expected error.type=%q, got %q", tc.errorType, payload.Error.Type)
			}
			assertNoTrackedRequest(t, tracker, requestID)
		})
	}

	accountService.mu.Lock()
	prepareCalls := len(accountService.prepareCalls)
	savedDrafts := len(accountService.savedDrafts)
	completeCalls := len(accountService.completeCalls)
	accountService.mu.Unlock()
	if prepareCalls != 0 || savedDrafts != 0 || completeCalls != 0 {
		t.Fatalf("expected non-POST requests to skip account scheduling, prepare=%d saved=%d complete=%d", prepareCalls, savedDrafts, completeCalls)
	}
	router.mu.Lock()
	prepareRouterCalls := len(router.prepareCalls)
	router.mu.Unlock()
	if prepareRouterCalls != 0 {
		t.Fatalf("expected non-POST requests to skip model routing, got %d calls", prepareRouterCalls)
	}
}

func TestHandler_CodexResponsesPostStillReachesAccountPipeline(t *testing.T) {
	accountService := &mockAccountPoolService{}
	router := &mockCodexModelRouter{
		enabled: true,
		result: service.CodexModelRouteResult{
			Scope: service.CodexModelRouteScope{
				Kind:             "unassigned",
				UnassignedReason: service.CodexModelRoutingUnassigned,
			},
		},
	}
	handler := newAccountPipelineTestHandler(t, "https://example.com", accountService)
	handler.SetCodexModelRouter(router)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.6","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected POST to reach model routing and return its route error, got %d body=%s", rec.Code, rec.Body.String())
	}
	accountService.mu.Lock()
	prepareCalls := len(accountService.prepareCalls)
	savedDrafts := len(accountService.savedDrafts)
	accountService.mu.Unlock()
	if prepareCalls != 0 {
		t.Fatalf("expected model-routed POST not to call account service Prepare, got %d", prepareCalls)
	}
	if savedDrafts != 1 {
		t.Fatalf("expected model-routed POST to save one schedule draft, got %d", savedDrafts)
	}
	router.mu.Lock()
	prepareRouterCalls := len(router.prepareCalls)
	router.mu.Unlock()
	if prepareRouterCalls != 1 {
		t.Fatalf("expected POST to invoke model routing once, got %d", prepareRouterCalls)
	}
}
