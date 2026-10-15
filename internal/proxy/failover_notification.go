package proxy

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"sinuo/internal/store"
)

const (
	FailoverEventKind = "failover"
	FailoverLaneCC    = "cc"
	FailoverLaneCodex = "codex"

	FailoverReasonUnknown                       = "unknown"
	FailoverReasonConnectionFailedBeforeHeaders = "connection_failed_before_wrote_headers"
	FailoverReasonForwardError                  = "forward_error"
	FailoverReasonEmptyResponse                 = "empty_response"
	FailoverReasonAuthFailed                    = "auth_failed"
	FailoverReasonAuthRejected                  = "auth_rejected"
	FailoverReasonRateLimited                   = "rate_limited"
	FailoverReasonUsageLimit                    = "usage_limit"
	FailoverReasonServerError                   = "server_error"
	FailoverReasonProcessingError               = "processing_error"
	FailoverReasonPayloadTooLarge               = "payload_too_large"
	FailoverReasonModelUnsupported              = "model_unsupported"
	FailoverReasonSchemaIncompatible            = "schema_incompatible"
)

// FailoverEvent 描述一次请求内真实发生的候选切换。
// 该事件只在请求从 From 实际进入 To 时发出，不代表每次新请求的首次选路。
type FailoverEvent struct {
	Kind         string `json:"kind"`
	Lane         string `json:"lane"`
	From         string `json:"from"`
	To           string `json:"to"`
	ReasonCode   string `json:"reason_code"`
	ReasonLabel  string `json:"reason_label"`
	ReasonDetail string `json:"reason_detail,omitempty"`
	RequestID    string `json:"request_id,omitempty"`
	RequestPath  string `json:"request_path,omitempty"`
	Attempt      int    `json:"attempt,omitempty"`
}

var (
	failoverBearerPattern     = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]+`)
	failoverSecretPattern     = regexp.MustCompile(`(?i)(?:sk|rk|rt|sess|access|refresh)[_-][A-Za-z0-9._~-]{8,}`)
	failoverJSONSecretPattern = regexp.MustCompile(`(?i)("(?:access_token|refresh_token|id_token|api_key|token|credential_raw)"\s*:\s*")([^"]*)(")`)
	failoverURLPattern        = regexp.MustCompile(`(?i)https?://[^\s]+`)
	failoverReasonLabels      = map[string]string{
		FailoverReasonUnknown:                       "未知原因",
		FailoverReasonConnectionFailedBeforeHeaders: "连接建立失败",
		FailoverReasonForwardError:                  "上游连接错误",
		FailoverReasonEmptyResponse:                 "上游未返回响应",
		FailoverReasonAuthFailed:                    "鉴权失败",
		FailoverReasonAuthRejected:                  "鉴权被拒绝",
		FailoverReasonRateLimited:                   "触发限流",
		FailoverReasonUsageLimit:                    "额度已用尽",
		FailoverReasonServerError:                   "上游服务错误",
		FailoverReasonProcessingError:               "响应处理失败",
		FailoverReasonPayloadTooLarge:               "请求体过大",
		FailoverReasonModelUnsupported:              "模型不受支持",
		FailoverReasonSchemaIncompatible:            "请求格式不兼容",
	}
)

// FailoverReasonLabel 返回面向用户的稳定中文原因；未知码使用中文前缀并保留原码便于排障。
func FailoverReasonLabel(reasonCode string) string {
	reasonCode = strings.TrimSpace(reasonCode)
	if label := failoverReasonLabels[reasonCode]; label != "" {
		return label
	}
	if reasonCode == "" {
		return failoverReasonLabels[FailoverReasonUnknown]
	}
	return fmt.Sprintf("未知原因（%s）", reasonCode)
}

// SetOnFailoverTriggered 注册前端故障转移通知回调。
// 回调只接收已经脱敏、截断后的事件；调度管线不依赖回调返回值。
func (h *Handler) SetOnFailoverTriggered(fn func(FailoverEvent)) {
	if h == nil {
		return
	}
	h.failoverNotifierMu.Lock()
	h.onFailoverTriggered = fn
	h.failoverNotifierMu.Unlock()
}

// notifyFailover 发送一次结构化故障转移事件。
func (h *Handler) notifyFailover(event FailoverEvent) {
	if h == nil {
		return
	}
	event, ok := normalizeFailoverEvent(event)
	if !ok {
		return
	}

	h.failoverNotifierMu.RLock()
	notifier := h.onFailoverTriggered
	h.failoverNotifierMu.RUnlock()
	if notifier != nil {
		notifier(event)
	}
}

func normalizeFailoverEvent(event FailoverEvent) (FailoverEvent, bool) {
	event.Kind = FailoverEventKind
	event.Lane = strings.TrimSpace(event.Lane)
	event.From = sanitizeFailoverText(event.From, 96)
	event.To = sanitizeFailoverText(event.To, 96)
	event.ReasonCode = sanitizeFailoverText(event.ReasonCode, 96)
	if event.ReasonCode == "" {
		event.ReasonCode = FailoverReasonUnknown
	}
	event.ReasonLabel = FailoverReasonLabel(event.ReasonCode)
	event.ReasonDetail = sanitizeFailoverText(event.ReasonDetail, 180)
	event.RequestID = sanitizeFailoverText(event.RequestID, 128)
	event.RequestPath = sanitizeFailoverText(event.RequestPath, 256)

	if event.Lane == "" || event.From == "" || event.To == "" || event.From == event.To {
		return FailoverEvent{}, false
	}
	if event.Attempt < 0 {
		event.Attempt = 0
	}
	return event, true
}

func sanitizeFailoverText(value string, limit int) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	value = failoverBearerPattern.ReplaceAllString(value, "Bearer [redacted]")
	value = failoverSecretPattern.ReplaceAllString(value, "[redacted]")
	value = failoverJSONSecretPattern.ReplaceAllString(value, `${1}[redacted]${3}`)
	value = failoverURLPattern.ReplaceAllString(value, "[upstream-url]")
	if limit > 0 {
		runes := []rune(value)
		if len(runes) > limit {
			value = string(runes[:limit]) + "…"
		}
	}
	return value
}

// failoverHTTPDetail 生成适合展示给用户的短错误详情，避免把整段上游 body 直接推到 UI。
func failoverHTTPDetail(statusCode int, detail string) string {
	if statusCode > 0 {
		// 有明确 HTTP 状态时只展示状态码，不把上游响应 body 直接带入 Toast。
		return fmt.Sprintf("HTTP %d", statusCode)
	}
	cleanDetail := sanitizeFailoverText(detail, 120)
	if cleanDetail == "" {
		return "上游连接失败"
	}
	return cleanDetail
}

func failoverRequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if requestID, ok := ctx.Value("conn_id").(string); ok {
		return strings.TrimSpace(requestID)
	}
	return ""
}

func (h *Handler) notifyAccountFailover(ctx context.Context, accounts []*store.UpstreamAccountRecord, index int, from, reasonCode string, statusCode int, detail, requestID, requestPath string) {
	if ctx != nil && ctx.Err() != nil {
		return
	}
	next, nextIndex := nextAccountFailoverCandidate(accounts, index)
	if next == nil {
		return
	}
	h.notifyFailover(FailoverEvent{
		Lane:         FailoverLaneCodex,
		From:         from,
		To:           accountFailoverDisplayName(next),
		ReasonCode:   reasonCode,
		ReasonDetail: failoverHTTPDetail(statusCode, detail),
		RequestID:    requestID,
		RequestPath:  requestPath,
		Attempt:      accountFailoverAttempt(accounts, nextIndex),
	})
}

func nextAccountFailoverCandidate(accounts []*store.UpstreamAccountRecord, index int) (*store.UpstreamAccountRecord, int) {
	if index < 0 {
		return nil, -1
	}
	for nextIndex := index + 1; nextIndex < len(accounts); nextIndex++ {
		if accounts[nextIndex] != nil {
			return accounts[nextIndex], nextIndex
		}
	}
	return nil, -1
}

func hasNextAccountFailoverCandidate(accounts []*store.UpstreamAccountRecord, index int) bool {
	next, _ := nextAccountFailoverCandidate(accounts, index)
	return next != nil
}

// accountFailoverAttempt 表示进入下一非空候选前已经失败的真实尝试次数。
func accountFailoverAttempt(accounts []*store.UpstreamAccountRecord, nextIndex int) int {
	attempt := 0
	for index := 0; index < nextIndex && index < len(accounts); index++ {
		if accounts[index] != nil {
			attempt++
		}
	}
	return attempt
}

func accountFailoverDisplayName(account *store.UpstreamAccountRecord) string {
	if account == nil {
		return ""
	}
	if name := strings.TrimSpace(account.AccountName); name != "" {
		return name
	}
	return fmt.Sprintf("account-%d", account.ID)
}
