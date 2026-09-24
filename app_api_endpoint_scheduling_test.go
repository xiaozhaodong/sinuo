// app_api_endpoint_scheduling_test.go - 手动解除冷却 API(ClearEndpointCooldown)
// 与 pending-retry 闭环测试。fixture 装配真实 SQLite(含 endpoint_runtime_states 表),
// 使断言真正穿透到 DB。
// 2026-08-05

package main

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cc-forwarder/config"
	"cc-forwarder/internal/endpoint"
	"cc-forwarder/internal/service"
	"cc-forwarder/internal/store"
)

// newEndpointCooldownAPITestApp 装配完整 App fixture:
// endpoints + endpoint_runtime_states 两表、runtime store、persist hook,
// 并创建一个名为 "cooldown-ep" 的端点(DB 与内存同步)
func newEndpointCooldownAPITestApp(t *testing.T) (*App, *store.SQLiteEndpointRuntimeStateStore, *sql.DB, func()) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "endpoint_cooldown_api_test_*")
	if err != nil {
		t.Fatalf("create temp dir failed: %v", err)
	}

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_synchronous=NORMAL")
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		t.Fatalf("open sqlite failed: %v", err)
	}
	// 与生产 adapter 一致:单连接串行化写,避免并发写锁竞争(SQLITE_BUSY)
	db.SetMaxOpenConns(1)

	schema := `
		CREATE TABLE IF NOT EXISTS endpoints (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT UNIQUE NOT NULL CHECK (length(trim(name)) > 0),
			url TEXT NOT NULL CHECK (length(trim(url)) > 0),
			token TEXT,
			api_key TEXT,
			headers TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(headers) AND json_type(headers) = 'object'),
			auth_mode TEXT NOT NULL DEFAULT 'configured',
			priority INTEGER NOT NULL DEFAULT 1 CHECK (priority >= 0),
			failover_enabled INTEGER NOT NULL DEFAULT 1 CHECK (failover_enabled IN (0, 1)),
			availability_enabled INTEGER NOT NULL DEFAULT 1 CHECK (availability_enabled IN (0, 1)),
			cooldown_seconds INTEGER,
			timeout_seconds INTEGER NOT NULL DEFAULT 300 CHECK (timeout_seconds > 0),
			supports_count_tokens INTEGER NOT NULL DEFAULT 0 CHECK (supports_count_tokens IN (0, 1)),
			model_rewrite_rules TEXT NOT NULL DEFAULT '',
			soft_failure_window_minutes INTEGER,
			soft_failure_threshold INTEGER,
			cost_multiplier REAL NOT NULL DEFAULT 1.0 CHECK (cost_multiplier > 0),
			input_cost_multiplier REAL NOT NULL DEFAULT 1.0 CHECK (input_cost_multiplier > 0),
			output_cost_multiplier REAL NOT NULL DEFAULT 1.0 CHECK (output_cost_multiplier > 0),
			cache_creation_cost_multiplier REAL NOT NULL DEFAULT 1.0 CHECK (cache_creation_cost_multiplier > 0),
			cache_creation_cost_multiplier_1h REAL NOT NULL DEFAULT 1.0 CHECK (cache_creation_cost_multiplier_1h > 0),
			cache_read_cost_multiplier REAL NOT NULL DEFAULT 1.0 CHECK (cache_read_cost_multiplier > 0),
			created_at DATETIME DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now', 'localtime') || '+08:00'),
			updated_at DATETIME DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now', 'localtime') || '+08:00')
		);
		CREATE INDEX IF NOT EXISTS idx_endpoints_priority ON endpoints(priority);
		CREATE INDEX IF NOT EXISTS idx_endpoints_failover ON endpoints(failover_enabled);
		CREATE INDEX IF NOT EXISTS idx_endpoints_availability ON endpoints(availability_enabled);
		CREATE TABLE IF NOT EXISTS endpoint_runtime_states (
			endpoint_id INTEGER NOT NULL,
			scope TEXT NOT NULL CHECK (scope IN ('global', 'messages', 'count_tokens')),
			state TEXT NOT NULL DEFAULT 'active',
			cooldown_until DATETIME,
			cooldown_reason TEXT NOT NULL DEFAULT '',
			revision INTEGER NOT NULL DEFAULT 0,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (endpoint_id, scope)
		);
	`
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		_ = os.RemoveAll(tmpDir)
		t.Fatalf("create schema failed: %v", err)
	}

	cfg := &config.Config{
		Health: config.HealthConfig{Timeout: 5 * time.Second},
		FailureTracker: config.FailureTrackerConfig{
			Enabled: true, TimeWindow: 5 * time.Minute, Threshold: 3, Action: "failover",
		},
		Endpoints: []config.EndpointConfig{},
	}

	manager := endpoint.NewManager(cfg)
	st := store.NewSQLiteEndpointStore(db)
	svc := service.NewEndpointService(st, manager)
	runtimeStore := store.NewSQLiteEndpointRuntimeStateStore(db)

	app := NewApp()
	app.config = cfg
	app.endpointManager = manager
	app.endpointStore = st
	app.endpointService = svc
	app.endpointRuntimeStateStore = runtimeStore
	app.logger = slog.New(slog.NewTextHandler(io.Discard, nil))

	// persist hook 装配(与生产 setupEndpointRuntimeStates 一致)
	manager.SetCooldownPersistHook(func(name string, until time.Time, reason string, revision int64) {
		record, err := st.Get(context.Background(), name)
		if err != nil || record == nil {
			return
		}
		scope := endpoint.CooldownScopeForReason(reason)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		untilCopy := until
		_ = runtimeStore.Upsert(ctx, &store.EndpointRuntimeStateRecord{
			EndpointID:     record.ID,
			Scope:          scope,
			State:          "cooldown",
			CooldownUntil:  &untilCopy,
			CooldownReason: reason,
			Revision:       revision,
		})
	})

	// 创建端点(DB 与内存同步)
	if err := app.CreateEndpointRecord(CreateEndpointInput{
		Name: "cooldown-ep", URL: "https://test.example.com", Priority: 1, TimeoutSeconds: 30,
	}); err != nil {
		manager.Stop()
		_ = db.Close()
		_ = os.RemoveAll(tmpDir)
		t.Fatalf("create endpoint failed: %v", err)
	}

	cleanup := func() {
		manager.Stop()
		_ = db.Close()
		_ = os.RemoveAll(tmpDir)
	}
	return app, runtimeStore, db, cleanup
}

// seedCooldown 预置双槽冷却:内存(SetEndpointCooldown)+ DB 两行(cooldown 记录)
func seedCooldown(t *testing.T, app *App, runtimeStore *store.SQLiteEndpointRuntimeStateStore, name string) int64 {
	t.Helper()
	record, err := app.endpointStore.Get(context.Background(), name)
	if err != nil || record == nil {
		t.Fatalf("读取端点失败: err=%v record=%v", err, record)
	}
	until := time.Now().Add(30 * time.Minute)
	// DB 两行(global + messages)
	if err := runtimeStore.Upsert(context.Background(), &store.EndpointRuntimeStateRecord{
		EndpointID: record.ID, Scope: store.EndpointRuntimeScopeGlobal, State: "cooldown",
		CooldownUntil: &until, CooldownReason: "auth_rejected", Revision: 1,
	}); err != nil {
		t.Fatalf("预置 global 冷却失败: %v", err)
	}
	if err := runtimeStore.Upsert(context.Background(), &store.EndpointRuntimeStateRecord{
		EndpointID: record.ID, Scope: store.EndpointRuntimeScopeMessages, State: "cooldown",
		CooldownUntil: &until, CooldownReason: "soft_failure_server_error", Revision: 2,
	}); err != nil {
		t.Fatalf("预置 messages 冷却失败: %v", err)
	}
	// 内存:auth_rejected → global 槽
	app.endpointManager.SetEndpointCooldown(name, 30*time.Minute, "auth_rejected")
	return record.ID
}

func requireRuntimeStateRow(t *testing.T, db *sql.DB, endpointID int64, scope string) (state string, cooldownUntil sql.NullString) {
	t.Helper()
	err := db.QueryRow(
		`SELECT state, cooldown_until FROM endpoint_runtime_states WHERE endpoint_id = ? AND scope = ?`,
		endpointID, scope,
	).Scan(&state, &cooldownUntil)
	if err != nil {
		t.Fatalf("查询运行态失败: %v", err)
	}
	return state, cooldownUntil
}

// TestClearEndpointCooldown_SuccessPersistsTombstones 成功路径持久化闭环:
// 预置双槽冷却 → ClearEndpointCooldown 返回 nil → DB 两行 tombstone、
// GetEndpointRecords 中 in_cooldown==false 且 cooldown_persist_pending==false
func TestClearEndpointCooldown_SuccessPersistsTombstones(t *testing.T) {
	app, runtimeStore, db, cleanup := newEndpointCooldownAPITestApp(t)
	defer cleanup()
	id := seedCooldown(t, app, runtimeStore, "cooldown-ep")

	if !app.endpointManager.IsEndpointInCooldown("cooldown-ep") {
		t.Fatal("预置后端点应处于冷却")
	}

	if err := app.ClearEndpointCooldown("cooldown-ep"); err != nil {
		t.Fatalf("ClearEndpointCooldown 失败: %v", err)
	}

	// DB 两行 tombstone
	for _, scope := range []string{store.EndpointRuntimeScopeGlobal, store.EndpointRuntimeScopeMessages} {
		state, cooldownUntil := requireRuntimeStateRow(t, db, id, scope)
		if state != "active" || cooldownUntil.Valid {
			t.Fatalf("scope=%s 应为 tombstone: state=%s until=%v", scope, state, cooldownUntil)
		}
	}
	// ListActiveCooldowns 不再返回
	records, err := runtimeStore.ListActiveCooldowns(context.Background(), time.Now())
	if err != nil || len(records) != 0 {
		t.Fatalf("tombstone 后不应有活跃冷却: len=%d err=%v", len(records), err)
	}
	// 前端口径
	if app.endpointManager.IsEndpointInCooldown("cooldown-ep") {
		t.Fatal("Clear 后内存不应有冷却")
	}
	infos, err := app.GetEndpointRecords()
	if err != nil {
		t.Fatalf("GetEndpointRecords 失败: %v", err)
	}
	var info *EndpointRecordInfo
	for i := range infos {
		if infos[i].Name == "cooldown-ep" {
			info = &infos[i]
			break
		}
	}
	if info == nil {
		t.Fatal("GetEndpointRecords 未返回 cooldown-ep")
	}
	if info.InCooldown || info.CooldownPersistPending {
		t.Fatalf("清除后 in_cooldown=%v cooldown_persist_pending=%v 应均为 false", info.InCooldown, info.CooldownPersistPending)
	}
}

// TestClearEndpointCooldown_RestartDoesNotRestore 重启恢复模拟:
// 预置冷却并落库 → 清除 → 重建 manager 并重跑恢复流程(ListActiveCooldowns +
// RestoreEndpointCooldown)→ 端点无冷却("再次重启不恢复")
func TestClearEndpointCooldown_RestartDoesNotRestore(t *testing.T) {
	app, runtimeStore, _, cleanup := newEndpointCooldownAPITestApp(t)
	defer cleanup()
	seedCooldown(t, app, runtimeStore, "cooldown-ep")

	if err := app.ClearEndpointCooldown("cooldown-ep"); err != nil {
		t.Fatalf("ClearEndpointCooldown 失败: %v", err)
	}

	// 模拟重启:重建 manager 并重跑恢复流程
	cfg := &config.Config{
		Health: config.HealthConfig{Timeout: 5 * time.Second},
		Endpoints: []config.EndpointConfig{
			{Name: "cooldown-ep", URL: "https://test.example.com", Priority: 1, Timeout: 30 * time.Second},
		},
	}
	restarted := endpoint.NewManager(cfg)
	defer restarted.Stop()

	records, err := runtimeStore.ListActiveCooldowns(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("ListActiveCooldowns 失败: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("清除后重启不应恢复冷却, records=%+v", records)
	}
	for _, record := range records {
		restarted.RestoreEndpointCooldown("cooldown-ep", record.Scope, *record.CooldownUntil, record.CooldownReason)
	}
	if restarted.IsEndpointInCooldown("cooldown-ep") {
		t.Fatal("重启后端点不应有冷却(证明再次重启不恢复)")
	}
}

// TestClearEndpointCooldown_PersistFailureSetsPending 落库失败 → pending 置位:
// trigger 注入失败 → API 返回错误、文案含"重启后可能恢复";GetEndpointRecords 中
// cooldown_persist_pending==true
func TestClearEndpointCooldown_PersistFailureSetsPending(t *testing.T) {
	app, runtimeStore, db, cleanup := newEndpointCooldownAPITestApp(t)
	defer cleanup()
	id := seedCooldown(t, app, runtimeStore, "cooldown-ep")

	if _, err := db.Exec(`
		CREATE TRIGGER fail_clear_tombstone
		BEFORE UPDATE ON endpoint_runtime_states
		WHEN NEW.scope = 'messages' AND NEW.state = 'active'
		BEGIN SELECT RAISE(ABORT, 'injected'); END;
	`); err != nil {
		t.Fatalf("创建注入 trigger 失败: %v", err)
	}

	err := app.ClearEndpointCooldown("cooldown-ep")
	if err == nil || !strings.Contains(err.Error(), "重启后可能恢复") {
		t.Fatalf("落库失败应返回含重试提示的错误, got=%v", err)
	}

	// 内存冷却已清(Reset 已执行),但 pending 置位
	if app.endpointManager.IsEndpointInCooldown("cooldown-ep") {
		t.Fatal("落库失败后内存冷却应已清除")
	}
	infos, err := app.GetEndpointRecords()
	if err != nil {
		t.Fatalf("GetEndpointRecords 失败: %v", err)
	}
	var info *EndpointRecordInfo
	for i := range infos {
		if infos[i].Name == "cooldown-ep" {
			info = &infos[i]
			break
		}
	}
	if info == nil || !info.CooldownPersistPending {
		t.Fatal("落库失败后 cooldown_persist_pending 应为 true")
	}

	// 解除注入后重试:成功、两行 tombstone、pending 清除
	if _, err := db.Exec(`DROP TRIGGER fail_clear_tombstone`); err != nil {
		t.Fatalf("删除注入 trigger 失败: %v", err)
	}
	if err := app.ClearEndpointCooldown("cooldown-ep"); err != nil {
		t.Fatalf("重试 ClearEndpointCooldown 失败: %v", err)
	}
	for _, scope := range []string{store.EndpointRuntimeScopeGlobal, store.EndpointRuntimeScopeMessages} {
		state, cooldownUntil := requireRuntimeStateRow(t, db, id, scope)
		if state != "active" || cooldownUntil.Valid {
			t.Fatalf("重试后 scope=%s 应为 tombstone: state=%s until=%v", scope, state, cooldownUntil)
		}
	}
	infos, err = app.GetEndpointRecords()
	if err != nil {
		t.Fatalf("GetEndpointRecords 失败: %v", err)
	}
	for i := range infos {
		if infos[i].Name == "cooldown-ep" && infos[i].CooldownPersistPending {
			t.Fatal("重试成功后 pending 应清除")
		}
	}
}

// TestCooldownPendingRevisionGuard 乱序守卫(helper 直测):
// clear(rev=6) 后 mark(rev=5) 不得复活;正向 mark(5) → clear(6) 正常;
// 同 revision 后到更新允许生效
func TestCooldownPendingRevisionGuard(t *testing.T) {
	app := NewApp()

	// 乱序:clear 6 后低 revision mark 5 不得复活
	app.clearCooldownPersistPending("ep", 1, 6)
	app.markCooldownPersistPending("ep", 1, 5)
	if app.isCooldownPersistPending("ep", 1) {
		t.Fatal("低 revision mark 不得复活已清除的 pending")
	}

	// 正向:mark 5 → clear 6 正常清除
	app.markCooldownPersistPending("ep", 1, 5)
	app.clearCooldownPersistPending("ep", 1, 6)
	if app.isCooldownPersistPending("ep", 1) {
		t.Fatal("正向 mark→clear 应清除 pending")
	}

	// 同 revision 后到更新允许生效
	app.markCooldownPersistPending("ep", 1, 7)
	app.markCooldownPersistPending("ep", 1, 7)
	if !app.isCooldownPersistPending("ep", 1) {
		t.Fatal("同 revision mark 应生效")
	}
	app.clearCooldownPersistPending("ep", 1, 7)
	if app.isCooldownPersistPending("ep", 1) {
		t.Fatal("同 revision clear 应生效")
	}
	app.markCooldownPersistPending("ep", 1, 8)
	if !app.isCooldownPersistPending("ep", 1) {
		t.Fatal("mark 8 应生效")
	}
}

// TestCooldownPendingDeleteRecreateIsolation 删除重建不继承 pending:
// endpointID 不匹配 → false;DeleteEndpointRecord 成功后 pending 条目被移除
func TestCooldownPendingDeleteRecreateIsolation(t *testing.T) {
	app, runtimeStore, _, cleanup := newEndpointCooldownAPITestApp(t)
	defer cleanup()
	id := seedCooldown(t, app, runtimeStore, "cooldown-ep")

	// 模拟同名重建:新 ID 与 entry ID 不匹配
	app.markCooldownPersistPending("cooldown-ep", id, 5)
	if app.isCooldownPersistPending("cooldown-ep", id+100) {
		t.Fatal("endpointID 不匹配不应视为 pending(删除重建不继承)")
	}
	if !app.isCooldownPersistPending("cooldown-ep", id) {
		t.Fatal("endpointID 匹配时应为 pending")
	}

	// DeleteEndpointRecord 成功后 pending 条目被移除
	if err := app.DeleteEndpointRecord("cooldown-ep"); err != nil {
		t.Fatalf("DeleteEndpointRecord 失败: %v", err)
	}
	if app.isCooldownPersistPending("cooldown-ep", id) {
		t.Fatal("端点删除成功后 pending 应被清理")
	}
}

// TestClearEndpointCooldown_ErrorPaths 三类错误路径:
// 服务未初始化 / 端点不存在(顺带清理孤儿 pending)/ 读取失败
func TestClearEndpointCooldown_ErrorPaths(t *testing.T) {
	t.Run("服务未初始化", func(t *testing.T) {
		app := NewApp()
		err := app.ClearEndpointCooldown("ep")
		if err == nil || !strings.Contains(err.Error(), "未初始化") {
			t.Fatalf("未初始化应返回明确错误, got=%v", err)
		}
	})

	t.Run("端点不存在清理孤儿pending", func(t *testing.T) {
		app, _, _, cleanup := newEndpointCooldownAPITestApp(t)
		defer cleanup()
		app.markCooldownPersistPending("ghost", 42, 5)
		err := app.ClearEndpointCooldown("ghost")
		if err == nil || !strings.Contains(err.Error(), "不存在") {
			t.Fatalf("不存在端点应返回明确错误, got=%v", err)
		}
		if app.isCooldownPersistPending("ghost", 42) {
			t.Fatal("端点不存在时孤儿 pending 应被清理")
		}
	})

	t.Run("读取失败", func(t *testing.T) {
		app, _, db, cleanup := newEndpointCooldownAPITestApp(t)
		// 先关闭 DB 再调用(读取失败路径)
		_ = db.Close()
		cleanup()
		err := app.ClearEndpointCooldown("cooldown-ep")
		if err == nil || !strings.Contains(err.Error(), "读取端点") {
			t.Fatalf("读取失败应返回明确错误, got=%v", err)
		}
	})
}
