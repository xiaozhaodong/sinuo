package main

import (
	"context"
	"net/http"
	"time"

	"sinuo/internal/service"
	"sinuo/internal/transport"

	"github.com/wailsapp/wails/v3/pkg/updater"
	"github.com/wailsapp/wails/v3/pkg/updater/providers/github"
)

func defaultAppUpdateStatus(autoCheck bool) AppUpdateStatus {
	return AppUpdateStatus{CurrentVersion: Version, Commit: Commit, BuildTime: BuildTime,
		LocalBuild: Commit == "unknown", State: "idle", AutoCheck: autoCheck}
}

func (a *App) setupAppUpdate(ctx context.Context) {
	if a == nil || a.wailsApp == nil || a.settingsService == nil {
		return
	}
	manager := newAppUpdateManager(a, ctx, a.settingsService.GetBool(ctx, service.CategoryAppUpdate, "auto_check", true))
	a.appUpdate = manager
	metrics := a.monitoringMiddleware
	manager.activeRequests = func() int64 {
		if metrics == nil {
			return 0
		}
		return int64(metrics.GetMetrics().ActiveConnectionCount())
	}
	// 回执不依赖网络，代理或 provider 初始化失败时仍必须处理。
	manager.consumePendingUpdate()
	manager.scheduleUpdateCleanup()
	httpTransport, err := transport.CreateTransport(a.config)
	if err != nil {
		a.logger.Warn("软件更新代理配置无效", "error", err)
		manager.setError("无法连接 GitHub，请检查网络或代理设置")
		return
	}
	if a.config == nil || !a.config.Proxy.Enabled {
		httpTransport.Proxy = http.ProxyFromEnvironment
	}
	client := &http.Client{Timeout: 30 * time.Second, Transport: appUpdateTransport{httpTransport}}
	inner, err := github.New(github.Config{Repository: "xiaozhaodong/sinuo", BaseURL: appUpdateAPIBase, ChecksumAsset: "checksums.txt", HTTPClient: client})
	if err != nil {
		a.logger.Warn("软件更新 GitHub provider 初始化失败", "error", err)
		manager.setError("软件更新检查暂不可用")
		return
	}
	provider := &signedGitHubProvider{inner: inner, client: client, logger: manager.logger,
		onProgress: manager.downloadProgress, onComplete: manager.downloadComplete}
	public, keyErr := decodeAppUpdatePublicKey(appUpdatePublicKey)
	if keyErr != nil {
		a.logger.Warn("软件更新公钥无效，一键更新不可用", "error", keyErr)
	}
	if err := a.wailsApp.Updater.Init(updater.Config{CurrentVersion: Version, Providers: []updater.Provider{provider}, PublicKey: public, Window: updater.WindowNone}); err != nil {
		a.logger.Warn("软件更新器初始化失败", "error", err)
		manager.setError("软件更新检查暂不可用")
		return
	}
	manager.check = a.wailsApp.Updater.Check
	manager.download = a.wailsApp.Updater.DownloadAndInstall
	manager.restart = a.wailsApp.Updater.Restart
	manager.startSchedule()
}
