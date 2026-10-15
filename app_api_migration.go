package main

import (
	"context"
	"fmt"

	"sinuo/internal/migration"
)

// GetMigrationStatus 返回启动迁移与恢复状态。迁移失败时前端只依赖此只读 API。
func (a *App) GetMigrationStatus() migration.Status {
	if a == nil {
		return migration.Status{State: migration.StartupInitializing, MigrationID: migration.MigrationID}
	}
	coordinator, status := a.migrationState()
	// App 是整体启动结果的权威来源；协调器只报告其负责的迁移进度。
	if status.State == migration.StartupReady || status.State == migration.StartupMigrationFailed {
		return status
	}
	if coordinator != nil {
		status = coordinator.Status()
		// 协调器成功后还有配置加载、增量 schema 和组件初始化，不提前开放业务页。
		if status.State == migration.StartupReady {
			status.State = migration.StartupInitializing
		}
	}
	if status.State == "" {
		status.State = migration.StartupInitializing
	}
	return status
}

// RetryStartupMigration 仅重试协调器允许的幂等阶段；成功后启动此前被阻断的运行态组件。
func (a *App) RetryStartupMigration() (migration.Status, error) {
	// 首次 ServiceStartup 在后台运行时直接返回当前状态，避免与迁移/组件初始化重入。
	if !a.startupMu.TryLock() {
		return a.GetMigrationStatus(), nil
	}
	defer a.startupMu.Unlock()

	a.mu.Lock()
	defer a.mu.Unlock()
	coordinator, fallback := a.migrationState()
	if coordinator == nil {
		return fallback, nil
	}
	if a.isRunning {
		return a.GetMigrationStatus(), nil
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if err := a.prepareMigratedRuntime(ctx, coordinator); err != nil {
		return a.failStartup(err), err
	}
	a.setupLogger()
	a.startOperationalComponentsLocked(ctx)
	return a.GetMigrationStatus(), nil
}

// prepareMigratedRuntime 统一首次启动与重试的完整准备顺序。
// 返回成功只代表可初始化组件；ready 由组件启动末尾的 completeStartup 发布。
func (a *App) prepareMigratedRuntime(ctx context.Context, coordinator *migration.Coordinator) error {
	a.setMigrationStatus(migration.Status{State: migration.StartupInitializing})
	if _, err := coordinator.Run(ctx); err != nil {
		return err
	}
	if err := a.loadRuntimeConfig(coordinator.DatabasePath); err != nil {
		return err
	}
	if a.coreDatabase == nil {
		return fmt.Errorf("initialize core database schema: core database is nil")
	}
	return a.coreDatabase.InitSchema()
}

// failStartup 只更新 App 启动结果，不回退或重写已经完成的迁移 ledger。
func (a *App) failStartup(err error) migration.Status {
	coordinator, status := a.migrationState()
	if coordinator != nil {
		status = coordinator.Status()
		if status.State == migration.StartupMigrationFailed {
			// 保留协调器已有的错误处理与重试策略。
			a.setMigrationStatus(status)
			return status
		}
	}
	status.State = migration.StartupMigrationFailed
	status.Error = err.Error()
	status.RetryAllowed = coordinator != nil
	a.setMigrationStatus(status)
	return status
}

func (a *App) completeStartup() {
	coordinator, status := a.migrationState()
	if coordinator != nil {
		status = coordinator.Status()
	}
	status.State = migration.StartupReady
	status.Error = ""
	status.RetryAllowed = false
	a.setMigrationStatus(status)
}

func (a *App) migrationState() (*migration.Coordinator, migration.Status) {
	a.migrationMu.RLock()
	defer a.migrationMu.RUnlock()
	return a.migrationCoordinator, a.migrationStatus
}

func (a *App) setMigrationCoordinator(coordinator *migration.Coordinator) {
	a.migrationMu.Lock()
	a.migrationCoordinator = coordinator
	a.migrationMu.Unlock()
}

func (a *App) setMigrationStatus(status migration.Status) {
	a.migrationMu.Lock()
	a.migrationStatus = status
	a.migrationMu.Unlock()
	if a.tray != nil {
		a.tray.signal(a.tray.refresh)
	}
}
