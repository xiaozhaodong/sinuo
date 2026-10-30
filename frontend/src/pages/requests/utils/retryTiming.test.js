import test from 'node:test';
import assert from 'node:assert/strict';
import { firstResponseWait, visibleRetryCount } from './retryTiming.js';
import { getPendingSendPillState } from './timing.js';

const initial = '2026-10-30T00:00:00.000000Z';
const retry = '2026-10-30T00:00:02.000000Z';
const sent = '2026-10-30T00:00:03.000000Z';

test('重试开始后等待点从本次取连接起算，写完后恢复首响计时', () => {
  const waiting = firstResponseWait({ firstTokenStartAt: initial, retryStartedAt: retry });
  assert.equal(waiting.timestamp, retry);
  assert.equal(waiting.hint, '上一次尝试未成功，正在重试（尚未发到上游）');
  assert.deepEqual(getPendingSendPillState(500), { tone: 'tone-slate', animate: false });
  assert.deepEqual(getPendingSendPillState(3500), { tone: 'tone-amber', animate: true });
  assert.deepEqual(getPendingSendPillState(11000), { tone: 'tone-rose', animate: true });
  assert.equal(firstResponseWait({ firstTokenStartAt: sent, retryStartedAt: retry }), null);
  assert.equal(firstResponseWait({ firstTokenStartAt: retry, retryStartedAt: retry }), null);
  assert.deepEqual(firstResponseWait({ timestamp: initial }), { timestamp: initial });
});

test('列表重试标记只显示可信次数，真实0偏移有效，旧数据1隐藏', () => {
  assert.equal(visibleRetryCount({ retryCount: 1 }, false), 0);
  assert.equal(visibleRetryCount({ retryCount: 1, upstreamFirstDispatchMs: 0 }, false), 1);
  assert.equal(visibleRetryCount({ retryCount: 0, upstreamFirstDispatchMs: 20 }, false), 0);
  assert.equal(visibleRetryCount({ retryCount: 1, upstreamFirstDispatchMs: 20 }, true), 0);
  assert.equal(visibleRetryCount({ retryCount: 1, retryStartedAt: retry }, true), 1);
  assert.equal(visibleRetryCount({ retryCount: 1, retryStartedAt: retry }, false), 0);
});
