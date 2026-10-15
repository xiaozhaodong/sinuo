package main

import (
	"context"
	"database/sql"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sinuo/config"
	"sinuo/internal/endpoint"
	"sinuo/internal/middleware"
	"sinuo/internal/proxy"
	"sinuo/internal/service"
	"sinuo/internal/store"

	_ "github.com/mattn/go-sqlite3"
)

// newClaudeCodeEntryTestApp 装配一个只有设置存储与代理处理器的 App，入口配置全部走真实服务
func newClaudeCodeEntryTestApp(t *testing.T) *App {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	schemaSQL, err := os.ReadFile(filepath.Join("internal", "tracking", "schema.sql"))
	if err != nil {
		t.Fatalf("read schema failed: %v", err)
	}
	if _, err := db.Exec(string(schemaSQL)); err != nil {
		t.Fatalf("exec schema failed: %v", err)
	}

	cfg := &config.Config{}
	manager := endpoint.NewManager(cfg)
	t.Cleanup(manager.Stop)
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	app := &App{
		logger:            logger,
		settingsStore:     store.NewSQLiteSettingsStore(db),
		proxyHandler:      proxy.NewHandler(manager, cfg),
		loggingMiddleware: middleware.NewLoggingMiddleware(logger),
	}
	t.Cleanup(func() {
		if app.claudeCodeEntryRuntime != nil {
			_ = app.claudeCodeEntryRuntime.Stop(context.Background())
		}
	})
	return app
}

func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port failed: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

// assertClaudeQuotaActive 开启时没有凭据会被拒为「没有凭据」，关闭时被拒为「未启用」
func assertClaudeQuotaActive(t *testing.T, app *App, want bool, label string) {
	t.Helper()
	_, err := app.RefreshClaudeSubscriptionRateLimit()
	if err == nil {
		t.Fatalf("%s：没有凭据时手动刷新不应成功", label)
	}
	if inactive := strings.Contains(err.Error(), "未启用"); inactive == want {
		t.Fatalf("%s：额度开关应为 %v，got err=%v", label, want, err)
	}
}

func TestClaudeCodeEntry_SubscriptionQuotaFollowsEnabledSubscriptionMode(t *testing.T) {
	app := newClaudeCodeEntryTestApp(t)
	app.setupClaudeCodeEntry(context.Background())
	assertClaudeQuotaActive(t, app, false, "默认未启用入口")

	port := freeLoopbackPort(t)
	info, err := app.SaveClaudeCodeEntrySettings(ClaudeCodeEntryInput{Enabled: true, ListenPort: port, Mode: endpoint.ClaudeCodeEntryModeSubscription})
	if err != nil || !info.Enabled || info.Mode != endpoint.ClaudeCodeEntryModeSubscription {
		t.Fatalf("启用订阅直通失败: err=%v %+v", err, info)
	}
	assertClaudeQuotaActive(t, app, true, "启用且订阅直通")

	if _, err := app.SetClaudeCodeEntryMode(endpoint.ClaudeCodeEntryModeEndpoint); err != nil {
		t.Fatalf("切到端点模式失败: %v", err)
	}
	assertClaudeQuotaActive(t, app, false, "切到端点模式")

	if _, err := app.SetClaudeCodeEntryMode(endpoint.ClaudeCodeEntryModeSubscription); err != nil {
		t.Fatalf("切回订阅直通失败: %v", err)
	}
	assertClaudeQuotaActive(t, app, true, "切回订阅直通")

	if _, err := app.SaveClaudeCodeEntrySettings(ClaudeCodeEntryInput{Enabled: false, ListenPort: port, Mode: endpoint.ClaudeCodeEntryModeSubscription}); err != nil {
		t.Fatalf("停用入口失败: %v", err)
	}
	assertClaudeQuotaActive(t, app, false, "停用入口")
}

func TestClaudeCodeEntry_SubscriptionQuotaRestoredOnStartup(t *testing.T) {
	app := newClaudeCodeEntryTestApp(t)
	settings := service.NewClaudeCodeEntrySettingsService(app.settingsStore)
	if _, err := settings.Save(context.Background(), service.ClaudeCodeEntryInput{Enabled: true, ListenPort: freeLoopbackPort(t), Mode: "auto"}); err != nil {
		t.Fatalf("预置入口配置失败: %v", err)
	}
	app.setupClaudeCodeEntry(context.Background())
	assertClaudeQuotaActive(t, app, true, "启动时按持久化配置开启（旧值 auto 按订阅直通）")
}
