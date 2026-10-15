package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sinuo/internal/tracking"
)

// TestStreamProcessorTokenIntegration 测试流式处理器与Token解析的集成
func TestStreamProcessorTokenIntegration(t *testing.T) {
	// 模拟带有token信息的SSE响应
	sseData := `event: message_start
data: {"type":"message_start","message":{"id":"msg_01ABC123","type":"message","role":"assistant","model":"claude-3-5-haiku-20241022","content":[],"usage":{"input_tokens":25,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}

event: message_delta
data: {"type":"message_delta","delta":{"type":"text","text":"Hello"},"usage":{"input_tokens":25,"output_tokens":97,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}

event: message_stop
data: {"type":"message_stop"}

`

	// 创建模拟的HTTP响应
	recorder := httptest.NewRecorder()
	flusher := &streamMockFlusher{}

	// 创建TokenParser和StreamProcessor (不使用usageTracker，专注于token解析)
	tokenParser := NewTokenParser()

	processor := NewStreamProcessor(tokenParser, nil, recorder, flusher, "test-stream-123", "test-endpoint")

	// 创建模拟的HTTP响应
	resp := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(sseData)),
		Header:     make(http.Header),
	}

	t.Logf("🧪 测试开始：流式处理器Token解析集成测试")

	// 先手动测试TokenParser
	t.Logf("🔍 手动测试TokenParser解析能力")

	// 测试message_start事件
	tokenParser.ParseSSELine("event: message_start")
	tokenParser.ParseSSELine("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_01ABC123\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-3-5-haiku-20241022\",\"content\":[],\"usage\":{\"input_tokens\":25,\"output_tokens\":0,\"cache_creation_input_tokens\":0,\"cache_read_input_tokens\":0}}}")
	tokenParser.ParseSSELine("") // 空行结束事件

	modelAfterStart := tokenParser.GetModelName()
	t.Logf("   message_start后的模型: '%s'", modelAfterStart)

	// 测试message_delta事件
	usage1 := tokenParser.ParseSSELine("event: message_delta")
	usage2 := tokenParser.ParseSSELine("data: {\"type\":\"message_delta\",\"delta\":{\"type\":\"text\",\"text\":\"Hello\"},\"usage\":{\"input_tokens\":25,\"output_tokens\":97,\"cache_creation_input_tokens\":0,\"cache_read_input_tokens\":0}}")
	usage3 := tokenParser.ParseSSELine("") // 空行结束事件

	t.Logf("   message_delta解析结果: %v, %v, %v", usage1, usage2, usage3)

	finalAfterDelta := tokenParser.GetFinalUsage()
	t.Logf("   message_delta后的token: %v", finalAfterDelta)

	// 执行流式处理
	_, err := processor.ProcessStream(context.Background(), resp)
	if err != nil {
		t.Fatalf("ProcessStream failed: %v", err)
	}

	// 验证模型信息是否被正确提取
	modelName := tokenParser.GetModelName()
	if modelName != "claude-3-5-haiku-20241022" {
		t.Errorf("❌ 模型名称错误: 期望 'claude-3-5-haiku-20241022', 实际 '%s'", modelName)
	} else {
		t.Logf("✅ 模型名称提取正确: %s", modelName)
	}

	// 验证token使用统计是否被正确提取
	finalUsage := tokenParser.GetFinalUsage()
	if finalUsage == nil {
		t.Errorf("❌ 未能提取token使用统计")
	} else {
		t.Logf("✅ Token使用统计提取成功:")
		t.Logf("   输入Token: %d", finalUsage.InputTokens)
		t.Logf("   输出Token: %d", finalUsage.OutputTokens)
		t.Logf("   缓存创建Token: %d", finalUsage.CacheCreationTokens)
		t.Logf("   缓存读取Token: %d", finalUsage.CacheReadTokens)

		// 验证具体数值
		if finalUsage.InputTokens != 25 {
			t.Errorf("❌ 输入Token数量错误: 期望 25, 实际 %d", finalUsage.InputTokens)
		}
		if finalUsage.OutputTokens != 97 {
			t.Errorf("❌ 输出Token数量错误: 期望 97, 实际 %d", finalUsage.OutputTokens)
		}
	}
}

// TestStreamProcessorWithUsageTracker 测试流式处理器与usage tracker的集成
func TestStreamProcessorWithUsageTracker(t *testing.T) {
	// 模拟带有token信息的SSE响应
	sseData := `event: message_start
data: {"type":"message_start","message":{"id":"msg_01ABC123","type":"message","role":"assistant","model":"claude-3-5-haiku-20241022","content":[],"usage":{"input_tokens":25,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}

event: message_delta
data: {"type":"message_delta","delta":{"type":"text","text":"Hello"},"usage":{"input_tokens":25,"output_tokens":97,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}

`

	// 创建模拟的HTTP响应
	recorder := httptest.NewRecorder()
	flusher := &streamMockFlusher{}

	// 创建TokenParser
	tokenParser := NewTokenParser()

	// 创建一个适配器来满足interface
	processor := NewStreamProcessor(tokenParser, nil, recorder, flusher, "test-stream-456", "test-endpoint")

	// 手动设置usageTracker (模拟)
	processor.usageTracker = &tracking.UsageTracker{} // 实际使用中这会是真实的tracker

	// 创建模拟的HTTP响应
	resp := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(sseData)),
		Header:     make(http.Header),
	}

	t.Logf("🧪 测试开始：流式处理器与UsageTracker集成测试")

	// 执行流式处理
	_, err := processor.ProcessStream(context.Background(), resp)
	if err != nil {
		t.Fatalf("ProcessStream failed: %v", err)
	}

	t.Logf("✅ 流式处理完成，测试通过")
}

// streamMockFlusher 模拟HTTP Flusher
type streamMockFlusher struct{}

func (f *streamMockFlusher) Flush() {
	// Mock implementation - do nothing
}

// streamMockUsageTracker 模拟使用跟踪器
type streamMockUsageTracker struct {
	logs []string
}

func (m *streamMockUsageTracker) RecordRequestStart(requestID, clientIP, userAgent, method, path string, isStreaming bool) {
	m.logs = append(m.logs, "RecordRequestStart: "+requestID)
}

func (m *streamMockUsageTracker) RecordRequestUpdate(requestID, endpoint, group, status string, retryCount, httpStatus int) {
	m.logs = append(m.logs, "RecordRequestUpdate: "+requestID+" - "+status)
}

func (m *streamMockUsageTracker) RecordRequestComplete(requestID, modelName string, tokens *tracking.TokenUsage, duration int64) {
	m.logs = append(m.logs, "RecordRequestComplete: "+requestID+" - "+modelName)
}

func (m *streamMockUsageTracker) IsRunning() bool { return true }
func (m *streamMockUsageTracker) Start()          {}
func (m *streamMockUsageTracker) Stop()           {}
func (m *streamMockUsageTracker) GetStats(ctx context.Context, startTime, endTime string, modelName, endpointName, groupName string) (map[string]interface{}, error) {
	return nil, nil
}
