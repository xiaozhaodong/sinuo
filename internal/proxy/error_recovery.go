package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"sinuo/internal/tracking"
)

// ErrorType 错误类型枚举
type ErrorType int

const (
	ErrorTypeUnknown            ErrorType = iota
	ErrorTypeNetwork                      // 网络错误（连接失败等，可重试）
	ErrorTypeEOF                          // EOF 错误（连接中断，不可重试，避免重复计费）
	ErrorTypeConnectionTimeout            // 连接超时（可重试，未开始处理）
	ErrorTypeResponseTimeout              // 响应超时（不可重试，可能已计费）
	ErrorTypeTimeout                      // 超时错误（兼容旧代码，映射到响应超时）
	ErrorTypeHTTP                         // HTTP错误
	ErrorTypeServerError                  // 服务器错误（5xx）
	ErrorTypeStream                       // 流式处理错误
	ErrorTypeAuth                         // 认证错误
	ErrorTypeRateLimit                    // 限流错误
	ErrorTypeParsing                      // 解析错误
	ErrorTypeClientCancel                 // 客户端取消错误
	ErrorTypeNoHealthyEndpoints           // 没有健康端点可用
)

// ErrorContext 错误上下文信息
type ErrorContext struct {
	RequestID      string
	EndpointName   string
	AttemptCount   int
	ErrorType      ErrorType
	OriginalError  error
	RetryableAfter time.Duration // 建议重试延迟
	MaxRetries     int
}

// ErrorRecoveryManager 错误恢复管理器
// 负责识别错误类型、制定恢复策略、执行恢复操作
type ErrorRecoveryManager struct {
	usageTracker  *tracking.UsageTracker
	maxRetries    int
	baseDelay     time.Duration
	maxDelay      time.Duration
	backoffFactor float64
}

var (
	endpointReturnedStatusRegex = regexp.MustCompile(`endpoint returned error:\s*(\d{3})`)
	httpStatusRegex             = regexp.MustCompile(`(?:http(?:/[\d.]+)?(?:\s+status)?|status(?:\s+code)?)[:\s]+(\d{3})\b`)
)

// NewErrorRecoveryManager 创建错误恢复管理器
func NewErrorRecoveryManager(usageTracker *tracking.UsageTracker) *ErrorRecoveryManager {
	return &ErrorRecoveryManager{
		usageTracker:  usageTracker,
		maxRetries:    3,
		baseDelay:     time.Second,
		maxDelay:      30 * time.Second,
		backoffFactor: 2.0,
	}
}

// ClassifyError 分类错误类型并创建错误上下文
func (erm *ErrorRecoveryManager) ClassifyError(err error, requestID, endpoint string, legacyArgs ...interface{}) *ErrorContext {
	attempt := 0
	for _, arg := range legacyArgs {
		if value, ok := arg.(int); ok {
			attempt = value
		}
	}
	errorCtx := &ErrorContext{
		RequestID:     requestID,
		EndpointName:  endpoint,
		AttemptCount:  attempt,
		OriginalError: err,
		MaxRetries:    erm.maxRetries,
	}

	if err == nil {
		errorCtx.ErrorType = ErrorTypeUnknown
		return errorCtx
	}

	errStr := strings.ToLower(err.Error())

	// 首先检查客户端取消错误（最高优先级）
	if erm.isClientCancelError(err) {
		errorCtx.ErrorType = ErrorTypeClientCancel
		errorCtx.RetryableAfter = 0 // 客户端取消不可重试
		slog.Info(fmt.Sprintf("🚫 [客户端取消分类] [%s] 端点: %s, 尝试: %d, 错误: %v",
			requestID, endpoint, attempt, err))
		return errorCtx
	}

	// 检查 EOF 错误（优先级高于网络错误，不可重试，避免重复计费）
	if erm.isEOFError(err) {
		errorCtx.ErrorType = ErrorTypeEOF
		errorCtx.RetryableAfter = 0 // EOF 不可重试，可能已计费
		slog.Warn(fmt.Sprintf("📛 [EOF错误分类] [%s] 端点: %s, 尝试: %d, 连接中断不重试避免重复计费, 错误: %v",
			requestID, endpoint, attempt, err))
		return errorCtx
	}

	// 检查连接超时（可重试，因为还没开始处理）
	if erm.isConnectionTimeoutError(err) {
		errorCtx.ErrorType = ErrorTypeConnectionTimeout
		errorCtx.RetryableAfter = erm.calculateBackoffDelay(attempt)
		slog.Warn(fmt.Sprintf("🔌 [连接超时分类] [%s] 端点: %s, 尝试: %d, 连接超时可重试, 错误: %v",
			requestID, endpoint, attempt, err))
		return errorCtx
	}

	// 检查响应/读取超时（不可重试，可能已计费）
	if erm.isTimeoutError(err) {
		errorCtx.ErrorType = ErrorTypeResponseTimeout
		errorCtx.RetryableAfter = 0 // 响应超时不可重试，可能已计费
		slog.Warn(fmt.Sprintf("⏰ [响应超时分类] [%s] 端点: %s, 尝试: %d, 响应超时不重试避免重复计费, 错误: %v",
			requestID, endpoint, attempt, err))
		return errorCtx
	}

	// 网络错误分类（连接失败等，可重试）
	if erm.isNetworkError(err) {
		errorCtx.ErrorType = ErrorTypeNetwork
		errorCtx.RetryableAfter = erm.calculateBackoffDelay(attempt)
		slog.Warn(fmt.Sprintf("🌐 [网络错误分类] [%s] 端点: %s, 尝试: %d, 错误: %v",
			requestID, endpoint, attempt, err))
		return errorCtx
	}

	// 限流错误分类 - 高优先级，必须在服务器错误和HTTP通用检查之前
	// 仅使用语义关键词，避免用裸数字造成误判
	if strings.Contains(errStr, "rate") ||
		strings.Contains(errStr, "quota") || strings.Contains(errStr, "limit") ||
		strings.Contains(errStr, "too many requests") || strings.Contains(errStr, "rate_limit") ||
		strings.Contains(errStr, "throttle") || strings.Contains(errStr, "quota exceeded") {
		errorCtx.ErrorType = ErrorTypeRateLimit
		errorCtx.RetryableAfter = time.Minute // 限流错误建议等待1分钟
		slog.Warn(fmt.Sprintf("🚦 [限流错误分类] [%s] 端点: %s, 尝试: %d, 错误: %v",
			requestID, endpoint, attempt, err))
		return errorCtx
	}

	// 流处理错误分类 - 高优先级，必须在HTTP错误检查之前
	// 使用精确匹配，避免误判普通网络错误（如"upstream connect error"）
	if strings.HasPrefix(errStr, "stream_status:") ||
		strings.Contains(errStr, "streaming not supported") ||
		strings.Contains(errStr, "stream_error") ||
		strings.Contains(errStr, "sse") ||
		strings.Contains(errStr, "event-stream") ||
		strings.Contains(errStr, "stream parsing") {

		if strings.Contains(errStr, "streaming not supported") {
			// 特殊处理：这不是流处理本身的错误，而是环境不支持
			errorCtx.ErrorType = ErrorTypeUnknown
			errorCtx.RetryableAfter = 0 // 不可重试
			slog.Warn(fmt.Sprintf("🌊 [环境不支持] [%s] 端点: %s, 尝试: %d, 错误: %v",
				requestID, endpoint, attempt, err))
		} else {
			errorCtx.ErrorType = ErrorTypeStream
			errorCtx.RetryableAfter = erm.calculateBackoffDelay(attempt)
			slog.Warn(fmt.Sprintf("🌊 [流处理错误分类] [%s] 端点: %s, 尝试: %d, 错误: %v",
				requestID, endpoint, attempt, err))
		}
		return errorCtx
	}

	// 精确提取 HTTP 状态码，避免 "1500 bytes" / "port 8500" 这类误判
	if statusCode, ok := extractHTTPStatusCode(errStr); ok {
		switch {
		case statusCode == http.StatusTooManyRequests:
			errorCtx.ErrorType = ErrorTypeRateLimit
			errorCtx.RetryableAfter = time.Minute
			slog.Warn(fmt.Sprintf("🚦 [限流错误分类] [%s] 端点: %s, 尝试: %d, HTTP状态码: %d, 错误: %v",
				requestID, endpoint, attempt, statusCode, err))
		case statusCode >= 500 && statusCode <= 599:
			errorCtx.ErrorType = ErrorTypeServerError
			errorCtx.RetryableAfter = erm.calculateBackoffDelay(attempt)
			slog.Warn(fmt.Sprintf("🚨 [服务器错误分类] [%s] 端点: %s, 尝试: %d, HTTP状态码: %d, 错误: %v",
				requestID, endpoint, attempt, statusCode, err))
		case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden:
			errorCtx.ErrorType = ErrorTypeAuth
			errorCtx.RetryableAfter = 0
			slog.Error(fmt.Sprintf("🔐 [认证/权限错误分类] [%s] 端点: %s, 尝试: %d, HTTP状态码: %d, 错误: %v",
				requestID, endpoint, attempt, statusCode, err))
		default:
			errorCtx.ErrorType = ErrorTypeHTTP
			errorCtx.RetryableAfter = 0
			slog.Error(fmt.Sprintf("🔗 [HTTP错误分类] [%s] 端点: %s, 尝试: %d, HTTP状态码: %d, 错误: %v",
				requestID, endpoint, attempt, statusCode, err))
		}
		return errorCtx
	}

	// 认证/权限错误分类（无明确状态码时）
	if strings.Contains(errStr, "auth") || strings.Contains(errStr, "unauthorized") ||
		strings.Contains(errStr, "forbidden") || strings.Contains(errStr, "permission denied") {
		errorCtx.ErrorType = ErrorTypeAuth
		// 认证/权限错误不在同一端点重试，但允许切换端点
		errorCtx.RetryableAfter = 0
		slog.Error(fmt.Sprintf("🔐 [认证/权限错误分类] [%s] 端点: %s, 尝试: %d, 错误: %v",
			requestID, endpoint, attempt, err))
		return errorCtx
	}

	// HTTP错误分类（非数字状态码的文本场景）
	if strings.Contains(errStr, "endpoint returned error") ||
		strings.Contains(errStr, "http status") ||
		strings.Contains(errStr, "status code") {
		errorCtx.ErrorType = ErrorTypeHTTP
		// 非5xx HTTP错误通常不可重试
		slog.Error(fmt.Sprintf("🔗 [HTTP错误分类] [%s] 端点: %s, 尝试: %d, 错误: %v",
			requestID, endpoint, attempt, err))
		return errorCtx
	}

	if strings.Contains(errStr, "no endpoints available") || strings.Contains(errStr, "no healthy endpoints available") {
		errorCtx.ErrorType = ErrorTypeNoHealthyEndpoints
		errorCtx.RetryableAfter = 0 // 立即重试，不需要退避
		slog.Warn(fmt.Sprintf("🏥 [无可用端点] [%s] 端点: %s, 尝试: %d, 错误: %v",
			requestID, endpoint, attempt, err))
		return errorCtx
	}

	// 默认为未知错误
	errorCtx.ErrorType = ErrorTypeUnknown
	errorCtx.RetryableAfter = erm.calculateBackoffDelay(attempt)
	slog.Error(fmt.Sprintf("❓ [未知错误分类] [%s] 端点: %s, 尝试: %d, 错误: %v",
		requestID, endpoint, attempt, err))

	return errorCtx
}

func extractHTTPStatusCode(errStr string) (int, bool) {
	for _, re := range []*regexp.Regexp{endpointReturnedStatusRegex, httpStatusRegex} {
		match := re.FindStringSubmatch(errStr)
		if len(match) != 2 {
			continue
		}
		code, err := strconv.Atoi(match[1])
		if err != nil || code < 100 || code > 599 {
			continue
		}
		return code, true
	}
	return 0, false
}

// ShouldRetry 判断是否应该重试
func (erm *ErrorRecoveryManager) ShouldRetry(errorCtx *ErrorContext) bool {
	// 超过最大重试次数
	if errorCtx.AttemptCount >= errorCtx.MaxRetries {
		slog.Info(fmt.Sprintf("🛑 [重试判断] [%s] 超过最大重试次数 %d, 不再重试",
			errorCtx.RequestID, errorCtx.MaxRetries))
		return false
	}

	// 根据错误类型判断是否可重试
	switch errorCtx.ErrorType {
	case ErrorTypeClientCancel:
		// 客户端取消错误绝对不可重试
		slog.Info(fmt.Sprintf("🚫 [重试判断] [%s] 客户端取消错误不可重试", errorCtx.RequestID))
		return false

	case ErrorTypeEOF:
		// EOF 错误不可重试，避免重复计费（连接已中断，服务器可能已处理）
		slog.Info(fmt.Sprintf("📛 [重试判断] [%s] EOF错误不可重试，避免重复计费", errorCtx.RequestID))
		return false

	case ErrorTypeResponseTimeout, ErrorTypeTimeout:
		// 响应超时不可重试，服务器可能还在处理，重试会导致重复计费
		slog.Info(fmt.Sprintf("⏰ [重试判断] [%s] 响应超时不可重试，避免重复计费", errorCtx.RequestID))
		return false

	case ErrorTypeConnectionTimeout:
		// 连接超时可重试，因为还没开始处理
		slog.Info(fmt.Sprintf("🔌 [重试判断] [%s] 连接超时可重试, 尝试: %d/%d",
			errorCtx.RequestID, errorCtx.AttemptCount, errorCtx.MaxRetries))
		return true

	case ErrorTypeNetwork:
		// 网络错误（连接失败）可重试
		slog.Info(fmt.Sprintf("🌐 [重试判断] [%s] 网络错误可重试, 尝试: %d/%d",
			errorCtx.RequestID, errorCtx.AttemptCount, errorCtx.MaxRetries))
		return true

	case ErrorTypeServerError:
		// 服务器错误（5xx）可重试，但要注意可能已计费
		slog.Info(fmt.Sprintf("🚨 [重试判断] [%s] 服务器错误可重试, 尝试: %d/%d",
			errorCtx.RequestID, errorCtx.AttemptCount, errorCtx.MaxRetries))
		return true

	case ErrorTypeStream:
		// 流处理错误不可重试，数据已部分发送
		slog.Info(fmt.Sprintf("🌊 [重试判断] [%s] 流处理错误不可重试，数据已部分发送", errorCtx.RequestID))
		return false

	case ErrorTypeHTTP:
		// 非5xx HTTP错误通常不可重试
		slog.Info(fmt.Sprintf("❌ [重试判断] [%s] 非5xx HTTP错误不可重试", errorCtx.RequestID))
		return false

	case ErrorTypeRateLimit:
		// 限流错误可重试，但需要更长的延迟
		slog.Info(fmt.Sprintf("✅ [重试判断] [%s] 限流错误可重试, 尝试: %d/%d, 建议延迟: %v",
			errorCtx.RequestID, errorCtx.AttemptCount, errorCtx.MaxRetries, errorCtx.RetryableAfter))
		return true

	case ErrorTypeAuth:
		// 2025-12-10: 认证/权限错误不在同一端点重试，但支持切换端点（在 RetryManager 中处理）
		slog.Info(fmt.Sprintf("🔐 [重试判断] [%s] 认证/权限错误不在同一端点重试，但可切换端点", errorCtx.RequestID))
		return false

	case ErrorTypeParsing:
		// 解析错误可以尝试重试，可能是临时问题
		slog.Info(fmt.Sprintf("✅ [重试判断] [%s] 解析错误可重试, 尝试: %d/%d",
			errorCtx.RequestID, errorCtx.AttemptCount, errorCtx.MaxRetries))
		return true

	default:
		// 未知错误不重试，保守策略避免重复计费
		slog.Info(fmt.Sprintf("⚠️ [重试判断] [%s] 未知错误不重试，保守策略",
			errorCtx.RequestID))
		return false
	}
}

// ExecuteRetry 执行重试操作
func (erm *ErrorRecoveryManager) ExecuteRetry(ctx context.Context, errorCtx *ErrorContext) error {
	if errorCtx.RetryableAfter > 0 {
		slog.Info(fmt.Sprintf("⏳ [重试延迟] [%s] 等待 %v 后重试",
			errorCtx.RequestID, errorCtx.RetryableAfter))

		select {
		case <-time.After(errorCtx.RetryableAfter):
			// 延迟完成，继续重试
		case <-ctx.Done():
			// 上下文取消，停止重试
			return ctx.Err()
		}
	}

	// 记录重试状态
	if erm.usageTracker != nil && errorCtx.RequestID != "" {
		opts := tracking.UpdateOptions{
			EndpointName: &errorCtx.EndpointName,
			Status:       stringPtr("retry"),
			RetryCount:   &errorCtx.AttemptCount,
			HttpStatus:   intPtr(0),
		}
		erm.usageTracker.RecordRequestUpdate(errorCtx.RequestID, opts)
	}

	slog.Info(fmt.Sprintf("🔄 [执行重试] [%s] 第 %d 次重试, 端点: %s",
		errorCtx.RequestID, errorCtx.AttemptCount+1, errorCtx.EndpointName))

	return nil
}

// HandleFinalFailure 处理最终失败情况
func (erm *ErrorRecoveryManager) HandleFinalFailure(errorCtx *ErrorContext) {
	// 记录最终失败状态
	if erm.usageTracker != nil && errorCtx.RequestID != "" {
		status := "error"
		switch errorCtx.ErrorType {
		case ErrorTypeClientCancel:
			status = "cancelled"
		case ErrorTypeTimeout, ErrorTypeResponseTimeout:
			status = "timeout"
		case ErrorTypeConnectionTimeout:
			status = "connection_timeout"
		case ErrorTypeEOF:
			status = "eof_interrupted"
		case ErrorTypeAuth:
			status = "auth_error"
		case ErrorTypeRateLimit:
			status = "rate_limited"
		case ErrorTypeServerError:
			status = "server_error"
		case ErrorTypeStream:
			status = "stream_error"
		}

		opts := tracking.UpdateOptions{
			EndpointName: &errorCtx.EndpointName,
			Status:       &status,
			RetryCount:   &errorCtx.AttemptCount,
			HttpStatus:   intPtr(0),
		}
		erm.usageTracker.RecordRequestUpdate(errorCtx.RequestID, opts)
	}

	slog.Error(fmt.Sprintf("💀 [最终失败] [%s] 错误类型: %s, 尝试次数: %d, 端点: %s, 原始错误: %v",
		errorCtx.RequestID, erm.getErrorTypeName(errorCtx.ErrorType),
		errorCtx.AttemptCount, errorCtx.EndpointName, errorCtx.OriginalError))
}

// RecoverFromPartialData 从部分数据中恢复
func (erm *ErrorRecoveryManager) RecoverFromPartialData(requestID string, partialData []byte, processingTime time.Duration) {
	if len(partialData) == 0 {
		slog.Warn(fmt.Sprintf("⚠️ [部分数据恢复] [%s] 无部分数据可恢复", requestID))
		return
	}

	// 尝试从部分数据中提取有用信息
	dataStr := string(partialData)

	// 检查是否包含部分Token信息
	if strings.Contains(dataStr, "usage") || strings.Contains(dataStr, "tokens") {
		slog.Info(fmt.Sprintf("💾 [部分数据恢复] [%s] 从部分数据中发现Token信息, 长度: %d字节",
			requestID, len(partialData)))

		// 可以在这里添加部分Token解析逻辑
		if erm.usageTracker != nil {
			// 记录部分数据恢复状态
			opts := tracking.UpdateOptions{
				Status: stringPtr("partial_recovery"),
			}
			erm.usageTracker.RecordRequestUpdate(requestID, opts)
		}
	} else {
		slog.Info(fmt.Sprintf("📝 [部分数据恢复] [%s] 保存部分响应数据, 长度: %d字节, 处理时间: %v",
			requestID, len(partialData), processingTime))
	}
}

// isNetworkError 判断是否为网络错误（连接失败等，不包括 EOF）
func (erm *ErrorRecoveryManager) isNetworkError(err error) bool {
	if err == nil {
		return false
	}

	// 检查网络操作错误
	var netOpErr *net.OpError
	if errors.As(err, &netOpErr) {
		// 排除超时类型的 OpError
		if netOpErr.Timeout() {
			return false
		}
		return true
	}

	// 检查DNS错误
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}

	// 检查系统调用错误（排除超时）
	var syscallErr *syscall.Errno
	if errors.As(err, &syscallErr) {
		switch *syscallErr {
		case syscall.ECONNREFUSED, syscall.ECONNRESET,
			syscall.ENETUNREACH, syscall.EHOSTUNREACH:
			return true
		}
	}

	// 字符串匹配（不包括 EOF 和超时）
	errStr := strings.ToLower(err.Error())
	networkErrors := []string{
		"connection reset", "connection refused", "connection closed",
		"network is unreachable", "no route to host", "broken pipe",
		"upstream connect", "connect error",
		"stream reset",
	}

	for _, netErr := range networkErrors {
		if strings.Contains(errStr, netErr) {
			return true
		}
	}

	return false
}

// isEOFError 判断是否为 EOF 错误（连接中断，不可重试）
func (erm *ErrorRecoveryManager) isEOFError(err error) bool {
	if err == nil {
		return false
	}

	// 检查标准库 EOF 错误
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	// 字符串匹配
	errStr := strings.ToLower(err.Error())
	eofErrors := []string{
		"eof", "unexpected eof",
	}

	for _, eofErr := range eofErrors {
		if strings.Contains(errStr, eofErr) {
			return true
		}
	}

	return false
}

// isConnectionTimeoutError 判断是否为连接超时（可重试，因为还没开始处理）
func (erm *ErrorRecoveryManager) isConnectionTimeoutError(err error) bool {
	if err == nil {
		return false
	}

	errStr := strings.ToLower(err.Error())

	// 连接超时的特征：dial timeout, connection timeout
	connectionTimeoutErrors := []string{
		"dial timeout", "dial tcp", "connection timed out",
		"connect timeout", "dial i/o timeout",
	}

	for _, ctErr := range connectionTimeoutErrors {
		if strings.Contains(errStr, ctErr) {
			return true
		}
	}

	// 检查 net.OpError 中的 dial 操作超时
	var netOpErr *net.OpError
	if errors.As(err, &netOpErr) {
		if netOpErr.Op == "dial" && netOpErr.Timeout() {
			return true
		}
	}

	return false
}

// isTimeoutError 判断是否为超时错误
func (erm *ErrorRecoveryManager) isTimeoutError(err error) bool {
	if err == nil {
		return false
	}

	// 检查context.DeadlineExceeded
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	// 检查http.Client超时
	if errors.Is(err, http.ErrHandlerTimeout) {
		return true
	}

	// 检查系统调用超时错误
	var syscallErr *syscall.Errno
	if errors.As(err, &syscallErr) {
		if errors.Is(*syscallErr, syscall.ETIMEDOUT) {
			return true
		}
	}

	// 字符串匹配
	errStr := strings.ToLower(err.Error())
	timeoutErrors := []string{
		"timeout", "deadline exceeded", "context deadline exceeded",
		"i/o timeout", "read timeout", "write timeout", "operation timed out",
	}

	for _, timeoutErr := range timeoutErrors {
		if strings.Contains(errStr, timeoutErr) {
			return true
		}
	}

	return false
}

// isClientCancelError 判断是否为客户端取消错误
func (erm *ErrorRecoveryManager) isClientCancelError(err error) bool {
	if err == nil {
		return false
	}

	// 检查context.Canceled
	if errors.Is(err, context.Canceled) {
		return true
	}

	// 字符串匹配客户端取消相关错误
	errStr := strings.ToLower(err.Error())
	cancelErrors := []string{
		"context canceled", "canceled", "client disconnected",
		"connection closed by client", "client gone away",
	}

	for _, cancelErr := range cancelErrors {
		if strings.Contains(errStr, cancelErr) {
			return true
		}
	}

	return false
}

// calculateBackoffDelay 计算指数退避延迟
func (erm *ErrorRecoveryManager) calculateBackoffDelay(attempt int) time.Duration {
	if attempt <= 0 {
		return erm.baseDelay
	}

	// 指数退避: baseDelay * (backoffFactor ^ attempt)
	delay := time.Duration(float64(erm.baseDelay) *
		func() float64 {
			result := 1.0
			for i := 0; i < attempt; i++ {
				result *= erm.backoffFactor
			}
			return result
		}())

	// 限制最大延迟
	if delay > erm.maxDelay {
		delay = erm.maxDelay
	}

	return delay
}

// String 实现 ErrorType 的字符串方法，用于与重试策略的类型断言兼容
func (et ErrorType) String() string {
	switch et {
	case ErrorTypeNetwork:
		return "网络"
	case ErrorTypeEOF:
		return "EOF中断"
	case ErrorTypeConnectionTimeout:
		return "连接超时"
	case ErrorTypeResponseTimeout:
		return "响应超时"
	case ErrorTypeTimeout:
		return "超时"
	case ErrorTypeHTTP:
		return "HTTP"
	case ErrorTypeServerError:
		return "服务器"
	case ErrorTypeStream:
		return "流处理"
	case ErrorTypeAuth:
		return "认证"
	case ErrorTypeRateLimit:
		return "限流"
	case ErrorTypeParsing:
		return "解析"
	case ErrorTypeClientCancel:
		return "客户端取消"
	case ErrorTypeNoHealthyEndpoints:
		return "无健康端点"
	default:
		return "未知"
	}
}

// getErrorTypeName 获取错误类型名称（保持向后兼容）
func (erm *ErrorRecoveryManager) getErrorTypeName(errorType ErrorType) string {
	return errorType.String()
}

// SetRetryPolicy 设置重试策略
func (erm *ErrorRecoveryManager) SetRetryPolicy(maxRetries int, baseDelay, maxDelay time.Duration, backoffFactor float64) {
	erm.maxRetries = maxRetries
	erm.baseDelay = baseDelay
	erm.maxDelay = maxDelay
	erm.backoffFactor = backoffFactor

	slog.Info("⚙️ [重试策略] 已更新重试策略",
		"max_retries", maxRetries,
		"base_delay", baseDelay,
		"max_delay", maxDelay,
		"backoff_factor", backoffFactor)
}

// 辅助函数：创建string指针
func stringPtr(s string) *string {
	return &s
}

// 辅助函数：创建int指针
func intPtr(i int) *int {
	return &i
}
