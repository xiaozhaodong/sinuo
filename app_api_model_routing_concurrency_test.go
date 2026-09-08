package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"

	"cc-forwarder/internal/service"
	"cc-forwarder/internal/store"
)

// TestUpdateModelRoutingSettings_ConcurrentSwitchesBothPersist 锁定 P2 修复：
// Claude 与 Codex 两个开关并发提交时都必须落库，不允许后写的一方覆盖另一方。
// Wails 对每个前端调用起独立 goroutine，这里直接并发调用同一个入口复现该交错。
func TestUpdateModelRoutingSettings_ConcurrentSwitchesBothPersist(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "routing-concurrent.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	app := NewApp()
	app.modelRoutingStore = store.NewSQLiteModelRoutingStore(db)
	app.modelRoutingService = service.NewModelRoutingService(app.modelRoutingStore)
	app.codexModelRoutingService = service.NewCodexModelRoutingService(app.modelRoutingStore, nil)
	app.modelRoutingService.SetWriteMutex(&app.routingMu)
	app.codexModelRoutingService.SetWriteMutex(&app.routingMu)

	on, off := true, false
	for round := 0; round < 20; round++ {
		if _, err := app.modelRoutingService.UpdateSettings(ctx, store.ModelRoutingSettingsPatch{ClaudeEnabled: &off, CodexEnabled: &off}); err != nil {
			t.Fatalf("第 %d 轮重置开关失败: %v", round, err)
		}
		var wg sync.WaitGroup
		for _, input := range []UpdateModelRoutingSettingsInput{{ClaudeEnabled: &on}, {CodexEnabled: &on}} {
			wg.Add(1)
			go func(input UpdateModelRoutingSettingsInput) {
				defer wg.Done()
				if _, err := app.UpdateModelRoutingSettings(input); err != nil {
					t.Errorf("第 %d 轮并发开关失败: %v", round, err)
				}
			}(input)
		}
		wg.Wait()

		stored, err := app.modelRoutingStore.GetSettings(ctx)
		if err != nil || stored == nil {
			t.Fatalf("第 %d 轮读取设置失败: %+v err=%v", round, stored, err)
		}
		if !stored.ClaudeEnabled || !stored.CodexEnabled {
			t.Fatalf("第 %d 轮并发开关丢失更新: %+v", round, stored)
		}
	}
}
