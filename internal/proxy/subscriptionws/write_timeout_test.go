package subscriptionws

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestMessageWriteTimeout(t *testing.T) {
	cases := []struct {
		name string
		base time.Duration
		size int
		want time.Duration
	}{
		{"小消息用基础超时", DefaultWriteTimeout, 64 << 10, DefaultWriteTimeout},
		{"宽限边界不放宽", DefaultWriteTimeout, writeTimeoutGraceBytes, DefaultWriteTimeout},
		{"超出部分按最低速率放宽", DefaultWriteTimeout, writeTimeoutGraceBytes + 3*writeTimeoutMinRate, DefaultWriteTimeout + 3*time.Second},
		{"16MB 断线重发", DefaultWriteTimeout, 16 << 20, DefaultWriteTimeout + 480*time.Second},
		{"封顶", DefaultWriteTimeout, 64 << 20, maxWriteTimeout},
		{"基础超时大于上限时不缩短", 20 * time.Minute, 64 << 20, 20 * time.Minute},
		{"测试用短超时对小消息不变", 100 * time.Millisecond, 1024, 100 * time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := messageWriteTimeout(tc.base, tc.size); got != tc.want {
				t.Fatalf("messageWriteTimeout(%v, %d) = %v, want %v", tc.base, tc.size, got, tc.want)
			}
		})
	}
}

// 慢速读取的对端在大消息传输中途发 Ping，必须在消息写完前收到 Pong。
func TestWriteMessageLetsPongInterleave(t *testing.T) {
	ctx := testContext(t)
	src, peer := lanePair(t)
	peer.SetReadLimit(-1)
	// src 由后台读循环处理对端的 Ping。
	src.CloseRead(ctx)

	data := bytes.Repeat([]byte("x"), 16<<20)
	written := make(chan error, 1)
	go func() { written <- writeMessage(ctx, src, websocket.MessageBinary, data) }()

	_, r, err := peer.Reader(ctx)
	if err != nil {
		t.Fatal(err)
	}
	received := make(chan int, 1)
	go func() {
		n := 0
		buf := make([]byte, writeFragmentBytes)
		for {
			m, err := r.Read(buf)
			n += m
			if err != nil {
				received <- n
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	pingCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := peer.Ping(pingCtx); err != nil {
		t.Fatalf("大消息写出期间 Ping 未得到 Pong: %v", err)
	}
	if n := <-received; n != len(data) {
		t.Fatalf("收到 %d 字节, want %d", n, len(data))
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
}
