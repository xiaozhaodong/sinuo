package main

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	timezonepolicy "sinuo/internal/timezone"
)

func TestRequestSessionAPIs_DefaultRecentAndExplicitCost(t *testing.T) {
	app := newUsageStatsTestApp(t)
	now, err := time.Parse(time.RFC3339, "2026-11-03T10:00:00+08:00")
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"expensive-old", "cheap-new", "cheap-new-tie"} {
		at := now
		cost := 1
		if i == 0 {
			at = now.Add(-time.Hour)
			cost = 100
		}
		_, err := app.usageTracker.GetWriteDB().Exec(`INSERT INTO request_logs
		(request_id, session_id, request_family, start_time, status, request_kind, input_tokens, cache_read_tokens, total_cost_usd)
		VALUES (?, ?, 'codex', ?, 'completed', 'generation', 100, NULL, ?)`, id, id, timezonepolicy.FormatStorage(at), cost)
		if err != nil {
			t.Fatal(err)
		}
	}
	params := RequestSessionQueryParams{RequestQueryParams: RequestQueryParams{SourceView: "all", PageSize: 1,
		StartDate: "2026-11-03T00:00:00+08:00", EndDate: "2026-11-04T00:00:00+08:00"}}
	for page, want := range []string{"cheap-new", "cheap-new-tie", "expensive-old"} {
		params.Page = page + 1
		got, err := app.GetRequestSessions(params)
		if err != nil || got.Total != 3 || len(got.Rows) != 1 || got.Rows[0].SessionID != want {
			t.Fatalf("缺省最近活动须全局降序再分页，同时间稳定: %+v %v", got, err)
		}
		encoded, err := json.Marshal(got.Rows[0])
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]interface{}
		if err := json.Unmarshal(encoded, &fields); err != nil {
			t.Fatal(err)
		}
		if fields["missing_cache_usage"] != float64(1) || fields["missing_costs"] != float64(0) || fields["cache_ratio"] != nil {
			t.Fatalf("桌面DTO必须保留独立缓存缺失数: %s", encoded)
		}
	}
	params.Page, params.SortBy, params.SortOrder = 1, "cost", "desc"
	got, err := app.GetRequestSessions(params)
	if err != nil || got.Rows[0].SessionID != "expensive-old" {
		t.Fatalf("手动成本排序: %+v %v", got, err)
	}
}

func TestRequestSessionAPIs_SingleEndedRangesStayConsistent(t *testing.T) {
	app := newUsageStatsTestApp(t)
	policy, err := timezonepolicy.New("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	app.timezonePolicy = policy
	start, end, err := policy.DayRange(policy.BusinessDate(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		id string
		at time.Time
	}{
		{"before-day", start.Add(-time.Hour)}, {"at-11", start.Add(11 * time.Hour)},
		{"at-cutoff", start.Add(12 * time.Hour)}, {"at-13", start.Add(13 * time.Hour)}, {"at-day-end", end},
	} {
		_, err := app.usageTracker.GetWriteDB().Exec(`INSERT INTO request_logs
		(request_id, session_id, request_family, start_time, status, request_kind, upstream_type, input_tokens, cache_read_tokens, total_cost_usd)
		VALUES (?, 'range-session', 'codex', ?, 'completed', 'generation', 'account', 100, 50, 7)`, row.id, timezonepolicy.FormatStorage(row.at))
		if err != nil {
			t.Fatal(err)
		}
	}
	format := func(at time.Time) string { return at.Format(time.RFC3339) }
	for _, tc := range []struct {
		name, from, to, target string
		count                  int64
	}{
		{"仅开始", format(start.Add(12 * time.Hour)), "", "at-cutoff", 2},
		{"仅结束", "", format(start.Add(12 * time.Hour)), "at-11", 1},
		{"双端", format(start.Add(11 * time.Hour)), format(start.Add(12 * time.Hour)), "at-11", 1},
		{"完全缺省", "", "", "at-11", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			include := true
			params := RequestQueryParams{SourceView: "all", RequestFamily: "codex", SessionID: "range-session", StartDate: tc.from, EndDate: tc.to, IncludeCompaction: &include}
			rank, err := app.GetRequestSessions(RequestSessionQueryParams{RequestQueryParams: params})
			if err != nil || rank.TotalRequests != tc.count || rank.KnownCostUSD != float64(tc.count)*7 {
				t.Fatalf("排行: %+v %v", rank, err)
			}
			trend, err := app.GetRequestSessionTrend(params, "", 0)
			if err != nil || int64(trend.Total) != tc.count {
				t.Fatalf("走势: %+v %v", trend, err)
			}
			for _, point := range trend.Points {
				if point.RequestID == "at-day-end" || ((tc.name == "仅结束" || tc.name == "双端") && point.RequestID == "at-cutoff") {
					t.Fatalf("截止时刻必须排除: %+v", point)
				}
			}
			params.TargetRequestID = tc.target
			list, err := app.GetRequests(params)
			if err != nil || int64(list.Total) != tc.count {
				t.Fatalf("下钻列表: %+v %v", list, err)
			}
			statsParams := UsageStatsQueryParams{SourceView: "all", RequestFamily: "codex", SessionID: "range-session", StartDate: tc.from, EndDate: tc.to, IncludeCompaction: &include}
			stats, err := app.GetUsageStats(statsParams)
			if err != nil || int64(stats.TotalRequests) != tc.count || stats.TotalCostUSD != float64(tc.count)*7 {
				t.Fatalf("请求页统计: %+v %v", stats, err)
			}
			if tc.from != "" || tc.to != "" {
				statsParams.IncludeCompaction = nil
				stats, err = app.GetUsageStats(statsParams)
				if err != nil || int64(stats.TotalRequests) != tc.count {
					t.Fatalf("自定义区间也应共用边界: %+v %v", stats, err)
				}
			}
		})
	}
	from, to, period, err := resolveUsageStatsRange(policy, "30d", "", "")
	if err != nil || period != "30d" || !from.Equal(to.AddDate(0, 0, -30)) {
		t.Fatalf("概览缺省 period 不应改变: %s %s %s %v", from, to, period, err)
	}
}

func TestRequestSessionAPIs_KnownCostAndIndependentMissingCounts(t *testing.T) {
	app := newUsageStatsTestApp(t)
	now := timezonepolicy.FormatStorage(time.Now())
	for _, row := range []struct {
		id, session       string
		cost, input, read interface{}
	}{
		{"known", "partial", 8.125, 100, 93},
		{"missing-cost", "partial", nil, 100, 50},
		{"missing-usage", "partial", 0.0, nil, nil},
		{"zero", "zero", 0.0, 0, 0},
		{"unknown", "unknown", nil, nil, nil},
	} {
		_, err := app.usageTracker.GetWriteDB().Exec(`INSERT INTO request_logs
		(request_id, session_id, request_family, start_time, status, request_kind, total_cost_usd, input_tokens, cache_read_tokens)
		VALUES (?, ?, 'codex', ?, 'completed', 'generation', ?, ?, ?)`, row.id, row.session, now, row.cost, row.input, row.read)
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := app.GetRequestSessions(RequestSessionQueryParams{RequestQueryParams: RequestQueryParams{SourceView: "all"}, SortBy: "cost", SortOrder: "desc"})
	if err != nil || len(got.Rows) != 3 {
		t.Fatalf("会话API: %+v %v", got, err)
	}
	partial := got.Rows[0]
	if partial.SessionID != "partial" || partial.TotalCostUSD == nil || *partial.TotalCostUSD != 8.125 || partial.MissingCosts != 1 || partial.MissingCacheUsage != 1 || partial.CacheRatio == nil || *partial.CacheRatio != 0.715 {
		t.Fatalf("Wails数据应保留已知费用及独立缺失数: %+v", partial)
	}
	if got.Rows[1].TotalCostUSD == nil || *got.Rows[1].TotalCostUSD != 0 || got.Rows[1].MissingCosts != 0 || got.Rows[2].TotalCostUSD != nil || got.Rows[2].MissingCosts != 1 {
		t.Fatalf("API不能混淆真实零与全部未知: %+v", got.Rows)
	}
}

func TestRequestSessionAPIs_SelectionAndActualRequestPage(t *testing.T) {
	app := newUsageStatsTestApp(t)
	now := time.Now().Add(-time.Hour)
	for i := 1; i <= 70; i++ {
		input := 28000
		if i == 5 {
			input = 196000
		}
		_, err := app.usageTracker.GetWriteDB().Exec(`INSERT INTO request_logs
		(request_id, session_id, start_time, request_family, request_kind, upstream_type, status,
		input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens, total_cost_usd)
		VALUES (?, 'synthetic-session', ?, 'codex', 'generation', 'account', 'completed', ?, 10, 0, 100, 1)`,
			fmt.Sprintf("session-%03d", i), timezonepolicy.FormatStorage(now.Add(time.Duration(i)*time.Second)), input)
		if err != nil {
			t.Fatal(err)
		}
	}
	params := RequestQueryParams{StartDate: now.Add(-time.Minute).Format(time.RFC3339), EndDate: now.Add(time.Hour).Format(time.RFC3339), SourceView: "all", Page: 1, PageSize: 20}
	ranking, err := app.GetRequestSessions(RequestSessionQueryParams{RequestQueryParams: params})
	if err != nil || ranking.Total != 1 || ranking.Rows[0].RequestCount != 70 {
		t.Fatalf("排行: %+v %v", ranking, err)
	}
	params.RequestFamily, params.SessionID = "codex", "synthetic-session"
	trend, err := app.GetRequestSessionTrend(params, "", 0)
	if err != nil || trend.SelectedID != "session-005" {
		t.Fatalf("图和详情默认应选第5次: %+v %v", trend, err)
	}
	params.TargetRequestID = trend.SelectedID
	page, err := app.GetRequests(params)
	if err != nil || page.Page != 4 || page.Total != 70 {
		t.Fatalf("正式表应跳到第4页: %+v %v", page, err)
	}
	found := false
	for _, row := range page.Requests {
		found = found || row.RequestID == trend.SelectedID
	}
	if !found {
		t.Fatal("只返回详情快照而实际表格没有目标行")
	}
	params.TargetRequestID = "gone"
	if _, err := app.GetRequests(params); err == nil {
		t.Fatal("范围外目标应明确报错")
	}
}

func TestRequestSessionAPIs_MissingIDAndBusinessDate(t *testing.T) {
	app := newUsageStatsTestApp(t)
	now := time.Now()
	seedAPISessions(t, app, now)
	params := RequestQueryParams{SessionMissing: true, SourceView: "all", StartDate: now.Add(-time.Hour).Format(time.RFC3339), EndDate: now.Add(time.Hour).Format(time.RFC3339)}
	rank, err := app.GetRequestSessions(RequestSessionQueryParams{RequestQueryParams: params})
	if err != nil || rank.Total != 0 || rank.Ungrouped != 1 || rank.TotalRequests != 1 {
		t.Fatalf("未归组: %+v %v", rank, err)
	}
	requests, err := app.GetRequests(params)
	if err != nil || requests.Total != 1 || requests.Requests[0].SessionID != "" {
		t.Fatalf("无ID与未筛选必须区分: %+v %v", requests, err)
	}
	stats, err := app.GetUsageStats(UsageStatsQueryParams{SessionMissing: true, SourceView: "all", StartDate: params.StartDate, EndDate: params.EndDate})
	if err != nil || stats.TotalRequests != 1 {
		t.Fatalf("无ID统计与正式表不一致: %+v %v", stats, err)
	}
	params.SessionID = "conflict"
	if _, err := app.GetRequests(params); err == nil {
		t.Fatal("必须拒绝冲突筛选")
	}
	policy, err := timezonepolicy.New("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	for _, day := range []string{"2026-11-02", "2026-11-03"} {
		start, end, err := policy.DayRange(day)
		if err != nil {
			t.Fatal(err)
		}
		_, err = app.usageTracker.GetWriteDB().Exec(`INSERT INTO request_logs (request_id, session_id, request_family, start_time, status, request_kind, input_tokens, total_cost_usd) VALUES (?, 'cross-day', 'codex', ?, 'completed', 'compaction', 10, 3)`, day, timezonepolicy.FormatStorage(start))
		if err != nil {
			t.Fatal(err)
		}
		include := true
		query := RequestSessionQueryParams{RequestQueryParams: RequestQueryParams{SessionID: "cross-day", SourceView: "all", StartDate: start.Format(time.RFC3339), EndDate: end.Format(time.RFC3339), IncludeCompaction: &include}}
		got, err := app.GetRequestSessions(query)
		if err != nil || got.TotalRequests != 1 {
			t.Fatalf("跨业务日范围: %+v %v", got, err)
		}
		include = false
		got, err = app.GetRequestSessions(query)
		if err != nil || got.TotalRequests != 0 {
			t.Fatalf("不含压缩: %+v %v", got, err)
		}
	}
}
