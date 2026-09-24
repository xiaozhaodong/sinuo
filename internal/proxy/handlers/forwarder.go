package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"time"

	"cc-forwarder/config"
	"cc-forwarder/internal/endpoint"
	"cc-forwarder/internal/modelrewrite"
	"cc-forwarder/internal/transport"
)

const (
	anthropicBetaHeader     = "anthropic-beta"
	anyrouteContextBetaFlag = "context-1m-2025-08-07"
)

// Forwarder 负责HTTP请求转发和头部处理
type Forwarder struct {
	config          *config.Config
	endpointManager *endpoint.Manager
	transportOnce   sync.Once
	transportErr    error
	httpTransport   *http.Transport
	// 🛡️ 出站隐私过滤（可选注入，nil 时不影响原有链路）
	privacyFilter PrivacyFilter
}

// SetPrivacyFilter 注入出站隐私过滤依赖
func (f *Forwarder) SetPrivacyFilter(filter PrivacyFilter) {
	f.privacyFilter = filter
}

// NewForwarder 创建新的Forwarder实例
func NewForwarder(cfg *config.Config, endpointManager *endpoint.Manager) *Forwarder {
	return &Forwarder{
		config:          cfg,
		endpointManager: endpointManager,
	}
}

// ForwardRequestToEndpoint 转发请求到指定端点
func (f *Forwarder) ForwardRequestToEndpoint(ctx context.Context, r *http.Request, bodyBytes []byte, ep *endpoint.Endpoint) (*http.Response, error) {
	bodyBytes, _ = prepareBodyForEndpoint(r.URL.Path, bodyBytes, ep)

	// 🛡️ 出站隐私过滤（PolicyError 由调用方短路，不进入 retry/failover）
	bodyBytes, err := ApplyPrivacyFilterForEndpoint(f.privacyFilter, r, bodyBytes, ep)
	if err != nil {
		return nil, err
	}

	// 创建目标URL
	targetURL := ep.Config.URL + r.URL.Path
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	// 创建请求
	req, err := http.NewRequestWithContext(ctx, r.Method, targetURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// 复制和修改头部
	f.CopyHeaders(r, req, ep)

	// 创建HTTP传输
	httpTransport, err := f.buildStreamingTransport()
	if err != nil {
		return nil, fmt.Errorf("failed to create transport: %w", err)
	}

	client := &http.Client{
		Timeout:   0, // 流式请求无超时
		Transport: httpTransport,
	}

	// 执行请求
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	// 检查响应状态
	if resp.StatusCode >= 400 {
		detail := BuildUpstreamErrorDetail(resp, ep.Config.Name, defaultUpstreamErrorPreviewLimit)
		resp.Body.Close()
		if detail == "" {
			return nil, fmt.Errorf("endpoint returned error: %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("endpoint returned error: %d %s", resp.StatusCode, detail)
	}

	return resp, nil
}

// ForwardStreamingRequestToEndpoint 为流式请求创建独立的 upstream context。
// 这样下游客户端断开后，调用方仍可短时继续 drain 上游尾部，以补齐终止事件和 usage。
func (f *Forwarder) ForwardStreamingRequestToEndpoint(r *http.Request, bodyBytes []byte, ep *endpoint.Endpoint, onWroteRequest ...func(time.Time)) (*http.Response, context.CancelFunc, error) {
	upstreamCtx, release := context.WithCancel(context.Background())
	if len(onWroteRequest) > 0 && onWroteRequest[0] != nil {
		trace := &httptrace.ClientTrace{
			WroteRequest: func(httptrace.WroteRequestInfo) {
				onWroteRequest[0](time.Now())
			},
		}
		upstreamCtx = httptrace.WithClientTrace(upstreamCtx, trace)
	}
	bodyBytes, _ = prepareBodyForEndpoint(r.URL.Path, bodyBytes, ep)

	// 🛡️ 出站隐私过滤（PolicyError 由调用方短路，不进入 retry/failover）
	bodyBytes, err := ApplyPrivacyFilterForEndpoint(f.privacyFilter, r, bodyBytes, ep)
	if err != nil {
		release()
		return nil, nil, err
	}

	// 创建目标URL
	targetURL := ep.Config.URL + r.URL.Path
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	req, err := http.NewRequestWithContext(upstreamCtx, r.Method, targetURL, bytes.NewReader(bodyBytes))
	if err != nil {
		release()
		return nil, nil, fmt.Errorf("failed to create request: %w", err)
	}

	// 复制和修改头部
	f.CopyHeaders(r, req, ep)

	httpTransport, err := f.buildStreamingTransport()
	if err != nil {
		release()
		return nil, nil, fmt.Errorf("failed to create transport: %w", err)
	}

	client := &http.Client{
		Timeout:   0,
		Transport: httpTransport,
	}

	resp, err := client.Do(req)
	if err != nil {
		release()
		return nil, nil, fmt.Errorf("request failed: %w", err)
	}

	if resp.StatusCode >= 400 {
		detail := BuildUpstreamErrorDetail(resp, ep.Config.Name, defaultUpstreamErrorPreviewLimit)
		resp.Body.Close()
		release()
		if detail == "" {
			return nil, nil, fmt.Errorf("endpoint returned error: %d", resp.StatusCode)
		}
		return nil, nil, fmt.Errorf("endpoint returned error: %d %s", resp.StatusCode, detail)
	}

	return resp, release, nil
}

// prepareBodyForEndpoint 应用端点级模型改写；第二个返回值是改写后的上游模型，未改写时为空串。
func prepareBodyForEndpoint(path string, bodyBytes []byte, ep *endpoint.Endpoint) ([]byte, string) {
	return rewriteEndpointModel(path, bodyBytes, ep)
}

func rewriteEndpointModel(path string, bodyBytes []byte, ep *endpoint.Endpoint) ([]byte, string) {
	if ep == nil || strings.TrimSpace(ep.Config.ModelRewriteRules) == "" || !isClaudeMessagesPath(path) || len(bytes.TrimSpace(bodyBytes)) == 0 {
		return bodyBytes, ""
	}

	var payload map[string]any
	if !decodeSingleJSON(bodyBytes, &payload) {
		return bodyBytes, ""
	}
	model, ok := payload["model"].(string)
	if !ok {
		return bodyBytes, ""
	}
	target, rewritten, err := modelrewrite.Rewrite(ep.Config.ModelRewriteRules, path, model)
	if err != nil {
		slog.Warn("⚠️ [端点模型改写] 忽略无效规则", "endpoint", ep.Config.Name, "error", err)
		return bodyBytes, ""
	}
	if !rewritten {
		return bodyBytes, ""
	}

	payload["model"] = target
	rewrittenBody, err := json.Marshal(payload)
	if err != nil {
		return bodyBytes, ""
	}
	slog.Debug("🔀 [端点模型改写] 已替换上游模型", "endpoint", ep.Config.Name, "from", model, "to", target, "path", path)
	return rewrittenBody, target
}

func isClaudeMessagesPath(path string) bool {
	return path == "/v1/messages" || path == "/v1/messages/count_tokens"
}

func decodeSingleJSON(bodyBytes []byte, target any) bool {
	decoder := json.NewDecoder(bytes.NewReader(bodyBytes))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return false
	}
	var trailing any
	return decoder.Decode(&trailing) == io.EOF
}

func (f *Forwarder) buildStreamingTransport() (*http.Transport, error) {
	f.transportOnce.Do(func() {
		httpTransport, err := transport.CreateTransport(f.config)
		if err != nil {
			f.transportErr = err
			return
		}

		// 优化传输设置用于流式处理
		httpTransport.DisableKeepAlives = false
		httpTransport.MaxIdleConns = 10
		httpTransport.MaxIdleConnsPerHost = 16
		httpTransport.MaxConnsPerHost = 32
		httpTransport.IdleConnTimeout = 0 // 无空闲超时
		httpTransport.TLSHandshakeTimeout = 10 * time.Second
		httpTransport.ExpectContinueTimeout = 1 * time.Second

		// 从配置中读取响应头超时时间，默认60秒
		responseHeaderTimeout := f.config.Streaming.ResponseHeaderTimeout
		if responseHeaderTimeout == 0 {
			responseHeaderTimeout = 60 * time.Second
		}
		httpTransport.ResponseHeaderTimeout = responseHeaderTimeout

		httpTransport.DisableCompression = true // 禁用压缩以防缓冲延迟
		httpTransport.WriteBufferSize = 4096    // 较小的写缓冲区
		httpTransport.ReadBufferSize = 4096     // 较小的读缓冲区

		f.httpTransport = httpTransport
	})

	if f.transportErr != nil {
		return nil, f.transportErr
	}
	return f.httpTransport, nil
}

// CopyHeaders 复制头部逻辑
func (f *Forwarder) CopyHeaders(src *http.Request, dst *http.Request, ep *endpoint.Endpoint) {
	f.copyHeaders(src, dst, ep.Config.URL, ep.Config.Headers, outboundCredentials{
		token:       f.endpointManager.GetTokenForEndpoint(ep),
		apiKey:      f.endpointManager.GetApiKeyForEndpoint(ep),
		clientOAuth: ep.Config.IsClientOAuth(),
	})
}

// CopyAttemptHeaders 只使用 admission 快照中已解析的凭据与 headers。
// 即使快照凭据为空，也不得回退到运行态组内继承。
func (f *Forwarder) CopyAttemptHeaders(src *http.Request, dst *http.Request, target *endpoint.EndpointAttemptTarget) {
	cfg := target.Config()
	f.copyHeaders(src, dst, cfg.URL, cfg.Headers, outboundCredentials{
		token:       cfg.Token,
		apiKey:      cfg.ApiKey,
		clientOAuth: cfg.IsClientOAuth(),
	})
}

// outboundCredentials 出站认证来源：端点凭据，或透传客户端的 OAuth（订阅直通）
type outboundCredentials struct {
	token       string
	apiKey      string
	clientOAuth bool
}

// configuredSkipHeaders 端点凭据模式：删除客户端认证头，改用端点凭据
var configuredSkipHeaders = map[string]bool{
	"host":          true, // We'll set this based on target endpoint
	"authorization": true, // We'll add our own if configured
	"x-api-key":     true, // Remove sensitive client API keys
}

// clientOAuthSkipHeaders 订阅直通模式：保留客户端 Authorization（OAuth token），
// 其余沿用黑名单复制，另外剔除 Cookie、代理与来源类头。上游固定为 api.anthropic.com，
// Claude Code 直连时本来就会把其余头发给它，原样转发等价于直连。
var clientOAuthSkipHeaders = map[string]bool{
	"host":                true,
	"x-api-key":           true,
	"cookie":              true,
	"proxy-authorization": true,
	"forwarded":           true,
	"origin":              true,
	"referer":             true,
}

func (f *Forwarder) copyHeaders(src *http.Request, dst *http.Request, targetURL string, headers map[string]string, creds outboundCredentials) {
	copyClientHeaders(src.Header, dst.Header, creds.clientOAuth)

	// Set Host header based on target endpoint URL
	if u, err := url.Parse(targetURL); err == nil {
		dst.Header.Set("Host", u.Host)
		// Also set the Host field directly on the request for proper HTTP/1.1 behavior
		dst.Host = u.Host
	}

	// 订阅直通不注入端点凭据（保存前已校验为空，这里再兜底一次）
	if !creds.clientOAuth {
		if creds.token != "" {
			dst.Header.Set("Authorization", "Bearer "+creds.token)
		}
		if creds.apiKey != "" {
			dst.Header.Set("X-Api-Key", creds.apiKey)
		}
	}

	// Add custom headers from endpoint configuration
	for key, value := range headers {
		if creds.clientOAuth && clientOAuthProtectedHeader(key) {
			continue
		}
		dst.Header.Set(key, value)
	}
	if isAnyRouteURL(targetURL) {
		ensureBetaFlag(dst.Header, anyrouteContextBetaFlag)
	}

	// Remove hop-by-hop headers
	hopByHopHeaders := []string{
		"Connection",
		"Keep-Alive",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Te",
		"Trailers",
		"Transfer-Encoding",
		"Upgrade",
	}

	for _, header := range hopByHopHeaders {
		dst.Header.Del(header)
	}
}

// copyClientHeaders 按认证方式复制客户端请求头
func copyClientHeaders(src, dst http.Header, clientOAuth bool) {
	skipHeaders := configuredSkipHeaders
	if clientOAuth {
		skipHeaders = clientOAuthSkipHeaders
	}
	for key, values := range src {
		lower := strings.ToLower(key)
		if skipHeaders[lower] || (clientOAuth && strings.HasPrefix(lower, "x-forwarded-")) {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

// clientOAuthProtectedHeader 订阅直通端点的自定义头不得覆盖认证类头
func clientOAuthProtectedHeader(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "authorization", "x-api-key", "cookie", "proxy-authorization":
		return true
	}
	return false
}

var anyRouteHosts = map[string]struct{}{
	"anyrouter.top":                      {},
	"a-ocnfniawgw.cn-shanghai.fcapp.run": {},
}

func isAnyRouteURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") {
		return false
	}
	_, ok := anyRouteHosts[strings.ToLower(parsed.Hostname())]
	return ok
}

// ensureBetaFlag 确保 anthropic-beta 头包含指定标志：
// 头不存在时直接设置；已存在但缺该标志时追加，保留客户端原有标志；已包含则不动。
func ensureBetaFlag(header http.Header, flag string) {
	existing := header.Get(anthropicBetaHeader)
	if existing == "" {
		header.Set(anthropicBetaHeader, flag)
		return
	}
	for _, part := range strings.Split(existing, ",") {
		if strings.EqualFold(strings.TrimSpace(part), flag) {
			return
		}
	}
	header.Set(anthropicBetaHeader, existing+","+flag)
}

// ForwardForPipeline 新端点转发管线专用（方案 §4.4）：
//   - 挂载 GotConn / TLSHandshakeDone / WroteHeaders / WroteRequest 四事件 trace，
//     供决策表以 WroteHeaders 判定重放安全边界；
//   - 保留 4xx/5xx 响应结构（分类交给决策表，不在此转换为 error）；
//   - detachUpstream 为 true 时创建独立 upstream context（下游断开后仍可短时 drain 尾部），返回其 release；
//   - 非 SSE 请求恢复端点级总超时，SSE 保持无总超时，避免长流被硬切断。
func (f *Forwarder) ForwardForPipeline(ctx context.Context, r *http.Request, bodyBytes []byte, target *endpoint.EndpointAttemptTarget, isSSE, detachUpstream bool, onWroteRequest func(time.Time)) (*http.Response, context.CancelFunc, *UpstreamTraceState, error) {
	// Config 返回 admission 快照的独立副本；后续 helper 继续复用既有逻辑，
	// 但不会再读取 Manager 持有的可变 Endpoint 或动态凭据状态。
	ep := &endpoint.Endpoint{Config: target.Config()}
	bodyBytes, upstreamModel := prepareBodyForEndpoint(r.URL.Path, bodyBytes, ep)

	// 🛡️ 出站隐私过滤（PolicyError 由调用方短路，不进入换候选/标记）
	bodyBytes, err := ApplyPrivacyFilterForEndpoint(f.privacyFilter, r, bodyBytes, ep)
	if err != nil {
		return nil, nil, nil, err
	}

	requestCtx := ctx
	var release context.CancelFunc
	if detachUpstream {
		requestCtx, release = context.WithCancel(context.Background())
	}
	requestCtx, traceState := WithUpstreamTrace(requestCtx, onWroteRequest)
	traceState.setUpstreamModel(upstreamModel)

	targetURL := ep.Config.URL + r.URL.Path
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	req, err := http.NewRequestWithContext(requestCtx, r.Method, targetURL, bytes.NewReader(bodyBytes))
	if err != nil {
		if release != nil {
			release()
		}
		return nil, nil, traceState, fmt.Errorf("failed to create request: %w", err)
	}
	f.CopyAttemptHeaders(r, req, target)

	httpTransport, err := f.buildStreamingTransport()
	if err != nil {
		if release != nil {
			release()
		}
		return nil, nil, traceState, fmt.Errorf("failed to create transport: %w", err)
	}
	requestTimeout := time.Duration(0)
	if !isSSE {
		requestTimeout = ep.Config.Timeout
	}
	client := &http.Client{
		Timeout:   requestTimeout,
		Transport: httpTransport,
	}

	resp, err := client.Do(req)
	if err != nil {
		if release != nil {
			release()
		}
		return nil, nil, traceState, fmt.Errorf("request failed: %w", err)
	}
	return resp, release, traceState, nil
}
