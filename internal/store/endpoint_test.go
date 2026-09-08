package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func createTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "endpoint-store.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`
		CREATE TABLE endpoints (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT UNIQUE NOT NULL,
			url TEXT NOT NULL,
			token TEXT NOT NULL DEFAULT '',
			api_key TEXT NOT NULL DEFAULT '',
			headers TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(headers) AND json_type(headers) = 'object'),
			priority INTEGER NOT NULL DEFAULT 1 CHECK(priority >= 0),
			failover_enabled INTEGER NOT NULL DEFAULT 1,
			cooldown_seconds INTEGER,
			timeout_seconds INTEGER NOT NULL DEFAULT 300,
			supports_count_tokens INTEGER NOT NULL DEFAULT 0,
			model_rewrite_rules TEXT NOT NULL DEFAULT '',
			soft_failure_window_minutes INTEGER,
			soft_failure_threshold INTEGER,
			cost_multiplier REAL NOT NULL DEFAULT 1.0,
			input_cost_multiplier REAL NOT NULL DEFAULT 1.0,
			output_cost_multiplier REAL NOT NULL DEFAULT 1.0,
			cache_creation_cost_multiplier REAL NOT NULL DEFAULT 1.0,
			cache_creation_cost_multiplier_1h REAL NOT NULL DEFAULT 1.0,
			cache_read_cost_multiplier REAL NOT NULL DEFAULT 1.0,
			availability_enabled INTEGER NOT NULL DEFAULT 1,
			created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now', 'localtime') || '+08:00'),
			updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now', 'localtime') || '+08:00')
		);
		CREATE INDEX idx_endpoints_priority ON endpoints(priority);
		CREATE INDEX idx_endpoints_failover ON endpoints(failover_enabled);
		CREATE INDEX idx_endpoints_availability ON endpoints(availability_enabled);
	`)
	if err != nil {
		t.Fatalf("创建目标表失败: %v", err)
	}
	return db
}

func boolPtr(value bool) *bool { return &value }

func TestSQLiteEndpointStoreCRUDUsesFlatSchema(t *testing.T) {
	store := NewSQLiteEndpointStore(createTestDB(t))
	ctx := context.Background()
	cooldown := 120
	record := &EndpointRecord{
		Name:                          "primary",
		URL:                           "https://api.example.com",
		Token:                         "token",
		ApiKey:                        "api-key",
		Headers:                       map[string]string{"X-Custom": "value"},
		Priority:                      2,
		FailoverEnabled:               true,
		CooldownSeconds:               &cooldown,
		TimeoutSeconds:                45,
		SupportsCountTokens:           true,
		ModelRewriteRules:             `[{"paths":["/v1/messages","/v1/messages/count_tokens"],"match":"exact","from":"old","to":"new"}]`,
		CostMultiplier:                1.1,
		InputCostMultiplier:           1.2,
		OutputCostMultiplier:          1.3,
		CacheCreationCostMultiplier:   1.4,
		CacheCreationCostMultiplier1h: 1.5,
		CacheReadCostMultiplier:       1.6,
		AvailabilityEnabled:           boolPtr(true),
	}
	created, err := store.Create(ctx, record)
	if err != nil {
		t.Fatalf("创建端点失败: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("创建后必须分配 ID")
	}

	got, err := store.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("按 ID 获取失败: %v", err)
	}
	if got == nil || got.Name != record.Name || got.Token != "token" || got.ApiKey != "api-key" {
		t.Fatalf("读取结果不匹配: %+v", got)
	}
	if got.Headers["X-Custom"] != "value" || got.CooldownSeconds == nil || *got.CooldownSeconds != cooldown {
		t.Fatalf("headers/cooldown 未正确持久化: %+v", got)
	}

	got.URL = "https://updated.example.com"
	got.Token = "updated-token"
	got.ApiKey = ""
	got.Priority = 7
	got.FailoverEnabled = false
	got.AvailabilityEnabled = boolPtr(false)
	if err := store.Update(ctx, got); err != nil {
		t.Fatalf("更新端点失败: %v", err)
	}
	updated, err := store.Get(ctx, "primary")
	if err != nil {
		t.Fatalf("读取更新结果失败: %v", err)
	}
	if updated.URL != got.URL || updated.Token != "updated-token" || updated.ApiKey != "" || updated.Priority != 7 {
		t.Fatalf("更新结果不匹配: %+v", updated)
	}
	if updated.FailoverEnabled || updated.IsAvailabilityEnabled() {
		t.Fatalf("双状态未正确持久化: %+v", updated)
	}

	if err := store.Delete(ctx, "primary"); err != nil {
		t.Fatalf("删除端点失败: %v", err)
	}
	missing, err := store.Get(ctx, "primary")
	if err != nil || missing != nil {
		t.Fatalf("删除后仍能读取端点: record=%+v err=%v", missing, err)
	}
}

func TestSQLiteEndpointStoreDefaultsAndOrdering(t *testing.T) {
	store := NewSQLiteEndpointStore(createTestDB(t))
	ctx := context.Background()
	for _, record := range []*EndpointRecord{
		{Name: "zeta", URL: "https://zeta.example.com", Priority: 2, FailoverEnabled: true},
		{Name: "beta", URL: "https://beta.example.com", Priority: 1, FailoverEnabled: true},
		{Name: "alpha", URL: "https://alpha.example.com", Priority: 1, FailoverEnabled: true},
	} {
		if _, err := store.Create(ctx, record); err != nil {
			t.Fatalf("创建 %s 失败: %v", record.Name, err)
		}
	}
	records, err := store.List(ctx)
	if err != nil {
		t.Fatalf("列出端点失败: %v", err)
	}
	if len(records) != 3 || records[0].Name != "alpha" || records[1].Name != "beta" || records[2].Name != "zeta" {
		t.Fatalf("端点排序不符合 priority/name: %+v", records)
	}
	for _, record := range records {
		if record.TimeoutSeconds != 300 || record.CostMultiplier != 1 || record.InputCostMultiplier != 1 || record.OutputCostMultiplier != 1 || record.CacheCreationCostMultiplier != 1 || record.CacheCreationCostMultiplier1h != 1 || record.CacheReadCostMultiplier != 1 {
			t.Fatalf("默认值不完整: %+v", record)
		}
		if !record.IsAvailabilityEnabled() {
			t.Fatalf("availability 默认必须启用: %+v", record)
		}
	}
}

func TestSQLiteEndpointStoreNormalizesNilHeadersToObject(t *testing.T) {
	db := createTestDB(t)
	store := NewSQLiteEndpointStore(db)
	ctx := context.Background()
	assertObject := func(name string) {
		t.Helper()
		var raw, jsonType string
		if err := db.QueryRow(`SELECT headers, json_type(headers) FROM endpoints WHERE name = ?`, name).Scan(&raw, &jsonType); err != nil {
			t.Fatalf("读取 %s headers 失败: %v", name, err)
		}
		if raw != "{}" || jsonType != "object" {
			t.Fatalf("%s headers = %q (%s), want {} (object)", name, raw, jsonType)
		}
	}

	created, err := store.Create(ctx, &EndpointRecord{Name: "create-nil", URL: "https://create.example.com"})
	if err != nil {
		t.Fatalf("创建 nil headers 端点失败: %v", err)
	}
	assertObject(created.Name)

	created.Headers = nil
	if err := store.Update(ctx, created); err != nil {
		t.Fatalf("更新 nil headers 端点失败: %v", err)
	}
	assertObject(created.Name)

	if err := store.BatchCreate(ctx, []*EndpointRecord{{Name: "batch-nil", URL: "https://batch.example.com"}}); err != nil {
		t.Fatalf("批量创建 nil headers 端点失败: %v", err)
	}
	assertObject("batch-nil")
}

func TestSQLiteEndpointStoreStateMutations(t *testing.T) {
	store := NewSQLiteEndpointStore(createTestDB(t))
	ctx := context.Background()
	if _, err := store.Create(ctx, &EndpointRecord{Name: "stateful", URL: "https://state.example.com", FailoverEnabled: true}); err != nil {
		t.Fatalf("创建端点失败: %v", err)
	}
	if err := store.SetAvailabilityEnabled(ctx, "stateful", false); err != nil {
		t.Fatalf("关闭硬启用失败: %v", err)
	}
	if err := store.SetFailoverEnabled(ctx, "stateful", false); err != nil {
		t.Fatalf("关闭自动调度失败: %v", err)
	}
	got, err := store.Get(ctx, "stateful")
	if err != nil {
		t.Fatalf("读取端点失败: %v", err)
	}
	if got.IsAvailabilityEnabled() || got.FailoverEnabled {
		t.Fatalf("状态更新未生效: %+v", got)
	}
	if err := store.SetAvailabilityEnabled(ctx, "missing", true); err == nil {
		t.Fatal("更新不存在端点应失败")
	}
	if err := store.SetFailoverEnabled(ctx, "missing", true); err == nil {
		t.Fatal("更新不存在端点应失败")
	}
}

func TestSQLiteEndpointStoreBatchAndDuplicateName(t *testing.T) {
	store := NewSQLiteEndpointStore(createTestDB(t))
	ctx := context.Background()
	records := []*EndpointRecord{
		{Name: "one", URL: "https://one.example.com", FailoverEnabled: true},
		{Name: "two", URL: "https://two.example.com", FailoverEnabled: true},
	}
	if err := store.BatchCreate(ctx, records); err != nil {
		t.Fatalf("批量创建失败: %v", err)
	}
	if count, err := store.Count(ctx); err != nil || count != 2 {
		t.Fatalf("计数错误: count=%d err=%v", count, err)
	}
	if _, err := store.Create(ctx, &EndpointRecord{Name: "one", URL: "https://duplicate.example.com"}); err == nil {
		t.Fatal("重复名称必须失败")
	}
	if err := store.BatchDelete(ctx, []string{"one", "two"}); err != nil {
		t.Fatalf("批量删除失败: %v", err)
	}
	if count, err := store.Count(ctx); err != nil || count != 0 {
		t.Fatalf("批量删除后计数错误: count=%d err=%v", count, err)
	}
}

func TestEndpointSchemaHasNoLegacyChannelOrEnabled(t *testing.T) {
	db := createTestDB(t)
	rows, err := db.Query(`PRAGMA table_info(endpoints)`)
	if err != nil {
		t.Fatalf("读取表结构失败: %v", err)
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notNull, pk int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("扫描表结构失败: %v", err)
		}
		columns[name] = true
	}
	if columns["channel"] || columns["enabled"] {
		t.Fatalf("目标表仍包含 legacy 列: %+v", columns)
	}
	if !columns["availability_enabled"] || !columns["failover_enabled"] {
		t.Fatalf("目标双状态列缺失: %+v", columns)
	}
}
