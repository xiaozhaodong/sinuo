package subscriptionws

import (
	"compress/flate"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
)

// ErrorDiagnostic 只包含固定类别、Go 类型和关闭码，绝不保留错误正文或关闭原因。
type ErrorDiagnostic struct {
	Kind      string
	RootType  string
	CloseCode websocket.StatusCode
}

// IOEnd 是一次读写结束的观测，不代表已确认的服务端或客户端根因。
type IOEnd struct {
	Direction Direction
	Phase     string
	At        time.Time
	Error     ErrorDiagnostic
}

type bridgeDiagnostics struct {
	once  sync.Once
	first IOEnd
}

// observeEnd 在取消上下文、关闭对端之前调用，保留最先观察到的故障。
func (o Options) observeEnd(direction Direction, phase string, err error) IOEnd {
	end := IOEnd{Direction: direction, Phase: phase, At: time.Now(), Error: DescribeError(err)}
	if o.diagnostics != nil {
		o.diagnostics.once.Do(func() { o.diagnostics.first = end })
	}
	return end
}

// DescribeError 对包装错误分类；未知错误仅输出类型，不能把 URL、凭据或帧正文写进日志。
func DescribeError(err error) ErrorDiagnostic {
	diagnostic := ErrorDiagnostic{Kind: "none", CloseCode: -1}
	if err == nil {
		return diagnostic
	}
	root := err
	for next := errors.Unwrap(root); next != nil; next = errors.Unwrap(root) {
		root = next
	}
	diagnostic.RootType = fmt.Sprintf("%T", root)
	diagnostic.CloseCode = websocket.CloseStatus(err)
	var netErr net.Error
	var corrupt flate.CorruptInputError
	switch {
	case errors.Is(err, context.Canceled):
		diagnostic.Kind = "context_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		diagnostic.Kind = "deadline_exceeded"
	case errors.Is(err, websocket.ErrMessageTooBig):
		diagnostic.Kind = "message_too_big"
	case diagnostic.CloseCode == websocket.StatusNormalClosure:
		diagnostic.Kind = "normal_close"
	case diagnostic.CloseCode != -1:
		diagnostic.Kind = "peer_close"
	case errors.Is(err, syscall.ECONNRESET):
		diagnostic.Kind = "connection_reset"
	case errors.Is(err, syscall.EPIPE):
		diagnostic.Kind = "broken_pipe"
	case errors.Is(err, net.ErrClosed):
		diagnostic.Kind = "connection_closed"
	case errors.Is(err, io.ErrUnexpectedEOF):
		diagnostic.Kind = "unexpected_eof"
	case errors.Is(err, io.EOF):
		diagnostic.Kind = "eof"
	case errors.Is(err, errQueueClosed):
		diagnostic.Kind = "queue_closed"
	case errors.As(err, &netErr) && netErr.Timeout():
		diagnostic.Kind = "network_timeout"
	case errors.As(err, &corrupt):
		diagnostic.Kind = "invalid_compressed_data"
	default:
		diagnostic.Kind = "other"
		// coder/websocket 的协议校验没有统一哨兵；只匹配固定前缀，不输出动态值。
		for _, prefix := range []string{
			"received header with unexpected rsv bits", "received unmasked frame",
			"received unknown opcode", "received control frame payload with invalid length",
			"received fragmented control frame", "received continuation frame without",
			"received new data message without", "close payload ", "invalid status code ",
		} {
			if strings.HasPrefix(root.Error(), prefix) {
				diagnostic.Kind = "websocket_protocol_error"
				break
			}
		}
	}
	return diagnostic
}
