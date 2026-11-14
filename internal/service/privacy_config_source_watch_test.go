package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPrivacyConfigSourcesWatchDirectoryReplacement(t *testing.T) {
	for _, kind := range []string{"file", "directory"} {
		t.Run(kind, func(t *testing.T) {
			s := newConfigSourceService(t)
			ctx := context.Background()
			base := t.TempDir()
			dir := filepath.Join(base, "project")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, ".env")
			writeSourceFile(t, path, "PASSWORD=before-directory-replacement-secret")
			sourcePath := path
			if kind == "directory" {
				sourcePath = dir
			}
			r := addSource(t, s, sourcePath, kind)
			if err := s.StartConfigSourceSync(ctx); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(s.CloseConfigSourceSync)
			if err := os.Rename(dir, filepath.Join(base, "old-project")); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			writeSourceFile(t, path, "PASSWORD=replacement-directory-secret")
			awaitSource(t, func() bool {
				ok, _ := s.ExactSecretExists(ctx, "replacement-directory-secret")
				return ok
			})
			// 等替换事件的防抖结束，再单独修改新目录内的文件，不能靠旧事件补扫。
			time.Sleep(600 * time.Millisecond)
			writeSourceFile(t, path, "PASSWORD=after-directory-replacement-secret")
			awaitSource(t, func() bool {
				ok, _ := s.ExactSecretExists(ctx, "after-directory-replacement-secret")
				return ok && sourceState(t, s, r.ID).LastError == ""
			})
		})
	}
}

func TestPrivacyConfigSourcesWatchAncestorDirectoryReplacement(t *testing.T) {
	for _, kind := range []string{"file", "directory"} {
		for _, change := range []string{"replace", "recreate"} {
			t.Run(kind+"/"+change, func(t *testing.T) {
				s := newConfigSourceService(t)
				ctx := context.Background()
				base := t.TempDir()
				project := filepath.Join(base, "project")
				dir := filepath.Join(project, "nested", "config")
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(dir, ".env")
				writeSourceFile(t, path, "PASSWORD=before-ancestor-replacement-secret")
				sourcePath := path
				if kind == "directory" {
					sourcePath = dir
				}
				r := addSource(t, s, sourcePath, kind)
				if err := s.StartConfigSourceSync(ctx); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(s.CloseConfigSourceSync)
				// 替换非直接父目录；只监听来源目录和它的父目录都无法覆盖此事件。
				if err := os.Rename(project, filepath.Join(base, "old-project")); err != nil {
					t.Fatal(err)
				}
				if change == "recreate" {
					awaitSource(t, func() bool { return sourceState(t, s, r.ID).LastError != "" })
				}
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				writeSourceFile(t, path, "PASSWORD=replacement-ancestor-secret")
				awaitSource(t, func() bool {
					ok, _ := s.ExactSecretExists(ctx, "replacement-ancestor-secret")
					return ok && sourceState(t, s, r.ID).LastError == ""
				})
				// 新目录中的后续写入也必须自动同步，不能只靠祖先替换事件补扫一次。
				time.Sleep(600 * time.Millisecond)
				writeSourceFile(t, path, "PASSWORD=after-ancestor-replacement-secret")
				awaitSource(t, func() bool {
					ok, _ := s.ExactSecretExists(ctx, "after-ancestor-replacement-secret")
					return ok
				})
				if ok, err := s.ExactSecretExists(ctx, "before-ancestor-replacement-secret"); err != nil || !ok {
					t.Fatal("祖先替换不能移除已导入值", err)
				}
			})
		}
	}
}

func TestPrivacyConfigSourcesWatchSymlinkAncestors(t *testing.T) {
	for _, layout := range []string{"ancestor", "chain"} {
		for _, kind := range []string{"file", "directory"} {
			t.Run(layout+"/"+kind, func(t *testing.T) {
				s := newConfigSourceService(t)
				ctx := context.Background()
				base := t.TempDir()
				first, second := filepath.Join(base, "first"), filepath.Join(base, "second")
				for _, dir := range []string{first, second} {
					if err := os.MkdirAll(filepath.Join(dir, "nested"), 0700); err != nil {
						t.Fatal(err)
					}
				}
				original := filepath.Join(first, "nested", ".env")
				writeSourceFile(t, original, "PASSWORD=original-ancestor-link-secret")
				writeSourceFile(t, filepath.Join(second, "nested", ".env"), "PASSWORD=unauthorized-ancestor-link-secret")
				alias := filepath.Join(base, "alias")
				changingLink := alias
				if layout == "chain" {
					links := filepath.Join(base, "links")
					if err := os.Mkdir(links, 0700); err != nil {
						t.Fatal(err)
					}
					changingLink = filepath.Join(links, "current")
					// 链中间的链接不在登记路径的字面前缀中，并使用相对链接目标。
					if err := os.Symlink(filepath.Join("links", "current"), alias); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(first, changingLink); err != nil {
					t.Fatal(err)
				}
				sourcePath := filepath.Join(alias, "nested")
				if kind == "file" {
					sourcePath = filepath.Join(sourcePath, ".env")
				}
				r := addSource(t, s, sourcePath, kind)
				if err := s.StartConfigSourceSync(ctx); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(s.CloseConfigSourceSync)
				replaceLink := func(target string) {
					t.Helper()
					tmp := changingLink + ".replacement"
					if err := os.Symlink(target, tmp); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(tmp, changingLink); err != nil {
						t.Fatal(err)
					}
				}
				replaceLink(second)
				awaitSource(t, func() bool { return sourceState(t, s, r.ID).LastError != "" })
				if ok, _ := s.ExactSecretExists(ctx, "unauthorized-ancestor-link-secret"); ok {
					t.Fatal("链接变化只能报告错误，不能自动扩大读取范围")
				}
				if ok, _ := s.ExactSecretExists(ctx, "original-ancestor-link-secret"); !ok {
					t.Fatal("链接变化不能删除已导入值")
				}
				replaceLink(first)
				awaitSource(t, func() bool { return sourceState(t, s, r.ID).LastError == "" })
				if err := os.Remove(changingLink); err != nil {
					t.Fatal(err)
				}
				awaitSource(t, func() bool { return sourceState(t, s, r.ID).LastError != "" })
				if err := os.Symlink(first, changingLink); err != nil {
					t.Fatal(err)
				}
				awaitSource(t, func() bool { return sourceState(t, s, r.ID).LastError == "" })
				writeSourceFile(t, original, "PASSWORD=restored-ancestor-link-secret")
				awaitSource(t, func() bool {
					ok, _ := s.ExactSecretExists(ctx, "restored-ancestor-link-secret")
					return ok
				})
			})
		}
	}
}

func TestPrivacyConfigSourcesWatchIgnoresUnrelatedDirectoryChanges(t *testing.T) {
	s := newConfigSourceService(t)
	ctx := context.Background()
	base := t.TempDir()
	dir := filepath.Join(base, "project", "nested")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	target, alias := filepath.Join(dir, "config.data"), filepath.Join(dir, ".env")
	writeSourceFile(t, target, "PASSWORD=unrelated-directory-watch-secret")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	r := addSource(t, s, alias, "file")
	if err := s.StartConfigSourceSync(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.CloseConfigSourceSync)
	before := sourceState(t, s, r.ID).LastScanAt
	writeSourceFile(t, filepath.Join(dir, "unrelated.txt"), "unrelated file")
	writeSourceFile(t, filepath.Join(base, "unrelated.txt"), "unrelated ancestor sibling")
	// 覆盖两个防抖窗口，确认原生目录通知没有把普通兄弟文件变更当成来源变化。
	time.Sleep(600 * time.Millisecond)
	if sourceState(t, s, r.ID).LastScanAt != before {
		t.Fatal("无关目录项变化不应触发来源扫描")
	}
}
