package main

import (
	"testing"

	"sinuo/internal/tracking"
)

// 模型表两类口径：次数 / 成功率取生成口径，Token / 成本含独立压缩；
// 压缩专用模型保留 0 次调用行，不能丢行或清零。
func TestGetModelUsageStats_SplitsMetricScopesAndKeepsCompactionOnlyModels(t *testing.T) {
	app := newUsageStatsTestApp(t)
	insertRequestLogRow(t, app, seededRequestLog{
		ID: "gen-a", Kind: tracking.RequestKindGeneration, Source: tracking.RequestSourceLegacyProxy,
		UpstreamType: "account", UpstreamName: "account-a", Model: "model-shared",
		Status: "completed", InputTokens: 10, OutputTokens: 2, Cost: 0.3,
	})
	insertRequestLogRow(t, app, seededRequestLog{
		ID: "comp-a", Kind: tracking.RequestKindCompaction, Source: tracking.RequestSourceLegacyProxy,
		UpstreamType: "account", UpstreamName: "account-a", Model: "model-shared",
		Status: "completed", InputTokens: 100, OutputTokens: 10, Cost: 1.2,
	})
	insertRequestLogRow(t, app, seededRequestLog{
		ID: "comp-only", Kind: tracking.RequestKindCompaction, Source: tracking.RequestSourceCodexSubscriptionProxy,
		UpstreamType: "subscription", UpstreamName: "chatgpt-subscription", Model: "model-compact-only",
		Status: "completed", InputTokens: 50, OutputTokens: 5, Cost: 0.5,
	})

	startDate, endDate := explicitUsageWindow()
	result, err := app.GetModelUsageStats(ModelUsageQueryParams{
		Period: "1d", StartDate: startDate, EndDate: endDate, SourceView: "all", Limit: 20,
	})
	if err != nil {
		t.Fatalf("GetModelUsageStats failed: %v", err)
	}

	rows := make(map[string]ModelUsageRowData, len(result.Rows))
	for _, row := range result.Rows {
		rows[row.ModelName] = row
	}

	shared, ok := rows["model-shared"]
	if !ok {
		t.Fatalf("missing shared model row: %+v", result.Rows)
	}
	if shared.RequestCount != 1 || shared.TotalTokens != 122 || shared.TotalCostUSD != 1.5 {
		t.Fatalf("shared model must mix generation counts with full usage: %+v", shared)
	}
	if shared.SuccessRate != 100 {
		t.Fatalf("shared model success rate must use generation samples, got %f", shared.SuccessRate)
	}

	compactOnly, ok := rows["model-compact-only"]
	if !ok {
		t.Fatalf("compaction-only model row must not be dropped: %+v", result.Rows)
	}
	if compactOnly.RequestCount != 0 || compactOnly.TotalTokens != 55 || compactOnly.TotalCostUSD != 0.5 {
		t.Fatalf("compaction-only model must keep usage with zero calls: %+v", compactOnly)
	}

	if result.TotalRequests != 1 || result.TotalTokens != 177 || result.TotalCostUSD != 2 {
		t.Fatalf("summary must split metrics by scope: %+v", result)
	}
}
