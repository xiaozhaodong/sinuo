// main.go - AI Switchboard Wails 应用入口
// 保留原有的核心功能，添加 Wails 桌面应用支持

package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"cc-forwarder/config"
	"cc-forwarder/internal/logging"
	"cc-forwarder/internal/utils"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

// 版本信息
var (
	Version   = "5.3.0"
	Commit    = "unknown"
	BuildTime = "unknown"
)

// 命令行参数
var (
	configPath  = flag.String("config", "config/config.yaml", "配置文件路径")
	showVersion = flag.Bool("version", false, "显示版本信息")
)

// 嵌入前端资源
//
//go:embed all:frontend/dist
var assets embed.FS

// 嵌入应用图标
//
//go:embed build/appicon.png
var icon []byte

// 嵌入默认配置文件
//
//go:embed config/config.yaml
var defaultConfigContent []byte

// 运行时变量
var (
	startTime         = time.Now()
	currentLogHandler *SimpleHandler
)

func main() {
	flag.Parse()

	// 处理版本标志
	if *showVersion {
		fmt.Printf("AI Switchboard Desktop\n")
		fmt.Printf("Version: %s\n", Version)
		fmt.Printf("Commit: %s\n", Commit)
		fmt.Printf("Built: %s\n", BuildTime)
		os.Exit(0)
	}

	// 品牌更名（CC-Forwarder -> AI-Switchboard）后的一次性数据目录迁移，
	// 必须在任何组件打开数据目录之前执行
	if migrated, err := utils.MigrateLegacyAppDataDir(); err != nil {
		fmt.Fprintf(os.Stderr, "⚠️ 应用数据目录迁移失败: %v（旧数据仍保留在原目录）\n", err)
	} else if migrated {
		fmt.Println("✅ 应用数据目录已迁移: CC-Forwarder -> AI-Switchboard")
	}

	// 创建应用实例
	app := NewApp()
	app.configPath = *configPath
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "config" {
			app.configPathExplicit = true
		}
	})

	// 运行 Wails 应用
	err := wails.Run(&options.App{
		Title:     "",
		Width:     1280,
		Height:    800,
		MinWidth:  1024,
		MinHeight: 600,

		// 资源服务器
		AssetServer: &assetserver.Options{
			Assets: assets,
		},

		// 背景色 (窗口创建到 webview 首帧之间可见，数十 ms)
		// Go 侧静态值，读不到 localStorage，无法随主题切换。
		// 取中性深灰 #111827：暗色下贴近 canvas #0b1120，浅色下也比脚手架默认深蓝突兀感更低。
		BackgroundColour: &options.RGBA{R: 17, G: 24, B: 39, A: 1},

		// 生命周期回调
		OnStartup:     app.startup,
		OnDomReady:    app.domReady,
		OnBeforeClose: app.beforeClose,
		OnShutdown:    app.shutdown,

		// 绑定到前端的方法
		Bind: []interface{}{
			app,
		},

		// 后端 error 回传前端的格式
		ErrorFormatter: formatWailsError,

		// macOS 配置
		Mac: &mac.Options{
			TitleBar: &mac.TitleBar{
				TitlebarAppearsTransparent: true,
				HideTitle:                  true,
				HideTitleBar:               false,
				FullSizeContent:            true,
				UseToolbar:                 false,
			},
			About: &mac.AboutInfo{
				Title:   "AI Switchboard",
				Message: fmt.Sprintf("Claude/OpenAI API 本地代理转发服务\n版本 %s", Version),
				Icon:    icon,
			},
			WebviewIsTransparent: true,
			WindowIsTranslucent:  false,
		},

		// Windows 配置
		Windows: &windows.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			DisableWindowIcon:    false,
		},
	})

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// formatWailsError 让前端 catch 到的错误带上可读文案。
// Wails 默认只回传 err.Error() 字符串，前端拿到的不是 Error 实例，
// err.message 恒为 undefined，所有 `err.message || '操作失败'` 都会丢掉真实原因。
func formatWailsError(err error) any {
	if err == nil {
		return nil
	}
	return map[string]any{"message": err.Error()}
}

// ============================================================
// 日志相关函数 (从原 main.go 保留)
// ============================================================

// setupLogger 配置结构化日志
func setupLogger(cfg config.LoggingConfig) (*slog.Logger, *logging.BroadcastHandler) {
	var level slog.Level
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	var fileRotator *logging.FileRotator
	// 设置文件日志
	if cfg.FileEnabled {
		maxSize, err := logging.ParseSize(cfg.MaxFileSize)
		if err != nil {
			fmt.Printf("警告：无法解析日志文件大小配置 '%s'，使用默认值 100MB: %v\n", cfg.MaxFileSize, err)
			maxSize = 100 * 1024 * 1024
		}

		fileRotator, err = logging.NewFileRotator(cfg.FilePath, maxSize, cfg.MaxFiles, cfg.CompressRotated)
		if err != nil {
			fmt.Printf("警告：无法创建日志文件轮转器: %v\n", err)
			fileRotator = nil
		}
	}

	// 创建 SimpleHandler（文件和控制台输出）
	simpleHandler := &SimpleHandler{
		level:                    level,
		fileRotator:              fileRotator,
		disableFileResponseLimit: cfg.FileEnabled && cfg.DisableResponseLimit,
	}
	currentLogHandler = simpleHandler

	// 用 BroadcastHandler 包装（添加日志查看功能）
	broadcastHandler := logging.NewBroadcastHandler(simpleHandler, 1000)

	if cfg.FileEnabled {
		fmt.Printf("🔧 文件日志已启用: 路径=%s\n", cfg.FilePath)
	}

	return slog.New(broadcastHandler), broadcastHandler
}

// SimpleHandler 简化的日志处理器
type SimpleHandler struct {
	level                    slog.Level
	fileRotator              *logging.FileRotator
	disableFileResponseLimit bool
}

func (h *SimpleHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *SimpleHandler) Handle(_ context.Context, r slog.Record) error {
	message := r.Message

	var attrs []string
	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, fmt.Sprintf("%s=%v", a.Key, a.Value))
		return true
	})

	if len(attrs) > 0 {
		message = message + " " + strings.Join(attrs, " ")
	}

	timestamp := time.Now().Format("2006-01-02 15:04:05.000")
	pid := os.Getpid()
	gid := getGoroutineID()
	level := "INFO"
	switch r.Level {
	case slog.LevelDebug:
		level = "DEBUG"
	case slog.LevelWarn:
		level = "WARN"
	case slog.LevelError:
		level = "ERROR"
	}

	// 文件输出
	if h.fileRotator != nil {
		fileMessage := message
		if !h.disableFileResponseLimit && len(message) > 500 {
			fileMessage = message[:500] + "... (文件日志截断)"
		}
		formattedMessage := fmt.Sprintf("[%s] [PID:%d] [GID:%d] [%s] %s\n", timestamp, pid, gid, level, fileMessage)
		h.fileRotator.Write([]byte(formattedMessage))
	}

	// 控制台输出
	displayMessage := message
	if len(displayMessage) > 500 {
		displayMessage = displayMessage[:500] + "... (显示截断)"
	}

	consoleMessage := fmt.Sprintf("[%s] [PID:%d] [GID:%d] [%s] %s", timestamp, pid, gid, level, displayMessage)
	fmt.Println(consoleMessage)

	return nil
}

func (h *SimpleHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return h
}

func (h *SimpleHandler) WithGroup(name string) slog.Handler {
	return h
}

func (h *SimpleHandler) Close() error {
	if h.fileRotator != nil {
		h.fileRotator.Sync()
		return h.fileRotator.Close()
	}
	return nil
}

func getGoroutineID() int {
	buf := make([]byte, 64)
	buf = buf[:runtime.Stack(buf, false)]
	idField := strings.Fields(string(buf))[1]
	id, err := strconv.Atoi(idField)
	if err != nil {
		return 0
	}
	return id
}

// ============================================================
// 配置转换函数 (从原 main.go 保留)
// ============================================================

// v5.0+ 注意：convertModelPricing 和 convertModelPricingSingle 已移除
// 模型定价现在从 SQLite model_pricing 表加载，不再依赖 config.yaml
// 相关代码参见：
//   - app.go: syncPricingToTracker()
//   - internal/service/model_pricing.go: ToTrackingPricing()
