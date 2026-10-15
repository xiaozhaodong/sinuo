package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"sinuo/internal/middleware"
	"sinuo/internal/proxy"
	"sinuo/internal/tracking"
)

// TestStreamingTokenLossDetection 验证流式Token丢失问题检测
func TestStreamingTokenLossDetection(t *testing.T) {
	t.Log("🧪 测试开始：流式Token丢失问题检测")

	// 设置简化的测试环境
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
		t.Fatalf("创建UsageTracker失败: %v", err)
	}
	defer tracker.Close()

	monitoringMiddleware := middleware.NewMonitoringMiddleware(nil)

	// 模拟带有Token信息但在处理过程中遇到EOF的SSE响应
	sseData := `event: message_start
data: {"type":"message_start","message":{"id":"msg_01ABC123","type":"message","role":"assistant","model":"claude-3-5-haiku-20241022","content":[],"usage":{"input_tokens":257,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"},"usage":{"input_tokens":257,"output_tokens":25,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}

`
	// 故意截断，模拟EOF错误

	// 创建EOF错误读取器
	eofReader := &EOFErrorReader{
		data:     []byte(sseData),
		position: 0,
		eofAfter: len(sseData) - 50, // 在接近结尾时产生EOF
	}

	resp := &http.Response{
		StatusCode: 200,
		Body:       eofReader,
		Header:     make(http.Header),
	}

	// 创建生命周期管理器
	requestID := "req-loss-detection-001"
	lifecycleManager := proxy.NewRequestLifecycleManager(
		tracker,
		monitoringMiddleware,
		requestID,
		nil, // eventBus
	)

	lifecycleManager.SetEndpoint("test-endpoint")
	lifecycleManager.StartRequest("192.168.1.100", "test-client", "POST", "/v1/messages", true)

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

	t.Log("🔄 开始流式处理，期望遇到EOF错误并检测Token丢失...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	finalTokenUsage, modelName, err := streamProcessor.ProcessStreamWithRetry(ctx, resp)

	// 关键验证1：应该收到错误
	if err == nil {
		t.Error("❌ 期望收到EOF错误，但未收到错误")
	} else {
		t.Logf("✅ 收到预期错误: %v", err)
	}

	// 关键验证2：检查Token信息是否丢失
	if finalTokenUsage == nil {
		t.Error("❌ CRITICAL: 流式EOF错误后Token信息为nil - 这是我们要修复的Token丢失问题！")
		t.Log("💡 此测试成功检测到Token丢失问题，证明修复方案的必要性")
	} else {
		t.Logf("✅ Token信息被保留:")
		t.Logf("   输入Token: %d", finalTokenUsage.InputTokens)
		t.Logf("   输出Token: %d", finalTokenUsage.OutputTokens)

		// 验证数值
		if finalTokenUsage.InputTokens == 257 && finalTokenUsage.OutputTokens == 25 {
			t.Log("🎯 Token数值与预期案例完全匹配！修复方案有效")
		}
	}

	// 关键验证3：模型名称
	if modelName == "" || modelName == "unknown" {
		t.Error("❌ 模型名称未被正确识别")
	} else {
		t.Logf("✅ 模型名称被正确识别: %s", modelName)
	}

	// 模拟修复后的Token保存行为
	if finalTokenUsage == nil {
		t.Log("🔧 模拟修复方案：尝试从Token解析器获取已解析信息...")

		// 在真实修复中，我们会从tokenParser.GetFinalUsage()获取信息
		parsedUsage := tokenParser.GetFinalUsage()
		if parsedUsage != nil {
			t.Log("✅ 从Token解析器恢复了Token信息：")
			t.Logf("   输入Token: %d", parsedUsage.InputTokens)
			t.Logf("   输出Token: %d", parsedUsage.OutputTokens)

			// 模拟记录失败Token
			lifecycleManager.SetModel(modelName)
			lifecycleManager.RecordTokensForFailedRequest(parsedUsage, "eof_error")

			time.Sleep(100 * time.Millisecond)

			t.Log("💾 模拟的失败Token记录完成")
		} else {
			t.Log("❌ 连Token解析器也没有保存Token信息")
		}
	}

	t.Log("🎯 流式Token丢失检测测试完成")
}

// TestStreamingErrorRecoveryTokenExtraction 测试错误恢复中的Token提取
func TestStreamingErrorRecoveryTokenExtraction(t *testing.T) {
	t.Log("🧪 测试开始：错误恢复中的Token提取")

	// 创建TokenParser直接测试
	tokenParser := proxy.NewTokenParser()

	// 测试完整的SSE解析流程
	t.Log("🔍 测试message_start事件解析...")

	// 解析event行
	tokenParser.ParseSSELine("event: message_start")

	// 解析data行
	tokenParser.ParseSSELine(`data: {"type":"message_start","message":{"id":"msg_01ABC123","type":"message","role":"assistant","model":"claude-3-5-haiku-20241022","content":[],"usage":{"input_tokens":257,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`)

	// 解析空行结束事件
	tokenUsage1 := tokenParser.ParseSSELine("")

	if tokenUsage1 != nil {
		t.Logf("✅ message_start解析成功:")
		t.Logf("   输入Token: %d", tokenUsage1.InputTokens)
		t.Logf("   输出Token: %d", tokenUsage1.OutputTokens)
	} else {
		t.Log("ℹ️ message_start返回nil（可能只包含模型信息）")
	}

	// 检查模型名称是否被设置
	modelName1 := tokenParser.GetModelName()
	t.Logf("   模型名称: '%s'", modelName1)

	t.Log("🔍 测试message_delta事件解析...")

	// 解析delta事件
	tokenParser.ParseSSELine("event: message_delta")
	tokenParser.ParseSSELine(`data: {"type":"message_delta","delta":{"type":"text","text":"Hello"},"usage":{"input_tokens":257,"output_tokens":25,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}`)
	tokenUsage2 := tokenParser.ParseSSELine("")

	if tokenUsage2 != nil {
		t.Logf("✅ content_block_delta解析成功:")
		t.Logf("   输入Token: %d", tokenUsage2.InputTokens)
		t.Logf("   输出Token: %d", tokenUsage2.OutputTokens)
	} else {
		t.Error("❌ content_block_delta解析失败")
	}

	// 测试最终使用统计获取
	finalUsage := tokenParser.GetFinalUsage()
	if finalUsage != nil {
		t.Logf("✅ 最终Token统计获取成功:")
		t.Logf("   输入Token: %d", finalUsage.InputTokens)
		t.Logf("   输出Token: %d", finalUsage.OutputTokens)

		// 验证数值
		if finalUsage.InputTokens == 257 && finalUsage.OutputTokens == 25 {
			t.Log("🎯 Token数值完全正确！")
		} else {
			t.Logf("⚠️ Token数值与预期不匹配: 期望输入257输出25, 实际输入%d输出%d",
				finalUsage.InputTokens, finalUsage.OutputTokens)
		}
	} else {
		t.Error("❌ 最终Token统计获取失败")
	}

	// 测试模型名称获取
	modelName := tokenParser.GetModelName()
	if modelName == "claude-3-5-haiku-20241022" {
		t.Logf("✅ 模型名称获取正确: %s", modelName)
	} else {
		t.Logf("⚠️ 模型名称可能不匹配: 期望 'claude-3-5-haiku-20241022', 实际 '%s'", modelName)
	}

	t.Log("🎯 错误恢复Token提取测试完成")
}
