// app_subscription_proxy.go - Codex 订阅代理运行态装配
// 创建时间: 2026-09-10
//
// 装配顺序：专用配置服务 → 新入口处理器 → 运行态。启动时读取持久化配置，
// 只有 enabled=true 才绑定独立回环 listener；启动失败保留真实错误并推送状态摘要，
// 不影响旧入口、Claude 转发与账号池。
package main

import (
	"context"
	"fmt"
	"time"

	"cc-forwarder/internal/proxy"
	"cc-forwarder/internal/service"
)

// setupCodexSubscriptionProxy 装配新入口；必须在 setupProxyHandler 之后调用。
func (a *App) setupCodexSubscriptionProxy(ctx context.Context) {
	if a.settingsStore == nil {
		a.logger.Warn("⚠️ Codex 订阅代理未装配：设置存储不可用")
		return
	}

	settingsService := service.NewCodexSubscriptionProxySettingsService(a.settingsStore)

	var subscriptionRuntime *proxy.CodexSubscriptionProxyRuntime
	handler := proxy.NewCodexSubscriptionProxyHandler(proxy.SubscriptionProxyHandlerOptions{
		Proxy:      a.proxyHandler,
		Monitoring: a.monitoringMiddleware,
		Logger:     a.logger,
		Snapshot: func() *proxy.SubscriptionProxySnapshot {
			if subscriptionRuntime == nil {
				return nil
			}
			return subscriptionRuntime.Snapshot()
		},
	})
	subscriptionRuntime = proxy.NewCodexSubscriptionProxyRuntime(handler, a.logger)

	a.subscriptionProxyMu.Lock()
	a.subscriptionProxySettingsService = settingsService
	a.subscriptionProxyHandler = handler
	a.subscriptionProxyRuntime = subscriptionRuntime
	a.subscriptionProxyMu.Unlock()

	a.startCodexSubscriptionProxy(ctx)
}

// startCodexSubscriptionProxy 按持久化配置启动新入口；未初始化或未启用时不监听。
func (a *App) startCodexSubscriptionProxy(ctx context.Context) {
	settings, runtime := a.subscriptionProxyDeps()
	if runtime == nil || settings == nil {
		return
	}
	state, err := settings.Load(ctx)
	if err != nil {
		a.logger.Error("❌ Codex 订阅代理配置读取失败，未启动", "error", err)
		return
	}
	if !state.Initialized || !state.Config.Enabled {
		return
	}
	if err := a.applyCodexSubscriptionProxySnapshot(ctx, state.Config); err != nil {
		a.logger.Error("❌ Codex 订阅代理启动失败", "error", err)
		a.emitCodexSubscriptionProxyStatus()
		return
	}
	a.logger.Info("✅ Codex 订阅代理已启动", "listen", state.Config.ListenAddr(), "mode", state.Config.Mode)
	a.emitCodexSubscriptionProxyStatus()
}

// applyCodexSubscriptionProxySnapshot 把持久化配置转成运行态快照并应用。
// 运行中修改监听地址由运行态拒绝，调用方应先把错误回传给前端。
func (a *App) applyCodexSubscriptionProxySnapshot(ctx context.Context, cfg service.CodexSubscriptionProxyConfig) error {
	_, runtime := a.subscriptionProxyDeps()
	if runtime == nil {
		return nil
	}
	snapshot, err := codexSubscriptionProxySnapshot(cfg)
	if err != nil {
		return err
	}
	return runtime.Apply(ctx, snapshot)
}

// stopCodexSubscriptionProxy 停用新入口；不触碰旧入口与共用服务。
func (a *App) stopCodexSubscriptionProxy(ctx context.Context) {
	_, runtime := a.subscriptionProxyDeps()
	if runtime == nil {
		return
	}
	stopCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := runtime.Stop(stopCtx); err != nil {
		a.logger.Error("Codex 订阅代理停用失败", "error", err)
	}
}

// codexSubscriptionProxySnapshot 把持久化配置转成运行态快照。
func codexSubscriptionProxySnapshot(cfg service.CodexSubscriptionProxyConfig) (*proxy.SubscriptionProxySnapshot, error) {
	hash := cfg.AccessHashBytes()
	if cfg.Enabled && len(hash) == 0 {
		return nil, fmt.Errorf("Codex 订阅代理缺少可用访问密钥，请重新初始化或轮换后再启用")
	}
	return &proxy.SubscriptionProxySnapshot{
		Revision:      cfg.Revision,
		Enabled:       cfg.Enabled,
		Mode:          cfg.Mode,
		ListenHost:    cfg.ListenHost,
		ListenPort:    cfg.ListenPort,
		WSCompression: cfg.WSCompression,
		AccessHash:    hash,
	}, nil
}

// emitCodexSubscriptionProxyStatus 只推送运行态摘要，不触发请求列表刷新。
func (a *App) emitCodexSubscriptionProxyStatus() {
	if a.ctx == nil {
		return
	}
	status, err := a.GetCodexSubscriptionProxyStatus()
	if err != nil {
		a.logger.Warn("⚠️ Codex 订阅代理状态读取失败", "error", err)
		return
	}
	a.emit(EventCodexSubscriptionProxyStatus, status)
}
