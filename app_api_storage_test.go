package main

import (
	"context"
	"database/sql"
	"fmt"
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

	_ "modernc.org/sqlite"
)

func TestCreateEndpointRecord_PersistsModelRewriteRulesAndCacheCreationCostMultiplier1h(t *testing.T) {
	app, cleanup := newEndpointStorageAPITestApp(t)
	defer cleanup()
	availabilityEnabled := false

	input := CreateEndpointInput{
		Name:                          "ep-cache-1h",
		URL:                           "https://api.example.com",
		Token:                         "sk-test-token",
		Priority:                      10,
		FailoverEnabled:               true,
		AvailabilityEnabled:           &availabilityEnabled,
		TimeoutSeconds:                120,
		SupportsCountTokens:           true,
		ModelRewriteRules:             `[{"paths":["/v1/messages","/v1/messages/count_tokens"],"match":"exact","from":"claude-sonnet-4-5","to":"provider-sonnet"}]`,
		CostMultiplier:                1.2,
		InputCostMultiplier:           1.3,
		OutputCostMultiplier:          1.4,
		CacheCreationCostMultiplier:   1.5,
		CacheCreationCostMultiplier1h: 2.5,
		CacheReadCostMultiplier:       1.6,
	}

	if err := app.CreateEndpointRecord(input); err != nil {
		t.Fatalf("CreateEndpointRecord failed: %v", err)
	}

	records, err := app.GetEndpointRecords()
	if err != nil {
		t.Fatalf("GetEndpointRecords failed: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 endpoint record, got %d", len(records))
	}
	if got := records[0].CacheCreationCostMultiplier1h; got != 2.5 {
		t.Fatalf("expected list API cache_creation_cost_multiplier_1h=2.5, got %v", got)
	}
	if got := records[0].ModelRewriteRules; got != input.ModelRewriteRules {
		t.Fatalf("expected list API model_rewrite_rules=%q, got %q", input.ModelRewriteRules, got)
	}
	if records[0].AvailabilityEnabled {
		t.Fatal("expected list API availability_enabled=false")
	}

	record, err := app.GetEndpointRecord("ep-cache-1h")
	if err != nil {
		t.Fatalf("GetEndpointRecord failed: %v", err)
	}
	if got := record.CacheCreationCostMultiplier1h; got != 2.5 {
		t.Fatalf("expected detail API cache_creation_cost_multiplier_1h=2.5, got %v", got)
	}
	if got := record.ModelRewriteRules; got != input.ModelRewriteRules {
		t.Fatalf("expected detail API model_rewrite_rules=%q, got %q", input.ModelRewriteRules, got)
	}
	if record.AvailabilityEnabled {
		t.Fatal("expected detail API availability_enabled=false")
	}
	if runtimeEndpoint := app.endpointManager.GetEndpointByNameAny("ep-cache-1h"); runtimeEndpoint == nil || app.endpointManager.EndpointHardEnabled(runtimeEndpoint) {
		t.Fatalf("expected created endpoint to be hard disabled, got %+v", runtimeEndpoint)
	}

	update := input
	availabilityEnabled = true
	update.AvailabilityEnabled = &availabilityEnabled
	update.CacheCreationCostMultiplier1h = 3.5
	update.ModelRewriteRules = `[{"paths":["/v1/messages","/v1/messages/count_tokens"],"match":"exact","from":"claude-opus-4-1","to":"provider-opus"}]`
	if err := app.UpdateEndpointRecord("ep-cache-1h", update); err != nil {
		t.Fatalf("UpdateEndpointRecord failed: %v", err)
	}

	updated, err := app.GetEndpointRecord("ep-cache-1h")
	if err != nil {
		t.Fatalf("GetEndpointRecord after update failed: %v", err)
	}
	if got := updated.CacheCreationCostMultiplier1h; got != 3.5 {
		t.Fatalf("expected updated detail API cache_creation_cost_multiplier_1h=3.5, got %v", got)
	}
	if got := updated.ModelRewriteRules; got != update.ModelRewriteRules {
		t.Fatalf("expected updated detail API model_rewrite_rules=%q, got %q", update.ModelRewriteRules, got)
	}
	if !updated.AvailabilityEnabled {
		t.Fatal("expected updated detail API availability_enabled=true")
	}
	runtimeEndpoint := app.endpointManager.GetEndpointByNameAny("ep-cache-1h")
	if runtimeEndpoint == nil || runtimeEndpoint.Config.ModelRewriteRules != update.ModelRewriteRules {
		t.Fatalf("expected runtime model rewrite rules to update immediately, got %+v", runtimeEndpoint)
	}
	if !app.endpointManager.EndpointHardEnabled(runtimeEndpoint) {
		t.Fatal("expected updated endpoint to be hard enabled at runtime")
	}
}

func TestCreateEndpointRecord_DefaultsAvailabilityEnabledToTrue(t *testing.T) {
	app, cleanup := newEndpointStorageAPITestApp(t)
	defer cleanup()

	if err := app.CreateEndpointRecord(CreateEndpointInput{
		Name:            "default-availability",
		URL:             "https://default.example.com",
		Priority:        1,
		TimeoutSeconds:  30,
		CostMultiplier:  1,
		FailoverEnabled: true,
	}); err != nil {
		t.Fatalf("CreateEndpointRecord failed: %v", err)
	}

	record, err := app.GetEndpointRecord("default-availability")
	if err != nil {
		t.Fatalf("GetEndpointRecord failed: %v", err)
	}
	if !record.AvailabilityEnabled {
		t.Fatal("omitted availability_enabled must default to true")
	}

	availabilityEnabled := false
	if err := app.UpdateEndpointRecord("default-availability", CreateEndpointInput{
		URL:                 "https://default.example.com",
		Priority:            1,
		TimeoutSeconds:      30,
		CostMultiplier:      1,
		FailoverEnabled:     true,
		AvailabilityEnabled: &availabilityEnabled,
	}); err != nil {
		t.Fatalf("UpdateEndpointRecord hard disable failed: %v", err)
	}
	updated, err := app.GetEndpointRecord("default-availability")
	if err != nil {
		t.Fatalf("GetEndpointRecord after hard disable failed: %v", err)
	}
	if updated.AvailabilityEnabled {
		t.Fatal("explicit availability_enabled=false must persist")
	}
	runtimeEndpoint := app.endpointManager.GetEndpointByNameAny("default-availability")
	if runtimeEndpoint == nil || app.endpointManager.EndpointHardEnabled(runtimeEndpoint) {
		t.Fatalf("explicit availability_enabled=false must hard disable runtime endpoint, got %+v", runtimeEndpoint)
	}
}

func TestUpdateEndpointRecord_SecretEditRequiresExplicitClear(t *testing.T) {
	app, cleanup := newEndpointStorageAPITestApp(t)
	defer cleanup()

	if err := app.CreateEndpointRecord(CreateEndpointInput{
		Name:            "existing-endpoint",
		URL:             "https://existing.example.com",
		Token:           "sk-existing",
		ApiKey:          "api-existing",
		Priority:        1,
		TimeoutSeconds:  30,
		CostMultiplier:  1,
		FailoverEnabled: true,
	}); err != nil {
		t.Fatalf("create existing endpoint failed: %v", err)
	}

	baseUpdate := CreateEndpointInput{
		URL:             "https://updated.example.com",
		Priority:        2,
		TimeoutSeconds:  30,
		FailoverEnabled: true,
	}
	if err := app.UpdateEndpointRecord("existing-endpoint", baseUpdate); err != nil {
		t.Fatalf("update while preserving secrets failed: %v", err)
	}
	preserved, err := app.endpointService.GetEndpoint(context.Background(), "existing-endpoint")
	if err != nil {
		t.Fatalf("get endpoint after preserving secrets failed: %v", err)
	}
	if preserved.Token != "sk-existing" || preserved.ApiKey != "api-existing" {
		t.Fatalf("empty secret fields must preserve stored values: token=%q api_key=%q", preserved.Token, preserved.ApiKey)
	}

	baseUpdate.ClearToken = true
	baseUpdate.ClearApiKey = true
	if err := app.UpdateEndpointRecord("existing-endpoint", baseUpdate); err != nil {
		t.Fatalf("explicit secret clear failed: %v", err)
	}
	cleared, err := app.endpointService.GetEndpoint(context.Background(), "existing-endpoint")
	if err != nil {
		t.Fatalf("get endpoint after clear failed: %v", err)
	}
	if cleared.Token != "" || cleared.ApiKey != "" {
		t.Fatalf("explicit clear must remove stored secrets: token=%q api_key=%q", cleared.Token, cleared.ApiKey)
	}

	conflicting := baseUpdate
	conflicting.Token = "replacement"
	if err := app.UpdateEndpointRecord("existing-endpoint", conflicting); err == nil {
		t.Fatal("setting and clearing token in one update must fail")
	}
}

func TestUpdateEndpointRecord_MissingEndpointReturnsError(t *testing.T) {
	app, cleanup := newEndpointStorageAPITestApp(t)
	defer cleanup()

	err := app.UpdateEndpointRecord("missing-endpoint", CreateEndpointInput{
		URL:            "https://missing.example.com",
		Priority:       1,
		TimeoutSeconds: 30,
	})
	if err == nil || !strings.Contains(err.Error(), "端点 'missing-endpoint' 不存在") {
		t.Fatalf("missing endpoint error = %v", err)
	}
}

func TestCreateEndpointRecord_ClientOAuthValidationAndRuntime(t *testing.T) {
	app, cleanup := newEndpointStorageAPITestApp(t)
	defer cleanup()

	base := CreateEndpointInput{
		URL:            "https://api.anthropic.com",
		AuthMode:       config.EndpointAuthModeClientOAuth,
		Priority:       1,
		TimeoutSeconds: 30,
	}
	rejected := []struct {
		name  string
		patch func(*CreateEndpointInput)
	}{
		{"非官方地址", func(in *CreateEndpointInput) { in.URL = "https://relay.example.com" }},
		{"填写 Token", func(in *CreateEndpointInput) { in.Token = "sk-should-not-store" }},
		{"自定义 Authorization", func(in *CreateEndpointInput) { in.Headers = map[string]string{"Authorization": "Bearer x"} }},
	}
	for i, tc := range rejected {
		input := base
		input.Name = fmt.Sprintf("oauth-rejected-%d", i)
		tc.patch(&input)
		if err := app.CreateEndpointRecord(input); err == nil {
			t.Fatalf("%s 应被拒绝", tc.name)
		}
	}

	input := base
	input.Name = "oauth-ok"
	if err := app.CreateEndpointRecord(input); err != nil {
		t.Fatalf("合法订阅直通端点创建失败: %v", err)
	}
	info, err := app.GetEndpointRecord("oauth-ok")
	if err != nil || info.AuthMode != config.EndpointAuthModeClientOAuth {
		t.Fatalf("前端读到的认证方式不正确: info=%+v err=%v", info, err)
	}
	runtime := app.endpointManager.GetEndpointByNameAny("oauth-ok")
	if runtime == nil || !runtime.Config.IsClientOAuth() {
		t.Fatalf("运行态端点未带上认证方式: %+v", runtime)
	}
}

func TestUpdateEndpointRecord_SwitchToClientOAuthDropsStoredSecrets(t *testing.T) {
	app, cleanup := newEndpointStorageAPITestApp(t)
	defer cleanup()

	if err := app.CreateEndpointRecord(CreateEndpointInput{
		Name:           "switching",
		URL:            "https://relay.example.com",
		Token:          "sk-old",
		ApiKey:         "api-old",
		Priority:       1,
		TimeoutSeconds: 30,
	}); err != nil {
		t.Fatalf("create endpoint failed: %v", err)
	}

	if err := app.UpdateEndpointRecord("switching", CreateEndpointInput{
		URL:            "https://api.anthropic.com",
		AuthMode:       config.EndpointAuthModeClientOAuth,
		Priority:       1,
		TimeoutSeconds: 30,
	}); err != nil {
		t.Fatalf("切换为订阅直通失败: %v", err)
	}
	updated, err := app.endpointService.GetEndpoint(context.Background(), "switching")
	if err != nil {
		t.Fatalf("get endpoint failed: %v", err)
	}
	if updated.Token != "" || updated.ApiKey != "" || updated.AuthMode != config.EndpointAuthModeClientOAuth {
		t.Fatalf("切换后旧凭据必须清空: %+v", updated)
	}

	if err := app.UpdateEndpointRecord("switching", CreateEndpointInput{
		URL:            "https://api.anthropic.com",
		AuthMode:       config.EndpointAuthModeClientOAuth,
		Token:          "sk-explicit",
		Priority:       1,
		TimeoutSeconds: 30,
	}); err == nil {
		t.Fatal("订阅直通端点显式填写 Token 必须被拒绝")
	}
}

func newEndpointStorageAPITestApp(t *testing.T) (*App, func()) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "endpoint_storage_api_test_*")
	if err != nil {
		t.Fatalf("create temp dir failed: %v", err)
	}

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_synchronous=NORMAL")
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		t.Fatalf("open sqlite failed: %v", err)
	}

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
	`
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		_ = os.RemoveAll(tmpDir)
		t.Fatalf("create schema failed: %v", err)
	}

	cfg := &config.Config{
		Health: config.HealthConfig{
			Timeout: 5 * time.Second,
		},
		Endpoints: []config.EndpointConfig{},
	}

	manager := endpoint.NewManager(cfg)
	st := store.NewSQLiteEndpointStore(db)
	svc := service.NewEndpointService(st, manager)

	app := NewApp()
	app.config = cfg
	app.endpointManager = manager
	app.endpointStore = st
	app.endpointService = svc
	app.logger = slog.New(slog.NewTextHandler(io.Discard, nil))

	cleanup := func() {
		manager.Stop()
		_ = db.Close()
		_ = os.RemoveAll(tmpDir)
	}
	return app, cleanup
}
