package logging

import (
	"context"
	"testing"
	"time"
)

func TestEventEmitterEmitFlushInvokesCallbackOutsideLock(t *testing.T) {
	emitter := NewEventEmitter()
	emitter.batchSize = 1
	callbackDone := make(chan struct{})
	emitter.SetEmitFunc(func(name string, _ any) {
		if name != "log:batch" {
			t.Errorf("event name = %q, want log:batch", name)
		}
		_ = emitter.IsEnabled()
		close(callbackDone)
	})
	emitter.Start(context.Background())
	t.Cleanup(emitter.Stop)

	emitter.Emit(LogEntry{})
	select {
	case <-callbackDone:
	case <-time.After(time.Second):
		t.Fatal("event callback did not complete; callback may hold emitter lock")
	}
}

func TestEventEmitterStopFlushInvokesCallbackOutsideLock(t *testing.T) {
	emitter := NewEventEmitter()
	callbackDone := make(chan struct{})
	emitter.SetEmitFunc(func(name string, _ any) {
		if name != "log:batch" {
			t.Errorf("event name = %q, want log:batch", name)
		}
		_ = emitter.IsEnabled()
		close(callbackDone)
	})
	emitter.Start(context.Background())
	emitter.mu.Lock()
	emitter.ticker.Stop()
	emitter.mu.Unlock()

	emitter.Emit(LogEntry{})
	stopDone := make(chan struct{})
	go func() {
		emitter.Stop()
		close(stopDone)
	}()

	select {
	case <-callbackDone:
	case <-time.After(time.Second):
		t.Fatal("stop flush callback did not complete; callback may hold emitter lock")
	}
	select {
	case <-stopDone:
	case <-time.After(time.Second):
		t.Fatal("Stop did not complete")
	}
}
