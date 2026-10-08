import test from 'node:test';
import assert from 'node:assert/strict';
import { formatRouteMode, getRequestPresentation } from './requestPresentation.js';
import { normalizeRequest } from './normalizeRequest.js';
import { buildLifecycleSegments } from './lifecycle.js';
import { STATUS_SELECT_OPTIONS } from './constants.js';
import { getStatusConfig } from '../../../utils/constants.js';

const clientClosed = {
  status: 'cancelled',
  request_source: 'codex_subscription_proxy',
  failure_reason: 'subscription_client_disconnected'
};

test('数据库和规范化行使用同一展示分类，原始结算与未知用量不变', () => {
  const raw = Object.freeze({
    ...clientClosed, request_id: 'synthetic-client-close', duration_ms: 1200,
    first_token_ms: 100, usage_known: false, cost_known: false
  });
  const normalized = normalizeRequest(raw);
  for (const row of [raw, normalized]) {
    const result = getRequestPresentation(row);
    assert.equal(result.status, 'interrupted');
    assert.equal(result.isNeutral, true);
    assert.equal(result.heading, '结束原因');
    assert.match(result.description, /具体触发原因未确认/);
    assert.doesNotMatch(result.description, /已续接|子代理正常|用户主动取消/);
    assert.equal(getStatusConfig(result.status).label, '已中断');
    assert.equal(getStatusConfig(result.status).color, 'tone-slate');
    assert.equal(row.status, 'cancelled');
  }
  assert.equal(normalized.usageKnown, false);
  assert.equal(normalized.costKnown, false);
  const segments = buildLifecycleSegments(normalized);
  assert.equal(segments.at(-1).key, 'cancelled');
  assert.equal(segments.at(-1).label, '已中断');
  assert.equal(segments.some(segment => segment.key === 'failure'), false);
});

test('模糊历史中断、上游错误和其他来源不被改成已中断', () => {
  for (const row of [
    { ...clientClosed, status: 'failed', failure_reason: 'subscription_interrupted' },
    { ...clientClosed, status: 'failed', failure_reason: 'subscription_response_failed' },
    { ...clientClosed, status: 'failed' },
    { ...clientClosed, request_source: 'legacy_proxy' },
    { ...clientClosed, request_source: undefined },
    { ...clientClosed, failure_reason: 'subscription_client_disconnected_extra' },
    { ...clientClosed, failure_reason: 'subscription_client_disconnected: unverified text' },
    { ...clientClosed, status: 'completed' }
  ]) {
    const result = getRequestPresentation(normalizeRequest(row));
    assert.equal(result.status, row.status);
    assert.equal(result.isInterrupted, false);
    if (row.status === 'failed') {
      assert.equal(result.isNeutral, false);
      assert.equal(result.heading, '错误信息');
      assert.equal(getStatusConfig(result.status).color, 'tone-rose');
    }
  }
});

test('明确取消仍显示已取消，且筛选中与已中断分开', () => {
  const result = getRequestPresentation({ ...clientClosed, failure_reason: 'subscription_cancelled' });
  assert.equal(result.status, 'cancelled');
  assert.equal(result.isInterrupted, false);
  assert.equal(result.isNeutral, true);
  assert.equal(result.heading, '结束原因');
  assert.equal(getStatusConfig(result.status).label, '已取消');
  const choices = new Map(STATUS_SELECT_OPTIONS.map(option => [option.value, option.label]));
  assert.equal(choices.get('cancelled'), '已取消');
  assert.equal(choices.get('interrupted'), '已中断');
});

test('详情优先使用规范化字段，空值不被旧别名覆盖', () => {
  assert.equal(getRequestPresentation({
    ...clientClosed, failureReason: '', requestSource: 'codex_subscription_proxy'
  }).isInterrupted, false);
  assert.equal(getRequestPresentation({
    status: 'cancelled', requestSource: 'codex_subscription_proxy',
    failureReason: 'subscription_client_disconnected'
  }).isInterrupted, true);
  assert.equal(getRequestPresentation().isInterrupted, false);
});

test('formatRouteMode 只认两种手动模式，其余按自动', () => {
  assert.equal(formatRouteMode('manual_fixed'), '严格固定');
  assert.equal(formatRouteMode('manual_preferred'), '手动优选');
  assert.equal(formatRouteMode('auto'), '自动');
  assert.equal(formatRouteMode(undefined), '自动');
});
