package tracking

import (
	"sync"
	"testing"
	"time"

	timezonepolicy "sinuo/internal/timezone"
)

func newRetentionTestTracker(t *testing.T) *UsageTracker {
	t.Helper()
	tracker, err := NewUsageTracker(&Config{
		Enabled: true, DatabasePath: ":memory:", RetentionDays: 30,
		CleanupInterval: time.Hour, HotPool: &HotPoolSettings{Enabled: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tracker.Close() })
	now := time.Now()
	for id, days := range map[string]int{"old": 10, "recent": 1} {
		if _, err := tracker.writeDB.Exec("INSERT INTO request_logs (request_id, start_time, status) VALUES (?, ?, ?)",
			id, timezonepolicy.FormatStorage(now.AddDate(0, 0, -days)), "completed"); err != nil {
			t.Fatal(err)
		}
	}
	return tracker
}

func assertRetentionRequestCount(t *testing.T, tracker *UsageTracker, id string, want int) {
	t.Helper()
	var count int
	if err := tracker.readDB.QueryRow("SELECT COUNT(*) FROM request_logs WHERE request_id = ?", id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("请求 %s 的记录数应为 %d，得到 %d", id, want, count)
	}
}

func TestSetRetentionDays_ChangesCleanupWithoutRestart(t *testing.T) {
	tracker := newRetentionTestTracker(t)
	if tracker.getRetentionDays() != 30 {
		t.Fatal("构造时应以配置保留天数初始化")
	}
	for _, days := range []int{0, -5} {
		tracker.SetRetentionDays(days)
		if tracker.getRetentionDays() != 0 {
			t.Fatal("非正数应永久保留")
		}
		if err := tracker.cleanupOldRecords(); err != nil {
			t.Fatal(err)
		}
		assertRetentionRequestCount(t, tracker, "old", 1)
		assertRetentionRequestCount(t, tracker, "recent", 1)
	}
	tracker.SetRetentionDays(3)
	if err := tracker.cleanupOldRecords(); err != nil {
		t.Fatal(err)
	}
	assertRetentionRequestCount(t, tracker, "old", 0)
	assertRetentionRequestCount(t, tracker, "recent", 1)
	if tracker.config.RetentionDays != 30 {
		t.Fatal("热更新不应改写共享的初始配置")
	}
}

func TestSetRetentionDays_ConcurrentAccess(t *testing.T) {
	tracker := &UsageTracker{}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 1000 {
				tracker.SetRetentionDays(i%5 - 1)
				if days := tracker.getRetentionDays(); days < 0 {
					t.Errorf("并发读取不应得到负保留天数: %d", days)
				}
			}
		}()
	}
	wg.Wait()
}

func TestRetentionDays_DisabledTrackerInitialization(t *testing.T) {
	for _, cfg := range []*Config{nil, {RetentionDays: 7}, {RetentionDays: -1}} {
		tracker, err := NewUsageTrackerWithPolicy(cfg, nil)
		if err != nil || tracker == nil {
			t.Fatalf("停用追踪器应可初始化: %+v, %v", tracker, err)
		}
		want := 0
		if cfg != nil && cfg.RetentionDays > 0 {
			want = cfg.RetentionDays
		}
		if tracker.getRetentionDays() != want {
			t.Fatalf("停用追踪器保留天数错误: %d", tracker.getRetentionDays())
		}
	}
}
