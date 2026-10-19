package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"sinuo/internal/service"
	"sinuo/internal/transport"

	"github.com/wailsapp/wails/v3/pkg/updater"
	"github.com/wailsapp/wails/v3/pkg/updater/providers/github"
)

const (
	appUpdateStateCategory      = "app_update_state"
	appUpdateSkippedVersionKey  = "skipped_version"
	appUpdateNotifiedVersionKey = "notified_version"
	appUpdateCheckDelay         = time.Minute
	appUpdateCheckInterval      = 6 * time.Hour
	appUpdateReleaseURLPrefix   = "https://github.com/xiaozhaodong/sinuo/releases/"
	appUpdateLatestReleaseURL   = appUpdateReleaseURLPrefix + "latest"
	appUpdateNotesMarker        = "<!-- sinuo:release-notes-end -->"
)

var appUpdateNoAssetVersionPattern = regexp.MustCompile(`release\s+(v?[0-9][^ ]*)\s+has no asset`)

// AppUpdateStatus 是前端展示的更新检查状态。P1 只检查版本，不下载或安装。
type AppUpdateStatus struct {
	CurrentVersion  string `json:"current_version"`
	Commit          string `json:"commit"`
	BuildTime       string `json:"build_time"`
	LocalBuild      bool   `json:"local_build"`
	State           string `json:"state"`
	UpdateAvailable bool   `json:"update_available"`
	LatestVersion   string `json:"latest_version"`
	ReleaseName     string `json:"release_name"`
	Notes           string `json:"notes"`
	PublishedAt     string `json:"published_at"`
	CheckedAt       string `json:"checked_at"`
	ReleaseURL      string `json:"release_url"`
	Error           string `json:"error"`
	Skipped         bool   `json:"skipped"`
	AutoCheck       bool   `json:"auto_check"`
	Revision        int64  `json:"revision"`
}

type appUpdateManager struct {
	app    *App
	logger *slog.Logger

	stateMu sync.RWMutex
	state   AppUpdateStatus
	checkMu sync.Mutex

	rootCtx context.Context
	cancel  context.CancelFunc

	scheduleMu     sync.Mutex
	scheduleCancel context.CancelFunc
	scheduleDone   chan struct{}
	firstDelay     time.Duration
	interval       time.Duration

	check  func(context.Context) (*updater.Release, error)
	notify func(string, string, string)
}

func newAppUpdateManager(app *App, parent context.Context, autoCheck bool) *appUpdateManager {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	manager := &appUpdateManager{
		app:        app,
		rootCtx:    ctx,
		cancel:     cancel,
		state:      defaultAppUpdateStatus(autoCheck),
		firstDelay: appUpdateCheckDelay,
		interval:   appUpdateCheckInterval,
		logger:     slog.Default(),
	}
	if app != nil {
		manager.notify = app.emitNotification
		if app.logger != nil {
			manager.logger = app.logger
		}
	}
	return manager
}

func defaultAppUpdateStatus(autoCheck bool) AppUpdateStatus {
	return AppUpdateStatus{
		CurrentVersion: Version,
		Commit:         Commit,
		BuildTime:      BuildTime,
		LocalBuild:     Commit == "unknown",
		State:          "idle",
		AutoCheck:      autoCheck,
	}
}

func (a *App) setupAppUpdate(ctx context.Context) {
	if a == nil || a.wailsApp == nil || a.settingsService == nil {
		return
	}
	autoCheck := a.settingsService.GetBool(ctx, service.CategoryAppUpdate, "auto_check", true)
	manager := newAppUpdateManager(a, ctx, autoCheck)
	a.appUpdate = manager

	httpTransport, err := transport.CreateTransport(a.config)
	if err != nil {
		a.logger.Warn("软件更新代理配置无效，更新检查不可用", "error", err)
		manager.setError("无法连接 GitHub，请检查网络或代理设置")
		return
	}
	if a.config == nil || !a.config.Proxy.Enabled {
		httpTransport.Proxy = http.ProxyFromEnvironment
	}
	provider, err := github.New(github.Config{
		Repository:    "xiaozhaodong/sinuo",
		ChecksumAsset: "checksums.txt",
		HTTPClient:    &http.Client{Timeout: 30 * time.Second, Transport: appUpdateTransport{httpTransport}},
	})
	if err != nil {
		a.logger.Warn("软件更新 GitHub provider 初始化失败", "error", err)
		manager.setError("无法连接 GitHub，请检查网络或代理设置")
		return
	}
	if err := a.wailsApp.Updater.Init(updater.Config{
		CurrentVersion: Version,
		Providers:      []updater.Provider{provider},
		Window:         updater.WindowNone,
	}); err != nil {
		a.logger.Warn("软件更新器初始化失败", "error", err)
		manager.setError("软件更新检查暂不可用")
		return
	}
	manager.check = a.wailsApp.Updater.Check
	manager.startSchedule()
}

func (m *appUpdateManager) startSchedule() {
	m.scheduleMu.Lock()
	if m.scheduleCancel != nil {
		m.scheduleCancel()
		m.scheduleCancel = nil
	}
	if m.rootCtx.Err() != nil || !m.status().AutoCheck || m.check == nil {
		m.scheduleMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(m.rootCtx)
	m.scheduleCancel = cancel
	done := make(chan struct{})
	m.scheduleDone = done
	m.scheduleMu.Unlock()

	go func() {
		defer close(done)
		timer := time.NewTimer(m.firstDelay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			m.runCheck(ctx, false)
		}
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.runCheck(ctx, false)
			}
		}
	}()
}

func (m *appUpdateManager) OnSettingsChanged() {
	if m == nil || m.app == nil || m.app.settingsService == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	autoCheck := m.app.settingsService.GetBool(ctx, service.CategoryAppUpdate, "auto_check", true)
	if autoCheck == m.status().AutoCheck {
		return
	}
	m.update(func(status *AppUpdateStatus) {
		status.AutoCheck = autoCheck
	})
	m.startSchedule()
}

func (m *appUpdateManager) Stop() {
	if m == nil {
		return
	}
	m.scheduleMu.Lock()
	m.cancel()
	if m.scheduleCancel != nil {
		m.scheduleCancel()
		m.scheduleCancel = nil
	}
	done := m.scheduleDone
	m.scheduleMu.Unlock()
	if done != nil {
		<-done
	}
	// 取消后等待正在执行的检查结束，避免数据库关闭后仍读写通知标记。
	m.checkMu.Lock()
	m.checkMu.Unlock()
}

func (m *appUpdateManager) status() AppUpdateStatus {
	m.stateMu.RLock()
	defer m.stateMu.RUnlock()
	return m.state
}

func (m *appUpdateManager) update(fn func(*AppUpdateStatus)) AppUpdateStatus {
	m.stateMu.Lock()
	fn(&m.state)
	m.state.Revision++
	status := m.state
	m.stateMu.Unlock()
	if m.app != nil {
		m.app.emit(EventAppUpdateStatus, status)
	}
	return status
}

func (m *appUpdateManager) setError(message string) {
	m.update(func(status *AppUpdateStatus) {
		status.State = "error"
		status.Error = message
	})
}

func (m *appUpdateManager) runCheck(parent context.Context, manual bool) (AppUpdateStatus, error) {
	if m == nil {
		return defaultAppUpdateStatus(false), errors.New("软件更新检查暂不可用")
	}
	if m.check == nil {
		status := m.status()
		if status.Error != "" {
			return status, errors.New(status.Error)
		}
		return status, errors.New("软件更新检查暂不可用")
	}
	m.checkMu.Lock()
	defer m.checkMu.Unlock()

	if m.rootCtx.Err() != nil || parent.Err() != nil {
		return m.status(), context.Canceled
	}
	previous := m.status()
	m.update(func(status *AppUpdateStatus) {
		status.State = "checking"
		status.Error = ""
	})
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	release, err := m.check(ctx)
	if err != nil {
		if m.rootCtx.Err() != nil || parent.Err() != nil {
			m.update(func(status *AppUpdateStatus) { status.State = previous.State; status.Error = previous.Error })
			return m.status(), ctx.Err()
		}
		message := appUpdateErrorMessage(err)
		m.update(func(status *AppUpdateStatus) {
			status.State = "error"
			status.Error = message
			status.CheckedAt = time.Now().UTC().Format(time.RFC3339)
			// 检查失败保留已知版本；缺少当前平台安装包仍展示该版本和下载入口。
			if match := appUpdateNoAssetVersionPattern.FindStringSubmatch(err.Error()); len(match) == 2 {
				status.UpdateAvailable = false
				status.LatestVersion = strings.TrimPrefix(match[1], "v")
				status.ReleaseName = ""
				status.Notes = ""
				status.PublishedAt = ""
				status.Skipped = false
				status.ReleaseURL = appUpdateLatestReleaseURL
			}
		})
		m.logger.Warn("检查软件更新失败", "manual", manual, "error", err)
		return m.status(), errors.New(message)
	}

	if release == nil {
		status := m.update(func(status *AppUpdateStatus) {
			status.State = "up_to_date"
			status.UpdateAvailable = false
			status.LatestVersion = ""
			status.ReleaseName = ""
			status.Notes = ""
			status.PublishedAt = ""
			status.ReleaseURL = ""
			status.Skipped = false
			status.Error = ""
			status.CheckedAt = time.Now().UTC().Format(time.RFC3339)
		})
		return status, nil
	}

	skippedVersion, err := m.readSetting(appUpdateSkippedVersionKey)
	if err != nil {
		m.setError(err.Error())
		return m.status(), err
	}
	notifiedVersion, err := m.readSetting(appUpdateNotifiedVersionKey)
	if err != nil {
		m.setError(err.Error())
		return m.status(), err
	}
	status := m.update(func(status *AppUpdateStatus) {
		status.State = "available"
		status.UpdateAvailable = true
		status.LatestVersion = strings.TrimPrefix(release.Version, "v")
		status.ReleaseName = release.Name
		status.Notes = appUpdateNotes(release.Notes)
		status.PublishedAt = formatAppUpdateTime(release.PublishedAt)
		status.ReleaseURL = appUpdateReleaseURL(release)
		status.Skipped = skippedVersion == strings.TrimPrefix(release.Version, "v")
		status.Error = ""
		status.CheckedAt = time.Now().UTC().Format(time.RFC3339)
	})
	if !status.Skipped && status.LatestVersion != "" && notifiedVersion != status.LatestVersion {
		if m.notify != nil {
			m.notify("info", "发现新版本 "+status.LatestVersion, "可在「设置 → 软件更新」查看更新内容")
		}
		if err := m.writeSetting(appUpdateNotifiedVersionKey, status.LatestVersion); err != nil {
			return status, err
		}
	}
	return status, nil
}

func (m *appUpdateManager) readSetting(key string) (string, error) {
	if m == nil || m.app == nil || m.app.settingsStore == nil {
		return "", errors.New("软件更新设置存储不可用")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	record, err := m.app.settingsStore.Get(ctx, appUpdateStateCategory, key)
	if err != nil {
		m.logger.Warn("软件更新状态读取失败", "key", key, "error", err)
		return "", errors.New("读取软件更新设置失败")
	}
	if record == nil {
		return "", nil
	}
	return record.Value, nil
}

func (m *appUpdateManager) writeSetting(key, value string) error {
	if m == nil || m.app == nil || m.app.settingsStore == nil {
		return errors.New("软件更新设置存储不可用")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.app.settingsStore.Set(ctx, appUpdateStateCategory, key, value); err != nil {
		m.logger.Warn("软件更新状态写入失败", "key", key, "error", err)
		return errors.New("保存软件更新设置失败")
	}
	return nil
}

func (m *appUpdateManager) skipVersion(version string) error {
	m.checkMu.Lock()
	defer m.checkMu.Unlock()
	if m.rootCtx.Err() != nil {
		return context.Canceled
	}
	status := m.status()
	if version != "" && version != status.LatestVersion {
		return fmt.Errorf("只能跳过当前可用版本 %s", status.LatestVersion)
	}
	if err := m.writeSetting(appUpdateSkippedVersionKey, version); err != nil {
		return err
	}
	m.update(func(status *AppUpdateStatus) {
		status.Skipped = version != "" && version == status.LatestVersion
	})
	return nil
}

func formatAppUpdateTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func appUpdateNotes(notes string) string {
	if index := strings.Index(notes, appUpdateNotesMarker); index >= 0 {
		notes = notes[:index]
	}
	notes = strings.TrimSpace(notes)
	runes := []rune(notes)
	if len(runes) > 8000 {
		notes = string(runes[:8000])
	}
	return notes
}

func appUpdateReleaseURL(release *updater.Release) string {
	if release != nil && release.Metadata != nil {
		if value, ok := release.Metadata["github.release.htmlURL"].(string); ok && strings.HasPrefix(value, appUpdateReleaseURLPrefix) {
			return value
		}
	}
	return appUpdateLatestReleaseURL
}

func appUpdateErrorMessage(err error) string {
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "api 403") || strings.Contains(text, "api 429") || strings.Contains(text, "http 403") || strings.Contains(text, "http 429") {
		return "GitHub 访问频率受限，请稍后再试"
	}
	if match := appUpdateNoAssetVersionPattern.FindStringSubmatch(err.Error()); len(match) == 2 {
		return "新版本 " + strings.TrimPrefix(match[1], "v") + " 暂未提供当前平台的安装包"
	}
	if strings.Contains(text, "no asset for") {
		return "新版本暂未提供当前平台的安装包"
	}
	return "无法连接 GitHub，请检查网络或代理设置"
}

// GetAppUpdateStatus 获取软件更新状态。
func (a *App) GetAppUpdateStatus() AppUpdateStatus {
	manager := a.appUpdateRuntime()
	if manager == nil {
		return defaultAppUpdateStatus(false)
	}
	return manager.status()
}

// CheckAppUpdate 手动检查更新，返回完整状态。
func (a *App) CheckAppUpdate() (AppUpdateStatus, error) {
	manager := a.appUpdateRuntime()
	if manager == nil {
		return defaultAppUpdateStatus(false), errors.New("软件更新检查暂不可用")
	}
	return manager.runCheck(manager.rootCtx, true)
}

// SkipAppUpdateVersion 跳过当前版本；传空字符串取消跳过。
func (a *App) SkipAppUpdateVersion(version string) error {
	manager := a.appUpdateRuntime()
	if manager == nil {
		return errors.New("软件更新检查暂不可用")
	}
	return manager.skipVersion(version)
}

func (a *App) appUpdateRuntime() *appUpdateManager {
	if a == nil {
		return nil
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.appUpdate
}

// GitHub provider 会把失败响应正文拼入 error；日志只保留 HTTP 状态，不带正文。
type appUpdateTransport struct{ base http.RoundTripper }

func (t appUpdateTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err == nil && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(strings.NewReader(""))
	}
	return resp, err
}
