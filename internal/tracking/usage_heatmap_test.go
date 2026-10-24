package tracking

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	timezonepolicy "sinuo/internal/timezone"
)

func heatmapTestBuckets(t *testing.T, zone, first, last string) []TimeBucket {
	t.Helper()
	policy := mustPolicy(t, zone)
	start, _, err := policy.DayRange(first)
	if err != nil {
		t.Fatal(err)
	}
	_, end, err := policy.DayRange(last)
	if err != nil {
		t.Fatal(err)
	}
	buckets, err := BuildTrendBuckets(policy, start, end, TrendGranularityDay, 400)
	if err != nil {
		t.Fatal(err)
	}
	return buckets
}

func seedHeatmapRequest(t *testing.T, tracker *UsageTracker, id, kind, family, path string, start time.Time, input, output, creation, read int64, cost float64) {
	t.Helper()
	_, err := tracker.GetWriteDB().Exec(`INSERT INTO request_logs (
		request_id, start_time, status, request_kind, request_family, path,
		input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens, total_cost_usd
	) VALUES (?, ?, 'completed', ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, timezonepolicy.FormatStorage(start), kind, family, path, input, output, creation, read, cost)
	if err != nil {
		t.Fatal(err)
	}
}

func TestQueryDailyUsageWithHotPool_BucketsAndMetricKinds(t *testing.T) {
	tracker := newModelUsageTestTracker(t)
	buckets := heatmapTestBuckets(t, "Asia/Shanghai", "2026-10-19", "2026-10-21")
	seedHeatmapRequest(t, tracker, "claude", RequestKindGeneration, "claude", "/v1/messages", buckets[0].Start, 100, 20, 10, 30, 1)
	seedHeatmapRequest(t, tracker, "codex", RequestKindLegacy, "codex", "/v1/responses", buckets[1].Start, 100, 20, 10, 80, 2)
	seedHeatmapRequest(t, tracker, "compact", RequestKindCompaction, "codex", "/v1/responses/compact", buckets[1].Start.Add(time.Hour), 200, 10, 0, 150, 3)
	// 缺少 family 的历史 Responses 也由共用 Token 规则识别。
	seedHeatmapRequest(t, tracker, "legacy-path", RequestKindLegacy, "other", "/backend-api/codex/responses", buckets[1].Start.Add(2*time.Hour), 40, 5, 0, 30, .5)
	seedHeatmapRequest(t, tracker, "outside", RequestKindGeneration, "claude", "/v1/messages", buckets[2].End, 999, 0, 0, 0, 9)
	got, err := tracker.QueryDailyUsageWithHotPool(context.Background(), buckets)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].RequestCount != 1 || got[0].TotalTokens != 160 || got[0].TotalCostUSD != 1 {
		t.Fatalf("首日及结果长度错误: %+v", got)
	}
	if got[1].RequestCount != 2 || got[1].TotalTokens != 375 || got[1].InputTokens != 340 || got[1].CacheReadTokens != 260 || got[1].TotalCostUSD != 5.5 {
		t.Fatalf("生成/压缩口径或 Codex 去重错误: %+v", got[1])
	}
	if got[2] != (DailyUsage{Start: buckets[2].Label}) {
		t.Fatalf("空日必须补零且排除右边界: %+v", got[2])
	}
	for i := range got {
		if !got[i].Start.Equal(buckets[i].Label) {
			t.Fatalf("桶顺序错误: %+v", got)
		}
	}
}

func TestQueryDailyUsageWithHotPool_DSTBoundaries(t *testing.T) {
	for _, tc := range []struct {
		first, last string
		hours       time.Duration
	}{
		{"2026-03-08", "2026-03-09", 23},
		{"2026-11-01", "2026-11-02", 25},
	} {
		t.Run(tc.first, func(t *testing.T) {
			tracker := newModelUsageTestTracker(t)
			buckets := heatmapTestBuckets(t, "America/New_York", tc.first, tc.last)
			if buckets[0].End.Sub(buckets[0].Start) != tc.hours*time.Hour {
				t.Fatal("DST 桶时长错误")
			}
			for i, at := range []time.Time{buckets[0].Start.Add(-time.Microsecond), buckets[0].Start, buckets[0].End.Add(-time.Microsecond), buckets[1].Start, buckets[1].End} {
				seedHeatmapRequest(t, tracker, fmt.Sprint(i), RequestKindGeneration, "claude", "/v1/messages", at, 1, 0, 0, 0, 0)
			}
			got, err := tracker.QueryDailyUsageWithHotPool(context.Background(), buckets)
			if err != nil || got[0].RequestCount != 2 || got[1].RequestCount != 1 {
				t.Fatalf("DST 边界归桶错误: %+v err=%v", got, err)
			}
		})
	}
}

func TestQueryDailyUsageWithHotPool_OverlapUsesDatabaseDayAndDeduplicates(t *testing.T) {
	tracker := newModelUsageTestTracker(t)
	buckets := heatmapTestBuckets(t, "UTC", "2026-10-22", "2026-10-23")
	seedHeatmapRequest(t, tracker, "moved", RequestKindGeneration, "claude", "/v1/messages", buckets[0].Start, 100, 50, 20, 30, 9)
	seedHeatmapRequest(t, tracker, "same", RequestKindCompaction, "codex", "/v1/responses/compact", buckets[1].Start, 80, 20, 0, 60, 5)
	for _, req := range []*ActiveRequest{
		{RequestID: "moved", StartTime: buckets[1].Start, RequestFamily: "claude", Path: "/v1/messages", InputTokens: 10, OutputTokens: 2, CacheCreationTokens: 3, CacheReadTokens: 4, CostOverrideUSD: floatPointer(1)},
		{RequestID: "same", StartTime: buckets[1].Start.Add(time.Hour), RequestKind: RequestKindCompaction, RequestFamily: "codex", InputTokens: 40, OutputTokens: 5, CacheReadTokens: 30, CostOverrideUSD: floatPointer(2)},
		{RequestID: "hot-only", StartTime: buckets[1].Start.Add(2 * time.Hour), RequestKind: RequestKindGeneration, InputTokens: 7, CostOverrideUSD: floatPointer(.5)},
		{RequestID: "outside", StartTime: buckets[1].End, InputTokens: 999},
	} {
		if err := tracker.hotPool.Add(req); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { tracker.hotPool.Remove(req.RequestID) })
	}
	// 同一 ID 同时出现在活跃和归档过渡快照中，仍只算一次。
	tracker.hotPool.mu.Lock()
	tracker.hotPool.archiving["moved"] = tracker.hotPool.requests["moved"]
	tracker.hotPool.mu.Unlock()
	t.Cleanup(func() { tracker.hotPool.ConfirmArchived([]string{"moved"}) })
	got, err := tracker.QueryDailyUsageWithHotPool(context.Background(), buckets)
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != (DailyUsage{Start: buckets[0].Label}) {
		t.Fatalf("必须从库内原来的日期扣掉全部字段: %+v", got[0])
	}
	if got[1].RequestCount != 2 || got[1].TotalTokens != 71 || got[1].InputTokens != 57 || got[1].OutputTokens != 7 || got[1].CacheCreationTokens != 3 || got[1].CacheReadTokens != 34 || math.Abs(got[1].TotalCostUSD-3.5) > 1e-12 {
		t.Fatalf("热池去重及成本错误: %+v", got[1])
	}
}

func floatPointer(value float64) *float64 { return &value }

func TestQueryEarliestRequestTime_EmptyAndMinimum(t *testing.T) {
	tracker := newModelUsageTestTracker(t)
	ctx := context.Background()
	if got, ok, err := tracker.QueryEarliestRequestTime(ctx); err != nil || ok || !got.IsZero() {
		t.Fatalf("空库: %v %v %v", got, ok, err)
	}
	first := time.Date(2026, 4, 9, 23, 59, 59, 123000, time.UTC)
	seedHeatmapRequest(t, tracker, "later", RequestKindLegacy, "claude", "", first.Add(time.Hour), 0, 0, 0, 0, 0)
	seedHeatmapRequest(t, tracker, "earliest", RequestKindLegacy, "claude", "", first, 0, 0, 0, 0, 0)
	if got, ok, err := tracker.QueryEarliestRequestTime(ctx); err != nil || !ok || !got.Equal(first) {
		t.Fatalf("最早时间: %v %v %v", got, ok, err)
	}
	if _, err := tracker.GetWriteDB().Exec(`UPDATE request_logs SET start_time = 'invalid'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tracker.QueryEarliestRequestTime(ctx); err == nil {
		t.Fatal("非法存储时间必须报错")
	}
}

func TestDailyUsageQuery_UsesStartTimeIndex(t *testing.T) {
	tracker := newModelUsageTestTracker(t)
	buckets := heatmapTestBuckets(t, "UTC", "2025-10-20", "2026-10-25")
	query, args, err := dailyUsageQuery(buckets, nil)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := tracker.GetReadDB().QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	indexed := false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		t.Log(detail)
		indexed = indexed || strings.Contains(detail, "SEARCH r USING INDEX idx_request_logs_start_time")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !indexed {
		t.Fatal("一年查询必须使用 start_time 范围索引")
	}
}

func TestQueryDailyUsageWithHotPool_RejectsInvalidBucketsAndUninitializedDB(t *testing.T) {
	ctx := context.Background()
	tracker := &UsageTracker{}
	if _, err := tracker.QueryDailyUsageWithHotPool(ctx, nil); err == nil {
		t.Fatal("未初始化数据库必须报错")
	}
	if _, _, err := tracker.QueryEarliestRequestTime(ctx); err == nil {
		t.Fatal("最早时间查询也必须报错")
	}
	tracker = newModelUsageTestTracker(t)
	if got, err := tracker.QueryDailyUsageWithHotPool(ctx, nil); err != nil || len(got) != 0 {
		t.Fatalf("空桶: %+v %v", got, err)
	}
	if _, err := tracker.QueryDailyUsageWithHotPool(ctx, []TimeBucket{{}}); err == nil {
		t.Fatal("非法桶必须报错")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := tracker.QueryDailyUsageWithHotPool(canceled, heatmapTestBuckets(t, "UTC", "2026-10-23", "2026-10-23")); err == nil {
		t.Fatal("取消查询必须向上传递")
	}
}
