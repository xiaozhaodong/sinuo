package logging

import (
	"context"
	"sync"
	"time"
)

// EventEmitter 日志事件发射器。
// 具体推送实现由调用方注入，logging 包不依赖桌面运行时。
type EventEmitter struct {
	emit      func(name string, data any)
	enabled   bool
	batchSize int          // 批量发送大小
	buffer    []LogEntry   // 缓冲区
	ticker    *time.Ticker // 定时器
	mu        sync.Mutex
	stopChan  chan struct{}
	stopped   bool // 防止重复关闭
}

// NewEventEmitter 创建事件发射器
func NewEventEmitter() *EventEmitter {
	return &EventEmitter{
		batchSize: 10, // 每批最多10条
		buffer:    make([]LogEntry, 0, 10),
		stopChan:  make(chan struct{}),
	}
}

// SetEmitFunc 注入事件推送函数。
func (e *EventEmitter) SetEmitFunc(emit func(name string, data any)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emit = emit
}

// Start 启动事件发射器（前端订阅后调用）
func (e *EventEmitter) Start(_ context.Context) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.enabled {
		return // 已启动
	}

	e.enabled = true
	e.stopped = false                                 // 重置停止标志
	e.stopChan = make(chan struct{})                  // 重新创建 channel
	e.ticker = time.NewTicker(100 * time.Millisecond) // 100ms刷新一次

	// 启动批量发送goroutine
	go e.batchSendLoop()
}

// Stop 停止事件发射器
func (e *EventEmitter) Stop() {
	e.mu.Lock()
	if !e.enabled || e.stopped {
		e.mu.Unlock()
		return // 已经停止，避免重复关闭
	}

	e.enabled = false
	e.stopped = true

	if e.ticker != nil {
		e.ticker.Stop()
	}

	close(e.stopChan)
	batch, emit := e.takeBatchLocked()
	e.mu.Unlock()

	// 刷新剩余日志；回调必须在释放锁后执行，避免回调重入发射器时死锁。
	emitBatch(emit, batch)
}

// Emit 发射一条日志事件
func (e *EventEmitter) Emit(entry LogEntry) {
	e.mu.Lock()
	if !e.enabled {
		e.mu.Unlock()
		return
	}

	// 添加到缓冲区
	e.buffer = append(e.buffer, entry)

	// 如果缓冲区满了，立即发送
	var batch []LogEntry
	var emit func(name string, data any)
	if len(e.buffer) >= e.batchSize {
		batch, emit = e.takeBatchLocked()
	}
	e.mu.Unlock()
	emitBatch(emit, batch)
}

// batchSendLoop 批量发送循环
func (e *EventEmitter) batchSendLoop() {
	e.mu.Lock()
	ticker := e.ticker
	stopChan := e.stopChan
	e.mu.Unlock()
	if ticker == nil || stopChan == nil {
		return
	}

	for {
		select {
		case <-ticker.C:
			e.mu.Lock()
			batch, emit := e.takeBatchLocked()
			e.mu.Unlock()
			emitBatch(emit, batch)
		case <-stopChan:
			return
		}
	}
}

// takeBatchLocked 复制并清空当前批次（需要持有锁）。
func (e *EventEmitter) takeBatchLocked() ([]LogEntry, func(name string, data any)) {
	if len(e.buffer) == 0 {
		return nil, nil
	}

	// 复制切片，避免回调返回后复用底层数组造成数据竞争。
	batch := append([]LogEntry(nil), e.buffer...)
	e.buffer = e.buffer[:0]
	return batch, e.emit
}

// emitBatch 在不持有 e.mu 的情况下调用事件回调。
func emitBatch(emit func(name string, data any), batch []LogEntry) {
	if emit != nil && len(batch) > 0 {
		emit("log:batch", batch)
	}
}

// IsEnabled 返回是否已启用
func (e *EventEmitter) IsEnabled() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.enabled
}
