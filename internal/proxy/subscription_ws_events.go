package proxy

import "strings"

// subscriptionWSControlEvent 集中声明不新建生成请求的客户端控制事件。
// interrupt 只中断已有响应；steer 追加输入，可能触发新的生成，排空策略因此不同。
func subscriptionWSControlEvent(eventType string) bool {
	switch eventType {
	case "response.steer", "response.interrupt":
		return true
	default:
		return false
	}
}

func subscriptionWSControlReceipt(eventType string) bool {
	separator := strings.LastIndexByte(eventType, '.')
	return separator > 0 && subscriptionWSControlEvent(eventType[:separator])
}

// subscriptionWSClientEventType 在 JSON 歧义校验后读取精确的 type 字段。
// 只按事件类型决定代理能力；具体请求参数和目标响应是否支持控制操作由上游校验。
func subscriptionWSClientEventType(payload []byte) string {
	raw, _ := jsonObjectField(payload, "type")
	return jsonStringValue(raw)
}

// subscriptionWSEventLogType 只记录有界的协议标识，拒绝控制符和超长值，不携带帧正文。
func subscriptionWSEventLogType(eventType string) string {
	if eventType == "" {
		return "missing"
	}
	if len(eventType) > 64 {
		return "invalid"
	}
	for _, c := range eventType {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.' || c == '_' || c == '-' {
			continue
		}
		return "invalid"
	}
	return eventType
}
