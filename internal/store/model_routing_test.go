package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func openModelRoutingTestStore(t *testing.T) (*SQLiteModelRoutingStore, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "model-routing.db")+"?_foreign_keys=1")
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return NewSQLiteModelRoutingStore(db), db
}

func TestSQLiteModelRoutingStore_CRUD(t *testing.T) {
	st, _ := openModelRoutingTestStore(t)
	ctx := context.Background()
	settings, err := st.GetSettings(ctx)
	if err != nil {
		t.Fatalf("get default settings failed: %v", err)
	}
	if settings.ID != 1 || settings.ClaudeEnabled {
		t.Fatalf("unexpected default settings: %#v", settings)
	}
	if err := st.UpdateSettings(ctx, ModelRoutingSettingsPatch{ClaudeEnabled: boolPtr(true)}); err != nil {
		t.Fatalf("update settings failed: %v", err)
	}
	updated, err := st.GetSettings(ctx)
	if err != nil || !updated.ClaudeEnabled {
		t.Fatalf("settings update not persisted: %#v, %v", updated, err)
	}

	group, err := st.CreateGroup(ctx, &ModelUpstreamGroupRecord{
		Name: "primary", MembersJSON: `["endpoint-a"]`, RouteMode: "auto",
	})
	if err != nil {
		t.Fatalf("create group failed: %v", err)
	}
	if group.ID <= 0 || group.Family != "claude" {
		t.Fatalf("unexpected group: %#v", group)
	}
	group.RouteMode = "manual_fixed"
	group.RouteTarget = "endpoint-a"
	group.RouteRevision = 1
	if err := st.UpdateGroup(ctx, group); err != nil {
		t.Fatalf("update group failed: %v", err)
	}
	gotGroup, err := st.GetGroup(ctx, group.ID)
	if err != nil || gotGroup.RouteRevision != 1 || gotGroup.RouteTarget != "endpoint-a" {
		t.Fatalf("group update not persisted: %#v, %v", gotGroup, err)
	}

	assignment, err := st.CreateAssignment(ctx, &ModelRoutingAssignmentRecord{
		ModelName: "claude-sonnet", GroupID: group.ID, Enabled: true, Note: "primary",
	})
	if err != nil {
		t.Fatalf("create assignment failed: %v", err)
	}
	if assignment.ID <= 0 || assignment.MatchType != "exact" || assignment.Family != "claude" {
		t.Fatalf("unexpected assignment: %#v", assignment)
	}
	if _, err := st.CreateAssignment(ctx, &ModelRoutingAssignmentRecord{
		ModelName: "Claude-Sonnet", GroupID: group.ID, Enabled: true,
	}); err == nil {
		t.Fatal("case-insensitive duplicate assignment should fail")
	}
	assignment.Enabled = false
	if err := st.UpdateAssignment(ctx, assignment); err != nil {
		t.Fatalf("update assignment failed: %v", err)
	}
	if got, err := st.GetAssignment(ctx, assignment.ID); err != nil || got.Enabled {
		t.Fatalf("assignment update not persisted: %#v, %v", got, err)
	}
	if err := st.DeleteAssignment(ctx, assignment.ID); err != nil {
		t.Fatalf("delete assignment failed: %v", err)
	}
	if err := st.DeleteGroup(ctx, group.ID); err != nil {
		t.Fatalf("delete group failed: %v", err)
	}
}

func TestSQLiteModelRoutingStore_SchemaIsIdempotent(t *testing.T) {
	st, db := openModelRoutingTestStore(t)
	ctx := context.Background()
	if _, err := st.GetSettings(ctx); err != nil {
		t.Fatalf("first schema init failed: %v", err)
	}
	if _, err := st.GetSettings(ctx); err != nil {
		t.Fatalf("second schema init failed: %v", err)
	}
	for _, table := range []string{"model_routing_settings", "model_upstream_groups", "model_routing_assignments"} {
		var name string
		if err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name); err != nil {
			t.Fatalf("table %s missing: %v", table, err)
		}
	}
}

func TestSQLiteModelRoutingStore_RetryAfterSchemaInitFailure(t *testing.T) {
	st, db := openModelRoutingTestStore(t)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := st.GetSettings(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("首次初始化应返回取消错误: %v", err)
	}
	ctx := context.Background()
	if err := st.UpdateSettings(ctx, ModelRoutingSettingsPatch{ClaudeEnabled: boolPtr(true), CodexEnabled: boolPtr(true)}); err != nil {
		t.Fatalf("临时错误消失后应允许重新保存: %v", err)
	}
	settings, err := NewSQLiteModelRoutingStore(db).GetSettings(ctx)
	if err != nil || settings == nil || !settings.ClaudeEnabled || !settings.CodexEnabled {
		t.Fatalf("独立读取未确认设置已保存: settings=%+v err=%v", settings, err)
	}
}

func TestSQLiteModelRoutingStore_SaveRestoresMissingSettingsRow(t *testing.T) {
	st, db := openModelRoutingTestStore(t)
	ctx := context.Background()
	if _, err := st.GetSettings(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM model_routing_settings WHERE id = ?`, 1); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateSettings(ctx, ModelRoutingSettingsPatch{ClaudeEnabled: boolPtr(true), CodexEnabled: boolPtr(true)}); err != nil {
		t.Fatalf("设置行缺失时重新保存失败: %v", err)
	}
	settings, err := st.GetSettings(ctx)
	if err != nil || settings == nil || !settings.ClaudeEnabled || !settings.CodexEnabled {
		t.Fatalf("保存成功却没有恢复设置行: settings=%+v err=%v", settings, err)
	}
}

// TestSQLiteModelRoutingStore_PartialPatchKeepsOtherColumn 锁定列级写语义：
// 只提交 Claude 开关时，并发/既有的 Codex 开关不能被整行写清掉。
func TestSQLiteModelRoutingStore_PartialPatchKeepsOtherColumn(t *testing.T) {
	st, _ := openModelRoutingTestStore(t)
	ctx := context.Background()
	if err := st.UpdateSettings(ctx, ModelRoutingSettingsPatch{ClaudeEnabled: boolPtr(true), CodexEnabled: boolPtr(true)}); err != nil {
		t.Fatalf("开启两个开关失败: %v", err)
	}
	if err := st.UpdateSettings(ctx, ModelRoutingSettingsPatch{ClaudeEnabled: boolPtr(false)}); err != nil {
		t.Fatalf("只关闭 Claude 失败: %v", err)
	}
	settings, err := st.GetSettings(ctx)
	if err != nil || settings == nil {
		t.Fatalf("读取设置失败: %+v, %v", settings, err)
	}
	if settings.ClaudeEnabled || !settings.CodexEnabled {
		t.Fatalf("只写 Claude 列不应影响 Codex 列: %+v", settings)
	}
	if err := st.UpdateSettings(ctx, ModelRoutingSettingsPatch{}); err != nil {
		t.Fatalf("空 patch 应是无操作: %v", err)
	}
	unchanged, err := st.GetSettings(ctx)
	if err != nil || unchanged == nil || !unchanged.CodexEnabled {
		t.Fatalf("空 patch 不应改写设置: %+v, %v", unchanged, err)
	}
}
