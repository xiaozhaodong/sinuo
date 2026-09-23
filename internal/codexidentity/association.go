package codexidentity

import (
	"net/http"
	"strings"
)

// 来源关联信息的边界：写入内部 context 的值必须先经过长度、字符和结构校验，
// 不能把整个原始 Header 放进 context。
const (
	maxAssociationIDLength = 128
	maxTurnStateLength     = 8192
	maxTurnMetadataLength  = 64 << 10
	maxDescriptorLength    = 512
	maxFeatureListLength   = 1024
)

// 来源头名（小写规范）。
const (
	HeaderSessionID            = "session-id"
	HeaderThreadID             = "thread-id"
	HeaderClientRequestID      = "x-client-request-id"
	HeaderWindowID             = "x-codex-window-id"
	HeaderParentThreadID       = "x-codex-parent-thread-id"
	HeaderInstallationID       = "x-codex-installation-id"
	HeaderTurnState            = "x-codex-turn-state"
	HeaderTurnMetadata         = "x-codex-turn-metadata"
	HeaderBetaFeatures         = "x-codex-beta-features"
	HeaderRoutingHint          = "x-codex-routing-hint"
	HeaderOriginator           = "originator"
	HeaderVersion              = "version"
	HeaderResponsesLite        = "x-openai-internal-codex-responses-lite"
	HeaderSubagent             = "x-openai-subagent"
	HeaderMemgenRequest        = "x-openai-memgen-request"
	HeaderIncludeTimingMetrics = "x-responsesapi-include-timing-metrics"
	HeaderAttestation          = "x-oai-attestation"
)

// SourceHeaders 来源请求中与身份/关联相关的头（已做长度与字符校验）。
// 凭据（Authorization / chatgpt-account-id / Cookie 等）不在此结构中，永远不进入池。
type SourceHeaders struct {
	UserAgent  string
	Originator string
	Version    string

	SessionID       string
	ThreadID        string
	ClientRequestID string
	WindowID        string
	ParentThreadID  string
	InstallationID  string

	TurnState    string
	TurnMetadata string
	BetaFeatures string

	ResponsesLite        string
	Subagent             string
	MemgenRequest        string
	IncludeTimingMetrics string

	// Invalid 记录未通过校验、已被丢弃的来源头名。
	Invalid []string
	// Unrecognized 记录未识别的首方扩展头名（仅名称），首方映射时删除并用于告警。
	Unrecognized []string
}

// CaptureSourceHeaders 从来源请求头提取有界的身份/关联信息。
func CaptureSourceHeaders(header http.Header) SourceHeaders {
	var src SourceHeaders
	if header == nil {
		return src
	}
	capture := func(name string, maxLength int, allowSpace bool, dst *string) {
		values := header.Values(name)
		if len(values) > 1 {
			src.Invalid = append(src.Invalid, name)
			return
		}
		value := strings.TrimSpace(header.Get(name))
		if value == "" {
			return
		}
		if len(value) > maxLength || !isVisibleASCII(value, allowSpace) {
			src.Invalid = append(src.Invalid, name)
			return
		}
		*dst = value
	}
	capture("User-Agent", maxDescriptorLength, true, &src.UserAgent)
	capture(HeaderOriginator, maxAssociationIDLength, true, &src.Originator)
	capture(HeaderVersion, maxAssociationIDLength, false, &src.Version)
	capture(HeaderSessionID, maxAssociationIDLength, false, &src.SessionID)
	capture(HeaderThreadID, maxAssociationIDLength, false, &src.ThreadID)
	capture(HeaderClientRequestID, maxAssociationIDLength, false, &src.ClientRequestID)
	capture(HeaderWindowID, maxAssociationIDLength, false, &src.WindowID)
	capture(HeaderParentThreadID, maxAssociationIDLength, false, &src.ParentThreadID)
	capture(HeaderInstallationID, maxAssociationIDLength, false, &src.InstallationID)
	capture(HeaderTurnState, maxTurnStateLength, false, &src.TurnState)
	capture(HeaderTurnMetadata, maxTurnMetadataLength, true, &src.TurnMetadata)
	capture(HeaderBetaFeatures, maxFeatureListLength, true, &src.BetaFeatures)
	capture(HeaderResponsesLite, maxAssociationIDLength, false, &src.ResponsesLite)
	capture(HeaderSubagent, maxAssociationIDLength, false, &src.Subagent)
	capture(HeaderMemgenRequest, maxAssociationIDLength, false, &src.MemgenRequest)
	capture(HeaderIncludeTimingMetrics, maxAssociationIDLength, false, &src.IncludeTimingMetrics)
	src.Unrecognized = unrecognizedExtensionHeaders(header)
	return src
}

// isVisibleASCII 只接受可见 ASCII（可选允许空格），拒绝控制字符、CR/LF 与非 ASCII。
func isVisibleASCII(value string, allowSpace bool) bool {
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c == ' ' {
			if allowSpace {
				continue
			}
			return false
		}
		if c < 0x21 || c > 0x7e {
			return false
		}
	}
	return true
}

// Association 一次请求的逻辑关联信息（来源值或映射后的值）。
type Association struct {
	SessionID       string
	ThreadID        string
	ClientRequestID string
	WindowID        string
	ParentThreadID  string
	InstallationID  string
	PromptCacheKey  string
}

// mergeSourceValue 汇总同一逻辑字段在多个位置的值：全部为空返回空；
// 出现两个不同的非空值即视为冲突。
func mergeSourceValue(field string, values ...string) (string, error) {
	merged := ""
	for _, value := range values {
		if value == "" {
			continue
		}
		if merged == "" {
			merged = value
			continue
		}
		if value != merged {
			return "", newError(CodeAssociationConflict, http.StatusBadRequest,
				"request header and body disagree on %s", field)
		}
	}
	return merged, nil
}
