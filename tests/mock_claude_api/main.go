// Claude API Mock Server
// 用于测试 Sinuo 的各种错误场景
// 2025-12-25 创建
//
// 启动方式: go run tests/mock_claude_api/main.go
// 默认端口: 9999
//
// 配置文件: tests/mock_claude_api/mock_config.yaml
// 通过修改配置文件来控制错误模式，服务会自动热加载配置
//
// 也可以通过 HTTP API 动态修改配置:
//   GET  /mock/config          - 查看当前配置
//   POST /mock/config          - 更新配置（JSON 格式）
//   POST /mock/reset           - 重置为默认配置

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

// MockConfig 模拟配置
type MockConfig struct {
	// 错误模式: none, 429, 500, 503, timeout, stream_eof, stream_error
	ErrorMode string `yaml:"error_mode" json:"error_mode"`
	// 响应延迟（如 "5s", "100ms"）
	Delay string `yaml:"delay" json:"delay"`
	// 流式错误发生的位置（0-100%，表示在流式输出的哪个位置触发错误）
	StreamErrorPosition int `yaml:"stream_error_position" json:"stream_error_position"`
	// 错误次数限制（0=无限制，>0 表示返回 N 次错误后恢复正常）
	ErrorCount int `yaml:"error_count" json:"error_count"`
	// 当前已返回的错误次数（运行时状态）
	currentErrorCount int
	// Retry-After 头（秒）
	RetryAfterSeconds int `yaml:"retry_after_seconds" json:"retry_after_seconds"`
}

var (
	port       = flag.Int("port", 9999, "服务监听端口")
	configFile = flag.String("config", "", "配置文件路径（默认为同目录下的 mock_config.yaml）")

	config     MockConfig
	configLock sync.RWMutex
)

func main() {
	flag.Parse()

	// 确定配置文件路径
	configPath := *configFile
	if configPath == "" {
		// 默认使用同目录下的 mock_config.yaml
		exe, _ := os.Executable()
		configPath = filepath.Join(filepath.Dir(exe), "mock_config.yaml")
		// 如果不存在，尝试使用源码目录
		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			configPath = "tests/mock_claude_api/mock_config.yaml"
		}
	}

	// 加载配置
	if err := loadConfig(configPath); err != nil {
		log.Printf("⚠️ 加载配置失败，使用默认配置: %v", err)
		config = MockConfig{ErrorMode: "none", StreamErrorPosition: 50}
	}

	// 启动配置文件监控
	go watchConfig(configPath)

	// 注册路由
	http.HandleFunc("/v1/models", handleModels)
	http.HandleFunc("/v1/messages", handleMessages)
	http.HandleFunc("/v1/messages/count_tokens", handleCountTokens)
	http.HandleFunc("/health", handleHealth)
	http.HandleFunc("/mock/config", handleMockConfig)
	http.HandleFunc("/mock/reset", handleMockReset)

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("🚀 Claude API Mock Server 启动在 http://localhost%s", addr)
	log.Printf("📋 支持的端点:")
	log.Printf("   GET  /v1/models           - 模型列表")
	log.Printf("   POST /v1/messages         - 消息请求（流式/非流式）")
	log.Printf("   POST /v1/messages/count_tokens - Token 计数")
	log.Printf("   GET  /health              - 健康检查")
	log.Printf("   GET  /mock/config         - 查看当前配置")
	log.Printf("   POST /mock/config         - 更新配置")
	log.Printf("   POST /mock/reset          - 重置配置")
	log.Printf("")
	log.Printf("📁 配置文件: %s", configPath)
	log.Printf("📌 当前配置: error_mode=%s, delay=%s", config.ErrorMode, config.Delay)
	log.Printf("")

	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("❌ 服务启动失败: %v", err)
	}
}

// loadConfig 加载配置文件
func loadConfig(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	configLock.Lock()
	defer configLock.Unlock()

	if err := yaml.Unmarshal(data, &config); err != nil {
		return err
	}

	// 设置默认值
	if config.StreamErrorPosition <= 0 {
		config.StreamErrorPosition = 50
	}

	log.Printf("📝 配置已加载: error_mode=%s, delay=%s, error_count=%d",
		config.ErrorMode, config.Delay, config.ErrorCount)
	return nil
}

// watchConfig 监控配置文件变化
func watchConfig(path string) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("⚠️ 无法监控配置文件: %v", err)
		return
	}
	defer watcher.Close()

	// 监控配置文件所在目录
	dir := filepath.Dir(path)
	if err := watcher.Add(dir); err != nil {
		log.Printf("⚠️ 无法监控目录 %s: %v", dir, err)
		return
	}

	log.Printf("👀 正在监控配置文件变化: %s", path)

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if filepath.Base(event.Name) == filepath.Base(path) {
				if event.Op&(fsnotify.Write|fsnotify.Create) != 0 {
					time.Sleep(100 * time.Millisecond) // 等待文件写入完成
					if err := loadConfig(path); err != nil {
						log.Printf("⚠️ 重新加载配置失败: %v", err)
					} else {
						log.Printf("🔄 配置已热加载")
					}
				}
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Printf("⚠️ 文件监控错误: %v", err)
		}
	}
}

// getConfig 获取当前配置（线程安全）
func getConfig() MockConfig {
	configLock.RLock()
	defer configLock.RUnlock()
	return config
}

// handleMockConfig 处理配置查看/更新请求
func handleMockConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		cfg := getConfig()
		json.NewEncoder(w).Encode(cfg)
	case http.MethodPost:
		var newConfig MockConfig
		if err := json.NewDecoder(r.Body).Decode(&newConfig); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		configLock.Lock()
		config = newConfig
		if config.StreamErrorPosition <= 0 {
			config.StreamErrorPosition = 50
		}
		configLock.Unlock()
		log.Printf("📝 配置已更新: error_mode=%s, delay=%s", config.ErrorMode, config.Delay)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleMockReset 重置配置
func handleMockReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	configLock.Lock()
	config = MockConfig{ErrorMode: "none", StreamErrorPosition: 50}
	configLock.Unlock()

	log.Printf("🔄 配置已重置为默认值")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleHealth 健康检查
func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleModels 返回模型列表
func handleModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	response := map[string]any{
		"object": "list",
		"data": []map[string]any{
			{
				"id":           "claude-3-5-sonnet-20241022",
				"object":       "model",
				"created":      time.Now().Unix(),
				"owned_by":     "anthropic",
				"display_name": "Claude 3.5 Sonnet",
			},
			{
				"id":           "claude-3-opus-20240229",
				"object":       "model",
				"created":      time.Now().Unix(),
				"owned_by":     "anthropic",
				"display_name": "Claude 3 Opus",
			},
		},
	}
	json.NewEncoder(w).Encode(response)
}

// handleCountTokens 处理 Token 计数请求
func handleCountTokens(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if shouldReturnError(w) {
		return
	}

	w.Header().Set("Content-Type", "application/json")
	response := map[string]any{
		"input_tokens": 100,
	}
	json.NewEncoder(w).Encode(response)
}

// handleMessages 处理消息请求
func handleMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	cfg := getConfig()

	// 非流式错误在这里处理
	if cfg.ErrorMode != "" && cfg.ErrorMode != "none" &&
		cfg.ErrorMode != "stream_eof" && cfg.ErrorMode != "stream_error" {
		if shouldReturnError(w) {
			return
		}
	}

	// 解析请求体
	var req map[string]any
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sendErrorResponse(w, http.StatusBadRequest, "invalid_request_error", "Invalid JSON")
		return
	}

	// 获取模型名称
	model, _ := req["model"].(string)
	if model == "" {
		model = "claude-3-5-sonnet-20241022"
	}

	// 检查是否为流式请求
	stream, _ := req["stream"].(bool)

	if stream {
		handleStreamingResponse(w, r, model, cfg)
	} else {
		handleRegularResponse(w, r, model, cfg)
	}
}

// handleRegularResponse 处理非流式响应
func handleRegularResponse(w http.ResponseWriter, _ *http.Request, model string, cfg MockConfig) {
	// 应用延迟
	applyDelay(cfg)

	msgID := "msg_" + uuid.New().String()[:8]

	w.Header().Set("Content-Type", "application/json")
	response := map[string]any{
		"id":    msgID,
		"type":  "message",
		"role":  "assistant",
		"model": model,
		"content": []map[string]any{
			{
				"type": "text",
				"text": "这是 Mock 服务的测试响应。Hello from Claude API Mock Server!",
			},
		},
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":  50,
			"output_tokens": 20,
		},
	}
	json.NewEncoder(w).Encode(response)
	log.Printf("✅ [非流式] 请求完成 model=%s msg_id=%s", model, msgID)
}

// handleStreamingResponse 处理流式响应
func handleStreamingResponse(w http.ResponseWriter, _ *http.Request, model string, cfg MockConfig) {
	// 设置 SSE 响应头
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	msgID := "msg_" + uuid.New().String()[:8]

	// 应用延迟
	applyDelay(cfg)

	// 1. message_start
	sendSSE(w, flusher, "message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            msgID,
			"type":          "message",
			"role":          "assistant",
			"model":         model,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage": map[string]any{
				"input_tokens":  50,
				"output_tokens": 0,
			},
		},
	})

	// 2. content_block_start
	sendSSE(w, flusher, "content_block_start", map[string]any{
		"type":  "content_block_start",
		"index": 0,
		"content_block": map[string]any{
			"type": "text",
			"text": "",
		},
	})

	// 3. 模拟流式输出内容
	responseText := "这是 Mock 服务的流式测试响应。Hello from Claude API Mock Server! 这段文字会逐字输出，模拟真实的流式响应效果。"
	chunks := splitIntoChunks(responseText, 5)
	errorPosition := len(chunks) * cfg.StreamErrorPosition / 100

	for i, chunk := range chunks {
		// 检查是否需要在指定位置模拟错误
		if i == errorPosition && shouldTriggerStreamError(cfg) {
			if cfg.ErrorMode == "stream_eof" {
				// 模拟 EOF：直接断开连接（不发送任何内容）
				log.Printf("🔴 [流式] 模拟 EOF 错误，中途断开连接 model=%s msg_id=%s position=%d%%", model, msgID, cfg.StreamErrorPosition)
				return
			}
			if cfg.ErrorMode == "stream_error" {
				// 模拟流式错误：发送错误事件
				sendSSE(w, flusher, "error", map[string]any{
					"type": "error",
					"error": map[string]any{
						"type":    "api_error",
						"message": "Simulated stream error from mock server",
					},
				})
				log.Printf("🔴 [流式] 模拟 stream_error 错误 model=%s msg_id=%s position=%d%%", model, msgID, cfg.StreamErrorPosition)
				return
			}
		}

		sendSSE(w, flusher, "content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]any{
				"type": "text_delta",
				"text": chunk,
			},
		})
		time.Sleep(50 * time.Millisecond) // 模拟打字效果
	}

	// 4. content_block_stop
	sendSSE(w, flusher, "content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": 0,
	})

	// 5. message_delta
	sendSSE(w, flusher, "message_delta", map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   "end_turn",
			"stop_sequence": nil,
		},
		"usage": map[string]any{
			"output_tokens": len([]rune(responseText)) / 4,
		},
	})

	// 6. message_stop
	sendSSE(w, flusher, "message_stop", map[string]any{
		"type": "message_stop",
	})

	log.Printf("✅ [流式] 请求完成 model=%s msg_id=%s", model, msgID)
}

// sendSSE 发送 SSE 事件
func sendSSE(w http.ResponseWriter, flusher http.Flusher, event string, data any) {
	jsonData, _ := json.Marshal(data)
	fmt.Fprintf(w, "event: %s\n", event)
	fmt.Fprintf(w, "data: %s\n\n", string(jsonData))
	flusher.Flush()
}

// shouldReturnError 检查是否应该返回错误
func shouldReturnError(w http.ResponseWriter) bool {
	cfg := getConfig()

	if cfg.ErrorMode == "" || cfg.ErrorMode == "none" {
		return false
	}

	// 检查错误次数限制
	if cfg.ErrorCount > 0 {
		configLock.Lock()
		if config.currentErrorCount >= cfg.ErrorCount {
			configLock.Unlock()
			return false // 已达到错误次数限制，恢复正常
		}
		config.currentErrorCount++
		currentCount := config.currentErrorCount
		configLock.Unlock()
		log.Printf("📊 错误计数: %d/%d", currentCount, cfg.ErrorCount)
	}

	// 应用延迟
	applyDelay(cfg)

	switch cfg.ErrorMode {
	case "429":
		retryAfter := cfg.RetryAfterSeconds
		if retryAfter <= 0 {
			retryAfter = 60
		}
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		sendErrorResponse(w, http.StatusTooManyRequests, "rate_limit_error",
			fmt.Sprintf("Rate limit exceeded. Please retry after %d seconds.", retryAfter))
		log.Printf("🔴 [模拟错误] 429 Too Many Requests")
		return true
	case "500":
		sendErrorResponse(w, http.StatusInternalServerError, "api_error", "Internal server error (simulated)")
		log.Printf("🔴 [模拟错误] 500 Internal Server Error")
		return true
	case "503":
		sendErrorResponse(w, http.StatusServiceUnavailable, "overloaded_error", "Service temporarily unavailable (simulated)")
		log.Printf("🔴 [模拟错误] 503 Service Unavailable")
		return true
	case "timeout":
		log.Printf("🔴 [模拟错误] 超时（延迟 30 秒）")
		time.Sleep(30 * time.Second)
		sendErrorResponse(w, http.StatusGatewayTimeout, "timeout_error", "Request timeout (simulated)")
		return true
	default:
		// 尝试解析为 HTTP 状态码
		if code, err := strconv.Atoi(cfg.ErrorMode); err == nil && code >= 400 && code < 600 {
			sendErrorResponse(w, code, "mock_error", fmt.Sprintf("Simulated HTTP %d error", code))
			log.Printf("🔴 [模拟错误] HTTP %d", code)
			return true
		}
	}
	return false
}

// shouldTriggerStreamError 检查是否应该触发流式错误
func shouldTriggerStreamError(cfg MockConfig) bool {
	if cfg.ErrorMode != "stream_eof" && cfg.ErrorMode != "stream_error" {
		return false
	}

	// 检查错误次数限制
	if cfg.ErrorCount > 0 {
		configLock.Lock()
		if config.currentErrorCount >= cfg.ErrorCount {
			configLock.Unlock()
			return false
		}
		config.currentErrorCount++
		currentCount := config.currentErrorCount
		configLock.Unlock()
		log.Printf("📊 流式错误计数: %d/%d", currentCount, cfg.ErrorCount)
	}

	return true
}

// sendErrorResponse 发送错误响应
func sendErrorResponse(w http.ResponseWriter, statusCode int, errorType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	response := map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    errorType,
			"message": message,
		},
	}
	json.NewEncoder(w).Encode(response)
}

// applyDelay 应用配置的延迟
func applyDelay(cfg MockConfig) {
	if cfg.Delay != "" {
		if delay, err := time.ParseDuration(cfg.Delay); err == nil && delay > 0 {
			log.Printf("⏳ 应用延迟: %v", delay)
			time.Sleep(delay)
		}
	}
}

// splitIntoChunks 将字符串分割成指定大小的块
func splitIntoChunks(s string, chunkSize int) []string {
	runes := []rune(s)
	var chunks []string
	for i := 0; i < len(runes); i += chunkSize {
		end := i + chunkSize
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[i:end]))
	}
	return chunks
}
