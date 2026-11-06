package tracking

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// 关闭时旧事件队列里尚未刷写的事件必须落库，且不能空转重试拖慢关闭。
// 曾经的行为：Close 先取消 ctx，写处理器随即退出，processBatch 返回 context canceled，
// flushBatch 按 MaxRetry 逐次 sleep 后整批丢弃（生产 max_retry=5 时每批卡 10 秒）。
func TestClose_FlushesPendingLegacyEventsPromptly(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.db")
	tracker := newShutdownFlushTestTracker(t, dbPath)

	const requestID = "req-shutdown-flush"
	tracker.RecordRequestStart(requestID, "127.0.0.1", "test-agent", "POST", "/v1/messages", false)

	started := time.Now()
	if err := tracker.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("Close 耗时 %v，关闭阶段不应空转重试", elapsed)
	}

	reopened := newShutdownFlushTestTracker(t, dbPath)
	defer reopened.Close()
	var count int
	if err := reopened.readDB.QueryRow(`SELECT COUNT(*) FROM request_logs WHERE request_id = ?`, requestID).Scan(&count); err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("关闭前排队的事件应在关闭时落库，实际 %d 行", count)
	}
}

func newShutdownFlushTestTracker(t *testing.T, dbPath string) *UsageTracker {
	t.Helper()
	tracker, err := NewUsageTracker(&Config{
		Enabled:       true,
		DatabasePath:  dbPath,
		BufferSize:    100,
		BatchSize:     100,       // 不按条数触发刷写
		FlushInterval: time.Hour, // 不按时间触发刷写，事件只能在关闭时处理
		MaxRetry:      5,         // 与生产一致
		HotPool:       &HotPoolSettings{Enabled: false},
	})
	if err != nil {
		t.Fatalf("创建 tracker 失败: %v", err)
	}
	return tracker
}

// 并发关闭必须等待同一次清理完成，不能重复取消或重复关闭 writerStop。
func TestClose_ConcurrentCallsWaitForShutdown(t *testing.T) {
	tracker := newShutdownFlushTestTracker(t, ":memory:")
	t.Cleanup(func() { _ = tracker.Close() })
	originalCancel := tracker.cancel
	cancelled := make(chan struct{}, 2)
	release := make(chan struct{})
	releaseClose := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseClose)
	tracker.cancel = func() {
		originalCancel()
		cancelled <- struct{}{}
		<-release
	}

	results := make(chan error, 2)
	closeTracker := func() {
		var err error
		defer func() {
			if p := recover(); p != nil {
				err = fmt.Errorf("Close panic: %v", p)
			}
			results <- err
		}()
		err = tracker.Close()
	}
	go closeTracker()
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("首次 Close 未进入关闭流程")
	}

	secondStarted := make(chan struct{})
	go func() {
		close(secondStarted)
		closeTracker()
	}()
	<-secondStarted
	remaining := 2
	select {
	case <-cancelled:
		t.Error("并发 Close 重复执行了关闭流程")
	case err := <-results:
		remaining--
		t.Errorf("清理完成前 Close 提前返回: %v", err)
	case <-time.After(50 * time.Millisecond):
		// 首次关闭仍被阻塞，其他调用应等待它完成。
	}

	releaseClose()
	for range remaining {
		select {
		case err := <-results:
			if err != nil {
				t.Errorf("关闭失败: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("并发 Close 未完成")
		}
	}
}

// 构造器在启动后台协程后失败，也必须完整执行关闭流程。
func TestNewUsageTracker_SummaryFailureStopsWriter(t *testing.T) {
	for _, hotPoolEnabled := range []bool{true, false} {
		t.Run(fmt.Sprintf("hot_pool_%t", hotPoolEnabled), func(t *testing.T) {
			config := &Config{
				Enabled:       true,
				DatabasePath:  filepath.Join(t.TempDir(), "usage.db"),
				FlushInterval: time.Hour,
				HotPool:       &HotPoolSettings{Enabled: hotPoolEnabled},
			}
			seed, err := NewUsageTracker(config)
			if err != nil {
				t.Fatalf("创建测试数据库失败: %v", err)
			}
			t.Cleanup(func() { _ = seed.Close() })
			// Schema 保持有效，只让构造器末尾的汇总重建写入失败。
			_, err = seed.writeDB.Exec(`
				INSERT INTO usage_summary (timezone_name, date, model_name, request_family)
				VALUES ('Asia/Shanghai', '2000-01-01', 'shutdown-test', 'claude');
				CREATE TRIGGER fail_summary_rebuild BEFORE DELETE ON usage_summary
				BEGIN SELECT RAISE(ABORT, 'forced summary failure'); END;
			`)
			if err != nil {
				t.Fatalf("设置汇总失败场景失败: %v", err)
			}
			if err := seed.Close(); err != nil {
				t.Fatalf("关闭测试数据库失败: %v", err)
			}

			before := runningUsageWriterCount()
			tracker, err := NewUsageTracker(config)
			if err == nil {
				_ = tracker.Close()
				t.Fatal("预期汇总初始化失败")
			}
			if tracker != nil || !strings.Contains(err.Error(), "initialize usage summary cache") ||
				!strings.Contains(err.Error(), "forced summary failure") {
				t.Fatalf("应返回空 tracker 并保留初始化错误，实际 tracker=%p error=%v", tracker, err)
			}

			deadline := time.Now().Add(time.Second)
			for runningUsageWriterCount() > before {
				if time.Now().After(deadline) {
					t.Fatalf("初始化失败后写协程未退出: before=%d after=%d", before, runningUsageWriterCount())
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

func runningUsageWriterCount() int {
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	return strings.Count(string(buf[:n]), "sinuo/internal/tracking.(*UsageTracker).processWriteQueue(")
}
