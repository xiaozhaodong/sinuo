package tracking

import (
	"context"
	"testing"
	"time"

	timezonepolicy "cc-forwarder/internal/timezone"
)

func TestAggregateUsageSummaryAcrossTimezonesPreservesExplicitZeroAndUnknown(t *testing.T) {
	tracker, err := NewUsageTracker(&Config{
		Enabled: true, DatabasePath: ":memory:", BufferSize: 10, BatchSize: 10,
		FlushInterval: time.Hour, MaxRetry: 1, CleanupInterval: time.Hour, RetentionDays: 30,
		HotPool: &HotPoolSettings{Enabled: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tracker.Close()
	now := time.Now().UTC().Truncate(time.Second)
	stored := timezonepolicy.FormatStorage(now)
	if _, err := tracker.GetWriteDB().Exec(`INSERT INTO request_logs (
		request_id, start_time, model_name, request_family, request_source, request_kind,
		upstream_type, upstream_name, status, input_tokens, output_tokens,
		cache_creation_tokens, cache_read_tokens, total_cost_usd
	) VALUES
		('explicit-zero', ?, 'zero-model', 'codex', 'codex_subscription_proxy', 'generation',
		 'subscription', 'chatgpt-subscription', 'completed', 0, 0, 0, 0, 0),
		('unknown', ?, 'unknown-model', 'codex', 'codex_subscription_proxy', 'generation',
		 'subscription', 'chatgpt-subscription', 'failed', NULL, NULL, NULL, NULL, NULL)`, stored, stored); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"UTC", "Asia/Shanghai"} {
		policy, err := timezonepolicy.New(name)
		if err != nil {
			t.Fatal(err)
		}
		summaries, err := AggregateUsageSummaryWithQuerier(
			context.Background(), tracker.GetReadDB(), now.Add(-time.Hour), now.Add(time.Hour),
			&QueryOptions{UpstreamType: "subscription", RequestKinds: []string{RequestKindGeneration}}, policy,
		)
		if err != nil {
			t.Fatalf("%s aggregate: %v", name, err)
		}
		if len(summaries) != 2 {
			t.Fatalf("%s summaries = %+v", name, summaries)
		}
		byModel := make(map[string]UsageSummary, len(summaries))
		for _, summary := range summaries {
			byModel[summary.ModelName] = summary
		}
		if zero := byModel["zero-model"]; !zero.UsageKnown || !zero.CostKnown || zero.TotalInputTokens != 0 || zero.TotalCostUSD != 0 {
			t.Fatalf("%s explicit zero = %+v", name, zero)
		}
		if unknown := byModel["unknown-model"]; unknown.UsageKnown || unknown.CostKnown {
			t.Fatalf("%s unknown = %+v", name, unknown)
		}
	}
}
