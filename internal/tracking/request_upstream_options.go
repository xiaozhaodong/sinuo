package tracking

import (
	"context"
	"fmt"
)

// ============================================================
// 请求页上游筛选候选
// ============================================================

// RequestUpstreamOption 是查询范围内实际出现过的一个上游（按请求族 + 上游类型 + 上游名去重）。
type RequestUpstreamOption struct {
	RequestFamily string `json:"request_family"`
	UpstreamType  string `json:"upstream_type"`
	UpstreamName  string `json:"upstream_name"`
}

// 空上游名（无端点可路由等）筛选不出东西，不进候选；NULL <> '' 也为假，一并排除。
const requestUpstreamOptionsSelect = `SELECT DISTINCT
	COALESCE(request_family, 'other'),
	COALESCE(upstream_type, 'endpoint'),
	upstream_name
	FROM request_logs WHERE upstream_name <> ''`

// QueryRequestUpstreamOptions 返回请求页上游筛选候选。
// 与请求列表共用 QueryOptions，但只取时间范围、请求族、来源与 kind：候选不能被当前选中的
// 上游（以及模型 / 状态）收窄，否则选中一个上游后就切不到别的上游。
// 不合并热池：在途请求都排在列表第一页，前端会把当前页出现的上游并入候选。
func (ut *UsageTracker) QueryRequestUpstreamOptions(ctx context.Context, opts *QueryOptions) ([]RequestUpstreamOption, error) {
	if ut.readDB == nil {
		return nil, fmt.Errorf("read database not initialized")
	}
	scope := requestUpstreamOptionScope(opts)
	if err := validateQueryRequestKinds(scope); err != nil {
		return nil, err
	}

	query, args := appendRequestQueryFilters(requestUpstreamOptionsSelect, make([]interface{}, 0, 8), scope, resolveRequestQuerySource(scope.UpstreamType))
	rows, err := ut.readDB.QueryContext(ctx, query+" ORDER BY upstream_name", args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query request upstream options: %w", err)
	}
	defer rows.Close()

	options := make([]RequestUpstreamOption, 0)
	for rows.Next() {
		var option RequestUpstreamOption
		if err := rows.Scan(&option.RequestFamily, &option.UpstreamType, &option.UpstreamName); err != nil {
			return nil, fmt.Errorf("failed to scan request upstream option: %w", err)
		}
		options = append(options, option)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating request upstream options: %w", err)
	}
	return options, nil
}

func requestUpstreamOptionScope(opts *QueryOptions) *QueryOptions {
	if opts == nil {
		return &QueryOptions{}
	}
	return &QueryOptions{
		StartDate:     opts.StartDate,
		EndDate:       opts.EndDate,
		RequestFamily: opts.RequestFamily,
		UpstreamType:  opts.UpstreamType,
		RequestKinds:  opts.RequestKinds,
	}
}
