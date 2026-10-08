package tracking

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func seedRequestUpstreamOptionRows(t *testing.T, tracker *UsageTracker, now time.Time) {
	t.Helper()
	seedModelUsageRequests(t, tracker, []seededModelUsageRequest{
		{requestID: "req-uo-1", modelName: "claude-sonnet-4", requestFamily: "claude", upstreamType: "endpoint", upstreamName: "duckcoding", status: "completed", startTime: now.Add(-10 * time.Minute)},
		{requestID: "req-uo-2", modelName: "claude-opus-4", requestFamily: "claude", upstreamType: "endpoint", upstreamName: "duckcoding", status: "failed", startTime: now.Add(-9 * time.Minute)},
		{requestID: "req-uo-3", modelName: "claude-sonnet-4", requestFamily: "claude", upstreamType: "subscription", upstreamName: "claude-subscription", status: "completed", startTime: now.Add(-8 * time.Minute)},
		{requestID: "req-uo-4", modelName: "gpt-5-codex", requestFamily: "codex", upstreamType: "account", upstreamName: "acct-1", status: "completed", startTime: now.Add(-7 * time.Minute)},
		// 空上游名（无端点可路由）不进候选
		{requestID: "req-uo-5", modelName: "claude-sonnet-4", requestFamily: "claude", upstreamType: "endpoint", upstreamName: "", status: "failed", startTime: now.Add(-6 * time.Minute)},
		// 时间范围外
		{requestID: "req-uo-6", modelName: "claude-sonnet-4", requestFamily: "claude", upstreamType: "endpoint", upstreamName: "old-endpoint", status: "completed", startTime: now.Add(-3 * time.Hour)},
	})
}

func upstreamOptionNames(options []RequestUpstreamOption) []string {
	names := make([]string, 0, len(options))
	for _, option := range options {
		names = append(names, option.UpstreamName)
	}
	return names
}

func TestQueryRequestUpstreamOptions_DistinctUpstreamsInRange(t *testing.T) {
	tracker := newModelUsageTestTracker(t)
	now := time.Now()
	seedRequestUpstreamOptionRows(t, tracker, now)

	options, err := tracker.QueryRequestUpstreamOptions(context.Background(), modelUsageQueryRange(now))
	if err != nil {
		t.Fatalf("QueryRequestUpstreamOptions failed: %v", err)
	}

	want := []RequestUpstreamOption{
		{RequestFamily: "codex", UpstreamType: "account", UpstreamName: "acct-1"},
		{RequestFamily: "claude", UpstreamType: "subscription", UpstreamName: "claude-subscription"},
		{RequestFamily: "claude", UpstreamType: "endpoint", UpstreamName: "duckcoding"},
	}
	if !reflect.DeepEqual(options, want) {
		t.Fatalf("unexpected options:\n got %+v\nwant %+v", options, want)
	}
}

func TestQueryRequestUpstreamOptions_NarrowsByFamilyAndSource(t *testing.T) {
	tracker := newModelUsageTestTracker(t)
	now := time.Now()
	seedRequestUpstreamOptionRows(t, tracker, now)

	cases := []struct {
		name         string
		family       string
		upstreamType string
		want         []string
	}{
		{name: "claude 全部来源", family: "claude", want: []string{"claude-subscription", "duckcoding"}},
		{name: "claude 端点", family: "claude", upstreamType: "endpoint", want: []string{"duckcoding"}},
		{name: "订阅直通", upstreamType: "subscription", want: []string{"claude-subscription"}},
		{name: "codex", family: "codex", want: []string{"acct-1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := modelUsageQueryRange(now)
			opts.RequestFamily = tc.family
			opts.UpstreamType = tc.upstreamType
			options, err := tracker.QueryRequestUpstreamOptions(context.Background(), opts)
			if err != nil {
				t.Fatalf("QueryRequestUpstreamOptions failed: %v", err)
			}
			if got := upstreamOptionNames(options); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// 列表当前选中的上游 / 模型 / 状态不能收窄候选，否则选中一个上游后就切不到别的上游
func TestQueryRequestUpstreamOptions_IgnoresUpstreamModelAndStatusFilters(t *testing.T) {
	tracker := newModelUsageTestTracker(t)
	now := time.Now()
	seedRequestUpstreamOptionRows(t, tracker, now)

	opts := modelUsageQueryRange(now)
	opts.UpstreamName = "duckcoding"
	opts.ModelName = "claude-opus-4"
	opts.Status = "failed"
	opts.Limit = 1
	options, err := tracker.QueryRequestUpstreamOptions(context.Background(), opts)
	if err != nil {
		t.Fatalf("QueryRequestUpstreamOptions failed: %v", err)
	}
	want := []string{"acct-1", "claude-subscription", "duckcoding"}
	if got := upstreamOptionNames(options); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestQueryRequestUpstreamOptions_RejectsInvalidRequestKinds(t *testing.T) {
	tracker := newModelUsageTestTracker(t)
	opts := modelUsageQueryRange(time.Now())
	opts.RequestKinds = []string{"bogus"}
	if _, err := tracker.QueryRequestUpstreamOptions(context.Background(), opts); err == nil {
		t.Fatal("expected invalid request kind error")
	}
}
