import test from 'node:test';
import assert from 'node:assert/strict';
import {
  formatListTimestamp,
  formatTimestamp,
  getRecentDaysRange,
  getTodayTimeRange,
  parseTimestamp,
  validateTimezone
} from './timezone.js';

test('formatTimestamp ignores the host timezone and uses configured timezone', () => {
  assert.equal(formatTimestamp('2026-08-04T06:50:10.000000Z', 'Asia/Shanghai'), '2026/8/4 14:50:10');
  assert.equal(formatTimestamp('2026-08-04T06:50:10.000000Z', 'America/New_York'), '2026/8/4 02:50:10');
});

test('today range uses configured calendar date with a half-open next-day boundary', () => {
  const now = new Date('2026-03-08T05:30:00Z');
  assert.deepEqual(getTodayTimeRange('America/New_York', now), {
    start_date: '2026-03-08T00:00', end_date: '2026-03-09T00:00'
  });
});

test('recent range shifts calendar dates instead of subtracting fixed 24 hour durations', () => {
  const now = new Date('2026-11-01T17:00:00Z');
  assert.deepEqual(getRecentDaysRange(7, 'America/New_York', now), {
    start_date: '2026-10-26T00:00', end_date: '2026-11-02T00:00'
  });
});

test('invalid or missing timezone fails explicitly', () => {
  assert.throws(() => validateTimezone(''), /未返回活动时区/);
  assert.throws(() => validateTimezone('Mars\/Olympus'), /无效时区/);
});

test('parseTimestamp accepts absolute instants and rejects ambiguous strings', () => {
  assert.equal(parseTimestamp('2026-08-13T00:00:01.000000Z').getTime(), Date.parse('2026-08-13T00:00:01.000000Z'));
  assert.equal(parseTimestamp('2026-08-13T08:00:00+08:00').getTime(), Date.parse('2026-08-13T08:00:00+08:00'));

  assert.throws(() => parseTimestamp(''), /时间为空/);
  assert.throws(() => parseTimestamp('2026-08-13 00:00:01'), /缺少时区/);
  assert.throws(() => parseTimestamp('not-a-date'), /缺少时区/);
  assert.throws(() => parseTimestamp('2026-99-99T00:00:00Z'), /无效时间/);
});

test('formatListTimestamp 当天显示时分秒，其它日期显示月/日 时:分，按配置时区判断当天', () => {
  const now = new Date('2026-08-04T10:00:00.000Z'); // 上海 18:00，纽约 06:00，同为 8/4
  assert.equal(formatListTimestamp('2026-08-04T06:50:10.000000Z', 'Asia/Shanghai', now), '14:50:10');
  assert.equal(formatListTimestamp('2026-08-03T06:50:10.000000Z', 'Asia/Shanghai', now), '8/3 14:50');
  // 上海已是 8/5 00:30，但纽约仍是 8/4 —— 「当天」跟随配置时区，而不是宿主时区
  const lateNow = new Date('2026-08-04T16:30:00.000Z');
  assert.equal(formatListTimestamp('2026-08-04T06:50:10.000000Z', 'Asia/Shanghai', lateNow), '8/4 14:50');
  assert.equal(formatListTimestamp('2026-08-04T06:50:10.000000Z', 'America/New_York', lateNow), '02:50:10');
  assert.equal(formatListTimestamp('', 'Asia/Shanghai', now), '-');
});
