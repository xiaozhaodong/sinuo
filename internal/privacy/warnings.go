package privacy

import "encoding/json"

// 异常码只描述处理阶段和结果，不携带正文、匹配值或解析器错误。
const (
	WarningRequestFailOpen  = "request_fail_open"
	WarningRequestFailed    = "request_failed"
	WarningResponseBypassed = "response_bypassed"
	WarningResponseFailed   = "response_failed"
)

// MergeWarnings 保持固定顺序并去重；未知码不能进入列表或通知。
func MergeWarnings(groups ...[]string) []string {
	seen := make(map[string]bool, 4)
	for _, group := range groups {
		for _, code := range group {
			seen[code] = true
		}
	}
	var result []string
	for _, code := range []string{WarningRequestFailOpen, WarningRequestFailed, WarningResponseBypassed, WarningResponseFailed} {
		if seen[code] {
			result = append(result, code)
		}
	}
	return result
}

// RequestWarnings 区分处理失败和正常规则拦截/客户端格式错误。
func RequestWarnings(result ApplyResult, rejection *PolicyError) []string {
	if rejection != nil {
		switch rejection.Code {
		case CodeScanFailed, CodeScanBodyTooLarge, codeRestoreRejected:
			return []string{WarningRequestFailed}
		}
		return nil
	}
	if result.FailOpen || result.SkippedReason == SkippedFailOpen || result.SkippedReason == SkippedOverLimitOpen {
		return []string{WarningRequestFailOpen}
	}
	return nil
}

func ResponseWarnings(status string) []string {
	switch status {
	case "bypassed":
		return []string{WarningResponseBypassed}
	case "failed":
		return []string{WarningResponseFailed}
	default:
		return nil
	}
}

// WarningsFromSummary 是实时事件、热池和历史列表共用的读取入口。
// 新记录的 warnings 保留此前失败；旧记录只能从当时保存的最终状态推导。
func WarningsFromSummary(payload string) []string {
	var summary struct {
		Warnings      []string `json:"warnings"`
		Action        string   `json:"action"`
		FailOpen      bool     `json:"fail_open"`
		SkippedReason string   `json:"skipped_reason"`
		BlockedCode   string   `json:"blocked_code"`
		Restore       struct {
			Status string `json:"status"`
		} `json:"restore"`
	}
	if json.Unmarshal([]byte(payload), &summary) != nil {
		return nil
	}
	if summary.Warnings != nil {
		return MergeWarnings(summary.Warnings)
	}
	var rejection *PolicyError
	if summary.Action == "blocked" {
		rejection = &PolicyError{Code: summary.BlockedCode}
	}
	request := RequestWarnings(ApplyResult{FailOpen: summary.FailOpen, SkippedReason: summary.SkippedReason}, rejection)
	response := ResponseWarnings(summary.Restore.Status)
	// 请求放行会连带禁用回填，不能把同一故障报成两个阶段失败。
	if len(request) > 0 && summary.Restore.Status == "bypassed" {
		response = nil
	}
	return MergeWarnings(request, response)
}
