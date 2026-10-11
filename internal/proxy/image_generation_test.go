package proxy

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cc-forwarder/config"
	"cc-forwarder/internal/endpoint"
	"cc-forwarder/internal/middleware"
	"cc-forwarder/internal/tracking"
)

type staticImageGenerationConfigProvider struct {
	config ImageGenerationConfig
	err    error
}

func (p staticImageGenerationConfigProvider) GetImageGenerationConfig(context.Context) (ImageGenerationConfig, error) {
	return p.config, p.err
}

func TestImageGeneration_ForwardsConfiguredProviderAndTracksRequest(t *testing.T) {
	var receivedAuth string
	var receivedPath string
	var receivedBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		receivedPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&receivedBody); err != nil {
			t.Fatalf("decode upstream request failed: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1,"data":[{"b64_json":"aW1hZ2U="}]}`))
	}))
	defer upstream.Close()

	tracker := newImageGenerationTestTracker(t)
	defer tracker.Close()
	endpointManager := endpoint.NewManager(&config.Config{})
	handler := NewHandler(endpointManager, &config.Config{})
	handler.SetMonitoringMiddleware(middleware.NewMonitoringMiddleware(endpointManager))
	handler.SetUsageTracker(tracker)
	handler.imageDirectHTTPClientFactory = func(int, int) (*http.Client, func(), error) {
		t.Fatal("default image generation route must not use direct client")
		return nil, nil, nil
	}
	handler.SetImageGenerationConfigProvider(staticImageGenerationConfigProvider{config: ImageGenerationConfig{
		Enabled:       true,
		EndpointURL:   upstream.URL + openAIImagesGenerationsPath,
		APIKey:        "secret-key",
		Model:         "gpt-image-2",
		FixedPriceUSD: 0.25,
		Timeout:       5 * time.Second,
	}})

	req := httptest.NewRequest(http.MethodPost, openAIImagesGenerationsPath, strings.NewReader(`{"prompt":"draw a test icon","quality":"high"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), "conn_id", "req-image-generation"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if receivedPath != openAIImagesGenerationsPath {
		t.Fatalf("expected upstream path %s, got %s", openAIImagesGenerationsPath, receivedPath)
	}
	if receivedAuth != "Bearer secret-key" {
		t.Fatalf("unexpected upstream authorization header: %q", receivedAuth)
	}
	if receivedBody["model"] != "gpt-image-2" || receivedBody["prompt"] != "draw a test icon" {
		t.Fatalf("unexpected upstream body: %+v", receivedBody)
	}

	start := time.Now().Add(-time.Minute)
	end := time.Now().Add(time.Minute)
	details, _, err := tracker.QueryRequestDetailsWithHotPool(context.Background(), &tracking.QueryOptions{
		StartDate: &start,
		EndDate:   &end,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("query tracked image request failed: %v", err)
	}
	for _, detail := range details {
		if detail.RequestID != "req-image-generation" {
			continue
		}
		if detail.Status != "completed" || detail.Path != openAIImagesGenerationsPath {
			t.Fatalf("unexpected tracked status/path: %+v", detail)
		}
		if detail.RequestFamily != tracking.RequestFamilyImage || detail.UpstreamSourceName != "image_generation" {
			t.Fatalf("unexpected tracked image source: %+v", detail)
		}
		if detail.ModelName != "gpt-image-2" {
			t.Fatalf("unexpected tracked model: %q", detail.ModelName)
		}
		if detail.TotalCostUSD != 0.25 {
			t.Fatalf("expected tracked image cost 0.25, got %f", detail.TotalCostUSD)
		}
		// 非流响应读完即首响，生成耗时记 0，与端点 / 账号池非流链路一致
		if detail.FirstTokenMs == nil || detail.CompletionMs == nil || *detail.CompletionMs != 0 {
			t.Fatalf("expected non-stream image first response and zero completion, got first=%v completion=%v", detail.FirstTokenMs, detail.CompletionMs)
		}
		lifecycleData, err := tracker.GetRequestLifecycleData(context.Background(), detail.RequestID)
		if err != nil {
			t.Fatalf("query image lifecycle data failed: %v", err)
		}
		if lifecycleData == nil || lifecycleData.UpstreamWriteMs == nil {
			t.Fatalf("expected image request upstream_write_ms, got %+v", lifecycleData)
		}
		return
	}
	t.Fatal("expected image generation request in request tracking")
}

func TestImageGeneration_DirectConnectUsesDedicatedClient(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1,"data":[{"b64_json":"aW1hZ2U="}]}`))
	}))
	defer upstream.Close()

	directClientUsed := false
	directPortMin := 0
	directPortMax := 0
	handler := NewHandler(endpoint.NewManager(&config.Config{}), &config.Config{})
	handler.imageDirectHTTPClientFactory = func(startPort, endPort int) (*http.Client, func(), error) {
		directClientUsed = true
		directPortMin = startPort
		directPortMax = endPort
		return upstream.Client(), func() {}, nil
	}
	handler.SetImageGenerationConfigProvider(staticImageGenerationConfigProvider{config: ImageGenerationConfig{
		Enabled:       true,
		DirectConnect: true,
		DirectPortMin: 31080,
		DirectPortMax: 31179,
		EndpointURL:   upstream.URL + openAIImagesGenerationsPath,
		APIKey:        "secret-key",
		Model:         "gpt-image-2",
		Timeout:       5 * time.Second,
	}})

	req := httptest.NewRequest(http.MethodPost, openAIImagesGenerationsPath, strings.NewReader(`{"prompt":"direct test"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !directClientUsed {
		t.Fatal("expected dedicated direct image client to be used")
	}
	if directPortMin != 31080 || directPortMax != 31179 {
		t.Fatalf("unexpected direct source port range: %d-%d", directPortMin, directPortMax)
	}
}

func TestImageGeneration_ForcesIdentityAcceptEncodingUpstream(t *testing.T) {
	var receivedAcceptEncoding string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAcceptEncoding = r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"created":1,"data":[{"b64_json":"aW1hZ2U="}]}`)
	}))
	defer upstream.Close()

	handler := newTrackedImageGenerationHandler(t, nil, upstream.URL, 0)
	req := httptest.NewRequest(http.MethodPost, openAIImagesGenerationsPath, strings.NewReader(`{"prompt":"draw a test icon"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if receivedAcceptEncoding != "identity" {
		t.Fatalf("expected upstream Accept-Encoding identity, got %q", receivedAcceptEncoding)
	}
}

func TestImageGeneration_RejectsUnexpectedCompressedSuccess(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		compressed := gzip.NewWriter(w)
		_, _ = io.WriteString(compressed, `{"created":1,"data":[{"b64_json":"aW1hZ2U="}]}`)
		_ = compressed.Close()
	}))
	defer upstream.Close()

	tracker := newImageGenerationTestTracker(t)
	defer tracker.Close()
	handler := newTrackedImageGenerationHandler(t, tracker, upstream.URL, 0.25)
	req := httptest.NewRequest(http.MethodPost, openAIImagesGenerationsPath, strings.NewReader(`{"prompt":"draw a test icon"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), "conn_id", "req-image-compressed"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "image_api_invalid_upstream_response") {
		t.Fatalf("expected compressed response rejection, status=%d body=%s", rec.Code, rec.Body.String())
	}
	detail := findTrackedImageRequest(t, tracker, "req-image-compressed")
	if detail.Status != "failed" || detail.TotalCostUSD != 0 {
		t.Fatalf("compressed response must fail without charge, got %+v", detail)
	}
}

func TestImageGeneration_RejectsHTTP200HTMLWithoutCompletingOrCharging(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<!doctype html><html><body>upstream gateway page</body></html>")
	}))
	defer upstream.Close()

	tracker := newImageGenerationTestTracker(t)
	defer tracker.Close()
	handler := newTrackedImageGenerationHandler(t, tracker, upstream.URL, 0.25)
	req := httptest.NewRequest(http.MethodPost, openAIImagesGenerationsPath, strings.NewReader(`{"prompt":"draw a test icon"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), "conn_id", "req-image-html"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d body=%s", rec.Code, rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("expected JSON error Content-Type, got %q", contentType)
	}
	if strings.Contains(strings.ToLower(rec.Body.String()), "<html") || !strings.Contains(rec.Body.String(), "image_api_invalid_upstream_response") {
		t.Fatalf("HTML must not be forwarded, body=%s", rec.Body.String())
	}
	detail := findTrackedImageRequest(t, tracker, "req-image-html")
	if detail.Status != "failed" || detail.HTTPStatusCode == nil || *detail.HTTPStatusCode != http.StatusBadGateway {
		t.Fatalf("expected tracked 502 failure, got %+v", detail)
	}
	if detail.TotalCostUSD != 0 || !strings.HasPrefix(detail.FailureReason, "image_api_invalid_upstream_response") {
		t.Fatalf("invalid HTML response must not be charged, got %+v", detail)
	}
}

func TestImageGeneration_RejectsHTTP200JSONErrorEnvelope(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"error":{"message":"provider failed","code":"provider_error"}}`)
	}))
	defer upstream.Close()

	tracker := newImageGenerationTestTracker(t)
	defer tracker.Close()
	handler := newTrackedImageGenerationHandler(t, tracker, upstream.URL, 0.25)
	req := httptest.NewRequest(http.MethodPost, openAIImagesGenerationsPath, strings.NewReader(`{"prompt":"draw a test icon"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), "conn_id", "req-image-json-error"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d body=%s", rec.Code, rec.Body.String())
	}
	detail := findTrackedImageRequest(t, tracker, "req-image-json-error")
	if detail.Status != "failed" || detail.TotalCostUSD != 0 {
		t.Fatalf("200 error envelope must be failed without charge, got %+v", detail)
	}
}

func TestImageGeneration_NormalizesNonJSONUpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, "<html><body>gateway failure</body></html>")
	}))
	defer upstream.Close()

	handler := newTrackedImageGenerationHandler(t, nil, upstream.URL, 0)
	req := httptest.NewRequest(http.MethodPost, openAIImagesGenerationsPath, strings.NewReader(`{"prompt":"draw a test icon"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway || strings.Contains(strings.ToLower(rec.Body.String()), "<html") {
		t.Fatalf("expected normalized upstream error, status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("expected JSON error Content-Type, got %q", rec.Header().Get("Content-Type"))
	}
}

func TestImageGeneration_NormalizesNonObjectJSONUpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `["gateway failure"]`)
	}))
	defer upstream.Close()

	handler := newTrackedImageGenerationHandler(t, nil, upstream.URL, 0)
	req := httptest.NewRequest(http.MethodPost, openAIImagesGenerationsPath, strings.NewReader(`{"prompt":"draw a test icon"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway || strings.Contains(rec.Body.String(), "gateway failure") {
		t.Fatalf("expected normalized object error, status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("expected JSON error Content-Type, got %q", rec.Header().Get("Content-Type"))
	}
}

func TestImageGeneration_AcceptsValidJSONWithNonstandardContentType(t *testing.T) {
	responseBody := `{"created":1,"data":[{"b64_json":"aW1hZ2U="}]}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, responseBody)
	}))
	defer upstream.Close()

	handler := newTrackedImageGenerationHandler(t, nil, upstream.URL, 0)
	req := httptest.NewRequest(http.MethodPost, openAIImagesGenerationsPath, strings.NewReader(`{"prompt":"draw a test icon"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != responseBody {
		t.Fatalf("expected validated JSON passthrough, status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("expected normalized JSON Content-Type, got %q", rec.Header().Get("Content-Type"))
	}
}

func TestValidateImageAPIJSONResponse_RequiresUsableImageResults(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "padded base64", body: `{"data":[{"b64_json":"aW1hZ2U="}]}`},
		{name: "raw base64", body: `{"data":[{"b64_json":"aW1hZ2U"}]}`},
		{name: "absolute URL", body: `{"data":[{"url":"https://example.com/image.png"}]}`},
		{name: "error envelope", body: `{"error":{"message":"failed"}}`, wantErr: true},
		{name: "empty data", body: `{"data":[]}`, wantErr: true},
		{name: "invalid base64", body: `{"data":[{"b64_json":"not base64!"}]}`, wantErr: true},
		{name: "relative URL", body: `{"data":[{"url":"/image.png"}]}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateImageAPIJSONResponse([]byte(tt.body))
			if (err != nil) != tt.wantErr {
				t.Fatalf("validate error=%v wantErr=%v", err, tt.wantErr)
			}
		})
	}
}

func TestRelayValidatedImageAPIStream_RejectsHTMLBeforeCommit(t *testing.T) {
	recorder := httptest.NewRecorder()
	resp := &http.Response{
		StatusCode:    http.StatusOK,
		ContentLength: 31,
		Header:        http.Header{"Content-Type": []string{"text/html"}},
		Body:          io.NopCloser(strings.NewReader("<html><body>error</body></html>")),
	}
	result, err := relayValidatedImageAPIStream(recorder, resp, openAIImagesGenerationsPath, imageAPIStreamTiming{})
	if err == nil || result.Committed || recorder.Code != http.StatusOK || recorder.Body.Len() != 0 {
		t.Fatalf("HTML stream response must fail before commit, result=%+v status=%d body=%s err=%v", result, recorder.Code, recorder.Body.String(), err)
	}
}

func TestRelayValidatedImageAPIStream_RejectsCompressedStreamBeforeCommit(t *testing.T) {
	recorder := httptest.NewRecorder()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":     []string{"text/event-stream"},
			"Content-Encoding": []string{"gzip"},
		},
		Body: io.NopCloser(strings.NewReader("compressed stream")),
	}
	result, err := relayValidatedImageAPIStream(recorder, resp, openAIImagesGenerationsPath, imageAPIStreamTiming{})
	if err == nil || result.Committed || recorder.Body.Len() != 0 {
		t.Fatalf("compressed stream must fail before commit, result=%+v body=%s err=%v", result, recorder.Body.String(), err)
	}
}

func TestRelayValidatedImageAPIStream_AllowsUnknownFieldsAndTextEvents(t *testing.T) {
	recorder := httptest.NewRecorder()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			"event: ping\n" +
				"data: pong\n\n" +
				"data: [DONE]\n\n" +
				"ping: keepalive\n" +
				"id\n" +
				"event: image_generation.completed\n" +
				`data: {"type":"image_generation.completed","b64_json":"ZmluYWw="}` + "\n\n",
		)),
	}

	result, err := relayValidatedImageAPIStream(recorder, resp, openAIImagesGenerationsPath, imageAPIStreamTiming{})
	if err != nil {
		t.Fatalf("relay compatible event stream failed: %v", err)
	}
	if !result.Committed || !strings.Contains(recorder.Body.String(), "event: ping") || !strings.Contains(recorder.Body.String(), "ping: keepalive") {
		t.Fatalf("expected unknown events and fields to pass through, result=%+v body=%s", result, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "[DONE]") {
		t.Fatalf("DONE sentinel should be ignored, body=%s", recorder.Body.String())
	}
}

func TestRelayValidatedImageAPIStream_KnownCompletedEventStillRequiresJSON(t *testing.T) {
	recorder := httptest.NewRecorder()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			"event: image_generation.completed\n" +
				"data: completed\n\n",
		)),
	}

	result, err := relayValidatedImageAPIStream(recorder, resp, openAIImagesGenerationsPath, imageAPIStreamTiming{})
	if err == nil || result.Committed || recorder.Body.Len() != 0 {
		t.Fatalf("known completed event must remain strict, result=%+v body=%s err=%v", result, recorder.Body.String(), err)
	}
}

func TestRelayValidatedImageAPIStream_DONESentinelIsNotCompletion(t *testing.T) {
	recorder := httptest.NewRecorder()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader("data: [DONE]\n\n")),
	}

	result, err := relayValidatedImageAPIStream(recorder, resp, openAIImagesGenerationsPath, imageAPIStreamTiming{})
	if err == nil || result.Committed || recorder.Body.Len() != 0 {
		t.Fatalf("DONE sentinel must not complete the request, result=%+v body=%s err=%v", result, recorder.Body.String(), err)
	}
}

func TestRelayValidatedImageAPIStream_TimingHooksFollowFirstAndTerminalEvents(t *testing.T) {
	recorder := httptest.NewRecorder()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			"event: ping\n" +
				"data: pong\n\n" +
				"event: image_generation.partial_image\n" +
				`data: {"type":"image_generation.partial_image","b64_json":"cGFydGlhbA==","partial_image_index":0}` + "\n\n" +
				"event: image_generation.partial_image\n" +
				`data: {"type":"image_generation.partial_image","b64_json":"cGFydGlhbA==","partial_image_index":1}` + "\n\n" +
				"event: image_generation.completed\n" +
				`data: {"type":"image_generation.completed","b64_json":"ZmluYWw="}` + "\n\n",
		)),
	}

	var calls []string
	_, err := relayValidatedImageAPIStream(recorder, resp, openAIImagesGenerationsPath, imageAPIStreamTiming{
		OnFirstEvent: func() { calls = append(calls, "first") },
		OnCompleted:  func() { calls = append(calls, "completed") },
	})
	if err != nil {
		t.Fatalf("relay stream failed: %v", err)
	}
	// ping 这类未识别事件不算首响；多个 partial 只记一次首响
	if strings.Join(calls, ",") != "first,completed" {
		t.Fatalf("expected first then completed exactly once, got %v", calls)
	}
}

func TestImageGeneration_StreamRequiresCompletedEventBeforeCharging(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: image_generation.partial_image\n")
		_, _ = io.WriteString(w, `data: {"type":"image_generation.partial_image","b64_json":"cGFydGlhbA==","partial_image_index":0}`+"\n\n")
		_, _ = io.WriteString(w, "event: image_generation.completed\n")
		_, _ = io.WriteString(w, `data: {"type":"image_generation.completed","b64_json":"ZmluYWw="}`+"\n\n")
	}))
	defer upstream.Close()

	tracker := newImageGenerationTestTracker(t)
	defer tracker.Close()
	handler := newTrackedImageGenerationHandler(t, tracker, upstream.URL, 0.25)
	req := httptest.NewRequest(http.MethodPost, openAIImagesGenerationsPath, strings.NewReader(`{"prompt":"draw a test icon","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), "conn_id", "req-image-stream-completed"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "image_generation.completed") {
		t.Fatalf("expected completed stream, status=%d body=%s", rec.Code, rec.Body.String())
	}
	detail := findTrackedImageRequest(t, tracker, "req-image-stream-completed")
	if detail.Status != "completed" || !detail.IsStreaming || detail.TotalCostUSD != 0.25 {
		t.Fatalf("expected completed charged stream, got %+v", detail)
	}
	if detail.FirstTokenMs == nil || detail.CompletionMs == nil {
		t.Fatalf("expected stream image first response and completion timing, got first=%v completion=%v", detail.FirstTokenMs, detail.CompletionMs)
	}
}

func TestImageGeneration_StreamEOFBeforeCompletedFailsWithoutCharging(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: image_generation.partial_image\n")
		_, _ = io.WriteString(w, `data: {"type":"image_generation.partial_image","b64_json":"cGFydGlhbA==","partial_image_index":0}`+"\n\n")
	}))
	defer upstream.Close()

	tracker := newImageGenerationTestTracker(t)
	defer tracker.Close()
	handler := newTrackedImageGenerationHandler(t, tracker, upstream.URL, 0.25)
	req := httptest.NewRequest(http.MethodPost, openAIImagesGenerationsPath, strings.NewReader(`{"prompt":"draw a test icon","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), "conn_id", "req-image-stream-incomplete"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "event: error") {
		t.Fatalf("expected committed stream to end with error event, status=%d body=%s", rec.Code, rec.Body.String())
	}
	detail := findTrackedImageRequest(t, tracker, "req-image-stream-incomplete")
	if detail.Status != "failed" || detail.TotalCostUSD != 0 || !strings.HasPrefix(detail.FailureReason, "image_api_invalid_upstream_response") {
		t.Fatalf("incomplete stream must fail without charge, got %+v", detail)
	}
}

func TestImageGeneration_DisabledReturnsExplicitErrorWithoutEndpointFallback(t *testing.T) {
	fallbackHits := 0
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackHits++
		w.WriteHeader(http.StatusOK)
	}))
	defer fallback.Close()

	cfg := &config.Config{Endpoints: []config.EndpointConfig{{
		Name: "fallback", URL: fallback.URL,
	}}}
	handler := NewHandler(endpoint.NewManager(cfg), cfg)
	handler.SetImageGenerationConfigProvider(staticImageGenerationConfigProvider{})
	req := httptest.NewRequest(http.MethodPost, openAIImagesGenerationsPath, strings.NewReader(`{"prompt":"test"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d body=%s", rec.Code, rec.Body.String())
	}
	if fallbackHits != 0 {
		t.Fatalf("expected no endpoint fallback, got %d hits", fallbackHits)
	}
	if !strings.Contains(rec.Body.String(), "image_generation_not_configured") {
		t.Fatalf("unexpected error body: %s", rec.Body.String())
	}
}

func TestPrepareImageGenerationRequestBody_PreservesExplicitModel(t *testing.T) {
	body := []byte(`{"model":"custom-image-model","prompt":"test","n":1}`)
	prepared, model, err := prepareImageGenerationRequestBody(body, "gpt-image-2")
	if err != nil {
		t.Fatalf("prepare request failed: %v", err)
	}
	if model != "custom-image-model" || string(prepared) != string(body) {
		t.Fatalf("expected explicit model request to remain unchanged, model=%s body=%s", model, prepared)
	}
}

func TestDetectSSERequest_ImageGenerationParsesStreamField(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, openAIImagesGenerationsPath, strings.NewReader(`{"prompt":"test","nested":{"stream":true},"stream":false}`))
	if (&Handler{}).detectSSERequest(req, []byte(`{"prompt":"test","nested":{"stream":true},"stream":false}`)) {
		t.Fatal("nested stream field must not mark image request as streaming")
	}
	if !(&Handler{}).detectSSERequest(req, []byte(`{"prompt":"test","stream" : true}`)) {
		t.Fatal("top-level stream=true must mark image request as streaming")
	}
}

func newTrackedImageGenerationHandler(t *testing.T, tracker *tracking.UsageTracker, upstreamURL string, fixedPrice float64) *Handler {
	t.Helper()
	endpointManager := endpoint.NewManager(&config.Config{})
	handler := NewHandler(endpointManager, &config.Config{})
	if tracker != nil {
		handler.SetMonitoringMiddleware(middleware.NewMonitoringMiddleware(endpointManager))
		handler.SetUsageTracker(tracker)
	}
	handler.SetImageGenerationConfigProvider(staticImageGenerationConfigProvider{config: ImageGenerationConfig{
		Enabled:       true,
		EndpointURL:   upstreamURL + openAIImagesGenerationsPath,
		APIKey:        "secret-key",
		Model:         "gpt-image-2",
		FixedPriceUSD: fixedPrice,
		Timeout:       5 * time.Second,
	}})
	return handler
}

func findTrackedImageRequest(t *testing.T, tracker *tracking.UsageTracker, requestID string) tracking.RequestDetail {
	t.Helper()
	start := time.Now().Add(-time.Minute)
	end := time.Now().Add(time.Minute)
	details, _, err := tracker.QueryRequestDetailsWithHotPool(context.Background(), &tracking.QueryOptions{
		StartDate: &start,
		EndDate:   &end,
		Limit:     20,
	})
	if err != nil {
		t.Fatalf("query tracked image request failed: %v", err)
	}
	for _, detail := range details {
		if detail.RequestID == requestID {
			return detail
		}
	}
	t.Fatalf("expected tracked image request %s", requestID)
	return tracking.RequestDetail{}
}

func newImageGenerationTestTracker(t *testing.T) *tracking.UsageTracker {
	t.Helper()
	tracker, err := tracking.NewUsageTracker(&tracking.Config{
		Enabled:         true,
		DatabasePath:    ":memory:",
		BufferSize:      20,
		BatchSize:       5,
		FlushInterval:   50 * time.Millisecond,
		MaxRetry:        3,
		CleanupInterval: 24 * time.Hour,
		RetentionDays:   30,
	})
	if err != nil {
		t.Fatalf("create usage tracker failed: %v", err)
	}
	return tracker
}
