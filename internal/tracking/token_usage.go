package tracking

import "sinuo/internal/tokenusage"

// IncludesCacheInInput 优先保留解析器确认的语义，缺少标志时按请求路径识别。
func (u TokenUsage) IncludesCacheInInput(requestPath string) bool {
	return u.InputIncludesCache || tokenusage.InputIncludesCache("", requestPath)
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
