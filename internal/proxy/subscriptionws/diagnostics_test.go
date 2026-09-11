package subscriptionws

import (
	"compress/flate"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestDescribeErrorClassifiesWithoutLeakingPeerData(t *testing.T) {
	const secret = "SYNTHETIC-CREDENTIAL-AND-PAYLOAD"
	cases := []struct {
		name string
		err  error
		kind string
		code websocket.StatusCode
	}{
		{"none", nil, "none", -1},
		{"cancel", fmt.Errorf("%s: %w", secret, context.Canceled), "context_canceled", -1},
		{"deadline", context.DeadlineExceeded, "deadline_exceeded", -1},
		{"limit", websocket.ErrMessageTooBig, "message_too_big", -1},
		{"normal", websocket.CloseError{Code: 1000, Reason: secret}, "normal_close", 1000},
		{"peer", websocket.CloseError{Code: 1011, Reason: secret}, "peer_close", 1011},
		{"reset", &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}, "connection_reset", -1},
		{"pipe", syscall.EPIPE, "broken_pipe", -1},
		{"closed", net.ErrClosed, "connection_closed", -1},
		{"eof", io.EOF, "eof", -1},
		{"truncated", io.ErrUnexpectedEOF, "unexpected_eof", -1},
		{"compression", flate.CorruptInputError(123), "invalid_compressed_data", -1},
		{"protocol", fmt.Errorf("received invalid close payload: %w", errors.New("close payload "+secret+" too small")), "websocket_protocol_error", -1},
		{"url", &url.Error{Op: "Get", URL: "https://" + secret, Err: errors.New(secret)}, "other", -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DescribeError(tc.err)
			if got.Kind != tc.kind || got.CloseCode != tc.code {
				t.Fatalf("错误分类不符: %+v", got)
			}
			encoded, err := json.Marshal(got)
			if err != nil || strings.Contains(string(encoded), secret) {
				t.Fatalf("诊断不应含原始错误文本: %s, %v", encoded, err)
			}
		})
	}
}

func TestBridgeDiagnosticsPreserveFirstObservation(t *testing.T) {
	for _, scenario := range []string{"upstream_drop", "client_drop", "upstream_close", "parent_cancel", "interceptor_error"} {
		t.Run(scenario, func(t *testing.T) {
			client, downstream := lanePair(t)
			upstream, server := lanePair(t)
			ctx, cancel := context.WithCancel(testContext(t))
			defer cancel()
			opts := Options{WriteTimeout: time.Second}
			if scenario == "interceptor_error" {
				opts.Interceptor = testInterceptor(func(_ context.Context, direction Direction, payload []byte) (InterceptResult, error) {
					if direction == DirectionDownstreamToUpstream && string(payload) == "fail" {
						return InterceptResult{}, errors.New("SYNTHETIC-SECRET")
					}
					return InterceptResult{Forward: payload}, nil
				})
			}
			done := make(chan BridgeStats, 1)
			go func() { done <- Bridge(ctx, downstream, upstream, opts) }()
			// 两个方向都先传过真实消息，故障发生时不存在未消费的写入。
			if err := client.Write(ctx, websocket.MessageText, []byte("request")); err != nil {
				t.Fatal(err)
			}
			if _, _, err := server.Read(ctx); err != nil {
				t.Fatal(err)
			}
			if err := server.Write(ctx, websocket.MessageText, []byte("response")); err != nil {
				t.Fatal(err)
			}
			if _, _, err := client.Read(ctx); err != nil {
				t.Fatal(err)
			}
			wantDirection, wantPhase := DirectionUpstreamToDownstream, "read"
			switch scenario {
			case "upstream_drop":
				_ = server.CloseNow()
			case "client_drop":
				wantDirection = DirectionDownstreamToUpstream
				_ = client.CloseNow()
			case "upstream_close":
				go func() { _ = server.Close(websocket.StatusNormalClosure, "SYNTHETIC-SECRET") }()
				_, _, _ = client.Read(ctx)
			case "parent_cancel":
				cancel()
			case "interceptor_error":
				wantDirection, wantPhase = DirectionDownstreamToUpstream, "intercept"
				if err := client.Write(ctx, websocket.MessageText, []byte("fail")); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case stats := <-done:
				first := stats.FirstObserved
				if scenario == "parent_cancel" {
					if stats.ParentContextError != "context_canceled" || first.Error.Kind != "context_canceled" {
						t.Fatalf("未记录父上下文取消: %+v", stats)
					}
				} else if first.Direction != wantDirection || first.Phase != wantPhase || first.Error.Kind == "context_canceled" || stats.ParentContextError != "none" {
					t.Fatalf("起因被后续关闭覆盖: %+v", stats)
				}
				if first.At.IsZero() || stats.DownstreamToUpstream.Messages != 1 || stats.UpstreamToDownstream.Messages != 1 ||
					stats.DownstreamToUpstream.Bytes != 7 || stats.UpstreamToDownstream.Bytes != 8 {
					t.Fatalf("缺少时间或实际转发统计: %+v", stats)
				}
				if scenario == "upstream_close" && (first.Error.Kind != "normal_close" || first.Error.CloseCode != 1000) {
					t.Fatalf("正常关闭码丢失: %+v", first)
				}
				encoded, _ := json.Marshal(stats)
				if strings.Contains(string(encoded), "SYNTHETIC-SECRET") {
					t.Fatal("桥接摘要泄漏了关闭原因或拦截错误")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("桥接未及时结束")
			}
		})
	}
}

func TestBridgeDiagnosticsWriteTimeoutPrecedesCleanup(t *testing.T) {
	_, downstream := lanePair(t)
	upstream, server := lanePair(t)
	ctx := testContext(t)
	// 真实 WS 写锁让向客户端写入必定超时，不依赖 socket 缓冲大小。
	locked, err := downstream.Writer(ctx, websocket.MessageText)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Close()
	done := make(chan BridgeStats, 1)
	go func() {
		done <- Bridge(ctx, downstream, upstream, Options{WriteTimeout: 100 * time.Millisecond})
	}()
	if err := server.Write(ctx, websocket.MessageText, []byte("SYNTHETIC-RESPONSE")); err != nil {
		t.Fatal(err)
	}
	select {
	case stats := <-done:
		first := stats.FirstObserved
		if first.Direction != DirectionUpstreamToDownstream || first.Phase != "write" || first.Error.Kind != "deadline_exceeded" {
			t.Fatalf("写超时被后续关闭覆盖: %+v", stats)
		}
		if stats.UpstreamToDownstream.Messages != 0 || stats.UpstreamToDownstream.WriteEnd.Error.Kind != "deadline_exceeded" {
			t.Fatalf("失败写入不得记为成功转发: %+v", stats)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("写超时后桥接未结束")
	}
}
