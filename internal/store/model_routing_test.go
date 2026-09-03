package store

import (
	"context"
	"database/sql"
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
	settings.ClaudeEnabled = true
	if err := st.UpdateSettings(ctx, settings); err != nil {
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
