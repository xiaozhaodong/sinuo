import test from 'node:test';
import assert from 'node:assert/strict';
import { claudeRouteModePatch } from './routingView.js';
import { filterRoutingBindings } from '../../../components/model-routing/routingView.js';
import { bindingDisplayName, patchBindingPayload } from '../../endpoints/utils/modelRouting.js';

const bindings = [
  { assignment_id: 1, model: 'Claude-Opus', note: '主力', enabled: true },
  { assignment_id: 2, kind: 'web_tools', model: '', enabled: false }
];

test('Claude 筛选由协议显示名保留联网工具标识', () => {
  const filter = (options) => filterRoutingBindings(bindings, { ...options, displayName: bindingDisplayName });
  assert.deepEqual(filter({ query: ' OPUS ' }), [bindings[0]]);
  assert.deepEqual(filter({ query: '主力', filter: 'enabled' }), [bindings[0]]);
  assert.deepEqual(filter({ query: 'web_fetch', filter: 'disabled' }), [bindings[1]]);
  assert.deepEqual(filter({ query: 'web_search', filter: 'enabled' }), []);
  assert.deepEqual(filter({}), bindings);
});

test('Claude 行内切换策略保留目标，单成员自动指定，自动模式清空目标', () => {
  const binding = { model: 'opus', enabled: true, endpoints: [{ name: 'a' }], route_mode: 'auto', note: '保留' };
  const fixed = patchBindingPayload(binding, claudeRouteModePatch(binding, 'manual_fixed'));
  assert.deepEqual(fixed, { kind: 'model', model: 'opus', endpoints: ['a'], route_mode: 'manual_fixed', route_target: 'a', enabled: true, note: '保留' });
  const multiple = { ...binding, endpoints: [{ name: 'a' }, { name: 'b' }], route_target: 'b' };
  assert.equal(claudeRouteModePatch(multiple, 'manual_preferred').route_target, 'b');
  assert.equal(claudeRouteModePatch(multiple, 'auto').route_target, '');
  assert.equal(claudeRouteModePatch({ ...multiple, route_target: '' }, 'manual_fixed').route_target, '');
});
