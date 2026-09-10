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
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"cc-forwarder/config"
	"cc-forwarder/internal/transport"
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
	if strings.TrimSpace(r.Header.Get("Authorization")) == "" {
		writeAccountPipelineError(w, http.StatusUnauthorized, "codex_subscription_unauthorized",
			"missing client authorization; the Codex subscription proxy does not substitute local credentials")
		return
	}

	body, bodyErr := normalizeRequestBodyWithMetadata(w, r, p.config())
	if bodyErr != nil {
		p.logger.Debug(fmt.Sprintf("⚠️ [Codex 订阅代理] [%s] 请求体不可用: %s", requestID, bodyErr.Code),
			"conn_id", requestID, "kind", string(route.Kind))
		writeAccountPipelineError(w, bodyErr.StatusCode, bodyErr.Code, bodyErr.Message)
		return
	}
	wireBody := body.normalizedBody
	outboundEncoding := ""
	if route.NormalizeBody {
		wireBody, outboundEncoding = p.prepareOutboundBody(body)
	}

	resp, cancel, err := p.roundTrip(r, route, wireBody, outboundEncoding)
	if err != nil {
		p.writeTransportError(w, r, route, requestID, err)
		return
	}
	defer cancel()
	defer resp.Body.Close()

	if requestID != "" {
		p.logger.Debug(fmt.Sprintf("📥 [Codex 订阅代理] [%s] 上游响应 %d", requestID, resp.StatusCode),
			"conn_id", requestID, "kind", string(route.Kind), "status_code", resp.StatusCode)
	}
	p.writeResponse(w, resp)
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
			p.logger.Warn("⚠️ [Codex 订阅代理] 出站压缩失败，改为明文转发", "error", err)
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
func (p *subscriptionPassthrough) writeResponse(w http.ResponseWriter, resp *http.Response) {
	copySubscriptionResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	if resp.Body == nil {
		return
	}
	// 正文不做改写，Content-Length 保持上游值；逐块 flush 以便 SSE 立即下行。
	writer := subscriptionFlushingWriter{w: w}
	if _, err := io.CopyBuffer(writer, resp.Body, make([]byte, subscriptionCopyBufferSize)); err != nil {
		// 下游断开或上游中断：状态与已写字节已经反映在传输层，这里只记录。
		if p.logger != nil && !errors.Is(err, context.Canceled) {
			p.logger.Debug("⚠️ [Codex 订阅代理] 直通响应复制中断", "error", err)
		}
	}
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
		"conn_id", requestID, "kind", string(route.Kind), "error", err)
	writeAccountPipelineError(w, status, code, message)
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
