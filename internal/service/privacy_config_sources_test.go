package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sinuo/internal/privacy"
	"sinuo/internal/privacy/configimport"
	"sinuo/internal/store"
)

func newConfigSourceService(t *testing.T) *PrivacyService {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	// :memory: 每个连接独立；后台同步与断言必须共享同一个测试数据库。
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	s := NewPrivacyService(store.NewSQLitePrivacyStore(db))
	if err := s.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}

func writeSourceFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func addSource(t *testing.T, s *PrivacyService, path, kind string) *store.PrivacyConfigSourceRecord {
	t.Helper()
	r, err := s.SaveConfigSource(context.Background(), store.PrivacyConfigSourceRecord{Path: path, Kind: kind, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func sourceState(t *testing.T, s *PrivacyService, id int64) *store.PrivacyConfigSourceRecord {
	t.Helper()
	state, err := s.ListConfigSources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range state.Sources {
		if r.ID == id {
			return r
		}
	}
	t.Fatal("来源不存在")
	return nil
}

func TestPrivacyConfigSourcesRotationAndLifecycle(t *testing.T) {
	s := newConfigSourceService(t)
	ctx := context.Background()
	enableRedactMode(t, s)
	path := filepath.Join(t.TempDir(), ".env")
	writeSourceFile(t, path, "PASSWORD=first-synthetic-password\nHOST=example.invalid\nOTHER_PASSWORD=changeme\nSHORT_PASSWORD=tiny\nTOKEN=${FROM_ENV}\n")
	r := addSource(t, s, path, "file")
	version := s.SnapshotVersion()
	if err := s.SyncConfigSources(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if s.SnapshotVersion() != version+1 {
		t.Fatal("一次同步应只发布一次快照")
	}
	state := sourceState(t, s, r.ID)
	if state.ValueCount != 1 || state.ReviewCount != 2 || state.ExcludedCount != 2 || state.LastSuccessAt == "" {
		t.Fatal(state)
	}
	wire, _ := json.Marshal(state)
	if bytes.Contains(wire, []byte("first-synthetic-password")) {
		t.Fatal("状态包含原值")
	}
	if err := s.SyncConfigSources(ctx, r.ID); err != nil || s.SnapshotVersion() != version+1 {
		t.Fatal("未变化不应重编译", err)
	}
	writeSourceFile(t, path, "PASSWORD=second-synthetic-password")
	if err := s.SyncConfigSources(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"first-synthetic-password", "second-synthetic-password"} {
		body, _ := json.Marshal(map[string]any{"messages": []map[string]string{{"role": "user", "content": v}}})
		result, err := s.Apply(ctx, privacy.Request{Path: "/v1/messages", UpstreamType: privacy.UpstreamTypeEndpoint}, body)
		if err != nil || bytes.Contains(result.Body, []byte(v)) {
			t.Fatal("轮换前后值均应保护", err)
		}
	}
	before := s.SnapshotVersion()
	writeSourceFile(t, path, "PASSWORD=third-synthetic-password\ninvalid-line")
	if err := s.SyncConfigSources(ctx, r.ID); err == nil || strings.Contains(err.Error(), "third-synthetic-password") {
		t.Fatal("坏文件必须安全失败", err)
	}
	if s.SnapshotVersion() != before || sourceState(t, s, r.ID).LastError == "" {
		t.Fatal("失败不应发布部分词典")
	}
	writeSourceFile(t, path, "PASSWORD=second-synthetic-password")
	if err := s.SyncConfigSources(ctx, r.ID); err != nil || sourceState(t, s, r.ID).LastError != "" {
		t.Fatal("原内容恢复应清除失败状态", err)
	}
	records, _ := s.ListExactSecrets(ctx)
	for _, record := range records {
		if record.SecretValue == "second-synthetic-password" {
			if err := s.DeleteExactSecret(ctx, record.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	restarted := NewPrivacyService(s.store)
	if err := restarted.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if err := restarted.StartConfigSourceSync(ctx); err != nil {
		t.Fatal(err)
	}
	defer restarted.CloseConfigSourceSync()
	if ok, _ := restarted.ExactSecretExists(ctx, "second-synthetic-password"); ok {
		t.Fatal("重启不能复活手动删除值")
	}
	if err := restarted.DeleteConfigSource(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if ok, _ := restarted.ExactSecretExists(ctx, "first-synthetic-password"); !ok {
		t.Fatal("移除来源不能删除历史敏感值")
	}
}

func TestPrivacyConfigSourcesDirectoryAtomicityAndBoundaries(t *testing.T) {
	s := newConfigSourceService(t)
	ctx := context.Background()
	dir := t.TempDir()
	writeSourceFile(t, filepath.Join(dir, ".env"), "PASSWORD=first-directory-secret")
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	writeSourceFile(t, filepath.Join(dir, "nested", ".env"), "PASSWORD=nested-directory-secret")
	r := addSource(t, s, dir, "directory")
	if err := s.SyncConfigSources(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.ExactSecretExists(ctx, "nested-directory-secret"); ok {
		t.Fatal("不应递归扫描")
	}
	writeSourceFile(t, filepath.Join(dir, ".env.new"), "PASSWORD=new-directory-secret")
	writeSourceFile(t, filepath.Join(dir, "broken.json"), "{broken")
	version := s.SnapshotVersion()
	if err := s.SyncConfigSources(ctx, r.ID); err == nil || s.SnapshotVersion() != version {
		t.Fatal("来源内任一坏文件必须整批失败", err)
	}
	if ok, _ := s.ExactSecretExists(ctx, "new-directory-secret"); ok {
		t.Fatal("不能部分导入")
	}
	if err := os.Remove(filepath.Join(dir, "broken.json")); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncConfigSources(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.ExactSecretExists(ctx, "new-directory-secret"); !ok {
		t.Fatal("修复后应恢复导入")
	}
	link := filepath.Join(dir, ".env.link")
	if err := os.Symlink(filepath.Join(dir, "nested", ".env"), link); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncConfigSources(ctx, r.ID); err == nil {
		t.Fatal("不能跟随目录中链接")
	}
	for _, input := range []store.PrivacyConfigSourceRecord{
		{Path: "relative/.env", Kind: "file"}, {Path: dir, Kind: "directory", Patterns: "../*.json"}, {Path: dir, Kind: "directory", Patterns: "["},
		{Path: filepath.Join(dir, "binary.dat"), Kind: "file"},
	} {
		if _, err := s.SaveConfigSource(ctx, input); err == nil {
			t.Fatal("非法来源被接受", input)
		}
	}
}

func awaitSource(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("等待文件同步超时")
}

func TestPrivacyConfigSourcesWatchStartupRenameAndRecreate(t *testing.T) {
	s := newConfigSourceService(t)
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	writeSourceFile(t, path, "PASSWORD=startup-watch-secret")
	r := addSource(t, s, path, "file")
	if err := s.StartConfigSourceSync(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.CloseConfigSourceSync)
	if ok, _ := s.ExactSecretExists(ctx, "startup-watch-secret"); !ok {
		t.Fatal("启动返回前必须完成初始同步")
	}
	tmp := filepath.Join(dir, "atomic.tmp")
	writeSourceFile(t, tmp, "PASSWORD=renamed-watch-secret")
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	awaitSource(t, func() bool { ok, _ := s.ExactSecretExists(ctx, "renamed-watch-secret"); return ok })
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	awaitSource(t, func() bool { return sourceState(t, s, r.ID).LastError != "" })
	writeSourceFile(t, path, "PASSWORD=recreated-watch-secret")
	awaitSource(t, func() bool { ok, _ := s.ExactSecretExists(ctx, "recreated-watch-secret"); return ok })
	r.Enabled = false
	if _, err := s.SaveConfigSource(ctx, *r); err != nil {
		t.Fatal(err)
	}
	writeSourceFile(t, path, "PASSWORD=disabled-watch-secret")
	if err := s.SyncConfigSources(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.ExactSecretExists(ctx, "disabled-watch-secret"); ok {
		t.Fatal("停用来源不应同步")
	}
	s.CloseConfigSourceSync()
	s.CloseConfigSourceSync()
	state, err := s.ListConfigSources(ctx)
	if err != nil || state.Watching {
		t.Fatal("关闭后不能显示正在监听", err)
	}
}

func TestPrivacyConfigSourcesDuplicateKeepsDisabledAndRollsBackBudget(t *testing.T) {
	s := newConfigSourceService(t)
	ctx := context.Background()
	dir := t.TempDir()
	_, err := s.CreateExactSecret(ctx, &store.PrivacyExactSecretRecord{Name: "manual", SecretValue: "existing-disabled-secret", Category: "password", Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	writeSourceFile(t, filepath.Join(dir, ".env"), "PASSWORD=existing-disabled-secret")
	r := addSource(t, s, dir, "directory")
	if err := s.SyncConfigSources(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	all, _ := s.ListExactSecrets(ctx)
	if len(all) != 1 || all[0].Enabled || all[0].Name != "manual" {
		t.Fatal("导入改动了已有配置")
	}
	// 让来源超过手动导入的批次数量，容量校验仍必须覆盖整个来源。
	for i := 0; i < 33; i++ {
		writeSourceFile(t, filepath.Join(dir, fmt.Sprintf(".env.empty.%03d", i)), "# synthetic empty config")
	}
	for i := 0; i < 3; i++ {
		var content strings.Builder
		for j := 0; j < 30; j++ {
			fmt.Fprintf(&content, "PASSWORD_%d_PASSWORD=%d_%d_%s\n", j, i, j, strings.Repeat("q", 50000))
		}
		writeSourceFile(t, filepath.Join(dir, ".env."+string(rune('a'+i))), content.String())
	}
	version := s.SnapshotVersion()
	if err := s.SyncConfigSources(ctx, r.ID); !errors.Is(err, errPrivacyConfigDictionaryLimit) {
		t.Fatal("应拒绝超出原文预算", err)
	}
	all, _ = s.ListExactSecrets(ctx)
	if len(all) != 1 || s.SnapshotVersion() != version {
		t.Fatal("容量失败不应写入或发布")
	}
}

func TestPrivacyConfigSourcesDirectoryMoreThan32Files(t *testing.T) {
	s := newConfigSourceService(t)
	ctx := context.Background()
	dir := t.TempDir()
	for i := 0; i < 64; i++ {
		writeSourceFile(t, filepath.Join(dir, fmt.Sprintf(".env.%03d", i)), fmt.Sprintf("PASSWORD=directory-many-files-secret-%03d", i))
	}
	r := addSource(t, s, dir, "directory")
	if err := s.StartConfigSourceSync(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.CloseConfigSourceSync)
	state := sourceState(t, s, r.ID)
	all, err := s.ListExactSecrets(ctx)
	if err != nil || len(all) != 64 || state.FileCount != 64 || state.ValueCount != 64 || state.LastError != "" {
		t.Fatalf("启动应完整导入 64 个文件: values=%d files=%d error=%q err=%v", len(all), state.FileCount, state.LastError, err)
	}
	writeSourceFile(t, filepath.Join(dir, ".env.063"), "PASSWORD=directory-many-files-rotated-secret")
	awaitSource(t, func() bool { ok, _ := s.ExactSecretExists(ctx, "directory-many-files-rotated-secret"); return ok })
	if ok, _ := s.ExactSecretExists(ctx, "directory-many-files-secret-063"); !ok {
		t.Fatal("文件变更后应保留旧值")
	}
	s.CloseConfigSourceSync()
	version := s.SnapshotVersion()
	state = sourceState(t, s, r.ID)
	writeSourceFile(t, filepath.Join(dir, ".env.000"), "PASSWORD=must-not-partially-import-secret")
	writeSourceFile(t, filepath.Join(dir, "zz-broken.json"), "{broken")
	if err := s.SyncConfigSources(ctx, r.ID); err == nil {
		t.Fatal("第 65 个文件解析失败时应拒绝整个来源")
	}
	if ok, _ := s.ExactSecretExists(ctx, "must-not-partially-import-secret"); ok {
		t.Fatal("不能因取消文件数限制而部分导入")
	}
	failed := sourceState(t, s, r.ID)
	if s.SnapshotVersion() != version || failed.LastError == "" || failed.FileCount != state.FileCount || failed.LastSuccessAt != state.LastSuccessAt {
		t.Fatal("失败应保留上次有效词典和同步结果")
	}
}

func TestPrivacyConfigSourcesManyFilesBudgetRollback(t *testing.T) {
	for _, kind := range []string{"single-file", "total-bytes", "candidates"} {
		t.Run(kind, func(t *testing.T) {
			s := newConfigSourceService(t)
			ctx := context.Background()
			dir := t.TempDir()
			writeSourceFile(t, filepath.Join(dir, ".env"), "PASSWORD=already-protected-source-secret")
			r := addSource(t, s, dir, "directory")
			if err := s.SyncConfigSources(ctx, r.ID); err != nil {
				t.Fatal(err)
			}
			version := s.SnapshotVersion()
			before := sourceState(t, s, r.ID)
			wantError := ""
			for i := 0; i < 64; i++ {
				body := fmt.Sprintf("PASSWORD=new-many-files-secret-%03d", i)
				switch kind {
				case "single-file":
					wantError = "2 MiB"
					if i == 63 {
						body = "#" + strings.Repeat("x", configimport.MaxFileBytes)
					}
				case "total-bytes":
					wantError = "8 MiB"
					body = "#" + strings.Repeat("x", configimport.MaxTotalBytes/64)
				case "candidates":
					wantError = "候选超过 10000"
					var content strings.Builder
					for j := 0; j < 160; j++ {
						fmt.Fprintf(&content, "DB_%d_PASSWORD=new-many-files-secret-%03d\n", j, i)
					}
					body = content.String()
				}
				writeSourceFile(t, filepath.Join(dir, fmt.Sprintf(".env.%03d", i)), body)
			}
			if err := s.SyncConfigSources(ctx, r.ID); err == nil || !strings.Contains(err.Error(), wantError) {
				t.Fatalf("应命中原有容量限制 %q: %v", wantError, err)
			}
			all, err := s.ListExactSecrets(ctx)
			if err != nil || len(all) != 1 || all[0].SecretValue != "already-protected-source-secret" || s.SnapshotVersion() != version {
				t.Fatal("超限不能写入部分新值或改变旧词典", err)
			}
			after := sourceState(t, s, r.ID)
			if after.LastError == "" || after.LastSuccessAt != before.LastSuccessAt || after.ContentHash != before.ContentHash {
				t.Fatal("超限应记录错误并保留上次成功结果")
			}
		})
	}
}

func TestPrivacyConfigSourcesMoreThan32Registrations(t *testing.T) {
	s := newConfigSourceService(t)
	ctx := context.Background()
	base := t.TempDir()
	var sources []*store.PrivacyConfigSourceRecord
	var files []string
	for i := 0; i < 64; i++ {
		path, kind := filepath.Join(base, fmt.Sprintf(".env.%03d", i)), "file"
		file := path
		if i%2 != 0 {
			path, kind = filepath.Join(base, fmt.Sprintf("project-%03d", i)), "directory"
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			file = filepath.Join(path, ".env")
		}
		writeSourceFile(t, file, fmt.Sprintf("PASSWORD=registered-source-secret-%03d", i))
		sources = append(sources, addSource(t, s, path, kind))
		files = append(files, file)
	}
	if err := s.StartConfigSourceSync(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.CloseConfigSourceSync)
	state, err := s.ListConfigSources(ctx)
	if err != nil || len(state.Sources) != 64 {
		t.Fatal("应完整保存并读取 64 个来源", err)
	}
	for i, source := range state.Sources {
		if source.LastError != "" || source.FileCount != 1 || source.ValueCount != 1 {
			t.Fatalf("第 %d 个来源未在启动时同步: %+v", i, source)
		}
		if ok, err := s.ExactSecretExists(ctx, fmt.Sprintf("registered-source-secret-%03d", i)); err != nil || !ok {
			t.Fatalf("第 %d 个来源的值遗漏: %v", i, err)
		}
	}
	writeSourceFile(t, files[63], "PASSWORD=registered-source-tail-rotated-secret")
	awaitSource(t, func() bool { ok, _ := s.ExactSecretExists(ctx, "registered-source-tail-rotated-secret"); return ok })
	s.CloseConfigSourceSync()
	last := *sources[63]
	last.Name = "renamed last source"
	if _, err := s.SaveConfigSource(ctx, last); err != nil {
		t.Fatal("超过 32 个来源后仍应能编辑", err)
	}
	if _, err := s.SaveConfigSource(ctx, store.PrivacyConfigSourceRecord{Path: sources[0].Path, Kind: "file", Enabled: true}); err == nil {
		t.Fatal("取消数量上限后仍应拒绝重复路径")
	}
	oversize := filepath.Join(base, ".env.oversize")
	writeSourceFile(t, oversize, "#"+strings.Repeat("x", configimport.MaxFileBytes))
	bad := addSource(t, s, oversize, "file")
	writeSourceFile(t, files[0], "PASSWORD=registered-source-first-rotated-secret")
	if err := s.SyncConfigSources(ctx, 0); err == nil || !strings.Contains(err.Error(), "2 MiB") {
		t.Fatal("第 65 个来源仍应受文件大小限制", err)
	}
	if sourceState(t, s, bad.ID).LastError == "" {
		t.Fatal("超大文件的失败状态应可见")
	}
	if ok, _ := s.ExactSecretExists(ctx, "registered-source-first-rotated-secret"); !ok {
		t.Fatal("一个来源超限不能阻止其他来源同步")
	}
	if ok, _ := s.ExactSecretExists(ctx, "registered-source-secret-000"); !ok {
		t.Fatal("同步后应保留已导入旧值")
	}
}

func TestPrivacyConfigSourcesDirectoryOnlyReadsSupportedMatchingFiles(t *testing.T) {
	s := newConfigSourceService(t)
	ctx := context.Background()
	dir := t.TempDir()
	configs := map[string]string{
		".env":                "PASSWORD=filter-env-original-secret",
		".env.development":    "PASSWORD=filter-env-development-secret",
		".npmrc":              "_authToken=filter-npm-token-secret",
		".netrc":              "machine example.invalid password filter-netrc-password",
		"_netrc":              "machine example.invalid password filter-netrc-other-password",
		"credentials":         "[default]\naws_secret_access_key=filter-aws-credential-secret",
		"secrets.json":        `{"password":"filter-json-password"}`,
		"settings.yaml":       "password: filter-yaml-password",
		"settings.yml":        "password: filter-yml-password",
		"settings.properties": "password=filter-properties-password",
	}
	for name, content := range configs {
		writeSourceFile(t, filepath.Join(dir, name), content)
	}
	// 非支持格式含非法 UTF-8/NUL 或超大内容；若被读取会使整个来源失败。
	for _, name := range []string{"main.go", "index.js", "notes.txt", "image.png", "settings.toml", "settings.ini"} {
		writeSourceFile(t, filepath.Join(dir, name), "\x00\xff")
	}
	writeSourceFile(t, filepath.Join(dir, "large.go"), strings.Repeat("x", configimport.MaxFileBytes+1))
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	writeSourceFile(t, filepath.Join(dir, "nested", ".env"), "PASSWORD=nested-must-not-import-secret")
	r := addSource(t, s, dir, "directory")
	for _, patterns := range []string{DefaultPrivacySourcePatterns, "*"} {
		r.Patterns = patterns
		if _, err := s.SaveConfigSource(ctx, *r); err != nil {
			t.Fatal(err)
		}
		if err := s.SyncConfigSources(ctx, r.ID); err != nil {
			t.Fatal("不应读取不支持的文件格式", err)
		}
		state := sourceState(t, s, r.ID)
		if state.FileCount != len(configs) || state.ValueCount != len(configs) {
			t.Fatalf("应仅提取受支持配置文件: files=%d values=%d", state.FileCount, state.ValueCount)
		}
	}
	if ok, _ := s.ExactSecretExists(ctx, "nested-must-not-import-secret"); ok {
		t.Fatal("不能递归进入子目录")
	}
	// 支持格式仍需匹配用户模式：缩小到 env 后不读取同目录中的坏 JSON。
	writeSourceFile(t, filepath.Join(dir, "bad.json"), "{broken")
	writeSourceFile(t, filepath.Join(dir, ".env"), "PASSWORD=filter-env-updated-secret")
	r.Patterns = ".env,.env.*"
	if _, err := s.SaveConfigSource(ctx, *r); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncConfigSources(ctx, r.ID); err != nil {
		t.Fatal("不匹配模式的文件不应影响同步", err)
	}
	if sourceState(t, s, r.ID).FileCount != 2 {
		t.Fatal("应只读取匹配模式的两个 env 文件")
	}
	if ok, _ := s.ExactSecretExists(ctx, "filter-env-updated-secret"); !ok {
		t.Fatal("匹配文件应正常更新")
	}
}
