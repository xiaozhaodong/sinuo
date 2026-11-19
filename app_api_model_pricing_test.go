package main

import (
	"fmt"
	"sinuo/config"
	"sinuo/internal/endpoint"
	"sinuo/internal/proxy"
	"sinuo/internal/service"
	"sinuo/internal/store"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"sinuo/internal/tracking"
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

func TestModelPricingAPI_ExplicitZeroIsPublished(t *testing.T) {
	app := newUsageStatsTestApp(t)
	pricingApp := newModelPricingDefaultsTestApp(t)
	app.modelPricingStore = pricingApp.modelPricingStore
	app.modelPricingService = pricingApp.modelPricingService
	const model = "zero-base"
	if err := app.CreateModelPricing(CreateModelPricingInput{ModelName: model, InputPrice: 2}); err != nil {
		t.Fatal(err)
	}
	first := app.usageTracker.GetPricing(model)
	if first.Input != 2 || first.Output != 0 || first.CacheCreation1h != 0 {
		t.Fatalf("显式零价被改写: %+v", first)
	}
	if err := app.UpdateModelPricing(model, CreateModelPricingInput{}); err != nil {
		t.Fatal(err)
	}
	if got := app.usageTracker.GetPricing(model); got != (tracking.ModelPricing{}) {
		t.Fatalf("免费行包含非零单价: %+v", got)
	}
}

func TestChannelPricingAPI_ListCurrentAccountOverrides(t *testing.T) {
	a := newModelPricingDefaultsTestApp(t)
	account := store.BillingChannel{Type: "account", ID: 83, Name: "显示名称"}
	other := store.BillingChannel{Type: "endpoint", ID: 83}
	zero, one := 0.0, 1.0
	override := store.ChannelModelRule{FastMultiplier: &one}
	zeroFast := store.ChannelModelRule{FastMultiplier: &zero}
	for _, model := range []string{"astra", "sol", "reset", "removed"} {
		require.NoError(t, a.CreateModelPricing(CreateModelPricingInput{ModelName: model, InputPrice: 2}))
		require.NoError(t, a.SaveChannelModelRule(account, model, override))
	}
	require.NoError(t, a.SaveChannelModelRule(account, "sol", zeroFast))
	require.NoError(t, a.SaveChannelModelRule(account, "reset", store.ChannelModelRule{}))
	require.NoError(t, a.SaveChannelModelRule(other, "reset", override))
	require.NoError(t, a.DeleteModelPricing("removed"))

	account.Name = "改名后"
	got, err := a.GetChannelModelRules(account)
	require.NoError(t, err)
	require.Equal(t, map[string]store.ChannelModelRule{"astra": override, "sol": zeroFast}, got)
	got, err = a.GetChannelModelRules(other)
	require.NoError(t, err)
	require.Equal(t, map[string]store.ChannelModelRule{"reset": override}, got)
}

func TestChannelPricingAPI_SaveReloadAndSettleZero(t *testing.T) {
	a := newUsageStatsTestApp(t)
	db := a.usageTracker.GetWriteDB()
	a.modelPricingStore = store.NewSQLiteModelPricingStore(db)
	a.modelPricingService = service.NewModelPricingService(a.modelPricingStore)
	a.endpointStore = store.NewSQLiteEndpointStore(db)
	manager := endpoint.NewManager(&config.Config{Failover: config.FailoverConfig{Enabled: true, MaxCandidateAttempts: 3}, Health: config.HealthConfig{Timeout: time.Second}})
	t.Cleanup(manager.Stop)
	a.endpointManager = manager
	a.endpointService = service.NewEndpointService(a.endpointStore, manager)
	require.NoError(t, a.CreateEndpointRecord(CreateEndpointInput{Name: "billing-local", FailoverEnabled: true, URL: "http://127.0.0.1:1", InputCostMultiplier: .5, CostMultiplier: 0}))
	ep, err := a.endpointStore.Get(t.Context(), "billing-local")
	require.NoError(t, err)
	require.Positive(t, ep.ID)
	require.Zero(t, ep.CostMultiplier)
	require.NoError(t, a.UpdateEndpointRecord(ep.Name, CreateEndpointInput{Name: ep.Name, URL: ep.URL, FailoverEnabled: true, TimeoutSeconds: 30, CostMultiplier: 0, InputCostMultiplier: .5}))
	plan := manager.PrepareRouteCandidates(t.Context(), endpoint.RouteRequestProfile{})
	require.NotEmpty(t, plan.Plans)
	admission, err := manager.AcquireEndpointAttempt(plan.Plans[0])
	require.NoError(t, err)
	defer admission.Release()
	target := admission.Target.Config()
	require.Equal(t, ep.ID, target.ID)
	n := func(v float64) *float64 { return &v }
	const model = "api-sample"
	require.NoError(t, a.PublishModelRateSheet(model, store.RateSourceOfficial, store.RateSheet{Rows: []store.RateRow{{Input: n(2), Output: n(0)}, {Fast: true, Input: n(6), Output: n(0)}}}))
	sheet, err := a.GetModelRateSheet(model)
	require.NoError(t, err)
	require.Len(t, sheet.Rows, 2)
	editor, err := a.GetModelRateSheetEditor(model)
	require.NoError(t, err)
	require.True(t, editor.Structured)
	require.Positive(t, editor.VersionID)
	require.Equal(t, store.RateSourceOfficial, editor.Source)
	require.Equal(t, *sheet, editor.Sheet)
	require.Nil(t, editor.Sheet.Rows[0].CacheRead)
	require.Equal(t, n(0), editor.Sheet.Rows[0].Output)
	require.NoError(t, a.UpdateModelPricing(model, CreateModelPricingInput{DisplayName: "新显示名", MetadataOnly: true}))
	afterEdit, err := a.GetModelRateSheetEditor(model)
	require.NoError(t, err)
	require.Equal(t, editor, afterEdit)
	c := store.BillingChannel{Type: "endpoint", ID: target.ID, Name: target.Name}
	for i, fast := range []*float64{nil, n(0), nil} {
		require.NoError(t, a.SaveChannelModelRule(c, model, store.ChannelModelRule{FastMultiplier: fast}))
		saved, err := a.GetChannelModelRule(c, model)
		require.NoError(t, err)
		require.Equal(t, fast, saved.FastMultiplier)
		id := fmt.Sprintf("billing-api-%d", i)
		req := proxy.NewRequestLifecycleManager(a.usageTracker, nil, id, nil)
		req.SetModel(model)
		req.StartRequest("127.0.0.1", "api-test", "POST", "/v1/messages", false)
		req.SetEndpointAttempt(target.Name, target.ID, admission.Target.Revision())
		req.SetFirstTokenStartTime(time.Now())
		req.SetResponseReasoning("speed:fast")
		req.CompleteRequest(&tracking.TokenUsage{InputTokens: 100000})
		details, _, err := a.usageTracker.QueryRequestDetailsWithHotPool(t.Context(), &tracking.QueryOptions{RequestID: id})
		require.NoError(t, err)
		require.Len(t, details, 1)
		require.True(t, details[0].CostKnown)
		require.InDelta(t, []float64{.3, 0, .3}[i], details[0].TotalCostUSD, 1e-9)
		lifecycle, err := a.GetRequestLifecycleDetail(id)
		require.NoError(t, err)
		require.NotNil(t, lifecycle.OfficialRecalculation)
		require.True(t, lifecycle.OfficialRecalculation.Available)
		require.NotNil(t, lifecycle.BillingMultipliers)
		require.Equal(t, .5, lifecycle.BillingMultipliers.Input)
	}
	bad := store.ChannelModelRule{Tiers: []store.ChannelTier{{Threshold: new(int64)}}}
	require.Error(t, a.SaveChannelModelRule(c, model, bad))
}
