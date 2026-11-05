package proxy

import (
	"bytes"
	"encoding/json"
	"strings"

	"sinuo/internal/tracking"

	"github.com/tidwall/gjson"
)

// subscriptionHTTPTrackingKind 集中定义需要登记的订阅 HTTP 业务请求。
// generation 是计入调用次数的业务请求，具体工具类型沿用 family/path 区分。
func subscriptionHTTPTrackingKind(kind subscriptionRouteKind) (requestKind, family string, tool bool) {
	switch kind {
	case subscriptionRouteResponses:
		return tracking.RequestKindGeneration, tracking.RequestFamilyCodex, false
	case subscriptionRouteResponsesCompact:
		return tracking.RequestKindCompaction, tracking.RequestFamilyCodex, false
	case subscriptionRouteSearch:
		return tracking.RequestKindGeneration, tracking.RequestFamilyCodex, true
	case subscriptionRouteImagesGenerations, subscriptionRouteImagesEdits:
		return tracking.RequestKindGeneration, tracking.RequestFamilyImage, true
	default:
		return "", "", false
	}
}

// subscriptionToolResponse 只观察工具响应的模型和结束状态，不改变直通协议。
// 订阅图片/搜索没有本地渠道的固定价；也不能用文本模型价格推算工具用量和费用。
type subscriptionToolResponse struct {
	path     string
	track    *subscriptionRequestTracking
	terminal bool
	failed   bool
	parsed   bool
}

func (s *subscriptionToolResponse) parsePayload(event string, payload []byte) *ParseResult {
	event = strings.TrimSpace(event)
	payload = bytes.TrimSpace(payload)
	if !json.Valid(payload) {
		return nil
	}
	if event == "" {
		event = gjson.GetBytes(payload, "type").String()
	}
	upstreamError := gjson.GetBytes(payload, "error")
	if upstreamError.Exists() && upstreamError.Type != gjson.Null {
		event = "error"
	}
	result := s.parseEvent(event)
	model := gjson.GetBytes(payload, "model")
	if model.Type == gjson.String {
		result.ModelName = safeSubscriptionModel(model.String())
		result.ResponseModel = result.ModelName
	}
	return result
}

func (s *subscriptionToolResponse) parseEvent(event string) *ParseResult {
	result := &ParseResult{}
	if event == "error" {
		result.IsCompleted, result.Status = true, StatusErrorAPI
		return result
	}
	if event == "image_generation.completed" && s.path == openAIImagesGenerationsPath ||
		event == "image_edit.completed" && s.path == openAIImagesEditsPath {
		result.IsCompleted, result.Status = true, "completed"
	}
	return result
}

func (s *subscriptionToolResponse) observe(result *ParseResult) {
	if result == nil {
		return
	}
	s.parsed = true
	s.track.SetModel(result.ModelName)
	s.track.MergeResponseFields(result.ResponseModel, result.ResponseReasoningFields)
	if result.IsCompleted {
		s.terminal = true
		s.failed = s.failed || result.Status == StatusErrorAPI
	}
}

func (s *subscriptionToolResponse) finish(httpStatus int, mode string, copyErr, contextErr error) {
	status, reason := "completed", ""
	switch {
	case httpStatus >= 400:
		status, reason = "failed", "subscription_http_error"
	case s.failed:
		status, reason = "failed", "subscription_response_failed"
	case s.terminal:
		// 已观察到工具终态，尾部取消不覆盖成功状态，与 Responses 一致。
	case copyErr != nil && contextErr != nil:
		status, reason = "cancelled", "subscription_client_closed"
	case copyErr != nil:
		status, reason = "failed", "subscription_response_interrupted"
	case mode == "sse" && (s.path == openAIImagesGenerationsPath || s.path == openAIImagesEditsPath):
		status, reason = "failed", "subscription_response_incomplete"
	case mode == "json" && !s.parsed:
		status, reason = "failed", "subscription_response_invalid"
	}
	s.track.settle(status, reason, nil, &httpStatus)
}
