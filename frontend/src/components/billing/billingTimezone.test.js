import assert from 'node:assert/strict';
import test from 'node:test';
import { matchesTimezoneQuery, timezoneOption } from './billingTimezone.js';

const winter = new Date('2026-01-15T12:00:00Z');
const summer = new Date('2026-07-15T12:00:00Z');

test('纽约夏令时和标准时使用不同偏移，选择历史别名仍返回原 ID', () => {
  const ny = timezoneOption('US/Eastern', winter);
  assert.equal(ny.label, '纽约（UTC-5）');
  assert.equal(ny.id, 'US/Eastern');
  assert.equal(timezoneOption(ny.id, summer).label, '纽约（UTC-4）');
});

test('中文城市、零偏移、非整点偏移及 Etc/GMT 的反向符号均可区分', () => {
  assert.equal(timezoneOption('Asia/Shanghai', winter).label, '上海（UTC+8）');
  assert.equal(timezoneOption('UTC', winter).label, '协调世界时（UTC+0）');
  assert.equal(timezoneOption('Asia/Kathmandu', winter).label, '加德满都（UTC+5:45）');
  assert.equal(timezoneOption('Etc/GMT+8', winter).label, '固定偏移（UTC-8）');
});

test('时区搜索同时支持中英文多词分词与英文下划线城市名', () => {
  const shanghai = timezoneOption('Asia/Shanghai', winter);
  const newYork = timezoneOption('America/New_York', winter);
  assert.equal(matchesTimezoneQuery(shanghai, '上海 utc+8'), true);
  assert.equal(matchesTimezoneQuery(shanghai, 'asia shanghai'), true);
  assert.equal(matchesTimezoneQuery(shanghai, '上海 utc+9'), false);
  assert.equal(matchesTimezoneQuery(newYork, 'new york'), true);
});
