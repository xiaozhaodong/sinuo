import test from 'node:test';
import assert from 'node:assert/strict';

import { resolveClaudeRouteLabel } from './claudeRouteLabel.js';

test('模型路由启用时只统计模型映射，不计联网工具映射', () => {
  const bindings = [{ kind: 'model' }, { kind: 'web_tools' }, {}];
  assert.equal(resolveClaudeRouteLabel({ modelRoutingEnabled: true, bindings }), '模型路由 · 2 模型');
  assert.equal(resolveClaudeRouteLabel({ modelRoutingEnabled: true, bindings, compact: true }), '2 模型');
});

test('没有可用端点时显示明确的空状态标签', () => {
  assert.equal(resolveClaudeRouteLabel({ hasAvailableEndpoints: false }), '无可用端点');
  assert.equal(resolveClaudeRouteLabel({ hasAvailableEndpoints: false, compact: true }), '无可用端点');
});

test('全局路由：自动调度附最近命中端点，手动模式显示目标端点', () => {
  assert.equal(resolveClaudeRouteLabel({}), 'Auto');
  assert.equal(resolveClaudeRouteLabel({ routingState: { mode: 'auto', lastEffectiveEndpoint: 'vendor' } }), 'Auto · vendor');
  assert.equal(resolveClaudeRouteLabel({ routingState: { mode: 'manual_fixed', endpoint_name: 'cc主key' } }), 'cc主key');
  assert.equal(resolveClaudeRouteLabel({ routingState: { mode: 'manual_preferred' } }), '未选择');
});
