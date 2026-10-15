package integration

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"sinuo/internal/middleware"
	"sinuo/internal/proxy"
	"sinuo/internal/tracking"
)

// MissingNewlineReader 模拟在 message_delta 后直接 EOF，不发送终止空行的场景
type MissingNewlineReader struct {
	data     []byte
	position int
}

func (r *MissingNewlineReader) Read(p []byte) (n int, err error) {
	if r.position >= len(r.data) {
		return 0, io.EOF
	}

	n = copy(p, r.data[r.position:])
	r.position += n

	if r.position >= len(r.data) {
		return n, io.EOF
	}

	return n, nil
}

func (r *MissingNewlineReader) Close() error {
	return nil
}

// TestStreamingMissingNewlineFlush 测试缺少终止空行时的 flush 机制
func TestStreamingMissingNewlineFlush(t *testing.T) {
	t.Log("🧪 测试开始：message_delta 后缺少终止空行的 flush 修复")

	// 设置测试环境
	trackerConfig := &tracking.Config{
		Enabled:       true,
		DatabasePath:  ":memory:",
		BufferSize:    50,
		BatchSize:     5,
		FlushInterval: 50 * time.Millisecond,
		MaxRetry:      2,
		DefaultPricing: tracking.ModelPricing{
			Input:  2.0,
			Output: 10.0,
		},
	}

	tracker, err := tracking.NewUsageTracker(trackerConfig)
	if err != nil {
		t.Fatalf("创建 UsageTracker 失败: %v", err)
	}
	defer tracker.Close()

	monitoringMiddleware := middleware.NewMonitoringMiddleware(nil)

	// SSE 数据：包含完整的 usage 信息，但在 message_delta 后直接断流，没有终止空行
	sseData := `event: message_start
data: {"type":"message_start","message":{"id":"msg_test123","type":"message","role":"assistant","model":"claude-3-5-sonnet-20241022","content":[],"usage":{"input_tokens":100,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}

event: message_delta
data: {"type":"message_delta","delta":{"type":"text","text":"Hello"},"usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":20,"cache_read_input_tokens":10}}`
	// 注意：这里故意不加终止空行，直接结束

	reader := &MissingNewlineReader{
		data:     []byte(sseData),
		position: 0,
	}

	resp := &http.Response{
		StatusCode: 200,
		Body:       reader,
		Header:     make(http.Header),
	}

	// 创建生命周期管理器
	requestID := "req-missing-newline-001"
	lifecycleManager := proxy.NewRequestLifecycleManager(
		tracker,
		monitoringMiddleware,
		requestID,
		nil, // eventBus 在测试中不需要
	)

	// lifecycleManager.SetEndpoint("test-endpoint", "test-group")
	// lifecycleManager.StartRequest("192.168.1.100", "test-client", "POST", "/v1/messages", true)
	_ = lifecycleManager // 忽略未使用警告

	// 创建流处理器
	recorder := httptest.NewRecorder()
	flusher := &mockFlusher{}

	tokenParser := proxy.NewTokenParser()
	streamProcessor := proxy.NewStreamProcessor(
		tokenParser,
		tracker,
		recorder,
		flusher,
		requestID,
		"test-endpoint",
	)

	t.Log("🔄 开始流式处理，模拟缺少终止空行的场景...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	finalTokenUsage, modelName, err := streamProcessor.ProcessStreamWithRetry(ctx, resp)

	// 验证1：应该正常完成（EOF 作为流结束标志）
	if err != nil {
		t.Logf("ℹ️ 收到错误（可能是预期的 EOF）: %v", err)
	}

	// 验证2：关键验证 - Token 信息必须被正确捕获
	if finalTokenUsage == nil {
		t.Error("❌ FAIL: 缺少终止空行导致 Token 信息丢失 - flush 机制未生效！")
		t.Log("💡 这表明 flushPendingEvent 未被正确调用或未正常工作")
	} else {
		t.Log("✅ PASS: Token 信息被正确保留（flush 机制生效）")
		t.Logf("   输入 Token: %d (期望: 100)", finalTokenUsage.InputTokens)
		t.Logf("   输出 Token: %d (期望: 50)", finalTokenUsage.OutputTokens)
		t.Logf("   缓存创建: %d (期望: 20)", finalTokenUsage.CacheCreationTokens)
		t.Logf("   缓存读取: %d (期望: 10)", finalTokenUsage.CacheReadTokens)

		// 验证数值精确性
		if finalTokenUsage.InputTokens != 100 {
			t.Errorf("❌ 输入 Token 不匹配: 期望 100, 实际 %d", finalTokenUsage.InputTokens)
		}
		if finalTokenUsage.OutputTokens != 50 {
			t.Errorf("❌ 输出 Token 不匹配: 期望 50, 实际 %d", finalTokenUsage.OutputTokens)
		}
		if finalTokenUsage.CacheCreationTokens != 20 {
			t.Errorf("❌ 缓存创建 Token 不匹配: 期望 20, 实际 %d", finalTokenUsage.CacheCreationTokens)
		}
		if finalTokenUsage.CacheReadTokens != 10 {
			t.Errorf("❌ 缓存读取 Token 不匹配: 期望 10, 实际 %d", finalTokenUsage.CacheReadTokens)
		}

		if finalTokenUsage.InputTokens == 100 &&
			finalTokenUsage.OutputTokens == 50 &&
			finalTokenUsage.CacheCreationTokens == 20 &&
			finalTokenUsage.CacheReadTokens == 10 {
			t.Log("🎯 所有 Token 数值完全匹配！flush 修复方案成功")
		}
	}

	// 验证3：模型名称
	if modelName != "claude-3-5-sonnet-20241022" {
		t.Errorf("❌ 模型名称不匹配: 期望 'claude-3-5-sonnet-20241022', 实际 '%s'", modelName)
	} else {
		t.Logf("✅ 模型名称正确: %s", modelName)
	}

	// 验证4：确认 empty_response 不会出现
	if finalTokenUsage != nil {
		hasTokens := finalTokenUsage.InputTokens > 0 || finalTokenUsage.OutputTokens > 0
		if hasTokens {
			t.Log("✅ 有真实 Token，不会被误判为 empty_response")
		}
	}

	t.Log("🎯 缺少终止空行的 flush 修复测试完成")
}

// TestTokenParserFlushMethod 直接测试 TokenParser 的 flushPendingEvent 方法
func TestTokenParserFlushMethod(t *testing.T) {
	t.Log("🧪 测试开始：TokenParser.flushPendingEvent() 方法单元测试")

	tokenParser := proxy.NewTokenParserWithRequestID("req-flush-test")

	// 场景1：解析 message_delta，但不发送终止空行
	t.Log("📝 场景1：模拟缺少终止空行的 message_delta 事件")

	// 解析 event 行
	result1 := tokenParser.ParseSSELineV2("event: message_delta")
	if result1 != nil {
		t.Error("❌ event 行不应返回结果")
	}

	// 解析 data 行（包含 usage）
	result2 := tokenParser.ParseSSELineV2(`data: {"type":"message_delta","delta":{"type":"text","text":"Test"},"usage":{"input_tokens":200,"output_tokens":100,"cache_creation_input_tokens":30,"cache_read_input_tokens":15}}`)
	if result2 != nil {
		t.Error("❌ data 行不应返回结果（等待空行）")
	}

	// 此时应该没有空行触发解析，eventBuffer 中有数据
	// 现在调用 flushPendingEvent 强制解析
	t.Log("🔄 调用 flushPendingEvent 强制解析...")
	flushResult := tokenParser.FlushPendingEvent()

	if flushResult == nil {
		t.Error("❌ FAIL: flushPendingEvent 返回 nil，未能解析缓存的事件")
	} else {
		t.Log("✅ PASS: flushPendingEvent 成功返回解析结果")

		if flushResult.TokenUsage == nil {
			t.Error("❌ Token 使用信息为 nil")
		} else {
			t.Logf("   输入 Token: %d (期望: 200)", flushResult.TokenUsage.InputTokens)
			t.Logf("   输出 Token: %d (期望: 100)", flushResult.TokenUsage.OutputTokens)
			t.Logf("   缓存创建: %d (期望: 30)", flushResult.TokenUsage.CacheCreationTokens)
			t.Logf("   缓存读取: %d (期望: 15)", flushResult.TokenUsage.CacheReadTokens)

			if flushResult.TokenUsage.InputTokens != 200 ||
				flushResult.TokenUsage.OutputTokens != 100 ||
				flushResult.TokenUsage.CacheCreationTokens != 30 ||
				flushResult.TokenUsage.CacheReadTokens != 15 {
				t.Error("❌ Token 数值不匹配")
			} else {
				t.Log("🎯 所有 Token 数值完全正确！")
			}
		}
	}

	// 场景2：测试没有待处理事件时的行为
	t.Log("📝 场景2：没有待处理事件时调用 flush")
	tokenParser.Reset()
	emptyFlushResult := tokenParser.FlushPendingEvent()
	if emptyFlushResult != nil {
		t.Error("❌ 没有待处理事件时应返回 nil")
	} else {
		t.Log("✅ 正确返回 nil")
	}

	t.Log("🎯 TokenParser.flushPendingEvent() 单元测试完成")
}
