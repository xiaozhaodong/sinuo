package handlers

import (
	"net/http"
	"strings"
)

// 上游请求ID优先级由流阶段观测约定；不影响响应头转发白名单。
var upstreamRequestIDHeaders = [...]string{
	"request-id", "anthropic-request-id", "x-request-id", "openai-request-id", "x-oneapi-request-id", "cf-ray",
}

// UpstreamRequestID 按统一优先级取最终响应的请求ID，缺失时返回空串。
func UpstreamRequestID(header http.Header) string {
	for _, name := range upstreamRequestIDHeaders {
		if value := strings.TrimSpace(header.Get(name)); value != "" {
			return value
		}
	}
	return ""
}
