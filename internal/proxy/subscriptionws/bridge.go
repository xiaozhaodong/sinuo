// Package subscriptionws - Codex 订阅代理的 WebSocket 握手与双向桥接
// 创建时间: 2026-09-10
//
// 一个下游连接对应一个上游连接，不跨客户端合并。上下游各自协商压缩、分片与关闭握手；
// 本包只搬运“完整解压消息”，不复制原始帧或扩展头。
package subscriptionws

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

const (
	// DefaultReadLimit 单条完整解压消息上限（与请求体上限同量级）。
	DefaultReadLimit = 64 << 20
	// DefaultWriteTimeout 单次写入超时。
	DefaultWriteTimeout = 30 * time.Second
	// DefaultMaxQueueMessages 每方向排队条数上限。
	DefaultMaxQueueMessages = 64
	// DefaultMaxQueueBytes 每方向排队字节上限。
	DefaultMaxQueueBytes = 8 << 20
	// DefaultCloseReason 代理主动关闭对端时使用的原因（≤125 字节）。
	DefaultCloseReason = "subscription proxy closed the peer connection"
)

// statusReservedClose 是协议保留、不允许出现在关闭帧中的 1004。
const statusReservedClose = 1004

// Direction 消息方向。
type Direction string

const (
	DirectionDownstreamToUpstream Direction = "downstream_to_upstream"
	DirectionUpstreamToDownstream Direction = "upstream_to_downstream"
)

// InterceptResult 描述一次拦截结果。Forward=nil 表示不向原方向转发；
// Reply 会进入反方向现有写队列，保持与该方向其它响应消息相同的背压与顺序约束。
type InterceptResult struct {
	Forward []byte
	Reply   []byte
}

// MessageInterceptor 在消息进入转发队列前逐条处理。
// 同一方向严格按读取顺序调用，供父包挂接隐私扫描与事件关联。
type MessageInterceptor interface {
	Intercept(ctx context.Context, direction Direction, payload []byte) (InterceptResult, error)
}

// MessageForwardObserver 只报告成功写出的字节与时间，不把入队当作出站成功。
type MessageForwardObserver interface {
	Forwarded(direction Direction, bytes int64, at time.Time)
}

// Options 握手与桥接参数。
type Options struct {
	CompressionMode  websocket.CompressionMode
	ReadLimit        int64
	WriteTimeout     time.Duration
	CloseReason      string
	MaxQueueMessages int
	MaxQueueBytes    int64
	Interceptor      MessageInterceptor
	diagnostics      *bridgeDiagnostics
}

func (o Options) withDefaults() Options {
	if o.ReadLimit <= 0 {
		o.ReadLimit = DefaultReadLimit
	}
	if o.WriteTimeout <= 0 {
		o.WriteTimeout = DefaultWriteTimeout
	}
	if o.MaxQueueMessages <= 0 {
		o.MaxQueueMessages = DefaultMaxQueueMessages
	}
	if o.MaxQueueBytes <= 0 {
		o.MaxQueueBytes = DefaultMaxQueueBytes
	}
	if o.CloseReason == "" {
		o.CloseReason = DefaultCloseReason
	}
	return o
}

// LaneStats 单方向转发统计。
type LaneStats struct {
	Messages  int64
	Bytes     int64
	CloseCode websocket.StatusCode
	ReadEnd   IOEnd
	WriteEnd  IOEnd
}

// BridgeStats 双向转发统计；CloseCode 为 -1 表示该方向没有收到关闭帧。
type BridgeStats struct {
	DownstreamToUpstream LaneStats
	UpstreamToDownstream LaneStats
	FirstObserved        IOEnd
	ParentContextError   string
}

// DialUpstream 按已完成白名单重建的请求头拨通上游；失败时返回真实 HTTP 响应。
func DialUpstream(ctx context.Context, targetURL string, header http.Header, client *http.Client, opts Options) (*websocket.Conn, *http.Response, error) {
	opts = opts.withDefaults()
	conn, resp, err := websocket.Dial(ctx, targetURL, &websocket.DialOptions{
		HTTPClient:      client,
		HTTPHeader:      header,
		CompressionMode: opts.CompressionMode,
	})
	if err != nil {
		return nil, resp, err
	}
	conn.SetReadLimit(opts.ReadLimit)
	return conn, resp, nil
}

// AcceptDownstream 完成下游握手并返回连接。方法内不写任何业务响应。
// 入口已校验 Host 并拒绝所有带 Origin 的请求，这里不再重复来源校验。
func AcceptDownstream(w http.ResponseWriter, r *http.Request, opts Options) (*websocket.Conn, error) {
	opts = opts.withDefaults()
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
		CompressionMode:    opts.CompressionMode,
	})
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(opts.ReadLimit)
	return conn, nil
}

// Bridge 在两条连接之间双向转发，直到任意一侧结束。
// 正常关闭先排空已入队消息再传播关闭码；上下文取消时直接断开两端。
func Bridge(ctx context.Context, downstream, upstream *websocket.Conn, opts Options) BridgeStats {
	var stats BridgeStats
	if downstream == nil || upstream == nil {
		return stats
	}
	opts = opts.withDefaults()

	parentCtx := ctx
	opts.diagnostics = &bridgeDiagnostics{}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make(chan laneResult, 2)
	downstreamQueue := newMessageQueue(opts.MaxQueueMessages, opts.MaxQueueBytes)
	upstreamQueue := newMessageQueue(opts.MaxQueueMessages, opts.MaxQueueBytes)
	downstreamRead, upstreamRead := newLaneReadState(), newLaneReadState()
	go func() {
		results <- runLane(ctx, DirectionDownstreamToUpstream, downstream, upstream, upstreamQueue, downstreamQueue, downstreamRead, upstreamRead, opts)
	}()
	go func() {
		results <- runLane(ctx, DirectionUpstreamToDownstream, upstream, downstream, downstreamQueue, upstreamQueue, upstreamRead, downstreamRead, opts)
	}()

	first := <-results
	var second laneResult
	if first.waitForPeer && ctx.Err() == nil {
		// 对端正常关闭也可能先表现为反向写失败。让该连接的读取方向
		// 完成关闭分类和尾帧排空，避免先返回的 writer 抢先取消它。
		timer := time.NewTimer(opts.WriteTimeout)
		select {
		case second = <-results:
		case <-timer.C:
			cancel()
			second = <-results
		case <-ctx.Done():
			second = <-results
		}
		timer.Stop()
	} else {
		cancel()
		second = <-results
	}
	cancel()

	for _, result := range []laneResult{first, second} {
		switch result.direction {
		case DirectionDownstreamToUpstream:
			stats.DownstreamToUpstream = result.stats
		case DirectionUpstreamToDownstream:
			stats.UpstreamToDownstream = result.stats
		}
	}
	// 两端都已被各自的 lane 关闭；这里兜底解除可能残留的底层 I/O。
	closeQuietly(downstream)
	closeQuietly(upstream)
	stats.FirstObserved = opts.diagnostics.first
	stats.ParentContextError = DescribeError(parentCtx.Err()).Kind
	return stats
}

type laneResult struct {
	direction   Direction
	stats       LaneStats
	err         error
	waitForPeer bool
}

type writerResult struct {
	messages int64
	bytes    int64
	err      error
	end      IOEnd
}

// laneReadState 在关闭 done 后发布读取结果，供反向 writer 区分正常关闭与写故障。
type laneReadState struct {
	done chan struct{}
	err  error
}

func newLaneReadState() *laneReadState {
	return &laneReadState{done: make(chan struct{})}
}

// stopFailedWriter 保留正常关闭的反方向排空，其他写故障则结束 src 读取。
func stopFailedWriter(ctx context.Context, cancel context.CancelFunc, src, dst *websocket.Conn, peerRead *laneReadState, timeout time.Duration) {
	// 不能立即取消 src.Read：coder/websocket 会连同 src 一起关闭，
	// 从而截断反方向正在向 src 排空的尾帧。
	closeQuietly(dst)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-peerRead.done:
		if websocket.CloseStatus(peerRead.err) == websocket.StatusNormalClosure {
			return
		}
	case <-ctx.Done():
	case <-timer.C:
	}
	cancel()
	closeQuietly(src)
}

// runLane 运行一个方向的读取 → 队列 → 写入；返回时对端连接已按需关闭。
func runLane(ctx context.Context, direction Direction, src, dst *websocket.Conn, queue, reverseQueue *messageQueue, readState, peerRead *laneReadState, opts Options) laneResult {
	laneCtx, laneCancel := context.WithCancel(ctx)
	defer laneCancel()

	writer := make(chan writerResult, 1)
	writeFailed := func() {
		stopFailedWriter(laneCtx, laneCancel, src, dst, peerRead, opts.WriteTimeout)
	}
	go func() { writer <- writeLane(laneCtx, writeFailed, queue, dst, direction, opts) }()

	result := laneResult{direction: direction, stats: LaneStats{CloseCode: -1}}
	stop := func(phase string, err error) {
		result.stats.ReadEnd = opts.observeEnd(direction, phase, err)
		readState.err = err
		close(readState.done)
		if result.err == nil {
			result.err = err
		}
		if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
			queue.seal()
			// 限制整段排空时间；单条写入的超时仍由 writeLane 控制。
			timer := time.AfterFunc(opts.WriteTimeout, laneCancel)
			defer timer.Stop()
			result.waitForPeer = true
		} else {
			laneCancel()
			result.waitForPeer = errors.Is(err, errQueueClosed)
		}
		written := <-writer
		result.stats.Messages = written.messages
		result.stats.Bytes = written.bytes
		result.stats.WriteEnd = written.end
		if written.err != nil && (result.err == nil || errors.Is(result.err, context.Canceled) || websocket.CloseStatus(result.err) == websocket.StatusNormalClosure) {
			result.err = written.err
			result.waitForPeer = !errors.Is(written.err, context.Canceled) && !errors.Is(written.err, context.DeadlineExceeded)
		}
	}

	for {
		typ, payload, err := src.Read(ctx)
		if err != nil {
			stop("read", err)
			break
		}
		if opts.Interceptor != nil {
			intercepted, interceptErr := opts.Interceptor.Intercept(laneCtx, direction, payload)
			err = interceptErr
			if err != nil {
				stop("intercept", err)
				break
			}
			if intercepted.Reply != nil {
				if err := reverseQueue.push(laneCtx, message{typ: websocket.MessageText, data: intercepted.Reply}); err != nil {
					stop("reply_enqueue", err)
					break
				}
			}
			payload = intercepted.Forward
			if payload == nil {
				continue
			}
		}
		if err := queue.push(laneCtx, message{typ: typ, data: payload}); err != nil {
			stop("enqueue", err)
			break
		}
	}

	if code, ok := closeCodeOf(result.err); ok {
		result.stats.CloseCode = code
	}
	closePeer(ctx, dst, result.err, opts.CloseReason)
	return result
}

// closePeer 按对端关闭码关闭另一侧连接；上下文取消或无法传播时直接断开。
func closePeer(ctx context.Context, dst *websocket.Conn, err error, reason string) {
	if ctx.Err() != nil {
		closeQuietly(dst)
		return
	}
	if errors.Is(err, context.Canceled) {
		return
	}
	if code, ok := closeCodeOf(err); ok {
		if closeErr := dst.Close(code, reason); closeErr != nil && !errors.Is(closeErr, context.Canceled) {
			closeQuietly(dst)
		}
		return
	}
	closeQuietly(dst)
}

// closeCodeOf 提取可安全传播的关闭码；无关闭帧或协议保留码返回 false。
func closeCodeOf(err error) (websocket.StatusCode, bool) {
	code := websocket.CloseStatus(err)
	if code < 1000 || code > 4999 {
		return 0, false
	}
	switch code {
	case websocket.StatusNoStatusRcvd, websocket.StatusAbnormalClosure, websocket.StatusTLSHandshake, statusReservedClose:
		return 0, false
	}
	return code, true
}

func closeQuietly(conn *websocket.Conn) {
	if conn == nil {
		return
	}
	_ = conn.CloseNow()
}

// writeLane 串行写出队列内容，保持同方向的提交顺序。
func writeLane(ctx context.Context, onWriteError func(), queue *messageQueue, dst *websocket.Conn, direction Direction, opts Options) writerResult {
	var result writerResult
	for {
		msg, err := queue.pop(ctx)
		if err != nil {
			if !errors.Is(err, errQueueClosed) {
				result.err = err
				result.end = opts.observeEnd(direction, "dequeue", err)
			}
			return result
		}
		writeCtx, cancelWrite := context.WithTimeout(ctx, opts.WriteTimeout)
		err = dst.Write(writeCtx, msg.typ, msg.data)
		cancelWrite()
		if err != nil {
			result.err = err
			result.end = opts.observeEnd(direction, "write", err)
			onWriteError()
			return result
		}
		result.messages++
		result.bytes += int64(len(msg.data))
		if observer, ok := opts.Interceptor.(MessageForwardObserver); ok {
			observer.Forwarded(direction, int64(len(msg.data)), time.Now())
		}
	}
}
