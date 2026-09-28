package proxy

import (
	"errors"
	"testing"
	"time"
)

func TestParseClaudeSubscriptionUsage_FullPayload(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 30, 0, 0, time.FixedZone("CST", 8*3600))
	usage, err := parseClaudeSubscriptionUsage([]byte(`{
		"five_hour": {"utilization": 37.456, "resets_at": "2026-09-28T05:00:00.123456+00:00"},
		"seven_day": {"utilization": 61, "resets_at": "2026-10-02T08:00:00+08:00"},
		"seven_day_oauth_apps": null,
		"seven_day_opus": null,
		"seven_day_sonnet": {"utilization": 12, "resets_at": "2026-10-02T00:00:00Z"},
		"limits": [
			{"kind": "weekly_scoped", "percent": 40, "resets_at": "2026-10-03T00:00:00Z", "scope": {"model": {"display_name": "Fable"}}},
			{"kind": "weekly_scoped", "percent": 99, "resets_at": null, "scope": {"model": {"display_name": "sonnet"}}},
			{"kind": "session", "percent": 5, "resets_at": null, "scope": {}}
		],
		"extra_usage": {"is_enabled": true, "monthly_limit": 5000, "used_credits": 1234, "utilization": 24.68, "currency": "usd"}
	}`), now)
	if err != nil {
		t.Fatalf("完整响应应解析成功: %v", err)
	}
	assertClaudeRateLimitPercent(t, "5h 已用（保留两位）", usage.FiveHour.UsedPercent, 37.46)
	if usage.FiveHour.ResetAt == nil || !usage.FiveHour.ResetAt.Equal(time.Date(2026, 9, 28, 5, 0, 0, 123456000, time.UTC)) || usage.FiveHour.ResetAt.Location() != time.UTC {
		t.Fatalf("5h 重置时间应解析为 UTC，got %v", usage.FiveHour.ResetAt)
	}
	assertClaudeRateLimitPercent(t, "7d 已用", usage.SevenDay.UsedPercent, 61)
	assertClaudeRateLimitTime(t, "7d 重置时间（带偏移转 UTC）", usage.SevenDay.ResetAt, time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC).Unix())

	if len(usage.ModelWeekly) != 2 || usage.ModelWeekly[0].Label != "Sonnet" || usage.ModelWeekly[1].Label != "Fable" {
		t.Fatalf("按模型周额度应合并旧字段与 limits[]、同名去重、跳过非 weekly_scoped，got %+v", usage.ModelWeekly)
	}
	assertClaudeRateLimitPercent(t, "Sonnet 周已用取旧字段", usage.ModelWeekly[0].UsedPercent, 12)
	assertClaudeRateLimitPercent(t, "Fable 周已用", usage.ModelWeekly[1].UsedPercent, 40)

	extra := usage.ExtraUsage
	if extra == nil || !extra.Enabled || extra.Currency != "USD" {
		t.Fatalf("额外用量应带出并规范币种，got %+v", extra)
	}
	assertClaudeRateLimitPercent(t, "额外用量已用", extra.UsedPercent, 24.68)
	assertClaudeRateLimitPercent(t, "额外用量金额", extra.UsedCredits, 1234)
	assertClaudeRateLimitPercent(t, "额外用量上限", extra.MonthlyLimit, 5000)
	if !usage.FetchedAt.Equal(now) || usage.FetchedAt.Location() != time.UTC {
		t.Fatalf("查询时间应为 UTC，got %v", usage.FetchedAt)
	}
}

func TestParseClaudeSubscriptionUsage_MissingAndInvalidFieldsStayNil(t *testing.T) {
	usage, err := parseClaudeSubscriptionUsage([]byte(`{
		"five_hour": {"utilization": -1, "resets_at": "not-a-time"},
		"seven_day": {"utilization": null, "resets_at": null},
		"seven_day_opus": {"utilization": null, "resets_at": null}
	}`), time.Now())
	if err != nil {
		t.Fatalf("字段非法不应整体失败: %v", err)
	}
	if usage.FiveHour.UsedPercent != nil || usage.FiveHour.ResetAt != nil || usage.SevenDay.UsedPercent != nil {
		t.Fatalf("缺失或非法的数值应保持 nil，不能填 0: %+v", usage)
	}
	if len(usage.ModelWeekly) != 0 || usage.ExtraUsage != nil {
		t.Fatalf("没有读数的按模型周额度与缺失的额外用量都不应出现: %+v", usage)
	}
}

func TestParseClaudeSubscriptionUsage_RejectsEmptyAndMalformed(t *testing.T) {
	if _, err := parseClaudeSubscriptionUsage([]byte(`{}`), time.Now()); !errors.Is(err, errClaudeUsageEmpty) {
		t.Fatalf("没有任何窗口时应返回 errClaudeUsageEmpty，got %v", err)
	}
	if _, err := parseClaudeSubscriptionUsage([]byte(`<html>`), time.Now()); err == nil {
		t.Fatal("非 JSON 响应应返回错误")
	}
}
