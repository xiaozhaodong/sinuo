// endpoint_runtime_state_test.go - 端点运行态 revision 乱序保护测试（§14.4）
// 2026-08-02 19:08:47

package store

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func createRuntimeStateTestStore(t *testing.T) (*SQLiteEndpointRuntimeStateStore, *sql.DB, func()) {
	t.Helper()
	db := createTestDB(t)
	return NewSQLiteEndpointRuntimeStateStore(db), db, func() {}
}

func runtimeStateRow(t *testing.T, db *sql.DB, endpointID int64, scope string) (state string, cooldownUntil sql.NullString, revision int64) {
	t.Helper()
	err := db.QueryRow(
		`SELECT state, cooldown_until, revision FROM endpoint_runtime_states WHERE endpoint_id = ? AND scope = ?`,
		endpointID, scope,
	).Scan(&state, &cooldownUntil, &revision)
	if err != nil {
		t.Fatalf("查询运行态失败: %v", err)
	}
	return state, cooldownUntil, revision
}

func cooldownRecord(endpointID int64, scope string, until time.Time, reason string, revision int64) *EndpointRuntimeStateRecord {
	return &EndpointRuntimeStateRecord{
		EndpointID:     endpointID,
		Scope:          scope,
		State:          "cooldown",
		CooldownUntil:  &until,
		CooldownReason: reason,
		Revision:       revision,
	}
}

// TestUpsertStaleCooldownDoesNotResurrectTombstone
// 场景：clear tombstone(rev2) 先落库，延迟的旧 persist(rev1) 后执行——冷却不得复活
func TestUpsertStaleCooldownDoesNotResurrectTombstone(t *testing.T) {
	s, db, cleanup := createRuntimeStateTestStore(t)
	defer cleanup()
	ctx := context.Background()

	// tombstone：state=active、until=NULL、rev2
	if err := s.Upsert(ctx, &EndpointRuntimeStateRecord{
		EndpointID: 1, Scope: EndpointRuntimeScopeMessages, State: "active", Revision: 2,
	}); err != nil {
		t.Fatalf("写入 tombstone 失败: %v", err)
	}

	// 晚执行的旧 persist：rev1，必须被丢弃
	until := time.Now().Add(10 * time.Minute)
	if err := s.Upsert(ctx, cooldownRecord(1, EndpointRuntimeScopeMessages, until, "soft_failure_server_error", 1)); err != nil {
		t.Fatalf("旧 persist Upsert 报错: %v", err)
	}

	state, cooldownUntil, revision := runtimeStateRow(t, db, 1, EndpointRuntimeScopeMessages)
	if state != "active" || cooldownUntil.Valid || revision != 2 {
		t.Fatalf("tombstone 被旧 persist 覆盖: state=%s until=%v revision=%d", state, cooldownUntil, revision)
	}

	// 启动恢复口径：tombstone 不应出现在活跃冷却里
	records, err := s.ListActiveCooldowns(ctx, time.Now())
	if err != nil {
		t.Fatalf("ListActiveCooldowns 失败: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("tombstone 不应被列为活跃冷却，实际返回 %d 条", len(records))
	}
}

// TestUpsertStaleTombstoneDoesNotClearNewCooldown
// 场景：新 persist(rev2) 先落库，延迟的旧 clear tombstone(rev1) 后执行——新冷却必须保留
func TestUpsertStaleTombstoneDoesNotClearNewCooldown(t *testing.T) {
	s, db, cleanup := createRuntimeStateTestStore(t)
	defer cleanup()
	ctx := context.Background()

	until := time.Now().Add(30 * time.Minute)
	if err := s.Upsert(ctx, cooldownRecord(1, EndpointRuntimeScopeGlobal, until, "auth_rejected", 2)); err != nil {
		t.Fatalf("写入新 cooldown 失败: %v", err)
	}

	// 晚执行的旧 clear：rev1 tombstone，必须被丢弃
	if err := s.Upsert(ctx, &EndpointRuntimeStateRecord{
		EndpointID: 1, Scope: EndpointRuntimeScopeGlobal, State: "active", Revision: 1,
	}); err != nil {
		t.Fatalf("旧 tombstone Upsert 报错: %v", err)
	}

	state, cooldownUntil, revision := runtimeStateRow(t, db, 1, EndpointRuntimeScopeGlobal)
	if state != "cooldown" || !cooldownUntil.Valid || revision != 2 {
		t.Fatalf("新 cooldown 被旧 tombstone 清除: state=%s until=%v revision=%d", state, cooldownUntil, revision)
	}

	records, err := s.ListActiveCooldowns(ctx, time.Now())
	if err != nil {
		t.Fatalf("ListActiveCooldowns 失败: %v", err)
	}
	if len(records) != 1 || records[0].CooldownReason != "auth_rejected" {
		t.Fatalf("新 cooldown 应保留为活跃冷却，实际: %+v", records)
	}
}

// TestMaxRevision 播种口径：空表为 0，有数据取最大值
func TestMaxRevision(t *testing.T) {
	s, _, cleanup := createRuntimeStateTestStore(t)
	defer cleanup()
	ctx := context.Background()

	max, err := s.MaxRevision(ctx)
	if err != nil || max != 0 {
		t.Fatalf("空表 MaxRevision 应为 0: max=%d err=%v", max, err)
	}

	until := time.Now().Add(time.Minute)
	if err := s.Upsert(ctx, cooldownRecord(1, EndpointRuntimeScopeMessages, until, "soft_failure_connection", 7)); err != nil {
		t.Fatalf("Upsert 失败: %v", err)
	}
	if err := s.Upsert(ctx, cooldownRecord(2, EndpointRuntimeScopeGlobal, until, "auth_rejected", 42)); err != nil {
		t.Fatalf("Upsert 失败: %v", err)
	}

	max, err = s.MaxRevision(ctx)
	if err != nil || max != 42 {
		t.Fatalf("MaxRevision 应为 42: max=%d err=%v", max, err)
	}
}

// TestClearCooldownTombstonesWritesBothScopes
// 场景:双槽冷却预置后,ClearCooldownTombstones 一次写入 global + messages 两行 tombstone
func TestClearCooldownTombstonesWritesBothScopes(t *testing.T) {
	s, db, cleanup := createRuntimeStateTestStore(t)
	defer cleanup()
	ctx := context.Background()

	until := time.Now().Add(30 * time.Minute)
	if err := s.Upsert(ctx, cooldownRecord(1, EndpointRuntimeScopeGlobal, until, "auth_rejected", 1)); err != nil {
		t.Fatalf("预置 global 冷却失败: %v", err)
	}
	if err := s.Upsert(ctx, cooldownRecord(1, EndpointRuntimeScopeMessages, until, "soft_failure_server_error", 2)); err != nil {
		t.Fatalf("预置 messages 冷却失败: %v", err)
	}

	if err := s.ClearCooldownTombstones(ctx, 1, 10); err != nil {
		t.Fatalf("ClearCooldownTombstones 失败: %v", err)
	}

	for _, scope := range []string{EndpointRuntimeScopeGlobal, EndpointRuntimeScopeMessages} {
		state, cooldownUntil, revision := runtimeStateRow(t, db, 1, scope)
		if state != "active" || cooldownUntil.Valid || revision != 10 {
			t.Fatalf("scope=%s 应为 tombstone: state=%s until=%v revision=%d", scope, state, cooldownUntil, revision)
		}
	}

	// tombstone 不出现在活跃冷却恢复口径中
	records, err := s.ListActiveCooldowns(ctx, time.Now())
	if err != nil {
		t.Fatalf("ListActiveCooldowns 失败: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("tombstone 不应被列为活跃冷却,实际返回 %d 条", len(records))
	}
}

// TestClearCooldownTombstonesRevisionGuard
// 场景:tombstone(R2) 落库后,旧 persist(R1) 被丢弃;新冷却(R3) 正常覆盖
func TestClearCooldownTombstonesRevisionGuard(t *testing.T) {
	s, db, cleanup := createRuntimeStateTestStore(t)
	defer cleanup()
	ctx := context.Background()

	if err := s.ClearCooldownTombstones(ctx, 1, 2); err != nil {
		t.Fatalf("ClearCooldownTombstones 失败: %v", err)
	}

	// 晚执行的旧 persist:rev1,必须被丢弃
	until := time.Now().Add(10 * time.Minute)
	if err := s.Upsert(ctx, cooldownRecord(1, EndpointRuntimeScopeMessages, until, "soft_failure_server_error", 1)); err != nil {
		t.Fatalf("旧 persist Upsert 报错: %v", err)
	}
	state, cooldownUntil, revision := runtimeStateRow(t, db, 1, EndpointRuntimeScopeMessages)
	if state != "active" || cooldownUntil.Valid || revision != 2 {
		t.Fatalf("tombstone 被旧 persist 覆盖: state=%s until=%v revision=%d", state, cooldownUntil, revision)
	}

	// 更新的冷却(rev3)正常覆盖 tombstone
	if err := s.Upsert(ctx, cooldownRecord(1, EndpointRuntimeScopeMessages, until, "soft_failure_server_error", 3)); err != nil {
		t.Fatalf("新 persist Upsert 报错: %v", err)
	}
	state, cooldownUntil, revision = runtimeStateRow(t, db, 1, EndpointRuntimeScopeMessages)
	if state != "cooldown" || !cooldownUntil.Valid || revision != 3 {
		t.Fatalf("新冷却应覆盖 tombstone: state=%s until=%v revision=%d", state, cooldownUntil, revision)
	}
}

// TestClearCooldownTombstonesClosedDB 前置错误路径:关闭的 DB 必须返回错误
func TestClearCooldownTombstonesClosedDB(t *testing.T) {
	s, db, cleanup := createRuntimeStateTestStore(t)
	defer cleanup()

	if err := db.Close(); err != nil {
		t.Fatalf("关闭 DB 失败: %v", err)
	}
	if err := s.ClearCooldownTombstones(context.Background(), 1, 10); err == nil {
		t.Fatal("对已关闭的 DB 调用应返回错误")
	}
}

// TestClearCooldownTombstonesCancelledContext 前置错误路径:已取消 ctx 必须返回错误
func TestClearCooldownTombstonesCancelledContext(t *testing.T) {
	s, _, cleanup := createRuntimeStateTestStore(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.ClearCooldownTombstones(ctx, 1, 10); err == nil {
		t.Fatal("对已取消 ctx 调用应返回错误")
	}
}

// TestClearCooldownTombstonesRollbackOnSecondScopeFailure
// 场景:trigger 注入让 messages 行(写入顺序上的第二条)写入失败,
// 整个事务回滚——global 行必须保持原冷却记录,不得出现单 scope 部分成功。
// 依赖实现的 global → messages 固定写入顺序。
func TestClearCooldownTombstonesRollbackOnSecondScopeFailure(t *testing.T) {
	s, db, cleanup := createRuntimeStateTestStore(t)
	defer cleanup()
	ctx := context.Background()

	until := time.Now().Add(30 * time.Minute)
	if err := s.Upsert(ctx, cooldownRecord(1, EndpointRuntimeScopeGlobal, until, "auth_rejected", 1)); err != nil {
		t.Fatalf("预置 global 冷却失败: %v", err)
	}
	if err := s.Upsert(ctx, cooldownRecord(1, EndpointRuntimeScopeMessages, until, "soft_failure_server_error", 2)); err != nil {
		t.Fatalf("预置 messages 冷却失败: %v", err)
	}

	// 注入:第二条 upsert(messages scope 转为 active 时)中止
	if _, err := db.Exec(`
		CREATE TRIGGER fail_messages_tombstone
		BEFORE UPDATE ON endpoint_runtime_states
		WHEN NEW.scope = 'messages' AND NEW.state = 'active'
		BEGIN SELECT RAISE(ABORT, 'injected'); END;
	`); err != nil {
		t.Fatalf("创建注入 trigger 失败: %v", err)
	}

	if err := s.ClearCooldownTombstones(ctx, 1, 10); err == nil {
		t.Fatal("注入失败场景应返回错误")
	}

	// 回滚断言:global 行(第一条已执行的 upsert)仍为原冷却记录
	state, cooldownUntil, revision := runtimeStateRow(t, db, 1, EndpointRuntimeScopeGlobal)
	if state != "cooldown" || !cooldownUntil.Valid || revision != 1 {
		t.Fatalf("global 行应回滚为原冷却记录: state=%s until=%v revision=%d", state, cooldownUntil, revision)
	}
	state, cooldownUntil, revision = runtimeStateRow(t, db, 1, EndpointRuntimeScopeMessages)
	if state != "cooldown" || !cooldownUntil.Valid || revision != 2 {
		t.Fatalf("messages 行应保持原冷却记录: state=%s until=%v revision=%d", state, cooldownUntil, revision)
	}

	// 解除注入后重试成功
	if _, err := db.Exec(`DROP TRIGGER fail_messages_tombstone`); err != nil {
		t.Fatalf("删除注入 trigger 失败: %v", err)
	}
	if err := s.ClearCooldownTombstones(ctx, 1, 10); err != nil {
		t.Fatalf("解除注入后重试失败: %v", err)
	}
	for _, scope := range []string{EndpointRuntimeScopeGlobal, EndpointRuntimeScopeMessages} {
		state, cooldownUntil, revision := runtimeStateRow(t, db, 1, scope)
		if state != "active" || cooldownUntil.Valid || revision != 10 {
			t.Fatalf("重试后 scope=%s 应为 tombstone: state=%s until=%v revision=%d", scope, state, cooldownUntil, revision)
		}
	}
}

func TestPruneOrphansRemovesRowsOfDeletedEndpoints(t *testing.T) {
	st, db, cleanup := createRuntimeStateTestStore(t)
	defer cleanup()
	ctx := context.Background()
	live, err := NewSQLiteEndpointStore(db).Create(ctx, &EndpointRecord{Name: "live", URL: "https://live.example.com", Priority: 1})
	if err != nil {
		t.Fatal(err)
	}
	orphanID := live.ID + 100
	insertRuntimeState(t, db, live.ID, "global")
	insertRuntimeState(t, db, orphanID, "global")
	insertRuntimeState(t, db, orphanID, "messages")

	pruned, err := st.PruneOrphans(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pruned != 2 {
		t.Fatalf("pruned = %d, want 2", pruned)
	}
	if n := countRuntimeStates(t, db, live.ID); n != 1 {
		t.Fatalf("现存端点的运行态被误删，剩余 %d 行", n)
	}
	if again, err := st.PruneOrphans(ctx); err != nil || again != 0 {
		t.Fatalf("重复清理应无操作: pruned=%d err=%v", again, err)
	}
}
