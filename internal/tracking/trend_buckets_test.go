package tracking

import (
	"context"
	"testing"
	"time"

	timezonepolicy "sinuo/internal/timezone"
)

func mustPolicy(t *testing.T, name string) *timezonepolicy.Policy {
	t.Helper()
	policy, err := timezonepolicy.New(name)
	if err != nil {
		t.Fatalf("failed to create policy %s: %v", name, err)
	}
	return policy
}

func TestBuildTrendBuckets_MinuteBucketsAlignToLocalDay(t *testing.T) {
	policy := mustPolicy(t, "Asia/Shanghai")
	start, err := policy.ParseInput("2026-09-09T10:00:00")
	if err != nil {
		t.Fatal(err)
	}
	end, err := policy.ParseInput("2026-09-09T11:00:00")
	if err != nil {
		t.Fatal(err)
	}

	buckets, err := BuildTrendBuckets(policy, start, end, TrendGranularityMinute, 400)
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 12 {
		t.Fatalf("expected 12 five-minute buckets for a 1h range, got %d", len(buckets))
	}
	if !buckets[0].Start.Equal(start) {
		t.Fatalf("first bucket should start at the range start, got %s", buckets[0].Start)
	}
	if got := buckets[len(buckets)-1].End; !got.Equal(end) {
		t.Fatalf("last bucket should end at the range end, got %s", got)
	}
	for i := 1; i < len(buckets); i++ {
		if !buckets[i].Start.Equal(buckets[i-1].End) {
			t.Fatalf("buckets are not contiguous at %d", i)
		}
	}
}

func TestBuildTrendBuckets_MinuteBucketsAlignToWallClock(t *testing.T) {
	policy := mustPolicy(t, "Asia/Shanghai")
	// 起点落在 5 分钟桶中间，应回退到桶边界
	start, err := policy.ParseInput("2026-09-09T10:07:31")
	if err != nil {
		t.Fatal(err)
	}
	end, err := policy.ParseInput("2026-09-09T10:20:00")
	if err != nil {
		t.Fatal(err)
	}

	buckets, err := BuildTrendBuckets(policy, start, end, TrendGranularityMinute, 400)
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 3 {
		t.Fatalf("expected 3 buckets (10:05/10:10/10:15), got %d", len(buckets))
	}
	first := buckets[0].Label.In(policy.Location())
	if first.Minute() != 5 || first.Second() != 0 {
		t.Fatalf("expected first bucket to align to 10:05 local, got %s", first)
	}
}

func TestBuildTrendBuckets_DayBucketsFollowBusinessDaysAcrossDST(t *testing.T) {
	policy := mustPolicy(t, "America/New_York")
	start, _, err := policy.DayRange("2026-03-08")
	if err != nil {
		t.Fatal(err)
	}
	_, end, err := policy.DayRange("2026-03-09")
	if err != nil {
		t.Fatal(err)
	}

	buckets, err := BuildTrendBuckets(policy, start, end, TrendGranularityDay, 400)
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 2 {
		t.Fatalf("expected 2 business-day buckets, got %d", len(buckets))
	}
	if span := buckets[0].End.Sub(buckets[0].Start); span != 23*time.Hour {
		t.Fatalf("spring-forward day should span 23h, got %s", span)
	}
	if span := buckets[1].End.Sub(buckets[1].Start); span != 24*time.Hour {
		t.Fatalf("next day should span 24h, got %s", span)
	}
}

func TestBuildTrendBuckets_RejectsExcessiveRange(t *testing.T) {
	policy := mustPolicy(t, "Asia/Shanghai")
	start, _ := policy.ParseInput("0001-01-01T00:00:00+08:00")
	end, _ := policy.ParseInput("2026-09-30T00:00:00+08:00")
	if buckets, err := BuildTrendBuckets(policy, start, end, TrendGranularityMinute, 100); err == nil || len(buckets) != 0 {
		t.Fatalf("expected oversized range to fail without partial results, got %d buckets, err=%v", len(buckets), err)
	}
}

func TestBuildTrendBuckets_EmptyRange(t *testing.T) {
	policy := mustPolicy(t, "Asia/Shanghai")
	now := time.Now().UTC()

	buckets, err := BuildTrendBuckets(policy, now, now, TrendGranularityHour, 400)
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 0 {
		t.Fatalf("expected no buckets for empty range, got %d", len(buckets))
	}
}

func TestQueryRequestTrendBuckets_CountsEachBucketSeparately(t *testing.T) {
	tracker := newModelUsageTestTracker(t)
	policy := mustPolicy(t, "Asia/Shanghai")

	dayStart, _, err := policy.DayRange(policy.BusinessDate(time.Now()))
	if err != nil {
		t.Fatal(err)
	}

	seedModelUsageRequests(t, tracker, []seededModelUsageRequest{
		// 第 1 个桶（00:00-01:00）
		{requestID: "trend-1", modelName: "m", requestFamily: "claude", upstreamType: "endpoint", status: "completed", startTime: dayStart.Add(10 * time.Minute)},
		{requestID: "trend-2", modelName: "m", requestFamily: "claude", upstreamType: "endpoint", status: "failed", startTime: dayStart.Add(20 * time.Minute)},
		// 第 3 个桶（02:00-03:00）
		{requestID: "trend-3", modelName: "m", requestFamily: "claude", upstreamType: "endpoint", status: "completed", startTime: dayStart.Add(2*time.Hour + 5*time.Minute)},
	})

	buckets, err := BuildTrendBuckets(policy, dayStart, dayStart.Add(3*time.Hour), TrendGranularityHour, 400)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := tracker.QueryRequestTrendBuckets(context.Background(), &QueryOptions{}, buckets)
	if err != nil {
		t.Fatalf("QueryRequestTrendBuckets failed: %v", err)
	}
	if len(stats) != 3 {
		t.Fatalf("expected 3 buckets, got %d", len(stats))
	}
	if stats[0].Total != 2 || stats[0].Success != 1 || stats[0].Failed != 1 {
		t.Fatalf("unexpected first bucket: %+v", stats[0])
	}
	if stats[1].Total != 0 {
		t.Fatalf("expected empty middle bucket, got %+v", stats[1])
	}
	if stats[2].Total != 1 || stats[2].Success != 1 {
		t.Fatalf("unexpected third bucket: %+v", stats[2])
	}
}

func TestQueryRequestTrendBuckets_IncludesHotPool(t *testing.T) {
	tracker := newModelUsageTestTracker(t)
	policy := mustPolicy(t, "Asia/Shanghai")
	now := time.Now()

	tracker.RecordRequestStart("trend-hot", "127.0.0.1", "stats-agent", "POST", "/v1/messages", false)
	status := "processing"
	modelName := "claude-sonnet-4"
	tracker.RecordRequestUpdate("trend-hot", UpdateOptions{Status: &status, ModelName: &modelName})

	buckets, err := BuildTrendBuckets(policy, now.Add(-1*time.Hour), now.Add(time.Minute), TrendGranularityMinute, 400)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := tracker.QueryRequestTrendBuckets(context.Background(), &QueryOptions{}, buckets)
	if err != nil {
		t.Fatalf("QueryRequestTrendBuckets failed: %v", err)
	}

	var total int64
	for _, bucket := range stats {
		total += bucket.Total
	}
	if total != 1 {
		t.Fatalf("expected hot-pool request counted once, got %d", total)
	}
	_ = tracker.hotPool.Remove("trend-hot")
}

func TestBuildTrendBuckets_ClipsEdgesAndRetainsLabels(t *testing.T) {
	policy := mustPolicy(t, "Asia/Shanghai")
	for _, grain := range []TrendGranularity{TrendGranularityMinute, TrendGranularityHour, TrendGranularityDay} {
		t.Run(string(grain), func(t *testing.T) {
			start, _ := policy.ParseInput("2026-09-08T10:07:31.123456")
			end, _ := policy.ParseInput("2026-09-09T10:08:32.654321")
			buckets, err := BuildTrendBuckets(policy, start, end, grain, 400)
			if err != nil {
				t.Fatal(err)
			}
			if !buckets[0].Start.Equal(start) || !buckets[len(buckets)-1].End.Equal(end) {
				t.Fatal("query window was expanded")
			}
			if !buckets[0].Label.Before(start) {
				t.Fatal("expected aligned label before partial bucket start")
			}
			for i, b := range buckets {
				if b.Start.Before(start) || b.End.After(end) {
					t.Fatal("bucket escapes the requested window")
				}
				if i > 0 && !b.Start.Equal(buckets[i-1].End) {
					t.Fatal("bucket gap or overlap")
				}
			}
		})
	}
}

func TestBuildTrendBuckets_FallDSTAndInvalidInput(t *testing.T) {
	policy := mustPolicy(t, "America/New_York")
	start, end, err := policy.DayRange("2026-11-01")
	if err != nil {
		t.Fatal(err)
	}
	buckets, err := BuildTrendBuckets(policy, start, end, TrendGranularityHour, 400)
	if err != nil || len(buckets) != 25 {
		t.Fatalf("expected 25 hourly buckets, got %d, %v", len(buckets), err)
	}
	if _, err := BuildTrendBuckets(policy, start, end, TrendGranularity("invalid"), 400); err == nil {
		t.Fatal("expected invalid granularity error")
	}
	if _, err := BuildTrendBuckets(policy, time.Time{}, end, TrendGranularityHour, 400); err == nil {
		t.Fatal("expected missing start error")
	}
}
