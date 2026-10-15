// Package proxy - Codex 订阅代理：capability 路径、路由表与请求/响应头策略
// 创建时间: 2026-09-10
//
// 新入口地址形如 http://<host>:<port>/subscription/<secret>/backend-api/codex/<operation>。
// 本文件只��纯函数：解析并剥离 capability 前缀、按 method/operation 精确匹配路由、
// 按白名单重建出站请求头、按策略复制上游响应头。不持有任何网络资源。
package proxy

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"sinuo/internal/codexidentity"
	svc "sinuo/internal/service"
)

const (
	chatGPTCodexBackendBaseURL   = "https://chatgpt.com/backend-api/codex"
	chatGPTCodexBackendWSBaseURL = "wss://chatgpt.com/backend-api/codex"

	subscriptionRouteTimeoutModels       = 60 * time.Second
	subscriptionRouteTimeoutSearch       = 120 * time.Second
	subscriptionRouteTimeoutImages       = 300 * time.Second
	subscriptionRouteTimeoutHistoryNotes = 60 * time.Second
	subscriptionRouteTimeoutRealtimeCall = 60 * time.Second
	subscriptionRouteTimeoutCompact      = 300 * time.Second

	// subscriptionOperationEchoMaxLength 限制错误信息中回显的操作名长度。
	subscriptionOperationEchoMaxLength = 80
)

// subscriptionRouteKind 标识路由类别（也作为日志中的规范化操作分类）。
type subscriptionRouteKind string

const (
	subscriptionRouteResponses         subscriptionRouteKind = "responses"
	subscriptionRouteResponsesWS       subscriptionRouteKind = "responses_ws"
	subscriptionRouteResponsesCompact  subscriptionRouteKind = "responses_compact"
	subscriptionRouteModels            subscriptionRouteKind = "models"
	subscriptionRouteSearch            subscriptionRouteKind = "search"
	subscriptionRouteImagesGenerations subscriptionRouteKind = "images_generations"
	subscriptionRouteImagesEdits       subscriptionRouteKind = "images_edits"
	subscriptionRouteHistory           subscriptionRouteKind = "history"
	subscriptionRouteNotes             subscriptionRouteKind = "notes"
	subscriptionRouteRealtimeCalls     subscriptionRouteKind = "realtime_calls"
)

// subscriptionRoute 精确路由条目。
type subscriptionRoute struct {
	Kind          subscriptionRouteKind
	Operation     string // 规范化操作名（不带前导斜杠），也是上游路径后缀
	InternalPath  string // 内部规范化路径，例如 /v1/responses；仅用于日志/追踪/隐私规则语义
	WebSocket     bool
	NormalizeBody bool          // Responses/compact：入站正文走现有 zstd 规范化
	Timeout       time.Duration // 0 表示不设总超时（SSE 由客户端断连驱动结束）
	ExtraHeaders  []string      // 该路由额外允许透传的客户端头（已规范化大小写）
}

// subscriptionCapabilityPath capability 路径解析结果。
type subscriptionCapabilityPath struct {
	Secret    string
	Operation string
}

// parseSubscriptionCapabilityPath 解析 /subscription/<secret>/backend-api/codex/<operation>。
// 含百分号转义歧义、空段、"."/".." 段、缺少 secret 或后缀不符时一律返回 false。
func parseSubscriptionCapabilityPath(u *url.URL) (subscriptionCapabilityPath, bool) {
	if u == nil {
		return subscriptionCapabilityPath{}, false
	}
	path := u.Path
	if u.RawPath != "" && u.RawPath != path {
		return subscriptionCapabilityPath{}, false
	}
	if !strings.HasPrefix(path, svc.CodexSubscriptionProxyCapabilityPrefix) {
		return subscriptionCapabilityPath{}, false
	}
	rest := path[len(svc.CodexSubscriptionProxyCapabilityPrefix):]
	idx := strings.Index(rest, "/")
	if idx <= 0 {
		return subscriptionCapabilityPath{}, false
	}
	secret := rest[:idx]
	if len(secret) > svc.CodexSubscriptionProxyMaxSecretLength {
		return subscriptionCapabilityPath{}, false
	}
	rest = rest[idx:]
	if !strings.HasPrefix(rest, svc.CodexSubscriptionProxyCapabilitySuffix) {
		return subscriptionCapabilityPath{}, false
	}
	rest = rest[len(svc.CodexSubscriptionProxyCapabilitySuffix):]
	if rest == "" {
		return subscriptionCapabilityPath{Secret: secret}, true
	}
	if rest[0] != '/' {
		return subscriptionCapabilityPath{}, false
	}
	operation := strings.TrimPrefix(rest, "/")
	if operation == "" {
		return subscriptionCapabilityPath{Secret: secret}, true
	}
	for _, segment := range strings.Split(operation, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return subscriptionCapabilityPath{}, false
		}
	}
	return subscriptionCapabilityPath{Secret: secret, Operation: operation}, true
}

var subscriptionHistoryOperations = map[string]struct{}{
	"list_windows": {}, "list_items": {}, "read_item": {}, "search_contents": {},
}

var subscriptionNotesOperations = map[string]struct{}{
	"list_files_by_prefix": {}, "read_file": {}, "search_contents": {},
	"append_to_file": {}, "write_file": {}, "thread_hint": {},
}

// isWebSocketUpgradeRequest 判断是否为 WS 升级握手。
func isWebSocketUpgradeRequest(r *http.Request) bool {
	if r == nil || r.Method != http.MethodGet {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket") {
		return false
	}
	for _, token := range strings.Split(r.Header.Get("Connection"), ",") {
		if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
			return true
		}
	}
	return false
}

// resolveSubscriptionRoute 按 method/operation 精确匹配；未命中返回 nil 与 404/405。
func resolveSubscriptionRoute(method, operation string, upgrade bool) (*subscriptionRoute, int) {
	switch operation {
	case "responses":
		if upgrade {
			return &subscriptionRoute{Kind: subscriptionRouteResponsesWS, Operation: operation, InternalPath: "/v1/responses", WebSocket: true}, 0
		}
		if method == http.MethodPost {
			return &subscriptionRoute{Kind: subscriptionRouteResponses, Operation: operation, InternalPath: "/v1/responses", NormalizeBody: true}, 0
		}
		return nil, http.StatusMethodNotAllowed
	case "responses/compact":
		return postOnly(method, &subscriptionRoute{Kind: subscriptionRouteResponsesCompact, Operation: operation, InternalPath: "/v1/responses/compact", NormalizeBody: true, Timeout: subscriptionRouteTimeoutCompact})
	case "models":
		if method != http.MethodGet {
			return nil, http.StatusMethodNotAllowed
		}
		return &subscriptionRoute{Kind: subscriptionRouteModels, Operation: operation, InternalPath: "/v1/models", Timeout: subscriptionRouteTimeoutModels, ExtraHeaders: subscriptionModelsExtraHeaders}, 0
	case "alpha/search":
		return postOnly(method, &subscriptionRoute{Kind: subscriptionRouteSearch, Operation: operation, InternalPath: "/v1/alpha/search", Timeout: subscriptionRouteTimeoutSearch})
	case "images/generations":
		return postOnly(method, &subscriptionRoute{Kind: subscriptionRouteImagesGenerations, Operation: operation, InternalPath: "/v1/images/generations", Timeout: subscriptionRouteTimeoutImages, ExtraHeaders: subscriptionImagesExtraHeaders})
	case "images/edits":
		return postOnly(method, &subscriptionRoute{Kind: subscriptionRouteImagesEdits, Operation: operation, InternalPath: "/v1/images/edits", Timeout: subscriptionRouteTimeoutImages, ExtraHeaders: subscriptionImagesExtraHeaders})
	case "realtime/calls":
		return postOnly(method, &subscriptionRoute{Kind: subscriptionRouteRealtimeCalls, Operation: operation, InternalPath: "/v1/realtime/calls", Timeout: subscriptionRouteTimeoutRealtimeCall, ExtraHeaders: subscriptionRealtimeExtraHeaders})
	}
	if name, ok := strings.CutPrefix(operation, "alpha/history/v2/"); ok {
		if _, allowed := subscriptionHistoryOperations[name]; allowed {
			return postOnly(method, &subscriptionRoute{Kind: subscriptionRouteHistory, Operation: operation, InternalPath: "/v1/" + operation, Timeout: subscriptionRouteTimeoutHistoryNotes, ExtraHeaders: subscriptionHistoryNotesExtraHeaders})
		}
	}
	if name, ok := strings.CutPrefix(operation, "alpha/notes/v2/"); ok {
		if _, allowed := subscriptionNotesOperations[name]; allowed {
			return postOnly(method, &subscriptionRoute{Kind: subscriptionRouteNotes, Operation: operation, InternalPath: "/v1/" + operation, Timeout: subscriptionRouteTimeoutHistoryNotes, ExtraHeaders: subscriptionHistoryNotesExtraHeaders})
		}
	}
	return nil, http.StatusNotFound
}

func postOnly(method string, route *subscriptionRoute) (*subscriptionRoute, int) {
	if method != http.MethodPost {
		return nil, http.StatusMethodNotAllowed
	}
	return route, 0
}

// truncateSubscriptionOperation 限制错误信息中回显的操作名长度。
func truncateSubscriptionOperation(operation string) string {
	if len(operation) <= subscriptionOperationEchoMaxLength {
		return operation
	}
	return operation[:subscriptionOperationEchoMaxLength] + "…"
}

// ---------------------------------------------------------------------------
// 请求头策略
// ---------------------------------------------------------------------------

// subscriptionPassthroughBaseHeaders 订阅直通与辅助路径允许透传的客户端头（P0 录制 + rust-v0.153.4 源码常量）。
// Content-Length / Content-Encoding / Host 及 hop-by-hop 头不在此列，由调用方按最终正文重建。
var subscriptionPassthroughBaseHeaders = canonicalHeaderKeys([]string{
	"Authorization", "chatgpt-account-id",
	"Content-Type", "Accept",
	"originator", "version", "User-Agent",
	"OpenAI-Beta",
	"session-id", "thread-id", "x-client-request-id",
	"x-codex-turn-metadata", "x-codex-beta-features", "x-codex-turn-state",
	"x-codex-window-id", "x-codex-parent-thread-id", "x-codex-routing-hint",
	"x-codex-installation-id",
	"x-openai-subagent", "x-openai-internal-codex-responses-lite", "x-openai-memgen-request",
	"x-responsesapi-include-timing-metrics", "x-openai-internal-codex-residency",
	"x-oai-attestation",
})

var (
	subscriptionModelsExtraHeaders       = canonicalHeaderKeys([]string{"If-None-Match", "If-Modified-Since"})
	subscriptionImagesExtraHeaders       = canonicalHeaderKeys([]string{"x-codex-image-turn-id"})
	subscriptionHistoryNotesExtraHeaders = canonicalHeaderKeys([]string{"x-openai-tool-output-truncation-policy", "x-openai-encrypted-tool-arguments"})
	// 语音协议版本由客户端声明，必须原样透传；不能由代理固定或补默认值。
	subscriptionRealtimeExtraHeaders = canonicalHeaderKeys([]string{"OpenAI-Alpha"})
)

// subscriptionPoolAllowedHeaders 池模式进池前唯一允许保留的客户端头；其余由所选账号 helper 重新生成。
var subscriptionPoolAllowedHeaders = canonicalHeaderKeys([]string{"Content-Type", "Accept"})

// subscriptionLocalOnlyHeaders 本地校验后必须删除、任何模式都不转发的头。
var subscriptionLocalOnlyHeaders = canonicalHeaderKeys([]string{
	"Cookie", "x-api-key", "Forwarded", "Proxy-Authorization", "Origin", "Referer",
})

// subscriptionAssociationHeaders 池模式下先提取到内部 context、再从请求头剥离的关联头。
var subscriptionAssociationHeaders = canonicalHeaderKeys([]string{
	"thread-id", "session-id", "x-client-request-id", "originator", "x-codex-window-id",
})

func canonicalHeaderKeys(keys []string) []string {
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, http.CanonicalHeaderKey(key))
	}
	return out
}

// stripSubscriptionLocalHeaders 删除本地专用头（含 X-Forwarded-* 前缀），在任何转发或日志之前调用。
func stripSubscriptionLocalHeaders(header http.Header) {
	named := connectionNamedHeaders(header)
	for key := range header {
		lower := strings.ToLower(key)
		if _, drop := named[lower]; drop && lower != "upgrade" {
			delete(header, key)
		}
	}
	for _, key := range subscriptionLocalOnlyHeaders {
		header.Del(key)
	}
	for key := range header {
		if strings.HasPrefix(strings.ToLower(key), "x-forwarded-") {
			header.Del(key)
		}
	}
}

// buildSubscriptionPassthroughHeaders 从空集合按白名单重建透传请求头（不含正文/传输相关头）。
func buildSubscriptionPassthroughHeaders(src http.Header, route *subscriptionRoute) http.Header {
	dst := make(http.Header, len(subscriptionPassthroughBaseHeaders)+4)
	copyHeaderValues(src, dst, subscriptionPassthroughBaseHeaders)
	if route != nil {
		copyHeaderValues(src, dst, route.ExtraHeaders)
	}
	return dst
}

// buildSubscriptionPoolHeaders 池模式从空集合只保留已校验的 Content-Type 与 Accept。
func buildSubscriptionPoolHeaders(src http.Header) http.Header {
	dst := make(http.Header, 2)
	copyHeaderValues(src, dst, subscriptionPoolAllowedHeaders)
	return dst
}

func copyHeaderValues(src, dst http.Header, keys []string) {
	for _, key := range keys {
		for _, value := range src.Values(key) {
			dst.Add(key, value)
		}
	}
}

// subscriptionClientAssociation 池模式下保留在内部 context 的关联信息，不发往上游。
type subscriptionClientAssociation struct {
	ThreadID        string
	SessionID       string
	ClientRequestID string
	Originator      string
	WindowID        string
}

// extractSubscriptionClientAssociation 读取关联头并做长度约束。
func extractSubscriptionClientAssociation(header http.Header) subscriptionClientAssociation {
	source := codexidentity.CaptureSourceHeaders(header)
	return subscriptionClientAssociation{ThreadID: source.ThreadID, SessionID: source.SessionID, ClientRequestID: source.ClientRequestID, Originator: source.Originator, WindowID: source.WindowID}
}

// ---------------------------------------------------------------------------
// 响应头策略
// ---------------------------------------------------------------------------

// subscriptionDroppedResponseHeaders 不回传下游的上游响应头：hop-by-hop 与 Set-Cookie。
var subscriptionDroppedResponseHeaders = map[string]struct{}{
	"Connection": {}, "Keep-Alive": {}, "Proxy-Authenticate": {}, "Proxy-Authorization": {},
	"Te": {}, "Trailer": {}, "Transfer-Encoding": {}, "Upgrade": {},
	"Set-Cookie": {},
}

// copySubscriptionWebSocketHandshakeHeaders 只把上游 101 的提示类头补进下游握手响应：
// x-codex-*、x-openai-* 与 x-models-etag。Sec-WebSocket-* 由 WS 库按两侧各自协商结果设置，
// Set-Cookie、hop-by-hop 与其他头一律不复制。
func copySubscriptionWebSocketHandshakeHeaders(dst, src http.Header) {
	for key, values := range src {
		if key != "X-Models-Etag" && !strings.HasPrefix(key, "X-Codex-") && !strings.HasPrefix(key, "X-Openai-") {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

// copySubscriptionResponseHeaders 复制上游响应头：保留 x-codex-*、x-models-etag、Location、Content-Type 等，
// 丢弃 hop-by-hop、Connection 点名字段与 Set-Cookie。正文不改写时 Content-Length 原样保留。
func copySubscriptionResponseHeaders(dst, src http.Header) {
	connectionTokens := map[string]struct{}{}
	for _, value := range src.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			if token = strings.TrimSpace(token); token != "" {
				connectionTokens[http.CanonicalHeaderKey(token)] = struct{}{}
			}
		}
	}
	for key, values := range src {
		if _, drop := subscriptionDroppedResponseHeaders[key]; drop {
			continue
		}
		if _, drop := connectionTokens[key]; drop {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}
