package proxy

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"sinuo/internal/tracking"
)

func TestRequestLifecycleManager_NewRequestLifecycleManager(t *testing.T) {
	requestID := "test-lifecycle-123"

	rlm := NewRequestLifecycleManager(nil, nil, requestID, nil)

	if rlm.requestID != requestID {
		t.Error("RequestID not set correctly")
	}
	if rlm.lastStatus != "pending" {
		t.Error("Initial status should be 'pending'")
	}
}

func TestRequestLifecycleManager_SettersAndGetters(t *testing.T) {
	requestID := "test-getters-303"
	rlm := NewRequestLifecycleManager(nil, nil, requestID, nil)

	// 测试设置方法
	rlm.SetEndpoint("test-endpoint")
	rlm.SetModel("claude-3-sonnet")
	rlm.SetFinalStatusCode(404)

	// 测试获取方法
	if rlm.GetRequestID() != requestID {
		t.Errorf("Expected RequestID %s, got %s", requestID, rlm.GetRequestID())
	}

	if rlm.GetEndpointName() != "test-endpoint" {
		t.Errorf("Expected endpoint 'test-endpoint', got '%s'", rlm.GetEndpointName())
	}

	if rlm.modelName != "claude-3-sonnet" {
		t.Errorf("Expected model 'claude-3-sonnet', got '%s'", rlm.modelName)
	}

	if rlm.GetFinalStatusCode() != 404 {
		t.Errorf("Expected final status code 404, got %d", rlm.GetFinalStatusCode())
	}
}

func TestRequestLifecycleManager_UpdateStatus(t *testing.T) {
	requestID := "test-update-789"
	rlm := NewRequestLifecycleManager(nil, nil, requestID, nil)

	// 设置端点信息
	rlm.SetEndpoint("test-endpoint")

	// 更新状态
	rlm.UpdateStatus("processing", 1, 200)

	// 验证状态更新
	if rlm.lastStatus != "processing" {
		t.Errorf("Expected status 'processing', got '%s'", rlm.lastStatus)
	}
	if rlm.retryCount != 0 {
		t.Errorf("未联系上游时重试数应为0，got %d", rlm.retryCount)
	}
}

func TestRequestLifecycleManager_Duration(t *testing.T) {
	requestID := "test-duration-404"
	rlm := NewRequestLifecycleManager(nil, nil, requestID, nil)

	// 等待一小段时间
	time.Sleep(10 * time.Millisecond)

	duration := rlm.GetDuration()
	if duration < 10*time.Millisecond {
		t.Error("Duration should be at least 10ms")
	}

	if duration > 100*time.Millisecond {
		t.Error("Duration should not be more than 100ms")
	}
}

func TestRequestLifecycleManager_RecordFirstToken_OnlyRecordsOnce(t *testing.T) {
	tracker, err := tracking.NewUsageTracker(&tracking.Config{
		Enabled:         true,
		DatabasePath:    ":memory:",
		BufferSize:      10,
		BatchSize:       5,
		FlushInterval:   50 * time.Millisecond,
		MaxRetry:        3,
		CleanupInterval: 24 * time.Hour,
		RetentionDays:   30,
		HotPool: &tracking.HotPoolSettings{
			Enabled:          true,
			MaxAge:           30 * time.Minute,
			MaxSize:          1000,
			CleanupInterval:  time.Minute,
			ArchiveOnCleanup: true,
		},
	})
	if err != nil {
		t.Fatalf("failed to create usage tracker: %v", err)
	}
	defer tracker.Close()

	rlm := NewRequestLifecycleManager(tracker, nil, "req-first-response-once", nil)
	rlm.StartRequest("127.0.0.1", "test-agent", "POST", "/v1/responses", true)

	time.Sleep(5 * time.Millisecond)
	rlm.RecordFirstToken()
	firstDuration := rlm.GetDuration().Milliseconds()
	time.Sleep(5 * time.Millisecond)
	rlm.RecordFirstToken()

	details, _, err := tracker.QueryRequestDetailsWithHotPool(context.Background(), &tracking.QueryOptions{
		StartDate: func() *time.Time {
			tm := time.Now().Add(-time.Minute)
			return &tm
		}(),
		EndDate: func() *time.Time {
			tm := time.Now().Add(time.Minute)
			return &tm
		}(),
		Limit:  10,
		Offset: 0,
	})
	if err != nil {
		t.Fatalf("failed to query request details: %v", err)
	}
	if len(details) == 0 {
		t.Fatal("expected request details from hot pool")
	}

	var detail *tracking.RequestDetail
	for i := range details {
		if details[i].RequestID == "req-first-response-once" {
			detail = &details[i]
			break
		}
	}
	if detail == nil {
		t.Fatal("expected target request detail in hot pool results")
	}
	if detail.FirstTokenMs == nil {
		t.Fatal("expected first token ms to be recorded")
	}
	if *detail.FirstTokenMs < 0 {
		t.Fatalf("expected non-negative first token ms, got %d", *detail.FirstTokenMs)
	}
	if *detail.FirstTokenMs > firstDuration+2 {
		t.Fatalf("expected first token ms to keep the first recorded value, got %d > %d", *detail.FirstTokenMs, firstDuration+2)
	}
}

func TestRequestLifecycleManager_CompactPathRecordedInHotPool(t *testing.T) {
	tracker, err := tracking.NewUsageTracker(&tracking.Config{
		Enabled:         true,
		DatabasePath:    ":memory:",
		BufferSize:      10,
		BatchSize:       5,
		FlushInterval:   50 * time.Millisecond,
		MaxRetry:        3,
		CleanupInterval: 24 * time.Hour,
		RetentionDays:   30,
		HotPool: &tracking.HotPoolSettings{
			Enabled:          true,
			MaxAge:           30 * time.Minute,
			MaxSize:          1000,
			CleanupInterval:  time.Minute,
			ArchiveOnCleanup: true,
		},
	})
	if err != nil {
		t.Fatalf("failed to create usage tracker: %v", err)
	}
	defer tracker.Close()

	rlm := NewRequestLifecycleManager(tracker, nil, "req-compact-hot-pool", nil)
	rlm.StartRequest("127.0.0.1", "test-agent", "POST", "/v1/responses/compact", false)
	rlm.HandleNonTokenResponse(`{"output":[]}`)

	details, _, err := tracker.QueryRequestDetailsWithHotPool(context.Background(), &tracking.QueryOptions{
		StartDate: func() *time.Time {
			tm := time.Now().Add(-time.Minute)
			return &tm
		}(),
		EndDate: func() *time.Time {
			tm := time.Now().Add(time.Minute)
			return &tm
		}(),
		Limit:  10,
		Offset: 0,
	})
	if err != nil {
		t.Fatalf("failed to query request details: %v", err)
	}

	var detail *tracking.RequestDetail
	for i := range details {
		if details[i].RequestID == "req-compact-hot-pool" {
			detail = &details[i]
			break
		}
	}
	if detail == nil {
		t.Fatal("expected compact request detail in hot pool results")
	}
	if detail.Path != "/v1/responses/compact" {
		t.Fatalf("expected compact path to be recorded, got %s", detail.Path)
	}
	if detail.Status != "completed" {
		t.Fatalf("expected compact request to be completed, got %s", detail.Status)
	}
}

func TestRequestLifecycleManager_IsCompleted(t *testing.T) {
	requestID := "test-completed-505"
	rlm := NewRequestLifecycleManager(nil, nil, requestID, nil)

	// 初始状态不应该是完成
	if rlm.IsCompleted() {
		t.Error("Initial state should not be completed")
	}

	// 更新状态为处理中
	rlm.UpdateStatus("processing", 0, 200)
	if rlm.IsCompleted() {
		t.Error("Processing state should not be completed")
	}

	// 更新状态为完成
	rlm.UpdateStatus("completed", 0, 200)
	if !rlm.IsCompleted() {
		t.Error("Completed state should be completed")
	}
}

func TestRequestLifecycleManager_GetStats(t *testing.T) {
	requestID := "test-stats-606"
	rlm := NewRequestLifecycleManager(nil, nil, requestID, nil)

	// 设置一些状态
	rlm.SetEndpoint("stats-endpoint")
	rlm.SetModel("claude-3-haiku")
	rlm.UpdateStatus("processing", 2, 200)

	// 获取统计信息
	stats := rlm.GetStats()

	// 验证统计信息
	expectedFields := []string{"request_id", "endpoint", "model", "status", "retry_count", "duration_ms", "start_time"}
	for _, field := range expectedFields {
		if _, exists := stats[field]; !exists {
			t.Errorf("Stats should contain field: %s", field)
		}
	}

	if stats["request_id"] != requestID {
		t.Errorf("Expected request_id %s, got %v", requestID, stats["request_id"])
	}
	if stats["endpoint"] != "stats-endpoint" {
		t.Errorf("Expected endpoint 'stats-endpoint', got %v", stats["endpoint"])
	}
	if stats["model"] != "claude-3-haiku" {
		t.Errorf("Expected model 'claude-3-haiku', got %v", stats["model"])
	}
	if stats["status"] != "processing" {
		t.Errorf("Expected status 'processing', got %v", stats["status"])
	}
	if stats["retry_count"] != 0 {
		t.Errorf("次数参数不能改变联系上游计数，got %v", stats["retry_count"])
	}
}

func TestRequestLifecycleManager_FailRequest_LogsErrorDetail(t *testing.T) {
	var logBuffer bytes.Buffer
	originalLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuffer, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))
	defer slog.SetDefault(originalLogger)

	rlm := NewRequestLifecycleManager(nil, nil, "req-fail-log", nil)
	rlm.SetEndpoint("claude-endpoint")

	errorDetail := `status=429 endpoint=claude-endpoint request_id=req_upstream_123 body={"error":{"message":"quota exceeded"}}`
	rlm.FailRequest("rate_limited", errorDetail, http.StatusTooManyRequests)

	logs := logBuffer.String()
	if !strings.Contains(logs, "请求最终失败") {
		t.Fatalf("expected failure log to be written, got %q", logs)
	}

	expectedFragments := []string{
		"详情:",
		"status=429",
		"endpoint=claude-endpoint",
		"request_id=req_upstream_123",
		"quota exceeded",
	}
	for _, fragment := range expectedFragments {
		if !strings.Contains(logs, fragment) {
			t.Fatalf("expected failure log to contain fragment %q, got %q", fragment, logs)
		}
	}
}

func TestRequestLifecycleManager_AnalyzeResponseType(t *testing.T) {
	requestID := "test-analyze-707"
	rlm := NewRequestLifecycleManager(nil, nil, requestID, nil)

	testCases := []struct {
		response string
		expected string
	}{
		{"", "empty_response"},
		{"Internal Server Error occurred", "error_response"},
		{"ERROR: Invalid request", "error_response"},
		{`{"data":[{"id":"gpt-4","object":"model"}]}`, "models_list"},
		{`{"config":{"server":"1.0"}}`, "config_response"},
		{`{"version":"2.1.0"}`, "config_response"},
		{"Just some plain text", "non_token_response"},
		{"Normal API response without special markers", "non_token_response"},
	}

	for _, tc := range testCases {
		result := rlm.analyzeResponseType(tc.response)
		if result != tc.expected {
			t.Errorf("For response '%s', expected '%s', got '%s'",
				tc.response, tc.expected, result)
		}
	}
}

// TestRequestLifecycleManager_AttemptCounter 测试尝试计数器的核心功能（语义修复）
func TestRequestLifecycleManager_AttemptCounter(t *testing.T) {
	requestID := "test-attempt-808"
	rlm := NewRequestLifecycleManager(nil, nil, requestID, nil)

	// 验证初始状态：attemptCounter应该为0
	initialCount := rlm.GetAttemptCount()
	if initialCount != 0 {
		t.Errorf("Expected initial attempt count 0, got %d", initialCount)
	}

	// 测试第一次增加尝试计数
	count1 := rlm.IncrementAttempt()
	if count1 != 1 {
		t.Errorf("Expected first increment to return 1, got %d", count1)
	}

	// 验证GetAttemptCount返回正确值
	currentCount := rlm.GetAttemptCount()
	if currentCount != 1 {
		t.Errorf("Expected current attempt count 1, got %d", currentCount)
	}

	// 测试多次增加
	for i := 2; i <= 5; i++ {
		count := rlm.IncrementAttempt()
		if count != i {
			t.Errorf("Expected increment %d to return %d, got %d", i, i, count)
		}
	}

	// 验证最终计数
	finalCount := rlm.GetAttemptCount()
	if finalCount != 5 {
		t.Errorf("Expected final attempt count 5, got %d", finalCount)
	}
}

// TestRequestLifecycleManager_UpdateStatusWithAttemptCounter 测试UpdateStatus与尝试计数器的集成
func TestRequestLifecycleManager_UpdateStatusWithAttemptCounter(t *testing.T) {
	requestID := "test-update-attempt-909"
	rlm := NewRequestLifecycleManager(nil, nil, requestID, nil)

	// 设置端点信息
	rlm.SetEndpoint("test-endpoint")

	// 增加尝试计数器
	rlm.IncrementAttempt()
	rlm.IncrementAttempt()
	rlm.IncrementAttempt()

	// 次数参数 -1 不再使用旧内部计数器
	rlm.UpdateStatus("suspended", -1, 0)

	// 未联系上游时，旧计数器不影响重试数
	if rlm.lastStatus != "suspended" {
		t.Errorf("Expected status 'suspended', got '%s'", rlm.lastStatus)
	}
	if rlm.retryCount != 0 {
		t.Errorf("旧尝试计数器不能改变重试数，got %d", rlm.retryCount)
	}

	// 显式次数参数同样被忽略
	rlm.UpdateStatus("processing", 5, 200)
	if rlm.retryCount != 0 {
		t.Errorf("显式次数参数不能改变重试数，got %d", rlm.retryCount)
	}
}

// TestRequestLifecycleManager_AttemptCounterThreadSafety 测试尝试计数器的线程安全性
func TestRequestLifecycleManager_AttemptCounterThreadSafety(t *testing.T) {
	requestID := "test-threadsafe-1010"
	rlm := NewRequestLifecycleManager(nil, nil, requestID, nil)

	const numGoroutines = 100
	const incrementsPerGoroutine = 10

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	// 并发增加计数器
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < incrementsPerGoroutine; j++ {
				rlm.IncrementAttempt()
			}
		}()
	}

	wg.Wait()

	// 验证最终计数是否正确
	expectedCount := numGoroutines * incrementsPerGoroutine
	actualCount := rlm.GetAttemptCount()
	if actualCount != expectedCount {
		t.Errorf("Expected final count %d, got %d", expectedCount, actualCount)
	}
}

// TestRequestLifecycleManager_UpdateStatusSemanticFix 测试语义修复的完整流程
func TestRequestLifecycleManager_UpdateStatusSemanticFix(t *testing.T) {
	requestID := "test-semantic-1111"
	rlm := NewRequestLifecycleManager(nil, nil, requestID, nil)

	rlm.SetEndpoint("endpoint1")

	// 模拟流式请求的挂起逻辑
	rlm.IncrementAttempt() // 第一次尝试
	rlm.IncrementAttempt() // 第二次尝试
	rlm.IncrementAttempt() // 第三次尝试

	// 获取真实尝试次数
	actualAttemptCount := rlm.GetAttemptCount()
	if actualAttemptCount != 3 {
		t.Errorf("Expected 3 attempts, got %d", actualAttemptCount)
	}

	// 保留 -1 调用形式，验证旧计数器不再参与新口径
	rlm.UpdateStatus("suspended", -1, 0)

	// 没有取连接事件，重试数仍为0
	if rlm.GetRetryCount() != 0 {
		t.Errorf("没有联系上游时重试数应为0，got %d", rlm.GetRetryCount())
	}

	// 验证状态正确设置为suspended而不是retry
	if rlm.GetLastStatus() != "suspended" {
		t.Errorf("Expected status 'suspended', got '%s'", rlm.GetLastStatus())
	}
}

// TestRequestLifecycleManager_AttemptCounterIndependentOfRetryCount 测试尝试计数器与retryCount的独立性
func TestRequestLifecycleManager_AttemptCounterIndependentOfRetryCount(t *testing.T) {
	requestID := "test-independent-1212"
	rlm := NewRequestLifecycleManager(nil, nil, requestID, nil)

	// 增加尝试计数器
	rlm.IncrementAttempt()
	rlm.IncrementAttempt()

	// 模拟旧路径传入候选下标
	rlm.UpdateStatus("processing", 5, 200)

	// 验证attemptCounter和retryCount是独立的
	attemptCount := rlm.GetAttemptCount()
	retryCount := rlm.GetRetryCount()

	if attemptCount != 2 {
		t.Errorf("Expected attempt count 2, got %d", attemptCount)
	}
	if retryCount != 0 {
		t.Errorf("候选下标不能改变重试数，got %d", retryCount)
	}

	// 继续增加尝试计数器
	rlm.IncrementAttempt()

	// 验证attemptCounter增加了，但retryCount不变
	newAttemptCount := rlm.GetAttemptCount()
	sameRetryCount := rlm.GetRetryCount()

	if newAttemptCount != 3 {
		t.Errorf("Expected new attempt count 3, got %d", newAttemptCount)
	}
	if sameRetryCount != 0 {
		t.Errorf("重试数应保持0，got %d", sameRetryCount)
	}
}

// ===== CompleteRequestWithQuality 测试 =====
// 2025-12-11: 测试流完整性检测后的数据质量标记功能

func TestRequestLifecycleManager_CompleteRequestWithQuality_NoFailure(t *testing.T) {
	// 测试正常完成（无质量问题）
	requestID := "test-quality-ok-1"
	rlm := NewRequestLifecycleManager(nil, nil, requestID, nil)
	rlm.SetEndpoint("test-endpoint")
	rlm.SetModel("claude-sonnet-4-20250514")

	// 调用带空 failureReason 的完成方法
	rlm.CompleteRequestWithQuality(nil, "")

	// 验证状态为 completed
	if rlm.GetLastStatus() != "completed" {
		t.Errorf("期望状态 'completed'，但得到: %s", rlm.GetLastStatus())
	}

	t.Log("✅ 正常完成测试通过")
}

func TestRequestLifecycleManager_CompleteRequestWithQuality_IncompleteStream(t *testing.T) {
	// 测试不完整流的标记
	requestID := "test-quality-incomplete-2"
	rlm := NewRequestLifecycleManager(nil, nil, requestID, nil)
	rlm.SetEndpoint("test-endpoint")
	rlm.SetModel("claude-sonnet-4-20250514")

	// 调用带 incomplete_stream failureReason 的完成方法
	rlm.CompleteRequestWithQuality(nil, "incomplete_stream")

	// 验证状态为 completed（不是 failed）
	if rlm.GetLastStatus() != "completed" {
		t.Errorf("期望状态 'completed'，但得到: %s", rlm.GetLastStatus())
	}

	t.Log("✅ incomplete_stream 标记测试通过")
}

func TestRequestLifecycleManager_CompleteRequestWithQuality_StreamTruncated(t *testing.T) {
	// 测试流截断的标记
	requestID := "test-quality-truncated-3"
	rlm := NewRequestLifecycleManager(nil, nil, requestID, nil)
	rlm.SetEndpoint("test-endpoint")
	rlm.SetModel("claude-sonnet-4-20250514")

	// 调用带 stream_truncated failureReason 的完成方法
	rlm.CompleteRequestWithQuality(nil, "stream_truncated")

	// 验证状态为 completed
	if rlm.GetLastStatus() != "completed" {
		t.Errorf("期望状态 'completed'，但得到: %s", rlm.GetLastStatus())
	}

	t.Log("✅ stream_truncated 标记测试通过")
}

func TestRequestLifecycleManager_CompleteRequestWithQuality_WithTokens(t *testing.T) {
	// 测试带 Token 信息的质量标记
	requestID := "test-quality-tokens-4"
	rlm := NewRequestLifecycleManager(nil, nil, requestID, nil)
	rlm.SetEndpoint("test-endpoint")
	rlm.SetModel("claude-sonnet-4-20250514")

	tokens := &tracking.TokenUsage{
		InputTokens:  100,
		OutputTokens: 50,
	}

	// 调用带 Token 和 failureReason 的完成方法
	rlm.CompleteRequestWithQuality(tokens, "incomplete_stream")

	// 验证状态为 completed
	if rlm.GetLastStatus() != "completed" {
		t.Errorf("期望状态 'completed'，但得到: %s", rlm.GetLastStatus())
	}

	t.Log("✅ 带 Token 的质量标记测试通过")
}
