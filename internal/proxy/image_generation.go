package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"cc-forwarder/internal/privacy"
	"cc-forwarder/internal/proxy/handlers"
	"cc-forwarder/internal/transport"
)

const (
	openAIImagesGenerationsPath = "/v1/images/generations"
	openAIImagesEditsPath       = "/v1/images/edits"
)

// ImageGenerationConfig 是独立、单上游的图像生成配置。
type ImageGenerationConfig struct {
	Enabled       bool
	DirectConnect bool
	DirectPortMin int
	DirectPortMax int
	EndpointURL   string
	APIKey        string
	Model         string
	FixedPriceUSD float64
	Timeout       time.Duration
}

type imageGenerationRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

type preparedImageAPIRequest struct {
	Body        []byte
	ContentType string
	Model       string
}

func isImageAPIPath(path string) bool {
	return path == openAIImagesGenerationsPath || path == openAIImagesEditsPath
}

func (h *Handler) handleImageGeneration(ctx context.Context, w http.ResponseWriter, r *http.Request, bodyBytes []byte, lifecycleManager *RequestLifecycleManager) {
	h.handleImageAPIRequest(ctx, w, r, bodyBytes, lifecycleManager)
}

func (h *Handler) handleImageEdit(ctx context.Context, w http.ResponseWriter, r *http.Request, bodyBytes []byte, lifecycleManager *RequestLifecycleManager) {
	h.handleImageAPIRequest(ctx, w, r, bodyBytes, lifecycleManager)
}

func (h *Handler) handleImageAPIRequest(ctx context.Context, w http.ResponseWriter, r *http.Request, bodyBytes []byte, lifecycleManager *RequestLifecycleManager) {
	lifecycleManager.SetUpstream("endpoint", "image_generation", "image-generation", 0)
	lifecycleManager.SetEndpoint("image-generation")
	if r.Method != http.MethodPost {
		h.failImageGenerationRequest(w, lifecycleManager, http.StatusMethodNotAllowed, "method_not_allowed", "only POST is supported")
		return
	}

	config, err := h.loadImageGenerationConfig(ctx)
	if err != nil {
		h.failImageGenerationRequest(w, lifecycleManager, http.StatusInternalServerError, "image_generation_config_error", err.Error())
		return
	}
	providerName := imageGenerationProviderName(config.EndpointURL)
	lifecycleManager.SetUpstream("endpoint", "image_generation", providerName, 0)
	lifecycleManager.SetEndpoint(providerName)

	if !config.Enabled || strings.TrimSpace(config.EndpointURL) == "" || strings.TrimSpace(config.APIKey) == "" {
		h.failImageGenerationRequest(w, lifecycleManager, http.StatusServiceUnavailable, "image_generation_not_configured", "image generation provider is not configured or enabled")
		return
	}
	upstreamURL, err := resolveImageAPIEndpoint(config.EndpointURL, r.URL.Path)
	if err != nil {
		h.failImageGenerationRequest(w, lifecycleManager, http.StatusServiceUnavailable, "image_generation_invalid_config", err.Error())
		return
	}

	prepared, err := prepareImageAPIRequest(r, bodyBytes, config.Model)
	if err != nil {
		h.failImageGenerationRequest(w, lifecycleManager, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	lifecycleManager.SetModel(prepared.Model)
	// 注入默认模型视为一次「改写」：上游模型固定记录为实际发出的模型
	lifecycleManager.SetUpstreamModel(prepared.Model)
	isStreaming := imageAPIRequestStreamEnabled(r, prepared.Body)

	prepared.Body, err = h.applyImageAPIPrivacyFilter(r, prepared, providerName)
	if err != nil {
		if policyErr := handlers.AsPrivacyPolicyError(err); policyErr != nil {
			lifecycleManager.FailRequest(handlers.PrivacyFailureReason(policyErr), policyErr.Message, policyErr.StatusCode)
			writeImageGenerationError(w, policyErr.StatusCode, policyErr.Code, policyErr.Message)
			return
		}
		h.failImageGenerationRequest(w, lifecycleManager, http.StatusInternalServerError, "privacy_filter_error", err.Error())
		return
	}

	timeout := config.Timeout
	if timeout <= 0 {
		timeout = 300 * time.Second
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	requestCtx, _ = handlers.WithUpstreamTrace(requestCtx, lifecycleManager.RecordDirectUpstreamWrite)

	upstreamReq, err := http.NewRequestWithContext(requestCtx, http.MethodPost, upstreamURL, bytes.NewReader(prepared.Body))
	if err != nil {
		h.failImageGenerationRequest(w, lifecycleManager, http.StatusInternalServerError, "image_generation_request_error", err.Error())
		return
	}
	copyImageAPIRequestHeaders(r, upstreamReq)
	upstreamReq.Header.Set("Authorization", "Bearer "+config.APIKey)
	upstreamReq.Header.Set("Content-Type", prepared.ContentType)
	upstreamReq.Header.Set("Accept-Encoding", "identity")
	if upstreamReq.Header.Get("Accept") == "" {
		upstreamReq.Header.Set("Accept", "application/json")
	}

	client, releaseClient, err := h.getImageGenerationHTTPClient(config)
	if err != nil {
		h.failImageGenerationRequest(w, lifecycleManager, http.StatusInternalServerError, "image_generation_transport_error", err.Error())
		return
	}
	defer releaseClient()
	lifecycleManager.UpdateStatus("forwarding", 0, 0)
	resp, err := client.Do(upstreamReq)
	if err != nil {
		status := http.StatusBadGateway
		code := "image_generation_upstream_error"
		if requestCtx.Err() == context.DeadlineExceeded {
			status = http.StatusGatewayTimeout
			code = "image_generation_timeout"
		}
		h.failImageGenerationRequest(w, lifecycleManager, status, code, err.Error())
		return
	}
	defer resp.Body.Close()

	lifecycleManager.UpdateStatus("processing", 0, resp.StatusCode)
	if resp.StatusCode != http.StatusOK {
		status, reason, writeErr := writeImageAPIUpstreamFailure(w, resp)
		if writeErr != nil {
			lifecycleManager.FailRequest("image_generation_response_write_error", writeErr.Error(), status)
			return
		}
		lifecycleManager.FailRequest("image_generation_upstream_error", reason, status)
		return
	}

	if isStreaming {
		result, relayErr := relayValidatedImageAPIStream(w, resp, r.URL.Path, imageAPIStreamTiming{
			OnFirstEvent: lifecycleManager.RecordFirstToken,
			OnCompleted:  lifecycleManager.RecordStreamCompletion,
		})
		if relayErr != nil {
			if result.WriteFailure {
				lifecycleManager.FailRequest("image_generation_response_write_error", relayErr.Error(), resp.StatusCode)
				return
			}
			if result.Committed {
				writeImageAPIStreamError(w, "image_api_invalid_upstream_response", "Image upstream returned an invalid event stream")
				lifecycleManager.FailRequest("image_api_invalid_upstream_response", relayErr.Error(), http.StatusBadGateway)
				return
			}
			lifecycleManager.FailRequest("image_api_invalid_upstream_response", relayErr.Error(), http.StatusBadGateway)
			writeImageGenerationError(w, http.StatusBadGateway, "image_api_invalid_upstream_response", "Image upstream returned an invalid event stream")
			return
		}
		lifecycleManager.CompleteRequestWithCost(config.FixedPriceUSD)
		return
	}

	responseBody, err := readValidatedImageAPIJSONResponse(resp)
	if err != nil {
		lifecycleManager.FailRequest("image_api_invalid_upstream_response", err.Error(), http.StatusBadGateway)
		writeImageGenerationError(w, http.StatusBadGateway, "image_api_invalid_upstream_response", "Image upstream returned an invalid response")
		return
	}
	lifecycleManager.RecordNonStreamingResponseComplete()
	copyImageAPIResponseHeaders(resp, w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(responseBody); err != nil {
		lifecycleManager.FailRequest("image_generation_response_write_error", err.Error(), resp.StatusCode)
		return
	}
	lifecycleManager.CompleteRequestWithCost(config.FixedPriceUSD)
}

func prepareImageAPIRequest(r *http.Request, bodyBytes []byte, defaultModel string) (preparedImageAPIRequest, error) {
	if r == nil || r.URL == nil {
		return preparedImageAPIRequest{}, fmt.Errorf("image API request is missing URL")
	}
	switch r.URL.Path {
	case openAIImagesGenerationsPath:
		body, model, err := prepareImageGenerationRequestBody(bodyBytes, defaultModel)
		if err != nil {
			return preparedImageAPIRequest{}, err
		}
		return preparedImageAPIRequest{Body: body, ContentType: "application/json", Model: model}, nil
	case openAIImagesEditsPath:
		return prepareImageEditRequestBody(bodyBytes, r.Header.Get("Content-Type"), defaultModel)
	default:
		return preparedImageAPIRequest{}, fmt.Errorf("unsupported image API path: %s", r.URL.Path)
	}
}

func (h *Handler) loadImageGenerationConfig(ctx context.Context) (ImageGenerationConfig, error) {
	if h == nil || h.imageGenerationConfigProvider == nil {
		return ImageGenerationConfig{}, nil
	}
	return h.imageGenerationConfigProvider.GetImageGenerationConfig(ctx)
}

func (h *Handler) getImageGenerationHTTPClient(config ImageGenerationConfig) (*http.Client, func(), error) {
	if config.DirectConnect {
		if h.imageDirectHTTPClientFactory != nil {
			return h.imageDirectHTTPClientFactory(config.DirectPortMin, config.DirectPortMax)
		}
		directTransport, err := transport.CreateSourcePortTransport(config.DirectPortMin, config.DirectPortMax)
		if err != nil {
			return nil, nil, err
		}
		directTransport.DisableCompression = true
		client := &http.Client{Transport: directTransport}
		return client, directTransport.CloseIdleConnections, nil
	}
	h.imageHTTPInitOnce.Do(func() {
		if h.config == nil {
			h.imageHTTPInitErr = fmt.Errorf("handler config is nil")
			return
		}
		h.imageHTTPTransport, h.imageHTTPInitErr = transport.CreateTransport(h.config)
		if h.imageHTTPInitErr != nil {
			return
		}
		h.imageHTTPTransport.DisableCompression = true
		h.imageHTTPClient = &http.Client{Transport: h.imageHTTPTransport}
	})
	return h.imageHTTPClient, func() {}, h.imageHTTPInitErr
}

func prepareImageGenerationRequestBody(bodyBytes []byte, defaultModel string) ([]byte, string, error) {
	var request imageGenerationRequest
	if err := json.Unmarshal(bodyBytes, &request); err != nil {
		return nil, "", fmt.Errorf("request body must be valid JSON: %w", err)
	}
	if strings.TrimSpace(request.Prompt) == "" {
		return nil, "", fmt.Errorf("prompt is required")
	}
	model := strings.TrimSpace(request.Model)
	if model != "" {
		return bodyBytes, model, nil
	}
	model = strings.TrimSpace(defaultModel)
	if model == "" {
		model = "gpt-image-2"
	}
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(bodyBytes))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return nil, "", fmt.Errorf("request body must be a JSON object: %w", err)
	}
	payload["model"] = model
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, "", fmt.Errorf("encode image generation request: %w", err)
	}
	return encoded, model, nil
}

func (h *Handler) applyImageAPIPrivacyFilter(r *http.Request, prepared preparedImageAPIRequest, providerName string) ([]byte, error) {
	if h == nil || h.privacyFilter == nil {
		return prepared.Body, nil
	}
	if r.URL.Path == openAIImagesEditsPath && isMultipartImageEditContentType(prepared.ContentType) {
		return h.applyMultipartImageEditPrivacyFilter(r, prepared.Body, prepared.ContentType, providerName)
	}
	request := privacy.Request{
		Path:         r.URL.Path,
		Method:       http.MethodPost,
		UpstreamType: privacy.UpstreamTypeEndpoint,
		EndpointName: providerName,
		ContentType:  prepared.ContentType,
	}
	return handlers.ApplyPrivacyFilter(h.privacyFilter, r, request, prepared.Body)
}

func copyImageAPIRequestHeaders(src, dst *http.Request) {
	for key, values := range src.Header {
		switch strings.ToLower(key) {
		case "host", "authorization", "x-api-key", "cookie", "content-length", "content-type", "accept-encoding":
			continue
		}
		for _, value := range values {
			dst.Header.Add(key, value)
		}
	}
}

func validateImageGenerationEndpoint(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return fmt.Errorf("image generation endpoint must be a valid http/https URL")
	}
	return nil
}

func resolveImageAPIEndpoint(raw, requestPath string) (string, error) {
	if err := validateImageGenerationEndpoint(raw); err != nil {
		return "", err
	}
	trimmed := strings.TrimSpace(raw)
	if requestPath == openAIImagesGenerationsPath {
		return trimmed, nil
	}
	if requestPath != openAIImagesEditsPath {
		return "", fmt.Errorf("unsupported image API path: %s", requestPath)
	}

	parsed, _ := url.Parse(trimmed)
	cleanPath := strings.TrimSuffix(parsed.Path, "/")
	const generationSuffix = "/images/generations"
	if !strings.HasSuffix(cleanPath, generationSuffix) {
		return "", fmt.Errorf("image generation endpoint must end with /images/generations to derive /images/edits")
	}
	parsed.Path = strings.TrimSuffix(cleanPath, generationSuffix) + "/images/edits"
	parsed.RawPath = ""
	return parsed.String(), nil
}

func imageGenerationProviderName(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err == nil && parsed.Hostname() != "" {
		return parsed.Hostname()
	}
	return "image-generation"
}

func (h *Handler) failImageGenerationRequest(w http.ResponseWriter, lifecycleManager *RequestLifecycleManager, status int, code, message string) {
	lifecycleManager.FailRequest(code, message, status)
	writeImageGenerationError(w, status, code, message)
}

func writeImageGenerationError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "image_generation_error",
			"code":    code,
		},
	})
}

func summarizeImageGenerationUpstreamError(body []byte, status int) string {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &envelope) == nil && envelope.Error.Message != "" {
		if envelope.Error.Code != "" {
			return envelope.Error.Code + ": " + envelope.Error.Message
		}
		return envelope.Error.Message
	}
	return fmt.Sprintf("image generation upstream returned HTTP %d", status)
}
