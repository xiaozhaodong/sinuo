// ============================================
// normalizeRequest - 请求记录唯一规范化入口
// 2026-08-13（生命周期面板方案 F1）
// 同时接受：
//   - Wails 列表形状（duration_ms / status_code，经 wailsApi 重映射）
//   - HTTP 列表形状
//   - Go RequestRecord 原始形状（response_time / http_status，B5.2 详情接口直出）
// 数值字段使用 ?? 保留合法 0；不再新增第二个 requestRecordToShape 事实来源。
// ============================================

import { normalizeRequestSource } from './requestSource.js';

const pick = (request, keys, fallback) => {
  for (const key of keys) {
    if (request[key] !== undefined && request[key] !== null) {
      return request[key];
    }
  }
  return fallback;
};

export const normalizeRequest = (request = {}) => normalizeRequestSource({
  ...request,
  requestFamily: pick(request, ['request_family', 'requestFamily'], 'other'),
  upstreamType: pick(request, ['upstream_type', 'upstreamType'], 'endpoint'),
  upstreamName: pick(request, ['upstream_name', 'upstreamName'], ''),
  upstreamSourceName: pick(request, ['upstream_source_name', 'upstreamSourceName'], ''),
  upstreamId: pick(request, ['upstream_id', 'upstreamId'], null),
  requestId: pick(request, ['request_id', 'requestId', 'id'], ''),
  id: pick(request, ['request_id', 'requestId', 'id'], ''),
  timestamp: pick(request, ['start_time', 'timestamp'], ''),
  model: pick(request, ['model_name', 'model'], 'unknown'),
  endpoint: pick(request, ['upstream_name', 'upstreamName', 'endpoint_name', 'endpoint'], 'unknown'),
  duration: pick(request, ['duration_ms', 'duration', 'response_time'], 0),
  statusCode: pick(request, ['status_code', 'statusCode', 'http_status'], null),
  retryCount: pick(request, ['retry_count', 'retryCount'], 0),
  failureReason: pick(request, ['failure_reason', 'failureReason'], ''),
  cancelReason: pick(request, ['cancel_reason', 'cancelReason'], ''),
  path: pick(request, ['path'], ''),
  method: pick(request, ['method'], ''),
  requestSource: pick(request, ['request_source', 'requestSource'], ''),
  requestKind: pick(request, ['request_kind', 'requestKind'], 'legacy'),
  // 缺省 true 兼容旧来源：没有该标记时 0 视为真实零。
  usageKnown: pick(request, ['usage_known', 'usageKnown'], true),
  costKnown: pick(request, ['cost_known', 'costKnown'], true),
  httpStatusCode: pick(
    request,
    ['http_status_code', 'httpStatusCode', 'status_code', 'statusCode', 'http_status'],
    null
  ),
  firstTokenMs: pick(request, ['first_token_ms', 'firstTokenMs'], null),
  completionMs: pick(request, ['completion_ms', 'completionMs'], null),
  inputTokens: pick(request, ['input_tokens', 'inputTokens'], 0),
  outputTokens: pick(request, ['output_tokens', 'outputTokens'], 0),
  cacheCreationTokens: pick(request, ['cache_creation_tokens', 'cacheCreationTokens'], 0),
  cacheCreation5mTokens: pick(request, ['cache_creation_5m_tokens', 'cacheCreation5mTokens'], 0),
  cacheCreation1hTokens: pick(request, ['cache_creation_1h_tokens', 'cacheCreation1hTokens'], 0),
  cacheReadTokens: pick(request, ['cache_read_tokens', 'cacheReadTokens'], 0),
  cost: pick(request, ['total_cost_usd', 'cost'], 0),
  isStreaming: pick(request, ['is_streaming', 'isStreaming'], false),
  routeMode: pick(request, ['route_mode', 'routeMode'], 'auto'),
  requestedEndpoint: pick(request, ['requested_endpoint', 'requestedEndpoint'], ''),
  effectiveEndpoint: pick(request, ['effective_endpoint', 'effectiveEndpoint'], ''),
  fallbackReason: pick(request, ['fallback_reason', 'fallbackReason'], ''),
  routeDecisionAt: pick(request, ['route_decision_at', 'routeDecisionAt'], '')
});

export default normalizeRequest;
