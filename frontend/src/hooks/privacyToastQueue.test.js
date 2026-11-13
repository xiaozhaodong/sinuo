import test from 'node:test';
import assert from 'node:assert/strict';
import { createPrivacyNotificationGate, PRIVACY_RECEIPT_LIMIT } from './privacyToastQueue.js';
import { enqueueNotification, dismissNotification, MAX_VISIBLE_TOASTS } from './useGlobalToasts.js';

const notice = (id, codes = ['request_fail_open']) => ({ kind: 'privacy_warning', request_id: id, warning_codes: codes });
const dispatch = (gate, queue, event, now = 1000) => {
  const accepted = gate.accept(event);
  return accepted ? enqueueNotification(queue, accepted, now) : queue;
};

test('重复事件、关闭后的重放不重弹；同一请求新增阶段异常不会丢失', () => {
  const gate = createPrivacyNotificationGate();
  let queue = dispatch(gate, [], notice('a'));
  const first = queue;
  queue = dispatch(gate, queue, { data: notice('a') }, 2000);
  assert.equal(queue, first);
  queue = dismissNotification(queue, queue[0].id, 3000);
  assert.equal(dispatch(gate, queue, notice('a'), 4000), queue);
  queue = dispatch(gate, queue, notice('a', ['request_fail_open', 'response_failed']), 5000);
  assert.equal(queue.length, 1);
  assert.match(queue[0].title, /响应回填失败/);
  assert.doesNotMatch(queue[0].message, /原文放行/);
});

test('并发两类异常合并精确计数，不刷新截止时间或串扰普通通知', () => {
  const gate = createPrivacyNotificationGate();
  let queue = enqueueNotification([], { message: '普通通知', level: 'info' }, 0);
  for (let n = 0; n < 100; n += 1) {
    queue = dispatch(gate, queue, notice(`a-${n}`, n % 2 ? ['response_bypassed'] : ['request_fail_open']), 1000 + n);
  }
  assert.equal(queue.length, 2);
  assert.equal(queue[0].message, '普通通知');
  assert.equal(queue[0].expiresAt, 6500);
  assert.equal(queue[1].expiresAt, 9000);
  assert.match(queue[1].title, /^100 个请求/);
  assert.match(queue[1].message, /请求脱敏失败后原文放行 50 个/);
  assert.match(queue[1].message, /响应回填失败后原样放行 50 个/);
  queue = dispatch(gate, queue, notice('a-0', ['request_fail_open', 'response_failed']), 2000);
  assert.match(queue[1].title, /^100 个请求/);
  assert.match(queue[1].message, /停止交付 1 个/);
});

test('排队中的异常合并，展示时才计时；过期组不吸收新的异常', () => {
  const gate = createPrivacyNotificationGate();
  let queue = [];
  for (let n = 0; n < MAX_VISIBLE_TOASTS; n += 1) queue = enqueueNotification(queue, { message: `普通-${n}` }, 0);
  queue = dispatch(gate, queue, notice('a'), 100);
  queue = dispatch(gate, queue, notice('b'), 20000);
  assert.equal(queue.length, 5);
  assert.equal(queue[4].expiresAt, null);
  queue = dismissNotification(queue, queue[0].id, 30000);
  assert.equal(queue[3].expiresAt, 38000);
  queue = dispatch(gate, queue, notice('c'), 38001);
  assert.equal(queue.length, 5);
  assert.match(queue[4].title, /请求脱敏失败/);
});

test('收据有界；特殊 requestID 不污染对象；恶意文案与未知代码被拒绝', () => {
  const gate = createPrivacyNotificationGate();
  let queue = [];
  for (const id of ['__proto__', 'constructor', 'toString']) queue = dispatch(gate, queue, notice(id));
  assert.match(queue[0].title, /^3 个请求/);
  for (let n = 0; n < PRIVACY_RECEIPT_LIMIT + 2; n += 1) gate.accept(notice(String(n)));
  assert.equal(gate.size, PRIVACY_RECEIPT_LIMIT);
  for (const bad of [notice(''), notice('a', ['SECRET']), notice('x'.repeat(129)), notice('a', 'request_fail_open')]) {
    assert.equal(gate.accept(bad), null);
  }
  queue = enqueueNotification([], { ...notice('safe'), title: 'SECRET', message: 'SECRET', duration: 1 });
  assert.doesNotMatch(JSON.stringify(queue), /SECRET/);
  assert.equal(queue[0].duration, 8000);
});
