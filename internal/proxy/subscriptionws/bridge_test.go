package subscriptionws

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

const (
	testDialTimeout   = 5 * time.Second
	testReadTimeout   = 10 * time.Second
	testWriteTimeout  = 10 * time.Second
	testReadLimit     = 4 << 20
	testCloseDeadline = 10 * time.Second
)

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testReadTimeout)
	t.Cleanup(cancel)
	return ctx
}

// echoServer 上游 WS 回显服务；记录每次握手请求头与读到的关闭码。
type echoServer struct {
	server *httptest.Server
	abort  chan struct{}

	mu         sync.Mutex
	handshakes []http.Header
	closeCodes []websocket.StatusCode
}

func newEchoServer(t *testing.T, mode websocket.CompressionMode) *echoServer {
	t.Helper()
	es := &echoServer{abort: make(chan struct{}, 1)}
	es.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		es.mu.Lock()
		es.handshakes = append(es.handshakes, r.Header.Clone())
		es.mu.Unlock()

		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
			CompressionMode:    mode,
		})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		conn.SetReadLimit(testReadLimit)

		// 模拟上游 TCP 直接断开（不发关闭帧）。
		handlerDone := make(chan struct{})
		defer close(handlerDone)
		go func() {
			select {
			case <-es.abort:
				_ = conn.CloseNow()
			case <-handlerDone:
			}
		}()

		ctx := r.Context()
		for {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				es.recordCloseCode(err)
				return
			}
			if err := conn.Write(ctx, typ, data); err != nil {
				es.recordCloseCode(err)
				return
			}
		}
	}))
	t.Cleanup(es.server.Close)
	return es
}

func (es *echoServer) recordCloseCode(err error) {
	es.mu.Lock()
	defer es.mu.Unlock()
	es.closeCodes = append(es.closeCodes, websocket.CloseStatus(err))
}

func (es *echoServer) wsURL(t *testing.T) string {
	t.Helper()
	return "ws" + strings.TrimPrefix(es.server.URL, "http")
}

func (es *echoServer) handshakeHeaders() []http.Header {
	es.mu.Lock()
	defer es.mu.Unlock()
	out := make([]http.Header, len(es.handshakes))
	copy(out, es.handshakes)
	return out
}

func (es *echoServer) waitCloseCode(t *testing.T, want websocket.StatusCode) {
	t.Helper()
	deadline := time.Now().Add(testCloseDeadline)
	for time.Now().Before(deadline) {
		es.mu.Lock()
		for _, code := range es.closeCodes {
			if code == want {
				es.mu.Unlock()
				return
			}
		}
		es.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("上游未收到关闭码 %d，实际: %v", want, es.closeCodes)
}

// bridgeServer 把下游握手桥接到上游；返回 (URL, 停止函数)。
func bridgeServer(t *testing.T, upstreamURL string, options Options) *httptest.Server {
	t.Helper()
	client := &http.Client{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream, _, err := DialUpstream(r.Context(), upstreamURL, http.Header{"Authorization": []string{"Bearer test-token"}}, client, options)
		if err != nil {
			http.Error(w, "upstream dial failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		downstream, err := AcceptDownstream(w, r, options)
		if err != nil {
			_ = upstream.CloseNow()
			return
		}
		Bridge(r.Context(), downstream, upstream, options)
	}))
	t.Cleanup(server.Close)
	return server
}

func dialBridge(ctx context.Context, t *testing.T, server *httptest.Server, mode websocket.CompressionMode) (*websocket.Conn, *http.Response) {
	t.Helper()
	url := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{CompressionMode: mode})
	if err != nil {
		t.Fatalf("下游握手失败: %v", err)
	}
	conn.SetReadLimit(testReadLimit)
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn, resp
}

func writeAndEcho(ctx context.Context, t *testing.T, conn *websocket.Conn, payload string) string {
	t.Helper()
	if err := conn.Write(ctx, websocket.MessageText, []byte(payload)); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	typ, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("消息类型不符: %v", typ)
	}
	return string(data)
}

func TestBridgeNegotiatesCompressionOnBothHops(t *testing.T) {
	options := Options{ReadLimit: testReadLimit, CompressionMode: websocket.CompressionContextTakeover}
	upstream := newEchoServer(t, websocket.CompressionContextTakeover)
	server := bridgeServer(t, upstream.wsURL(t), options)

	ctx := testContext(t)
	conn, resp := dialBridge(ctx, t, server, websocket.CompressionContextTakeover)

	if got := resp.Header.Get("Sec-WebSocket-Extensions"); !strings.Contains(got, "permessage-deflate") {
		t.Fatalf("下游未协商 permessage-deflate: %q", got)
	}

	// 超过压缩阈值的长消息，压缩后才可能正常往返。
	payload := strings.Repeat("codex-subscription-proxy-", 512)
	if got := writeAndEcho(ctx, t, conn, payload); got != payload {
		t.Fatalf("回显内容不一致: 长度 %d != %d", len(got), len(payload))
	}

	handshakes := upstream.handshakeHeaders()
	if len(handshakes) == 0 {
		t.Fatal("上游未收到握手")
	}
	if got := handshakes[0].Get("Sec-WebSocket-Extensions"); !strings.Contains(got, "permessage-deflate") {
		t.Fatalf("上游未协商 permessage-deflate: %q", got)
	}
	if got := handshakes[0].Get("Authorization"); got != "Bearer test-token" {
		t.Fatalf("上游未收到透传请求头: %q", got)
	}
}

func TestBridgeDisabledCompressionHasNoExtension(t *testing.T) {
	options := Options{ReadLimit: testReadLimit, CompressionMode: websocket.CompressionDisabled}
	upstream := newEchoServer(t, websocket.CompressionDisabled)
	server := bridgeServer(t, upstream.wsURL(t), options)

	ctx := testContext(t)
	conn, resp := dialBridge(ctx, t, server, websocket.CompressionDisabled)

	if got := resp.Header.Get("Sec-WebSocket-Extensions"); got != "" {
		t.Fatalf("禁压缩时不应有扩展头: %q", got)
	}
	if got := writeAndEcho(ctx, t, conn, strings.Repeat("x", 4096)); got != strings.Repeat("x", 4096) {
		t.Fatal("禁压缩路径回显不一致")
	}
	handshakes := upstream.handshakeHeaders()
	if len(handshakes) == 0 || handshakes[0].Get("Sec-WebSocket-Extensions") != "" {
		t.Fatalf("上游不应协商压缩: %q", handshakes)
	}
}

func TestBridgePreservesMessageOrderAndContent(t *testing.T) {
	options := Options{ReadLimit: testReadLimit, CompressionMode: websocket.CompressionContextTakeover}
	upstream := newEchoServer(t, websocket.CompressionContextTakeover)
	server := bridgeServer(t, upstream.wsURL(t), options)

	ctx := testContext(t)
	conn, _ := dialBridge(ctx, t, server, websocket.CompressionContextTakeover)

	for i := 1; i <= 40; i++ {
		payload := strings.Repeat("a", i*37)
		if got := writeAndEcho(ctx, t, conn, payload); got != payload {
			t.Fatalf("第 %d 条消息回显不一致: 长度 %d != %d", i, len(got), len(payload))
		}
	}
}

func TestBridgeReassemblesFragmentedMessage(t *testing.T) {
	options := Options{ReadLimit: testReadLimit, CompressionMode: websocket.CompressionContextTakeover}
	upstream := newEchoServer(t, websocket.CompressionContextTakeover)
	server := bridgeServer(t, upstream.wsURL(t), options)

	ctx := testContext(t)
	conn, _ := dialBridge(ctx, t, server, websocket.CompressionContextTakeover)

	parts := []string{"first-", "second-", "third"}
	writeErr := make(chan error, 1)
	go func() {
		writer, err := conn.Writer(ctx, websocket.MessageText)
		if err != nil {
			writeErr <- err
			return
		}
		for _, part := range parts {
			if _, err := writer.Write([]byte(part)); err != nil {
				writeErr <- err
				return
			}
		}
		writeErr <- writer.Close()
	}()

	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if got := string(data); got != strings.Join(parts, "") {
		t.Fatalf("分片消息未按单条重组: %q", got)
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("分片写入失败: %v", err)
	}
}

func TestBridgePropagatesCloseCodeUpstream(t *testing.T) {
	options := Options{ReadLimit: testReadLimit, CompressionMode: websocket.CompressionContextTakeover}
	upstream := newEchoServer(t, websocket.CompressionContextTakeover)
	server := bridgeServer(t, upstream.wsURL(t), options)

	ctx := testContext(t)
	conn, _ := dialBridge(ctx, t, server, websocket.CompressionContextTakeover)

	if err := conn.Close(4001, "client done"); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("下游关闭失败: %v", err)
	}
	upstream.waitCloseCode(t, 4001)
}

func TestBridgePropagatesUpstreamCloseToDownstream(t *testing.T) {
	closeUpstream := make(chan struct{})
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
			CompressionMode:    websocket.CompressionContextTakeover,
		})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		<-closeUpstream
		_ = conn.Close(4002, "upstream done")
	}))
	t.Cleanup(upstreamServer.Close)

	options := Options{ReadLimit: testReadLimit, CompressionMode: websocket.CompressionContextTakeover}
	upstreamURL := "ws" + strings.TrimPrefix(upstreamServer.URL, "http")
	server := bridgeServer(t, upstreamURL, options)

	ctx := testContext(t)
	conn, _ := dialBridge(ctx, t, server, websocket.CompressionContextTakeover)
	close(closeUpstream)

	_, _, err := conn.Read(ctx)
	if err == nil {
		t.Fatal("上游关闭后下游读取应失败")
	}
	if code := websocket.CloseStatus(err); code != 4002 {
		t.Fatalf("下游未收到上游关闭码: %v", code)
	}
}

func TestBridgeClosesBothSidesWhenMessageTooBig(t *testing.T) {
	options := Options{ReadLimit: 1024, CompressionMode: websocket.CompressionDisabled}
	upstream := newEchoServer(t, websocket.CompressionDisabled)
	server := bridgeServer(t, upstream.wsURL(t), options)

	ctx := testContext(t)
	conn, _ := dialBridge(ctx, t, server, websocket.CompressionDisabled)

	if err := conn.Write(ctx, websocket.MessageText, []byte(strings.Repeat("z", 4096))); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	_, _, err := conn.Read(ctx)
	if err == nil {
		t.Fatal("超限消息应导致连接被关闭")
	}
	if !errors.Is(err, websocket.ErrMessageTooBig) {
		if code := websocket.CloseStatus(err); code != websocket.StatusMessageTooBig {
			t.Fatalf("关闭码应为 %d: %v", websocket.StatusMessageTooBig, err)
		}
	}
}

func TestBridgeClosesDownstreamWhenUpstreamDrops(t *testing.T) {
	options := Options{ReadLimit: testReadLimit, CompressionMode: websocket.CompressionContextTakeover}
	upstream := newEchoServer(t, websocket.CompressionContextTakeover)
	server := bridgeServer(t, upstream.wsURL(t), options)

	ctx := testContext(t)
	conn, _ := dialBridge(ctx, t, server, websocket.CompressionContextTakeover)
	if got := writeAndEcho(ctx, t, conn, "ping"); got != "ping" {
		t.Fatalf("预热回显不一致: %q", got)
	}

	// 上游不发关闭帧直接断开，下游必须在短时间内跟着结束。
	upstream.abort <- struct{}{}

	readCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := conn.Read(readCtx)
	if err == nil {
		t.Fatal("上游断开后下游读取应失败")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("上游断开后下游仍未及时关闭")
	}
}

type testInterceptor func(context.Context, Direction, []byte) (InterceptResult, error)

type testWriteObserver struct{ written chan int64 }

func (o *testWriteObserver) Intercept(_ context.Context, _ Direction, body []byte) (InterceptResult, error) {
	return InterceptResult{Forward: body}, nil
}

func (o *testWriteObserver) Forwarded(direction Direction, size int64, _ time.Time) {
	if direction == DirectionDownstreamToUpstream {
		o.written <- size
	}
}

func TestBridgeReportsActualForwardedBytes(t *testing.T) {
	observer := &testWriteObserver{written: make(chan int64, 1)}
	upstream := newEchoServer(t, websocket.CompressionDisabled)
	server := bridgeServer(t, upstream.wsURL(t), Options{Interceptor: observer})
	ctx := testContext(t)
	conn, _ := dialBridge(ctx, t, server, websocket.CompressionDisabled)
	if got := writeAndEcho(ctx, t, conn, "synthetic"); got != "synthetic" {
		t.Fatal(got)
	}
	select {
	case size := <-observer.written:
		if size != int64(len("synthetic")) {
			t.Fatal(size)
		}
	case <-ctx.Done():
		t.Fatal("missing successful-write notification")
	}
}

func (f testInterceptor) Intercept(ctx context.Context, direction Direction, payload []byte) (InterceptResult, error) {
	return f(ctx, direction, payload)
}

func TestBridgeInjectsReplyWithoutForwardOrClosingConnection(t *testing.T) {
	interceptor := testInterceptor(func(_ context.Context, direction Direction, payload []byte) (InterceptResult, error) {
		if direction == DirectionDownstreamToUpstream && string(payload) == "blocked" {
			return InterceptResult{Reply: []byte(`{"type":"error","status":422}`)}, nil
		}
		return InterceptResult{Forward: payload}, nil
	})
	options := Options{ReadLimit: testReadLimit, CompressionMode: websocket.CompressionDisabled, Interceptor: interceptor}
	upstream := newEchoServer(t, websocket.CompressionDisabled)
	server := bridgeServer(t, upstream.wsURL(t), options)

	ctx := testContext(t)
	conn, _ := dialBridge(ctx, t, server, websocket.CompressionDisabled)
	if err := conn.Write(ctx, websocket.MessageBinary, []byte("blocked")); err != nil {
		t.Fatalf("write blocked message: %v", err)
	}
	typ, reply, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read local reply: %v", err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("local JSON error must be text, got %v", typ)
	}
	if string(reply) != `{"type":"error","status":422}` {
		t.Fatalf("unexpected local reply: %s", reply)
	}
	if got := writeAndEcho(ctx, t, conn, "allowed-after-block"); got != "allowed-after-block" {
		t.Fatalf("blocked payload leaked upstream or connection stopped: %q", got)
	}
}

func TestBridgeReplySharesReverseQueueOrderWithUpstreamEvents(t *testing.T) {
	upstreamSent := make(chan struct{})
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		if err := conn.Write(r.Context(), websocket.MessageText, []byte("upstream-first")); err != nil {
			return
		}
		close(upstreamSent)
		for {
			if _, _, err := conn.Read(r.Context()); err != nil {
				return
			}
		}
	}))
	t.Cleanup(upstreamServer.Close)

	interceptor := testInterceptor(func(_ context.Context, direction Direction, payload []byte) (InterceptResult, error) {
		if direction == DirectionDownstreamToUpstream && string(payload) == "blocked" {
			return InterceptResult{Reply: []byte(`{"type":"error","status":422}`)}, nil
		}
		return InterceptResult{Forward: payload}, nil
	})
	options := Options{ReadLimit: testReadLimit, CompressionMode: websocket.CompressionDisabled, Interceptor: interceptor}
	server := bridgeServer(t, "ws"+strings.TrimPrefix(upstreamServer.URL, "http"), options)
	ctx := testContext(t)
	conn, _ := dialBridge(ctx, t, server, websocket.CompressionDisabled)
	select {
	case <-upstreamSent:
	case <-ctx.Done():
		t.Fatal("upstream event was not queued")
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte("blocked")); err != nil {
		t.Fatalf("write blocked message: %v", err)
	}
	_, first, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read upstream event: %v", err)
	}
	_, second, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read local reply: %v", err)
	}
	if string(first) != "upstream-first" || string(second) != `{"type":"error","status":422}` {
		t.Fatalf("reverse queue order mismatch: first=%s second=%s", first, second)
	}
}

func TestBridgeInterceptorErrorStillTerminatesConnection(t *testing.T) {
	interceptor := testInterceptor(func(_ context.Context, direction Direction, payload []byte) (InterceptResult, error) {
		if direction == DirectionDownstreamToUpstream {
			return InterceptResult{}, errors.New("synthetic interceptor failure")
		}
		return InterceptResult{Forward: payload}, nil
	})
	options := Options{ReadLimit: testReadLimit, CompressionMode: websocket.CompressionDisabled, Interceptor: interceptor}
	upstream := newEchoServer(t, websocket.CompressionDisabled)
	server := bridgeServer(t, upstream.wsURL(t), options)

	ctx := testContext(t)
	conn, _ := dialBridge(ctx, t, server, websocket.CompressionDisabled)
	if err := conn.Write(ctx, websocket.MessageText, []byte("fatal")); err != nil {
		t.Fatalf("write fatal message: %v", err)
	}
	readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, _, err := conn.Read(readCtx); err == nil {
		t.Fatal("connection-level interceptor error must terminate the bridge")
	}
}

// lanePair 返回一对真实 WS 连接，测试可直接控制桥接侧的读写和队列。
func lanePair(t *testing.T) (*websocket.Conn, *websocket.Conn) {
	t.Helper()
	accepted := make(chan *websocket.Conn, 1)
	stop := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		accepted <- conn
		<-stop
	}))
	ctx := testContext(t)
	client, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	var peer *websocket.Conn
	select {
	case peer = <-accepted:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	t.Cleanup(func() {
		close(stop)
		_ = client.CloseNow()
		_ = peer.CloseNow()
		server.Close()
	})
	return client, peer
}

type drainWriteGate struct {
	first   chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *drainWriteGate) Intercept(_ context.Context, _ Direction, body []byte) (InterceptResult, error) {
	return InterceptResult{Forward: body}, nil
}

func (g *drainWriteGate) Forwarded(_ Direction, _ int64, _ time.Time) {
	g.once.Do(func() {
		close(g.first)
		<-g.release
	})
}

func waitQueueSealed(t *testing.T, ctx context.Context, queue *messageQueue) {
	t.Helper()
	for {
		queue.mu.Lock()
		sealed, notify := queue.closed, queue.notify
		queue.mu.Unlock()
		if sealed {
			return
		}
		select {
		case <-notify:
		case <-ctx.Done():
			t.Fatal("正常关闭未进入队列排空阶段")
		}
	}
}

func TestRunLaneNormalCloseDrainsQueuedTailBeforeClose(t *testing.T) {
	ctx := testContext(t)
	sender, src := lanePair(t)
	receiver, dst := lanePair(t)
	queue := newMessageQueue(8, 1<<20)
	gate := &drainWriteGate{first: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gate.release) }) }
	t.Cleanup(release)
	opts := Options{Interceptor: gate}.withDefaults()
	done := make(chan laneResult, 1)
	readState := newLaneReadState()
	go func() {
		done <- runLane(ctx, DirectionUpstreamToDownstream, src, dst, queue, newMessageQueue(8, 1<<20), readState, newLaneReadState(), opts)
	}()
	payloads := []string{"delta-1", "delta-2", `{"type":"response.completed"}`}
	for i, payload := range payloads {
		if err := sender.Write(ctx, websocket.MessageText, []byte(payload)); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			select {
			case <-gate.first:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
	}
	closed := make(chan error, 1)
	go func() { closed <- sender.Close(websocket.StatusNormalClosure, "done") }()
	waitQueueSealed(t, ctx, queue)
	if got := queue.depth(); got != len(payloads)-1 {
		t.Fatalf("测试必须在尾帧仍排队时触发关闭，实际队列长度 %d", got)
	}
	// 此时上游已经关闭，模拟恰好到来的反向请求写失败。
	// 使用实际 Write 和生产中的失败处理，验证它不会关闭仍在排空的下游。
	if err := src.Write(ctx, websocket.MessageText, []byte("late-request")); err == nil {
		t.Fatal("已关闭的上游不应接受反向写入")
	}
	reverseCtx, reverseCancel := context.WithCancel(ctx)
	defer reverseCancel()
	stopFailedWriter(reverseCtx, reverseCancel, dst, src, readState, opts.WriteTimeout)
	if reverseCtx.Err() != nil {
		t.Fatal("上游正常关闭导致的反向写失败不应取消下游 reader")
	}
	release()
	for _, want := range payloads {
		_, body, err := receiver.Read(ctx)
		if err != nil || string(body) != want {
			t.Fatalf("尾帧未按顺序写出: want=%q got=%q err=%v", want, body, err)
		}
	}
	if _, _, err := receiver.Read(ctx); websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Fatalf("排空后未传播正常关闭码: %v", err)
	}
	select {
	case result := <-done:
		if result.stats.Messages != int64(len(payloads)) || result.stats.CloseCode != websocket.StatusNormalClosure {
			t.Fatalf("转发统计不符: %+v", result)
		}
	case <-ctx.Done():
		t.Fatal("lane 未结束")
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
}

func TestRunLaneDrainStopsOnCancelOrWriteTimeout(t *testing.T) {
	for _, cancelDrain := range []bool{true, false} {
		name := "write_timeout"
		if cancelDrain {
			name = "context_cancel"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(testContext(t))
			defer cancel()
			sender, src := lanePair(t)
			_, dst := lanePair(t)
			// 持有真实连接的写锁，确保排空中的 Write 无法自行完成。
			lockedWriter, err := dst.Writer(ctx, websocket.MessageText)
			if err != nil {
				t.Fatal(err)
			}
			defer lockedWriter.Close()
			queue := newMessageQueue(4, 1024)
			opts := Options{WriteTimeout: 300 * time.Millisecond}.withDefaults()
			if cancelDrain {
				opts.WriteTimeout = 5 * time.Second
			}
			done := make(chan laneResult, 1)
			go func() {
				done <- runLane(ctx, DirectionUpstreamToDownstream, src, dst, queue, newMessageQueue(4, 1024), newLaneReadState(), newLaneReadState(), opts)
			}()
			if err := sender.Write(ctx, websocket.MessageText, []byte("tail")); err != nil {
				t.Fatal(err)
			}
			closed := make(chan error, 1)
			go func() { closed <- sender.Close(websocket.StatusNormalClosure, "done") }()
			waitQueueSealed(t, ctx, queue)
			if cancelDrain {
				cancel()
			}
			select {
			case result := <-done:
				if result.stats.Messages != 0 || result.err == nil || result.stats.CloseCode == websocket.StatusNormalClosure {
					t.Fatalf("未写出的尾帧不应报告成功排空: %+v", result)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("取消或写超时后排空仍挂起")
			}
			if err := <-closed; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCloseCodeOfDoesNotTreatEOFAsNormal(t *testing.T) {
	if code, ok := closeCodeOf(io.EOF); ok {
		t.Fatalf("裸 EOF 不应作为正常关闭传播: %d", code)
	}
}

func TestMessageQueueSealPreservesAcceptedMessagesAndWakesProducers(t *testing.T) {
	queue := newMessageQueue(1, 1)
	ctx := testContext(t)
	if err := queue.push(ctx, testMessage("first")); err != nil {
		t.Fatal(err)
	}
	blocked := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { blocked <- queue.push(ctx, testMessage("reply")) }()
	}
	queue.seal()
	for i := 0; i < 2; i++ {
		select {
		case err := <-blocked:
			if !errors.Is(err, errQueueClosed) {
				t.Fatalf("封口后应拒绝新的正向或 Reply 入队: %v", err)
			}
		case <-ctx.Done():
			t.Fatal("封口未唤醒生产者")
		}
	}
	if msg, err := queue.pop(ctx); err != nil || string(msg.data) != "first" {
		t.Fatalf("封口丢失已接受消息: %q %v", msg.data, err)
	}
	if _, err := queue.pop(ctx); !errors.Is(err, errQueueClosed) || queue.bytesInFlight() != 0 {
		t.Fatalf("队列未在排空后结束或释放预算: %v", err)
	}
}
