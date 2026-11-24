package proxy

import (
	"context"
	"errors"
	"net"
	"syscall"
	"testing"
	"time"
)

func TestErrorRecoveryManager_NewErrorRecoveryManager(t *testing.T) {
	erm := NewErrorRecoveryManager(nil)

	if erm.maxRetries != 3 {
		t.Error("Expected maxRetries to be 3")
	}
	if erm.baseDelay != time.Second {
		t.Error("Expected baseDelay to be 1 second")
	}
	if erm.maxDelay != 30*time.Second {
		t.Error("Expected maxDelay to be 30 seconds")
	}
	if erm.backoffFactor != 2.0 {
		t.Error("Expected backoffFactor to be 2.0")
	}
}

func TestErrorRecoveryManager_ClassifyError(t *testing.T) {
	erm := NewErrorRecoveryManager(nil)
	requestID := "test-classify-123"
	endpoint := "test-endpoint"
	group := "test-group"
	attempt := 1

	testCases := []struct {
		err          error
		expectedType ErrorType
		description  string
		shouldRetry  bool
	}{
		{nil, ErrorTypeUnknown, "nil error", false},
		{errors.New("connection reset"), ErrorTypeNetwork, "connection reset", true},
		{errors.New("connection refused"), ErrorTypeNetwork, "connection refused", true},
		{errors.New("i/o timeout"), ErrorTypeResponseTimeout, "i/o timeout", false},                     // 响应超时不可重试
		{errors.New("context deadline exceeded"), ErrorTypeResponseTimeout, "deadline exceeded", false}, // 响应超时不可重试
		{errors.New("HTTP 500 Internal Server Error"), ErrorTypeServerError, "HTTP 5xx error", true},
		{errors.New("HTTP 400 Bad Request"), ErrorTypeHTTP, "HTTP 400 error", false},
		{errors.New("HTTP 404 Not Found"), ErrorTypeHTTP, "HTTP 4xx error (non-400)", false},
		{errors.New("unauthorized access"), ErrorTypeAuth, "auth error", false},
		{errors.New("rate limit exceeded"), ErrorTypeRateLimit, "rate limit", true},
		{errors.New("stream parsing error"), ErrorTypeStream, "stream error", false}, // 流处理错误不可重试
		{errors.New("unknown issue"), ErrorTypeUnknown, "unknown error", false},      // 未知错误不重试（保守策略）
	}

	for _, tc := range testCases {
		errorCtx := erm.ClassifyError(tc.err, requestID, endpoint, group, attempt)

		if errorCtx.ErrorType != tc.expectedType {
			t.Errorf("For error '%v', expected type %v, got %v", tc.err, tc.expectedType, errorCtx.ErrorType)
		}

		if errorCtx.RequestID != requestID {
			t.Errorf("RequestID not set correctly")
		}

		if errorCtx.EndpointName != endpoint {
			t.Errorf("EndpointName not set correctly")
		}

		if errorCtx.AttemptCount != attempt {
			t.Errorf("AttemptCount not set correctly")
		}

		shouldRetry := erm.ShouldRetry(errorCtx)
		if tc.description == "HTTP 4xx error (non-400)" && shouldRetry {
			t.Errorf("HTTP 4xx errors (non-400) should not be retryable")
		}
		if tc.description == "HTTP 400 error" && shouldRetry {
			t.Errorf("HTTP 400 errors should not be retryable")
		}
		if tc.description == "auth error" && shouldRetry {
			t.Errorf("Auth errors should not be retryable")
		}
	}
}

func TestErrorRecoveryManager_ShouldRetry(t *testing.T) {
	erm := NewErrorRecoveryManager(nil)

	testCases := []struct {
		errorType    ErrorType
		attemptCount int
		maxRetries   int
		expected     bool
		description  string
	}{
		{ErrorTypeNetwork, 1, 3, true, "network error within retry limit"},
		{ErrorTypeNetwork, 5, 3, false, "network error exceeds retry limit"},
		{ErrorTypeConnectionTimeout, 2, 3, true, "connection timeout within retry limit"},
		{ErrorTypeResponseTimeout, 1, 3, false, "response timeout not retryable"},
		{ErrorTypeTimeout, 1, 3, false, "timeout error not retryable (maps to response timeout)"},
		{ErrorTypeEOF, 1, 3, false, "EOF error not retryable"},
		{ErrorTypeAuth, 1, 3, false, "auth error not retryable"},
		{ErrorTypeHTTP, 1, 3, false, "general HTTP error not retryable"},
		{ErrorTypeRateLimit, 2, 3, true, "rate limit retryable"},
		{ErrorTypeStream, 1, 3, false, "stream error not retryable (data already sent)"},
		{ErrorTypeParsing, 1, 3, true, "parsing error retryable"},
		{ErrorTypeUnknown, 1, 3, false, "unknown error not retryable (conservative)"},
		{ErrorTypeServerError, 1, 3, true, "server error (5xx) retryable"},
	}

	for _, tc := range testCases {
		errorCtx := &ErrorContext{
			RequestID:     "test-retry-456",
			ErrorType:     tc.errorType,
			AttemptCount:  tc.attemptCount,
			MaxRetries:    tc.maxRetries,
			OriginalError: errors.New("test error"),
		}

		result := erm.ShouldRetry(errorCtx)
		if result != tc.expected {
			t.Errorf("%s: expected %v, got %v", tc.description, tc.expected, result)
		}
	}
}

func TestErrorRecoveryManager_CalculateBackoffDelay(t *testing.T) {
	erm := NewErrorRecoveryManager(nil)

	testCases := []struct {
		attempt  int
		expected time.Duration
	}{
		{0, time.Second},
		{1, 2 * time.Second},
		{2, 4 * time.Second},
		{3, 8 * time.Second},
	}

	for _, tc := range testCases {
		delay := erm.calculateBackoffDelay(tc.attempt)
		if delay != tc.expected {
			t.Errorf("For attempt %d, expected delay %v, got %v", tc.attempt, tc.expected, delay)
		}
	}

	// Test max delay limit
	largeAttempt := 10
	delay := erm.calculateBackoffDelay(largeAttempt)
	if delay > erm.maxDelay {
		t.Errorf("Delay should not exceed maxDelay, got %v", delay)
	}
}

func TestErrorRecoveryManager_IsNetworkError(t *testing.T) {
	erm := NewErrorRecoveryManager(nil)

	testCases := []struct {
		err      error
		expected bool
	}{
		{nil, false},
		{errors.New("connection reset"), true},
		{errors.New("connection refused"), true},
		{errors.New("Connection REFUSED"), true}, // Case insensitive
		{errors.New("i/o timeout"), false},       // This is a timeout error, not network
		{errors.New("network is unreachable"), true},
		{errors.New("no route to host"), true},
		{errors.New("broken pipe"), true},
		{errors.New("eof"), false},            // EOF is now a separate error type, not network
		{errors.New("unexpected eof"), false}, // EOF is now a separate error type, not network
		{errors.New("random error"), false},
		{&net.OpError{Op: "dial"}, true},
		{&net.DNSError{}, true},
	}

	for _, tc := range testCases {
		result := erm.isNetworkError(tc.err)
		if result != tc.expected {
			t.Errorf("isNetworkError(%v) = %v, expected %v", tc.err, result, tc.expected)
		}
	}
}

func TestErrorRecoveryManager_IsTimeoutError(t *testing.T) {
	erm := NewErrorRecoveryManager(nil)

	testCases := []struct {
		err      error
		expected bool
	}{
		{nil, false},
		{context.DeadlineExceeded, true},
		{errors.New("timeout occurred"), true},
		{errors.New("i/o timeout"), true},
		{errors.New("context deadline exceeded"), true},
		{errors.New("read timeout"), true},
		{errors.New("write timeout"), true},
		{errors.New("random error"), false},
	}

	for _, tc := range testCases {
		result := erm.isTimeoutError(tc.err)
		if result != tc.expected {
			t.Errorf("isTimeoutError(%v) = %v, expected %v", tc.err, result, tc.expected)
		}
	}
}

func TestErrorRecoveryManager_SetRetryPolicy(t *testing.T) {
	erm := NewErrorRecoveryManager(nil)

	newMaxRetries := 5
	newBaseDelay := 2 * time.Second
	newMaxDelay := 60 * time.Second
	newBackoffFactor := 1.5

	erm.SetRetryPolicy(newMaxRetries, newBaseDelay, newMaxDelay, newBackoffFactor)

	if erm.maxRetries != newMaxRetries {
		t.Errorf("MaxRetries not updated correctly")
	}
	if erm.baseDelay != newBaseDelay {
		t.Errorf("BaseDelay not updated correctly")
	}
	if erm.maxDelay != newMaxDelay {
		t.Errorf("MaxDelay not updated correctly")
	}
	if erm.backoffFactor != newBackoffFactor {
		t.Errorf("BackoffFactor not updated correctly")
	}
}

func TestErrorRecoveryManager_GetErrorTypeName(t *testing.T) {
	erm := NewErrorRecoveryManager(nil)

	testCases := []struct {
		errorType ErrorType
		expected  string
	}{
		{ErrorTypeNetwork, "网络"},
		{ErrorTypeTimeout, "超时"},
		{ErrorTypeHTTP, "HTTP"},
		{ErrorTypeStream, "流处理"},
		{ErrorTypeAuth, "认证"},
		{ErrorTypeRateLimit, "限流"},
		{ErrorTypeParsing, "解析"},
		{ErrorTypeUnknown, "未知"},
	}

	for _, tc := range testCases {
		result := erm.getErrorTypeName(tc.errorType)
		if result != tc.expected {
			t.Errorf("getErrorTypeName(%v) = %s, expected %s", tc.errorType, result, tc.expected)
		}
	}
}

func TestErrorRecoveryManager_RecoverFromPartialData(t *testing.T) {
	erm := NewErrorRecoveryManager(nil)
	requestID := "test-recover-101"

	// Test with empty data
	erm.RecoverFromPartialData(requestID, nil, time.Second)

	// Test with data containing token info
	tokenData := []byte(`{"usage": {"input_tokens": 100, "output_tokens": 50}}`)
	erm.RecoverFromPartialData(requestID, tokenData, time.Second)

	// Test with regular data
	regularData := []byte("some response data without token info")
	erm.RecoverFromPartialData(requestID, regularData, time.Second)
}

// mockNetError for testing
type mockSyscallError struct {
	errno syscall.Errno
}

func (e *mockSyscallError) Error() string {
	return e.errno.Error()
}

func (e *mockSyscallError) Unwrap() error {
	return e.errno
}

func TestErrorRecoveryManager_ClassifyError_SyscallErrors(t *testing.T) {
	erm := NewErrorRecoveryManager(nil)

	testCases := []struct {
		errno    syscall.Errno
		expected ErrorType
	}{
		{syscall.ECONNREFUSED, ErrorTypeNetwork},
		{syscall.ECONNRESET, ErrorTypeNetwork},
		{syscall.ETIMEDOUT, ErrorTypeResponseTimeout}, // 系统超时归类为响应超时（不可重试）
		{syscall.ENETUNREACH, ErrorTypeNetwork},
		{syscall.EHOSTUNREACH, ErrorTypeNetwork},
	}

	for _, tc := range testCases {
		mockErr := &mockSyscallError{errno: tc.errno}
		errorCtx := erm.ClassifyError(mockErr, "test", "endpoint", "group", 1)

		if errorCtx.ErrorType != tc.expected {
			t.Errorf("For syscall error %v, expected %v, got %v", tc.errno, tc.expected, errorCtx.ErrorType)
		}
	}
}
