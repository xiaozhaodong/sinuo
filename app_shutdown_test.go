package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAppUpdateShutdown_TotalDeadline(t *testing.T) {
	updateCtx, updateCancel := appShutdownContext(true)
	defer updateCancel()
	updateDeadline, bounded := updateCtx.Deadline()
	if !bounded || time.Until(updateDeadline) > 20*time.Second || time.Until(updateDeadline) < 19*time.Second {
		t.Fatalf("更新关停上下文没有 20 秒总时限: %v", updateDeadline)
	}
	if appUpdateShutdownTimeout != 20*time.Second {
		t.Fatalf("总时限=%v", appUpdateShutdownTimeout)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	deadline, _ := ctx.Deadline()
	seen := make(chan time.Time, 1)
	err := runAppShutdown(ctx, nil, func(parent context.Context) {
		for _, duration := range []time.Duration{15 * time.Second, 10 * time.Second, 10 * time.Second} {
			child, stop := context.WithTimeout(parent, duration)
			got, _ := child.Deadline()
			if got.After(deadline) {
				t.Error("子步骤超过总时限")
			}
			seen <- got
			<-child.Done()
			stop()
			break
		}
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	<-seen
}

func TestAppUpdateShutdown_BoundsUncooperativeWaitAndStartup(t *testing.T) {
	for _, startup := range []bool{false, true} {
		t.Run(map[bool]string{false: "close", true: "startup"}[startup], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			blocked := make(chan struct{})
			finished := make(chan struct{})
			var startupDone <-chan struct{}
			if startup {
				startupDone = blocked
			}
			start := time.Now()
			err := runAppShutdown(ctx, startupDone, func(context.Context) { defer close(finished); <-blocked })
			if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
				t.Fatalf("总时限未生效: %v", err)
			}
			close(blocked)
			if !startup {
				<-finished
			}
		})
	}
}

func TestShutdown_NormalContextPreserved(t *testing.T) {
	normal, cancel := appShutdownContext(false)
	defer cancel()
	if normal.Done() != nil {
		t.Fatal("普通关停被施加总时限")
	}
	called := false
	if err := runAppShutdown(context.Background(), nil, func(ctx context.Context) {
		called = true
		if ctx.Done() != nil {
			t.Error("普通关停增加了总时限")
		}
	}); err != nil || !called {
		t.Fatalf("called=%v err=%v", called, err)
	}
}
