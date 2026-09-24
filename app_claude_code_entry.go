// app_claude_code_entry.go - Claude Code 专用入口装配与 Wails API
// 创建时间: 2026-09-24
//
// 入口复用主端口的代理处理器（带日志中间件、不带 AuthMiddleware），只多一层边界校验与入口标记。
// 启动时按持久化配置决定是否监听；绑定失败只记录错误，不影响主端口与其他入口。
package main

import (
	"context"
	"fmt"
	"time"

	"cc-forwarder/internal/proxy"
	"cc-forwarder/internal/service"
)

// claudeCodeEntryStopTimeout 停用或改端口时等待在途请求的上限，超时后强制关闭
const claudeCodeEntryStopTimeout = 10 * time.Second

// ClaudeCodeEntryInfo 前端展示用的入口状态
type ClaudeCodeEntryInfo struct {
	Enabled    bool   `json:"enabled"`     // 持久化的启用意图
	ListenPort int    `json:"listen_port"` // 持久化的端口
	Mode       string `json:"mode"`        // 全局模式：auto / subscription / api
	BaseURL    string `json:"base_url"`    // Claude Code 的 ANTHROPIC_BASE_URL
	Running    bool   `json:"running"`     // 当前是否在监听
	Error      string `json:"error"`       // 最近一次启动失败或监听异常
}

// ClaudeCodeEntryInput 保存入口配置的输入
type ClaudeCodeEntryInput struct {
	Enabled    bool   `json:"enabled"`
	ListenPort int    `json:"listen_port"`
	Mode       string `json:"mode"` // 空值按 auto
}

// setupClaudeCodeEntry 装配入口；必须在 setupProxyHandler 之后调用。
// startup 已持有 a.mu（shutdown 也在 a.mu 下读取运行态）；Wails API 走 claudeCodeEntryMu，这里一并持有。
func (a *App) setupClaudeCodeEntry(ctx context.Context) {
	if a.settingsStore == nil || a.proxyHandler == nil || a.loggingMiddleware == nil {
		a.logger.Warn("⚠️ Claude Code 入口未装配：设置存储或代理处理器不可用")
		return
	}
	a.claudeCodeEntryMu.Lock()
	defer a.claudeCodeEntryMu.Unlock()
	a.claudeCodeEntrySettings = service.NewClaudeCodeEntrySettingsService(a.settingsStore)
	a.claudeCodeEntryRuntime = proxy.NewClaudeCodeEntryRuntime(a.loggingMiddleware.Wrap(a.proxyHandler), a.logger)

	cfg, err := a.claudeCodeEntrySettings.Load(ctx)
	if err != nil {
		a.logger.Error("❌ Claude Code 入口配置读取失败，未启动", "error", err)
		return
	}
	if err := a.claudeCodeEntryRuntime.Apply(ctx, cfg.Enabled, cfg.ListenAddr(), claudeCodeEntryPolicy(cfg)); err != nil {
		a.logger.Error("❌ Claude Code 入口启动失败", "error", err)
	}
}

// GetClaudeCodeEntryStatus 返回入口配置与运行态
func (a *App) GetClaudeCodeEntryStatus() (ClaudeCodeEntryInfo, error) {
	a.claudeCodeEntryMu.Lock()
	defer a.claudeCodeEntryMu.Unlock()
	return a.claudeCodeEntryInfoLocked(context.Background())
}

// SaveClaudeCodeEntrySettings 保存配置并立即应用。
// 配置已落库但监听失败（如端口被占用）时不返回 error，而是在 Error 字段带回原因，前端就地展示。
func (a *App) SaveClaudeCodeEntrySettings(input ClaudeCodeEntryInput) (ClaudeCodeEntryInfo, error) {
	a.claudeCodeEntryMu.Lock()
	defer a.claudeCodeEntryMu.Unlock()
	if a.claudeCodeEntrySettings == nil || a.claudeCodeEntryRuntime == nil {
		return ClaudeCodeEntryInfo{}, fmt.Errorf("Claude Code 入口未初始化")
	}

	ctx := context.Background()
	cfg, err := a.claudeCodeEntrySettings.Save(ctx, service.ClaudeCodeEntryInput{
		Enabled:    input.Enabled,
		ListenPort: input.ListenPort,
		Mode:       input.Mode,
	})
	if err != nil {
		return ClaudeCodeEntryInfo{}, err
	}
	applyCtx, cancel := context.WithTimeout(ctx, claudeCodeEntryStopTimeout)
	defer cancel()
	if err := a.claudeCodeEntryRuntime.Apply(applyCtx, cfg.Enabled, cfg.ListenAddr(), claudeCodeEntryPolicy(cfg)); err != nil {
		a.logger.Error("❌ Claude Code 入口应用配置失败", "error", err)
	}
	return a.claudeCodeEntryInfoLocked(ctx)
}

// claudeCodeEntryInfoLocked 组合持久化配置与运行态；调用方须持有 claudeCodeEntryMu
func (a *App) claudeCodeEntryInfoLocked(ctx context.Context) (ClaudeCodeEntryInfo, error) {
	if a.claudeCodeEntrySettings == nil || a.claudeCodeEntryRuntime == nil {
		return ClaudeCodeEntryInfo{}, fmt.Errorf("Claude Code 入口未初始化")
	}
	cfg, err := a.claudeCodeEntrySettings.Load(ctx)
	if err != nil {
		return ClaudeCodeEntryInfo{}, err
	}
	status := a.claudeCodeEntryRuntime.Status()
	return ClaudeCodeEntryInfo{
		Enabled:    cfg.Enabled,
		ListenPort: cfg.ListenPort,
		Mode:       cfg.Mode,
		BaseURL:    cfg.BaseURL(),
		Running:    status.Running,
		Error:      status.Error,
	}, nil
}

// claudeCodeEntryPolicy 把持久化配置转换为运行态策略
func claudeCodeEntryPolicy(cfg service.ClaudeCodeEntryConfig) proxy.ClaudeCodeEntryPolicy {
	return proxy.ClaudeCodeEntryPolicy{Mode: cfg.Mode}
}
