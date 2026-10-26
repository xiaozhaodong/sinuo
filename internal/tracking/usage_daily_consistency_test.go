package tracking

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestUsageDailyTimezoneAndChangedBoundaries(t *testing.T) {
	tracker, state, _ := newDailyTestTracker(t)
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	for _, zone := range []string{"UTC", "Asia/Shanghai", "America/New_York"} {
		policy := mustPolicy(t, zone)
		buckets := heatmapTestBuckets(t, zone, "2026-10-20", "2026-10-23")
		assertDailyMatchesDirect(t, tracker, policy, buckets, now)
		var zones int
		if err := tracker.readDB.QueryRow("SELECT COUNT(DISTINCT timezone_name) FROM usage_daily").Scan(&zones); err != nil {
			t.Fatal(err)
		}
		if zones != 1 || dailyCacheCount(t, tracker) != 4 {
			t.Fatal("换时区必须清理旧行")
		}
		days := state.aggregatedDays.Load()
		dailyExec(t, tracker.writeDB, "UPDATE usage_daily SET start_utc=? WHERE date=?", "2000-01-01T00:00:00.000000Z", "2026-10-21")
		dailyQuery(t, tracker, policy, buckets, now)
		if state.aggregatedDays.Load()-days != 1 {
			t.Fatal("边界不符只重算该日")
		}
	}
}

func TestUsageDailyOldArchivingRequestCountedOnce(t *testing.T) {
	tracker := newModelUsageTestTracker(t)
	policy := mustPolicy(t, "UTC")
	buckets := heatmapTestBuckets(t, "UTC", "2026-10-20", "2026-10-23")
	now := buckets[3].End.AddDate(0, 0, 2)
	old := &ActiveRequest{RequestID: "archiving-old", StartTime: buckets[0].Start, RequestKind: RequestKindGeneration,
		RequestFamily: "claude", InputTokens: 10, OutputTokens: 2, CacheCreationTokens: 3, CacheReadTokens: 4, CostOverrideUSD: floatPointer(1)}
	// 已在数据库，但热池有更晚的结算值；归档失败后仍留在 archiving。
	seedHeatmapRequest(t, tracker, old.RequestID, old.RequestKind, old.RequestFamily, "", old.StartTime, 1, 0, 0, 0, .1)
	tracker.hotPool.mu.Lock()
	tracker.hotPool.archiving[old.RequestID] = old
	tracker.hotPool.mu.Unlock()
	t.Cleanup(func() { tracker.hotPool.ConfirmArchived([]string{old.RequestID}) })
	got := dailyQuery(t, tracker, policy, buckets, now)
	if got[0].RequestCount != 1 || got[0].TotalTokens != 19 || got[0].TotalCostUSD != 1 {
		t.Fatalf("旧热池补偿错误: %+v", got[0])
	}
	var cachedTokens int64
	if err := tracker.readDB.QueryRow("SELECT total_tokens FROM usage_daily WHERE date=?", "2026-10-20").Scan(&cachedTokens); err != nil {
		t.Fatal(err)
	}
	if cachedTokens != 1 {
		t.Fatal("缓存不得持久化热池值")
	}
	assertDailyMatchesDirect(t, tracker, policy, buckets, now)
	dailyExec(t, tracker.writeDB, `UPDATE request_logs SET input_tokens=10, output_tokens=2, cache_creation_tokens=3,
 cache_read_tokens=4, total_cost_usd=1 WHERE request_id=?`, old.RequestID)
	assertDailyEqual(t, dailyQuery(t, tracker, policy, buckets, now), got)
	tracker.hotPool.ConfirmArchived([]string{old.RequestID})
	assertDailyEqual(t, dailyQuery(t, tracker, policy, buckets, now), got)
}

func TestUsageDailyFailuresAndRollback(t *testing.T) {
	for _, failure := range []string{"begin", "write", "commit", "read", "aggregate"} {
		t.Run(failure, func(t *testing.T) {
			tracker, state, _ := newDailyTestTracker(t)
			policy := mustPolicy(t, "UTC")
			buckets := heatmapTestBuckets(t, "UTC", "2026-10-20", "2026-10-21")
			now := buckets[1].End.AddDate(0, 0, 2)
			seedHeatmapRequest(t, tracker, "old", RequestKindGeneration, "claude", "", buckets[0].Start, 10, 2, 3, 4, 1)
			if failure == "commit" {
				// 真正的延迟外键错误发生在 COMMIT，覆盖 guardedSQLiteTx 的回滚行为。
				dailyExec(t, tracker.writeDB, `PRAGMA foreign_keys=ON;
     CREATE TABLE daily_test_parent(id INTEGER PRIMARY KEY);
     CREATE TABLE daily_test_child(id INTEGER REFERENCES daily_test_parent(id) DEFERRABLE INITIALLY DEFERRED);
     CREATE TRIGGER daily_test_commit_failure AFTER INSERT ON usage_daily BEGIN
      INSERT INTO daily_test_child VALUES(1);
     END;`)
			} else {
				state.fail = failure
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			got, err := tracker.QueryUsageHeatmapDaily(ctx, policy, buckets, now)
			if failure == "read" || failure == "aggregate" {
				if err == nil || !strings.Contains(err.Error(), "injected query") {
					t.Fatalf("查询错误被掩盖: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				state.fail = ""
				want, err := tracker.QueryDailyUsageWithHotPool(ctx, buckets)
				if err != nil {
					t.Fatal(err)
				}
				assertDailyEqual(t, got, want)
			}
			if dailyCacheCount(t, tracker) != 0 {
				t.Fatal("失败不得残留部分缓存")
			}
			if failure == "commit" {
				dailyExec(t, tracker.writeDB, "INSERT INTO daily_test_parent VALUES(1)")
			}
			state.fail = ""
			assertDailyMatchesDirect(t, tracker, policy, buckets, now)
			if dailyCacheCount(t, tracker) != 2 {
				t.Fatal("下次查询必须重试填表")
			}
		})
	}
}

func TestUsageDailySingleConnectionAndRetentionInterleaving(t *testing.T) {
	tracker, state, path := newDailyTestTracker(t)
	policy := mustPolicy(t, "UTC")
	buckets := heatmapTestBuckets(t, "UTC", "2026-10-20", "2026-10-21")
	now := buckets[1].End.AddDate(0, 0, 2)
	seedHeatmapRequest(t, tracker, "old", RequestKindGeneration, "claude", "", buckets[0].Start, 10, 0, 0, 0, 1)
	external := sql.OpenDB(newSQLiteConnector(path + "?_pragma=busy_timeout(0)&_txlock=immediate"))
	defer external.Close()
	// 在聚合完成但写缓存之前尝试外部保留期删除，必须被 immediate 写锁拒绝。
	var once sync.Once
	state.beforeCacheWrite = func() {
		once.Do(func() {
			_, err := external.Exec("DELETE FROM request_logs")
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), "locked") {
				t.Errorf("封存中删除应遇到写锁: %v", err)
			}
		})
	}
	waits := tracker.readDB.Stats().WaitCount
	got := dailyQuery(t, tracker, policy, buckets, now)
	if got[0].RequestCount != 1 || tracker.readDB.Stats().WaitCount != waits {
		t.Fatal("单连接封存不应自等待")
	}
	state.beforeCacheWrite = nil
	dailyExec(t, external, "DELETE FROM request_logs")
	if dailyCacheCount(t, tracker) != 1 {
		t.Fatal("提交后删除必须失效缓存")
	}
	assertDailyMatchesDirect(t, tracker, policy, buckets, now)

	// 首读命中某日、另一日缺失；等待 writeMu 时命中日被删除，事务必须重读整段。
	seedHeatmapRequest(t, tracker, "next", RequestKindGeneration, "claude", "", buckets[0].Start, 20, 0, 0, 0, 2)
	dailyQuery(t, tracker, policy, buckets, now)
	dailyExec(t, external, "DELETE FROM usage_daily WHERE date=?", "2026-10-21")
	readDone := make(chan struct{})
	once = sync.Once{}
	state.afterCacheRead = func() { once.Do(func() { close(readDone) }) }
	tracker.writeMu.Lock()
	type outcome struct {
		usage []DailyUsage
		err   error
	}
	done := make(chan outcome, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() {
		usage, err := tracker.QueryUsageHeatmapDaily(ctx, policy, buckets, now)
		done <- outcome{usage, err}
	}()
	select {
	case <-readDone:
	case <-ctx.Done():
		tracker.writeMu.Unlock()
		t.Fatal(ctx.Err())
	}
	_, deleteErr := external.Exec("DELETE FROM request_logs")
	tracker.writeMu.Unlock()
	if deleteErr != nil {
		t.Fatal(deleteErr)
	}
	result := <-done
	if result.err != nil || result.usage[0].RequestCount != 0 {
		t.Fatalf("不能把删除前的旧值写回: %+v / %v", result.usage, result.err)
	}
	state.afterCacheRead = nil
	assertDailyMatchesDirect(t, tracker, policy, buckets, now)
}

func TestUsageDailyRejectsInvalidInputs(t *testing.T) {
	tracker, _, _ := newDailyTestTracker(t)
	policy := mustPolicy(t, "UTC")
	buckets := heatmapTestBuckets(t, "UTC", "2026-10-20", "2026-10-21")
	now := buckets[1].End.AddDate(0, 0, 2)
	if _, err := (&UsageTracker{}).QueryUsageHeatmapDaily(context.Background(), policy, buckets, now); err == nil {
		t.Fatal("未初始化应报错")
	}
	if _, err := tracker.QueryUsageHeatmapDaily(context.Background(), nil, buckets, now); err == nil {
		t.Fatal("无时区应报错")
	}
	if got := dailyQuery(t, tracker, policy, nil, now); len(got) != 0 {
		t.Fatal("空区间应为空")
	}
	for _, invalid := range [][]TimeBucket{{{}}, {buckets[1], buckets[0]}, {{Start: buckets[0].Start.Add(time.Hour), End: buckets[0].End}}} {
		if _, err := tracker.QueryUsageHeatmapDaily(context.Background(), policy, invalid, now); err == nil {
			t.Fatal("非法桶应报错")
		}
	}
}

func TestUsageDailyIncrementalSchemaBeforeRequestKindColumn(t *testing.T) {
	adapter, err := NewSQLiteAdapter(DatabaseConfig{DatabasePath: filepath.Join(t.TempDir(), "legacy.db")})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Open(); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	raw, err := sqliteSchemaFS.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	schema := string(raw)
	// 模拟已有 request_family、但尚无 request_kind 和日缓存的旧运行库。
	start, end := strings.Index(schema, "-- 热力图按天汇总缓存"), strings.Index(schema, "-- 索引优化")
	if start < 0 || end <= start {
		t.Fatal("未找到日缓存 schema 边界")
	}
	schema = schema[:start] + schema[end:]
	schema = strings.Replace(schema, "    request_kind TEXT NOT NULL DEFAULT 'legacy'\n        CHECK (request_kind IN ('legacy', 'generation', 'compaction')),\n", "", 1)
	dailyExec(t, adapter.db, schema)
	dailyExec(t, adapter.db, `INSERT INTO request_logs(request_id,start_time) VALUES(?,?)`, "old", "2026-10-20T00:00:00.000000Z")
	if err := adapter.InitSchema(); err != nil {
		t.Fatal(err)
	}
	tracker := &UsageTracker{readDB: adapter.db, writeDB: adapter.db}
	policy := mustPolicy(t, "UTC")
	buckets := heatmapTestBuckets(t, "UTC", "2026-10-20", "2026-10-20")
	got := dailyQuery(t, tracker, policy, buckets, buckets[0].End.AddDate(0, 0, 2))
	if got[0].RequestCount != 1 {
		t.Fatal("增量迁移后 legacy 次数错误")
	}
	dailyExec(t, adapter.db, `UPDATE request_logs SET request_kind=? WHERE request_id=?`, RequestKindCompaction, "old")
	if dailyCacheCount(t, tracker) != 0 {
		t.Fatal("补列后触发器必须对 request_kind 更新生效")
	}
}
