package response

import "strings"

// StopReason 从已解码的非流式响应提取结束原因，不重复解析响应体。
func StopReason(payload map[string]interface{}) string {
	if reason, _ := payload["stop_reason"].(string); reason != "" {
		return reason
	}
	return ResponsesStopReason("", payload)
}

// ResponsesStopReason 供流式与非流式共用；只有流式终态事件可补足缺失的 status。
func ResponsesStopReason(event string, payload map[string]interface{}) string {
	response, ok := payload["response"].(map[string]interface{})
	if !ok {
		response = payload
	}
	status, _ := response["status"].(string)
	if status == "" {
		status = strings.TrimPrefix(event, "response.")
		if status == "done" {
			status = "completed"
		}
	}
	if status == "incomplete" {
		details, _ := response["incomplete_details"].(map[string]interface{})
		if reason, _ := details["reason"].(string); reason != "" {
			return status + ":" + reason
		}
	}
	return status
}
