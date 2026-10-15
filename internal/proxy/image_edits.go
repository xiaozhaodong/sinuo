package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"

	"sinuo/internal/privacy"
	"sinuo/internal/proxy/handlers"
)

type imageEditMultipartPart struct {
	Header   textproto.MIMEHeader
	Name     string
	Filename string
	Data     []byte
}

type parsedImageEditMultipart struct {
	Boundary string
	Parts    []imageEditMultipartPart
	Prompt   string
	Model    string
	Stream   string
	Images   int
}

func prepareImageEditRequestBody(bodyBytes []byte, contentType, defaultModel string) (preparedImageAPIRequest, error) {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(contentType))
	if err != nil {
		return preparedImageAPIRequest{}, fmt.Errorf("image edit Content-Type is invalid: %w", err)
	}

	switch strings.ToLower(mediaType) {
	case "application/json":
		preparedBody, model, err := prepareImageGenerationRequestBody(bodyBytes, defaultModel)
		if err != nil {
			return preparedImageAPIRequest{}, err
		}
		if err := validateJSONImageEditReferences(preparedBody); err != nil {
			return preparedImageAPIRequest{}, err
		}
		return preparedImageAPIRequest{Body: preparedBody, ContentType: contentType, Model: model}, nil
	case "multipart/form-data":
		metadata, err := inspectImageEditMultipartBody(bodyBytes, contentType)
		if err != nil {
			return preparedImageAPIRequest{}, err
		}
		model := strings.TrimSpace(metadata.Model)
		if model == "" {
			model = strings.TrimSpace(defaultModel)
		}
		if model == "" {
			model = "gpt-image-2"
		}
		if strings.TrimSpace(metadata.Model) != "" {
			return preparedImageAPIRequest{Body: bodyBytes, ContentType: contentType, Model: model}, nil
		}
		parsed, err := parseImageEditMultipartBody(bodyBytes, contentType)
		if err != nil {
			return preparedImageAPIRequest{}, err
		}
		preparedBody, err := rebuildImageEditMultipartBody(parsed, parsed.Prompt, model)
		if err != nil {
			return preparedImageAPIRequest{}, err
		}
		return preparedImageAPIRequest{Body: preparedBody, ContentType: contentType, Model: model}, nil
	default:
		return preparedImageAPIRequest{}, fmt.Errorf("image edit Content-Type must be application/json or multipart/form-data")
	}
}

func validateJSONImageEditReferences(bodyBytes []byte) error {
	var request struct {
		Images []json.RawMessage `json:"images"`
	}
	if err := json.Unmarshal(bodyBytes, &request); err != nil {
		return fmt.Errorf("request body must be valid JSON: %w", err)
	}
	if len(request.Images) == 0 {
		return fmt.Errorf("images is required for image edits")
	}
	return nil
}

// imageEditRequestModel 返回客户端显式给出的原始模型：multipart 读表单 model 字段，其他情况沿用 JSON 解析结果。
// multipart 解析失败时返回空串，具体错误由后续 prepareImageEditRequestBody 统一报出。
func imageEditRequestModel(bodyBytes []byte, contentType, jsonModel string) string {
	if !isMultipartImageEditContentType(contentType) {
		return jsonModel
	}
	metadata, err := inspectImageEditMultipartBody(bodyBytes, contentType)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(metadata.Model)
}

func isMultipartImageEditContentType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(contentType))
	return err == nil && strings.EqualFold(mediaType, "multipart/form-data")
}

func parseImageEditMultipartBody(bodyBytes []byte, contentType string) (parsedImageEditMultipart, error) {
	return readImageEditMultipartBody(bodyBytes, contentType, true)
}

func inspectImageEditMultipartBody(bodyBytes []byte, contentType string) (parsedImageEditMultipart, error) {
	return readImageEditMultipartBody(bodyBytes, contentType, false)
}

func readImageEditMultipartBody(bodyBytes []byte, contentType string, captureParts bool) (parsedImageEditMultipart, error) {
	mediaType, params, err := mime.ParseMediaType(strings.TrimSpace(contentType))
	if err != nil || !strings.EqualFold(mediaType, "multipart/form-data") || strings.TrimSpace(params["boundary"]) == "" {
		return parsedImageEditMultipart{}, fmt.Errorf("image edit multipart request requires a valid boundary")
	}
	parsed := parsedImageEditMultipart{Boundary: params["boundary"]}
	reader := multipart.NewReader(bytes.NewReader(bodyBytes), parsed.Boundary)
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return parsedImageEditMultipart{}, fmt.Errorf("read image edit multipart body: %w", err)
		}
		name := part.FormName()
		filename := part.FileName()
		if filename != "" && (name == "image" || name == "image[]") {
			parsed.Images++
		}
		if captureParts {
			data, err := io.ReadAll(part)
			if err != nil {
				return parsedImageEditMultipart{}, fmt.Errorf("read image edit multipart part: %w", err)
			}
			parsed.Parts = append(parsed.Parts, imageEditMultipartPart{
				Header:   cloneMIMEHeader(part.Header),
				Name:     name,
				Filename: filename,
				Data:     data,
			})
			if filename == "" {
				setImageEditMultipartField(&parsed, name, data)
			}
		} else if filename == "" && (name == "prompt" || name == "model" || name == "stream") {
			data, err := io.ReadAll(part)
			if err != nil {
				return parsedImageEditMultipart{}, fmt.Errorf("read image edit multipart field: %w", err)
			}
			setImageEditMultipartField(&parsed, name, data)
		}
	}
	if strings.TrimSpace(parsed.Prompt) == "" {
		return parsedImageEditMultipart{}, fmt.Errorf("prompt is required")
	}
	if parsed.Images == 0 {
		return parsedImageEditMultipart{}, fmt.Errorf("at least one image or image[] file is required for image edits")
	}
	return parsed, nil
}

func setImageEditMultipartField(parsed *parsedImageEditMultipart, name string, data []byte) {
	if parsed == nil {
		return
	}
	switch name {
	case "prompt":
		parsed.Prompt = string(data)
	case "model":
		parsed.Model = string(data)
	case "stream":
		parsed.Stream = string(data)
	}
}

func rebuildImageEditMultipartBody(parsed parsedImageEditMultipart, prompt, model string) ([]byte, error) {
	var output bytes.Buffer
	writer := multipart.NewWriter(&output)
	if err := writer.SetBoundary(parsed.Boundary); err != nil {
		return nil, fmt.Errorf("reuse image edit multipart boundary: %w", err)
	}
	promptWritten := false
	modelWritten := false
	for _, sourcePart := range parsed.Parts {
		partWriter, err := writer.CreatePart(cloneMIMEHeader(sourcePart.Header))
		if err != nil {
			return nil, fmt.Errorf("create image edit multipart part: %w", err)
		}
		data := sourcePart.Data
		if sourcePart.Filename == "" {
			switch sourcePart.Name {
			case "prompt":
				data = []byte(prompt)
				promptWritten = true
			case "model":
				data = []byte(model)
				modelWritten = true
			}
		}
		if _, err := partWriter.Write(data); err != nil {
			return nil, fmt.Errorf("write image edit multipart part: %w", err)
		}
	}
	if !promptWritten {
		field, err := writer.CreateFormField("prompt")
		if err != nil {
			return nil, fmt.Errorf("create image edit prompt field: %w", err)
		}
		if _, err := field.Write([]byte(prompt)); err != nil {
			return nil, fmt.Errorf("write image edit prompt field: %w", err)
		}
	}
	if !modelWritten {
		field, err := writer.CreateFormField("model")
		if err != nil {
			return nil, fmt.Errorf("create image edit model field: %w", err)
		}
		if _, err := field.Write([]byte(model)); err != nil {
			return nil, fmt.Errorf("write image edit model field: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close image edit multipart body: %w", err)
	}
	return output.Bytes(), nil
}

func cloneMIMEHeader(source textproto.MIMEHeader) textproto.MIMEHeader {
	cloned := make(textproto.MIMEHeader, len(source))
	for key, values := range source {
		cloned[key] = append([]string(nil), values...)
	}
	return cloned
}

func (h *Handler) applyMultipartImageEditPrivacyFilter(r *http.Request, bodyBytes []byte, contentType, providerName string) ([]byte, error) {
	metadata, err := inspectImageEditMultipartBody(bodyBytes, contentType)
	if err != nil {
		return nil, err
	}
	promptBody, err := json.Marshal(map[string]string{"prompt": metadata.Prompt})
	if err != nil {
		return nil, fmt.Errorf("encode image edit prompt for privacy scan: %w", err)
	}
	request := privacy.Request{
		Path:         openAIImagesEditsPath,
		Method:       http.MethodPost,
		UpstreamType: privacy.UpstreamTypeEndpoint,
		EndpointName: providerName,
		ContentType:  "application/json",
	}
	filteredBody, err := handlers.ApplyPrivacyFilter(h.privacyFilter, r, request, promptBody)
	if err != nil {
		return nil, err
	}
	var filtered struct {
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(filteredBody, &filtered); err != nil {
		return nil, fmt.Errorf("decode privacy-filtered image edit prompt: %w", err)
	}
	if filtered.Prompt == metadata.Prompt {
		return bodyBytes, nil
	}
	parsed, err := parseImageEditMultipartBody(bodyBytes, contentType)
	if err != nil {
		return nil, err
	}
	return rebuildImageEditMultipartBody(parsed, filtered.Prompt, metadata.Model)
}

func imageEditMultipartStreamEnabled(bodyBytes []byte, contentType string) bool {
	parsed, err := inspectImageEditMultipartBody(bodyBytes, contentType)
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(parsed.Stream), "true")
}
