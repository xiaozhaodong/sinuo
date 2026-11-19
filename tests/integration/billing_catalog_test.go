package integration

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"sinuo/internal/proxy"
	"sinuo/internal/store"
	"sinuo/internal/tracking"

	"github.com/stretchr/testify/require"
)

func openBillingCatalog(t *testing.T, path string, hot bool) *tracking.UsageTracker {
	t.Helper()
	tr, err := tracking.NewUsageTracker(&tracking.Config{
		Enabled: true, DatabasePath: path, BatchSize: 1, FlushInterval: 10 * time.Millisecond,
		HotPool: &tracking.HotPoolSettings{Enabled: hot},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tr.Close()) })
	return tr
}

func completeCatalogRequest(t *testing.T, tr *tracking.UsageTracker, id, model string, endpoint int64, usage tracking.TokenUsage) *tracking.RequestDetail {
	t.Helper()
	req := proxy.NewRequestLifecycleManager(tr, nil, id, nil)
	req.SetModel(model)
	req.StartRequest("127.0.0.1", "catalog-test", "POST", "/v1/messages", false)
	req.SetEndpointAttempt("test-endpoint", endpoint, 1)
	req.SetFirstTokenStartTime(time.Now())
	req.SetResponseReasoning("speed:fast")
	req.CompleteRequest(&usage)
	return waitBillingRecord(t, tr, req, func(r *tracking.RequestDetail) bool { return r.Status == "completed" })
}

func TestBillingCatalog_LegacyCacheAndMissingModel(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "usage.db")
	old := openBillingCatalog(t, path, true)
	for _, legacy := range []struct {
		name  string
		cache float64
	}{{"legacy-zero", 0}, {"legacy-negative", -1}, {"legacy-priced", 4}, {"_default", 9}} {
		_, err := old.GetWriteDB().Exec("INSERT INTO model_pricing(model_name,input_price,cache_creation_price_1h) VALUES(?,?,?)", legacy.name, 3, legacy.cache)
		require.NoError(t, err)
	}
	require.NoError(t, old.Close())
	tr := openBillingCatalog(t, path, true)
	prices := store.NewSQLiteModelPricingStore(tr.GetWriteDB())
	_, err := prices.Create(ctx, &store.ModelPricingRecord{ModelName: "custom-free", InputPrice: 3})
	require.NoError(t, err)
	input, zero := 3.0, 0.0
	row := store.RateRow{Input: &input, Output: &zero}
	require.NoError(t, prices.PublishRateSheet(ctx, "sheet-missing-cache", store.RateSourceOfficial, store.RateSheet{Rows: []store.RateRow{row}}))
	row.Cache1h = &zero
	require.NoError(t, prices.PublishRateSheet(ctx, "sheet-free-cache", store.RateSourceOfficial, store.RateSheet{Rows: []store.RateRow{row}}))
	require.NoError(t, tr.LoadBaseRates(ctx, prices))
	models := []struct {
		model string
		cost  sql.NullFloat64
	}{
		{"legacy-zero", sql.NullFloat64{Valid: true, Float64: 6}},
		{"legacy-negative", sql.NullFloat64{Valid: true, Float64: 6}},
		{"legacy-priced", sql.NullFloat64{Valid: true, Float64: 4}},
		{"custom-free", sql.NullFloat64{Valid: true}},
		{"sheet-free-cache", sql.NullFloat64{Valid: true}},
		{"sheet-missing-cache", sql.NullFloat64{}},
		{"unpriced", sql.NullFloat64{Valid: true, Float64: 9}},
	}
	for _, tc := range models {
		got := completeCatalogRequest(t, tr, tc.model, tc.model, 1, tracking.TokenUsage{CacheCreation1hTokens: 1_000_000})
		require.Equal(t, tc.cost.Valid, got.CostKnown, tc.model)
		require.Equal(t, tc.cost.Float64, got.TotalCostUSD, tc.model)
		require.EqualValues(t, 1_000_000, got.BillingDecision.FullInput, tc.model)
	}
	require.NoError(t, tr.Close())
	reader, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	require.NoError(t, err)
	defer reader.Close()
	for _, tc := range models {
		var cost sql.NullFloat64
		require.NoError(t, reader.QueryRow("SELECT total_cost_usd FROM request_logs WHERE request_id=?", tc.model).Scan(&cost))
		require.Equal(t, tc.cost, cost, tc.model)
	}
}

func TestBillingCatalog_DefaultEstimateFollowsRequestSnapshot(t *testing.T) {
	for _, mode := range []struct {
		name string
		hot  bool
	}{{"hot", true}, {"event", false}} {
		t.Run(mode.name, func(t *testing.T) {
			ctx := t.Context()
			path := filepath.Join(t.TempDir(), "usage.db")
			tr := openBillingCatalog(t, path, mode.hot)
			prices := store.NewSQLiteModelPricingStore(tr.GetWriteDB())
			require.NoError(t, tr.LoadBaseRates(ctx, prices))
			u := tracking.TokenUsage{InputTokens: 100_000}
			builtin := completeCatalogRequest(t, tr, "builtin", "new-model", 1, u)
			require.True(t, builtin.CostKnown)
			require.InDelta(t, .30, builtin.TotalCostUSD, 1e-12)

			input, output, freeCacheRead := 4., 8., 0.
			sheet := store.RateSheet{Rows: []store.RateRow{{Input: &input, Output: &output, CacheRead: &freeCacheRead}}}
			require.NoError(t, prices.PublishRateSheet(ctx, "first-default", store.RateSourceOfficial, sheet))
			next := store.ModelPricingRecord{ModelName: "next-default", InputPrice: 10}
			_, err := prices.Create(ctx, &next)
			require.NoError(t, err)
			require.NoError(t, prices.SetDefault(ctx, "first-default"))
			require.NoError(t, tr.LoadBaseRates(ctx, prices))
			cache := completeCatalogRequest(t, tr, "estimated-cache", "new-model", 1,
				tracking.TokenUsage{CacheReadTokens: 1_000_000, CacheCreation5mTokens: 1_000_000})
			require.True(t, cache.CostKnown)
			require.Equal(t, 3.75, cache.TotalCostUSD)
			require.Zero(t, cache.CacheReadCostUSD)
			tr.UpdateEndpointMultipliers(map[string]tracking.EndpointMultiplier{"test-endpoint": {CostMultiplier: .5}})
			req := proxy.NewRequestLifecycleManager(tr, nil, "in-flight", nil)
			req.SetClientSession("default-estimate")
			req.SetModel(billingTestModel)
			req.StartRequest("127.0.0.1", "catalog-test", "POST", "/v1/messages", false)
			req.SetEndpointAttempt("test-endpoint", 1, 1)
			req.SetFirstTokenStartTime(time.Now())
			require.NoError(t, prices.SetDefault(ctx, next.ModelName))
			input = 40
			require.NoError(t, prices.PublishRateSheet(ctx, "first-default", store.RateSourceOfficial, sheet))
			require.NoError(t, tr.LoadBaseRates(ctx, prices))
			req.SetResponseReasoning("speed:flex")
			req.CompleteRequest(&u)
			old := waitBillingRecord(t, tr, req, func(d *tracking.RequestDetail) bool { return d.Status == "completed" })
			require.True(t, old.CostKnown)
			require.Equal(t, billingTestModel, old.ModelName)
			require.InDelta(t, .20, old.TotalCostUSD, 1e-12)
			sessions, err := tr.QueryRequestSessions(ctx, &tracking.QueryOptions{SessionID: "default-estimate"}, 1, 10, "cost", "desc")
			require.NoError(t, err)
			require.Len(t, sessions.Rows, 1)
			require.Zero(t, sessions.Rows[0].MissingCosts)
			require.InDelta(t, .20, sessions.KnownCostUSD, 1e-12)
			_, err = tr.RecalculateOfficialCost(ctx, req.GetRequestID())
			require.Error(t, err)
			after := completeCatalogRequest(t, tr, "after-switch", billingTestModel, 1, u)
			require.True(t, after.CostKnown)
			require.InDelta(t, .50, after.TotalCostUSD, 1e-12)

			_, err = prices.Create(ctx, &store.ModelPricingRecord{ModelName: billingTestModel, InputPrice: 20})
			require.NoError(t, err)
			require.NoError(t, tr.LoadBaseRates(ctx, prices))
			exact := completeCatalogRequest(t, tr, "exact", billingTestModel, 1, u)
			require.True(t, exact.CostKnown)
			require.InDelta(t, 1.0, exact.TotalCostUSD, 1e-12)
			require.NoError(t, tr.Close())
			reopened := openBillingCatalog(t, path, false)
			require.NoError(t, reopened.LoadBaseRates(ctx, store.NewSQLiteModelPricingStore(reopened.GetWriteDB())))
			_, err = reopened.RecalculateOfficialCost(ctx, req.GetRequestID())
			require.Error(t, err)
			reopened.RecoverRequestTokens(req.GetRequestID(), billingTestModel, &tracking.TokenUsage{InputTokens: 200_000})
			late := waitBillingRecord(t, reopened, req, func(d *tracking.RequestDetail) bool { return d.InputTokens == 200_000 })
			require.True(t, late.CostKnown)
			require.InDelta(t, .40, late.TotalCostUSD, 1e-12)
		})
	}
}

func TestBillingCatalog_IsolatesInvalidVersions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		hot     bool
		restart bool
	}{{"event-reload", false, false}, {"hot-restart", true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			path := filepath.Join(t.TempDir(), "usage.db")
			tr := openBillingCatalog(t, path, tc.hot)
			st := store.NewSQLiteModelPricingStore(tr.GetWriteDB())
			for _, model := range []string{"broken", "healthy"} {
				_, err := st.Create(ctx, &store.ModelPricingRecord{ModelName: model, RateSource: store.RateSourceOfficial, InputPrice: 2})
				require.NoError(t, err)
			}
			endpoint := store.BillingChannel{Type: "endpoint", ID: 7}
			free := 0.0
			require.NoError(t, st.SaveChannelModelRule(ctx, endpoint, "healthy", store.ChannelModelRule{FastMultiplier: &free}))
			require.NoError(t, tr.LoadBaseRates(ctx, st))
			input, output := 8.0, 1.0
			sheet := store.RateSheet{Rows: []store.RateRow{{Input: &input, Output: &output}}}
			for _, model := range []string{"broken", "healthy"} {
				require.NoError(t, st.PublishRateSheet(ctx, model, store.RateSourceOfficial, sheet))
			}
			broken, err := st.CurrentBaseRate(ctx, "broken")
			require.NoError(t, err)
			_, err = tr.GetWriteDB().Exec("UPDATE model_rate_sheets SET sheet=? WHERE version_id=?", "null", broken.ID)
			require.NoError(t, err)
			res, err := tr.GetWriteDB().Exec("INSERT INTO channel_model_rules(channel_type,channel_id,channel_name,model_name,rule) VALUES(?,?,?,?,?)", endpoint.Type, endpoint.ID, "", "healthy", "{")
			require.NoError(t, err)
			ruleID, err := res.LastInsertId()
			require.NoError(t, err)
			if tc.restart {
				require.NoError(t, tr.Close())
				tr = openBillingCatalog(t, path, tc.hot)
				st = store.NewSQLiteModelPricingStore(tr.GetWriteDB())
			}
			require.NoError(t, tr.LoadBaseRates(ctx, st))
			usage := tracking.TokenUsage{InputTokens: 1_000_000}
			good := completeCatalogRequest(t, tr, "good", "healthy", 8, usage)
			require.True(t, good.CostKnown)
			require.Equal(t, 8.0, good.TotalCostUSD)
			badRate := completeCatalogRequest(t, tr, "bad-rate", "broken", 8, usage)
			require.False(t, badRate.CostKnown)
			require.Equal(t, broken.ID, badRate.BillingRateID)
			require.Contains(t, badRate.BillingDecision.Missing, "费率版本已损坏")
			badRule := completeCatalogRequest(t, tr, "bad-rule", "healthy", endpoint.ID, usage)
			require.False(t, badRule.CostKnown)
			require.Equal(t, ruleID, badRule.BillingDecision.RuleID)
			require.Contains(t, badRule.BillingDecision.Missing, "规则已损坏")
			// 坏版本留在历史中；发布新版本后，后续调用可以正常结算。
			require.NoError(t, st.PublishRateSheet(ctx, "broken", store.RateSourceOfficial, sheet))
			require.NoError(t, st.SaveChannelModelRule(ctx, endpoint, "healthy", store.ChannelModelRule{}))
			require.NoError(t, tr.LoadBaseRates(ctx, st))
			for _, model := range []string{"broken", "healthy"} {
				fixed := completeCatalogRequest(t, tr, "repaired-"+model, model, endpoint.ID, usage)
				require.True(t, fixed.CostKnown)
				require.Equal(t, 8.0, fixed.TotalCostUSD)
			}
			require.NoError(t, tr.Close())
			tr = openBillingCatalog(t, path, tc.hot)
			for _, id := range []string{"bad-rate", "bad-rule", "good"} {
				var cost sql.NullFloat64
				require.NoError(t, tr.GetReadDB().QueryRow("SELECT total_cost_usd FROM request_logs WHERE request_id=?", id).Scan(&cost))
				require.Equal(t, id == "good", cost.Valid, id)
			}
			official, err := tr.RecalculateOfficialCost(ctx, "bad-rule")
			require.NoError(t, err)
			require.Equal(t, 8.0, official.TotalCost)
			_, err = tr.RecalculateOfficialCost(ctx, "bad-rate")
			require.Error(t, err)
		})
	}
}

func TestBillingCatalog_ReadFailurePreservesKnownPrices(t *testing.T) {
	tr := openBillingCatalog(t, filepath.Join(t.TempDir(), "usage.db"), true)
	st := store.NewSQLiteModelPricingStore(tr.GetWriteDB())
	_, err := st.Create(t.Context(), &store.ModelPricingRecord{ModelName: "healthy", InputPrice: 8})
	require.NoError(t, err)
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	require.Error(t, tr.LoadBaseRates(cancelled, st))
	usage := tracking.TokenUsage{InputTokens: 1_000_000}
	require.False(t, completeCatalogRequest(t, tr, "cold", "healthy", 1, usage).CostKnown)
	require.NoError(t, tr.LoadBaseRates(t.Context(), st))
	require.Error(t, tr.LoadBaseRates(cancelled, st))
	got := completeCatalogRequest(t, tr, "reload", "healthy", 1, usage)
	require.True(t, got.CostKnown)
	require.Equal(t, 8.0, got.TotalCostUSD)
}
