package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type appUpdateEnvironment struct {
	platform, commit, publicKey string
	executable                  func() (string, error)
	writable                    func(string) bool
}

func runtimeAppUpdateEnvironment() appUpdateEnvironment {
	return appUpdateEnvironment{runtime.GOOS, Commit, appUpdatePublicKey, os.Executable, appUpdateDirectoryWritable}
}

func appUpdateInstallBlockedReason(env appUpdateEnvironment) string {
	if env.platform != "darwin" {
		return "当前平台暂不支持一键更新"
	}
	if _, err := decodeAppUpdatePublicKey(env.publicKey); err != nil {
		return "一键更新暂不可用"
	}
	if env.commit == "unknown" {
		return "本地构建不支持一键更新"
	}
	path, err := env.executable()
	if err != nil || strings.Contains(path, "/AppTranslocation/") {
		return "请先把 Sinuo 移到「应用程序」文件夹，重新打开后再更新"
	}
	// Wails 的 bundleTarget 选择最外层 .app；权限检查必须针对同一个替换目标。
	bundle := ""
	for candidate := filepath.Clean(path); candidate != filepath.Dir(candidate); candidate = filepath.Dir(candidate) {
		if strings.HasSuffix(candidate, ".app") {
			bundle = candidate
		}
	}
	if bundle == "" || bundle == filepath.Clean(path) {
		return "请先把 Sinuo 移到「应用程序」文件夹，重新打开后再更新"
	}
	dir := filepath.Dir(bundle)
	if !env.writable(dir) {
		return fmt.Sprintf("没有写入「%s」的权限，请前往下载手动更新", dir)
	}
	return ""
}

// 调用方持有 checkMu；安装资格和最近成功检查的 release 只有这一处判定。
func (m *appUpdateManager) applyInstallSupport(status *AppUpdateStatus) {
	reason := m.baseBlockedReason
	if reason == "" && (m.release == nil || m.release.Metadata["sinuo.signed"] != true) {
		reason = "此版本未提供签名，请前往下载"
	}
	status.InstallBlockedReason = reason
	status.InstallSupported = reason == ""
}

func appUpdateBusy(state string) bool {
	switch state {
	case "downloading", "installing", "ready", "waiting_idle", "restarting":
		return true
	}
	return false
}

// DownloadAppUpdate 只接受已签名的已知版本，后台执行下载、校验和解压。
func (a *App) DownloadAppUpdate() error {
	m := a.appUpdateRuntime()
	if m == nil {
		return errors.New("一键更新暂不可用")
	}
	return m.startDownload()
}

func (m *appUpdateManager) startDownload() error {
	m.checkMu.Lock()
	defer m.checkMu.Unlock()
	if m.rootCtx.Err() != nil {
		return context.Canceled
	}
	status := m.status()
	if appUpdateBusy(status.State) || m.downloadDone != nil {
		return errors.New("更新操作正在进行")
	}
	if !status.UpdateAvailable {
		return errors.New("没有可安装的新版本")
	}
	if m.baseBlockedReason != "" {
		return errors.New(m.baseBlockedReason)
	}
	if m.release == nil || m.release.Metadata["sinuo.signed"] != true || !appUpdateVerifiedRelease(m.release) {
		return errors.New("安装包缺少签名或校验值，已拒绝安装")
	}
	if strings.TrimPrefix(m.release.Version, "v") != status.LatestVersion {
		return errors.New("更新信息已变化，请重新检查更新")
	}
	if m.download == nil {
		return errors.New("一键更新暂不可用")
	}
	ctx, cancel := context.WithCancel(m.rootCtx)
	done := make(chan struct{})
	m.downloadCancel, m.downloadDone = cancel, done
	m.progressAt = time.Time{}
	version := status.LatestVersion
	m.update(func(s *AppUpdateStatus) {
		s.State, s.Error, s.ErrorStage, s.ReadyVersion = "downloading", "", "", ""
		s.ProgressWritten, s.ProgressTotal = 0, 0
	})
	go func() {
		err := m.download(ctx)
		m.checkMu.Lock()
		defer m.checkMu.Unlock()
		defer close(done)
		defer cancel()
		m.downloadCancel, m.downloadDone = nil, nil
		m.update(func(s *AppUpdateStatus) {
			if ctx.Err() != nil {
				s.State, s.Error, s.ErrorStage = "available", "", ""
				s.ProgressWritten, s.ProgressTotal = 0, 0
			} else if err != nil {
				s.ErrorStage = appUpdateFailureStage(s.State, err)
				s.State, s.Error = "error", appUpdateInstallError(s.ErrorStage)
			} else {
				s.State, s.ReadyVersion, s.ErrorStage = "ready", version, ""
			}
		})
		if err != nil && ctx.Err() == nil {
			m.logger.Warn("软件更新安装失败", "error", err)
		}
	}()
	return nil
}

func appUpdateInstallError(stage string) string {
	switch stage {
	case "verify":
		return "安装包校验失败，已拒绝安装，请重试或前往下载"
	case "install":
		return "安装更新失败，请重试或前往下载"
	default:
		return "下载更新失败，请检查网络或代理设置后重试"
	}
}

func (m *appUpdateManager) downloadProgress(written, total int64) {
	m.checkMu.Lock()
	defer m.checkMu.Unlock()
	if m.downloadDone == nil || m.status().State != "downloading" {
		return
	}
	if time.Since(m.progressAt) < 250*time.Millisecond {
		return
	}
	m.progressAt = time.Now()
	m.update(func(s *AppUpdateStatus) { s.ProgressWritten, s.ProgressTotal = written, total })
}

func (m *appUpdateManager) downloadComplete() {
	m.checkMu.Lock()
	defer m.checkMu.Unlock()
	if m.downloadDone == nil {
		return
	}
	m.update(func(s *AppUpdateStatus) { s.State = "installing" })
}

func appUpdateFailureStage(state string, err error) string {
	if state == "downloading" {
		return "download"
	}
	// beta.27 的 Error 事件异步投递，返回错误才是本次操作的可靠结果。
	// provider 完成流式传输后，Wails 的 verify 错误都带有下列摘要/签名标识。
	for _, marker := range []string{"digest", "signature", "ed25519", "public key"} {
		if strings.Contains(err.Error(), marker) {
			return "verify"
		}
	}
	return "install"
}

// CancelAppUpdateDownload 取消下载；等待该任务结束再允许重试，防止旧回调覆盖新状态。
func (a *App) CancelAppUpdateDownload() error {
	m := a.appUpdateRuntime()
	if m == nil {
		return errors.New("没有正在下载的更新")
	}
	m.checkMu.Lock()
	if m.downloadCancel == nil {
		m.checkMu.Unlock()
		return errors.New("没有正在下载的更新")
	}
	m.downloadCancel()
	done := m.downloadDone
	m.checkMu.Unlock()
	<-done
	return nil
}
