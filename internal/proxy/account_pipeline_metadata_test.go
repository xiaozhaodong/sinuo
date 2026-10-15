package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sinuo/internal/privacy"
	"sinuo/internal/store"
	"sinuo/internal/tracking"
)

const accountMetadataFixture = ` {"model":"gpt-5.4","input":[{"type":"function_call_output","call_id":"fixture-call","output":"fixture output","internal_chat_message_metadata_passthrough":{"cell_id":"fixture-cell"}},{"type":"compaction","id":"fixture-compaction","encrypted_content":"ciphertext-unchanged"}],"number":9007199254740993} `

func TestAccountPipeline_CodexMetadataInvalidRequestDoesNotPenalizeAccount(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		for _, body := range []string{`{"model":"gpt-5.4","input":`, `{"model":"gpt-5.4","input":{}}`, `{"model":"gpt-5.4"} {}`} {
			t.Run(body+map[bool]string{false: "/auto", true: "/pinned"}[pinned], func(t *testing.T) {
				var hits atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hits.Add(1)
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"id":"fixture","status":"completed","output":[]}`))
				}))
				defer upstream.Close()
				svc := &mockAccountPoolService{pinned: pinned, accounts: []*store.UpstreamAccountRecord{
					{ID: 1, ProviderType: "api_key", AccountName: "filtered", CredentialRaw: "fixture-a", BaseURL: upstream.URL, Enabled: true, StripCodexInternalMetadata: true},
					{ID: 2, ProviderType: "api_key", AccountName: "original", CredentialRaw: "fixture-b", BaseURL: upstream.URL, Enabled: true},
				}}
				handler := newAccountPipelineTestHandler(t, upstream.URL, svc)
				req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(body))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				if rec.Code != http.StatusBadRequest || !bytes.Contains(rec.Body.Bytes(), []byte(requestBodyCodeCodexMetadataInvalid)) {
					t.Fatalf("expected local metadata 400, got %d: %s", rec.Code, rec.Body.String())
				}
				if hits.Load() != 0 || len(svc.softFailureCalls) != 0 || len(svc.transientCalls) != 0 || len(svc.authFailCalls) != 0 {
					t.Fatal("invalid local request reached upstream or penalized an account")
				}
				if len(svc.completeCalls) != 1 || svc.completeCalls[0].accountID != 1 || svc.completeCalls[0].outcome != "invalid_request" || svc.completeCalls[0].finalError != requestBodyCodeCodexMetadataInvalid {
					t.Fatalf("local error was not finalized once: %+v", svc.completeCalls)
				}
			})
		}
	}
}

func TestAccountPipeline_CodexMetadataAccountOptIn(t *testing.T) {
	for _, path := range []string{"/v1/responses", "/v1/responses/compact"} {
		for _, enabled := range []bool{false, true} {
			t.Run(path+map[bool]string{false: "/关闭", true: "/开启"}[enabled], func(t *testing.T) {
				received := make(chan []byte, 1)
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					received <- body
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"id":"fixture-response","status":"completed","output":[]}`))
				}))
				defer upstream.Close()
				svc := newRealAccountPipelineTestService(t, []*store.UpstreamAccountRecord{{
					ProviderType: "api_key", AccountName: "metadata-account", CredentialRaw: "fixture-credential",
					BaseURL: upstream.URL, Enabled: true, StripCodexInternalMetadata: enabled,
				}})
				handler := newAccountPipelineTestHandler(t, upstream.URL, svc)
				req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(accountMetadataFixture))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					t.Fatalf("status = %d, response = %s", rec.Code, rec.Body.String())
				}
				body := <-received
				if !enabled && string(body) != accountMetadataFixture {
					t.Fatal("disabled account must receive the original request bytes")
				}
				var payload struct {
					Input []map[string]json.RawMessage `json:"input"`
				}
				if err := json.Unmarshal(body, &payload); err != nil {
					t.Fatal(err)
				}
				_, exists := payload.Input[0][codexInternalMetadataKey]
				if exists == enabled {
					t.Fatalf("metadata present = %v, enabled = %v", exists, enabled)
				}
			})
		}
	}
}

type metadataWireRequest struct {
	body          []byte
	encoding      string
	contentLength int64
	trace         string
}

func TestAccountPipeline_CodexMetadataFailoverStartsFromOriginalBody(t *testing.T) {
	for _, firstEnabled := range []bool{true, false} {
		t.Run(fmt.Sprintf("first-enabled=%v", firstEnabled), func(t *testing.T) {
			var hits atomic.Int32
			received := make(chan []byte, 2)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				received <- body
				w.Header().Set("Content-Type", "application/json")
				if hits.Add(1) == 1 {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"error":{"message":"fixture auth failure"}}`))
					return
				}
				_, _ = w.Write([]byte(`{"id":"fixture-response","status":"completed","output":[]}`))
			}))
			defer upstream.Close()
			svc := newRealAccountPipelineTestService(t, []*store.UpstreamAccountRecord{
				{ProviderType: "api_key", AccountName: "first", CredentialRaw: "fixture-a", BaseURL: upstream.URL, Enabled: true, GroupKey: "primary", Priority: 10, EnableRequestCompression: true, StripCodexInternalMetadata: firstEnabled},
				{ProviderType: "api_key", AccountName: "second", CredentialRaw: "fixture-b", BaseURL: upstream.URL, Enabled: true, GroupKey: "primary", Priority: 20, EnableRequestCompression: true, StripCodexInternalMetadata: !firstEnabled},
			})
			handler := newAccountPipelineTestHandler(t, upstream.URL, svc)
			wire := mustEncodeZstd(t, []byte(accountMetadataFixture))
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(wire))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Content-Encoding", "zstd")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK || hits.Load() != 2 {
				t.Fatalf("failover: status=%d hits=%d response=%s", rec.Code, hits.Load(), rec.Body.String())
			}
			for _, enabled := range []bool{firstEnabled, !firstEnabled} {
				got := <-received
				if bytes.Equal(got, wire) != !enabled {
					t.Fatal("failover reused the wrong account's transformed body or compressed frame")
				}
				decoded, err := decodeZstdRequestBody(got, defaultEncodedRequestBodyMaxBytes)
				if err != nil {
					t.Fatal(err)
				}
				if bytes.Contains(decoded, []byte(codexInternalMetadataKey)) == enabled {
					t.Fatal("failover inherited another account's metadata setting")
				}
			}
		})
	}
}

func TestAccountPipeline_CodexMetadataCompressionAndTransformations(t *testing.T) {
	for _, provider := range []string{"api_key", "chatgpt_refresh_token"} {
		for _, path := range []string{"/v1/responses", "/v1/responses/compact"} {
			for _, inboundZstd := range []bool{false, true} {
				for _, outboundZstd := range []bool{false, true} {
					for _, strip := range []bool{false, true} {
						t.Run(fmt.Sprintf("%s%s/in=%v/out=%v/strip=%v", provider, path, inboundZstd, outboundZstd, strip), func(t *testing.T) {
							testMetadataCompression(t, provider, path, inboundZstd, outboundZstd, strip, false)
						})
					}
				}
			}
		}
	}
	for _, path := range []string{"/v1/responses", "/v1/responses/compact"} {
		t.Run("rewrite-and-privacy"+path, func(t *testing.T) {
			testMetadataCompression(t, "api_key", path, true, true, true, true)
		})
	}
}

func testMetadataCompression(t *testing.T, provider, path string, inboundZstd, outboundZstd, strip, transforms bool) {
	t.Helper()
	received := make(chan metadataWireRequest, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		received <- metadataWireRequest{body, r.Header.Get("Content-Encoding"), r.ContentLength, r.Header.Get("X-Fixture-Trace")}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"fixture-response","status":"completed","output":[]}`))
	}))
	defer upstream.Close()
	credential := "fixture-credential"
	if provider == "chatgpt_refresh_token" {
		credential = `{"refresh_token":"fixture-refresh","access_token":"fixture-access","expires_at":"2099-01-01T00:00:00Z","chatgpt_account_id":"fixture-account"}`
	}
	account := &store.UpstreamAccountRecord{ID: 1, ProviderType: provider, AccountName: "metadata-fixture", CredentialRaw: credential,
		BaseURL: upstream.URL, Enabled: true, StripCodexInternalMetadata: strip, EnableRequestCompression: outboundZstd}
	if transforms {
		account.ModelRewriteRules = testGPT54ModelRewriteRules
	}
	handler := newAccountPipelineTestHandler(t, upstream.URL, &mockAccountPoolService{accounts: []*store.UpstreamAccountRecord{account}})
	routeMetadataFixtureTransport(t, handler, upstream.URL)
	if transforms {
		filter := newProxyTestPrivacyService(t)
		settings, err := filter.GetSettings(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		settings.Mode = privacy.ModeRedact
		if _, err := filter.UpdateSettings(context.Background(), settings); err != nil {
			t.Fatal(err)
		}
		if _, err := filter.CreateRule(context.Background(), &store.PrivacyRuleRecord{Enabled: true, Name: "fixture-output", Priority: 1,
			MatchType: "regex", Pattern: "fixture output", Placeholder: "redacted-output", Action: "redact",
			ScopeJSON: `{"paths":["/v1/responses","/v1/responses/compact"]}`}); err != nil {
			t.Fatal(err)
		}
		handler.SetPrivacyFilter(filter)
	}
	body := []byte(accountMetadataFixture)
	wire := body
	if inboundZstd {
		wire = mustEncodeZstd(t, body)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(wire))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Fixture-Trace", "fixture-trace")
	if inboundZstd {
		req.Header.Set("Content-Encoding", "zstd")
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	got := <-received
	if got.contentLength != int64(len(got.body)) || got.trace != "fixture-trace" {
		t.Fatal("outbound length or existing header forwarding changed")
	}
	decoded := got.body
	if outboundZstd {
		if got.encoding != "zstd" {
			t.Fatalf("Content-Encoding = %q", got.encoding)
		}
		var err error
		decoded, err = decodeZstdRequestBody(got.body, defaultEncodedRequestBodyMaxBytes)
		if err != nil {
			t.Fatal(err)
		}
	} else if got.encoding != "" {
		t.Fatalf("unexpected Content-Encoding: %s", got.encoding)
	}
	if inboundZstd && outboundZstd && bytes.Equal(got.body, wire) == (strip || transforms) {
		t.Fatal("original zstd frame reuse did not follow the final request bytes")
	}
	if !strip && !transforms && !bytes.Equal(decoded, body) {
		t.Fatal("disabled compatibility changed the normalized request")
	}
	var payload struct {
		Model  string                       `json:"model"`
		Number json.RawMessage              `json:"number"`
		Input  []map[string]json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(decoded, &payload); err != nil {
		t.Fatal(err)
	}
	_, hasMetadata := payload.Input[0][codexInternalMetadataKey]
	if hasMetadata == strip || string(payload.Number) != "9007199254740993" || len(payload.Input) != 2 || string(payload.Input[1]["encrypted_content"]) != `"ciphertext-unchanged"` {
		t.Fatal("metadata filtering changed protocol data or failed to remove the target key")
	}
	if transforms && (payload.Model != "gpt-5.5" || string(payload.Input[0]["output"]) != `"redacted-output"`) {
		t.Fatal("model rewrite or privacy filtering did not run after metadata cleanup")
	}
}

// OAuth 的出站 URL 固定为官方域名；测试只连接本地 fixture，保留真实 HTTP 编解码。
func routeMetadataFixtureTransport(t *testing.T, handler *Handler, fixtureURL string) {
	t.Helper()
	target, err := url.Parse(fixtureURL)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)
	handler.accountHTTPInitOnce.Do(func() {})
	handler.accountHTTPClient = &http.Client{Timeout: 5 * time.Second, Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		local := req.Clone(req.Context())
		local.URL.Scheme = target.Scheme
		local.URL.Host = target.Host
		return transport.RoundTrip(local)
	})}
	handler.accountSSEHTTPClient = handler.accountHTTPClient
}

func TestAccountPipeline_CodexMetadataExactMethodAndPathScope(t *testing.T) {
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/responses"}, {http.MethodPut, "/v1/responses/compact"},
		{http.MethodPost, "/v1/responses/other"}, {http.MethodGet, "/v1/models"},
		{http.MethodPost, "/v1/alpha/search"}, {http.MethodPost, "/v1/images/generations"},
		{http.MethodPost, "/v1/images/edits"}, {http.MethodPost, "/v1/messages"},
		{http.MethodPost, "/v1/messages/count_tokens"},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			received := make(chan []byte, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				received <- body
				w.WriteHeader(http.StatusOK)
			}))
			defer upstream.Close()
			account := &store.UpstreamAccountRecord{ProviderType: "api_key", CredentialRaw: "fixture-credential", BaseURL: upstream.URL, StripCodexInternalMetadata: true}
			handler := newAccountPipelineTestHandler(t, upstream.URL, nil)
			req := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(accountMetadataFixture))
			response, release, _, err := handler.forwardRequestToAccount(req.Context(), req, []byte(accountMetadataFixture), account, false, nil)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if release != nil {
				release()
			}
			if string(<-received) != accountMetadataFixture {
				t.Fatal("metadata filtering escaped its exact POST Responses scope")
			}
		})
	}
}

func TestAccountPipeline_CodexMetadataStreamingAndRepeatedRequests(t *testing.T) {
	for _, provider := range []string{"api_key", "chatgpt_refresh_token"} {
		t.Run(provider, func(t *testing.T) {
			received := make(chan []byte, 2)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				received <- body
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"fixture\",\"status\":\"completed\",\"output\":[]}}\n\n")
			}))
			defer upstream.Close()
			credential := "fixture-credential"
			if provider == "chatgpt_refresh_token" {
				credential = `{"refresh_token":"fixture-refresh","access_token":"fixture-access","expires_at":"2099-01-01T00:00:00Z","chatgpt_account_id":"fixture-account"}`
			}
			account := &store.UpstreamAccountRecord{ID: 1, ProviderType: provider, AccountName: "metadata-stream", CredentialRaw: credential, BaseURL: upstream.URL, Enabled: true, StripCodexInternalMetadata: true}
			handler := newAccountPipelineTestHandler(t, upstream.URL, &mockAccountPoolService{accounts: []*store.UpstreamAccountRecord{account}})
			routeMetadataFixtureTransport(t, handler, upstream.URL)
			body := strings.Replace(accountMetadataFixture, `"model"`, `"stream":true,"model"`, 1)
			var previous []byte
			for attempt := 0; attempt < 2; attempt++ {
				req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Accept", "text/event-stream")
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "response.completed") {
					t.Fatalf("stream failed: status=%d body=%s", rec.Code, rec.Body.String())
				}
				got := <-received
				if bytes.Contains(got, []byte(codexInternalMetadataKey)) || attempt > 0 && !bytes.Equal(got, previous) {
					t.Fatal("streaming or repeated requests did not preserve the metadata transform")
				}
				previous = got
			}
		})
	}
}

func TestAccountPipeline_CodexMetadataLocalErrorFinishesLifecycleAndPreservesPin(t *testing.T) {
	ctx := context.Background()
	svc := newRealAccountPipelineTestService(t, []*store.UpstreamAccountRecord{{
		ProviderType: "api_key", AccountName: "metadata-pinned", CredentialRaw: "fixture-credential",
		BaseURL: "http://127.0.0.1:1", Enabled: true, StripCodexInternalMetadata: true,
	}})
	accounts, err := svc.ListAccounts(ctx, true)
	if err != nil || len(accounts) != 1 {
		t.Fatalf("list fixture accounts: %v", err)
	}
	id := accounts[0].ID
	if _, err := svc.PinAccountSelection(ctx, id); err != nil {
		t.Fatal(err)
	}
	tracker, err := tracking.NewUsageTracker(&tracking.Config{
		Enabled: true, DatabasePath: ":memory:", BufferSize: 16, BatchSize: 8,
		FlushInterval: 10 * time.Millisecond, MaxRetry: 1, CleanupInterval: time.Hour,
		HotPool: &tracking.HotPoolSettings{Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tracker.Close() })
	handler := newAccountPipelineTestHandler(t, "http://127.0.0.1:1", svc)
	handler.SetUsageTracker(tracker)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"fixture","input":{}}`))
	req = req.WithContext(context.WithValue(req.Context(), "conn_id", "metadata-local-error-fixture"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
	snapshot, err := svc.GetLatestAccountScheduleSnapshot(ctx)
	if err != nil || snapshot == nil || snapshot.FinalOutcome != "invalid_request" || snapshot.FinalError != requestBodyCodeCodexMetadataInvalid {
		t.Fatalf("local error schedule snapshot mismatch: %v", err)
	}
	rows, _, err := tracker.QueryRequestDetailsWithHotPool(ctx, &tracking.QueryOptions{RequestID: snapshot.RequestID, Limit: 1})
	if err != nil || len(rows) != 1 {
		t.Fatalf("query local error lifecycle: count=%d err=%v", len(rows), err)
	}
	row := rows[0]
	if row.Status != "failed" || row.EndTime == nil || row.HTTPStatusCode == nil || *row.HTTPStatusCode != http.StatusBadRequest || !strings.HasPrefix(row.FailureReason, requestBodyCodeCodexMetadataInvalid+":") {
		t.Fatalf("local request lifecycle did not finish as an input error: status=%q reason=%q", row.Status, row.FailureReason)
	}
	after, err := svc.GetAccount(ctx, id)
	if err != nil || after.FailCount != 0 || after.State != "active" || after.CooldownUntil != nil {
		t.Fatalf("local error changed account health: %v", err)
	}
	pinnedID, pinned, err := svc.GetActiveSelectionAccountID(ctx)
	if err != nil || !pinned || pinnedID != id {
		t.Fatalf("local error changed pinned account: id=%d pinned=%v err=%v", pinnedID, pinned, err)
	}
}
