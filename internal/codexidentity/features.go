package codexidentity

import (
	"net/http"
	"slices"
	"sort"
	"strings"
)

// 功能声明与首方扩展字段策略：
//   - 功能声明只校验协议格式，原样保留；客户端描述识别只决定出站 UA，不承担权限判断，
//     是否支持由上游决定，本地不猜账号 plan，也不删除声明后重试。
//   - 未识别的 x-codex-* / x-openai-* / x-oai-* 头和 metadata 键在首方映射时删除：
//     这片命名空间是身份字段的高发区，宁可功能异常可见，也不让未映射的 ID 静默出站。
//   - 两类情况都只记录名称供限频告警，不记录值。

// rust-v0.155.1 会写入 x-codex-beta-features 的全部值：remote_compaction_v2 无条件声明，
// 其余来自 /experimental 菜单。只用于识别需要告警的未验证值，不参与放行判断。
var knownBetaFeatures = map[string]struct{}{
	"remote_compaction_v2": {}, "network_proxy": {}, "worktrees": {}, "realtime_conversation": {},
}

// 已知首方扩展头；由身份层映射、校验或按策略隔离。
var knownExtensionHeaders = map[string]struct{}{
	HeaderWindowID: {}, HeaderParentThreadID: {}, HeaderInstallationID: {}, HeaderTurnState: {},
	HeaderTurnMetadata: {}, HeaderBetaFeatures: {}, HeaderRoutingHint: {}, HeaderResponsesLite: {},
	HeaderSubagent: {}, HeaderMemgenRequest: {}, HeaderAttestation: {},
}

// maxReportedFieldNames 限制单个请求记录的告警名称数量，防止来源用大量字段放大日志与内存。
const maxReportedFieldNames = 16

// isFirstPartyExtension 判断小写名是否属于 Codex / OpenAI 首方扩展命名空间。
func isFirstPartyExtension(lower string) bool {
	return strings.HasPrefix(lower, "x-codex-") || strings.HasPrefix(lower, "x-openai-") || strings.HasPrefix(lower, "x-oai-")
}

// appendFieldName 收集仅用于告警的名称：去重、限量，拒绝控制字符。
func appendFieldName(names []string, name string) []string {
	if len(names) >= maxReportedFieldNames || len(name) > maxAssociationIDLength || !isVisibleASCII(name, false) || slices.Contains(names, name) {
		return names
	}
	return append(names, name)
}

// unrecognizedExtensionHeaders 列出来源中未识别的首方扩展头名。
func unrecognizedExtensionHeaders(header http.Header) []string {
	var names []string
	for key := range header {
		lower := strings.ToLower(key)
		if _, known := knownExtensionHeaders[lower]; !known && isFirstPartyExtension(lower) {
			names = appendFieldName(names, "header."+lower)
		}
	}
	sort.Strings(names)
	return names
}

// validateFeatures 只拒绝无法按协议解释的取值；rust-v0.155.1 的布尔声明只发送 "true"。
func validateFeatures(src SourceHeaders) error {
	for _, value := range []string{src.ResponsesLite, src.MemgenRequest, src.IncludeTimingMetrics} {
		if value != "" && value != "true" && value != "false" {
			return newError(CodeUnsupportedFeature, 422, "unsupported Codex feature declaration value")
		}
	}
	if src.Subagent != "" && !validSubagent(src.Subagent) {
		return newError(CodeUnsupportedFeature, 422, "unsupported x-openai-subagent declaration")
	}
	return nil
}

// applyFeatureHeaders 原样写回功能声明，beta 列表保留原始值与顺序。
// 布尔声明的 "false" 与缺省同义，不补发原生客户端不会发送的值。
func (p *Prepared) applyFeatureHeaders(src SourceHeaders) error {
	if err := validateFeatures(src); err != nil {
		return err
	}
	if src.Subagent != "" {
		p.Headers.Set(HeaderSubagent, src.Subagent)
	}
	if src.BetaFeatures != "" {
		p.Headers.Set(HeaderBetaFeatures, src.BetaFeatures)
		for _, feature := range strings.Split(src.BetaFeatures, ",") {
			feature = strings.TrimSpace(feature)
			if _, known := knownBetaFeatures[feature]; feature != "" && !known {
				p.UnknownBetaFeatures = appendFieldName(p.UnknownBetaFeatures, feature)
			}
		}
	}
	for header, value := range map[string]string{HeaderResponsesLite: src.ResponsesLite, HeaderMemgenRequest: src.MemgenRequest, HeaderIncludeTimingMetrics: src.IncludeTimingMetrics} {
		if value == "true" {
			p.Headers.Set(header, "true")
		}
	}
	return nil
}

// dropField 记录按策略删除的未知首方扩展字段名。
func (p *Prepared) dropField(name string) {
	p.DroppedFields = appendFieldName(p.DroppedFields, name)
}
