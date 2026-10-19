package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAppUpdate_CleanupExactNamesAndAge(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	for _, tc := range []struct {
		name                   string
		directory, old, remove bool
	}{
		{"wails-update-123.log", false, true, true}, {"wails-update-123-456.ready", false, true, true}, {"wails-update-123", true, true, true},
		{"wails-update-124.log", false, false, false}, {"wails-update-124-456.ready", false, false, false}, {"wails-update-124", true, false, false},
		{"wails-update-abc", true, true, false}, {"wails-update-123.log.bak", false, true, false}, {"wails-update-123-456.ready.extra", false, true, false},
		{"wails-update-125", false, true, false}, {"wails-update-125.log", true, true, false}, {"unrelated", false, true, false},
	} {
		path := filepath.Join(dir, tc.name)
		if tc.directory {
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := os.WriteFile(path, []byte("test"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		age := time.Hour
		if tc.old {
			age = 25 * time.Hour
		}
		if err := os.Chtimes(path, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, err := os.Lstat(path)
			if tc.remove != os.IsNotExist(err) {
				t.Errorf("%s removed=%v want=%v", tc.name, os.IsNotExist(err), tc.remove)
			}
		})
	}
	// 即使名字精确匹配，也不跟随指向其他位置的符号链接。
	target := filepath.Join(t.TempDir(), "keep")
	if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "wails-update-999.log")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := cleanupAppUpdateTemp(context.Background(), dir, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatal("误删符号链接", err)
	}
}
