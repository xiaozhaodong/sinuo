package integration

import (
	"database/sql"
	"os"
	"path/filepath"
	"sinuo/internal/proxy"
	"strings"
	"testing"
	"time"

	"sinuo/internal/store"
	"sinuo/internal/tracking"

	"github.com/stretchr/testify/require"
)

func TestBillingChannel_LastWrittenAccountSurvivesUnsentRetry(t *testing.T) {
	tracker, err := tracking.NewUsageTracker(&tracking.Config{Enabled: true, DatabasePath: filepath.Join(t.TempDir(), "usage.db")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tracker.Close()) })
	prices := store.NewSQLiteModelPricingStore(tracker.GetWriteDB())
	_, err = prices.Create(t.Context(), &store.ModelPricingRecord{ModelName: billingTestModel, InputPrice: 2})
	require.NoError(t, err)
	require.NoError(t, tracker.LoadBaseRates(t.Context(), prices))
	tracker.UpdateAccountMultipliers(map[int64]tracking.EndpointMultiplier{11: {CostMultiplier: .5}, 22: {CostMultiplier: 3}})
	req := startBillingRequest(t, tracker, "failover", false)
	req.SetUpstream("account", "pool", "written", 11)
	req.SetFirstTokenStartTime(time.Now())
	req.SetUpstream("account", "pool", "unsent", 22)
	req.CompleteRequest(&tracking.TokenUsage{InputTokens: 100000})
	detail := waitBillingRecord(t, tracker, req, func(r *tracking.RequestDetail) bool { return r.Status == "completed" })
	require.InDelta(t, .1, detail.TotalCostUSD, 1e-9)
}

func TestBillingChannel_FastLongSettlement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "matrix.db")
	tracker, err := tracking.NewUsageTracker(&tracking.Config{Enabled: true, DatabasePath: path, BatchSize: 100, FlushInterval: time.Hour})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tracker.Close()) })
	ctx := t.Context()
	prices := store.NewSQLiteModelPricingStore(tracker.GetWriteDB())
	sheet := store.RateSheet{ContextLimits: []int64{200000}}
	number := func(v float64) *float64 { return &v }
	for i, v := range []float64{2, 3, 4, 7} {
		sheet.Rows = append(sheet.Rows, store.RateRow{Tier: i / 2, Fast: i%2 == 1, Input: number(v), Output: number(0)})
	}
	require.NoError(t, prices.PublishRateSheet(ctx, billingTestModel, store.RateSourceOfficial, sheet))
	channel := store.BillingChannel{Type: "endpoint", ID: 5, Name: "test-endpoint"}
	tracker.UpdateEndpointMultipliers(map[string]tracking.EndpointMultiplier{channel.Name: {CostMultiplier: .8}})
	var ids []string
	for _, tc := range []struct {
		fast, long *float64
		want       float64
	}{{nil, nil, 1.4}, {number(1), nil, .8}, {number(2), nil, 1.6}, {nil, number(1.5), .9}, {number(2), number(1.5), 1.2}, {number(0), nil, 0}} {
		rule := store.ChannelModelRule{FastMultiplier: tc.fast, Tiers: []store.ChannelTier{{Multiplier: tc.long}}}
		require.NoError(t, prices.SaveChannelModelRule(ctx, channel, billingTestModel, rule))
		require.NoError(t, tracker.LoadBaseRates(ctx, prices))
		req := startBillingRequest(t, tracker, "matrix", false)
		req.SetEndpointAttempt(channel.Name, channel.ID, 1)
		req.SetFirstTokenStartTime(time.Now())
		req.SetResponseReasoning("speed:fast")
		req.CompleteRequest(&tracking.TokenUsage{InputTokens: 250000})
		detail := waitBillingRecord(t, tracker, req, func(r *tracking.RequestDetail) bool { return r.Status == "completed" })
		require.True(t, detail.CostKnown)
		id := req.GetRequestID()
		var archived int
		require.NoError(t, tracker.GetReadDB().QueryRow("SELECT COUNT(*) FROM request_logs WHERE request_id=?", id).Scan(&archived))
		require.Zero(t, archived)
		ref, err := tracker.RecalculateOfficialCost(ctx, id)
		require.NoError(t, err)
		require.InDelta(t, 1.75, ref.TotalCost, 1e-9)
		require.Equal(t, detail.BillingDecision.OfficialRow, ref.Decision.OfficialRow)
		ids = append(ids, id)
		require.InDelta(t, tc.want, detail.TotalCostUSD, 1e-9)
	}
	tracker.UpdateEndpointMultipliers(map[string]tracking.EndpointMultiplier{channel.Name: {CostMultiplier: 4}})
	for _, id := range ids {
		detail, err := tracker.GetRequestLifecycleData(ctx, id)
		require.NoError(t, err)
		require.Equal(t, "hot_pool", detail.Source)
		require.True(t, detail.OfficialRecalculation.Available)
		require.Equal(t, &tracking.AppliedBillingMultipliers{Unified: .8}, detail.BillingMultipliers)
	}
	ro, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	require.NoError(t, err)
	defer ro.Close()
	require.NoError(t, tracker.Close())
	again, err := tracking.NewUsageTracker(&tracking.Config{Enabled: true, DatabasePath: path})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, again.Close()) })
	require.NoError(t, again.LoadBaseRates(ctx, store.NewSQLiteModelPricingStore(again.GetWriteDB())))
	for i, id := range ids {
		var total float64
		require.NoError(t, ro.QueryRow("SELECT total_cost_usd FROM request_logs WHERE request_id=?", id).Scan(&total))
		require.InDelta(t, []float64{1.4, .8, 1.6, .9, 1.2, 0}[i], total, 1e-9)
		ref, err := again.RecalculateOfficialBaseCost(ctx, id)
		require.NoError(t, err)
		require.InDelta(t, 1.75, ref.TotalCost, 1e-9)
		detail, err := again.GetRequestLifecycleData(ctx, id)
		require.NoError(t, err)
		require.Equal(t, "database", detail.Source)
		require.True(t, detail.OfficialRecalculation.Available)
		require.Equal(t, &tracking.AppliedBillingMultipliers{Unified: .8}, detail.BillingMultipliers)
	}
}

func TestBillingChannel_LongTierZeroWithUnsupportedSpeed(t *testing.T) {
	for _, mode := range []struct {
		name string
		hot  bool
	}{{"hot", true}, {"event", false}} {
		t.Run(mode.name, func(t *testing.T) {
			ctx := t.Context()
			tr := openBillingCatalog(t, filepath.Join(t.TempDir(), "flex.db"), mode.hot)
			prices := store.NewSQLiteModelPricingStore(tr.GetWriteDB())
			input, zero, two := 2., 0., 2.
			low, high := int64(100), int64(200)
			sheet := store.RateSheet{Rows: []store.RateRow{{Input: &input, Output: &input}}}
			require.NoError(t, prices.PublishRateSheet(ctx, billingTestModel, store.RateSourceOfficial, sheet))
			channel := store.BillingChannel{Type: "account", ID: 5}
			rule := store.ChannelModelRule{Tiers: []store.ChannelTier{
				{Threshold: &low, Multiplier: &zero},
				{Threshold: &high, Multiplier: &two},
			}}
			require.NoError(t, prices.SaveChannelModelRule(ctx, channel, billingTestModel, rule))
			require.NoError(t, tr.LoadBaseRates(ctx, prices))

			for _, tc := range []struct {
				name, speed string
				tokens      int64
				tier        int
				free        bool
			}{
				{"at-threshold", "flex", 100, 0, false},
				{"free-flex", "flex", 101, 1, true},
				{"higher-paid-tier", "flex", 201, 2, false},
				{"free-standard", "default", 101, 1, true},
			} {
				req := proxy.NewRequestLifecycleManager(tr, nil, tc.name, nil)
				req.SetModel(billingTestModel)
				req.StartRequest("127.0.0.1", "billing-test", "POST", "/v1/responses", false)
				req.SetUpstream("account", "pool", "billing-test", channel.ID)
				req.SetFirstTokenStartTime(time.Now())
				req.SetResponseReasoning("speed:" + tc.speed)
				req.CompleteRequest(&tracking.TokenUsage{InputTokens: tc.tokens, OutputTokens: 10})
				got := waitBillingRecord(t, tr, req, func(r *tracking.RequestDetail) bool { return r.Status == "completed" })
				require.Equal(t, tc.tier, got.BillingDecision.ChannelTier, tc.name)
				require.Equal(t, tc.free, got.CostKnown, tc.name)
				if tc.free {
					require.Zero(t, got.TotalCostUSD)
					require.Zero(t, got.BillingDecision.Factor)
					require.Empty(t, got.BillingDecision.Missing)
				} else {
					require.Contains(t, got.BillingDecision.Missing, tc.speed)
				}
				ref, err := tr.RecalculateOfficialCost(ctx, req.GetRequestID())
				if tc.speed == "flex" {
					require.ErrorContains(t, err, "官方价表未涵盖响应服务档位 flex")
				} else {
					require.NoError(t, err)
					require.InDelta(t, float64(tc.tokens+10)*input/1e6, ref.TotalCost, 1e-12)
				}
			}
		})
	}
}

func TestBillingChannel_OfficialAvailabilityIndependentOfChannelCost(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "official.db")
	tracker, err := tracking.NewUsageTracker(&tracking.Config{Enabled: true, DatabasePath: path, BatchSize: 100, FlushInterval: time.Hour})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tracker.Close()) })
	prices := store.NewSQLiteModelPricingStore(tracker.GetWriteDB())
	input, fast, output, cacheRead := 2., 6., 0., .1
	longLimit := int64(10)
	standardRow := store.RateRow{Input: &input, Output: &output, CacheRead: &cacheRead}
	fastRow := store.RateRow{Fast: true, Input: &fast, Output: &output}
	peak, valley := standardRow, standardRow
	peak.Period, valley.Period = "peak", "valley"
	one := 1.
	channel := store.BillingChannel{Type: "endpoint", ID: 5, Name: "billing-test"}
	cases := []struct {
		model, response, missing string
		sheet                    store.RateSheet
		rule                     store.ChannelModelRule
		written                  bool
		usage                    tracking.TokenUsage
	}{
		{"speed", "", "缺少实际服务档位", store.RateSheet{Rows: []store.RateRow{standardRow, fastRow}}, store.ChannelModelRule{FastMultiplier: &one}, true, tracking.TokenUsage{InputTokens: 100000}},
		{"row", "standard", "缺少当时的官方费率行", store.RateSheet{ContextLimits: []int64{longLimit}, Rows: []store.RateRow{standardRow}}, store.ChannelModelRule{Tiers: []store.ChannelTier{{Multiplier: &one}}}, true, tracking.TokenUsage{InputTokens: 100000}},
		{"period", "standard", "缺少上游写出时间", store.RateSheet{Timezone: "UTC", Periods: []store.RatePeriod{{Name: "peak", StartMinute: 0}, {Name: "valley", StartMinute: 720}}, Rows: []store.RateRow{peak, valley}}, store.ChannelModelRule{Period: "peak"}, false, tracking.TokenUsage{InputTokens: 100000}},
		{"cache", "fast", "缺少缓存读取单价", store.RateSheet{Rows: []store.RateRow{standardRow, fastRow}}, store.ChannelModelRule{FastMultiplier: &one}, true, tracking.TokenUsage{InputTokens: 100000, CacheReadTokens: 100}},
	}
	for _, tc := range cases {
		require.NoError(t, prices.PublishRateSheet(ctx, tc.model, store.RateSourceOfficial, tc.sheet))
		require.NoError(t, prices.SaveChannelModelRule(ctx, channel, tc.model, tc.rule))
	}
	require.NoError(t, tracker.LoadBaseRates(ctx, prices))
	check := func(tr *tracking.UsageTracker, source, requestID, missing string) {
		t.Helper()
		detail, err := tr.GetRequestLifecycleData(ctx, requestID)
		require.NoError(t, err)
		require.Equal(t, source, detail.Source)
		require.True(t, detail.Detail.CostKnown, requestID)
		require.Empty(t, detail.Detail.BillingDecision.Missing)
		require.False(t, detail.OfficialRecalculation.Available)
		require.Contains(t, detail.OfficialRecalculation.Missing, missing)
		_, err = tr.RecalculateOfficialCost(ctx, requestID)
		require.EqualError(t, err, detail.OfficialRecalculation.Missing)
	}
	for _, tc := range cases {
		req := proxy.NewRequestLifecycleManager(tracker, nil, tc.model, nil)
		req.SetModel(tc.model)
		req.StartRequest("127.0.0.1", "billing-test", "POST", "/v1/messages", false)
		req.SetEndpointAttempt(channel.Name, channel.ID, 1)
		if tc.written {
			req.SetFirstTokenStartTime(time.Now())
		}
		if tc.response != "" {
			req.SetResponseReasoning("speed:" + tc.response)
		}
		req.CompleteRequest(&tc.usage)
		check(tracker, "hot_pool", tc.model, tc.missing)
	}
	require.NoError(t, tracker.Close())
	archived := openBillingCatalog(t, path, false)
	for _, tc := range cases {
		check(archived, "database", tc.model, tc.missing)
	}
}

func TestBillingChannel_HistoricalFlatAvailabilityAndMultiplierPresence(t *testing.T) {
	ctx := t.Context()
	tr := openBillingCatalog(t, filepath.Join(t.TempDir(), "old.db"), false)
	const out = 0
	db := tr.GetWriteDB()
	for _, source := range []string{store.RateSourceOfficial, store.RateSourceCustom, "legacy"} {
		res, err := db.Exec(`INSERT INTO model_base_rates(model_name,source,effective_from_ns,input_price,output_price) VALUES(?,?,?,?,?)`, source, source, 1, 2, out)
		require.NoError(t, err)
		id, err := res.LastInsertId()
		require.NoError(t, err)
		_, err = db.Exec(`INSERT INTO request_logs(request_id,start_time,status,input_tokens,billing_rate_id,billing_basis)
			VALUES(?,'2026-01-01T00:00:00.000000Z','completed',100000,?,'{"decision":{"model_multiplier":1}}')`, source, id)
		require.NoError(t, err)
	}
	for id, missing := range map[string]string{store.RateSourceOfficial: "", store.RateSourceCustom: "自定义价", "legacy": "旧版费率"} {
		detail, err := tr.GetRequestLifecycleData(ctx, id)
		require.NoError(t, err)
		require.Nil(t, detail.BillingMultipliers)
		require.Zero(t, detail.Detail.BillingDecision.OfficialRow)
		require.Equal(t, missing == "", detail.OfficialRecalculation.Available)
		ref, err := tr.RecalculateOfficialCost(ctx, id)
		if missing == "" {
			require.NoError(t, err)
			require.InDelta(t, .2, ref.TotalCost, 1e-9)
		} else {
			require.Contains(t, detail.OfficialRecalculation.Missing, missing)
			require.EqualError(t, err, detail.OfficialRecalculation.Missing)
		}
	}
}

func TestBillingChannel_PeriodThresholdAndEstimatedSpeed(t *testing.T) {
	for _, hot := range []bool{true, false} {
		name := "event"
		if hot {
			name = "hot"
		}
		t.Run(name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "channel.db")
			tracker, err := tracking.NewUsageTracker(&tracking.Config{Enabled: true, DatabasePath: dbPath, HotPool: &tracking.HotPoolSettings{Enabled: hot}, BatchSize: 1, FlushInterval: time.Millisecond * 20})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, tracker.Close()) })
			ctx := t.Context()
			prices := store.NewSQLiteModelPricingStore(tracker.GetWriteDB())
			n := func(v float64) *float64 { return &v }
			sheet := store.RateSheet{Timezone: "Asia/Shanghai", Periods: []store.RatePeriod{{Name: "peak", StartMinute: 8 * 60}, {Name: "valley", StartMinute: 22 * 60}}, ContextLimits: []int64{200000}}
			for _, period := range sheet.Periods {
				for tier := 0; tier < 2; tier++ {
					for _, fast := range []bool{false, true} {
						price := 2.0
						if period.Name == "peak" {
							price += 2
						}
						if tier == 1 {
							price *= 2
						}
						if fast {
							price *= 3
						}
						sheet.Rows = append(sheet.Rows, store.RateRow{Period: period.Name, Tier: tier, Fast: fast, Input: n(price), Output: n(0), CacheRead: n(price)})
					}
				}
			}
			require.NoError(t, prices.PublishRateSheet(ctx, billingTestModel, store.RateSourceOfficial, sheet))
			limit := int64(128000)
			codex := store.BillingChannel{Type: "subscription", Name: "chatgpt-subscription"}
			require.NoError(t, prices.SaveChannelModelRule(ctx, codex, billingTestModel, store.ChannelModelRule{Period: "peak", Tiers: []store.ChannelTier{{Threshold: &limit}}}))
			require.NoError(t, tracker.LoadBaseRates(ctx, prices))
			loc, err := time.LoadLocation(sheet.Timezone)
			require.NoError(t, err)
			// 取下一次 01:00，确保调用实际写出时刻落在已发布版本的跨日谷段。
			tomorrow := time.Now().In(loc).AddDate(0, 0, 1)
			at := time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 1, 0, 0, 0, loc)
			for _, tc := range []struct {
				id                        string
				c                         store.BillingChannel
				path, requested, response string
				input                     int64
				want, official            float64
				known, estimated          bool
			}{
				{"estimated", codex, "/v1/responses", "priority", "default", 150000, 3.6, .9, true, true},
				{"boundary", codex, "/v1/responses", "", "default", 128000, .512, .256, true, true},
				{"next", codex, "/v1/responses", "", "default", 128001, 1.024008, .256002, true, true},
				{"public", store.BillingChannel{Type: "account", ID: 21, Name: "public"}, "/v1/responses", "priority", "default", 150000, .3, .3, true, false},
				{"claude", store.BillingChannel{Type: "subscription", Name: "claude-subscription"}, "/v1/messages", "fast", "fast", 150000, .9, .9, true, false},
				{"missing", codex, "/v1/responses", "", "", 150000, 0, 0, false, false},
			} {
				request := proxy.NewRequestLifecycleManager(tracker, nil, tc.id, nil)
				request.SetModel(billingTestModel)
				requested := ""
				if tc.requested != "" {
					requested = "speed:" + tc.requested
				}
				request.SetRequestProfile(billingTestModel, requested)
				request.StartRequest("127.0.0.1", "billing-test", "POST", tc.path, false)
				request.SetUpstream(tc.c.Type, "test", tc.c.Name, tc.c.ID)
				request.SetFirstTokenStartTime(at)
				if tc.response != "" {
					request.SetResponseReasoning("speed:" + tc.response)
				}
				request.CompleteRequest(&tracking.TokenUsage{InputTokens: tc.input})
				detail := waitBillingRecord(t, tracker, request, func(r *tracking.RequestDetail) bool { return r.Status == "completed" })
				require.Equal(t, tc.known, detail.CostKnown, tc.id)
				require.NotNil(t, detail.BillingDecision)
				require.Equal(t, tc.estimated, detail.BillingDecision.Estimated, tc.id)
				require.Equal(t, tc.c, detail.BillingDecision.Channel)
				require.InDelta(t, tc.want, detail.TotalCostUSD, 1e-9, tc.id)
			}
			require.NoError(t, tracker.Close())
			again, err := tracking.NewUsageTracker(&tracking.Config{Enabled: true, DatabasePath: dbPath})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, again.Close()) })
			st := store.NewSQLiteModelPricingStore(again.GetWriteDB())
			require.NoError(t, st.SaveChannelModelRule(ctx, codex, billingTestModel, store.ChannelModelRule{}))
			require.NoError(t, again.LoadBaseRates(ctx, st))
			for id, want := range map[string]float64{"estimated": .9, "boundary": .256, "next": .256002, "public": .3, "claude": .9} {
				ref, err := again.RecalculateOfficialBaseCost(ctx, id)
				require.NoError(t, err)
				require.InDelta(t, want, ref.TotalCost, 1e-9, id)
			}
			_, err = again.RecalculateOfficialBaseCost(ctx, "missing")
			require.Error(t, err)
			again.RecoverRequestTokens("estimated", billingTestModel, &tracking.TokenUsage{InputTokens: 250000})
			reader, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
			require.NoError(t, err)
			defer reader.Close()
			require.NoError(t, again.Close())
			var amount float64
			var officialTier, channelTier int
			require.NoError(t, reader.QueryRow(`SELECT total_cost_usd,json_extract(billing_basis,'$.decision.official_tier'),json_extract(billing_basis,'$.decision.channel_tier') FROM request_logs WHERE request_id='estimated'`).Scan(&amount, &officialTier, &channelTier))
			require.InDelta(t, 6, amount, 1e-9)
			require.Equal(t, 1, officialTier)
			require.Equal(t, 1, channelTier)
		})
	}
}

func TestBillingChannel_WeeklyRateSheet(t *testing.T) {
	tracker, err := tracking.NewUsageTracker(&tracking.Config{Enabled: true, DatabasePath: filepath.Join(t.TempDir(), "weekly.db")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tracker.Close()) })
	prices := store.NewSQLiteModelPricingStore(tracker.GetWriteDB())
	low, peak := .15, .30
	week := []int{1, 2, 3, 4, 5}
	sheet := store.RateSheet{
		Timezone: "Asia/Shanghai",
		Periods: []store.RatePeriod{{Name: "低峰", Default: true}, {Name: "高峰", Windows: []store.RateWindow{
			{Weekdays: week, StartMinute: 540, EndMinute: 720}, {Weekdays: week, StartMinute: 840, EndMinute: 1080},
		}}},
		Rows: []store.RateRow{{Period: "低峰", Input: &low, Output: &low}, {Period: "高峰", Input: &peak, Output: &peak}},
	}
	require.NoError(t, prices.PublishRateSheet(t.Context(), billingTestModel, store.RateSourceCustom, sheet))
	require.NoError(t, tracker.LoadBaseRates(t.Context(), prices))
	loc, err := time.LoadLocation(sheet.Timezone)
	require.NoError(t, err)
	now := time.Now().In(loc)
	nextFriday := now.AddDate(0, 0, 1+(11-int(now.Weekday()))%7)
	at := time.Date(nextFriday.Year(), nextFriday.Month(), nextFriday.Day(), 10, 0, 0, 0, loc)
	require.Equal(t, time.Friday, at.Weekday())
	for _, tc := range []struct {
		name  string
		days  int
		price float64
	}{{"高峰", 0, peak}, {"低峰", 1, low}} {
		req := startBillingRequest(t, tracker, tc.name, true)
		req.SetUpstream("endpoint", "test", "test-endpoint", 5)
		req.SetFirstTokenStartTime(at.AddDate(0, 0, tc.days))
		req.CompleteRequest(&tracking.TokenUsage{InputTokens: 1000000})
		detail := waitBillingRecord(t, tracker, req, func(r *tracking.RequestDetail) bool { return r.Status == "completed" })
		require.True(t, detail.CostKnown)
		require.Equal(t, tc.name, detail.BillingDecision.ChannelPeriod)
		require.InDelta(t, tc.price, detail.TotalCostUSD, 1e-9)
	}
}

func TestBillingChannel_MigratesEndpointMultiplierWithoutChangingRuntimeState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	schema, err := os.ReadFile("../../internal/tracking/schema.sql")
	require.NoError(t, err)
	historical := strings.Replace(string(schema), "CHECK (cost_multiplier >= 0)", "CHECK (cost_multiplier > 0)", 1)
	require.NotEqual(t, string(schema), historical)
	_, err = db.Exec(historical)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO endpoints(id,name,url,cost_multiplier,updated_at) VALUES(71,'old','http://127.0.0.1:1',1.7,'2020-01-01T00:00:00Z')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO endpoint_runtime_states(endpoint_id,scope,state,revision) VALUES(71,'global','active',9)`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	tr, err := tracking.NewUsageTracker(&tracking.Config{Enabled: true, DatabasePath: path})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tr.Close()) })
	db = tr.GetWriteDB()
	var m float64
	var rev int
	var stamp string
	require.NoError(t, db.QueryRow(`SELECT cost_multiplier,CAST(e.updated_at AS TEXT),s.revision FROM endpoints e JOIN endpoint_runtime_states s ON e.id=s.endpoint_id WHERE e.id=71`).Scan(&m, &stamp, &rev))
	require.Equal(t, 1.7, m)
	require.Equal(t, 9, rev)
	require.Equal(t, "2020-01-01T00:00:00Z", stamp)
	_, err = db.Exec("UPDATE endpoints SET cost_multiplier=0 WHERE id=71")
	require.NoError(t, err)
	require.NoError(t, db.QueryRow("SELECT CAST(updated_at AS TEXT) FROM endpoints WHERE id=71").Scan(&stamp))
	require.NotEqual(t, "2020-01-01T00:00:00Z", stamp)
	require.NoError(t, tr.Close())
	again, err := tracking.NewUsageTracker(&tracking.Config{Enabled: true, DatabasePath: path})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, again.Close()) })
	db = again.GetWriteDB()
	var reopenedStamp string
	require.NoError(t, db.QueryRow("SELECT cost_multiplier,CAST(updated_at AS TEXT) FROM endpoints WHERE id=71").Scan(&m, &reopenedStamp))
	require.Zero(t, m)
	require.Equal(t, stamp, reopenedStamp)
	_, err = db.Exec("UPDATE endpoints SET cost_multiplier=0 WHERE id=71")
	require.NoError(t, err)
	_, err = db.Exec("UPDATE endpoints SET cost_multiplier=-1 WHERE id=71")
	require.Error(t, err)
}

func TestBillingChannel_PublishAfterChannelDeletion(t *testing.T) {
	ctx := t.Context()
	tr, err := tracking.NewUsageTracker(&tracking.Config{Enabled: true, DatabasePath: filepath.Join(t.TempDir(), "publish.db"), HotPool: &tracking.HotPoolSettings{Enabled: false}, BatchSize: 1, FlushInterval: 10 * time.Millisecond})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tr.Close()) })
	db := tr.GetWriteDB()
	prices := store.NewSQLiteModelPricingStore(db)
	endpoints := store.NewSQLiteEndpointStore(db)
	accounts := store.NewSQLiteAccountPoolStore(db)
	ep, err := endpoints.Create(ctx, &store.EndpointRecord{Name: "retired", URL: "http://127.0.0.1:1"})
	require.NoError(t, err)
	account, err := accounts.CreateAccount(ctx, &store.UpstreamAccountRecord{AccountName: "retired", ProviderType: "api_key", Enabled: false})
	require.NoError(t, err)
	channels := []store.BillingChannel{{Type: "endpoint", ID: ep.ID, Name: ep.Name}, {Type: "account", ID: account.ID, Name: account.AccountName}, {Type: "subscription", Name: "chatgpt-subscription"}}
	v := 2.0
	old := store.RateSheet{Timezone: "UTC", Periods: []store.RatePeriod{{Name: "peak"}}, Rows: []store.RateRow{{Period: "peak", Input: &v, Output: &v}}}
	next := store.RateSheet{Rows: []store.RateRow{{Input: &v, Output: &v}}}
	for _, c := range channels {
		t.Run(c.Type, func(t *testing.T) {
			model := billingTestModel + "-" + c.Type
			require.NoError(t, prices.PublishRateSheet(ctx, model, store.RateSourceOfficial, old))
			rule := store.ChannelModelRule{Period: "peak"}
			require.NoError(t, prices.SaveChannelModelRule(ctx, c, model, rule))
			require.ErrorContains(t, prices.PublishRateSheet(ctx, model, store.RateSourceOfficial, next), "固定时段")
			require.NoError(t, tr.LoadBaseRates(ctx, prices))
			req := proxy.NewRequestLifecycleManager(tr, nil, "before-delete-"+c.Type, nil)
			req.SetModel(model)
			req.StartRequest("127.0.0.1", "billing-review", "POST", "/v1/messages", false)
			req.SetUpstream(c.Type, "billing-review", c.Name, c.ID)
			req.SetFirstTokenStartTime(time.Now())
			req.CompleteRequest(&tracking.TokenUsage{InputTokens: 100000})
			detail := waitBillingRecord(t, tr, req, func(row *tracking.RequestDetail) bool { return row.Status == "completed" })
			require.True(t, detail.CostKnown)
			require.InDelta(t, .2, detail.TotalCostUSD, 1e-12)
			require.Equal(t, "peak", detail.BillingDecision.ChannelPeriod)
			switch c.Type {
			case "endpoint":
				require.NoError(t, endpoints.Delete(ctx, ep.Name))
				replacement, err := endpoints.Create(ctx, &store.EndpointRecord{Name: ep.Name, URL: ep.URL})
				require.NoError(t, err)
				require.NotEqual(t, ep.ID, replacement.ID)
			case "account":
				require.NoError(t, accounts.DeleteAccount(ctx, account.ID))
			default:
				require.NoError(t, prices.SaveChannelModelRule(ctx, c, model, store.ChannelModelRule{}))
			}
			require.NoError(t, prices.PublishRateSheet(ctx, model, store.RateSourceOfficial, next))
			historical, err := prices.ListChannelRules(ctx)
			require.NoError(t, err)
			require.Contains(t, historical, store.ChannelRuleVersion{ID: detail.BillingDecision.RuleID, Channel: c.Key(), Model: model, Rule: rule})
			require.NoError(t, tr.LoadBaseRates(ctx, prices))
			ref, err := tr.RecalculateOfficialCost(ctx, req.GetRequestID())
			require.NoError(t, err)
			require.InDelta(t, .2, ref.TotalCost, 1e-12)
			require.Equal(t, "peak", ref.Decision.OfficialPeriod)
		})
	}
}

func TestBillingChannel_MissingModelRuleExplainsHowToSetPrice(t *testing.T) {
	tr := openBillingCatalog(t, filepath.Join(t.TempDir(), "rules.db"), false)
	st := store.NewSQLiteModelPricingStore(tr.GetWriteDB())
	c := store.BillingChannel{Type: "endpoint", ID: 5}
	v := 2.0
	err := st.SaveChannelModelRule(t.Context(), c, "missing", store.ChannelModelRule{FastMultiplier: &v})
	require.ErrorIs(t, err, store.ErrModelRateMissing)
	require.EqualError(t, err, "该模型尚未配置价表，请先在基础定价页添加")
	require.NoError(t, st.SaveChannelModelRule(t.Context(), c, "missing", store.ChannelModelRule{}))
}

func TestBillingChannel_OAuthSubscriptionAccountEstimatesSpeed(t *testing.T) {
	ctx := t.Context()
	tr := openBillingCatalog(t, filepath.Join(t.TempDir(), "oauth-speed.db"), true)
	st := store.NewSQLiteModelPricingStore(tr.GetWriteDB())
	n := func(v float64) *float64 { return &v }
	sheet := store.RateSheet{Rows: []store.RateRow{
		{Input: n(2), Output: n(0)},
		{Fast: true, Input: n(6), Output: n(0)},
	}}
	require.NoError(t, st.PublishRateSheet(ctx, billingTestModel, store.RateSourceOfficial, sheet))
	require.NoError(t, tr.LoadBaseRates(ctx, st))
	oauthChannel := store.BillingChannel{Type: "account", ID: 9, Name: "oauth-sub", SubscriptionAccount: true}
	for _, tc := range []struct {
		id, reqSpeed, respSpeed, wantSpeed string
		wantCost                           float64
	}{
		{"oauth-fast", "priority", "default", "fast", .6},
		{"oauth-standard", "standard", "default", "standard", .2},
	} {
		req := proxy.NewRequestLifecycleManager(tr, nil, tc.id, nil)
		req.SetModel(billingTestModel)
		req.SetRequestProfile(billingTestModel, "speed:"+tc.reqSpeed)
		req.StartRequest("127.0.0.1", "oauth-test", "POST", "/v1/responses", false)
		at, ms := time.Now(), int64(1)
		tr.RecordRequestUpdate(tc.id, tracking.UpdateOptions{FirstTokenStartAt: &at, UpstreamWriteMs: &ms, BillingChannel: oauthChannel})
		req.SetResponseReasoning("speed:" + tc.respSpeed)
		req.CompleteRequest(&tracking.TokenUsage{InputTokens: 100000})
		got := waitBillingRecord(t, tr, req, func(d *tracking.RequestDetail) bool { return d.Status == "completed" })
		require.True(t, got.CostKnown, tc.id)
		require.NotNil(t, got.BillingDecision)
		require.Equal(t, tc.wantSpeed, got.BillingDecision.Speed, tc.id)
		require.True(t, got.BillingDecision.Estimated, tc.id)
		require.InDelta(t, tc.wantCost, got.TotalCostUSD, 1e-12, tc.id)
	}
}

func TestBillingChannel_EventModeFreezesBillingBasisAfterFirstToken(t *testing.T) {
	ctx := t.Context()
	tr := openBillingCatalog(t, filepath.Join(t.TempDir(), "freeze.db"), false)
	st := store.NewSQLiteModelPricingStore(tr.GetWriteDB())
	_, err := st.Create(ctx, &store.ModelPricingRecord{ModelName: billingTestModel, InputPrice: 2})
	require.NoError(t, err)
	require.NoError(t, tr.LoadBaseRates(ctx, st))
	tr.UpdateAccountMultipliers(map[int64]tracking.EndpointMultiplier{
		11: {CostMultiplier: .5},
		22: {CostMultiplier: 3},
	})
	req := startBillingRequest(t, tr, "event-freeze", false)
	id := req.GetRequestID()
	t1, write1 := time.Now(), int64(5)
	tr.RecordRequestUpdate(id, tracking.UpdateOptions{
		FirstTokenStartAt: &t1,
		UpstreamWriteMs:   &write1,
		BillingChannel:    store.BillingChannel{Type: "account", ID: 11, Name: "first"},
	})
	firstTokenMs := int64(10)
	tr.RecordRequestUpdate(id, tracking.UpdateOptions{FirstTokenMs: &firstTokenMs})
	t2, write2 := t1.Add(time.Second), int64(20)
	tr.RecordRequestUpdate(id, tracking.UpdateOptions{
		FirstTokenStartAt: &t2,
		UpstreamWriteMs:   &write2,
		BillingChannel:    store.BillingChannel{Type: "account", ID: 22, Name: "late"},
	})
	req.CompleteRequest(&tracking.TokenUsage{InputTokens: 100000})
	got := waitBillingRecord(t, tr, req, func(d *tracking.RequestDetail) bool { return d.Status == "completed" })
	require.True(t, got.CostKnown)
	require.InDelta(t, .1, got.TotalCostUSD, 1e-12)
	require.EqualValues(t, 11, got.UpstreamID)
}

func TestBillingChannel_FastZeroDoesNotMaskBrokenBaseRate(t *testing.T) {
	ctx := t.Context()
	tr := openBillingCatalog(t, filepath.Join(t.TempDir(), "broken-fast.db"), true)
	st := store.NewSQLiteModelPricingStore(tr.GetWriteDB())
	n := func(v float64) *float64 { return &v }
	require.NoError(t, st.PublishRateSheet(ctx, billingTestModel, store.RateSourceOfficial, store.RateSheet{
		Rows: []store.RateRow{{Input: n(2), Output: n(0)}, {Fast: true, Input: n(6), Output: n(0)}},
	}))
	channel := store.BillingChannel{Type: "endpoint", ID: 7, Name: "ep"}
	require.NoError(t, st.SaveChannelModelRule(ctx, channel, billingTestModel, store.ChannelModelRule{FastMultiplier: n(0)}))
	cur, err := st.CurrentBaseRate(ctx, billingTestModel)
	require.NoError(t, err)
	_, err = tr.GetWriteDB().Exec("UPDATE model_rate_sheets SET sheet='null' WHERE version_id=?", cur.ID)
	require.NoError(t, err)
	require.NoError(t, tr.LoadBaseRates(ctx, st))
	req := proxy.NewRequestLifecycleManager(tr, nil, "broken-fast-free", nil)
	req.SetModel(billingTestModel)
	req.StartRequest("127.0.0.1", "test", "POST", "/v1/messages", false)
	req.SetEndpointAttempt(channel.Name, channel.ID, 1)
	req.SetFirstTokenStartTime(time.Now())
	req.SetResponseReasoning("speed:fast")
	req.CompleteRequest(nil)
	got := waitBillingRecord(t, tr, req, func(d *tracking.RequestDetail) bool { return d.Status == "completed" })
	require.False(t, got.CostKnown)
	require.Contains(t, got.BillingDecision.Missing, "费率版本已损坏")
}
