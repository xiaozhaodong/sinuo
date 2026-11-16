//go:build darwin

package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrivacyDirectoryWatcherPartialRegistration(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing")
	for _, paths := range [][]string{{missing}, {dir, missing}, {missing, dir}} {
		w, skipped, err := newPrivacyDirectoryWatcher(paths)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(w.close)
		if len(skipped) != 1 || !errors.Is(skipped[0], os.ErrNotExist) {
			t.Fatalf("应只报告缺失目录：%v", skipped)
		}
		if len(paths) == 1 {
			if w != nil {
				t.Fatal("没有可监听目录时应释放 kqueue")
			}
			continue
		}
		f, err := os.CreateTemp(dir, "change")
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		select {
		case path := <-w.events():
			if path != dir {
				t.Fatalf("目录事件来自 %q，预期 %q", path, dir)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("可用目录没有发出原生监听事件")
		}
		w.close()
	}
}

func TestPrivacyConfigSourcesWatchRestrictedAncestor(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("目录权限场景需要非 root 用户")
	}
	for _, phase := range []string{"startup", "rebuild"} {
		t.Run(phase, func(t *testing.T) {
			s := newConfigSourceService(t)
			ctx := context.Background()
			healthy := filepath.Join(t.TempDir(), ".env")
			restricted := filepath.Join(t.TempDir(), "traverse-only")
			nested := filepath.Join(restricted, "config", ".env")
			if err := os.MkdirAll(filepath.Dir(nested), 0700); err != nil {
				t.Fatal(err)
			}
			writeSourceFile(t, healthy, "PASSWORD=healthy-initial-test-value")
			writeSourceFile(t, nested, "PASSWORD=nested-initial-test-value")
			addSource(t, s, healthy, "file")
			if err := os.Chmod(restricted, 0100); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.Chmod(restricted, 0700); err != nil {
					t.Error(err)
				}
			})
			if phase == "startup" {
				addSource(t, s, nested, "file")
			}
			if err := s.StartConfigSourceSync(ctx); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(s.CloseConfigSourceSync)
			if phase == "rebuild" {
				addSource(t, s, nested, "file")
			}
			// 首次扫描完成后，分别触发两个来源，验证各自的文件事件都能唤醒同步。
			awaitSource(t, func() bool {
				state, err := s.ListConfigSources(ctx)
				ok, _ := s.ExactSecretExists(ctx, "nested-initial-test-value")
				return err == nil && state.Watching && strings.Contains(state.WatchError, restricted) && ok
			})
			for _, file := range []struct{ path, value string }{
				{healthy, "healthy-updated-test-value"},
				{nested, "nested-updated-test-value"},
			} {
				writeSourceFile(t, file.path, "PASSWORD="+file.value)
				awaitSource(t, func() bool {
					ok, _ := s.ExactSecretExists(ctx, file.value)
					return ok
				})
			}
			state, err := s.ListConfigSources(ctx)
			if err != nil || !strings.Contains(state.WatchError, restricted) {
				t.Fatalf("权限未恢复时应保留受限目录告警：%+v, %v", state, err)
			}
			if err := os.Chmod(restricted, 0700); err != nil {
				t.Fatal(err)
			}
			writeSourceFile(t, healthy, "PASSWORD=healthy-recovered-test-value")
			awaitSource(t, func() bool {
				state, err := s.ListConfigSources(ctx)
				ok, _ := s.ExactSecretExists(ctx, "healthy-recovered-test-value")
				return err == nil && state.Watching && !strings.Contains(state.WatchError, restricted) && ok
			})
		})
	}
}
