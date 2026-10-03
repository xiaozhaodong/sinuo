package monitor

import "sync"

// SubscribeActiveConnections 返回变化信号而非计数快照，消费者收到后读取最新计数。
// 缓冲区满时合并通知，不阻塞请求路径；取消订阅与发送共用 Metrics 锁。
func (m *Metrics) SubscribeActiveConnections() (<-chan struct{}, func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.activeListeners == nil {
		m.activeListeners = make(map[chan struct{}]struct{})
	}
	ch := make(chan struct{}, 1)
	m.activeListeners[ch] = struct{}{}
	ch <- struct{}{}
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			delete(m.activeListeners, ch)
			close(ch)
		})
	}
}

func (m *Metrics) ActiveConnectionCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.ActiveConnections)
}

// notifyActiveConnectionsLocked 仅由连接集合的增删点调用，调用方已持有写锁。
func (m *Metrics) notifyActiveConnectionsLocked() {
	for ch := range m.activeListeners {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
