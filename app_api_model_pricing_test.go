package main

import (
	"slices"
	"testing"

	"cc-forwarder/internal/tracking"
)

func TestModelPricingAPI_SyncsTrackerPricingOnWrite(t *testing.T) {
	app := newUsageStatsTestApp(t)
	pricingApp := newModelPricingDefaultsTestApp(t)
	app.modelPricingStore = pricingApp.modelPricingStore
	app.modelPricingService = pricingApp.modelPricingService

	const model = "claude-opus-5-5"
	input := CreateModelPricingInput{
		ModelName:            model,
		InputPrice:           4.0,
		OutputPrice:          20.0,
		CacheCreationPrice5m: 5.0,
		CacheCreationPrice1h: 8.0,
		CacheReadPrice:       0.2,
	}

	if err := app.CreateModelPricing(input); err != nil {
		t.Fatalf("create pricing failed: %v", err)
	}
	want := tracking.ModelPricing{Input: 4.0, Output: 20.0, CacheCreation: 5.0, CacheCreation1h: 8.0, CacheRead: 0.2}
	if got := app.usageTracker.GetPricing(model); got != want {
		t.Fatalf("tracker pricing after create = %+v, want %+v", got, want)
	}

	input.InputPrice = 5.0
	input.OutputPrice = 25.0
	if err := app.UpdateModelPricing(model, input); err != nil {
		t.Fatalf("update pricing failed: %v", err)
	}
	want = tracking.ModelPricing{Input: 5.0, Output: 25.0, CacheCreation: 5.0, CacheCreation1h: 8.0, CacheRead: 0.2}
	if got := app.usageTracker.GetPricing(model); got != want {
		t.Fatalf("tracker pricing after update = %+v, want %+v", got, want)
	}

	if err := app.DeleteModelPricing(model); err != nil {
		t.Fatalf("delete pricing failed: %v", err)
	}
	if slices.Contains(app.usageTracker.GetConfiguredModels(), model) {
		t.Fatalf("tracker still has pricing for %s after delete", model)
	}
}
