package logging

import (
	"context"
	"log/slog"
	timezonepolicy "sinuo/internal/timezone"
	"sync"
)

// LogEntry 表示一条结构化的日志记录
type LogEntry struct {
	Timestamp string            `json:"timestamp"` // ISO8601格式
	Level     string            `json:"level"`     // DEBUG/INFO/WARN/ERROR
	Message   string            `json:"message"`   // 日志消息
	Attrs     map[string]string `json:"attrs"`     // 附加属性（request_id等）
}

// BroadcastHandler 是一个slog.Handler，它会拦截所有日志并广播
// 1. 写入文件（保持原有逻辑）
// 2. 写入环形缓冲区（供查询历史）
// 3. 广播给订阅者（实时推送）
type BroadcastHandler struct {
	fileHandler slog.Handler  // 原有的文件Handler
	buffer      *RingBuffer   // 环形缓冲区（最近N条日志）
	Emitter     *EventEmitter // 事件发射器（Wails Events）- 导出用于设置
	minLevel    slog.Level    // 最低日志级别
	mu          sync.RWMutex
}

// NewBroadcastHandler 创建一个新的广播Handler
func NewBroadcastHandler(fileHandler slog.Handler, bufferSize int) *BroadcastHandler {
	return &BroadcastHandler{
		fileHandler: fileHandler,
		buffer:      NewRingBuffer(bufferSize),
		Emitter:     NewEventEmitter(),
		minLevel:    slog.LevelDebug, // 默认记录所有级别
	}
}

// SetEventEmitter 设置事件发射器（在Wails App启动后调用）
func (h *BroadcastHandler) SetEventEmitter(emitter *EventEmitter) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.Emitter = emitter
}

// Handle 实现 slog.Handler 接口
func (h *BroadcastHandler) Handle(ctx context.Context, r slog.Record) error {
	// 1. 先写入文件（保持原有逻辑）
	if err := h.fileHandler.Handle(ctx, r); err != nil {
		return err
	}

	// 2. 检查日志级别过滤
	if r.Level < h.minLevel {
		return nil
	}

	// 3. 构建 LogEntry
	entry := h.buildLogEntry(r)

	// 4. 写入环形缓冲区
	h.buffer.Add(entry)

	// 5. 广播给订阅者（Wails Events）
	h.mu.RLock()
	emitter := h.Emitter
	h.mu.RUnlock()

	if emitter != nil {
		emitter.Emit(entry)
	}

	return nil
}

// Enabled 实现 slog.Handler 接口
func (h *BroadcastHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.fileHandler.Enabled(ctx, level)
}

// WithAttrs 实现 slog.Handler 接口
func (h *BroadcastHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &BroadcastHandler{
		fileHandler: h.fileHandler.WithAttrs(attrs),
		buffer:      h.buffer,
		Emitter:     h.Emitter,
		minLevel:    h.minLevel,
	}
}

// WithGroup 实现 slog.Handler 接口
func (h *BroadcastHandler) WithGroup(name string) slog.Handler {
	return &BroadcastHandler{
		fileHandler: h.fileHandler.WithGroup(name),
		buffer:      h.buffer,
		Emitter:     h.Emitter,
		minLevel:    h.minLevel,
	}
}

// GetRecentLogs 获取最近的N条日志
func (h *BroadcastHandler) GetRecentLogs(limit int) []LogEntry {
	return h.buffer.GetRecent(limit)
}

// buildLogEntry 从 slog.Record 构建 LogEntry
func (h *BroadcastHandler) buildLogEntry(r slog.Record) LogEntry {
	entry := LogEntry{
		Timestamp: timezonepolicy.FormatStorage(r.Time),
		Level:     r.Level.String(),
		Message:   r.Message,
		Attrs:     make(map[string]string),
	}

	// 提取所有属性
	r.Attrs(func(a slog.Attr) bool {
		entry.Attrs[a.Key] = a.Value.String()
		return true
	})

	return entry
}
