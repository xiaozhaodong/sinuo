import test from 'node:test';
import assert from 'node:assert/strict';

import {
  bindingPayload,
  createBindingDraft,
  endpointStatusTone,
  filterEndpoints,
  normalizeDraftTarget,
  patchBindingPayload,
  routeModeLabel,
  setDraftRouteMode,
  toggleDraftEndpoint,
  validateBindingDraft
} from './modelRouting.js';

const draftOf = (overrides = {}) => ({
  ...createBindingDraft({ model: 'claude-opus-4-5', endpoints: [{ name: 'relay-a' }, { name: 'relay-b' }] }),
  ...overrides
});

test('draft is built from the binding view without leaking group concepts', () => {
  const draft = createBindingDraft({
    assignment_id: 7,
    model: 'claude-opus-4-5',
    endpoints: [{ name: 'relay-a' }, { name: 'relay-b' }],
    route_mode: 'manual_fixed',
    route_target: 'relay-b',
    enabled: false,
    note: '主力'
  });
  assert.deepEqual(draft, {
    assignmentId: 7,
    model: 'claude-opus-4-5',
    endpoints: ['relay-a', 'relay-b'],
    routeMode: 'manual_fixed',
    routeTarget: 'relay-b',
    enabled: false,
    note: '主力'
  });
  assert.deepEqual(createBindingDraft(), {
    assignmentId: null, model: '', endpoints: [], routeMode: 'auto', routeTarget: '', enabled: true, note: ''
  });
});

test('unselecting the routing target drops it instead of saving an invalid pair', () => {
  const fixed = { ...draftOf(), routeMode: 'manual_fixed', routeTarget: 'relay-b' };
  const afterRemoval = toggleDraftEndpoint(fixed, 'relay-b');
  assert.deepEqual(afterRemoval.endpoints, ['relay-a']);
  // 只剩一个候选时直接补上，避免用户必须再点一次下拉。
  assert.equal(afterRemoval.routeTarget, 'relay-a');

  const twoLeft = toggleDraftEndpoint({ ...fixed, endpoints: ['relay-a', 'relay-b', 'relay-c'] }, 'relay-b');
  assert.equal(twoLeft.routeTarget, '');
});

test('switching back to auto clears the target', () => {
  const fixed = { ...draftOf(), routeMode: 'manual_fixed', routeTarget: 'relay-b' };
  assert.equal(setDraftRouteMode(fixed, 'auto').routeTarget, '');
  assert.equal(normalizeDraftTarget({ ...fixed, routeMode: 'auto' }).routeTarget, '');
});

test('validation mirrors the backend contract', () => {
  assert.equal(validateBindingDraft({ ...draftOf(), model: '' }), '请输入模型名');
  assert.equal(validateBindingDraft({ ...draftOf(), model: 'claude opus' }), '模型名不能包含空格或控制字符');
  // 连字符和点号是模型名的常态，不能被误判。
  assert.equal(validateBindingDraft({ ...draftOf(), model: 'claude-3.5-haiku-20241022' }), '');
  assert.equal(validateBindingDraft({ ...draftOf(), endpoints: [] }), '至少选择 1 个上游端点');
  assert.equal(
    validateBindingDraft({ ...draftOf(), routeMode: 'manual_fixed', routeTarget: '' }),
    '优选 / 固定模式需要指定一个目标端点'
  );
  assert.equal(
    validateBindingDraft({ ...draftOf(), routeMode: 'manual_preferred', routeTarget: 'relay-z' }),
    '目标端点必须在已选端点中'
  );
});

test('duplicate model detection is case-insensitive and skips the edited row', () => {
  const existing = [
    { assignment_id: 1, model: 'Claude-Opus-4-5' },
    { assignment_id: 2, model: 'claude-haiku' }
  ];
  assert.equal(
    validateBindingDraft({ ...draftOf(), assignmentId: null }, existing),
    '模型「claude-opus-4-5」已经配置过上游端点'
  );
  assert.equal(validateBindingDraft({ ...draftOf(), assignmentId: 1 }, existing), '');
});

test('payload strips the target in auto mode and trims free text', () => {
  const payload = bindingPayload({ ...draftOf(), model: '  claude-opus-4-5 ', routeMode: 'auto', routeTarget: 'relay-b', note: ' 主力 ' });
  assert.equal(payload.model, 'claude-opus-4-5');
  assert.equal(payload.route_target, '');
  assert.equal(payload.note, '主力');
});

test('labels and tones describe what actually blocks a request', () => {
  assert.equal(routeModeLabel('auto', 'relay-b'), '自动');
  assert.equal(routeModeLabel('manual_fixed', 'relay-b'), '固定 → relay-b');
  assert.equal(endpointStatusTone('ok'), 'tone-emerald');
  assert.equal(endpointStatusTone('cooldown'), 'tone-amber');
  assert.equal(endpointStatusTone('availability_disabled'), 'tone-rose');
  assert.equal(endpointStatusTone('missing'), 'tone-rose');
});

test('endpoint search matches name and url', () => {
  const endpoints = [
    { name: 'relay-a', url: 'https://alpha.example.com' },
    { name: 'relay-b', url: 'https://beta.example.com' }
  ];
  assert.deepEqual(filterEndpoints(endpoints, 'beta').map((item) => item.name), ['relay-b']);
  assert.deepEqual(filterEndpoints(endpoints, ' RELAY-A ').map((item) => item.name), ['relay-a']);
  assert.equal(filterEndpoints(endpoints, '  ').length, 2);
});

test('patchBindingPayload keeps the endpoint set and normalizes the target', () => {
  const binding = {
    assignment_id: 3,
    model: 'claude-opus-4-5',
    endpoints: [{ name: 'relay-a' }, { name: 'relay-b' }],
    route_mode: 'manual_fixed',
    route_target: 'relay-b',
    enabled: true,
    note: '主力'
  };
  assert.deepEqual(patchBindingPayload(binding, { route_mode: 'auto' }), {
    model: 'claude-opus-4-5', endpoints: ['relay-a', 'relay-b'], route_mode: 'auto', route_target: '', enabled: true, note: '主力'
  });
  assert.equal(patchBindingPayload(binding, { route_mode: 'manual_preferred', route_target: 'relay-a' }).route_target, 'relay-a');
  assert.equal(patchBindingPayload(binding, { route_mode: 'manual_preferred' }).route_target, '');
  assert.equal(patchBindingPayload({ ...binding, endpoints: [{ name: 'relay-a' }], route_mode: 'auto', route_target: '' }, { route_mode: 'manual_fixed' }).route_target, 'relay-a');
  assert.equal(patchBindingPayload(binding, { enabled: false }).enabled, false);
});
