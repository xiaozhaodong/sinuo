import test from 'node:test';
import assert from 'node:assert/strict';

import {
  EMPTY_TREND,
  PERIOD_GROUP_FALLBACKS,
  PERIOD_GROUP_KEYS,
  isGroupClean,
  isPeriodPanelLoading,
  runLatest,
  settleGroup
} from './useOverviewData.helpers.js';

const deferred = () => {
  let resolve;
  const promise = new Promise((done) => { resolve = done; });
  return { promise, resolve };
};

test('settleGroup：一项失败不丢弃同组其他结果，失败项回落缺省值并记录错误', () => {
  const costError = new Error('成本查询失败');
  const group = settleGroup(PERIOD_GROUP_KEYS, [
    { status: 'fulfilled', value: { points: [1] } },
    { status: 'fulfilled', value: null },
    { status: 'rejected', reason: costError }
  ], PERIOD_GROUP_FALLBACKS);

  assert.deepEqual(group.data.trend, { points: [1] });
  assert.equal(group.data.modelUsage, null);
  // 失败的成本不能变成「暂无数据」的成功结果：空列表只是缺省值，错误单独记录
  assert.deepEqual(group.data.endpointCosts, []);
  assert.deepEqual(group.errors, { endpointCosts: costError });
  assert.equal(isGroupClean(group), false);
});

test('settleGroup：全部成功时没有错误，空值回落缺省值', () => {
  const group = settleGroup(PERIOD_GROUP_KEYS, [
    { status: 'fulfilled', value: undefined },
    { status: 'fulfilled', value: { rows: [] } },
    { status: 'fulfilled', value: [] }
  ], PERIOD_GROUP_FALLBACKS);

  assert.equal(group.data.trend, EMPTY_TREND);
  assert.equal(isGroupClean(group), true);
});

test('runLatest：快速切换 24h → 1h → 24h 时只提交最后一次，乱序返回的旧结果被丢弃', async () => {
  const versionRef = { current: 0 };
  const first = deferred();
  const second = deferred();
  const third = deferred();

  const firstRun = runLatest(versionRef, () => first.promise);
  const secondRun = runLatest(versionRef, () => second.promise);
  const thirdRun = runLatest(versionRef, () => third.promise);

  // 最后一次先返回，旧请求随后才到：旧的 24h 不能凭同样的标签「复活」
  third.resolve('24h-new');
  second.resolve('1h');
  first.resolve('24h-old');

  assert.deepEqual(await thirdRun, { current: true, value: '24h-new' });
  assert.deepEqual(await secondRun, { current: false });
  assert.deepEqual(await firstRun, { current: false });
});

test('runLatest：两组各自持有版本，时段组切换不会作废 KPI 组的结果', async () => {
  const kpiVersion = { current: 0 };
  const periodVersion = { current: 0 };

  const kpiRun = runLatest(kpiVersion, async () => 'kpi');
  const periodRun = runLatest(periodVersion, async () => 'old-period');
  const switchedRun = runLatest(periodVersion, async () => 'new-period');

  assert.deepEqual(await kpiRun, { current: true, value: 'kpi' });
  assert.deepEqual(await periodRun, { current: false });
  assert.deepEqual(await switchedRun, { current: true, value: 'new-period' });
});

test('runLatest：卸载时递增版本，在途请求的结果全部丢弃', async () => {
  const versionRef = { current: 0 };
  const pending = deferred();
  const run = runLatest(versionRef, () => pending.promise);

  versionRef.current += 1;
  pending.resolve('late');

  assert.deepEqual(await run, { current: false });
});

test('isPeriodPanelLoading：切换范围后四块都显示加载，同窗口刷新只让失败的那块显示加载', () => {
  // 切换后新数据未到：不能让旧窗口的数字顶着新标题
  assert.equal(isPeriodPanelLoading({ period: '1h', committedPeriod: '1d', pending: true, error: null }), true);
  // 同一窗口刷新 / 重试：成功的面板保留内容
  assert.equal(isPeriodPanelLoading({ period: '1d', committedPeriod: '1d', pending: true, error: null }), false);
  // 原本失败的面板在重试期间显示加载，而不是继续显示旧错误
  assert.equal(isPeriodPanelLoading({ period: '1d', committedPeriod: '1d', pending: true, error: new Error('x') }), true);
  // 请求结束后失败的面板显示错误
  assert.equal(isPeriodPanelLoading({ period: '1d', committedPeriod: '1d', pending: false, error: new Error('x') }), false);
});
