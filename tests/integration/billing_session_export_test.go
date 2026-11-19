package integration

import (
	"path/filepath"
	"testing"
	"time"

	"sinuo/internal/proxy"
	"sinuo/internal/store"
	"sinuo/internal/tracking"

	"github.com/stretchr/testify/require"
)

// 同一会话的请求在两个费率版本下结算，再核对热池、归档明细、会话汇总与官方价复算。
func TestBillingSession_PreservesArchivedChannelCostsAcrossPriceChange(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "usage.db")
	open := func() *tracking.UsageTracker {
		tr, err := tracking.NewUsageTracker(&tracking.Config{Enabled: true, DatabasePath: path, BatchSize: 100, FlushInterval: time.Hour})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, tr.Close()) })
		return tr
	}
	const session = "across-price-change"
	opts := &tracking.QueryOptions{SessionID: session, RequestFamily: "claude"}
	tr := open()
	prices := store.NewSQLiteModelPricingStore(tr.GetWriteDB())
	base := store.ModelPricingRecord{ModelName: billingTestModel, InputPrice: 2.0000014, RateSource: store.RateSourceOfficial}
	_, err := prices.Create(ctx, &base)
	require.NoError(t, err)
	require.NoError(t, tr.LoadBaseRates(ctx, prices))
	tr.UpdateEndpointMultipliers(map[string]tracking.EndpointMultiplier{"session-endpoint": {CostMultiplier: .5}})
	request := func(id string) {
		r := proxy.NewRequestLifecycleManager(tr, nil, id, nil)
		r.SetClientSession(session)
		r.SetRequestFamily(tracking.RequestFamilyClaude)
		r.SetModel(billingTestModel)
		r.StartRequest("127.0.0.1", "session-billing-test", "POST", "/v1/messages", false)
		r.SetEndpointAttempt("session-endpoint", 51, 1)
		r.SetFirstTokenStartTime(time.Now())
		r.CompleteRequest(&tracking.TokenUsage{InputTokens: 100000})
	}
	request("before")
	base.InputPrice = 4.0000018
	require.NoError(t, prices.Update(ctx, &base))
	require.NoError(t, tr.LoadBaseRates(ctx, prices))
	request("after")
	want := map[string]float64{"before": .10000007, "after": .20000009}
	checkSession := func(tr *tracking.UsageTracker) {
		t.Helper()
		details, count, err := tr.QueryRequestDetailsWithHotPool(ctx, opts)
		require.NoError(t, err)
		require.EqualValues(t, 2, count)
		require.Len(t, details, 2)
		for _, row := range details {
			require.Contains(t, want, row.RequestID)
			require.True(t, row.CostKnown)
			require.InDelta(t, want[row.RequestID], row.TotalCostUSD, 1e-14)
		}
		summary, err := tr.QueryRequestSessions(ctx, opts, 1, 30, "cost", "desc")
		require.NoError(t, err)
		require.Len(t, summary.Rows, 1)
		require.EqualValues(t, 2, summary.Rows[0].RequestCount)
		require.Zero(t, summary.Rows[0].MissingCosts)
		require.NotNil(t, summary.Rows[0].TotalCostUSD)
		require.InDelta(t, .30000016, *summary.Rows[0].TotalCostUSD, 1e-14)
		require.InDelta(t, .30000016, summary.KnownCostUSD, 1e-14)
		trend, err := tr.QueryRequestSessionTrend(ctx, opts, "", 1)
		require.NoError(t, err)
		require.Len(t, trend.Points, 2)
		for _, point := range trend.Points {
			require.NotNil(t, point.Cost)
			require.InDelta(t, want[point.RequestID], *point.Cost, 1e-14)
		}
	}
	checkSession(tr)
	var archived int
	require.NoError(t, tr.GetReadDB().QueryRow("SELECT COUNT(*) FROM request_logs WHERE session_id=?", session).Scan(&archived))
	require.Zero(t, archived)
	require.NoError(t, tr.Close())
	tr = open()
	require.NoError(t, tr.LoadBaseRates(ctx, store.NewSQLiteModelPricingStore(tr.GetWriteDB())))
	checkSession(tr)
	// 归档明细保留逐笔结算金额，包括第六位小数之后的有效数字。
	archive, err := tr.QueryRequestDetails(ctx, opts)
	require.NoError(t, err)
	require.Len(t, archive, 2)
	for _, row := range archive {
		require.Contains(t, want, row.RequestID)
		require.Equal(t, session, row.SessionID)
		require.True(t, row.CostKnown)
		require.InDelta(t, want[row.RequestID], row.TotalCostUSD, 1e-14)
		official, err := tr.RecalculateOfficialCost(ctx, row.RequestID)
		require.NoError(t, err)
		require.InDelta(t, 2*row.TotalCostUSD, official.TotalCost, 1e-14)
	}
}

func TestBillingSession_CorruptBasisKeepsAmountsAndOtherRows(t *testing.T) {
	tr := openBillingCatalog(t, filepath.Join(t.TempDir(), "basis.db"), false)
	db := tr.GetWriteDB()
	_, err := db.Exec(`INSERT INTO request_logs(request_id,start_time,status,total_cost_usd,billing_basis) VALUES
	  ('bad','2026-01-01T00:00:00.000000Z','completed',1.25,'{broken'),('good','2026-01-01T00:00:00.000000Z','completed',0.5,'{"decision":{"model_multiplier":2}}')`)
	require.NoError(t, err)
	for _, skip := range []bool{true, false} {
		rows, err := tr.QueryRequestDetails(t.Context(), &tracking.QueryOptions{SkipArchivedBillingBasis: skip})
		require.NoError(t, err)
		require.Len(t, rows, 2)
		for _, row := range rows {
			if row.RequestID == "bad" {
				require.Nil(t, row.BillingDecision)
				require.Equal(t, 1.25, row.TotalCostUSD)
			} else {
				require.Equal(t, !skip, row.BillingDecision != nil)
			}
		}
	}
	detail, err := tr.GetRequestLifecycleData(t.Context(), "bad")
	require.NoError(t, err)
	require.Nil(t, detail.Detail.BillingDecision)
	require.Equal(t, 1.25, detail.Detail.TotalCostUSD)
}
