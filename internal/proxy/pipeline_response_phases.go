package proxy

import "encoding/json"

func regularStopReason(data []byte) string {
	var payload map[string]interface{}
	if json.Unmarshal(data, &payload) != nil {
		return ""
	}
	if reason, _ := payload["stop_reason"].(string); reason != "" {
		return reason
	}
	if status, _ := payload["status"].(string); status != "" {
		return responseStopReason("", payload)
	}
	if response, ok := payload["response"].(map[string]interface{}); ok {
		if status, _ := response["status"].(string); status != "" {
			return responseStopReason("", payload)
		}
	}
	return ""
}
