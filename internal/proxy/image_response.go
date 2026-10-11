package proxy

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
)

const (
	imageAPIResponseMaxBytes       = int64(128 << 20)
	imageAPIErrorResponseMaxBytes  = int64(1 << 20)
	imageAPIStreamEventMaxBytes    = int64(128 << 20)
	imageAPIStreamPreambleMaxBytes = int64(1 << 20)
)

type imageAPIStreamRelayResult struct {
	Committed     bool
	ResponseBytes int64
	WriteFailure  bool
}

// imageAPIStreamTiming 流式生图的计时回调：首个有效事件（partial / completed）到达记首响，
// 完成事件写出后记生成耗时。零值表示不计时。
type imageAPIStreamTiming struct {
	OnFirstEvent func()
	OnCompleted  func()
}

type imageAPIStreamEvent struct {
	Raw       []byte
	EventType string
	Data      []byte
}

type imageAPIResponseItem struct {
	B64JSON string `json:"b64_json"`
	URL     string `json:"url"`
}

type imageAPIResponseEnvelope struct {
	Data  []imageAPIResponseItem `json:"data"`
	Error json.RawMessage        `json:"error"`
}

type imageAPIStreamPayload struct {
	Type    string          `json:"type"`
	B64JSON string          `json:"b64_json"`
	Error   json.RawMessage `json:"error"`
}

type imageAPIStreamEventValidation struct {
	Recognized bool
	Terminal   bool
	Ignore     bool
}

func imageAPIRequestStreamEnabled(r *http.Request, bodyBytes []byte) bool {
	if r == nil {
		return false
	}
	if strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/event-stream") ||
		strings.EqualFold(strings.TrimSpace(r.Header.Get("stream")), "true") {
		return true
	}
	if r.URL != nil && r.URL.Path == openAIImagesEditsPath && isMultipartImageEditContentType(r.Header.Get("Content-Type")) {
		return imageEditMultipartStreamEnabled(bodyBytes, r.Header.Get("Content-Type"))
	}
	var payload struct {
		Stream bool `json:"stream"`
	}
	return json.Unmarshal(bodyBytes, &payload) == nil && payload.Stream
}

func readValidatedImageAPIJSONResponse(resp *http.Response) ([]byte, error) {
	if resp == nil || resp.Body == nil {
		return nil, fmt.Errorf("image upstream response is missing body")
	}
	if err := validateImageAPIResponseContentEncoding(resp); err != nil {
		return nil, err
	}
	mediaType := imageAPIResponseMediaType(resp.Header.Get("Content-Type"))
	if mediaType == "text/html" || mediaType == "application/xhtml+xml" {
		return nil, imageAPIProtocolError(resp, 0, "response Content-Type is HTML")
	}
	if mediaType == "text/event-stream" {
		return nil, imageAPIProtocolError(resp, 0, "received an event stream for a non-streaming request")
	}
	body, exceeded, err := readBoundedImageAPIResponse(resp.Body, imageAPIResponseMaxBytes)
	if err != nil {
		return nil, imageAPIProtocolError(resp, int64(len(body)), "read response body: "+err.Error())
	}
	if exceeded {
		return nil, imageAPIProtocolError(resp, int64(len(body)), fmt.Sprintf("response body exceeds %d bytes", imageAPIResponseMaxBytes))
	}
	if err := validateImageAPIJSONResponse(body); err != nil {
		return nil, imageAPIProtocolError(resp, int64(len(body)), err.Error())
	}
	return body, nil
}

func validateImageAPIJSONResponse(body []byte) error {
	if len(bytes.TrimSpace(body)) == 0 {
		return fmt.Errorf("response body is empty")
	}
	var envelope imageAPIResponseEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("response body is not valid JSON: %w", err)
	}
	if hasImageAPIError(envelope.Error) {
		return fmt.Errorf("response contains an error object")
	}
	if len(envelope.Data) == 0 {
		return fmt.Errorf("response data is empty")
	}
	for index, item := range envelope.Data {
		if validImageAPIBase64(item.B64JSON) || validImageAPIURL(item.URL) {
			continue
		}
		return fmt.Errorf("response data[%d] contains neither valid b64_json nor URL", index)
	}
	return nil
}

func relayValidatedImageAPIStream(w http.ResponseWriter, resp *http.Response, path string, timing imageAPIStreamTiming) (imageAPIStreamRelayResult, error) {
	result := imageAPIStreamRelayResult{}
	if resp == nil || resp.Body == nil {
		return result, fmt.Errorf("image upstream response is missing body")
	}
	if err := validateImageAPIResponseContentEncoding(resp); err != nil {
		return result, err
	}
	if imageAPIResponseMediaType(resp.Header.Get("Content-Type")) != "text/event-stream" {
		return result, imageAPIProtocolError(resp, 0, "streaming request did not return text/event-stream")
	}

	partialEvent, completedEvent, err := imageAPIStreamEventTypes(path)
	if err != nil {
		return result, err
	}
	reader := bufio.NewReaderSize(resp.Body, 32*1024)
	var preamble bytes.Buffer
	for {
		event, readErr := readImageAPIStreamEvent(reader)
		if readErr != nil {
			if readErr == io.EOF {
				return result, imageAPIProtocolError(resp, result.ResponseBytes, "event stream ended before a valid completed event")
			}
			return result, imageAPIProtocolError(resp, result.ResponseBytes, "read event stream: "+readErr.Error())
		}
		result.ResponseBytes += int64(len(event.Raw))

		validation, validateErr := validateImageAPIStreamEvent(event, partialEvent, completedEvent)
		if validateErr != nil {
			return result, imageAPIProtocolError(resp, result.ResponseBytes, validateErr.Error())
		}
		if validation.Ignore {
			continue
		}
		if !validation.Recognized {
			if result.Committed {
				if err := writeImageAPIStreamChunk(w, event.Raw); err != nil {
					result.WriteFailure = true
					return result, err
				}
				continue
			}
			if _, err := preamble.Write(event.Raw); err != nil {
				return result, err
			}
			if int64(preamble.Len()) > imageAPIStreamPreambleMaxBytes {
				return result, imageAPIProtocolError(resp, result.ResponseBytes, "event stream preamble exceeds validation limit")
			}
			continue
		}

		if !result.Committed {
			if timing.OnFirstEvent != nil {
				timing.OnFirstEvent()
			}
			copyImageAPIResponseHeaders(resp, w)
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("X-Accel-Buffering", "no")
			w.WriteHeader(http.StatusOK)
			result.Committed = true
			if preamble.Len() > 0 {
				if err := writeImageAPIStreamChunk(w, preamble.Bytes()); err != nil {
					result.WriteFailure = true
					return result, err
				}
			}
		}
		if err := writeImageAPIStreamChunk(w, event.Raw); err != nil {
			result.WriteFailure = true
			return result, err
		}
		if validation.Terminal {
			if timing.OnCompleted != nil {
				timing.OnCompleted()
			}
			return result, nil
		}
	}
}

func readImageAPIStreamEvent(reader *bufio.Reader) (imageAPIStreamEvent, error) {
	var event imageAPIStreamEvent
	var raw bytes.Buffer
	var data bytes.Buffer
	dataLineSeen := false
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			if int64(raw.Len()+len(line)) > imageAPIStreamEventMaxBytes {
				return imageAPIStreamEvent{}, fmt.Errorf("event exceeds %d bytes", imageAPIStreamEventMaxBytes)
			}
			raw.WriteString(line)
			trimmed := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
			if trimmed == "" {
				event.Raw = append([]byte(nil), raw.Bytes()...)
				event.Data = append([]byte(nil), data.Bytes()...)
				return event, nil
			}
			if strings.HasPrefix(trimmed, ":") {
				continue
			}
			fieldName, fieldValue, hasColon := strings.Cut(trimmed, ":")
			if !hasColon {
				fieldValue = ""
			} else if strings.HasPrefix(fieldValue, " ") {
				fieldValue = strings.TrimPrefix(fieldValue, " ")
			}
			switch fieldName {
			case "event":
				event.EventType = fieldValue
			case "data":
				if dataLineSeen {
					data.WriteByte('\n')
				}
				data.WriteString(fieldValue)
				dataLineSeen = true
			case "id", "retry":
			default:
				// SSE 规范要求忽略未知字段；原始行仍保留在 event.Raw 中供下游透传。
			}
		}
		if err != nil {
			if err == io.EOF && raw.Len() > 0 {
				event.Raw = append([]byte(nil), raw.Bytes()...)
				event.Data = append([]byte(nil), data.Bytes()...)
				return event, nil
			}
			return imageAPIStreamEvent{}, err
		}
	}
}

func validateImageAPIStreamEvent(event imageAPIStreamEvent, partialEvent, completedEvent string) (imageAPIStreamEventValidation, error) {
	validation := imageAPIStreamEventValidation{}
	if len(bytes.TrimSpace(event.Data)) == 0 {
		return validation, nil
	}
	eventType := strings.TrimSpace(event.EventType)
	if bytes.Equal(bytes.TrimSpace(event.Data), []byte("[DONE]")) && !isKnownImageAPIStreamEventType(eventType, partialEvent, completedEvent) {
		validation.Ignore = true
		return validation, nil
	}
	var payload imageAPIStreamPayload
	if err := json.Unmarshal(event.Data, &payload); err != nil {
		if isKnownImageAPIStreamEventType(eventType, partialEvent, completedEvent) {
			return validation, fmt.Errorf("event data is not valid JSON: %w", err)
		}
		return validation, nil
	}
	payloadType := strings.TrimSpace(payload.Type)
	if eventType != "" && payloadType != "" && eventType != payloadType {
		return validation, fmt.Errorf("event type %q does not match payload type %q", eventType, payloadType)
	}
	if payloadType == "" {
		payloadType = eventType
	}
	if payloadType == "error" || hasImageAPIError(payload.Error) {
		return validation, fmt.Errorf("event stream contains an error event")
	}
	switch payloadType {
	case partialEvent:
		if !validImageAPIBase64(payload.B64JSON) {
			return validation, fmt.Errorf("partial image event contains invalid b64_json")
		}
		validation.Recognized = true
		return validation, nil
	case completedEvent:
		if !validImageAPIBase64(payload.B64JSON) {
			return validation, fmt.Errorf("completed image event contains invalid b64_json")
		}
		validation.Recognized = true
		validation.Terminal = true
		return validation, nil
	default:
		return validation, nil
	}
}

func isKnownImageAPIStreamEventType(eventType, partialEvent, completedEvent string) bool {
	return eventType == partialEvent || eventType == completedEvent || eventType == "error"
}

func imageAPIStreamEventTypes(path string) (string, string, error) {
	switch path {
	case openAIImagesGenerationsPath:
		return "image_generation.partial_image", "image_generation.completed", nil
	case openAIImagesEditsPath:
		return "image_edit.partial_image", "image_edit.completed", nil
	default:
		return "", "", fmt.Errorf("unsupported image API path: %s", path)
	}
}

func writeImageAPIStreamChunk(w http.ResponseWriter, chunk []byte) error {
	if len(chunk) == 0 {
		return nil
	}
	if _, err := w.Write(chunk); err != nil {
		return err
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

func writeImageAPIStreamError(w http.ResponseWriter, code, message string) {
	payload, _ := json.Marshal(map[string]any{
		"type": "error",
		"error": map[string]string{
			"type":    "image_generation_error",
			"code":    code,
			"message": message,
		},
	})
	_, _ = fmt.Fprintf(w, "event: error\ndata: %s\n\n", payload)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func writeImageAPIUpstreamFailure(w http.ResponseWriter, resp *http.Response) (int, string, error) {
	status := http.StatusBadGateway
	if resp != nil && resp.StatusCode >= http.StatusBadRequest {
		status = resp.StatusCode
	}
	if resp == nil || resp.Body == nil {
		writeImageGenerationError(w, status, "image_generation_upstream_error", "image upstream returned an invalid response")
		return status, "image upstream response is missing body", nil
	}
	body, exceeded, readErr := readBoundedImageAPIResponse(resp.Body, imageAPIErrorResponseMaxBytes)
	reason := summarizeImageGenerationUpstreamError(body, resp.StatusCode)
	if readErr == nil && !exceeded && isImageAPIJSONObject(body) {
		copyImageAPIResponseHeaders(resp, w)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, err := w.Write(body)
		return status, reason, err
	}
	writeImageGenerationError(w, status, "image_generation_upstream_error", reason)
	return status, reason, nil
}

func isImageAPIJSONObject(body []byte) bool {
	var payload map[string]json.RawMessage
	return json.Unmarshal(body, &payload) == nil && payload != nil
}

func copyImageAPIResponseHeaders(resp *http.Response, w http.ResponseWriter) {
	if resp == nil {
		return
	}
	for key, values := range resp.Header {
		lowerKey := strings.ToLower(key)
		allowed := lowerKey == "openai-request-id" || lowerKey == "request-id" || lowerKey == "x-request-id" ||
			lowerKey == "openai-processing-ms" || lowerKey == "openai-version" || lowerKey == "retry-after" ||
			strings.HasPrefix(lowerKey, "x-ratelimit-")
		if !allowed {
			continue
		}
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
}

func readBoundedImageAPIResponse(body io.Reader, limit int64) ([]byte, bool, error) {
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return data, false, err
	}
	if int64(len(data)) > limit {
		return data[:limit], true, nil
	}
	return data, false, nil
}

func imageAPIResponseMediaType(contentType string) string {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(contentType))
	if err == nil {
		return strings.ToLower(mediaType)
	}
	if index := strings.IndexByte(contentType, ';'); index >= 0 {
		contentType = contentType[:index]
	}
	return strings.ToLower(strings.TrimSpace(contentType))
}

func validateImageAPIResponseContentEncoding(resp *http.Response) error {
	if resp == nil {
		return fmt.Errorf("image upstream response is missing")
	}
	values := resp.Header.Values("Content-Encoding")
	for _, value := range values {
		for _, encoding := range strings.Split(value, ",") {
			encoding = strings.TrimSpace(encoding)
			if encoding == "" || strings.EqualFold(encoding, "identity") {
				continue
			}
			return imageAPIProtocolError(resp, 0, fmt.Sprintf("unsupported Content-Encoding %q", strings.Join(values, ", ")))
		}
	}
	return nil
}

func imageAPIProtocolError(resp *http.Response, responseBytes int64, reason string) error {
	status := 0
	contentType := ""
	if resp != nil {
		status = resp.StatusCode
		contentType = resp.Header.Get("Content-Type")
		if responseBytes == 0 && resp.ContentLength > 0 {
			responseBytes = resp.ContentLength
		}
	}
	return fmt.Errorf("upstream_status=%d content_type=%q response_bytes=%d reason=%s", status, contentType, responseBytes, reason)
}

func hasImageAPIError(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) && !bytes.Equal(trimmed, []byte("{}"))
}

func validImageAPIBase64(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if _, err := io.Copy(io.Discard, base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(value))); err == nil {
		return true
	}
	_, err := io.Copy(io.Discard, base64.NewDecoder(base64.RawStdEncoding.Strict(), strings.NewReader(value)))
	return err == nil
}

func validImageAPIURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https")
}
