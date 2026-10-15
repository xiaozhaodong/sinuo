package appdir

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "appdir-test-home-")
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

func isolatedAppDirs(t *testing.T) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "APPDATA", "XDG_DATA_HOME"} {
		t.Setenv(key, dir)
	}
	return GetAppDataDir(), currentPlatformDir(LegacyDesktopName, legacyUnixName, legacyFallbackName), GetLegacyAppDataDir()
}

func writeFixture(t *testing.T, dir, marker string) {
	t.Helper()
	for _, name := range []string{"data/usage.db", "config/config.yaml", "logs/app.log", ".marker"} {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(marker+":"+name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func assertFixture(t *testing.T, dir, marker string) {
	t.Helper()
	for _, name := range []string{"data/usage.db", "config/config.yaml", "logs/app.log", ".marker"} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(got) != marker+":"+name {
			t.Fatalf("文件内容变化 %s: %q, err=%v", name, got, err)
		}
	}
}

func assertAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("目录或文件不应存在 %s: %v", path, err)
	}
}

func TestMigrateLegacyAppDataDir(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		previous, older, current bool
		wantMigrated             bool
	}{
		{name: "仅 AI-Switchboard", previous: true, wantMigrated: true},
		{name: "仅 CC-Forwarder", older: true, wantMigrated: true},
		{name: "两旧目录优先 AI-Switchboard", previous: true, older: true, wantMigrated: true},
		{name: "新目录存在不迁移", previous: true, older: true, current: true},
		{name: "全新安装不创建目录"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current, previous, older := isolatedAppDirs(t)
			if tc.previous {
				writeFixture(t, previous, "previous")
			}
			if tc.older {
				writeFixture(t, older, "older")
			}
			if tc.current {
				writeFixture(t, current, "current")
				// 目标已存在时，连旧目录侧车也不应检查。
				if err := os.WriteFile(filepath.Join(previous, "data", "usage.db-wal"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			migrated, err := MigrateLegacyAppDataDir()
			if err != nil || migrated != tc.wantMigrated {
				t.Fatalf("migrated=%v, err=%v", migrated, err)
			}
			switch {
			case tc.current:
				assertFixture(t, current, "current")
				assertFixture(t, previous, "previous")
				assertFixture(t, older, "older")
			case tc.previous:
				assertFixture(t, current, "previous")
				assertAbsent(t, previous)
				if tc.older {
					assertFixture(t, older, "older")
				}
			case tc.older:
				assertFixture(t, current, "older")
				assertAbsent(t, older)
			default:
				assertAbsent(t, current)
			}
		})
	}
}

func TestMigrateLegacyAppDataDir_ExactSidecarGuard(t *testing.T) {
	for _, useOlder := range []bool{false, true} {
		for _, sidecar := range []string{"usage.db-wal", "usage.db-shm"} {
			t.Run(fmt.Sprintf("older=%v/%s", useOlder, sidecar), func(t *testing.T) {
				current, previous, older := isolatedAppDirs(t)
				source := previous
				if useOlder {
					source = older
				} else {
					// 首选被阻断时，不得转而迁移更旧的数据。
					writeFixture(t, older, "older")
				}
				writeFixture(t, source, "source")
				path := filepath.Join(source, "data", sidecar)
				if err := os.WriteFile(path, []byte("active"), 0o600); err != nil {
					t.Fatal(err)
				}
				if migrated, err := MigrateLegacyAppDataDir(); err == nil || migrated || !strings.Contains(err.Error(), sidecar) {
					t.Fatalf("应阻断: migrated=%v, err=%v", migrated, err)
				}
				assertAbsent(t, current)
				assertFixture(t, source, "source")
				if got, err := os.ReadFile(path); err != nil || string(got) != "active" {
					t.Fatalf("不得改动侧车: %q, %v", got, err)
				}
				if !useOlder {
					assertFixture(t, older, "older")
				}
			})
		}
	}
}

func TestMigrateLegacyAppDataDir_BackupSidecarsDoNotBlock(t *testing.T) {
	current, previous, _ := isolatedAppDirs(t)
	writeFixture(t, previous, "previous")
	for _, name := range []string{"usage.db.bak-xxx-wal", "usage.db.bak-xxx-shm"} {
		if err := os.WriteFile(filepath.Join(previous, "data", name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if migrated, err := MigrateLegacyAppDataDir(); err != nil || !migrated {
		t.Fatalf("备份残留不应阻断: migrated=%v, err=%v", migrated, err)
	}
	assertAbsent(t, previous)
	assertFixture(t, current, "previous")
	for _, name := range []string{"usage.db.bak-xxx-wal", "usage.db.bak-xxx-shm"} {
		if got, err := os.ReadFile(filepath.Join(current, "data", name)); err != nil || string(got) != name {
			t.Fatalf("备份残留未完整迁移: %q, %v", got, err)
		}
	}
}

func TestMigrateLegacyAppDataDir_RenameFailure(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("只读父目录故障需要非 root 的 Unix 权限")
	}
	current, previous, older := isolatedAppDirs(t)
	writeFixture(t, previous, "previous")
	writeFixture(t, older, "older")
	parent := filepath.Dir(current)
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(parent, 0o755); err != nil {
			t.Error(err)
		}
	})
	if migrated, err := MigrateLegacyAppDataDir(); err == nil || migrated || !strings.Contains(err.Error(), "迁移数据目录") {
		t.Fatalf("rename 失败必须阻断: migrated=%v, err=%v", migrated, err)
	}
	assertAbsent(t, current)
	assertFixture(t, previous, "previous")
	assertFixture(t, older, "older")
}

func TestResolveAppDataDir(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	profile := filepath.Join(dir, "profile")
	appData := filepath.Join(dir, "roaming")
	xdg := filepath.Join(dir, "xdg")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", profile)
	for _, tc := range []struct {
		name, goos, appData, xdg, want string
	}{
		{"windows APPDATA", "windows", appData, "", filepath.Join(appData, DesktopName)},
		{"windows USERPROFILE", "windows", "", "", filepath.Join(profile, "AppData", "Roaming", DesktopName)},
		{"darwin", "darwin", appData, xdg, filepath.Join(home, "Library", "Application Support", DesktopName)},
		{"linux XDG", "linux", "", xdg, filepath.Join(xdg, UnixName)},
		{"linux HOME", "linux", "", "", filepath.Join(home, ".local", "share", UnixName)},
		{"其他平台", "freebsd", "", "", filepath.Join(home, FallbackName)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("APPDATA", tc.appData)
			t.Setenv("XDG_DATA_HOME", tc.xdg)
			if got := resolveAppDataDir(tc.goos, home, DesktopName, UnixName, FallbackName); got != tc.want {
				t.Fatalf("got=%q, want=%q", got, tc.want)
			}
			assertAbsent(t, tc.want)
		})
	}
}
