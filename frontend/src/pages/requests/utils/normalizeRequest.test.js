// normalizeRequest 统一字段单测（方案 F1 测试计划）
import test from 'node:test';
import assert from 'node:assert/strict';
import { normalizeRequest } from './normalizeRequest.js';

test('会话字段兼容两种命名，旧数据保持空值', () => {
  assert.equal(normalizeRequest({ session_id: 'session-snake' }).sessionId, 'session-snake');
  assert.equal(normalizeRequest({ sessionId: 'session-camel' }).sessionId, 'session-camel');
  assert.equal(normalizeRequest({ session_id: 'original', sessionId: 'other' }).sessionId, 'original');
  assert.equal(normalizeRequest({}).sessionId, '');
  assert.equal(normalizeRequest({ session_id: null }).sessionId, '');
});

test('Wails 列表形状：duration_ms / status_code', () => {
  const result = normalizeRequest({
    request_id: 'req-1',
    duration_ms: 120,
    status_code: 200,
    retry_count: 2,
    failure_reason: 'boom',
    cancel_reason: '',
    first_token_ms: 30,
    completion_ms: 50,
    route_decision_at: '2026-08-13T00:00:00.000000Z'
  });
  assert.equal(result.duration, 120);
  assert.equal(result.statusCode, 200);
  assert.equal(result.httpStatusCode, 200);
  assert.equal(result.retryCount, 2);
  assert.equal(result.failureReason, 'boom');
  assert.equal(result.cancelReason, '');
  assert.equal(result.firstTokenMs, 30);
  assert.equal(result.completionMs, 50);
});

test('Go RequestRecord 原始形状：response_time / http_status，无 duration_ms/status_code', () => {
  const result = normalizeRequest({
    request_id: 'req-2',
    response_time: 321,
    http_status: 503,
    http_status_code: 503,
    retry_count: 1
  });
  assert.equal(result.duration, 321);
  assert.equal(result.statusCode, 503);
  assert.equal(result.httpStatusCode, 503);
  assert.equal(result.retryCount, 1);
});

test('camelCase 输入与合法 0 保留', () => {
  const result = normalizeRequest({
    requestId: 'req-3',
    durationMs: 0,
    statusCode: 0,
    retryCount: 0,
    failureReason: '',
    firstTokenMs: 0,
    completionMs: 0
  });
  assert.equal(result.requestId, 'req-3');
  assert.equal(result.duration, 0);
  assert.equal(result.statusCode, 0);
  assert.equal(result.retryCount, 0);
  assert.equal(result.firstTokenMs, 0);
  assert.equal(result.completionMs, 0);
});

test('HTTP 列表形状与默认值', () => {
  const result = normalizeRequest({
    id: 'req-4',
    start_time: '2026-08-13T00:00:00.000000Z',
    model_name: 'gpt-5.4'
  });
  assert.equal(result.requestId, 'req-4');
  assert.equal(result.model, 'gpt-5.4');
  assert.equal(result.retryCount, 0);
  assert.equal(result.statusCode, null);
  assert.equal(result.routeMode, 'auto');
});

test('同时存在时 start_time 保持为权威请求时间', () => {
  const result = normalizeRequest({
    timestamp: '2026-08-13T00:00:01.000000Z',
    start_time: '2026-08-13T00:00:02.000000Z'
  });
  assert.equal(result.timestamp, '2026-08-13T00:00:02.000000Z');
});

// 空上游归类必须穿过真实规范化入口才算数：normalizeRequest 会在 endpoint 位写
// pick(..., 'unknown') 兜底，只有当两条链路都把空上游落成空串（而非省略字段）时，
// 该兜底才不会抢在 failureReason 归类之前。任一侧改成省略空字段都会打断这条链路。
test('Wails 链路空上游穿过规范化入口仍归类为「无可用上游」', () => {
  // wailsApi.js getRequests 重映射：upstream_name / endpoint 均显式写 '' 而非省略
  const result = normalizeRequest({
    request_id: 'req-50067b2c',
    timestamp: '2026-08-21T09:31:35.242112Z',
    request_family: 'claude',
    upstream_type: 'endpoint',
    upstream_name: '',
    upstream_source_name: '',
    upstream_id: null,
    endpoint_name: '',
    endpoint: '',
    model: 'claude-opus-5',
    status: 'failed',
    failure_reason: 'no_endpoints_available: no routable endpoint candidates'
  });
  assert.equal(result.upstreamName, '无可用上游');
  assert.equal(result.endpoint, '', "endpoint 落 'unknown' 会抢在 failureReason 归类前");
});

test('HTTP 链路空上游穿过规范化入口仍归类为「无可用上游」', () => {
  // queries.go 结构体无 omitempty，空串一定出现在 JSON 中
  const result = normalizeRequest({
    request_id: 'req-3256d654',
    start_time: '2026-08-21T09:31:44.317779Z',
    request_family: 'claude',
    endpoint_name: '',
    upstream_type: 'endpoint',
    upstream_source_name: '',
    upstream_name: '',
    upstream_id: 0,
    route_mode: 'manual_preferred',
    requested_endpoint: 'xuanwulei',
    effective_endpoint: '',
    fallback_reason: 'no_routable_candidates',
    model_name: 'claude-opus-5',
    status: 'failed',
    failure_reason: 'no_endpoints_available: no routable endpoint candidates'
  });
  assert.equal(result.upstreamName, '无可用上游');
  assert.equal(result.routeMode, 'manual_preferred');
  assert.equal(result.requestedEndpoint, 'xuanwulei');
});

test('真实上游存在时规范化入口不做归类改写', () => {
  const result = normalizeRequest({
    request_id: 'req-8e26f8f2',
    upstream_name: 'xuanwulei',
    endpoint_name: 'xuanwulei',
    endpoint: 'xuanwulei',
    status: 'failed',
    failure_reason: 'rate_limited: endpoint=xuanwulei status=429'
  });
  assert.equal(result.upstreamName, 'xuanwulei');
});

test('模型一致性 5 字段：snake / camel 双来源，缺省空串，不影响 model', () => {
  const snake = normalizeRequest({
    request_id: 'req-mc-1',
    model_name: 'gpt-5.5',
    request_model: 'gpt-5.4',
    upstream_model: 'gpt-5.5',
    response_model: 'gpt-5.5',
    request_reasoning: 'effort:xhigh',
    response_reasoning: 'effort:medium reasoning_tokens:12'
  });
  assert.equal(snake.model, 'gpt-5.5');
  assert.equal(snake.requestModel, 'gpt-5.4');
  assert.equal(snake.upstreamModel, 'gpt-5.5');
  assert.equal(snake.responseModel, 'gpt-5.5');
  assert.equal(snake.requestReasoning, 'effort:xhigh');
  assert.equal(snake.responseReasoning, 'effort:medium reasoning_tokens:12');

  const camel = normalizeRequest({
    requestId: 'req-mc-2',
    requestModel: 'claude-test',
    upstreamModel: '',
    responseModel: 'claude-test-20250929',
    requestReasoning: 'thinking:adaptive',
    responseReasoning: 'thinking:present'
  });
  assert.equal(camel.requestModel, 'claude-test');
  assert.equal(camel.upstreamModel, '');
  assert.equal(camel.responseModel, 'claude-test-20250929');
  assert.equal(camel.responseReasoning, 'thinking:present');

  const legacy = normalizeRequest({ request_id: 'req-legacy', model_name: 'claude-test' });
  assert.equal(legacy.requestModel, '');
  assert.equal(legacy.upstreamModel, '');
  assert.equal(legacy.responseModel, '');
  assert.equal(legacy.requestReasoning, '');
  assert.equal(legacy.responseReasoning, '');
});

test('联网搜索的 0 用量按未知展示，其他请求不受影响', () => {
  const search = normalizeRequest({ path: '/v1/alpha/search', usage_known: true, cost_known: true, input_tokens: 0, total_cost_usd: 0 });
  assert.equal(search.usageKnown, false);
  assert.equal(search.costKnown, false);
  const responses = normalizeRequest({ path: '/v1/responses', input_tokens: 0 });
  assert.equal(responses.usageKnown, true);
  assert.equal(responses.costKnown, true);
});
