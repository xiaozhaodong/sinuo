package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sinuo/internal/service"

	"github.com/wailsapp/wails/v3/pkg/updater"
	"github.com/wailsapp/wails/v3/pkg/updater/providers/github"
)

// 仅驱动 updater.Check；创建窗口或退出即表示误进入 P2 流程。
type appUpdateTestHost struct{ t *testing.T }

func (h appUpdateTestHost) Emit(string, ...any) bool         { return true }
func (h appUpdateTestHost) OnEvent(string, func(any)) func() { return func() {} }
func (h appUpdateTestHost) OpenWindow(updater.WindowOptions) updater.WindowHandle {
	h.t.Error("P1 不应打开 updater 窗口")
	return nil
}
func (h appUpdateTestHost) Quit() { h.t.Error("P1 不应退出 App") }

func updateTestManager(t *testing.T) (*App, *appUpdateManager) {
	t.Helper()
	app := newImageGenerationSettingsTestApp(t)
	app.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := newAppUpdateManager(app, context.Background(), true)
	app.appUpdate = manager
	t.Cleanup(manager.Stop)
	return app, manager
}

func TestAppUpdate_GitHubCheckResults(t *testing.T) {
	for _, tc := range []struct {
		name, version, state, message string
		code                          int
		asset                         bool
	}{
		{name: "available", version: "v99.0.0", code: 200, asset: true, state: "available"},
		{name: "current", version: "v" + Version, code: 200, state: "up_to_date"},
		{name: "not found", code: 404, state: "up_to_date"},
		{name: "forbidden", code: 403, state: "error", message: "GitHub 访问频率受限，请稍后再试"},
		{name: "rate limited", code: 429, state: "error", message: "GitHub 访问频率受限，请稍后再试"},
		{name: "missing asset", version: "v99.0.0", code: 200, state: "error", message: "新版本 99.0.0 暂未提供当前平台的安装包"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, manager := updateTestManager(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/repos/xiaozhaodong/sinuo/releases/latest" {
					t.Errorf("unexpected request: %s", r.URL)
					w.WriteHeader(500)
					return
				}
				w.WriteHeader(tc.code)
				if tc.code != 200 {
					_, _ = io.WriteString(w, "private-response-body")
					return
				}
				assets := []map[string]any{}
				if tc.asset {
					assets = append(assets, map[string]any{"name": fmt.Sprintf("Sinuo-%s-%s.zip", runtime.GOOS, runtime.GOARCH), "browser_download_url": "https://example.invalid/never-download"})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": tc.version, "name": "Release test", "body": "## 更新\n**修复**\n" + appUpdateNotesMarker + "\n下载说明", "html_url": appUpdateReleaseURLPrefix + "tag/" + tc.version, "published_at": "2026-10-19T00:00:00Z", "assets": assets})
			}))
			defer server.Close()
			provider, err := github.New(github.Config{Repository: "xiaozhaodong/sinuo", BaseURL: server.URL, ChecksumAsset: "checksums.txt", HTTPClient: &http.Client{Transport: appUpdateTransport{http.DefaultTransport}, Timeout: time.Second}})
			if err != nil {
				t.Fatal(err)
			}
			checker := updater.New(appUpdateTestHost{t})
			if err := checker.Init(updater.Config{CurrentVersion: Version, Providers: []updater.Provider{provider}, Window: updater.WindowNone}); err != nil {
				t.Fatal(err)
			}
			manager.check = checker.Check
			status, err := app.CheckAppUpdate()
			if status.State != tc.state || status.Error != tc.message || (err != nil) != (tc.message != "") {
				t.Fatalf("status=%+v err=%v", status, err)
			}
			if status.UpdateAvailable != (tc.state == "available") {
				t.Fatalf("update_available=%v state=%s", status.UpdateAvailable, status.State)
			}
			if err != nil && strings.Contains(err.Error(), "private-response-body") {
				t.Fatal("响应正文泄漏到返回错误")
			}
			if _, err := time.Parse(time.RFC3339, status.CheckedAt); err != nil {
				t.Fatal(err)
			}
			if status.State == "available" && (status.LatestVersion != "99.0.0" || strings.Contains(status.Notes, "下载说明") || status.ReleaseName != "Release test") {
				t.Fatalf("release=%+v", status)
			}
			if tc.name == "missing asset" && (status.ReleaseURL != appUpdateLatestReleaseURL || status.LatestVersion != "99.0.0") {
				t.Fatalf("missing asset=%+v", status)
			}
		})
	}
}

func TestAppUpdate_KnownReleaseSurvivesCheckingAndErrors(t *testing.T) {
	for _, skipped := range []bool{false, true} {
		t.Run(fmt.Sprintf("skipped=%v", skipped), func(t *testing.T) {
			app, manager := updateTestManager(t)
			app.completeStartup()
			tray := &appTray{app: app}
			manager.check = func(context.Context) (*updater.Release, error) {
				return &updater.Release{Version: "99.0.0", Name: "Sinuo 99", Notes: "更新内容", PublishedAt: time.Now(),
					Metadata: map[string]any{"github.release.htmlURL": appUpdateReleaseURLPrefix + "tag/v99.0.0"}}, nil
			}
			if _, err := app.CheckAppUpdate(); err != nil {
				t.Fatal(err)
			}
			if skipped {
				if err := app.SkipAppUpdateVersion("99.0.0"); err != nil {
					t.Fatal(err)
				}
			}
			known := app.GetAppUpdateStatus()
			assertRetained := func(state string) {
				t.Helper()
				status := app.GetAppUpdateStatus()
				if status.State != state || !status.UpdateAvailable || status.Skipped != skipped {
					t.Fatalf("已知更新状态丢失: %+v", status)
				}
				status.State, status.Error, status.CheckedAt, status.Revision = known.State, known.Error, known.CheckedAt, known.Revision
				if status != known {
					t.Fatalf("已知版本信息变化: got=%+v want=%+v", status, known)
				}
				want := "99.0.0"
				if skipped {
					want = ""
				}
				if got := tray.snapshot().UpdateVersion; got != want {
					t.Fatalf("托盘版本=%q want=%q", got, want)
				}
			}
			assertRetained("available")
			manager.check = func(context.Context) (*updater.Release, error) {
				assertRetained("checking")
				return nil, errors.New("network is unreachable")
			}
			if status, err := app.CheckAppUpdate(); err == nil || status.Error == "" {
				t.Fatalf("检查错误未暴露: %+v %v", status, err)
			}
			assertRetained("error")
			manager.check = func(context.Context) (*updater.Release, error) {
				assertRetained("checking")
				return nil, nil
			}
			status, err := app.CheckAppUpdate()
			if err != nil || status.State != "up_to_date" || status.UpdateAvailable || status.Skipped || status.Error != "" ||
				status.LatestVersion != "" || status.ReleaseName != "" || status.Notes != "" || status.PublishedAt != "" || status.ReleaseURL != "" {
				t.Fatalf("成功检查为最新后未清除提示: %+v %v", status, err)
			}
			if got := tray.snapshot().UpdateVersion; got != "" {
				t.Fatalf("最新版本仍有托盘提示: %q", got)
			}
		})
	}
}

func TestAppUpdate_MissingAssetReplacesKnownRelease(t *testing.T) {
	app, manager := updateTestManager(t)
	manager.check = func(context.Context) (*updater.Release, error) {
		return &updater.Release{Version: "99.0.0", Name: "Sinuo 99", Notes: "旧版本说明", PublishedAt: time.Now()}, nil
	}
	if _, err := app.CheckAppUpdate(); err != nil {
		t.Fatal(err)
	}
	manager.check = func(context.Context) (*updater.Release, error) {
		return nil, errors.New("release v100.0.0 has no asset for current platform")
	}
	status, err := app.CheckAppUpdate()
	if err == nil || status.State != "error" || status.UpdateAvailable || status.LatestVersion != "100.0.0" ||
		status.ReleaseURL != appUpdateLatestReleaseURL || status.ReleaseName != "" || status.Notes != "" || status.PublishedAt != "" || status.Skipped {
		t.Fatalf("缺少安装包的既有展示规则发生变化: %+v %v", status, err)
	}
}

func TestAppUpdate_NetworkErrorsAndNotificationDedupe(t *testing.T) {
	app, manager := updateTestManager(t)
	manager.check = func(context.Context) (*updater.Release, error) { return nil, errors.New("network is unreachable") }
	if status, err := app.CheckAppUpdate(); err == nil || status.Error != "无法连接 GitHub，请检查网络或代理设置" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	var notices int
	manager.notify = func(level, title, body string) {
		notices++
		if level != "info" || title != "发现新版本 99.0.0" {
			t.Errorf("notification: %s %s", level, title)
		}
	}
	manager.check = func(context.Context) (*updater.Release, error) { return &updater.Release{Version: "99.0.0"}, nil }
	for i := 0; i < 2; i++ {
		if _, err := app.CheckAppUpdate(); err != nil {
			t.Fatal(err)
		}
	}
	if notices != 1 {
		t.Fatalf("通知次数=%d", notices)
	}
	if err := app.SkipAppUpdateVersion("98.0.0"); err == nil {
		t.Fatal("接受了非当前版本")
	}
	if err := app.SkipAppUpdateVersion("99.0.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.CheckAppUpdate(); err != nil {
		t.Fatal(err)
	}
	if !app.GetAppUpdateStatus().Skipped || notices != 1 {
		t.Fatal("跳过状态或通知去重失效")
	}
	if err := app.SkipAppUpdateVersion(""); err != nil {
		t.Fatal(err)
	}
	if app.GetAppUpdateStatus().Skipped {
		t.Fatal("取消跳过未生效")
	}
	// 新进程读取持久化 notified_version，仍不能重复通知。
	restarted := newAppUpdateManager(app, context.Background(), true)
	defer restarted.Stop()
	restarted.check, restarted.notify = manager.check, manager.notify
	if _, err := restarted.runCheck(restarted.rootCtx, true); err != nil {
		t.Fatal(err)
	}
	if notices != 1 {
		t.Fatal("重启后重复通知")
	}
	if err := manager.writeSetting(appUpdateNotifiedVersionKey, ""); err != nil {
		t.Fatal(err)
	}
	if err := app.SkipAppUpdateVersion("99.0.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.CheckAppUpdate(); err != nil {
		t.Fatal(err)
	}
	if notices != 1 {
		t.Fatal("已跳过版本弹出了通知")
	}
}

func TestAppUpdate_MetadataAndLocalBuild(t *testing.T) {
	for _, raw := range []string{"", "https://evil.example/releases/1", "https://github.com/xiaozhaodong/sinuo.evil/releases/1"} {
		if got := appUpdateReleaseURL(&updater.Release{Metadata: map[string]any{"github.release.htmlURL": raw}}); got != appUpdateLatestReleaseURL {
			t.Fatalf("URL=%s", got)
		}
	}
	want := appUpdateReleaseURLPrefix + "tag/v6.1.0"
	if got := appUpdateReleaseURL(&updater.Release{Metadata: map[string]any{"github.release.htmlURL": want}}); got != want {
		t.Fatalf("URL=%s", got)
	}
	if got := appUpdateNotes("  Notes \n" + appUpdateNotesMarker + "footer"); got != "Notes" {
		t.Fatalf("notes=%q", got)
	}
	if got := len([]rune(appUpdateNotes(strings.Repeat("中", 8001)))); got != 8000 {
		t.Fatalf("notes 长度=%d", got)
	}
	if got := defaultAppUpdateStatus(true); got.LocalBuild != (Commit == "unknown") || got.CurrentVersion != Version || got.Commit != Commit || got.BuildTime != BuildTime {
		t.Fatalf("build=%+v", got)
	}
}

func TestAppUpdate_SerialChecksAndStopCancels(t *testing.T) {
	app, manager := updateTestManager(t)
	var active, maximum atomic.Int32
	manager.check = func(ctx context.Context) (*updater.Release, error) {
		current := active.Add(1)
		defer active.Add(-1)
		if current > maximum.Load() {
			maximum.Store(current)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Millisecond):
			return nil, nil
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = app.CheckAppUpdate() }()
	}
	wg.Wait()
	if maximum.Load() != 1 {
		t.Fatalf("并行检查数=%d", maximum.Load())
	}
	entered := make(chan struct{})
	manager.check = func(ctx context.Context) (*updater.Release, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	done := make(chan struct{})
	go func() { defer close(done); _, _ = app.CheckAppUpdate() }()
	<-entered
	manager.Stop()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("关停未取消检查")
	}
}

func TestAppUpdate_AutoCheckScheduleAndSettings(t *testing.T) {
	app, manager := updateTestManager(t)
	manager.firstDelay, manager.interval = 10*time.Millisecond, 10*time.Millisecond
	checks := make(chan struct{}, 20)
	manager.check = func(context.Context) (*updater.Release, error) { checks <- struct{}{}; return nil, nil }
	app.settingsService.SetOnChangeCallback(manager.OnSettingsChanged)
	manager.startSchedule()
	for i := 0; i < 2; i++ {
		select {
		case <-checks:
		case <-time.After(time.Second):
			t.Fatal("启动或周期检查未触发")
		}
	}
	if err := app.UpdateSetting(UpdateSettingInput{Category: service.CategoryAppUpdate, Key: "auto_check", Value: "false"}); err != nil {
		t.Fatal(err)
	}
	manager.scheduleMu.Lock()
	done := manager.scheduleDone
	manager.scheduleMu.Unlock()
	<-done
	for len(checks) > 0 {
		<-checks
	}
	select {
	case <-checks:
		t.Fatal("关闭后仍定时检查")
	case <-time.After(25 * time.Millisecond):
	}
	if err := app.UpdateSetting(UpdateSettingInput{Category: service.CategoryAppUpdate, Key: "auto_check", Value: "true"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-checks:
	case <-time.After(time.Second):
		t.Fatal("重新打开未恢复定时")
	}
	manager.Stop()
	if manager.rootCtx.Err() == nil {
		t.Fatal("关停 context 未取消")
	}
	if app.GetAppUpdateStatus().Revision < 3 {
		t.Fatal("状态 revision 未递增")
	}
}

func TestAppUpdate_SettingsRegistrationAndInternalKeys(t *testing.T) {
	app, manager := updateTestManager(t)
	for _, key := range []string{appUpdateSkippedVersionKey, appUpdateNotifiedVersionKey} {
		if err := manager.writeSetting(key, "99.0.0"); err != nil {
			t.Fatal(err)
		}
	}
	if err := app.settingsService.InitDefaults(context.Background()); err != nil {
		t.Fatal(err)
	}
	settings, err := app.GetAllSettings()
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, row := range settings {
		if row.Category == appUpdateStateCategory {
			t.Fatalf("内部状态显示在设置页: %+v", row)
		}
		if row.Category == service.CategoryAppUpdate {
			found++
			if row.Key != "auto_check" || row.Value != "true" || row.RequiresRestart {
				t.Fatalf("设置=%+v", row)
			}
		}
	}
	if found != 1 {
		t.Fatalf("更新设置数量=%d", found)
	}
	if app.settingsService.GetCategoryInfo(appUpdateStateCategory) != nil {
		t.Fatal("内部状态分类不应登记")
	}
	if value, err := manager.readSetting(appUpdateSkippedVersionKey); err != nil || value != "99.0.0" {
		t.Fatalf("更新内部状态被退役清理: %q %v", value, err)
	}
}

func TestAppUpdate_ResetSettingsPreservesSkippedAndNotifiedVersions(t *testing.T) {
	app, manager := updateTestManager(t)
	var notices int
	manager.notify = func(string, string, string) { notices++ }
	manager.check = func(context.Context) (*updater.Release, error) { return &updater.Release{Version: "99.0.0"}, nil }
	if _, err := app.CheckAppUpdate(); err != nil {
		t.Fatal(err)
	}
	if err := app.SkipAppUpdateVersion("99.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := app.UpdateSetting(UpdateSettingInput{Category: service.CategoryAppUpdate, Key: "auto_check", Value: "false"}); err != nil {
		t.Fatal(err)
	}
	if err := app.ResetCategorySettings(service.CategoryAppUpdate); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if !app.settingsService.GetBool(ctx, service.CategoryAppUpdate, "auto_check", false) {
		t.Fatal("自动检查未恢复默认值")
	}
	for _, key := range []string{appUpdateSkippedVersionKey, appUpdateNotifiedVersionKey} {
		record, err := app.settingsStore.Get(ctx, appUpdateStateCategory, key)
		if err != nil || record == nil || record.Value != "99.0.0" {
			t.Fatalf("重置删除了内部状态 %s: %+v %v", key, record, err)
		}
		if record, err := app.settingsStore.Get(ctx, service.CategoryAppUpdate, key); err != nil || record != nil {
			t.Fatalf("内部状态仍写在用户设置分类 %s: %+v %v", key, record, err)
		}
	}
	restarted := newAppUpdateManager(app, context.Background(), true)
	defer restarted.Stop()
	restarted.check, restarted.notify = manager.check, manager.notify
	status, err := restarted.runCheck(restarted.rootCtx, true)
	if err != nil || !status.Skipped || notices != 1 {
		t.Fatalf("重置后持久化跳过或通知去重失效: %+v notices=%d err=%v", status, notices, err)
	}
}
