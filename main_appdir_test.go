package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"cc-forwarder/config"
	"cc-forwarder/internal/appdir"
	"cc-forwarder/internal/tracking"
	"cc-forwarder/internal/utils"
)

// 桌面包的全部测试均隔离用户目录，防止默认配置路径污染正在运行的 App。
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "desktop-test-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, key := range []string{"HOME", "USERPROFILE", "APPDATA", "XDG_DATA_HOME"} {
		if err := os.Setenv(key, dir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	code := m.Run()
	if err := os.RemoveAll(dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	os.Exit(code)
}

func TestRunWithAppDataMigration_BlocksBeforeBusinessStartup(t *testing.T) {
	for _, failure := range []string{"usage.db-wal", "usage.db-shm", "rename"} {
		t.Run(failure, func(t *testing.T) {
			if failure == "rename" && (runtime.GOOS == "windows" || os.Geteuid() == 0) {
				t.Skip("只读父目录故障需要非 root 的 Unix 权限")
			}
			dir := t.TempDir()
			for _, key := range []string{"HOME", "USERPROFILE", "APPDATA", "XDG_DATA_HOME"} {
				t.Setenv(key, dir)
			}
			oldDir, newDir := utils.GetLegacyAppDataDir(), appdir.GetAppDataDir()
			if err := os.MkdirAll(filepath.Join(oldDir, "data"), 0o755); err != nil {
				t.Fatal(err)
			}
			if failure == "rename" {
				parent := filepath.Dir(newDir)
				if err := os.Chmod(parent, 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := os.Chmod(parent, 0o755); err != nil {
						t.Error(err)
					}
				})
			} else if err := os.WriteFile(filepath.Join(oldDir, "data", failure), []byte("active"), 0o600); err != nil {
				t.Fatal(err)
			}
			started, shown := false, false
			err := runWithAppDataMigration(func() error {
				started = true
				return nil
			}, func(err error) {
				shown = true
				if err == nil {
					t.Error("错误框必须收到迁移错误")
				}
				if _, err := os.Lstat(newDir); !os.IsNotExist(err) {
					t.Errorf("弹框前不得创建目标目录: %v", err)
				}
			})
			if err == nil || started || !shown {
				t.Fatalf("err=%v started=%v shown=%v", err, started, shown)
			}
			if _, err := os.Lstat(newDir); !os.IsNotExist(err) {
				t.Fatalf("阻断后不得创建目标目录: %v", err)
			}
			if _, err := os.Stat(oldDir); err != nil {
				t.Fatalf("旧目录必须保留: %v", err)
			}
		})
	}
}

func TestRunWithAppDataMigration_StartsOnlyAfterMigration(t *testing.T) {
	dir := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "APPDATA", "XDG_DATA_HOME"} {
		t.Setenv(key, dir)
	}
	oldDir := utils.GetLegacyAppDataDir()
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "marker"), []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("业务启动错误应原样返回")
	err := runWithAppDataMigration(func() error {
		if got, err := os.ReadFile(filepath.Join(appdir.GetAppDataDir(), "marker")); err != nil || string(got) != "kept" {
			t.Fatalf("业务启动前必须迁移完成: %q, %v", got, err)
		}
		return wantErr
	}, func(err error) {
		t.Fatalf("迁移成功不应弹框: %v", err)
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("got=%v, want=%v", err, wantErr)
	}
}

func TestAppDataPathsShareResolver(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("endpoints_storage:\n  type: sqlite\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(appdir.GetAppDataDir(), "data", "usage.db")
	if cfg.UsageTracking.DatabasePath != want || utils.GetDataDir() != filepath.Dir(want) {
		t.Fatalf("路径未统一: config=%q, utils=%q, want=%q", cfg.UsageTracking.DatabasePath, utils.GetDataDir(), want)
	}
}

func TestShutdown_ClosesAllUsageDatabasePools(t *testing.T) {
	for _, hotPool := range []bool{false, true} {
		t.Run(fmt.Sprintf("hotPool=%v", hotPool), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "usage.db")
			core, err := tracking.OpenCoreDatabase(tracking.DatabaseConfig{Type: "sqlite", DatabasePath: path})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = core.Close() })
			tracker, err := tracking.NewUsageTracker(&tracking.Config{
				Enabled: true, DatabasePath: path, HotPool: &tracking.HotPoolSettings{Enabled: hotPool},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = tracker.Close() })
			coreDB, usageDB := core.DB(), tracker.GetDB()
			if coreDB == usageDB || usageDB != tracker.GetReadDB() || usageDB != tracker.GetWriteDB() {
				t.Fatal("应有两个独立连接池；tracker 的读、写引用共用一个池")
			}
			if _, err := coreDB.Exec("CREATE TABLE shutdown_marker(v TEXT); INSERT INTO shutdown_marker VALUES ('kept')"); err != nil {
				t.Fatal(err)
			}
			for _, suffix := range []string{"-wal", "-shm"} {
				if _, err := os.Stat(path + suffix); err != nil {
					t.Fatalf("关闭前侧车必须存在 %s: %v", suffix, err)
				}
			}
			app := NewApp()
			app.coreDatabase, app.usageTracker = core, tracker
			app.shutdown(context.Background())
			if coreDB.Ping() == nil || usageDB.Ping() == nil || coreDB.Stats().OpenConnections != 0 || usageDB.Stats().OpenConnections != 0 {
				t.Fatal("shutdown 必须关闭两个连接池的全部连接")
			}
			for _, suffix := range []string{"-wal", "-shm"} {
				if _, err := os.Stat(path + suffix); !os.IsNotExist(err) {
					t.Fatalf("shutdown 后侧车必须消失 %s: %v", suffix, err)
				}
			}
		})
	}
}
