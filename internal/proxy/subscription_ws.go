// Package proxy - Codex 订阅代理：Responses WebSocket 分支
// 创建时间: 2026-09-10
//
// 订阅模式：先按白名单拨通上游，成功后向下游返回 101 并双向桥接；
// 池模式：直接返回 426（客户端的预期降级信号），不建立上游连接。
// 一个下游 WS 对应一个上游 WS，不跨客户端合并。
package proxy

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"cc-forwarder/config"
	"cc-forwarder/internal/proxy/subscriptionws"
	svc "cc-forwarder/internal/service"

	"github.com/coder/websocket"
)

const (
	// subscriptionWSHandshakeTimeout 上游握手超时；长连接寿命不套用该超时。
	subscriptionWSHandshakeTimeout = 30 * time.Second
	// subscriptionWSFailureBodyLimit 回传上游握手失败正文的上限。
	subscriptionWSFailureBodyLimit = 64 << 10
)

// subscriptionWSProxy Responses WS 分支。
type subscriptionWSProxy struct {
	passthrough *subscriptionPassthrough
	logger      *slog.Logger
	baseURL     string
	registry    *subscriptionWSRegistry
}

// newSubscriptionWSProxy 创建 WS 分支；出站 transport 与订阅直通共用。
func newSubscriptionWSProxy(passthrough *subscriptionPassthrough, logger *slog.Logger, wsBaseURL string) *subscriptionWSProxy {
	return &subscriptionWSProxy{
		passthrough: passthrough,
		logger:      logger,
		baseURL:     wsBaseURL,
		registry:    newSubscriptionWSRegistry(),
	}
}

// serve 处理一次 Responses WS 握手。
func (p *subscriptionWSProxy) serve(w http.ResponseWriter, r *http.Request, route *subscriptionRoute, snap *SubscriptionProxySnapshot, requestID string) {
	if snap != nil && snap.IsPoolMode() {
		// 426 是客户端的降级信号：池模式不建立上游 WS，也不消费订阅凭据。
		writeAccountPipelineError(w, http.StatusUpgradeRequired, "codex_subscription_pool_mode",
			"the Codex subscription proxy serves Responses over HTTP in account pool mode")
		return
	}
	if strings.TrimSpace(r.Header.Get("Authorization")) == "" {
		writeAccountPipelineError(w, http.StatusUnauthorized, "codex_subscription_unauthorized",
			"missing client authorization; the Codex subscription proxy does not substitute local credentials")
		return
	}

	client, err := p.passthrough.httpClient()
	if err != nil {
		p.logger.Error("❌ [Codex 订阅代理] WS 出站客户端不可用", "conn_id", requestID, "error", err)
		writeAccountPipelineError(w, http.StatusServiceUnavailable, "codex_subscription_transport_unavailable",
			"failed to initialize the subscription proxy transport")
		return
	}

	options := subscriptionWSBridgeOptions(snap, p.passthrough.config())
	headers := buildSubscriptionWebSocketHeaders(r.Header, route)
	targetURL := strings.TrimSuffix(p.baseURL, "/") + "/" + route.Operation
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	handshakeCtx, cancelHandshake := context.WithTimeout(r.Context(), subscriptionWSHandshakeTimeout)
	upstream, upstreamResp, err := subscriptionws.DialUpstream(handshakeCtx, targetURL, headers, client, options)
	cancelHandshake()
	if err != nil {
		p.relayHandshakeFailure(w, upstreamResp, requestID, err)
		return
	}

	// 101 之前的响应头：只补上游的 x-codex-* / x-models-etag 等提示信息；
	// Sec-WebSocket-* 由库按两侧各自协商结果设置，不能复制。
	copySubscriptionWebSocketHandshakeHeaders(w.Header(), upstreamResp.Header)

	downstream, err := subscriptionws.AcceptDownstream(w, r, options)
	if err != nil {
		_ = upstream.CloseNow()
		p.logger.Warn("⚠️ [Codex 订阅代理] 下游 WS 握手失败", "conn_id", requestID, "error", err)
		return
	}

	release, ok := p.registry.register(downstream, upstream)
	if !ok {
		// 入口正在停用：不建立新连接。
		_ = downstream.CloseNow()
		_ = upstream.CloseNow()
		return
	}
	defer release()

	p.logger.Debug("🔌 [Codex 订阅代理] WS 已建立",
		"conn_id", requestID, "mode", snapshotMode(snap), "active", p.registry.count())

	stats := subscriptionws.Bridge(r.Context(), downstream, upstream, options)
	p.logger.Debug("🔌 [Codex 订阅代理] WS 已结束",
		"conn_id", requestID,
		"upstream_messages", stats.DownstreamToUpstream.Messages,
		"downstream_messages", stats.UpstreamToDownstream.Messages,
		"downstream_close_code", int(stats.UpstreamToDownstream.CloseCode))
}

// relayHandshakeFailure 把上游握手失败的真实状态与正文回传下游；网络层失败用 502/504。
func (p *subscriptionWSProxy) relayHandshakeFailure(w http.ResponseWriter, upstreamResp *http.Response, requestID string, err error) {
	if upstreamResp == nil {
		status := http.StatusBadGateway
		code := "codex_subscription_upstream_unreachable"
		message := "failed to reach the Codex subscription upstream"
		if err != nil && strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
			status = http.StatusGatewayTimeout
			code = "codex_subscription_upstream_timeout"
			message = "the Codex subscription upstream did not complete the WebSocket handshake in time"
		}
		p.logger.Warn(fmt.Sprintf("⚠️ [Codex 订阅代理] [%s] WS 握手失败: %s", requestID, code),
			"conn_id", requestID, "error", err)
		writeAccountPipelineError(w, status, code, message)
		return
	}

	defer upstreamResp.Body.Close()
	copySubscriptionResponseHeaders(w.Header(), upstreamResp.Header)
	w.WriteHeader(upstreamResp.StatusCode)
	_, _ = io.CopyN(w, upstreamResp.Body, subscriptionWSFailureBodyLimit)
	p.logger.Debug("⚠️ [Codex 订阅代理] 上游拒绝 WS 握手",
		"conn_id", requestID, "status_code", upstreamResp.StatusCode)
}

// buildSubscriptionWebSocketHeaders 按白名单重建握手头；没有正文，移除正文相关字段。
func buildSubscriptionWebSocketHeaders(src http.Header, route *subscriptionRoute) http.Header {
	headers := buildSubscriptionPassthroughHeaders(src, route)
	headers.Del("Content-Type")
	headers.Del("Content-Length")
	return headers
}

// subscriptionWSBridgeOptions 按配置快照与请求体上限给出桥接参数。
func subscriptionWSBridgeOptions(snap *SubscriptionProxySnapshot, cfg *config.Config) subscriptionws.Options {
	readLimit := int64(defaultEncodedRequestBodyMaxBytes)
	if limit := requestBodyMaxBytes(cfg); limit > 0 {
		readLimit = limit
	}
	options := subscriptionws.Options{
		ReadLimit:        readLimit,
		WriteTimeout:     subscriptionws.DefaultWriteTimeout,
		MaxQueueMessages: subscriptionws.DefaultMaxQueueMessages,
		MaxQueueBytes:    subscriptionws.DefaultMaxQueueBytes,
	}
	if snap != nil && snap.WSCompression == svc.CodexSubscriptionProxyCompressionDisabled {
		options.CompressionMode = websocket.CompressionDisabled
	} else {
		options.CompressionMode = websocket.CompressionContextTakeover
	}
	return options
}

func snapshotMode(snap *SubscriptionProxySnapshot) string {
	if snap == nil {
		return ""
	}
	return snap.Mode
}

// ---------------------------------------------------------------------------
// 活动连接登记
// ---------------------------------------------------------------------------

// subscriptionWSRegistry 记录活动 WS 连接，供停用时一次性关闭。
// 停用后 register 返回 false，入口不再建立新连接。
type subscriptionWSRegistry struct {
	mu     sync.Mutex
	conns  map[*websocket.Conn]struct{}
	closed bool
}

func newSubscriptionWSRegistry() *subscriptionWSRegistry {
	return &subscriptionWSRegistry{conns: make(map[*websocket.Conn]struct{})}
}

// register 登记一对连接；停用中返回 false。
func (r *subscriptionWSRegistry) register(conns ...*websocket.Conn) (func(), bool) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return func() {}, false
	}
	for _, conn := range conns {
		if conn != nil {
			r.conns[conn] = struct{}{}
		}
	}
	r.mu.Unlock()

	return func() {
		r.mu.Lock()
		for _, conn := range conns {
			delete(r.conns, conn)
		}
		r.mu.Unlock()
	}, true
}

// closeAll 关闭全部活动连接并拒绝后续登记；重复调用安全。
func (r *subscriptionWSRegistry) closeAll() int {
	r.mu.Lock()
	r.closed = true
	conns := make([]*websocket.Conn, 0, len(r.conns))
	for conn := range r.conns {
		conns = append(conns, conn)
	}
	r.conns = make(map[*websocket.Conn]struct{})
	r.mu.Unlock()

	for _, conn := range conns {
		_ = conn.CloseNow()
	}
	return len(conns)
}

// count 返回当前登记的活动连接数。
func (r *subscriptionWSRegistry) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.conns)
}

// reopen 允许新一轮运行重新接受连接。
func (r *subscriptionWSRegistry) reopen() {
	r.mu.Lock()
	r.closed = false
	r.mu.Unlock()
}
