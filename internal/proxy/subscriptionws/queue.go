// Package subscriptionws - Codex 订阅代理的 WebSocket I/O 与桥接
// 创建时间: 2026-09-10
//
// 本包只负责连接、队列、协议与预算，不持有任务路由表，也不认识业务语义。
// 隐私、事件解析等能力由父包通过 MessageInterceptor 接口注入。
package subscriptionws

import (
	"context"
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
}

func newMessageQueue(maxMessages int, maxBytes int64) *messageQueue {
	if maxMessages < 1 {
		maxMessages = 1
	}
	if maxBytes < 1 {
		maxBytes = 1
	}
	return &messageQueue{
		messages: make(chan message, maxMessages),
		notify:   make(chan struct{}, 1),
		maxBytes: maxBytes,
	}
}

// push 入队；条数或字节预算不足时阻塞等待，ctx 结束时返回 ctx 错误。
func (q *messageQueue) push(ctx context.Context, msg message) error {
	size := int64(len(msg.data))
	if err := q.reserve(ctx, size); err != nil {
		return err
	}
	select {
	case q.messages <- msg:
		return nil
	case <-ctx.Done():
		q.release(size)
		return ctx.Err()
	}
}

// pop 出队；队列为空时阻塞等待，ctx 结束时返回 ctx 错误。
func (q *messageQueue) pop(ctx context.Context) (message, error) {
	select {
	case msg := <-q.messages:
		q.release(int64(len(msg.data)))
		return msg, nil
	case <-ctx.Done():
		return message{}, ctx.Err()
	}
}

// reserve 申请字节预算。
func (q *messageQueue) reserve(ctx context.Context, size int64) error {
	for {
		q.mu.Lock()
		if q.used+size <= q.maxBytes || q.used == 0 {
			q.used += size
			q.mu.Unlock()
			return nil
		}
		q.mu.Unlock()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-q.notify:
		}
	}
}

// release 归还字节预算并唤醒等待者。
func (q *messageQueue) release(size int64) {
	q.mu.Lock()
	q.used -= size
	if q.used < 0 {
		q.used = 0
	}
	q.mu.Unlock()

	select {
	case q.notify <- struct{}{}:
	default:
	}
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
