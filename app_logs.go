package main

import (
	"sinuo/internal/logging"
)

// ============================================
// 日志管理相关 Wails 绑定方法
// ============================================

// GetRecentLogs 获取最近的日志（初始加载）
// @param limit 获取的日志数量（默认500，最大1000）
// @return []logging.LogEntry 日志列表
func (a *App) GetRecentLogs(limit int) []logging.LogEntry {
	if limit <= 0 {
		limit = 500 // 默认500条
	}
	if limit > 1000 {
		limit = 1000 // 最多1000条
	}

	// 从 BroadcastHandler 获取日志
	if a.logHandler != nil {
		return a.logHandler.GetRecentLogs(limit)
	}

	return []logging.LogEntry{}
}

// StartLogStream 启动日志实时流
// 前端调用后，开始通过 Wails Events 推送新日志
func (a *App) StartLogStream() {
	if a.logEmitter != nil {
		a.logEmitter.Start(a.ctx)
	}
}

// StopLogStream 停止日志实时流
func (a *App) StopLogStream() {
	if a.logEmitter != nil {
		a.logEmitter.Stop()
	}
}

// GetLogStreamStatus 获取日志流状态
// @return bool true=运行中, false=已停止
func (a *App) GetLogStreamStatus() bool {
	if a.logEmitter != nil {
		return a.logEmitter.IsEnabled()
	}
	return false
}
