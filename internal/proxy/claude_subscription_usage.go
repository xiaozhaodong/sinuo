// Package proxy - Claude Code 订阅额度主动查询
// 创建时间: 2026-09-28
//
// 响应头只带 5h / 7d 两个总窗口，而且只在请求经过入口时更新；claude.ai 网页等其他客户端的用量看不到。
// 这里用订阅直通请求留下的 OAuth 凭据查询官方 /api/oauth/usage（与 Claude Code /usage、claude.ai 用量页同源），
// 补齐按模型的周额度与额外用量。什么时候查、凭据留多久由 claude_subscription_quota.go 决定。
package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// claudeUsageMaxBodyBytes 额度响应体读取上限，正常只有几百字节
const claudeUsageMaxBodyBytes = 1 << 20

// claudeUsageWeeklyScopedKind limits[] 里按模型划分的周额度
const claudeUsageWeeklyScopedKind = "weekly_scoped"

// errClaudeUsageEmpty 响应里没有任何额度窗口（例如凭据不是订阅账号）
var errClaudeUsageEmpty = errors.New("官方未返回订阅额度")

// ClaudeSubscriptionModelQuota 按模型的周额度，Label 为官方给的模型名（Sonnet / Opus 等）
type ClaudeSubscriptionModelQuota struct {
	Label string
	ClaudeSubscriptionQuotaWindow
}

// ClaudeSubscriptionExtraUsage 额外用量（订阅额度用尽后按量计费）；金额为货币最小单位，如美分
type ClaudeSubscriptionExtraUsage struct {
	Enabled      bool
	UsedPercent  *float64
	UsedCredits  *float64
	MonthlyLimit *float64
	Currency     string
}

// ClaudeSubscriptionUsage 最近一次主动查询官方额度的结果
type ClaudeSubscriptionUsage struct {
	FiveHour    ClaudeSubscriptionQuotaWindow
	SevenDay    ClaudeSubscriptionQuotaWindow
	ModelWeekly []ClaudeSubscriptionModelQuota
	ExtraUsage  *ClaudeSubscriptionExtraUsage
	FetchedAt   time.Time
}

// claudeUsageStatusError 官方返回非 200；401/403 表示凭据失效，429 带退避时长
type claudeUsageStatusError struct {
	status     int
	retryAfter time.Duration
}

func (e *claudeUsageStatusError) Error() string {
	return fmt.Sprintf("官方返回 HTTP %d", e.status)
}

type claudeUsageWindowJSON struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    *string  `json:"resets_at"`
}

type claudeUsageLimitJSON struct {
	Kind     string   `json:"kind"`
	Percent  *float64 `json:"percent"`
	ResetsAt *string  `json:"resets_at"`
	Scope    struct {
		Model *struct {
			DisplayName string `json:"display_name"`
		} `json:"model"`
	} `json:"scope"`
}

type claudeExtraUsageJSON struct {
	IsEnabled    bool     `json:"is_enabled"`
	MonthlyLimit *float64 `json:"monthly_limit"`
	UsedCredits  *float64 `json:"used_credits"`
	Utilization  *float64 `json:"utilization"`
	Currency     *string  `json:"currency"`
}

type claudeUsageResponseJSON struct {
	FiveHour       *claudeUsageWindowJSON `json:"five_hour"`
	SevenDay       *claudeUsageWindowJSON `json:"seven_day"`
	SevenDaySonnet *claudeUsageWindowJSON `json:"seven_day_sonnet"`
	SevenDayOpus   *claudeUsageWindowJSON `json:"seven_day_opus"`
	Limits         []claudeUsageLimitJSON `json:"limits"`
	ExtraUsage     *claudeExtraUsageJSON  `json:"extra_usage"`
}

// fetchClaudeSubscriptionUsage 发起一次额度查询；非 200 返回 *claudeUsageStatusError
func (h *Handler) fetchClaudeSubscriptionUsage(ctx context.Context, credential claudeUsageCredential) (ClaudeSubscriptionUsage, error) {
	resp, err := h.forwarder.FetchClaudeSubscriptionUsage(ctx, h.claudeSubscriptionBaseURL, credential.authorization, credential.userAgent)
	if err != nil {
		return ClaudeSubscriptionUsage{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ClaudeSubscriptionUsage{}, &claudeUsageStatusError{status: resp.StatusCode, retryAfter: parseAccountRetryAfter(resp)}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, claudeUsageMaxBodyBytes))
	if err != nil {
		return ClaudeSubscriptionUsage{}, err
	}
	return parseClaudeSubscriptionUsage(body, time.Now())
}

// parseClaudeSubscriptionUsage 解析 /api/oauth/usage；utilization 已是 0–100，官方没带的字段保持 nil
func parseClaudeSubscriptionUsage(body []byte, now time.Time) (ClaudeSubscriptionUsage, error) {
	var payload claudeUsageResponseJSON
	if err := json.Unmarshal(body, &payload); err != nil {
		return ClaudeSubscriptionUsage{}, fmt.Errorf("额度响应解析失败: %w", err)
	}
	if payload.FiveHour == nil && payload.SevenDay == nil {
		return ClaudeSubscriptionUsage{}, errClaudeUsageEmpty
	}
	return ClaudeSubscriptionUsage{
		FiveHour:    toClaudeUsageWindow(payload.FiveHour),
		SevenDay:    toClaudeUsageWindow(payload.SevenDay),
		ModelWeekly: collectClaudeModelWeekly(payload),
		ExtraUsage:  toClaudeExtraUsage(payload.ExtraUsage),
		FetchedAt:   now.UTC(),
	}, nil
}

// collectClaudeModelWeekly 合并旧字段 seven_day_sonnet / seven_day_opus 与 limits[] 里的按模型周额度，同名只留第一条
func collectClaudeModelWeekly(payload claudeUsageResponseJSON) []ClaudeSubscriptionModelQuota {
	var quotas []ClaudeSubscriptionModelQuota
	seen := map[string]bool{}
	add := func(label string, window ClaudeSubscriptionQuotaWindow) {
		key := strings.ToLower(strings.TrimSpace(label))
		if key == "" || seen[key] || window.UsedPercent == nil {
			return
		}
		seen[key] = true
		quotas = append(quotas, ClaudeSubscriptionModelQuota{Label: strings.TrimSpace(label), ClaudeSubscriptionQuotaWindow: window})
	}
	add("Sonnet", toClaudeUsageWindow(payload.SevenDaySonnet))
	add("Opus", toClaudeUsageWindow(payload.SevenDayOpus))
	for _, limit := range payload.Limits {
		if limit.Kind != claudeUsageWeeklyScopedKind || limit.Scope.Model == nil {
			continue
		}
		add(limit.Scope.Model.DisplayName, toClaudeUsageWindow(&claudeUsageWindowJSON{Utilization: limit.Percent, ResetsAt: limit.ResetsAt}))
	}
	return quotas
}

func toClaudeUsageWindow(window *claudeUsageWindowJSON) ClaudeSubscriptionQuotaWindow {
	if window == nil {
		return ClaudeSubscriptionQuotaWindow{}
	}
	return ClaudeSubscriptionQuotaWindow{
		UsedPercent: toClaudeUsagePercent(window.Utilization),
		ResetAt:     parseClaudeUsageTime(window.ResetsAt),
	}
}

func toClaudeExtraUsage(extra *claudeExtraUsageJSON) *ClaudeSubscriptionExtraUsage {
	if extra == nil {
		return nil
	}
	currency := ""
	if extra.Currency != nil {
		currency = strings.ToUpper(strings.TrimSpace(*extra.Currency))
	}
	return &ClaudeSubscriptionExtraUsage{
		Enabled:      extra.IsEnabled,
		UsedPercent:  toClaudeUsagePercent(extra.Utilization),
		UsedCredits:  toClaudeUsageAmount(extra.UsedCredits),
		MonthlyLimit: toClaudeUsageAmount(extra.MonthlyLimit),
		Currency:     currency,
	}
}

// toClaudeUsagePercent 保留两位小数；负数与非法值按缺失处理，超额不截断
func toClaudeUsagePercent(value *float64) *float64 {
	if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 {
		return nil
	}
	percent := math.Round(*value*100) / 100
	return &percent
}

func toClaudeUsageAmount(value *float64) *float64 {
	if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 {
		return nil
	}
	amount := *value
	return &amount
}

func parseClaudeUsageTime(raw *string) *time.Time {
	if raw == nil {
		return nil
	}
	at, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(*raw))
	if err != nil {
		return nil
	}
	at = at.UTC()
	return &at
}
