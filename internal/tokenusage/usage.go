// Package tokenusage 统一不同协议的输入与缓存包含关系。
package tokenusage

import "strings"

// InputIncludesCache 判断输入总量是否已经包含缓存读写。
// 优先使用已明确的请求族；历史记录缺少请求族时，按 Responses 路径识别。
func InputIncludesCache(requestFamily, requestPath string) bool {
	switch strings.ToLower(strings.TrimSpace(requestFamily)) {
	case "codex":
		return true
	case "claude":
		return false
	}
	path := strings.ToLower(strings.TrimSpace(requestPath))
	return strings.HasSuffix(path, "/responses") || strings.HasSuffix(path, "/responses/compact")
}

// TotalTokens 返回去重后的 Token 总量；缓存读写始终保留为独立的统计明细。
func TotalTokens(requestFamily, requestPath string, input, output, creation, read int64) int64 {
	total := input + output
	if !InputIncludesCache(requestFamily, requestPath) {
		total += creation + read
	}
	return total
}

// inputIncludesCacheSQL 是所有 SQL 用量计算的协议识别规则，与 InputIncludesCache 对齐。
const inputIncludesCacheSQL = `CASE
	WHEN LOWER(TRIM(COALESCE(request_family, ''))) = 'codex' THEN 1
	WHEN LOWER(TRIM(COALESCE(request_family, ''))) = 'claude' THEN 0
	WHEN LOWER(TRIM(COALESCE(path, ''))) LIKE '%/responses'
		OR LOWER(TRIM(COALESCE(path, ''))) LIKE '%/responses/compact' THEN 1
	ELSE 0 END`

// FullInputSQL 保留字段级 NULL；缺少必需分项时不能把未知输入当成真实零。
// 与 TotalTokensSQL 使用相同的协议识别规则，创建总数优先于 5m/1h 分项。
const FullInputSQL = `input_tokens + CASE WHEN (` + inputIncludesCacheSQL + `) = 1 THEN 0
	ELSE COALESCE(cache_creation_tokens, cache_creation_5m_tokens + cache_creation_1h_tokens) + cache_read_tokens END`

// TotalTokensSQL 用于 request_logs 的查询，与 TotalTokens 保持相同口径。
// 这里只引用固定列名，不接收或拼接外部输入；NULL 表示未上报，不贡献已知总量。
const TotalTokensSQL = `COALESCE(input_tokens, 0) + COALESCE(output_tokens, 0) +
	CASE WHEN (` + inputIncludesCacheSQL + `) = 1 THEN 0
		ELSE COALESCE(cache_creation_tokens, 0) + COALESCE(cache_read_tokens, 0)
	END`
