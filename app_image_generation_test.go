package main

import (
	"context"
	"database/sql"
	"testing"

	"sinuo/internal/service"
	"sinuo/internal/store"

	_ "modernc.org/sqlite"
)

func TestSettingRecordToInfo_MasksImageGenerationAPIKey(t *testing.T) {
	app := &App{}
	info := app.settingRecordToInfo(&store.SettingRecord{
		Category:  service.CategoryImageGeneration,
		Key:       "api_key",
		Value:     "sk-secret",
		ValueType: service.ValueTypePassword,
	})
	if info.Value != "" || !info.SecretConfigured {
		t.Fatalf("expected masked configured secret, got %+v", info)
	}
}

func TestValidateImageGenerationSettingUpdates_RequiresURLAndKeyWhenEnabled(t *testing.T) {
	app := newImageGenerationSettingsTestApp(t)
	err := app.validateImageGenerationSettingUpdates(context.Background(), []UpdateSettingInput{{
		Category: service.CategoryImageGeneration,
		Key:      "enabled",
		Value:    "true",
	}})
	if err == nil {
		t.Fatal("expected enabled image generation without URL/key to fail validation")
	}

	err = app.validateImageGenerationSettingUpdates(context.Background(), []UpdateSettingInput{
		{Category: service.CategoryImageGeneration, Key: "enabled", Value: "true"},
		{Category: service.CategoryImageGeneration, Key: "endpoint_url", Value: "https://api.duckcoding.ai/v1/images/generations"},
		{Category: service.CategoryImageGeneration, Key: "api_key", Value: "sk-test"},
		{Category: service.CategoryImageGeneration, Key: "fixed_price_usd", Value: "0.25"},
	})
	if err != nil {
		t.Fatalf("expected complete image generation settings to pass: %v", err)
	}
}

func TestImageGenerationConfigProvider_ReadsDirectConnect(t *testing.T) {
	app := newImageGenerationSettingsTestApp(t)
	if err := app.settingsService.Set(context.Background(), service.CategoryImageGeneration, "direct_connect", "true"); err != nil {
		t.Fatalf("set direct_connect failed: %v", err)
	}
	config, err := (&imageGenerationConfigProvider{app: app}).GetImageGenerationConfig(context.Background())
	if err != nil {
		t.Fatalf("get image generation config failed: %v", err)
	}
	if !config.DirectConnect {
		t.Fatal("expected direct image connection to be enabled")
	}
	if config.DirectPortMin != 31080 || config.DirectPortMax != 31179 {
		t.Fatalf("unexpected direct source port range: %d-%d", config.DirectPortMin, config.DirectPortMax)
	}
}

func newImageGenerationSettingsTestApp(t *testing.T) *App {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
		CREATE TABLE settings (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			category TEXT NOT NULL,
			key TEXT NOT NULL,
			value TEXT NOT NULL DEFAULT '',
			value_type TEXT NOT NULL DEFAULT 'string',
			label TEXT,
			description TEXT,
			display_order INTEGER DEFAULT 0,
			requires_restart INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now', 'localtime') || '+08:00'),
			updated_at DATETIME DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now', 'localtime') || '+08:00'),
			UNIQUE(category, key)
		)
	`); err != nil {
		t.Fatalf("create settings table failed: %v", err)
	}
	settingsStore := store.NewSQLiteSettingsStore(db)
	settingsService := service.NewSettingsService(settingsStore)
	if err := settingsService.InitDefaults(context.Background()); err != nil {
		t.Fatalf("init defaults failed: %v", err)
	}
	return &App{settingsStore: settingsStore, settingsService: settingsService}
}
