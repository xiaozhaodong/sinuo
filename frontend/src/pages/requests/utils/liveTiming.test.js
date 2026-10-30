// 计时事件补丁单测：事件与查询谁先到都不能让已定格的跑秒回退
import test from 'node:test';
import assert from 'node:assert/strict';
import {
  PENDING_TIMING_PATCH_TTL_MS,
  applyPendingTimingPatches,
  applyTimingPatch,
  carryLiveTiming,
  extractTimingPatch,
  stashTimingPatch
} from './liveTiming.js';

const row = (fields = {}) => ({ requestId: 'req-1', status: 'processing', timestamp: '2026-10-11T02:00:00.000000Z', ...fields });

test('只取计时字段，没有计时字段的通知返回 null', () => {
  assert.deepEqual(
    extractTimingPatch({ request_id: 'req-1', change_type: 'first_response', status: 'processing', first_token_ms: 812, first_token_at: '2026-10-11T02:00:01.100000Z' }),
    { requestId: 'req-1', fields: { firstTokenMs: 812, firstTokenAt: '2026-10-11T02:00:01.100000Z' } }
  );
  assert.equal(extractTimingPatch({ request_id: 'req-1', change_type: 'status_changed', status: 'processing' }), null);
  assert.equal(extractTimingPatch({ first_token_ms: 1 }), null, '没有 request_id 不补');
  assert.equal(extractTimingPatch(null), null);
});

test('首响 0ms 是合法值，不能当成空缺', () => {
  const patch = extractTimingPatch({ request_id: 'req-1', first_token_ms: 0, completion_ms: 0 });
  assert.deepEqual(patch.fields, { firstTokenMs: 0, completionMs: 0 });
  const [patched] = applyTimingPatch([row()], patch);
  assert.equal(patched.firstTokenMs, 0);
  assert.equal(patched.completionMs, 0);
});

test('补丁只补空缺：已有的首响不被改写，无变化时返回原数组', () => {
  const rows = [row({ firstTokenMs: 812 }), { requestId: 'req-2' }];
  assert.equal(applyTimingPatch(rows, { requestId: 'req-1', fields: { firstTokenMs: 900 } }), rows);
  assert.equal(applyTimingPatch(rows, { requestId: 'req-x', fields: { firstTokenMs: 1 } }), rows, '不在当前页的行不动');

  const patched = applyTimingPatch(rows, { requestId: 'req-1', fields: { completionMs: 4200 } });
  assert.notEqual(patched, rows);
  assert.equal(patched[0].completionMs, 4200);
  assert.equal(patched[0].firstTokenMs, 812);
  assert.equal(patched[1], rows[1], '其他行保持原引用');
});

test('首响起点随重试后移：取较晚的那个', () => {
  const earlier = '2026-10-11T02:00:00.300000Z';
  const later = '2026-10-11T02:00:05.300000Z';
  const [moved] = applyTimingPatch([row({ firstTokenStartAt: earlier })], { requestId: 'req-1', fields: { firstTokenStartAt: later } });
  assert.equal(moved.firstTokenStartAt, later);

  const rows = [row({ firstTokenStartAt: later })];
  assert.equal(applyTimingPatch(rows, { requestId: 'req-1', fields: { firstTokenStartAt: earlier } }), rows, '晚到的旧起点不能把它拉回去');
});

test('查询早于事件发出、晚于事件返回：补回事件给过的计时，不让跑秒重跑', () => {
  const prev = [row({ firstTokenStartAt: '2026-10-11T02:00:00.300000Z', firstTokenMs: 812, firstTokenAt: '2026-10-11T02:00:01.112000Z' })];
  const staleQuery = [row({ firstTokenStartAt: '2026-10-11T02:00:00.300000Z', firstTokenMs: null, firstTokenAt: '' })];
  const [carried] = carryLiveTiming(prev, staleQuery);
  assert.equal(carried.firstTokenMs, 812);
  assert.equal(carried.firstTokenAt, '2026-10-11T02:00:01.112000Z');
});

test('查询已有的值为准；没有上一版的行原样返回', () => {
  const prev = [row({ completionMs: 4200 })];
  const next = [row({ completionMs: 4199 }), { requestId: 'req-new', completionMs: null }];
  const carried = carryLiveTiming(prev, next);
  assert.equal(carried[0], next[0], '查询已有值时不产生新对象');
  assert.equal(carried[1], next[1]);
  assert.equal(carryLiveTiming([], next), next);
});

test('行还没出现时暂存补丁，查询结果到达时补上并移出暂存', () => {
  const pending = new Map();
  const startAt = '2026-10-11T02:00:00.300000Z';
  stashTimingPatch(pending, { requestId: 'req-1', fields: { firstTokenStartAt: startAt } }, 1000);

  const queried = [row(), { requestId: 'req-2' }];
  const patched = applyPendingTimingPatches(queried, pending, 1500);
  assert.equal(patched[0].firstTokenStartAt, startAt, '关闭热池时查询不带首响起点，靠暂存补上');
  assert.equal(patched[1], queried[1], '没有暂存补丁的行保持原引用');
  assert.equal(pending.size, 0, '补上后移出暂存');
  assert.equal(applyPendingTimingPatches(queried, pending, 1600), queried, '暂存为空时返回原数组');
});

test('同一请求的多条暂存补丁合并：首响写一次，首响起点取较晚者', () => {
  const pending = new Map();
  const earlier = '2026-10-11T02:00:00.300000Z';
  const later = '2026-10-11T02:00:05.300000Z';
  stashTimingPatch(pending, { requestId: 'req-1', fields: { firstTokenStartAt: earlier } }, 1000);
  stashTimingPatch(pending, { requestId: 'req-1', fields: { firstTokenStartAt: later } }, 1100);
  stashTimingPatch(pending, { requestId: 'req-1', fields: { firstTokenMs: 812 } }, 1200);

  const [patched] = applyPendingTimingPatches([row()], pending, 1300);
  assert.equal(patched.firstTokenStartAt, later);
  assert.equal(patched.firstTokenMs, 812);
});

test('暂存补丁只补空缺，查询已有的值为准', () => {
  const pending = new Map();
  stashTimingPatch(pending, { requestId: 'req-1', fields: { firstTokenMs: 900 } }, 1000);
  const queried = [row({ firstTokenMs: 812 })];
  assert.equal(applyPendingTimingPatches(queried, pending, 1100), queried);
});

test('暂存补丁过期丢弃：行在别的页或被筛掉时不会一直留着', () => {
  const pending = new Map();
  stashTimingPatch(pending, { requestId: 'req-gone', fields: { firstTokenMs: 1 } }, 1000);
  stashTimingPatch(pending, { requestId: 'req-1', fields: { firstTokenMs: 812 } }, 1000 + PENDING_TIMING_PATCH_TTL_MS + 1);
  assert.deepEqual([...pending.keys()], ['req-1'], '再次暂存时清掉过期项');

  const queried = [row()];
  assert.equal(applyPendingTimingPatches(queried, pending, 1000 + 2 * PENDING_TIMING_PATCH_TTL_MS + 2), queried, '过期补丁不再补');
  assert.equal(pending.size, 0);
});

test('重试事件提取两项新字段，时间取较晚者，次数取较大者', () => {
  const earlier = '2026-10-30T00:00:01.000000Z';
  const later = '2026-10-30T00:00:02.000000Z';
  const patch = extractTimingPatch({ request_id: 'req-1', change_type: 'upstream_retry', retry_count: 2, retry_started_at: later });
  assert.deepEqual(patch.fields, { retryStartedAt: later, retryCount: 2 });
  const [current] = applyTimingPatch([row({ retryCount: 1, retryStartedAt: earlier })], patch);
  assert.equal(current.retryCount, 2);
  assert.equal(current.retryStartedAt, later);
  assert.equal(applyTimingPatch([current], { requestId: 'req-1', fields: { retryCount: 1, retryStartedAt: earlier } })[0], current);
  const [carried] = carryLiveTiming([current], [row({ retryCount: 0, retryStartedAt: earlier })]);
  assert.equal(carried.retryCount, 2);
  assert.equal(carried.retryStartedAt, later);
});

test('暂存重试事件不会被晚到的旧次数和旧起点覆盖', () => {
  const pending = new Map();
  stashTimingPatch(pending, { requestId: 'req-1', fields: { retryCount: 2, retryStartedAt: '2026-10-30T00:00:02.000000Z' } }, 0);
  stashTimingPatch(pending, { requestId: 'req-1', fields: { retryCount: 1, retryStartedAt: '2026-10-30T00:00:01.000000Z' } }, 10);
  const [current] = applyPendingTimingPatches([row({ retryCount: 0 })], pending, 20);
  assert.equal(current.retryCount, 2);
  assert.equal(current.retryStartedAt, '2026-10-30T00:00:02.000000Z');
});
