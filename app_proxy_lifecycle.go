package main

import (
	"context"
	"errors"
	"net/http"
	"sync"
)

// proxyRequestLifecycle 在关闭写回服务前等待所有已接收的 HTTP 请求退出。
type proxyRequestLifecycle struct {
	next   http.Handler
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	closed bool
	active sync.WaitGroup
}

func newProxyRequestLifecycle(next http.Handler) *proxyRequestLifecycle {
	ctx, cancel := context.WithCancel(context.Background())
	return &proxyRequestLifecycle{next: next, ctx: ctx, cancel: cancel}
}

func (p *proxyRequestLifecycle) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		http.Error(w, "proxy shutting down", http.StatusServiceUnavailable)
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

func (p *proxyRequestLifecycle) shutdown(ctx context.Context, server *http.Server) error {
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
	p.active.Wait()
	p.cancel()
	return err
}
