package proxy

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"sinuo/internal/proxy/handlers"
)

// 复现真机 req-5d918340：HTTP/2 缓存连接接不了新请求时，Go 返回 ErrNoCachedConn、清掉该连接后重拨，
// 同一次请求触发两次 GetConn，但上游只收到一次请求。这里用「最大并发流 1 + 第一条请求挂起」稳定触发。
func TestUpstreamRetry_HTTP2PoolReconnectIsSameContact(t *testing.T) {
	upstreamURL, hits, release := newSingleStreamH2Server(t)
	tr := &http.Transport{ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	t.Cleanup(tr.CloseIdleConnections)
	client := &http.Client{Transport: tr, Timeout: 3 * time.Second}

	holdDone := make(chan error, 1)
	go func() {
		resp, err := client.Get(upstreamURL + "/hold")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		holdDone <- err
	}()
	waitForHits(t, hits, 1)

	var logs retryLogBuffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	rlm := NewRequestLifecycleManager(nil, nil, "pool-reconnect", nil)
	var getConns atomic.Int32
	ctx, _ := handlers.WithUpstreamTrace(context.Background(), rlm)
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GetConn: func(string) { getConns.Add(1) }})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstreamURL+"/second", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	release()
	if err := <-holdDone; err != nil {
		t.Fatal(err)
	}

	if resp.ProtoMajor != 2 || getConns.Load() != 2 {
		t.Fatalf("未复现连接池重拨路径: HTTP/%d, GetConn=%d", resp.ProtoMajor, getConns.Load())
	}
	if rlm.upstreamContactCount() != 1 || rlm.GetRetryCount() != 0 {
		t.Fatalf("连接池重拨不应计为重试: contacts=%d retries=%d", rlm.upstreamContactCount(), rlm.GetRetryCount())
	}
	if hits.Load() != 2 {
		t.Fatalf("上游应各收到一次请求，实际 %d", hits.Load())
	}
	if strings.Contains(logs.String(), "[传输层重发]") || !strings.Contains(logs.String(), "[连接池换连接]") {
		t.Fatalf("日志应为连接池换连接而非传输层重发: %s", logs.String())
	}
}

// newSingleStreamH2Server 启动只允许 1 个并发流的 HTTP/2 服务端；/hold 一直挂起到调用 release。
// release 也注册在清理里且先于关闭服务执行，测试中途失败时挂起的请求不会让 Close 卡死。
func newSingleStreamH2Server(t *testing.T) (string, *atomic.Int32, func()) {
	t.Helper()
	hits := &atomic.Int32{}
	block := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(block) }) }
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/hold" {
			<-block
		}
		_, _ = io.WriteString(w, "ok")
	}))
	if err := http2.ConfigureServer(server.Config, &http2.Server{MaxConcurrentStreams: 1}); err != nil {
		t.Fatal(err)
	}
	server.TLS = &tls.Config{NextProtos: []string{"h2"}}
	server.StartTLS()
	t.Cleanup(server.Close)
	t.Cleanup(release)
	return server.URL, hits, release
}

func waitForHits(t *testing.T, hits *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for hits.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("等待上游收到 %d 个请求超时，实际 %d", want, hits.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
}
