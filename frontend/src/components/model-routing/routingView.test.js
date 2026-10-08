import test from 'node:test';
import assert from 'node:assert/strict';
import { filterRoutingBindings, routingCounts, routingOutcomeLabel } from './routingView.js';

const bindings = [
  { assignment_id: 1, model: 'Claude-Opus', note: '主力', enabled: true },
  { assignment_id: 2, model: 'GPT-6', note: '备用', enabled: false }
];

test('路由筛选匹配模型、备注，并叠加启用状态', () => {
  assert.deepEqual(filterRoutingBindings(bindings, { query: ' OPUS ' }), [bindings[0]]);
  assert.deepEqual(filterRoutingBindings(bindings, { query: '主力', filter: 'enabled' }), [bindings[0]]);
  assert.deepEqual(filterRoutingBindings(bindings, { query: 'gpt-6', filter: 'disabled' }), [bindings[1]]);
  assert.deepEqual(filterRoutingBindings(bindings, { query: '备用', filter: 'enabled' }), []);
  assert.deepEqual(filterRoutingBindings(bindings), bindings);
});

test('状态栏计数使用全量映射，不受筛选影响', () => {
  assert.deepEqual(routingCounts(bindings), { total: 2, enabled: 1, disabled: 1 });
  assert.deepEqual(routingCounts([]), { total: 0, enabled: 0, disabled: 0 });
});

test('最近命中保留所有旧结果文案，未知结果不被掩盖', () => {
  assert.equal(routingOutcomeLabel('quality_incomplete'), '响应不完整');
  assert.equal(routingOutcomeLabel('manual_fixed_blocked'), '固定目标不可用');
  assert.equal(routingOutcomeLabel('future_outcome'), 'future_outcome');
  assert.equal(routingOutcomeLabel(''), '—');
});
