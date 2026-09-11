package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"cc-forwarder/internal/privacy"
	"cc-forwarder/internal/proxy/handlers"
	"cc-forwarder/internal/proxy/subscriptionws"
	"cc-forwarder/internal/tracking"

	"github.com/coder/websocket"
)

func TestSubscriptionWSHandshakeFailurePreservesCompleteBody(t *testing.T) {
	for _, size := range []int{2053, 96 << 10} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			body := strings.Repeat("x", size)
			env := newSubscriptionTestEnv(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Content-Length", fmt.Sprint(len(body)))
				w.Header().Set("X-Codex-Turn-State", "synthetic-turn")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, body[:1025])
				w.(http.Flusher).Flush()
				// 后半段必须在 Dial 返回后继续读取，不能提前取消握手上下文。
				time.Sleep(10 * time.Millisecond)
				_, _ = io.WriteString(w, body[1025:])
			}, "subscription")
			resp := env.request(http.MethodGet, "responses", func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer synthetic-client-token")
				r.Header.Set("Connection", "Upgrade")
				r.Header.Set("Upgrade", "websocket")
				r.Header.Set("Sec-WebSocket-Version", "13")
				r.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
			})
			defer resp.Body.Close()
			got, err := io.ReadAll(resp.Body)
			if err != nil || resp.StatusCode != http.StatusTooManyRequests || string(got) != body {
				t.Fatalf("status=%d length=%d want=%d err=%v", resp.StatusCode, len(got), len(body), err)
			}
			if resp.ContentLength >= 0 && resp.ContentLength != int64(len(got)) {
				t.Fatalf("Content-Length=%d actual=%d", resp.ContentLength, len(got))
			}
			if resp.Header.Get("X-Codex-Turn-State") != "synthetic-turn" || env.upstreamCount() != 1 {
				t.Fatalf("response header or handshake count changed: header=%v requests=%d", resp.Header, env.upstreamCount())
			}
		})
	}
}

type subscriptionWSObservedBody struct {
	io.ReadCloser
	closed chan struct{}
}

func (b *subscriptionWSObservedBody) Close() error {
	err := b.ReadCloser.Close()
	select {
	case b.closed <- struct{}{}:
	default:
	}
	return err
}

func TestSubscriptionWSHandshakeFailureReadErrorsAbortAndReleaseBody(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(fmt.Sprintf("timeout=%t", timeout), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", "8192")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, strings.Repeat("x", 4096))
				w.(http.Flusher).Flush()
				if timeout {
					<-r.Context().Done()
				}
			}))
			defer upstream.Close()
			closed := make(chan struct{}, 1)
			client := upstream.Client()
			base := client.Transport
			client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				resp, err := base.RoundTrip(r)
				if resp != nil {
					resp.Body = &subscriptionWSObservedBody{ReadCloser: resp.Body, closed: closed}
				}
				return resp, err
			})
			proxy := &subscriptionWSProxy{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx, cancel := context.WithTimeout(r.Context(), 200*time.Millisecond)
				defer cancel()
				_, resp, err := dialSubscriptionWebSocket(ctx, upstream.URL, nil, client, subscriptionws.Options{})
				proxy.relayHandshakeFailure(w, resp, "synthetic-incomplete", err)
			}))
			defer server.Close()
			downstream := server.Client()
			downstream.Timeout = 2 * time.Second
			resp, err := downstream.Get(server.URL)
			if err == nil {
				_, err = io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusTooManyRequests {
					t.Fatalf("upstream rejection status changed: %d", resp.StatusCode)
				}
			}
			if err == nil {
				t.Fatal("incomplete response appeared complete")
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("upstream response body was not released")
			}
		})
	}
}

type p2aPrivacyFilter struct {
	mu       sync.Mutex
	version  int64
	calls    int
	requests []privacy.Request
	result   privacy.ApplyResult
	err      error
}

func (f *p2aPrivacyFilter) Apply(_ context.Context, req privacy.Request, body []byte) (privacy.ApplyResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.requests = append(f.requests, req)
	result := f.result
	if result.Body == nil {
		result.Body = append([]byte(nil), body...)
	}
	return result, f.err
}

func (f *p2aPrivacyFilter) SnapshotVersion() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.version
}

func (f *p2aPrivacyFilter) setVersion(version int64, body []byte) {
	f.mu.Lock()
	f.version = version
	f.result.Body = append([]byte(nil), body...)
	f.mu.Unlock()
}

func (f *p2aPrivacyFilter) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestSubscriptionPrivacyDefaultFingerprintRescansAfterSnapshotUpdate(t *testing.T) {
	filter := &p2aPrivacyFilter{version: 1}
	filter.setVersion(1, []byte(`{"input":"redacted-v1"}`))
	state := newSubscriptionPrivacyRequestState("req-hot", nil)
	req, _ := http.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBuffer(nil))
	req.Header.Set("Content-Type", "application/json")
	req = withSubscriptionPrivacyState(req, state)
	body := []byte(`{"input":"SYNTHETIC-SECRET"}`)

	first, err := applySubscriptionPrivacy(filter, req, "/v1/responses", body)
	if err != nil || !bytes.Contains(first, []byte("redacted-v1")) {
		t.Fatalf("first apply failed: body=%s err=%v", first, err)
	}
	filter.setVersion(2, []byte(`{"input":"redacted-v2"}`))
	second, err := applySubscriptionPrivacy(filter, req, "/v1/responses", body)
	if err != nil || !bytes.Contains(second, []byte("redacted-v2")) {
		t.Fatalf("updated apply failed: body=%s err=%v", second, err)
	}
	if filter.callCount() != 2 {
		t.Fatalf("snapshot update reused stale attempt cache: calls=%d", filter.callCount())
	}
}

func TestSubscriptionPrivacyObserverReceivesNoBodyOrMatches(t *testing.T) {
	filter := &p2aPrivacyFilter{
		version: 1,
		result: privacy.ApplyResult{
			Action:   privacy.ModeRedact,
			RuleHits: []privacy.RuleHit{{RuleName: "fixture", Count: 1, Matches: []string{"SYNTHETIC-SECRET"}}},
		},
	}
	var observed privacy.ApplyResult
	state := newSubscriptionPrivacyRequestState("req-summary", func(_ string, result privacy.ApplyResult, _ *privacy.PolicyError) {
		observed = result
	})
	req, _ := http.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set("Content-Type", "application/json")
	req = withSubscriptionPrivacyState(req, state)
	body := []byte(`{"input":"SYNTHETIC-SECRET"}`)
	forward, err := applySubscriptionPrivacy(filter, req, "/v1/responses", body)
	if err != nil || !bytes.Equal(forward, body) {
		t.Fatalf("forward body changed unexpectedly: %s err=%v", forward, err)
	}
	if observed.Body != nil || len(observed.RuleHits) != 1 || observed.RuleHits[0].Matches != nil {
		t.Fatalf("observer received sensitive fields: %+v", observed)
	}
}

func TestSubscriptionPrivacyValidatesDuplicateKeysWhenDisabled(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "/v1/responses", nil)
	for _, body := range [][]byte{
		[]byte(`{"input":"one","input":"two"}`),
		[]byte(`{"type":"response.create","generate":true,"generate":false,"input":[]}`),
	} {
		out, err := applySubscriptionPrivacy(nil, req, "/v1/responses", body)
		if out != nil || handlers.AsPrivacyPolicyError(err) == nil {
			t.Fatalf("duplicate JSON must fail even without a filter: out=%s err=%v", out, err)
		}
	}
}

func TestSubscriptionPrivacyUsesSubscriptionScope(t *testing.T) {
	filter := &p2aPrivacyFilter{version: 1}
	req, _ := http.NewRequest(http.MethodPost, "/v1/alpha/search", nil)
	req.Header.Set("Content-Type", "application/json")
	if _, err := applySubscriptionPrivacy(filter, req, "/v1/alpha/search", []byte(`{"input":"query"}`)); err != nil {
		t.Fatalf("apply search privacy: %v", err)
	}
	filter.mu.Lock()
	defer filter.mu.Unlock()
	if len(filter.requests) != 1 || filter.requests[0].UpstreamType != privacy.UpstreamTypeSubscription || filter.requests[0].AccountID != 0 {
		t.Fatalf("unexpected subscription scope: %+v", filter.requests)
	}
}

func TestSubscriptionPrivacyRejectsNonObjectBodies(t *testing.T) {
	for _, body := range []string{`null`, `[]`, `"text"`, `42`} {
		out, err := applySubscriptionPrivacy(nil, nil, "/v1/responses", []byte(body))
		if out != nil || handlers.AsPrivacyPolicyError(err) == nil {
			t.Fatalf("non-object body accepted: %s", body)
		}
	}
}

func TestSubscriptionPrivacyRevalidatesNestedJSON(t *testing.T) {
	original := []byte(`{"input":[{"type":"function_call","arguments":"{\"value\":\"SECRET\"}"}],"max_output_tokens":18446744073709551615}`)
	for _, tc := range []struct {
		name     string
		argument string
		valid    bool
	}{
		{"escaped replacement", `{"value":"a\"b"}`, true},
		{"broken replacement", `{"value":"a"b"}`, false},
		{"ambiguous replacement", `{"value":"one","value":"two"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			quoted, err := json.Marshal(tc.argument)
			if err != nil {
				t.Fatal(err)
			}
			filtered := []byte(`{"input":[{"type":"function_call","arguments":` + string(quoted) + `}],"max_output_tokens":18446744073709551615}`)
			filter := &p2aPrivacyFilter{version: 1, result: privacy.ApplyResult{Body: filtered, Changed: true, Action: privacy.ModeRedact}}
			out, err := applySubscriptionPrivacy(filter, nil, "/v1/responses", original)
			if tc.valid {
				if err != nil || !bytes.Equal(out, filtered) {
					t.Fatalf("valid replacement changed: %s, %v", out, err)
				}
			} else if out != nil || handlers.AsPrivacyPolicyError(err) == nil {
				t.Fatalf("invalid replacement escaped: %s, %v", out, err)
			}
		})
	}
}

func TestSubscriptionPrivacyAllowsWholePlainTextReplacement(t *testing.T) {
	original := []byte(`{"input":"{\"value\":\"SECRET\"}"}`)
	filtered := []byte(`{"input":"[masked]"}`)
	filter := &p2aPrivacyFilter{version: 1, result: privacy.ApplyResult{Body: filtered, Changed: true, Action: privacy.ModeRedact}}
	out, err := applySubscriptionPrivacy(filter, nil, "/v1/responses", original)
	if err != nil || !bytes.Equal(out, filtered) {
		t.Fatalf("plain message was treated as tool JSON: out=%s err=%v", out, err)
	}
}

func TestSubscriptionWSInterceptorPolicyErrorZeroForwardAndSubmissionClasses(t *testing.T) {
	policyErr := &privacy.PolicyError{StatusCode: 422, Code: privacy.CodeScanFailed, Message: "internal detail must not escape"}
	filter := &p2aPrivacyFilter{version: 1, err: policyErr}
	observer := &recordingSubmissionObserver{}
	state := newSubscriptionSubmissionState(context.Background(), "ws-policy", observer)
	interceptor := &subscriptionWSInterceptor{
		filter: filter, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), submissions: state,
	}

	generated, err := interceptor.Intercept(context.Background(), subscriptionws.DirectionDownstreamToUpstream,
		[]byte(`{"type":"response.create","generate":true,"input":"SYNTHETIC-SECRET"}`))
	if err != nil || generated.Forward != nil || !bytes.Contains(generated.Reply, []byte(`"type":"error"`)) {
		t.Fatalf("generation rejection mismatch: %+v err=%v", generated, err)
	}
	warmup, err := interceptor.Intercept(context.Background(), subscriptionws.DirectionDownstreamToUpstream,
		[]byte(`{"type":"response.create","generate":false,"input":[]}`))
	if err != nil || warmup.Forward != nil || warmup.Reply == nil {
		t.Fatalf("warmup rejection mismatch: %+v err=%v", warmup, err)
	}
	unknown, err := interceptor.Intercept(context.Background(), subscriptionws.DirectionDownstreamToUpstream,
		[]byte(`{"type":"response.cancel","response_id":"r1"}`))
	if err != nil || unknown.Forward != nil || unknown.Reply == nil {
		t.Fatalf("unknown rejection mismatch: %+v err=%v", unknown, err)
	}
	if observer.count("started") != 2 || observer.count("rejected") != 2 {
		t.Fatalf("classification mismatch: started=%d rejected=%d", observer.count("started"), observer.count("rejected"))
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	var started []submissionEvent
	for _, event := range observer.events {
		if event.kind == "started" {
			started = append(started, event)
		}
	}
	if len(started) != 2 || !started[0].generate || started[1].generate {
		t.Fatalf("generate classification lost: %+v", started)
	}
}

func TestSubscriptionWSInterceptorUnknownSuccessIsCompatibilityError(t *testing.T) {
	state := newSubscriptionSubmissionState(context.Background(), "ws-unknown", &recordingSubmissionObserver{})
	interceptor := &subscriptionWSInterceptor{
		filter: &p2aPrivacyFilter{version: 1}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), submissions: state,
	}
	result, err := interceptor.Intercept(context.Background(), subscriptionws.DirectionDownstreamToUpstream,
		[]byte(`{"type":"response.cancel","response_id":"r1"}`))
	if err != nil || result.Forward != nil || !bytes.Contains(result.Reply, []byte(subscriptionUnsupportedWSEventCode)) {
		t.Fatalf("unknown client event must not pass: %+v err=%v", result, err)
	}
}

func TestSubscriptionWSInterceptorDuplicateGenerateDoesNotStartSubmission(t *testing.T) {
	observer := &recordingSubmissionObserver{}
	state := newSubscriptionSubmissionState(context.Background(), "ws-duplicate", observer)
	interceptor := &subscriptionWSInterceptor{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), submissions: state,
	}
	result, err := interceptor.Intercept(context.Background(), subscriptionws.DirectionDownstreamToUpstream,
		[]byte(`{"type":"response.create","generate":true,"generate":false,"input":[]}`))
	if err != nil || result.Forward != nil || result.Reply == nil {
		t.Fatalf("duplicate generate rejection mismatch: %+v err=%v", result, err)
	}
	if observer.count("started") != 0 || len(state.active) != 0 {
		t.Fatalf("ambiguous create started lifecycle: starts=%d active=%d", observer.count("started"), len(state.active))
	}
}

func TestSubscriptionWSInterceptorUsesIndependentPrivacyStatePerCreate(t *testing.T) {
	filter := &p2aPrivacyFilter{version: 1}
	state := newSubscriptionSubmissionState(context.Background(), "ws-cache", nil)
	interceptor := &subscriptionWSInterceptor{
		filter: filter, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), submissions: state,
	}
	payload := []byte(`{"type":"response.create","input":"same body"}`)
	for n := 0; n < 2; n++ {
		result, err := interceptor.Intercept(context.Background(), subscriptionws.DirectionDownstreamToUpstream, payload)
		if err != nil || result.Forward == nil {
			t.Fatalf("create %d failed: %+v err=%v", n, result, err)
		}
	}
	if filter.callCount() != 2 {
		t.Fatalf("different submissions shared privacy cache: calls=%d", filter.callCount())
	}
}

func TestSubscriptionHTTPPolicyErrorHasZeroUpstreamRequests(t *testing.T) {
	env := newSubscriptionTestEnv(t, nil, "subscription")
	env.handler.proxy.SetPrivacyFilter(&p2aPrivacyFilter{
		version: 1,
		err: &privacy.PolicyError{
			StatusCode: http.StatusUnprocessableEntity,
			Code:       privacy.CodeScanFailed,
			Message:    "synthetic internal parser detail",
		},
	})
	resp := env.request(http.MethodPost, "responses", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer synthetic-client-token")
		r.Header.Set("Content-Type", "application/json")
		r.Body = io.NopCloser(bytes.NewBufferString(`{"model":"m","input":"SYNTHETIC-SECRET"}`))
		r.ContentLength = int64(len(`{"model":"m","input":"SYNTHETIC-SECRET"}`))
	})
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusUnprocessableEntity || env.upstreamCount() != 0 {
		t.Fatalf("blocked HTTP request escaped: status=%d upstream=%d body=%s", resp.StatusCode, env.upstreamCount(), body)
	}
	if bytes.Contains(body, []byte("synthetic internal parser detail")) || bytes.Contains(body, []byte("SYNTHETIC-SECRET")) {
		t.Fatalf("policy response leaked internal detail: %s", body)
	}
}

func TestSubscriptionHTTPPrivacyRunsBeforeZstdOutbound(t *testing.T) {
	env := newSubscriptionTestEnv(t, nil, "subscription")
	filtered := []byte(`{"model":"m","input":"[redacted]"}`)
	env.handler.proxy.SetPrivacyFilter(&p2aPrivacyFilter{
		version: 1,
		result:  privacy.ApplyResult{Body: filtered, Action: privacy.ModeRedact, Changed: true},
	})
	original := []byte(`{"model":"m","input":"SYNTHETIC-SECRET"}`)
	compressed := zstdCompress(t, original)
	resp := env.request(http.MethodPost, "responses", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer synthetic-client-token")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Content-Encoding", "zstd")
		r.Body = io.NopCloser(bytes.NewReader(compressed))
		r.ContentLength = int64(len(compressed))
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP response status=%d", resp.StatusCode)
	}
	requests := env.upstreamRequests()
	if len(requests) != 1 || requests[0].Header.Get("Content-Encoding") != "zstd" {
		t.Fatalf("unexpected outbound request: %+v", requests)
	}
	plain := zstdDecompress(t, requests[0].Body)
	if !bytes.Equal(plain, filtered) || bytes.Contains(plain, []byte("SYNTHETIC-SECRET")) {
		t.Fatalf("privacy/compression order mismatch: %s", plain)
	}
}

func TestSubscriptionHTTPCaptureChunkingAndTerminal(t *testing.T) {
	for _, body := range []string{
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"usage\":{\"input_tokens\":3,\"output_tokens\":2}}}\n\n",
		"data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":3,\"output_tokens\":2}}}",
		`{"object":"response","status":"completed","usage":{"input_tokens":3,"output_tokens":2}}`,
		`{"object":"response.compaction","output":[],"usage":{"input_tokens":3,"output_tokens":2}}`,
	} {
		var terminal *ParseResult
		capture := &subscriptionResponseCapture{httpStatus: 200, compaction: true, onResult: func(result *ParseResult) {
			if result != nil && result.IsCompleted {
				terminal = result
			}
		}}
		for index := range len(body) {
			if n, err := capture.Write([]byte(body[index : index+1])); n != 1 || err != nil {
				t.Fatal(n, err)
			}
		}
		capture.Finish()
		if terminal == nil || !terminal.HasTokenUsage || terminal.TokenUsage.InputTokens != 3 || terminal.TokenUsage.OutputTokens != 2 {
			t.Fatalf("terminal/usage lost for %q: %+v", body, terminal)
		}
		if capture.pending != nil || capture.parser != nil {
			t.Fatal("capture retained response")
		}
	}
}

func TestSubscriptionHTTPCaptureDoesNotInventTerminal(t *testing.T) {
	for _, body := range []string{`{}`, `{"status":"in_progress"}`, `{"type":"future.event","usage":{"input_tokens":3}}`, "not JSON"} {
		terminal := false
		capture := &subscriptionResponseCapture{httpStatus: 200, onResult: func(result *ParseResult) {
			terminal = terminal || result != nil && result.IsCompleted
		}}
		_, _ = capture.Write([]byte(body))
		capture.Finish()
		if terminal {
			t.Fatalf("invented terminal for %q", body)
		}
	}
}

func TestSubscriptionHTTPCaptureOversizeEventRecoversAtBoundary(t *testing.T) {
	var terminal *ParseResult
	warnings := 0
	capture := &subscriptionResponseCapture{onWarning: func(string) { warnings++ }, onResult: func(result *ParseResult) {
		if result != nil && result.IsCompleted {
			terminal = result
		}
	}}
	_, _ = capture.Write([]byte("event: response.output_text.delta\ndata: "))
	chunk := bytes.Repeat([]byte("x"), 65536)
	for range subscriptionCaptureLimit/len(chunk) + 1 {
		_, _ = capture.Write(chunk)
	}
	_, _ = capture.Write([]byte("\n\nevent: response.completed\ndata: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n"))
	capture.Finish()
	if warnings != 1 || terminal == nil || !terminal.HasTokenUsage {
		t.Fatalf("capture did not recover: warnings=%d terminal=%+v", warnings, terminal)
	}
}

func TestSubscriptionHTTPTrackingEntryIntegration(t *testing.T) {
	for _, tc := range []struct {
		name, operation, output, kind string
		blocked                       bool
		upstreamError                 bool
	}{
		{"generation", "responses", "event: response.completed\ndata: {\"response\":{\"id\":\"r\",\"model\":\"synthetic-model\",\"usage\":{\"input_tokens\":3,\"output_tokens\":2}}}\n\n", "generation", false, false},
		{"compact", "responses/compact", `{"object":"response.compaction","output":[],"usage":{"input_tokens":3,"output_tokens":2}}`, "compaction", false, false},
		{"unknown usage", "responses", `{"status":"completed","output":[]}`, "generation", false, false},
		{"search", "alpha/search", `{"ok":true}`, "", false, false},
		{"privacy rejection", "responses", `{"ok":true}`, "generation", true, false},
		{"upstream error", "responses", `{"error":{"type":"SYNTHETIC-SECRET","code":"SYNTHETIC-SECRET","message":"SYNTHETIC-SECRET"}}`, "generation", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tracker := newLifecyclePanelTestTracker(t)
			env := newSubscriptionTestEnv(t, func(w http.ResponseWriter, _ *http.Request) {
				if tc.upstreamError {
					w.WriteHeader(http.StatusBadRequest)
				}
				_, _ = io.WriteString(w, tc.output)
			}, "subscription")
			env.handler.proxy.SetUsageTracker(tracker)
			if tc.blocked {
				env.handler.proxy.SetPrivacyFilter(&p2aPrivacyFilter{version: 1, err: &privacy.PolicyError{StatusCode: 422, Code: privacy.CodeScanFailed, Message: "SYNTHETIC-SECRET"}})
			}
			resp := env.request(http.MethodPost, tc.operation, func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer synthetic-client-token")
				r.Header.Set("Content-Type", "application/json")
				r.Body = io.NopCloser(strings.NewReader(`{"model":"synthetic-model","input":"SYNTHETIC-SECRET"}`))
			})
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if err := tracker.ForceFlush(); err != nil {
				t.Fatal(err)
			}
			rows, _, err := tracker.QueryRequestDetailsWithHotPool(context.Background(), &tracking.QueryOptions{Limit: 10})
			// Content-Length 响应可能先被客户端读完，服务端随后才解析 JSON 终态。
			// 持续查询直到结算可见，也覆盖列表读取与结算的真实并发。
			for deadline := time.Now().Add(time.Second); tc.kind != "" && err == nil && time.Now().Before(deadline); {
				if len(rows) == 1 && (rows[0].Status == "completed" || rows[0].Status == "failed" || rows[0].Status == "cancelled") {
					break
				}
				time.Sleep(time.Millisecond)
				rows, _, err = tracker.QueryRequestDetailsWithHotPool(context.Background(), &tracking.QueryOptions{Limit: 10})
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.kind == "" {
				if len(rows) != 0 {
					t.Fatalf("auxiliary route created rows: %+v", rows)
				}
				return
			}
			if len(rows) != 1 {
				t.Fatalf("want one row, got %+v", rows)
			}
			row := rows[0]
			if row.RequestKind != tc.kind || row.RequestSource != "codex_subscription_proxy" || row.RequestFamily != "codex" || row.UpstreamType != "subscription" || row.UpstreamName != "chatgpt-subscription" || row.UpstreamID != 0 {
				t.Fatalf("incorrect attribution: %+v", row)
			}
			if row.HTTPStatusCode == nil || *row.HTTPStatusCode != resp.StatusCode {
				t.Fatalf("HTTP status lost: %+v", row)
			}
			if tc.blocked {
				if env.upstreamCount() != 0 || row.Status != "failed" {
					t.Fatalf("blocked request escaped: %+v", row)
				}
			} else if tc.upstreamError {
				if row.Status != "failed" {
					t.Fatalf("upstream error not recorded: %+v", row)
				}
			} else if row.Status != "completed" || (tc.name != "unknown usage" && (row.InputTokens != 3 || row.OutputTokens != 2)) {
				t.Fatalf("terminal/usage lost: %+v", row)
			}
			wantKnown := !tc.blocked && !tc.upstreamError && tc.name != "unknown usage"
			if row.UsageKnown != wantKnown || row.CostKnown != wantKnown {
				t.Fatalf("unknown usage/cost collapsed into zero: %+v", row)
			}
			encoded, err := json.Marshal(row)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(encoded, []byte("SYNTHETIC-SECRET")) || strings.Contains(env.logs.String(), "SYNTHETIC-SECRET") {
				t.Fatal("sensitive value reached records/logs")
			}
		})
	}
}

func TestSubscriptionWSTrackingEntryWarmupAndGeneration(t *testing.T) {
	tracker := newLifecyclePanelTestTracker(t)
	env := newSubscriptionTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			_, payload, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			var create struct {
				Generate *bool `json:"generate"`
			}
			if json.Unmarshal(payload, &create) != nil {
				return
			}
			id, usage := "generated", `,"usage":{"input_tokens":3,"output_tokens":2}`
			if create.Generate != nil && !*create.Generate {
				id, usage = "warmup", ""
			}
			if conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.created","response":{"id":"`+id+`","model":"synthetic-model"}}`)) != nil {
				return
			}
			if conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.completed","response":{"id":"`+id+`"`+usage+`}}`)) != nil {
				return
			}
		}
	}, "subscription")
	env.handler.proxy.SetUsageTracker(tracker)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(env.server.URL, "http")+"/subscription/"+subscriptionTestSecret+"/backend-api/codex/responses", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": []string{"Bearer synthetic-client-token"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	for _, payload := range []string{`{"type":"response.create","generate":false,"input":[]}`, `{"type":"response.create","model":"synthetic-model","input":[]}`} {
		if err := conn.Write(ctx, websocket.MessageText, []byte(payload)); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if _, _, err := conn.Read(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	_ = conn.Close(websocket.StatusNormalClosure, "done")
	if err := tracker.ForceFlush(); err != nil {
		t.Fatal(err)
	}
	rows, _, err := tracker.QueryRequestDetailsWithHotPool(context.Background(), &tracking.QueryOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("warmup recorded or generation duplicated: %+v", rows)
	}
	row := rows[0]
	if row.HTTPStatusCode != nil || row.Status != "completed" || row.InputTokens != 3 || row.RequestKind != "generation" || row.UpstreamType != "subscription" {
		t.Fatalf("incorrect WS record: %+v", row)
	}
}

type subscriptionSnapshotFilter struct{ snapshot *privacy.Snapshot }

func (f subscriptionSnapshotFilter) Apply(_ context.Context, req privacy.Request, body []byte) (privacy.ApplyResult, error) {
	return f.snapshot.Apply(req, body)
}
func (f subscriptionSnapshotFilter) SnapshotVersion() int64 { return f.snapshot.Version }

func TestSubscriptionSharedPrivacyModeMatrix(t *testing.T) {
	rules, err := privacy.CompileRules([]privacy.Rule{{ID: 1, Enabled: true, Name: "synthetic", MatchType: privacy.MatchTypeRegex,
		Pattern: "SYNTHETIC-SECRET", Placeholder: "[masked]", Action: privacy.ActionRedact,
		Scope: privacy.Scope{UpstreamTypes: []string{privacy.UpstreamTypeSubscription}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []struct{ name, path, body string }{
		{"responses", "/v1/responses", `{"input":"SYNTHETIC-SECRET","encrypted_content":"opaque-data"}`},
		{"compact", "/v1/responses/compact", `{"input":[{"type":"function_call_output","output":"SYNTHETIC-SECRET"}],"encrypted_content":"opaque-data"}`},
		{"search", "/v1/alpha/search", `{"commands":{"search_query":[{"q":"SYNTHETIC-SECRET"}]},"encrypted_content":"opaque-data"}`},
		{"warmup", "/v1/responses", `{"type":"response.create","generate":false,"input":"SYNTHETIC-SECRET","encrypted_content":"opaque-data"}`},
	} {
		for _, mode := range []string{"disabled", "detect", "redact", "no_rules", "scan_prefix", "fail_closed"} {
			t.Run(request.name+"/"+mode, func(t *testing.T) {
				settings := privacy.DefaultSettings()
				settings.Mode = mode
				activeRules := rules
				switch mode {
				case "no_rules":
					settings.Mode, activeRules = privacy.ModeRedact, nil
				case "scan_prefix":
					settings.Mode, settings.ScanMaxBytes = privacy.ModeRedact, 1
				case "fail_closed":
					settings.Mode, settings.ScanMaxBytes, settings.OverLimitAction = privacy.ModeRedact, 1, privacy.OverLimitFailClosed
				}
				filter := subscriptionSnapshotFilter{snapshot: &privacy.Snapshot{Version: 1, Settings: settings, Rules: activeRules}}
				var summary privacy.ApplyResult
				state := newSubscriptionPrivacyRequestState("synthetic-matrix", func(_ string, result privacy.ApplyResult, _ *privacy.PolicyError) { summary = result })
				req, _ := http.NewRequest(http.MethodPost, request.path, nil)
				req.Header.Set("Content-Type", "application/json")
				req = withSubscriptionPrivacyState(req, state)
				out, err := applySubscriptionPrivacy(filter, req, request.path, []byte(request.body))
				if mode == "fail_closed" {
					if out != nil || handlers.AsPrivacyPolicyError(err) == nil {
						t.Fatal("fail-closed request escaped")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if mode == "redact" {
					if bytes.Contains(out, []byte("SYNTHETIC-SECRET")) || !bytes.Contains(out, []byte("[masked]")) {
						t.Fatalf("redaction missing: %s", out)
					}
				} else if string(out) != request.body {
					t.Fatalf("passthrough mode altered body: %s", out)
				}
				if !bytes.Contains(out, []byte(`"encrypted_content":"opaque-data"`)) {
					t.Fatal("opaque field changed")
				}
				if mode == "scan_prefix" && !summary.Truncated {
					t.Fatal("partial scan not disclosed")
				}
				if mode == "detect" && summary.HitCount == 0 {
					t.Fatal("detect did not report hit")
				}
				if summary.Body != nil {
					t.Fatal("summary retained body")
				}
				for _, hit := range summary.RuleHits {
					if len(hit.Matches) != 0 {
						t.Fatal("summary retained sensitive matches")
					}
				}
			})
		}
	}
}
