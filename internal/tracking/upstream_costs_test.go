package tracking

import (
	"context"
	"math"
	"reflect"
	"testing"
	"time"

	timezonepolicy "sinuo/internal/timezone"
)

func seedUpstreamCostRow(t *testing.T, tracker *UsageTracker, requestID, kind, upstream string, start time.Time, cost float64) {
	t.Helper()
	if _, err := tracker.GetWriteDB().Exec(`INSERT INTO request_logs (
		request_id, start_time, status, request_family, request_kind, upstream_type, upstream_name,
		input_tokens, output_tokens, total_cost_usd
	) VALUES (?, ?, 'completed', 'codex', ?, 'account', ?, 100, 10, ?)`,
		requestID, timezonepolicy.FormatStorage(start), kind, upstream, cost); err != nil {
		t.Fatalf("failed to seed %s: %v", requestID, err)
	}
}

// 窗口是半开区间：开始时刻计入、结束时刻不计入，与模型调用统计的边界一致。
func TestGetEndpointCostsForRange_HalfOpenWindow(t *testing.T) {
	tracker := newModelUsageTestTracker(t)
	start := time.Date(2026, 10, 24, 1, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	seedUpstreamCostRow(t, tracker, "at-start", RequestKindGeneration, "inside", start, 0.1)
	seedUpstreamCostRow(t, tracker, "before-end", RequestKindGeneration, "inside", end.Add(-time.Microsecond), 0.2)
	seedUpstreamCostRow(t, tracker, "before-start", RequestKindGeneration, "outside", start.Add(-time.Microsecond), 1)
	seedUpstreamCostRow(t, tracker, "at-end", RequestKindGeneration, "outside", end, 1)

	costs, err := tracker.GetEndpointCostsForRange(context.Background(), start, end)
	if err != nil {
		t.Fatal(err)
	}
	if len(costs) != 1 || costs[0].UpstreamName != "inside" || costs[0].RequestCount != 2 || math.Abs(costs[0].TotalCostUSD-0.3) > 1e-12 {
		t.Fatalf("窗口边界不正确：%+v", costs)
	}
}

// Token 与成本含独立压缩：与模型调用统计的用量口径一致，压缩专用上游也要出现在排行里。
func TestGetEndpointCostsForRange_IncludesCompactionUsage(t *testing.T) {
	tracker := newModelUsageTestTracker(t)
	now := time.Now().UTC()
	seedUpstreamCostRow(t, tracker, "gen", RequestKindGeneration, "mixed", now, 0.3)
	seedUpstreamCostRow(t, tracker, "comp", RequestKindCompaction, "mixed", now, 1.2)
	seedUpstreamCostRow(t, tracker, "comp-only", RequestKindCompaction, "compact-only", now, 0.5)

	costs, err := tracker.GetEndpointCostsForRange(context.Background(), now.Add(-time.Minute), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]EndpointCostSummary{}
	for _, cost := range costs {
		byName[cost.UpstreamName] = cost
	}
	if got := byName["mixed"]; got.TotalTokens != 220 || math.Abs(got.TotalCostUSD-1.5) > 1e-12 {
		t.Fatalf("生成与压缩的用量都应计入：%+v", got)
	}
	if got := byName["compact-only"]; got.TotalTokens != 110 || math.Abs(got.TotalCostUSD-0.5) > 1e-12 {
		t.Fatalf("压缩专用上游不应丢失：%+v", got)
	}
}

func TestGetEndpointCostsForRange_EmptyAndInvalidWindow(t *testing.T) {
	tracker := newModelUsageTestTracker(t)
	now := time.Now().UTC()

	costs, err := tracker.GetEndpointCostsForRange(context.Background(), now.Add(-time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	if costs == nil || len(costs) != 0 {
		t.Fatalf("没有记录时应返回空切片：%#v", costs)
	}
	if _, err := tracker.GetEndpointCostsForRange(context.Background(), now, now); err == nil {
		t.Fatal("空窗口应返回错误")
	}
	if _, err := tracker.GetEndpointCostsForRange(context.Background(), now, now.Add(-time.Hour)); err == nil {
		t.Fatal("结束早于开始应返回错误")
	}
}

// 按业务日查询只是把日期换算成 DayRange，结果与同一窗口的范围查询一致。
func TestGetEndpointCostsForDate_MatchesBusinessDayRange(t *testing.T) {
	tracker := newModelUsageTestTracker(t)
	policy := tracker.TimezonePolicy()
	date := policy.BusinessDate(time.Now())
	dayStart, dayEnd, err := policy.DayRange(date)
	if err != nil {
		t.Fatal(err)
	}
	seedUpstreamCostRow(t, tracker, "today", RequestKindGeneration, "today", dayStart, 0.1)
	seedUpstreamCostRow(t, tracker, "yesterday", RequestKindGeneration, "yesterday", dayStart.Add(-time.Microsecond), 0.2)

	byDate, err := tracker.GetEndpointCostsForDate(context.Background(), date)
	if err != nil {
		t.Fatal(err)
	}
	byRange, err := tracker.GetEndpointCostsForRange(context.Background(), dayStart, dayEnd)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(byDate, byRange) || len(byDate) != 1 || byDate[0].UpstreamName != "today" {
		t.Fatalf("按日期与按范围结果不一致：date=%+v range=%+v", byDate, byRange)
	}
	if _, err := tracker.GetEndpointCostsForDate(context.Background(), "2026/10/24"); err == nil {
		t.Fatal("日期格式错误应返回错误")
	}
}
