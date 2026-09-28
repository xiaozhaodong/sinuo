// Package proxy - Claude Code 订阅直通的官方额度快照
// 创建时间: 2026-09-27
//
// 官方在响应头 anthropic-ratelimit-unified-* 里带回订阅额度，字段口径对齐 Claude Code 客户端的解析。
// 快照只在内存里（见 claude_subscription_quota.go），不落库；重启后等下一次订阅请求。
// 只有带 unified-status 的响应才整份替换快照：count_tokens、鉴权失败等不带额度头的响应保留旧值。
package proxy

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	claudeRateLimitHeaderPrefix = "Anthropic-Ratelimit-Unified-"
	// claudeRateLimitMaxUnixSeconds 重置时间的合理上限，超出按非法值丢弃，避免转换 int64 溢出
	claudeRateLimitMaxUnixSeconds = 1e11
)

// ClaudeSubscriptionQuotaWindow 单个额度窗口；官方没带的字段保持 nil，不填 0
type ClaudeSubscriptionQuotaWindow struct {
	UsedPercent *float64   // 已用百分比 0–100；官方给的是 0–1 小数
	ResetAt     *time.Time // 窗口重置时间（UTC）
}

// ClaudeSubscriptionRateLimit 最近一次订阅直通响应带回的官方额度
type ClaudeSubscriptionRateLimit struct {
	Status              string     // allowed / allowed_warning / rejected
	RepresentativeClaim string     // 当前卡在哪个窗口：five_hour / seven_day / seven_day_overage_included / overage
	ResetAt             *time.Time // 整体重置时间；rejected 时以它为准
	OverageStatus       string     // status=rejected 且此项为 allowed / allowed_warning 时表示正在消耗额外用量
	FiveHour            ClaudeSubscriptionQuotaWindow
	SevenDay            ClaudeSubscriptionQuotaWindow
	CapturedAt          time.Time
}

// parseClaudeSubscriptionRateLimit 解析官方额度头；不带 unified-status 时返回 false
func parseClaudeSubscriptionRateLimit(header http.Header, now time.Time) (ClaudeSubscriptionRateLimit, bool) {
	status := claudeRateLimitText(header, "status")
	if status == "" {
		return ClaudeSubscriptionRateLimit{}, false
	}
	return ClaudeSubscriptionRateLimit{
		Status:              status,
		RepresentativeClaim: claudeRateLimitText(header, "representative-claim"),
		ResetAt:             parseClaudeRateLimitUnix(header.Get(claudeRateLimitHeaderPrefix + "reset")),
		OverageStatus:       claudeRateLimitText(header, "overage-status"),
		FiveHour:            parseClaudeQuotaWindow(header, "5h"),
		SevenDay:            parseClaudeQuotaWindow(header, "7d"),
		CapturedAt:          now.UTC(),
	}, true
}

func claudeRateLimitText(header http.Header, name string) string {
	return strings.ToLower(strings.TrimSpace(header.Get(claudeRateLimitHeaderPrefix + name)))
}

// parseClaudeQuotaWindow 解析 <window>-utilization 与 <window>-reset
func parseClaudeQuotaWindow(header http.Header, window string) ClaudeSubscriptionQuotaWindow {
	return ClaudeSubscriptionQuotaWindow{
		UsedPercent: parseClaudeUtilizationPercent(header.Get(claudeRateLimitHeaderPrefix + window + "-utilization")),
		ResetAt:     parseClaudeRateLimitUnix(header.Get(claudeRateLimitHeaderPrefix + window + "-reset")),
	}
}

// parseClaudeUtilizationPercent 把 0–1 小数换算成保留两位的百分比；不设上限，超额时由展示端截断
func parseClaudeUtilizationPercent(raw string) *float64 {
	value, ok := parseClaudeRateLimitNumber(raw)
	if !ok || value < 0 {
		return nil
	}
	percent := math.Round(value*10000) / 100
	return &percent
}

// parseClaudeRateLimitUnix 解析 Unix 秒（可能带小数），与 Claude Code 一样四舍五入到秒
func parseClaudeRateLimitUnix(raw string) *time.Time {
	value, ok := parseClaudeRateLimitNumber(raw)
	if !ok || value <= 0 || value > claudeRateLimitMaxUnixSeconds {
		return nil
	}
	at := time.Unix(int64(math.Round(value)), 0).UTC()
	return &at
}

func parseClaudeRateLimitNumber(raw string) (float64, bool) {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	return value, true
}
