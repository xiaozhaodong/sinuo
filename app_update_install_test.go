package main

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sinuo/internal/store"

	"github.com/wailsapp/wails/v3/pkg/updater"
)

func TestAppUpdate_InstallEligibility(t *testing.T) {
	base := appUpdateEnvironment{"darwin", "release-commit", appUpdatePublicKey, func() (string, error) { return "/Applications/Sinuo.app/Contents/MacOS/Sinuo", nil }, func(string) bool { return true }}
	for _, tc := range []struct {
		name   string
		change func(*appUpdateEnvironment)
		want   string
	}{
		{"valid", func(*appUpdateEnvironment) {}, ""},
		{"platform first", func(e *appUpdateEnvironment) { e.platform = "windows"; e.publicKey = "bad" }, "当前平台"},
		{"key before local", func(e *appUpdateEnvironment) { e.publicKey = "bad"; e.commit = "unknown" }, "一键更新暂不可用"},
		{"key length", func(e *appUpdateEnvironment) { e.publicKey = "AA==" }, "一键更新暂不可用"},
		{"local", func(e *appUpdateEnvironment) { e.commit = "unknown" }, "本地构建"},
		{"outside app", func(e *appUpdateEnvironment) { e.executable = func() (string, error) { return "/tmp/Sinuo", nil } }, "应用程序"},
		{"path error", func(e *appUpdateEnvironment) {
			e.executable = func() (string, error) { return "", errors.New("not found") }
		}, "应用程序"},
		{"translocation", func(e *appUpdateEnvironment) {
			e.executable = func() (string, error) { return "/private/AppTranslocation/id/Sinuo.app/Contents/MacOS/Sinuo", nil }
		}, "应用程序"},
		{"not writable", func(e *appUpdateEnvironment) {
			e.writable = func(path string) bool {
				if path != "/Applications" {
					t.Fatalf("dir=%s", path)
				}
				return false
			}
		}, "没有写入「/Applications」的权限"},
		{"nested bundle uses Wails target", func(e *appUpdateEnvironment) {
			e.executable = func() (string, error) {
				return "/Applications/Sinuo.app/Contents/Helpers/Nested.app/Contents/MacOS/Helper", nil
			}
			e.writable = func(dir string) bool {
				if dir != "/Applications" {
					t.Fatalf("Wails 替换目标目录=%s", dir)
				}
				return true
			}
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := base
			tc.change(&e)
			got := appUpdateInstallBlockedReason(e)
			if (tc.want == "" && got != "") || (tc.want != "" && !strings.Contains(got, tc.want)) {
				t.Fatalf("reason=%q", got)
			}
		})
	}
}

func prepareAppUpdateDownload(t *testing.T) (*App, *appUpdateManager) {
	t.Helper()
	app, m := updateTestManager(t)
	m.baseBlockedReason = ""
	m.check = func(context.Context) (*updater.Release, error) {
		return &updater.Release{Version: "99.0.0", Metadata: map[string]any{"sinuo.signed": true}, Verification: &updater.Verification{DigestAlgo: "sha256", Digest: make([]byte, 32), SignatureAlgo: "ed25519", Signature: make([]byte, 64)}}, nil
	}
	if _, err := app.CheckAppUpdate(); err != nil {
		t.Fatal(err)
	}
	return app, m
}

func TestAppUpdate_DownloadGuardCancelAndProgress(t *testing.T) {
	app, m := prepareAppUpdateDownload(t)
	var checks atomic.Int32
	check := m.check
	m.check = func(ctx context.Context) (*updater.Release, error) { checks.Add(1); return check(ctx) }
	entered := make(chan struct{})
	m.download = func(ctx context.Context) error {
		close(entered)
		m.downloadProgress(10, 100)
		for i := 0; i < 20; i++ {
			m.downloadProgress(int64(i+20), 100)
		}
		<-ctx.Done()
		return ctx.Err()
	}
	if err := app.DownloadAppUpdate(); err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := app.DownloadAppUpdate(); err == nil {
		t.Fatal("重复启动下载")
	}
	before := m.status()
	m.downloadProgress(99, 100)
	after := m.status()
	if after.ProgressWritten != 10 || after.Revision != before.Revision {
		t.Fatalf("没有节流: %+v -> %+v", before, after)
	}
	if _, err := m.runCheck(m.rootCtx, false); err != nil {
		t.Fatal(err)
	}
	if _, err := app.CheckAppUpdate(); err == nil {
		t.Fatal("下载中允许检查")
	}
	if checks.Load() != 0 {
		t.Fatal("下载期间触发 provider.Check")
	}
	if err := app.CancelAppUpdateDownload(); err != nil {
		t.Fatal(err)
	}
	if status := m.status(); status.State != "available" || !status.UpdateAvailable || status.Error != "" {
		t.Fatalf("取消状态=%+v", status)
	}
	if err := app.CancelAppUpdateDownload(); err == nil {
		t.Fatal("重复取消未报错")
	}
	m.release.Metadata["sinuo.signed"] = false
	if err := app.DownloadAppUpdate(); err == nil {
		t.Fatal("入口未拒绝未签名版本")
	}
	m.checkMu.Lock()
	m.update(func(s *AppUpdateStatus) { m.applyInstallSupport(s) })
	m.checkMu.Unlock()
	if s := m.status(); s.InstallSupported || s.InstallBlockedReason != "此版本未提供签名，请前往下载" {
		t.Fatalf("unsigned=%+v", s)
	}
}

func TestAppUpdate_DownloadFailureStagesAndRetry(t *testing.T) {
	for _, stage := range []string{"download", "verify", "install"} {
		t.Run(stage, func(t *testing.T) {
			app, m := prepareAppUpdateDownload(t)
			m.download = func(context.Context) error {
				if stage != "download" {
					m.downloadComplete()
				}
				if stage == "verify" {
					return errors.New("updater: digest mismatch")
				}
				return errors.New("operation failed")
			}
			if err := app.DownloadAppUpdate(); err != nil {
				t.Fatal(err)
			}
			waitAppUpdateState(t, m, func(s AppUpdateStatus) bool { return s.State == "error" })
			if s := m.status(); s.ErrorStage != stage || !s.UpdateAvailable || s.LatestVersion != "99.0.0" {
				t.Fatalf("failure=%+v", s)
			}
			m.download = func(context.Context) error { m.downloadComplete(); return nil }
			if err := app.DownloadAppUpdate(); err != nil {
				t.Fatal(err)
			}
			waitAppUpdateState(t, m, func(s AppUpdateStatus) bool { return s.State == "ready" })
			if s := m.status(); s.ReadyVersion != "99.0.0" || s.Error != "" || s.ErrorStage != "" {
				t.Fatalf("retry=%+v", s)
			}
		})
	}
}

func TestAppUpdate_StopCancelsDownload(t *testing.T) {
	app, m := prepareAppUpdateDownload(t)
	entered := make(chan struct{})
	m.download = func(ctx context.Context) error { close(entered); <-ctx.Done(); return ctx.Err() }
	if err := app.DownloadAppUpdate(); err != nil {
		t.Fatal(err)
	}
	<-entered
	done := make(chan struct{})
	go func() { m.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("关停没有取消下载")
	}
	if err := app.DownloadAppUpdate(); !errors.Is(err, context.Canceled) {
		t.Fatalf("关停后允许下载: %v", err)
	}
}

type appUpdateFailReadStore struct{ store.SettingsStore }

func (s appUpdateFailReadStore) Get(context.Context, string, string) (*store.SettingRecord, error) {
	return nil, errors.New("settings read failed")
}

func TestAppUpdate_DownloadRejectsDifferentPendingVersion(t *testing.T) {
	app, manager := prepareAppUpdateDownload(t)
	previousCheck := manager.check
	manager.check = func(ctx context.Context) (*updater.Release, error) {
		release, err := previousCheck(ctx)
		release.Version = "100.0.0"
		return release, err
	}
	app.settingsStore = appUpdateFailReadStore{app.settingsStore}
	if status, err := app.CheckAppUpdate(); err == nil || status.LatestVersion != "99.0.0" || !status.UpdateAvailable {
		t.Fatalf("设置读取失败未保留已知信息: %+v %v", status, err)
	}
	var downloads atomic.Int32
	manager.download = func(context.Context) error { downloads.Add(1); return nil }
	if err := app.DownloadAppUpdate(); err == nil || !strings.Contains(err.Error(), "更新信息已变化") || downloads.Load() != 0 {
		t.Fatalf("下载目标与展示版本不一致时仍下载: %v", err)
	}
}

func TestAppUpdate_SuccessfulCheckClearsOldReadyVersion(t *testing.T) {
	for _, current := range []bool{false, true} {
		app, manager := prepareAppUpdateDownload(t)
		manager.update(func(s *AppUpdateStatus) {
			s.State, s.ReadyVersion, s.ProgressWritten, s.ProgressTotal = "error", "98.0.0", 10, 10
		})
		if current {
			manager.check = func(context.Context) (*updater.Release, error) { return nil, nil }
		}
		status, err := app.CheckAppUpdate()
		if err != nil || status.ReadyVersion != "" || status.ProgressWritten != 0 || status.ProgressTotal != 0 {
			t.Fatalf("新检查结果残留旧安装阶段信息: %+v %v", status, err)
		}
	}
}
