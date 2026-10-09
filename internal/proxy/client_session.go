package proxy

import (
	"context"
	"net/http"
	"strings"
)

// clientSessionID 只读取客户端原始会话；订阅池剥离请求头后仍可从 association 取得。
func clientSessionID(r *http.Request) string {
	if r == nil {
		return ""
	}
	if sessionID := clientSessionFromContext(r.Context()); sessionID != "" {
		return sessionID
	}
	for _, name := range []string{"X-Claude-Code-Session-Id", "session-id", "session_id", "thread-id"} {
		values := r.Header.Values(name)
		if len(values) != 1 {
			continue
		}
		if sessionID := normalizeClientSessionID(values[0]); sessionID != "" {
			return sessionID
		}
	}
	return ""
}

func clientSessionFromContext(ctx context.Context) string {
	if info := subscriptionRequestInfoFromContext(ctx); info != nil {
		for _, value := range []string{info.Association.SessionID, info.Association.ThreadID} {
			if sessionID := normalizeClientSessionID(value); sessionID != "" {
				return sessionID
			}
		}
	}
	return ""
}

func normalizeClientSessionID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 128 {
		return ""
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 0x21 || value[i] > 0x7e {
			return ""
		}
	}
	return value
}
