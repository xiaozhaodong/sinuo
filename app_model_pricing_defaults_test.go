package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"sinuo/internal/service"
	"sinuo/internal/store"

	_ "modernc.org/sqlite"
)

func TestEnsureGPT56ModelPricing_InsertsOfficialPrices(t *testing.T) {
	app := newModelPricingDefaultsTestApp(t)
	ctx := context.Background()

	inserted, err := app.ensureGPT56ModelPricing(ctx)
	if err != nil {
		t.Fatalf("ensure GPT-5.6 pricing failed: %v", err)
	}
	if inserted != 4 {
		t.Fatalf("expected 4 inserted prices, got %d", inserted)
	}

	tests := []struct {
		model      string
		input      float64
		output     float64
		cacheWrite float64
		cacheRead  float64
	}{
		{model: "gpt-5.6", input: 5.0, output: 30.0, cacheWrite: 6.25, cacheRead: 0.50},
		{model: "gpt-5.6-sol", input: 5.0, output: 30.0, cacheWrite: 6.25, cacheRead: 0.50},
		{model: "gpt-5.6-terra", input: 2.5, output: 15.0, cacheWrite: 3.125, cacheRead: 0.25},
		{model: "gpt-5.6-luna", input: 1.0, output: 6.0, cacheWrite: 1.25, cacheRead: 0.10},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			record, err := app.modelPricingService.GetPricing(ctx, tt.model)
			if err != nil {
				t.Fatalf("get pricing failed: %v", err)
			}
			if record == nil {
				t.Fatal("expected pricing record")
			}
			if record.InputPrice != tt.input || record.OutputPrice != tt.output ||
				record.CacheCreationPrice5m != tt.cacheWrite || record.CacheCreationPrice1h != tt.cacheWrite ||
				record.CacheReadPrice != tt.cacheRead {
				t.Fatalf("unexpected pricing: %+v", record)
			}
		})
	}
}

func TestEnsureGPT56ModelPricing_PreservesExistingUserPrice(t *testing.T) {
	app := newModelPricingDefaultsTestApp(t)
	ctx := context.Background()

	_, err := app.modelPricingService.CreatePricing(ctx, &store.ModelPricingRecord{
		ModelName:            "gpt-5.6-terra",
		DisplayName:          "自定义 Terra",
		Description:          "用户自定义价格",
		InputPrice:           9.0,
		OutputPrice:          18.0,
		CacheCreationPrice5m: 11.25,
		CacheCreationPrice1h: 11.25,
		CacheReadPrice:       0.90,
	})
	if err != nil {
		t.Fatalf("create custom pricing failed: %v", err)
	}

	inserted, err := app.ensureGPT56ModelPricing(ctx)
	if err != nil {
		t.Fatalf("ensure GPT-5.6 pricing failed: %v", err)
	}
	if inserted != 3 {
		t.Fatalf("expected only 3 missing prices to be inserted, got %d", inserted)
	}

	record, err := app.modelPricingService.GetPricing(ctx, "gpt-5.6-terra")
	if err != nil {
		t.Fatalf("get custom pricing failed: %v", err)
	}
	if record == nil || record.InputPrice != 9.0 || record.OutputPrice != 18.0 || record.DisplayName != "自定义 Terra" {
		t.Fatalf("expected custom pricing to remain unchanged, got %+v", record)
	}

	inserted, err = app.ensureGPT56ModelPricing(ctx)
	if err != nil {
		t.Fatalf("second ensure GPT-5.6 pricing failed: %v", err)
	}
	if inserted != 0 {
		t.Fatalf("expected idempotent ensure to insert 0 prices, got %d", inserted)
	}
}

func newModelPricingDefaultsTestApp(t *testing.T) *App {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	schemaSQL, err := os.ReadFile(filepath.Join("internal", "tracking", "schema.sql"))
	if err != nil {
		t.Fatalf("read schema failed: %v", err)
	}
	if _, err := db.Exec(string(schemaSQL)); err != nil {
		t.Fatalf("exec schema failed: %v", err)
	}

	pricingStore := store.NewSQLiteModelPricingStore(db)
	return &App{
		modelPricingStore:   pricingStore,
		modelPricingService: service.NewModelPricingService(pricingStore),
	}
}
