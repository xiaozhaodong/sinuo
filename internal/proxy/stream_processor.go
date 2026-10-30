package proxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"sinuo/internal/proxy/response"
	"sinuo/internal/tracking"
	"sinuo/internal/utils"
)

// 缓冲区大小常量
const (
	StreamBufferSize     = 8192 // 8KB主缓冲区
	LineBufferInitSize   = 1024 // 1KB行缓冲区初始大小
	BackgroundBufferSize = 4096 // 4KB后台解析缓冲区
	DebugLineLimit       = 100  // 调试模式下最多保存100行SSE数据
	DebugLineDebugLimit  = 2000 // 开启 token_debug 时保留最近 2000 行，避免单请求无上限增长
)

// StreamProcessor 流式处理器核心结构体
type StreamProcessor struct {
	// 核心组件
	tokenParser    *TokenParser           // Token解析器，用于提取模型信息和使用统计
	usageTracker   *tracking.UsageTracker // 使用跟踪器，记录请求生命周期
	responseWriter http.ResponseWriter    // HTTP响应写入器
	flusher        http.Flusher           // HTTP刷新器，用于立即发送数据到客户端

	// 错误处理和恢复
	errorRecovery    *ErrorRecoveryManager // 错误恢复管理器
	lastAPIError     error                 // V2架构：最后一次API错误信息
	lastAPIErrorInfo *ErrorInfo            // 保留结构化错误元数据，供上层做 cooldown 判定

	// 请求标识信息
	requestID string // 请求唯一标识符
	endpoint  string // 端点名称

	// 流式处理状态
	startTime      time.Time // 处理开始时间
	bytesProcessed int64     // 已处理字节数
	lineBuffer     []byte    // SSE行缓冲区
	partialData    []byte    // 部分数据缓冲区，用于错误恢复

	// 并发控制
	parseWg    sync.WaitGroup // 等待组，确保后台解析完成
	parseMutex sync.Mutex     // 解析互斥锁，保护共享状态

	// 错误处理
	parseErrors    []error // 解析过程中的错误集合
	maxParseErrors int     // 最大允许解析错误数

	// 完成状态跟踪
	completionRecorded bool // 是否已经记录完成状态，防止重复记录
	firstTokenRecorded bool // 是否已记录首个有效流式响应耗时
	firstTokenRecorder func() bool
	completionRecorder func()
	phaseObserver      func(*ParseResult, int, time.Time)

	// 🔍 [调试缓冲区] 轻量级调试数据收集（仅在token解析失败时使用）
	debugLines []string // SSE行数据收集，默认最多保存DebugLineLimit行
	// 临时调试开关：启用 token_debug 时完整保留所有 SSE 行，便于定位 terminal/usage 丢失
	captureAllDebugLines bool
	// 去重日志签名：仅在Token统计变化时输出实时更新日志
	lastTokenLogSignature string

	// 下游断开后的短时尾部drain能力（仅按需启用）
	enableDownstreamTailDrain  bool
	downstreamTailDrainTimeout time.Duration
	cancelUpstreamRead         context.CancelFunc
	downstreamTailDrainTimer   *time.Timer
	downstreamDisconnected     bool
}

// NewStreamProcessor 创建新的流式处理器实例
func NewStreamProcessor(tokenParser *TokenParser, usageTracker *tracking.UsageTracker,
	w http.ResponseWriter, flusher http.Flusher, requestID, endpoint string) *StreamProcessor {

	sp := &StreamProcessor{
		tokenParser:          tokenParser,
		usageTracker:         usageTracker,
		responseWriter:       w,
		flusher:              flusher,
		errorRecovery:        NewErrorRecoveryManager(usageTracker),
		requestID:            requestID,
		endpoint:             endpoint,
		startTime:            time.Now(),
		lineBuffer:           make([]byte, 0, LineBufferInitSize),
		partialData:          make([]byte, 0, BackgroundBufferSize),
		maxParseErrors:       10,                                // 最多允许10个解析错误
		debugLines:           make([]string, 0, DebugLineLimit), // 🔍 [调试] 初始化调试缓冲区
		captureAllDebugLines: utils.IsTokenDebugEnabled(),
	}

	return sp
}

// EnableDownstreamTailDrain 启用下游断开后的短时尾部读取。
// 仅在调用方明确开启时生效，用于在下游提前断开时继续尝试收齐 response.completed / usage。
func (sp *StreamProcessor) EnableDownstreamTailDrain(timeout time.Duration, cancelUpstream context.CancelFunc) {
	if timeout <= 0 {
		timeout = time.Second
	}
	sp.enableDownstreamTailDrain = true
	sp.downstreamTailDrainTimeout = timeout
	sp.cancelUpstreamRead = cancelUpstream
}

// SetFirstTokenRecorder 设置首响耗时记录回调。
// 回调由生命周期管理器提供，确保耗时基于请求开始时间而不是流处理器创建时间。
func (sp *StreamProcessor) SetFirstTokenRecorder(recorder func()) {
	if recorder == nil {
		sp.firstTokenRecorder = nil
		return
	}
	sp.firstTokenRecorder = func() bool {
		recorder()
		return true
	}
}

// SetStreamTimingRecorders 设置首响与首响后完成耗时记录回调。
func (sp *StreamProcessor) SetStreamTimingRecorders(firstTokenRecorder func() bool, completionRecorder func()) {
	sp.firstTokenRecorder = firstTokenRecorder
	sp.completionRecorder = completionRecorder
}

func (sp *StreamProcessor) beginDownstreamTailDrain(trigger string) {
	if sp.downstreamDisconnected {
		return
	}

	sp.downstreamDisconnected = true
	slog.Warn(fmt.Sprintf("⚠️ [下游断开Drain] [%s] 检测到下游断开，继续读取上游尾部，超时: %s, 触发: %s",
		sp.requestID, sp.downstreamTailDrainTimeout, trigger))

	if sp.cancelUpstreamRead != nil {
		sp.downstreamTailDrainTimer = time.AfterFunc(sp.downstreamTailDrainTimeout, func() {
			slog.Warn(fmt.Sprintf("⏰ [下游断开Drain超时] [%s] 未在 %s 内收齐上游尾部，停止继续读取",
				sp.requestID, sp.downstreamTailDrainTimeout))
			sp.cancelUpstreamRead()
		})
	}
}

func (sp *StreamProcessor) stopDownstreamTailDrain() {
	if sp.downstreamTailDrainTimer != nil {
		sp.downstreamTailDrainTimer.Stop()
		sp.downstreamTailDrainTimer = nil
	}
}

func (sp *StreamProcessor) finalizeTerminalAfterDisconnect(trigger error) (*tracking.TokenUsage, error) {
	sp.waitForBackgroundParsing()
	tokenUsage := sp.getFinalTokenUsage()

	modelName := sp.tokenParser.GetModelName()
	if modelName == "" {
		modelName = "unknown"
	}

	completeness := sp.tokenParser.GetStreamCompleteness()
	if completeness.IsComplete {
		slog.Info(fmt.Sprintf("✅ [完成后断连] [%s] 已确认 terminal event，按完成处理，模型: %s, 触发错误: %v",
			sp.requestID, modelName, trigger))
	} else {
		slog.Warn(fmt.Sprintf("⚠️ [终态质量异常] [%s] 已收到 terminal event，停止尾部读取并保留完整性错误: %s",
			sp.requestID, completeness.Reason))
	}
	return tokenUsage, nil
}

// ProcessStream 实现边接收边转发的8KB缓冲区流式处理
// 这是核心方法，实现真正的流式处理机制
func (sp *StreamProcessor) ProcessStream(ctx context.Context, resp *http.Response) (*tracking.TokenUsage, error) {
	defer resp.Body.Close()
	defer sp.waitForBackgroundParsing() // 确保所有后台解析完成
	defer sp.stopDownstreamTailDrain()

	// 🔧 [解压缩修复] 创建响应处理器并获取解压缩的流式读取器
	processor := response.NewProcessor()
	decompressedReader, err := processor.DecompressStreamReader(resp)
	if err != nil {
		return nil, fmt.Errorf("🗜️ [解压缩失败] [%s] 端点: %s, 错误: %w", sp.requestID, sp.endpoint, err)
	}
	defer decompressedReader.Close() // 确保解压缩读取器被关闭

	// 记录解压缩状态
	contentEncoding := resp.Header.Get("Content-Encoding")
	if contentEncoding != "" {
		slog.Info(fmt.Sprintf("🗜️ [流式解压] [%s] 端点: %s, 编码: %s", sp.requestID, sp.endpoint, contentEncoding))
	}

	// 初始化8KB缓冲区，使用解压缩后的读取器
	buffer := make([]byte, StreamBufferSize)
	reader := bufio.NewReader(decompressedReader)

	// 记录流处理开始
	slog.Info(fmt.Sprintf("🌊 [流式处理] [%s] 开始流式处理，端点: %s", sp.requestID, sp.endpoint))

	// 主流式处理循环
	for {
		// 检查context取消 - 优先级最高
		if !sp.downstreamDisconnected {
			select {
			case <-ctx.Done():
				if sp.enableDownstreamTailDrain {
					sp.beginDownstreamTailDrain(ctx.Err().Error())
					if sp.tokenParser.HasTerminalEvent() {
						return sp.finalizeTerminalAfterDisconnect(ctx.Err())
					}
				} else {
					// 客户端取消，进入优雅取消处理
					return sp.handleCancellationV2(ctx, ctx.Err())
				}
			default:
				// 继续正常处理
			}
		}

		// 1. 从响应中读取数据到8KB缓冲区
		n, err := reader.Read(buffer)

		if n > 0 {
			chunk := buffer[:n]
			sp.observeStreamRead(chunk)

			// 保存部分数据用于错误恢复
			sp.savePartialData(chunk)

			// 2. 立即转发到客户端 - 这是关键！不等待完整响应
			if !sp.downstreamDisconnected {
				if writeErr := sp.forwardToClient(chunk); writeErr != nil {
					if sp.enableDownstreamTailDrain {
						sp.beginDownstreamTailDrain(writeErr.Error())
					} else {
						// 使用错误恢复管理器处理转发错误
						errorCtx := sp.errorRecovery.ClassifyError(writeErr, sp.requestID, sp.endpoint, 0)
						sp.errorRecovery.HandleFinalFailure(errorCtx)
						slog.Error(fmt.Sprintf("❌ [流式错误] [%s] 转发到客户端失败: %v", sp.requestID, writeErr))
						return nil, fmt.Errorf("failed to forward to client: %w", writeErr)
					}
				}
			}

			// 3. 并行解析Token信息 - 不影响转发性能
			sp.parseTokensFromChunk(chunk)

			// 4. 更新处理状态
			sp.bytesProcessed += int64(n)

			if sp.tokenParser.HasTerminalEvent() {
				if sp.downstreamDisconnected {
					return sp.finalizeTerminalAfterDisconnect(context.Canceled)
				}
				finalTokenUsage := sp.getFinalTokenUsage()
				slog.Info(fmt.Sprintf("✅ [流式终态] [%s] 端点: %s, 已收到 terminal event，主动结束上游读取，已处理 %d 字节",
					sp.requestID, sp.endpoint, sp.bytesProcessed))
				return finalTokenUsage, nil
			}
		}

		// 处理读取结束和错误
		if err == io.EOF {
			// 等待所有后台解析完成
			sp.waitForBackgroundParsing()

			// 获取最终的 Token 使用信息
			finalTokenUsage := sp.getFinalTokenUsage()

			slog.Info(fmt.Sprintf("✅ [流式完成] [%s] 端点: %s, 流处理正常完成，已处理 %d 字节",
				sp.requestID, sp.endpoint, sp.bytesProcessed))
			return finalTokenUsage, nil
		}

		if err != nil {
			if sp.downstreamDisconnected && sp.tokenParser.HasTerminalEvent() {
				return sp.finalizeTerminalAfterDisconnect(err)
			}
			// 网络中断或其他错误，尝试部分数据处理
			return sp.handlePartialStreamV2(err)
		}
	}
}

// forwardToClient 立即转发数据到客户端
func (sp *StreamProcessor) forwardToClient(data []byte) error {
	// 写入数据到响应
	if _, err := sp.responseWriter.Write(data); err != nil {
		return err
	}

	// 立即刷新，确保数据立即发送到客户端
	sp.flusher.Flush()

	return nil
}

// parseTokensFromChunk 同步解析当前数据块中的 SSE 行。
// 这里不启动后台 goroutine，仅在当前读循环内增量更新解析状态。
func (sp *StreamProcessor) parseTokensFromChunk(data []byte) {
	parseBuffer := make([]byte, len(data))
	copy(parseBuffer, data)

	sp.parseMutex.Lock()
	defer sp.parseMutex.Unlock()

	for _, b := range parseBuffer {
		sp.lineBuffer = append(sp.lineBuffer, b)

		if b == '\n' {
			line := strings.TrimSpace(string(sp.lineBuffer))
			sp.processSSELine(line)
			sp.lineBuffer = sp.lineBuffer[:0]
		}
	}
}

// processSSELine 处理单个SSE行
// 修改版本：仅进行 Token 解析，不再直接记录到 usageTracker
func (sp *StreamProcessor) processSSELine(line string) {
	// 🔍 [调试数据收集] 轻量级收集SSE行数据（无性能影响）
	sp.appendDebugLine(line)

	if isFirstResponseDataLine(line) {
		sp.recordFirstStreamResponse()
	}

	// ✅ 使用V2架构进行解析
	result := sp.tokenParser.ParseSSELineV2(line)
	if sp.phaseObserver != nil {
		sp.phaseObserver(result, 0, time.Now())
	}

	if result != nil {
		if result.HasStreamOutput || result.HasVisibleText || result.HasFallbackOutput {
			sp.recordFirstStreamResponse()
		}

		// ✅ 检查是否有错误信息
		if result.ErrorInfo != nil {
			// V2架构：处理API错误信息
			slog.Error(fmt.Sprintf("❌ [API错误V2] [%s] 类型: %s, 消息: %s",
				sp.requestID, result.ErrorInfo.Type, result.ErrorInfo.Message))

			// 将错误信息存储，供上层生命周期管理器处理
			sp.lastAPIErrorInfo = result.ErrorInfo
			sp.lastAPIError = fmt.Errorf("API错误 %s: %s", result.ErrorInfo.Type, result.ErrorInfo.Message)
			return
		}

		// ✅ 处理正常Token信息
		if result.TokenUsage != nil {
			// V2架构：直接使用ParseResult，无需类型转换
			trackingTokens := result.TokenUsage
			modelName := result.ModelName

			// 确保模型名称不为空（用于日志）
			if modelName == "" {
				modelName = "unknown"
			}

			// ✅ 移除completionRecorded限制，每次都更新最新token统计
			signature := fmt.Sprintf("%s:%d:%d:%d:%d:%d:%d",
				modelName,
				trackingTokens.InputTokens,
				trackingTokens.OutputTokens,
				trackingTokens.CacheCreationTokens,
				trackingTokens.CacheReadTokens,
				trackingTokens.CacheCreation5mTokens,
				trackingTokens.CacheCreation1hTokens,
			)
			if signature != sp.lastTokenLogSignature {
				slog.Debug(fmt.Sprintf("🔄 [Token实时更新] [%s] 模型: %s, 输入: %d, 输出: %d, 缓存创建: %d, 缓存读取: %d",
					sp.requestID, modelName, trackingTokens.InputTokens, trackingTokens.OutputTokens,
					trackingTokens.CacheCreationTokens, trackingTokens.CacheReadTokens))
				sp.lastTokenLogSignature = signature
			}

			// ✅ 移除或注释掉这个字段，因为不再需要
			// sp.completionRecorded = true
		}
	}
}

func isFirstResponseDataLine(line string) bool {
	if !strings.HasPrefix(line, "data:") {
		return false
	}
	data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	return data != "" && !strings.HasPrefix(data, "[DONE]")
}

func (sp *StreamProcessor) recordFirstStreamResponse() {
	if sp.firstTokenRecorded {
		return
	}

	if sp.firstTokenRecorder != nil {
		if !sp.firstTokenRecorder() {
			return
		}
	}
	sp.firstTokenRecorded = true
}

func (sp *StreamProcessor) appendDebugLine(line string) {
	limit := DebugLineLimit
	if sp.captureAllDebugLines {
		limit = DebugLineDebugLimit
	}
	if limit <= 0 {
		return
	}

	if len(sp.debugLines) >= limit {
		copy(sp.debugLines, sp.debugLines[1:])
		sp.debugLines = sp.debugLines[:limit-1]
	}
	sp.debugLines = append(sp.debugLines, line)
}

// ensureRequestCompletion 确保请求完成状态被记录（fallback机制）
// 🚫 DEPRECATED: 已被 getFinalTokenUsage() 替代，此方法已完全移除违规调用
// 此方法不再执行任何操作，仅保留方法签名以维持兼容性
func (sp *StreamProcessor) ensureRequestCompletion() {
	// ⚠️ 此方法已完全弃用，所有功能已迁移到 getFinalTokenUsage() 方法
	// 原因：违反单一责任原则，直接调用 usageTracker 而非通过生命周期管理器
	//
	// 新的架构要求：
	// 1. StreamProcessor 只负责解析和返回Token信息
	// 2. Handler 调用生命周期管理器记录完成状态
	// 3. 不再有任何组件直接调用 usageTracker

	slog.Debug(fmt.Sprintf("⚠️ [已弃用] [%s] ensureRequestCompletion已弃用，请使用getFinalTokenUsage", sp.requestID))
}

// attemptUsageRecovery 异步尝试从debug文件恢复完整的usage信息
// 🔧 [Fallback修复] 等待debug文件写入完成后，尝试提取完整的token使用统计并更新数据库
func (sp *StreamProcessor) attemptUsageRecovery() {
	// 等待一段时间确保debug文件写入完成
	time.Sleep(2 * time.Second)

	modelName := sp.tokenParser.GetModelName()
	if modelName == "" {
		modelName = "unknown"
	}

	// 尝试从debug文件恢复usage信息
	err := utils.RecoverAndUpdateUsage(sp.requestID, modelName, sp.usageTracker)
	if err != nil {
		slog.Warn(fmt.Sprintf("🔧 [Recovery失败] [%s] 从debug文件恢复usage失败: %v", sp.requestID, err))
		return
	}

	slog.Info(fmt.Sprintf("🔧 [Recovery成功] [%s] 成功从debug文件恢复完整的usage信息", sp.requestID))
}

// classifyStreamError 智能状态分类方法
// 确保 context canceled 正确归类为 cancelled 而不是 error
func (sp *StreamProcessor) classifyStreamError(err error, tokenUsage *tracking.TokenUsage) string {
	if err == nil {
		return "success"
	}

	errStr := err.Error()

	// ✅ 优先检测取消状态（修复核心问题）
	if errStr == "context canceled" || strings.Contains(errStr, "context canceled") {
		// 检查是否有token信息，用于日志区分
		hasTokens := tokenUsage != nil && (tokenUsage.InputTokens > 0 || tokenUsage.OutputTokens > 0 ||
			tokenUsage.CacheCreationTokens > 0 || tokenUsage.CacheReadTokens > 0)

		if hasTokens {
			slog.Warn(fmt.Sprintf("⚠️ [已计费取消] [%s] 用户取消但已产生token计费", sp.requestID))
		} else {
			slog.Info(fmt.Sprintf("🚫 [早期取消] [%s] 用户早期取消，未产生计费", sp.requestID))
		}

		return "cancelled" // ✅ 使用现有cancelled状态
	}

	// 其他错误保持原有逻辑
	if strings.Contains(errStr, "timeout") || strings.Contains(errStr, "deadline exceeded") {
		return "timeout"
	}

	return "error"
}

// handlePartialStream 处理部分数据流中断情况（修复版本）
// 🚫 DEPRECATED: 已被 handlePartialStreamV2() 替代，此方法已完全移除违规调用
// 当网络中断或其他错误发生时，不再进行错误分类，让上层统一处理
func (sp *StreamProcessor) handlePartialStream(err error) error {
	// ⚠️ 此方法已弃用，请使用 handlePartialStreamV2() 方法
	// 原因：违反生命周期管理器架构，直接调用 usageTracker 而非返回Token信息

	// 记录流处理中断但不做任何usageTracker调用
	slog.Warn(fmt.Sprintf("⚠️ [流式中断] [%s] 流处理中断: %v, 已处理 %d 字节. 错误将由上层统一处理.",
		sp.requestID, err, sp.bytesProcessed))

	// 等待所有后台解析完成
	sp.waitForBackgroundParsing()

	// 尝试从部分数据中恢复有用信息
	if len(sp.partialData) > 0 {
		sp.errorRecovery.RecoverFromPartialData(sp.requestID, sp.partialData, time.Since(sp.startTime))
	}

	// 直接返回错误，让调用者(handler)通过生命周期管理器来分类和处理最终失败
	return err
}

// ProcessStreamWithRetry 流式处理入口（无重试版本）
// 2025-12-11: 移除无效的重试循环，流式传输阶段不重试以避免重复计费
// 原因：resp.Body 在 ProcessStream 返回后会被关闭，重试无法重新读取
// 返回值：(finalTokenUsage *tracking.TokenUsage, modelName string, err error)
func (sp *StreamProcessor) ProcessStreamWithRetry(ctx context.Context, resp *http.Response) (*tracking.TokenUsage, string, error) {
	// 执行流式处理（单次调用，不重试）
	finalTokenUsage, err := sp.ProcessStream(ctx, resp)

	if err == nil {
		// ✅ 检查是否在处理过程中遇到了API错误（如SSE错误事件）
		if sp.lastAPIError != nil {
			modelName := sp.tokenParser.GetModelName()
			if modelName == "" {
				modelName = "unknown"
			}

			// 智能错误包装：检查API错误是否已被包装，避免重复包装
			if strings.HasPrefix(sp.lastAPIError.Error(), "stream_status:") {
				return finalTokenUsage, modelName, sp.lastAPIError
			}

			// 根据API错误内容智能确定状态
			status := classifyAccountStreamAPIError(sp.lastAPIErrorInfo, sp.lastAPIError.Error())
			var usageLimit *accountUsageLimitWindow
			if window, ok := accountUsageLimitWindowFromErrorInfo(sp.lastAPIErrorInfo, time.Now()); ok {
				usageLimit = &window
			}

			wrappedErr := &accountStreamStatusError{
				status:     status,
				modelName:  modelName,
				usageLimit: usageLimit,
				cause:      sp.lastAPIError,
			}
			return finalTokenUsage, modelName, wrappedErr
		}

		// 🆕 [流完整性检测] 2025-12-11
		// 检查流是否完整，即使 err == nil 也可能是不完整的流
		completeness := sp.tokenParser.GetStreamCompleteness()
		modelName := sp.tokenParser.GetModelName()
		// 🔧 [模型语义修复] 2025-12-26: 使用 "unknown" 而非 "default"
		// 当没有收到 message_start 事件时，模型应该标记为 "unknown"
		// 上层会根据这个值决定是否回退使用请求体中的模型
		if modelName == "" {
			modelName = "unknown"
		}

		if !completeness.IsComplete {
			// 流不完整，返回结构化错误让上层设置 failure_reason
			slog.Warn(fmt.Sprintf("⚠️ [流不完整] [%s] %s", sp.requestID, completeness.Reason))
			if sp.completionRecorder != nil {
				sp.completionRecorder()
			}

			// 🔧 [结构化错误] 2025-12-11: 使用 StreamIncompleteError 替代字符串格式
			streamErr := &StreamIncompleteError{
				FailureReason: completeness.FailureReason,
				ModelName:     modelName,
				Reason:        completeness.Reason,
			}
			return finalTokenUsage, modelName, streamErr
		}

		// 处理成功且流完整
		if sp.completionRecorder != nil {
			sp.completionRecorder()
		}
		return finalTokenUsage, modelName, nil
	}

	// 流式处理失败，进行错误包装
	tokenUsage := sp.getFinalTokenUsage()
	modelName := sp.tokenParser.GetModelName()
	if modelName == "" {
		modelName = "unknown"
	}

	// 智能错误包装：检查错误是否已被包装，避免重复包装
	if strings.HasPrefix(err.Error(), "stream_status:") {
		return tokenUsage, modelName, err
	}

	// 根据错误内容智能确定状态
	status := "error"
	errStr := err.Error()
	if strings.Contains(errStr, "timeout") || strings.Contains(errStr, "deadline") {
		status = "timeout"
	} else if strings.Contains(errStr, "cancel") {
		status = "cancelled"
	} else if strings.Contains(errStr, "network") || strings.Contains(errStr, "connection") {
		status = "network_error"
	}

	wrappedErr := fmt.Errorf("stream_status:%s:model:%s: %w", status, modelName, err)
	return tokenUsage, modelName, wrappedErr
}

// waitForBackgroundParsing 等待所有后台解析完成
func (sp *StreamProcessor) waitForBackgroundParsing() {
	// 等待所有后台goroutine完成
	sp.parseWg.Wait()

	// 处理剩余的行缓冲区数据
	sp.parseMutex.Lock()
	if len(sp.lineBuffer) > 0 {
		line := strings.TrimSpace(string(sp.lineBuffer))
		if len(line) > 0 {
			sp.processSSELine(line)
		}
		sp.lineBuffer = sp.lineBuffer[:0]
	}

	// ✅ 强制刷新缓存的待处理事件
	// 确保即使上游没有发送终止空行，也能解析完整的 usage 信息
	if result := sp.tokenParser.FlushPendingEvent(); result != nil {
		if sp.phaseObserver != nil {
			sp.phaseObserver(result, 0, time.Now())
		}
		// 处理刷新后的解析结果
		if result.ErrorInfo != nil {
			slog.Error(fmt.Sprintf("❌ [Flush错误] [%s] 类型: %s, 消息: %s",
				sp.requestID, result.ErrorInfo.Type, result.ErrorInfo.Message))
			sp.lastAPIErrorInfo = result.ErrorInfo
			sp.lastAPIError = fmt.Errorf("API错误 %s: %s", result.ErrorInfo.Type, result.ErrorInfo.Message)
		} else if result.TokenUsage != nil {
			slog.Debug(fmt.Sprintf("🔄 [Flush成功] [%s] 成功解析待处理事件的Token信息", sp.requestID))
		}
	}

	// 🔧 [Responses兼容] 某些上游会在 SSE 流尾部直接附带完整 JSON 响应对象。
	// 当标准 terminal 事件未命中时，从原始尾部窗口中恢复 usage 与完成状态。
	if len(sp.partialData) > 0 && sp.tokenParser.RecoverResponsesUsageFromRawTail(string(sp.partialData)) {
		slog.Info(fmt.Sprintf("🔧 [Responses恢复] [%s] 从流尾原始 JSON 恢复 usage 信息", sp.requestID))
	}

	sp.parseMutex.Unlock()
}

// savePartialData 保存部分数据用于错误恢复
func (sp *StreamProcessor) savePartialData(chunk []byte) {
	// 限制部分数据缓冲区大小，防止内存过度使用
	const maxPartialDataSize = 64 * 1024 // 64KB

	sp.parseMutex.Lock()
	defer sp.parseMutex.Unlock()

	// 如果添加新数据会超过限制，则移除旧数据
	if len(sp.partialData)+len(chunk) > maxPartialDataSize {
		// 保留最后的32KB数据，丢弃更早的数据
		keepSize := maxPartialDataSize/2 - len(chunk)
		if keepSize > 0 && keepSize < len(sp.partialData) {
			copy(sp.partialData, sp.partialData[len(sp.partialData)-keepSize:])
			sp.partialData = sp.partialData[:keepSize]
		} else {
			sp.partialData = sp.partialData[:0]
		}
	}

	// 添加新的数据块
	sp.partialData = append(sp.partialData, chunk...)
}

// GetProcessingStats 获取处理统计信息
func (sp *StreamProcessor) GetProcessingStats() map[string]interface{} {
	return map[string]interface{}{
		"request_id":       sp.requestID,
		"endpoint":         sp.endpoint,
		"bytes_processed":  sp.bytesProcessed,
		"processing_time":  time.Since(sp.startTime),
		"parse_errors":     len(sp.parseErrors),
		"max_parse_errors": sp.maxParseErrors,
	}
}

// Reset 重置处理器状态，用于复用
func (sp *StreamProcessor) Reset() {
	sp.parseMutex.Lock()
	defer sp.parseMutex.Unlock()

	sp.startTime = time.Now()
	sp.bytesProcessed = 0
	sp.lineBuffer = sp.lineBuffer[:0]
	sp.partialData = sp.partialData[:0] // 重置部分数据缓冲区
	sp.parseErrors = sp.parseErrors[:0]
	sp.lastTokenLogSignature = ""
	sp.firstTokenRecorded = false
	sp.lastAPIError = nil
	sp.lastAPIErrorInfo = nil

	// 重置TokenParser状态
	if sp.tokenParser != nil {
		sp.tokenParser.Reset()
	}

	slog.Info(fmt.Sprintf("🔄 [处理器重置] [%s] 流处理器已重置", sp.requestID))
}

func classifyAccountStreamAPIError(info *ErrorInfo, errorText string) string {
	if info != nil {
		switch strings.TrimSpace(strings.ToLower(info.Type)) {
		case "usage_limit_reached", "rate_limited", "rate_limit_exceeded":
			return "rate_limited"
		case "timeout", "deadline_exceeded":
			return "timeout"
		case "cancelled", "canceled":
			return "cancelled"
		case "auth_error", "authentication_error", "unauthorized", "forbidden":
			return "auth_error"
		}
	}

	switch {
	case strings.Contains(errorText, "rate"), strings.Contains(errorText, "429"):
		return "rate_limited"
	case strings.Contains(errorText, "timeout"), strings.Contains(errorText, "deadline"):
		return "timeout"
	case strings.Contains(errorText, "cancel"):
		return "cancelled"
	case strings.Contains(errorText, "auth"), strings.Contains(errorText, "401"):
		return "auth_error"
	default:
		return "stream_error"
	}
}

func accountUsageLimitWindowFromErrorInfo(info *ErrorInfo, now time.Time) (accountUsageLimitWindow, bool) {
	if info == nil {
		return accountUsageLimitWindow{}, false
	}
	return buildAccountUsageLimitWindow(info.Type, info.PlanType, info.ResetsAt, info.ResetsInSeconds, now)
}

// handleCancellation 处理客户端取消请求 - Phase 2 优雅取消处理器
// 🚫 DEPRECATED: 已被 handleCancellationV2() 替代，不再直接调用 usageTracker
func (sp *StreamProcessor) handleCancellation(ctx context.Context, cancelErr error) error {
	// ⚠️ 此方法已弃用，请使用 handleCancellationV2() 方法
	// 原因：违反生命周期管理器架构，通过 collectAvailableInfo 间接调用 usageTracker

	slog.Info(fmt.Sprintf("🚫 [客户端取消] [%s] 检测到客户端取消: %v", sp.requestID, cancelErr))

	// 等待后台解析完成，但不超过超时时间
	if finished := sp.waitForParsingWithTimeout(2 * time.Second); finished {
		// 成功等待解析完成，调用新版本方法获取Token信息
		tokenUsage, err := sp.collectAvailableInfoV2(cancelErr, "cancelled_with_data")
		_ = tokenUsage // 忽略Token信息，保持原接口兼容
		return err
	} else {
		// 超时未完成，调用新版本方法获取Token信息
		tokenUsage, err := sp.collectAvailableInfoV2(cancelErr, "cancelled_timeout")
		_ = tokenUsage // 忽略Token信息，保持原接口兼容
		return err
	}
}

// waitForParsingWithTimeout 带超时的等待解析完成 - Phase 2 超时等待机制
func (sp *StreamProcessor) waitForParsingWithTimeout(timeout time.Duration) bool {
	done := make(chan struct{})

	go func() {
		sp.parseWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		slog.Info(fmt.Sprintf("✅ [解析完成] [%s] 后台解析在取消前完成", sp.requestID))
		return true
	case <-time.After(timeout):
		slog.Warn(fmt.Sprintf("⏰ [解析超时] [%s] 后台解析在 %v 内未完成", sp.requestID, timeout))
		return false
	}
}

// collectAvailableInfo 智能信息收集 - Phase 2 分阶段保存逻辑
// 🚫 DEPRECATED: 已被 collectAvailableInfoV2() 替代，此方法已完全移除违规调用
func (sp *StreamProcessor) collectAvailableInfo(cancelErr error, status string) error {
	// ⚠️ 此方法已完全弃用，请使用 collectAvailableInfoV2() 方法
	// 原因：违反生命周期管理器架构，直接调用 usageTracker 而非返回Token信息
	//
	// 新的架构要求：
	// 1. StreamProcessor 只负责收集Token信息
	// 2. Handler 调用生命周期管理器记录状态
	// 3. 不再有任何组件直接调用 usageTracker

	slog.Debug(fmt.Sprintf("⚠️ [已弃用] [%s] collectAvailableInfo已弃用，请使用collectAvailableInfoV2", sp.requestID))
	return cancelErr
}

// getFinalTokenUsage 获取最终的Token使用信息（修复版）
func (sp *StreamProcessor) getFinalTokenUsage() *tracking.TokenUsage {
	sp.parseMutex.Lock()
	defer sp.parseMutex.Unlock()

	// 尝试从TokenParser获取最终使用统计
	finalUsage := sp.tokenParser.GetFinalUsage()

	// 🔍 [Fallback检测] 检查是否使用了fallback机制
	if sp.tokenParser.IsFallbackUsed() {
		slog.Warn(fmt.Sprintf("🚨 [Fallback检测] [%s] 使用了message_start fallback机制，保存完整调试数据", sp.requestID))

		// 🔧 [Fallback修复] fallback时使用完整的响应数据，不受100行限制
		if len(sp.partialData) > 0 {
			// 将完整的原始数据按行分割
			fullLines := strings.Split(string(sp.partialData), "\n")
			slog.Info(fmt.Sprintf("🔧 [完整数据] [%s] 使用%d字节完整数据替代100行限制数据", sp.requestID, len(sp.partialData)))

			// 使用完整数据保存debug文件
			utils.WriteStreamDebugResponse(sp.requestID, sp.endpoint, fullLines, sp.bytesProcessed)
		} else {
			// 兜底：如果没有完整数据，使用现有的debug数据
			utils.WriteStreamDebugResponse(sp.requestID, sp.endpoint, sp.debugLines, sp.bytesProcessed)
		}

		// 🔧 [Fallback修复] 异步尝试从debug文件恢复完整的usage信息
		go sp.attemptUsageRecovery()
	}

	if finalUsage != nil {
		// ✅ 检查是否有真实的Token使用
		hasRealTokens := finalUsage.InputTokens > 0 || finalUsage.OutputTokens > 0 ||
			finalUsage.CacheCreationTokens > 0 || finalUsage.CacheReadTokens > 0

		if hasRealTokens {
			// 有真实token信息，记录详细日志
			modelName := sp.tokenParser.GetModelName()
			if modelName == "" {
				modelName = "unknown"
			}
			slog.Info(fmt.Sprintf("🪙 [Token最终统计] [%s] 流式处理完成 - 模型: %s, 输入: %d, 输出: %d, 缓存创建: %d, 缓存读取: %d",
				sp.requestID, modelName, finalUsage.InputTokens, finalUsage.OutputTokens, finalUsage.CacheCreationTokens, finalUsage.CacheReadTokens))
			return finalUsage
		} else {
			// 有finalUsage结构但无实际token，返回nil
			slog.Info(fmt.Sprintf("🎯 [无Token完成] [%s] 流式响应包含空Token信息", sp.requestID))

			// 🔍 [调试] 异步保存流式调试数据用于分析Token解析失败
			utils.WriteStreamDebugResponse(sp.requestID, sp.endpoint, sp.debugLines, sp.bytesProcessed)

			return nil
		}
	} else {
		// 没有token信息，返回nil
		slog.Info(fmt.Sprintf("🎯 [无Token完成] [%s] 流式响应不包含token信息", sp.requestID))

		// 🔍 [调试] 异步保存流式调试数据用于分析Token解析失败
		utils.WriteStreamDebugResponse(sp.requestID, sp.endpoint, sp.debugLines, sp.bytesProcessed)

		return nil
	}
}

// handlePartialStreamV2 处理部分数据流中断情况（返回Token信息版本）
// 当网络中断或其他错误发生时，收集已解析的Token信息并返回
func (sp *StreamProcessor) handlePartialStreamV2(err error) (*tracking.TokenUsage, error) {
	// ✅ 等待所有后台解析完成（包含flush逻辑）
	sp.waitForBackgroundParsing()

	// 获取Token信息和模型名称
	finalUsage := sp.tokenParser.GetFinalUsage()
	modelName := sp.tokenParser.GetModelName()
	if modelName == "" {
		modelName = "unknown"
	}

	// ✅ 使用智能状态分类
	status := sp.classifyStreamError(err, finalUsage)

	if finalUsage != nil {
		slog.Info(fmt.Sprintf("💾 [流中断保存] [%s] 状态: %s, 模型: %s, 输入: %d, 输出: %d, 缓存: %d+%d",
			sp.requestID, status, modelName, finalUsage.InputTokens, finalUsage.OutputTokens,
			finalUsage.CacheCreationTokens, finalUsage.CacheReadTokens))
	} else {
		slog.Info(fmt.Sprintf("💾 [流中断保存] [%s] 状态: %s, 模型: %s, 无token信息",
			sp.requestID, status, modelName))
	}

	// 尝试从部分数据中恢复有用信息
	if len(sp.partialData) > 0 {
		sp.errorRecovery.RecoverFromPartialData(sp.requestID, sp.partialData, time.Since(sp.startTime))
	}

	// 🔍 [EOF调试] 流中断时保存 debug 文件，无论是否有 Token 信息，方便调试
	if len(sp.partialData) > 0 {
		fullLines := strings.Split(string(sp.partialData), "\n")
		utils.WriteStreamDebugResponse(sp.requestID, sp.endpoint, fullLines, sp.bytesProcessed)
	} else if len(sp.debugLines) > 0 {
		utils.WriteStreamDebugResponse(sp.requestID, sp.endpoint, sp.debugLines, sp.bytesProcessed)
	}

	// 处理Token信息
	var partialTokenUsage *tracking.TokenUsage
	if finalUsage != nil {
		partialTokenUsage = finalUsage
	} else {
		partialTokenUsage = &tracking.TokenUsage{
			InputTokens: 0, OutputTokens: 0,
			CacheCreationTokens: 0, CacheReadTokens: 0,
		}
	}

	// ✅ 返回包含状态和模型信息的错误，供上层处理
	return partialTokenUsage, fmt.Errorf("stream_status:%s:model:%s: %w", status, modelName, err)
}

// handleCancellationV2 处理客户端取消请求（返回Token信息版本）
func (sp *StreamProcessor) handleCancellationV2(ctx context.Context, cancelErr error) (*tracking.TokenUsage, error) {
	slog.Info(fmt.Sprintf("🚫 [客户端取消] [%s] 检测到客户端取消: %v", sp.requestID, cancelErr))

	// 等待后台解析完成，但不超过超时时间
	if finished := sp.waitForParsingWithTimeout(2 * time.Second); finished {
		// 成功等待解析完成，收集可用信息
		return sp.collectAvailableInfoV2(cancelErr, "cancelled_with_data")
	} else {
		// 超时未完成，收集部分信息
		return sp.collectAvailableInfoV2(cancelErr, "cancelled_timeout")
	}
}

// collectAvailableInfoV2 智能信息收集（返回Token信息版本）
func (sp *StreamProcessor) collectAvailableInfoV2(cancelErr error, status string) (*tracking.TokenUsage, error) {
	sp.parseMutex.Lock()
	defer sp.parseMutex.Unlock()

	// ✅ 尝试刷新待处理事件
	if result := sp.tokenParser.FlushPendingEvent(); result != nil {
		if result.ErrorInfo != nil {
			slog.Error(fmt.Sprintf("❌ [取消Flush错误] [%s] 类型: %s, 消息: %s",
				sp.requestID, result.ErrorInfo.Type, result.ErrorInfo.Message))
		} else if result.TokenUsage != nil {
			slog.Debug(fmt.Sprintf("🔄 [取消Flush成功] [%s] 成功解析取消前的待处理事件", sp.requestID))
		}
	}

	// 获取已解析的信息
	modelName := sp.tokenParser.GetModelName()
	finalUsage := sp.tokenParser.GetFinalUsage()
	completeness := sp.tokenParser.GetStreamCompleteness()

	var tokenUsage *tracking.TokenUsage

	if finalUsage != nil {
		tokenUsage = finalUsage
	} else {
		tokenUsage = &tracking.TokenUsage{
			InputTokens: 0, OutputTokens: 0,
			CacheCreationTokens: 0, CacheReadTokens: 0,
		}
	}

	if completeness.IsComplete {
		if finalUsage != nil {
			slog.Info(fmt.Sprintf("💾 [完成保存] [%s] 模型: %s, 输入: %d, 输出: %d",
				sp.requestID, modelName, finalUsage.InputTokens, finalUsage.OutputTokens))
		} else {
			slog.Info(fmt.Sprintf("💾 [完成保存] [%s] 模型: %s, 无token信息",
				sp.requestID, modelName))
		}
		slog.Info(fmt.Sprintf("✅ [取消转完成] [%s] 流已完整，忽略后续 context canceled", sp.requestID))
		return tokenUsage, nil
	}

	// ✅ 使用智能状态分类
	statusClassified := sp.classifyStreamError(cancelErr, finalUsage)
	if finalUsage != nil {
		slog.Info(fmt.Sprintf("💾 [取消保存] [%s] 状态: %s, 模型: %s, 输入: %d, 输出: %d",
			sp.requestID, statusClassified, modelName, finalUsage.InputTokens, finalUsage.OutputTokens))
	} else {
		slog.Info(fmt.Sprintf("💾 [取消保存] [%s] 状态: %s, 模型: %s, 无token信息",
			sp.requestID, statusClassified, modelName))
	}

	// ✅ 返回包含完整信息的错误
	return tokenUsage, fmt.Errorf("stream_status:%s:model:%s: %w", statusClassified, modelName, cancelErr)
}
