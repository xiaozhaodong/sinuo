package main

import (
	"testing"
	"time"

	timezonepolicy "sinuo/internal/timezone"
)

func TestUsageHeatmapRange_MondayWednesdaySundayAndWeekBounds(t *testing.T) {
	policy, err := timezonepolicy.New("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		date string
		days int
	}{
		{"2026-10-19", 365}, {"2026-10-21", 367}, {"2026-10-25", 371},
	} {
		for _, weeks := range []int{53, 0, -1, 54} {
			now, _ := policy.ParseInput(tc.date + "T12:00:00")
			start, end, buckets, err := usageHeatmapRange(policy, now, weeks)
			if err != nil || len(buckets) != tc.days || end != tc.date {
				t.Fatalf("%s weeks=%d: %s %s %d %v", tc.date, weeks, start, end, len(buckets), err)
			}
			first, _ := time.Parse(time.DateOnly, start)
			if first.Weekday() != time.Monday {
				t.Fatalf("首列必须周一: %s", start)
			}
		}
	}
	now, _ := policy.ParseInput("2026-10-21T12:00:00")
	if start, _, buckets, err := usageHeatmapRange(policy, now, 1); err != nil || start != "2026-10-19" || len(buckets) != 3 {
		t.Fatalf("单周错误: %s %d %v", start, len(buckets), err)
	}
}

func TestGetUsageHeatmap_ContinuousBusinessDatesAndDataStart(t *testing.T) {
	app := newUsageStatsTestApp(t)
	policy, err := timezonepolicy.New("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	app.timezonePolicy = policy
	now := time.Now()
	first := now.AddDate(0, 0, -20)
	if _, err := app.usageTracker.GetWriteDB().Exec(`INSERT INTO request_logs (
		request_id, start_time, status, request_kind, request_family, input_tokens, output_tokens, cache_read_tokens
	) VALUES ('heatmap', ?, 'completed', 'generation', 'codex', 100, 20, 80)`, timezonepolicy.FormatStorage(first)); err != nil {
		t.Fatal(err)
	}
	got, err := app.GetUsageHeatmap(UsageHeatmapQueryParams{Weeks: 4})
	if err != nil {
		t.Fatal(err)
	}
	if got.Timezone != policy.Name() || got.EndDate != policy.BusinessDate(now) || got.DataStartDate != policy.BusinessDate(first) || got.Days[0].Date != got.StartDate || got.Days[len(got.Days)-1].Date != got.EndDate {
		t.Fatalf("日期元数据错误: %+v", got)
	}
	var count, tokens int64
	for i, day := range got.Days {
		start, err := policy.ParseInput(day.StartTime)
		if err != nil || policy.BusinessDate(start) != day.Date {
			t.Fatalf("业务日/UTC 边界错误: %+v %v", day, err)
		}
		end, err := policy.ParseInput(day.EndTime)
		if err != nil || !end.After(start) {
			t.Fatalf("桶终点错误: %+v %v", day, err)
		}
		if i > 0 && got.Days[i-1].EndTime != day.StartTime {
			t.Fatalf("日期不连续: %+v", day)
		}
		count += day.RequestCount
		tokens += day.TotalTokens
	}
	if count != 1 || tokens != 120 {
		t.Fatalf("API 用量口径错误: %d %d", count, tokens)
	}
}

func TestGetUsageHeatmap_EmptyNilTrackerAndHotPoolOnly(t *testing.T) {
	policy, _ := timezonepolicy.New("Asia/Shanghai")
	app := &App{timezonePolicy: policy}
	if got, err := app.GetUsageHeatmap(UsageHeatmapQueryParams{}); err != nil || got.Days == nil || len(got.Days) != 0 || got.DataStartDate != "" {
		t.Fatalf("nil tracker: %+v %v", got, err)
	}
	app = newUsageStatsTestApp(t)
	app.timezonePolicy = policy
	got, err := app.GetUsageHeatmap(UsageHeatmapQueryParams{})
	if err != nil || got.DataStartDate != "" || len(got.Days) < 365 {
		t.Fatalf("空库: %+v %v", got, err)
	}
	app.usageTracker.RecordRequestStart("heatmap-hot", "127.0.0.1", "test", "POST", "/v1/messages", false)
	got, err = app.GetUsageHeatmap(UsageHeatmapQueryParams{})
	if err != nil || got.DataStartDate != got.EndDate || got.Days[len(got.Days)-1].RequestCount != 1 {
		t.Fatalf("仅热池: %+v %v", got, err)
	}
}

func TestUsageHeatmapRange_UsesConfiguredDateAndDST(t *testing.T) {
	policy, _ := timezonepolicy.New("America/New_York")
	now := time.Date(2026, 11, 2, 2, 0, 0, 0, time.UTC)
	start, end, buckets, err := usageHeatmapRange(policy, now, 1)
	if err != nil || start != "2026-10-26" || end != "2026-11-01" || len(buckets) != 7 || buckets[6].End.Sub(buckets[6].Start) != 25*time.Hour {
		t.Fatalf("配置日期/DST 错误: %s %s %d %v", start, end, len(buckets), err)
	}
}
