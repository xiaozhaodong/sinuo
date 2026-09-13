// Package proxy - Codex 订阅代理：Responses WebSocket 分支
// 创建时间: 2026-09-10
//
// 订阅模式：先按白名单拨通上游，成功后向下游返回 101 并双向桥接；
// 池模式：直接返回 426（客户端的预期降级信号），不建立上游连接。
// 一个下游 WS 对应一个上游 WS，不跨客户端合并。
package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"cc-forwarder/config"
	"cc-forwarder/internal/monitor"
	"cc-forwarder/internal/privacy"
	"cc-forwarder/internal/proxy/handlers"
	"cc-forwarder/internal/proxy/subscriptionws"
	svc "cc-forwarder/internal/service"
	"cc-forwarder/internal/tracking"

	"github.com/coder/websocket"
)

const (
	// subscriptionWSHandshakeTimeout 上游握手超时；长连接寿命不套用该超时。
	subscriptionWSHandshakeTimeout     = 30 * time.Second
	subscriptionUnsupportedWSEventCode = "codex_subscription_ws_event_unsupported"
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
		p.logger.Error("❌ [Codex 订阅代理] WS 出站客户端不可用", "conn_id", requestID, "code", "subscription_ws_transport_unavailable")
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
	defer cancelHandshake()
	upstream, upstreamResp, err := dialSubscriptionWebSocket(handshakeCtx, targetURL, headers, client, options)
	if err != nil {
		// 失败正文仍依赖握手请求的上下文；读完或中断后才能取消。
		controller := http.NewResponseController(w)
		if deadline, ok := handshakeCtx.Deadline(); ok {
			_ = controller.SetWriteDeadline(deadline)
			defer controller.SetWriteDeadline(time.Time{})
		}
		p.relayHandshakeFailure(w, upstreamResp, requestID, err)
		return
	}
	cancelHandshake()

	// 101 之前的响应头：只补上游的 x-codex-* / x-models-etag 等提示信息；
	// Sec-WebSocket-* 由库按两侧各自协商结果设置，不能复制。
	copySubscriptionWebSocketHandshakeHeaders(w.Header(), upstreamResp.Header)

	downstream, err := subscriptionws.AcceptDownstream(w, r, options)
	if err != nil {
		_ = upstream.CloseNow()
		p.logger.Warn("⚠️ [Codex 订阅代理] 下游 WS 握手失败", "conn_id", requestID, "code", "subscription_ws_downstream_handshake_failed")
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

	observer := newSubscriptionTrackingObserver(nil, nil)
	if p.passthrough.proxy != nil {
		observer = newSubscriptionTrackingObserver(p.passthrough.proxy.usageTracker, p.passthrough.proxy.eventBus)
	}
	submissions := newSubscriptionSubmissionState(r.Context(), requestID, observer)
	options.Interceptor = &subscriptionWSInterceptor{
		filter:      p.passthrough.privacyFilter(),
		logger:      p.logger,
		submissions: submissions,
	}
	bridgeStarted := time.Now()
	stats := subscriptionws.Bridge(r.Context(), downstream, upstream, options)
	p.registry.mu.Lock()
	proxyStopping := p.registry.closed
	p.registry.mu.Unlock()
	submissions.close(subscriptionWSInterruptionReason(stats, proxyStopping))
	p.logBridgeEnd(requestID, stats, time.Since(bridgeStarted), options, proxyStopping)
}

// subscriptionWSInterruptionReason 只影响追踪呈现，不改变连接关闭和转发行为。
// EOF 只能证明客户端连接结束；协议错误、超时等仍按失败处理，不能推断正常续接。
func subscriptionWSInterruptionReason(stats subscriptionws.BridgeStats, proxyStopping bool) string {
	first := stats.FirstObserved
	if proxyStopping || stats.ParentContextError != "none" ||
		first.Direction != subscriptionws.DirectionDownstreamToUpstream || first.Phase != "read" ||
		(first.Error.Kind != "eof" && first.Error.Kind != "normal_close") {
		return "connection_closed"
	}
	for _, end := range []subscriptionws.IOEnd{
		stats.DownstreamToUpstream.ReadEnd, stats.DownstreamToUpstream.WriteEnd,
		stats.UpstreamToDownstream.ReadEnd, stats.UpstreamToDownstream.WriteEnd,
	} {
		switch end.Error.Kind {
		case "", "none", "eof", "normal_close", "context_canceled", "connection_closed", "queue_closed":
			// 允许同一次关闭过程中的正常清理结果；有独立错误时保留失败。
		default:
			return "connection_closed"
		}
	}
	return tracking.SubscriptionClientDisconnected
}

// logBridgeEnd 拆成一条起因摘要和两条方向摘要，避免 SimpleHandler 默认 500 字节截断。
// request_id 通过结算日志中的 conn_id 关联；所有字段均不含原始错误正文。
func (p *subscriptionWSProxy) logBridgeEnd(connectionID string, stats subscriptionws.BridgeStats, duration time.Duration, options subscriptionws.Options, proxyStopping bool) {
	level := slog.LevelWarn
	if proxyStopping || stats.FirstObserved.Error.Kind == "normal_close" || stats.ParentContextError == "context_canceled" {
		level = slog.LevelInfo
	}
	p.logger.Log(context.Background(), level, "🔌 [Codex 订阅代理] WS 已结束",
		"conn_id", connectionID, "duration_ms", duration.Milliseconds(), "proxy_stopping", proxyStopping,
		"parent_context_error", stats.ParentContextError,
		"first_observed_at", stats.FirstObserved.At,
		"first_direction", string(stats.FirstObserved.Direction),
		subscriptionWSEndLog("first_observed", stats.FirstObserved))
	for _, lane := range []struct {
		direction string
		stats     subscriptionws.LaneStats
	}{
		{"client_to_upstream", stats.DownstreamToUpstream},
		{"upstream_to_client", stats.UpstreamToDownstream},
	} {
		p.logger.Log(context.Background(), level, "🔌 [Codex 订阅代理] WS 方向已结束",
			"conn_id", connectionID, "direction", lane.direction,
			"messages", lane.stats.Messages, "bytes", lane.stats.Bytes,
			"compression_mode", int(options.CompressionMode), "read_limit", options.ReadLimit,
			"write_timeout_ms", options.WriteTimeout.Milliseconds(),
			subscriptionWSEndLog("read_end", lane.stats.ReadEnd),
			subscriptionWSEndLog("write_end", lane.stats.WriteEnd))
	}
}

func subscriptionWSEndLog(key string, end subscriptionws.IOEnd) slog.Attr {
	if end.Error.Kind == "" {
		return slog.Group(key, "error_kind", "none")
	}
	return slog.Group(key, "phase", end.Phase, "error_kind", end.Error.Kind, "root_error_type", end.Error.RootType,
		"close_code", int(end.Error.CloseCode))
}

// subscriptionWSHandshakeTransport 独占一次拨号的失败响应体，避免 websocket 库
// 为调试读取并关闭原始正文（库只保留前 1024 字节）。成功 101 的双向 Body 原样交给库。
type subscriptionWSHandshakeTransport struct {
	base     http.RoundTripper
	response *http.Response
}

func (t *subscriptionWSHandshakeTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	// http.Client 跟随重定向时，前一个响应不再需要转发。
	t.closeResponse()
	resp, err := t.base.RoundTrip(r)
	if err != nil || resp.StatusCode == http.StatusSwitchingProtocols {
		return resp, err
	}
	t.response = resp
	stub := *resp
	stub.Body = http.NoBody
	return &stub, nil
}

func (t *subscriptionWSHandshakeTransport) closeResponse() {
	if t.response != nil {
		_ = t.response.Body.Close()
		t.response = nil
	}
}

func dialSubscriptionWebSocket(ctx context.Context, targetURL string, headers http.Header, client *http.Client, options subscriptionws.Options) (*websocket.Conn, *http.Response, error) {
	transport := &subscriptionWSHandshakeTransport{base: client.Transport}
	if transport.base == nil {
		transport.base = http.DefaultTransport
	}
	defer transport.closeResponse()
	handshakeClient := *client
	handshakeClient.Transport = transport
	conn, resp, err := subscriptionws.DialUpstream(ctx, targetURL, headers, &handshakeClient, options)
	if err != nil && resp != nil && transport.response != nil {
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
		resp = transport.response
		transport.response = nil // 所有权移交给 relayHandshakeFailure。
	}
	return conn, resp, err
}

// relayHandshakeFailure 把上游握手失败的真实状态与正文回传下游；网络层失败用 502/504。
func (p *subscriptionWSProxy) relayHandshakeFailure(w http.ResponseWriter, upstreamResp *http.Response, requestID string, err error) {
	if upstreamResp != nil && upstreamResp.StatusCode == http.StatusSwitchingProtocols {
		// 非法 101（例如缺少 Upgrade 头）不能被回传成成功升级。
		if upstreamResp.Body != nil {
			_ = upstreamResp.Body.Close()
		}
		upstreamResp = nil
	}
	if upstreamResp == nil {
		status := http.StatusBadGateway
		code := "codex_subscription_upstream_unreachable"
		message := "failed to reach the Codex subscription upstream"
		if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
			code = "codex_subscription_upstream_timeout"
			message = "the Codex subscription upstream did not complete the WebSocket handshake in time"
		}
		diagnostic := subscriptionws.DescribeError(err)
		p.logger.Warn(fmt.Sprintf("⚠️ [Codex 订阅代理] [%s] WS 握手失败: %s", requestID, code),
			"conn_id", requestID, "error_kind", diagnostic.Kind, "root_error_type", diagnostic.RootType)
		writeAccountPipelineError(w, status, code, message)
		return
	}

	defer upstreamResp.Body.Close()
	copySubscriptionResponseHeaders(w.Header(), upstreamResp.Header)
	// 由 net/http 按实际写出内容定帧，不能沿用可能被上游错误声明的长度。
	w.Header().Del("Content-Length")
	w.WriteHeader(upstreamResp.StatusCode)
	if _, copyErr := io.Copy(w, upstreamResp.Body); copyErr != nil {
		p.logger.Warn("⚠️ [Codex 订阅代理] WS 握手失败正文传输中断",
			"conn_id", requestID, "status_code", upstreamResp.StatusCode, "code", "subscription_ws_failure_body_incomplete")
		// 状态已经写出，禁止正常结束分块响应而把残缺正文呈现为完整响应。
		panic(http.ErrAbortHandler)
	}
	p.logger.Debug("⚠️ [Codex 订阅代理] 上游拒绝 WS 握手",
		"conn_id", requestID, "status_code", upstreamResp.StatusCode)
}

// subscriptionWSInterceptor 将隐私处理与传输桥隔离：PolicyError 生成本地 error 帧，
// 返回 nil Forward 保证被拒正文零出站，但不返回连接级 error、也不主动 close。
type subscriptionWSInterceptor struct {
	filter        handlers.PrivacyFilter
	logger        *slog.Logger
	submissions   *subscriptionSubmissionState
	writeMu       sync.Mutex
	pendingWrites []*subscriptionSubmission
}

func (i *subscriptionWSInterceptor) Intercept(ctx context.Context, direction subscriptionws.Direction, payload []byte) (subscriptionws.InterceptResult, error) {
	if direction == subscriptionws.DirectionUpstreamToDownstream {
		if i.submissions != nil {
			i.submissions.handleUpstream(payload)
		}
		return subscriptionws.InterceptResult{Forward: payload}, nil
	}

	if err := validateSubscriptionJSON(payload); err != nil {
		i.warn("invalid_json")
		return subscriptionws.InterceptResult{Reply: subscriptionWSErrorReply(http.StatusBadRequest, subscriptionInvalidJSONCode,
			"request contains invalid or ambiguous JSON")}, nil
	}

	submission, isCreate := i.submissions.beginCreate(payload)
	requestID := monitor.NewConnectionID()
	privacyState := newSubscriptionPrivacyRequestState(requestID, nil)
	requestContext := ctx
	if submission != nil {
		requestID = submission.RequestID
		privacyState = submission.PrivacyState
		requestContext = submission.Context
	} else {
		requestContext = context.WithValue(ctx, "conn_id", requestID) //nolint:staticcheck
		requestContext = handlers.WithPrivacyRequestState(requestContext, privacyState)
	}
	req := (&http.Request{Method: http.MethodPost, Header: http.Header{"Content-Type": []string{"application/json"}}}).WithContext(requestContext)
	filtered, err := applySubscriptionPrivacy(i.filter, req, "/v1/responses", payload)
	if err == nil {
		if !isCreate {
			i.submissions.warn("unsupported_client_event")
			i.warn(subscriptionUnsupportedWSEventCode)
			return subscriptionws.InterceptResult{Reply: subscriptionWSErrorReply(http.StatusBadRequest,
				subscriptionUnsupportedWSEventCode, "unsupported WebSocket client event")}, nil
		}
		i.writeMu.Lock()
		i.pendingWrites = append(i.pendingWrites, submission)
		i.writeMu.Unlock()
		return subscriptionws.InterceptResult{Forward: filtered}, nil
	}
	policyErr := handlers.AsPrivacyPolicyError(err)
	if policyErr == nil {
		policyErr = &privacy.PolicyError{StatusCode: http.StatusUnprocessableEntity, Code: privacy.CodeScanFailed, Message: "privacy scan failed"}
	}
	policyErr = sanitizeSubscriptionPolicyError(policyErr)
	if isCreate && submission != nil {
		i.submissions.reject(submission, policyErr)
	} else {
		i.submissions.warn("non_create_privacy_rejected")
	}
	i.warn(policyErr.Code)
	return subscriptionws.InterceptResult{Reply: subscriptionWSErrorReply(policyErr.StatusCode, policyErr.Code, policyErr.Message)}, nil
}

func (i *subscriptionWSInterceptor) Forwarded(direction subscriptionws.Direction, size int64, at time.Time) {
	if direction != subscriptionws.DirectionDownstreamToUpstream {
		return
	}
	i.writeMu.Lock()
	var submission *subscriptionSubmission
	if len(i.pendingWrites) > 0 {
		submission = i.pendingWrites[0]
		i.pendingWrites[0] = nil
		i.pendingWrites = i.pendingWrites[1:]
	}
	i.writeMu.Unlock()
	if submission != nil {
		if observer, ok := i.submissions.observer.(interface {
			SubmissionOutbound(*subscriptionSubmission, int64, time.Time)
		}); ok {
			observer.SubmissionOutbound(submission, size, at)
		}
	}
}

func (i *subscriptionWSInterceptor) warn(code string) {
	if i == nil || i.logger == nil {
		return
	}
	connectionID := ""
	if i.submissions != nil {
		connectionID = i.submissions.connectionID
	}
	i.logger.Warn("⚠️ [Codex 订阅代理] WS 提交被本地拒绝", "conn_id", connectionID, "code", code)
}

func subscriptionWSErrorReply(status int, code, message string) []byte {
	if status < 400 || status > 499 {
		status = http.StatusUnprocessableEntity
	}
	payload, _ := json.Marshal(struct {
		Type   string `json:"type"`
		Status int    `json:"status"`
		Error  struct {
			Type    string `json:"type"`
			Code    string `json:"code,omitempty"`
			Message string `json:"message"`
		} `json:"error"`
	}{
		Type:   "error",
		Status: status,
		Error: struct {
			Type    string `json:"type"`
			Code    string `json:"code,omitempty"`
			Message string `json:"message"`
		}{Type: "invalid_request_error", Code: code, Message: message},
	})
	return payload
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
