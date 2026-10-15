package main

import (
	"testing"
	"time"

	"sinuo/internal/proxy"
)

func TestToClaudeSubscriptionRateLimitInfo_FormatsUTCAndKeepsMissingEmpty(t *testing.T) {
	used := 29.0
	reset := time.Unix(1790500000, 0).UTC()
	captured := time.Date(2026, 9, 27, 11, 30, 0, 0, time.FixedZone("CST", 8*3600))

	info := toClaudeSubscriptionRateLimitInfo(proxy.ClaudeSubscriptionQuotaState{
		Revision: 3,
		Headers: &proxy.ClaudeSubscriptionRateLimit{
			Status:              "rejected",
			RepresentativeClaim: "five_hour",
			ResetAt:             &reset,
			OverageStatus:       "allowed",
			FiveHour:            proxy.ClaudeSubscriptionQuotaWindow{UsedPercent: &used, ResetAt: &reset},
			CapturedAt:          captured,
		},
		CredentialAvailable: true,
	})

	if info.Revision != 3 || !info.CredentialAvailable || info.Usage != nil || info.RefreshAvailableAt != "" || info.LastQueriedAt != "" {
		t.Fatalf("状态字段应原样带出，未查询过时查询相关字段为空: %+v", info)
	}
	headers := info.Headers
	if headers == nil || headers.Status != "rejected" || headers.RepresentativeClaim != "five_hour" || headers.OverageStatus != "allowed" {
		t.Fatalf("响应头状态字段应原样带出: %+v", headers)
	}
	if headers.ResetAt != "2026-09-27T09:06:40.000000Z" || headers.FiveHour.ResetAt != headers.ResetAt {
		t.Fatalf("重置时间应为固定微秒精度 UTC，got reset=%q 5h=%q", headers.ResetAt, headers.FiveHour.ResetAt)
	}
	if headers.CapturedAt != "2026-09-27T03:30:00.000000Z" {
		t.Fatalf("采集时间应转成 UTC，got %q", headers.CapturedAt)
	}
	if headers.FiveHour.UsedPercent == nil || *headers.FiveHour.UsedPercent != 29 {
		t.Fatalf("5h 已用应带出，got %v", headers.FiveHour.UsedPercent)
	}
	if headers.SevenDay.UsedPercent != nil || headers.SevenDay.ResetAt != "" {
		t.Fatalf("官方没带的窗口应保持 null / 空串，got %+v", headers.SevenDay)
	}
}

func TestToClaudeSubscriptionRateLimitInfo_UsageAndQueryState(t *testing.T) {
	used, sonnet, credits := 37.5, 12.0, 1234.0
	fetched := time.Date(2026, 9, 28, 9, 30, 0, 0, time.FixedZone("CST", 8*3600))
	available := fetched.Add(time.Minute)

	info := toClaudeSubscriptionRateLimitInfo(proxy.ClaudeSubscriptionQuotaState{
		Usage: &proxy.ClaudeSubscriptionUsage{
			FiveHour:    proxy.ClaudeSubscriptionQuotaWindow{UsedPercent: &used},
			ModelWeekly: []proxy.ClaudeSubscriptionModelQuota{{Label: "Sonnet", ClaudeSubscriptionQuotaWindow: proxy.ClaudeSubscriptionQuotaWindow{UsedPercent: &sonnet}}},
			ExtraUsage:  &proxy.ClaudeSubscriptionExtraUsage{Enabled: true, UsedCredits: &credits, Currency: "USD"},
			FetchedAt:   fetched,
		},
		RefreshAvailableAt: &available,
		LastQueriedAt:      &fetched,
		LastError:          "官方限流，已暂停查询",
	})

	if info.Headers != nil {
		t.Fatalf("没有响应头快照时应为 null: %+v", info.Headers)
	}
	usage := info.Usage
	if usage == nil || usage.FetchedAt != "2026-09-28T01:30:00.000000Z" || *usage.FiveHour.UsedPercent != 37.5 {
		t.Fatalf("主动查询结果应带出并转 UTC: %+v", usage)
	}
	if len(usage.ModelWeekly) != 1 || usage.ModelWeekly[0].Label != "Sonnet" || usage.ModelWeekly[0].ResetAt != "" {
		t.Fatalf("按模型周额度应带出，缺失的重置时间为空串: %+v", usage.ModelWeekly)
	}
	if usage.ExtraUsage == nil || !usage.ExtraUsage.Enabled || *usage.ExtraUsage.UsedCredits != 1234 || usage.ExtraUsage.Currency != "USD" {
		t.Fatalf("额外用量应带出: %+v", usage.ExtraUsage)
	}
	if info.RefreshAvailableAt != "2026-09-28T01:31:00.000000Z" || info.LastQueriedAt != "2026-09-28T01:30:00.000000Z" || info.LastError == "" {
		t.Fatalf("查询状态应带出: %+v", info)
	}
}

func TestClaudeSubscriptionRateLimitAPI_BeforeProxyReady(t *testing.T) {
	app := &App{}
	if info := app.GetClaudeSubscriptionRateLimit(); info != nil {
		t.Fatalf("代理处理器未就绪时应返回 null，got %+v", info)
	}
	if _, err := app.RefreshClaudeSubscriptionRateLimit(); err == nil {
		t.Fatal("代理处理器未就绪时手动刷新应返回错误")
	}
}
