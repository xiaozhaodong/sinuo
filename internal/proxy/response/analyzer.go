package response

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"sinuo/internal/monitor"
	"sinuo/internal/tracking"
	"sinuo/internal/utils"
)

// ResponseFormat 定义响应格式类型
type ResponseFormat int

const (
	FormatUnknown ResponseFormat = iota
	FormatJSON
	FormatSSE
	FormatPlainText
)

// RequestLifecycleManager 定义生命周期管理器接口
type RequestLifecycleManager interface {
	GetDuration() time.Duration
}

// ParseResult v5.0+: 解析结果结构体
type ParseResult struct {
	TokenUsage  *tracking.TokenUsage
	ModelName   string
	IsCompleted bool
	Status      string
}

// TokenParser 定义token parser接口
type TokenParser interface {
	ParseSSELine(line string) *monitor.TokenUsage
	SetModelName(model string)
	GetFinalUsage() *tracking.TokenUsage // v5.0+: 获取完整的 TokenUsage（包含 5m/1h 缓存字段）
}

// TokenParserProvider 提供token parser创建功能
type TokenParserProvider interface {
	NewTokenParser() TokenParser
	NewTokenParserWithUsageTracker(requestID string, usageTracker *tracking.UsageTracker) TokenParser
}

// TokenAnalyzer 负责分析响应中的Token使用信息
type TokenAnalyzer struct {
	usageTracker         *tracking.UsageTracker
	monitoringMiddleware interface{}
	tokenParserProvider  TokenParserProvider
}

// NewTokenAnalyzer 创建新的TokenAnalyzer实例
func NewTokenAnalyzer(usageTracker *tracking.UsageTracker, monitoringMiddleware interface{}, provider TokenParserProvider) *TokenAnalyzer {
	return &TokenAnalyzer{
		usageTracker:         usageTracker,
		monitoringMiddleware: monitoringMiddleware,
		tokenParserProvider:  provider,
	}
}

// AnalyzeResponseForTokens analyzes the complete response body for token usage information
// 🆕 [修复] 使用新的三层防护格式检测系统，避免JSON响应被误判为SSE
func (a *TokenAnalyzer) AnalyzeResponseForTokens(ctx context.Context, responseBody, endpointName string, r *http.Request) {

	// Get connection ID from request context
	connID := ""
	if connIDValue, ok := r.Context().Value("conn_id").(string); ok {
		connID = connIDValue
	}

	// Add entry log for debugging
	slog.DebugContext(ctx, fmt.Sprintf("🎯 [Token分析入口] [%s] 端点: %s, 响应长度: %d字节",
		connID, endpointName, len(responseBody)))

	// 🆕 [修复] 使用结构化格式检测替代strings.Contains判断
	format := detectResponseFormat(responseBody)
	slog.DebugContext(ctx, fmt.Sprintf("🎯 [格式检测] [%s] 响应格式: %s, 长度: %d",
		connID, formatName(format), len(responseBody)))

	// 🆕 [修复] 基于检测结果进行智能路由
	switch format {
	case FormatJSON:
		// ✅ 明确是JSON，直接使用JSON解析器
		slog.DebugContext(ctx, fmt.Sprintf("🔍 [JSON路由] [%s] 检测为JSON格式，使用JSON解析器", connID))
		a.ParseJSONTokens(ctx, responseBody, endpointName, connID)
		return

	case FormatSSE:
		// ✅ 明确是SSE，使用SSE解析器
		slog.DebugContext(ctx, fmt.Sprintf("🌊 [SSE路由] [%s] 检测为SSE格式，使用SSE解析器", connID))
		a.ParseSSETokens(ctx, responseBody, endpointName, connID)
		return

	default:
		// ⚠️ 格式未知，启用防护性回退机制
		slog.DebugContext(ctx, fmt.Sprintf("❓ [未知格式] [%s] 格式检测失败，启用回退机制", connID))
		analysis := a.parseWithFallback(responseBody, connID, endpointName)
		tokenUsage, modelName := analysis.TokenUsage, analysis.ModelName
		if tokenUsage != nil {
			// 回退机制成功找到Token信息
			slog.InfoContext(ctx, fmt.Sprintf("✅ [回退成功] [%s] 模型: %s, Token信息已解析", connID, modelName))
			return
		}

		// 最终失败：无Token信息
		slog.InfoContext(ctx, fmt.Sprintf("🎯 [无Token响应] 端点: %s, 连接: %s - 响应不包含token信息，标记为完成", endpointName, connID))
		if connID != "" {
			slog.InfoContext(ctx, fmt.Sprintf("✅ [无Token完成] 连接: %s 检测为无Token响应，模型: non_token_response", connID))
		}
	}
}

// ParseSSETokens parses SSE format response for token usage or error events
func (a *TokenAnalyzer) ParseSSETokens(ctx context.Context, responseBody, endpointName, connID string) {
	tokenParser := a.tokenParserProvider.NewTokenParserWithUsageTracker(connID, a.usageTracker)
	lines := strings.Split(responseBody, "\n")

	foundTokenUsage := false
	hasErrorEvent := false

	// Check if response contains error events first
	if strings.Contains(responseBody, "event:error") || strings.Contains(responseBody, "event: error") {
		hasErrorEvent = true
		slog.InfoContext(ctx, fmt.Sprintf("❌ [SSE错误检测] [%s] 端点: %s - 检测到error事件", connID, endpointName))
	}

	for _, line := range lines {
		if tokenUsage := tokenParser.ParseSSELine(line); tokenUsage != nil {
			foundTokenUsage = true
			// 获取模型名称(需要类型断言获取TokenParser的模型信息)
			modelName := "unknown"
			if tp, ok := tokenParser.(interface{ GetModelName() string }); ok {
				modelName = tp.GetModelName()
			}

			// 详细显示token使用信息
			slog.InfoContext(ctx, fmt.Sprintf("✅ [SSE解析成功] [%s] 端点: %s - 模型: %s, 输入: %d, 输出: %d, 缓存创建: %d, 缓存读取: %d",
				connID, endpointName, modelName,
				tokenUsage.InputTokens, tokenUsage.OutputTokens,
				tokenUsage.CacheCreationTokens, tokenUsage.CacheReadTokens))

			// Record token usage in monitoring middleware if available
			if mm, ok := a.monitoringMiddleware.(interface {
				RecordTokenUsage(connID string, endpoint string, tokens *monitor.TokenUsage)
			}); ok && connID != "" {
				mm.RecordTokenUsage(connID, endpointName, tokenUsage)
			}

			// ⚠️ 重要：TokenParser现在只负责解析，不直接记录到数据库
			// 数据库记录由上层AnalyzeResponseForTokensWithLifecycle统一处理
			return
		}
	}

	// If we found an error event, the parseErrorEvent method would have already handled it
	if hasErrorEvent {
		slog.InfoContext(ctx, fmt.Sprintf("❌ [SSE错误处理] [%s] 端点: %s - 错误事件已处理", connID, endpointName))
		return
	}

	if !foundTokenUsage {
		slog.InfoContext(ctx, fmt.Sprintf("🚫 [SSE解析] [%s] 端点: %s - 未找到token usage信息", connID, endpointName))

		// 🔍 [调试] 异步保存响应数据用于调试Token解析失败问题
		utils.WriteTokenDebugResponse(connID, endpointName, responseBody)
	}
}

// ParseJSONTokens parses single JSON response for token usage
func (a *TokenAnalyzer) ParseJSONTokens(ctx context.Context, responseBody, endpointName, connID string) {
	// Simulate SSE parsing for a single JSON response
	tokenParser := a.tokenParserProvider.NewTokenParserWithUsageTracker(connID, a.usageTracker)

	slog.InfoContext(ctx, fmt.Sprintf("🔍 [JSON解析] [%s] 尝试解析JSON响应", connID))

	// 🆕 First extract model information directly from JSON
	var jsonResp map[string]interface{}
	if err := json.Unmarshal([]byte(responseBody), &jsonResp); err == nil {
		if model, ok := jsonResp["model"].(string); ok && model != "" {
			tokenParser.SetModelName(model)
			slog.InfoContext(ctx, "📋 [JSON解析] 提取到模型信息", "model", model)
		}
	}

	// 按响应协议进入共用解析器，避免用 Claude 字段解释 Responses 的缓存明细。
	tokenParser.ParseSSELine("event: " + jsonUsageEvent(jsonResp))
	tokenParser.ParseSSELine("data: " + responseBody)
	if tokenUsage := tokenParser.ParseSSELine(""); tokenUsage != nil {
		// Record token usage
		if mm, ok := a.monitoringMiddleware.(interface {
			RecordTokenUsage(connID string, endpoint string, tokens *monitor.TokenUsage)
		}); ok && connID != "" {
			mm.RecordTokenUsage(connID, endpointName, tokenUsage)
			slog.InfoContext(ctx, "✅ [JSON解析] 成功记录token使用",
				"endpoint", endpointName,
				"inputTokens", tokenUsage.InputTokens,
				"outputTokens", tokenUsage.OutputTokens,
				"cacheCreation", tokenUsage.CacheCreationTokens,
				"cacheRead", tokenUsage.CacheReadTokens)
		}
	} else {
		slog.DebugContext(ctx, fmt.Sprintf("🚫 [JSON解析] [%s] JSON中未找到token usage信息", connID))
	}
}

// AnalyzeResponseForTokensWithLifecycle analyzes response with accurate duration from lifecycle manager
// 🆕 [修复] 使用新的三层防护格式检测系统，避免JSON响应被误判为SSE
func (a *TokenAnalyzer) AnalyzeResponseForTokensWithLifecycle(ctx context.Context, responseBody, endpointName string, r *http.Request, lifecycleManager RequestLifecycleManager) {
	// Get connection ID from request context
	connID := ""
	if connIDValue, ok := r.Context().Value("conn_id").(string); ok {
		connID = connIDValue
	}

	// Add entry log for debugging
	slog.DebugContext(ctx, fmt.Sprintf("🎯 [Token分析入口] [%s] 端点: %s, 响应长度: %d字节",
		connID, endpointName, len(responseBody)))

	// 🆕 [修复] 使用结构化格式检测替代strings.Contains判断
	format := detectResponseFormat(responseBody)
	slog.DebugContext(ctx, fmt.Sprintf("🎯 [格式检测] [%s] 响应格式: %s, 长度: %d",
		connID, formatName(format), len(responseBody)))

	// 🆕 [修复] 基于检测结果进行智能路由
	switch format {
	case FormatJSON:
		// ✅ 明确是JSON，直接使用JSON解析器
		slog.DebugContext(ctx, fmt.Sprintf("🔍 [JSON路由] [%s] 检测为JSON格式，使用JSON解析器", connID))

		// 使用统一的JSON解析方法
		analysis := a.parseJSONForTokens(responseBody, connID, endpointName)
		tokenUsage, modelName := analysis.TokenUsage, analysis.ModelName
		if tokenUsage != nil {
			// Record token usage to monitoring middleware
			if mm, ok := a.monitoringMiddleware.(interface {
				RecordTokenUsage(connID string, endpoint string, tokens *monitor.TokenUsage)
			}); ok && connID != "" {
				// 转换为monitor.TokenUsage格式
				monitorTokenUsage := &monitor.TokenUsage{
					InputTokens:         tokenUsage.InputTokens,
					OutputTokens:        tokenUsage.OutputTokens,
					CacheCreationTokens: tokenUsage.CacheCreationTokens,
					CacheReadTokens:     tokenUsage.CacheReadTokens,
				}
				requestFamily := ""
				if tokenUsage.IncludesCacheInInput(r.URL.Path) {
					requestFamily = tracking.RequestFamilyCodex
				}
				monitorTokenUsage.SetProtocol(requestFamily, r.URL.Path)
				mm.RecordTokenUsage(connID, endpointName, monitorTokenUsage)
				slog.InfoContext(ctx, "✅ [JSON解析] 成功记录token使用",
					"endpoint", endpointName,
					"inputTokens", tokenUsage.InputTokens,
					"outputTokens", tokenUsage.OutputTokens,
					"cacheCreation", tokenUsage.CacheCreationTokens,
					"cacheRead", tokenUsage.CacheReadTokens)
			}
			slog.InfoContext(ctx, "💾 [JSON数据库保存] JSON解析的Token信息已解析完成",
				"request_id", connID, "model", modelName,
				"inputTokens", tokenUsage.InputTokens, "outputTokens", tokenUsage.OutputTokens)
		} else {
			slog.DebugContext(ctx, fmt.Sprintf("🚫 [JSON解析] [%s] JSON中未找到token usage信息", connID))
			slog.InfoContext(ctx, fmt.Sprintf("✅ [无Token完成] 连接: %s 将由Handler标记为完成状态，模型: non_token_response", connID))
		}
		return

	case FormatSSE:
		// ✅ 明确是SSE，使用SSE解析器
		slog.DebugContext(ctx, fmt.Sprintf("🌊 [SSE路由] [%s] 检测为SSE格式，使用SSE解析器", connID))
		a.ParseSSETokens(ctx, responseBody, endpointName, connID)

		// 使用统一的SSE解析方法
		analysis := a.parseSSEForTokens(responseBody, connID, endpointName)
		tokenUsage, modelName := analysis.TokenUsage, analysis.ModelName
		if tokenUsage != nil {
			slog.InfoContext(ctx, fmt.Sprintf("💾 [SSEToken解析] [%s] 模型: %s, Token信息已解析完成", connID, modelName))
		}
		return

	default:
		// ⚠️ [第三层] 格式未知，启用防护性回退机制
		slog.DebugContext(ctx, fmt.Sprintf("❓ [未知格式] [%s] 格式检测失败，启用回退机制", connID))
		analysis := a.parseWithFallback(responseBody, connID, endpointName)
		tokenUsage, modelName := analysis.TokenUsage, analysis.ModelName
		if tokenUsage != nil {
			// 回退机制成功找到Token信息
			slog.InfoContext(ctx, fmt.Sprintf("✅ [回退成功] [%s] 模型: %s, Token信息已解析", connID, modelName))
		} else {
			// 最终失败：无Token信息
			slog.InfoContext(ctx, fmt.Sprintf("✅ [无Token完成] 连接: %s 将由Handler标记为完成状态，模型: non_token_response", connID))
		}
	}
}

// ResponseAnalysis 是非流式响应的统一解析结果（v6.3）。
// TokenUsage 为 nil 表示无 Token 信息；ResponseModel / ResponseReasoning / StopReason 与 usage 相互独立，
// 无 usage 时仍可能非空。ModelName 保持旧语义（含 unknown / default / error_response 等占位）。
type ResponseAnalysis struct {
	TokenUsage        *tracking.TokenUsage
	ModelName         string
	ResponseModel     string
	ResponseReasoning string
	StopReason        string
}

// reasoningAwareParser 是 TokenParser 的可选扩展：具备响应模型 / 思考信息累计能力。
type reasoningAwareParser interface {
	GetResponseModel() string
	GetResponseReasoning() string
	MergeResponseReasoningFromJSON(payload map[string]interface{})
}

func readParserDiagnostics(parser TokenParser) (model, reasoning string) {
	if aware, ok := parser.(reasoningAwareParser); ok {
		return aware.GetResponseModel(), aware.GetResponseReasoning()
	}
	return "", ""
}

// AnalyzeResponseForTokensUnified 简化版本的Token分析（用于统一接口）
// 返回值: (tokenUsage, modelName) - tokenUsage为nil表示无Token信息
// 旧签名薄包装；新调用方请用 AnalyzeResponseUnified。
func (a *TokenAnalyzer) AnalyzeResponseForTokensUnified(responseBytes []byte, connID, endpointName string) (*tracking.TokenUsage, string) {
	analysis := a.AnalyzeResponseUnified(responseBytes, connID, endpointName)
	return analysis.TokenUsage, analysis.ModelName
}

// AnalyzeResponseUnified 全量消费响应，模型 / 思考 / usage 三者独立提取。
// 🆕 [Pike方案] 使用三层防护的格式检测系统
func (a *TokenAnalyzer) AnalyzeResponseUnified(responseBytes []byte, connID, endpointName string) ResponseAnalysis {
	if len(responseBytes) == 0 {
		return ResponseAnalysis{ModelName: "empty_response"}
	}

	responseStr := string(responseBytes)

	// 🆕 [第一层] 使用结构化格式检测
	format := detectResponseFormat(responseStr)
	slog.Debug(fmt.Sprintf("🎯 [格式检测] [%s] 响应格式: %s, 长度: %d",
		connID, formatName(format), len(responseStr)))

	// 🆕 [第二层] 基于检测结果进行智能路由
	switch format {
	case FormatJSON:
		slog.Debug(fmt.Sprintf("🔍 [JSON路由] [%s] 检测为JSON格式，使用JSON解析器", connID))
		return a.parseJSONForTokens(responseStr, connID, endpointName)

	case FormatSSE:
		slog.Debug(fmt.Sprintf("🌊 [SSE路由] [%s] 检测为SSE格式，使用SSE解析器", connID))
		return a.parseSSEForTokens(responseStr, connID, endpointName)

	default:
		// ⚠️ [第三层] 格式未知，启用防护性回退机制
		slog.Debug(fmt.Sprintf("❓ [未知格式] [%s] 格式检测失败，启用回退机制", connID))
		return a.parseWithFallback(responseStr, connID, endpointName)
	}
}

// parseSSEForTokens 解析SSE格式响应获取Token信息（不直接记录）
// v6.3：消费全部行，usage 出现后继续解析 content_block_start / message_stop 等事件；
// usage 取 GetFinalUsage()（累计值，与改动前一致），模型 / 思考从解析器终态读取。
func (a *TokenAnalyzer) parseSSEForTokens(responseStr, connID, endpointName string) ResponseAnalysis {
	tokenParser := a.tokenParserProvider.NewTokenParserWithUsageTracker(connID, a.usageTracker)
	lines := strings.Split(responseStr, "\n")

	hasErrorEvent := strings.Contains(responseStr, "event:error") || strings.Contains(responseStr, "event: error")
	if hasErrorEvent {
		slog.Info(fmt.Sprintf("❌ [SSE错误检测] [%s] 端点: %s - 检测到error事件", connID, endpointName))
	}

	var firstUsage *monitor.TokenUsage
	for _, line := range lines {
		if tokenUsage := tokenParser.ParseSSELine(line); tokenUsage != nil && firstUsage == nil {
			firstUsage = tokenUsage
		}
	}

	analysis := ResponseAnalysis{ModelName: "unknown"}
	analysis.ResponseModel, analysis.ResponseReasoning = readParserDiagnostics(tokenParser)
	if firstUsage != nil {
		if tp, ok := tokenParser.(interface{ GetModelName() string }); ok && tp.GetModelName() != "" {
			analysis.ModelName = tp.GetModelName()
		}
		// v5.0+: 使用 GetFinalUsage 获取完整的 TokenUsage（包含 5m/1h 缓存字段）
		analysis.TokenUsage = tokenParser.GetFinalUsage()
		if analysis.TokenUsage == nil {
			analysis.TokenUsage = &tracking.TokenUsage{
				InputTokens:         firstUsage.InputTokens,
				OutputTokens:        firstUsage.OutputTokens,
				CacheCreationTokens: firstUsage.CacheCreationTokens,
				CacheReadTokens:     firstUsage.CacheReadTokens,
			}
		}
		usage := analysis.TokenUsage
		slog.Info(fmt.Sprintf("✅ [SSE解析成功] [%s] 端点: %s - 模型: %s, 输入: %d, 输出: %d, 缓存创建: %d (5m=%d, 1h=%d), 缓存读取: %d",
			connID, endpointName, analysis.ModelName,
			usage.InputTokens, usage.OutputTokens,
			usage.CacheCreationTokens, usage.CacheCreation5mTokens, usage.CacheCreation1hTokens,
			usage.CacheReadTokens))
		return analysis
	}

	// 如果有错误事件或没找到Token信息
	if hasErrorEvent {
		slog.Info(fmt.Sprintf("❌ [SSE错误处理] [%s] 端点: %s - 错误事件已处理", connID, endpointName))
		analysis.ModelName = "error_response"
		return analysis
	}

	slog.Info(fmt.Sprintf("🚫 [SSE解析] [%s] 端点: %s - 未找到token usage信息", connID, endpointName))

	// 🔍 [调试] 异步保存响应数据用于调试Token解析失败问题
	utils.WriteTokenDebugResponse(connID, endpointName, responseStr)

	analysis.ModelName = "no_token_sse"
	return analysis
}

// jsonUsageEvent 只按协议结构选择共用解析入口，不根据模型名字猜测协议。
func jsonUsageEvent(payload map[string]interface{}) string {
	if object, _ := payload["object"].(string); object == "response" || object == "response.compaction" {
		return "response.completed"
	}
	if _, ok := payload["output"].([]interface{}); ok {
		return "response.completed"
	}
	if usage, ok := payload["usage"].(map[string]interface{}); ok {
		if _, ok := usage["input_tokens_details"].(map[string]interface{}); ok {
			return "response.completed"
		}
	}
	return "message_delta"
}

// parseJSONForTokens 解析JSON格式响应获取Token信息（不直接记录）
func (a *TokenAnalyzer) parseJSONForTokens(responseStr, connID, endpointName string) ResponseAnalysis {
	tokenParser := a.tokenParserProvider.NewTokenParserWithUsageTracker(connID, a.usageTracker)

	slog.Info(fmt.Sprintf("🔍 [JSON解析] [%s] 尝试解析JSON响应", connID))

	// 首先提取模型信息；v6.3 同时扫描 content[] / reasoning / usage 得到思考信息（不依赖 usage）
	analysis := ResponseAnalysis{ModelName: "default"}
	var jsonResp map[string]interface{}
	if err := json.Unmarshal([]byte(responseStr), &jsonResp); err == nil {
		analysis.StopReason = StopReason(jsonResp)
		if model, ok := jsonResp["model"].(string); ok && model != "" {
			analysis.ModelName = model
			analysis.ResponseModel = model
			tokenParser.SetModelName(model)
			slog.Info("📋 [JSON解析] 提取到模型信息", "model", model)
		}
		if aware, ok := tokenParser.(reasoningAwareParser); ok {
			aware.MergeResponseReasoningFromJSON(jsonResp)
			analysis.ResponseReasoning = aware.GetResponseReasoning()
		}
	}

	// 将 JSON 按协议包装为 SSE 事件，复用同一个 Token 解析器。
	// v5.0+: 使用 ParseSSELine 触发解析，然后通过 GetFinalUsage 获取包含 5m/1h 缓存字段的完整结果
	tokenParser.ParseSSELine("event: " + jsonUsageEvent(jsonResp))
	tokenParser.ParseSSELine("data: " + responseStr)
	if tokenParser.ParseSSELine("") != nil {
		// 使用 GetFinalUsage 获取完整的 TokenUsage（包含 5m/1h 缓存字段）
		if tokenUsage := tokenParser.GetFinalUsage(); tokenUsage != nil {
			slog.Info("✅ [JSON解析] 成功解析Token使用信息",
				"endpoint", endpointName,
				"inputTokens", tokenUsage.InputTokens,
				"outputTokens", tokenUsage.OutputTokens,
				"cacheCreation", tokenUsage.CacheCreationTokens,
				"cache5m", tokenUsage.CacheCreation5mTokens,
				"cache1h", tokenUsage.CacheCreation1hTokens,
				"cacheRead", tokenUsage.CacheReadTokens)
			analysis.TokenUsage = tokenUsage
			return analysis
		}
	}

	slog.Debug(fmt.Sprintf("🚫 [JSON解析] [%s] JSON中未找到token usage信息", connID))

	// 🔍 [调试] 异步保存响应数据用于调试Token解析失败问题
	utils.WriteTokenDebugResponse(connID, endpointName, responseStr)

	return analysis
}

// ============================================================================
// 🔧 [Pike方案] 三层防护的格式检测系统
// ============================================================================

// formatName 返回格式类型的可读名称
func formatName(format ResponseFormat) string {
	switch format {
	case FormatJSON:
		return "JSON"
	case FormatSSE:
		return "SSE"
	case FormatPlainText:
		return "PlainText"
	default:
		return "Unknown"
	}
}

// detectResponseFormat 智能检测响应格式（第一层：结构化检测）
// 基于响应的实际结构而非内容进行判断，避免被JSON中的字符串内容误导
func detectResponseFormat(response string) ResponseFormat {
	trimmed := strings.TrimSpace(response)
	if len(trimmed) == 0 {
		return FormatUnknown
	}

	// 1️⃣ JSON优先检测 - 基于结构而非内容
	if isValidJSONStructure(trimmed) {
		return FormatJSON
	}

	// 2️⃣ SSE规范检测 - 基于协议结构
	if isValidSSEStructure(trimmed) {
		return FormatSSE
	}

	// 3️⃣ 其他格式
	return FormatUnknown
}

// isValidJSONStructure 严格的JSON结构验证
// 不依赖内容，只验证是否符合JSON格式规范
func isValidJSONStructure(content string) bool {
	// 快速结构检查
	if !strings.HasPrefix(content, "{") || !strings.HasSuffix(content, "}") {
		return false
	}

	// 严格验证：尝试解析JSON结构
	var temp map[string]interface{}
	return json.Unmarshal([]byte(content), &temp) == nil
}

// isValidSSEStructure 严格的SSE结构验证
// 验证是否符合Server-Sent Events规范结构，而非简单的字符串匹配
func isValidSSEStructure(content string) bool {
	lines := strings.Split(content, "\n")
	hasEventOrData := false
	validSSELines := 0
	totalNonEmptyLines := 0

	for _, line := range lines {
		originalLine := line
		line = strings.TrimSpace(line)

		// 空行是SSE规范的一部分，跳过
		if line == "" {
			continue
		}

		totalNonEmptyLines++

		// 真正的SSE行必须以event:或data:开头（行首检查）
		if strings.HasPrefix(originalLine, "event:") || strings.HasPrefix(originalLine, "data:") {
			hasEventOrData = true
			validSSELines++
		} else {
			// 发现不符合SSE格式的行，可能是JSON中的内容
			// 如果大部分行都不符合SSE格式，则认为这不是真正的SSE响应
			continue
		}
	}

	// 必须满足两个条件：
	// 1. 至少有一个event或data行
	// 2. SSE格式行占比超过50%（防止JSON中偶然包含SSE关键字）
	if !hasEventOrData || totalNonEmptyLines == 0 {
		return false
	}

	sseRatio := float64(validSSELines) / float64(totalNonEmptyLines)
	return sseRatio > 0.5 // SSE格式行占比超过50%才认为是真正的SSE
}

// ============================================================================
// 🛡️ [Pike方案] 防护性回退机制（第三层：兜底处理）
// ============================================================================

// parseWithFallback 当结构化检测也失败时的最后防线
func (a *TokenAnalyzer) parseWithFallback(responseStr, connID, endpointName string) ResponseAnalysis {
	slog.Debug(fmt.Sprintf("🛡️ [回退机制] [%s] 启动兜底解析", connID))

	// 尝试1: 强制JSON解析（忽略结构验证）
	if analysis, ok := a.tryForceJSONParse(responseStr, connID, endpointName); ok {
		slog.Info(fmt.Sprintf("✅ [回退成功] [%s] 强制JSON解析成功", connID))
		return analysis
	}

	// 尝试2: 宽松SSE解析（降低验证标准）
	lenient, ok := a.tryLenientSSEParse(responseStr, connID, endpointName)
	if ok {
		slog.Info(fmt.Sprintf("✅ [回退成功] [%s] 宽松SSE解析成功", connID))
		return lenient
	}

	// 最终失败：usage 缺失不等于诊断缺失，宽松解析已拿到的响应模型 / 思考信息仍要带出
	slog.Info(fmt.Sprintf("🎯 [回退失败] [%s] 端点: %s - 所有解析方法均失败", connID, endpointName))
	return ResponseAnalysis{
		ModelName:         "non_token_response",
		ResponseModel:     lenient.ResponseModel,
		ResponseReasoning: lenient.ResponseReasoning,
	}
}

// tryForceJSONParse 强制尝试JSON解析（忽略结构验证）；只有拿到 usage 才算成功。
func (a *TokenAnalyzer) tryForceJSONParse(responseStr, connID, endpointName string) (ResponseAnalysis, bool) {
	// 如果响应包含usage字段，强制按JSON解析
	if strings.Contains(responseStr, "\"usage\"") {
		slog.Debug(fmt.Sprintf("🔍 [强制JSON] [%s] 发现usage字段，强制JSON解析", connID))
		analysis := a.parseJSONForTokens(responseStr, connID, endpointName)
		return analysis, analysis.TokenUsage != nil
	}
	return ResponseAnalysis{}, false
}

// tryLenientSSEParse 宽松的SSE解析（降低验证标准）；只有拿到 usage 才算成功。
func (a *TokenAnalyzer) tryLenientSSEParse(responseStr, connID, endpointName string) (ResponseAnalysis, bool) {
	// 宽松条件：只要包含任何event或data行就尝试解析
	if strings.Contains(responseStr, "event:") || strings.Contains(responseStr, "data:") {
		slog.Debug(fmt.Sprintf("🌊 [宽松SSE] [%s] 发现SSE关键字，尝试宽松解析", connID))
		analysis := a.parseSSEForTokens(responseStr, connID, endpointName)
		return analysis, analysis.TokenUsage != nil
	}
	return ResponseAnalysis{}, false
}
