package utils

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"sinuo/config"
	"sinuo/internal/tracking"
)

// 全局配置实例，用于debug功能开关控制
var (
	debugConfig     *config.TokenDebugConfig
	debugConfigOnce sync.Once
	// 清理节流控制：每 24 小时最多执行一次清理
	lastCleanupTime            time.Time
	cleanupMutex               sync.Mutex
	debugInputTokensRe         = regexp.MustCompile(`"input_tokens"\s*:\s*(\d+)`)
	debugOutputTokensRe        = regexp.MustCompile(`"output_tokens"\s*:\s*(\d+)`)
	debugCacheCreationTokensRe = regexp.MustCompile(`"cache_creation_input_tokens"\s*:\s*(\d+)`)
	debugCacheReadTokensRe     = regexp.MustCompile(`"cache_read_input_tokens"\s*:\s*(\d+)`)
	debugCachedTokensRe        = regexp.MustCompile(`"cached_tokens"\s*:\s*(\d+)`)
)

// SetDebugConfig 设置调试配置（应该在程序启动时调用）
func SetDebugConfig(cfg *config.Config) {
	debugConfigOnce.Do(func() {
		if cfg != nil {
			debugConfig = &cfg.Logging.TokenDebug
		}
	})
}

// isDebugEnabled 检查是否启用Token调试功能
func isDebugEnabled() bool {
	return debugConfig != nil && debugConfig.Enabled
}

// IsTokenDebugEnabled 返回当前是否启用了 Token 调试。
func IsTokenDebugEnabled() bool {
	return isDebugEnabled()
}

// getDebugLogDir 获取调试日志目录
// 如果配置的是相对路径，则使用应用数据目录下的日志目录
// 如果配置的是绝对路径，则直接使用配置的路径
func getDebugLogDir() string {
	if debugConfig != nil && debugConfig.SavePath != "" {
		// 检查是否为绝对路径
		if filepath.IsAbs(debugConfig.SavePath) {
			return debugConfig.SavePath
		}
		// 相对路径：使用应用数据目录下的日志目录
		// macOS: ~/Library/Application Support/Sinuo/logs
		return GetLogDir()
	}
	// 默认：使用应用数据目录下的日志目录
	return GetLogDir()
}

// cleanupDebugFiles 清理过期的 debug 文件
// 根据配置的 MaxFiles 和 AutoCleanupDays 执行清理
// 使用节流机制，每 24 小时最多执行一次
func cleanupDebugFiles() {
	// 节流：每 24 小时最多执行一次清理
	cleanupMutex.Lock()
	if time.Since(lastCleanupTime) < 24*time.Hour {
		cleanupMutex.Unlock()
		return
	}
	lastCleanupTime = time.Now()
	cleanupMutex.Unlock()

	if debugConfig == nil {
		return
	}

	// 如果两个清理条件都未配置，直接返回
	if debugConfig.MaxFiles <= 0 && debugConfig.AutoCleanupDays <= 0 {
		return
	}

	logDir := getDebugLogDir()

	// 获取所有 .debug 文件
	files, err := filepath.Glob(filepath.Join(logDir, "*.debug"))
	if err != nil || len(files) == 0 {
		return
	}

	// 获取文件信息并排序（按修改时间，最旧的在前）
	type fileInfo struct {
		path    string
		modTime time.Time
	}
	var fileInfos []fileInfo
	for _, f := range files {
		info, err := os.Stat(f)
		if err != nil {
			continue
		}
		fileInfos = append(fileInfos, fileInfo{path: f, modTime: info.ModTime()})
	}

	if len(fileInfos) == 0 {
		return
	}

	sort.Slice(fileInfos, func(i, j int) bool {
		return fileInfos[i].modTime.Before(fileInfos[j].modTime)
	})

	deletedCount := 0

	// 1. 按天数清理：删除 N 天前的文件
	if debugConfig.AutoCleanupDays > 0 {
		cutoff := time.Now().AddDate(0, 0, -debugConfig.AutoCleanupDays)
		newFileInfos := make([]fileInfo, 0, len(fileInfos))
		for _, f := range fileInfos {
			if f.modTime.Before(cutoff) {
				if err := os.Remove(f.path); err == nil {
					deletedCount++
				}
			} else {
				newFileInfos = append(newFileInfos, f)
			}
		}
		fileInfos = newFileInfos
	}

	// 2. 按数量清理：保留最新的 MaxFiles 个文件
	if debugConfig.MaxFiles > 0 && len(fileInfos) > debugConfig.MaxFiles {
		toDelete := len(fileInfos) - debugConfig.MaxFiles
		for i := 0; i < toDelete; i++ {
			if err := os.Remove(fileInfos[i].path); err == nil {
				deletedCount++
			}
		}
	}

	if deletedCount > 0 {
		slog.Debug(fmt.Sprintf("🧹 [Debug清理] 清理了 %d 个过期调试文件", deletedCount))
	}
}

// WriteTokenDebugResponse 异步保存Token解析失败的响应数据用于调试
// 不影响主流程性能，如果写入失败也会静默忽略
// 同一requestID的多次调用会追加到同一文件中
func WriteTokenDebugResponse(requestID, endpoint, responseBody string) {
	if requestID == "" {
		return
	}

	// 🔧 检查配置开关：如果禁用Token调试，直接返回
	if !isDebugEnabled() {
		return
	}

	// 异步写入，不阻塞主流程
	go func() {
		logDir := getDebugLogDir()
		// 确保日志目录存在
		if err := os.MkdirAll(logDir, 0755); err != nil {
			return // 静默失败，不影响主流程
		}

		// 文件名：logs/{requestID}.debug
		filename := filepath.Join(logDir, fmt.Sprintf("%s.debug", requestID))

		// 创建调试内容
		debugContent := "\n=== TOKEN解析失败调试信息 ===\n"
		debugContent += fmt.Sprintf("请求ID: %s\n", requestID)
		debugContent += fmt.Sprintf("端点: %s\n", endpoint)
		debugContent += fmt.Sprintf("时间: %s\n", time.Now().Format("2006-01-02 15:04:05"))
		debugContent += fmt.Sprintf("响应长度: %d 字节\n", len(responseBody))
		debugContent += "=== 响应内容 ===\n" + responseBody + "\n"
		debugContent += "=== 分割线 ===\n\n"

		// 追加写入文件（如果失败，静默忽略）
		file, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return // 静默失败
		}
		defer file.Close()

		file.WriteString(debugContent)

		// 🧹 触发清理（节流控制，每 24 小时最多执行一次）
		cleanupDebugFiles()
	}()
}

// WriteStreamDebugResponse 异步保存流式Token解析失败的调试数据
// streamData 包含流式处理过程中收集到的原始数据
// bytesProcessed 表示处理的总字节数
func WriteStreamDebugResponse(requestID, endpoint string, streamData []string, bytesProcessed int64) {
	if requestID == "" {
		return
	}

	// 🔧 检查配置开关：如果禁用Token调试，直接返回
	if !isDebugEnabled() {
		return
	}

	// 异步写入，不阻塞主流程
	go func() {
		logDir := getDebugLogDir()
		// 确保日志目录存在
		if err := os.MkdirAll(logDir, 0755); err != nil {
			return // 静默失败，不影响主流程
		}

		// 文件名：logs/{requestID}.debug
		filename := filepath.Join(logDir, fmt.Sprintf("%s.debug", requestID))

		// 创建调试内容
		debugContent := "\n=== 流式TOKEN解析失败调试信息 ===\n"
		debugContent += fmt.Sprintf("请求ID: %s\n", requestID)
		debugContent += fmt.Sprintf("端点: %s\n", endpoint)
		debugContent += fmt.Sprintf("时间: %s\n", time.Now().Format("2006-01-02 15:04:05"))
		debugContent += fmt.Sprintf("已处理字节数: %d\n", bytesProcessed)
		debugContent += fmt.Sprintf("流数据行数: %d\n", len(streamData))
		debugContent += "=== 流式数据内容 ===\n"

		for i, line := range streamData {
			debugContent += fmt.Sprintf("[行%d] %s\n", i+1, line)
		}

		debugContent += "=== 流式分割线 ===\n\n"

		// 追加写入文件（如果失败，静默忽略）
		file, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return // 静默失败
		}
		defer file.Close()

		file.WriteString(debugContent)

		// 🧹 触发清理（节流控制，每 24 小时最多执行一次）
		cleanupDebugFiles()
	}()
}

// RecoverUsageFromDebugFile 从debug文件中恢复usage信息
// 🔧 [Fallback修复] 分析debug文件内容，提取完整的token使用统计
func RecoverUsageFromDebugFile(requestID string) (*tracking.TokenUsage, error) {
	if requestID == "" {
		return nil, fmt.Errorf("requestID不能为空")
	}

	// 🔧 检查配置开关：如果禁用Token调试，直接返回
	if !isDebugEnabled() {
		return nil, fmt.Errorf("Token调试功能已禁用")
	}

	logDir := getDebugLogDir()
	filename := filepath.Join(logDir, fmt.Sprintf("%s.debug", requestID))

	// 检查文件是否存在
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		return nil, fmt.Errorf("debug文件不存在: %s", filename)
	}

	// 读取文件内容
	content, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("读取debug文件失败: %v", err)
	}

	// 提取usage信息
	usage, err := extractUsageFromDebugContent(string(content))
	if err != nil {
		return nil, fmt.Errorf("从debug文件提取usage失败: %v", err)
	}

	return usage, nil
}

// extractUsageFromDebugContent 从debug文件内容中提取usage信息
// 🔧 [Fallback修复] 同时兼容 Claude message_stop 与 Codex /v1/responses usage 结构
func extractUsageFromDebugContent(content string) (*tracking.TokenUsage, error) {
	inputTokens, okInput := extractLastInt64FromDebugContent(content, debugInputTokensRe)
	outputTokens, okOutput := extractLastInt64FromDebugContent(content, debugOutputTokensRe)
	if !okInput || !okOutput {
		return nil, fmt.Errorf("未找到完整usage信息")
	}

	cacheCreationTokens, _ := extractLastInt64FromDebugContent(content, debugCacheCreationTokensRe)
	cacheReadTokens, okCacheRead := extractLastInt64FromDebugContent(content, debugCacheReadTokensRe)
	if !okCacheRead {
		if cachedTokens, ok := extractLastInt64FromDebugContent(content, debugCachedTokensRe); ok {
			cacheReadTokens = cachedTokens
		}
	}

	return &tracking.TokenUsage{
		InputTokens:         inputTokens,
		OutputTokens:        outputTokens,
		CacheCreationTokens: cacheCreationTokens,
		CacheReadTokens:     cacheReadTokens,
	}, nil
}

func extractLastInt64FromDebugContent(content string, re *regexp.Regexp) (int64, bool) {
	if content == "" || re == nil {
		return 0, false
	}
	matches := re.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return 0, false
	}
	lastMatch := matches[len(matches)-1]
	if len(lastMatch) < 2 {
		return 0, false
	}
	var value int64
	if _, err := fmt.Sscanf(lastMatch[1], "%d", &value); err != nil {
		return 0, false
	}
	return value, true
}

// RecoverAndUpdateUsage 从debug文件恢复usage并更新数据库
// 🔧 [Fallback修复] 完整的恢复流程：读取debug文件 -> 提取usage -> 更新数据库
func RecoverAndUpdateUsage(requestID string, modelName string, usageTracker *tracking.UsageTracker) error {
	if usageTracker == nil {
		return fmt.Errorf("usageTracker不能为nil")
	}

	// 从debug文件恢复usage信息
	recoveredUsage, err := RecoverUsageFromDebugFile(requestID)
	if err != nil {
		return fmt.Errorf("恢复usage失败: %v", err)
	}

	// 使用专用的Token恢复方法，只更新Token字段，不触发其他流程
	usageTracker.RecoverRequestTokens(requestID, modelName, recoveredUsage)

	return nil
}
