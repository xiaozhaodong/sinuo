// main.go - Sinuo Wails 应用入口
// 保留原有的核心功能，添加 Wails 桌面应用支持

package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"sinuo/config"
	"sinuo/internal/appdir"
	"sinuo/internal/logging"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/updater"
)

// 版本信息
var (
	Version   = "6.3.0"
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
	// 助手进程是 App 自身重新执行，必须在 flag、数据迁移和任何业务初始化前退出。
	updater.HandleHelperMode()
	flag.Parse()

	// 处理版本标志
	if *showVersion {
		fmt.Printf("Sinuo Desktop\n")
		fmt.Printf("Version: %s\n", Version)
		fmt.Printf("Commit: %s\n", Commit)
		fmt.Printf("Built: %s\n", BuildTime)
		os.Exit(0)
	}

	if err := runWithAppDataMigration(runDesktop, showAppDataMigrationError); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// runWithAppDataMigration 是业务初始化的唯一前置门槛；失败不能进入正常桌面启动。
func runWithAppDataMigration(start func() error, showError func(error)) error {
	migrated, err := appdir.MigrateLegacyAppDataDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "应用数据目录迁移失败（旧数据仍保留在原目录）: %v\n", err)
		showError(err)
		return err
	}
	if migrated {
		fmt.Printf("✅ 应用数据目录已迁移: %s -> %s\n", appdir.LegacyDesktopName, appdir.DesktopName)
	}
	return start()
}

// showAppDataMigrationError 只运行原生事件循环，不注册业务服务、窗口或托盘。
// v3 Dialog.Show 需要已就绪的原生主线程，因此等 ApplicationStarted 后再弹框。
func showAppDataMigrationError(migrationErr error) {
	dialogApp := application.New(application.Options{Name: "Sinuo", Icon: icon})
	dialogApp.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		dialog := dialogApp.Dialog.Error().SetTitle("Sinuo 数据目录迁移失败")
		dialog.SetMessage(fmt.Sprintf("请先完全退出 AI Switchboard（菜单栏图标 → 退出）后再启动 %s；若旧 App 已退出仍提示，请打开一次旧 App 再正常退出。\n\n旧数据仍保留在原目录。\n%v", appdir.DesktopName, migrationErr))
		buttonLabel := "退出"
		if runtime.GOOS == "windows" {
			// v3 Windows MessageBox 只按固定英文响应名匹配回调。
			buttonLabel = "Ok"
		}
		dialog.AddButton(buttonLabel).SetAsDefault().SetAsCancel().OnClick(func() {
			go dialogApp.Quit()
		})
		dialog.Show()
	})
	if err := dialogApp.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "无法显示迁移错误对话框: %v\n", err)
	}
}

func runDesktop() error {
	// 创建应用实例
	app := NewApp()
	app.configPath = *configPath
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "config" {
			app.configPathExplicit = true
		}
	})

	// 创建 v3 应用实例。ServiceStartup 只登记上下文并异步初始化业务组件，
	// 这样迁移恢复页可以在后台启动期间正常显示。
	wailsApp := application.New(application.Options{
		Name:         "Sinuo",
		Description:  fmt.Sprintf("AI 编程工具的本地网关\n版本 %s", Version),
		Icon:         icon,
		MarshalError: marshalWailsError,
		ShouldQuit:   app.shouldQuit,
		ErrorHandler: func(err error) {
			if app.logger != nil {
				app.logger.Error("Wails 运行时错误", "error", err)
				return
			}
			slog.Default().Error("Wails 运行时错误", "error", err)
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: false,
		},
		Windows: application.WindowsOptions{DisableQuitOnLastWindowClosed: true},
		Linux:   application.LinuxOptions{DisableQuitOnLastWindowClosed: true},
	})
	app.wailsApp = wailsApp
	if runtime.GOOS == "darwin" {
		menu := wailsApp.NewMenu()
		menu.AddRole(application.AppMenu)
		menu.AddRole(application.EditMenu)
		menu.AddRole(application.WindowMenu)
		wailsApp.Menu.Set(menu)
	}

	window := wailsApp.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:     "",
		Width:     1280,
		Height:    800,
		MinWidth:  1024,
		MinHeight: 600,
		Hidden:    runtime.GOOS == "windows",
		// 背景色在窗口创建到 WebView 首帧之间可见；v3 默认构建下透明配置为空操作，
		// 因此保持窗口不透明并使用暗色 canvas #0b1120，避免首帧前显示 clearColor。
		BackgroundColour: application.NewRGBA(11, 17, 32, 255),
		Mac: application.MacWindow{
			Backdrop: application.MacBackdropNormal,
			// 统一工具栏：红绿灯下移到 52pt 工具栏的竖直中线，前端侧边栏顶部与内容区工具栏同高。
			TitleBar: application.MacTitleBarHiddenInsetUnified,
		},
		Windows: application.WindowsWindow{
			DisableIcon:  false,
			BackdropType: application.None,
		},
		URL: "/",
	})
	window.OnWindowEvent(events.Common.WindowRuntimeReady, func(_ *application.WindowEvent) {
		app.domReady()
	})
	app.setupTray(window)
	wailsApp.RegisterService(application.NewService(app))

	return wailsApp.Run()
}

// formatWailsError 提供给 MarshalError 的错误字段结构。
// v3 前端最终收到的是 Error 实例，message 来自 err.Error()。
func formatWailsError(err error) map[string]any {
	if err == nil {
		return nil
	}
	return map[string]any{"message": err.Error()}
}

// marshalWailsError：v3 前端收到的是 Error 实例，message 来自 err.Error()；
// MarshalError 只决定序列化后的 cause 字段。
func marshalWailsError(err error) []byte {
	if err == nil {
		return nil
	}
	payload, marshalErr := json.Marshal(formatWailsError(err))
	if marshalErr != nil {
		return nil
	}
	return payload
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
