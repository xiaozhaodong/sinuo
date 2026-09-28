// app_claude_subscription_rate_limit.go - Claude Code 订阅直通额度的 Wails API
// 创建时间: 2026-09-27
//
// 额度只在内存里：响应头快照随每个订阅请求更新，主动查询在请求结束一段时间后补查或由用户手动刷新；
// 重启后为空，等下一次订阅请求。节流与凭据保留规则见 internal/proxy/claude_subscription_quota.go。
// 与入口配置分开推送：保存入口配置时持有 claudeCodeEntryMu，停用或改端口还会等在途请求结束，
// 请求路径上采集额度不能再去抢这把锁。
package main

import (
	"errors"

	"cc-forwarder/internal/proxy"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// EventClaudeSubscriptionRateLimit 订阅额度状态变化后推送给前端
const EventClaudeSubscriptionRateLimit = "claude-code-entry:rate-limit"

var errClaudeSubscriptionProxyNotReady = errors.New("代理尚未就绪")

// ClaudeSubscriptionQuotaWindowInfo 单个额度窗口；官方没带的字段为 null / 空串
type ClaudeSubscriptionQuotaWindowInfo struct {
	UsedPercent *float64 `json:"used_percent"` // 已用百分比 0–100
	ResetAt     string   `json:"reset_at"`
}

// ClaudeSubscriptionHeaderQuotaInfo 最近一次订阅响应头带回的额度
type ClaudeSubscriptionHeaderQuotaInfo struct {
	Status              string                            `json:"status"`               // allowed / allowed_warning / rejected
	RepresentativeClaim string                            `json:"representative_claim"` // 当前卡在哪个窗口
	ResetAt             string                            `json:"reset_at"`             // 整体重置时间
	OverageStatus       string                            `json:"overage_status"`       // rejected 时区分「被拒绝」与「正在消耗额外用量」
	FiveHour            ClaudeSubscriptionQuotaWindowInfo `json:"five_hour"`
	SevenDay            ClaudeSubscriptionQuotaWindowInfo `json:"seven_day"`
	CapturedAt          string                            `json:"captured_at"`
}

// ClaudeSubscriptionModelQuotaInfo 按模型的周额度
type ClaudeSubscriptionModelQuotaInfo struct {
	Label       string   `json:"label"`
	UsedPercent *float64 `json:"used_percent"`
	ResetAt     string   `json:"reset_at"`
}

// ClaudeSubscriptionExtraUsageInfo 额外用量；金额为货币最小单位（如美分）
type ClaudeSubscriptionExtraUsageInfo struct {
	Enabled      bool     `json:"enabled"`
	UsedPercent  *float64 `json:"used_percent"`
	UsedCredits  *float64 `json:"used_credits"`
	MonthlyLimit *float64 `json:"monthly_limit"`
	Currency     string   `json:"currency"`
}

// ClaudeSubscriptionUsageInfo 最近一次主动查询官方额度的结果
type ClaudeSubscriptionUsageInfo struct {
	FiveHour    ClaudeSubscriptionQuotaWindowInfo  `json:"five_hour"`
	SevenDay    ClaudeSubscriptionQuotaWindowInfo  `json:"seven_day"`
	ModelWeekly []ClaudeSubscriptionModelQuotaInfo `json:"model_weekly"`
	ExtraUsage  *ClaudeSubscriptionExtraUsageInfo  `json:"extra_usage"`
	FetchedAt   string                             `json:"fetched_at"`
}

// ClaudeSubscriptionRateLimitInfo 前端展示用的订阅额度状态；revision 越大越新
type ClaudeSubscriptionRateLimitInfo struct {
	Revision            uint64                             `json:"revision"`
	Headers             *ClaudeSubscriptionHeaderQuotaInfo `json:"headers"`
	Usage               *ClaudeSubscriptionUsageInfo       `json:"usage"`
	CredentialAvailable bool                               `json:"credential_available"` // 内存里是否还有可用于查询的登录凭据
	RefreshAvailableAt  string                             `json:"refresh_available_at"` // 手动刷新最早可用时间；空为立即可用
	LastQueriedAt       string                             `json:"last_queried_at"`
	LastError           string                             `json:"last_error"`
}

// GetClaudeSubscriptionRateLimit 返回当前订阅额度状态；代理未就绪时返回 null
func (a *App) GetClaudeSubscriptionRateLimit() *ClaudeSubscriptionRateLimitInfo {
	handler := a.currentProxyHandler()
	if handler == nil {
		return nil
	}
	info := toClaudeSubscriptionRateLimitInfo(handler.ClaudeSubscriptionQuota())
	return &info
}

// RefreshClaudeSubscriptionRateLimit 手动查询官方额度；冷却中、正在查询或没有凭据时返回错误，查询失败写在 last_error
func (a *App) RefreshClaudeSubscriptionRateLimit() (*ClaudeSubscriptionRateLimitInfo, error) {
	handler := a.currentProxyHandler()
	if handler == nil {
		return nil, errClaudeSubscriptionProxyNotReady
	}
	state, err := handler.RefreshClaudeSubscriptionQuota()
	if err != nil {
		return nil, err
	}
	info := toClaudeSubscriptionRateLimitInfo(state)
	return &info, nil
}

func (a *App) currentProxyHandler() *proxy.Handler {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.proxyHandler
}

func (a *App) emitClaudeSubscriptionRateLimit(state proxy.ClaudeSubscriptionQuotaState) {
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, EventClaudeSubscriptionRateLimit, toClaudeSubscriptionRateLimitInfo(state))
}

func toClaudeSubscriptionRateLimitInfo(state proxy.ClaudeSubscriptionQuotaState) ClaudeSubscriptionRateLimitInfo {
	info := ClaudeSubscriptionRateLimitInfo{
		Revision:            state.Revision,
		CredentialAvailable: state.CredentialAvailable,
		RefreshAvailableAt:  formatOptionalTime(state.RefreshAvailableAt),
		LastQueriedAt:       formatOptionalTime(state.LastQueriedAt),
		LastError:           state.LastError,
	}
	if state.Headers != nil {
		info.Headers = toClaudeSubscriptionHeaderQuotaInfo(*state.Headers)
	}
	if state.Usage != nil {
		info.Usage = toClaudeSubscriptionUsageInfo(*state.Usage)
	}
	return info
}

func toClaudeSubscriptionHeaderQuotaInfo(snapshot proxy.ClaudeSubscriptionRateLimit) *ClaudeSubscriptionHeaderQuotaInfo {
	return &ClaudeSubscriptionHeaderQuotaInfo{
		Status:              snapshot.Status,
		RepresentativeClaim: snapshot.RepresentativeClaim,
		ResetAt:             formatOptionalTime(snapshot.ResetAt),
		OverageStatus:       snapshot.OverageStatus,
		FiveHour:            toClaudeSubscriptionQuotaWindowInfo(snapshot.FiveHour),
		SevenDay:            toClaudeSubscriptionQuotaWindowInfo(snapshot.SevenDay),
		CapturedAt:          formatAPITime(snapshot.CapturedAt),
	}
}

func toClaudeSubscriptionUsageInfo(usage proxy.ClaudeSubscriptionUsage) *ClaudeSubscriptionUsageInfo {
	info := &ClaudeSubscriptionUsageInfo{
		FiveHour:    toClaudeSubscriptionQuotaWindowInfo(usage.FiveHour),
		SevenDay:    toClaudeSubscriptionQuotaWindowInfo(usage.SevenDay),
		ModelWeekly: make([]ClaudeSubscriptionModelQuotaInfo, 0, len(usage.ModelWeekly)),
		FetchedAt:   formatAPITime(usage.FetchedAt),
	}
	for _, quota := range usage.ModelWeekly {
		info.ModelWeekly = append(info.ModelWeekly, ClaudeSubscriptionModelQuotaInfo{
			Label:       quota.Label,
			UsedPercent: quota.UsedPercent,
			ResetAt:     formatOptionalTime(quota.ResetAt),
		})
	}
	if extra := usage.ExtraUsage; extra != nil {
		info.ExtraUsage = &ClaudeSubscriptionExtraUsageInfo{
			Enabled:      extra.Enabled,
			UsedPercent:  extra.UsedPercent,
			UsedCredits:  extra.UsedCredits,
			MonthlyLimit: extra.MonthlyLimit,
			Currency:     extra.Currency,
		}
	}
	return info
}

func toClaudeSubscriptionQuotaWindowInfo(window proxy.ClaudeSubscriptionQuotaWindow) ClaudeSubscriptionQuotaWindowInfo {
	return ClaudeSubscriptionQuotaWindowInfo{
		UsedPercent: window.UsedPercent,
		ResetAt:     formatOptionalTime(window.ResetAt),
	}
}
