package tracking

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	timezonepolicy "sinuo/internal/timezone"
)

func seedSessionRequest(t *testing.T, tracker *UsageTracker, id, family, session string, at time.Time, input, creation, read, cost interface{}) {
	t.Helper()
	_, err := tracker.GetWriteDB().Exec(`INSERT INTO request_logs
		(request_id, request_family, session_id, start_time, status, request_kind, upstream_type, upstream_name, model_name,
		input_tokens, cache_creation_tokens, cache_creation_5m_tokens, cache_creation_1h_tokens, cache_read_tokens, total_cost_usd)
		VALUES (?, ?, ?, ?, 'completed', 'generation', 'account', 'synthetic-upstream', 'synthetic-model', ?, ?, 100, 200, ?, ?)`,
		id, family, session, timezonepolicy.FormatStorage(at), input, creation, read, cost)
	if err != nil {
		t.Fatal(err)
	}
}

func TestRequestSessions_FullSetPaginationAndUnknowns(t *testing.T) {
	tr := newModelUsageTestTracker(t)
	now := time.Now()
	for i := 0; i < 130; i++ {
		seedSessionRequest(t, tr, fmt.Sprintf("low-%03d", i), "codex", fmt.Sprintf("low-%03d", i), now, 100, 0, 50, 0.1)
	}
	for i := 0; i < 150; i++ {
		seedSessionRequest(t, tr, fmt.Sprintf("high-%03d", i), "codex", "expensive", now.Add(-time.Duration(i)*time.Second), 1000, 0, 900, 0.2)
	}
	seedSessionRequest(t, tr, "unknown", "codex", "missing", now, nil, nil, nil, nil)
	seedSessionRequest(t, tr, "known-in-missing", "codex", "missing", now, 10, 0, 0, 99.0)
	seedSessionRequest(t, tr, "all-unknown", "codex", "all-unknown", now, nil, nil, nil, nil)
	seedSessionRequest(t, tr, "zero", "codex", "zero", now, 0, 0, 0, 0.0)
	seedSessionRequest(t, tr, "orphan", "codex", "", now, 10, 0, 0, 2.0)
	result, err := tr.QueryRequestSessions(context.Background(), modelUsageQueryRange(now), 1, 20, "cost", "desc")
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 134 || result.TotalRequests != 285 || result.Ungrouped != 1 || result.MissingCosts != 2 {
		t.Fatalf("完整范围汇总错误: %+v", result)
	}
	partial := result.Rows[0]
	if partial.SessionID != "missing" || partial.TotalCostUSD == nil || *partial.TotalCostUSD != 99 || partial.MissingCosts != 1 || partial.MissingCacheUsage != 1 || partial.CacheRatio == nil || *partial.CacheRatio != 0 {
		t.Fatalf("部分缺失仍按已记录成本参与全局排序，缺失数量独立: %+v", partial)
	}
	if result.Rows[1].SessionID != "expensive" || result.Rows[1].RequestCount != 150 || math.Abs(*result.Rows[1].TotalCostUSD-30) > 1e-8 {
		t.Fatalf("必须先聚合再分页: %+v", result.Rows[0])
	}
	last, err := tr.QueryRequestSessions(context.Background(), nil, 7, 20, "cost", "desc")
	if err != nil {
		t.Fatal(err)
	}
	missing := last.Rows[len(last.Rows)-1]
	if missing.SessionID != "all-unknown" || missing.TotalCostUSD != nil || missing.CacheRatio != nil || missing.MissingCosts != 1 || missing.MissingCacheUsage != 1 {
		t.Fatalf("未知成本必须置后且保留缺失数: %+v", missing)
	}
	zero := last.Rows[len(last.Rows)-2]
	if zero.SessionID != "zero" || zero.TotalCostUSD == nil || *zero.TotalCostUSD != 0 || zero.CacheRatio != nil {
		t.Fatalf("真实零与未知混淆: %+v", zero)
	}
	asc, err := tr.QueryRequestSessions(context.Background(), nil, 1, 20, "cost", "asc")
	if err != nil || asc.Rows[0].SessionID != "zero" {
		t.Fatalf("升序未知也必须置后: %+v %v", asc, err)
	}
}

func TestRequestSessions_ProtocolAndFilters(t *testing.T) {
	tr := newModelUsageTestTracker(t)
	now := time.Now()
	seedSessionRequest(t, tr, "claude", "claude", "same", now, 100, 300, 600, 1.0)
	seedSessionRequest(t, tr, "codex", "codex", "same", now, 1000, 300, 900, 1.0)
	seedSessionRequest(t, tr, "missing-read", "codex", "missing-read", now, 1000, 0, nil, 0.0)
	seedSessionRequest(t, tr, "missing-input", "claude", "missing-input", now, nil, 0, 100, 0.0)
	result, err := tr.QueryRequestSessions(context.Background(), nil, 1, 20, "cache", "desc")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 4 || *result.Rows[0].CacheRatio != 0.9 || *result.Rows[1].CacheRatio != 0.6 || result.Rows[2].CacheRatio != nil {
		t.Fatalf("协议包含关系或字段级未知错误: %+v", result.Rows)
	}
	// 缓存创建只有分项时可求和；同时有总数时不能重复加分项。
	_, err = tr.GetWriteDB().Exec(`UPDATE request_logs SET cache_creation_tokens = NULL WHERE request_id = 'claude'`)
	if err != nil {
		t.Fatal(err)
	}
	trend, err := tr.QueryRequestSessionTrend(context.Background(), &QueryOptions{RequestFamily: "claude", SessionID: "same"}, "", 0)
	if err != nil || *trend.Points[0].FullInput != 1000 {
		t.Fatalf("缓存创建分项错误: %+v %v", trend, err)
	}
	start, end := now, now.Add(time.Second)
	opts := &QueryOptions{StartDate: &start, EndDate: &end, RequestFamily: "codex", UpstreamType: "account", UpstreamName: "synthetic-upstream", ModelName: "synthetic-model", Status: "completed", SessionID: "same", RequestKinds: GenerationMetricRequestKinds()}
	filtered, err := tr.QueryRequestSessions(context.Background(), opts, 1, 20, "recent", "desc")
	if err != nil || filtered.TotalRequests != 1 {
		t.Fatalf("公共筛选不一致: %+v %v", filtered, err)
	}
	opts.EndDate = &start
	filtered, err = tr.QueryRequestSessions(context.Background(), opts, 1, 20, "count", "desc")
	if err != nil || filtered.TotalRequests != 0 {
		t.Fatalf("时间上边界必须排除: %+v %v", filtered, err)
	}
}

func TestRequestSessions_HotArchiveDedupAndLocation(t *testing.T) {
	tr := newModelUsageTestTracker(t)
	now := time.Now()
	for i := 0; i < 70; i++ {
		seedSessionRequest(t, tr, fmt.Sprintf("req-%03d", i), "codex", "session", now.Add(time.Duration(i)*time.Second), 100, 0, 50, 1.0)
	}
	// 比数据库请求更旧的热池记录仍排在正式表前面。
	seedSessionRequest(t, tr, "overlap", "codex", "old-session", now, 100, 0, 50, 9.0)
	err := tr.hotPool.Add(&ActiveRequest{RequestID: "overlap", RequestFamily: "codex", SessionID: "session", StartTime: now.Add(-time.Minute), RequestKind: "generation", UpstreamType: "account", Status: "completed", InputTokens: 1000, CacheReadTokens: 900, UsageKnown: true, CostKnown: true, CostOverrideUSD: ptrSessionCost(3)})
	if err != nil {
		t.Fatal(err)
	}
	// 模拟归档排队，不写入；GetActive 必须同时看到 archiving。
	tr.hotPool.SetArchiveCallback(func(*ActiveRequest) {})
	if err := tr.hotPool.CompleteAndArchive("overlap", nil); err != nil {
		t.Fatal(err)
	}
	// 即使活跃与归档映射意外同时保留相同 ID，也只能算一次。
	active := tr.hotPool.GetActive()[0]
	if err := tr.hotPool.Add(active); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	result, err := tr.QueryRequestSessions(ctx, nil, 1, 20, "cost", "desc")
	if err != nil || result.Total != 1 || result.TotalRequests != 71 || math.Abs(*result.Rows[0].TotalCostUSD-73) > 1e-8 {
		t.Fatalf("归档重叠重复计数或旧分组泄漏: %+v %v", result, err)
	}
	opts := &QueryOptions{RequestFamily: "codex", SessionID: "session", Limit: 20}
	for _, id := range []string{"overlap", "req-000", "req-050"} {
		page, err := tr.LocateRequestPage(ctx, opts, id, 20)
		if err != nil {
			t.Fatal(err)
		}
		opts.Offset = (page - 1) * 20
		rows, count, err := tr.QueryRequestDetailsWithHotPool(ctx, opts)
		found := false
		for _, row := range rows {
			found = found || row.RequestID == id
		}
		if err != nil || count != 71 || !found {
			t.Fatalf("实际页中必须找到目标 %s page=%d count=%d err=%v", id, page, count, err)
		}
	}
	// 旧分组不能在正式请求表里复活。
	rows, count, err := tr.QueryRequestDetailsWithHotPool(ctx, &QueryOptions{SessionID: "old-session", Limit: 20})
	if err != nil || count != 0 || len(rows) != 0 {
		t.Fatalf("旧归档快照泄漏: %v %d %v", rows, count, err)
	}
	_, err = tr.GetWriteDB().Exec(`UPDATE request_logs SET session_id = 'session', input_tokens = 1000, cache_read_tokens = 900, total_cost_usd = 3 WHERE request_id = 'overlap'`)
	if err != nil {
		t.Fatal(err)
	}
	tr.hotPool.Remove("overlap")
	tr.hotPool.ConfirmArchived([]string{"overlap"})
	archived, err := tr.QueryRequestSessions(ctx, nil, 1, 20, "cost", "desc")
	if err != nil || archived.TotalRequests != result.TotalRequests || *archived.Rows[0].TotalCostUSD != *result.Rows[0].TotalCostUSD {
		t.Fatalf("归档完成前后应一致: %+v %v", archived, err)
	}
}

func TestRequestSessions_CacheRatioUsesSummedInputs(t *testing.T) {
	tr := newModelUsageTestTracker(t)
	now := time.Now()
	seedSessionRequest(t, tr, "large", "claude", "weighted", now, 100, 300, 600, 1.0)
	seedSessionRequest(t, tr, "small", "claude", "weighted", now, 100, 0, 0, 0.0)
	got, err := tr.QueryRequestSessions(context.Background(), nil, 1, 20, "cost", "desc")
	if err != nil || got.Rows[0].CacheRatio == nil || math.Abs(*got.Rows[0].CacheRatio-600.0/1100) > 1e-12 {
		t.Fatalf("缓存比率必须求和后相除: %+v %v", got, err)
	}
}

func ptrSessionCost(value float64) *float64 { return &value }

func TestRequestSessions_CacheRatioKnownSampleCoverage(t *testing.T) {
	tr := newModelUsageTestTracker(t)
	now := time.Now()
	seedSessionRequest(t, tr, "known", "codex", "sample", now, 1000, 9999, 900, nil)
	seedSessionRequest(t, tr, "zero-read", "codex", "sample", now, 100, 0, 0, 0.0)
	seedSessionRequest(t, tr, "cancelled-usage", "codex", "sample", now, 200, 0, 100, 1.0)
	seedSessionRequest(t, tr, "unknown-input", "codex", "sample", now, nil, 0, 9000, 1.0)
	seedSessionRequest(t, tr, "unknown-read", "codex", "sample", now, 9000, 0, nil, 1.0)
	seedSessionRequest(t, tr, "zero-denominator", "codex", "zero", now, 0, 0, 0, 0.0)
	seedSessionRequest(t, tr, "all-unknown", "codex", "unknown", now, nil, nil, nil, 0.0)
	seedSessionRequest(t, tr, "claude-known", "claude", "claude", now, 100, 300, 600, 0.0)
	seedSessionRequest(t, tr, "claude-zero-read", "claude", "claude", now, 100, 0, 0, 0.0)
	seedSessionRequest(t, tr, "claude-missing-input", "claude", "claude", now, nil, 0, 9000, 0.0)
	seedSessionRequest(t, tr, "claude-missing-creation", "claude", "claude", now, 100, nil, 9000, 0.0)
	if _, err := tr.GetWriteDB().Exec(`UPDATE request_logs SET status = 'cancelled' WHERE request_id = 'cancelled-usage'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.GetWriteDB().Exec(`UPDATE request_logs SET cache_creation_1h_tokens = NULL WHERE request_id = 'claude-missing-creation'`); err != nil {
		t.Fatal(err)
	}
	result, err := tr.QueryRequestSessions(context.Background(), nil, 1, 20, "cache", "desc")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]RequestSessionRow{}
	for _, row := range result.Rows {
		byID[row.SessionID] = row
	}
	sample := byID["sample"]
	if sample.RequestCount != 5 || sample.MissingCacheUsage != 2 || sample.CacheRatio == nil || math.Abs(*sample.CacheRatio-1000.0/1300) > 1e-12 || sample.TotalCostUSD == nil || *sample.TotalCostUSD != 3 || sample.MissingCosts != 1 {
		t.Fatalf("分子分母须使用相同已知样本；零读取和有用量的取消请求应计入，费用独立: %+v", sample)
	}
	claude := byID["claude"]
	if claude.MissingCacheUsage != 2 || claude.CacheRatio == nil || math.Abs(*claude.CacheRatio-600.0/1100) > 1e-12 || claude.MissingCosts != 0 {
		t.Fatalf("Claude 总数优先且缺失创建分项不能当成零: %+v", claude)
	}
	if byID["zero"].CacheRatio != nil || byID["zero"].MissingCacheUsage != 0 || byID["unknown"].CacheRatio != nil || byID["unknown"].MissingCacheUsage != 1 {
		t.Fatalf("无样本与零分母均未知，但缺失计数不同: %+v", byID)
	}
	trend, err := tr.QueryRequestSessionTrend(context.Background(), &QueryOptions{RequestFamily: "codex", SessionID: "sample"}, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	missing := 0
	for _, point := range trend.Points {
		if point.FullInput == nil || point.CacheRead == nil {
			missing++
		}
	}
	if missing != int(sample.MissingCacheUsage) {
		t.Fatalf("走势必须能定位每条未知用量: %+v", trend)
	}
}

func TestRequestSessions_SummaryEmptyAndOrphans(t *testing.T) {
	tr := newModelUsageTestTracker(t)
	empty, err := tr.QueryRequestSessions(context.Background(), nil, 1, 20, "recent", "desc")
	if err != nil || empty.TotalRequests != 0 || empty.Ungrouped != 0 || empty.Total != 0 || empty.MissingCosts != 0 || empty.KnownCostUSD != 0 {
		t.Fatalf("空集汇总: %+v %v", empty, err)
	}
	now := time.Now()
	seedSessionRequest(t, tr, "empty", "codex", "", now, 0, 0, 0, 0.0)
	seedSessionRequest(t, tr, "null", "codex", "", now, nil, nil, nil, nil)
	seedSessionRequest(t, tr, "other-family", "claude", "", now, 0, 0, 0, 2.0)
	seedSessionRequest(t, tr, "grouped", "codex", "one", now, 0, 0, 0, 3.0)
	if _, err := tr.GetWriteDB().Exec(`UPDATE request_logs SET session_id = NULL WHERE request_id = 'null'`); err != nil {
		t.Fatal(err)
	}
	got, err := tr.QueryRequestSessions(context.Background(), nil, 1, 20, "recent", "desc")
	if err != nil || got.TotalRequests != 4 || got.Ungrouped != 3 || got.Total != 1 || got.MissingCosts != 1 || got.KnownCostUSD != 5 {
		t.Fatalf("NULL/空ID均为未归组且已知成本含未归组: %+v %v", got, err)
	}
}

func TestRequestSessions_CostSortUnknownLastAcrossPages(t *testing.T) {
	tr := newModelUsageTestTracker(t)
	now := time.Now()
	// 先插入已知费用，确保聚合的代表行不以 NULL 偶然掩盖别名冲突。
	seedSessionRequest(t, tr, "partial-known", "codex", "partial", now, 100, 0, 0, 2.0)
	seedSessionRequest(t, tr, "partial-missing", "codex", "partial", now, 100, 0, 0, nil)
	seedSessionRequest(t, tr, "all-missing", "codex", "unknown", now, 100, 0, 0, nil)
	seedSessionRequest(t, tr, "zero-known", "codex", "zero-partial", now, 100, 0, 0, 0.0)
	seedSessionRequest(t, tr, "zero-missing", "codex", "zero-partial", now, 100, 0, 0, nil)
	seedSessionRequest(t, tr, "sort-zero", "codex", "zero", now, 0, 0, 0, 0.0)
	seedSessionRequest(t, tr, "sort-five", "codex", "five", now, 100, 0, 0, 5.0)
	for direction, want := range map[string][]string{"asc": {"zero", "zero-partial", "partial", "five", "unknown"}, "desc": {"five", "partial", "zero", "zero-partial", "unknown"}} {
		for page, session := range want {
			got, err := tr.QueryRequestSessions(context.Background(), nil, page+1, 1, "cost", direction)
			if err != nil {
				t.Fatal(err)
			}
			if got.Total != 5 || len(got.Rows) != 1 || got.Rows[0].SessionID != session {
				t.Fatalf("%s 第%d页: got=%+v want=%s", direction, page+1, got, session)
			}
			row := got.Rows[0]
			if session == "partial" && (row.TotalCostUSD == nil || *row.TotalCostUSD != 2 || row.MissingCosts != 1) {
				t.Fatalf("已记录成本与缺失数量必须独立: %+v", row)
			}
			if session == "zero-partial" && (row.TotalCostUSD == nil || *row.TotalCostUSD != 0 || row.MissingCosts != 1) {
				t.Fatalf("已记录真实零不能因其他请求缺失变成未知: %+v", row)
			}
		}
	}
}

func TestRequestSessions_StatsReplaceHotIDsBeforeFiltering(t *testing.T) {
	tr := newModelUsageTestTracker(t)
	now := time.Now()
	seedSessionRequest(t, tr, "moving-request", "claude", "old-session", now, 100, 30, 20, 9.0)
	_, err := tr.GetWriteDB().Exec(`UPDATE request_logs SET status='failed', model_name='old-model', upstream_type='endpoint', upstream_name='old-upstream' WHERE request_id='moving-request'`)
	if err != nil {
		t.Fatal(err)
	}
	err = tr.hotPool.Add(&ActiveRequest{RequestID: "moving-request", SessionID: "new-session", RequestFamily: "codex", StartTime: now,
		RequestSource: RequestSourceLegacyProxy, RequestKind: RequestKindGeneration, Status: "completed", ModelName: "new-model",
		UpstreamType: "account", UpstreamName: "new-upstream", UsageKnown: true, CostKnown: true, CostOverrideUSD: ptrSessionCost(3),
		InputTokens: 100, OutputTokens: 10, CacheCreationTokens: 30, CacheReadTokens: 20})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, tc := range []struct {
		name          string
		opts          QueryOptions
		count, tokens int64
		cost          float64
	}{
		{"旧会话", QueryOptions{SessionID: "old-session"}, 0, 0, 0},
		{"旧请求族", QueryOptions{RequestFamily: "claude"}, 0, 0, 0},
		{"旧来源", QueryOptions{UpstreamType: "endpoint"}, 0, 0, 0},
		{"旧上游", QueryOptions{UpstreamName: "old-upstream"}, 0, 0, 0},
		{"旧模型", QueryOptions{ModelName: "old-model"}, 0, 0, 0},
		{"旧状态", QueryOptions{Status: "failed"}, 0, 0, 0},
		{"新会话", QueryOptions{SessionID: "new-session"}, 1, 110, 3},
		{"新来源", QueryOptions{UpstreamType: "account"}, 1, 110, 3},
		{"完整范围", QueryOptions{}, 1, 110, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.opts.Limit = 20
			rows, total, err := tr.QueryRequestDetailsWithHotPool(ctx, &tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			rank, err := tr.QueryRequestSessions(ctx, &tc.opts, 1, 20, "cost", "desc")
			if err != nil {
				t.Fatal(err)
			}
			stats, err := tr.QueryAggregatedRequestStatsWithHotPool(ctx, &tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			if total != tc.count || int64(len(rows)) != tc.count || rank.TotalRequests != tc.count || stats.TotalRequests != tc.count || stats.TotalTokens != tc.tokens || stats.TotalCostUSD != tc.cost {
				t.Fatalf("正式表=%d/%d 排行=%d 汇总=%+v want count=%d tokens=%d cost=%v", len(rows), total, rank.TotalRequests, stats, tc.count, tc.tokens, tc.cost)
			}
		})
	}
}

func TestRequestSessions_HotCostsUseRecordedValues(t *testing.T) {
	tr := newModelUsageTestTracker(t)
	now := time.Now()
	seedSessionRequest(t, tr, "historical-overlap", "codex", "recorded", now, 100, 0, 0, 12.3456)
	for _, req := range []*ActiveRequest{
		{RequestID: "historical-overlap", RequestFamily: "codex", SessionID: "recorded", StartTime: now, UsageKnown: true, CostKnown: true, InputTokens: 100, Status: "completed"},
		{RequestID: "not-settled", RequestFamily: "codex", SessionID: "unsettled", StartTime: now, UsageKnown: true, CostKnown: true, InputTokens: 100, Status: "completed"},
		{RequestID: "explicit-zero", RequestFamily: "codex", SessionID: "zero", StartTime: now, UsageKnown: true, CostKnown: true, CostOverrideUSD: ptrSessionCost(0), Status: "completed"},
		{RequestID: "not-reported", RequestFamily: "codex", SessionID: "unknown", StartTime: now, Status: "pending"},
	} {
		if err := tr.hotPool.Add(req); err != nil {
			t.Fatal(err)
		}
	}
	result, err := tr.QueryRequestSessions(context.Background(), nil, 1, 20, "cost", "desc")
	if err != nil {
		t.Fatal(err)
	}
	if result.KnownCostUSD != 12.3456 || result.MissingCosts != 2 || result.TotalRequests != 4 || result.Rows[0].TotalCostUSD == nil || *result.Rows[0].TotalCostUSD != 12.3456 || *result.Rows[1].TotalCostUSD != 0 {
		t.Fatalf("热池不能用现价覆盖历史成本，未结算也不能显示真实零: %+v", result)
	}
	trend, err := tr.QueryRequestSessionTrend(context.Background(), &QueryOptions{SessionID: "unknown", RequestFamily: "codex"}, "", 0)
	if err != nil || trend.Points[0].FullInput != nil || trend.Points[0].Cost != nil {
		t.Fatalf("热池未上报应保留未知: %+v %v", trend, err)
	}
}

func TestRequestSessions_TrendBoundedSpikeAndStableSelection(t *testing.T) {
	tr := newModelUsageTestTracker(t)
	now := time.Now()
	for i := 1; i <= 240; i++ {
		input := 28000
		if i == 5 || i == 205 {
			input = 196000
		}
		seedSessionRequest(t, tr, fmt.Sprintf("req-%03d", i), "codex", "long", now.Add(time.Duration(i)*time.Second), input, 0, 100, 1.0)
	}
	opts := &QueryOptions{RequestFamily: "codex", SessionID: "long"}
	trend, err := tr.QueryRequestSessionTrend(context.Background(), opts, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if trend.SelectedID != "req-205" || trend.Page != 3 || len(trend.Points) != 40 || trend.Total != 240 {
		t.Fatalf("最大突增并列取最近，且读取有界: %+v", trend)
	}
	trend, err = tr.QueryRequestSessionTrend(context.Background(), opts, "req-005", 0)
	if err != nil || trend.SelectedID != "req-005" || trend.Page != 1 || len(trend.Points) != 100 {
		t.Fatalf("刷新必须保留用户选择: %+v %v", trend, err)
	}
	trend, err = tr.QueryRequestSessionTrend(context.Background(), opts, "req-005", 2)
	if err != nil || trend.SelectedID != "req-101" || trend.Points[0].Previous == nil {
		t.Fatalf("跨页前值不能丢失: %+v %v", trend, err)
	}
	trend, err = tr.QueryRequestSessionTrend(context.Background(), opts, "removed", 0)
	if err != nil || !trend.SelectedMissing || trend.SelectedID != "" {
		t.Fatalf("范围外选择需明确标记: %+v %v", trend, err)
	}
	// 同一时间的稳定 ID 顺序也必须与请求定位一致。
	_, err = tr.GetWriteDB().Exec(`UPDATE request_logs SET start_time = ?`, timezonepolicy.FormatStorage(now))
	if err != nil {
		t.Fatal(err)
	}
	trend, err = tr.QueryRequestSessionTrend(context.Background(), opts, "", 0)
	if err != nil || trend.SelectedID != "req-205" {
		t.Fatalf("稳定键排序错误: %+v %v", trend, err)
	}
}

func TestRequestSessions_QueryPlanAndValidation(t *testing.T) {
	tr := newModelUsageTestTracker(t)
	ctx := context.Background()
	base, args, err := tr.sessionSource(&QueryOptions{SessionID: "test", RequestFamily: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := tr.readDB.QueryContext(ctx, "EXPLAIN QUERY PLAN "+base+" SELECT request_id FROM scoped", args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var a, b, c int
		var detail string
		if err := rows.Scan(&a, &b, &c, &detail); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(detail + "\n")
	}
	t.Log(plan.String())
	if !strings.Contains(plan.String(), "INDEX") || !strings.Contains(plan.String(), "session") {
		t.Fatal("会话读取应使用现有会话索引")
	}
	base, args, err = tr.sessionSource(modelUsageQueryRange(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	summaryPlan, err := tr.readDB.QueryContext(ctx, "EXPLAIN QUERY PLAN "+base+sessionSummarySQL, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer summaryPlan.Close()
	plan.Reset()
	for summaryPlan.Next() {
		var a, b, c int
		var detail string
		if err := summaryPlan.Scan(&a, &b, &c, &detail); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(detail + "\n")
	}
	if err := summaryPlan.Err(); err != nil {
		t.Fatal(err)
	}
	t.Log(plan.String())
	if strings.Contains(plan.String(), "MATERIALIZE session_records") || !strings.Contains(plan.String(), "SEARCH request_logs USING INDEX idx_request_logs_start_time") {
		t.Fatalf("范围汇总必须下推日期索引，不能物化全部历史记录: %s", plan.String())
	}
	if _, err := tr.QueryRequestSessions(ctx, nil, 1, 20, "cost; DROP TABLE request_logs", "desc"); err == nil {
		t.Fatal("必须拒绝非法排序")
	}
	if _, err := tr.QueryRequestSessionTrend(ctx, &QueryOptions{}, "", 0); err == nil {
		t.Fatal("必须拒绝无会话的走势读取")
	}
	if _, err := tr.LocateRequestPage(ctx, nil, "missing", 20); err == nil {
		t.Fatal("必须报告范围外目标")
	}
}
