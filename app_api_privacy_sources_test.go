package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"sinuo/internal/service"
	"sinuo/internal/store"
)

func TestPrivacyConfigSourceAPI(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	svc := service.NewPrivacyService(store.NewSQLitePrivacyStore(db))
	if err := svc.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	app := &App{privacyService: svc}
	path := filepath.Join(t.TempDir(), ".env")
	const secret = "synthetic-source-api-secret"
	if err := os.WriteFile(path, []byte("PASSWORD="+secret+"\nHOST=source-api-review.example.invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	saved, err := app.SavePrivacyConfigSource(store.PrivacyConfigSourceRecord{Path: path, ResolvedPath: "/forged/.env", Kind: "file", Enabled: true, LastError: secret, LastSuccessAt: "forged", ContentHash: "forged"})
	if err != nil || saved.ID <= 0 || saved.LastError != "" || saved.LastSuccessAt != "" || saved.ContentHash != "" {
		t.Fatal("客户端不能伪造状态", err)
	}
	wantPath, err := filepath.EvalSymlinks(path)
	if err != nil || saved.ResolvedPath != wantPath {
		t.Fatal("实际读取路径必须由服务端生成", err)
	}
	if err := app.SyncPrivacyConfigSources(saved.ID); err != nil {
		t.Fatal(err)
	}
	state, err := app.ListPrivacyConfigSources()
	if err != nil || len(state.Sources) != 1 || state.Sources[0].ValueCount != 1 || state.Sources[0].ReviewCount != 1 || state.Sources[0].LastSuccessAt == "" {
		t.Fatal(state, err)
	}
	wire, err := json.Marshal(state)
	if err != nil || bytes.Contains(wire, []byte(secret)) || bytes.Contains(wire, []byte("content_hash")) || bytes.Contains(wire, []byte("resolved_path")) {
		t.Fatal("API 暴露原值或内容摘要", err)
	}
	preview, err := app.PreviewPrivacyConfigSourceImport(saved.ID)
	if err != nil || len(preview.Files) != 1 {
		t.Fatal("按来源 ID 直接加载预览失败", err)
	}
	wire, _ = json.Marshal(preview)
	if bytes.Contains(wire, []byte("source-api-review.example.invalid")) || bytes.Contains(wire, []byte(secret)) {
		t.Fatal("来源预览 API 暴露原值")
	}
	var selected []string
	for _, candidate := range preview.Candidates {
		if candidate.Status == "review" {
			selected = append(selected, candidate.ID)
		}
	}
	if result, err := app.CommitPrivacyConfigImport(preview.ID, selected); err != nil || result.Added != 1 {
		t.Fatal("来源预览应复用现有确认 API", result, err)
	}
	state, err = app.ListPrivacyConfigSources()
	if err != nil || state.Sources[0].ReviewCount != 0 {
		t.Fatal("确认后 API 应返回最新剩余数量", err)
	}
	if _, err := app.PreviewPrivacyConfigSourceImport(0); err == nil {
		t.Fatal("来源预览必须拒绝无效 ID")
	}
	if err := app.DeletePrivacyConfigSource(saved.ID); err != nil {
		t.Fatal(err)
	}
	if exists, err := svc.ExactSecretExists(context.Background(), secret); err != nil || !exists {
		t.Fatal("移除来源不得移除词典原值", err)
	}
	if err := app.SyncPrivacyConfigSources(-1); err == nil {
		t.Fatal("非法 ID 被接受")
	}
}
