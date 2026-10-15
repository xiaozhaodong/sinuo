package proxy

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sinuo/config"
	svc "sinuo/internal/service"

	"github.com/coder/websocket"
)

const runtimeTestSecret = "runtime-secret"

func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("获取空闲端口失败: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

func newSubscriptionRuntime(t *testing.T, upstreamBaseURL string) *CodexSubscriptionProxyRuntime {
	t.Helper()
	var runtime *CodexSubscriptionProxyRuntime
	handler := NewCodexSubscriptionProxyHandler(SubscriptionProxyHandlerOptions{
		Proxy:             NewHandler(nil, &config.Config{}),
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		UpstreamBaseURL:   upstreamBaseURL,
		UpstreamWSBaseURL: upstreamBaseURL,
		Snapshot: func() *SubscriptionProxySnapshot {
			if runtime == nil {
				return nil
			}
			return runtime.Snapshot()
		},
	})
	runtime = NewCodexSubscriptionProxyRuntime(handler, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runtime.Stop(stopCtx)
	})
	return runtime
}

// waitForPortFree 等待端口从内核取回；关闭监听到可重新绑定之间可能有短暂延迟。
func waitForPortFree(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			_ = listener.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("端口 %d 未在预期时间内释放", port)
}

func runtimeSnapshot(port int, mode string) *SubscriptionProxySnapshot {
	return &SubscriptionProxySnapshot{
		Revision:      1,
		Enabled:       true,
		Mode:          mode,
		ListenHost:    "127.0.0.1",
		ListenPort:    port,
		WSCompression: svc.CodexSubscriptionProxyCompressionAuto,
		AccessHash:    svc.HashCodexSubscriptionProxyAccessSecret(runtimeTestSecret),
	}
}

func TestSubscriptionProxyRuntimeStartsAndStops(t *testing.T) {
	port := freeLoopbackPort(t)
	runtime := newSubscriptionRuntime(t, "http://upstream.invalid")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runtime.Apply(ctx, runtimeSnapshot(port, svc.CodexSubscriptionProxyModeSubscription)); err != nil {
		t.Fatalf("启动失败: %v", err)
	}

	status := runtime.Status()
	if status.State != SubscriptionProxyStateRunning {
		t.Fatalf("状态应为 running: %+v", status)
	}
	if status.ListenAddr == "" || !strings.HasSuffix(status.ListenAddr, fmt.Sprintf(":%d", port)) {
		t.Fatalf("监听地址不符: %+v", status)
	}
	if runtime.Snapshot() == nil {
		t.Fatal("运行中应有配置快照")
	}

	if err := runtime.Stop(ctx); err != nil {
		t.Fatalf("停用失败: %v", err)
	}
	if status := runtime.Status(); status.State != SubscriptionProxyStateStopped {
		t.Fatalf("停用后状态应为 stopped: %+v", status)
	}
	if runtime.Snapshot() != nil {
		t.Fatal("停用后不应残留配置快照")
	}

	// 端口已释放，可被重新绑定。
	waitForPortFree(t, port)
}

func TestSubscriptionProxyRuntimeSwapsSnapshotWithoutRestart(t *testing.T) {
	port := freeLoopbackPort(t)
	runtime := newSubscriptionRuntime(t, "http://upstream.invalid")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runtime.Apply(ctx, runtimeSnapshot(port, svc.CodexSubscriptionProxyModeSubscription)); err != nil {
		t.Fatalf("启动失败: %v", err)
	}

	runtime.mu.Lock()
	serverBefore := runtime.server
	runtime.mu.Unlock()

	next := runtimeSnapshot(port, svc.CodexSubscriptionProxyModePool)
	next.Revision = 2
	if err := runtime.Apply(ctx, next); err != nil {
		t.Fatalf("热替换失败: %v", err)
	}

	runtime.mu.Lock()
	serverAfter := runtime.server
	runtime.mu.Unlock()
	if serverBefore != serverAfter {
		t.Fatal("同地址热替换不应重启 listener")
	}
	snapshot := runtime.Snapshot()
	if snapshot == nil || snapshot.Mode != svc.CodexSubscriptionProxyModePool || snapshot.Revision != 2 {
		t.Fatalf("快照未热替换: %+v", snapshot)
	}
}

func TestSubscriptionProxyRuntimePortConflictReportsFailure(t *testing.T) {
	port := freeLoopbackPort(t)
	blocker, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("占用端口失败: %v", err)
	}
	defer blocker.Close()

	runtime := newSubscriptionRuntime(t, "http://upstream.invalid")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := runtime.Apply(ctx, runtimeSnapshot(port, svc.CodexSubscriptionProxyModeSubscription)); err == nil {
		t.Fatal("端口冲突应返回错误")
	}
	status := runtime.Status()
	if status.State != SubscriptionProxyStateFailed {
		t.Fatalf("端口冲突应进入 failed: %+v", status)
	}
	if !strings.Contains(status.LastError, fmt.Sprintf(":%d", port)) {
		t.Fatalf("失败原因应包含监听地址: %q", status.LastError)
	}
	if runtime.Snapshot() != nil {
		t.Fatal("启动失败不应留下配置快照")
	}

	// 不换端口：错误后仍不应有任何监听。
	if _, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
		t.Fatal("端口仍被占用时应报错")
	}
}

func TestSubscriptionProxyRuntimeRejectsListenChangeWhileRunning(t *testing.T) {
	port := freeLoopbackPort(t)
	runtime := newSubscriptionRuntime(t, "http://upstream.invalid")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := runtime.Apply(ctx, runtimeSnapshot(port, svc.CodexSubscriptionProxyModeSubscription)); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	if err := runtime.ValidateListenAddressChange("127.0.0.1", port+1); err != ErrSubscriptionProxyListenAddressLocked {
		t.Fatalf("运行中改端口应被拒绝: %v", err)
	}
	if err := runtime.ValidateListenAddressChange("127.0.0.1", port); err != nil {
		t.Fatalf("同地址应放行: %v", err)
	}
	if err := runtime.Apply(ctx, runtimeSnapshot(port+1, svc.CodexSubscriptionProxyModeSubscription)); err != ErrSubscriptionProxyListenAddressLocked {
		t.Fatalf("运行中 Apply 新端口应被拒绝: %v", err)
	}

	// 停用后即可改地址。
	if err := runtime.Stop(ctx); err != nil {
		t.Fatalf("停用失败: %v", err)
	}
	if err := runtime.ValidateListenAddressChange("127.0.0.1", port+1); err != nil {
		t.Fatalf("停用后应允许改端口: %v", err)
	}
}

func TestSubscriptionProxyRuntimeStopClosesActiveWebSockets(t *testing.T) {
	upstream := newEchoWebSocketServer(t)
	port := freeLoopbackPort(t)
	runtime := newSubscriptionRuntime(t, "ws"+strings.TrimPrefix(upstream.URL, "http"))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runtime.Apply(ctx, runtimeSnapshot(port, svc.CodexSubscriptionProxyModeSubscription)); err != nil {
		t.Fatalf("启动失败: %v", err)
	}

	target := fmt.Sprintf("ws://127.0.0.1:%d/subscription/%s/backend-api/codex/responses", port, runtimeTestSecret)
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()
	conn, _, err := websocket.Dial(dialCtx, target, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer client-token"}},
	})
	if err != nil {
		t.Fatalf("WS 握手失败: %v", err)
	}
	defer conn.CloseNow()

	writeCtx, writeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer writeCancel()
	const warmup = `{"type":"response.create","generate":false,"input":[]}`
	if err := conn.Write(writeCtx, websocket.MessageText, []byte(warmup)); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if _, data, err := conn.Read(writeCtx); err != nil || string(data) != warmup {
		t.Fatalf("回显失败: %v %q", err, string(data))
	}
	if got := runtime.Status().ActiveWebSockets; got != 2 {
		t.Fatalf("活动 WS 连接数应为 2（上下游各一）: %d", got)
	}

	stopped := make(chan error, 1)
	go func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		stopped <- runtime.Stop(stopCtx)
	}()

	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("停用失败: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("停用未能在活动 WS 存在时结束")
	}

	readCtx, readCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer readCancel()
	if _, _, err := conn.Read(readCtx); err == nil {
		t.Fatal("停用后 WS 应被关闭")
	}
	if got := runtime.Status().ActiveWebSockets; got != 0 {
		t.Fatalf("停用后不应残留 WS 登记: %d", got)
	}
}

func TestSubscriptionProxyRuntimeStopDoesNotTouchOtherServers(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("old-entry"))
	}))
	defer other.Close()

	port := freeLoopbackPort(t)
	runtime := newSubscriptionRuntime(t, "http://upstream.invalid")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runtime.Apply(ctx, runtimeSnapshot(port, svc.CodexSubscriptionProxyModeSubscription)); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	if err := runtime.Stop(ctx); err != nil {
		t.Fatalf("停用失败: %v", err)
	}

	resp, err := other.Client().Get(other.URL)
	if err != nil {
		t.Fatalf("旧入口在新入口停用后仍应可用: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "old-entry" {
		t.Fatalf("旧入口响应异常: %q", string(body))
	}
}

func TestSubscriptionProxyRuntimeDisabledApplyIsNoop(t *testing.T) {
	runtime := newSubscriptionRuntime(t, "http://upstream.invalid")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := runtime.Apply(ctx, nil); err != nil {
		t.Fatalf("空快照应当作停用: %v", err)
	}
	disabled := runtimeSnapshot(0, svc.CodexSubscriptionProxyModeSubscription)
	disabled.Enabled = false
	if err := runtime.Apply(ctx, disabled); err != nil {
		t.Fatalf("未启用应当作停用: %v", err)
	}
	status := runtime.Status()
	if status.State != SubscriptionProxyStateStopped || status.ListenAddr != "" {
		t.Fatalf("未启用不应监听: %+v", status)
	}
}

// newEchoWebSocketServer 本地 WS 回显上游，替代真实 ChatGPT 后端。
func newEchoWebSocketServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			typ, data, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			if err := conn.Write(r.Context(), typ, data); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	return server
}
