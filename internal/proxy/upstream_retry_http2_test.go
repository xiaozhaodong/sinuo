package proxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
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

type retryLogBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *retryLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}

func (b *retryLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

// newRefusingH2Server 使用真实 TLS + ALPN，与客户端逐帧通信，首流拒绝、后续流成功。
func newRefusingH2Server(t *testing.T) string {
	t.Helper()
	certSource := httptest.NewTLSServer(http.NotFoundHandler())
	cert := certSource.TLS.Certificates[0]
	certSource.Close()
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	var workers sync.WaitGroup
	var mu sync.Mutex
	var connections []net.Conn
	done := make(chan struct{})
	failures := make(chan error, 8)
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			connections = append(connections, conn)
			mu.Unlock()
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer conn.Close()
				if err := serveRefusingH2(conn, &requests); err != nil {
					failures <- err
				}
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-done
		mu.Lock()
		for _, conn := range connections {
			_ = conn.Close()
		}
		mu.Unlock()
		workers.Wait()
		close(failures)
		for err := range failures {
			t.Errorf("HTTP/2服务端: %v", err)
		}
	})
	return "https://" + listener.Addr().String()
}

func serveRefusingH2(conn net.Conn, requests *atomic.Int32) error {
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	preface := make([]byte, len(http2.ClientPreface))
	if _, err := io.ReadFull(conn, preface); err != nil {
		return err
	}
	if string(preface) != http2.ClientPreface {
		return fmt.Errorf("非法HTTP/2前导")
	}
	framer := http2.NewFramer(conn, conn)
	if err := framer.WriteSettings(); err != nil {
		return err
	}
	for {
		frame, err := framer.ReadFrame()
		if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := replyH2Frame(framer, frame, requests); err != nil {
			return err
		}
	}
}

func replyH2Frame(framer *http2.Framer, frame http2.Frame, requests *atomic.Int32) error {
	switch f := frame.(type) {
	case *http2.SettingsFrame:
		if !f.IsAck() {
			return framer.WriteSettingsAck()
		}
	case *http2.PingFrame:
		if !f.Flags.Has(http2.FlagPingAck) {
			return framer.WritePing(true, f.Data)
		}
	case *http2.HeadersFrame:
		if requests.Add(1) == 1 {
			// 跨越毫秒精度边界，保证首次与最终取连接偏移可区分。
			time.Sleep(5 * time.Millisecond)
			return framer.WriteRSTStream(f.StreamID, http2.ErrCodeRefusedStream)
		}
		// HPACK 静态表索引8为 :status=200。
		if err := framer.WriteHeaders(http2.HeadersFrameParam{
			StreamID: f.StreamID, BlockFragment: []byte{0x88}, EndHeaders: true,
		}); err != nil {
			return err
		}
		return framer.WriteData(f.StreamID, true, []byte("ok"))
	}
	return nil
}

func TestUpstreamRetry_HTTP2RefusedStreamEndToEnd(t *testing.T) {
	upstream := newRefusingH2Server(t)
	tracker := newLifecyclePanelTestTracker(t)
	rlm := NewRequestLifecycleManager(tracker, nil, "retry-h2-refused", nil)
	rlm.StartRequest("127.0.0.1", "test", "POST", "/v1/responses", false)
	var logs retryLogBuffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	tr := &http.Transport{ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	t.Cleanup(tr.CloseIdleConnections)
	ctx, _ := handlers.WithUpstreamTrace(context.Background(), rlm)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstream, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Transport: tr, Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || resp.ProtoMajor != 2 || string(body) != "ok" {
		t.Fatalf("响应: %s, %v", body, err)
	}
	rlm.RecordNonStreamingResponseComplete()
	rlm.CompleteRequest(nil)
	data := timingLifecycleData(t, tracker, "retry-h2-refused")
	if rlm.upstreamContactCount() != 2 || data.Detail.RetryCount != 1 {
		t.Fatalf("联系次数与重试数: %+v", data.Detail)
	}
	if data.UpstreamFirstDispatchMs == nil || data.UpstreamDispatchMs == nil ||
		*data.UpstreamFirstDispatchMs >= *data.UpstreamDispatchMs {
		t.Fatalf("首次/最终取连接: %+v", data)
	}
	if !strings.Contains(logs.String(), "[传输层重发]") || !strings.Contains(logs.String(), "attempt=2") {
		t.Fatalf("缺少传输层重发日志: %s", logs.String())
	}
}

func TestUpstreamRetry_NormalHTTPContactCounts(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		t.Run(fmt.Sprint("h2=", h2), func(t *testing.T) {
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, "ok")
			}))
			server.EnableHTTP2 = h2
			server.StartTLS()
			t.Cleanup(server.Close)
			tr := &http.Transport{ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
			t.Cleanup(tr.CloseIdleConnections)
			for i := range 2 {
				requireSingleContact(t, server.URL, tr, h2, i)
			}
		})
	}
}

func requireSingleContact(t *testing.T, url string, tr *http.Transport, h2 bool, index int) {
	t.Helper()
	rlm := NewRequestLifecycleManager(nil, nil, fmt.Sprint(index), nil)
	ctx, _ := handlers.WithUpstreamTrace(context.Background(), rlm)
	var reused bool
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused }})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Transport: tr, Timeout: time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if index > 0 && !reused {
		t.Fatal("第二条正常请求应复用同一连接")
	}
	wantMajor := 1
	if h2 {
		wantMajor = 2
	}
	if resp.ProtoMajor != wantMajor || rlm.upstreamContactCount() != 1 || rlm.GetRetryCount() != 0 {
		t.Fatalf("正常HTTP/%d请求不应重复计数: HTTP/%d, contacts=%d", wantMajor, resp.ProtoMajor, rlm.upstreamContactCount())
	}
}
