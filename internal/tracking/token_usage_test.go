package tracking

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"
)

func TestCalculateCostV2_CodexCacheWritesDoNotChangeBaseCosts(t *testing.T) {
	pricing := ModelPricing{Input: 10, Output: 50, CacheRead: 1, CacheCreation: 12.5, CacheCreation1h: 20}
	for _, path := range []string{"/v1/responses", "/v1/responses/compact", "/backend-api/codex/responses"} {
		for name, multiplier := range map[string]*EndpointMultiplier{
			"默认":  nil,
			"总倍率": {CostMultiplier: 0.8},
			"分项倍率": {InputCostMultiplier: 1.1, OutputCostMultiplier: 0.7, CacheReadCostMultiplier: 0.2,
				CacheCreationCostMultiplier: 9, CacheCreationCostMultiplier1h: 7},
		} {
			t.Run(path+"/"+name, func(t *testing.T) {
				usage := TokenUsage{InputTokens: 272077, OutputTokens: 1671, CacheReadTokens: 265159}
				before := CalculateCostV2(&usage, &pricing, multiplier, path)
				if multiplier == nil && (math.Abs(before.TotalCost-0.417889) > 1e-12 || fmt.Sprintf("$%.3f", before.TotalCost) != "$0.418") {
					t.Fatalf("截图基准费用错误：%+v", before)
				}
				for _, written := range []int64{0, 2000, 6918} {
					usage.CacheCreationTokens = written
					after := CalculateCostV2(&usage, &pricing, multiplier, path)
					if after != before {
						t.Fatalf("缓存写入 %d 改变了费用：修改前 %+v，修改后 %+v", written, before, after)
					}
				}
			})
		}
	}
}

func TestCalculateCostV2_ClaudeCacheWritesRemainSeparatelyCharged(t *testing.T) {
	usage := TokenUsage{InputTokens: 1000, OutputTokens: 20, CacheCreationTokens: 300,
		CacheCreation5mTokens: 100, CacheCreation1hTokens: 200, CacheReadTokens: 500}
	pricing := ModelPricing{Input: 10, Output: 50, CacheCreation: 12.5, CacheCreation1h: 20, CacheRead: 1}
	for _, path := range []string{"", "/v1/messages"} {
		cost := CalculateCostV2(&usage, &pricing, nil, path)
		if math.Abs(cost.TotalCost-0.01675) > 1e-12 || math.Abs(cost.CacheCreation5mCost-0.00125) > 1e-12 || math.Abs(cost.CacheCreation1hCost-0.004) > 1e-12 {
			t.Fatalf("Claude 独立缓存计费改变：%+v", cost)
		}
	}
}

func TestCacheWriteTotals_QueriesKeepStoredHistoricalCost(t *testing.T) {
	tracker := newModelUsageTestTracker(t)
	now := time.Now()
	seedModelUsageRequests(t, tracker, []seededModelUsageRequest{
		{requestID: "cache-history", modelName: "gpt-6-astra", requestFamily: "codex", upstreamType: "account", upstreamName: "synthetic",
			status: "completed", inputTokens: 272077, outputTokens: 1671, cacheRead: 265159, cost: 0.123456, durationMs: 100, startTime: now},
		{requestID: "cache-new", modelName: "gpt-6-astra", requestFamily: "codex", upstreamType: "account", upstreamName: "synthetic",
			status: "completed", inputTokens: 272077, outputTokens: 1671, cacheCreation: 2000, cacheRead: 265159, cost: 0.417889, durationMs: 100, startTime: now},
		{requestID: "cache-claude", modelName: "claude-test", requestFamily: "claude", upstreamType: "endpoint", upstreamName: "synthetic",
			status: "completed", inputTokens: 100, outputTokens: 50, cacheCreation: 10, cacheRead: 20, cost: 0.5, durationMs: 100, startTime: now},
	})
	opts := modelUsageQueryRange(now)
	models, err := tracker.QueryModelUsageStatsWithHotPool(context.Background(), opts, 10)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := tracker.QueryAggregatedRequestStatsWithHotPool(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	const wantTotal = 273748*2 + 180
	const wantCost = 0.123456 + 0.417889 + 0.5
	if models.TotalTokens != wantTotal || stats.TotalTokens != wantTotal || math.Abs(models.TotalCostUSD-wantCost) > 1e-12 || math.Abs(stats.TotalCostUSD-wantCost) > 1e-12 {
		t.Fatalf("汇总与历史费用不一致：models=%+v stats=%+v", models, stats)
	}
	var creation int64
	var storedCost float64
	if err := tracker.GetWriteDB().QueryRow(`SELECT cache_creation_tokens, total_cost_usd FROM request_logs WHERE request_id = ?`, "cache-history").Scan(&creation, &storedCost); err != nil {
		t.Fatal(err)
	}
	if creation != 0 || storedCost != 0.123456 {
		t.Fatalf("历史记录被修改：creation=%d cost=%f", creation, storedCost)
	}
	if models.CacheCreationTokens != 2010 || models.CacheReadTokens != 530338 {
		t.Fatalf("缓存分项应独立保留：%+v", models)
	}
}

func TestCacheWriteArchive_PersistsUsageWithoutExtraCost(t *testing.T) {
	adapter, err := NewSQLiteAdapter(DatabaseConfig{DatabasePath: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	if err := adapter.InitSchema(); err != nil {
		t.Fatal(err)
	}
	price := ModelPricing{Input: 10, Output: 50, CacheRead: 1, CacheCreation: 12.5, CacheCreation1h: 20}
	archive := &ArchiveManager{adapter: adapter, pricing: map[string]ModelPricing{"gpt-6-astra": price}}
	now := time.Now()
	for _, creation := range []int64{0, 2000} {
		req := &ActiveRequest{
			RequestID: fmt.Sprintf("archive-cache-%d", creation), StartTime: now.Add(-time.Second), EndTime: &now,
			Method: "POST", Path: "/v1/responses", RequestFamily: RequestFamilyCodex,
			RequestSource: RequestSourceCodexSubscriptionProxy, RequestKind: RequestKindGeneration,
			Status: "completed", ModelName: "gpt-6-astra", UpstreamType: "account",
			InputTokens: 272077, OutputTokens: 1671, CacheReadTokens: 265159, CacheCreationTokens: creation,
			UsageKnown: true, CostKnown: true,
		}
		if err := archive.batchInsert([]*ArchiveEvent{{Request: req, Timestamp: now}}); err != nil {
			t.Fatal(err)
		}
		var storedCreation, stored5m, stored1h int64
		var inputCost, creationCost, totalCost float64
		if err := adapter.GetReadDB().QueryRow(`SELECT cache_creation_tokens, cache_creation_5m_tokens, cache_creation_1h_tokens,
			input_cost_usd, cache_creation_cost_usd, total_cost_usd FROM request_logs WHERE request_id = ?`, req.RequestID).
			Scan(&storedCreation, &stored5m, &stored1h, &inputCost, &creationCost, &totalCost); err != nil {
			t.Fatal(err)
		}
		if storedCreation != creation || stored5m != 0 || stored1h != 0 || creationCost != 0 || math.Abs(inputCost-0.06918) > 1e-12 || math.Abs(totalCost-0.417889) > 1e-12 {
			t.Fatalf("归档不一致：creation=%d 5m=%d 1h=%d inputCost=%f creationCost=%f totalCost=%f", storedCreation, stored5m, stored1h, inputCost, creationCost, totalCost)
		}
	}
}

func TestCacheWriteFinalFailure_KeepsProtocolSpecificDuration(t *testing.T) {
	for _, hot := range []bool{true, false} {
		for _, codex := range []bool{true, false} {
			t.Run(fmt.Sprintf("hot=%t/codex=%t", hot, codex), func(t *testing.T) {
				tracker := newRequestLifecycleTestTracker(t, hot, 20*time.Millisecond)
				tracker.UpdatePricing(map[string]ModelPricing{"fixture-model": {Input: 10, Output: 50, CacheCreation: 12.5, CacheRead: 1}})
				path, want5m := "/v1/messages", int64(2000)
				if codex {
					path, want5m = "/v1/responses", 0
				}
				const id = "cache-write-failure"
				tracker.RecordRequestStart(id, "127.0.0.1", "synthetic", "POST", path, true)
				usage := &TokenUsage{InputTokens: 272077, OutputTokens: 1671, CacheCreationTokens: 2000, CacheReadTokens: 265159, InputIncludesCache: codex}
				tracker.RecordRequestFinalFailure(id, "fixture-model", "failed", "synthetic_failure", "synthetic", time.Second, 502, usage)
				deadline := time.Now().Add(3 * time.Second)
				for time.Now().Before(deadline) {
					data, err := tracker.GetRequestLifecycleData(context.Background(), id)
					if err != nil {
						t.Fatal(err)
					}
					if data != nil && data.Detail.Status == "failed" {
						row := data.Detail
						if row.CacheCreationTokens != 2000 || row.CacheCreation5mTokens != want5m || row.CacheCreation1hTokens != 0 {
							t.Fatalf("失败请求的缓存时长不正确：%+v", row)
						}
						if codex && (row.CacheCreationCostUSD != 0 || math.Abs(row.TotalCostUSD-0.417889) > 1e-12) {
							t.Fatalf("Codex 失败请求费用改变：%+v", row)
						}
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Fatal("失败请求未完成结算")
			})
		}
	}
}

func TestCacheWriteMissingProtocolFlag_KeepsDurationAndBaseCost(t *testing.T) {
	for _, hot := range []bool{true, false} {
		for _, status := range []string{"completed", "failed", "cancelled"} {
			for _, path := range []string{"/v1/responses", "/v1/responses/compact", "/backend-api/codex/responses", "/v1/messages"} {
				t.Run(fmt.Sprintf("hot=%t/%s/%s", hot, status, path), func(t *testing.T) {
					tracker := newRequestLifecycleTestTracker(t, hot, 20*time.Millisecond)
					tracker.UpdatePricing(map[string]ModelPricing{"fixture-model": {Input: 10, Output: 50, CacheCreation: 12.5, CacheRead: 1}})
					const id = "cache-missing-protocol"
					tracker.RecordRequestStart(id, "127.0.0.1", "synthetic", "POST", path, true)
					// 热池直接读内存；传统模式等待起始记录落库后验证路径识别。
					waitTrackingDetail(t, tracker, id, "pending")
					usage := &TokenUsage{InputTokens: 272077, OutputTokens: 1671, CacheCreationTokens: 2000, CacheReadTokens: 265159}
					before := *usage
					if status == "completed" {
						tracker.RecordRequestSuccess(id, "fixture-model", usage, time.Second)
					} else {
						tracker.RecordRequestFinalFailure(id, "fixture-model", status, "synthetic_failure", "", time.Second, 502, usage)
					}
					row := waitTrackingDetail(t, tracker, id, status)
					want5m, wantCreationCost := int64(0), 0.0
					if path == "/v1/messages" {
						want5m, wantCreationCost = 2000, 0.025
					}
					if row.CacheCreationTokens != 2000 || row.CacheCreation5mTokens != want5m || row.CacheCreation1hTokens != 0 || math.Abs(row.CacheCreationCostUSD-wantCreationCost) > 1e-12 {
						t.Fatalf("未按请求路径保留缓存时长和费用：%+v", row)
					}
					if path != "/v1/messages" && math.Abs(row.TotalCostUSD-0.417889) > 1e-12 {
						t.Fatalf("Codex 基础费用改变：%+v", row)
					}
					if *usage != before {
						t.Fatal("结算修改了调用方的原始用量")
					}
				})
			}
		}
	}
}
