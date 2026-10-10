import test from 'node:test';
import assert from 'node:assert/strict';
import {
  MAX_VISIBLE_TOASTS,
  dismissNotification,
  enqueueNotification,
  getNotificationDedupeKey,
  normalizeNotification
} from './useGlobalToasts.js';

test('normalizes Wails notification payloads for the global toast host', () => {
  const notification = normalizeNotification({
    event: 'notification',
    data: {
      level: 'warning',
      title: '发生故障转移',
      message: 'Codex 账号已从「main」切换到「backup」。原因：鉴权失败（HTTP 401）',
      kind: 'failover',
      lane: 'codex',
      from: 'main',
      to: 'backup',
      reason_code: 'auth_failed',
      reason_label: '鉴权失败',
      request_id: 'req-1',
      attempt: 1
    }
  });

  assert.equal(notification.level, 'warning');
  assert.equal(notification.kind, 'failover');
  assert.equal(notification.lane, 'codex');
  assert.equal(notification.from, 'main');
  assert.equal(notification.to, 'backup');
  assert.equal(notification.reasonLabel, '鉴权失败');
  assert.equal(notification.duration, 7500);
});

test('keeps absent or blank notification titles empty', () => {
  for (const title of [undefined, '', '  ']) {
    const notification = normalizeNotification({ title, message: '操作完成' });
    assert.equal(notification.title, '');
  }
});

test('honors positive finite notification durations and preserves defaults otherwise', () => {
  for (const kind of ['notification', 'failover']) {
    assert.equal(normalizeNotification({ kind, message: '操作完成', duration: 4000 }).duration, 4000);

    for (const duration of [undefined, 0, -1, NaN, Infinity, '4000']) {
      const notification = normalizeNotification({ kind, message: '操作完成', duration });
      assert.equal(notification.duration, kind === 'failover' ? 7500 : 6500);
    }
  }
});

test('keeps separate failover attempts distinct while deduplicating the same event', () => {
  const first = {
    kind: 'failover',
    lane: 'cc',
    from: 'primary',
    to: 'backup',
    reason_code: 'auth_rejected',
    request_id: 'req-1',
    attempt: 1,
    message: '切换'
  };
  const duplicate = { ...first };
  const secondAttempt = { ...first, to: 'cold', attempt: 2 };

  assert.equal(getNotificationDedupeKey(first), getNotificationDedupeKey(duplicate));
  assert.notEqual(getNotificationDedupeKey(first), getNotificationDedupeKey(secondAttempt));
});

test('keeps unrelated global notifications distinct', () => {
  const first = {
    level: 'info',
    title: '连接状态',
    message: '代理已启动'
  };
  const second = {
    level: 'warning',
    title: '连接状态',
    message: '代理已停止'
  };

  assert.notEqual(getNotificationDedupeKey(first), getNotificationDedupeKey(second));
});

test('preserves remaining toast deadlines when another toast is dismissed', () => {
  let queue = [];
  for (let index = 0; index < MAX_VISIBLE_TOASTS + 1; index += 1) {
    queue = enqueueNotification(queue, {
      kind: 'failover',
      lane: 'codex',
      from: `account-${index}`,
      to: `account-${index + 1}`,
      reason_code: 'auth_failed',
      request_id: `request-${index}`,
      attempt: index + 1,
      message: `切换 ${index}`
    }, 1_000 + index);
  }

  assert.equal(queue.length, MAX_VISIBLE_TOASTS + 1);
  assert.ok(queue.slice(0, MAX_VISIBLE_TOASTS).every((toast) => Number.isFinite(toast.expiresAt)));
  assert.equal(queue[1].expiresAt, 1_001 + queue[1].duration);
  assert.equal(queue[MAX_VISIBLE_TOASTS].expiresAt, null);

  const firstId = queue[0].id;
  const secondExpiry = queue[1].expiresAt;
  queue = dismissNotification(queue, firstId, 5_000);

  assert.equal(queue[0].expiresAt, secondExpiry);
  assert.equal(queue[MAX_VISIBLE_TOASTS - 1].expiresAt, 5_000 + queue[MAX_VISIBLE_TOASTS - 1].duration);
});

test('queues cascade notifications without dropping the earliest event', () => {
  let queue = [];
  for (let index = 0; index < 8; index += 1) {
    queue = enqueueNotification(queue, {
      kind: 'failover',
      lane: 'cc',
      from: `endpoint-${index}`,
      to: `endpoint-${index + 1}`,
      reason_code: 'rate_limited',
      request_id: `request-${index}`,
      attempt: index + 1,
      message: `故障转移 ${index}`
    }, 10_000);
  }

  assert.equal(queue.length, 8);
  assert.equal(queue[0].from, 'endpoint-0');
  assert.equal(queue.at(-1).from, 'endpoint-7');
});
