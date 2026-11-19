package integration

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"sinuo/internal/service"
	"sinuo/internal/store"
	"sinuo/internal/tracking"

	"github.com/stretchr/testify/require"
)

func TestBillingPriceChange_ArchiveKeepsCompletedRequestCost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.db")
	// 两个请求都留在未满的归档批次里，最后通过 Close 等待写事务提交。
	tracker, err := tracking.NewUsageTracker(&tracking.Config{
		Enabled:       true,
		DatabasePath:  path,
		BatchSize:     10,
		FlushInterval: time.Hour,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tracker.Close()) })

	reader, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reader.Close()) })
	tracker.UpdateEndpointMultipliers(map[string]tracking.EndpointMultiplier{
		"test-endpoint": {CostMultiplier: 0.5},
	})
	ctx := t.Context()
	prices := store.NewSQLiteModelPricingStore(tracker.GetWriteDB())
	publisher := service.NewModelPricingService(prices)
	base := store.ModelPricingRecord{ModelName: billingTestModel, InputPrice: 2, RateSource: store.RateSourceOfficial}
	_, err = publisher.CreatePricing(ctx, &base)
	require.NoError(t, err)
	require.NoError(t, tracker.LoadBaseRates(ctx, prices))
	rowID := func() int64 {
		var id int64
		require.NoError(t, reader.QueryRow("SELECT id FROM model_base_rates WHERE model_name=? AND effective_until_ns IS NULL", billingTestModel).Scan(&id))
		return id
	}
	v1 := rowID()
	tokens := &tracking.TokenUsage{InputTokens: 100_000}
	first := startBillingRequest(t, tracker, "old-price", false)
	first.CompleteRequest(tokens)

	var count int
	require.NoError(t, reader.QueryRow("SELECT COUNT(*) FROM request_logs WHERE request_id = ?", first.GetRequestID()).Scan(&count))
	require.Zero(t, count, "调价前旧请求仍在热池")
	hot := waitBillingRecord(t, tracker, first, func(r *tracking.RequestDetail) bool { return r.Status == "completed" })
	require.InDelta(t, 0.10, hot.TotalCostUSD, 1e-9)

	require.Equal(t, v1, hot.BillingRateID)
	base.InputPrice = 4
	require.NoError(t, publisher.UpdatePricing(ctx, &base))
	require.NoError(t, tracker.LoadBaseRates(ctx, prices))
	v2 := rowID()
	require.NotEqual(t, v1, v2)
	hot = waitBillingRecord(t, tracker, first, func(r *tracking.RequestDetail) bool { return r.Status == "completed" })
	require.InDelta(t, 0.10, hot.TotalCostUSD, 1e-9)
	second := startBillingRequest(t, tracker, "new-price", false)
	second.CompleteRequest(tokens)
	require.NoError(t, tracker.Close())

	restarted, err := tracking.NewUsageTracker(&tracking.Config{Enabled: true, DatabasePath: path})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, restarted.Close()) })
	require.NoError(t, restarted.LoadBaseRates(ctx, store.NewSQLiteModelPricingStore(restarted.GetWriteDB())))
	require.Equal(t, 4.0, restarted.GetPricing(billingTestModel).Input)
	for _, tc := range []struct {
		id     string
		want   float64
		rateID int64
	}{{first.GetRequestID(), 0.10, v1}, {second.GetRequestID(), 0.20, v2}} {
		var status string
		var input, output int64
		var cost float64
		var rateID int64
		require.NoError(t, reader.QueryRow("SELECT COUNT(*) FROM request_logs WHERE request_id = ?", tc.id).Scan(&count))
		require.Equal(t, 1, count)
		require.NoError(t, reader.QueryRow(`
			SELECT status, input_tokens, output_tokens, total_cost_usd,billing_rate_id
			FROM request_logs WHERE request_id = ?`, tc.id).Scan(&status, &input, &output, &cost, &rateID))
		require.Equal(t, tc.rateID, rateID)
		official, err := restarted.RecalculateOfficialBaseCost(ctx, tc.id)
		require.NoError(t, err)
		require.InDelta(t, tc.want*2, official.TotalCost, 1e-9)
		require.Equal(t, "completed", status)
		require.EqualValues(t, 100_000, input)
		require.Zero(t, output)
		require.InDelta(t, tc.want, cost, 1e-9, "已提交的请求 %s", tc.id)
	}
}

func TestBillingPriceChange_EventSettlementUsesOutboundRate(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "usage.db")
	tracker, err := tracking.NewUsageTracker(&tracking.Config{
		Enabled: true, DatabasePath: path,
		HotPool: &tracking.HotPoolSettings{Enabled: false}, BatchSize: 10, FlushInterval: 20 * time.Millisecond,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tracker.Close()) })
	prices := store.NewSQLiteModelPricingStore(tracker.GetWriteDB())
	publisher := service.NewModelPricingService(prices)
	base := store.ModelPricingRecord{ModelName: billingTestModel, InputPrice: 2, RateSource: store.RateSourceOfficial}
	_, err = publisher.CreatePricing(ctx, &base)
	require.NoError(t, err)
	require.NoError(t, tracker.LoadBaseRates(ctx, prices))
	tracker.UpdateEndpointMultipliers(map[string]tracking.EndpointMultiplier{"test-endpoint": {CostMultiplier: 0.5}})
	request := startBillingRequest(t, tracker, "event", false)
	base.InputPrice = 4
	require.NoError(t, publisher.UpdatePricing(ctx, &base))
	// V2 已提交但还没有加载到转发器；在此窗口内实际写出的调用仍命中 V1。
	request.SetFirstTokenStartTime(time.Now())
	require.NoError(t, tracker.LoadBaseRates(ctx, prices))
	tracker.UpdateEndpointMultipliers(map[string]tracking.EndpointMultiplier{"test-endpoint": {CostMultiplier: 3}})
	request.CompleteRequest(&tracking.TokenUsage{InputTokens: 100_000})
	require.NoError(t, tracker.ForceFlush())
	record := waitBillingRecord(t, tracker, request, func(r *tracking.RequestDetail) bool { return r.Status == "completed" })
	require.InDelta(t, 0.10, record.TotalCostUSD, 1e-9)
	require.Positive(t, record.BillingRateID)
	tracker.RecoverRequestTokens(request.GetRequestID(), billingTestModel, &tracking.TokenUsage{InputTokens: 200_000})
	// 关闭会提交尚未成批的补报事件。
	ro, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	require.NoError(t, err)
	defer ro.Close()
	require.NoError(t, tracker.Close())
	var total float64
	var id int64
	require.NoError(t, ro.QueryRow("SELECT total_cost_usd,billing_rate_id FROM request_logs WHERE request_id=?", request.GetRequestID()).Scan(&total, &id))
	require.Equal(t, record.BillingRateID, id)
	require.InDelta(t, 0.20, total, 1e-9)
}

func TestBillingPriceChange_MigratesCustomPricesOnce(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "usage.db")
	open := func() *tracking.UsageTracker {
		tr, err := tracking.NewUsageTracker(&tracking.Config{Enabled: true, DatabasePath: path})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, tr.Close()) })
		return tr
	}
	old := open()
	_, err := old.GetWriteDB().Exec("INSERT INTO model_pricing(model_name,input_price,output_price) VALUES('custom-old',9,11)")
	require.NoError(t, err)
	_, err = old.GetWriteDB().Exec(`INSERT INTO request_logs(request_id,start_time,status,total_cost_usd)
		VALUES('historic','2026-01-01T00:00:00.000000Z','completed',1.23)`)
	require.NoError(t, err)
	require.NoError(t, old.Close())

	tracker := open()
	st := store.NewSQLiteModelPricingStore(tracker.GetWriteDB())
	rates, err := st.ListBaseRates(ctx)
	require.NoError(t, err)
	require.Len(t, rates, 1)
	legacy := rates[0]
	require.Equal(t, "legacy", legacy.Source)
	require.Equal(t, 9.0, legacy.Input)
	publisher := service.NewModelPricingService(st)
	model, err := publisher.GetPricing(ctx, "custom-old")
	require.NoError(t, err)
	edit := *model
	edit.DisplayName = "改名"
	require.NoError(t, publisher.UpdatePricing(ctx, &edit))
	edit.InputPrice = 4
	require.NoError(t, publisher.UpdatePricing(ctx, &edit))
	require.NoError(t, publisher.DeletePricing(ctx, "custom-old"))
	require.NoError(t, tracker.Close())

	reopened := open()
	st = store.NewSQLiteModelPricingStore(reopened.GetWriteDB())
	rates, err = st.ListBaseRates(ctx)
	require.NoError(t, err)
	require.Len(t, rates, 2)
	require.Equal(t, legacy.ID, rates[0].ID)
	require.Equal(t, 9.0, rates[0].Input)
	require.Equal(t, rates[0].UntilNS.Int64, rates[1].FromNS)
	require.True(t, rates[1].UntilNS.Valid)
	require.Equal(t, "custom", rates[1].Source)
	require.NoError(t, reopened.LoadBaseRates(ctx, st))
	require.NotContains(t, reopened.GetConfiguredModels(), "custom-old")
	var historicCost float64
	var rate sql.NullInt64
	require.NoError(t, reopened.GetReadDB().QueryRow("SELECT total_cost_usd,billing_rate_id FROM request_logs WHERE request_id='historic'").Scan(&historicCost, &rate))
	require.Equal(t, 1.23, historicCost)
	require.False(t, rate.Valid)
}

func TestBillingPriceChange_NullCacheRateCanBePublishedAsZero(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "usage.db")
	old := openBillingCatalog(t, path, false)
	models := []string{"legacy-null-5m", "legacy-null-1h", "legacy-null-read"}
	for i, model := range models {
		cache := []any{2.5, 4.0, 0.2}
		cache[i] = nil
		_, err := old.GetWriteDB().ExecContext(ctx, `INSERT INTO model_pricing
			(model_name,input_price,output_price,cache_creation_price_5m,cache_creation_price_1h,cache_read_price,is_default)
			VALUES(?,2,10,?,?,?,?)`, model, cache[0], cache[1], cache[2], i == 0)
		require.NoError(t, err)
	}
	require.NoError(t, old.Close())

	tracker := openBillingCatalog(t, path, false)
	prices := store.NewSQLiteModelPricingStore(tracker.GetWriteDB())
	publisher := service.NewModelPricingService(prices)
	list, err := publisher.ListPricings(ctx)
	require.NoError(t, err)
	require.Len(t, list, len(models))
	defaultForm, err := prices.GetDefault(ctx)
	require.NoError(t, err)
	require.NotNil(t, defaultForm)
	require.Equal(t, models[0], defaultForm.ModelName)
	require.Equal(t, defaultForm, publisher.GetDefaultPricing(ctx))
	require.NoError(t, publisher.UpdatePricing(ctx, &store.ModelPricingRecord{
		ModelName: models[1], IsDefault: true, MetadataOnly: true,
	}))
	require.Equal(t, models[1], publisher.GetDefaultPricing(ctx).ModelName)

	for i, model := range models {
		t.Run(model, func(t *testing.T) {
			before, err := prices.CurrentBaseRate(ctx, model)
			require.NoError(t, err)
			require.Equal(t, "legacy", before.Source)
			require.False(t, []sql.NullFloat64{before.Cache5m, before.Cache1h, before.CacheRead}[i].Valid)

			cache := []float64{2.5, 4.0, 0.2}
			cache[i] = 0
			form, err := publisher.GetPricing(ctx, model)
			require.NoError(t, err)
			require.NotNil(t, form)
			require.Equal(t, cache, []float64{form.CacheCreationPrice5m, form.CacheCreationPrice1h, form.CacheReadPrice})
			require.Equal(t, i == 1, form.IsDefault)
			byID, err := prices.GetByID(ctx, form.ID)
			require.NoError(t, err)
			require.Equal(t, form, byID)

			require.NoError(t, publisher.UpdatePricing(ctx, &store.ModelPricingRecord{
				ModelName: model, MetadataOnly: true, DisplayName: "更新名称", Description: "模型说明", IsDefault: form.IsDefault,
			}))
			afterMetadata, err := prices.CurrentBaseRate(ctx, model)
			require.NoError(t, err)
			require.Equal(t, before, afterMetadata)
			var rawCache [3]sql.NullFloat64
			require.NoError(t, tracker.GetWriteDB().QueryRowContext(ctx, `SELECT
				cache_creation_price_5m, cache_creation_price_1h, cache_read_price FROM model_pricing WHERE model_name=?`,
				model).Scan(&rawCache[0], &rawCache[1], &rawCache[2]))
			require.False(t, rawCache[i].Valid)
			saved, err := publisher.GetPricing(ctx, model)
			require.NoError(t, err)
			require.NotNil(t, saved)
			expected := *form
			expected.DisplayName = "更新名称"
			expected.Description = "模型说明"
			expected.UpdatedAt = saved.UpdatedAt
			require.Equal(t, &expected, saved)

			// 表单明确保存数字 0，此时才发布新费率。
			edit := *saved
			require.NoError(t, publisher.UpdatePricing(ctx, &edit))
			current, err := prices.CurrentBaseRate(ctx, model)
			require.NoError(t, err)
			require.NotEqual(t, before.ID, current.ID)
			require.Equal(t, store.RateSourceCustom, current.Source)
			require.Equal(t, sql.NullFloat64{Valid: true}, []sql.NullFloat64{current.Cache5m, current.Cache1h, current.CacheRead}[i])

			edit.Description = "显式零价"
			require.NoError(t, publisher.UpdatePricing(ctx, &edit))
			rates, err := prices.ListBaseRates(ctx)
			require.NoError(t, err)
			var history []store.BaseRateVersion
			for _, rate := range rates {
				if rate.ModelName == model {
					history = append(history, rate)
				}
			}
			require.Len(t, history, 2)
			before.UntilNS = sql.NullInt64{Valid: true, Int64: current.FromNS}
			require.Equal(t, before, history[0])
			require.Equal(t, current, history[1])
		})
	}
}

func TestBillingPriceChange_StructuredRateOnlyAllowsMetadataThroughFlatUpdate(t *testing.T) {
	ctx := t.Context()
	tracker := openBillingCatalog(t, filepath.Join(t.TempDir(), "usage.db"), false)
	db := tracker.GetWriteDB()
	prices := store.NewSQLiteModelPricingStore(db)
	const model = "structured-model"
	input, output, zero, read := 2.0, 10.0, 0.0, 0.2
	sheet := store.RateSheet{Rows: []store.RateRow{{Input: &input, Output: &output, Cache1h: &zero, CacheRead: &read}}}
	require.NoError(t, prices.PublishRateSheet(ctx, model, store.RateSourceOfficial, sheet))
	before, err := prices.CurrentBaseRate(ctx, model)
	require.NoError(t, err)
	require.False(t, before.Cache5m.Valid)
	require.Equal(t, sql.NullFloat64{Valid: true}, before.Cache1h)

	form, err := prices.Get(ctx, model)
	require.NoError(t, err)
	require.NotNil(t, form)
	require.Zero(t, form.CacheCreationPrice5m)
	edit := *form
	edit.DisplayName = "显示名称"
	edit.Description = "模型说明"
	require.NoError(t, service.NewModelPricingService(prices).UpdatePricing(ctx, &edit))
	edit.IsDefault = true
	require.NoError(t, prices.BatchUpsert(ctx, []*store.ModelPricingRecord{&edit}))
	saved, err := prices.Get(ctx, model)
	require.NoError(t, err)
	require.NotNil(t, saved)
	edit.UpdatedAt = saved.UpdatedAt
	require.Equal(t, &edit, saved)

	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	view := prices.WithTx(tx)
	for _, change := range []func(*store.ModelPricingRecord){
		func(r *store.ModelPricingRecord) { r.InputPrice++ },
		func(r *store.ModelPricingRecord) { r.OutputPrice++ },
		func(r *store.ModelPricingRecord) { r.CacheCreationPrice5m++ },
		func(r *store.ModelPricingRecord) { r.CacheCreationPrice1h++ },
		func(r *store.ModelPricingRecord) { r.CacheReadPrice++ },
		func(r *store.ModelPricingRecord) { r.RateSource = store.RateSourceCustom },
	} {
		invalid := edit
		invalid.Description = "拒绝的修改"
		change(&invalid)
		require.ErrorContains(t, view.Update(ctx, &invalid), "请通过价表发布接口调整单价")
	}
	// 调用方可继续提交自己的事务，之前被拒绝的更新不应留下任何写入。
	require.NoError(t, tx.Commit())

	after, err := prices.Get(ctx, model)
	require.NoError(t, err)
	require.Equal(t, saved, after)
	rates, err := prices.ListBaseRates(ctx)
	require.NoError(t, err)
	require.Equal(t, []store.BaseRateVersion{before}, rates)
}

func TestBillingPriceChange_RejectedPriceChangePreservesDefault(t *testing.T) {
	ctx := t.Context()
	tracker := openBillingCatalog(t, filepath.Join(t.TempDir(), "usage.db"), false)
	prices := store.NewSQLiteModelPricingStore(tracker.GetWriteDB())
	publisher := service.NewModelPricingService(prices)
	_, err := publisher.CreatePricing(ctx, &store.ModelPricingRecord{
		ModelName: "current-default", InputPrice: 2, OutputPrice: 10, IsDefault: true,
	})
	require.NoError(t, err)
	defaultBefore := publisher.GetDefaultPricing(ctx)

	const model = "structured-model"
	input, output := 3.0, 15.0
	sheet := store.RateSheet{Rows: []store.RateRow{{Input: &input, Output: &output}}}
	require.NoError(t, prices.PublishRateSheet(ctx, model, store.RateSourceOfficial, sheet))
	form, err := prices.Get(ctx, model)
	require.NoError(t, err)
	require.NotNil(t, form)
	ratesBefore, err := prices.ListBaseRates(ctx)
	require.NoError(t, err)

	invalid := *form
	invalid.IsDefault = true
	invalid.InputPrice++
	require.ErrorContains(t, publisher.UpdatePricing(ctx, &invalid), "请通过价表发布接口调整单价")

	defaultAfter, err := prices.GetDefault(ctx)
	require.NoError(t, err)
	require.NotNil(t, defaultAfter)
	require.Equal(t, defaultBefore.ModelName, defaultAfter.ModelName)
	require.Equal(t, defaultBefore, publisher.GetDefaultPricing(ctx))
	saved, err := prices.Get(ctx, model)
	require.NoError(t, err)
	require.Equal(t, form, saved)
	ratesAfter, err := prices.ListBaseRates(ctx)
	require.NoError(t, err)
	require.Equal(t, ratesBefore, ratesAfter)

	previous, err := publisher.GetPricing(ctx, defaultBefore.ModelName)
	require.NoError(t, err)
	require.True(t, previous.IsDefault)
	_, err = publisher.CreatePricing(ctx, &store.ModelPricingRecord{
		ModelName: "replacement-default", InputPrice: 2, OutputPrice: 10, IsDefault: true,
	})
	require.NoError(t, err)
	require.Equal(t, "replacement-default", publisher.GetDefaultPricing(ctx).ModelName)
	previous, err = publisher.GetPricing(ctx, defaultBefore.ModelName)
	require.NoError(t, err)
	require.False(t, previous.IsDefault)
}

func TestBillingPriceChange_BatchUpsertMaintainsSingleDefaultAndValidatesChannelRules(t *testing.T) {
	ctx := t.Context()
	tr := openBillingCatalog(t, filepath.Join(t.TempDir(), "batch-default.db"), false)
	db := tr.GetWriteDB()
	prices := store.NewSQLiteModelPricingStore(db)
	_, err := prices.Create(ctx, &store.ModelPricingRecord{ModelName: "first-default", InputPrice: 2, IsDefault: true})
	require.NoError(t, err)

	err = prices.BatchUpsert(ctx, []*store.ModelPricingRecord{
		{ModelName: "dup-a", InputPrice: 1, IsDefault: true},
		{ModelName: "dup-b", InputPrice: 2, IsDefault: true},
	})
	require.ErrorContains(t, err, "只能指定一个默认")

	require.NoError(t, prices.BatchUpsert(ctx, []*store.ModelPricingRecord{
		{ModelName: "second-default", InputPrice: 5, IsDefault: true},
		{ModelName: "other", InputPrice: 3},
	}))
	var defaultCount int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT COUNT(*) FROM model_pricing WHERE is_default = 1").Scan(&defaultCount))
	require.Equal(t, 1, defaultCount)
	def, err := prices.GetDefault(ctx)
	require.NoError(t, err)
	require.Equal(t, "second-default", def.ModelName)

	// 删除带活跃时段规则的模型后，通过平价重建必须被渠道规则兼容性校验拦截。
	n := func(v float64) *float64 { return &v }
	const phased = "phased-model"
	require.NoError(t, prices.PublishRateSheet(ctx, phased, store.RateSourceOfficial, store.RateSheet{
		Timezone: "UTC",
		Periods:  []store.RatePeriod{{Name: "peak", StartMinute: 0}},
		Rows:     []store.RateRow{{Period: "peak", Input: n(2), Output: n(4)}},
	}))
	sub := store.BillingChannel{Type: "subscription", Name: "claude-subscription"}
	require.NoError(t, prices.SaveChannelModelRule(ctx, sub, phased, store.ChannelModelRule{Period: "peak"}))
	require.NoError(t, prices.Delete(ctx, phased))
	_, err = prices.Create(ctx, &store.ModelPricingRecord{ModelName: phased, InputPrice: 2, OutputPrice: 4})
	require.ErrorContains(t, err, "固定时段")
}
