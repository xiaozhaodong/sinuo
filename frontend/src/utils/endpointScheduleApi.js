import { isWailsEnvironment } from './wailsApi.js';

const normalizeDecision = (decision = {}) => ({
  name: decision.name || '',
  decision: decision.decision || '',
  reason: decision.reason || '',
  availableAt: decision.availableAt || decision.available_at || '',
  runtimeOutcome: decision.runtimeOutcome || decision.runtime_outcome || '',
  runtimeError: decision.runtimeError || decision.runtime_error || ''
});

const normalizeSnapshot = (snapshot = {}) => ({
  hasSnapshot: snapshot.hasSnapshot === true || snapshot.has_snapshot === true,
  requestId: snapshot.requestId || snapshot.request_id || '',
  capturedAt: snapshot.capturedAt || snapshot.captured_at || '',
  updatedAt: snapshot.updatedAt || snapshot.updated_at || '',
  requestPath: snapshot.requestPath || snapshot.request_path || '',
  selectedEndpoint: snapshot.selectedEndpoint || snapshot.selected_endpoint || '',
  routeMode: snapshot.routeMode || snapshot.route_mode || 'auto',
  routeEndpointName: snapshot.routeEndpointName || snapshot.route_endpoint_name || '',
  routeFallbackEnabled: snapshot.routeFallbackEnabled ?? snapshot.route_fallback_enabled ?? true,
  routeScopeKind: snapshot.routeScopeKind || snapshot.route_scope_kind || snapshot.routeScope || snapshot.route_scope || 'global',
  routeScope: snapshot.routeScope || snapshot.route_scope || snapshot.routeScopeKind || snapshot.route_scope_kind || 'global',
  routeScopeGroupId: snapshot.routeScopeGroupID ?? snapshot.route_scope_group_id ?? 0,
  routeScopeGroupName: snapshot.routeScopeGroupName || snapshot.route_scope_group_name || '',
  upstreamGroup: snapshot.upstreamGroup || snapshot.upstream_group || null,
  requestModel: snapshot.requestModel || snapshot.request_model || '',
  modelRoutingVersion: snapshot.modelRoutingVersion ?? snapshot.model_routing_version ?? 0,
  failoverEnabled: snapshot.failoverEnabled ?? snapshot.failover_enabled ?? false,
  finalOutcome: snapshot.finalOutcome || snapshot.final_outcome || '',
  finalError: snapshot.finalError || snapshot.final_error || '',
  summary: snapshot.summary || '',
  decisions: Array.isArray(snapshot.decisions) ? snapshot.decisions.map(normalizeDecision) : []
});

export const fetchLatestEndpointScheduleSnapshot = async () => {
  if (!isWailsEnvironment()) {
    return {
      ...normalizeSnapshot(),
      unsupported: true,
      message: '最近一次端点调度仅在桌面应用中可用。'
    };
  }

  const method = window?.go?.main?.App?.GetLatestEndpointScheduleSnapshot;
  if (typeof method !== 'function') {
    return {
      ...normalizeSnapshot(),
      unsupported: true,
      message: '当前运行版本尚未提供端点调度快照，请重新构建并重启应用。'
    };
  }

  return normalizeSnapshot(await method());
};
