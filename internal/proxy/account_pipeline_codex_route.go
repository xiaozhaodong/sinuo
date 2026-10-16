package proxy

import (
	"context"
	"time"

	svc "sinuo/internal/service"
)

type codexAccountRoute struct {
	Result         svc.CodexModelRouteResult
	Schedule       svc.AccountScheduleResult
	Applies        bool
	FailureReason  string
	FailureMessage string
}

func (h *Handler) prepareCodexAccountRoute(ctx context.Context, requestID, path, model string) (codexAccountRoute, error) {
	var route codexAccountRoute
	if h.codexModelRouter == nil {
		return route, nil
	}
	var err error
	switch {
	case path == codexStandaloneSearchPath:
		route.Result, route.Applies, err = h.codexModelRouter.PrepareSearch(ctx, model)
	case (path == "/v1/responses" || path == "/v1/responses/compact") && h.codexModelRouter.Enabled():
		route.Applies = true
		route.Result, err = h.codexModelRouter.Prepare(ctx, model, path)
	}
	if !route.Applies {
		return route, err
	}
	route.Schedule = codexRouteSchedule(requestID, path, model, route.Result)
	if recorder, ok := h.accountPoolService.(interface {
		SaveLatestScheduleSnapshot(*svc.LatestAccountScheduleSnapshot)
	}); ok {
		recorder.SaveLatestScheduleSnapshot(route.Schedule.Snapshot)
	}
	if err == nil && route.Result.Scope.Kind == "unassigned" {
		route.FailureReason, route.FailureMessage = codexRouteFailure(route.Result.Scope)
	}
	return route, err
}

func codexRouteSchedule(requestID, path, model string, result svc.CodexModelRouteResult) svc.AccountScheduleResult {
	scope, now := result.Scope, time.Now()
	snapshot := &svc.LatestAccountScheduleSnapshot{
		RequestID: requestID, CapturedAt: now, UpdatedAt: now, RequestPath: path,
		RequestModel: model, ModelRoutingFamily: "codex", ModelRoutingVersion: scope.SnapshotVersion,
		RouteScopeKind: scope.Kind, RouteScopeGroupID: scope.GroupID, RouteScopeGroupName: scope.GroupName,
		RouteMode: scope.Mode, RouteTargetAccountID: scope.TargetAccountID, Candidates: result.Candidates,
	}
	if len(result.Accounts) > 0 {
		snapshot.SelectedAccountID = result.Accounts[0].ID
		snapshot.SelectedAccountName = accountFailoverDisplayName(result.Accounts[0])
	}
	return svc.AccountScheduleResult{Accounts: result.Accounts, Snapshot: snapshot}
}

func codexRouteFailure(scope svc.CodexModelRouteScope) (string, string) {
	switch scope.UnassignedReason {
	case svc.CodexModelRoutingMissingModel:
		return scope.UnassignedReason, "Codex 请求未携带 model"
	case svc.CodexModelRoutingLoadFailed:
		return scope.UnassignedReason, "Codex 模型路由配置加载失败"
	case "":
		return svc.CodexModelRoutingUnassigned, "Codex 模型未配置账号组"
	default:
		return scope.UnassignedReason, "Codex 模型未配置账号组"
	}
}

func codexUnavailableRoute(scope svc.CodexModelRouteScope, path string) (string, string) {
	if scope.Mode == "manual_fixed" {
		if path == codexStandaloneSearchPath {
			return svc.CodexModelRoutingFixedUnavailable, "联网搜索映射的固定账号不可用"
		}
		return svc.CodexModelRoutingFixedUnavailable, "模型路由固定账号不可用"
	}
	if path == codexStandaloneSearchPath {
		return svc.CodexModelRoutingNoAvailable, "联网搜索映射中没有可用账号"
	}
	return svc.CodexModelRoutingNoAvailable, "模型路由账号组中没有可用账号"
}
