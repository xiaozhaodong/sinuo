package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sinuo/config"
	"sinuo/internal/endpoint"
	"sinuo/internal/middleware"
	"sinuo/internal/privacy"
	"sinuo/internal/tracking"
)

func TestImageEdit_ForwardsMultipartWithDefaultModelAndTracksRequest(t *testing.T) {
	var receivedAuth string
	var receivedPrompt string
	var receivedModel string
	var receivedImages [][]byte
	var receivedMask []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != openAIImagesEditsPath {
			t.Fatalf("unexpected upstream path: %s", r.URL.Path)
		}
		receivedAuth = r.Header.Get("Authorization")
		if err := r.ParseMultipartForm(2 << 20); err != nil {
			t.Fatalf("parse upstream multipart failed: %v", err)
		}
		receivedPrompt = r.FormValue("prompt")
		receivedModel = r.FormValue("model")
		receivedImages = readMultipartFiles(t, r, "image[]")
		receivedMask = readMultipartFile(t, r, "mask")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1,"data":[{"b64_json":"ZWRpdGVk"}]}`))
	}))
	defer upstream.Close()

	tracker := newImageGenerationTestTracker(t)
	defer tracker.Close()
	endpointManager := endpoint.NewManager(&config.Config{})
	handler := NewHandler(endpointManager, &config.Config{})
	handler.SetMonitoringMiddleware(middleware.NewMonitoringMiddleware(endpointManager))
	handler.SetUsageTracker(tracker)
	handler.SetImageGenerationConfigProvider(staticImageGenerationConfigProvider{config: ImageGenerationConfig{
		Enabled:       true,
		EndpointURL:   upstream.URL + openAIImagesGenerationsPath,
		APIKey:        "secret-key",
		Model:         "gpt-image-2",
		FixedPriceUSD: 0.35,
		Timeout:       5 * time.Second,
	}})

	body, contentType := buildImageEditMultipartBody(t, map[string]string{
		"prompt":  "replace the background",
		"quality": "high",
	}, []imageEditTestFile{
		{Field: "image[]", Filename: "source-a.png", Content: []byte("source-image-a")},
		{Field: "image[]", Filename: "source-b.png", Content: []byte("source-image-b")},
		{Field: "mask", Filename: "mask.png", Content: []byte("mask-image")},
	})
	req := httptest.NewRequest(http.MethodPost, openAIImagesEditsPath, bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	req = req.WithContext(context.WithValue(req.Context(), "conn_id", "req-image-edit"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if receivedAuth != "Bearer secret-key" {
		t.Fatalf("unexpected upstream authorization: %q", receivedAuth)
	}
	if receivedPrompt != "replace the background" || receivedModel != "gpt-image-2" {
		t.Fatalf("unexpected upstream prompt/model: prompt=%q model=%q", receivedPrompt, receivedModel)
	}
	if len(receivedImages) != 2 || string(receivedImages[0]) != "source-image-a" || string(receivedImages[1]) != "source-image-b" || string(receivedMask) != "mask-image" {
		t.Fatalf("multipart image data changed: images=%q mask=%q", receivedImages, receivedMask)
	}

	assertTrackedImageEdit(t, tracker, "req-image-edit", 0.35)
	detail := waitRequestDetail(t, tracker, "req-image-edit", func(d tracking.RequestDetail) bool { return d.Status == "completed" })
	if detail.RequestModel != "" || detail.UpstreamModel != "gpt-image-2" {
		t.Fatalf("default-model injection must keep request_model empty: request=%q upstream=%q", detail.RequestModel, detail.UpstreamModel)
	}
}

func TestImageEdit_ForwardsJSONReferences(t *testing.T) {
	var received map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			t.Fatalf("unexpected Content-Type: %s", r.Header.Get("Content-Type"))
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode upstream JSON failed: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1,"data":[{"b64_json":"ZWRpdGVk"}]}`))
	}))
	defer upstream.Close()

	handler := NewHandler(endpoint.NewManager(&config.Config{}), &config.Config{})
	handler.SetImageGenerationConfigProvider(staticImageGenerationConfigProvider{config: ImageGenerationConfig{
		Enabled:     true,
		EndpointURL: upstream.URL + openAIImagesGenerationsPath,
		APIKey:      "secret-key",
		Model:       "gpt-image-2",
		Timeout:     5 * time.Second,
	}})
	req := httptest.NewRequest(http.MethodPost, openAIImagesEditsPath, strings.NewReader(`{"prompt":"use this reference","images":[{"image_url":"https://example.com/source.png"}]}`))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if received["model"] != "gpt-image-2" || received["prompt"] != "use this reference" {
		t.Fatalf("unexpected upstream JSON: %+v", received)
	}
}

func TestPrepareImageEditMultipart_ExplicitModelPreservesBody(t *testing.T) {
	body, contentType := buildImageEditMultipartBody(t, map[string]string{
		"model":  "custom-image-model",
		"prompt": "preserve multipart bytes",
	}, []imageEditTestFile{{Field: "image[]", Filename: "source.png", Content: []byte("source")}})
	prepared, err := prepareImageEditRequestBody(body, contentType, "gpt-image-2")
	if err != nil {
		t.Fatalf("prepare multipart edit failed: %v", err)
	}
	if prepared.Model != "custom-image-model" || !bytes.Equal(prepared.Body, body) {
		t.Fatalf("explicit-model multipart body must remain byte-identical, model=%q", prepared.Model)
	}
	if got := imageEditRequestModel(body, contentType, ""); got != "custom-image-model" {
		t.Fatalf("imageEditRequestModel multipart = %q", got)
	}
	if got := imageEditRequestModel([]byte(`{"model":"json-model"}`), "application/json", "json-model"); got != "json-model" {
		t.Fatalf("imageEditRequestModel json = %q", got)
	}
}

// multipart 显式 model 时 request_model 必须落库，否则详情会把显式模型误标为「默认模型」。
func TestImageEdit_MultipartExplicitModelRecordsRequestModel(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1,"data":[{"b64_json":"ZWRpdGVk"}]}`))
	}))
	defer upstream.Close()

	tracker := newImageGenerationTestTracker(t)
	defer tracker.Close()
	endpointManager := endpoint.NewManager(&config.Config{})
	handler := NewHandler(endpointManager, &config.Config{})
	handler.SetMonitoringMiddleware(middleware.NewMonitoringMiddleware(endpointManager))
	handler.SetUsageTracker(tracker)
	handler.SetImageGenerationConfigProvider(staticImageGenerationConfigProvider{config: ImageGenerationConfig{
		Enabled: true, EndpointURL: upstream.URL + openAIImagesGenerationsPath, APIKey: "secret-key",
		Model: "gpt-image-2", FixedPriceUSD: 0.35, Timeout: 5 * time.Second,
	}})

	body, contentType := buildImageEditMultipartBody(t, map[string]string{
		"model":  "custom-image-model",
		"prompt": "explicit model",
	}, []imageEditTestFile{{Field: "image[]", Filename: "source.png", Content: []byte("source")}})
	const requestID = "req-image-edit-explicit-model"
	req := httptest.NewRequest(http.MethodPost, openAIImagesEditsPath, bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	req = req.WithContext(context.WithValue(req.Context(), "conn_id", requestID))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	detail := waitRequestDetail(t, tracker, requestID, func(d tracking.RequestDetail) bool { return d.Status == "completed" })
	if detail.RequestModel != "custom-image-model" || detail.UpstreamModel != "custom-image-model" || detail.ModelName != "custom-image-model" {
		t.Fatalf("explicit multipart model not recorded: request=%q upstream=%q billing=%q", detail.RequestModel, detail.UpstreamModel, detail.ModelName)
	}
}

func TestImageEdit_DirectConnectUsesDedicatedClient(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != openAIImagesEditsPath {
			t.Fatalf("unexpected upstream path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1,"data":[{"b64_json":"ZWRpdGVk"}]}`))
	}))
	defer upstream.Close()

	directClientUsed := false
	handler := NewHandler(endpoint.NewManager(&config.Config{}), &config.Config{})
	handler.imageDirectHTTPClientFactory = func(startPort, endPort int) (*http.Client, func(), error) {
		directClientUsed = startPort == 31080 && endPort == 31179
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
	body, contentType := buildImageEditMultipartBody(t, map[string]string{
		"model":  "gpt-image-2",
		"prompt": "direct edit",
	}, []imageEditTestFile{{Field: "image[]", Filename: "source.png", Content: []byte("source")}})
	req := httptest.NewRequest(http.MethodPost, openAIImagesEditsPath, bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !directClientUsed {
		t.Fatalf("expected successful edit through dedicated direct client, status=%d used=%v body=%s", rec.Code, directClientUsed, rec.Body.String())
	}
}

func TestImageEdit_MultipartPrivacyFiltersPromptWithoutChangingImage(t *testing.T) {
	var receivedPrompt string
	var receivedImage []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(2 << 20); err != nil {
			t.Fatalf("parse upstream multipart failed: %v", err)
		}
		receivedPrompt = r.FormValue("prompt")
		receivedImage = readMultipartFile(t, r, "image[]")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1,"data":[{"b64_json":"ZWRpdGVk"}]}`))
	}))
	defer upstream.Close()

	filter := &imageEditPrivacyFilter{redactedPrompt: "[REDACTED]"}
	handler := NewHandler(endpoint.NewManager(&config.Config{}), &config.Config{})
	handler.SetPrivacyFilter(filter)
	handler.SetImageGenerationConfigProvider(staticImageGenerationConfigProvider{config: ImageGenerationConfig{
		Enabled:     true,
		EndpointURL: upstream.URL + openAIImagesGenerationsPath,
		APIKey:      "secret-key",
		Model:       "gpt-image-2",
		Timeout:     5 * time.Second,
	}})
	body, contentType := buildImageEditMultipartBody(t, map[string]string{
		"prompt": "contains sensitive text",
	}, []imageEditTestFile{{Field: "image[]", Filename: "source.png", Content: []byte("unchanged-image-bytes")}})
	req := httptest.NewRequest(http.MethodPost, openAIImagesEditsPath, bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if receivedPrompt != "[REDACTED]" {
		t.Fatalf("prompt was not redacted: %q", receivedPrompt)
	}
	if string(receivedImage) != "unchanged-image-bytes" {
		t.Fatalf("image bytes changed during privacy filtering: %q", receivedImage)
	}
	if len(filter.requests) != 1 || filter.requests[0].Path != openAIImagesEditsPath || filter.requests[0].ContentType != "application/json" {
		t.Fatalf("unexpected privacy request: %+v", filter.requests)
	}
}

func TestImageEdit_DisabledReturnsExplicitErrorWithoutEndpointFallback(t *testing.T) {
	fallbackHits := 0
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackHits++
		w.WriteHeader(http.StatusOK)
	}))
	defer fallback.Close()
	cfg := &config.Config{Endpoints: []config.EndpointConfig{{Name: "fallback", URL: fallback.URL}}}
	handler := NewHandler(endpoint.NewManager(cfg), cfg)
	handler.SetImageGenerationConfigProvider(staticImageGenerationConfigProvider{})
	body, contentType := buildImageEditMultipartBody(t, map[string]string{"prompt": "test"}, []imageEditTestFile{{Field: "image[]", Filename: "source.png", Content: []byte("source")}})
	req := httptest.NewRequest(http.MethodPost, openAIImagesEditsPath, bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable || fallbackHits != 0 {
		t.Fatalf("expected isolated 503 without fallback, status=%d fallback_hits=%d body=%s", rec.Code, fallbackHits, rec.Body.String())
	}
}

func TestResolveImageAPIEndpoint_DerivesEditSibling(t *testing.T) {
	resolved, err := resolveImageAPIEndpoint("https://api.example.com/v1/images/generations?region=us", openAIImagesEditsPath)
	if err != nil {
		t.Fatalf("resolve edit endpoint failed: %v", err)
	}
	if resolved != "https://api.example.com/v1/images/edits?region=us" {
		t.Fatalf("unexpected edit endpoint: %s", resolved)
	}
	if _, err := resolveImageAPIEndpoint("https://api.example.com/custom-generate", openAIImagesEditsPath); err == nil {
		t.Fatal("expected non-standard generation URL to fail edit derivation")
	}
}

func TestDetectSSERequest_ImageEditMultipartStreamField(t *testing.T) {
	body, contentType := buildImageEditMultipartBody(t, map[string]string{
		"prompt": "stream edit",
		"stream": "true",
	}, []imageEditTestFile{{Field: "image[]", Filename: "source.png", Content: []byte("source")}})
	req := httptest.NewRequest(http.MethodPost, openAIImagesEditsPath, bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	if !(&Handler{}).detectSSERequest(req, body) {
		t.Fatal("expected multipart stream=true to be detected as SSE")
	}
}

func TestRelayValidatedImageAPIStream_AcceptsCompletedImageEdit(t *testing.T) {
	recorder := httptest.NewRecorder()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			"event: image_edit.completed\n" +
				`data: {"type":"image_edit.completed","b64_json":"ZWRpdGVk"}` + "\n\n",
		)),
	}
	result, err := relayValidatedImageAPIStream(recorder, resp, openAIImagesEditsPath, imageAPIStreamTiming{})
	if err != nil {
		t.Fatalf("relay event stream failed: %v", err)
	}
	if !result.Committed || !recorder.Flushed || !strings.Contains(recorder.Body.String(), "image_edit.completed") {
		t.Fatalf("expected completed flushed event stream, result=%+v body=%s", result, recorder.Body.String())
	}
}

type imageEditTestFile struct {
	Field    string
	Filename string
	Content  []byte
}

func buildImageEditMultipartBody(t *testing.T, fields map[string]string, files []imageEditTestFile) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatalf("write multipart field %s failed: %v", name, err)
		}
	}
	for _, file := range files {
		part, err := writer.CreateFormFile(file.Field, file.Filename)
		if err != nil {
			t.Fatalf("create multipart file %s failed: %v", file.Field, err)
		}
		if _, err := part.Write(file.Content); err != nil {
			t.Fatalf("write multipart file %s failed: %v", file.Field, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer failed: %v", err)
	}
	return body.Bytes(), writer.FormDataContentType()
}

func readMultipartFiles(t *testing.T, r *http.Request, field string) [][]byte {
	t.Helper()
	files := r.MultipartForm.File[field]
	result := make([][]byte, 0, len(files))
	for _, header := range files {
		file, err := header.Open()
		if err != nil {
			t.Fatalf("open multipart file %s failed: %v", field, err)
		}
		data, err := io.ReadAll(file)
		_ = file.Close()
		if err != nil {
			t.Fatalf("read multipart file bytes %s failed: %v", field, err)
		}
		result = append(result, data)
	}
	return result
}

func readMultipartFile(t *testing.T, r *http.Request, field string) []byte {
	t.Helper()
	file, _, err := r.FormFile(field)
	if err != nil {
		t.Fatalf("read multipart file %s failed: %v", field, err)
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("read multipart file bytes %s failed: %v", field, err)
	}
	return data
}

func assertTrackedImageEdit(t *testing.T, tracker *tracking.UsageTracker, requestID string, expectedCost float64) {
	t.Helper()
	start := time.Now().Add(-time.Minute)
	end := time.Now().Add(time.Minute)
	details, _, err := tracker.QueryRequestDetailsWithHotPool(context.Background(), &tracking.QueryOptions{
		StartDate: &start,
		EndDate:   &end,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("query tracked image edit failed: %v", err)
	}
	for _, detail := range details {
		if detail.RequestID != requestID {
			continue
		}
		if detail.Status != "completed" || detail.Path != openAIImagesEditsPath || detail.ModelName != "gpt-image-2" {
			t.Fatalf("unexpected tracked image edit: %+v", detail)
		}
		if detail.RequestFamily != tracking.RequestFamilyImage || detail.UpstreamSourceName != "image_generation" || detail.TotalCostUSD != expectedCost {
			t.Fatalf("unexpected tracked image edit source/cost: %+v", detail)
		}
		return
	}
	t.Fatalf("expected tracked image edit %s", requestID)
}

type imageEditPrivacyFilter struct {
	requests       []privacy.Request
	redactedPrompt string
}

func (f *imageEditPrivacyFilter) Apply(_ context.Context, request privacy.Request, body []byte) (privacy.ApplyResult, error) {
	f.requests = append(f.requests, request)
	var payload map[string]string
	if err := json.Unmarshal(body, &payload); err != nil {
		return privacy.ApplyResult{}, err
	}
	payload["prompt"] = f.redactedPrompt
	redacted, err := json.Marshal(payload)
	if err != nil {
		return privacy.ApplyResult{}, err
	}
	return privacy.ApplyResult{Body: redacted, Changed: true, Action: privacy.ModeRedact}, nil
}

func (f *imageEditPrivacyFilter) SnapshotVersion() int64 { return 1 }
