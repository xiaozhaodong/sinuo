package proxy

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

type subscriptionWSRoute struct {
	typ, responseID, streamID string
}

// 普通事件、错误和控制回执采用同一规则：路由字段必须规范，所有显式响应 ID 必须一致。
// 调用方负责校验完整语法与重复键；此处不展开非路由字段，复用已解码的观测对象。
func canonicalSubscriptionWSRoute(m map[string]any) (route subscriptionWSRoute, valid bool) {
	defer func() {
		if !valid {
			// 不暴露 map 遍历失败前碰巧读到的部分归属，错误事件也不能使用它结算。
			route.responseID, route.streamID = "", ""
		}
	}()
	if m == nil {
		return route, false
	}
	readString := func(value any, target *string) bool {
		if value == nil {
			return true
		}
		s, ok := value.(string)
		if !ok || *target != "" && s != "" && *target != s {
			return false
		}
		if s != "" {
			*target = s
		}
		return true
	}
	if !readString(m["type"], &route.typ) {
		return route, false
	}
	route.typ = strings.TrimSpace(route.typ)
	for key, value := range m {
		switch key {
		case "type":
		case "response_id":
			if !readString(value, &route.responseID) {
				return route, false
			}
		case "stream_id":
			if !readString(value, &route.streamID) {
				return route, false
			}
		case "response", "steer":
			if key == "steer" && !subscriptionWSControlReceipt(route.typ) {
				continue
			}
			if value == nil {
				continue
			}
			nested, ok := value.(map[string]any)
			if !ok {
				return route, false
			}
			field := "id"
			if key == "steer" {
				field = "previous_response_id"
			}
			for name, v := range nested {
				if name == field {
					if !readString(v, &route.responseID) {
						return route, false
					}
				} else if strings.EqualFold(name, field) {
					return route, false
				}
			}
		default:
			if strings.EqualFold(key, "type") || strings.EqualFold(key, "response_id") || strings.EqualFold(key, "response") ||
				strings.EqualFold(key, "stream_id") || subscriptionWSControlReceipt(route.typ) && strings.EqualFold(key, "steer") {
				return route, false
			}
		}
	}
	return route, true
}

// 只放宽本地回填深度限制；语法、重复键与路由歧义仍严格拒绝。
func decodeSubscriptionWSRoute(raw []byte) (subscriptionWSRoute, bool) {
	if !utf8.Valid(raw) || validateSubscriptionJSON(raw) != nil {
		return subscriptionWSRoute{}, false
	}
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if dec.Decode(&m) != nil {
		return subscriptionWSRoute{}, false
	}
	return canonicalSubscriptionWSRoute(m)
}

// 回执缺少显式响应 ID 时，放行预判与实际写侧采用同一个唯一归属规则。
func (s *subscriptionSubmissionState) controlRouteID(route subscriptionWSRoute) string {
	if route.responseID != "" {
		return route.responseID
	}
	return s.controlResponseID(route.streamID)
}
