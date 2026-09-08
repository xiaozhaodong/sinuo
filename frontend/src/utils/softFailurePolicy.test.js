import test from 'node:test';
import assert from 'node:assert/strict';
import {
  MAX_SOFT_FAILURE_WINDOW_MINUTES,
  buildSoftFailurePolicyPayload,
  createSoftFailurePolicyState,
  normalizeSoftFailurePolicyPayload,
  validateSoftFailurePolicyState
} from './softFailurePolicy.js';

test('旧记录默认继承，自定义策略可以恢复默认', () => {
  const initial = createSoftFailurePolicyState();
  assert.deepEqual(initial, { softFailureCustom: false, softFailureWindowMinutes: '5', softFailureThreshold: '3' });
  const inherited = { soft_failure_window_minutes: null, soft_failure_threshold: null };
  assert.deepEqual(buildSoftFailurePolicyPayload(initial), inherited);
  const edited = { softFailureCustom: true, softFailureWindowMinutes: '10', softFailureThreshold: '5' };
  const payload = buildSoftFailurePolicyPayload(edited);
  assert.deepEqual(payload, { soft_failure_window_minutes: 10, soft_failure_threshold: 5 });
  assert.deepEqual(createSoftFailurePolicyState(payload), edited);
  assert.deepEqual(buildSoftFailurePolicyPayload({ ...edited, softFailureCustom: false }), inherited);
});

test('显式清空优先于旧别名，重复归一化保持默认', () => {
  const input = { soft_failure_window_minutes: null, soft_failure_threshold: null, softFailureWindowMinutes: '10', softFailureThreshold: '5' };
  const payload = normalizeSoftFailurePolicyPayload(input);
  assert.deepEqual(payload, { soft_failure_window_minutes: null, soft_failure_threshold: null });
  assert.deepEqual(normalizeSoftFailurePolicyPayload(payload), payload);
});

test('两侧边界拒绝小数、非数字、越界和不完整策略', () => {
  for (const bad of [0, -1, 1.5, '', 'bad', true, [], [1], Infinity, MAX_SOFT_FAILURE_WINDOW_MINUTES + 1]) {
    const state = { softFailureCustom: true, softFailureWindowMinutes: bad, softFailureThreshold: '3' };
    assert.ok(validateSoftFailurePolicyState(state));
    assert.throws(() => buildSoftFailurePolicyPayload(state));
  }
  for (const bad of [0, -1, 1.5, '', 'bad', false]) {
    assert.throws(() => normalizeSoftFailurePolicyPayload({ softFailureWindowMinutes: 5, softFailureThreshold: bad }));
  }
  assert.throws(() => normalizeSoftFailurePolicyPayload({ softFailureWindowMinutes: 5 }));
  assert.throws(() => normalizeSoftFailurePolicyPayload({ softFailureThreshold: 3 }));
});
