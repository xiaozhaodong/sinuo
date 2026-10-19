package main

import (
	"context"
	"time"
)

const appUpdateShutdownTimeout = 20 * time.Second

// ServiceShutdown 使用独立上下文，Wails 在调用前已经取消应用上下文。
func (a *App) ServiceShutdown() error {
	a.closing.Store(true)
	a.stopTray()
	ctx, cancel := appShutdownContext(a.updateRestarting.Load())
	defer cancel()
	if err := runAppShutdown(ctx, a.startupDone, a.shutdown); err != nil {
		a.logger.Error("更新关停已到总时限，结束 ServiceShutdown 以免助手放弃更新", "error", err)
	}
	return nil
}

func appShutdownContext(updating bool) (context.Context, context.CancelFunc) {
	if updating {
		// 助手只等父进程 30 秒；留 10 秒给 Wails/native 退出及调度余量。
		return context.WithTimeout(context.Background(), appUpdateShutdownTimeout)
	}
	return context.Background(), func() {}
}

func runAppShutdown(ctx context.Context, startupDone <-chan struct{}, shutdown func(context.Context)) error {
	work := func() {
		if startupDone != nil {
			select {
			case <-startupDone:
			case <-ctx.Done():
				return
			}
		}
		shutdown(ctx)
	}
	if ctx.Done() == nil {
		work()
		return nil
	}
	done := make(chan struct{})
	go func() { defer close(done); work() }()
	// Wait/Close 不全支持 ctx；总时限也必须覆盖这些步骤和启动锁等待。
	select {
	case <-done:
		return ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}
