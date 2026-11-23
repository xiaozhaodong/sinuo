import test from 'node:test';
import assert from 'node:assert/strict';

import {
  appendLogBatch,
  createInitialLogBuffer,
  replaceRecentLogs,
} from './useWailsLogs.helpers.js';

const makeLogs = (count, start = 1, level = 'INFO') =>
  Array.from({ length: count }, (_, index) => ({
    id: start + index,
    level,
    message: `log-${start + index}`,
  }));

test('缓冲区满 1000 条后继续接收实时日志时，长度保持上限但 receivedCount 持续递增', () => {
  let buffer = createInitialLogBuffer();
  buffer = replaceRecentLogs(buffer, makeLogs(1000, 1), 1000);

  assert.equal(buffer.logs.length, 1000);
  assert.equal(buffer.receivedCount, 0);

  const afterFirstBatch = appendLogBatch(buffer, makeLogs(3, 1001), 1000);
  assert.equal(afterFirstBatch.logs.length, 1000);
  assert.equal(afterFirstBatch.logs[0].id, 4);
  assert.equal(afterFirstBatch.logs.at(-1).id, 1003);
  assert.equal(afterFirstBatch.receivedCount, 3);

  const afterSecondBatch = appendLogBatch(afterFirstBatch, makeLogs(1, 1004), 1000);
  assert.equal(afterSecondBatch.logs.length, 1000);
  assert.equal(afterSecondBatch.logs[0].id, 5);
  assert.equal(afterSecondBatch.logs.at(-1).id, 1004);
  assert.equal(afterSecondBatch.receivedCount - afterFirstBatch.receivedCount, 1);
});

test('跨过 1000 条边界的单批日志不会被长度差截断少算', () => {
  let buffer = createInitialLogBuffer();
  buffer = appendLogBatch(buffer, makeLogs(995, 1), 1000);
  assert.equal(buffer.logs.length, 995);
  assert.equal(buffer.receivedCount, 995);

  const next = appendLogBatch(buffer, makeLogs(10, 996), 1000);
  assert.equal(next.logs.length, 1000);
  assert.equal(next.receivedCount - buffer.receivedCount, 10);
});

test('空批次或被级别过滤掉的批次不增加 receivedCount 且保持引用不变', () => {
  const initial = appendLogBatch(createInitialLogBuffer(), makeLogs(2, 1, 'ERROR'), 1000, 'ERROR');
  assert.equal(initial.receivedCount, 2);

  const afterEmpty = appendLogBatch(initial, [], 1000, 'ERROR');
  assert.equal(afterEmpty, initial);

  const afterFilteredOut = appendLogBatch(initial, makeLogs(5, 3, 'INFO'), 1000, 'ERROR');
  assert.equal(afterFilteredOut, initial);
  assert.equal(afterFilteredOut.receivedCount, 2);
});

test('历史快照刷新保留 receivedCount 基线，不会把历史条数误算为新增日志', () => {
  let buffer = appendLogBatch(createInitialLogBuffer(), makeLogs(5, 1), 1000);
  assert.equal(buffer.receivedCount, 5);

  const refreshed = replaceRecentLogs(buffer, makeLogs(1000, 100), 1000);
  assert.equal(refreshed.logs.length, 1000);
  assert.equal(refreshed.receivedCount, 5);
});
