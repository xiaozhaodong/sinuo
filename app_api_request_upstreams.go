// app_api_request_upstreams.go - 请求页上游筛选候选 API (Wails Bindings)

package main

import (
	"context"
	"time"
)

// RequestUpstreamOption 请求页上游筛选候选：查询范围内实际出现过的上游
type RequestUpstreamOption struct {
	RequestFamily string `json:"request_family"`
	UpstreamType  string `json:"upstream_type"` // endpoint/account/subscription
	UpstreamName  string `json:"upstream_name"` // 账号名/端点名；订阅直通为固定标识
}

const requestUpstreamOptionsTimeout = 10 * time.Second

// GetRequestUpstreamOptions 返回请求页上游筛选候选。
// 参数与 GetRequests 相同，但只按时间范围、请求族、来源与种类收窄，忽略分页与上游 / 模型 / 状态筛选：
// 候选若跟着当前页或当前选中的上游走，选中一个上游后就切不到别的上游。
func (a *App) GetRequestUpstreamOptions(params RequestQueryParams) ([]RequestUpstreamOption, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	options := []RequestUpstreamOption{}
	if a.usageTracker == nil {
		return options, nil
	}

	policy, err := a.activeTimezonePolicy()
	if err != nil {
		return options, err
	}
	opts, err := resolveRequestQueryOptions(policy, params)
	if err != nil {
		return options, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestUpstreamOptionsTimeout)
	defer cancel()

	rows, err := a.usageTracker.QueryRequestUpstreamOptions(ctx, opts)
	if err != nil {
		return options, err
	}
	for _, row := range rows {
		options = append(options, RequestUpstreamOption(row))
	}
	return options, nil
}
