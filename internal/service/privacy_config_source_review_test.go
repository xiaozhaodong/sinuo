package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sinuo/internal/privacy"
	"sinuo/internal/privacy/configimport"
	"sinuo/internal/store"
)

func TestPrivacyConfigSourceReviewCountDeduplicatesAndExcludesExisting(t *testing.T) {
	s := newConfigSourceService(t)
	ctx := context.Background()
	if _, err := s.CreateExactSecret(ctx, &store.PrivacyExactSecretRecord{Name: "已有停用地址", SecretValue: "existing-review.example.invalid", Category: "custom", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeSourceFile(t, filepath.Join(dir, ".env"), "HOST=shared-review.example.invalid\nDB_HOST=shared-review.example.invalid\nAPI_HOST=existing-review.example.invalid\nUSERNAME=source-review-user\nPASSWORD=source-review-auto-secret")
	writeSourceFile(t, filepath.Join(dir, ".env.local"), "HOST=shared-review.example.invalid")
	r := addSource(t, s, dir, "directory")
	if err := s.SyncConfigSources(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if got := sourceState(t, s, r.ID).ReviewCount; got != 2 {
		t.Fatalf("去重并排除已有停用值后应剩余 2 项，实际 %d", got)
	}
}

func reviewCandidateIDs(preview PrivacyConfigPreview) []string {
	var ids []string
	for _, candidate := range preview.Candidates {
		if candidate.Status == "review" && candidate.Selectable {
			ids = append(ids, candidate.ID)
		}
	}
	return ids
}

func TestPrivacyConfigSourceReviewDeletedValueStaysDeleted(t *testing.T) {
	s := newConfigSourceService(t)
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	const confirmed = "confirmed-source-synthetic-value"
	const unselected = "unselected-source-synthetic-value"
	writeSourceFile(t, path, "USERNAME="+confirmed+"\nHOST="+unselected)
	writeSourceFile(t, filepath.Join(dir, ".env.local"), "USERNAME="+confirmed)
	r := addSource(t, s, dir, "directory")
	if err := s.SyncConfigSources(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	preview, err := s.PreviewConfigSourceImport(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	var selected string
	for _, candidate := range preview.Candidates {
		if strings.HasPrefix(candidate.Name, ".env:USERNAME（") {
			selected = candidate.ID
		}
	}
	if selected == "" {
		t.Fatal("缺少待确认候选")
	}
	if _, err := s.CommitConfigImport(ctx, preview.ID, []string{selected}); err != nil {
		t.Fatal(err)
	}
	secrets, err := s.ListExactSecrets(ctx)
	if err != nil || len(secrets) != 1 {
		t.Fatal("只应导入选中的值", err)
	}
	if err := s.DeleteExactSecret(ctx, secrets[0].ID); err != nil {
		t.Fatal(err)
	}
	// 先重启，再让已确认和未选择的值都被密码字段命中。
	restarted := NewPrivacyService(s.store)
	if err := restarted.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	writeSourceFile(t, path, "USERNAME="+confirmed+"\nPASSWORD="+confirmed+"\nOTHER_PASSWORD="+unselected)
	if err := restarted.SyncConfigSources(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if exists, _ := restarted.ExactSecretExists(ctx, confirmed); exists {
		t.Fatal("同一来源不得自动重新加入已手动删除的确认值")
	}
	if exists, _ := restarted.ExactSecretExists(ctx, unselected); !exists {
		t.Fatal("未确认的候选不能被误记为已处理历史")
	}
}

func TestPrivacyConfigSourceReviewPauseAndRenameKeepPending(t *testing.T) {
	s := newConfigSourceService(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), ".env")
	writeSourceFile(t, path, "USERNAME=pending-source-user")
	r := addSource(t, s, path, "file")
	if err := s.SyncConfigSources(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	before := sourceState(t, s, r.ID)
	r.Enabled = false
	paused, err := s.SaveConfigSource(ctx, *r)
	if err != nil {
		t.Fatal(err)
	}
	r.Name = "暂停后的新名称"
	renamed, err := s.SaveConfigSource(ctx, *r)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []*store.PrivacyConfigSourceRecord{paused, renamed, sourceState(t, s, r.ID)} {
		if state.Enabled || state.ReviewCount != 1 || state.LastScanAt != before.LastScanAt || state.LastSuccessAt != before.LastSuccessAt || state.FileCount != before.FileCount {
			t.Fatal("暂停与改名必须保留候选入口和扫描结果", state)
		}
	}
	preview, err := s.PreviewConfigSourceImport(ctx, r.ID)
	if err != nil || len(reviewCandidateIDs(preview)) != 1 {
		t.Fatal("暂停状态仍应允许手动预览", err)
	}
	if result, err := s.CommitConfigImport(ctx, preview.ID, reviewCandidateIDs(preview)); err != nil || result.Added != 1 || sourceState(t, s, r.ID).ReviewCount != 0 {
		t.Fatal("暂停状态手动确认应生效并更新剩余数量", result, err)
	}
	writeSourceFile(t, path, "USERNAME=pending-source-user\nPASSWORD=paused-source-new-secret")
	if err := s.SyncConfigSources(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if exists, _ := s.ExactSecretExists(ctx, "paused-source-new-secret"); exists {
		t.Fatal("保留候选入口不能重新开启自动同步")
	}
}

func TestPrivacyConfigSourceReviewPreviewCommitAndRemaining(t *testing.T) {
	s := newConfigSourceService(t)
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	writeSourceFile(t, path, "HOST=review-host.example.invalid\nUSERNAME=review-confirm-user\nPASSWORD=review-auto-password")
	writeSourceFile(t, filepath.Join(dir, ".env.local"), "HOST=review-host.example.invalid")
	directory, file := addSource(t, s, dir, "directory"), addSource(t, s, path, "file")
	if err := s.SyncConfigSources(ctx, 0); err != nil {
		t.Fatal(err)
	}
	before := s.SnapshotVersion()
	at := sourceState(t, s, directory.ID).LastScanAt
	preview, err := s.PreviewConfigSourceImport(ctx, directory.ID)
	if err != nil || len(preview.Files) != 2 || len(reviewCandidateIDs(preview)) != 2 {
		t.Fatal("直接预览应加载整个来源并合并相同值", err)
	}
	wire, _ := json.Marshal(preview)
	for _, value := range []string{"review-host.example.invalid", "review-confirm-user", "review-auto-password", HashPrivacySecretValue("review-confirm-user")} {
		if bytes.Contains(wire, []byte(value)) {
			t.Fatal("预览泄露原值或摘要")
		}
	}
	s.CancelConfigImport(preview.ID)
	if s.SnapshotVersion() != before || sourceState(t, s, directory.ID).ReviewCount != 2 {
		t.Fatal("打开或取消预览不应导入值")
	}
	if _, err := s.CommitConfigImport(ctx, preview.ID, reviewCandidateIDs(preview)); err == nil {
		t.Fatal("已取消预览不能确认")
	}
	preview, err = s.PreviewConfigSourceImport(ctx, directory.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CommitConfigImport(ctx, preview.ID, []string{"forged-id"}); err == nil {
		t.Fatal("不能确认预览以外的候选")
	}
	result, err := s.CommitConfigImport(ctx, preview.ID, reviewCandidateIDs(preview)[:1])
	if err != nil || result.Added != 1 || s.SnapshotVersion() != before+1 {
		t.Fatal("部分确认必须一次写入并热生效", result, err)
	}
	for _, source := range []*store.PrivacyConfigSourceRecord{directory, file} {
		if got := sourceState(t, s, source.ID).ReviewCount; got != 1 {
			t.Fatalf("同值所在来源均应立即减少到 1 项，实际 %d", got)
		}
	}
	if sourceState(t, s, directory.ID).LastScanAt != at {
		t.Fatal("更新数量不应依赖重新读取配置文件")
	}
	restarted := NewPrivacyService(s.store)
	if err := restarted.Initialize(ctx); err != nil || sourceState(t, restarted, directory.ID).ReviewCount != 1 {
		t.Fatal("重启后必须保留剩余数量", err)
	}
	if err := restarted.SyncConfigSources(ctx, directory.ID); err != nil || sourceState(t, restarted, directory.ID).ReviewCount != 1 {
		t.Fatal("重新扫描不能恢复已确认数量", err)
	}
	preview, err = restarted.PreviewConfigSourceImport(ctx, directory.ID)
	if err != nil || len(reviewCandidateIDs(preview)) != 1 {
		t.Fatal("再次预览仅有剩余 1 项待确认", err)
	}
	if _, err := restarted.CommitConfigImport(ctx, preview.ID, reviewCandidateIDs(preview)); err != nil || sourceState(t, restarted, directory.ID).ReviewCount != 0 {
		t.Fatal("全部确认后计数必须归零", err)
	}
}

func TestPrivacyConfigSourceReviewRejectsChangedOrRemovedSource(t *testing.T) {
	for _, change := range []string{"content", "definition", "deleted", "symlink"} {
		t.Run(change, func(t *testing.T) {
			s := newConfigSourceService(t)
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), ".env")
			writeSourceFile(t, path, "USERNAME=original-review-user")
			r := addSource(t, s, path, "file")
			preview, err := s.PreviewConfigSourceImport(ctx, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.CancelConfigImport(preview.ID) })
			switch change {
			case "content":
				writeSourceFile(t, path, "USERNAME=changed-review-user")
			case "definition":
				r.Path = filepath.Join(t.TempDir(), ".env")
				writeSourceFile(t, r.Path, "USERNAME=other-review-user")
				if _, err := s.SaveConfigSource(ctx, *r); err != nil {
					t.Fatal(err)
				}
			case "deleted":
				if err := s.DeleteConfigSource(ctx, r.ID); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(t.TempDir(), "target")
				writeSourceFile(t, target, "USERNAME=outside-review-user")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			before := s.SnapshotVersion()
			if _, err := s.CommitConfigImport(ctx, preview.ID, reviewCandidateIDs(preview)); err == nil {
				t.Fatal("来源改变后不应确认旧预览")
			}
			all, err := s.ListExactSecrets(ctx)
			if err != nil || len(all) != 0 || s.SnapshotVersion() != before {
				t.Fatal("失败确认不得写入或发布快照", err)
			}
			if change == "content" {
				fresh, err := s.PreviewConfigSourceImport(ctx, r.ID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.CommitConfigImport(ctx, fresh.ID, reviewCandidateIDs(fresh)); err != nil {
					t.Fatal("重新检查后应可确认当前值", err)
				}
				if ok, _ := s.ExactSecretExists(ctx, "changed-review-user"); !ok {
					t.Fatal("未确认当前值")
				}
			}
		})
	}
}

func TestPrivacyConfigSourceReviewMoreThan32FilesAndReadBoundaries(t *testing.T) {
	s := newConfigSourceService(t)
	ctx := context.Background()
	dir := t.TempDir()
	for i := 0; i < 40; i++ {
		writeSourceFile(t, filepath.Join(dir, fmt.Sprintf(".env.%02d", i)), fmt.Sprintf("USERNAME=source-review-user-%02d", i))
	}
	writeSourceFile(t, filepath.Join(dir, "source.go"), "invalid content")
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	writeSourceFile(t, filepath.Join(dir, "nested", ".env"), "USERNAME=nested-review-user")
	r := addSource(t, s, dir, "directory")
	preview, err := s.PreviewConfigSourceImport(ctx, r.ID)
	if err != nil || len(preview.Files) != 40 || len(reviewCandidateIDs(preview)) != 40 || sourceState(t, s, r.ID).ReviewCount != 40 {
		t.Fatal("来源预览不能受手动导入的 32 个文件限制，也不能递归或读取其他格式", err)
	}
	s.CancelConfigImport(preview.ID)
	for _, id := range []int64{0, -1, r.ID + 100} {
		if _, err := s.PreviewConfigSourceImport(ctx, id); err == nil {
			t.Fatal("未登记的来源 ID 不应被接受")
		}
	}
	writeSourceFile(t, filepath.Join(dir, "broken.json"), "{broken")
	if _, err := s.PreviewConfigSourceImport(ctx, r.ID); err == nil || s.configImport != nil {
		t.Fatal("来源内损坏文件必须阻止整批预览")
	}
	if err := os.Remove(filepath.Join(dir, "broken.json")); err != nil {
		t.Fatal(err)
	}
	writeSourceFile(t, filepath.Join(dir, ".env.large"), strings.Repeat("x", configimport.MaxFileBytes+1))
	if _, err := s.PreviewConfigSourceImport(ctx, r.ID); err == nil || !strings.Contains(err.Error(), "2 MiB") {
		t.Fatal("来源预览必须保留单文件限制", err)
	}
}

func TestPrivacyConfigSourceReviewBudgetRollbackAndRetry(t *testing.T) {
	s := newConfigSourceService(t)
	ctx := context.Background()
	values := make([]map[string]string, 25)
	for i := range values {
		values[i] = map[string]string{"username": strings.Repeat("A+/:?&= ࠾࠿z", 1000) + fmt.Sprintf("%04d", i)}
	}
	body, _ := json.Marshal(values)
	path := filepath.Join(t.TempDir(), "config.json")
	writeSourceFile(t, path, string(body))
	r := addSource(t, s, path, "file")
	preview, err := s.PreviewConfigSourceImport(ctx, r.ID)
	if err != nil || len(reviewCandidateIDs(preview)) != len(values) {
		t.Fatal("应生成全部可选候选", err)
	}
	before := s.SnapshotVersion()
	ids := reviewCandidateIDs(preview)
	if _, err := s.CommitConfigImport(ctx, preview.ID, ids); !errors.Is(err, privacy.ErrLiteralBudget) {
		t.Fatal("超出词典容量必须明确拒绝", err)
	}
	all, _ := s.ListExactSecrets(ctx)
	if len(all) != 0 || s.SnapshotVersion() != before || sourceState(t, s, r.ID).ReviewCount != len(values) {
		t.Fatal("失败事务不能改变词典、快照或剩余数量")
	}
	if result, err := s.CommitConfigImport(ctx, preview.ID, ids[:1]); err != nil || result.Added != 1 || sourceState(t, s, r.ID).ReviewCount != len(values)-1 {
		t.Fatal("减少选择后重试应生效并更新剩余数量", result, err)
	}
}

func TestPrivacyConfigSourceReviewRefreshDoesNotHideRestoredContent(t *testing.T) {
	s := newConfigSourceService(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), ".env")
	writeSourceFile(t, path, "USERNAME=original-review-user")
	r := addSource(t, s, path, "file")
	if err := s.SyncConfigSources(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	writeSourceFile(t, path, "USERNAME=changed-review-user\nHOST=changed-review.example.invalid")
	preview, err := s.PreviewConfigSourceImport(ctx, r.ID)
	if err != nil || sourceState(t, s, r.ID).ReviewCount != 2 {
		t.Fatal("预览应刷新最新剩余数量", err)
	}
	s.CancelConfigImport(preview.ID)
	writeSourceFile(t, path, "USERNAME=original-review-user")
	if err := s.SyncConfigSources(ctx, r.ID); err != nil || sourceState(t, s, r.ID).ReviewCount != 1 {
		t.Fatal("恢复原内容后不能因摘要相同而保留错误的待确认数量", err)
	}
	manual, err := s.PreviewConfigImport(ctx, []configimport.File{{Name: ".env", Content: "USERNAME=original-review-user"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CommitConfigImport(ctx, manual.ID, reviewCandidateIDs(manual)); err != nil || sourceState(t, s, r.ID).ReviewCount != 0 {
		t.Fatal("从手动导入收录同值，也必须更新来源的剩余数量", err)
	}
}

func TestPrivacyConfigSourceReviewSourceBudgets(t *testing.T) {
	for _, budget := range []string{"total-bytes", "candidate-count", "dictionary-bytes"} {
		t.Run(budget, func(t *testing.T) {
			s := newConfigSourceService(t)
			ctx := context.Background()
			dir := t.TempDir()
			r := addSource(t, s, dir, "directory")
			switch budget {
			case "total-bytes":
				for i := 0; i < 5; i++ {
					writeSourceFile(t, filepath.Join(dir, fmt.Sprintf(".env.%d", i)), "#"+strings.Repeat("x", configimport.MaxFileBytes-1))
				}
				if _, err := s.PreviewConfigSourceImport(ctx, r.ID); err == nil || !strings.Contains(err.Error(), "8 MiB") {
					t.Fatal("来源预览必须保留合计大小限制", err)
				}
			case "candidate-count":
				var body strings.Builder
				for i := 0; i <= configimport.MaxValues; i++ {
					fmt.Fprintf(&body, "FIELD_%d_USERNAME=synthetic-user-%d\n", i, i)
				}
				writeSourceFile(t, filepath.Join(dir, ".env"), body.String())
				if _, err := s.PreviewConfigSourceImport(ctx, r.ID); err == nil || !strings.Contains(err.Error(), "候选") {
					t.Fatal("来源预览必须保留候选数量限制", err)
				}
			case "dictionary-bytes":
				for i := 0; i < 3; i++ {
					var body strings.Builder
					for j := 0; j < 30; j++ {
						fmt.Fprintf(&body, "FIELD_%d_USERNAME=%d_%d_%s\n", j, i, j, strings.Repeat("q", 50000))
					}
					writeSourceFile(t, filepath.Join(dir, fmt.Sprintf(".env.%d", i)), body.String())
				}
				preview, err := s.PreviewConfigSourceImport(ctx, r.ID)
				if err != nil || len(reviewCandidateIDs(preview)) != 90 {
					t.Fatal("应允许预览预算内文件", err)
				}
				t.Cleanup(func() { s.CancelConfigImport(preview.ID) })
				if _, err := s.CommitConfigImport(ctx, preview.ID, reviewCandidateIDs(preview)); !errors.Is(err, errPrivacyConfigDictionaryLimit) {
					t.Fatal("确认时必须保留词典原文总容量限制", err)
				}
				if sourceState(t, s, r.ID).ReviewCount != 90 {
					t.Fatal("超限确认不能减少剩余数量")
				}
			}
			if all, err := s.ListExactSecrets(ctx); err != nil || len(all) != 0 {
				t.Fatal("超限不能部分导入", err)
			}
		})
	}
}
