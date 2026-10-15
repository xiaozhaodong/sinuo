package proxy

import (
	"sinuo/internal/monitor"
	"testing"
)

func TestResponsesUsageDistinguishesUnknownAndReportedZero(t *testing.T) {
	for _, tc := range []struct {
		usage    string
		reported bool
	}{
		{`null`, false}, {`{}`, false}, {`{"input_tokens":"unknown"}`, false},
		{`{"input_tokens":-1}`, false}, {`{"input_tokens":0,"output_tokens":0}`, true},
	} {
		for _, event := range []string{"response.completed", "response.failed"} {
			result := ParseResponsesEventJSON(event, []byte(`{"response":{"usage":`+tc.usage+`}}`))
			if result == nil || result.HasTokenUsage != tc.reported {
				t.Fatalf("%s usage=%s: %+v", event, tc.usage, result)
			}
		}
	}
}

func TestTokenParser(t *testing.T) {
	parser := NewTokenParser()

	// Test parsing Claude API message_delta event with usage
	lines := []string{
		"event: message_delta",
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"input_tokens\":5,\"cache_creation_input_tokens\":494,\"cache_read_input_tokens\":110689,\"output_tokens\":582}}",
		"",
	}

	var result *monitor.TokenUsage
	for _, line := range lines {
		if tokens := parser.ParseSSELine(line); tokens != nil {
			result = tokens
		}
	}

	if result == nil {
		t.Fatal("Expected to parse token usage, got nil")
	}

	// Check the values
	if result.InputTokens != 5 {
		t.Errorf("Expected InputTokens=5, got %d", result.InputTokens)
	}
	if result.OutputTokens != 582 {
		t.Errorf("Expected OutputTokens=582, got %d", result.OutputTokens)
	}
	if result.CacheCreationTokens != 494 {
		t.Errorf("Expected CacheCreationTokens=494, got %d", result.CacheCreationTokens)
	}
	if result.CacheReadTokens != 110689 {
		t.Errorf("Expected CacheReadTokens=110689, got %d", result.CacheReadTokens)
	}
}

func TestTokenParserNonUsageEvent(t *testing.T) {
	parser := NewTokenParser()

	// Test parsing non-usage message_delta event
	lines := []string{
		"event: message_delta",
		"data: {\"type\":\"message_delta\",\"delta\":{\"text\":\"Hello world\"}}",
		"",
	}

	var result *monitor.TokenUsage
	for _, line := range lines {
		if tokens := parser.ParseSSELine(line); tokens != nil {
			result = tokens
		}
	}

	if result != nil {
		t.Error("Expected nil for message_delta without usage, got result")
	}
}

func TestTokenParserOtherEvents(t *testing.T) {
	parser := NewTokenParser()

	// Test parsing non-message_delta events
	lines := []string{
		"event: ping",
		"data: {\"type\":\"ping\"}",
		"",
	}

	var result *monitor.TokenUsage
	for _, line := range lines {
		if tokens := parser.ParseSSELine(line); tokens != nil {
			result = tokens
		}
	}

	if result != nil {
		t.Error("Expected nil for non-message_delta events, got result")
	}
}

// ===== V2 职责纯化测试 =====

func TestTokenParserV2_MessageDeltaWithUsage(t *testing.T) {
	parser := NewTokenParserWithRequestID("test-req-123")

	// Test parsing Claude API message_delta event with usage using V2 method
	lines := []string{
		"event: message_delta",
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"input_tokens\":5,\"cache_creation_input_tokens\":494,\"cache_read_input_tokens\":110689,\"output_tokens\":582}}",
		"",
	}

	var result *ParseResult
	for _, line := range lines {
		if parseResult := parser.ParseSSELineV2(line); parseResult != nil {
			result = parseResult
		}
	}

	if result == nil {
		t.Fatal("Expected to parse token usage with V2 method, got nil")
	}

	// Check the ParseResult structure
	if result.TokenUsage == nil {
		t.Fatal("Expected TokenUsage in ParseResult, got nil")
	}

	if result.TokenUsage.InputTokens != 5 {
		t.Errorf("Expected InputTokens=5, got %d", result.TokenUsage.InputTokens)
	}
	if result.TokenUsage.OutputTokens != 582 {
		t.Errorf("Expected OutputTokens=582, got %d", result.TokenUsage.OutputTokens)
	}
	if result.TokenUsage.CacheCreationTokens != 494 {
		t.Errorf("Expected CacheCreationTokens=494, got %d", result.TokenUsage.CacheCreationTokens)
	}
	if result.TokenUsage.CacheReadTokens != 110689 {
		t.Errorf("Expected CacheReadTokens=110689, got %d", result.TokenUsage.CacheReadTokens)
	}

	if !result.IsCompleted {
		t.Error("Expected IsCompleted=true for message_delta with usage")
	}

	if result.Status != "completed" {
		t.Errorf("Expected Status=completed, got %s", result.Status)
	}
}

func TestTokenParserV2_MessageDeltaWithoutUsage(t *testing.T) {
	parser := NewTokenParserWithRequestID("test-req-456")

	// Test parsing non-usage message_delta event using V2 method
	lines := []string{
		"event: message_delta",
		"data: {\"type\":\"message_delta\",\"delta\":{\"text\":\"Hello world\"}}",
		"",
	}

	var result *ParseResult
	for _, line := range lines {
		if parseResult := parser.ParseSSELineV2(line); parseResult != nil {
			result = parseResult
		}
	}

	if result == nil {
		t.Fatal("Expected to get ParseResult for non-usage message_delta, got nil")
	}

	// Check the ParseResult for non-token response
	if result.TokenUsage == nil {
		t.Fatal("Expected empty TokenUsage in ParseResult, got nil")
	}

	// Should have empty token usage
	if result.TokenUsage.InputTokens != 0 {
		t.Errorf("Expected InputTokens=0 for non-usage, got %d", result.TokenUsage.InputTokens)
	}

	if !result.IsCompleted {
		t.Error("Expected IsCompleted=true for non-usage message_delta")
	}

	if result.Status != "non_token_response" {
		t.Errorf("Expected Status=non_token_response, got %s", result.Status)
	}

	if result.ModelName != "default" {
		t.Errorf("Expected ModelName=default, got %s", result.ModelName)
	}
}

func TestTokenParserV2_ResponsesOutputTextDeltaMarksVisibleText(t *testing.T) {
	parser := NewTokenParserWithRequestID("test-responses-visible-text")

	lines := []string{
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","delta":"Hello world","output_index":0,"sequence_number":1}`,
		"",
	}

	var result *ParseResult
	for _, line := range lines {
		if parseResult := parser.ParseSSELineV2(line); parseResult != nil {
			result = parseResult
		}
	}

	if result == nil {
		t.Fatal("Expected ParseResult for responses output_text delta, got nil")
	}
	if !result.HasVisibleText {
		t.Fatal("Expected HasVisibleText=true for responses output_text delta")
	}
	if result.TokenUsage != nil {
		t.Fatal("Expected no TokenUsage for plain visible text delta")
	}
}

func TestTokenParserV2_ResponsesInProgressMarksStreamOutput(t *testing.T) {
	parser := NewTokenParserWithRequestID("test-responses-fallback")

	lines := []string{
		"event: response.in_progress",
		`data: {"type":"response.in_progress","response":{"model":"gpt-5.4"}}`,
		"",
	}

	var result *ParseResult
	for _, line := range lines {
		if parseResult := parser.ParseSSELineV2(line); parseResult != nil {
			result = parseResult
		}
	}

	if result == nil {
		t.Fatal("Expected ParseResult for response.in_progress, got nil")
	}
	if !result.HasStreamOutput {
		t.Fatal("Expected HasStreamOutput=true for response.in_progress")
	}
	if result.HasVisibleText {
		t.Fatal("Expected HasVisibleText=false for response.in_progress")
	}
}

func TestTokenParserV2_ResponsesOutputItemAddedMarksVisibleText(t *testing.T) {
	parser := NewTokenParserWithRequestID("test-responses-output-item")

	lines := []string{
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","item":{"type":"message","content":[{"type":"output_text","text":"Hello from item"}]}}`,
		"",
	}

	var result *ParseResult
	for _, line := range lines {
		if parseResult := parser.ParseSSELineV2(line); parseResult != nil {
			result = parseResult
		}
	}

	if result == nil {
		t.Fatal("Expected ParseResult for response.output_item.added, got nil")
	}
	if !result.HasVisibleText {
		t.Fatal("Expected HasVisibleText=true for response.output_item.added")
	}
}

func TestTokenParserV2_MessageDeltaWithoutUsage_ReportedOnce(t *testing.T) {
	parser := NewTokenParserWithRequestID("test-req-456-repeat")

	lines := []string{
		"event: message_delta",
		"data: {\"type\":\"message_delta\",\"delta\":{\"text\":\"Hello\"}}",
		"",
		"event: message_delta",
		"data: {\"type\":\"message_delta\",\"delta\":{\"text\":\"World\"}}",
		"",
	}

	resultCount := 0
	for _, line := range lines {
		if parseResult := parser.ParseSSELineV2(line); parseResult != nil {
			resultCount++
		}
	}

	if resultCount != 1 {
		t.Errorf("Expected non-usage ParseResult to be reported once, got %d", resultCount)
	}
}

func TestTokenParserV2_MessageDeltaWithoutUsage_AfterMessageStartIgnored(t *testing.T) {
	parser := NewTokenParserWithRequestID("test-req-message-start")

	lines := []string{
		"event: message_start",
		"data: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-sonnet-4-6\",\"usage\":{\"input_tokens\":1,\"output_tokens\":8,\"cache_creation_input_tokens\":0,\"cache_read_input_tokens\":0}}}",
		"",
		"event: message_delta",
		"data: {\"type\":\"message_delta\",\"delta\":{\"text\":\"hello\"}}",
		"",
	}

	var results []*ParseResult
	for _, line := range lines {
		if parseResult := parser.ParseSSELineV2(line); parseResult != nil {
			results = append(results, parseResult)
		}
	}

	if len(results) != 1 {
		t.Fatalf("Expected 1 ParseResult entry (message_start stream output), got %d", len(results))
	}
	if !results[0].HasStreamOutput {
		t.Fatal("Expected message_start to provide stream output signal")
	}

	finalUsage := parser.GetFinalUsage()
	if finalUsage == nil {
		t.Fatal("Expected fallback final usage from message_start, got nil")
	}
	if finalUsage.InputTokens != 1 || finalUsage.OutputTokens != 8 {
		t.Errorf("Expected fallback usage input=1 output=8, got input=%d output=%d", finalUsage.InputTokens, finalUsage.OutputTokens)
	}
}

func TestTokenParserV2_ErrorEvent(t *testing.T) {
	parser := NewTokenParserWithRequestID("test-req-error")

	// Test parsing error event using V2 method
	lines := []string{
		"event: error",
		"data: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Server is overloaded\"}}",
		"",
	}

	var result *ParseResult
	for _, line := range lines {
		if parseResult := parser.ParseSSELineV2(line); parseResult != nil {
			result = parseResult
		}
	}

	if result == nil {
		t.Fatal("Expected to get ParseResult for error event, got nil")
	}

	// Check error handling
	if result.ErrorInfo == nil {
		t.Fatal("Expected ErrorInfo in ParseResult, got nil")
	}

	if result.ErrorInfo.Type != "overloaded_error" {
		t.Errorf("Expected ErrorInfo.Type=overloaded_error, got %s", result.ErrorInfo.Type)
	}

	if result.ErrorInfo.Message != "Server is overloaded" {
		t.Errorf("Expected ErrorInfo.Message=Server is overloaded, got %s", result.ErrorInfo.Message)
	}

	if !result.IsCompleted {
		t.Error("Expected IsCompleted=true for error event")
	}

	if result.Status != StatusErrorAPI {
		t.Errorf("Expected Status=%s, got %s", StatusErrorAPI, result.Status)
	}

	if result.ModelName != "error:overloaded_error" {
		t.Errorf("Expected ModelName=error:overloaded_error, got %s", result.ModelName)
	}
}

func TestTokenParserV2_ResponsesCompletedWithUsage(t *testing.T) {
	parser := NewTokenParserWithRequestID("test-responses-usage")

	lines := []string{
		"event: response.in_progress",
		`data: {"type":"response.in_progress","response":{"model":"gpt-5-codex"}}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"model":"gpt-5-codex"},"usage":{"input_tokens":16069,"output_tokens":27,"input_tokens_details":{"cached_tokens":13}}}`,
		"",
	}

	var result *ParseResult
	for _, line := range lines {
		if parseResult := parser.ParseSSELineV2(line); parseResult != nil {
			result = parseResult
		}
	}

	if result == nil {
		t.Fatal("Expected ParseResult for response.completed, got nil")
	}
	if result.TokenUsage == nil {
		t.Fatal("Expected TokenUsage in ParseResult, got nil")
	}
	if result.TokenUsage.InputTokens != 16069 {
		t.Errorf("Expected InputTokens=16069, got %d", result.TokenUsage.InputTokens)
	}
	if result.TokenUsage.OutputTokens != 27 {
		t.Errorf("Expected OutputTokens=27, got %d", result.TokenUsage.OutputTokens)
	}
	if result.TokenUsage.CacheReadTokens != 13 {
		t.Errorf("Expected CacheReadTokens=13, got %d", result.TokenUsage.CacheReadTokens)
	}
	if result.ModelName != "gpt-5-codex" {
		t.Errorf("Expected ModelName=gpt-5-codex, got %s", result.ModelName)
	}
	if !result.IsCompleted {
		t.Error("Expected IsCompleted=true for response.completed")
	}
	if result.Status != "completed" {
		t.Errorf("Expected Status=completed, got %s", result.Status)
	}
}

func TestTokenParserV2_ResponsesCompletedWithUsage_DataOnlyLines(t *testing.T) {
	parser := NewTokenParserWithRequestID("test-responses-usage-data-only")

	lines := []string{
		`data: {"type":"response.in_progress","response":{"model":"gpt-5-codex"}}`,
		"",
		`data: {"type":"response.completed","response":{"model":"gpt-5-codex"},"usage":{"input_tokens":42,"output_tokens":7,"input_tokens_details":{"cached_tokens":5}}}`,
		"",
	}

	var result *ParseResult
	for _, line := range lines {
		if parseResult := parser.ParseSSELineV2(line); parseResult != nil {
			result = parseResult
		}
	}

	if result == nil {
		t.Fatal("Expected ParseResult for data-only response.completed, got nil")
	}
	if result.TokenUsage == nil {
		t.Fatal("Expected TokenUsage in ParseResult, got nil")
	}
	if result.TokenUsage.InputTokens != 42 || result.TokenUsage.OutputTokens != 7 || result.TokenUsage.CacheReadTokens != 5 {
		t.Fatalf("unexpected usage: input=%d output=%d cache=%d", result.TokenUsage.InputTokens, result.TokenUsage.OutputTokens, result.TokenUsage.CacheReadTokens)
	}
	completeness := parser.GetStreamCompleteness()
	if !completeness.IsComplete {
		t.Fatalf("Expected data-only responses stream complete, got reason=%s failure=%s", completeness.Reason, completeness.FailureReason)
	}
}

func TestTokenParserV2_ResponsesCompletedWithoutUsage(t *testing.T) {
	parser := NewTokenParserWithRequestID("test-responses-no-usage")

	lines := []string{
		"event: response.in_progress",
		`data: {"type":"response.in_progress","response":{"model":"gpt-5-codex"}}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"model":"gpt-5-codex"}}`,
		"",
	}

	var result *ParseResult
	for _, line := range lines {
		if parseResult := parser.ParseSSELineV2(line); parseResult != nil {
			result = parseResult
		}
	}

	if result == nil {
		t.Fatal("Expected ParseResult for response.completed without usage, got nil")
	}
	if result.TokenUsage == nil {
		t.Fatal("Expected empty TokenUsage in ParseResult, got nil")
	}
	if result.TokenUsage.InputTokens != 0 || result.TokenUsage.OutputTokens != 0 {
		t.Errorf("Expected empty usage for no-usage response.completed, got input=%d output=%d",
			result.TokenUsage.InputTokens, result.TokenUsage.OutputTokens)
	}
	if result.Status != "non_token_response" {
		t.Errorf("Expected Status=non_token_response, got %s", result.Status)
	}
}

func TestTokenParserV2_ResponseDeltaOnly_UsesResponsesCompleteness(t *testing.T) {
	parser := NewTokenParserWithRequestID("test-responses-delta-only")

	lines := []string{
		`data: {"type":"response.output_text.delta","delta":"hello"}`,
		"",
	}

	for _, line := range lines {
		_ = parser.ParseSSELineV2(line)
	}

	completeness := parser.GetStreamCompleteness()
	if completeness.IsComplete {
		t.Fatal("expected incomplete responses stream when response.completed is missing")
	}
	if completeness.Reason != "未收到 response terminal 事件" {
		t.Fatalf("expected response terminal completeness failure, got %s", completeness.Reason)
	}
	if completeness.FailureReason != "stream_truncated" {
		t.Fatalf("expected stream_truncated, got %s", completeness.FailureReason)
	}
}

func TestTokenParserV2_ResponsesDoneWithUsage(t *testing.T) {
	parser := NewTokenParserWithRequestID("test-responses-done")

	lines := []string{
		`data: {"type":"response.output_text.delta","delta":"hello"}`,
		"",
		`data: {"type":"response.done","response":{"model":"gpt-5.3-codex"},"usage":{"input_tokens":88,"output_tokens":9,"input_tokens_details":{"cached_tokens":6}}}`,
		"",
	}

	var result *ParseResult
	for _, line := range lines {
		if parseResult := parser.ParseSSELineV2(line); parseResult != nil {
			result = parseResult
		}
	}

	if result == nil {
		t.Fatal("Expected ParseResult for response.done, got nil")
	}
	if result.TokenUsage == nil {
		t.Fatal("Expected TokenUsage for response.done, got nil")
	}
	if result.TokenUsage.InputTokens != 88 || result.TokenUsage.OutputTokens != 9 || result.TokenUsage.CacheReadTokens != 6 {
		t.Fatalf("unexpected usage: input=%d output=%d cache=%d", result.TokenUsage.InputTokens, result.TokenUsage.OutputTokens, result.TokenUsage.CacheReadTokens)
	}
	completeness := parser.GetStreamCompleteness()
	if !completeness.IsComplete {
		t.Fatalf("expected response.done to mark stream complete, got reason=%s failure=%s", completeness.Reason, completeness.FailureReason)
	}
}

func TestTokenParserV2_ResponsesCompletedWithUsage_FallbackWhenPayloadInvalid(t *testing.T) {
	parser := NewTokenParserWithRequestID("test-responses-usage-fallback")

	lines := []string{
		"event: response.completed",
		`data: {"type":"response.completed","response":{"model":"gpt-5-codex"},"usage":{"input_tokens":12,"output_tokens":3,"input_tokens_details":{"cached_tokens":1}},"broken": }`,
		"",
	}

	var result *ParseResult
	for _, line := range lines {
		if parseResult := parser.ParseSSELineV2(line); parseResult != nil {
			result = parseResult
		}
	}

	if result == nil {
		t.Fatal("Expected ParseResult for response.completed fallback, got nil")
	}
	if result.TokenUsage == nil {
		t.Fatal("Expected TokenUsage in ParseResult, got nil")
	}
	if result.TokenUsage.InputTokens != 12 {
		t.Errorf("Expected InputTokens=12, got %d", result.TokenUsage.InputTokens)
	}
	if result.TokenUsage.OutputTokens != 3 {
		t.Errorf("Expected OutputTokens=3, got %d", result.TokenUsage.OutputTokens)
	}
	if result.TokenUsage.CacheReadTokens != 1 {
		t.Errorf("Expected CacheReadTokens=1, got %d", result.TokenUsage.CacheReadTokens)
	}
	if result.ModelName != "gpt-5-codex" {
		t.Errorf("Expected ModelName=gpt-5-codex, got %s", result.ModelName)
	}
	if result.Status != "completed" {
		t.Errorf("Expected Status=completed, got %s", result.Status)
	}

	completeness := parser.GetStreamCompleteness()
	if !completeness.IsComplete {
		t.Fatalf("Expected responses stream complete after fallback, got reason=%s failure=%s", completeness.Reason, completeness.FailureReason)
	}
}

func TestParseResponsesEventJSONMatchesSSEPath(t *testing.T) {
	payload := `{"type":"response.completed","response":{"id":"resp-shared","model":"gpt-6-astra","usage":{"input_tokens":42,"output_tokens":7,"input_tokens_details":{"cached_tokens":5}}}}`
	direct := ParseResponsesEventJSON("response.completed", []byte(payload))
	parser := NewTokenParserWithRequestID("test-shared-parser")
	_ = parser.ParseSSELineV2("event: response.completed")
	_ = parser.ParseSSELineV2("data: " + payload)
	viaSSE := parser.ParseSSELineV2("")

	if direct == nil || viaSSE == nil || direct.TokenUsage == nil || viaSSE.TokenUsage == nil {
		t.Fatalf("shared parser returned nil: direct=%+v sse=%+v", direct, viaSSE)
	}
	if direct.Status != viaSSE.Status || direct.IsCompleted != viaSSE.IsCompleted || direct.HasTokenUsage != viaSSE.HasTokenUsage ||
		direct.ModelName != viaSSE.ModelName || *direct.TokenUsage != *viaSSE.TokenUsage {
		t.Fatalf("SSE and JSON parser diverged: direct=%+v sse=%+v", direct, viaSSE)
	}
}

func TestParseResponsesEventJSONFailedAndSteeredTerminalSemantics(t *testing.T) {
	failed := ParseResponsesEventJSON("response.failed", []byte(`{"type":"response.failed","response":{"id":"r1","error":{"type":"invalid_request_error","message":"synthetic"},"usage":{"input_tokens":9,"output_tokens":1}}}`))
	if failed == nil || !failed.IsCompleted || failed.Status != StatusErrorAPI || failed.ErrorInfo == nil ||
		!failed.HasTokenUsage || failed.TokenUsage == nil || failed.TokenUsage.InputTokens != 9 {
		t.Fatalf("failed terminal parse mismatch: %+v", failed)
	}
	steered := ParseResponsesEventJSON("response.incomplete", []byte(`{"type":"response.incomplete","response":{"id":"r2","incomplete_details":{"reason":"steered"}}}`))
	if steered == nil || !steered.IsCompleted || steered.Status != "steered" || steered.ErrorInfo != nil || steered.HasTokenUsage {
		t.Fatalf("steered terminal parse mismatch: %+v", steered)
	}
}

func TestParseResponsesEventJSONWrappedErrorIsTerminal(t *testing.T) {
	result := ParseResponsesEventJSON("error", []byte(`{"type":"error","status":429,"error":{"type":"rate_limit_error","message":"synthetic","resets_in_seconds":30}}`))
	if result == nil || !result.IsCompleted || result.Status != StatusErrorAPI || result.ErrorInfo == nil {
		t.Fatalf("wrapped error must be terminal: %+v", result)
	}
	if result.ErrorInfo.Type != "rate_limit_error" || result.ErrorInfo.ResetsInSeconds != 30 {
		t.Fatalf("wrapped error fields mismatch: %+v", result.ErrorInfo)
	}
}

// ===== v5.0.1+: Cache Creation 5m/1h 分开解析测试 =====

// TestTokenParserV2_CacheCreation1hTokens 测试 1 小时缓存 token 解析
func TestTokenParserV2_CacheCreation1hTokens(t *testing.T) {
	parser := NewTokenParserWithRequestID("test-req-cache-1h")

	// 模拟真实 Claude API 响应，包含 cache_creation 嵌套对象
	lines := []string{
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":5000,"output_tokens":800,"cache_creation_input_tokens":2000,"cache_read_input_tokens":10000,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":2000}}}`,
		"",
	}

	var result *ParseResult
	for _, line := range lines {
		if parseResult := parser.ParseSSELineV2(line); parseResult != nil {
			result = parseResult
		}
	}

	if result == nil {
		t.Fatal("Expected to parse token usage with cache_creation, got nil")
	}

	if result.TokenUsage == nil {
		t.Fatal("Expected TokenUsage in ParseResult, got nil")
	}

	// 验证基础 token 计数
	if result.TokenUsage.InputTokens != 5000 {
		t.Errorf("Expected InputTokens=5000, got %d", result.TokenUsage.InputTokens)
	}
	if result.TokenUsage.OutputTokens != 800 {
		t.Errorf("Expected OutputTokens=800, got %d", result.TokenUsage.OutputTokens)
	}

	// 验证缓存 token 计数
	if result.TokenUsage.CacheCreationTokens != 2000 {
		t.Errorf("Expected CacheCreationTokens=2000, got %d", result.TokenUsage.CacheCreationTokens)
	}
	if result.TokenUsage.CacheReadTokens != 10000 {
		t.Errorf("Expected CacheReadTokens=10000, got %d", result.TokenUsage.CacheReadTokens)
	}

	// v5.0.1+: 验证分开的 5m/1h 缓存 tokens
	if result.TokenUsage.CacheCreation5mTokens != 0 {
		t.Errorf("Expected CacheCreation5mTokens=0, got %d", result.TokenUsage.CacheCreation5mTokens)
	}
	if result.TokenUsage.CacheCreation1hTokens != 2000 {
		t.Errorf("Expected CacheCreation1hTokens=2000, got %d", result.TokenUsage.CacheCreation1hTokens)
	}

	t.Logf("✅ 1h cache parsing successful: 5m=%d, 1h=%d, total=%d",
		result.TokenUsage.CacheCreation5mTokens,
		result.TokenUsage.CacheCreation1hTokens,
		result.TokenUsage.CacheCreationTokens)
}

// TestTokenParserV2_CacheCreationMixed 测试混合 5m 和 1h 缓存
func TestTokenParserV2_CacheCreationMixed(t *testing.T) {
	parser := NewTokenParserWithRequestID("test-req-cache-mixed")

	// 模拟同时包含 5m 和 1h 缓存的响应
	lines := []string{
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":10000,"output_tokens":1500,"cache_creation_input_tokens":8000,"cache_read_input_tokens":50000,"cache_creation":{"ephemeral_5m_input_tokens":3000,"ephemeral_1h_input_tokens":5000}}}`,
		"",
	}

	var result *ParseResult
	for _, line := range lines {
		if parseResult := parser.ParseSSELineV2(line); parseResult != nil {
			result = parseResult
		}
	}

	if result == nil || result.TokenUsage == nil {
		t.Fatal("Expected to parse token usage, got nil")
	}

	// 验证分开的缓存 tokens
	if result.TokenUsage.CacheCreation5mTokens != 3000 {
		t.Errorf("Expected CacheCreation5mTokens=3000, got %d", result.TokenUsage.CacheCreation5mTokens)
	}
	if result.TokenUsage.CacheCreation1hTokens != 5000 {
		t.Errorf("Expected CacheCreation1hTokens=5000, got %d", result.TokenUsage.CacheCreation1hTokens)
	}

	// 验证总数（向后兼容）
	if result.TokenUsage.CacheCreationTokens != 8000 {
		t.Errorf("Expected CacheCreationTokens=8000, got %d", result.TokenUsage.CacheCreationTokens)
	}

	t.Logf("✅ Mixed cache parsing successful: 5m=%d, 1h=%d, total=%d",
		result.TokenUsage.CacheCreation5mTokens,
		result.TokenUsage.CacheCreation1hTokens,
		result.TokenUsage.CacheCreationTokens)
}

// TestTokenParserV2_BackwardCompatibleNoCacheCreationObject 测试向后兼容（无 cache_creation 对象）
func TestTokenParserV2_BackwardCompatibleNoCacheCreationObject(t *testing.T) {
	parser := NewTokenParserWithRequestID("test-req-cache-compat")

	// 模拟旧版 API 响应，没有 cache_creation 嵌套对象
	lines := []string{
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":1000,"output_tokens":500,"cache_creation_input_tokens":200,"cache_read_input_tokens":5000}}`,
		"",
	}

	var result *ParseResult
	for _, line := range lines {
		if parseResult := parser.ParseSSELineV2(line); parseResult != nil {
			result = parseResult
		}
	}

	if result == nil || result.TokenUsage == nil {
		t.Fatal("Expected to parse token usage, got nil")
	}

	// 验证基础 token 计数仍然正确
	if result.TokenUsage.CacheCreationTokens != 200 {
		t.Errorf("Expected CacheCreationTokens=200, got %d", result.TokenUsage.CacheCreationTokens)
	}

	// 无 cache_creation 对象时，5m/1h 应为 0
	if result.TokenUsage.CacheCreation5mTokens != 0 {
		t.Errorf("Expected CacheCreation5mTokens=0 for backward compatible, got %d", result.TokenUsage.CacheCreation5mTokens)
	}
	if result.TokenUsage.CacheCreation1hTokens != 0 {
		t.Errorf("Expected CacheCreation1hTokens=0 for backward compatible, got %d", result.TokenUsage.CacheCreation1hTokens)
	}

	t.Log("✅ Backward compatibility test passed: old API format still works")
}

// Responses 显式取消通过 SSE/JSON 共享解析器识别，不改变旧入口错误分类。
func TestResponsesCancellationIsExplicitAndShared(t *testing.T) {
	for _, event := range []string{"response.cancelled", "response.canceled"} {
		payload := `{"type":"` + event + `","response":{"id":"cancelled","usage":{"input_tokens":3,"output_tokens":0}}}`
		jsonResult := ParseResponsesEventJSON(event, []byte(payload))
		parser := NewTokenParser()
		parser.ParseSSELineV2("event: " + event)
		parser.ParseSSELineV2("data: " + payload)
		sseResult := parser.ParseSSELineV2("")
		for _, result := range []*ParseResult{jsonResult, sseResult} {
			if result == nil || !result.IsCompleted || !result.IsCancelled || !result.HasTokenUsage || result.TokenUsage.InputTokens != 3 {
				t.Fatalf("cancellation/usage lost: %+v", result)
			}
			if result.Status != StatusErrorAPI {
				t.Fatal("legacy Status contract changed")
			}
		}
	}
}

// TestTokenParserV2_GetFinalUsageWithCacheCreation 测试 GetFinalUsage 包含 5m/1h 缓存
func TestTokenParserV2_GetFinalUsageWithCacheCreation(t *testing.T) {
	parser := NewTokenParserWithRequestID("test-req-final-usage")

	// 先解析 message_start 获取初始 usage
	startLines := []string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg-123","type":"message","role":"assistant","model":"claude-opus-4-5-20251101","usage":{"input_tokens":5000,"output_tokens":0,"cache_creation_input_tokens":2000,"cache_read_input_tokens":10000,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":2000}}}}`,
		"",
	}

	for _, line := range startLines {
		parser.ParseSSELineV2(line)
	}

	// 再解析 message_delta 获取最终 usage
	deltaLines := []string{
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":5000,"output_tokens":800,"cache_creation_input_tokens":2000,"cache_read_input_tokens":10000,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":2000}}}`,
		"",
	}

	for _, line := range deltaLines {
		parser.ParseSSELineV2(line)
	}

	// 获取最终 usage
	finalUsage := parser.GetFinalUsage()
	if finalUsage == nil {
		t.Fatal("Expected GetFinalUsage to return non-nil")
	}

	// 验证最终 usage 包含正确的 5m/1h 缓存信息
	if finalUsage.CacheCreation1hTokens != 2000 {
		t.Errorf("GetFinalUsage: Expected CacheCreation1hTokens=2000, got %d", finalUsage.CacheCreation1hTokens)
	}
	if finalUsage.CacheCreation5mTokens != 0 {
		t.Errorf("GetFinalUsage: Expected CacheCreation5mTokens=0, got %d", finalUsage.CacheCreation5mTokens)
	}

	t.Logf("✅ GetFinalUsage with cache_creation: input=%d, output=%d, cache_5m=%d, cache_1h=%d, cache_read=%d",
		finalUsage.InputTokens, finalUsage.OutputTokens,
		finalUsage.CacheCreation5mTokens, finalUsage.CacheCreation1hTokens,
		finalUsage.CacheReadTokens)
}
