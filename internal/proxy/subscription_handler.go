// Package proxy - Codex 订阅代理：新入口 HTTP 处理器
// 创建时间: 2026-09-10
//
// 每个请求的处理顺序：Host 校验 → capability 解析与校验 → Origin 拒绝 → 路由精确匹配 →
// 生成 RequestID 并注入 context 的 conn_id → 剥离本地头、改写为内部规范化路径 → 分发。
// 访问密钥在校验后立即丢弃：日志、错误信息、追踪只使用规范化路径与操作名。
package proxy

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"sinuo/internal/middleware"
	"sinuo/internal/monitor"
	svc "sinuo/internal/service"
)

// SubscriptionProxySnapshot 运行态配置快照；请求入口读取一次并固定到该请求/握手结束。
type SubscriptionProxySnapshot struct {
	Revision      int64
	Enabled       bool
	Mode          string
	ListenHost    string
	ListenPort    int
	WSCompression string
	AccessHash    []byte
}

// ListenAddr 返回 host:port（IPv6 自动加方括号）。
func (s *SubscriptionProxySnapshot) ListenAddr() string {
	if s == nil {
		return ""
	}
	return net.JoinHostPort(s.ListenHost, strconv.Itoa(s.ListenPort))
}

// VerifyAccessSecret 恒定时间校验访问密钥。
func (s *SubscriptionProxySnapshot) VerifyAccessSecret(secret string) bool {
	if s == nil {
		return false
	}
	return svc.VerifyCodexSubscriptionProxyAccessSecret(s.AccessHash, secret)
}

// IsPoolMode 表示 Responses 走账号池。
func (s *SubscriptionProxySnapshot) IsPoolMode() bool {
	return s != nil && s.Mode == svc.CodexSubscriptionProxyModePool
}

// subscriptionWebSocketServer 处理 Responses WS 握手与桥接（subscription_ws.go）。
type subscriptionWebSocketServer interface {
	serve(w http.ResponseWriter, r *http.Request, route *subscriptionRoute, snap *SubscriptionProxySnapshot, requestID string)
}

// subscriptionPoolServer 处理池模式的 HTTP /responses（subscription_pool.go）。
type subscriptionPoolServer interface {
	serve(w http.ResponseWriter, r *http.Request, route *subscriptionRoute, requestID string)
}

// subscriptionRequestInfo 写入 context 的请求元信息，供池适配器与后续追踪读取。
type subscriptionRequestInfo struct {
	RequestID   string
	Route       *subscriptionRoute
	Mode        string
	Revision    int64
	Association subscriptionClientAssociation
}

type subscriptionRequestInfoKey struct{}

func withSubscriptionRequestInfo(ctx context.Context, info *subscriptionRequestInfo) context.Context {
	return context.WithValue(ctx, subscriptionRequestInfoKey{}, info)
}

func subscriptionRequestInfoFromContext(ctx context.Context) *subscriptionRequestInfo {
	if ctx == nil {
		return nil
	}
	info, _ := ctx.Value(subscriptionRequestInfoKey{}).(*subscriptionRequestInfo)
	return info
}

// SubscriptionProxyHandlerOptions 构造参数。
type SubscriptionProxyHandlerOptions struct {
	Proxy      *Handler
	Monitoring *middleware.MonitoringMiddleware
	Logger     *slog.Logger
	// Snapshot 返回当前运行态快照；返回 nil 表示入口未运行。
	Snapshot func() *SubscriptionProxySnapshot
	// UpstreamBaseURL / UpstreamWSBaseURL 仅供测试注入；为空时固定为 ChatGPT Codex 后端。
	UpstreamBaseURL   string
	UpstreamWSBaseURL string
}

// CodexSubscriptionProxyHandler 新入口 HTTP 处理器。
type CodexSubscriptionProxyHandler struct {
	proxy          *Handler
	monitoring     *middleware.MonitoringMiddleware
	logger         *slog.Logger
	snapshot       func() *SubscriptionProxySnapshot
	passthrough    *subscriptionPassthrough
	pool           subscriptionPoolServer
	ws             subscriptionWebSocketServer
	wsRegistry     *subscriptionWSRegistry
	activeRequests atomic.Int64
}

// NewCodexSubscriptionProxyHandler 创建处理器；WS 与池分支由 subscription_ws.go / subscription_pool.go 装配。
func NewCodexSubscriptionProxyHandler(opts SubscriptionProxyHandlerOptions) *CodexSubscriptionProxyHandler {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	snapshot := opts.Snapshot
	if snapshot == nil {
		// 未提供运行态快照时入口始终返回 503：这是装配遗漏，明确告警而不是静默失败。
		logger.Warn("⚠️ [Codex 订阅代理] 未提供运行态快照提供器，入口将始终返回 503")
		snapshot = func() *SubscriptionProxySnapshot { return nil }
	}
	baseURL := strings.TrimSuffix(strings.TrimSpace(opts.UpstreamBaseURL), "/")
	if baseURL == "" {
		baseURL = chatGPTCodexBackendBaseURL
	}
	h := &CodexSubscriptionProxyHandler{
		proxy:      opts.Proxy,
		monitoring: opts.Monitoring,
		logger:     logger,
		snapshot:   snapshot,
	}
	h.passthrough = newSubscriptionPassthrough(opts.Proxy, logger, baseURL)
	h.pool = newSubscriptionPoolAdapter(opts.Proxy, logger)
	wsBaseURL := strings.TrimSuffix(strings.TrimSpace(opts.UpstreamWSBaseURL), "/")
	if wsBaseURL == "" {
		wsBaseURL = chatGPTCodexBackendWSBaseURL
	}
	h.ws = newSubscriptionWSProxy(h.passthrough, logger, wsBaseURL)
	h.wsRegistry = h.ws.(*subscriptionWSProxy).registry
	return h
}

// CloseActiveWebSockets 关闭全部活动 WS 并拒绝新连接；停用/重载入口时调用。
func (h *CodexSubscriptionProxyHandler) CloseActiveWebSockets() int {
	if h == nil || h.wsRegistry == nil {
		return 0
	}
	return h.wsRegistry.closeAll()
}

// ReopenWebSockets 允许新一轮运行重新接受 WS 连接。
func (h *CodexSubscriptionProxyHandler) ReopenWebSockets() {
	if h == nil || h.wsRegistry == nil {
		return
	}
	h.wsRegistry.reopen()
}

// ActiveWebSockets 返回当前活动 WS 连接数。
func (h *CodexSubscriptionProxyHandler) ActiveWebSockets() int {
	if h == nil || h.wsRegistry == nil {
		return 0
	}
	return h.wsRegistry.count()
}

// ActiveRequests 返回正在处理的请求数（含已升级的 WS 握手所在请求）。
func (h *CodexSubscriptionProxyHandler) ActiveRequests() int64 {
	return h.activeRequests.Load()
}

// ServeHTTP 实现 http.Handler。
func (h *CodexSubscriptionProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	snap := h.snapshot()
	if snap == nil {
		writeAccountPipelineError(w, http.StatusServiceUnavailable, "codex_subscription_proxy_stopped", "Codex subscription proxy is not running")
		return
	}
	if !subscriptionHostMatches(r.Host, snap.ListenAddr()) {
		h.logger.Debug("🚫 [Codex 订阅代理] Host 不匹配", "remote", r.RemoteAddr)
		writeAccountPipelineError(w, http.StatusForbidden, "codex_subscription_host_mismatch", "Host header does not match the configured loopback listener")
		return
	}
	capability, ok := parseSubscriptionCapabilityPath(r.URL)
	if !ok {
		writeAccountPipelineError(w, http.StatusNotFound, "codex_subscription_path_not_found", "expected /subscription/<local-access-key>/backend-api/codex/<operation>")
		return
	}
	if !snap.VerifyAccessSecret(capability.Secret) {
		h.logger.Warn("🚫 [Codex 订阅代理] 本地访问密钥校验失败", "remote", r.RemoteAddr)
		writeAccountPipelineError(w, http.StatusForbidden, "codex_subscription_access_denied", "invalid local access key")
		return
	}
	if r.Header.Get("Origin") != "" {
		h.logger.Warn("🚫 [Codex 订阅代理] 拒绝带 Origin 的跨域请求", "remote", r.RemoteAddr)
		writeAccountPipelineError(w, http.StatusForbidden, "codex_subscription_origin_rejected", "cross-origin requests are not allowed")
		return
	}
	route, status := resolveSubscriptionRoute(r.Method, capability.Operation, isWebSocketUpgradeRequest(r))
	if route == nil {
		echoed := truncateSubscriptionOperation(capability.Operation)
		if status == http.StatusMethodNotAllowed {
			writeAccountPipelineError(w, status, "codex_subscription_method_not_allowed", fmt.Sprintf("method %s is not supported for operation %q", r.Method, echoed))
			return
		}
		writeAccountPipelineError(w, http.StatusNotFound, "codex_subscription_operation_not_supported", fmt.Sprintf("operation %q is not supported by the Codex subscription proxy", echoed))
		return
	}

	h.serveRoute(w, r, route, snap)
}

// serveRoute 完成 RequestID 注入、头剥离与分发。
func (h *CodexSubscriptionProxyHandler) serveRoute(w http.ResponseWriter, r *http.Request, route *subscriptionRoute, snap *SubscriptionProxySnapshot) {
	start := time.Now()
	clientIP := subscriptionRemoteIP(r.RemoteAddr)
	userAgent := truncateSubscriptionText(r.UserAgent(), 50)
	requestID := h.newRequestID(clientIP, userAgent, r.Method, route.InternalPath)

	info := &subscriptionRequestInfo{
		RequestID:   requestID,
		Route:       route,
		Mode:        snap.Mode,
		Revision:    snap.Revision,
		Association: extractSubscriptionClientAssociation(r.Header),
	}
	ctx := context.WithValue(r.Context(), "conn_id", requestID) //nolint:staticcheck // 与旧入口 LoggingMiddleware 使用同一 key
	ctx = withSubscriptionRequestInfo(ctx, info)
	r = r.WithContext(ctx)
	stripSubscriptionLocalHeaders(r.Header)
	r.URL.Path = route.InternalPath
	r.URL.RawPath = ""

	rw := newSubscriptionResponseWriter(w)
	h.activeRequests.Add(1)
	defer h.activeRequests.Add(-1)

	h.logger.Debug(fmt.Sprintf("📝 [Codex 订阅代理] [%s] %s %s", requestID, r.Method, route.InternalPath),
		"conn_id", requestID, "kind", string(route.Kind), "mode", snap.Mode, "client_ip", clientIP)

	switch {
	case route.WebSocket:
		h.ws.serve(rw, r, route, snap, requestID)
	case route.Kind == subscriptionRouteResponses && snap.IsPoolMode():
		h.pool.serve(rw, r, route, requestID)
	default:
		h.passthrough.serve(rw, r, route, requestID)
	}

	duration := time.Since(start)
	h.recordResponse(requestID, rw.status(), duration, rw.bytesWritten(), rw.hijacked)
	h.logger.Debug(fmt.Sprintf("📤 [Codex 订阅代理] [%s] %s %s → %d (%s)", requestID, r.Method, route.InternalPath, rw.status(), duration.Round(time.Millisecond)),
		"conn_id", requestID, "kind", string(route.Kind), "status_code", rw.status(), "bytes_written", rw.bytesWritten())
}

// newRequestID 复用旧入口的 ID 生成：有监控中间件时登记一次连接，否则只生成同格式 ID。
func (h *CodexSubscriptionProxyHandler) newRequestID(clientIP, userAgent, method, path string) string {
	if h.monitoring != nil {
		return h.monitoring.RecordRequest("unknown", clientIP, userAgent, method, path)
	}
	return monitor.NewConnectionID()
}

// recordResponse 收尾监控连接。101 是协议切换确认而不是失败，
// 监控中间件按 HTTP 语义统计成功/失败，因此握手成功的 WS 按成功归类；
// 生成级别的成败由 P2 的请求追踪按 request_kind 单独记录。
func (h *CodexSubscriptionProxyHandler) recordResponse(requestID string, status int, duration time.Duration, bytes int64, hijacked bool) {
	if h.monitoring == nil || requestID == "" {
		return
	}
	metricStatus := status
	if hijacked && status == http.StatusSwitchingProtocols {
		metricStatus = http.StatusOK
	}
	h.monitoring.RecordResponse(requestID, metricStatus, duration, bytes, "unknown")
}

// subscriptionHostMatches 要求 Host 与监听地址完全一致（忽略大小写；IPv6 方括号形式按 host:port 比较）。
func subscriptionHostMatches(host, listenAddr string) bool {
	host = strings.TrimSpace(host)
	if host == "" || listenAddr == "" {
		return false
	}
	if strings.EqualFold(host, listenAddr) {
		return true
	}
	hostName, hostPort, err := net.SplitHostPort(host)
	if err != nil {
		return false
	}
	listenName, listenPort, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return false
	}
	if hostPort != listenPort {
		return false
	}
	hostIP, listenIP := net.ParseIP(hostName), net.ParseIP(listenName)
	return hostIP != nil && listenIP != nil && hostIP.Equal(listenIP)
}

func subscriptionRemoteIP(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}

func truncateSubscriptionText(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

// subscriptionResponseWriter 记录状态码与字节数，并转发 Flush / Hijack（WS 升级依赖）。
type subscriptionResponseWriter struct {
	http.ResponseWriter
	statusCode int
	bytes      int64
	hijacked   bool
}

func newSubscriptionResponseWriter(w http.ResponseWriter) *subscriptionResponseWriter {
	return &subscriptionResponseWriter{ResponseWriter: w}
}

func (rw *subscriptionResponseWriter) WriteHeader(code int) {
	if rw.statusCode == 0 {
		rw.statusCode = code
	}
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *subscriptionResponseWriter) Write(p []byte) (int, error) {
	if rw.statusCode == 0 {
		rw.statusCode = http.StatusOK
	}
	n, err := rw.ResponseWriter.Write(p)
	rw.bytes += int64(n)
	return n, err
}

func (rw *subscriptionResponseWriter) Flush() {
	if flusher, ok := rw.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
		return
	}
	_ = http.NewResponseController(rw.ResponseWriter).Flush()
}

// Hijack 通过 ResponseController 沿 Unwrap 链取得底层连接。
func (rw *subscriptionResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, brw, err := http.NewResponseController(rw.ResponseWriter).Hijack()
	if err == nil {
		rw.hijacked = true
		if rw.statusCode == 0 {
			rw.statusCode = http.StatusSwitchingProtocols
		}
	}
	return conn, brw, err
}

func (rw *subscriptionResponseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}

func (rw *subscriptionResponseWriter) status() int {
	if rw.statusCode == 0 {
		return http.StatusOK
	}
	return rw.statusCode
}

func (rw *subscriptionResponseWriter) bytesWritten() int64 {
	return rw.bytes
}
