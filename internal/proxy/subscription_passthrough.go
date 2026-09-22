// Package proxy - Codex 订阅代理：订阅凭据直通（HTTP）
// 创建时间: 2026-09-10
//
// 本文件只负责“客户端凭据透传”的 HTTP 转发：models、compact、search、images、
// History/Notes 与语音建连。Responses 在订阅模式下也走这里，池模式下改由
// subscription_pool.go 处理。
//
// 出站请求头一律从空集合按订阅白名单重建，客户端 Cookie / x-api-key /
// X-Forwarded-* / Origin 等在进入本文件前已被剥离；正文规范化复用旧入口的
// zstd 处理，出站长度按最终正文重新计算。
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"time"

	"cc-forwarder/config"
	"cc-forwarder/internal/privacy"
	"cc-forwarder/internal/proxy/handlers"
	"cc-forwarder/internal/reasoning"
	"cc-forwarder/internal/tracking"
	"cc-forwarder/internal/transport"

	"github.com/tidwall/gjson"
)

const (
	// subscriptionResponseHeaderTimeout 出站响应头超时；流式正文不设总超时。
	subscriptionResponseHeaderTimeout = 60 * time.Second
	// subscriptionCopyBufferSize 直通复制缓冲区大小。
	subscriptionCopyBufferSize = 32 << 10
)

// subscriptionPassthrough 订阅直通转发器；出站 client 与 transport 惰性创建并复用。
type subscriptionPassthrough struct {
	proxy   *Handler
	logger  *slog.Logger
	baseURL string

	clientOnce sync.Once
	client     *http.Client
	clientErr  error
}

// newSubscriptionPassthrough 创建直通转发器；baseURL 固定为 ChatGPT Codex 后端。
func newSubscriptionPassthrough(proxy *Handler, logger *slog.Logger, baseURL string) *subscriptionPassthrough {
	return &subscriptionPassthrough{proxy: proxy, logger: logger, baseURL: baseURL}
}

// serve 执行一次直通转发。
func (p *subscriptionPassthrough) serve(w http.ResponseWriter, r *http.Request, route *subscriptionRoute, requestID string) {
	if route == nil {
		writeAccountPipelineError(w, http.StatusNotFound, "codex_subscription_operation_not_supported", "unsupported operation")
		return
	}
	observer := newSubscriptionTrackingObserver(nil, nil)
	if p.proxy != nil {
		observer = newSubscriptionTrackingObserver(p.proxy.usageTracker, p.proxy.eventBus)
	}
	kind := ""
	switch route.Kind {
	case subscriptionRouteResponses:
		kind = tracking.RequestKindGeneration
	case subscriptionRouteResponsesCompact:
		kind = tracking.RequestKindCompaction
	}
	track := observer.StartHTTP(SubscriptionHTTPStart{
		RequestID: requestID, StartedAt: time.Now(), Method: r.Method, Path: route.InternalPath,
		RequestKind: kind, IsStreaming: strings.Contains(r.Header.Get("Accept"), "text/event-stream"),
	})
	defer track.Complete(nil, "")
	if strings.TrimSpace(r.Header.Get("Authorization")) == "" {
		status := http.StatusUnauthorized
		track.Complete(&status, "subscription_unauthorized")
		writeAccountPipelineError(w, http.StatusUnauthorized, "codex_subscription_unauthorized",
			"missing client authorization; the Codex subscription proxy does not substitute local credentials")
		return
	}

	body, bodyErr := normalizeRequestBodyWithMetadata(w, r, p.config())
	if bodyErr != nil {
		track.Complete(&bodyErr.StatusCode, bodyErr.Code)
		p.logger.Debug(fmt.Sprintf("⚠️ [Codex 订阅代理] [%s] 请求体不可用: %s", requestID, bodyErr.Code),
			"conn_id", requestID, "kind", string(route.Kind))
		writeAccountPipelineError(w, bodyErr.StatusCode, bodyErr.Code, bodyErr.Message)
		return
	}
	// v6.3：请求模型与思考串在 start 之后才可得，补写进同一行；stream 标记与之前一致
	requestModel, requestReasoning := reasoning.ParseRequest(route.InternalPath, body.normalizedBody)
	track.SetModel(requestModel)
	track.SetRequestProfile(requestModel, requestReasoning)
	track.SetStreaming(gjson.GetBytes(body.normalizedBody, "stream").Bool() || strings.Contains(r.Header.Get("Accept"), "text/event-stream"))
	privacyState := newSubscriptionPrivacyRequestState(requestID, func(_ string, result privacy.ApplyResult, policyErr *privacy.PolicyError) {
		track.RecordPrivacy(result, policyErr)
	})
	r = withSubscriptionPrivacyState(r, privacyState)
	filteredBody, privacyErr := applySubscriptionPrivacy(p.privacyFilter(), r, route.InternalPath, body.normalizedBody)
	if privacyErr != nil {
		if policyErr := handlers.AsPrivacyPolicyError(privacyErr); policyErr != nil {
			track.Complete(&policyErr.StatusCode, policyErr.Code)
			p.logger.Warn("⚠️ [Codex 订阅代理] 请求被隐私策略阻断",
				"conn_id", requestID, "kind", string(route.Kind), "code", policyErr.Code)
			handlers.WritePrivacyPolicyErrorResponse(w, policyErr)
			return
		}
		status := http.StatusUnprocessableEntity
		track.Complete(&status, privacy.CodeScanFailed)
		p.logger.Warn("⚠️ [Codex 订阅代理] 隐私处理失败",
			"conn_id", requestID, "kind", string(route.Kind), "code", privacy.CodeScanFailed)
		writeAccountPipelineError(w, http.StatusUnprocessableEntity, privacy.CodeScanFailed, "privacy scan failed")
		return
	}
	body.normalizedBody = filteredBody
	wireBody := body.normalizedBody
	outboundEncoding := ""
	if route.NormalizeBody {
		wireBody, outboundEncoding = p.prepareOutboundBody(body)
	}
	r = r.WithContext(httptrace.WithClientTrace(r.Context(), &httptrace.ClientTrace{WroteRequest: func(info httptrace.WroteRequestInfo) {
		if info.Err == nil {
			track.MarkOutbound(int64(len(wireBody)), time.Now())
		}
	}}))

	resp, cancel, err := p.roundTrip(r, route, wireBody, outboundEncoding)
	if err != nil {
		status, reason := http.StatusBadGateway, "subscription_upstream_unreachable"
		if r.Context().Err() != nil {
			status, reason = 499, "subscription_client_closed"
		} else if errors.Is(err, context.DeadlineExceeded) {
			status, reason = http.StatusGatewayTimeout, "subscription_upstream_timeout"
		}
		track.Complete(&status, reason)
		p.writeTransportError(w, r, route, requestID, err)
		return
	}
	defer cancel()
	defer resp.Body.Close()
	track.SetHTTPStatus(resp.StatusCode)
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		track.SetStreaming(true)
	}
	var capture *subscriptionResponseCapture
	var responseBytes int64
	if kind != "" && resp.Body != nil {
		capture = &subscriptionResponseCapture{
			compaction: kind == tracking.RequestKindCompaction, httpStatus: resp.StatusCode,
			onResult: track.ApplyParseResult,
			onOutput: func(at time.Time) { track.MarkFirstOutput(responseBytes, at) },
			onWarning: func(code string) {
				p.logger.Warn("⚠️ [Codex 订阅代理] 响应用量解析受限", "conn_id", requestID, "code", code)
			},
		}
		if encoding := strings.TrimSpace(resp.Header.Get("Content-Encoding")); encoding != "" && encoding != "identity" {
			capture.mode = "opaque"
			capture.warn("subscription_response_encoding_unsupported")
		}
		resp.Body = &subscriptionObservedBody{ReadCloser: resp.Body, capture: capture, bytes: &responseBytes, onBytes: track.MarkResponseBytes}
	}

	if requestID != "" {
		level := slog.LevelDebug
		if resp.StatusCode >= http.StatusBadRequest {
			level = slog.LevelWarn
		}
		p.logger.Log(r.Context(), level, fmt.Sprintf("📥 [Codex 订阅代理] [%s] 上游响应 %d", requestID, resp.StatusCode),
			"conn_id", requestID, "kind", string(route.Kind), "status_code", resp.StatusCode)
	}
	copyErr := p.writeResponse(w, resp)
	if capture != nil {
		capture.Finish()
	}
	if copyErr != nil {
		track.Complete(&resp.StatusCode, "subscription_response_interrupted")
	} else if resp.StatusCode >= 400 {
		track.Complete(&resp.StatusCode, "subscription_http_error")
	} else {
		track.Complete(&resp.StatusCode, "")
	}
}

// prepareOutboundBody 按客户端入站压缩重建出站正文：
// 客户端用 zstd 时按规范化后的正文重新压缩（不能复用修改前的压缩体），否则明文转发。
func (p *subscriptionPassthrough) prepareOutboundBody(body *normalizedRequestBodyMetadata) ([]byte, string) {
	if body == nil {
		return nil, ""
	}
	if body.inboundContentEncoding != requestContentEncodingZstd {
		return body.normalizedBody, ""
	}
	compressed, err := compressNormalizedRequestBody(body, body.normalizedBody)
	if err != nil {
		if p.logger != nil {
			p.logger.Warn("⚠️ [Codex 订阅代理] 出站压缩失败，改为明文转发", "code", "subscription_compression_failed")
		}
		return body.normalizedBody, ""
	}
	return compressed, requestContentEncodingZstd
}

// roundTrip 构造并发送上游请求；返回的 cancel 必须由调用方执行。
func (p *subscriptionPassthrough) roundTrip(r *http.Request, route *subscriptionRoute, wireBody []byte, outboundEncoding string) (*http.Response, context.CancelFunc, error) {
	targetURL := strings.TrimSuffix(p.baseURL, "/") + "/" + route.Operation
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	ctx := r.Context()
	cancel := context.CancelFunc(func() {})
	if route.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, route.Timeout)
	}

	req, err := http.NewRequestWithContext(ctx, r.Method, targetURL, bytes.NewReader(wireBody))
	if err != nil {
		cancel()
		return nil, func() {}, err
	}
	req.Header = buildSubscriptionPassthroughHeaders(r.Header, route)
	req.Host = ""
	if outboundEncoding != "" {
		req.Header.Set("Content-Encoding", outboundEncoding)
	}
	req.ContentLength = int64(len(wireBody))

	client, err := p.httpClient()
	if err != nil {
		cancel()
		return nil, func() {}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		cancel()
		return nil, func() {}, err
	}
	return resp, cancel, nil
}

// writeResponse 原样回传上游状态、响应头与正文（含 401/403/429 等协议语义）。
func (p *subscriptionPassthrough) writeResponse(w http.ResponseWriter, resp *http.Response) error {
	copySubscriptionResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	if resp.Body == nil {
		return nil
	}
	// 正文不做改写，Content-Length 保持上游值；逐块 flush 以便 SSE 立即下行。
	writer := subscriptionFlushingWriter{w: w}
	if _, err := io.CopyBuffer(writer, resp.Body, make([]byte, subscriptionCopyBufferSize)); err != nil {
		// 下游断开或上游中断：状态与已写字节已经反映在传输层，这里只记录。
		if p.logger != nil && !errors.Is(err, context.Canceled) {
			p.logger.Debug("⚠️ [Codex 订阅代理] 直通响应复制中断", "code", "subscription_response_copy_interrupted")
		}
		return err
	}
	return nil
}

type subscriptionObservedBody struct {
	io.ReadCloser
	capture *subscriptionResponseCapture
	bytes   *int64
	onBytes func(int64)
}

func (b *subscriptionObservedBody) Read(data []byte) (int, error) {
	n, err := b.ReadCloser.Read(data)
	*b.bytes += int64(n)
	if b.onBytes != nil {
		b.onBytes(*b.bytes)
	}
	if n > 0 {
		_, _ = b.capture.Write(data[:n])
	}
	return n, err
}

// writeTransportError 本地传输失败的安全错误；不泄露内部地址与错误详情。
func (p *subscriptionPassthrough) writeTransportError(w http.ResponseWriter, r *http.Request, route *subscriptionRoute, requestID string, err error) {
	if r.Context().Err() != nil {
		writeAccountPipelineError(w, 499, "codex_subscription_client_closed", "client closed the request before the upstream responded")
		return
	}
	status := http.StatusBadGateway
	code := "codex_subscription_upstream_unreachable"
	message := "failed to reach the Codex subscription upstream"
	if errors.Is(err, context.DeadlineExceeded) {
		status = http.StatusGatewayTimeout
		code = "codex_subscription_upstream_timeout"
		message = "the Codex subscription upstream did not respond in time"
	}
	p.logger.Warn(fmt.Sprintf("⚠️ [Codex 订阅代理] [%s] 直通失败: %s", requestID, code),
		"conn_id", requestID, "kind", string(route.Kind))
	writeAccountPipelineError(w, status, code, message)
}

func (p *subscriptionPassthrough) privacyFilter() handlers.PrivacyFilter {
	if p == nil || p.proxy == nil {
		return nil
	}
	return p.proxy.privacyFilter
}

// httpClient 返回直通共用 client：禁用自动压缩与总超时，只限制响应头等待。
func (p *subscriptionPassthrough) httpClient() (*http.Client, error) {
	p.clientOnce.Do(func() {
		cfg := p.config()
		if cfg == nil {
			p.clientErr = fmt.Errorf("proxy handler config is nil")
			return
		}
		httpTransport, err := transport.CreateTransport(cfg)
		if err != nil {
			p.clientErr = fmt.Errorf("failed to create subscription proxy transport: %w", err)
			return
		}
		httpTransport.DisableCompression = true
		httpTransport.ResponseHeaderTimeout = subscriptionResponseHeaderTimeout
		p.client = &http.Client{Transport: httpTransport}
	})
	return p.client, p.clientErr
}

func (p *subscriptionPassthrough) config() *config.Config {
	if p.proxy == nil {
		return nil
	}
	return p.proxy.config
}

// subscriptionFlushingWriter 每写一块就 flush，保证 SSE 与长响应及时下行。
type subscriptionFlushingWriter struct {
	w http.ResponseWriter
}

func (f subscriptionFlushingWriter) Write(data []byte) (int, error) {
	n, err := f.w.Write(data)
	if err == nil {
		_ = http.NewResponseController(f.w).Flush()
	}
	return n, err
}

// subscriptionResponseCapture 只暂存有界事件/JSON，不保留整段流，也不改动转发正文。
// SSE 仅在此分行，终态和用量仍由共享 TokenParser/ParseResponsesEventJSON 解释。
type subscriptionResponseCapture struct {
	mode        string
	pending     []byte
	parser      *TokenParser
	eventBytes  int
	skipEvent   bool
	discardLine bool
	eventType   string
	compaction  bool
	httpStatus  int
	onResult    func(*ParseResult)
	onOutput    func(time.Time)
	onWarning   func(string)
}

const subscriptionCaptureLimit = 16 << 20

func (c *subscriptionResponseCapture) warn(code string) {
	if c.onWarning != nil {
		c.onWarning(code)
	}
}

func (c *subscriptionResponseCapture) Write(data []byte) (int, error) {
	size := len(data)
	if c.mode == "opaque" {
		return size, nil
	}
	if c.mode == "" {
		c.pending = append(c.pending, data...)
		prefix := bytes.TrimSpace(c.pending)
		if len(prefix) == 0 {
			c.pending = nil
			return size, nil
		}
		if prefix[0] == '{' || prefix[0] == '[' {
			c.mode = "json"
		} else if len(prefix) < 6 {
			return size, nil
		} else if bytes.HasPrefix(prefix, []byte("event:")) || bytes.HasPrefix(prefix, []byte("data:")) || prefix[0] == ':' {
			c.mode = "sse"
			data, c.pending = c.pending, nil
		} else {
			c.mode, c.pending = "opaque", nil
			c.warn("subscription_response_format_unknown")
			return size, nil
		}
	} else if c.mode == "json" {
		if len(c.pending)+len(data) > subscriptionCaptureLimit {
			c.mode, c.pending = "opaque", nil
			c.warn("subscription_response_capture_limit")
			return size, nil
		}
		c.pending = append(c.pending, data...)
	}
	if c.mode == "json" {
		if len(c.pending) > subscriptionCaptureLimit {
			c.mode, c.pending = "opaque", nil
			c.warn("subscription_response_capture_limit")
		}
		return size, nil
	}
	for len(data) > 0 {
		newline := bytes.IndexByte(data, '\n')
		part := data
		if newline >= 0 {
			part = data[:newline]
		}
		if c.eventBytes+len(c.pending)+len(part) > subscriptionCaptureLimit {
			if !c.skipEvent {
				c.warn("subscription_response_event_limit")
			}
			c.skipEvent = true
			c.discardLine = true
			c.pending = nil
		} else if !c.skipEvent {
			c.pending = append(c.pending, part...)
		}
		if c.skipEvent && len(bytes.TrimSpace(part)) > 0 {
			c.discardLine = true
		}
		if newline < 0 {
			break
		}
		if c.skipEvent {
			// 超大事件之后必须跳过到空行，不能把截断片段喂给解析器。
			if !c.discardLine && len(bytes.TrimSpace(part)) == 0 {
				c.skipEvent, c.eventBytes, c.parser = false, 0, nil
				c.eventType = ""
			}
			c.discardLine = false
		} else {
			c.line(string(c.pending))
		}
		c.pending = nil
		data = data[newline+1:]
	}
	return size, nil
}

func (c *subscriptionResponseCapture) line(line string) {
	if c.parser == nil {
		c.parser = NewTokenParser()
	}
	c.eventBytes += len(line)
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "event:") {
		c.eventType = strings.TrimSpace(strings.TrimPrefix(trimmed, "event:"))
	} else if strings.HasPrefix(trimmed, "data:") && c.eventType == "" {
		c.eventType = responsesEventType([]byte(strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))))
	}
	result := c.parser.ParseSSELineV2(line)
	if trimmed == "" {
		if isSubscriptionOutputEvent(c.eventType) && c.onOutput != nil {
			c.onOutput(time.Now())
			c.onOutput = nil
		}
		c.eventBytes, c.eventType = 0, ""
	}
	if result != nil && c.onResult != nil {
		c.onResult(result)
	}
}

func (c *subscriptionResponseCapture) Finish() {
	defer func() { c.pending, c.parser = nil, nil }()
	if c.mode == "sse" {
		if c.skipEvent {
			return
		}
		if len(c.pending) > 0 {
			c.line(string(c.pending))
		}
		c.line("")
		return
	}
	if c.mode != "json" || !json.Valid(c.pending) {
		return
	}
	var envelope struct {
		Type   string          `json:"type"`
		Object string          `json:"object"`
		Status string          `json:"status"`
		Error  json.RawMessage `json:"error"`
		Output json.RawMessage `json:"output"`
	}
	if json.Unmarshal(c.pending, &envelope) != nil {
		return
	}
	event := envelope.Type
	if len(envelope.Error) > 0 && string(envelope.Error) != "null" || c.httpStatus >= 400 {
		event = "error"
	} else if event == "" {
		switch envelope.Status {
		case "completed":
			event = "response.completed"
		case "failed":
			event = "response.failed"
		case "incomplete":
			event = "response.incomplete"
		case "cancelled", "canceled":
			event = "response.cancelled"
		default:
			if c.compaction && (envelope.Object == "response.compaction" || len(envelope.Output) > 0) {
				event = "response.completed"
			}
		}
	}
	if event != "" && c.onResult != nil {
		c.onResult(ParseResponsesEventJSON(event, c.pending))
	}
}
