import test from 'node:test';
import assert from 'node:assert/strict';

import { consumePendingRequestsFilters, setPendingRequestsFilters } from './overviewHandoff.js';

test('概览带过来的筛选条件只被消费一次', () => {
  setPendingRequestsFilters({ model: 'claude-sonnet-4', requestFamily: 'claude' });

  assert.deepEqual(consumePendingRequestsFilters(), {
    model: 'claude-sonnet-4',
    requestFamily: 'claude'
  });
  // 第二次进入请求页不应被上一次的筛选粘住
  assert.equal(consumePendingRequestsFilters(), null);
});

test('时间范围随筛选条件一起交接', () => {
  setPendingRequestsFilters({
    model: 'gpt-6-astra',
    requestFamily: 'codex',
    startDate: '2026-09-02T10:25',
    endDate: '2026-09-09T10:26'
  });

  assert.deepEqual(consumePendingRequestsFilters(), {
    model: 'gpt-6-astra',
    requestFamily: 'codex',
    startDate: '2026-09-02T10:25',
    endDate: '2026-09-09T10:26'
  });
});

test('空筛选条件视为无交接，避免给请求页塞默认值', () => {
  setPendingRequestsFilters({});
  assert.equal(consumePendingRequestsFilters(), null);

  setPendingRequestsFilters({ requestFamily: 'all' });
  assert.equal(consumePendingRequestsFilters(), null);

  setPendingRequestsFilters(null);
  assert.equal(consumePendingRequestsFilters(), null);
});


test('订阅来源与「仅生成」口径随交接保留', () => {
  setPendingRequestsFilters({ model: 'gpt-5', sourceView: 'subscription', includeCompaction: false });
  assert.deepEqual(consumePendingRequestsFilters(), {
    model: 'gpt-5',
    sourceView: 'subscription',
    includeCompaction: false
  });
  // 默认「含独立压缩」不写进交接，避免给请求页塞默认值
  setPendingRequestsFilters({ includeCompaction: true });
  assert.equal(consumePendingRequestsFilters(), null);
});

test('模型行交接包含精确时间、请求族与上游类型，消费后清空', () => {
  const filters = { model: 'm', requestFamily: 'codex', sourceView: 'account',
    startDate: '2026-11-01T05:30:00.123456Z', endDate: '2026-11-01T06:30:00.654321Z' };
  setPendingRequestsFilters(filters);
  assert.deepEqual(consumePendingRequestsFilters(), filters);
  assert.equal(consumePendingRequestsFilters(), null);
  setPendingRequestsFilters({ sourceView: 'invalid' });
  assert.equal(consumePendingRequestsFilters(), null);
});

test('托盘带过来的状态筛选只交接具体状态，「全部状态」视为无交接', () => {
  setPendingRequestsFilters({ status: 'failed' });
  assert.deepEqual(consumePendingRequestsFilters(), { status: 'failed' });

  setPendingRequestsFilters({ status: 'all' });
  assert.equal(consumePendingRequestsFilters(), null);
});
