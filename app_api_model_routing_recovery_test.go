package main

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"sinuo/internal/service"
	"sinuo/internal/store"
)

func TestModelRoutingSettings_ManualEnableAfterStartupReadFailure(t *testing.T) {
	for _, initialEnabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("saved_enabled=%v", initialEnabled), func(t *testing.T) {
			ctx := context.Background()
			db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "routing-recovery.db"))
			if err != nil {
				t.Fatal(err)
			}
			db.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = db.Close() })
			persistedStore := store.NewSQLiteModelRoutingStore(db)
			if err := persistedStore.UpdateSettings(ctx, store.ModelRoutingSettingsPatch{ClaudeEnabled: &initialEnabled, CodexEnabled: &initialEnabled}); err != nil {
				t.Fatal(err)
			}

			app := NewApp()
			app.modelRoutingStore = store.NewSQLiteModelRoutingStore(db)
			app.modelRoutingService = service.NewModelRoutingService(app.modelRoutingStore)
			app.codexModelRoutingService = service.NewCodexModelRoutingService(app.modelRoutingStore, nil)
			app.modelRoutingService.SetWriteMutex(&app.routingMu)
			app.codexModelRoutingService.SetWriteMutex(&app.routingMu)
			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			if err := app.modelRoutingService.Initialize(cancelled); err == nil {
				t.Fatal("Claude 首次加载应失败")
			}
			if err := app.codexModelRoutingService.Initialize(cancelled); err == nil {
				t.Fatal("Codex 首次加载应失败")
			}

			info, err := app.GetModelRoutingSettings()
			if err != nil {
				t.Fatalf("临时错误恢复后应能打开设置: %v", err)
			}
			if info.ClaudeEnabled || info.CodexEnabled {
				t.Fatalf("界面应显示实际已回到全局路由，可直接重新开启: %+v", info)
			}
			stored, err := persistedStore.GetSettings(ctx)
			if err != nil || stored == nil || stored.ClaudeEnabled != initialEnabled || stored.CodexEnabled != initialEnabled {
				t.Fatalf("加载失败意外改写已保存设置: settings=%+v err=%v", stored, err)
			}

			on := true
			info, err = app.UpdateModelRoutingSettings(UpdateModelRoutingSettingsInput{ClaudeEnabled: &on})
			if err != nil || !info.ClaudeEnabled {
				t.Fatalf("单独重新开启 Claude 失败: settings=%+v err=%v", info, err)
			}
			info, err = app.UpdateModelRoutingSettings(UpdateModelRoutingSettingsInput{CodexEnabled: &on})
			if err != nil {
				t.Fatalf("重新开启保存失败: %v", err)
			}
			if !info.ClaudeEnabled || !info.CodexEnabled || info.ClaudeStatus != service.ModelRoutingStatusReady || info.CodexStatus != service.ModelRoutingStatusReady {
				t.Fatalf("重新开启后运行态未恢复: %+v", info)
			}
			reopenedStore := store.NewSQLiteModelRoutingStore(db)
			stored, err = reopenedStore.GetSettings(ctx)
			if err != nil || stored == nil || !stored.ClaudeEnabled || !stored.CodexEnabled {
				t.Fatalf("独立读取未确认开启状态已持久化: settings=%+v err=%v", stored, err)
			}
			claude := service.NewModelRoutingService(reopenedStore)
			codex := service.NewCodexModelRoutingService(reopenedStore, nil)
			if err := claude.Initialize(ctx); err != nil {
				t.Fatal(err)
			}
			if err := codex.Initialize(ctx); err != nil {
				t.Fatal(err)
			}
			if !claude.CurrentSnapshot().Enabled() || !codex.Enabled() {
				t.Fatal("重建服务后丢失已保存开关")
			}
		})
	}
}
