package subscriptionws

import (
	"context"
	"testing"

	"github.com/coder/websocket"
)

func TestBridgeAcknowledgesForwardedAndLocalReply(t *testing.T) {
	acknowledged := make(chan string, 4)
	interceptor := testInterceptor(func(_ context.Context, direction Direction, payload []byte) (InterceptResult, error) {
		if direction == DirectionDownstreamToUpstream && string(payload) == "blocked" {
			return InterceptResult{Reply: []byte("rejected"), ReplyForwarded: func() { acknowledged <- "reply" }}, nil
		}
		result := InterceptResult{Forward: payload}
		if direction == DirectionUpstreamToDownstream {
			result.Forwarded = func() { acknowledged <- "response" }
		}
		return result, nil
	})
	upstream := newEchoServer(t, websocket.CompressionDisabled)
	server := bridgeServer(t, upstream.wsURL(t), Options{Interceptor: interceptor})
	ctx := testContext(t)
	conn, _ := dialBridge(ctx, t, server, websocket.CompressionDisabled)
	if got := writeAndEcho(ctx, t, conn, "generation"); got != "generation" {
		t.Fatal(got)
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte("blocked")); err != nil {
		t.Fatal(err)
	}
	_, reply, err := conn.Read(ctx)
	if err != nil || string(reply) != "rejected" {
		t.Fatalf("reply=%s err=%v", reply, err)
	}
	for _, want := range []string{"response", "reply"} {
		select {
		case got := <-acknowledged:
			if got != want {
				t.Fatalf("ack=%s want=%s", got, want)
			}
		case <-ctx.Done():
			t.Fatal("消息写出后没有确认")
		}
	}
}

func TestWriteLaneDoesNotAcknowledgeFailedMessage(t *testing.T) {
	ctx := testContext(t)
	_, dst := lanePair(t)
	_ = dst.CloseNow()
	queue := newMessageQueue(1, 1024)
	acknowledged := false
	if err := queue.push(ctx, message{typ: websocket.MessageText, data: []byte("tail"), forwarded: func() { acknowledged = true }}); err != nil {
		t.Fatal(err)
	}
	result := writeLane(ctx, func() {}, queue, dst, DirectionUpstreamToDownstream, Options{}.withDefaults())
	if result.err == nil || acknowledged {
		t.Fatalf("失败写入被确认为成功: err=%v acknowledged=%v", result.err, acknowledged)
	}
}
