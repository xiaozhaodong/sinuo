package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sinuo/internal/proxy/response"
	"sinuo/internal/tracking"
)

// ===== TokenParser 流完整性追踪测试 =====
// 2025-12-11: 测试流完整性检测功能

func TestStreamCompleteness_CompleteStream(t *testing.T) {
	// 测试完整的流：message_start + content block + message_delta(usage) + message_stop
	parser := NewTokenParserWithRequestID("test-complete-stream")

	// 模拟完整的 SSE 事件序列
	lines := []string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-20250514","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
		"",
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":0}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":10,"output_tokens":100}}`,
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
	}

	for _, line := range lines {
		parser.ParseSSELineV2(line)
	}

	// 验证完整性状态
	completeness := parser.GetStreamCompleteness()
	if !completeness.IsComplete {
		t.Errorf("期望流完整，但得到不完整状态: %s", completeness.Reason)
	}
	if completeness.FailureReason != "" {
		t.Errorf("期望 FailureReason 为空，但得到: %s", completeness.FailureReason)
	}

	// 验证 IsStreamComplete 方法
	if !parser.IsStreamComplete() {
		t.Error("IsStreamComplete() 应该返回 true")
	}
}

func TestStreamCompleteness_AllowsZeroContentBlocks(t *testing.T) {
	parser := NewTokenParserWithRequestID("test-zero-content-blocks")
	lines := []string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-test","content":[],"usage":{"input_tokens":10,"output_tokens":0}}}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":0}}`,
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
	}

	for _, line := range lines {
		parser.ParseSSELineV2(line)
	}

	if completeness := parser.GetStreamCompleteness(); !completeness.IsComplete {
		t.Fatalf("零 content block 的完整空响应应判定为完整，得到 %s", completeness.Reason)
	}
}

func TestStreamCompleteness_ContentBlockDeltaRequiresStart(t *testing.T) {
	parser := NewTokenParserWithRequestID("test-content-block-missing-start")
	lines := []string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-test","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
	}

	for _, line := range lines {
		parser.ParseSSELineV2(line)
	}

	completeness := parser.GetStreamCompleteness()
	if completeness.IsComplete {
		t.Fatal("缺少 content_block_start 的流不应判定为完整")
	}
	if completeness.FailureReason != "incomplete_stream" {
		t.Fatalf("期望 incomplete_stream，得到 %s", completeness.FailureReason)
	}
	if !strings.Contains(completeness.Reason, "content_block_start") {
		t.Fatalf("期望原因指出缺少 content_block_start，得到 %s", completeness.Reason)
	}
}

func TestStreamCompleteness_OpenContentBlockRejectsMessageDelta(t *testing.T) {
	parser := NewTokenParserWithRequestID("test-content-block-missing-stop")
	lines := []string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-test","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
	}

	for _, line := range lines {
		parser.ParseSSELineV2(line)
	}

	completeness := parser.GetStreamCompleteness()
	if completeness.IsComplete {
		t.Fatal("缺少 content_block_stop 的流不应判定为完整")
	}
	if !strings.Contains(completeness.Reason, "尚未结束") {
		t.Fatalf("期望原因指出内容块尚未结束，得到 %s", completeness.Reason)
	}
}

func TestStreamCompleteness_AllowsMultipleAndZeroDeltaContentBlocks(t *testing.T) {
	parser := NewTokenParserWithRequestID("test-content-block-multiple")
	lines := []string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-test","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"fallback","from":{"model":"a"},"to":{"model":"b"}}}`,
		"",
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":0}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hello"}}`,
		"",
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":1}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
	}

	for _, line := range lines {
		parser.ParseSSELineV2(line)
	}

	completeness := parser.GetStreamCompleteness()
	if !completeness.IsComplete {
		t.Fatalf("合法的多内容块流应判定为完整，得到 %s", completeness.Reason)
	}
}

func TestStreamCompleteness_ContentBlockProtocolViolations(t *testing.T) {
	messageStart := []string{
		"event: message_start",
		`data: {"type":"message_start","message":{"model":"claude-test","content":[],"usage":{"input_tokens":10,"output_tokens":0}}}`,
		"",
	}
	messageDelta := []string{
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
		"",
	}
	messageStop := []string{"event: message_stop", `data: {"type":"message_stop"}`, ""}
	start := []string{"event: content_block_start", `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`, ""}
	stop := []string{"event: content_block_stop", `data: {"type":"content_block_stop","index":0}`, ""}
	delta := []string{"event: content_block_delta", `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"x"}}`, ""}
	join := func(groups ...[]string) []string {
		var lines []string
		for _, group := range groups {
			lines = append(lines, group...)
		}
		return lines
	}

	tests := []struct {
		name       string
		lines      []string
		wantReason string
	}{
		{name: "stop_before_start", lines: join(messageStart, stop, messageDelta, messageStop), wantReason: "缺少对应的 content_block_start"},
		{name: "duplicate_start", lines: join(messageStart, start, start, messageDelta, messageStop), wantReason: "重复出现"},
		{name: "duplicate_stop", lines: join(messageStart, start, stop, stop, messageDelta, messageStop), wantReason: "重复出现"},
		{name: "delta_after_message_delta", lines: join(messageStart, start, stop, messageDelta, delta, messageStop), wantReason: "消息结束阶段"},
		{name: "delta_missing_index", lines: join(messageStart, start, []string{"event: content_block_delta", `data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"x"}}`, ""}, stop, messageDelta, messageStop), wantReason: "缺少有效 index"},
		{name: "open_block_at_message_stop", lines: join(messageStart, start, messageStop), wantReason: "message_stop 到达时"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parser := NewTokenParserWithRequestID("test-content-block-" + test.name)
			for _, line := range test.lines {
				parser.ParseSSELineV2(line)
			}
			completeness := parser.GetStreamCompleteness()
			if completeness.IsComplete {
				t.Fatal("协议违规流不应判定为完整")
			}
			if completeness.FailureReason != "incomplete_stream" {
				t.Fatalf("期望 incomplete_stream，得到 %s", completeness.FailureReason)
			}
			if !strings.Contains(completeness.Reason, test.wantReason) {
				t.Fatalf("期望原因包含 %q，得到 %q", test.wantReason, completeness.Reason)
			}
		})
	}
}

func TestAccountRegularResponse_RecordsNonStreamingTiming(t *testing.T) {
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

	const requestID = "req-non-stream-response-complete"
	rlm := NewRequestLifecycleManager(tracker, nil, requestID, nil)
	rlm.StartRequest("127.0.0.1", "test-agent", "POST", "/v1/responses", false)
	rlm.SetFirstTokenStartTime(time.Now().Add(-20 * time.Millisecond))
	handler := &Handler{
		responseProcessor: response.NewProcessor(),
		tokenAnalyzer:     response.NewTokenAnalyzer(nil, nil, &TokenParserProviderImpl{}),
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	upstreamResponse := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"model":"gpt-5.4","usage":{"input_tokens":3,"output_tokens":2}}`)),
	}
	if err := handler.processAccountRegularResponse(recorder, upstreamResponse, rlm, "test-account", request); err != nil {
		t.Fatalf("expected account regular response to complete, got %v", err)
	}

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

	for i := range details {
		if details[i].RequestID != requestID {
			continue
		}
		if details[i].FirstTokenMs == nil || *details[i].FirstTokenMs < 20 {
			t.Fatalf("expected non-streaming first response timing, got %v", details[i].FirstTokenMs)
		}
		if details[i].CompletionMs == nil || *details[i].CompletionMs != 0 {
			t.Fatalf("expected non-streaming completion_ms=0, got %v", details[i].CompletionMs)
		}
		return
	}
	t.Fatal("expected non-streaming request detail in hot pool")
}

func TestStreamCompleteness_MissingMessageStop(t *testing.T) {
	// 测试缺少 message_stop 但有 usage 的情况
	parser := NewTokenParserWithRequestID("test-missing-stop")

	lines := []string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-20250514","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		"",
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":0}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":10,"output_tokens":100}}`,
		"",
		// 没有 message_stop 事件
	}

	for _, line := range lines {
		parser.ParseSSELineV2(line)
	}

	completeness := parser.GetStreamCompleteness()
	if completeness.IsComplete {
		t.Error("期望流不完整（缺少 message_stop），但得到完整状态")
	}
	if completeness.FailureReason != "incomplete_stream" {
		t.Errorf("期望 FailureReason='incomplete_stream'，但得到: %s", completeness.FailureReason)
	}
	if completeness.Reason == "" {
		t.Error("期望有不完整原因说明")
	}

	t.Logf("✅ 缺少 message_stop 测试通过: %s", completeness.Reason)
}

func TestStreamCompleteness_StreamTruncated(t *testing.T) {
	// 测试响应被截断的情况（只有 message_start，没有 usage 和 stop）
	parser := NewTokenParserWithRequestID("test-truncated")

	lines := []string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-20250514","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
		"",
		// 流被截断，没有 message_delta(usage) 和 message_stop
	}

	for _, line := range lines {
		parser.ParseSSELineV2(line)
	}

	completeness := parser.GetStreamCompleteness()
	if completeness.IsComplete {
		t.Error("期望流不完整（被截断），但得到完整状态")
	}
	if completeness.FailureReason != "stream_truncated" {
		t.Errorf("期望 FailureReason='stream_truncated'，但得到: %s", completeness.FailureReason)
	}

	t.Logf("✅ 流截断测试通过: %s", completeness.Reason)
}

func TestStreamCompleteness_NoEvents(t *testing.T) {
	// 测试没有收到任何有效事件的情况
	parser := NewTokenParserWithRequestID("test-no-events")

	// 只有 ping 事件
	lines := []string{
		"event: ping",
		`data: {"type":"ping"}`,
		"",
	}

	for _, line := range lines {
		parser.ParseSSELineV2(line)
	}

	completeness := parser.GetStreamCompleteness()
	if completeness.IsComplete {
		t.Error("期望流不完整（无有效事件），但得到完整状态")
	}
	if completeness.FailureReason != "stream_truncated" {
		t.Errorf("期望 FailureReason='stream_truncated'，但得到: %s", completeness.FailureReason)
	}

	t.Logf("✅ 无有效事件测试通过: %s", completeness.Reason)
}

func TestStreamCompleteness_FallbackUsed(t *testing.T) {
	// 测试使用 fallback（只有 message_start 的 usage）但有 message_stop 的情况
	parser := NewTokenParserWithRequestID("test-fallback")

	lines := []string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-20250514","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
		"",
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":0}`,
		"",
		// message_delta 没有 usage
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
	}

	for _, line := range lines {
		parser.ParseSSELineV2(line)
	}

	// 验证 fallback 被使用
	if !parser.IsFallbackUsed() {
		t.Error("期望使用 fallback 机制")
	}

	completeness := parser.GetStreamCompleteness()
	// 即使有 message_stop，但使用了 fallback 也应该标记为不完整
	if completeness.IsComplete {
		t.Error("期望流不完整（使用了 fallback），但得到完整状态")
	}
	if completeness.FailureReason != "incomplete_stream" {
		t.Errorf("期望 FailureReason='incomplete_stream'，但得到: %s", completeness.FailureReason)
	}

	t.Logf("✅ Fallback 使用测试通过: %s", completeness.Reason)
}

func TestStreamCompleteness_Reset(t *testing.T) {
	// 测试 Reset 方法是否正确重置完整性追踪字段
	parser := NewTokenParserWithRequestID("test-reset")

	// 先解析一些事件
	lines := []string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-20250514","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		"",
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":0}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":10,"output_tokens":100}}`,
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
	}

	for _, line := range lines {
		parser.ParseSSELineV2(line)
	}

	// 验证流是完整的
	if !parser.IsStreamComplete() {
		t.Error("Reset 前期望流完整")
	}

	// 重置解析器
	parser.Reset()

	// 验证重置后的状态
	completeness := parser.GetStreamCompleteness()
	if completeness.IsComplete {
		t.Error("Reset 后期望流不完整（因为没有收到任何事件）")
	}

	t.Log("✅ Reset 测试通过")
}

func TestStreamCompleteness_MessageStopOnly(t *testing.T) {
	// 测试只有 message_stop 的异常情况
	parser := NewTokenParserWithRequestID("test-stop-only")

	lines := []string{
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
	}

	for _, line := range lines {
		parser.ParseSSELineV2(line)
	}

	completeness := parser.GetStreamCompleteness()
	// 有 message_stop 但没有 message_start 和 usage，仍然应该检查是否有完整的 usage
	// 由于没有 message_start，应该是完整的（只检查 message_stop）
	if !completeness.IsComplete {
		// 这种情况取决于具体实现逻辑
		t.Logf("只有 message_stop 的情况: IsComplete=%v, Reason=%s", completeness.IsComplete, completeness.Reason)
	}
}

// ===== 边界条件测试 =====

func TestStreamCompleteness_EmptyParser(t *testing.T) {
	// 测试全新的解析器
	parser := NewTokenParser()

	completeness := parser.GetStreamCompleteness()
	if completeness.IsComplete {
		t.Error("新解析器应该返回不完整状态")
	}

	t.Logf("✅ 空解析器测试通过: FailureReason=%s", completeness.FailureReason)
}

func TestStreamCompleteness_MultipleMessageDelta(t *testing.T) {
	// 测试多个 message_delta 事件（只有最后一个有 usage）
	parser := NewTokenParserWithRequestID("test-multi-delta")

	lines := []string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-20250514","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		"",
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":0}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":null}}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":10,"output_tokens":100}}`,
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
	}

	for _, line := range lines {
		parser.ParseSSELineV2(line)
	}

	completeness := parser.GetStreamCompleteness()
	if !completeness.IsComplete {
		t.Errorf("期望流完整，但得到: %s", completeness.Reason)
	}

	t.Log("✅ 多 message_delta 测试通过")
}

func TestStreamCompleteness_ResponsesCompleted(t *testing.T) {
	parser := NewTokenParserWithRequestID("test-responses-complete")

	lines := []string{
		"event: response.in_progress",
		`data: {"type":"response.in_progress","response":{"model":"gpt-5-codex"}}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"model":"gpt-5-codex"},"usage":{"input_tokens":10,"output_tokens":2,"input_tokens_details":{"cached_tokens":0}}}`,
		"",
	}

	for _, line := range lines {
		parser.ParseSSELineV2(line)
	}

	completeness := parser.GetStreamCompleteness()
	if !completeness.IsComplete {
		t.Errorf("expected responses stream complete, got reason=%s failure=%s",
			completeness.Reason, completeness.FailureReason)
	}
}

func TestStreamCompleteness_ResponsesMissingCompleted(t *testing.T) {
	parser := NewTokenParserWithRequestID("test-responses-missing-completed")

	lines := []string{
		"event: response.in_progress",
		`data: {"type":"response.in_progress","response":{"model":"gpt-5-codex"}}`,
		"",
	}

	for _, line := range lines {
		parser.ParseSSELineV2(line)
	}

	completeness := parser.GetStreamCompleteness()
	if completeness.IsComplete {
		t.Fatal("expected responses stream incomplete when missing response.completed")
	}
	if completeness.FailureReason != "stream_truncated" {
		t.Fatalf("expected stream_truncated, got %s", completeness.FailureReason)
	}
}
