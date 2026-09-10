package subscriptionws

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func testMessage(payload string) message {
	return message{typ: websocket.MessageText, data: []byte(payload)}
}

func TestMessageQueueLimitsMessageCount(t *testing.T) {
	queue := newMessageQueue(2, 1<<20)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if err := queue.push(ctx, testMessage("hello")); err != nil {
			t.Fatalf("第 %d 条入队失败: %v", i, err)
		}
	}

	blocked := make(chan error, 1)
	go func() { blocked <- queue.push(ctx, testMessage("overflow")) }()

	select {
	case err := <-blocked:
		t.Fatalf("条数已满时不应立即入队: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	if _, err := queue.pop(ctx); err != nil {
		t.Fatalf("出队失败: %v", err)
	}
	select {
	case err := <-blocked:
		if err != nil {
			t.Fatalf("腾出名额后入队失败: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("腾出名额后仍未入队")
	}
}

func TestMessageQueueLimitsBytes(t *testing.T) {
	queue := newMessageQueue(16, 250)
	ctx := context.Background()

	payload := strings.Repeat("a", 100)
	for i := 0; i < 2; i++ {
		if err := queue.push(ctx, testMessage(payload)); err != nil {
			t.Fatalf("第 %d 条入队失败: %v", i, err)
		}
	}
	if got := queue.bytesInFlight(); got != 200 {
		t.Fatalf("字节预算不符: %d", got)
	}

	blocked := make(chan error, 1)
	go func() { blocked <- queue.push(ctx, testMessage(payload)) }()
	select {
	case err := <-blocked:
		t.Fatalf("字节预算不足时不应立即入队: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	if _, err := queue.pop(ctx); err != nil {
		t.Fatalf("出队失败: %v", err)
	}
	select {
	case err := <-blocked:
		if err != nil {
			t.Fatalf("释放预算后入队失败: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("释放预算后仍未入队")
	}
}

func TestMessageQueueAdmitsOversizedMessageWhenEmpty(t *testing.T) {
	queue := newMessageQueue(4, 64)
	ctx := context.Background()

	oversized := strings.Repeat("b", 256)
	if err := queue.push(ctx, testMessage(oversized)); err != nil {
		t.Fatalf("队列为空时超预算消息应放行: %v", err)
	}
	if got := queue.bytesInFlight(); got != 256 {
		t.Fatalf("超预算消息占用不符: %d", got)
	}

	// 放行后重新受预算约束：队列非空时普通消息必须等待。
	blocked := make(chan error, 1)
	go func() { blocked <- queue.push(ctx, testMessage("small")) }()
	select {
	case err := <-blocked:
		t.Fatalf("超预算占用期间不应再入队: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	msg, err := queue.pop(ctx)
	if err != nil || string(msg.data) != oversized {
		t.Fatalf("出队内容不符: %v %d", err, len(msg.data))
	}
	if got := queue.bytesInFlight(); got != 0 {
		t.Fatalf("出队后预算应归零: %d", got)
	}
}

func TestMessageQueuePushHonoursContextCancel(t *testing.T) {
	queue := newMessageQueue(1, 1<<20)
	if err := queue.push(context.Background(), testMessage("first")); err != nil {
		t.Fatalf("入队失败: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := queue.push(ctx, testMessage("second")); err == nil {
		t.Fatal("ctx 结束时入队应失败")
	}
}
