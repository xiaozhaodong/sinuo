// Package subscriptionws - Codex 订阅代理的 WebSocket I/O 与桥接
// 创建时间: 2026-09-10
//
// 本包只负责连接、队列、协议与预算，不持有任务路由表，也不认识业务语义。
// 隐私、事件解析等能力由父包通过 MessageInterceptor 接口注入。
package subscriptionws

import (
	"context"
	"errors"
	"sync"

	"github.com/coder/websocket"
)

// message 一条完整的（已解压、已重组）WS 消息。
type message struct {
	typ  websocket.MessageType
	data []byte
}

// messageQueue 条数 + 字节数双限、ctx 感知的消息队列。
//
// 单条消息超过总字节预算时，只在队列为空的情况下放行：宁可短暂超出预算，
// 也不丢消息或让双方互相等待。放行后队列重新受预算约束。
type messageQueue struct {
	messages chan message
	notify   chan struct{}

	mu       sync.Mutex
	used     int64
	maxBytes int64
	closed   bool
}

var errQueueClosed = errors.New("message queue drained or closed")

func newMessageQueue(maxMessages int, maxBytes int64) *messageQueue {
	if maxMessages < 1 {
		maxMessages = 1
	}
	if maxBytes < 1 {
		maxBytes = 1
	}
	return &messageQueue{
		messages: make(chan message, maxMessages),
		notify:   make(chan struct{}),
		maxBytes: maxBytes,
	}
}

// push 入队；条数或字节预算不足时阻塞等待，ctx 结束时返回 ctx 错误。
func (q *messageQueue) push(ctx context.Context, msg message) error {
	size := int64(len(msg.data))
	for {
		q.mu.Lock()
		if err := ctx.Err(); err != nil {
			q.mu.Unlock()
			return err
		}
		if q.closed {
			q.mu.Unlock()
			return errQueueClosed
		}
		if len(q.messages) < cap(q.messages) && (q.used+size <= q.maxBytes || q.used == 0) {
			q.messages <- msg
			q.used += size
			q.signalLocked()
			q.mu.Unlock()
			return nil
		}
		notify := q.notify
		q.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-notify:
		}
	}
}

// pop 出队；队列为空时阻塞等待，ctx 结束时返回 ctx 错误。
func (q *messageQueue) pop(ctx context.Context) (message, error) {
	for {
		q.mu.Lock()
		if err := ctx.Err(); err != nil {
			q.mu.Unlock()
			return message{}, err
		}
		if len(q.messages) > 0 {
			msg := <-q.messages
			q.used -= int64(len(msg.data))
			q.signalLocked()
			q.mu.Unlock()
			return msg, nil
		}
		if q.closed {
			q.mu.Unlock()
			return message{}, errQueueClosed
		}
		notify := q.notify
		q.mu.Unlock()
		select {
		case <-ctx.Done():
			return message{}, ctx.Err()
		case <-notify:
		}
	}
}

// seal 原子停止两个方向的生产者；已接受的消息仍由 writer 按顺序排空。
func (q *messageQueue) seal() {
	q.mu.Lock()
	q.closed = true
	q.signalLocked()
	q.mu.Unlock()
}

// signalLocked 广播状态变化，让等待条数、字节或封口的所有生产者重新检查。
func (q *messageQueue) signalLocked() {
	close(q.notify)
	q.notify = make(chan struct{})
}

// depth 返回当前排队条数（用于统计与测试）。
func (q *messageQueue) depth() int {
	return len(q.messages)
}

// bytesInFlight 返回当前占用的字节预算（用于统计与测试）。
func (q *messageQueue) bytesInFlight() int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.used
}
