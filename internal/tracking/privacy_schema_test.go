package tracking

import (
	"context"
	"testing"
)

// TestInitSchemaCreatesPrivacyTables 隐私保护表回归测试：
// schema.sql 全量执行后 privacy_settings / privacy_rules / privacy_exact_secrets 必须存在，
// 且 privacy_settings 默认行（id=1, mode=disabled）和 builtin PII 行已就位。
func TestInitSchemaCreatesPrivacyTables(t *testing.T) {
	adapter, err := NewSQLiteAdapter(DatabaseConfig{DatabasePath: ":memory:"})
	if err != nil {
		t.Fatalf("create adapter failed: %v", err)
	}
	if err := adapter.Open(); err != nil {
		t.Fatalf("open adapter failed: %v", err)
	}
	t.Cleanup(func() { _ = adapter.Close() })

	if err := adapter.InitSchema(); err != nil {
		t.Fatalf("init schema failed: %v", err)
	}

	db := adapter.GetDB()
	ctx := context.Background()

	for _, table := range []string{
		"privacy_settings", "privacy_rules", "privacy_exact_secrets",
		"model_routing_settings", "model_upstream_groups", "model_routing_assignments",
	} {
		var name string
		err := db.QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Fatalf("table %s missing: %v", table, err)
		}
	}

	var (
		mode            string
		scanMaxBytes    int64
		overLimitAction string
		onError         string
	)
	err = db.QueryRowContext(ctx, `
		SELECT mode, scan_max_bytes, over_limit_action, on_error
		FROM privacy_settings WHERE id = 1
	`).Scan(&mode, &scanMaxBytes, &overLimitAction, &onError)
	if err != nil {
		t.Fatalf("privacy_settings seed row missing: %v", err)
	}
	if mode != "disabled" || scanMaxBytes != 4194304 || overLimitAction != "scan_prefix" || onError != "fail_open" {
		t.Errorf("unexpected defaults: mode=%s scan_max_bytes=%d over_limit=%s on_error=%s",
			mode, scanMaxBytes, overLimitAction, onError)
	}

	// 重复执行 InitSchema 必须幂等（INSERT OR IGNORE / IF NOT EXISTS）
	if err := adapter.InitSchema(); err != nil {
		t.Fatalf("re-init schema failed: %v", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM privacy_settings`).Scan(&count); err != nil {
		t.Fatalf("count settings failed: %v", err)
	}
	if count != 1 {
		t.Errorf("privacy_settings row count = %d, want 1", count)
	}
	var routingSettingsCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM model_routing_settings`).Scan(&routingSettingsCount); err != nil {
		t.Fatalf("count model routing settings failed: %v", err)
	}
	if routingSettingsCount != 1 {
		t.Errorf("model_routing_settings row count = %d, want 1", routingSettingsCount)
	}

	var builtinCount int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM privacy_rules
		WHERE source = 'builtin' AND match_type = 'builtin'
	`).Scan(&builtinCount); err != nil {
		t.Fatalf("count builtin rules failed: %v", err)
	}
	if builtinCount != 3 {
		t.Errorf("builtin privacy rule count = %d, want 3", builtinCount)
	}
}
