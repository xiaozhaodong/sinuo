package privacy

import "net/http"

const (
	CodeInvalidRequest   = "privacy_invalid_request"
	SkippedFailOpen      = "privacy_scan_failed_fail_open"
	SkippedOverLimitOpen = "privacy_scan_over_limit_fail_open"
)

// failurePolicy 固定扫描时的配置，排队或重试期间的设置变化不改变本次失败策略。
type failurePolicy struct {
	onError, overLimit, path string
	restore                  bool
}

// ResolveFailure 是扫描、改写复验和出站发布共用的失败边界。
// 本地处理失败必须整体撤销；协议格式错误和独立超限拒绝不受 on_error 影响。
func (r ApplyResult) ResolveFailure(original []byte, failure *PolicyError) (ApplyResult, error) {
	if r.Restore != nil {
		r.Restore.Release()
	}
	r.Body, r.Changed, r.Restore, r.RemaskMatches = original, false, nil, 0
	r.RestoreStatus = "not_applicable"
	closed := r.failurePolicy == nil || r.failurePolicy.onError == OnErrorFailClosed
	if failure.Code == CodeInvalidRequest {
		closed = true
	} else if failure.Code == CodeScanBodyTooLarge {
		r.Truncated = true
		closed = r.failurePolicy == nil || r.failurePolicy.overLimit == OverLimitFailClosed
	}
	if closed {
		copy := *failure
		if copy.Code == codeRestoreRejected && r.failurePolicy != nil && !isClaudeMessagesPath(r.failurePolicy.path) {
			copy.StatusCode = http.StatusBadRequest
		}
		copy.Result = r
		return r, &copy
	}
	r.FailOpen = true
	r.SkippedReason = SkippedFailOpen
	if failure.Code == CodeScanBodyTooLarge {
		r.SkippedReason = SkippedOverLimitOpen
	}
	if r.failurePolicy.restore {
		r.RestoreStatus = "bypassed"
	}
	r.failureReason = failure.restoreReason
	return r, nil
}

// SafeFailureMessage 只返回策略代码内的固定原因，不接受解析器错误或命中值。
func (r ApplyResult) SafeFailureMessage() string { return string(r.failureReason) }

func invalidRequest(reason restoreRejection) *PolicyError {
	return &PolicyError{StatusCode: http.StatusBadRequest, Code: CodeInvalidRequest, Message: string(reason), restoreReason: reason}
}

// requestFormatError 区分协议字段类型错误和本地解析能力不足。
type requestFormatError string

func (e requestFormatError) Error() string { return string(e) }
