package tracking

import "sinuo/internal/tokenusage"

// CacheWriteDurations 保留 Responses 上游报告的写入明细，nil 表示该时长未报告。
type CacheWriteDurations struct {
	CacheCreation30mTokens *int64 `json:"cache_creation_30m_tokens,omitempty"`
	CacheCreation12hTokens *int64 `json:"cache_creation_12h_tokens,omitempty"`
}

func (w CacheWriteDurations) clone() CacheWriteDurations {
	return CacheWriteDurations{CacheCreation30mTokens: copyInt64Ptr(w.CacheCreation30mTokens), CacheCreation12hTokens: copyInt64Ptr(w.CacheCreation12hTokens)}
}

// IncludesCacheInInput 优先保留解析器确认的语义，缺少标志时按请求路径识别。
func (u TokenUsage) IncludesCacheInInput(requestPath string) bool {
	return u.InputIncludesCache || tokenusage.InputIncludesCache("", requestPath)
}

func (u TokenUsage) billableInputTokens(includesCache, separateWrites bool) int64 {
	if !includesCache {
		return u.InputTokens
	}
	input := u.InputTokens - u.CacheReadTokens
	if separateWrites {
		input -= u.CacheCreationTokens
	}
	return max(input, 0)
}

func (u TokenUsage) claudeCacheWriteTokens() (fiveMinutes, oneHour int64) {
	if u.CacheCreation5mTokens > 0 || u.CacheCreation1hTokens > 0 {
		return u.CacheCreation5mTokens, u.CacheCreation1hTokens
	}
	// 旧用量只报告总数时按 5m 计费。
	return u.CacheCreationTokens, 0
}

// requestPathForUsage 从现有请求记录获取路径，不另建协议状态。
func (ut *UsageTracker) requestPathForUsage(requestID string) string {
	if ut.hotPoolEnabled && ut.hotPool != nil {
		if req, ok := ut.hotPool.SnapshotRequest(requestID); ok && req.Path != "" {
			return req.Path
		}
	}
	return ut.lookupRequestPath(requestID)
}

// cacheCreationDurations 只为 Claude 旧用量补 5m，保留上游明确报告的时长分项。
func (ut *UsageTracker) cacheCreationDurations(requestID string, usage *TokenUsage) (int64, int64) {
	fiveMinutes, oneHour := usage.CacheCreation5mTokens, usage.CacheCreation1hTokens
	if fiveMinutes != 0 || oneHour != 0 || usage.CacheCreationTokens <= 0 || usage.InputIncludesCache {
		return fiveMinutes, oneHour
	}
	if !usage.IncludesCacheInInput(ut.requestPathForUsage(requestID)) {
		fiveMinutes = usage.CacheCreationTokens
	}
	return fiveMinutes, oneHour
}
