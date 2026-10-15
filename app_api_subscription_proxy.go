// app_api_subscription_proxy.go - Codex 订阅代理 Wails API
// 创建时间: 2026-09-10
//
// 五个入口：读取配置、保存配置、读取运行态、启停、轮换访问密钥。
// 访问密钥明文只在初始化与轮换的返回值中出现一次；其余读取只返回“是否已配置”。
package main

import (
	"context"
	"fmt"
	"time"

	"sinuo/internal/proxy"
	"sinuo/internal/service"
)

// CodexSubscriptionProxyConfigInfo 配置读取结果。
type CodexSubscriptionProxyConfigInfo struct {
	Initialized      bool   `json:"initialized"`
	Enabled          bool   `json:"enabled"`
	Mode             string `json:"mode"`
	ListenHost       string `json:"listen_host"`
	ListenPort       int    `json:"listen_port"`
	WSCompression    string `json:"ws_compression"`
	Revision         int64  `json:"revision"`
	AccessConfigured bool   `json:"access_configured"`
}

// CodexSubscriptionProxyConfigInput 保存输入；不含 revision 与密钥。
type CodexSubscriptionProxyConfigInput struct {
	Enabled       bool   `json:"enabled"`
	Mode          string `json:"mode"`
	ListenHost    string `json:"listen_host"`
	ListenPort    int    `json:"listen_port"`
	WSCompression string `json:"ws_compression"`
}

// CodexSubscriptionProxySaveResultInfo 保存结果；AccessSecret/AccessURL 仅首次初始化与轮换时返回。
type CodexSubscriptionProxySaveResultInfo struct {
	Config       CodexSubscriptionProxyConfigInfo `json:"config"`
	AccessSecret string                           `json:"access_secret,omitempty"`
	AccessURL    string                           `json:"access_url,omitempty"`
}

// CodexSubscriptionProxyStatusInfo 运行态摘要。
type CodexSubscriptionProxyStatusInfo struct {
	State            string `json:"state"`
	Enabled          bool   `json:"enabled"`
	Mode             string `json:"mode"`
	Revision         int64  `json:"revision"`
	ListenAddr       string `json:"listen_addr"`
	ActiveRequests   int64  `json:"active_requests"`
	ActiveWebSockets int    `json:"active_websockets"`
	LastError        string `json:"last_error"`
	AccessConfigured bool   `json:"access_configured"`
}

func (a *App) subscriptionProxyDeps() (*service.CodexSubscriptionProxySettingsService, *proxy.CodexSubscriptionProxyRuntime) {
	a.subscriptionProxyMu.RLock()
	defer a.subscriptionProxyMu.RUnlock()
	return a.subscriptionProxySettingsService, a.subscriptionProxyRuntime
}

func (a *App) subscriptionProxySettingsServiceOrError() (*service.CodexSubscriptionProxySettingsService, error) {
	settings, _ := a.subscriptionProxyDeps()
	if settings == nil {
		return nil, fmt.Errorf("Codex 订阅代理配置服务未初始化")
	}
	return settings, nil
}

func codexSubscriptionProxyAPIContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

// GetCodexSubscriptionProxyConfig 读取配置；不返回访问密钥。
func (a *App) GetCodexSubscriptionProxyConfig() (CodexSubscriptionProxyConfigInfo, error) {
	settings, err := a.subscriptionProxySettingsServiceOrError()
	if err != nil {
		return CodexSubscriptionProxyConfigInfo{}, err
	}
	ctx, cancel := codexSubscriptionProxyAPIContext()
	defer cancel()

	state, err := settings.Load(ctx)
	if err != nil {
		return CodexSubscriptionProxyConfigInfo{}, err
	}
	return codexSubscriptionProxyConfigInfo(state), nil
}

// SaveCodexSubscriptionProxyConfig 保存完整配置；expectedRevision 不匹配时拒绝覆盖。
func (a *App) SaveCodexSubscriptionProxyConfig(input CodexSubscriptionProxyConfigInput, expectedRevision int64) (CodexSubscriptionProxySaveResultInfo, error) {
	settings, err := a.subscriptionProxySettingsServiceOrError()
	if err != nil {
		return CodexSubscriptionProxySaveResultInfo{}, err
	}
	normalized, err := service.NormalizeCodexSubscriptionProxyInput(service.CodexSubscriptionProxyConfigInput{
		Enabled:       input.Enabled,
		Mode:          input.Mode,
		ListenHost:    input.ListenHost,
		ListenPort:    input.ListenPort,
		WSCompression: input.WSCompression,
	})
	if err != nil {
		return CodexSubscriptionProxySaveResultInfo{}, err
	}
	if err := a.validateSubscriptionProxyListenChange(normalized); err != nil {
		return CodexSubscriptionProxySaveResultInfo{}, err
	}

	ctx, cancel := codexSubscriptionProxyAPIContext()
	defer cancel()
	result, err := settings.Save(ctx, normalized, expectedRevision)
	if err != nil {
		return CodexSubscriptionProxySaveResultInfo{}, err
	}
	a.applyCodexSubscriptionProxyRuntime(ctx, result.Config)
	return codexSubscriptionProxySaveResultInfo(result), nil
}

// SetCodexSubscriptionProxyEnabled 只切换启用意图；未初始化时返回明确错误。
func (a *App) SetCodexSubscriptionProxyEnabled(enabled bool, expectedRevision int64) (CodexSubscriptionProxySaveResultInfo, error) {
	settings, err := a.subscriptionProxySettingsServiceOrError()
	if err != nil {
		return CodexSubscriptionProxySaveResultInfo{}, err
	}
	ctx, cancel := codexSubscriptionProxyAPIContext()
	defer cancel()
	result, err := settings.SetEnabled(ctx, enabled, expectedRevision)
	if err != nil {
		return CodexSubscriptionProxySaveResultInfo{}, err
	}
	a.applyCodexSubscriptionProxyRuntime(ctx, result.Config)
	return codexSubscriptionProxySaveResultInfo(result), nil
}

// RotateCodexSubscriptionProxyAccess 轮换访问密钥；旧接入地址立即失效，新明文只返回一次。
func (a *App) RotateCodexSubscriptionProxyAccess(expectedRevision int64) (CodexSubscriptionProxySaveResultInfo, error) {
	settings, err := a.subscriptionProxySettingsServiceOrError()
	if err != nil {
		return CodexSubscriptionProxySaveResultInfo{}, err
	}
	ctx, cancel := codexSubscriptionProxyAPIContext()
	defer cancel()
	result, err := settings.RotateAccess(ctx, expectedRevision)
	if err != nil {
		return CodexSubscriptionProxySaveResultInfo{}, err
	}
	a.applyCodexSubscriptionProxyRuntime(ctx, result.Config)
	return codexSubscriptionProxySaveResultInfo(result), nil
}

// GetCodexSubscriptionProxyStatus 返回运行态与访问密钥配置状态。
func (a *App) GetCodexSubscriptionProxyStatus() (CodexSubscriptionProxyStatusInfo, error) {
	info := CodexSubscriptionProxyStatusInfo{State: proxy.SubscriptionProxyStateStopped}
	settings, runtime := a.subscriptionProxyDeps()
	if runtime != nil {
		status := runtime.Status()
		info.State = status.State
		info.Enabled = status.Enabled
		info.Mode = status.Mode
		info.Revision = status.Revision
		info.ListenAddr = status.ListenAddr
		info.ActiveRequests = status.ActiveRequests
		info.ActiveWebSockets = status.ActiveWebSockets
		info.LastError = status.LastError
	}
	if settings != nil {
		ctx, cancel := codexSubscriptionProxyAPIContext()
		defer cancel()
		state, err := settings.Load(ctx)
		if err != nil {
			return info, err
		}
		info.AccessConfigured = state.Config.AccessConfigured()
		if info.Mode == "" {
			info.Mode = state.Config.Mode
		}
		if info.Revision == 0 {
			info.Revision = state.Config.Revision
		}
	}
	return info, nil
}

// validateSubscriptionProxyListenChange 运行中且仍要启用时，拒绝修改监听地址。
func (a *App) validateSubscriptionProxyListenChange(input service.CodexSubscriptionProxyConfigInput) error {
	if !input.Enabled {
		return nil
	}
	_, runtime := a.subscriptionProxyDeps()
	if runtime == nil {
		return nil
	}
	return runtime.ValidateListenAddressChange(input.ListenHost, input.ListenPort)
}

// applyCodexSubscriptionProxyRuntime 保存成功后同步运行态；失败只回传错误，不回滚已保存的意图。
func (a *App) applyCodexSubscriptionProxyRuntime(ctx context.Context, cfg service.CodexSubscriptionProxyConfig) {
	if err := a.applyCodexSubscriptionProxySnapshot(ctx, cfg); err != nil {
		a.logger.Error("❌ Codex 订阅代理应用配置失败", "error", err)
	}
	a.emitCodexSubscriptionProxyStatus()
}

func codexSubscriptionProxyConfigInfo(state service.CodexSubscriptionProxyConfigState) CodexSubscriptionProxyConfigInfo {
	return CodexSubscriptionProxyConfigInfo{
		Initialized:      state.Initialized,
		Enabled:          state.Config.Enabled,
		Mode:             state.Config.Mode,
		ListenHost:       state.Config.ListenHost,
		ListenPort:       state.Config.ListenPort,
		WSCompression:    state.Config.WSCompression,
		Revision:         state.Config.Revision,
		AccessConfigured: state.Config.AccessConfigured(),
	}
}

func codexSubscriptionProxySaveResultInfo(result service.CodexSubscriptionProxySaveResult) CodexSubscriptionProxySaveResultInfo {
	info := CodexSubscriptionProxySaveResultInfo{
		Config: CodexSubscriptionProxyConfigInfo{
			Initialized:      true,
			Enabled:          result.Config.Enabled,
			Mode:             result.Config.Mode,
			ListenHost:       result.Config.ListenHost,
			ListenPort:       result.Config.ListenPort,
			WSCompression:    result.Config.WSCompression,
			Revision:         result.Config.Revision,
			AccessConfigured: result.Config.AccessConfigured(),
		},
	}
	if result.AccessSecret != "" {
		info.AccessSecret = result.AccessSecret
		info.AccessURL = service.BuildCodexSubscriptionProxyAccessURL(result.Config, result.AccessSecret)
	}
	return info
}
