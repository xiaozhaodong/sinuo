package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"sinuo/internal/tracking"
)

// mockResponseWriter 实现 http.ResponseWriter 和 http.Flusher
type mockResponseWriter struct {
	buffer  bytes.Buffer
	headers http.Header
	status  int
	flushed int
}

func (m *mockResponseWriter) Header() http.Header {
	if m.headers == nil {
		m.headers = make(http.Header)
	}
	return m.headers
}

func (m *mockResponseWriter) Write(data []byte) (int, error) {
	return m.buffer.Write(data)
}

func (m *mockResponseWriter) WriteHeader(statusCode int) {
	m.status = statusCode
}

func (m *mockResponseWriter) Flush() {
	m.flushed++
}

// mockResponse 创建模拟HTTP响应
func mockResponse(body string, statusCode int) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestStreamProcessor_NewStreamProcessor(t *testing.T) {
	tokenParser := NewTokenParser()
	writer := &mockResponseWriter{}
	requestID := "test-req-123"
	endpoint := "test-endpoint"

	// Test with nil usage tracker for simplicity
	processor := NewStreamProcessor(tokenParser, nil, writer, writer, requestID, endpoint)

	if processor.tokenParser != tokenParser {
		t.Error("TokenParser not set correctly")
	}
	if processor.requestID != requestID {
		t.Error("RequestID not set correctly")
	}
	if processor.endpoint != endpoint {
		t.Error("Endpoint not set correctly")
	}
	if processor.maxParseErrors != 10 {
		t.Error("MaxParseErrors not set to default value")
	}
}

func TestStreamProcessor_ProcessStream_SimpleData(t *testing.T) {
	// 准备测试数据
	testData := "data: test line 1\ndata: test line 2\n"
	resp := mockResponse(testData, 200)

	// 创建处理器
	tokenParser := NewTokenParser()
	writer := &mockResponseWriter{}
	processor := NewStreamProcessor(tokenParser, nil, writer, writer, "test-123", "endpoint")

	// 执行流处理
	_, err := processor.ProcessStream(context.Background(), resp)

	// 验证结果
	if err != nil {
		t.Errorf("ProcessStream failed: %v", err)
	}

	// 验证数据被写入
	output := writer.buffer.String()
	if !strings.Contains(output, "test line 1") {
		t.Error("Output should contain 'test line 1'")
	}
	if !strings.Contains(output, "test line 2") {
		t.Error("Output should contain 'test line 2'")
	}

	// 验证Flush被调用
	if writer.flushed == 0 {
		t.Error("Flush should have been called")
	}

	// 验证字节数统计
	if processor.bytesProcessed == 0 {
		t.Error("BytesProcessed should be greater than 0")
	}
}

func TestStreamProcessor_GetProcessingStats(t *testing.T) {
	// 创建处理器
	tokenParser := NewTokenParser()
	writer := &mockResponseWriter{}
	processor := NewStreamProcessor(tokenParser, nil, writer, writer, "test-stats", "test-endpoint")

	// 设置一些处理统计
	processor.bytesProcessed = 2048

	// 获取统计信息
	stats := processor.GetProcessingStats()

	// 验证统计信息
	if stats["request_id"] != "test-stats" {
		t.Error("Request ID not in stats")
	}
	if stats["endpoint"] != "test-endpoint" {
		t.Error("Endpoint not in stats")
	}
	if stats["bytes_processed"] != int64(2048) {
		t.Error("Bytes processed not in stats")
	}
}

func TestStreamProcessor_Reset(t *testing.T) {
	// 创建处理器
	tokenParser := NewTokenParser()
	writer := &mockResponseWriter{}
	processor := NewStreamProcessor(tokenParser, nil, writer, writer, "test-reset", "endpoint")

	// 设置一些状态
	processor.bytesProcessed = 1024
	processor.parseErrors = append(processor.parseErrors, io.EOF)

	// 重置处理器
	processor.Reset()

	// 验证状态被重置
	if processor.bytesProcessed != 0 {
		t.Error("Bytes processed not reset")
	}
	if len(processor.parseErrors) != 0 {
		t.Error("Parse errors not reset")
	}
}

func TestStreamProcessor_ProcessSSELine_CaptureAllDebugLines(t *testing.T) {
	tokenParser := NewTokenParser()
	writer := &mockResponseWriter{}
	processor := NewStreamProcessor(tokenParser, nil, writer, writer, "test-debug-capture", "endpoint")
	processor.captureAllDebugLines = true

	for i := 0; i < DebugLineLimit+5; i++ {
		processor.processSSELine("data: line-" + strconv.Itoa(i))
	}

	if len(processor.debugLines) != DebugLineLimit+5 {
		t.Fatalf("debugLines should capture all lines when captureAllDebugLines is enabled, got %d", len(processor.debugLines))
	}

	if processor.debugLines[len(processor.debugLines)-1] != "data: line-104" {
		t.Fatalf("unexpected last debug line: %s", processor.debugLines[len(processor.debugLines)-1])
	}
}

func TestStreamProcessor_ProcessStreamWithRetry_RecoversResponsesUsageFromRawTail(t *testing.T) {
	streamBody := strings.Join([]string{
		"event: response.output_text.delta\n",
		`data: {"type":"response.output_text.delta","delta":"hello","output_index":0,"sequence_number":1}` + "\n",
		"\n",
		"event: response.output_text.done\n",
		`data: {"type":"response.output_text.done","output_index":0,"sequence_number":2,"text":"hello"}` + "\n",
		"\n",
		`{"id":"resp_test_123","model":"gpt-5.3-codex","output":[{"id":"msg_123","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":88,"output_tokens":9,"input_tokens_details":{"cached_tokens":6}}}`,
	}, "")

	resp := mockResponse(streamBody, http.StatusOK)
	tokenParser := NewTokenParserWithRequestID("test-responses-raw-tail")
	writer := &mockResponseWriter{}
	processor := NewStreamProcessor(tokenParser, nil, writer, writer, "test-responses-raw-tail", "endpoint")

	tokenUsage, modelName, err := processor.ProcessStreamWithRetry(context.Background(), resp)
	if err != nil {
		t.Fatalf("ProcessStreamWithRetry should recover usage from raw tail, got error: %v", err)
	}

	if tokenUsage == nil {
		t.Fatal("expected tokenUsage to be recovered from raw tail")
	}

	if tokenUsage.InputTokens != 88 || tokenUsage.OutputTokens != 9 || tokenUsage.CacheReadTokens != 6 {
		t.Fatalf("unexpected recovered usage: input=%d output=%d cache=%d", tokenUsage.InputTokens, tokenUsage.OutputTokens, tokenUsage.CacheReadTokens)
	}

	if modelName != "gpt-5.3-codex" {
		t.Fatalf("expected recovered modelName=gpt-5.3-codex, got %s", modelName)
	}

	completeness := tokenParser.GetStreamCompleteness()
	if !completeness.IsComplete {
		t.Fatalf("expected recovered raw tail responses stream to be complete, got reason=%s failure=%s", completeness.Reason, completeness.FailureReason)
	}
}

func TestStreamProcessor_ProcessStreamWithRetry_ParsesLargeResponsesCompletedEvent(t *testing.T) {
	largeText := strings.Repeat("调度说明", StreamBufferSize*2)
	streamBody := strings.Join([]string{
		"event: response.in_progress\n",
		`data: {"type":"response.in_progress","response":{"model":"gpt-5-codex"}}` + "\n",
		"\n",
		"event: response.completed\n",
		fmt.Sprintf(`data: {"type":"response.completed","response":{"model":"gpt-5-codex"},"usage":{"input_tokens":85388,"output_tokens":1929,"input_tokens_details":{"cached_tokens":85248},"output_tokens_details":{"reasoning_tokens":892},"total_tokens":87317},"text":"%s"}`+"\n", largeText),
		"\n",
	}, "")

	resp := mockResponse(streamBody, http.StatusOK)
	tokenParser := NewTokenParserWithRequestID("test-responses-large-terminal")
	writer := &mockResponseWriter{}
	processor := NewStreamProcessor(tokenParser, nil, writer, writer, "test-responses-large-terminal", "endpoint")

	tokenUsage, modelName, err := processor.ProcessStreamWithRetry(context.Background(), resp)
	if err != nil {
		t.Fatalf("ProcessStreamWithRetry should parse large response.completed event, got error: %v", err)
	}
	if tokenUsage == nil {
		t.Fatal("expected tokenUsage for large response.completed event")
	}
	if tokenUsage.InputTokens != 85388 || tokenUsage.OutputTokens != 1929 || tokenUsage.CacheReadTokens != 85248 {
		t.Fatalf("unexpected usage: input=%d output=%d cache=%d", tokenUsage.InputTokens, tokenUsage.OutputTokens, tokenUsage.CacheReadTokens)
	}
	if modelName != "gpt-5-codex" {
		t.Fatalf("expected modelName=gpt-5-codex, got %s", modelName)
	}
	completeness := tokenParser.GetStreamCompleteness()
	if !completeness.IsComplete {
		t.Fatalf("expected large responses stream complete, got reason=%s failure=%s", completeness.Reason, completeness.FailureReason)
	}
}

func TestStreamProcessor_ProcessStreamWithRetry_StopsAtResponsesTerminalWithoutWaitingForEOF(t *testing.T) {
	reader, writerPipe := io.Pipe()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       reader,
	}

	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		_, _ = io.WriteString(writerPipe, "event: response.completed\n")
		_, _ = io.WriteString(writerPipe, `data: {"type":"response.completed","response":{"model":"gpt-5.5"},"usage":{"input_tokens":18,"output_tokens":5,"input_tokens_details":{"cached_tokens":9}}}`+"\n\n")
		time.Sleep(300 * time.Millisecond)
		_ = writerPipe.Close()
	}()

	tokenParser := NewTokenParserWithRequestID("test-responses-terminal-open-connection")
	writer := &mockResponseWriter{}
	processor := NewStreamProcessor(tokenParser, nil, writer, writer, "test-responses-terminal-open-connection", "anyrouter")

	startedAt := time.Now()
	tokenUsage, modelName, err := processor.ProcessStreamWithRetry(context.Background(), resp)
	elapsed := time.Since(startedAt)
	if err != nil {
		t.Fatalf("ProcessStreamWithRetry should stop at response.completed, got error: %v", err)
	}
	if elapsed >= 150*time.Millisecond {
		t.Fatalf("expected stream to finish without waiting for EOF, elapsed=%s", elapsed)
	}
	if tokenUsage == nil {
		t.Fatal("expected tokenUsage from response.completed")
	}
	if tokenUsage.InputTokens != 18 || tokenUsage.OutputTokens != 5 || tokenUsage.CacheReadTokens != 9 {
		t.Fatalf("unexpected usage: input=%d output=%d cache=%d", tokenUsage.InputTokens, tokenUsage.OutputTokens, tokenUsage.CacheReadTokens)
	}
	if modelName != "gpt-5.5" {
		t.Fatalf("expected modelName=gpt-5.5, got %s", modelName)
	}

	select {
	case <-writeDone:
	case <-time.After(time.Second):
		t.Fatal("writer goroutine did not finish")
	}
}

func TestStreamProcessor_ProcessStreamWithRetry_RecordsFirstTokenOnce(t *testing.T) {
	streamBody := strings.Join([]string{
		"event: message_start\n",
		`data: {"type":"message_start","message":{"model":"claude-sonnet-4-20250514","usage":{"input_tokens":12,"output_tokens":0}}}` + "\n",
		"\n",
		"event: content_block_start\n",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n",
		"\n",
		"event: content_block_delta\n",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"   "}}` + "\n",
		"\n",
		"event: content_block_delta\n",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}` + "\n",
		"\n",
		"event: content_block_delta\n",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}` + "\n",
		"\n",
		"event: content_block_stop\n",
		`data: {"type":"content_block_stop","index":0}` + "\n",
		"\n",
		"event: message_delta\n",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":12,"output_tokens":7}}` + "\n",
		"\n",
		"event: message_stop\n",
		`data: {"type":"message_stop"}` + "\n",
		"\n",
	}, "")

	resp := mockResponse(streamBody, http.StatusOK)
	tokenParser := NewTokenParserWithRequestID("test-first-token-once")
	writer := &mockResponseWriter{}
	processor := NewStreamProcessor(tokenParser, nil, writer, writer, "test-first-token-once", "endpoint")

	callCount := 0
	processor.SetFirstTokenRecorder(func() {
		callCount++
	})

	tokenUsage, modelName, err := processor.ProcessStreamWithRetry(context.Background(), resp)
	if err != nil {
		t.Fatalf("expected stream to complete successfully, got %v", err)
	}
	if tokenUsage == nil {
		t.Fatal("expected token usage after successful stream")
	}
	if modelName != "claude-sonnet-4-20250514" {
		t.Fatalf("expected modelName=claude-sonnet-4-20250514, got %s", modelName)
	}
	if callCount != 1 {
		t.Fatalf("expected first token recorder to be called once, got %d", callCount)
	}
}

func TestStreamProcessor_ProcessStreamWithRetry_RecordsFirstResponseEventOnce(t *testing.T) {
	streamBody := strings.Join([]string{
		"event: response.created\n",
		`data: {"type":"response.created","response":{"model":"gpt-5.4"}}` + "\n",
		"\n",
		"event: response.in_progress\n",
		`data: {"type":"response.in_progress","response":{"model":"gpt-5.4"}}` + "\n",
		"\n",
		"event: response.output_item.added\n",
		`data: {"type":"response.output_item.added","item":{"type":"message","content":[{"type":"output_text","text":"hello"}]}}` + "\n",
		"\n",
		"event: response.completed\n",
		`data: {"type":"response.completed","response":{"model":"gpt-5.4"},"usage":{"input_tokens":18,"output_tokens":5,"input_tokens_details":{"cached_tokens":9}}}` + "\n",
		"\n",
	}, "")

	resp := mockResponse(streamBody, http.StatusOK)
	tokenParser := NewTokenParserWithRequestID("test-fallback-before-visible")
	writer := &mockResponseWriter{}
	processor := NewStreamProcessor(tokenParser, nil, writer, writer, "test-fallback-before-visible", "endpoint")

	callCount := 0
	processor.SetFirstTokenRecorder(func() { callCount++ })

	tokenUsage, modelName, err := processor.ProcessStreamWithRetry(context.Background(), resp)
	if err != nil {
		t.Fatalf("expected stream to complete successfully, got %v", err)
	}
	if tokenUsage == nil {
		t.Fatal("expected token usage after successful stream")
	}
	if modelName != "gpt-5.4" {
		t.Fatalf("expected modelName=gpt-5.4, got %s", modelName)
	}
	if callCount != 1 {
		t.Fatalf("expected first-response recorder to be called once, got %d", callCount)
	}
}

func TestStreamProcessor_ProcessSSELine_RecordsFirstRawDataResponse(t *testing.T) {
	tokenParser := NewTokenParserWithRequestID("test-raw-data-first-response")
	writer := &mockResponseWriter{}
	processor := NewStreamProcessor(tokenParser, nil, writer, writer, "test-raw-data-first-response", "endpoint")

	callCount := 0
	processor.SetFirstTokenRecorder(func() { callCount++ })

	processor.processSSELine("data: ping")
	processor.processSSELine("data: another")

	if callCount != 1 {
		t.Fatalf("expected raw data response recorder to be called once, got %d", callCount)
	}
}

func TestStreamProcessor_ProcessSSELine_IgnoresDoneAndEmptyDataForFirstResponse(t *testing.T) {
	tokenParser := NewTokenParserWithRequestID("test-empty-data-first-response")
	writer := &mockResponseWriter{}
	processor := NewStreamProcessor(tokenParser, nil, writer, writer, "test-empty-data-first-response", "endpoint")

	callCount := 0
	processor.SetFirstTokenRecorder(func() { callCount++ })

	processor.processSSELine("data:   ")
	processor.processSSELine("data: [DONE]")
	processor.processSSELine(": keep-alive")

	if callCount != 0 {
		t.Fatalf("expected no first response record for empty/DONE/comment lines, got %d", callCount)
	}
}

func TestStreamProcessor_IsNetworkError(t *testing.T) {
	processor := &StreamProcessor{}
	// 创建错误恢复管理器用于测试
	processor.errorRecovery = NewErrorRecoveryManager(nil)

	testCases := []struct {
		err      error
		expected bool
	}{
		{nil, false},
		{io.ErrUnexpectedEOF, false},
		{&mockNetError{"connection reset"}, true},
		{&mockNetError{"connection refused"}, true},
		{&mockNetError{"timeout"}, false}, // This is now classified as a timeout error, not network
		{&mockNetError{"network is unreachable"}, true},
		{&mockNetError{"no route to host"}, true},
		{&mockNetError{"broken pipe"}, true},
		{&mockNetError{"unknown error"}, false},
	}

	for _, tc := range testCases {
		result := processor.errorRecovery.isNetworkError(tc.err)
		if result != tc.expected {
			t.Errorf("isNetworkError(%v) = %v, expected %v", tc.err, result, tc.expected)
		}
	}
}

// mockNetError 模拟网络错误
type mockNetError struct {
	msg string
}

func (e *mockNetError) Error() string {
	return e.msg
}

func TestStreamProcessor_CollectAvailableInfoV2_CompleteResponsesTreatsCancelAsSuccess(t *testing.T) {
	tokenParser := NewTokenParserWithRequestID("test-complete-cancel")
	tokenParser.hasResponsesEvent = true
	tokenParser.hasResponseCompleted = true
	tokenParser.hasResponseCompletedUsage = true
	tokenParser.modelName = "gpt-5.4"
	tokenParser.finalUsage = &tracking.TokenUsage{
		InputTokens:     100,
		OutputTokens:    20,
		CacheReadTokens: 80,
	}

	writer := &mockResponseWriter{}
	processor := NewStreamProcessor(tokenParser, nil, writer, writer, "test-complete-cancel", "endpoint")

	tokenUsage, err := processor.collectAvailableInfoV2(context.Canceled, "cancelled_with_data")
	if err != nil {
		t.Fatalf("expected nil error for completed stream cancellation, got %v", err)
	}
	if tokenUsage == nil {
		t.Fatal("expected tokenUsage for completed stream cancellation")
	}
	if tokenUsage.InputTokens != 100 || tokenUsage.OutputTokens != 20 || tokenUsage.CacheReadTokens != 80 {
		t.Fatalf("unexpected token usage: %+v", tokenUsage)
	}
}

func TestStreamProcessor_ProcessStreamWithRetry_DrainsAfterDownstreamCancelUntilResponsesComplete(t *testing.T) {
	reader, writerPipe := io.Pipe()
	resp := &http.Response{
		StatusCode: 200,
		Header:     make(http.Header),
		Body:       reader,
	}

	downstreamCtx, cancelDownstream := context.WithCancel(context.Background())
	defer cancelDownstream()

	tokenParser := NewTokenParserWithRequestID("test-drain-after-cancel")
	writer := &mockResponseWriter{}
	processor := NewStreamProcessor(tokenParser, nil, writer, writer, "test-drain-after-cancel", "endpoint")
	processor.EnableDownstreamTailDrain(300*time.Millisecond, nil)

	go func() {
		defer writerPipe.Close()
		_, _ = io.WriteString(writerPipe, "event: response.in_progress\n")
		_, _ = io.WriteString(writerPipe, "data: {\"type\":\"response.in_progress\",\"response\":{\"model\":\"gpt-5.4\"}}\n\n")
		time.Sleep(20 * time.Millisecond)
		cancelDownstream()
		time.Sleep(20 * time.Millisecond)
		_, _ = io.WriteString(writerPipe, "event: response.completed\n")
		_, _ = io.WriteString(writerPipe, "data: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-5.4\"},\"usage\":{\"input_tokens\":18,\"output_tokens\":5,\"input_tokens_details\":{\"cached_tokens\":9}}}\n\n")
	}()

	tokenUsage, modelName, err := processor.ProcessStreamWithRetry(downstreamCtx, resp)
	if err != nil {
		t.Fatalf("expected nil error after draining completed tail, got %v", err)
	}
	if tokenUsage == nil {
		t.Fatal("expected token usage after draining completed tail")
	}
	if tokenUsage.InputTokens != 18 || tokenUsage.OutputTokens != 5 || tokenUsage.CacheReadTokens != 9 {
		t.Fatalf("unexpected token usage after drain: %+v", tokenUsage)
	}
	if modelName != "gpt-5.4" {
		t.Fatalf("expected modelName=gpt-5.4, got %s", modelName)
	}
}

func TestStreamProcessor_ProcessStreamWithRetry_DrainsAfterDownstreamCancelUntilMessageStop(t *testing.T) {
	reader, writerPipe := io.Pipe()
	resp := &http.Response{
		StatusCode: 200,
		Header:     make(http.Header),
		Body:       reader,
	}

	downstreamCtx, cancelDownstream := context.WithCancel(context.Background())
	defer cancelDownstream()

	tokenParser := NewTokenParserWithRequestID("test-drain-after-message-stop")
	writer := &mockResponseWriter{}
	processor := NewStreamProcessor(tokenParser, nil, writer, writer, "test-drain-after-message-stop", "endpoint")
	processor.EnableDownstreamTailDrain(300*time.Millisecond, nil)

	go func() {
		defer writerPipe.Close()
		_, _ = io.WriteString(writerPipe, "event: message_start\n")
		_, _ = io.WriteString(writerPipe, "data: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-sonnet-4-20250514\",\"usage\":{\"input_tokens\":12,\"output_tokens\":0}}}\n\n")
		_, _ = io.WriteString(writerPipe, "event: content_block_start\n")
		_, _ = io.WriteString(writerPipe, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		_, _ = io.WriteString(writerPipe, "event: content_block_stop\n")
		_, _ = io.WriteString(writerPipe, "data: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		_, _ = io.WriteString(writerPipe, "event: message_delta\n")
		_, _ = io.WriteString(writerPipe, "data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":7}}\n\n")
		time.Sleep(20 * time.Millisecond)
		cancelDownstream()
		time.Sleep(20 * time.Millisecond)
		_, _ = io.WriteString(writerPipe, "event: message_stop\n")
		_, _ = io.WriteString(writerPipe, "data: {\"type\":\"message_stop\"}\n\n")
	}()

	tokenUsage, modelName, err := processor.ProcessStreamWithRetry(downstreamCtx, resp)
	if err != nil {
		t.Fatalf("expected nil error after draining until message_stop, got %v", err)
	}
	if tokenUsage == nil {
		t.Fatal("expected token usage after draining until message_stop")
	}
	if tokenUsage.InputTokens != 12 || tokenUsage.OutputTokens != 7 {
		t.Fatalf("unexpected token usage after Claude drain: %+v", tokenUsage)
	}
	if modelName != "claude-sonnet-4-20250514" {
		t.Fatalf("expected modelName=claude-sonnet-4-20250514, got %s", modelName)
	}
}

func TestStreamProcessor_ProcessStreamWithRetry_TerminalEventStopsDrainBeforeQualityCheck(t *testing.T) {
	reader, writerPipe := io.Pipe()
	resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: reader}
	downstreamCtx, cancelDownstream := context.WithCancel(context.Background())
	defer cancelDownstream()

	tokenParser := NewTokenParserWithRequestID("test-terminal-before-quality-check")
	writer := &mockResponseWriter{}
	processor := NewStreamProcessor(tokenParser, nil, writer, writer, "test-terminal-before-quality-check", "endpoint")
	processor.EnableDownstreamTailDrain(500*time.Millisecond, func() {
		_ = writerPipe.CloseWithError(context.DeadlineExceeded)
	})

	releaseWriter := make(chan struct{})
	writerDone := make(chan struct{})
	defer func() {
		close(releaseWriter)
		<-writerDone
	}()
	go func() {
		defer close(writerDone)
		defer writerPipe.Close()
		_, _ = io.WriteString(writerPipe, "event: message_start\n")
		_, _ = io.WriteString(writerPipe, "data: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-sonnet-4-20250514\",\"usage\":{\"input_tokens\":12,\"output_tokens\":0}}}\n\n")
		_, _ = io.WriteString(writerPipe, "event: content_block_start\n")
		_, _ = io.WriteString(writerPipe, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		_, _ = io.WriteString(writerPipe, "event: message_delta\n")
		_, _ = io.WriteString(writerPipe, "data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":7}}\n\n")
		time.Sleep(20 * time.Millisecond)
		cancelDownstream()
		time.Sleep(20 * time.Millisecond)
		_, _ = io.WriteString(writerPipe, "event: message_stop\n")
		_, _ = io.WriteString(writerPipe, "data: {\"type\":\"message_stop\"}\n\n")
		<-releaseWriter
	}()

	startedAt := time.Now()
	_, modelName, err := processor.ProcessStreamWithRetry(downstreamCtx, resp)
	if elapsed := time.Since(startedAt); elapsed >= 400*time.Millisecond {
		t.Fatalf("terminal event 后不应等待 drain 超时，实际耗时 %s", elapsed)
	}
	streamErr, ok := IsStreamIncompleteError(err)
	if !ok {
		t.Fatalf("期望 terminal 后保留 incomplete_stream，得到 %v", err)
	}
	if streamErr.FailureReason != "incomplete_stream" || !strings.Contains(streamErr.Reason, "尚未结束") {
		t.Fatalf("unexpected stream completeness error: %+v", streamErr)
	}
	if modelName != "claude-sonnet-4-20250514" {
		t.Fatalf("expected modelName=claude-sonnet-4-20250514, got %s", modelName)
	}
}
