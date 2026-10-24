package main

import (
	"fmt"
	"math"
	"testing"
	"time"

	timezonepolicy "sinuo/internal/timezone"
	"sinuo/internal/tracking"
)

func TestCacheWriteEndpointCosts_UsesDeduplicatedTotalsAndStoredCosts(t *testing.T) {
	app := newUsageStatsTestApp(t)
	now := timezonepolicy.FormatStorage(time.Now())
	for _, row := range []struct {
		id, family, path                   string
		input, output, creation, cacheRead int64
		cost                               float64
	}{
		{"history", "codex", "/v1/responses", 272077, 1671, 0, 265159, 0.123456},
		{"current", "codex", "/backend-api/codex/responses/compact", 272077, 1671, 2000, 265159, 0.417889},
		{"claude", "claude", "/v1/messages", 100, 50, 10, 20, 0.5},
	} {
		_, err := app.usageTracker.GetWriteDB().Exec(`INSERT INTO request_logs (
			request_id, start_time, status, request_family, path, upstream_type, upstream_name,
			input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens, total_cost_usd
		) VALUES (?, ?, 'completed', ?, ?, 'endpoint', ?, ?, ?, ?, ?, ?)`,
			row.id, now, row.family, row.path, row.family, row.input, row.output, row.creation, row.cacheRead, row.cost)
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := app.GetEndpointCosts(EndpointCostQueryParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("上游成本分组不正确：%+v", got)
	}
	want := []EndpointCostItem{
		{Name: "codex", RequestFamily: "codex", UpstreamType: "endpoint", Tokens: 273748 * 2, Cost: 0.123456 + 0.417889},
		{Name: "claude", RequestFamily: "claude", UpstreamType: "endpoint", Tokens: 180, Cost: 0.5},
	}
	for i, row := range got {
		if row.Name != want[i].Name || row.RequestFamily != want[i].RequestFamily || row.UpstreamType != want[i].UpstreamType || row.Tokens != want[i].Tokens || math.Abs(row.Cost-want[i].Cost) > 1e-12 {
			t.Fatalf("上游排行重复累计缓存或改变了已存费用：got=%+v want=%+v", got, want)
		}
	}
}

func TestGetEndpointCosts_NormalizesAndReturnsUpstreamTypes(t *testing.T) {
	app := newUsageStatsTestApp(t)
	now := timezonepolicy.FormatStorage(time.Now())
	for _, row := range []struct {
		id, upstreamType, upstreamName string
	}{
		{id: "cost-account", upstreamType: "account", upstreamName: "账号上游"},
		{id: "cost-subscription", upstreamType: "subscription", upstreamName: "订阅上游"},
		{id: "cost-empty", upstreamType: "", upstreamName: "空类型上游"},
	} {
		_, err := app.usageTracker.GetWriteDB().Exec(`INSERT INTO request_logs (
			request_id, start_time, status, request_family, upstream_type, upstream_name,
			input_tokens, output_tokens, total_cost_usd
		) VALUES (?, ?, 'completed', 'claude', ?, ?, 100, 20, 0.1)`,
			row.id, now, row.upstreamType, row.upstreamName)
		if err != nil {
			t.Fatal(err)
		}
	}

	got, err := app.GetEndpointCosts(EndpointCostQueryParams{})
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]string, len(got))
	for _, row := range got {
		byName[row.Name] = row.UpstreamType
	}
	for name, wantType := range map[string]string{
		"账号上游":  "account",
		"订阅上游":  "subscription",
		"空类型上游": "endpoint",
	} {
		if gotType := byName[name]; gotType != wantType {
			t.Errorf("上游 %q 类型不正确：got %q want %q；全部结果=%+v", name, gotType, wantType, got)
		}
	}
}

func TestGetRequestTrendData_BucketsComeFromRequestLogs(t *testing.T) {
	app := newUsageStatsTestApp(t)
	db := app.usageTracker.GetWriteDB()
	if db == nil {
		t.Fatal("expected write db")
	}

	now := time.Now().UTC()
	for _, row := range []struct {
		id    string
		start time.Time
	}{
		{id: "trend-recent", start: now.Add(-30 * time.Minute)},
		{id: "trend-two-days", start: now.Add(-48 * time.Hour)},
		{id: "trend-ten-days", start: now.Add(-10 * 24 * time.Hour)},
	} {
		if _, err := db.Exec(`INSERT INTO request_logs (
			request_id, start_time, status, request_family, upstream_type, upstream_name, model_name
		) VALUES (?, ?, 'completed', 'claude', 'endpoint', 'upstream', 'claude-sonnet-4')`,
			row.id, timezonepolicy.FormatStorage(row.start)); err != nil {
			t.Fatalf("failed to seed %s: %v", row.id, err)
		}
	}

	sum := func(data RequestTrendData) int64 {
		var total int64
		for _, point := range data.Points {
			total += point.Total
		}
		return total
	}

	sevenDays, err := app.GetRequestTrendData(RequestTrendQueryParams{Period: "7d", SourceView: "all"})
	if err != nil {
		t.Fatalf("GetRequestTrendData(7d) failed: %v", err)
	}
	if sevenDays.Granularity != "day" {
		t.Fatalf("expected day granularity for 7d, got %q", sevenDays.Granularity)
	}
	if len(sevenDays.Points) == 0 {
		t.Fatal("expected non-empty trend points")
	}
	if got := sum(sevenDays); got != 2 {
		t.Fatalf("expected 2 requests inside the 7d window, got %d", got)
	}

	oneDay, err := app.GetRequestTrendData(RequestTrendQueryParams{Period: "1d", SourceView: "all"})
	if err != nil {
		t.Fatalf("GetRequestTrendData(1d) failed: %v", err)
	}
	if oneDay.Granularity != "hour" {
		t.Fatalf("expected hour granularity for 1d, got %q", oneDay.Granularity)
	}
	if got := sum(oneDay); got != 1 {
		t.Fatalf("expected 1 request inside the 1d window, got %d", got)
	}

	oneHour, err := app.GetRequestTrendData(RequestTrendQueryParams{Period: "1h", SourceView: "all"})
	if err != nil {
		t.Fatalf("GetRequestTrendData(1h) failed: %v", err)
	}
	if oneHour.Granularity != "minute" {
		t.Fatalf("expected minute granularity for 1h, got %q", oneHour.Granularity)
	}
	if got := sum(oneHour); got != 1 {
		t.Fatalf("expected 1 request inside the 1h window, got %d", got)
	}

	// 桶起点必须是 UTC ISO，且与返回的时间范围一致
	if oneHour.StartTime == "" || oneHour.EndTime == "" {
		t.Fatalf("expected range metadata, got %+v", oneHour)
	}
	if len(oneHour.Points) > 0 && oneHour.Points[0].Time == "" {
		t.Fatal("expected bucket start timestamp")
	}
}

func TestGetRequestTrendData_RejectsInvalidStartDate(t *testing.T) {
	app := newUsageStatsTestApp(t)

	if _, err := app.GetRequestTrendData(RequestTrendQueryParams{StartDate: "not-a-date"}); err == nil {
		t.Fatal("expected invalid start_date to return an error")
	}
}

func TestGetRequestTrendData_ExactWindowMatchesModelStats(t *testing.T) {
	for _, period := range []string{"1h", "1d", "7d", "30d"} {
		t.Run(period, func(t *testing.T) {
			app := newUsageStatsTestApp(t)
			policy, err := app.activeTimezonePolicy()
			if err != nil {
				t.Fatal(err)
			}
			start, _ := policy.ParseInput("2026-09-02T10:07:31.123456")
			end, _ := policy.ParseInput("2026-09-02T11:08:32.654321")
			for i, at := range []time.Time{start.Add(-time.Microsecond), start, start.Add(time.Minute), end.Add(-time.Microsecond), end, end.Add(time.Minute)} {
				duration := int64(0)
				if i == 1 {
					duration = 100
				}
				if i == 2 {
					duration = 300
				}
				_, err := app.usageTracker.GetWriteDB().Exec(`INSERT INTO request_logs
      (request_id,start_time,status,request_family,upstream_type,model_name,duration_ms)
      VALUES (?,?,'completed','codex','account','m',?)`, fmt.Sprintf("edge-%d", i), timezonepolicy.FormatStorage(at), duration)
				if err != nil {
					t.Fatal(err)
				}
			}
			query := RequestTrendQueryParams{Period: period, StartDate: formatAPITime(start), EndDate: formatAPITime(end), SourceView: "account", Model: "m"}
			trend, err := app.GetRequestTrendData(query)
			if err != nil {
				t.Fatal(err)
			}
			model, err := app.GetModelUsageStats(ModelUsageQueryParams{Period: period, StartDate: query.StartDate, EndDate: query.EndDate, SourceView: "account", Model: "m"})
			if err != nil {
				t.Fatal(err)
			}
			var total, samples int64
			var durationSum float64
			for _, p := range trend.Points {
				total += p.Total
				samples += p.DurationCount
				durationSum += p.AvgDurationMs * float64(p.DurationCount)
			}
			if total != 3 || total != model.TotalRequests {
				t.Fatalf("trend=%d model=%d, expected 3", total, model.TotalRequests)
			}
			if samples != 2 || durationSum != 400 {
				t.Fatalf("bad duration samples: n=%d sum=%f", samples, durationSum)
			}
			if trend.StartTime != model.StartTime || trend.EndTime != model.EndTime {
				t.Fatal("range metadata differs")
			}
		})
	}
}

func TestOverviewStats_RejectIncompleteReversedAndOversizedRanges(t *testing.T) {
	app := newUsageStatsTestApp(t)
	for _, query := range []RequestTrendQueryParams{
		{StartDate: "2026-09-02T10:00:00Z"},
		{EndDate: "2026-09-02T10:00:00Z"},
		{StartDate: "2026-09-02T11:00:00Z", EndDate: "2026-09-02T10:00:00Z"},
	} {
		if _, err := app.GetRequestTrendData(query); err == nil {
			t.Fatalf("expected invalid range error: %+v", query)
		}
		if _, err := app.GetModelUsageStats(ModelUsageQueryParams{StartDate: query.StartDate, EndDate: query.EndDate}); err == nil {
			t.Fatal("model stats must reject the same invalid range")
		}
	}
	if _, err := app.GetRequestTrendData(RequestTrendQueryParams{Period: "1h", StartDate: "2020-01-01T00:00:00Z", EndDate: "2026-01-01T00:00:00Z"}); err == nil {
		t.Fatal("expected oversized bucket error")
	}
}

func TestGetRequestTrendChart_CompatibilityKeepsCumulativeHistory(t *testing.T) {
	app := newUsageStatsTestApp(t)
	recordRuntimeStats(app, nil)
	app.monitoringMiddleware.GetMetrics().AddHistoryDataPoints()
	points := app.GetRequestTrendChart(60)
	if len(points) != 1 || points[0].Total != 1 {
		t.Fatalf("legacy history changed: %+v", points)
	}
}

// 趋势分桶是生成口径：独立压缩不进入请求数趋势，但同一窗口的 Token / 成本仍计入。
func TestGetRequestTrendData_GenerationScopeExcludesCompaction(t *testing.T) {
	app := newUsageStatsTestApp(t)
	insertRequestLogRow(t, app, seededRequestLog{
		ID: "gen-1", Kind: tracking.RequestKindGeneration, Source: tracking.RequestSourceLegacyProxy,
		UpstreamType: "account", UpstreamName: "account-a", Model: "m",
		Status: "completed", InputTokens: 10, OutputTokens: 2, Cost: 0.3,
	})
	insertRequestLogRow(t, app, seededRequestLog{
		ID: "comp-1", Kind: tracking.RequestKindCompaction, Source: tracking.RequestSourceLegacyProxy,
		UpstreamType: "account", UpstreamName: "account-a", Model: "m",
		Status: "completed", InputTokens: 100, OutputTokens: 5, Cost: 1.2,
	})

	now := time.Now()
	query := RequestTrendQueryParams{
		Period:     "1d",
		StartDate:  formatAPITime(now.Add(-1 * time.Hour)),
		EndDate:    formatAPITime(now.Add(1 * time.Hour)),
		SourceView: "account",
		Model:      "m",
	}
	trend, err := app.GetRequestTrendData(query)
	if err != nil {
		t.Fatalf("GetRequestTrendData failed: %v", err)
	}
	var total int64
	for _, point := range trend.Points {
		total += point.Total
	}
	if total != 1 {
		t.Fatalf("compaction must not enter the generation trend, got %d", total)
	}

	model, err := app.GetModelUsageStats(ModelUsageQueryParams{
		Period: "1d", StartDate: query.StartDate, EndDate: query.EndDate, SourceView: "account", Model: "m",
	})
	if err != nil {
		t.Fatalf("GetModelUsageStats failed: %v", err)
	}
	if model.TotalRequests != 1 || model.TotalTokens != 117 || model.TotalCostUSD != 1.5 {
		t.Fatalf("model summary must count generation but carry full usage: %+v", model)
	}
}

// 上游成本排行跟随概览时间切换：只汇总请求窗口内的记录，窗口参数与模型调用统计同一套解析规则。
func TestGetEndpointCosts_UsesRequestedWindow(t *testing.T) {
	app := newUsageStatsTestApp(t)
	now := time.Now().UTC()
	for _, row := range []struct {
		name  string
		start time.Time
		cost  float64
	}{
		{"recent", now.Add(-30 * time.Minute), 0.1},
		{"hours-ago", now.Add(-3 * time.Hour), 0.2},
		{"days-ago", now.Add(-3 * 24 * time.Hour), 0.4},
	} {
		if _, err := app.usageTracker.GetWriteDB().Exec(`INSERT INTO request_logs (
			request_id, start_time, status, request_family, upstream_type, upstream_name, input_tokens, total_cost_usd
		) VALUES (?, ?, 'completed', 'claude', 'endpoint', ?, 10, ?)`,
			"req-"+row.name, timezonepolicy.FormatStorage(row.start), row.name, row.cost); err != nil {
			t.Fatal(err)
		}
	}

	const layout = "2006-01-02T15:04:05-07:00"
	for _, tc := range []struct {
		name   string
		params EndpointCostQueryParams
		want   []string
	}{
		{"显式窗口", EndpointCostQueryParams{Period: "1h", StartDate: now.Add(-time.Hour).Format(layout), EndDate: now.Add(time.Minute).Format(layout)}, []string{"recent"}},
		{"默认近 24 小时", EndpointCostQueryParams{}, []string{"hours-ago", "recent"}},
		{"近 7 天", EndpointCostQueryParams{Period: "7d"}, []string{"days-ago", "hours-ago", "recent"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := app.GetEndpointCosts(tc.params)
			if err != nil {
				t.Fatal(err)
			}
			names := make([]string, 0, len(got))
			for _, row := range got {
				names = append(names, row.Name)
			}
			if fmt.Sprint(names) != fmt.Sprint(tc.want) {
				t.Fatalf("窗口内上游不正确：got %v want %v", names, tc.want)
			}
		})
	}
}

func TestGetEndpointCosts_RejectsInvalidWindow(t *testing.T) {
	app := newUsageStatsTestApp(t)
	now := time.Now()
	const layout = "2006-01-02T15:04:05-07:00"
	for name, params := range map[string]EndpointCostQueryParams{
		"只有开始时间": {StartDate: now.Add(-time.Hour).Format(layout)},
		"结束早于开始": {StartDate: now.Format(layout), EndDate: now.Add(-time.Hour).Format(layout)},
		"时间格式错误": {StartDate: "yesterday", EndDate: "today"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := app.GetEndpointCosts(params)
			if err == nil {
				t.Fatalf("非法窗口应返回错误，而不是空排行：%+v", got)
			}
		})
	}
}

// 「今日」的窗口：开始是配置时区今天 0 点的墙钟时间，结束是快照时刻的 UTC；
// 时段统计的三个接口都要按同一窗口解析，跨过 0 点的记录不计入。
func TestOverviewTodayWindow_SharedByPeriodStatsAPIs(t *testing.T) {
	app := newUsageStatsTestApp(t)
	policy, err := app.activeTimezonePolicy()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	date := policy.BusinessDate(now)
	dayStart, _, err := policy.DayRange(date)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		name  string
		start time.Time
	}{
		{"today", dayStart},
		{"yesterday", dayStart.Add(-time.Second)},
	} {
		if _, err := app.usageTracker.GetWriteDB().Exec(`INSERT INTO request_logs (
			request_id, start_time, status, request_family, request_kind, upstream_type, upstream_name, model_name, input_tokens, total_cost_usd
		) VALUES (?, ?, 'completed', 'claude', 'generation', 'endpoint', ?, ?, 10, 0.1)`,
			"req-"+row.name, timezonepolicy.FormatStorage(row.start), row.name, row.name); err != nil {
			t.Fatal(err)
		}
	}

	startDate, endDate := date+"T00:00", now.UTC().Format(time.RFC3339Nano)
	costs, err := app.GetEndpointCosts(EndpointCostQueryParams{Period: "1d", StartDate: startDate, EndDate: endDate})
	if err != nil || len(costs) != 1 || costs[0].Name != "today" {
		t.Fatalf("上游成本应只含今日：%+v err=%v", costs, err)
	}
	usage, err := app.GetModelUsageStats(ModelUsageQueryParams{Period: "1d", StartDate: startDate, EndDate: endDate, SourceView: "all", Limit: 20})
	if err != nil || len(usage.Rows) != 1 || usage.Rows[0].ModelName != "today" {
		t.Fatalf("模型调用应只含今日：%+v err=%v", usage.Rows, err)
	}
	trend, err := app.GetRequestTrendData(RequestTrendQueryParams{Period: "1d", StartDate: startDate, EndDate: endDate, SourceView: "all"})
	if err != nil || trend.Granularity != "hour" {
		t.Fatalf("今日趋势应按小时分桶：%+v err=%v", trend, err)
	}
	var total int64
	for _, point := range trend.Points {
		total += point.Total
	}
	if total != 1 {
		t.Fatalf("今日趋势应只含 1 个请求，got %d：%+v", total, trend.Points)
	}
}
