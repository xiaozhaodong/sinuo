package proxy

import (
	"encoding/json"

	"sinuo/internal/reasoning"
)

type subscriptionCreate struct {
	Generate           bool
	Model              string
	Reasoning          string // 请求思考与速度档位，外层及嵌套 response 均可提供
	PreviousResponseID string
	StreamID           string
}

type subscriptionReasoningField struct {
	Effort string `json:"effort"`
}

func (f subscriptionReasoningField) normalized(speed string) string {
	return reasoning.ParseCodexRequest(map[string]any{
		"reasoning": map[string]any{"effort": f.Effort}, "service_tier": speed,
	})
}

type subscriptionCreateFields struct {
	Generate           *bool                      `json:"generate"`
	Model              string                     `json:"model"`
	Reasoning          subscriptionReasoningField `json:"reasoning"`
	ServiceTier        string                     `json:"service_tier"`
	PreviousResponseID string                     `json:"previous_response_id"`
	StreamID           string                     `json:"stream_id"`
}

func parseSubscriptionCreate(payload []byte) (subscriptionCreate, bool) {
	var envelope struct {
		Type string `json:"type"`
		subscriptionCreateFields
		Response json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil || envelope.Type != "response.create" {
		return subscriptionCreate{}, false
	}
	fields := envelope.subscriptionCreateFields
	if len(envelope.Response) > 0 && string(envelope.Response) != "null" {
		var nested subscriptionCreateFields
		if json.Unmarshal(envelope.Response, &nested) == nil {
			fields.merge(nested)
		}
	}
	return subscriptionCreate{
		Generate:           fields.Generate == nil || *fields.Generate,
		Model:              fields.Model,
		Reasoning:          fields.Reasoning.normalized(fields.ServiceTier),
		PreviousResponseID: fields.PreviousResponseID,
		StreamID:           fields.StreamID,
	}, true
}

// merge 只用嵌套对象中明确提供的字段覆盖外层，思考与速度各自保留缺省值。
func (f *subscriptionCreateFields) merge(nested subscriptionCreateFields) {
	if nested.Generate != nil {
		f.Generate = nested.Generate
	}
	if nested.Model != "" {
		f.Model = nested.Model
	}
	if nested.Reasoning.normalized("") != "" {
		f.Reasoning = nested.Reasoning
	}
	if reasoning.ParseCodexRequest(map[string]any{"service_tier": nested.ServiceTier}) != "" {
		f.ServiceTier = nested.ServiceTier
	}
	if nested.PreviousResponseID != "" {
		f.PreviousResponseID = nested.PreviousResponseID
	}
	if nested.StreamID != "" {
		f.StreamID = nested.StreamID
	}
}
