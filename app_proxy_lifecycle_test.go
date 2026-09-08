package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"cc-forwarder/internal/store"
)

func TestProxyRequestLifecycle_GracefulDrainAndRejectNewRequests(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	resume := func() { once.Do(func() { close(release) }) }
	defer resume()
	requestCancelled := make(chan bool, 1)
	lifecycle := newProxyRequestLifecycle(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-release:
			requestCancelled <- false
		case <-r.Context().Done():
			requestCancelled <- true
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	server := httptest.NewServer(lifecycle)
	t.Cleanup(server.Close)
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		response, _ := server.Client().Get(server.URL)
		if response != nil {
			_ = response.Body.Close()
		}
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- lifecycle.shutdown(ctx, server.Config) }()

	deadline := time.Now().Add(time.Second)
	for {
		lifecycle.mu.Lock()
		closed := lifecycle.closed
		lifecycle.mu.Unlock()
		if closed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("关闭未停止受理请求")
		}
		time.Sleep(time.Millisecond)
	}
	rejected := httptest.NewRecorder()
	lifecycle.ServeHTTP(rejected, httptest.NewRequest(http.MethodGet, "/", nil))
	if rejected.Code != http.StatusServiceUnavailable {
		t.Errorf("关闭后新请求状态码=%d", rejected.Code)
	}
	select {
	case err := <-stopped:
		t.Fatalf("在途请求未结束便完成关闭: %v", err)
	default:
	}
	resume()
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	<-clientDone
	if <-requestCancelled {
		t.Error("正常排空提前取消了请求")
	}
}

func TestAppShutdown_CancelsAndSettlesRequestsBeforeAccountWriter(t *testing.T) {
	app, _ := newAccountPoolAPITestApp(t)
	app.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	account, err := app.accountPoolService.CreateAccount(context.Background(), &store.UpstreamAccountRecord{
		ProviderType: "api_key", AccountName: "shutdown", CredentialRaw: "fixture-shutdown", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	settled := make(chan error, 1)
	app.proxyRequests = newProxyRequestLifecycle(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		// 真实请求的配置回调会读取 App；关闭等待不能与它形成锁环。
		app.mu.RLock()
		svc := app.accountPoolService
		app.mu.RUnlock()
		settled <- svc.MarkAccountAuthFailed(r.Context(), account.ID, "late auth failure")
	}))
	server := httptest.NewServer(app.proxyRequests)
	app.proxyServer = server.Config
	t.Cleanup(server.Close)
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		response, _ := server.Client().Get(server.URL)
		if response != nil {
			_ = response.Body.Close()
		}
	}()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 模拟优雅关闭期限已耗尽。
	done := make(chan struct{})
	go func() { app.shutdown(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("取消后未能排空请求并关闭应用")
	}
	<-clientDone
	if err := <-settled; err != nil {
		t.Fatalf("在途结算被过早关闭的 writer 拒绝: %v", err)
	}
	persisted, err := app.accountPoolStore.GetAccount(context.Background(), account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.State != "disabled_auth" || persisted.Enabled || persisted.LastError != "late auth failure" {
		t.Errorf("关闭丢失鉴权结算: state=%s enabled=%v error=%q", persisted.State, persisted.Enabled, persisted.LastError)
	}
}
