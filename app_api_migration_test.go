package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sinuo/internal/migration"
	"sinuo/internal/tracking"
)

// 仅使用临时配置及内存数据库，不启动代理、日志或账号检查。
func newReadyMigrationTestApp(t *testing.T, preP2 bool) (*App, *migration.Coordinator) {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("timezone: UTC\nendpoints_storage:\n  type: sqlite\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	core, err := tracking.OpenCoreDatabase(tracking.DatabaseConfig{Type: "sqlite", DatabasePath: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.Close() })
	if preP2 {
		legacySchema, err := os.ReadFile("tests/testdata/pre_p2_tracking_schema.sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := core.DB().Exec(string(legacySchema)); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := core.InitSchema(); err != nil {
			t.Fatal(err)
		}
	}
	coordinator := &migration.Coordinator{
		DB: core.DB(), DatabasePath: ":memory:", ConfigPath: configPath, DataDir: dir,
	}
	status, err := coordinator.Run(context.Background())
	if err != nil || status.State != migration.StartupReady {
		t.Fatalf("prepare coordinator: status=%+v err=%v", status, err)
	}
	app := NewApp()
	app.ctx = context.Background()
	app.configPath = configPath
	app.coreDatabase = core
	app.setMigrationCoordinator(coordinator)
	return app, coordinator
}

func TestGetMigrationStatusDoesNotPublishCoordinatorReadyBeforeStartup(t *testing.T) {
	app, _ := newReadyMigrationTestApp(t, false)
	app.setMigrationStatus(migration.Status{State: migration.StartupInitializing})
	if status := app.GetMigrationStatus(); status.State != migration.StartupInitializing {
		t.Fatalf("components are not started, got status=%+v", status)
	}
}

func TestGetMigrationStatusPreservesStartupFailureAfterCoordinatorReady(t *testing.T) {
	app, _ := newReadyMigrationTestApp(t, false)
	app.setMigrationStatus(migration.Status{State: migration.StartupMigrationFailed, Error: "schema fixture failure", RetryAllowed: true})
	if status := app.GetMigrationStatus(); status.State != migration.StartupMigrationFailed || status.Error != "schema fixture failure" {
		t.Fatalf("startup failure hidden by ready coordinator: %+v", status)
	}
}

func TestRetryStartupMigrationPublishesSchemaFailure(t *testing.T) {
	app, coordinator := newReadyMigrationTestApp(t, false)
	// 制造确定性的后置 schema 错误；协调器本身仍成功。
	if _, err := app.coreDatabase.DB().Exec(`DROP INDEX idx_usage_summary_kind;
		CREATE TABLE idx_usage_summary_kind (marker INTEGER)`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		status, err := app.RetryStartupMigration()
		if err == nil || !strings.Contains(err.Error(), "idx_usage_summary_kind") {
			t.Fatalf("expected schema failure, got status=%+v err=%v", status, err)
		}
		if status.State != migration.StartupMigrationFailed || !status.RetryAllowed || status.Error != err.Error() {
			t.Fatalf("retry returned wrong failure state: %+v", status)
		}
		if got := app.GetMigrationStatus(); got != status {
			t.Fatalf("polling differs from retry: got=%+v returned=%+v", got, status)
		}
		if app.isRunning || app.settingsService != nil || app.subscriptionProxyRuntime != nil {
			t.Fatal("failed startup must not start operational components")
		}
	}
	// 修复隔离库中的故障后，完整准备阶段可再次执行，并清掉上轮错误。
	if _, err := app.coreDatabase.DB().Exec(`DROP TABLE idx_usage_summary_kind`); err != nil {
		t.Fatal(err)
	}
	if err := app.prepareMigratedRuntime(app.ctx, coordinator); err != nil {
		t.Fatalf("retry after repairing fixture: %v", err)
	}
	if got := app.GetMigrationStatus(); got.State != migration.StartupInitializing || got.Error != "" {
		t.Fatalf("preparation must await components and clear stale failure: %+v", got)
	}
}

func TestStartupPreparationUpgradesPreP2SchemaBeforeReady(t *testing.T) {
	app, coordinator := newReadyMigrationTestApp(t, true)
	for range 2 {
		if err := app.prepareMigratedRuntime(app.ctx, coordinator); err != nil {
			t.Fatalf("complete coordinator/config/schema preparation: %v", err)
		}
		if got := app.GetMigrationStatus(); got.State != migration.StartupInitializing {
			t.Fatalf("published ready before operational components: %+v", got)
		}
		var count int
		if err := app.coreDatabase.DB().QueryRow(`SELECT COUNT(*) FROM pragma_table_info('usage_summary')
			WHERE name='request_kind'`).Scan(&count); err != nil || count != 1 {
			t.Fatalf("summary column count=%d err=%v", count, err)
		}
		app.completeStartup()
		if got := app.GetMigrationStatus(); got.State != migration.StartupReady || got.Error != "" || got.RetryAllowed {
			t.Fatalf("completed startup status=%+v", got)
		}
	}
}

func TestRetryStartupMigrationPublishesConfigFailure(t *testing.T) {
	app, _ := newReadyMigrationTestApp(t, false)
	app.configPath = filepath.Join(t.TempDir(), "missing.yaml")
	status, err := app.RetryStartupMigration()
	if err == nil || !strings.Contains(err.Error(), "load migrated runtime config") {
		t.Fatalf("expected runtime config error, got %v", err)
	}
	if got := app.GetMigrationStatus(); got != status || got.State != migration.StartupMigrationFailed || got.Error != err.Error() {
		t.Fatalf("config failure status=%+v returned=%+v", got, status)
	}
}

func TestStartupFailureWithoutCoordinatorIsVisible(t *testing.T) {
	app := NewApp()
	app.failStartup(errors.New("fixture cannot open core database"))
	status := app.GetMigrationStatus()
	if status.State != migration.StartupMigrationFailed || status.Error == "" || status.RetryAllowed {
		t.Fatalf("early failure must be visible and require restart: %+v", status)
	}
}

func TestGetMigrationStatusWithoutCoordinator(t *testing.T) {
	app := NewApp()
	app.migrationStatus = migration.Status{State: migration.StartupMigrationFailed, Error: "fixture"}
	status := app.GetMigrationStatus()
	if status.State != migration.StartupMigrationFailed || status.Error != "fixture" {
		t.Fatalf("status = %+v", status)
	}
}

func TestGetMigrationStatusDoesNotWaitForAppLock(t *testing.T) {
	app := NewApp()
	app.migrationStatus = migration.Status{State: migration.StartupMigrating, Phase: migration.PhasePrepared}

	app.mu.Lock()
	defer app.mu.Unlock()

	done := make(chan migration.Status, 1)
	go func() {
		done <- app.GetMigrationStatus()
	}()

	select {
	case status := <-done:
		if status.State != migration.StartupMigrating || status.Phase != migration.PhasePrepared {
			t.Fatalf("status = %+v", status)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("GetMigrationStatus blocked on the global app lock")
	}
}

func TestGetSystemStatusDoesNotWaitForStartupAppLock(t *testing.T) {
	app := NewApp()
	app.configPath = "/tmp/config.yaml"

	app.mu.Lock()
	status := app.GetSystemStatus()
	app.mu.Unlock()

	if status.Version != Version || status.ConfigPath != "/tmp/config.yaml" || status.ProxyRunning {
		t.Fatalf("startup snapshot = %+v", status)
	}
}

func TestGetSystemStatusWaitsForAppLockAfterStartup(t *testing.T) {
	app := NewApp()
	close(app.startupDone)
	app.mu.Lock()

	done := make(chan SystemStatus, 1)
	go func() {
		done <- app.GetSystemStatus()
	}()

	select {
	case status := <-done:
		t.Fatalf("running status returned before app lock was released: %+v", status)
	case <-time.After(50 * time.Millisecond):
	}

	app.mu.Unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("GetSystemStatus did not resume after app lock was released")
	}
}

func TestRetryStartupMigrationReturnsCurrentStatusWhileStartupBusy(t *testing.T) {
	app := NewApp()
	app.setMigrationStatus(migration.Status{State: migration.StartupInitializing})

	app.startupMu.Lock()
	status, err := app.RetryStartupMigration()
	app.startupMu.Unlock()

	if err != nil {
		t.Fatalf("retry while startup is busy: %v", err)
	}
	if status.State != migration.StartupInitializing {
		t.Fatalf("busy startup status = %+v", status)
	}
}
