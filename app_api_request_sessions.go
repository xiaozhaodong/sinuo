package main

import (
	"context"
	"fmt"
	"time"
)

// RequestSessionQueryParams 复用正式请求页的全部筛选；会话分页独立保存。
type RequestSessionQueryParams struct {
	RequestQueryParams
	SortBy    string `json:"sort_by"`
	SortOrder string `json:"sort_order"`
}

// 以下 DTO 隔离桌面绑定与存储层，不把内部 tracking 类型暴露到前端。
type RequestSessionRow struct {
	RequestFamily     string   `json:"request_family"`
	SessionID         string   `json:"session_id"`
	RequestCount      int64    `json:"request_count"`
	TotalCostUSD      *float64 `json:"total_cost_usd"`
	MissingCosts      int64    `json:"missing_costs"`
	CacheRatio        *float64 `json:"cache_ratio"`
	MissingCacheUsage int64    `json:"missing_cache_usage"`
	LastActivity      string   `json:"last_activity"`
}

type RequestSessionResult struct {
	Rows          []RequestSessionRow `json:"rows"`
	Total         int                 `json:"total"`
	TotalRequests int64               `json:"total_requests"`
	Ungrouped     int64               `json:"ungrouped"`
	KnownCostUSD  float64             `json:"known_cost_usd"`
	MissingCosts  int64               `json:"missing_costs"`
	Page          int                 `json:"page"`
	PageSize      int                 `json:"page_size"`
}

type RequestSessionPoint struct {
	RequestID string   `json:"request_id"`
	StartTime string   `json:"start_time"`
	FullInput *int64   `json:"full_input"`
	CacheRead *int64   `json:"cache_read"`
	Cost      *float64 `json:"cost"`
	Previous  *int64   `json:"previous"`
	Ordinal   int      `json:"ordinal"`
}

type RequestSessionTrend struct {
	Points          []RequestSessionPoint `json:"points"`
	Total           int                   `json:"total"`
	Page            int                   `json:"page"`
	PageSize        int                   `json:"page_size"`
	SelectedID      string                `json:"selected_id"`
	SelectedMissing bool                  `json:"selected_missing"`
}

// GetRequestSessions 获取完整筛选集的会话排行（仅桌面端）。
func (a *App) GetRequestSessions(params RequestSessionQueryParams) (RequestSessionResult, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.usageTracker == nil {
		return RequestSessionResult{}, fmt.Errorf("请求追踪尚未就绪")
	}
	policy, err := a.activeTimezonePolicy()
	if err != nil {
		return RequestSessionResult{}, err
	}
	opts, err := resolveRequestQueryOptions(policy, params.RequestQueryParams)
	if err != nil {
		return RequestSessionResult{}, err
	}
	if params.Page == 0 {
		params.Page = 1
	}
	if params.PageSize == 0 {
		params.PageSize = 30
	}
	if params.SortBy == "" {
		params.SortBy = "recent"
	}
	if params.SortOrder == "" {
		params.SortOrder = "desc"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r, err := a.usageTracker.QueryRequestSessions(ctx, opts, params.Page, params.PageSize, params.SortBy, params.SortOrder)
	if err != nil {
		return RequestSessionResult{}, err
	}
	result := RequestSessionResult{Rows: []RequestSessionRow{}, Total: r.Total, TotalRequests: r.TotalRequests,
		Ungrouped: r.Ungrouped, KnownCostUSD: r.KnownCostUSD, MissingCosts: r.MissingCosts, Page: r.Page, PageSize: r.PageSize}
	for _, row := range r.Rows {
		result.Rows = append(result.Rows, RequestSessionRow(row))
	}
	return result, nil
}

// GetRequestSessionTrend 获取有界走势，selectedID 为空时默认选中全范围最大输入突增。
func (a *App) GetRequestSessionTrend(params RequestQueryParams, selectedID string, page int) (RequestSessionTrend, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.usageTracker == nil {
		return RequestSessionTrend{}, fmt.Errorf("请求追踪尚未就绪")
	}
	policy, err := a.activeTimezonePolicy()
	if err != nil {
		return RequestSessionTrend{}, err
	}
	opts, err := resolveRequestQueryOptions(policy, params)
	if err != nil {
		return RequestSessionTrend{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r, err := a.usageTracker.QueryRequestSessionTrend(ctx, opts, selectedID, page)
	if err != nil {
		return RequestSessionTrend{}, err
	}
	result := RequestSessionTrend{Points: []RequestSessionPoint{}, Total: r.Total, Page: r.Page,
		PageSize: r.PageSize, SelectedID: r.SelectedID, SelectedMissing: r.SelectedMissing}
	for _, row := range r.Points {
		result.Points = append(result.Points, RequestSessionPoint(row))
	}
	return result, nil
}
