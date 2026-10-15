package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sinuo/internal/accountauth"
	"sinuo/internal/middleware"
	"sinuo/internal/store"
	"sinuo/internal/tracking"
)

func TestAccountPipelineUsageKnownForSubscriptionPoolRegularResponse(t *testing.T) {
	tests := []struct {
		name           string
		responseBody   string
		wantUsageKnown bool
	}{
		{
			name:           "missing usage remains unknown",
			responseBody:   `{"id":"resp_pool","status":"completed","model":"gpt-5","output":[]}`,
			wantUsageKnown: false,
		},
		{
			name:           "reported zero usage remains known",
			responseBody:   `{"id":"resp_pool","status":"completed","model":"gpt-5","output":[],"usage":{"input_tokens":0,"output_tokens":0}}`,
			wantUsageKnown: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker := newLifecyclePanelTestTracker(t)
			env := newSubscriptionPoolTestEnv(t, []*store.UpstreamAccountRecord{{
				ID:            11,
				AccountName:   "pool-usage-account",
				ProviderType:  accountauth.ProviderAPIKey,
				CredentialRaw: "sk-pool-usage",
				BaseURL:       "http://upstream.invalid",
				Enabled:       true,
			}}, true)
			env.transport.responder = jsonAccountResponse(tt.responseBody)
			env.handler.proxy.SetMonitoringMiddleware(middleware.NewMonitoringMiddleware(env.handler.proxy.endpointManager))
			env.handler.proxy.SetUsageTracker(tracker)

			rec := env.serve(t, http.MethodPost, "responses", nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if got := rec.Body.String(); got != tt.responseBody {
				t.Fatalf("response body changed: got %q, want %q", got, tt.responseBody)
			}

			row := singleTrackedRequest(t, tracker)
			if row.Status != "completed" || row.HTTPStatusCode == nil || *row.HTTPStatusCode != http.StatusOK {
				t.Fatalf("successful response was not preserved: %+v", row)
			}
			if row.RequestSource != tracking.RequestSourceCodexSubscriptionProxy || row.RequestKind != tracking.RequestKindGeneration {
				t.Fatalf("unexpected request classification: source=%q kind=%q", row.RequestSource, row.RequestKind)
			}
			if row.UsageKnown != tt.wantUsageKnown || row.CostKnown != tt.wantUsageKnown {
				t.Fatalf("known state = usage:%v cost:%v, want both %v", row.UsageKnown, row.CostKnown, tt.wantUsageKnown)
			}
			if row.InputTokens != 0 || row.OutputTokens != 0 || row.TotalCostUSD != 0 {
				t.Fatalf("unexpected non-zero usage or cost: %+v", row)
			}
		})
	}
}

func TestAccountPipelineMissingUsageKeepsLegacyCompatibility(t *testing.T) {
	const responseBody = `{"id":"resp_legacy","status":"completed","model":"gpt-5","output":[]}`
	tracker := newLifecyclePanelTestTracker(t)
	env := newSubscriptionPoolTestEnv(t, []*store.UpstreamAccountRecord{{
		ID:            12,
		AccountName:   "legacy-usage-account",
		ProviderType:  accountauth.ProviderAPIKey,
		CredentialRaw: "sk-legacy-usage",
		BaseURL:       "http://upstream.invalid",
		Enabled:       true,
	}}, true)
	env.transport.responder = jsonAccountResponse(responseBody)
	env.handler.proxy.SetMonitoringMiddleware(middleware.NewMonitoringMiddleware(env.handler.proxy.endpointManager))
	env.handler.proxy.SetUsageTracker(tracker)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	req = req.WithContext(context.WithValue(req.Context(), "conn_id", "legacy-usage-request"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	env.handler.proxy.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != responseBody {
		t.Fatalf("legacy response changed: status=%d body=%s", rec.Code, rec.Body.String())
	}

	row := singleTrackedRequest(t, tracker)
	if row.RequestSource != tracking.RequestSourceLegacyProxy || row.RequestKind != tracking.RequestKindLegacy {
		t.Fatalf("unexpected legacy classification: source=%q kind=%q", row.RequestSource, row.RequestKind)
	}
	if !row.UsageKnown || !row.CostKnown {
		t.Fatalf("legacy missing-usage compatibility changed: usage=%v cost=%v", row.UsageKnown, row.CostKnown)
	}
}

func jsonAccountResponse(body string) func(*http.Request) (*http.Response, error) {
	return func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	}
}

func singleTrackedRequest(t *testing.T, tracker *tracking.UsageTracker) tracking.RequestDetail {
	t.Helper()
	if err := tracker.ForceFlush(); err != nil {
		t.Fatal(err)
	}
	rows, _, err := tracker.QueryRequestDetailsWithHotPool(context.Background(), &tracking.QueryOptions{
		UpstreamType: "all",
		Limit:        10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("tracked rows = %d, want 1", len(rows))
	}
	return rows[0]
}
