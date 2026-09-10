package tracking

import (
	"context"
	"testing"
	"time"
)

func TestDatabaseOperations(t *testing.T) {
	config := &Config{
		Enabled:         true,
		DatabasePath:    ":memory:",
		BufferSize:      100,
		BatchSize:       10,
		FlushInterval:   1 * time.Second,
		MaxRetry:        3,
		CleanupInterval: 24 * time.Hour,
		RetentionDays:   30,
		ModelPricing: map[string]ModelPricing{
			"claude-3-5-haiku-20241022": {
				Input:         1.00,
				Output:        5.00,
				CacheCreation: 1.25,
				CacheRead:     0.10,
			},
		},
		DefaultPricing: ModelPricing{
			Input:         2.00,
			Output:        10.00,
			CacheCreation: 2.50,
			CacheRead:     0.20,
		},
	}

	tracker, err := NewUsageTracker(config)
	if err != nil {
		t.Fatalf("Failed to create usage tracker: %v", err)
	}
	defer tracker.Close()

	ctx := context.Background()

	// Test database initialization
	stats, err := tracker.GetDatabaseStats(ctx)
	if err != nil {
		t.Fatalf("Failed to get database stats: %v", err)
	}

	if stats.TotalRequests != 0 {
		t.Errorf("Expected 0 requests initially, got %d", stats.TotalRequests)
	}

	// Test inserting a request start event
	events := []RequestEvent{
		{
			Type:      "start",
			RequestID: "req-test-001",
			Timestamp: time.Now(),
			Data: RequestStartData{
				ClientIP:  "127.0.0.1",
				UserAgent: "test-agent",
				Method:    "POST",
				Path:      "/v1/messages",
			},
		},
	}

	err = tracker.processBatch(events)
	if err != nil {
		t.Fatalf("Failed to process start event: %v", err)
	}

	// Test updating request status
	updateEvents := []RequestEvent{
		{
			Type:      "update",
			RequestID: "req-test-001",
			Timestamp: time.Now(),
			Data: RequestUpdateData{
				EndpointName: "test-endpoint",
				Status:       "success",
				RetryCount:   0,
				HTTPStatus:   200,
			},
		},
	}

	err = tracker.processBatch(updateEvents)
	if err != nil {
		t.Fatalf("Failed to process update event: %v", err)
	}

	// Test completing request with token usage
	completeEvents := []RequestEvent{
		{
			Type:      "complete",
			RequestID: "req-test-001",
			Timestamp: time.Now(),
			Data: RequestCompleteData{
				ModelName:           "claude-3-5-haiku-20241022",
				InputTokens:         100,
				OutputTokens:        50,
				CacheCreationTokens: 10,
				CacheReadTokens:     5,
				Duration:            500 * time.Millisecond,
			},
		},
	}

	err = tracker.processBatch(completeEvents)
	if err != nil {
		t.Fatalf("Failed to process complete event: %v", err)
	}

	// Verify the request was stored correctly
	stats, err = tracker.GetDatabaseStats(ctx)
	if err != nil {
		t.Fatalf("Failed to get database stats after insertion: %v", err)
	}

	if stats.TotalRequests != 1 {
		t.Errorf("Expected 1 request after insertion, got %d", stats.TotalRequests)
	}

	if stats.TotalCostUSD <= 0 {
		t.Errorf("Expected positive total cost, got %f", stats.TotalCostUSD)
	}
}

func TestRecordRequestStartRejectsInvalidSourceKindPairs(t *testing.T) {
	tracker, err := NewUsageTracker(&Config{
		Enabled: true, DatabasePath: ":memory:", BufferSize: 10, BatchSize: 1,
		FlushInterval: time.Hour, MaxRetry: 1, CleanupInterval: time.Hour, RetentionDays: 30,
		HotPool: &HotPoolSettings{Enabled: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tracker.Close()

	tests := []RequestDimensions{
		{RequestSource: RequestSourceLegacyProxy, RequestKind: RequestKindGeneration},
		{RequestSource: RequestSourceCodexSubscriptionProxy, RequestKind: RequestKindLegacy, UpstreamType: "subscription"},
		{RequestSource: RequestSourceCodexSubscriptionProxy, RequestKind: "", UpstreamType: "subscription"},
	}
	for index, dimensions := range tests {
		if err := tracker.RecordRequestStartAtWithDimensions(
			"invalid-pair", "", "", "POST", "/v1/responses", RequestFamilyCodex, true, time.Now(), dimensions,
		); err == nil {
			t.Fatalf("case %d unexpectedly accepted %+v", index, dimensions)
		}
	}
	if err := tracker.RecordRequestStartAtWithDimensions(
		"valid-pair", "", "", "POST", "/v1/responses", RequestFamilyCodex, true, time.Now(),
		RequestDimensions{RequestSource: RequestSourceCodexSubscriptionProxy, RequestKind: RequestKindGeneration, UpstreamType: "subscription"},
	); err != nil {
		t.Fatalf("valid subscription pair rejected: %v", err)
	}
}

func TestNonHotAccountSuccessUsesSelectedAccountMultiplier(t *testing.T) {
	tracker, err := NewUsageTracker(&Config{
		Enabled: true, DatabasePath: ":memory:", BufferSize: 10, BatchSize: 10,
		FlushInterval: time.Hour, MaxRetry: 1, CleanupInterval: time.Hour, RetentionDays: 30,
		HotPool:      &HotPoolSettings{Enabled: false},
		ModelPricing: map[string]ModelPricing{"synthetic-model": {Input: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tracker.Close()
	tracker.UpdateAccountMultipliers(map[int64]EndpointMultiplier{7: {
		CostMultiplier: 2, InputCostMultiplier: 1, OutputCostMultiplier: 1,
		CacheCreationCostMultiplier: 1, CacheCreationCostMultiplier1h: 1, CacheReadCostMultiplier: 1,
	}})
	started := time.Now().Add(-time.Second)
	if err := tracker.processBatch([]RequestEvent{{
		Type: "start", RequestID: "account-multiplier", Timestamp: started,
		Data: RequestStartData{
			Method: "POST", Path: "/v1/responses", RequestFamily: RequestFamilyCodex,
			RequestSource: RequestSourceLegacyProxy, RequestKind: RequestKindLegacy,
			UpstreamType: "account", UpstreamName: "selected-account", UpstreamID: 7, UsageKnown: true,
		},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := tracker.processBatch([]RequestEvent{{
		Type: "success", RequestID: "account-multiplier", Timestamp: time.Now(),
		Data: RequestCompleteData{
			ModelName: "synthetic-model", Path: "/v1/responses", InputTokens: 1_000_000,
			Duration: time.Second, HasTokenUsage: true,
		},
	}}); err != nil {
		t.Fatal(err)
	}
	var cost float64
	if err := tracker.GetReadDB().QueryRow(`SELECT total_cost_usd FROM request_logs WHERE request_id='account-multiplier'`).Scan(&cost); err != nil {
		t.Fatal(err)
	}
	if cost != 2 {
		t.Fatalf("account cost = %v, want 2", cost)
	}
}

func TestRequestAccountMultiplierPropagatesDatabaseErrors(t *testing.T) {
	tracker, err := NewUsageTracker(&Config{
		Enabled: true, DatabasePath: ":memory:", BufferSize: 10, BatchSize: 10,
		FlushInterval: time.Hour, MaxRetry: 1, CleanupInterval: time.Hour, RetentionDays: 30,
		HotPool: &HotPoolSettings{Enabled: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tracker.Close()
	if _, err := tracker.GetWriteDB().Exec(`DROP TABLE request_logs`); err != nil {
		t.Fatal(err)
	}

	if _, err := tracker.requestAccountMultiplier("synthetic-request"); err == nil {
		t.Fatal("database query error was silently treated as the default multiplier")
	}
}

func TestUsageKnownStateMatchesAcrossHotAndColdFinalization(t *testing.T) {
	for _, hot := range []bool{false, true} {
		t.Run(map[bool]string{false: "cold", true: "hot"}[hot], func(t *testing.T) {
			tracker, err := NewUsageTracker(&Config{
				Enabled: true, DatabasePath: ":memory:", BufferSize: 50, BatchSize: 1,
				FlushInterval: 10 * time.Millisecond, MaxRetry: 1, CleanupInterval: time.Hour, RetentionDays: 30,
				HotPool: &HotPoolSettings{Enabled: hot, MaxSize: 100, MaxAge: time.Hour, CleanupInterval: time.Hour},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer tracker.Close()

			start := func(id string, dimensions RequestDimensions) {
				t.Helper()
				if err := tracker.RecordRequestStartAtWithDimensions(
					id, "", "", "POST", "/v1/responses", RequestFamilyCodex, true, time.Now(), dimensions,
				); err != nil {
					t.Fatal(err)
				}
				waitTrackingDetail(t, tracker, id, "pending")
			}

			start("legacy-no-usage", RequestDimensions{
				RequestSource: RequestSourceLegacyProxy, RequestKind: RequestKindLegacy, UpstreamType: "endpoint", UsageKnown: true,
			})
			tracker.RecordRequestSuccess("legacy-no-usage", "synthetic-model", nil, time.Millisecond)

			start("pool-no-usage", RequestDimensions{
				RequestSource: RequestSourceCodexSubscriptionProxy, RequestKind: RequestKindGeneration,
				UpstreamType: "account", UpstreamName: "selected-account", UpstreamID: 7, UsageKnown: false,
			})
			tracker.RecordRequestSuccess("pool-no-usage", "synthetic-model", nil, time.Millisecond)

			start("pool-reported-before-failure", RequestDimensions{
				RequestSource: RequestSourceCodexSubscriptionProxy, RequestKind: RequestKindGeneration,
				UpstreamType: "account", UpstreamName: "selected-account", UpstreamID: 7, UsageKnown: false,
			})
			tracker.RecordFailedRequestTokens("pool-reported-before-failure", "synthetic-model", &TokenUsage{InputTokens: 7}, time.Millisecond, "partial_usage")
			tracker.RecordRequestFinalFailure("pool-reported-before-failure", "synthetic-model", "failed", "synthetic_failure", "", time.Millisecond, 500, nil)

			legacy := waitTrackingDetail(t, tracker, "legacy-no-usage", "completed")
			if !legacy.UsageKnown || !legacy.CostKnown {
				t.Fatalf("legacy known state = %+v", legacy)
			}
			unknown := waitTrackingDetail(t, tracker, "pool-no-usage", "completed")
			if unknown.UsageKnown || unknown.CostKnown {
				t.Fatalf("pool unknown state = %+v", unknown)
			}
			reported := waitTrackingDetail(t, tracker, "pool-reported-before-failure", "failed")
			if !reported.UsageKnown || !reported.CostKnown || reported.InputTokens != 7 {
				t.Fatalf("pool reported state = %+v", reported)
			}
		})
	}
}

func waitTrackingDetail(t *testing.T, tracker *UsageTracker, requestID, status string) RequestDetail {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		details, _, err := tracker.QueryRequestDetailsWithHotPool(context.Background(), &QueryOptions{
			RequestID: requestID, UpstreamType: "all", Limit: 10,
		})
		if err == nil && len(details) == 1 && details[0].Status == status {
			return details[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("request %s did not reach %s", requestID, status)
	return RequestDetail{}
}

func TestSuccessEventPreservesSplitCacheCreationPricing(t *testing.T) {
	config := &Config{
		Enabled:         true,
		DatabasePath:    ":memory:",
		BufferSize:      100,
		BatchSize:       10,
		FlushInterval:   time.Second,
		MaxRetry:        3,
		CleanupInterval: 24 * time.Hour,
		RetentionDays:   30,
		ModelPricing: map[string]ModelPricing{
			"gpt-5.4": {
				Input:           1.0,
				Output:          5.0,
				CacheCreation:   1.25,
				CacheCreation1h: 2.0,
				CacheRead:       0.1,
			},
		},
		DefaultPricing: ModelPricing{
			Input:           1.0,
			Output:          5.0,
			CacheCreation:   1.25,
			CacheCreation1h: 2.0,
			CacheRead:       0.1,
		},
	}

	tracker, err := NewUsageTracker(config)
	if err != nil {
		t.Fatalf("Failed to create usage tracker: %v", err)
	}
	defer tracker.Close()

	startTime := time.Now().Add(-2 * time.Second)
	err = tracker.processBatch([]RequestEvent{
		{
			Type:      "start",
			RequestID: "req-success-split-cache",
			Timestamp: startTime,
			Data: RequestStartData{
				ClientIP:    "127.0.0.1",
				UserAgent:   "test-agent",
				Method:      "POST",
				Path:        "/v1/responses",
				IsStreaming: true,
			},
		},
	})
	if err != nil {
		t.Fatalf("Failed to process start event: %v", err)
	}

	err = tracker.processBatch([]RequestEvent{
		{
			Type:      "success",
			RequestID: "req-success-split-cache",
			Timestamp: time.Now(),
			Data: RequestCompleteData{
				ModelName:             "gpt-5.4",
				Path:                  "/v1/responses",
				InputTokens:           0,
				OutputTokens:          0,
				CacheCreationTokens:   1000,
				CacheCreation5mTokens: 0,
				CacheCreation1hTokens: 1000,
				CacheReadTokens:       0,
				Duration:              2 * time.Second,
			},
		},
	})
	if err != nil {
		t.Fatalf("Failed to process success event: %v", err)
	}

	var cacheCreation5mTokens, cacheCreation1hTokens int64
	var cacheCreationCostUSD, cacheCreation5mCostUSD, cacheCreation1hCostUSD float64
	db := tracker.GetReadDB()
	err = db.QueryRow(`
		SELECT
			cache_creation_5m_tokens,
			cache_creation_1h_tokens,
			cache_creation_cost_usd,
			cache_creation_5m_cost_usd,
			cache_creation_1h_cost_usd
		FROM request_logs
		WHERE request_id = ?
	`, "req-success-split-cache").Scan(
		&cacheCreation5mTokens,
		&cacheCreation1hTokens,
		&cacheCreationCostUSD,
		&cacheCreation5mCostUSD,
		&cacheCreation1hCostUSD,
	)
	if err != nil {
		t.Fatalf("Failed to query request log: %v", err)
	}

	if cacheCreation5mTokens != 0 {
		t.Fatalf("Expected cache_creation_5m_tokens=0, got %d", cacheCreation5mTokens)
	}
	if cacheCreation1hTokens != 1000 {
		t.Fatalf("Expected cache_creation_1h_tokens=1000, got %d", cacheCreation1hTokens)
	}

	expected1hCost := 1000 * 2.0 / 1_000_000
	if abs(cacheCreation1hCostUSD-expected1hCost) > 0.0000001 {
		t.Fatalf("Expected cache_creation_1h_cost_usd=%f, got %f", expected1hCost, cacheCreation1hCostUSD)
	}
	if abs(cacheCreation5mCostUSD-0) > 0.0000001 {
		t.Fatalf("Expected cache_creation_5m_cost_usd=0, got %f", cacheCreation5mCostUSD)
	}
	if abs(cacheCreationCostUSD-expected1hCost) > 0.0000001 {
		t.Fatalf("Expected cache_creation_cost_usd=%f, got %f", expected1hCost, cacheCreationCostUSD)
	}
}

func TestCostCalculation(t *testing.T) {
	config := &Config{
		Enabled:         true,
		DatabasePath:    ":memory:",
		CleanupInterval: 24 * time.Hour,
		RetentionDays:   30,
		ModelPricing: map[string]ModelPricing{
			"claude-3-5-haiku-20241022": {
				Input:         1.00, // $1 per 1M tokens
				Output:        5.00, // $5 per 1M tokens
				CacheCreation: 1.25, // $1.25 per 1M tokens
				CacheRead:     0.10, // $0.10 per 1M tokens
			},
		},
		DefaultPricing: ModelPricing{
			Input:         2.00,
			Output:        10.00,
			CacheCreation: 2.50,
			CacheRead:     0.20,
		},
	}

	tracker, err := NewUsageTracker(config)
	if err != nil {
		t.Fatalf("Failed to create usage tracker: %v", err)
	}
	defer tracker.Close()

	tokens := &TokenUsage{
		InputTokens:         100000, // 0.1M tokens
		OutputTokens:        50000,  // 0.05M tokens
		CacheCreationTokens: 10000,  // 0.01M tokens
		CacheReadTokens:     5000,   // 0.005M tokens
	}

	inputCost, outputCost, cacheCost, readCost, totalCost := tracker.calculateCost("claude-3-5-haiku-20241022", tokens)

	// Expected costs:
	// Input: 0.1M * $1 = $0.10
	// Output: 0.05M * $5 = $0.25
	// Cache Creation: 0.01M * $1.25 = $0.0125
	// Cache Read: 0.005M * $0.10 = $0.0005
	// Total: $0.363

	expectedInputCost := 0.10
	expectedOutputCost := 0.25
	expectedCacheCost := 0.0125
	expectedReadCost := 0.0005
	expectedTotalCost := 0.363

	tolerance := 0.0001

	if abs(inputCost-expectedInputCost) > tolerance {
		t.Errorf("Expected input cost %f, got %f", expectedInputCost, inputCost)
	}

	if abs(outputCost-expectedOutputCost) > tolerance {
		t.Errorf("Expected output cost %f, got %f", expectedOutputCost, outputCost)
	}

	if abs(cacheCost-expectedCacheCost) > tolerance {
		t.Errorf("Expected cache cost %f, got %f", expectedCacheCost, cacheCost)
	}

	if abs(readCost-expectedReadCost) > tolerance {
		t.Errorf("Expected read cost %f, got %f", expectedReadCost, readCost)
	}

	if abs(totalCost-expectedTotalCost) > tolerance {
		t.Errorf("Expected total cost %f, got %f", expectedTotalCost, totalCost)
	}
}

func TestDefaultPricing(t *testing.T) {
	config := &Config{
		Enabled:         true,
		DatabasePath:    ":memory:",
		CleanupInterval: 24 * time.Hour,
		RetentionDays:   30,
		DefaultPricing: ModelPricing{
			Input:         2.00,
			Output:        10.00,
			CacheCreation: 2.50,
			CacheRead:     0.20,
		},
	}

	tracker, err := NewUsageTracker(config)
	if err != nil {
		t.Fatalf("Failed to create usage tracker: %v", err)
	}
	defer tracker.Close()

	tokens := &TokenUsage{
		InputTokens:  100000, // 0.1M tokens
		OutputTokens: 50000,  // 0.05M tokens
	}

	inputCost, outputCost, _, _, totalCost := tracker.calculateCost("unknown-model", tokens)

	// Expected costs with default pricing:
	// Input: 0.1M * $2 = $0.20
	// Output: 0.05M * $10 = $0.50
	// Total: $0.70

	expectedInputCost := 0.20
	expectedOutputCost := 0.50
	expectedTotalCost := 0.70

	tolerance := 0.0001

	if abs(inputCost-expectedInputCost) > tolerance {
		t.Errorf("Expected default input cost %f, got %f", expectedInputCost, inputCost)
	}

	if abs(outputCost-expectedOutputCost) > tolerance {
		t.Errorf("Expected default output cost %f, got %f", expectedOutputCost, outputCost)
	}

	if abs(totalCost-expectedTotalCost) > tolerance {
		t.Errorf("Expected default total cost %f, got %f", expectedTotalCost, totalCost)
	}
}

func TestHealthCheck(t *testing.T) {
	config := &Config{
		Enabled:         true,
		DatabasePath:    ":memory:",
		BufferSize:      100,
		CleanupInterval: 24 * time.Hour,
		RetentionDays:   30,
	}

	tracker, err := NewUsageTracker(config)
	if err != nil {
		t.Fatalf("Failed to create usage tracker: %v", err)
	}
	defer tracker.Close()

	ctx := context.Background()

	// Test healthy state
	err = tracker.HealthCheck(ctx)
	if err != nil {
		t.Errorf("Expected healthy tracker, got error: %v", err)
	}
}

func TestHealthCheckDisabled(t *testing.T) {
	config := &Config{
		Enabled:         false,
		CleanupInterval: 24 * time.Hour,
		RetentionDays:   30,
	}

	tracker, err := NewUsageTracker(config)
	if err != nil {
		t.Fatalf("Failed to create disabled usage tracker: %v", err)
	}
	defer tracker.Close()

	ctx := context.Background()

	// Test that disabled tracker is considered healthy
	err = tracker.HealthCheck(ctx)
	if err != nil {
		t.Errorf("Expected disabled tracker to be healthy, got error: %v", err)
	}
}

// Helper function to calculate absolute difference
func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
