import test from 'node:test';
import assert from 'node:assert/strict';

import { buildQueryParamsFromFilters, formatFilterDateTimeInput } from './filterTime.js';

test('buildQueryParamsFromFilters normalizes supported date-time precisions without producing malformed values', () => {
  assert.deepEqual(buildQueryParamsFromFilters({
    startDate: '2026-08-04',
    endDate: '2026-08-05T00:00'
  }), {
    start_date: '2026-08-04T00:00:00',
    end_date: '2026-08-05T00:00:00'
  });

  assert.deepEqual(buildQueryParamsFromFilters({
    startDate: '2026-08-04T01:02:03',
    endDate: '2026-08-05T01:02:03+08:00'
  }), {
    start_date: '2026-08-04T01:02:03',
    end_date: '2026-08-05T01:02:03+08:00'
  });
});


test('订阅来源与「仅生成」口径随查询传递', () => {
  assert.deepEqual(
    buildQueryParamsFromFilters({ sourceView: 'subscription' }),
    { source_view: 'subscription' }
  );
  assert.deepEqual(
    buildQueryParamsFromFilters({ sourceView: 'all', includeCompaction: false }),
    { include_compaction: false }
  );
  // 默认（含独立压缩）不下传，由后端承载全部落库记录
  assert.deepEqual(buildQueryParamsFromFilters({ includeCompaction: true }), {});
  assert.deepEqual(buildQueryParamsFromFilters({}), {});
});

test('概览交接保留绝对边界和来源，日期输入转换不修改查询', () => {
  const filters = { startDate: '2026-11-01T05:30:00.123456Z', endDate: '2026-11-01T06:30:00.654321Z', model: 'm', requestFamily: 'codex', sourceView: 'account' };
  const expected = { start_date: filters.startDate, end_date: filters.endDate, model: 'm', request_family: 'codex', source_view: 'account' };
  assert.deepEqual(buildQueryParamsFromFilters(filters), expected);
  // DST 重复 01:30 小时显示相同，但实际查询仍相差整整一小时。
  assert.equal(formatFilterDateTimeInput(filters.startDate, 'America/New_York'), '2026-11-01T01:30:00.123');
  assert.equal(formatFilterDateTimeInput(filters.endDate, 'America/New_York'), '2026-11-01T01:30:00.654');
  assert.deepEqual(buildQueryParamsFromFilters(filters), expected);
  assert.equal(buildQueryParamsFromFilters({ ...filters, sourceView: 'all' }).source_view, undefined);
  assert.equal(formatFilterDateTimeInput('2026-09-09T10:00', 'Asia/Shanghai'), '2026-09-09T10:00');
});
