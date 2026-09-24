// Package proxy - Claude Code 专用 loopback 入口
// 创建时间: 2026-09-24
//
// Claude Code 以 claude.ai 订阅登录、ANTHROPIC_BASE_URL 指向本入口时，请求携带自己的 OAuth token。
// 入口只做边界校验（Host / Origin / 路径），通过后给请求打上 Claude Code 入口标记，
// 交给与主端口相同的代理处理器；调度器据此决定是否允许命中订阅直通端点。
// 本入口不经过 AuthMiddleware：Authorization 里是 OAuth token，不是网关令牌。
package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cc-forwarder/internal/endpoint"
)

// claudeCodeEntryPaths 入口只放行 Claude Messages 两条路径；其余（含 HEAD /api/hello 预热探测）一律 404
var claudeCodeEntryPaths = map[string]struct{}{
	"/v1/messages":              {},
	"/v1/messages/count_tokens": {},
}

// ClaudeCodeEntryPolicy 入口的路由策略；运行中可热更新，不重启 listener
type ClaudeCodeEntryPolicy struct {
	// Mode 全局模式：auto / subscription / api
	Mode string
}

// resolve 为单个请求算出生效模式
func (p ClaudeCodeEntryPolicy) resolve(_ *http.Request) endpoint.ClaudeCodeEntryRequest {
	return endpoint.ClaudeCodeEntryRequest{Mode: endpoint.NormalizeClaudeCodeEntryMode(p.Mode)}
}

// claudeCodeEntryHandler 边界校验后转交代理处理器
type claudeCodeEntryHandler struct {
	next   http.Handler
	port   string
	policy func() ClaudeCodeEntryPolicy
}

func (h *claudeCodeEntryHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !claudeCodeEntryHostAllowed(r.Host, h.port) {
		writeClaudeCodeEntryError(w, http.StatusForbidden, "permission_error", "Host 与 Claude Code 入口地址不匹配")
		return
	}
	if r.Header.Get("Origin") != "" {
		writeClaudeCodeEntryError(w, http.StatusForbidden, "permission_error", "Claude Code 入口拒绝带 Origin 的跨站请求")
		return
	}
	if _, ok := claudeCodeEntryPaths[r.URL.Path]; !ok {
		writeClaudeCodeEntryError(w, http.StatusNotFound, "not_found_error", "Claude Code 入口只提供 /v1/messages 与 /v1/messages/count_tokens")
		return
	}
	if r.Method != http.MethodPost {
		writeClaudeCodeEntryError(w, http.StatusMethodNotAllowed, "invalid_request_error", "Claude Code 入口只接受 POST")
		return
	}
	entry := h.policy().resolve(r)
	h.next.ServeHTTP(w, r.WithContext(endpoint.WithClaudeCodeEntry(r.Context(), entry)))
}

// claudeCodeEntryHostAllowed 只接受 127.0.0.1 或 localhost 加本入口端口；
// 攻击者域名经 DNS rebinding 指向本机时 Host 是其域名，会被拒绝
func claudeCodeEntryHostAllowed(host, port string) bool {
	name, hostPort, err := net.SplitHostPort(strings.TrimSpace(host))
	if err != nil || hostPort != port {
		return false
	}
	return name == "127.0.0.1" || strings.EqualFold(name, "localhost")
}

// writeClaudeCodeEntryError 以 Anthropic 错误格式返回，Claude Code 可直接展示 message
func writeClaudeCodeEntryError(w http.ResponseWriter, status int, errorType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":  "error",
		"error": map[string]string{"type": errorType, "message": message},
	})
}

// ClaudeCodeEntryStatus 运行态摘要
type ClaudeCodeEntryStatus struct {
	Running    bool   `json:"running"`
	ListenAddr string `json:"listen_addr"`
	Error      string `json:"error"`
}

// ClaudeCodeEntryRuntime 管理入口 listener 的启停；地址固定，不做端口漂移
type ClaudeCodeEntryRuntime struct {
	mu      sync.Mutex
	next    http.Handler
	logger  *slog.Logger
	server  *http.Server
	addr    string
	lastErr string
	policy  atomic.Pointer[ClaudeCodeEntryPolicy]
}

// NewClaudeCodeEntryRuntime 创建运行态；next 为主端口同一代理处理器（不含 AuthMiddleware）
func NewClaudeCodeEntryRuntime(next http.Handler, logger *slog.Logger) *ClaudeCodeEntryRuntime {
	if logger == nil {
		logger = slog.Default()
	}
	rt := &ClaudeCodeEntryRuntime{next: next, logger: logger}
	rt.policy.Store(&ClaudeCodeEntryPolicy{Mode: endpoint.ClaudeCodeEntryModeAuto})
	return rt
}

// Policy 当前生效的入口策略
func (rt *ClaudeCodeEntryRuntime) Policy() ClaudeCodeEntryPolicy {
	return *rt.policy.Load()
}

// Apply 先热更新策略（对下一个请求生效），再按配置启停；运行中地址变化时先停旧 listener 再绑定新地址
func (rt *ClaudeCodeEntryRuntime) Apply(ctx context.Context, enabled bool, listenAddr string, policy ClaudeCodeEntryPolicy) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	rt.policy.Store(&policy)
	if !enabled {
		rt.lastErr = ""
		return rt.stopLocked(ctx)
	}
	if rt.server != nil && rt.addr == listenAddr {
		return nil
	}
	if err := rt.stopLocked(ctx); err != nil {
		return err
	}
	return rt.startLocked(listenAddr)
}

// Stop 停止 listener；等待在途请求直到 ctx 截止，超时后强制关闭
func (rt *ClaudeCodeEntryRuntime) Stop(ctx context.Context) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.stopLocked(ctx)
}

// Status 返回运行态摘要
func (rt *ClaudeCodeEntryRuntime) Status() ClaudeCodeEntryStatus {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return ClaudeCodeEntryStatus{Running: rt.server != nil, ListenAddr: rt.addr, Error: rt.lastErr}
}

func (rt *ClaudeCodeEntryRuntime) startLocked(listenAddr string) error {
	listener, err := listenSubscriptionAddress(listenAddr)
	if err != nil {
		rt.lastErr = fmt.Sprintf("监听 %s 失败: %v", listenAddr, err)
		return fmt.Errorf("Claude Code 入口%s", rt.lastErr)
	}
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	server := &http.Server{
		Handler:           &claudeCodeEntryHandler{next: rt.next, port: port, policy: rt.Policy},
		ReadHeaderTimeout: 30 * time.Second,
		WriteTimeout:      0, // 流式响应不设写超时，与主端口一致
		IdleTimeout:       120 * time.Second,
	}
	rt.server = server
	rt.addr = listenAddr
	rt.lastErr = ""
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			rt.failIfCurrent(server, err)
		}
	}()
	rt.logger.Info("✅ Claude Code 入口已启动", "listen", listenAddr)
	return nil
}

func (rt *ClaudeCodeEntryRuntime) stopLocked(ctx context.Context) error {
	server := rt.server
	if server == nil {
		return nil
	}
	rt.server = nil
	rt.addr = ""
	if err := server.Shutdown(ctx); err != nil {
		// 在途长流超过截止时间：强制关闭，释放端口
		_ = server.Close()
		rt.logger.Warn("⚠️ Claude Code 入口在途请求未在截止前结束，已强制关闭", "error", err)
	}
	rt.logger.Info("🛑 Claude Code 入口已停止")
	return nil
}

// failIfCurrent listener 意外退出时记录错误；若已被新的 server 替换则忽略
func (rt *ClaudeCodeEntryRuntime) failIfCurrent(server *http.Server, err error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.server != server {
		return
	}
	rt.server = nil
	rt.addr = ""
	rt.lastErr = fmt.Sprintf("监听意外退出: %v", err)
	rt.logger.Error("❌ Claude Code 入口监听意外退出", "error", err)
}
