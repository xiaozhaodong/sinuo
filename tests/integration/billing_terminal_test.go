package integration

import (
	"database/sql"
	"math"
	"path/filepath"
	"testing"
	"time"

	"sinuo/internal/proxy"
	"sinuo/internal/store"
	"sinuo/internal/tracking"

	"github.com/stretchr/testify/require"
)

func terminalBillingTracker(t *testing.T, path string, hot bool, fastMultiplier *float64) (*tracking.UsageTracker, *proxy.RequestLifecycleManager) {
	t.Helper()
	cfg := tracking.Config{Enabled: true, DatabasePath: path, BatchSize: 1, FlushInterval: 10 * time.Millisecond, HotPool: &tracking.HotPoolSettings{Enabled: hot}}
	if hot {
		cfg.BatchSize, cfg.FlushInterval = 100, time.Hour
	}
	tr, err := tracking.NewUsageTracker(&cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tr.Close()) })
	prices := store.NewSQLiteModelPricingStore(tr.GetWriteDB())
	price := func(v float64) *float64 { return &v }
	sheet := store.RateSheet{Rows: []store.RateRow{{Input: price(2), Output: price(0)}, {Fast: true, Input: price(6), Output: price(0)}}}
	ctx := t.Context()
	require.NoError(t, prices.PublishRateSheet(ctx, billingTestModel, store.RateSourceOfficial, sheet))
	channel := store.BillingChannel{Type: "endpoint", ID: 3, Name: "billing-endpoint"}
	if fastMultiplier != nil {
		require.NoError(t, prices.SaveChannelModelRule(ctx, channel, billingTestModel, store.ChannelModelRule{FastMultiplier: fastMultiplier}))
	}
	require.NoError(t, tr.LoadBaseRates(ctx, prices))
	r := proxy.NewRequestLifecycleManager(tr, nil, "billing-terminal", nil)
	r.SetModel(billingTestModel)
	r.SetRequestFamily(tracking.RequestFamilyClaude)
	r.StartRequest("127.0.0.1", "billing-review", "POST", "/v1/messages", true)
	r.SetEndpointAttempt(channel.Name, channel.ID, 1)
	r.SetFirstTokenStartTime(time.Now())
	return tr, r
}

func TestBillingTerminal_PartialUsageWithLateFast(t *testing.T) {
	for _, hot := range []bool{true, false} {
		t.Run(map[bool]string{true: "hot", false: "event"}[hot], func(t *testing.T) {
			tr, r := terminalBillingTracker(t, filepath.Join(t.TempDir(), "terminal.db"), hot, nil)
			id := r.GetRequestID()
			tr.RecordFailedRequestTokens(id, billingTestModel, &tracking.TokenUsage{InputTokens: 100000}, time.Millisecond, "partial")
			tr.RecordRequestFinalFailureWithDiagnostics(id, billingTestModel, "cancelled", "client", "", time.Second, 499, nil, &tracking.ResponseDiagnostics{ResponseReasoning: "speed:fast"})
			got := waitBillingRecord(t, tr, r, func(row *tracking.RequestDetail) bool { return row.Status == "cancelled" })
			require.True(t, got.UsageKnown)
			require.True(t, got.CostKnown)
			require.EqualValues(t, 100000, got.InputTokens)
			require.InDelta(t, .60, got.TotalCostUSD, 1e-12)
			require.NotNil(t, got.BillingDecision)
			require.Equal(t, "fast", got.BillingDecision.ResponseSpeed)
			require.InDelta(t, .60, got.InputCostUSD, 1e-12)
		})
	}
}

func TestBillingTerminal_FastZeroWithoutUsage(t *testing.T) {
	for _, hot := range []bool{true, false} {
		for _, tc := range []struct {
			name, response string
			free           bool
		}{
			{"confirmed", "speed:fast", true}, {"standard", "speed:standard", false}, {"unreported", "", false},
		} {
			t.Run(map[bool]string{true: "hot", false: "event"}[hot]+"/"+tc.name, func(t *testing.T) {
				zero := 0.0
				path := filepath.Join(t.TempDir(), "terminal.db")
				tr, r := terminalBillingTracker(t, path, hot, &zero)
				id := r.GetRequestID()
				tr.RecordRequestSuccessWithDiagnostics(id, billingTestModel, nil, time.Second, "", &tracking.ResponseDiagnostics{ResponseReasoning: tc.response})
				row := waitBillingRecord(t, tr, r, func(d *tracking.RequestDetail) bool { return d.Status == "completed" })
				require.False(t, row.UsageKnown)
				require.Equal(t, tc.free, row.CostKnown)
				require.Zero(t, row.TotalCostUSD)
				if tc.free {
					require.NotNil(t, row.BillingDecision)
					require.Zero(t, row.BillingDecision.Factor)
					require.Zero(t, row.BillingDecision.OfficialRow)
				}
				detail, err := tr.GetRequestLifecycleData(t.Context(), id)
				require.NoError(t, err)
				require.Equal(t, &tracking.OfficialRecalculationAvailability{Missing: "缺少计费用量"}, detail.OfficialRecalculation)
				_, err = tr.RecalculateOfficialCost(t.Context(), id)
				require.EqualError(t, err, detail.OfficialRecalculation.Missing)
				db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
				require.NoError(t, err)
				defer db.Close()
				require.NoError(t, tr.Close())
				var tokens sql.NullInt64
				var cost sql.NullFloat64
				require.NoError(t, db.QueryRow("SELECT input_tokens,total_cost_usd FROM request_logs WHERE request_id=?", id).Scan(&tokens, &cost))
				require.False(t, tokens.Valid)
				require.Equal(t, tc.free, cost.Valid)
				require.Zero(t, cost.Float64)
			})
		}
	}
}

func TestBillingTerminal_ResponsesCacheWrites(t *testing.T) {
	const payload = `{"usage":{"input_tokens":12000,"output_tokens":0,"input_tokens_details":{"cached_tokens":4000,"cache_write_tokens":7000,"cache_write_tokens_details":{"ttl_30m_tokens":3000,"ttl_12h_tokens":4000}}}}`
	const model = "gpt-5.6-luna"
	for _, mode := range []struct {
		name string
		hot  bool
	}{{"hot", true}, {"event", false}} {
		t.Run(mode.name, func(t *testing.T) {
			ctx := t.Context()
			path := filepath.Join(t.TempDir(), "writes.db")
			tr := openBillingCatalog(t, path, mode.hot)
			st := store.NewSQLiteModelPricingStore(tr.GetWriteDB())
			input, output, read, short, long := 1., 6., .1, 1.25, 2.5
			sheet := store.RateSheet{Rows: []store.RateRow{{Input: &input, Output: &output, CacheRead: &read, Cache30m: &short, Cache12h: &long}}}
			require.NoError(t, st.PublishRateSheet(ctx, model, store.RateSourceOfficial, sheet))
			require.NoError(t, tr.LoadBaseRates(ctx, st))
			channel := store.BillingChannel{Type: "account", ID: 7, Name: "API"}
			tr.UpdateAccountMultipliers(map[int64]tracking.EndpointMultiplier{8: {CacheCreationCostMultiplier: 2}})
			usage := proxy.ParseResponsesEventJSON("response.completed", []byte(payload)).TokenUsage
			require.NotNil(t, usage)
			parser := proxy.NewTokenParser()
			parser.ParseSSELineV2("event: response.completed")
			parser.ParseSSELineV2("data: " + payload)
			parsed := parser.ParseSSELineV2("")
			require.NotNil(t, parsed)
			require.Equal(t, usage, parsed.TokenUsage)
			check := func(id string, c store.BillingChannel, u *tracking.TokenUsage, want float64, known bool) {
				t.Helper()
				r := proxy.NewRequestLifecycleManager(tr, nil, id, nil)
				r.SetModel(model)
				r.StartRequest("127.0.0.1", "billing-test", "POST", "/v1/responses", true)
				at, ms := time.Now(), int64(1)
				tr.RecordRequestUpdate(id, tracking.UpdateOptions{FirstTokenStartAt: &at, UpstreamWriteMs: &ms, BillingChannel: c})
				switch id {
				case "partial":
					tr.RecordFailedRequestTokens(id, model, u, time.Millisecond, "partial")
					tr.RecordRequestFinalFailure(id, model, "cancelled", "client", "", time.Second, 499, nil)
				case "subscription":
					require.NoError(t, tr.RecordRequestSettlement(id, tracking.RequestSettlement{Status: "completed", ModelName: model, Path: "/v1/responses", TokenUsage: u}))
				default:
					r.CompleteRequest(u)
				}
				row := waitBillingRecord(t, tr, r, func(d *tracking.RequestDetail) bool { return d.Status == "completed" || d.Status == "cancelled" })
				require.Equal(t, known, row.CostKnown, id)
				require.Equal(t, u.CacheCreation30mTokens, row.CacheCreation30mTokens, id)
				require.Equal(t, u.CacheCreation12hTokens, row.CacheCreation12hTokens, id)
				detail, err := tr.GetRequestLifecycleData(ctx, id)
				require.NoError(t, err)
				require.Equal(t, known, detail.OfficialRecalculation.Available)
				switch id {
				case "split":
					require.Equal(t, &tracking.AppliedBillingMultipliers{Input: 1, CacheRead: 1, CacheCreation30m: 2, CacheCreation12h: 2}, detail.BillingMultipliers)
				case "subscription":
					require.Nil(t, detail.BillingMultipliers)
				case "api", "partial", "oauth":
					require.Equal(t, &tracking.AppliedBillingMultipliers{Unified: 1}, detail.BillingMultipliers)
				}
				if known {
					require.InDelta(t, want, row.TotalCostUSD, 1e-12, id)
					official, err := tr.RecalculateOfficialCost(ctx, id)
					require.NoError(t, err)
					require.InDelta(t, .01515, official.TotalCost, 1e-12)
				} else {
					require.Contains(t, row.BillingDecision.Missing, "缓存写入")
					require.Contains(t, detail.OfficialRecalculation.Missing, "缓存写入分项用量")
				}
			}
			check("api", channel, usage, .01515, true)
			check("partial", channel, usage, .01515, true)
			check("split", store.BillingChannel{Type: "account", ID: 8}, usage, .02890, true)
			check("subscription", store.BillingChannel{Type: "subscription", Name: "chatgpt-subscription"}, usage, .00840, true)
			check("oauth", store.BillingChannel{Type: "account", ID: 7, SubscriptionAccount: true}, usage, .00840, true)
			incomplete := *usage
			incomplete.CacheCreation12hTokens = nil
			check("missing", channel, &incomplete, 0, false)
			inconsistent := *usage
			inconsistent.CacheCreationTokens--
			check("mismatch", channel, &inconsistent, 0, false)
			require.NoError(t, tr.Close())
			again := openBillingCatalog(t, path, false)
			st = store.NewSQLiteModelPricingStore(again.GetWriteDB())
			long = 25
			require.NoError(t, st.PublishRateSheet(ctx, model, store.RateSourceOfficial, sheet))
			require.NoError(t, again.LoadBaseRates(ctx, st))
			for id, amount := range map[string]float64{"api": .01515, "partial": .01515, "subscription": .00840, "oauth": .00840, "split": .02890, "missing": 0, "mismatch": 0} {
				var shortCount, longCount sql.NullInt64
				var cost sql.NullFloat64
				require.NoError(t, again.GetReadDB().QueryRow("SELECT cache_creation_30m_tokens,cache_creation_12h_tokens,total_cost_usd FROM request_logs WHERE request_id=?", id).Scan(&shortCount, &longCount, &cost))
				require.EqualValues(t, 3000, shortCount.Int64)
				require.Equal(t, id != "missing", longCount.Valid)
				ref, err := again.RecalculateOfficialCost(ctx, id)
				if amount == 0 {
					require.False(t, cost.Valid)
					require.Error(t, err)
				} else {
					require.NoError(t, err)
					require.EqualValues(t, 4000, longCount.Int64)
					require.InDelta(t, amount, cost.Float64, 1e-12)
					require.InDelta(t, .01515, ref.TotalCost, 1e-12)
				}
				archivedDetail, err := again.GetRequestLifecycleData(ctx, id)
				require.NoError(t, err)
				switch id {
				case "subscription":
					require.Nil(t, archivedDetail.BillingMultipliers)
				case "api", "partial", "oauth":
					require.Equal(t, &tracking.AppliedBillingMultipliers{Unified: 1}, archivedDetail.BillingMultipliers)
				case "split":
					require.Equal(t, &tracking.AppliedBillingMultipliers{Input: 1, CacheRead: 1, CacheCreation30m: 2, CacheCreation12h: 2}, archivedDetail.BillingMultipliers)
				}
			}
			// 新版本显式零价依旧可结算，另一时长单价缺失只影响实际用了该时长的请求。
			zero, count := 0., int64(7000)
			sheet.Rows[0].Cache30m, sheet.Rows[0].Cache12h = &zero, nil
			require.NoError(t, st.PublishRateSheet(ctx, model, store.RateSourceOfficial, sheet))
			require.NoError(t, again.LoadBaseRates(ctx, st))
			onlyShort := *usage
			zeroCount := int64(0)
			onlyShort.CacheCreation30mTokens, onlyShort.CacheCreation12hTokens = &count, &zeroCount
			for _, item := range []struct {
				id    string
				usage *tracking.TokenUsage
				known bool
			}{
				{"zero-write-rate", &onlyShort, true}, {"missing-rate", usage, false},
			} {
				r := proxy.NewRequestLifecycleManager(again, nil, item.id, nil)
				r.SetModel(model)
				r.StartRequest("127.0.0.1", "billing-test", "POST", "/v1/responses", false)
				r.CompleteRequest(item.usage)
				row := waitBillingRecord(t, again, r, func(d *tracking.RequestDetail) bool { return d.Status == "completed" })
				require.Equal(t, item.known, row.CostKnown)
				if item.known {
					require.InDelta(t, .0014, row.TotalCostUSD, 1e-12)
				} else {
					detail, err := again.GetRequestLifecycleData(ctx, item.id)
					require.NoError(t, err)
					require.Equal(t, &tracking.OfficialRecalculationAvailability{Missing: "缺少 12h 缓存写入单价"}, detail.OfficialRecalculation)
				}
			}
			again.RecoverRequestTokens("missing", model, usage)
			require.Eventually(t, func() bool {
				ref, err := again.RecalculateOfficialCost(ctx, "missing")
				return err == nil && math.Abs(ref.TotalCost-.01515) < 1e-12
			}, 2*time.Second, 10*time.Millisecond)
			again.RecoverRequestTokens("missing", model, &incomplete)
			require.Eventually(t, func() bool {
				var blank bool
				return again.GetReadDB().QueryRow("SELECT cache_creation_12h_tokens IS NULL AND total_cost_usd IS NULL FROM request_logs WHERE request_id='missing'").Scan(&blank) == nil && blank
			}, 2*time.Second, 10*time.Millisecond)
		})
	}
}
