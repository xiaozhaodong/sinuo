import test from 'node:test';
import assert from 'node:assert/strict';
import { codexBindingPayload, codexMemberState, codexOutcomeLabel } from './codexRoutingView.js';
import { filterRoutingBindings, routingCounts } from '../../../components/model-routing/routingView.js';

const binding = {
  model: 'GPT-6', account_ids: [3, 1], member_enabled: { 3: false, 1: true },
  route_mode: 'manual_fixed', route_target_account_id: 3, enabled: true, note: '高配'
};

test('Codex 行内修改保留账号顺序、暂停成员、目标与备注', () => {
  assert.deepEqual(codexBindingPayload(binding, { enabled: false }), { ...binding, enabled: false });
  assert.deepEqual(codexBindingPayload(binding, { route_mode: 'auto' }), { ...binding, route_mode: 'auto', route_target_account_id: 0 });
  assert.equal(codexBindingPayload({ ...binding, account_ids: [1], route_target_account_id: 0 }, { route_mode: 'manual_preferred' }).route_target_account_id, 1);
  assert.equal(codexBindingPayload(binding, { route_target_account_id: '1' }).route_target_account_id, 1);
});

test('Codex 继续复用模型筛选和全量摘要', () => {
  assert.deepEqual(filterRoutingBindings([binding], { query: ' gpt-6 ', filter: 'enabled' }), [binding]);
  assert.deepEqual(filterRoutingBindings([binding], { query: '高配', filter: 'disabled' }), []);
  assert.deepEqual(routingCounts([binding]), { total: 1, enabled: 1, disabled: 0 });
});

test('暂停成员展示不参与自动调度，但不将手动固定标成不可用', () => {
  const state = codexMemberState(binding, 3, { enabled: true, state: 'active', has_credential: true });
  assert.equal(state.label, '未参与自动调度');
  assert.match(state.detail, /仍可被手动固定/);
});

test('缺失、鉴权失效、无凭据、冷却和耗尽额度可见', () => {
  assert.equal(codexMemberState(binding, 3, null).label, '账号不存在');
  assert.equal(codexMemberState(binding, 3, { state: 'disabled_auth' }).label, '鉴权失效');
  assert.equal(codexMemberState(binding, 1, {}).label, '无凭据');
  assert.equal(codexMemberState(binding, 1, { has_credential: true, state: 'cooldown', cooldown_until: '2999-01-01' }).label, '冷却中');
  assert.equal(codexMemberState(binding, 1, { has_credential: true, quota_status: 'exhausted', quota_weekly_used_percent: 100, quota_weekly_reset_at: '2999-01-01' }).label, '额度已耗尽');
  assert.equal(codexMemberState(binding, 1, { has_credential: true }).label, '可参与');
});

test('Codex 最近结果沿用账号池文案，未知结果原样显示', () => {
  assert.equal(codexOutcomeLabel('auth_failed'), '鉴权失败');
  assert.equal(codexOutcomeLabel('future_outcome'), 'future_outcome');
});
