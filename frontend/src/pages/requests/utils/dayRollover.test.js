import test from 'node:test';
import assert from 'node:assert/strict';
import { getRecentDaysRange, getTodayTimeRange } from '../../../utils/timezone.js';
import { computePresetRange, resolveDayRollover } from './dayRollover.js';

const timezone = 'Asia/Shanghai';
const beforeMidnight = new Date('2026-10-14T15:59:59Z');
const afterMidnight = new Date('2026-10-14T16:00:01Z');

test('上海「今天」在业务午夜前不变，午夜后顺延一天', () => {
  const filters = { startDate: '2026-10-14T00:00', endDate: '2026-10-15T00:00' };
  assert.equal(resolveDayRollover({ activeRange: 'today', filters, timezone, now: beforeMidnight }), null);
  assert.deepEqual(resolveDayRollover({ activeRange: 'today', filters, timezone, now: afterMidnight }), {
    startDate: '2026-10-15T00:00', endDate: '2026-10-16T00:00'
  });
});

for (const [activeRange, startDate, nextStartDate] of [
  ['7days', '2026-10-08T00:00', '2026-10-09T00:00'],
  ['30days', '2026-09-15T00:00', '2026-09-16T00:00']
]) {
  test(`${activeRange} 跨业务午夜后起止各顺延一天`, () => {
    const filters = { startDate, endDate: '2026-10-15T00:00' };
    assert.equal(resolveDayRollover({ activeRange, filters, timezone, now: beforeMidnight }), null);
    assert.deepEqual(resolveDayRollover({ activeRange, filters, timezone, now: afterMidnight }), {
      startDate: nextStartDate, endDate: '2026-10-16T00:00'
    });
  });
}

test('自定义区间与未知预设不自动顺延', () => {
  const filters = { startDate: '2026-10-01T08:00', endDate: '2026-10-02T09:00' };
  for (const activeRange of ['custom', 'unknown']) {
    assert.equal(resolveDayRollover({ activeRange, filters, timezone, now: afterMidnight }), null);
  }
});

test('活动时区为空时不顺延', () => {
  const filters = { startDate: '2026-10-14T00:00', endDate: '2026-10-15T00:00' };
  assert.equal(resolveDayRollover({ activeRange: 'today', filters, timezone: '', now: afterMidnight }), null);
});

test('纽约 DST 回拨日前后按业务日期顺延，重复小时不触发顺延', () => {
  const timezone = 'America/New_York';
  const previous = { startDate: '2026-10-31T00:00', endDate: '2026-11-01T00:00' };
  const current = { startDate: '2026-11-01T00:00', endDate: '2026-11-02T00:00' };
  assert.equal(resolveDayRollover({ activeRange: 'today', filters: previous, timezone, now: new Date('2026-11-01T03:59:59Z') }), null);
  assert.deepEqual(resolveDayRollover({ activeRange: 'today', filters: previous, timezone, now: new Date('2026-11-01T04:00:01Z') }), current);
  for (const timestamp of ['2026-11-01T05:30:00Z', '2026-11-01T06:30:00Z', '2026-11-02T04:59:59Z']) {
    assert.equal(resolveDayRollover({ activeRange: 'today', filters: current, timezone, now: new Date(timestamp) }), null);
  }
  assert.deepEqual(resolveDayRollover({ activeRange: 'today', filters: current, timezone, now: new Date('2026-11-02T05:00:01Z') }), {
    startDate: '2026-11-02T00:00', endDate: '2026-11-03T00:00'
  });
});

for (const [range, days] of [['today', 1], ['7days', 7], ['30days', 30]]) {
  test(`computePresetRange ${range} 与手动选择的原计算口径一致`, () => {
    const expected = range === 'today'
      ? getTodayTimeRange(timezone, afterMidnight)
      : getRecentDaysRange(days, timezone, afterMidnight);
    assert.deepEqual(computePresetRange(range, timezone, afterMidnight), {
      startDate: expected.start_date, endDate: expected.end_date
    });
  });
}

test('computePresetRange 未知值沿用今天预设', () => {
  assert.deepEqual(computePresetRange('unknown', timezone, afterMidnight), {
    startDate: '2026-10-15T00:00', endDate: '2026-10-16T00:00'
  });
});
