package privacy

import (
	"fmt"
	"time"
)

// Request 描述一次出站请求的过滤上下文
type Request struct {
	// StreamingHTTP 仅由实际选择 HTTP SSE 的转发入口设置。
	SessionID     string `json:"-"`
	StreamingHTTP bool   `json:"-"`
	WebSocket     bool   `json:"-"` // 原生 Responses 消息允许使用 response 包装请求。
	RequestID     string `json:"request_id"`
	Path          string `json:"path"`
	Method        string `json:"method"`
	UpstreamType  string `json:"upstream_type"` // endpoint/account
	EndpointName  string `json:"endpoint_name"`
	AccountID     int64  `json:"account_id"`
	ProviderType  string `json:"provider_type"`
	ContentType   string `json:"content_type"`
}

// RuleHit 单条规则的命中统计。
type RuleHit struct {
	RuleID   int64  `json:"rule_id"`
	RuleName string `json:"rule_name"`
	Source   string `json:"source"`
	Action   string `json:"action"`
	Count    int    `json:"count"`
	// Matches 临时用于本地误判调试，禁止通过 API JSON 回传给前端。
	Matches []string `json:"-"`
}

// 跳过原因
const (
	SkippedNonTextBody     = "skipped_non_text_body"
	SkippedUnsupportedPath = "skipped_unsupported_path"
	SkippedScanTruncated   = "scan_truncated"
	SkippedEmptyBody       = "skipped_empty_body"
)

// ApplyResult 一次过滤的结果。Body 始终可直接转发：
// 零命中 / 仅检测 / 关闭时与输入 byte-identical。
type ApplyResult struct {
	failurePolicy   *failurePolicy
	failureReason   restoreRejection
	FailOpen        bool          `json:"fail_open,omitempty"`
	RemaskDuration  time.Duration `json:"-"`
	RemaskMatches   int           `json:"-"`
	RestoreStatus   string        `json:"-"`
	Restore         *RestoreView  `json:"-"`
	Body            []byte        `json:"-"`
	Changed         bool          `json:"changed"`
	HitCount        int           `json:"hit_count"`
	RuleHits        []RuleHit     `json:"rule_hits"`
	Action          string        `json:"action"` // disabled/detect/redact
	ScanDuration    time.Duration `json:"scan_duration"`
	SkippedReason   string        `json:"skipped_reason"`
	Truncated       bool          `json:"truncated"`
	SnapshotVersion int64         `json:"snapshot_version"`
}

// PolicyError 本地隐私策略拒绝。调用方必须用 errors.As 识别并短路返回，
// 不允许进入端点重试、端点故障转移、账号池 failover 或账号冷却。
type PolicyError struct {
	StatusCode    int
	Code          string
	Message       string
	Result        ApplyResult
	restoreReason restoreRejection
}

func (e *PolicyError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("privacy policy rejected request: %s (%d): %s", e.Code, e.StatusCode, e.Message)
}

// 策略错误码
const (
	CodeScanBodyTooLarge = "privacy_scan_body_too_large"
	CodeScanFailed       = "privacy_scan_failed"
)
