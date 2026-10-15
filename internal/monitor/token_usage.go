package monitor

import "sinuo/internal/tokenusage"

// SetProtocol 标记本次用量中已经包含在输入总量里的缓存，供混合协议汇总去重。
// 在用量交给 Metrics 之前调用；它不修改上游报告的四项原始计数。
func (u *TokenUsage) SetProtocol(requestFamily, requestPath string) {
	if u == nil {
		return
	}
	u.cacheTokensInInput = 0
	if tokenusage.InputIncludesCache(requestFamily, requestPath) {
		u.cacheTokensInInput = u.CacheCreationTokens + u.CacheReadTokens
	}
}

// TotalTokens 对单条用量及混合协议汇总使用相同的去重口径。
func (u TokenUsage) TotalTokens() int64 {
	return u.InputTokens + u.OutputTokens + u.CacheCreationTokens + u.CacheReadTokens - u.cacheTokensInInput
}

func (u *TokenUsage) add(other TokenUsage) {
	u.InputTokens += other.InputTokens
	u.OutputTokens += other.OutputTokens
	u.CacheCreationTokens += other.CacheCreationTokens
	u.CacheReadTokens += other.CacheReadTokens
	u.cacheTokensInInput += other.cacheTokensInInput
}
