package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"sinuo/internal/store"
)

func TestPrivacyConfigSourcesMissingUnderSymlinkAncestor(t *testing.T) {
	for _, tc := range []struct{ name, kind, relative string }{
		{"file", "file", ".env"},
		{"nested-file", "file", filepath.Join("new", "project", ".env")},
		{"directory", "directory", filepath.Join("new", "project")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newConfigSourceService(t)
			ctx := context.Background()
			base := t.TempDir()
			real, alias := filepath.Join(base, "real"), filepath.Join(base, "alias")
			if err := os.Mkdir(real, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(real, alias); err != nil {
				t.Fatal(err)
			}
			canonical, err := filepath.EvalSymlinks(real)
			if err != nil {
				t.Fatal(err)
			}
			r := addSource(t, s, filepath.Join(alias, tc.relative), tc.kind)
			if r.ResolvedPath != filepath.Join(canonical, tc.relative) {
				t.Fatalf("缺失尾部没有拼接到规范化祖先: %q", r.ResolvedPath)
			}
			if err := s.StartConfigSourceSync(ctx); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(s.CloseConfigSourceSync)
			if sourceState(t, s, r.ID).LastError == "" {
				t.Fatal("不存在的来源应显示失败状态")
			}
			file := filepath.Join(real, tc.relative)
			if tc.kind == "directory" {
				file = filepath.Join(file, ".env")
			}
			if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
				t.Fatal(err)
			}
			writeSourceFile(t, file, "PASSWORD=created-after-registration-secret")
			awaitSource(t, func() bool {
				ok, _ := s.ExactSecretExists(ctx, "created-after-registration-secret")
				return ok && sourceState(t, s, r.ID).LastError == ""
			})
		})
	}
}

func TestPrivacyConfigSourcesSymlinkFormatSurvivesRestart(t *testing.T) {
	for _, tc := range []struct{ name, target, content, rotated string }{
		{".netrc", "netrc", "machine example.invalid password first-symlink-secret", "machine example.invalid password next-symlink-secret"},
		{"credentials", "aws-secret-data", "[default]\naws_secret_access_key=first-symlink-secret", "[default]\naws_secret_access_key=next-symlink-secret"},
		{".env", "config.json", "PASSWORD=first-symlink-secret", "PASSWORD=next-symlink-secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newConfigSourceService(t)
			ctx := context.Background()
			base := t.TempDir()
			path, target := filepath.Join(base, tc.name), filepath.Join(base, tc.target)
			writeSourceFile(t, target, tc.content)
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			r := addSource(t, s, path, "file")
			if r.Path != path || r.ResolvedPath == r.Path {
				t.Fatal("应分别保留登记路径与实际读取路径")
			}
			if err := s.SyncConfigSources(ctx, r.ID); err != nil {
				t.Fatal(err)
			}
			if ok, _ := s.ExactSecretExists(ctx, "first-symlink-secret"); !ok {
				t.Fatal("应按登记文件名解析凭据")
			}
			// 前端只回传公开字段；改名、暂停和恢复不能丢失解析类型。
			r.ResolvedPath, r.Name, r.Enabled = "", "renamed", false
			if _, err := s.SaveConfigSource(ctx, *r); err != nil {
				t.Fatal(err)
			}
			r.Enabled = true
			if _, err := s.SaveConfigSource(ctx, *r); err != nil {
				t.Fatal(err)
			}
			restarted := NewPrivacyService(s.store)
			if err := restarted.Initialize(ctx); err != nil {
				t.Fatal(err)
			}
			if err := restarted.StartConfigSourceSync(ctx); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(restarted.CloseConfigSourceSync)
			if sourceState(t, restarted, r.ID).Path != path {
				t.Fatal("登记文件名没有持久化")
			}
			writeSourceFile(t, target, tc.rotated)
			awaitSource(t, func() bool { ok, _ := restarted.ExactSecretExists(ctx, "next-symlink-secret"); return ok })
		})
	}
}

func TestPrivacyConfigSourcesResolvedPathBoundary(t *testing.T) {
	s := newConfigSourceService(t)
	ctx := context.Background()
	base := t.TempDir()
	target, alias := filepath.Join(base, "first.data"), filepath.Join(base, ".env")
	writeSourceFile(t, target, "PASSWORD=original-pinned-secret")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	r := addSource(t, s, alias, "file")
	if err := s.SyncConfigSources(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	otherAlias := filepath.Join(base, ".env.other")
	if err := os.Symlink(target, otherAlias); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveConfigSource(ctx, store.PrivacyConfigSourceRecord{Path: otherAlias, Kind: "file", Enabled: true}); err == nil {
		t.Fatal("不同逻辑路径不能重复登记同一实际位置")
	}
	otherTarget := filepath.Join(base, "second.data")
	writeSourceFile(t, otherTarget, "PASSWORD=unexpected-retargeted-secret")
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(otherTarget, alias); err != nil {
		t.Fatal(err)
	}
	r.Name = "renamed after retarget"
	updated, err := s.SaveConfigSource(ctx, *r)
	if err != nil || updated.ResolvedPath != r.ResolvedPath {
		t.Fatal("修改名称不能重新授权软链接的新目标", err)
	}
	if err := s.SyncConfigSources(ctx, r.ID); err == nil || sourceState(t, s, r.ID).LastError == "" {
		t.Fatal("逻辑链接改指向后应明确报告失败")
	}
	if ok, _ := s.ExactSecretExists(ctx, "original-pinned-secret"); !ok {
		t.Fatal("读取范围应固定在登记时的实际目标")
	}
	if ok, _ := s.ExactSecretExists(ctx, "unexpected-retargeted-secret"); ok {
		t.Fatal("不能自动跟随登记后改变的软链接")
	}
	if err := os.Remove(otherTarget); err != nil {
		t.Fatal(err)
	}
	r.Enabled = false
	if _, err := s.SaveConfigSource(ctx, *r); err != nil {
		t.Fatal("逻辑链接失效后仍应能暂停来源", err)
	}
	// 断开的已存在链接不属于可安全补全的缺失路径。
	dangling := filepath.Join(base, ".env.dangling")
	if err := os.Symlink(filepath.Join(base, "absent"), dangling); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveConfigSource(ctx, store.PrivacyConfigSourceRecord{Path: dangling, Kind: "file", Enabled: true}); err == nil {
		t.Fatal("不应保存无法解析的断开链接")
	}
}

func TestPrivacyConfigSourcesWatchAndManualCheckLogicalLinkStatus(t *testing.T) {
	s := newConfigSourceService(t)
	ctx := context.Background()
	base := t.TempDir()
	targetDir := filepath.Join(base, "targets")
	if err := os.Mkdir(targetDir, 0700); err != nil {
		t.Fatal(err)
	}
	original, next := filepath.Join(targetDir, "original"), filepath.Join(targetDir, "next")
	alias := filepath.Join(base, ".env")
	writeSourceFile(t, original, "PASSWORD=original-logical-link-secret")
	writeSourceFile(t, next, "PASSWORD=new-logical-link-secret")
	if err := os.Symlink(original, alias); err != nil {
		t.Fatal(err)
	}
	r := addSource(t, s, alias, "file")
	if err := s.StartConfigSourceSync(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.CloseConfigSourceSync)
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(next, alias); err != nil {
		t.Fatal(err)
	}
	// kqueue 可能合并同名链接的快速替换事件；手动检查仍必须校验登记路径。
	if err := s.SyncConfigSources(ctx, r.ID); err == nil || sourceState(t, s, r.ID).LastError == "" {
		t.Fatal("手动检查未发现逻辑链接已改指向")
	}
	if ok, _ := s.ExactSecretExists(ctx, "new-logical-link-secret"); ok {
		t.Fatal("逻辑链接重定向不能扩大读取范围")
	}
	if ok, _ := s.ExactSecretExists(ctx, "original-logical-link-secret"); !ok {
		t.Fatal("链接变更失败不应清除原有词典")
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(original, alias); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncConfigSources(ctx, r.ID); err != nil || sourceState(t, s, r.ID).LastError != "" {
		t.Fatal("恢复已登记目标后应清除错误状态", err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	// 实际目标的变更通知也必须重新校验逻辑路径，不能继续沿用旧的成功状态。
	writeSourceFile(t, original, "PASSWORD=original-logical-link-secret")
	awaitSource(t, func() bool { return sourceState(t, s, r.ID).LastError != "" })
}

func TestPrivacyConfigSourcesIdleDoesNotRescan(t *testing.T) {
	t.Parallel()
	s := newConfigSourceService(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), ".env")
	writeSourceFile(t, path, "PASSWORD=idle-watch-synthetic-secret")
	r := addSource(t, s, path, "file")
	if err := s.StartConfigSourceSync(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.CloseConfigSourceSync)
	initial := sourceState(t, s, r.ID)
	if initial.LastScanAt == "" || initial.LastError != "" {
		t.Fatal("启动必须完成来源检查", initial)
	}
	if ok, err := s.ExactSecretExists(ctx, "idle-watch-synthetic-secret"); err != nil || !ok {
		t.Fatal("启动必须导入来源值", err)
	}
	// 跨过原来的 30 秒扫描周期，确认没有事件时不重新读取来源或更新检查时间。
	time.Sleep(32 * time.Second)
	if sourceState(t, s, r.ID).LastScanAt != initial.LastScanAt {
		t.Fatal("没有文件变化时不应定时重新扫描来源")
	}
	if err := s.SyncConfigSources(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if sourceState(t, s, r.ID).LastScanAt == initial.LastScanAt {
		t.Fatal("手动立即检查必须重新检查来源")
	}
	writeSourceFile(t, path, "PASSWORD=changed-watch-synthetic-secret")
	awaitSource(t, func() bool {
		ok, _ := s.ExactSecretExists(ctx, "changed-watch-synthetic-secret")
		return ok
	})
}

func TestPrivacyConfigSourcesNpmBooleanDoesNotBlockDirectory(t *testing.T) {
	s := newConfigSourceService(t)
	ctx := context.Background()
	dir := t.TempDir()
	writeSourceFile(t, filepath.Join(dir, ".npmrc"), "strict-ssl\n//registry.example/:_authToken=first-npm-config-secret")
	writeSourceFile(t, filepath.Join(dir, ".env"), "PASSWORD=directory-env-secret")
	r := addSource(t, s, dir, "directory")
	for _, token := range []string{"first-npm-config-secret", "rotated-npm-config-secret"} {
		writeSourceFile(t, filepath.Join(dir, ".npmrc"), "strict-ssl\n//registry.example/:_authToken="+token)
		if err := s.SyncConfigSources(ctx, r.ID); err != nil {
			t.Fatal(err)
		}
		if ok, _ := s.ExactSecretExists(ctx, token); !ok {
			t.Fatal("npm 布尔项不能阻止新凭据导入")
		}
	}
	if ok, _ := s.ExactSecretExists(ctx, "directory-env-secret"); !ok {
		t.Fatal("npm 布尔项不能影响同目录其他配置")
	}
}
