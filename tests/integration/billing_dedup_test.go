package integration

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"sinuo/internal/proxy"
	"sinuo/internal/tracking"

	"github.com/stretchr/testify/require"
)

// 计费去重集成测试：经 RequestLifecycleManager 写入，再从「热池 + 数据库」合并视图读回，
// 验证同一请求最终只有一条计费记录，且记录的是最终结果。
// 「同一 request_id 只有一行」由 request_logs.request_id 的 UNIQUE 约束兜底，这里只覆盖业务路径。

const (
	billingTestModel    = "claude-3-5-haiku-20241022"
	billingWaitTimeout  = 2 * time.Second
	billingPollInterval = 20 * time.Millisecond
)

func TestBillingDedup_RetryIntermediateStatesRecordOnlyFinalFailure(t *testing.T) {
	tracker := newBillingTestTracker(t)
	rlm := startBillingRequest(t, tracker, "retry-final-failure", false)

	for attempt := 1; attempt <= 3; attempt++ {
		rlm.UpdateStatus("retry", attempt, 500)
	}
	rlm.UpdateStatus("timeout", 3, 500)
	rlm.RecordTokensForFailedRequest(&tracking.TokenUsage{InputTokens: 257, OutputTokens: 25}, "timeout")

	record := waitBillingRecord(t, tracker, rlm, func(r *tracking.RequestDetail) bool {
		return r.Status == "timeout" && r.InputTokens == 257
	})
	require.Equal(t, int64(25), record.OutputTokens)
	require.Positive(t, record.TotalCostUSD, "失败请求有真实用量时应计费")
}

func TestBillingDedup_SuccessAfterFailureOverridesFailedTokens(t *testing.T) {
	tracker := newBillingTestTracker(t)
	rlm := startBillingRequest(t, tracker, "fail-then-success", false)

	rlm.UpdateStatus("timeout", 1, 500)
	rlm.RecordTokensForFailedRequest(&tracking.TokenUsage{InputTokens: 100, OutputTokens: 50}, "timeout")
	rlm.UpdateStatus("completed", 2, 200)
	rlm.CompleteRequest(&tracking.TokenUsage{InputTokens: 300, OutputTokens: 150})

	record := waitBillingRecord(t, tracker, rlm, func(r *tracking.RequestDetail) bool {
		return r.Status == "completed"
	})
	require.Equal(t, int64(300), record.InputTokens, "最终计费应以成功的用量为准")
	require.Equal(t, int64(150), record.OutputTokens, "最终计费应以成功的用量为准")
}

func TestBillingDedup_ConcurrentWritesSameRequestKeepOneRecord(t *testing.T) {
	tracker := newBillingTestTracker(t)
	rlm := startBillingRequest(t, tracker, "concurrent-same-id", false)
	tokens := &tracking.TokenUsage{InputTokens: 200, OutputTokens: 100}

	const writers = 10
	var wg sync.WaitGroup
	for range writers {
		wg.Go(func() { rlm.RecordTokensForFailedRequest(tokens, "concurrent_test") })
	}
	wg.Wait()

	record := waitBillingRecord(t, tracker, rlm, func(r *tracking.RequestDetail) bool {
		return r.InputTokens > 0
	})
	require.Equal(t, int64(200), record.InputTokens, "并发重复写入不能累加用量")
	require.Equal(t, int64(100), record.OutputTokens, "并发重复写入不能累加用量")
}

func TestBillingDedup_PartialStreamKeepsParsedTokens(t *testing.T) {
	tracker := newBillingTestTracker(t)
	rlm := startBillingRequest(t, tracker, "partial-stream", true)

	rlm.UpdateStatus("stream_error", 1, 0)
	rlm.RecordTokensForFailedRequest(&tracking.TokenUsage{InputTokens: 257, OutputTokens: 25}, "stream_interrupted")

	record := waitBillingRecord(t, tracker, rlm, func(r *tracking.RequestDetail) bool {
		return r.Status == "stream_error" && r.InputTokens == 257
	})
	require.Equal(t, int64(25), record.OutputTokens, "流中断前已解析的输出应计费")
	require.Positive(t, record.TotalCostUSD)
}

func TestBillingDedup_InvalidTokensAreNotRecorded(t *testing.T) {
	cases := map[string]*tracking.TokenUsage{
		"nil":      nil,
		"all_zero": {},
		"negative": {InputTokens: -1, OutputTokens: 10},
	}
	for name, tokens := range cases {
		t.Run(name, func(t *testing.T) {
			tracker := newBillingTestTracker(t)
			rlm := startBillingRequest(t, tracker, "invalid-"+name, false)
			rlm.UpdateStatus("error", 1, 500)
			rlm.RecordTokensForFailedRequest(tokens, "invalid_tokens")

			record := waitBillingRecord(t, tracker, rlm, func(r *tracking.RequestDetail) bool {
				return r.Status == "error"
			})
			require.Zero(t, record.InputTokens, "无效用量不应写入")
			require.Zero(t, record.OutputTokens, "无效用量不应写入")
		})
	}
}

func newBillingTestTracker(t *testing.T) *tracking.UsageTracker {
	t.Helper()
	tracker, err := tracking.NewUsageTracker(&tracking.Config{
		Enabled:       true,
		DatabasePath:  ":memory:",
		BufferSize:    100,
		BatchSize:     10,
		FlushInterval: 50 * time.Millisecond,
		MaxRetry:      3,
		ModelPricing: map[string]tracking.ModelPricing{
			billingTestModel: {Input: 3.00, Output: 15.00, CacheCreation: 3.75, CacheRead: 0.30},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = tracker.Close() })
	return tracker
}

func startBillingRequest(t *testing.T, tracker *tracking.UsageTracker, name string, streaming bool) *proxy.RequestLifecycleManager {
	t.Helper()
	requestID := fmt.Sprintf("req-billing-%s-%d", name, time.Now().UnixNano())
	rlm := proxy.NewRequestLifecycleManager(tracker, nil, requestID, nil)
	rlm.SetModel(billingTestModel)
	// 与生产顺序一致：先进热池再设端点。反过来时 SetEndpoint 的追踪更新会降级进旧事件队列，
	// 而旧队列关闭时的最后一批会空转重试约 3 秒（见 tracking/database.go flushBatch）。
	rlm.StartRequest("192.168.1.1", "test-agent", "POST", "/v1/messages", streaming)
	rlm.SetEndpoint("test-endpoint")
	return rlm
}

// waitBillingRecord 轮询合并视图，直到该请求的唯一记录满足 ready，超时则失败。
func waitBillingRecord(t *testing.T, tracker *tracking.UsageTracker, rlm *proxy.RequestLifecycleManager, ready func(*tracking.RequestDetail) bool) *tracking.RequestDetail {
	t.Helper()
	requestID := rlm.GetRequestID()
	var matched []*tracking.RequestDetail
	deadline := time.Now().Add(billingWaitTimeout)
	for {
		details, _, err := tracker.QueryRequestDetailsWithHotPool(context.Background(), &tracking.QueryOptions{RequestID: requestID, Limit: 10})
		require.NoError(t, err)
		matched = matched[:0]
		for i := range details {
			if details[i].RequestID == requestID {
				matched = append(matched, &details[i])
			}
		}
		if len(matched) == 1 && ready(matched[0]) {
			return matched[0]
		}
		require.LessOrEqual(t, len(matched), 1, "同一请求只能有一条计费记录")
		if time.Now().After(deadline) {
			t.Fatalf("等待计费记录超时: request_id=%s records=%+v", requestID, matched)
		}
		time.Sleep(billingPollInterval)
	}
}
