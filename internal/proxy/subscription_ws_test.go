package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"testing"

	"cc-forwarder/internal/privacy"
	"cc-forwarder/internal/proxy/handlers"
	"cc-forwarder/internal/proxy/subscriptionws"
)

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
