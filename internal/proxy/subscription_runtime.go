// Package proxy - Codex 订阅代理：独立监听与运行态
// 创建时间: 2026-09-10
//
// 独立回环 listener 与状态机：stopped → starting → running → draining → stopped，
// 启动失败进入 failed 且保留真实错误。端口冲突明确报错，不自动换端口。
// 已运行时只原子热替换配置快照（模式、压缩、密钥哈希立即生效），不重启 listener。
package proxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// 运行态取值；与前端展示、Wails DTO 共用同一组字符串。
const (
	SubscriptionProxyStateStopped  = "stopped"
	SubscriptionProxyStateStarting = "starting"
	SubscriptionProxyStateRunning  = "running"
	SubscriptionProxyStateDraining = "draining"
	SubscriptionProxyStateFailed   = "failed"
)

const (
	// subscriptionServerReadHeaderTimeout 只限制握手头部读取；Hijack 后 deadline 会被清除。
	subscriptionServerReadHeaderTimeout = 30 * time.Second
	// subscriptionServerIdleTimeout keep-alive 空闲超时。
	subscriptionServerIdleTimeout = 120 * time.Second
	// subscriptionShutdownTimeout 停用时的等待上限。
	subscriptionShutdownTimeout = 10 * time.Second
	// subscriptionListenAttempts / subscriptionListenRetryDelay：刚停用的端口可能尚未从
	// 内核取回，同一地址做有界重试；不换端口，不做无限等待。
	subscriptionListenAttempts   = 5
	subscriptionListenRetryDelay = 100 * time.Millisecond
)

// ErrSubscriptionProxyListenAddressLocked 运行中不接受监听地址变更。
var ErrSubscriptionProxyListenAddressLocked = errors.New("Codex 订阅代理正在运行，请先停用再修改监听地址")

// SubscriptionProxyStatus 运行态快照。
type SubscriptionProxyStatus struct {
	State            string `json:"state"`
	Enabled          bool   `json:"enabled"`
	Mode             string `json:"mode"`
	Revision         int64  `json:"revision"`
	ListenAddr       string `json:"listen_addr"`
	ActiveRequests   int64  `json:"active_requests"`
	ActiveWebSockets int    `json:"active_websockets"`
	LastError        string `json:"last_error"`
}

// CodexSubscriptionProxyRuntime 新入口的运行态宿主。
type CodexSubscriptionProxyRuntime struct {
	handler *CodexSubscriptionProxyHandler
	logger  *slog.Logger

	// opMu 串行化启停操作；shutdown 期间不阻塞 Status 读取。
	opMu sync.Mutex

	mu         sync.Mutex
	state      string
	lastError  string
	listenAddr string
	server     *http.Server
	lifecycle  *subscriptionRequestLifecycle

	snapshot atomic.Pointer[SubscriptionProxySnapshot]
}

// NewCodexSubscriptionProxyRuntime 创建运行态；默认 stopped。
func NewCodexSubscriptionProxyRuntime(handler *CodexSubscriptionProxyHandler, logger *slog.Logger) *CodexSubscriptionProxyRuntime {
	if logger == nil {
		logger = slog.Default()
	}
	return &CodexSubscriptionProxyRuntime{handler: handler, logger: logger, state: SubscriptionProxyStateStopped}
}

// Snapshot 返回当前生效的配置快照；未运行时为 nil。
func (rt *CodexSubscriptionProxyRuntime) Snapshot() *SubscriptionProxySnapshot {
	if rt == nil {
		return nil
	}
	return rt.snapshot.Load()
}

// Status 返回运行态摘要。
func (rt *CodexSubscriptionProxyRuntime) Status() SubscriptionProxyStatus {
	if rt == nil {
		return SubscriptionProxyStatus{State: SubscriptionProxyStateStopped}
	}
	rt.mu.Lock()
	status := SubscriptionProxyStatus{
		State:      rt.state,
		ListenAddr: rt.listenAddr,
		LastError:  rt.lastError,
	}
	rt.mu.Unlock()

	if snap := rt.Snapshot(); snap != nil {
		status.Enabled = snap.Enabled
		status.Mode = snap.Mode
		status.Revision = snap.Revision
		if status.ListenAddr == "" {
			status.ListenAddr = snap.ListenAddr()
		}
	}
	if rt.handler != nil {
		status.ActiveRequests = rt.handler.ActiveRequests()
		status.ActiveWebSockets = rt.handler.ActiveWebSockets()
	}
	return status
}

// ValidateListenAddressChange 在保存配置前判断监听地址是否可改；运行中只允许保持原地址。
func (rt *CodexSubscriptionProxyRuntime) ValidateListenAddressChange(host string, port int) error {
	if rt == nil {
		return nil
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.server == nil || rt.listenAddr == "" {
		return nil
	}
	if net.JoinHostPort(host, fmt.Sprintf("%d", port)) == rt.listenAddr {
		return nil
	}
	return ErrSubscriptionProxyListenAddressLocked
}

// Apply 应用配置快照：未启用则停用；地址变化需先停用；其余情况原地热替换。
func (rt *CodexSubscriptionProxyRuntime) Apply(ctx context.Context, snap *SubscriptionProxySnapshot) error {
	if rt == nil {
		return nil
	}
	rt.opMu.Lock()
	defer rt.opMu.Unlock()

	if snap == nil || !snap.Enabled {
		return rt.stop(ctx)
	}
	if rt.isRunning() {
		if current := rt.currentListenAddr(); current != "" && current != snap.ListenAddr() {
			return ErrSubscriptionProxyListenAddressLocked
		}
		rt.snapshot.Store(snap)
		return nil
	}
	return rt.start(snap)
}

// Stop 停用新入口：拒绝新请求、关闭活动 WS、等待在途请求退出。
func (rt *CodexSubscriptionProxyRuntime) Stop(ctx context.Context) error {
	if rt == nil {
		return nil
	}
	rt.opMu.Lock()
	defer rt.opMu.Unlock()
	return rt.stop(ctx)
}

func (rt *CodexSubscriptionProxyRuntime) isRunning() bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.server != nil
}

func (rt *CodexSubscriptionProxyRuntime) currentListenAddr() string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.listenAddr
}

// start 绑定 listener 并启动服务；失败时进入 failed 并保留真实错误。
func (rt *CodexSubscriptionProxyRuntime) start(snap *SubscriptionProxySnapshot) error {
	rt.mu.Lock()
	rt.state = SubscriptionProxyStateStarting
	rt.lastError = ""
	rt.mu.Unlock()

	addr := snap.ListenAddr()
	listener, err := listenSubscriptionAddress(addr)
	if err != nil {
		rt.setFailed(fmt.Sprintf("监听 %s 失败: %v", addr, err))
		return fmt.Errorf("Codex 订阅代理监听 %s 失败: %w", addr, err)
	}

	if rt.handler != nil {
		rt.handler.ReopenWebSockets()
	}
	lifecycle := newSubscriptionRequestLifecycle(rt.handler)
	server := &http.Server{
		Handler:           lifecycle,
		ReadHeaderTimeout: subscriptionServerReadHeaderTimeout,
		// ReadTimeout/WriteTimeout 保持 0：Hijack 后的 WS 连接不能带 deadline。
		IdleTimeout: subscriptionServerIdleTimeout,
	}

	rt.mu.Lock()
	rt.listenAddr = listener.Addr().String()
	rt.server = server
	rt.lifecycle = lifecycle
	rt.state = SubscriptionProxyStateRunning
	rt.mu.Unlock()

	rt.snapshot.Store(snap)
	rt.logger.Info("🚀 [Codex 订阅代理] 已启动",
		"listen", addr, "mode", snap.Mode, "revision", snap.Revision)

	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			rt.setFailed(fmt.Sprintf("服务退出: %v", err))
			rt.logger.Error("❌ [Codex 订阅代理] 服务异常退出", "error", err)
		}
	}()
	return nil
}

// stop 执行停用流程；可重复调用。
func (rt *CodexSubscriptionProxyRuntime) stop(ctx context.Context) error {
	rt.mu.Lock()
	server, lifecycle := rt.server, rt.lifecycle
	rt.server, rt.lifecycle = nil, nil
	if server != nil {
		rt.state = SubscriptionProxyStateDraining
	}
	rt.mu.Unlock()

	if server == nil {
		rt.mu.Lock()
		rt.state = SubscriptionProxyStateStopped
		rt.listenAddr = ""
		rt.mu.Unlock()
		rt.snapshot.Store(nil)
		return nil
	}

	closedWS := 0
	if rt.handler != nil {
		closedWS = rt.handler.CloseActiveWebSockets()
	}

	shutdownCtx, cancel := context.WithTimeout(ctx, subscriptionShutdownTimeout)
	defer cancel()
	err := lifecycle.shutdown(shutdownCtx, server)

	rt.snapshot.Store(nil)
	rt.mu.Lock()
	rt.listenAddr = ""
	if err != nil {
		rt.state = SubscriptionProxyStateFailed
		rt.lastError = fmt.Sprintf("停用超时或失败: %v", err)
	} else {
		rt.state = SubscriptionProxyStateStopped
		rt.lastError = ""
	}
	rt.mu.Unlock()

	rt.logger.Info("🛑 [Codex 订阅代理] 已停用", "closed_websockets", closedWS)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return fmt.Errorf("Codex 订阅代理停用失败: %w", err)
	}
	return nil
}

func (rt *CodexSubscriptionProxyRuntime) setFailed(reason string) {
	rt.mu.Lock()
	rt.state = SubscriptionProxyStateFailed
	rt.lastError = reason
	rt.mu.Unlock()
}

// listenSubscriptionAddress 绑定固定地址；仅对“地址仍被上一次监听占用”做有界重试。
func listenSubscriptionAddress(addr string) (net.Listener, error) {
	var lastErr error
	for attempt := 0; attempt < subscriptionListenAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(subscriptionListenRetryDelay)
		}
		listener, err := net.Listen("tcp", addr)
		if err == nil {
			return listener, nil
		}
		if !errors.Is(err, syscall.EADDRINUSE) {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}

// subscriptionRequestLifecycle 在停用写回前等待本入口已接收的请求退出。
// 与旧入口 proxyRequestLifecycle 语义一致，独立一份避免包依赖倒置。
type subscriptionRequestLifecycle struct {
	next   http.Handler
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	closed bool
	active sync.WaitGroup
}

func newSubscriptionRequestLifecycle(next http.Handler) *subscriptionRequestLifecycle {
	ctx, cancel := context.WithCancel(context.Background())
	return &subscriptionRequestLifecycle{next: next, ctx: ctx, cancel: cancel}
}

func (p *subscriptionRequestLifecycle) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		writeAccountPipelineError(w, http.StatusServiceUnavailable, "codex_subscription_proxy_stopped",
			"Codex subscription proxy is shutting down")
		return
	}
	p.active.Add(1)
	p.mu.Unlock()
	defer p.active.Done()

	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(p.ctx, cancel)
	defer stop()
	defer cancel()
	p.next.ServeHTTP(w, r.WithContext(ctx))
}

func (p *subscriptionRequestLifecycle) shutdown(ctx context.Context, server *http.Server) error {
	// 与 Add 共用锁，禁止 Wait 开始后再受理新请求。
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()

	err := server.Shutdown(ctx)
	if err != nil {
		// Shutdown 超时不会等待活跃 handler 退出；取消上游 I/O 后仍需等待其结算。
		p.cancel()
		err = errors.Join(err, server.Close())
	}
	waitDone := make(chan struct{})
	go func() {
		p.active.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-ctx.Done():
		err = errors.Join(err, ctx.Err())
	}
	p.cancel()
	return err
}
