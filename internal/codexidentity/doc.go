// Package codexidentity 集中管理 Codex 出站身份补全（第二批）：
// 客户端描述档案（ClientProfile）、来源关联信息（SourceHeaders / Association）、
// 目标账号作用域（AccountScope）、稳定 ID 派生（Namespace → 账号 scope → 逻辑 ID）、
// 按入口的字段策略，以及 x-codex-turn-state 的 owner 索引。
//
// 本包只做纯计算：不做账号调度、不解析/刷新 token、不访问数据库、不发起网络请求。
// 鉴权由 internal/accountauth 单一负责；namespace 的持久化由 service 层适配。
//
// 创建时间: 2026-09-23
package codexidentity

import "fmt"

// 本地策略错误码；这些错误不是上游失败，不冷却账号、不触发换号。
const (
	// CodeNamespaceUnavailable 启用补全但稳定映射 namespace 不可用。
	CodeNamespaceUnavailable = "codex_identity_namespace_unavailable"
	// CodeInvalidAssociation 来源关联头长度/字符不合法。
	CodeInvalidAssociation = "codex_identity_invalid_association"
	// CodeAssociationConflict 请求头与正文的关联字段无法按已知版本语义解释。
	CodeAssociationConflict = "codex_identity_association_conflict"
	// CodeBodyInvalid 正文不是可编辑的 JSON 对象（含重复键、身份字段类型错误）。
	CodeBodyInvalid = "codex_identity_body_invalid"
	// CodePrivacyConflict 隐私规则改写了必要关联值，头/正文不再一致。
	CodePrivacyConflict = "codex_identity_privacy_conflict"
	// CodeUnsupportedFeature 功能声明取值无法按协议解释（如布尔声明不是 true/false）；不按客户端描述或 plan 拒绝。
	CodeUnsupportedFeature    = "codex_identity_unsupported_feature"
	CodeSettingsUnavailable   = "codex_identity_settings_unavailable"
	CodeCompatibilityInvalid  = "codex_identity_compatibility_invalid"
	CodeRedirectOriginChanged = "codex_identity_redirect_origin_changed"
)

// Error 本地策略错误；Status 为建议回传的 HTTP 状态码。
type Error struct {
	Code    string
	Message string
	Status  int
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func newError(code string, status int, format string, args ...any) *Error {
	return &Error{Code: code, Status: status, Message: fmt.Sprintf(format, args...)}
}
