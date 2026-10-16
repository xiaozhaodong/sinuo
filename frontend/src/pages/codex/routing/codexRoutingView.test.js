import test from 'node:test';
import assert from 'node:assert/strict';
import { CODEX_SEARCH_KIND, CODEX_SEARCH_LABEL, codexBindingDisplayName, codexBindingPayload, codexMemberState, codexModelBindings, codexOutcomeLabel, isCodexOAuthAccount, isCodexSearchBinding, sortCodexBindings } from './codexRoutingView.js';
import { filterRoutingBindings, routingCounts } from '../../../components/model-routing/routingView.js';

const binding = {
  kind: 'model',
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

test('Codex 搜索映射显示业务名称，旧映射仍按模型展示', () => {
  assert.equal(CODEX_SEARCH_KIND, 'search');
  assert.equal(CODEX_SEARCH_LABEL, '联网搜索');
  assert.equal(isCodexSearchBinding({ kind: 'search' }), true);
  assert.equal(isCodexSearchBinding({ model: 'legacy' }), false);
  assert.equal(codexBindingDisplayName({ kind: 'search', model: 'server_tool:search' }), '联网搜索');
  assert.equal(codexBindingDisplayName(binding), 'GPT-6');
});

test('Codex 搜索可以按中文筛选，但不计入模型数量与启用门禁', () => {
  const search = { kind: 'search', model: 'server_tool:search', enabled: true };
  const legacy = { model: 'legacy', enabled: false };
  const rows = [search, binding, legacy];
  assert.deepEqual(codexModelBindings(rows), [binding, legacy]);
  assert.deepEqual(routingCounts(codexModelBindings([search])), { total: 0, enabled: 0, disabled: 0 });
  assert.deepEqual(filterRoutingBindings(rows, { query: '联网', displayName: codexBindingDisplayName }), [search]);
});

test('Codex 搜索排序置顶且不改变普通映射相对顺序或输入', () => {
  const search = { kind: 'search', enabled: false };
  const another = { kind: 'model', model: 'a-model' };
  const rows = [binding, another, search];
  assert.deepEqual(sortCodexBindings(rows), [search, binding, another]);
  assert.deepEqual(rows, [binding, another, search]);
  assert.deepEqual(sortCodexBindings([]), []);
});

test('Codex 搜索账号识别复用 OAuth 别名并兼容字段命名', () => {
  for (const provider of ['chatgpt_refresh_token', 'chatgpt_rt', 'refresh_token', 'rt', 'oauth', ' OPENAI_OAUTH ']) {
    assert.equal(isCodexOAuthAccount({ provider_type: provider }), true);
    assert.equal(isCodexOAuthAccount({ providerType: provider }), true);
  }
  for (const provider of ['api_key', 'session_cookie', 'unknown', '']) {
    assert.equal(isCodexOAuthAccount({ provider_type: provider }), false);
  }
  assert.equal(isCodexOAuthAccount({}), false);
});

test('Codex 行内 payload 保留搜索类型，旧模型调用默认带 model 类型', () => {
  const search = { ...binding, kind: 'search', model: 'server_tool:search' };
  assert.deepEqual(codexBindingPayload(search, { enabled: false }), { ...search, enabled: false });
  assert.equal(codexBindingPayload({ ...binding, kind: undefined }, {}).kind, 'model');
  assert.equal(codexBindingPayload(search, { route_mode: 'auto' }).route_target_account_id, 0);
});
