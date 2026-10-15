package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"sinuo/internal/proxy"
	"sinuo/internal/service"
)

func newSubscriptionProxyTestApp(t *testing.T) *App {
	t.Helper()
	app := newImageGenerationSettingsTestApp(t)
	app.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	app.subscriptionProxySettingsService = service.NewCodexSubscriptionProxySettingsService(app.settingsStore)
	return app
}

func freeAppLoopbackPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("获取空闲端口失败: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

func subscriptionProxyInput(enabled bool, port int) CodexSubscriptionProxyConfigInput {
	return CodexSubscriptionProxyConfigInput{
		Enabled:       enabled,
		Mode:          service.CodexSubscriptionProxyModeSubscription,
		ListenHost:    "127.0.0.1",
		ListenPort:    port,
		WSCompression: service.CodexSubscriptionProxyCompressionAuto,
	}
}

func TestCodexSubscriptionProxyConfigFirstSaveReturnsSecretOnce(t *testing.T) {
	app := newSubscriptionProxyTestApp(t)
	port := freeAppLoopbackPort(t)

	before, err := app.GetCodexSubscriptionProxyConfig()
	if err != nil {
		t.Fatalf("读取配置失败: %v", err)
	}
	if before.Initialized {
		t.Fatal("首次读取不应已初始化")
	}
	if before.Mode != service.CodexSubscriptionProxyModeSubscription || before.ListenPort == 0 {
		t.Fatalf("默认值不符: %+v", before)
	}

	saved, err := app.SaveCodexSubscriptionProxyConfig(subscriptionProxyInput(true, port), 0)
	if err != nil {
		t.Fatalf("首次保存失败: %v", err)
	}
	if saved.AccessSecret == "" {
		t.Fatal("首次初始化应返回一次访问密钥")
	}
	if !strings.Contains(saved.AccessURL, saved.AccessSecret) {
		t.Fatalf("接入地址应包含访问密钥: %q", saved.AccessURL)
	}
	if !strings.Contains(saved.AccessURL, "/backend-api/codex") {
		t.Fatalf("接入地址应保留 provider 后缀: %q", saved.AccessURL)
	}
	// 前端在“改监听地址但不轮换”时会用同一格式本地重建地址，这里把格式钉死。
	wantURL := fmt.Sprintf("http://127.0.0.1:%d/subscription/%s/backend-api/codex", port, saved.AccessSecret)
	if saved.AccessURL != wantURL {
		t.Fatalf("接入地址格式不符:\n got %q\nwant %q", saved.AccessURL, wantURL)
	}
	if saved.Config.Revision != 1 || !saved.Config.Initialized || !saved.Config.AccessConfigured {
		t.Fatalf("保存结果不符: %+v", saved.Config)
	}

	again, err := app.SaveCodexSubscriptionProxyConfig(subscriptionProxyInput(true, port), saved.Config.Revision)
	if err != nil {
		t.Fatalf("二次保存失败: %v", err)
	}
	if again.AccessSecret != "" || again.AccessURL != "" {
		t.Fatal("非初始化保存不应再返回访问密钥")
	}
	if again.Config.Revision != 2 {
		t.Fatalf("revision 应递增: %d", again.Config.Revision)
	}

	loaded, err := app.GetCodexSubscriptionProxyConfig()
	if err != nil {
		t.Fatalf("读取配置失败: %v", err)
	}
	if !loaded.AccessConfigured || loaded.Enabled != true {
		t.Fatalf("读取结果不符: %+v", loaded)
	}
}

func TestCodexSubscriptionProxyConfigRejectsStaleRevision(t *testing.T) {
	app := newSubscriptionProxyTestApp(t)
	port := freeAppLoopbackPort(t)

	saved, err := app.SaveCodexSubscriptionProxyConfig(subscriptionProxyInput(false, port), 0)
	if err != nil {
		t.Fatalf("首次保存失败: %v", err)
	}

	_, err = app.SaveCodexSubscriptionProxyConfig(subscriptionProxyInput(false, port), 0)
	if !errors.Is(err, service.ErrCodexSubscriptionProxyRevisionConflict) {
		t.Fatalf("过期 revision 应返回冲突: %v", err)
	}

	updated, err := app.SaveCodexSubscriptionProxyConfig(subscriptionProxyInput(true, port), saved.Config.Revision)
	if err != nil {
		t.Fatalf("正确 revision 应保存成功: %v", err)
	}
	if !updated.Config.Enabled {
		t.Fatal("启用意图未保存")
	}
}

func TestCodexSubscriptionProxySetEnabledAndRotateAccess(t *testing.T) {
	app := newSubscriptionProxyTestApp(t)
	port := freeAppLoopbackPort(t)

	created, err := app.SaveCodexSubscriptionProxyConfig(subscriptionProxyInput(true, port), 0)
	if err != nil {
		t.Fatalf("首次保存失败: %v", err)
	}
	oldSecret := created.AccessSecret

	disabled, err := app.SetCodexSubscriptionProxyEnabled(false, created.Config.Revision)
	if err != nil {
		t.Fatalf("停用失败: %v", err)
	}
	if disabled.Config.Enabled {
		t.Fatal("停用意图未保存")
	}

	rotated, err := app.RotateCodexSubscriptionProxyAccess(disabled.Config.Revision)
	if err != nil {
		t.Fatalf("轮换失败: %v", err)
	}
	if rotated.AccessSecret == "" || rotated.AccessSecret == oldSecret {
		t.Fatalf("轮换应返回新的访问密钥: %q", rotated.AccessSecret)
	}
	if rotated.Config.Revision != disabled.Config.Revision+1 {
		t.Fatalf("轮换应递增 revision: %d", rotated.Config.Revision)
	}
	if rotated.Config.Enabled {
		t.Fatal("轮换不应改变启用意图")
	}

	settings, _ := app.subscriptionProxyDeps()
	state, err := settings.Load(context.Background())
	if err != nil {
		t.Fatalf("读取配置失败: %v", err)
	}
	hash := state.Config.AccessHashBytes()
	if service.VerifyCodexSubscriptionProxyAccessSecret(hash, oldSecret) {
		t.Fatal("旧密钥应立即失效")
	}
	if !service.VerifyCodexSubscriptionProxyAccessSecret(hash, rotated.AccessSecret) {
		t.Fatal("新密钥应可校验")
	}
}

func TestCodexSubscriptionProxySaveRejectsListenChangeWhileRunning(t *testing.T) {
	app := newSubscriptionProxyTestApp(t)
	port := freeAppLoopbackPort(t)

	created, err := app.SaveCodexSubscriptionProxyConfig(subscriptionProxyInput(true, port), 0)
	if err != nil {
		t.Fatalf("首次保存失败: %v", err)
	}

	runtime := proxy.NewCodexSubscriptionProxyRuntime(nil, app.logger)
	app.subscriptionProxyMu.Lock()
	app.subscriptionProxyRuntime = runtime
	app.subscriptionProxyMu.Unlock()
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runtime.Stop(stopCtx)
	})

	applyCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	settings, _ := app.subscriptionProxyDeps()
	persisted, err := settings.Load(applyCtx)
	if err != nil {
		t.Fatalf("读取配置失败: %v", err)
	}
	snapshot := &proxy.SubscriptionProxySnapshot{
		Revision:      persisted.Config.Revision,
		Enabled:       true,
		Mode:          persisted.Config.Mode,
		ListenHost:    persisted.Config.ListenHost,
		ListenPort:    persisted.Config.ListenPort,
		WSCompression: persisted.Config.WSCompression,
		AccessHash:    persisted.Config.AccessHashBytes(),
	}
	if err := runtime.Apply(applyCtx, snapshot); err != nil {
		t.Fatalf("启动运行态失败: %v", err)
	}

	_, err = app.SaveCodexSubscriptionProxyConfig(subscriptionProxyInput(true, port+1), created.Config.Revision)
	if !errors.Is(err, proxy.ErrSubscriptionProxyListenAddressLocked) {
		t.Fatalf("运行中改端口应被拒绝: %v", err)
	}

	loaded, err := app.GetCodexSubscriptionProxyConfig()
	if err != nil {
		t.Fatalf("读取配置失败: %v", err)
	}
	if loaded.Revision != created.Config.Revision || loaded.ListenPort != port {
		t.Fatalf("被拒绝的保存不应写入配置: %+v", loaded)
	}

	// 停用意图可以带着新端口保存，运行态随后停止。
	stopped, err := app.SaveCodexSubscriptionProxyConfig(subscriptionProxyInput(false, port+1), created.Config.Revision)
	if err != nil {
		t.Fatalf("停用并改端口应被允许: %v", err)
	}
	if stopped.Config.ListenPort != port+1 || stopped.Config.Enabled {
		t.Fatalf("配置不符: %+v", stopped.Config)
	}
}

func TestCodexSubscriptionProxyConfigStaysOutOfGenericSettingsReads(t *testing.T) {
	app := newSubscriptionProxyTestApp(t)

	if _, err := app.SaveCodexSubscriptionProxyConfig(subscriptionProxyInput(false, freeAppLoopbackPort(t)), 0); err != nil {
		t.Fatalf("初始化配置失败: %v", err)
	}

	all, err := app.GetAllSettings()
	if err != nil {
		t.Fatalf("读取设置列表失败: %v", err)
	}
	for _, item := range all {
		if item.Category == service.CategoryCodexSubscriptionProxy {
			t.Fatalf("受保护分类不应出现在通用设置列表: %+v", item)
		}
	}

	// 通用写入入口对该分类仍然明确拒绝，不会出现“列表看不到但能改”。
	if err := app.settingsService.Set(context.Background(), service.CategoryCodexSubscriptionProxy, service.CodexSubscriptionProxyConfigKey, "{}"); !errors.Is(err, service.ErrProtectedSettingsCategory) {
		t.Fatalf("通用写入应被拒绝: %v", err)
	}
	if err := app.settingsService.ResetCategory(context.Background(), service.CategoryCodexSubscriptionProxy); !errors.Is(err, service.ErrProtectedSettingsCategory) {
		t.Fatalf("通用重置应被拒绝: %v", err)
	}
}

func TestCodexSubscriptionProxyStatusWithoutRuntime(t *testing.T) {
	app := newSubscriptionProxyTestApp(t)

	status, err := app.GetCodexSubscriptionProxyStatus()
	if err != nil {
		t.Fatalf("读取状态失败: %v", err)
	}
	if status.State != proxy.SubscriptionProxyStateStopped {
		t.Fatalf("未装配运行态应为 stopped: %+v", status)
	}
	if status.AccessConfigured {
		t.Fatal("未初始化时不应显示已配置访问密钥")
	}
}
