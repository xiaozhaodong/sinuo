import test from 'node:test';
import assert from 'node:assert/strict';
import {
  filterRoutingBindings,
  routingCounts,
  routingOutcomeLabel,
  routingOutcomeTone,
  splitRoutingMembers
} from './routingView.js';

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

test('路由成员按上限拆分，溢出色调按 rose、amber、slate 降级', () => {
  const members = [
    { key: 'a', tone: 'tone-emerald' },
    { key: 'b', tone: 'tone-amber' },
    { key: 'c', tone: 'tone-rose' },
    { key: 'd', tone: 'tone-emerald' }
  ];
  assert.deepEqual(splitRoutingMembers(members, 2), { visible: members.slice(0, 2), hidden: members.slice(2), overflowTone: 'tone-rose' });
  assert.deepEqual(splitRoutingMembers(members.slice(0, 2), 2), { visible: members.slice(0, 2), hidden: [], overflowTone: '' });
  assert.deepEqual(splitRoutingMembers(members.slice(0, 1), 2), { visible: members.slice(0, 1), hidden: [], overflowTone: '' });
  assert.equal(splitRoutingMembers([
    { key: 'a', tone: 'tone-emerald' },
    { key: 'b', tone: 'tone-amber' },
    { key: 'c', tone: 'tone-emerald' }
  ], 1).overflowTone, 'tone-amber');
  assert.equal(splitRoutingMembers([
    { key: 'a', tone: 'tone-emerald' },
    { key: 'b', tone: 'tone-emerald' },
    { key: 'c', tone: 'tone-slate' }
  ], 1).overflowTone, 'tone-slate');
});

test('路由结果色调覆盖成功、进行中、取消、失败和空值', () => {
  assert.equal(routingOutcomeTone('success'), 'success');
  assert.equal(routingOutcomeTone('pending'), 'neutral');
  assert.equal(routingOutcomeTone('cancelled'), 'neutral');
  assert.equal(routingOutcomeTone('quality_incomplete'), 'danger');
  assert.equal(routingOutcomeTone('future_outcome'), 'danger');
  assert.equal(routingOutcomeTone(''), '');
  assert.equal(routingOutcomeTone(null), '');
});
